package group

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/fuuhoo/pipewright/internal/access"
	"github.com/fuuhoo/pipewright/internal/storetest"
	"github.com/fuuhoo/pipewright/internal/users"
	"github.com/fuuhoo/pipewright/internal/vault"
)

// ---- 夹具 -------------------------------------------------------------------

func masterKey() *[32]byte {
	var k [32]byte
	for i := range k {
		k[i] = byte(i + 3)
	}
	return &k
}

// addUser 建一个测试用户,返回 users.id。口令哈希填占位串:本包不校验口令。
func addUser(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	u, err := users.NewService(db).Create(users.CreateInput{
		Username: name, PasswordHash: "not-a-real-hash", Role: users.RoleUser,
	})
	if err != nil {
		t.Fatalf("add user %s: %v", name, err)
	}
	return u.ID
}

// addCred 建一条凭据(projects/servers 的 FK 目标),返回 credential_id。
func addCred(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	c, err := vault.New(db, masterKey()).Create(vault.CreateInput{
		Name: name, Type: vault.TypeGitToken, Secret: "s3cret-value",
	})
	if err != nil {
		t.Fatalf("add cred %s: %v", name, err)
	}
	return c.ID
}

func addProject(t *testing.T, db *sql.DB, id, groupID string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO projects (id, name, repo_url, credential_id, group_id, created_at, updated_at)
		 VALUES (?, ?, 'https://gitee.com/a/b.git', ?, ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		id, "proj-"+id, addCred(t, db, "cred-"+id), groupID); err != nil {
		t.Fatalf("add project: %v", err)
	}
}

func addServer(t *testing.T, db *sql.DB, id, groupID string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO servers (id, name, host, port, user, credential_id, group_id, created_at, updated_at)
		 VALUES (?, ?, '10.0.0.1', 22, 'root', ?, ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		id, "srv-"+id, addCred(t, db, "scred-"+id), groupID); err != nil {
		t.Fatalf("add server: %v", err)
	}
}

