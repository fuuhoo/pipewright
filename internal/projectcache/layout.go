// Package projectcache 是「每项目固定依赖缓存目录」(全局缓存,流水线里零配置)。
//
// 背景:平台的构建都在 `docker run --rm` 的隔离容器里跑,工作区又是即用即销的临时目录,
// 所以 Maven/npm/Go/pip 下载下来的依赖每次运行都随容器一起消失,下一次照样从头拉一遍。
// 已有的 buildcache(任务里配 cachePaths)治不了这件事:它按 key 打包 tar.gz 搬运**工作区内**
// 的目录,而工具的默认落点(`~/.m2/repository` 等)在容器 HOME 里,不在工作区。
//
// 本包的模型不同:**宿主上一个固定目录,永久 bind 进容器**,按项目 ID 分格:
//
//	<root>/<项目ID>/
//	  ├─ maven/  npm/  go-mod/  cache/  gradle/  cargo-registry/
//	  └─(整个项目目录同时挂到 /pw-cache,供 env 指向)
//
// 两套视图指向同一份宿主数据:
//   - 默认路径套:直接挂到工具在 root 容器里的默认落点(`/root/.m2/repository` 等),命令与
//     env 都不用动就能吃到缓存;
//   - 中立路径套:整个项目目录挂到 `/pw-cache`,并由平台注入 `MAVEN_ARGS/npm_config_cache/
//     GOMODCACHE/GOCACHE/PIP_CACHE_DIR/GRADLE_USER_HOME` 让工具改道到这里 —— 非 root 镜像
//     的 HOME 不是 /root,只有这一套能命中。
//     cargo 只走默认路径套:`CARGO_HOME` 若指到 /pw-cache/cargo-registry 就会把 registry 再
//     套一层,两视图对不上,而指到 /root/.cargo 会盖掉镜像里的 cargo 可执行文件,故不设 env。
//
// 只覆盖 root 的 `/root/.m2/repository` 而**不是** `/root/.m2`:后者会和「配置资源」注入的
// `/root/.m2/settings.xml` 形成父子嵌套挂载,谁后挂谁遮蔽前者,docker 对顺序无保证。
//
// 目录一律建 0777(os.MkdirAll 的权限位要过 umask,故建完再显式 Chmod):构建容器里的 uid
// 未必是宿主跑平台的那个 uid,写不进去的缓存等于没有。
//
// 边界:本包只管目录与挂载/env 清单,不碰远程执行 —— 按产品决定,远程 runner 上不留任何常驻
// 目录(见 internal/build/remote_stage_exec.go),要快就把依赖烘进镜像。
package projectcache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ContainerBase 是项目缓存目录在容器里的中立挂载点(env 套用它)。
const ContainerBase = "/pw-cache"

// slot 是一个缓存子目录:Dir 是宿主目录名(同时也是 /pw-cache 下的子目录名),
// ContainerPath 是它在 root 容器里的工具默认落点。
type slot struct {
	Dir           string
	ContainerPath string
}

// slots 是默认路径套的挂载表。顺序即挂载顺序;各项之间无嵌套。
var slots = []slot{
	{"maven", "/root/.m2/repository"},
	{"npm", "/root/.npm"},
	{"go-mod", "/root/go/pkg/mod"},
	{"cache", "/root/.cache"},
	{"gradle", "/root/.gradle"},
	{"cargo-registry", "/root/.cargo/registry"},
}

// envTemplate 是中立路径套要注入的环境变量(%s 填 ContainerBase)。
var envTemplate = []string{
	"MAVEN_ARGS=-Dmaven.repo.local=%s/maven",
	"npm_config_cache=%s/npm",
	"GOMODCACHE=%s/go-mod",
	"GOCACHE=%s/cache/go-build",
	"PIP_CACHE_DIR=%s/cache/pip",
	"GRADLE_USER_HOME=%s/gradle",
}

// Mount 是一条「宿主目录 → 容器路径」的可写挂载。字段与 pipeline.ContainerMount 对齐但本包
// 不 import pipeline(避免与 build/pipeline 的依赖关系纠缠),由调用方转换。
type Mount struct {
	HostPath      string
	ContainerPath string
	ReadOnly      bool
}

// Plan 是某项目一次执行要追加的挂载与 env。Skipped 记录被冲突规则让路的默认路径挂载
// (容器目标已被别的挂载占用),供日志说明「这一档缓存本次没生效」。
type Plan struct {
	Mounts  []Mount
	Env     []string
	Skipped []string
}

