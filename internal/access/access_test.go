package access

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func admin() *Actor         { return &Actor{UserID: "u-admin", Username: "admin", Role: RoleAdmin} }
func user(id string) *Actor { return &Actor{UserID: id, Username: id, Role: "user"} }

func privateGroup() *Group {
	return &Group{ID: "g-priv", Visibility: VisibilityPrivate, OwnerID: "g-owner", MemberIDs: []string{"g-member"}}
}
func publicGroup() *Group {
	return &Group{ID: "g-pub", Visibility: VisibilityPublic, OwnerID: "g-owner", MemberIDs: []string{"g-member"}}
}

// scopedAdmin / scopedUser 造带端点分组范围的 actor(即「这把令牌限了组」)。
// 不传分组时 GroupScope 为 nil,与「令牌不限组」同义。
func scopedAdmin(groups ...string) *Actor {
	a := admin()
	a.GroupScope = groups
	return a
}
func scopedUser(id string, groups ...string) *Actor {
	a := user(id)
	a.GroupScope = groups
	return a
}

// TestDecide_Matrix 穷举三态(未归组 / public / private)× 身份 × 动作。
func TestDecide_Matrix(t *testing.T) {
	cases := []struct {
		name    string
		actor   *Actor
		group   *Group
		act     Act
		wantErr error // nil=放行;非 nil 用 errors.Is 比对
	}{
		{"未归组/普通用户/看", user("alice"), nil, ActView, nil},
		{"未归组/普通用户/操作", user("alice"), nil, ActOperate, nil},
		{"未归组/普通用户/改归属", user("alice"), nil, ActManage, ErrForbidden},
		{"未归组/管理员/改归属", admin(), nil, ActManage, nil},

		{"public/路人/看", user("carol"), publicGroup(), ActView, nil},
		{"public/路人/操作", user("carol"), publicGroup(), ActOperate, nil},
		{"public/路人/管理", user("carol"), publicGroup(), ActManage, ErrForbidden},
		{"public/成员/操作", user("g-member"), publicGroup(), ActOperate, nil},
		{"public/组长/管理", user("g-owner"), publicGroup(), ActManage, nil},

		{"private/路人/看", user("carol"), privateGroup(), ActView, ErrForbidden},
		{"private/路人/操作", user("carol"), privateGroup(), ActOperate, ErrForbidden},
		{"private/路人/管理", user("carol"), privateGroup(), ActManage, ErrForbidden},
		{"private/成员/看", user("g-member"), privateGroup(), ActView, nil},
		{"private/成员/操作", user("g-member"), privateGroup(), ActOperate, nil},
		{"private/成员/管理", user("g-member"), privateGroup(), ActManage, ErrForbidden},
		{"private/组长/看", user("g-owner"), privateGroup(), ActView, nil},
		{"private/组长/管理", user("g-owner"), privateGroup(), ActManage, nil},
		{"private/管理员/管理", admin(), privateGroup(), ActManage, nil},

		// 端点分组范围:只能收窄,归组判定与身份判定用同一把尺(见 Decide 首刀)。
		{"限组/范围内成员/操作", scopedUser("g-member", "g-priv"), privateGroup(), ActOperate, nil},
		{"限组/范围内管理员/管理", scopedAdmin("g-priv"), privateGroup(), ActManage, nil},
		{"限组/范围外管理员/看", scopedAdmin("g-x"), publicGroup(), ActView, ErrForbidden},
		{"限组/范围外组长/管理", scopedUser("g-owner", "g-x"), privateGroup(), ActManage, ErrForbidden},
		// 限了组就碰不到未归组:范围存的是真组 ID,空串(Ungrouped)永远不在其中。
		{"限组/未归组资源/看", scopedUser("alice", "g-priv"), nil, ActView, ErrForbidden},
		{"空范围/等同不限", &Actor{UserID: "alice", Role: "user", GroupScope: []string{}}, nil, ActOperate, nil},

		{"匿名/未归组/看", nil, nil, ActView, ErrUnauthenticated},
		{"空身份/未归组/看", &Actor{Role: "user"}, nil, ActView, ErrUnauthenticated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Decide(tc.actor, tc.group, tc.act)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("放行失败:%v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestIsAdminNilActor 钉住与 vault.Actor 的差别:vault 里 nil=admin(系统调用),
// 本包只服务已登录请求,nil 必须 fail closed。
func TestIsAdminNilActor(t *testing.T) {
	var a *Actor
	if a.IsAdmin() {
		t.Fatal("nil actor 不得视为 admin")
	}
}

// TestActorInScope 钉住范围判定的边界:nil / 空 = 不限,其余按 ID 精确比对。
func TestActorInScope(t *testing.T) {
	var nilActor *Actor
	if !nilActor.InScope("g-any") {
		t.Fatal("nil actor 应视为不受端点范围约束(缺身份由 Decide 另一刀拦下)")
	}
	if !(&Actor{UserID: "u"}).InScope(Ungrouped) {
		t.Fatal("无范围 = 不限,未归组也应在范围内")
	}
	a := scopedUser("u", "g-a", "g-b")
	if !a.InScope("g-b") {
		t.Fatal("范围内的组应放行")
	}
	if a.InScope("g-c") {
		t.Fatal("范围外的组应拦下")
	}
	// 空串不在任何真实范围里(apitoken 存前剔除空项),所以限组 = 看不见未归组。
	if a.InScope(Ungrouped) {
		t.Fatal("限了组的令牌不该覆盖未归组")
	}
	if got := a.errOutOfScope("g-c"); !errors.Is(got, ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", got)
	}
	if got := a.errOutOfScope(Ungrouped); !strings.Contains(got.Error(), "未归组") {
		t.Fatalf("未归组的措辞应说明落点,得到:%v", got)
	}
}

// fakeRepo 是可编程的归属解析后端。
type fakeRepo struct {
	groups   map[string]*Group   // groupID → 快照
	byRes    map[string][]string // "kind/id" → 所属分组(空/缺失 = 未归组)
	visible  []string
	visErr   error
	visCalls []string
}

func (f *fakeRepo) GroupIDsOf(_ context.Context, kind Kind, id string) ([]string, error) {
	return f.byRes[string(kind)+"/"+id], nil
}

func (f *fakeRepo) GroupSnapshot(_ context.Context, groupID string) (*Group, error) {
	if groupID == Ungrouped {
		return nil, nil
	}
	return f.groups[groupID], nil
}

func (f *fakeRepo) PublicAndJoinedGroupIDs(_ context.Context, userID string) ([]string, error) {
	f.visCalls = append(f.visCalls, userID)
	return f.visible, f.visErr
}

func TestServiceCan(t *testing.T) {
	// 这里验的是数据轴(归属)的判定,而用例里的 actor 都是普通用户角色 —— 那四档预置自 0063
	// 起存在库里,不装进缓存就会 fail closed 成只读,「路人可操作」会被功能轴拦下,看着像数据轴坏了。
	useSeededPresets(t)
	repo := &fakeRepo{
		groups: map[string]*Group{"g-priv": privateGroup()},
		byRes: map[string][]string{
			"project/p1": {"g-priv"},
			"project/p2": nil,
			"project/p3": {"g-deleted"}, // 引用悬挂
			"run/r1":     {"g-priv"},    // run → project → group 由仓储内部完成
			"server/s1":  nil,
			"server/s2":  {"g-priv"},
		},
	}
	svc := NewService(repo)
	ctx := context.Background()

	cases := []struct {
		name, kind, id string
		actor          *Actor
		act            Act
		wantErr        error
	}{
		{"私有组项目/成员可看", "project", "p1", user("g-member"), ActView, nil},
		{"私有组项目/路人不可看", "project", "p1", user("carol"), ActView, ErrForbidden},
		{"私有组项目/管理员可管", "project", "p1", admin(), ActManage, nil},
		{"未归组项目/路人可操作", "project", "p2", user("carol"), ActOperate, nil},
		{"悬挂分组/路人403", "project", "p3", user("carol"), ActView, ErrForbidden},
		{"悬挂分组/管理员放行", "project", "p3", admin(), ActView, nil},
		{"运行继承项目分组/成员", "run", "r1", user("g-member"), ActOperate, nil},
		{"运行继承项目分组/路人", "run", "r1", user("carol"), ActView, ErrForbidden},
		{"未归组服务器/路人可操作", "server", "s1", user("carol"), ActOperate, nil},
		{"私有组服务器/路人不可", "server", "s2", user("carol"), ActOperate, ErrForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := svc.Can(ctx, tc.actor, Kind(tc.kind), tc.id, tc.act)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("放行失败:%v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}

	t.Run("缺资源ID不放行", func(t *testing.T) {
		if err := svc.Can(ctx, admin(), KindProject, "", ActView); err == nil {
			t.Fatal("空 ID 应报错")
		}
	})
	t.Run("仓储未装配时普通用户403", func(t *testing.T) {
		if err := NewService(nil).Can(ctx, user("carol"), KindProject, "p1", ActView); !errors.Is(err, ErrForbidden) {
			t.Fatalf("err = %v, want ErrForbidden", err)
		}
	})
	t.Run("仓储未装配+带端点范围也403", func(t *testing.T) {
		// 上面那条 admin 兜底走的是「归属查不到就算了」;可范围判定必须拿到归属,
		// 查不到不能当成落在范围内,否则限组令牌反而比不限组的更宽。
		if err := NewService(nil).Can(ctx, scopedAdmin("g-priv"), KindProject, "p1", ActView); !errors.Is(err, ErrForbidden) {
			t.Fatalf("err = %v, want ErrForbidden", err)
		}
	})
}

// TestServiceCan_EndpointScope 验端点分组范围在「读库解析归属」这条路上的表现:
// 它比本人原有的权限更严,且拦下的措辞要指向令牌而不是名册。
func TestServiceCan_EndpointScope(t *testing.T) {
	useSeededPresets(t)
	repo := &fakeRepo{
		groups: map[string]*Group{
			"g-priv": privateGroup(),
			"g-pub":  publicGroup(),
		},
		byRes: map[string][]string{
			"project/p1": {"g-priv"}, // 私有:owner/member
			"project/p2": {"g-pub"},  // 公开:全员本来可看
			"project/p3": nil,
			"run/r1":     {"g-pub"},
		},
	}
	svc := NewService(repo)
	ctx := context.Background()

	cases := []struct {
		name, kind, id string
		actor          *Actor
		act            Act
		wantErr        error
	}{
		{"范围内/公开组路人可看", "project", "p2", scopedUser("carol", "g-pub"), ActView, nil},
		{"范围外/公开组路人被收", "project", "p2", scopedUser("carol", "g-priv"), ActView, ErrForbidden},
		{"范围内/私有组成员可操作", "project", "p1", scopedUser("g-member", "g-priv"), ActOperate, nil},
		{"范围内/管理员可管", "project", "p1", scopedAdmin("g-priv"), ActManage, nil},
		// 硬收窄:归属人是管理员也一样被拦(与用户定的口径对齐)。
		{"范围外/管理员也被收", "project", "p2", scopedAdmin("g-priv"), ActView, ErrForbidden},
		{"范围外/组长也被收", "project", "p1", scopedUser("g-owner", "g-pub"), ActManage, ErrForbidden},
		{"限组后/未归组资源不可见", "project", "p3", scopedUser("carol", "g-pub"), ActView, ErrForbidden},
		{"运行同样受范围约束", "run", "r1", scopedUser("carol", "g-priv"), ActView, ErrForbidden},
		{"运行在范围内放行", "run", "r1", scopedUser("carol", "g-pub"), ActView, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := svc.Can(ctx, tc.actor, Kind(tc.kind), tc.id, tc.act)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("放行失败:%v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}

	t.Run("范围拦下的措辞指向令牌", func(t *testing.T) {
		// carol 对公开组本有权限,被拦纯粹因为范围;此时报「你不在名册里」会把人引偏。
		err := svc.Can(ctx, scopedUser("carol", "g-priv"), KindProject, "p2", ActView)
		if !strings.Contains(err.Error(), "端点范围") {
			t.Fatalf("应当提示是令牌范围问题,得到:%v", err)
		}
		if !strings.Contains(err.Error(), "g-pub") {
			t.Fatalf("消息应点出被拦的分组,得到:%v", err)
		}
	})
	t.Run("不限组的行为与从前一致", func(t *testing.T) {
		if err := svc.Can(ctx, user("carol"), KindProject, "p3", ActOperate); err != nil {
			t.Fatalf("未归组应照旧放行:%v", err)
		}
	})
}

func TestForRun(t *testing.T) {
	svc := NewService(&fakeRepo{
		groups: map[string]*Group{"g-priv": privateGroup()},
		byRes:  map[string][]string{"run/r1": {"g-priv"}},
	})
	if err := svc.ForRun(context.Background(), user("carol"), "r1", ActView); !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
	if err := svc.ForRun(context.Background(), user("g-owner"), "r1", ActView); err != nil {
		t.Fatalf("组长应放行:%v", err)
	}
}

func TestVisibleGroups(t *testing.T) {
	repo := &fakeRepo{visible: []string{"g-pub", "g-mine"}}
	svc := NewService(repo)
	ctx := context.Background()

	f, err := svc.VisibleGroups(ctx, admin())
	if err != nil {
		t.Fatal(err)
	}
	if !f.Unrestricted {
		t.Fatal("admin 应不受限")
	}
	if len(repo.visCalls) != 0 {
		t.Fatalf("admin 不该查库,却查了 %v", repo.visCalls)
	}

	f, err = svc.VisibleGroups(ctx, user("alice"))
	if err != nil {
		t.Fatal(err)
	}
	// 未归组必须排在白名单里,否则存量项目会从列表消失。
	if !reflect.DeepEqual(f.GroupIDs, []string{Ungrouped, "g-pub", "g-mine"}) {
		t.Fatalf("GroupIDs = %v", f.GroupIDs)
	}

	if _, err := svc.VisibleGroups(ctx, nil); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated", err)
	}
}

// TestVisibleGroups_EndpointScope 盯住列表侧的硬收窄:它必须与 Decide 给同一个答案,
// 否则会漂成「列表里看得见、点进去 403」。
func TestVisibleGroups_EndpointScope(t *testing.T) {
	ctx := context.Background()

	// 管理员 + 范围:不再不受限,且不必查库(范围就是答案)。
	repo := &fakeRepo{visible: []string{"g-pub", "g-mine"}}
	f, err := NewService(repo).VisibleGroups(ctx, scopedAdmin("g-a", "g-b"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Unrestricted {
		t.Fatal("带范围的令牌不能不受限")
	}
	if !reflect.DeepEqual(f.GroupIDs, []string{"g-a", "g-b"}) {
		t.Fatalf("GroupIDs = %v", f.GroupIDs)
	}
	if len(repo.visCalls) != 0 {
		t.Fatalf("admin 不该查库,却查了 %v", repo.visCalls)
	}

	// 普通用户 + 范围:取「范围 ∩ 本人可见集」,未归组那一项被自然排除,顺序按范围。
	f, err = NewService(repo).VisibleGroups(ctx, scopedUser("alice", "g-priv", "g-mine", "g-mine"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.GroupIDs, []string{"g-mine"}) {
		t.Fatalf("GroupIDs = %v, want [g-mine]", f.GroupIDs)
	}

	// 交集为空:必须是 DenyAll,不能掉进 Clause 的「只看未归组」兜底 —— 那等于放行未归组。
	f, err = NewService(repo).VisibleGroups(ctx, scopedUser("alice", "g-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if !f.DenyAll || len(f.GroupIDs) != 0 {
		t.Fatalf("空交集应为 DenyAll,得到 %+v", f)
	}
	if sql, args := f.Clause("p.group_id"); sql != "1 = 0" || len(args) != 0 {
		t.Fatalf("DenyAll 的 SQL = %q %v, want 1 = 0", sql, args)
	}

	// 仓储未装配:带范围又非管理员,无边界可算 → fail closed。
	if _, err := NewService(nil).VisibleGroups(ctx, scopedUser("alice", "g-a")); !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
	if _, err := NewService(nil).VisibleGroups(ctx, scopedAdmin("g-a")); err != nil {
		t.Fatalf("admin 带范围不该查库:%v", err)
	}
}

// TestListFilterClause 盯住拼接口径:占位符数量与参数一致,不受限时不出条件。
func TestListFilterClause(t *testing.T) {
	cases := []struct {
		name    string
		f       ListFilter
		wantSQL string
		wantArg []any
	}{
		{"admin 无附加条件", ListFilter{Unrestricted: true}, "", nil},
		{"仅未归组", ListFilter{GroupIDs: []string{Ungrouped}}, "p.group_id IN (?)", []any{Ungrouped}},
		{
			"未归组 + 两个组",
			ListFilter{GroupIDs: []string{Ungrouped, "a", "b"}},
			"p.group_id IN (?,?,?)",
			[]any{Ungrouped, "a", "b"},
		},
		// 零值 = 白名单为空,绝不能退化成「无条件放行」。
		{"零值仍要条件", ListFilter{}, "p.group_id = ?", []any{Ungrouped}},
		// 端点范围与本人可见集交集为空:必须恒假,空串在调用方眼里是「不加条件」= 全放行。
		{"范围空交集全拒", ListFilter{DenyAll: true}, "1 = 0", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql, args := tc.f.Clause("p.group_id")
			if sql != tc.wantSQL {
				t.Fatalf("sql = %q, want %q", sql, tc.wantSQL)
			}
			if !reflect.DeepEqual(args, tc.wantArg) && !(len(args) == 0 && len(tc.wantArg) == 0) {
				t.Fatalf("args = %v, want %v", args, tc.wantArg)
			}
		})
	}
}

func TestActString(t *testing.T) {
	if ActView.String() != "view" || ActOperate.String() != "operate" || ActManage.String() != "manage" {
		t.Fatal("Act.String 用于错误消息与日志,命名需稳定")
	}
	if got := Act(99).String(); got != "act(99)" {
		t.Fatalf("未知档位 = %q", got)
	}
}

// TestServiceCanPoint 钉住「按点判定」与「按类别上限判定」的差别 —— 这正是落点那条线今天会漏的地方:
// server.view / server.exec / container.operate / cert.manage 全是 KindServer,所以只给 container.operate
// 的角色,其对 KindServer 的上限同样是 operate。Can 拦不住它,CanPoint 才拦得住。
func TestServiceCanPoint(t *testing.T) {
	// 一个「能操作容器、但明确没有主机终端」的自定义角色:KindServer 上限照样是 operate。
	useStore(t, &fakeRoleStore{rows: []CustomRole{
		{ID: "r-container-only", Name: "容器操作员", Perms: []string{"server.view", "container.operate"}},
	}})
	if err := ReloadRoles(context.Background()); err != nil {
		t.Fatalf("装载角色目录: %v", err)
	}
	repo := &fakeRepo{
		groups: map[string]*Group{"g-priv": privateGroup()},
		byRes: map[string][]string{
			"server/s1": nil,
			"server/s2": {"g-priv"},
		},
	}
	svc := NewService(repo)
	ctx := context.Background()
	containerOnly := &Actor{UserID: "u-c", Username: "c", Role: "r-container-only"}

	t.Run("按上限放行、按点拦下", func(t *testing.T) {
		if err := svc.Can(ctx, containerOnly, KindServer, "s1", ActOperate); err != nil {
			t.Fatalf("上限这条线本该放行(它只要 KindServer 有任一 operate 点):%v", err)
		}
		if err := svc.CanPoint(ctx, containerOnly, "server.exec", KindServer, "s1", ActOperate); !errors.Is(err, ErrRoleCeiling) {
			t.Fatalf("err = %v, want ErrRoleCeiling", err)
		}
		if err := svc.CanPoint(ctx, containerOnly, "container.operate", KindServer, "s1", ActOperate); err != nil {
			t.Fatalf("持有该点本该放行:%v", err)
		}
	})
	t.Run("数据轴仍然生效", func(t *testing.T) {
		// 点够不等于归属够:私有组里的服务器,路人拿着 container.operate 也进不去。
		// 这条同时证明 CanPoint 没有把 Can 换成「只看点」。
		err := svc.CanPoint(ctx, containerOnly, "container.operate", KindServer, "s2", ActOperate)
		if !errors.Is(err, ErrForbidden) {
			t.Fatalf("err = %v, want ErrForbidden", err)
		}
		if errors.Is(err, ErrRoleCeiling) {
			t.Fatal("被数据轴拦下时不该带上上限标记(报错文案会指向错误的补救动作)")
		}
	})
	t.Run("管理员与旧空角色放行", func(t *testing.T) {
		if err := svc.CanPoint(ctx, admin(), "server.exec", KindServer, "s1", ActOperate); err != nil {
			t.Fatalf("管理员:%v", err)
		}
		legacy := &Actor{UserID: "u-legacy", Username: "legacy", Role: ""}
		if err := svc.CanPoint(ctx, legacy, "server.exec", KindServer, "s1", ActOperate); err != nil {
			t.Fatalf("空角色按管理员口径兜底:%v", err)
		}
	})
	t.Run("未知角色一律拦下", func(t *testing.T) {
		ghost := &Actor{UserID: "u-g", Username: "g", Role: "r-does-not-exist"}
		if err := svc.CanPoint(ctx, ghost, "server.exec", KindServer, "s1", ActOperate); !errors.Is(err, ErrRoleCeiling) {
			t.Fatalf("err = %v, want ErrRoleCeiling", err)
		}
	})
	t.Run("缺 actor fail closed", func(t *testing.T) {
		if err := svc.CanPoint(ctx, nil, "server.exec", KindServer, "s1", ActOperate); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("err = %v, want ErrUnauthenticated", err)
		}
	})
}

// TestServiceCanPoint_SeededPresets 钉住四档预置在新守卫下的表现:改判据不该顺手改变
// 存量角色的入口 —— user 与 ops 都有 server.exec / container.operate,developer 与 viewer 两个都没有。
func TestServiceCanPoint_SeededPresets(t *testing.T) {
	useSeededPresets(t)
	svc := NewService(&fakeRepo{byRes: map[string][]string{"server/s1": nil}})
	ctx := context.Background()

	cases := []struct {
		role, point string
		want        bool
	}{
		{"user", "server.exec", true},
		{"ops", "server.exec", true},
		{"ops", "container.operate", true},
		{"developer", "server.exec", false},
		{"developer", "container.operate", false},
		{"viewer", "server.exec", false},
	}
	for _, tc := range cases {
		a := &Actor{UserID: "u-" + tc.role, Username: tc.role, Role: tc.role}
		err := svc.CanPoint(ctx, a, tc.point, KindServer, "s1", ActOperate)
		if tc.want && err != nil {
			t.Fatalf("%s 持有 %s 却拦下了:%v", tc.role, tc.point, err)
		}
		if !tc.want && !errors.Is(err, ErrRoleCeiling) {
			t.Fatalf("%s 不持有 %s 却放行了:%v", tc.role, tc.point, err)
		}
	}
}

// TestServiceCan_MultiGroup 钉住多组归属的两条轴:
//   - 看得见 / 改得动 = **任一**所在组放行即可(并集):机器同时挂在两拨人名下,
//     不该因为另一拨人不在名册里就从这拨人眼前消失;
//   - 管归属 = **每一个**所在组都得管得住(交集):把它甩出或接进任何一组的后果都要自己担。
func TestServiceCan_MultiGroup(t *testing.T) {
	useSeededPresets(t)
	privOther := &Group{ID: "g-other", Visibility: VisibilityPrivate, OwnerID: "other-owner"}
	repo := &fakeRepo{
		groups: map[string]*Group{
			"g-priv":  privateGroup(),
			"g-pub":   publicGroup(),
			"g-other": privOther,
		},
		byRes: map[string][]string{
			"server/shared": {"g-priv", "g-pub"},
			"server/double": {"g-priv", "g-other"},
			"server/broken": {"g-deleted", "g-pub"}, // 一组引用悬挂 + 一组公开
		},
	}
	svc := NewService(repo)
	ctx := context.Background()

	cases := []struct {
		name, id string
		actor    *Actor
		act      Act
		wantErr  error
	}{
		{"并集:公开组那侧放行", "shared", user("carol"), ActOperate, nil},
		{"并集:私有组成员那侧放行", "shared", user("g-member"), ActView, nil},
		{"交集:只认得一个组不能管归属", "double", user("g-owner"), ActManage, ErrForbidden},
		{"交集:两组都管得住才放行", "double", admin(), ActManage, nil},
		// 引用悬挂只影响它自己那一组:另一组仍能放行(别把整台机器拖成 403)。
		{"悬挂组不连坐", "broken", user("carol"), ActView, nil},
		// 端点范围同样按并集:令牌限了其中一个组,就能碰这台机器(权限仍来自那一组)。
		{"范围命中任一所属组", "shared", scopedUser("g-member", "g-priv"), ActView, nil},
		{"范围一组都不含", "shared", scopedUser("carol", "g-other"), ActView, ErrForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := svc.Can(ctx, tc.actor, KindServer, tc.id, tc.act)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("放行失败:%v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}

	// 悬挂引用 + 私有组:并集不得把悬挂那一组当公开而放行路人。
	repo.byRes["server/broken-private"] = []string{"g-deleted", "g-priv"}
	if err := svc.Can(ctx, user("carol"), KindServer, "broken-private", ActView); !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden(悬挂组按最私有一档)", err)
	}
}

// TestMemberClause 钉住多组归属的列表 SQL:未归组用 NOT EXISTS 表达,
// 归组用 EXISTS + IN;两者 OR 起来才是「看得见的都列出来」。
func TestMemberClause(t *testing.T) {
	cases := []struct {
		name    string
		f       ListFilter
		wantSQL string
		wantArg []any
	}{
		{"admin 无附加条件", ListFilter{Unrestricted: true}, "", nil},
		{"范围空交集全拒", ListFilter{DenyAll: true}, "1 = 0", nil},
		{
			"仅未归组",
			ListFilter{GroupIDs: []string{Ungrouped}},
			"NOT EXISTS (SELECT 1 FROM server_groups m WHERE m.server_id = s.id)",
			nil,
		},
		{
			"未归组 + 两个组",
			ListFilter{GroupIDs: []string{Ungrouped, "a", "b"}},
			"(NOT EXISTS (SELECT 1 FROM server_groups m WHERE m.server_id = s.id) OR " +
				"EXISTS (SELECT 1 FROM server_groups m WHERE m.server_id = s.id AND m.group_id IN (?,?)))",
			[]any{"a", "b"},
		},
		{
			"仅分组(端点范围没有未归组)",
			ListFilter{GroupIDs: []string{"a"}},
			"EXISTS (SELECT 1 FROM server_groups m WHERE m.server_id = s.id AND m.group_id IN (?))",
			[]any{"a"},
		},
		// 零值 = 白名单为空,退化成「只见未归组」而不是「全放行」。
		{
			"零值只见未归组",
			ListFilter{},
			"NOT EXISTS (SELECT 1 FROM server_groups m WHERE m.server_id = s.id)",
			nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql, args := tc.f.MemberClause("s.id", "server_groups", "server_id", "group_id")
			if sql != tc.wantSQL {
				t.Fatalf("sql = %q, want %q", sql, tc.wantSQL)
			}
			if !reflect.DeepEqual(args, tc.wantArg) && !(len(args) == 0 && len(tc.wantArg) == 0) {
				t.Fatalf("args = %v, want %v", args, tc.wantArg)
			}
		})
	}
}
