package role_test

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/huangchengsir/pipewright/internal/access"
	"github.com/huangchengsir/pipewright/internal/role"
	"github.com/huangchengsir/pipewright/internal/storetest"
)

// role 服务的测试:自定义角色的读写与三条硬边界。
// 判定的口径(点集 → 档位)在 internal/access 那边已经穷举过,这里只验两件事:
// 写进去的点确实被判定读到;不该写进去的东西被挡在写路径上。

// testMaxNameLen 与 role 服务内部的展示名上限同步(它没导出,不为一个测试开 API 面)。
const testMaxNameLen = 40

// newService 开一份独立测试库并把它接到 access 的判定缓存上。
// 每次调用都是新库:storetest.Open 会建临时实例,所以同一测试里只能开一次,
// 否则服务写的库和 helper 插数据的库不是同一个(账号数、点集都会对不上)。
func newService(t *testing.T) (*role.Service, context.Context, *sql.DB) {
	t.Helper()
	st := storetest.Open(t)
	svc := role.New(st.DB)
	access.SetRoleStore(svc)
	t.Cleanup(func() { access.SetRoleStore(nil) })
	return svc, context.Background(), st.DB
}

func mustCreate(t *testing.T, svc *role.Service, ctx context.Context, name string, perms []string) *role.Role {
	t.Helper()
	r, err := svc.Create(ctx, role.CreateInput{Name: name, Perms: perms, CreatedBy: "u-admin"})
	if err != nil {
		t.Fatalf("建角色 %q 失败: %v", name, err)
	}
	return r
}

