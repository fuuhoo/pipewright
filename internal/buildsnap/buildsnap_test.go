package buildsnap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mkdir 建目录(含父级)。
func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

// mkfile 建一个 size 字节的普通文件。
func mkfile(t *testing.T, path string, size int) {
	t.Helper()
	mkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(strings.Repeat("x", size)), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// fixture 搭一棵典型工作区:.git 噪声 + 源码/产物两层树 + 一个成员超标的目录。
func fixture(t *testing.T) string {
	t.Helper()
	ws := filepath.Join(t.TempDir(), "ws")
	mkdir(t, filepath.Join(ws, ".git", "objects"))
	mkfile(t, filepath.Join(ws, ".git", "HEAD"), 20)
	mkfile(t, filepath.Join(ws, "package.json"), 42)
	mkfile(t, filepath.Join(ws, "src", "index.ts"), 7)
	mkfile(t, filepath.Join(ws, "src", "deep", "nested", "leaf.txt"), 3)
	mkfile(t, filepath.Join(ws, "dist", "app.js"), 100)
	mkfile(t, filepath.Join(ws, "dist", "assets", "logo.svg"), 5)
	for i := 0; i < 10; i++ {
		mkfile(t, filepath.Join(ws, "noise", string(rune('a'+i))+".txt"), 1)
	}
	return ws
}

func paths(entries []Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Path)
	}
	return out
}

func find(entries []Entry, p string) (Entry, bool) {
	for _, e := range entries {
		if e.Path == p {
			return e, true
		}
	}
	return Entry{}, false
}

// TestCollectBasics 验证基础清单形态:浅层先出、目录带尾斜杠、文件带大小、.git 整支不产出。
func TestCollectBasics(t *testing.T) {
	ws := fixture(t)
	snap := Collect(ws, nil, Limits{})

	got := paths(snap.Entries)
	for _, want := range []string{"package.json", "src/", "dist/", "dist/app.js", "dist/assets/", "src/deep/"} {
		if _, ok := find(snap.Entries, want); !ok {
			t.Fatalf("清单缺 %q,实际=%v", want, got)
		}
	}
	for _, banned := range []string{".git", ".git/", ".git/HEAD"} {
		if _, ok := find(snap.Entries, banned); ok {
			t.Fatalf(".git 不该出现在清单里:%v", got)
		}
	}
	if fi, ok := find(snap.Entries, "package.json"); !ok || fi.Size != 42 {
		t.Fatalf("package.json 大小 = %d (want 42)", fi.Size)
	}
	// 浅层优先:根层条目必须排在深层 leaf.txt 之前。
	leaf := -1
	for i, p := range got {
		if p == "src/deep/" {
			leaf = i
			break
		}
	}
	if leaf < 0 {
		t.Fatalf("未列到 src/deep/(深度上限?):%v", got)
	}
	for _, top := range []string{"package.json", "src/", "dist/", "noise/"} {
		for i, p := range got {
			if p == top && i > leaf {
				t.Fatalf("浅层 %q 排在深层 src/deep/ 之后:BFS 失效:%v", top, got)
			}
		}
	}
	if snap.Truncated {
		t.Fatalf("默认上限不该截断这么小的树")
	}
	if snap.CreatedAt.IsZero() {
		t.Fatalf("CreatedAt 未填")
	}
}

// TestCollectDepthCap 验证深度上限:深度 D 只列到第 D 层,更深一层不再展开。
func TestCollectDepthCap(t *testing.T) {
	ws := fixture(t)
	snap := Collect(ws, nil, Limits{MaxDepth: 2})
	got := paths(snap.Entries)
	if _, ok := find(snap.Entries, "src/"); !ok {
		t.Fatalf("深度 2 应列出第 1 层 src/:%v", got)
	}
	if _, ok := find(snap.Entries, "src/deep/"); !ok {
		t.Fatalf("深度 2 应列出第 2 层 src/deep/:%v", got)
	}
	if _, ok := find(snap.Entries, "src/deep/nested/"); ok {
		t.Fatalf("深度 2 不该展开到第 3 层:%v", got)
	}
}

