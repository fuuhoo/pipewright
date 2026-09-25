package store

import (
	"path/filepath"
	"testing"
)

// 迁移 harness 的外键处理:表重建(DROP + RENAME)必须在外键屏蔽下做,否则
// DROP 会顺子表的 ON DELETE CASCADE 真删数据;屏蔽只在迁移期间,终态再校验。

const harnessSchema = `
CREATE TABLE harness_parent (id TEXT PRIMARY KEY);
CREATE TABLE harness_child (
    id        TEXT NOT NULL PRIMARY KEY,
    parent_id TEXT NOT NULL REFERENCES harness_parent (id) ON DELETE CASCADE
);
INSERT INTO harness_parent (id) VALUES ('p1');
INSERT INTO harness_child (id, parent_id) VALUES ('c1', 'p1');
`

// rebuildParent 重建父表(新增一列),形状与 0061 一致:建表→拷贝→DROP→RENAME。
const rebuildParent = `
CREATE TABLE harness_parent_v2 (id TEXT PRIMARY KEY, extra TEXT NOT NULL DEFAULT '');
INSERT INTO harness_parent_v2 (id) SELECT id FROM harness_parent;
DROP TABLE harness_parent;
ALTER TABLE harness_parent_v2 RENAME TO harness_parent;
`

func openHarness(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.DB.Exec(harnessSchema); err != nil {
		t.Fatalf("harness schema: %v", err)
	}
	// migrate() 在业务表就绪前算基线,这里补建夹具后重算一次,与真实迁移环境一致。
	n, err := countFKViolations(s.DB)
	if err != nil {
		t.Fatalf("count fk violations: %v", err)
	}
	s.strictFKCheck = n == 0
	return s
}

func childCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow(`SELECT count(1) FROM harness_child`).Scan(&n); err != nil {
		t.Fatalf("count children: %v", err)
	}
	return n
}

func foreignKeysOn(t *testing.T, s *Store) bool {
	t.Helper()
	var n int
	if err := s.DB.QueryRow(`PRAGMA foreign_keys`).Scan(&n); err != nil {
		t.Fatalf("read pragma foreign_keys: %v", err)
	}
	return n == 1
}

func migrationApplied(t *testing.T, s *Store, version string) bool {
	t.Helper()
	var n int
	if err := s.DB.QueryRow(
		`SELECT count(1) FROM schema_migrations WHERE version = ?`, version,
	).Scan(&n); err != nil {
		t.Fatalf("check version: %v", err)
	}
	return n > 0
}

// TestApplyMigration_RebuildKeepsCascadeChildren 是本次重启事故的回归:外键开着时
// DROP TABLE 父表会级联清空子表,迁移必须先把外键关掉,并且结束后恢复。
func TestApplyMigration_RebuildKeepsCascadeChildren(t *testing.T) {
	s := openHarness(t)

	if err := s.applyMigration("9001_rebuild_parent", rebuildParent); err != nil {
		t.Fatalf("applyMigration: %v", err)
	}
	if got := childCount(t, s); got != 1 {
		t.Fatalf("children after rebuild = %d, want 1 (DROP 级联删了子表行)", got)
	}
	if !migrationApplied(t, s, "9001_rebuild_parent") {
		t.Fatal("version not recorded")
	}
	if !foreignKeysOn(t, s) {
		t.Fatal("PRAGMA foreign_keys not restored after migration")
	}
}

// TestApplyMigration_BrokenEndStateRollsBack 验证屏蔽期不是免检:终态有外键损坏时
// 迁移必须失败并整体回滚,版本也不记录。
func TestApplyMigration_BrokenEndStateRollsBack(t *testing.T) {
	s := openHarness(t)

	const orphan = `DELETE FROM harness_parent WHERE id = 'p1';`
	err := s.applyMigration("9002_orphan_child", orphan)
	if err == nil {
		t.Fatal("applyMigration = nil, want error for orphaned child row")
	}
	if migrationApplied(t, s, "9002_orphan_child") {
		t.Fatal("broken migration recorded its version")
	}
	if got := childCount(t, s); got != 1 {
		t.Fatalf("children = %d, want 1 (rollback should restore parent)", got)
	}
	if !foreignKeysOn(t, s) {
		t.Fatal("PRAGMA foreign_keys not restored after failed migration")
	}
}

// TestMigrate_DirtyDBSkipsStrictCheck 验证存量脏库(迁移前就有孤儿行)不会因为
// 别人的历史问题而起不来:此时跳过终态强校验。
func TestMigrate_DirtyDBSkipsStrictCheck(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "dirty.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, err := s.DB.Exec(harnessSchema); err != nil {
		t.Fatalf("harness schema: %v", err)
	}
	// 造一个孤儿行(屏蔽外键才能插,与手工 SQL 造脏数据等价)。
	if _, err := s.DB.Exec(
		`PRAGMA foreign_keys = OFF;
		 DELETE FROM harness_parent WHERE id = 'p1';
		 PRAGMA foreign_keys = ON;`,
	); err != nil {
		t.Fatalf("make orphan: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen dirty db: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if reopened.strictFKCheck {
		t.Fatal("strictFKCheck = true on a db with a pre-existing orphan")
	}
	if err := reopened.applyMigration("9003_anything", `SELECT 1;`); err != nil {
		t.Fatalf("applyMigration on dirty db: %v", err)
	}
}
