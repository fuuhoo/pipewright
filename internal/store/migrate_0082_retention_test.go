package store_test

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	_ "modernc.org/sqlite" // 与 internal/store 同一个纯 Go 驱动
)

// TestMigration0082RetentionGlobalKeep 钉住 0082 的两条承诺,因为它们直接决定「升级会不会删数据」:
//
//  1. keep_global 列补上且默认 1000 —— 构建目录快照改成随运行记录保留后,全局条数是唯一的生长边界。
//  2. 默认打开(enabled=1、max_age_days=365)**只作用于从未被配置过的行**。
//     任何人手工调过的策略(哪怕只是设过每项目条数)都不许被升级翻开关 —— 否则一次 `up -d`
//     就是一场无人授权的数据清理。
//
// 这里不跑全套迁移,而是拿 0042 + 0082 两个真实迁移文件在临时库上按序执行:
// 要验的就是这两份文本的语义,绕开其余 80 个迁移能让「已配置行」这一前置状态干净地摆出来。
func TestMigration0082RetentionGlobalKeep(t *testing.T) {
	db, _ := openBareSQLite(t)

	// 先建表并播种(0042 原文),此时状态等同一个全新实例。
	applyMigrationFile(t, db, "migrations/sqlite/0042_retention_config.sql")

	t.Run("默认行被打开", func(t *testing.T) {
		applyMigrationFile(t, db, "migrations/sqlite/0082_retention_global_keep.sql")
		assertRetentionRow(t, db, 1, 1000, 0, 365)
	})

	// 0082 是幂等迁移?不是(ADD COLUMN 不可重复)。所以第二个场景换个库跑。
	db2, _ := openBareSQLite(t)
	applyMigrationFile(t, db2, "migrations/sqlite/0042_retention_config.sql")
	if _, err := db2.Exec(
		`UPDATE retention_config SET enabled = 0, keep_per_project = 50, max_age_days = 0 WHERE id = 1`,
	); err != nil {
		t.Fatalf("模拟已配置过的实例: %v", err)
	}
	applyMigrationFile(t, db2, "migrations/sqlite/0082_retention_global_keep.sql")

	t.Run("配置过的行不被翻开关", func(t *testing.T) {
		// enabled 保持 0(用户显式关着),keep_per_project 保持 50(旧维度继续生效),
		// 仅补上新列的默认条数;365 天那条不许覆盖回去。
		assertRetentionRow(t, db2, 0, 1000, 50, 0)
	})

	t.Run("只改过天数的行也不被翻", func(t *testing.T) {
		db3, _ := openBareSQLite(t)
		applyMigrationFile(t, db3, "migrations/sqlite/0042_retention_config.sql")
		if _, err := db3.Exec(
			`UPDATE retention_config SET enabled = 1, keep_per_project = 0, max_age_days = 7 WHERE id = 1`,
		); err != nil {
			t.Fatalf("模拟只调过天数的实例: %v", err)
		}
		applyMigrationFile(t, db3, "migrations/sqlite/0082_retention_global_keep.sql")
		assertRetentionRow(t, db3, 1, 1000, 0, 7)
	})
}

func openBareSQLite(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := t.TempDir() + "/bare.db"
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open 临时 sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

// applyMigrationFile 执行迁移文件里的全部语句(去掉注释行后按 ; 切分)。
func applyMigrationFile(t *testing.T, db *sql.DB, rel string) {
	t.Helper()
	raw, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("读迁移 %s: %v", rel, err)
	}
	var lines []string
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "--") {
			continue
		}
		lines = append(lines, l)
	}
	for _, stmt := range strings.Split(strings.Join(lines, "\n"), ";") {
		s := strings.TrimSpace(stmt)
		if s == "" {
			continue
		}
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("执行迁移 %s 失败: %v\n语句: %s", rel, err, s)
		}
	}
}

func assertRetentionRow(t *testing.T, db *sql.DB, wantEnabled, wantGlobal, wantPerProject, wantAge int) {
	t.Helper()
	var enabled, global, perProject, age int
	if err := db.QueryRow(
		`SELECT enabled, keep_global, keep_per_project, max_age_days FROM retention_config WHERE id = 1`,
	).Scan(&enabled, &global, &perProject, &age); err != nil {
		t.Fatalf("读 retention_config: %v", err)
	}
	if enabled != wantEnabled || global != wantGlobal || perProject != wantPerProject || age != wantAge {
		t.Fatalf("retention_config = (enabled=%d, keep_global=%d, keep_per_project=%d, max_age_days=%d), 期望 (%d, %d, %d, %d)",
			enabled, global, perProject, age, wantEnabled, wantGlobal, wantPerProject, wantAge)
	}
}
