package target

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"

	"github.com/fuuhoo/pipewright/internal/access"
	"github.com/fuuhoo/pipewright/internal/group"
	"github.com/fuuhoo/pipewright/internal/storetest"
	"github.com/fuuhoo/pipewright/internal/vault"
)

// group_scope_test.go —— 服务器的分组归属与可见范围过滤。
//
// SSH 行为不在这里测(见 target_test.go 的 capturingDialer);本文件只关心
// server_groups 这几条归属边:入库、读回、按分组收敛列表、以及指向不存在分组的引用。

func scopeSvc(t *testing.T, db *sql.DB) Service {
	t.Helper()
	return New(db, vault.New(db, testMasterKey()), &capturingDialer{})
}

func TestServerGroupRoundTrip(t *testing.T) {
	db := storetest.OpenDB(t)
	ctx := context.Background()
	gid := seedGroup(t, db, "运维组", access.VisibilityPrivate)
	credID := newSSHCred(t, vault.New(db, testMasterKey()), leakMarker)
	svc := scopeSvc(t, db)

	s, err := svc.Create(ctx, CreateInput{
		Name: "web-1", Host: "10.0.0.1", Port: 22, User: "deploy", CredentialID: credID, GroupIDs: []string{" " + gid + " "},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// 入参里的空白必须被归一化:带空格的分组 ID 会让 access 侧的等值判定失配。
	if !slices.Equal(s.GroupIDs, []string{gid}) {
		t.Fatalf("GroupIDs = %v, want [%s]", s.GroupIDs, gid)
	}

	// 不存在的分组 → ErrGroupNotFound(server_groups.group_id 无外键,只能在这里挡)。
	if _, err := svc.Create(ctx, CreateInput{
		Name: "ghost", Host: "10.0.0.2", Port: 22, User: "deploy", CredentialID: credID, GroupIDs: []string{"no-such-group"},
	}); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("err = %v, want ErrGroupNotFound", err)
	}

	// 移出分组:nil 不动,空切片才清空。
	if _, err := svc.Update(ctx, s.ID, UpdateInput{Name: ptr("renamed")}); err != nil {
		t.Fatalf("Update name: %v", err)
	}
	if g, _ := svc.Get(ctx, s.ID); !slices.Equal(g.GroupIDs, []string{gid}) {
		t.Fatalf("未传 groupIds 时归属被改坏了: %v", g.GroupIDs)
	}
	if _, err := svc.Update(ctx, s.ID, UpdateInput{GroupIDs: &[]string{}}); err != nil {
		t.Fatalf("Update out: %v", err)
	}
	if g, _ := svc.Get(ctx, s.ID); len(g.GroupIDs) != 0 {
		t.Fatalf("移出后 GroupIDs = %v, want 空", g.GroupIDs)
	}
}

func TestListScoped_FiltersByGroup(t *testing.T) {
	db := storetest.OpenDB(t)
	ctx := context.Background()
	v := vault.New(db, testMasterKey())
	credID := newSSHCred(t, v, leakMarker)
	svc := scopeSvc(t, db)

	gid := seedGroup(t, db, "私有组", access.VisibilityPrivate)
	pubID := seedGroup(t, db, "公开组", access.VisibilityPublic)
	for _, in := range []CreateInput{
		{Name: "free"},
		{Name: "secret", GroupIDs: []string{gid}},
		{Name: "open", GroupIDs: []string{pubID}},
	} {
		in.Host, in.Port, in.User, in.CredentialID = "10.0.0.9", 22, "deploy", credID
		if _, err := svc.Create(ctx, in); err != nil {
			t.Fatalf("create %s: %v", in.Name, err)
		}
	}

	names := func(f ListFilter) map[string]bool {
		t.Helper()
		list, err := svc.ListScoped(ctx, f)
		if err != nil {
			t.Fatalf("ListScoped: %v", err)
		}
		out := map[string]bool{}
		for _, s := range list {
			out[s.Name] = true
		}
		return out
	}

	// 未归组必须在白名单里:它是存量数据的默认态,漏掉就等于把老服务器藏了。
	only := names(ListFilter{Visible: access.ListFilter{GroupIDs: []string{access.Ungrouped, pubID}}})
	if !only["free"] || !only["open"] || only["secret"] {
		t.Fatalf("受限列表 = %v, want free+open", only)
	}
	all := names(ListFilter{Visible: access.ListFilter{Unrestricted: true}})
	if len(all) != 3 {
		t.Fatalf("不受限列表 = %v, want 3 台", all)
	}
	// 零值过滤 = 只见未归组(fail closed,不是「全放行」)。
	if got := names(ListFilter{}); len(got) != 1 || !got["free"] {
		t.Fatalf("零值列表 = %v, want 只剩 free", got)
	}
	// List() 是内部系统视角:部署/代理/异常检测按 ID 用机,不该被分组挡住。
	sys, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sys) != 3 {
		t.Fatalf("List 内部视角 = %d 台, want 3", len(sys))
	}
}

