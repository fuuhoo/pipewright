package projectcache

import (
	"os"
	"path/filepath"
	"strings"
)

// ContainerSubdir 是容器形态下缓存根在 TMPDIR 里的子目录名。与构建工作区共用同一个「宿主必须
// 同路径映射」的目录(docker-compose.yml 的 PIPEWRIGHT_WORK_DIR/TMPDIR 约定),这样要往宿主映射
// 的路径只有一条,不用为缓存再开第二个约定。
const ContainerSubdir = "pipewright-project-cache"

// DefaultRoot 推断项目缓存根目录,并可能带回一句告警(告警为空 = 一切正常)。
//
// 优先级:显式 PIPEWRIGHT_PROJECT_CACHE_DIR > 容器形态的 TMPDIR 子目录 > DB 同级 project-cache。
//
// 为什么要分容器/进程两种形态:`docker run -v <宿主路径>:<容器路径>` 的源路径由**宿主 daemon**
// 解析,不是由平台自己看到的文件系统解析。
//   - 进程形态:平台就跑在宿主上,看到的绝对路径就是宿主的路径,DB 同级目录既持久又可见。
//   - 容器形态:DB 在 /data(具名卷),宿主上的真身是 /var/lib/docker/volumes/.../_data。拿
//     /data/project-cache 去 -v,daemon 会在宿主根下凭空建一个 /data:能写、看着像生效,但数据
//     不在卷里(down -v 删不掉、备份不到)。所以改用 TMPDIR —— 那个目录本就要求宿主同路径挂载。
func DefaultRoot(dbPath, explicit, tmpdir string, inContainer bool) (string, string) {
	if p := strings.TrimSpace(explicit); p != "" {
		return p, ""
	}
	if inContainer {
		base := strings.TrimSpace(tmpdir)
		if base == "" {
			base = "/tmp"
		}
		root := filepath.Join(base, ContainerSubdir)
		warn := ""
		if strings.TrimSpace(tmpdir) == "" {
			warn = "容器内未设 TMPDIR(通常由 PIPEWRIGHT_WORK_DIR 提供):项目缓存落在 " + root +
				",宿主上同名目录是宿主自己的临时目录,重启即失。要持久请把 PIPEWRIGHT_WORK_DIR 设成" +
				"一个宿主与容器同路径的目录,并把 docker-compose.yml 里 volumes 的 ②③ 两行放开。"
		}
		return root, warn
	}
	db := strings.TrimSpace(dbPath)
	if db == "" {
		db = "pipewright.db"
	}
	root := filepath.Join(filepath.Dir(db), "project-cache")
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return root, ""
}

// LikelyInContainer 粗略判定当前进程是否跑在容器里(供 DefaultRoot 选形态用)。
// /.dockerenv 命中即算;否则看 /proc/1/cgroup 里有没有 docker/podman/kubepods 字样。
// macOS 宿主没有 /proc,自然判为 false —— 进程形态正是我们要的默认。
func LikelyInContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	data, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		return false
	}
	s := string(data)
	for _, marker := range []string{"docker", "podman", "kubepods", "containerd"} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}
