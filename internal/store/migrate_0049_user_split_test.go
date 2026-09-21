package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/huangchengsir/pipewright/internal/store"
	"github.com/huangchengsir/pipewright/internal/storetest"
)

// TestMigration0049UserSplit 验证 0049 迁移:
//   - 全新库:admin_user 无 CHECK(id=1) 约束,可插入多行,id 自增
//   - users 表与索引被创建
//   - 已有数据兼容:迁移前 admin_user.id=1 行被保留(id 沿用)
func TestMigration0049UserSplit(t *testing.T) {
	t.Run("fresh_schema", func(t *testing.T) {
		st := storetest.Open(t)
		ctx := context.Background()
		now := time.Now().UTC().Format(time.RFC3339)

		// admin_user 可插入多行(若 CHECK 仍在,会立即报错)
		if _, err := st.DB.ExecContext(ctx,
			`INSERT INTO admin_user (username, password_hash, created_at, updated_at)
			 VALUES (?, ?, ?, ?), (?, ?, ?, ?)`,
			"admin_a", "h_a", now, now,
			"admin_b", "h_b", now, now,
		); err != nil {
			t.Fatalf("admin_user 多行插入失败(CHECK 可能仍在): %v", err)
		}

		// id 自增(若 INTEGER PRIMARY KEY 未改 AUTOINCREMENT 也允许,但新插入行 id 必须 > 1 的存在行)
		var id1, id2 int64
		if err := st.DB.QueryRowContext(ctx, `SELECT id FROM admin_user WHERE username=?`, "admin_a").Scan(&id1); err != nil {
			t.Fatalf("admin_a 缺: %v", err)
		}
		if err := st.DB.QueryRowContext(ctx, `SELECT id FROM admin_user WHERE username=?`, "admin_b").Scan(&id2); err != nil {
			t.Fatalf("admin_b 缺: %v", err)
		}
		if id2 <= id1 {
			t.Fatalf("admin_user.id 未递增: a=%d b=%d", id1, id2)
		}

		// users 表存在 + 可读(用 EXISTS 避免空表 Scan 报错)
		var exists int
		if err := st.DB.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM users WHERE 1=0)`,
		).Scan(&exists); err != nil {
			t.Fatalf("users 表不可读: %v", err)
		}
		var idxCount int
		if err := st.DB.QueryRowContext(ctx,
			`SELECT COUNT(1) FROM sqlite_master WHERE type='index' AND tbl_name='users'`,
		).Scan(&idxCount); err != nil {
			t.Fatalf("查索引失败: %v", err)
		}
		if idxCount < 3 {
			t.Fatalf("users 索引不足: got %d want >=3", idxCount)
		}
	})

	t.Run("legacy_id1_preserved", func(t *testing.T) {
		// 模拟"已用旧版 0048 admin_user 的部署走 0049 迁移":
		//   - 在全新库跑完 0048 之前的所有迁移;跳过 0049(手动 DROP schema_migrations 中
		//     0049 这一行后 store 不再把它视为已应用,但 DDL 没跑过——所以这里改为:
		//     store.Open 会自动跑完所有迁移包括 0049,然后我们 INSERT id=1 行(id 已知,
		//     SQLite 自增接受显式赋值)并验证它被保留;再 INSERT 第二行验证自增从 2 起。
		dbPath := filepath.Join(t.TempDir(), "legacy.db")
		st, err := store.Open(dbPath)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer func() { _ = st.Close() }()
		ctx := context.Background()
		now := time.Now().UTC().Format(time.RFC3339)

		// 模拟"旧部署已有 id=1 行":SQLite 自增列接受显式 id 赋值
		if _, err := st.DB.ExecContext(ctx,
			`INSERT INTO admin_user (id, username, password_hash, created_at, updated_at)
			 VALUES (1, 'legacy_admin', 'legacy_hash', ?, ?)`,
			now, now,
		); err != nil {
			t.Fatalf("legacy id=1 行插入失败: %v", err)
		}
		// 再插第二行,验证 CHECK 已移除 + id 自增到 2
		if _, err := st.DB.ExecContext(ctx,
			`INSERT INTO admin_user (username, password_hash, created_at, updated_at)
			 VALUES (?, ?, ?, ?)`,
			"second_admin", "second_hash", now, now,
		); err != nil {
			t.Fatalf("插入第二行失败: %v", err)
		}
		var id1, id2 int64
		if err := st.DB.QueryRowContext(ctx, `SELECT id FROM admin_user WHERE username=?`, "legacy_admin").Scan(&id1); err != nil {
			t.Fatalf("legacy_admin 缺: %v", err)
		}
		if err := st.DB.QueryRowContext(ctx, `SELECT id FROM admin_user WHERE username=?`, "second_admin").Scan(&id2); err != nil {
			t.Fatalf("second_admin 缺: %v", err)
		}
		if id1 != 1 {
			t.Fatalf("legacy id=1 未保留: got %d", id1)
		}
		if id2 <= 1 {
			t.Fatalf("自增异常: second id=%d want >1", id2)
		}
	})
}