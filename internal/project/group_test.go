package project

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/huangchengsir/pipewright/internal/access"
	"github.com/huangchengsir/pipewright/internal/vault"
)

// group_test.go —— v6.2 分组权限落在项目领域的三件事:
//   - 归组要认得真实分组(不建外键,故领域层自己查);
//   - 列表按可见分组收敛,且 total 与 items 同口径;
//   - 移出分组(groupID='')不该被当成「未传、保持原值」。

// addGroupRow 直插一行 resource_groups(本包不 import group,避免成环)。
func addGroupRow(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO resource_groups (id, name, description, visibility, owner_id, created_by, created_at, updated_at)
		 VALUES (?, ?, '', 'private', 'owner-1', 'owner-1', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		id, "grp-"+id); err != nil {
		t.Fatalf("add group %s: %v", id, err)
	}
}

func TestCreateValidatesGroupID(t *testing.T) {
	db := testDB(t)
	v := vault.New(db, testMasterKey())
	svc := New(db, v, &stubProber{branch: "main"})
	credID := newCred(t, v, "tok")
	addGroupRow(t, db, "g-1")
	ctx := context.Background()

	if _, err := svc.Create(ctx, CreateInput{
		Name: "x", RepoURL: "https://g/a.git", CredentialID: credID, GroupID: "no-such-group",
	}); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("err = %v, want ErrGroupNotFound", err)
	}
	p, err := svc.Create(ctx, CreateInput{
		Name: "x", RepoURL: "https://g/a.git", CredentialID: credID, GroupID: " g-1 ",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p.GroupID != "g-1" {
		t.Fatalf("groupID = %q, want g-1(trim 后)", p.GroupID)
	}
}

func TestUpdateSetAndClearGroup(t *testing.T) {
	db := testDB(t)
	v := vault.New(db, testMasterKey())
	svc := New(db, v, &stubProber{branch: "main"})
	credID := newCred(t, v, "tok")
	addGroupRow(t, db, "g-2")
	ctx := context.Background()

	p, _ := svc.Create(ctx, CreateInput{Name: "x", RepoURL: "https://g/a.git", CredentialID: credID})
	if p.GroupID != "" {
		t.Fatalf("新建未传分组应为未归组, got %q", p.GroupID)
	}

	// 指针为 nil = 不动归属(普通改名不该把项目甩出分组)。
	name := "renamed"
	if got, err := svc.Update(ctx, p.ID, UpdateInput{Name: &name}); err != nil || got.GroupID != "" {
		t.Fatalf("nil 指针应保持归属: %+v %v", got, err)
	}
	gid := "g-2"
	if got, err := svc.Update(ctx, p.ID, UpdateInput{GroupID: &gid}); err != nil || got.GroupID != "g-2" {
		t.Fatalf("归组失败: %+v %v", got, err)
	}
	if _, err := svc.Update(ctx, p.ID, UpdateInput{GroupID: strPtr("ghost")}); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("err = %v, want ErrGroupNotFound", err)
	}
	// 显式空串 = 移出分组,必须区别于 nil。
	if got, err := svc.Update(ctx, p.ID, UpdateInput{GroupID: strPtr("")}); err != nil || got.GroupID != "" {
		t.Fatalf("移出分组失败: %+v %v", got, err)
	}
}

func strPtr(s string) *string { return &s }

// projectNames 把列表转成名字集合,失败信息比逐下标断言好读。
func projectNames(list []Project) map[string]bool {
	out := map[string]bool{}
	for _, p := range list {
		out[p.Name] = true
	}
	return out
}

func TestListHonoursVisibleGroups(t *testing.T) {
	db := testDB(t)
	v := vault.New(db, testMasterKey())
	svc := New(db, v, &stubProber{branch: "main"})
	credID := newCred(t, v, "tok")
	addGroupRow(t, db, "ga")
	addGroupRow(t, db, "gb")
	ctx := context.Background()

	// 三条:未归组一条、ga 一条、gb 一条。
	if _, err := svc.Create(ctx, CreateInput{Name: "free", RepoURL: "https://g/free.git", CredentialID: credID}); err != nil {
		t.Fatalf("Create free: %v", err)
	}
	if _, err := svc.Create(ctx, CreateInput{Name: "in-a", RepoURL: "https://g/a.git", CredentialID: credID, GroupID: "ga"}); err != nil {
		t.Fatalf("Create in-a: %v", err)
	}
	if _, err := svc.Create(ctx, CreateInput{Name: "in-b", RepoURL: "https://g/b.git", CredentialID: credID, GroupID: "gb"}); err != nil {
		t.Fatalf("Create in-b: %v", err)
	}

	// 管理员:不受限,三条全见。
	all, err := svc.List(ctx, access.ListFilter{Unrestricted: true})
	if err != nil {
		t.Fatalf("List unrestricted: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("unrestricted = %d 条, want 3", len(all))
	}

	// 组长/成员:未归组 + 自己那一个组。
	got, err := svc.List(ctx, access.ListFilter{GroupIDs: []string{access.Ungrouped, "ga"}})
	if err != nil {
		t.Fatalf("List scoped: %v", err)
	}
	if names := projectNames(got); len(names) != 2 || !names["free"] || !names["in-a"] {
		t.Fatalf("scoped = %v, want free + in-a", names)
	}

	// 分页的 total 必须与 items 同口径,否则前端会按全量算页数。
	res, err := svc.ListPaged(ctx, 1, 10, access.ListFilter{GroupIDs: []string{access.Ungrouped, "gb"}})
	if err != nil {
		t.Fatalf("ListPaged: %v", err)
	}
	if res.Total != 2 || len(res.Items) != 2 {
		t.Fatalf("total/len = %d/%d, want 2/2", res.Total, len(res.Items))
	}

	// 零值 ListFilter = 「只见未归组」,不是「不受限」——这条最容易写反。
	zero, err := svc.List(ctx, access.ListFilter{})
	if err != nil {
		t.Fatalf("List zero: %v", err)
	}
	if len(zero) != 1 || zero[0].Name != "free" {
		t.Fatalf("zero = %+v, want 仅 free", zero)
	}
}
