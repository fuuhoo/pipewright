package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/storetest"
)

// TestMigration0062Roles 验证 0062 角色表落地:
//   - 自定义角色各列默认值(描述/模板来源/建号者可为空串)
//   - name 唯一
//   - 同一(角色, 功能点)不可重复(复合主键)
//   - 删角色级联删它的点集(不留半套权限)
func TestMigration0062Roles(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)

	if _, err := st.DB.ExecContext(ctx,
		`INSERT INTO roles (id, name, created_at, updated_at)
		 VALUES ('r-1', '发布操作员', ?, ?)`, now, now); err != nil {
		t.Fatalf("插入自定义角色失败: %v", err)
	}
	var description, baseRole, createdBy string
	if err := st.DB.QueryRowContext(ctx,
		`SELECT description, base_role, created_by FROM roles WHERE id='r-1'`).
		Scan(&description, &baseRole, &createdBy); err != nil {
		t.Fatalf("读回新列: %v", err)
	}
	if description != "" || baseRole != "" || createdBy != "" {
		t.Fatalf("新列默认值应为空串,实得 description=%q base_role=%q created_by=%q",
			description, baseRole, createdBy)
	}

	if _, err := st.DB.ExecContext(ctx,
		`INSERT INTO roles (id, name, created_at, updated_at)
		 VALUES ('r-2', '发布操作员', '', '')`, now, now); err == nil {
		t.Fatal("name 唯一约束未生效")
	}

	if _, err := st.DB.ExecContext(ctx,
		`INSERT INTO role_perms (role_id, perm_id) VALUES ('r-1', 'run.operate'), ('r-1', 'server.view')`); err != nil {
		t.Fatalf("插入角色点集失败: %v", err)
	}
	if _, err := st.DB.ExecContext(ctx,
		`INSERT INTO role_perms (role_id, perm_id) VALUES ('r-1', 'run.operate')`); err == nil {
		t.Fatal("重复功能点未被复合主键拦住")
	}

	if _, err := st.DB.ExecContext(ctx, `DELETE FROM roles WHERE id='r-1'`); err != nil {
		t.Fatalf("删角色失败: %v", err)
	}
	var left int
	// 只数自己插的那条角色:0063 起库里预置了四档角色共 65 个功能点,全表计数会读成「CASCADE 坏了」。
	if err := st.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM role_perms WHERE role_id='r-1'`).Scan(&left); err != nil {
		t.Fatalf("数角色点: %v", err)
	}
	if left != 0 {
		t.Fatalf("删角色后残留 %d 行功能点,CASCADE 未生效", left)
	}
}
