package users

import (
	"testing"

	"github.com/huangchengsir/pipewright/internal/storetest"
)

// 回归:`GetByID` 曾因 scanView 复用 scanInternal(9 列)而永远失败
// (scan: expected 9 destination, not 8)。锁住修复。
func TestRegression_GetByIDWorks(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := NewService(db)
	if err := svc.BootstrapAdminRow("admin", "phc_hash_value"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	u, err := svc.GetByID(BootstrapAdminRegularUserID)
	if err != nil {
		t.Fatalf("GetByID 应成功: %v", err)
	}
	if u.Username != "admin" || u.Role != RoleAdmin || !u.Enabled {
		t.Fatalf("字段错: %+v", u)
	}
	if u.CreatedAt.IsZero() || u.UpdatedAt.IsZero() {
		t.Fatal("CreatedAt/UpdatedAt 应被解析")
	}
	// 不存在 → ErrNotFound
	if _, err := svc.GetByID("no-such-id"); err == nil {
		t.Fatal("不存在应报错")
	}
}

// v6.2 §5.2:users List(admin 用户管理页数据源)。
func TestList_FiltersAndOrder(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := NewService(db)
	mustV(t, svc.BootstrapAdminRow("admin", "h"), "bootstrap")

	for _, u := range []struct{ id, name, role string; enabled bool }{
		{"u-alice", "alice", RoleUser, true},
		{"u-bob", "bob", RoleUser, false},
		{"u-carol", "carol", RoleAdmin, true},
	} {
		en := 0
		if u.enabled {
			en = 1
		}
		_, err := db.Exec(`INSERT INTO users (id, username, password_hash, role, enabled, created_at, updated_at)
		                   VALUES (?, ?, 'h', ?, ?, '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
			u.id, u.name, u.role, en)
		mustV(t, err, "seed "+u.name)
	}

	// 默认:仅 enabled,按 username 升序 → admin, alice, carol
	all, err := svc.List(ListFilter{})
	mustV(t, err, "list all")
	var names []string
	for _, u := range all {
		names = append(names, u.Username)
	}
	if len(names) != 3 || names[0] != "admin" || names[1] != "alice" || names[2] != "carol" {
		t.Fatalf("默认列表错: %v", names)
	}
	// 注:List 返回 []*User,而 User 结构体本身没有 PasswordHash 字段
	// (只有 InternalUser 才嵌入它),hash 泄漏在类型层面就被排除。

	// role=user + 含禁用 → alice, bob
	us, err := svc.List(ListFilter{Role: RoleUser, IncludeDisabled: true})
	mustV(t, err, "list users")
	if len(us) != 2 || us[0].Username != "alice" || us[1].Username != "bob" {
		t.Fatalf("role=user 过滤错: %+v", us)
	}

	// role=admin → admin, carol
	admins, err := svc.List(ListFilter{Role: RoleAdmin})
	mustV(t, err, "list admins")
	if len(admins) != 2 {
		t.Fatalf("role=admin 应 2 条, got %d", len(admins))
	}

	// 分页:limit=1, offset=1 → alice(跳过 admin)
	page, err := svc.List(ListFilter{Limit: 1, Offset: 1})
	mustV(t, err, "page")
	if len(page) != 1 || page[0].Username != "alice" {
		t.Fatalf("分页错: %+v", page)
	}
}

func mustV(t *testing.T, err error, step string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", step, err)
	}
}
