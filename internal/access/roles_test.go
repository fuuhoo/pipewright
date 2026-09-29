package access

import (
	"context"
	"errors"
	"testing"
)

// roles_test.go —— 功能轴(角色档位上限)的表驱动用例。
// 判定矩阵在 access_test.go(数据轴),这里只验两轴叠加后的取严结果。
//
// 四档预置(user / developer / ops / viewer)自 0063 起是库里的普通角色,代码侧不再有常量,
// 它们的档位由 roles_drift_test.go 读迁移播种的点集校验;下面这几张表只留代码侧那几行。

func TestCeiling_Matrix(t *testing.T) {
	cases := []struct {
		role string
		kind Kind
		want Act
	}{
		{RoleAdmin, KindProject, ActManage},
		{RoleAdmin, KindRun, ActManage},
		{RoleAdmin, KindServer, ActManage},
		{RoleAdmin, KindKubeCluster, ActManage},

		// 旧部署会话 role='' 按管理员上限:否则升级当天旧会话被静默降成只读。
		{"", KindProject, ActManage},
		{"", KindServer, ActManage},

		// 认不出的角色 / 类别一律先只让看(fail closed)。库里查到的角色要等 catalog 装载
		// 才算数(SetRoleStore 没接上时全都落这一档,所以装配失败的表现是「全员只读」)。
		{"strange-role", KindProject, ActView},
		{"developer", KindProject, ActView},
		{RoleAdmin, Kind("unknown-kind"), ActView},
	}
	for _, c := range cases {
		if got := Ceiling(c.role, c.kind); got != c.want {
			t.Errorf("Ceiling(%q, %s) = %s, want %s", c.role, c.kind, got, c.want)
		}
	}
}

func TestNormalizeRoleAndValid(t *testing.T) {
	if got := NormalizeRole(RoleAdmin); got != RoleAdmin {
		t.Fatalf("内置角色不该被改写: got %q", got)
	}
	if got := NormalizeRole("root"); got != RoleUser {
		t.Fatalf("未知名应按 user 落库: got %q", got)
	}
	// 空串是旧会话取值(语义 = 按管理员),不能被归一成 user。
	if got := NormalizeRole(""); got != "" {
		t.Fatalf("空角色应保留: got %q", got)
	}
	for _, r := range Roles() {
		if !ValidRole(r) {
			t.Fatalf("Roles() 里的 %q 不被 ValidRole 承认", r)
		}
	}
	if ValidRole("") || ValidRole("root") {
		t.Fatalf("空串与未知名都不算合法角色")
	}
}