// TestCollectPerDirCap 验证单目录上限:超限的兄弟项不整支丢弃,而是在目录条目上记 Dropped。
func TestCollectPerDirCap(t *testing.T) {
	ws := fixture(t)
	snap := Collect(ws, nil, Limits{MaxPerDir: 4})
	e, ok := find(snap.Entries, "noise/")
	if !ok {
		t.Fatalf("noise/ 应被列出:%v", paths(snap.Entries))
	}
	if e.Dropped != 6 {
		t.Fatalf("noise/ Dropped = %d (want 6)", e.Dropped)
	}
	// 保留下来的仍是工作区内容,而非整支消失。
	var kept int
	for _, x := range snap.Entries {
		if strings.HasPrefix(x.Path, "noise/") && x.Path != "noise/" {
			kept++
		}
	}
	if kept != 4 {
		t.Fatalf("noise/ 下实际列出 %d 项 (want 4)", kept)
	}
	if snap.Truncated {
		t.Fatalf("单目录截断不该标整份截断")
	}
}

// TestCollectRootOverflowMarksTruncated 验证根目录被截时的兜底:根没有目录条目可挂 Dropped,
// 只能标整份截断 —— 否则"少了几项"会完全不可见。
func TestCollectRootOverflowMarksTruncated(t *testing.T) {
	ws := fixture(t)
	snap := Collect(ws, nil, Limits{MaxPerDir: 2})
	if !snap.Truncated {
		t.Fatalf("根目录超限应置 Truncated:%v", paths(snap.Entries))
	}
}

// TestCollectTotalCap 验证总条数上限:截断时置 Truncated,且不多留一条。
func TestCollectTotalCap(t *testing.T) {
	ws := fixture(t)
	snap := Collect(ws, nil, Limits{MaxEntries: 3})
	if len(snap.Entries) != 3 {
		t.Fatalf("条目数 = %d (want 3):%v", len(snap.Entries), paths(snap.Entries))
	}
	if !snap.Truncated {
		t.Fatalf("命中总条数上限应置 Truncated")
	}
}

// TestCollectMissingAncestor 验证「路径写错」的直接证据:回溯到最近存在的祖先目录并列出实际条目。
func TestCollectMissingAncestor(t *testing.T) {
	ws := fixture(t)
	declared := []Declared{
		{Path: "dist/out/app.js", Status: StatusMissing},
		{Path: "build/*.zip", Status: StatusUnmatched},
		{Path: "package.json", Status: StatusOK},
		{Path: "/etc/shadow", Status: StatusRejected},
	}
	snap := Collect(ws, declared, Limits{})

	byPath := map[string]Declared{}
	for _, d := range snap.Declared {
		byPath[d.Path] = d
	}
	if d := byPath["dist/out/app.js"]; d.Parent != "dist" || strings.Join(d.Siblings, ",") != "app.js,assets/" {
		t.Fatalf("dist/out/app.js 祖先回溯 = %q %v (want dist [app.js assets/])", d.Parent, d.Siblings)
	}
	if d := byPath["build/*.zip"]; d.Parent != "." || len(d.Siblings) == 0 {
		t.Fatalf("build/*.zip 应回溯到工作区根并列出身项:%q %v", d.Parent, d.Siblings)
	}
	if d := byPath["package.json"]; d.Parent != "" || d.Siblings != nil {
		t.Fatalf("命中的路径不该补祖先信息:%q %v", d.Parent, d.Siblings)
	}
	if d := byPath["/etc/shadow"]; d.Parent != "" {
		t.Fatalf("越界路径不该去_stat_外部:%q", d.Parent)
	}
}

// TestHasMiss 验证"是否需要采集"的判定:ok 之外都算未命中,空清单不算。
func TestHasMiss(t *testing.T) {
	if HasMiss(nil) || HasMiss([]Declared{{Status: StatusOK}}) {
		t.Fatalf("全命中/空清单不该算 miss")
	}
	for _, st := range []string{StatusMissing, StatusUnmatched, StatusRejected} {
		if !HasMiss([]Declared{{Path: "x", Status: st}}) {
			t.Fatalf("%s 应算 miss", st)
		}
	}
}

// TestCollectAbsentWorkspace 验证工作区已消失时采集不炸(返回空清单而非 panic/error)。
func TestCollectAbsentWorkspace(t *testing.T) {
	snap := Collect(filepath.Join(t.TempDir(), "gone"), []Declared{{Path: "dist/x", Status: StatusMissing}}, Limits{})
	if len(snap.Entries) != 0 || snap.Truncated {
		t.Fatalf("不存在的工作区应得空清单:entries=%v truncated=%v", paths(snap.Entries), snap.Truncated)
	}
	if d := snap.Declared[0]; d.Parent != "." {
		t.Fatalf("祖先回溯兜底应为 \".\",得到 %q", d.Parent)
	}
}
