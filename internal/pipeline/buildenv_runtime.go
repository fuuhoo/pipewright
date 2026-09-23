// buildenv_runtime.go 把「job 选了哪个预置构建环境」解析成执行期真正要用的东西(#9/#10)。
//
// 这是镜像来源的**唯一出口**:执行器不再读 `config["image"]`、也不再拼 `语言:版本`,
// 而是拿快照解析出的目录条目 —— 于是「镜像必须来自预置目录」这条约束在保存期(#8)与
// 运行期(#9)由同一段代码保证,不会两处规则漂移。
//
// 同时解析配置资源引用为容器挂载(#10):宿主文件只读挂到 targetPath。挂载路径在这里
// 统一做安全收口(绝对路径、无 `..`),因为 targetPath 是入库的管理员输入,执行侧不能假设它干净。
package pipeline

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ResolvedJobEnv 是一个 job 的运行期构建环境(镜像 + 拉取凭据 + 配置资源挂载)。
type ResolvedJobEnv struct {
	// Env 是命中的预置目录条目(便于日志展示 displayName/language)。
	Env BuildEnvOption
	// Image 是最终要跑的镜像(等于 Env.Image,而非 job 配置里的字符串)。
	Image string
	// CredentialID 是拉取该镜像的镜像仓库凭据引用(保险库);空=公开镜像,无需登录。
	CredentialID string
	// Mounts 是配置资源的只读挂载。
	Mounts []ContainerMount
}

// ResolveJobEnv 解析 job 的构建环境引用;不在预置目录内(或仍是未渲染占位)时返回
// 包装 ErrBuildEnvRequired 的错误,消息定位到阶段/节点,诚实要求重选而非偷偷跑。
//
// renderedImage 为自定义节点渲染 {{参数}} 后得到的镜像文本;没有渲染场景传空串。
func (s BuildEnvSnapshot) ResolveJobEnv(cfg map[string]any, renderedImage, stage, job string) (ResolvedJobEnv, error) {
	return s.resolve(JobBuildEnvRef(cfg), renderedImage, stage, job)
}

// ResolveToolchainEnv 解析「工具链构建(build_image 模型 B)」的语言+版本为目录镜像。
//
// 旧配置只有 toolchainLanguage/toolchainVersion(曾是 `语言:版本` 拼镜像),这里同样只当作
// **查目录的键**:命中才用目录里的 image 字段,不再由平台自己拼 tag —— 手填/拼串镜像就此消失。
func (s BuildEnvSnapshot) ResolveToolchainEnv(tc Toolchain, stage, job string) (ResolvedJobEnv, error) {
	return s.resolve(BuildEnvRef{LegacyToolchain: tc}, "", stage, job)
}

func (s BuildEnvSnapshot) resolve(ref BuildEnvRef, renderedImage, stage, job string) (ResolvedJobEnv, error) {
	env, p := s.resolveJobEnv(ref, renderedImage, stage, job)
	if p != nil {
		return ResolvedJobEnv{}, fmt.Errorf("%w: %s", ErrBuildEnvRequired, p.Message)
	}
	if env.ID == "" || env.Image == "" {
		// 引用里仍是 {{参数}} 且执行期也没渲染出实值 —— 无法确定镜像,不猜 latest。
		return ResolvedJobEnv{}, fmt.Errorf("%w: 阶段「%s」的节点「%s」的镜像参数未解析出实际值,请在预置目录内选择构建环境",
			ErrBuildEnvRequired, stage, job)
	}
	mounts, err := s.profileMounts(ref.ProfileIDs, stage, job)
	if err != nil {
		return ResolvedJobEnv{}, err
	}
	return ResolvedJobEnv{Env: env, Image: env.Image, CredentialID: env.CredentialID, Mounts: mounts}, nil
}

// profileMounts 把配置资源引用解析成只读挂载;缺文件 / 路径不安全 → 报错(而非静默不注入,
// 否则构建会以「缺 .npmrc / settings.xml」的迷惑方式失败)。
func (s BuildEnvSnapshot) profileMounts(ids []string, stage, job string) ([]ContainerMount, error) {
	var out []ContainerMount
	for _, id := range ids {
		p, ok := s.profiles[id]
		if !ok || !p.Enabled {
			return nil, fmt.Errorf("%w: 阶段「%s」的节点「%s」引用的配置资源已不存在或被禁用(%s),请重新勾选",
				ErrBuildEnvRequired, stage, job, id)
		}
		host := strings.TrimSpace(p.FilePath)
		if msg, bad := profileDefect(p); bad {
			return nil, fmt.Errorf("%w: 配置资源「%s」%s", ErrConfigProfileUnusable, id, msg)
		}
		// 宿主路径必须是绝对路径:docker 把 `-v` 里不以 / 开头的来源当**命名卷**,会静默挂成空目录。
		// DATA_DIR 允许配成相对路径(相对进程工作目录),故这里按同一基准补全。
		if abs, err := filepath.Abs(host); err == nil {
			host = abs
		}
		target, _ := safeMountPath(p.TargetPath)
		out = append(out, ContainerMount{HostPath: host, ContainerPath: target, ReadOnly: true})
	}
	return out, nil
}

// profileDefect 判一条配置资源是否可注入:必须有磁盘文件、容器内路径为绝对且不上跳。
// 保存期(#8)与执行期(#10)共用,坏数据在保存时就被挡,而不是等到构建中途失败。
func profileDefect(p ConfigProfileOption) (string, bool) {
	if strings.TrimSpace(p.FilePath) == "" {
		return "没有磁盘文件,无法注入", true
	}
	if _, ok := safeMountPath(p.TargetPath); !ok {
		return "的容器内路径非法(" + p.TargetPath + "),必须是绝对路径且不含 ..", true
	}
	return "", false
}

// safeMountPath 归一化容器内目标路径:必须绝对、_clean_ 后仍在根下(拒绝 `..` 上跳)。
func safeMountPath(raw string) (string, bool) {
	p := strings.TrimSpace(raw)
	if p == "" || !strings.HasPrefix(p, "/") {
		return "", false
	}
	var parts []string
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "", ".":
		case "..":
			return "", false
		default:
			parts = append(parts, seg)
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	return "/" + strings.Join(parts, "/"), true
}