func addRun(t *testing.T, db *sql.DB, id, projectID string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO pipeline_runs (id, project_id, status, created_at) VALUES (?, ?, 'success', '2026-01-01T00:00:00Z')`,
		id, projectID); err != nil {
		t.Fatalf("add run: %v", err)
	}
}

func newSvc(t *testing.T) (*Service, *sql.DB) {
	t.Helper()
	db := storetest.OpenDB(t)
	return New(db), db
}

// groupIDs 收集分组列表的 id(断言可见集合时用)。
func groupIDs(list []Group) map[string]bool {
	out := map[string]bool{}
	for _, g := range list {
		out[g.ID] = true
	}
	return out
}

// ---- CRUD 校验 --------------------------------------------------------------

func TestCreateDefaultsToPrivate(t *testing.T) {
	svc, db := newSvc(t)
	ctx := context.Background()
	owner := addUser(t, db, "alice")

	g, err := svc.Create(ctx, CreateInput{Name: "  平台组  ", OwnerID: owner})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if g.Name != "平台组" {
		t.Fatalf("name 未 trim: %q", g.Name)
	}
	// 默认私有:公开是显式决定,漏传不该把资源摊给全员。
	if g.Visibility != access.VisibilityPrivate {
		t.Fatalf("visibility = %q, want private", g.Visibility)
	}
	if g.OwnerName != "alice" {
		t.Fatalf("ownerName = %q, want alice", g.OwnerName)
	}
}

func TestCreateRejectsBadInput(t *testing.T) {
	svc, db := newSvc(t)
	ctx := context.Background()
	owner := addUser(t, db, "bob")

	if _, err := svc.Create(ctx, CreateInput{Name: "   ", OwnerID: owner}); !errors.Is(err, ErrEmptyName) {
		t.Fatalf("err = %v, want ErrEmptyName", err)
	}
	if _, err := svc.Create(ctx, CreateInput{Name: "a", OwnerID: ""}); !errors.Is(err, ErrOwnerRequired) {
		t.Fatalf("err = %v, want ErrOwnerRequired", err)
	}
	if _, err := svc.Create(ctx, CreateInput{Name: "a", OwnerID: "no-such-user"}); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("err = %v, want ErrUserNotFound", err)
	}
	if _, err := svc.Create(ctx, CreateInput{Name: "a", OwnerID: owner, Visibility: "internal"}); !errors.Is(err, ErrInvalidVisibility) {
		t.Fatalf("err = %v, want ErrInvalidVisibility", err)
	}
	if _, err := svc.Create(ctx, CreateInput{Name: "dup", OwnerID: owner}); err != nil {
		t.Fatalf("Create dup: %v", err)
	}
	if _, err := svc.Create(ctx, CreateInput{Name: "dup", OwnerID: owner}); !errors.Is(err, ErrDuplicateName) {
		t.Fatalf("err = %v, want ErrDuplicateName", err)
	}
}

func TestMembersExcludeOwnerAndDedupe(t *testing.T) {
	svc, db := newSvc(t)
	ctx := context.Background()
	owner := addUser(t, db, "carol")
	m1 := addUser(t, db, "dave")
	m2 := addUser(t, db, "erin")

	g, err := svc.Create(ctx, CreateInput{
		Name: "core", OwnerID: owner, MemberIDs: []string{m1, m1, owner, "  ", m2},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(g.MemberIDs) != 2 {
		t.Fatalf("members = %v, want 去重且不含组长的 2 人", g.MemberIDs)
	}
	for _, id := range g.MemberIDs {
		if id == owner {
			t.Fatalf("组长不应出现在名册里(两处真相)")
		}
	}
	// 幂等:重复 AddMember 不该报错也不该产生第二行。
	if err := svc.AddMember(ctx, g.ID, m1, owner); err != nil {
		t.Fatalf("AddMember 重复: %v", err)
	}
	fresh, err := svc.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(fresh.MemberIDs) != 2 {
		t.Fatalf("重复 AddMember 后名册 = %v, want 仍 2 人", fresh.MemberIDs)
	}
	if err := svc.AddMember(ctx, g.ID, owner, owner); err != nil {
		t.Fatalf("AddMember 组长应静默通过: %v", err)
	}
	if err := svc.AddMember(ctx, g.ID, "ghost", owner); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("err = %v, want ErrUserNotFound", err)
	}
	if err := svc.RemoveMember(ctx, g.ID, "不存在的成员"); err != nil {
		t.Fatalf("RemoveMember 幂等失败: %v", err)
	}
}

func TestUpdateAndCounts(t *testing.T) {
	svc, db := newSvc(t)
	ctx := context.Background()
	owner := addUser(t, db, "frank")
	g, err := svc.Create(ctx, CreateInput{Name: "team", OwnerID: owner})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	addProject(t, db, "p1", g.ID)
	addServer(t, db, "s1", g.ID)

	pub := access.VisibilityPublic
	updated, err := svc.Update(ctx, g.ID, UpdateInput{Visibility: &pub})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Visibility != access.VisibilityPublic {
		t.Fatalf("visibility = %q", updated.Visibility)
	}
	if updated.ProjectCount != 1 || updated.ServerCount != 1 {
		t.Fatalf("counts = %d/%d, want 1/1", updated.ProjectCount, updated.ServerCount)
	}

	// 空名/非法可见性/悬挂组长一律拒绝,且原值不改。
	empty := "  "
	if _, err := svc.Update(ctx, g.ID, UpdateInput{Name: &empty}); !errors.Is(err, ErrEmptyName) {
		t.Fatalf("err = %v, want ErrEmptyName", err)
	}
	bad := "somewhat"
	if _, err := svc.Update(ctx, g.ID, UpdateInput{Visibility: &bad}); !errors.Is(err, ErrInvalidVisibility) {
		t.Fatalf("err = %v, want ErrInvalidVisibility", err)
	}
	// 未传 OwnerID 时组长保持不变。
	if got, _ := svc.Get(ctx, g.ID); got.OwnerID != owner {
		t.Fatalf("未传 ownerId 不该改组长")
	}
	if _, err := svc.Update(ctx, "no-such-group", UpdateInput{Visibility: &pub}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestDeleteRequiresEmptyGroup(t *testing.T) {
	svc, db := newSvc(t)
	ctx := context.Background()
	owner := addUser(t, db, "grace")
	g, err := svc.Create(ctx, CreateInput{Name: "del", OwnerID: owner, MemberIDs: []string{addUser(t, db, "heidi")}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	addProject(t, db, "p-keep", g.ID)
	if err := svc.Delete(ctx, g.ID); !errors.Is(err, ErrGroupNotEmpty) {
		t.Fatalf("err = %v, want ErrGroupNotEmpty", err)
	}
	if _, err := db.Exec(`DELETE FROM projects WHERE id = 'p-keep'`); err != nil {
		t.Fatalf("clean project: %v", err)
	}
	if err := svc.Delete(ctx, g.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// 名册随组级联清掉(删组前手工删,别依赖 FK 行为)。
	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM resource_group_members WHERE group_id = ?`, g.ID).Scan(&n); err != nil {
		t.Fatalf("count members: %v", err)
	}
	if n != 0 {
		t.Fatalf("残留名册 %d 行", n)
	}
	if err := svc.Delete(ctx, g.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复删 err = %v, want ErrNotFound", err)
	}
}

