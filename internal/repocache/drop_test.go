package repocache

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writeMirrorFiles 在镜像目录里造点内容(等价于一次 mirror clone 之后磁盘上有东西)。
func writeMirrorFiles(t *testing.T, dir string, files map[string]string) int64 {
	t.Helper()
	var total int64
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		total += int64(len(content))
	}
	return total
}

func TestMirrorInfoOfAndDrop(t *testing.T) {
	c := newTestCache(t)
	const repoURL = "file:///srv/git/acme.git"

	// 从没拉过 → 不存在,但 Path 要说清楚(日志/前端要显示「去哪儿看」)。
	info := c.MirrorInfoOf(repoURL)
	if info.Exists || info.Bytes != 0 {
		t.Fatalf("未拉取过的镜像 = %+v", info)
	}
	if info.Path != c.mirrorPath(repoURL) {
		t.Fatalf("Path = %q, want %q", info.Path, c.mirrorPath(repoURL))
	}
	if _, err := os.Stat(info.Path); err == nil {
		t.Fatal("MirrorInfoOf 不该把镜像目录建出来")
	}

	want := writeMirrorFiles(t, info.Path, map[string]string{
		"HEAD":                "ref: refs/heads/main",
		"objects/pack/a.idx":  "bbbbbb",
		"objects/pack/a.pack": "cccccccc",
	})
	info = c.MirrorInfoOf(repoURL)
	if !info.Exists || info.Bytes != want {
		t.Fatalf("MirrorInfoOf = %+v, want Exists=true Bytes=%d", info, want)
	}

	// 删除:回报删除**前**的体积,目录真的没了。
	dropped, err := c.DropMirror(repoURL)
	if err != nil {
		t.Fatalf("DropMirror: %v", err)
	}
	if !dropped.Exists || dropped.Bytes != want || dropped.Path != info.Path {
		t.Fatalf("DropMirror = %+v, want Exists=true Bytes=%d", dropped, want)
	}
	if _, err := os.Stat(info.Path); !os.IsNotExist(err) {
		t.Fatalf("删除后镜像仍在(err=%v)", err)
	}

	// 再删一次 = 幂等成功,没东西可删。
	again, err := c.DropMirror(repoURL)
	if err != nil {
		t.Fatalf("重复 DropMirror: %v", err)
	}
	if again.Exists || again.Bytes != 0 {
		t.Fatalf("重复 DropMirror = %+v", again)
	}
}

// TestDropMirrorBusyWhileBuildHoldsLock 构建持锁时清除必须立刻回绝(不排队等构建),
// 且什么都没删 —— 半路删镜像会让正在跑的 checkout 直接失败。
func TestDropMirrorBusyWhileBuildHoldsLock(t *testing.T) {
	c := newTestCache(t)
	const repoURL = "file:///srv/git/busy.git"
	writeMirrorFiles(t, c.mirrorPath(repoURL), map[string]string{"HEAD": "ref: refs/heads/main"})

	lock := c.repoLock(repoURL)
	lock.Lock()

	info, err := c.DropMirror(repoURL)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	// 回绝时也要带回实况:前端据此显示「镜像还在,4.x MB」。
	if !info.Exists || info.Bytes == 0 || info.Path != c.mirrorPath(repoURL) {
		t.Fatalf("busy 时 info = %+v", info)
	}
	if _, err := os.Stat(c.mirrorPath(repoURL)); err != nil {
		t.Fatalf("busy 回绝时不该删掉镜像: %v", err)
	}

	// 构建结束(释放锁)后同一调用就能删掉。
	lock.Unlock()
	if _, err := c.DropMirror(repoURL); err != nil {
		t.Fatalf("锁释放后 DropMirror: %v", err)
	}
	if _, err := os.Stat(c.mirrorPath(repoURL)); !os.IsNotExist(err) {
		t.Fatal("锁释放后应删掉镜像")
	}
	// 上面 defer unlock 还会再解锁一次,这里改成显式释放后去掉 defer。
	lock.Lock()
	lock.Unlock()
}

func TestNilCacheDrop(t *testing.T) {
	var c *Cache
	if got := c.MirrorInfoOf("https://example.com/x.git"); got.Path != "" || got.Exists {
		t.Fatalf("nil Cache MirrorInfoOf = %+v", got)
	}
	got, err := c.DropMirror("https://example.com/x.git")
	if err != nil || got.Path != "" || got.Exists {
		t.Fatalf("nil Cache DropMirror = %+v, %v", got, err)
	}
}
