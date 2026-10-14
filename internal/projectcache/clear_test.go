package projectcache

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTree 在项目缓存目录下造点内容,返回写入的总字节数。
func writeTree(t *testing.T, l *Layout, projectID string, files map[string]string) int64 {
	t.Helper()
	var total int64
	for name, content := range files {
		p := filepath.Join(l.ProjectDir(projectID), name)
		if err := os.MkdirAll(filepath.Dir(p), 0o777); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		total += int64(len(content))
	}
	return total
}

func TestInfoOfAndClear(t *testing.T) {
	l, err := New(filepath.Join(t.TempDir(), "pc"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	const pid = "proj-1"

	// 没构建过 → 目录不存在:Path 仍给出,Bytes=0。
	info := l.InfoOf(pid)
	if info.Exists {
		t.Fatalf("未构建过不该存在: %+v", info)
	}
	if info.Path != l.ProjectDir(pid) {
		t.Fatalf("Path = %q, want %q", info.Path, l.ProjectDir(pid))
	}
	// InfoOf 不该顺手建目录(否则「有没有缓存」被读操作改写了)。
	if _, err := os.Stat(info.Path); err == nil {
		t.Fatal("InfoOf 不该创建目录")
	}

	want := writeTree(t, l, pid, map[string]string{
		"maven/repository/cn/example/lib/1.0-SNAPSHOT/lib.jar":        "aaaaaaaa",
		"npm/_cacache/index-v5/ab/cd":                                 "bbbb",
		"maven/repository/cn/example/lib/1.0-SNAPSHOT/maven-metadata": "cccccc",
	})
	info = l.InfoOf(pid)
	if !info.Exists || info.Bytes != want {
		t.Fatalf("InfoOf = %+v, want Exists=true Bytes=%d", info, want)
	}

	// 清除:返回删除**前**的实况,目录真的没了。
	cleared, err := l.Clear(pid)
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if !cleared.Exists || cleared.Bytes != want || cleared.Path != l.ProjectDir(pid) {
		t.Fatalf("Clear 回报 = %+v, want Existed=true Bytes=%d", cleared, want)
	}
	if _, err := os.Stat(l.ProjectDir(pid)); !os.IsNotExist(err) {
		t.Fatalf("清除后目录仍在(err=%v)", err)
	}
	// 清干净后 InfoOf 回到「不存在」。
	if got := l.InfoOf(pid); got.Exists || got.Bytes != 0 {
		t.Fatalf("清除后 InfoOf = %+v", got)
	}

	// 再清一次 = 幂等成功,没东西可删。
	again, err := l.Clear(pid)
	if err != nil {
		t.Fatalf("重复 Clear: %v", err)
	}
	if again.Exists || again.Bytes != 0 {
		t.Fatalf("重复 Clear = %+v, want Existed=false Bytes=0", again)
	}
}

// TestClearThenPlanRecreates 锁住「清空不影响后续构建」:Plan 每次都会重新 ensure 出目录。
func TestClearThenPlanRecreates(t *testing.T) {
	l, err := New(filepath.Join(t.TempDir(), "pc"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	const pid = "proj-2"
	writeTree(t, l, pid, map[string]string{"go-mod/x": "yy"})
	if _, err := l.Clear(pid); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	plan := l.Plan(pid, nil)
	if len(plan.Mounts) == 0 {
		t.Fatal("清空后 Plan 应仍给出挂载目录")
	}
	for _, m := range plan.Mounts {
		st, err := os.Stat(m.HostPath)
		if err != nil {
			t.Fatalf("Plan 后宿主目录 %q 不在: %v", m.HostPath, err)
		}
		if !st.IsDir() {
			t.Fatalf("Plan 后 %q 不是目录", m.HostPath)
		}
	}
	// 重建出来的目录是空的:不会把「刚建空目录」当成「有缓存」。
	if got := l.InfoOf(pid); !got.Exists || got.Bytes != 0 {
		t.Fatalf("重建后 InfoOf = %+v, want Exists=true Bytes=0", got)
	}
}

// TestNilLayoutAndBlankID 零值/空 ID 不能 panic,也不能动根目录。
func TestNilLayoutAndBlankID(t *testing.T) {
	var nilLayout *Layout
	if got := nilLayout.InfoOf("p"); got.Path != "" || got.Exists {
		t.Fatalf("nil Layout InfoOf = %+v", got)
	}
	if got, err := nilLayout.Clear("p"); err != nil || got.Exists {
		t.Fatalf("nil Layout Clear = %+v, %v", got, err)
	}
	l, err := New(filepath.Join(t.TempDir(), "pc"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := l.InfoOf("  "); got.Path != "" {
		t.Fatalf("空 ID InfoOf = %+v", got)
	}
	if got, err := l.Clear(""); err != nil || got.Path != "" {
		t.Fatalf("空 ID Clear = %+v, %v", got, err)
	}
	// 空 ID 绝不该退化成「清根目录」。
	if _, err := os.Stat(l.Root()); err != nil {
		t.Fatalf("根目录被误删: %v", err)
	}
}