// Layout 是缓存根。零值不可用,请经 New。
type Layout struct{ root string }

// New 建根目录(不存在则创建,0777)。root 为空视为未配置。
func New(root string) (*Layout, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("projectcache: empty root")
	}
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("projectcache: root must be absolute: %q", root)
	}
	if err := os.MkdirAll(root, 0o777); err != nil {
		return nil, fmt.Errorf("projectcache: mkdir root: %w", err)
	}
	// MkdirAll 的 mode 过 umask,补一次显式 chmod(嵌套目录同样只 chmod 末级)。
	_ = os.Chmod(root, 0o777)
	return &Layout{root: root}, nil
}

// Root 返回缓存根(日志/自检用)。
func (l *Layout) Root() string {
	if l == nil {
		return ""
	}
	return l.root
}

// ProjectDir 返回某项目的固定缓存目录(已把项目 ID 折成安全目录名)。
func (l *Layout) ProjectDir(projectID string) string {
	return filepath.Join(l.root, SafeDir(projectID))
}

// Plan 备齐该项目的缓存目录并给出挂载 + env。
//
// taken 是本次执行**已经**要挂进容器的目标路径(配置资源注入那些)。与某个默认路径挂载形成
// 父子或同名关系时,让路给配置资源:配置文件被缓存目录盖住是功能损坏,缓存没生效只是慢一点。
// 中立路径 `/pw-cache` 同理避让。
func (l *Layout) Plan(projectID string, taken []string) Plan {
	if l == nil || strings.TrimSpace(projectID) == "" {
		return Plan{}
	}
	dir := l.ProjectDir(projectID)
	var plan Plan
	// 中立套:整个项目目录 → /pw-cache(env 全靠它,占用了就没法换道,直接跳过 env)。
	if conflicts(ContainerBase, taken) {
		plan.Skipped = append(plan.Skipped, ContainerBase)
	} else {
		if err := l.ensure(dir); err != nil {
			plan.Skipped = append(plan.Skipped, dir)
		} else {
			plan.Mounts = append(plan.Mounts, Mount{HostPath: dir, ContainerPath: ContainerBase})
			for _, t := range envTemplate {
				plan.Env = append(plan.Env, fmt.Sprintf(t, ContainerBase))
			}
		}
	}
	for _, s := range slots {
		host := filepath.Join(dir, s.Dir)
		if conflicts(s.ContainerPath, taken) {
			plan.Skipped = append(plan.Skipped, s.ContainerPath)
			continue
		}
		if err := l.ensure(host); err != nil {
			plan.Skipped = append(plan.Skipped, s.ContainerPath)
			continue
		}
		plan.Mounts = append(plan.Mounts, Mount{HostPath: host, ContainerPath: s.ContainerPath})
	}
	return plan
}

// ensure 建一个缓存目录并强制 0777(见包注释:构建容器里的 uid 未必能写默认 umask 出来的目录)。
func (l *Layout) ensure(dir string) error {
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return err
	}
	_ = os.Chmod(dir, 0o777)
	return nil
}

// conflicts 报告 path 是否与 taken 里任一路径同名、或是其祖先/后代(嵌套 bind 会互相遮蔽)。
func conflicts(path string, taken []string) bool {
	for _, t := range taken {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if path == t || strings.HasPrefix(t, path+"/") || strings.HasPrefix(path, t+"/") {
			return true
		}
	}
	return false
}

// SafeDir 把项目 ID 折成安全目录名:只留 [A-Za-z0-9._-],其余换成 '-';空串或折完仍是
// '.'/'..'(可穿越)时改用 ID 的 sha256 前缀。同一 ID 恒得同名,项目改名不影响缓存落点
// (落点认 ID,不认名字)。
func SafeDir(id string) string {
	var sb strings.Builder
	for _, r := range strings.TrimSpace(id) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			sb.WriteRune(r)
		default:
			sb.WriteRune('-')
		}
	}
	s := strings.Trim(sb.String(), ".-")
	if s == "" || s == "." || s == ".." {
		sum := sha256.Sum256([]byte(strings.TrimSpace(id)))
		return hex.EncodeToString(sum[:])[:16]
	}
	if len(s) > 64 {
		return s[:64]
	}
	return s
}