// ---- 可见性列表 -------------------------------------------------------------

func TestListVisibleScopesByRole(t *testing.T) {
	svc, db := newSvc(t)
	ctx := context.Background()
	alice := addUser(t, db, "alice2")
	bob := addUser(t, db, "bob2")
	carol := addUser(t, db, "carol2")

	pub, err := svc.Create(ctx, CreateInput{Name: "public-ish", OwnerID: alice, Visibility: access.VisibilityPublic})
	if err != nil {
		t.Fatalf("Create pub: %v", err)
	}
	owned, err := svc.Create(ctx, CreateInput{Name: "mine", OwnerID: bob})
	if err != nil {
		t.Fatalf("Create owned: %v", err)
	}
	joined, err := svc.Create(ctx, CreateInput{Name: "joined", OwnerID: alice, MemberIDs: []string{carol}})
	if err != nil {
		t.Fatalf("Create joined: %v", err)
	}
	hidden, err := svc.Create(ctx, CreateInput{Name: "hidden", OwnerID: alice})
	if err != nil {
		t.Fatalf("Create hidden: %v", err)
	}

	// carol:公开组 + 被邀的组;看不到别人的私有组。
	visible, err := svc.ListVisible(ctx, carol)
	if err != nil {
		t.Fatalf("ListVisible: %v", err)
	}
	got := groupIDs(visible)
	for _, want := range []string{pub.ID, joined.ID} {
		if !got[want] {
			t.Fatalf("carol 应看到 %s,得到 %v", want, got)
		}
	}
	for _, no := range []string{owned.ID, hidden.ID} {
		if got[no] {
			t.Fatalf("carol 不该看到私有组 %s", no)
		}
	}

	// bob 是 owned 的组长:组长不靠名册也有可见性。
	visible, _ = svc.ListVisible(ctx, bob)
	if !groupIDs(visible)[owned.ID] {
		t.Fatalf("组长应看到自己的组")
	}

	// List 不过滤(admin 视图)。
	all, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("List = %d 组, want 4", len(all))
	}
}

// ---- access.Repository ------------------------------------------------------

