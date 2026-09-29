package access

import (
	"context"
	"errors"
	"reflect"
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

// fakeRepo 是可编程的归属解析后端。
type fakeRepo struct {
	groups   map[string]*Group // groupID → 快照
	byRes    map[string]string // "kind/id" → groupID
	visible  []string
	visErr   error
	visCalls []string
}

func (f *fakeRepo) GroupIDOf(_ context.Context, kind Kind, id string) (string, error) {
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
		byRes: map[string]string{
			"project/p1": "g-priv",
			"project/p2": Ungrouped,
			"project/p3": "g-deleted", // 引用悬挂
			"run/r1":     "g-priv",    // run → project → group 由仓储内部完成
			"server/s1":  Ungrouped,
			"server/s2":  "g-priv",
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
}

func TestForRun(t *testing.T) {
	svc := NewService(&fakeRepo{
		groups: map[string]*Group{"g-priv": privateGroup()},
		byRes:  map[string]string{"run/r1": "g-priv"},
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
