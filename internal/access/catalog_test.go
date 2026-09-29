package access

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// catalog.go 的测试:自定义角色在功能轴上的读取口径。
//
// 缓存是包级状态,每个用例结束都要把仓储摘掉(SetRoleStore(nil)),否则后一个用例会
// 看见前一个用例灌进去的角色 —— 那种串味在 CI 上按随机顺序才会暴露,最难查。

// fakeRoleStore 是 RoleStore 的内存替身;errOnCall 用来模拟「第二次读库失败」。
type fakeRoleStore struct {
	rows      []CustomRole
	calls     int
	errOnCall int
}

func (f *fakeRoleStore) ListCustomRoles(context.Context) ([]CustomRole, error) {
	f.calls++
	if f.errOnCall == f.calls {
		return nil, errors.New("db down")
	}
	return f.rows, nil
}

func useStore(t *testing.T, s RoleStore) {
	t.Helper()
	SetRoleStore(s)
	t.Cleanup(func() { SetRoleStore(nil) })
}

func TestCatalogCustomRolePoints(t *testing.T) {
	useStore(t, &fakeRoleStore{rows: []CustomRole{{
		ID:    "r-release-op",
		Name:  "发布操作员",
		Perms: []string{"project.edit", "project.view", "ghost.point", "project.edit"},
	}}})
	if err := ReloadRoles(context.Background()); err != nil {
		t.Fatalf("重载失败: %v", err)
	}

	if !ValidRole("r-release-op") {
		t.Fatal("自定义角色没被认下来:users 建号与 RequireUser 都会把它当非法角色")
	}
	// 编排类给了 Operate(project.edit),落点类一个点都没有 → 只能 View。
	if got := Ceiling("r-release-op", KindProject); got != ActOperate {
		t.Errorf("Ceiling(project) = %s, want operate", got)
	}
	if got := Ceiling("r-release-op", KindServer); got != ActView {
		t.Errorf("Ceiling(server) = %s, want view(一个点都不给的类别只能看)", got)
	}
	// 字典外的 ghost.point 与重复项都该被清掉,且顺序按字典(project.view 在 project.edit 前)。
	want := []string{"project.view", "project.edit"}
	if got := PermsFor("r-release-op"); !reflect.DeepEqual(got, want) {
		t.Errorf("PermsFor = %v, want %v", got, want)
	}
	if HasPerm("r-release-op", "run.operate") {
		t.Error("HasPerm 给了点集外的点")
	}
	if SettingsAllowed("r-release-op") {
		t.Error("自定义角色拿到了设置点")
	}
	caps := CapabilitiesFor("r-release-op")
	if !reflect.DeepEqual(caps.Perms, want) {
		t.Errorf("能力位 perms = %v, want %v", caps.Perms, want)
	}
}

func TestCatalogNeverShadowsBuiltinRoles(t *testing.T) {
	// 脏数据里冒出一个 id='admin' 的自定义行(手工改库、迁移脚本写错都可能),不许把
	// 内置 admin 的点集与档位顶掉 —— 内置档的权威在代码表。
	useStore(t, &fakeRoleStore{rows: []CustomRole{{ID: "admin", Name: "冒牌", Perms: []string{}}}})
	if err := ReloadRoles(context.Background()); err != nil {
		t.Fatalf("重载失败: %v", err)
	}
	if got := Ceiling("admin", KindProject); got != ActManage {
		t.Errorf("Ceiling(admin) = %s, want manage(被库里的行顶掉了)", got)
	}
	if len(PermsFor("admin")) != len(Perms()) {
		t.Errorf("admin 的点集 = %d 条, want 全字典 %d 条", len(PermsFor("admin")), len(Perms()))
	}
	if ids := CustomRoleIDs(); len(ids) != 0 {
		t.Errorf("缓存里留下了内置 id 的自定义行: %v", ids)
	}
}

func TestCatalogUnknownRoleFailsClosed(t *testing.T) {
	useStore(t, &fakeRoleStore{})
	if err := ReloadRoles(context.Background()); err != nil {
		t.Fatalf("重载失败: %v", err)
	}
	if ValidRole("r-deleted") {
		t.Error("库里没有的角色被认成了合法角色")
	}
	if got := Ceiling("r-deleted", KindProject); got != ActView {
		t.Errorf("Ceiling(未知角色) = %s, want view", got)
	}
	if got := PermsFor("r-deleted"); len(got) != 0 {
		t.Errorf("PermsFor(未知角色) = %v, want 空集", got)
	}
	if SettingsAllowed("r-deleted") {
		t.Error("未知角色拿到了设置点")
	}
	// 空串是旧会话取值,语义「按管理员」,不许被 fail closed 顺手改掉。
	if got := Ceiling("", KindProject); got != ActManage {
		t.Errorf("Ceiling('') = %s, want manage", got)
	}
}

func TestCatalogReloadFailureKeepsLastSnapshot(t *testing.T) {
	store := &fakeRoleStore{
		rows:      []CustomRole{{ID: "r-1", Perms: []string{"project.view", "project.edit"}}},
		errOnCall: 2,
	}
	useStore(t, store)
	if err := ReloadRoles(context.Background()); err != nil {
		t.Fatalf("首次重载失败: %v", err)
	}
	if !ValidRole("r-1") {
		t.Fatal("首次重载没生效")
	}
	if err := ReloadRoles(context.Background()); err == nil {
		t.Fatal("第二次重载本该失败")
	}
	// 失败那一趟不许把缓存清空:清空等于把所有自定义角色瞬间降成「只能看」。
	if !ValidRole("r-1") {
		t.Error("重载失败后快照被清了(在线用户会突然少掉菜单)")
	}
}

func TestSetRoleStoreNilClearsCustomRoles(t *testing.T) {
	SetRoleStore(&fakeRoleStore{rows: []CustomRole{{ID: "r-1", Perms: []string{"project.view"}}}})
	if err := ReloadRoles(context.Background()); err != nil {
		t.Fatalf("重载失败: %v", err)
	}
	SetRoleStore(nil)
	t.Cleanup(func() { SetRoleStore(nil) })
	if ValidRole("r-1") {
		t.Error("摘掉仓储后自定义角色仍被认下来(缓存没清)")
	}
	if ids := CustomRoleIDs(); len(ids) != 0 {
		t.Errorf("摘掉仓储后缓存里还剩: %v", ids)
	}
}

func TestReloadWithoutStoreIsNoop(t *testing.T) {
	SetRoleStore(nil)
	if err := ReloadRoles(context.Background()); err != nil {
		t.Fatalf("未装配仓储时重载本该 no-op,得到错误: %v", err)
	}
	// 内置档不依赖仓储,照旧可用。
	if !ValidRole(RoleDeveloper) {
		t.Error("内置档在没接库的情况下不可用了")
	}
}