func TestGroupIDOfResolvesEveryKind(t *testing.T) {
	svc, db := newSvc(t)
	ctx := context.Background()
	owner := addUser(t, db, "dave2")
	g, err := svc.Create(ctx, CreateInput{Name: "repo", OwnerID: owner})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	addProject(t, db, "p-run", g.ID)
	addServer(t, db, "s-run", g.ID)
	addRun(t, db, "r-run", "p-run")
	addProject(t, db, "p-free", "")

	for _, tc := range []struct {
		name string
		kind access.Kind
		id   string
		want string
	}{
		{"project in group", access.KindProject, "p-run", g.ID},
		{"project ungrouped", access.KindProject, "p-free", access.Ungrouped},
		{"server", access.KindServer, "s-run", g.ID},
		// 运行没有自己的分组:归属经 run → project 两跳,和「谁能看这次运行」同义。
		{"run inherits project", access.KindRun, "r-run", g.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := svc.GroupIDOf(ctx, tc.kind, tc.id)
			if err != nil {
				t.Fatalf("GroupIDOf: %v", err)
			}
			if got != tc.want {
				t.Fatalf("groupID = %q, want %q", got, tc.want)
			}
		})
	}

	// 资源不存在 → ErrNotFound(映射 404),绝不能是 ErrForbidden(伪装成无权限)。
	if _, err := svc.GroupIDOf(ctx, access.KindProject, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if _, err := svc.GroupIDOf(ctx, access.KindRun, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if _, err := svc.GroupIDOf(ctx, access.Kind("bogus"), "x"); err == nil {
		t.Fatalf("未知 kind 应报错")
	}
}

func TestGroupSnapshotStates(t *testing.T) {
	svc, db := newSvc(t)
	ctx := context.Background()
	owner := addUser(t, db, "erin2")
	m := addUser(t, db, "fred2")
	g, err := svc.Create(ctx, CreateInput{
		Name: "snap", OwnerID: owner, Visibility: access.VisibilityPrivate, MemberIDs: []string{m},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 未归组 → nil 快照(Decide 当作公开)。
	snap, err := svc.GroupSnapshot(ctx, access.Ungrouped)
	if err != nil || snap != nil {
		t.Fatalf("ungrouped snapshot = %+v, %v; want nil, nil", snap, err)
	}

	snap, err = svc.GroupSnapshot(ctx, g.ID)
	if err != nil {
		t.Fatalf("GroupSnapshot: %v", err)
	}
	if snap.Visibility != access.VisibilityPrivate || snap.OwnerID != owner || len(snap.MemberIDs) != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}

	// 悬挂引用(组已删但资源还指着它)→ ErrGroupNotFound,由 access 侧按最私有一档收敛。
	name := "later"
	other, err := svc.Create(ctx, CreateInput{Name: name, OwnerID: owner})
	if err != nil {
		t.Fatalf("Create other: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM resource_groups WHERE id = ?`, other.ID); err != nil {
		t.Fatalf("hard delete: %v", err)
	}
	if _, err := svc.GroupSnapshot(ctx, other.ID); !errors.Is(err, access.ErrGroupNotFound) {
		t.Fatalf("err = %v, want access.ErrGroupNotFound", err)
	}
}

func TestPublicAndJoinedGroupIDs(t *testing.T) {
	svc, db := newSvc(t)
	ctx := context.Background()
	addUser(t, db, "alice3") // 无关第三人:证明可见性收敛不是「大家都被算进名册」的假象
	bob := addUser(t, db, "bob3")
	carol := addUser(t, db, "carol3")

	pub, _ := svc.Create(ctx, CreateInput{Name: "p1", OwnerID: bob, Visibility: access.VisibilityPublic})
	priv, _ := svc.Create(ctx, CreateInput{Name: "p2", OwnerID: bob})
	joined, _ := svc.Create(ctx, CreateInput{Name: "p3", OwnerID: bob, MemberIDs: []string{carol}})

	ids, err := svc.PublicAndJoinedGroupIDs(ctx, carol)
	if err != nil {
		t.Fatalf("PublicAndJoinedGroupIDs: %v", err)
	}
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	if !set[pub.ID] || !set[joined.ID] {
		t.Fatalf("ids = %v, want 含 public 与被邀组", ids)
	}
	if set[priv.ID] {
		t.Fatalf("ids 不该含未加入的私有组")
	}
	// 返回值刻意不含 '':未归组由 access.Service 固定补进白名单,这里重复会掩盖 bug。
	for _, id := range ids {
		if id == access.Ungrouped {
			t.Fatalf("ids 不该含未归组标记")
		}
	}
}

// 编译期断言:实现体没跑偏。
var _ access.Repository = (*Service)(nil)
