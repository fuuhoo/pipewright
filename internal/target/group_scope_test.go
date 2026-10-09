package target

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/fuuhoo/pipewright/internal/access"
	"github.com/fuuhoo/pipewright/internal/group"
	"github.com/fuuhoo/pipewright/internal/storetest"
	"github.com/fuuhoo/pipewright/internal/vault"
)

// group_scope_test.go —— 服务器的分组归属与可见范围过滤。
//
// SSH 行为不在这里测(见 target_test.go 的 capturingDialer);本文件只关心
// group_id 这条归属边:入库、读回、按分组收敛列表、以及指向不存在分组的引用。

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
		Name: "web-1", Host: "10.0.0.1", Port: 22, User: "deploy", CredentialID: credID, GroupID: " " + gid + " ",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// 入参里的空白必须被归一化:带空格的 group_id 会让 access 侧的等值判定失配。
	if s.GroupID != gid {
		t.Fatalf("GroupID = %q, want %q", s.GroupID, gid)
	}

	// 不存在的分组 → ErrGroupNotFound(servers.group_id 无外键,只能在这里挡)。
	if _, err := svc.Create(ctx, CreateInput{
		Name: "ghost", Host: "10.0.0.2", Port: 22, User: "deploy", CredentialID: credID, GroupID: "no-such-group",
	}); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("err = %v, want ErrGroupNotFound", err)
	}

	// 移出分组:nil 不动,空串才清空。
	if _, err := svc.Update(ctx, s.ID, UpdateInput{Name: ptr("renamed")}); err != nil {
		t.Fatalf("Update name: %v", err)
	}
	if g, _ := svc.Get(ctx, s.ID); g.GroupID != gid {
		t.Fatalf("未传 groupId 时归属被改坏了: %q", g.GroupID)
	}
	if _, err := svc.Update(ctx, s.ID, UpdateInput{GroupID: ptr("")}); err != nil {
		t.Fatalf("Update out: %v", err)
	}
	if g, _ := svc.Get(ctx, s.ID); g.GroupID != access.Ungrouped {
		t.Fatalf("移出后 GroupID = %q, want 空串", g.GroupID)
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
		{Name: "free", GroupID: ""},
		{Name: "secret", GroupID: gid},
		{Name: "open", GroupID: pubID},
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