// TestServiceCan_RoleCeiling 验 min(角色上限, 分组授予):
// 分组名册给了访问权,角色仍可以把这一档拦下来。
//
// 用例里的 developer / ops / viewer 是 0063 播种进库的预置档:先把它们装进判定缓存,
// 这里跑的才是线上装配后的同一形状(没装载时它们一律 fail closed 成只读)。
func TestServiceCan_RoleCeiling(t *testing.T) {
	useSeededPresets(t)
	repo := &fakeRepo{
		groups: map[string]*Group{
			"g-priv": {ID: "g-priv", Visibility: VisibilityPrivate, OwnerID: "viewer-owner", MemberIDs: []string{"viewer"}},
		},
		byRes: map[string]string{
			"project/p1":          Ungrouped,
			"server/s1":           Ungrouped,
			"run/r1":              Ungrouped,
			"project/p-priv":      "g-priv",
			"server/s-priv":       "g-priv",
			"kube_cluster/k-priv": "g-priv",
		},
	}
	svc := NewService(repo)
	ctx := context.Background()

	act := func(role string, kind Kind, id string, a Act) error {
		return svc.Can(ctx, &Actor{UserID: role + "-u-" + string(kind), Username: role, Role: role}, kind, id, a)
	}

	// 只读角色对未归组资源(数据轴全员可动)仍被功能轴拦下 —— 这正是新增档位要的效果。
	if err := act("viewer", KindProject, "p1", ActView); err != nil {
		t.Fatalf("viewer 看未归组项目: %v", err)
	}
	if err := act("viewer", KindProject, "p1", ActOperate); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer 改未归组项目应被角色上限拦下, got %v", err)
	}
	// 开终端 = KindServer/ActOperate,只读与开发者都不该拿得到 shell。
	if err := act("viewer", KindServer, "s1", ActOperate); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer 开服务器终端应 403, got %v", err)
	}
	if err := act("developer", KindServer, "s1", ActOperate); !errors.Is(err, ErrForbidden) {
		t.Fatalf("developer 开服务器终端应 403, got %v", err)
	}
	if err := act("ops", KindServer, "s1", ActOperate); err != nil {
		t.Fatalf("ops 开服务器终端: %v", err)
	}
	// 运维可以审批/部署已有的运行,但不能改项目配置。
	if err := act("ops", KindRun, "r1", ActOperate); err != nil {
		t.Fatalf("ops 操作运行: %v", err)
	}
	if err := act("ops", KindProject, "p1", ActOperate); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ops 改项目配置应 403, got %v", err)
	}
	// 存量 user 逐字不变。
	if err := act(RoleUser, KindProject, "p1", ActOperate); err != nil {
		t.Fatalf("user 改未归组项目: %v", err)
	}
	if err := act(RoleUser, KindServer, "s1", ActOperate); err != nil {
		t.Fatalf("user 操作未归组主机: %v", err)
	}
	// 旧会话空角色不被降权。
	if err := act("", KindServer, "s1", ActOperate); err != nil {
		t.Fatalf("空角色(旧会话)操作未归组主机: %v", err)
	}
	// 私有组内:名册给访问权,角色上限仍然生效(取严)。
	if err := svc.Can(ctx, &Actor{UserID: "viewer", Role: "viewer"}, KindProject, "p-priv", ActView); err != nil {
		t.Fatalf("私有组成员看: %v", err)
	}
	if err := svc.Can(ctx, &Actor{UserID: "viewer", Role: "viewer"}, KindProject, "p-priv", ActOperate); !errors.Is(err, ErrForbidden) {
		t.Fatalf("私有组成员是只读角色,操作应 403, got %v", err)
	}
	// 名册内的人看私有组里的主机:数据轴放行,功能轴(开发者对落点只读)拦下操作。
	if err := svc.Can(ctx, &Actor{UserID: "viewer", Role: "developer"}, KindServer, "s-priv", ActView); err != nil {
		t.Fatalf("名册成员看私有组主机: %v", err)
	}
	if err := svc.Can(ctx, &Actor{UserID: "viewer", Role: "developer"}, KindServer, "s-priv", ActOperate); !errors.Is(err, ErrForbidden) {
		t.Fatalf("名册成员但角色不允许动落点,应 403, got %v", err)
	}
	// Manage 刻意不受角色上限约束:组长(哪怕角色是只读)仍管得了自己组的归属。
	if err := svc.Can(ctx, &Actor{UserID: "viewer-owner", Role: "viewer"}, KindProject, "p-priv", ActManage); err != nil {
		t.Fatalf("组长改归属不该被角色上限夺走, got %v", err)
	}
	// 未归组的 Manage 依旧只有管理员(与加表前一致)。
	if err := act("viewer", KindProject, "p1", ActManage); !errors.Is(err, ErrForbidden) {
		t.Fatalf("未归组资源改归属应仅管理员, got %v", err)
	}
	// 数据轴先于功能轴拦人的情况:不在名册里的运维看私有组集群,仍是 403。
	if err := act("ops", KindKubeCluster, "k-priv", ActView); !errors.Is(err, ErrForbidden) {
		t.Fatalf("非成员看私有组集群应 403, got %v", err)
	}
}

// TestCapabilitiesFor 验回传给前端的能力位形状。
func TestCapabilitiesFor(t *testing.T) {
	useSeededPresets(t)
	admin := CapabilitiesFor(RoleAdmin)
	if !admin.Settings {
		t.Fatalf("管理员应有设置位")
	}
	if admin.Kinds[string(KindServer)] != "manage" {
		t.Fatalf("管理员主机档位 = %q, want manage", admin.Kinds[string(KindServer)])
	}
	viewer := CapabilitiesFor("viewer")
	if viewer.Settings {
		t.Fatalf("只读不该有设置位")
	}
	for _, k := range ResourceKinds() {
		if got := viewer.Kinds[string(k)]; got != "view" {
			t.Fatalf("viewer %s 档位 = %q, want view", k, got)
		}
	}
	// 未知角色按最严一档回传,不要因为查不到表就发散成 manage。
	unknown := CapabilitiesFor("root")
	for _, k := range ResourceKinds() {
		if got := unknown.Kinds[string(k)]; got != "view" {
			t.Fatalf("未知角色 %s 档位 = %q, want view", k, got)
		}
	}
	if unknown.Settings {
		t.Fatalf("未知角色不该有设置位")
	}
	// 空串(旧会话)与管理员同口径,前端菜单才不会突然少一块。
	if !CapabilitiesFor("").Settings {
		t.Fatalf("旧会话空角色应保留设置位")
	}
	// 每类资源都要有档位,前端按 kind 取值时不能拿到 undefined。
	for _, r := range Roles() {
		c := CapabilitiesFor(r)
		if len(c.Kinds) != len(ResourceKinds()) {
			t.Fatalf("%s 的能力位缺项: %+v", r, c.Kinds)
		}
	}
}