// seedGroup 建一个分组并返回其 ID(组长是一个直接落库的用户)。
func seedGroup(t *testing.T, db *sql.DB, name, visibility string) string {
	t.Helper()
	owner := "owner-" + name
	if _, err := db.Exec(
		`INSERT INTO users (id, username, password_hash, role, enabled, created_at, updated_at)
		 VALUES (?, ?, 'x', 'user', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		owner, owner); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	g, err := group.New(db).Create(context.Background(), group.CreateInput{
		Name: name, OwnerID: owner, Visibility: visibility,
	})
	if err != nil {
		t.Fatalf("seed group: %v", err)
	}
	return g.ID
}

func ptr[T any](v T) *T { return &v }

// TestServerMultiGroup 覆盖一台机器挂多个组:去重排序、读回、整组替换、以及列表按并集命中。
func TestServerMultiGroup(t *testing.T) {
	db := storetest.OpenDB(t)
	ctx := context.Background()
	credID := newSSHCred(t, vault.New(db, testMasterKey()), leakMarker)
	svc := scopeSvc(t, db)

	privID := seedGroup(t, db, "支付组", access.VisibilityPrivate)
	pubID := seedGroup(t, db, "生产组", access.VisibilityPublic)

	// 同一组传两次 + 带空白:入库前归一成一组,不留重复行。
	s, err := svc.Create(ctx, CreateInput{
		Name: "shared", Host: "10.0.0.5", Port: 22, User: "deploy", CredentialID: credID,
		GroupIDs: []string{pubID, " " + privID + " ", pubID},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	want := []string{privID, pubID}
	slices.Sort(want)
	if !slices.Equal(s.GroupIDs, want) {
		t.Fatalf("GroupIDs = %v, want %v(去重且有序)", s.GroupIDs, want)
	}

	// 列表按并集:只授权公开组的人也能看见它(它确实在公开组里)。
	got, err := svc.ListScoped(ctx, ListFilter{Visible: access.ListFilter{GroupIDs: []string{access.Ungrouped, pubID}}})
	if err != nil {
		t.Fatalf("ListScoped: %v", err)
	}
	if len(got) != 1 || got[0].ID != s.ID {
		t.Fatalf("公开组视角 = %v, want 含 shared", got)
	}
	// 与它无关的组看不到。
	otherID := seedGroup(t, db, "研发组", access.VisibilityPrivate)
	got, err = svc.ListScoped(ctx, ListFilter{Visible: access.ListFilter{GroupIDs: []string{otherID}}})
	if err != nil {
		t.Fatalf("ListScoped: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("无关组视角 = %v, want 空", got)
	}

	// 整组替换:新集合生效,旧行不残留。
	if _, err := svc.Update(ctx, s.ID, UpdateInput{GroupIDs: &[]string{otherID}}); err != nil {
		t.Fatalf("Update groups: %v", err)
	}
	if g, _ := svc.Get(ctx, s.ID); !slices.Equal(g.GroupIDs, []string{otherID}) {
		t.Fatalf("替换后 GroupIDs = %v, want [%s]", g.GroupIDs, otherID)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM server_groups WHERE server_id = ?`, s.ID).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if n != 1 {
		t.Fatalf("归属行数 = %d, want 1(旧行必须清掉)", n)
	}

	// 替换时只要有任一组不存在,整单拒绝 —— 不能「能写的写进去、写不了的跳过」。
	if _, err := svc.Update(ctx, s.ID, UpdateInput{GroupIDs: &[]string{otherID, "no-such-group"}}); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("err = %v, want ErrGroupNotFound", err)
	}
	if g, _ := svc.Get(ctx, s.ID); !slices.Equal(g.GroupIDs, []string{otherID}) {
		t.Fatalf("校验失败不该改动归属:%v", g.GroupIDs)
	}

	// 删除服务器时归属行一并清掉,不留孤儿(server_groups 无外键)。
	if err := svc.Delete(ctx, s.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM server_groups WHERE server_id = ?`, s.ID).Scan(&n); err != nil {
		t.Fatalf("count after delete: %v", err)
	}
	if n != 0 {
		t.Fatalf("删除后仍有 %d 行归属", n)
	}
}
