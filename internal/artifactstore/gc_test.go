package artifactstore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDeleteByHandle(t *testing.T) {
	s := newStore(t)
	key, _, err := s.Put(strings.NewReader("to be deleted"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Delete(key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if s.Has(key) {
		t.Fatal("Delete 后 Has 应 false")
	}
	// 再删一次:幂等,不报 ErrNotFound(GC 与并发删除都指望这点)。
	if err := s.Delete(key); err != nil {
		t.Fatalf("重复 Delete 应幂等,得到 %v", err)
	}
	if err := s.Delete("../etc/passwd"); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("非法句柄应 ErrInvalidKey,得到 %v", err)
	}
}

// putAt 存一份内容并把 mtime 钉到 want(造「老产物」)。
func putAt(t *testing.T, s *Store, content string, want time.Time) string {
	t.Helper()
	key, _, err := s.Put(bytes.NewReader([]byte(content)))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	p := s.pathFor(key)
	if err := os.Chtimes(p, want, want); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	return key
}

func TestSweepDeletesOnlyUnreferencedOldObjects(t *testing.T) {
	s := newStore(t)
	now := time.Now().UTC()
	old := now.Add(-3 * time.Hour)

	keptLive := putAt(t, s, "referenced bytes", old)                  // 老但有人认 → 留
	dropped := putAt(t, s, "orphan bytes aaa", old)                   // 老且没人认 → 删
	keptYoung := putAt(t, s, "still in grace", now.Add(-time.Minute)) // 没人认但未过宽限期 → 留

	res, err := s.Sweep(map[string]struct{}{keptLive: {}}, now, time.Hour)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Deleted != 1 || res.Freed != int64(len("orphan bytes aaa")) {
		t.Fatalf("Deleted=%d Freed=%d, want 1 / %d", res.Deleted, res.Freed, len("orphan bytes aaa"))
	}
	if res.Kept != 1 {
		t.Fatalf("Kept = %d, want 1", res.Kept)
	}
	if res.Young != 1 {
		t.Fatalf("Young = %d, want 1", res.Young)
	}
	if s.Has(dropped) {
		t.Fatal("孤儿字节应已删除")
	}
	if !s.Has(keptLive) || !s.Has(keptYoung) {
		t.Fatal("有人认的 / 宽限期内的字节被误删")
	}
}

// TestSweepIgnoresForeignEntries 验证回收范围只有本库写的东西:
// 手工放的目录 / 非 sha256 文件名的文件 / 根下未过宽限期的临时文件都不动。
func TestSweepIgnoresForeignEntries(t *testing.T) {
	s := newStore(t)
	now := time.Now().UTC()
	old := now.Add(-5 * time.Hour)

	foreignDir := filepath.Join(s.root, "manual")
	if err := os.MkdirAll(foreignDir, 0o700); err != nil {
		t.Fatal(err)
	}
	strays := []string{
		filepath.Join(s.root, "notes.txt"),
		filepath.Join(foreignDir, "whatever"),
	}
	for _, p := range strays {
		if err := os.WriteFile(p, []byte("keep me"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	// 分片目录里放一个非 hex 名(长度也不对)的文件:不是产物句柄,不该删。
	shard := filepath.Join(s.root, "zz")
	if err := os.MkdirAll(shard, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shard, "not-a-hash"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 两个临时文件:一个过期、一个没过期。
	for name, when := range map[string]time.Time{".put-old": old, ".put-new": now} {
		p := filepath.Join(s.root, name)
		if err := os.WriteFile(p, []byte("half written"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatal(err)
		}
	}

	res, err := s.Sweep(nil, now, time.Hour)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Deleted != 0 {
		t.Fatalf("不该删任何产物文件,Deleted=%d", res.Deleted)
	}
	if res.Temps != 1 {
		t.Fatalf("Temps = %d, want 1(只清过期的那个)", res.Temps)
	}
	for _, p := range append(strays, filepath.Join(shard, "not-a-hash")) {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("外部文件 %s 被动了:%v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(s.root, ".put-old")); !os.IsNotExist(err) {
		t.Fatal("过期临时文件应已清掉")
	}
	if _, err := os.Stat(filepath.Join(s.root, ".put-new")); err != nil {
		t.Fatal("宽限期内的临时文件不该动")
	}
}

// TestSweepEmptyLiveSetDropsEverythingOld 对应「项目全删光」后的正常状态:
// 活引用集合为空时,超宽限期的字节全算孤儿。
func TestSweepEmptyLiveSetDropsEverythingOld(t *testing.T) {
	s := newStore(t)
	now := time.Now().UTC()
	k1 := putAt(t, s, "one", now.Add(-2*time.Hour))
	k2 := putAt(t, s, "two", now.Add(-2*time.Hour))

	res, err := s.Sweep(map[string]struct{}{}, now, time.Hour)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Deleted != 2 {
		t.Fatalf("Deleted = %d, want 2", res.Deleted)
	}
	if s.Has(k1) || s.Has(k2) {
		t.Fatal("两个孤儿都应被删掉")
	}
}

// TestPutDedupBumpsMtime 是回收安全的前提:去重命中时必须把 mtime 推到此刻,
// 否则「库里刚要写行、盘上却是几个月前的旧字节」会被下一轮当成孤儿删掉。
func TestPutDedupBumpsMtime(t *testing.T) {
	s := newStore(t)
	key := putAt(t, s, "same content", time.Now().UTC().Add(-30*24*time.Hour))

	mtimeBefore := statModTime(t, s.pathFor(key))
	got, _, err := s.Put(strings.NewReader("same content"))
	if err != nil {
		t.Fatalf("去重 Put: %v", err)
	}
	if got != key {
		t.Fatalf("相同内容应同句柄:%q vs %q", got, key)
	}
	mtimeAfter := statModTime(t, s.pathFor(key))
	if !mtimeAfter.After(mtimeBefore) {
		t.Fatalf("去重命中后 mtime 应被推后:before=%s after=%s", mtimeBefore, mtimeAfter)
	}
}

func statModTime(t *testing.T, path string) time.Time {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat %s: %v", path, err)
	}
	return fi.ModTime()
}