func addUserWithRole(t *testing.T, db *sql.DB, ctx context.Context, id, roleID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users (id, username, password_hash, role, enabled, description, created_at, updated_at)
		 VALUES (?, ?, 'x', ?, 1, '', ?, ?)`, id, id, roleID, now, now); err != nil {
		t.Fatalf("插入用户 %s 失败: %v", id, err)
	}
}

func TestCreateGetAndList(t *testing.T) {
	svc, ctx, _ := newService(t)
	perms := []string{"project.view", "run.operate", "server.view"}
	created := mustCreate(t, svc, ctx, "发布操作员", perms)
	if created.ID == "" {
		t.Fatal("新角色没有 ID")
	}

	got, err := svc.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("读回角色失败: %v", err)
	}
	want := []string{"project.view", "run.operate", "server.view"}
	if !reflect.DeepEqual(got.Perms, want) {
		t.Errorf("点集 = %v, want %v(按字典声明序回读)", got.Perms, want)
	}

	// 写进去的点必须真的参与判定 —— 否则页面勾了、请求照样 403。
	if access.Ceiling(created.ID, access.KindRun) != access.ActOperate {
		t.Errorf("Ceiling(run) 应为 operate,实得 %s", access.Ceiling(created.ID, access.KindRun))
	}
	if access.Ceiling(created.ID, access.KindProject) != access.ActView {
		t.Errorf("Ceiling(project) 应为 view(只勾了 project.view),实得 %s",
			access.Ceiling(created.ID, access.KindProject))
	}
	if access.SettingsAllowed(created.ID) {
		t.Error("自定义角色拿到了设置点")
	}

	list, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("列角色失败: %v", err)
	}
	// 内置五档在前(按 access.Roles() 的声明序),自定义在后。
	if len(list) != len(access.Roles())+1 {
		t.Fatalf("列表长度 = %d, want %d", len(list), len(access.Roles())+1)
	}
	for i, id := range access.Roles() {
		if list[i].ID != id || !list[i].Builtin {
			t.Errorf("第 %d 位应是内置 %s,实得 %q(builtin=%v)", i, id, list[i].ID, list[i].Builtin)
		}
	}
	last := list[len(list)-1]
	if last.ID != created.ID || last.Builtin {
		t.Errorf("末位应是自定义 %s,实得 %q(builtin=%v)", created.ID, last.ID, last.Builtin)
	}
}

func TestCreateRejects(t *testing.T) {
	svc, ctx, _ := newService(t)
	cases := []struct {
		name  string
		input role.CreateInput
		want  error
	}{
		{"空名", role.CreateInput{Name: "   "}, role.ErrValidation},
		{"超长名", role.CreateInput{Name: strings.Repeat("角", testMaxNameLen+1)}, role.ErrValidation},
		{"设置点", role.CreateInput{Name: "半个管理员", Perms: []string{"settings.access"}}, role.ErrSettingsPoint},
		{"字典外的点", role.CreateInput{Name: "脏名单", Perms: []string{"project.fly"}}, role.ErrUnknownPoint},
		{"模板不是内置档", role.CreateInput{Name: "怪模板", BaseRole: "不存在的档"}, role.ErrValidation},
	}
	for _, c := range cases {
		if _, err := svc.Create(ctx, c.input); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}

	// 名字大小写不敏感唯一:两条只差大小写的名字会在同一个下拉里读成两行一样的。
	mustCreate(t, svc, ctx, "Release Op", nil)
	if _, err := svc.Create(ctx, role.CreateInput{Name: "release OP"}); !errors.Is(err, role.ErrDuplicateName) {
		t.Errorf("重名(仅大小写不同)没被拦住: %v", err)
	}
}

func TestUpdateRepointsRoleAndBlocksBuiltin(t *testing.T) {
	svc, ctx, _ := newService(t)
	r := mustCreate(t, svc, ctx, "先给运行操作", []string{"run.view", "run.operate"})
	if access.Ceiling(r.ID, access.KindRun) != access.ActOperate {
		t.Fatal("初始档位应为 operate")
	}

	next := []string{"run.view"}
	updated, err := svc.Update(ctx, r.ID, role.UpdateInput{Perms: &next})
	if err != nil {
		t.Fatalf("改点集失败: %v", err)
	}
	if !reflect.DeepEqual(updated.Perms, next) {
		t.Fatalf("回读点集 = %v, want %v", updated.Perms, next)
	}
	// 缓存跟着变了 —— 在线用户下次拉 /api/auth/session(含页面刷新)就拿到新能力位。
	if access.Ceiling(r.ID, access.KindRun) != access.ActView {
		t.Errorf("撤点之后 Ceiling(run) 仍是 %s,说明缓存没刷新", access.Ceiling(r.ID, access.KindRun))
	}

	if _, err := svc.Update(ctx, access.RoleOps, role.UpdateInput{Description: strPtr("改内置")}); !errors.Is(err, role.ErrBuiltin) {
		t.Errorf("改内置角色没被挡住: %v", err)
	}
	if _, err := svc.Update(ctx, r.ID, role.UpdateInput{Name: strPtr("  ")}); !errors.Is(err, role.ErrValidation) {
		t.Errorf("改名成空串没被挡住: %v", err)
	}
	if _, err := svc.Update(ctx, r.ID, role.UpdateInput{Perms: &[]string{"ghost.point"}}); !errors.Is(err, role.ErrUnknownPoint) {
		t.Errorf("写字典外的点没被挡住: %v", err)
	}
}

func TestCopyFromAdminStripsSettingsPoint(t *testing.T) {
	svc, ctx, _ := newService(t)
	// 「从管理员模板复制一份」是常见起手式;settings.access 不可下放,复制时剥掉而不是报错。
	copied, err := svc.Copy(ctx, access.RoleAdmin, "管理员副本", "u-admin")
	if err != nil {
		t.Fatalf("复制失败: %v", err)
	}
	if copied.BaseRole != access.RoleAdmin {
		t.Errorf("BaseRole = %q, want admin", copied.BaseRole)
	}
	if access.HasPerm(copied.ID, access.PermSettingsAccess) {
		t.Error("副本拿到了设置点")
	}
	if !access.HasPerm(copied.ID, "project.edit") {
		t.Error("副本丢了 admin 的其它点")
	}
	// 内置档自身读代码表,不受副本影响。
	if !access.SettingsAllowed(access.RoleAdmin) {
		t.Error("内置 admin 的设置点被复制操作影响了")
	}
}

func TestDeleteGuards(t *testing.T) {
	svc, ctx, db := newService(t)
	r := mustCreate(t, svc, ctx, "待删", []string{"project.view"})

	if err := svc.Delete(ctx, access.RoleUser); !errors.Is(err, role.ErrBuiltin) {
		t.Errorf("删内置角色没被挡住: %v", err)
	}
	if err := svc.Delete(ctx, "no-such-role"); !errors.Is(err, role.ErrNotFound) {
		t.Errorf("删不存在的角色应报 NotFound,实得 %v", err)
	}

	// 有账号在用 → 拒绝,并带上人数(前端据此提示「先改派 N 个账号」)。
	addUserWithRole(t, db, ctx, "u-in-use-1", r.ID)
	addUserWithRole(t, db, ctx, "u-in-use-2", r.ID)
	err := svc.Delete(ctx, r.ID)
	if !errors.Is(err, role.ErrInUse) {
		t.Fatalf("删在用角色没被挡住: %v", err)
	}
	if !strings.Contains(err.Error(), "2") {
		t.Errorf("错误里该带使用人数,实得 %q", err.Error())
	}
	if !access.ValidRole(r.ID) {
		t.Error("被拒绝的删除把角色弄丢了")
	}

	// 改派干净之后才允许删;删完判定侧也不该再认得它。
	dropUsersWithRole(t, db, ctx, r.ID)
	if err := svc.Delete(ctx, r.ID); err != nil {
		t.Fatalf("删空闲角色失败: %v", err)
	}
	if access.ValidRole(r.ID) {
		t.Error("删掉的角色仍被判定认得(缓存没刷新)")
	}
	if _, err := svc.Get(ctx, r.ID); !errors.Is(err, role.ErrNotFound) {
		t.Errorf("删后 Get 应报 NotFound,实得 %v", err)
	}
}

func TestListCustomRolesDropsPointsNotInDict(t *testing.T) {
	svc, ctx, db := newService(t)
	r := mustCreate(t, svc, ctx, "干净", []string{"project.view"})
	// 手工塞一条字典外的点,模拟「代码删了某个功能点而库里有残留引用」。
	if _, err := db.ExecContext(ctx, `INSERT INTO role_perms (role_id, perm_id) VALUES (?, ?)`,
		r.ID, "ghost.point"); err != nil {
		t.Fatalf("写残留点失败: %v", err)
	}
	rows, err := svc.ListCustomRoles(ctx)
	if err != nil {
		t.Fatalf("列自定义角色失败: %v", err)
	}
	for _, row := range rows {
		if row.ID != r.ID {
			continue
		}
		if !reflect.DeepEqual(row.Perms, []string{"project.view"}) {
			t.Fatalf("点集 = %v, want 只留 [project.view]", row.Perms)
		}
		return
	}
	t.Fatalf("列表里没有刚建的 %s", r.ID)
}

func TestUserCountShownInList(t *testing.T) {
	svc, ctx, db := newService(t)
	r := mustCreate(t, svc, ctx, "带人气的", []string{"run.view"})
	addUserWithRole(t, db, ctx, "u-count-1", r.ID)

	list, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("列角色失败: %v", err)
	}
	for _, item := range list {
		if item.ID != r.ID {
			continue
		}
		if item.UserCount != 1 {
			t.Fatalf("UserCount = %d, want 1", item.UserCount)
		}
		return
	}
	t.Fatal("列表里没有刚建的角色")
}

func dropUsersWithRole(t *testing.T, db *sql.DB, ctx context.Context, roleID string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `DELETE FROM users WHERE role = ?`, roleID); err != nil {
		t.Fatalf("清理用户失败: %v", err)
	}
}

func strPtr(s string) *string { return &s }
