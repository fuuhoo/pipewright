package access

import (
	"context"
	"errors"
	"testing"
)

// CanGroup 判的是「能不能把资源放进/移出这个组」,与 Can(从资源反查归属)相对。

func TestCanGroup(t *testing.T) {
	repo := &fakeRepo{
		groups: map[string]*Group{
			"priv": {ID: "priv", Visibility: VisibilityPrivate, OwnerID: "owner", MemberIDs: []string{"member"}},
		},
	}
	// 仓储路径:组不存在必须报错(上层映射成引用悬挂),不能静默放行。
	if err := NewService(repo).CanGroup(context.Background(), user("u1"), "missing", ActView); err == nil {
		t.Fatalf("missing group should error")
	}
	// 成员对私有组有 View/Operate,但没有 Manage:组员能干活,不能决定谁 belongs。
	if err := NewService(repo).CanGroup(context.Background(), user("member"), "priv", ActView); err != nil {
		t.Fatalf("member view: %v", err)
	}
	if err := NewService(repo).CanGroup(context.Background(), user("member"), "priv", ActManage); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member manage err = %v, want ErrForbidden", err)
	}
	if err := NewService(repo).CanGroup(context.Background(), user("owner"), "priv", ActManage); err != nil {
		t.Fatalf("owner manage: %v", err)
	}
	if err := NewService(repo).CanGroup(context.Background(), admin(), "priv", ActManage); err != nil {
		t.Fatalf("admin manage: %v", err)
	}
	// 移出分组(空串)不校验新侧:谁能管原组谁就能把它甩出去。
	if err := NewService(repo).CanGroup(context.Background(), user("u1"), Ungrouped, ActManage); err != nil {
		t.Fatalf("移出分组不该被挡: %v", err)
	}
	// 缺 actor fail closed。
	if err := NewService(repo).CanGroup(context.Background(), nil, "priv", ActView); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("nil actor err = %v, want ErrUnauthenticated", err)
	}
	// 仓储未装配:退化为「仅管理员」,而非放行。
	if err := NewService(nil).CanGroup(context.Background(), user("u1"), "priv", ActView); !errors.Is(err, ErrForbidden) {
		t.Fatalf("no-repo err = %v, want ErrForbidden", err)
	}
	if err := NewService(nil).CanGroup(context.Background(), admin(), "priv", ActView); err != nil {
		t.Fatalf("no-repo admin: %v", err)
	}
}

// TestCanGroup_EndpointScope 管归组的「新组」这一侧也要吃端点范围:
// 一把只授权了 A 组的令牌,既不该把资源放进 B 组,也不该把它甩出分组(甩出去 = 落范围外)。
func TestCanGroup_EndpointScope(t *testing.T) {
	ctx := context.Background()
	repo := &fakeRepo{
		groups: map[string]*Group{
			"priv": {ID: "priv", Visibility: VisibilityPrivate, OwnerID: "owner", MemberIDs: []string{"member"}},
			"pub":  {ID: "pub", Visibility: VisibilityPublic},
		},
	}
	svc := NewService(repo)

	// 组长本来能把资源放进自己的组,范围不含它 → 拦。
	if err := svc.CanGroup(ctx, scopedUser("owner", "pub"), "priv", ActManage); !errors.Is(err, ErrForbidden) {
		t.Fatalf("范围外目标组 err = %v, want ErrForbidden", err)
	}
	// 范围内照旧放行。
	if err := svc.CanGroup(ctx, scopedUser("owner", "priv"), "priv", ActManage); err != nil {
		t.Fatalf("范围内目标组: %v", err)
	}
	// 移出分组:不限组时谁能管原组谁就能甩出去;限了组的令牌连这个落点都不该碰。
	if err := svc.CanGroup(ctx, user("owner"), Ungrouped, ActManage); err != nil {
		t.Fatalf("移出分组不该被挡: %v", err)
	}
	if err := svc.CanGroup(ctx, scopedUser("owner", "priv"), Ungrouped, ActManage); !errors.Is(err, ErrForbidden) {
		t.Fatalf("限组令牌甩出分组 err = %v, want ErrForbidden", err)
	}
	// 管理员归属也收得到这条约束。
	if err := svc.CanGroup(ctx, scopedAdmin("pub"), "priv", ActManage); !errors.Is(err, ErrForbidden) {
		t.Fatalf("范围外管理员 err = %v, want ErrForbidden", err)
	}
}
