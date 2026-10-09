package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/storetest"
)

// TestMigration0054ResourceGroups 验证 0054/0055 分组表落地:
//   - visibility CHECK 只放行 public/private
//   - name 唯一
//   - 删组级联删成员名册
//   - 同一(组, 用户)不可重复入组
func TestMigration0054ResourceGroups(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)

	if _, err := st.DB.ExecContext(ctx,
		`INSERT INTO resource_groups (id, name, description, visibility, owner_id, created_by, created_at, updated_at)
		 VALUES ('g1', '平台组', '', 'private', 'u-owner', 'u-owner', ?, ?)`, now, now); err != nil {
		t.Fatalf("插入分组失败: %v", err)
	}

	var visibility string
	if err := st.DB.QueryRowContext(ctx, `SELECT visibility FROM resource_groups WHERE id='g1'`).Scan(&visibility); err != nil {
		t.Fatalf("读回分组: %v", err)
	}
	if visibility != "private" {
		t.Fatalf("visibility = %q, want private", visibility)
	}

	if _, err := st.DB.ExecContext(ctx,
		`INSERT INTO resource_groups (id, name, visibility, owner_id, created_by, created_at, updated_at)
		 VALUES ('g2', '其它组', 'team', 'u-owner', 'u-owner', ?, ?)`, now, now); err == nil {
		t.Fatal("visibility CHECK 未生效:'team' 不该插入成功")
	}
	if _, err := st.DB.ExecContext(ctx,
		`INSERT INTO resource_groups (id, name, visibility, owner_id, created_by, created_at, updated_at)
		 VALUES ('g3', '平台组', 'public', 'u-owner', 'u-owner', ?, ?)`, now, now); err == nil {
		t.Fatal("name 唯一约束未生效")
	}

	if _, err := st.DB.ExecContext(ctx,
		`INSERT INTO resource_group_members (group_id, user_id, created_at, created_by)
		 VALUES ('g1', 'u-a', ?, 'u-owner'), ('g1', 'u-b', ?, 'u-owner')`, now, now); err != nil {
		t.Fatalf("插入成员失败: %v", err)
	}
	if _, err := st.DB.ExecContext(ctx,
		`INSERT INTO resource_group_members (group_id, user_id, created_at, created_by)
		 VALUES ('g1', 'u-a', ?, 'u-owner')`, now, now); err == nil {
		t.Fatal("重复入组未被复合主键拦住")
	}

	if _, err := st.DB.ExecContext(ctx, `DELETE FROM resource_groups WHERE id='g1'`); err != nil {
		t.Fatalf("删组失败: %v", err)
	}
	var left int
	if err := st.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM resource_group_members`).Scan(&left); err != nil {
		t.Fatalf("数成员: %v", err)
	}
	if left != 0 {
		t.Fatalf("删组后残留 %d 行成员名册,CASCADE 未生效", left)
	}
}

// TestMigration0056To0058Defaults 验证三张既有表的新列默认值=改造前语义:
// 存量行(未指定新列)必须是「未归组 / 全局凭据 / 启用」。
func TestMigration0056To0058Defaults(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)

	credID := "c1"
	if _, err := st.DB.ExecContext(ctx,
		`INSERT INTO credentials (id, name, type, scope, ciphertext, masked_value, created_at, updated_at)
		 VALUES (?, 'token', 'git_token', 'global', X'00', '****', ?, ?)`, credID, now, now); err != nil {
		t.Fatalf("插入凭据失败: %v", err)
	}
	var (
		ownerID, description, disabledBy, disabledAt, createdBy string
		enabled                                                 int
	)
	if err := st.DB.QueryRowContext(ctx,
		`SELECT owner_id, description, enabled, disabled_by, disabled_at, created_by
		 FROM credentials WHERE id=?`, credID).Scan(&ownerID, &description, &enabled, &disabledBy, &disabledAt, &createdBy); err != nil {
		t.Fatalf("读回凭据新列: %v", err)
	}
	if ownerID != "" || description != "" || disabledBy != "" || disabledAt != "" || createdBy != "" {
		t.Fatalf("新列默认值非空串: %+v", []string{ownerID, description, disabledBy, disabledAt, createdBy})
	}
	if enabled != 1 {
		t.Fatalf("enabled = %d, want 1(存量凭据必须仍可用)", enabled)
	}

	if _, err := st.DB.ExecContext(ctx,
		`INSERT INTO projects (id, name, repo_url, credential_id, created_at, updated_at)
		 VALUES ('p1', 'demo', 'https://example.com/x.git', ?, ?, ?)`, credID, now, now); err != nil {
		t.Fatalf("插入项目失败: %v", err)
	}
	if _, err := st.DB.ExecContext(ctx,
		`INSERT INTO servers (id, name, host, port, `+"`user`"+`, credential_id, created_at, updated_at)
		 VALUES ('s1', 'web-1', '10.0.0.1', 22, 'root', ?, ?, ?)`, credID, now, now); err != nil {
		t.Fatalf("插入服务器失败: %v", err)
	}
	for _, q := range []struct {
		label, sql string
	}{
		{"projects", `SELECT group_id FROM projects WHERE id='p1'`},
		{"servers", `SELECT group_id FROM servers WHERE id='s1'`},
	} {
		var groupID string
		if err := st.DB.QueryRowContext(ctx, q.sql).Scan(&groupID); err != nil {
			t.Fatalf("%s 读回 group_id: %v", q.label, err)
		}
		if groupID != "" {
			t.Fatalf("%s.group_id = %q, want '' (未归组=存量语义不变)", q.label, groupID)
		}
	}

	// 归组后能被按组的查询命中(索引存在即可,部分索引在 MySQL 是普通索引)。
	if _, err := st.DB.ExecContext(ctx,
		`INSERT INTO resource_groups (id, name, visibility, owner_id, created_by, created_at, updated_at)
		 VALUES ('g1', '平台组', 'private', 'u-owner', 'u-owner', ?, ?)`, now, now); err != nil {
		t.Fatalf("插入分组失败: %v", err)
	}
	if _, err := st.DB.ExecContext(ctx, `UPDATE projects SET group_id='g1' WHERE id='p1'`); err != nil {
		t.Fatalf("项目归组失败: %v", err)
	}
	var grouped int
	if err := st.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE group_id != ''`).Scan(&grouped); err != nil {
		t.Fatalf("按组统计: %v", err)
	}
	if grouped != 1 {
		t.Fatalf("归组项目数 = %d, want 1", grouped)
	}
}
