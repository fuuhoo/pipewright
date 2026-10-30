package servicecatalog

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/fuuhoo/pipewright/internal/storetest"
	"github.com/fuuhoo/pipewright/internal/vault"
)

// addProject 插一条项目行;credential_id 是外键,得先有一条真凭据。
func addProject(t *testing.T, db *sql.DB, id, serviceID string) {
	t.Helper()
	var key [32]byte
	for i := range key {
		key[i] = byte(i + 3)
	}
	cred, err := vault.New(db, &key).Create(vault.CreateInput{
		Name: "cred-" + id, Type: vault.TypeGitToken, Secret: "s3cret-value",
	})
	if err != nil {
		t.Fatalf("add credential: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO projects (id, name, repo_url, credential_id, service_id, created_at, updated_at)
		 VALUES (?, ?, '', ?, ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		id, "proj-"+id, cred.ID, serviceID); err != nil {
		t.Fatalf("add project: %v", err)
	}
}

func TestCreateListAndCount(t *testing.T) {
	db := storetest.Open(t).DB
	ctx := context.Background()
	c := New(db)

	b, err := c.Create(ctx, CreateInput{Name: "订单中心", Description: "order"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	a, err := c.Create(ctx, CreateInput{Name: "支付网关"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	addProject(t, db, "p1", b.ID)
	addProject(t, db, "p2", b.ID)
	addProject(t, db, "p3", "")

	list, err := c.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 services, got %d", len(list))
	}
	// 按名字升序:支付网关 / 订单中心(字节序)。
	if list[0].ID != a.ID || list[1].ID != b.ID {
		t.Fatalf("unexpected order: %s, %s", list[0].Name, list[1].Name)
	}
	if list[1].ProjectCount != 2 {
		t.Fatalf("want 2 projects on %s, got %d", list[1].Name, list[1].ProjectCount)
	}
	if list[0].ProjectCount != 0 {
		t.Fatalf("want 0 projects on %s, got %d", list[0].Name, list[0].ProjectCount)
	}
}

func TestCreateValidation(t *testing.T) {
	db := storetest.Open(t).DB
	ctx := context.Background()
	c := New(db)

	if _, err := c.Create(ctx, CreateInput{Name: "  "}); !errors.Is(err, ErrEmptyName) {
		t.Fatalf("want ErrEmptyName, got %v", err)
	}
	if _, err := c.Create(ctx, CreateInput{Name: "结算"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := c.Create(ctx, CreateInput{Name: " 结算 "}); !errors.Is(err, ErrDuplicateName) {
		t.Fatalf("want ErrDuplicateName, got %v", err)
	}
}

func TestUpdateRenameAndDuplicate(t *testing.T) {
	db := storetest.Open(t).DB
	ctx := context.Background()
	c := New(db)

	a, _ := c.Create(ctx, CreateInput{Name: "A"})
	b, _ := c.Create(ctx, CreateInput{Name: "B"})

	name := "A2"
	got, err := c.Update(ctx, a.ID, UpdateInput{Name: &name})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if got.Name != "A2" {
		t.Fatalf("want A2, got %s", got.Name)
	}
	if _, err := c.Update(ctx, b.ID, UpdateInput{Name: &name}); !errors.Is(err, ErrDuplicateName) {
		t.Fatalf("want ErrDuplicateName, got %v", err)
	}
	empty := "  "
	if _, err := c.Update(ctx, b.ID, UpdateInput{Name: &empty}); !errors.Is(err, ErrEmptyName) {
		t.Fatalf("want ErrEmptyName, got %v", err)
	}
	if _, err := c.Update(ctx, "nope", UpdateInput{Name: &name}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestDeleteRefusesWhileInUse(t *testing.T) {
	db := storetest.Open(t).DB
	ctx := context.Background()
	c := New(db)

	svc, _ := c.Create(ctx, CreateInput{Name: "结算"})
	addProject(t, db, "p1", svc.ID)

	if err := c.Delete(ctx, svc.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("want ErrInUse, got %v", err)
	}
	if _, err := db.Exec(`UPDATE projects SET service_id = '' WHERE id = 'p1'`); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if err := c.Delete(ctx, svc.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := c.Get(ctx, svc.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound after delete, got %v", err)
	}
}
