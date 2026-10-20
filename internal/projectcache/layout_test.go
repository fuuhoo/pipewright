package projectcache

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewRejectsRelativeAndEmpty(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("空根应报错(未配置)")
	}
	if _, err := New("relative/dir"); err == nil {
		t.Fatal("相对路径应报错:docker -v 的源必须是宿主绝对路径")
	}
	root := filepath.Join(t.TempDir(), "cache")
	l, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if l.Root() != root {
		t.Fatalf("Root() = %q, want %q", l.Root(), root)
	}
	// 根目录建出来且 0777(容器里的 uid 未必是宿主跑平台的 uid,要能写)。
	fi, err := os.Stat(root)
	if err != nil {
		t.Fatalf("stat root: %v", err)
	}
	if fi.Mode().Perm() != 0o777 {
		t.Fatalf("root perm = %v, want 777", fi.Mode().Perm())
	}
}

func TestPlanMountsAndEnv(t *testing.T) {
	l, err := New(filepath.Join(t.TempDir(), "pc"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	plan := l.Plan("p1", nil)

	dir := l.ProjectDir("p1")
	if dir != filepath.Join(l.Root(), "p1") {
		t.Fatalf("ProjectDir = %q", dir)
	}

	// 中立套:整个项目目录 → /pw-cache。
	if len(plan.Mounts) == 0 || plan.Mounts[0].ContainerPath != ContainerBase {
		t.Fatalf("首条挂载应为中立套 %s, got %+v", ContainerBase, plan.Mounts)
	}
	if plan.Mounts[0].HostPath != dir || plan.Mounts[0].ReadOnly {
		t.Fatalf("中立套挂载错: %+v", plan.Mounts[0])
	}

	want := map[string]string{
		"maven":          "/root/.m2/repository",
		"npm":            "/root/.npm",
		"go-mod":         "/root/go/pkg/mod",
		"cache":          "/root/.cache",
		"gradle":         "/root/.gradle",
		"cargo-registry": "/root/.cargo/registry",
	}
	got := map[string]string{}
	for _, m := range plan.Mounts {
		got[m.ContainerPath] = m.HostPath
	}
	for dirName, containerPath := range want {
		host, ok := got[containerPath]
		if !ok {
			t.Fatalf("缺默认路径挂载 %s(%s)", containerPath, dirName)
		}
		if host != filepath.Join(dir, dirName) {
			t.Fatalf("%s 宿主目录 = %q, want %q", containerPath, host, filepath.Join(dir, dirName))
		}
		if _, err := os.Stat(host); err != nil {
			t.Fatalf("挂载的宿主目录应已建好: %v", err)
		}
	}
	// 只挂 repository 而不挂 /root/.m2:后者会与配置资源的 settings.xml 形成嵌套挂载。
	if _, ok := got["/root/.m2"]; ok {
		t.Fatal("不该挂 /root/.m2(会遮蔽配置资源注入的 settings.xml)")
	}
	// pnpm 不设挂载档:pnpm 12 根本不往 /root/.local/share 写(见 layout.go 包注释)。
	if _, ok := got["/root/.local/share/pnpm/store"]; ok {
		t.Fatal("不该挂 pnpm store 档:实测 pnpm 12 会静默改用 node_modules/.pnpm-store")
	}

	joined := strings.Join(plan.Env, "\n")
	for _, kv := range []string{
		"MAVEN_ARGS=-Dmaven.repo.local=/pw-cache/maven",
		"npm_config_cache=/pw-cache/npm",
		"GOMODCACHE=/pw-cache/go-mod",
		"GOCACHE=/pw-cache/cache/go-build",
		"PIP_CACHE_DIR=/pw-cache/cache/pip",
		"GRADLE_USER_HOME=/pw-cache/gradle",
	} {
		if !strings.Contains(joined, kv) {
			t.Fatalf("env 缺 %q, got:\n%s", kv, joined)
		}
	}
	if strings.Contains(joined, "CARGO_HOME") {
		t.Fatal("不该注入 CARGO_HOME(指到缓存目录会把 registry 多套一层)")
	}
	if strings.Contains(joined, "XDG_DATA_HOME") {
		t.Fatal("不该注入 XDG_DATA_HOME(会连带搬走其他工具的数据目录)")
	}
	if len(plan.Skipped) != 0 {
		t.Fatalf("无冲突时不该有 Skipped: %v", plan.Skipped)
	}
}

func TestPlanYieldsToConfigResourceMounts(t *testing.T) {
	l, err := New(filepath.Join(t.TempDir(), "pc"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// 配置资源把整个 /root/.m2 挂进容器(maven 缓存目录的**父**),docker 挂载顺序无保证,
	// 后挂的会遮蔽前者 → maven 档让路;/root/.cache/pip 是 cache 档的**子**→ cache 档也让路。
	plan := l.Plan("p2", []string{"/root/.m2", "/root/.cache/pip"})

	got := map[string]bool{}
	for _, m := range plan.Mounts {
		got[m.ContainerPath] = true
	}
	if got["/root/.m2/repository"] {
		t.Fatal("与配置资源挂载形成父子嵌套时必须让路,否则会遮蔽挂进来的 settings.xml")
	}
	if got["/root/.cache"] {
		t.Fatal("cache 档是 /root/.cache/pip 的父路径,同样会遮蔽挂进来的文件,必须让路")
	}
	// 兄弟路径不冲突(repository 与 settings.xml 同为 /root/.m2 的子项)。
	if !got["/root/.npm"] || !got["/root/go/pkg/mod"] {
		t.Fatalf("其余挂载不该受影响: %+v", plan.Mounts)
	}
	if !strings.Contains(strings.Join(plan.Skipped, ","), "/root/.m2/repository") {
		t.Fatalf("让路项要记进 Skipped 供日志说明: %v", plan.Skipped)
	}

	// 兄弟路径不冲突:挂 settings.xml 这个文件不影响 repository 档(两者同为 /root/.m2 的子项)。
	siblings := l.Plan("p2b", []string{"/root/.m2/settings.xml"})
	var hasRepo bool
	for _, m := range siblings.Mounts {
		if m.ContainerPath == "/root/.m2/repository" {
			hasRepo = true
		}
	}
	if !hasRepo {
		t.Fatalf("兄弟挂载不该让 maven 档让路: %+v", siblings.Mounts)
	}

	// /pw-cache 被占用(含被挂到它的子路径)→ 中立套与整套 env 一起跳过。
	plan = l.Plan("p3", []string{ContainerBase + "/maven"})
	for _, m := range plan.Mounts {
		if m.ContainerPath == ContainerBase {
			t.Fatal("/pw-cache 与其子路径嵌套时中立套要让路")
		}
	}
	if len(plan.Env) != 0 {
		t.Fatalf("/pw-cache 没挂上就不该注入指向它的 env: %v", plan.Env)
	}
	if !strings.Contains(strings.Join(plan.Skipped, ","), ContainerBase) {
		t.Fatalf("中立套让路也要记进 Skipped: %v", plan.Skipped)
	}
}

func TestPlanEmptyProjectIDIsNoop(t *testing.T) {
	l, err := New(filepath.Join(t.TempDir(), "pc"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p := l.Plan("   ", nil); len(p.Mounts) != 0 || len(p.Env) != 0 {
		t.Fatalf("无项目 ID 应零操作: %+v", p)
	}
	var nilLayout *Layout
	if p := nilLayout.Plan("p1", nil); len(p.Mounts) != 0 {
		t.Fatal("nil 布局应零操作(不应 panic)")
	}
	if nilLayout.Root() != "" {
		t.Fatal("nil 布局 Root() 应为空")
	}
}

func TestSafeDir(t *testing.T) {
	cases := []struct{ id, want string }{
		{"p_0001", "p_0001"},
		{"proj.name-2", "proj.name-2"},
		{"a/b", "a-b"},
		{"..", digestDir("..")},
		{"", digestDir("")},
	}
	for _, c := range cases {
		if got := SafeDir(c.id); got != c.want {
			t.Fatalf("SafeDir(%q) = %q, want %q", c.id, got, c.want)
		}
	}
	// 穿越片段:折完不含分隔符与 '..'(目录名永远落在根下面)。
	for _, id := range []string{"../../etc/passwd", "项目/名字", "a\\b", "id with space"} {
		got := SafeDir(id)
		if strings.ContainsAny(got, `/\`) || got == "." || got == ".." {
			t.Fatalf("SafeDir(%q) = %q,仍可穿越", id, got)
		}
	}
	// 长 ID 截断到 64。
	if len(SafeDir(strings.Repeat("x", 100))) != 64 {
		t.Fatal("长 ID 应截断到 64")
	}
	// 稳定性:落点认 ID,同一 ID 恒得同名。
	if SafeDir("abc123") != SafeDir("abc123") {
		t.Fatal("SafeDir 需稳定")
	}
}

func digestDir(id string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(id)))
	return hex.EncodeToString(sum[:])[:16]
}

func TestDefaultRootProcessForm(t *testing.T) {
	dir, warn := DefaultRoot(filepath.Join("/var/lib/pw", "pipewright.db"), "", "", false)
	if dir != "/var/lib/pw/project-cache" {
		t.Fatalf("进程形态默认落 DB 同级: %q", dir)
	}
	if warn != "" {
		t.Fatalf("进程形态不该告警: %q", warn)
	}
}

func TestDefaultRootContainerForm(t *testing.T) {
	dir, warn := DefaultRoot("/data/pipewright.db", "", "/srv/pw-work", true)
	if dir != filepath.Join("/srv/pw-work", ContainerSubdir) {
		t.Fatalf("容器形态应落在 TMPDIR 子目录(该目录要求宿主同路径): %q", dir)
	}
	if warn != "" {
		t.Fatalf("已设 TMPDIR 不该告警: %q", warn)
	}

	// 容器形态但没设 TMPDIR:退回 /tmp 并给出可操作的告警(否则缓存看着生效,实则重启即失)。
	dir, warn = DefaultRoot("/data/pipewright.db", "", "", true)
	if dir != filepath.Join("/tmp", ContainerSubdir) {
		t.Fatalf("缺 TMPDIR 应落 /tmp: %q", dir)
	}
	if !strings.Contains(warn, "PIPEWRIGHT_WORK_DIR") || !strings.Contains(warn, "TMPDIR") {
		t.Fatalf("告警要指名要设的 env: %q", warn)
	}

	// 显式指定优先,且不带告警(路径是否宿主同路径由用户负责)。
	dir, warn = DefaultRoot("/data/pipewright.db", "/mnt/nvme/pw-cache", "", true)
	if dir != "/mnt/nvme/pw-cache" || warn != "" {
		t.Fatalf("显式目录应最高优先: %q %q", dir, warn)
	}
}
