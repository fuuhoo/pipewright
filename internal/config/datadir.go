// datadir.go —— 「数据目录在容器形态下宿主的容器 daemon 看得见吗」这条启动自检。
//
// 配置资源的磁盘副本是读磁盘直挂(见 internal/pipeline/buildenv_runtime.go 与
// internal/build/shell_driver.go 的 bindMountFlag),而 bind 的**源路径由宿主的 daemon 解析**,
// 不是由平台进程自己看到的文件系统解析。容器形态下这两套视图可以完全不同:平台 stat 得到
// 「文件在」恰恰可能等于「只有容器看得见」(典型:/data 是具名卷,宿主真身在 volumes/.../_data 下)。
// 那种情况挂载会被 daemon 硬拒,失败点在构建日志里,离配置动作隔着一次运行 —— 所以启动时就说明白。
//
// 判据取自 /proc/self/mountinfo:覆盖该目录的那条挂载,只有能从挂载点与源子路径推出「宿主上同一
// 路径存在」才放行(compose 的同路径 bind 正是这种形态,PIPEWRIGHT_WORK_DIR 的约定同理;源文件系统
// 自己挂在宿主某个前缀下时会多出一段前缀,见 hostSamePath)。任何信息不足
// (非容器形态 / 没挂 socket / mountinfo 读不到)一律返回空串,绝不做可能误伤健康部署的破坏性判断。
package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/fuuhoo/pipewright/internal/projectcache"
)

// socketPaths 是容器里常见的宿主 docker/podman socket 落点。挂了它才说明平台真会在容器内起构建
// 容器,这条自检才有意义;没挂时配置资源注入压根不参与,报出来只是噪音。
var socketPaths = []string{"/var/run/docker.sock", "/run/docker.sock"}

// DataDirVisibilityWarning 检查 dataDir 在「容器形态 + 挂了宿主容器 socket」下是否对宿主 daemon 可见。
// 返回一句可照做的告警;空串 = 没问题或不适用(进程形态、没挂 socket、信息不足)。
func DataDirVisibilityWarning(dataDir string) string {
	if !projectcache.LikelyInContainer() || !socketMounted() {
		return ""
	}
	abs := strings.TrimSpace(dataDir)
	if abs == "" {
		return ""
	}
	if !filepath.IsAbs(abs) {
		// 相对路径按进程工作目录解析(compose 镜像的 WORKDIR 通常就落在具名卷上)。
		if p, err := filepath.Abs(abs); err == nil {
			abs = p
		}
	}
	raw, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return ""
	}
	return dataDirVisibility(abs, string(raw))
}

// dataDirVisibility 是上面那条判据的纯函数部分(mountinfo 由调用方给),便于按真机抓到的文本测。
func dataDirVisibility(absDataDir, mountinfo string) string {
	cover, ok := coveringMount(absDataDir, mountinfo)
	if !ok || cover.hostSamePath() {
		return ""
	}
	return "容器形态下 PIPEWRIGHT_DATA_DIR=" + absDataDir + " 落在挂载点 " + cover.mountpoint +
		"(其宿主真身是 " + cover.root + "),宿主的容器 daemon 按这个路径看不到它 —— 构建节点里勾选的配置资源" +
		"会在 docker 报 \"bind source path does not exist\" 而失败。要修:把 PIPEWRIGHT_DATA_DIR 设成一个" +
		"宿主与容器**同名同路径**的目录(直接用它已映射的那条:PIPEWRIGHT_WORK_DIR 的子目录最省事)," +
		"并在 compose 的 volumes 里把它按同一路径挂进容器(见 docker-compose.yml 的 ②③ 段)。"
}

// socketMounted 报告容器内是否能见到宿主容器 socket。
func socketMounted() bool {
	for _, p := range socketPaths {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// mountCover 是 mountinfo 里一条挂载的关键三列。
type mountCover struct {
	root       string // 该挂载在源文件系统里的子路径
	mountpoint string // 容器内看到的路径
	fstype     string
}

// hostSamePath 报告「容器里这个路径在宿主上也存在且同路径」。
//   - 根挂载(overlay/tmpfs 等容器自己的文件系统)显然不是宿主路径;
//   - 具名卷与「宿主 A → 容器 B」的 bind 都推不出同路径,daemon 按 B 解析必然落空;
//   - 同路径 bind 是 root == mountpoint。
//
// root 不是宿主绝对路径,而是**源文件系统内部**的子路径 —— 宿主把那个文件系统自己挂在某个前缀下,
// 再从中间挑一段 bind 进容器。RHEL/CentOS 默认把 /home 单独划一个卷,同路径映射就会呈现
// 「mountpoint = 宿主前缀(/home) + root(/base/x/work)」这种形态:容器路径与宿主路径仍然一致,
// daemon 解析得到。逐段比对才算对齐,否则 /fooobar 会被当成 /foo + /bar 的同路径映射。
func (m mountCover) hostSamePath() bool {
	if m.mountpoint == "/" || m.mountpoint == "" || m.root == "/" || m.root == "" {
		return false
	}
	if m.fstype == "overlay" || m.fstype == "tmpfs" {
		return false
	}
	ms := strings.Split(strings.Trim(m.mountpoint, "/"), "/")
	rs := strings.Split(strings.Trim(m.root, "/"), "/")
	if len(rs) > len(ms) {
		return false
	}
	for i, seg := range rs {
		if ms[len(ms)-len(rs)+i] != seg {
			return false
		}
	}
	return true
}

// coveringMount 在 mountinfo 文本里找覆盖 path 的最长挂载点(即真正生效的那层挂载)。
func coveringMount(path, mountinfo string) (mountCover, bool) {
	var best mountCover
	for _, line := range strings.Split(mountinfo, "\n") {
		c, ok := parseMountinfoLine(line)
		if !ok {
			continue
		}
		if !pathUnder(path, c.mountpoint) {
			continue
		}
		if best.mountpoint == "" || len(c.mountpoint) > len(best.mountpoint) {
			best = c
		}
	}
	return best, best.mountpoint != ""
}

// parseMountinfoLine 解一行 mountinfo:
// `36 0 0:35 / /mnt rw,relatime - tmpfs tmpfs rw` → 定长前 6 列 + "-" 分隔 + 后 3 列(可选 tag 段忽略)。
func parseMountinfoLine(line string) (mountCover, bool) {
	fields := strings.Fields(line)
	if len(fields) < 10 {
		return mountCover{}, false
	}
	dash := -1
	for i := 6; i < len(fields); i++ {
		if fields[i] == "-" {
			dash = i
			break
		}
	}
	if dash < 0 || len(fields) < dash+2 {
		return mountCover{}, false
	}
	return mountCover{
		root:       unescapeMount(fields[3]),
		mountpoint: unescapeMount(fields[4]),
		fstype:     fields[dash+1],
	}, true
}

// unescapeMount 还原 mountinfo 里的八进制转义(空格等字符在路径中会出现)。
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	r := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return r.Replace(s)
}

// pathUnder 报告 path 是否落在 dir 之下(含相等);按路径段比较,避免 /data-x 被算进 /data。
func pathUnder(path, dir string) bool {
	if dir == "/" {
		return true
	}
	path = strings.TrimRight(path, "/")
	dir = strings.TrimRight(dir, "/")
	if path == dir {
		return true
	}
	return strings.HasPrefix(path, dir+"/")
}
