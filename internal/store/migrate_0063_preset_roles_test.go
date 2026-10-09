package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/store"
	"github.com/fuuhoo/pipewright/internal/storetest"
)

// TestMigration0063PresetRoles 验证 0063 的四档预置角色确实落进了库,且点集与它们
// 当初在代码表 internal/access/perms.go 里的定位逐字一致。
//
// 这条守卫为什么重要:这四行是**存量账号的唯一救命稻草**。users.role / sessions.role 里存的就是
// 'user' / 'developer' / 'ops' / 'viewer' 这些字串,角色一旦既不在代码表也不在库里,
// access.ValidRole 就认不出它 → IsUserSession 为 false → 该账号的每个请求都 401「会话无效」
// (internal/httpapi/middleware.go 的 RequireUser)。所以播种缺一行、少一个点都得炸在测试里,
// 而不是炸在一个升级后忽然登不进去的账号上。
//
// 两方言都跑:MySQL 那份是另一个文件,语法只在 sqlite 验过的话等于没验。
func TestMigration0063PresetRoles(t *testing.T) {
	storetest.ForEachDialect(t, func(t *testing.T, st *store.Store) {
		ctx := context.Background()

		wants := []struct {
			id    string
			count int
			// 必须有的点 / 绝不能有的点(定位就落在这两组上)。
			must    []string
			mustNot []string
		}{
			// 普通用户:四类落点都能动,设置类不给。
			{"user", 20, []string{"server.exec", "cluster.operate", "project.edit"}, []string{"settings.access"}},
			// 开发者:编排可改,落点只读。
			{"developer", 15, []string{"project.edit", "server.view"}, []string{"server.exec", "container.operate", "settings.access"}},
			// 运维:落点可操作,编排只读。
			{"ops", 18, []string{"server.exec", "container.operate", "cluster.operate"}, []string{"project.edit", "library.edit", "settings.access"}},
			// 只读:全是 *.view,一个 operate 都不给。
			{"viewer", 12, []string{"dashboard.view", "server.view"}, []string{"run.operate", "server.exec", "settings.access"}},
		}

		var total int
		if err := st.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM roles`).Scan(&total); err != nil {
			t.Fatalf("数 roles 失败: %v", err)
		}
		if total != len(wants) {
			t.Fatalf("roles 行数 = %d, 期望 %d(0063 只播种这四档)", total, len(wants))
		}

		for _, w := range wants {
			var name, createdAt string
			if err := st.DB.QueryRowContext(ctx,
				`SELECT name, created_at FROM roles WHERE id = ?`, w.id,
			).Scan(&name, &createdAt); err != nil {
				t.Fatalf("角色 %s 没播种进库: %v", w.id, err)
			}
			// name 填 id 本身:前端据此回落到八语种标签(见 labelForRoleId),改名后才显库里的名字。
			if name != w.id {
				t.Fatalf("角色 %s 的 name = %q, 期望 %q", w.id, name, w.id)
			}

			n := countPerms(t, st, ctx, w.id)
			if n != w.count {
				t.Fatalf("角色 %s 点数 = %d, 期望 %d", w.id, n, w.count)
			}
			for _, p := range w.must {
				if !hasPerm(t, st, ctx, w.id, p) {
					t.Fatalf("角色 %s 缺点 %s", w.id, p)
				}
			}
			for _, p := range w.mustNot {
				if hasPerm(t, st, ctx, w.id, p) {
					t.Fatalf("角色 %s 多出点 %s(档位定位被写坏了)", w.id, p)
				}
			}
		}

		// 展示顺序靠 created_at 逐档 +1 秒撑住(列表按 created_at, id 排),别退化成字序。
		order := queryStrings(t, st, ctx,
			`SELECT id FROM roles ORDER BY created_at, id`)
		if got := strings.Join(order, ","); got != "user,developer,ops,viewer" {
			t.Fatalf("四档在页面上的顺序 = %s, 期望 user,developer,ops,viewer", got)
		}
	})
}

func countPerms(t *testing.T, st *store.Store, ctx context.Context, roleID string) int {
	t.Helper()
	var n int
	if err := st.DB.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM role_perms WHERE role_id = ?`, roleID,
	).Scan(&n); err != nil {
		t.Fatalf("数 %s 的点失败: %v", roleID, err)
	}
	return n
}

func hasPerm(t *testing.T, st *store.Store, ctx context.Context, roleID, permID string) bool {
	t.Helper()
	var n int
	if err := st.DB.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM role_perms WHERE role_id = ? AND perm_id = ?`, roleID, permID,
	).Scan(&n); err != nil {
		t.Fatalf("查 %s/%s 失败: %v", roleID, permID, err)
	}
	return n > 0
}

func queryStrings(t *testing.T, st *store.Store, ctx context.Context, q string) []string {
	t.Helper()
	rows, err := st.DB.QueryContext(ctx, q)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("扫描失败: %v", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("迭代失败: %v", err)
	}
	return out
}
