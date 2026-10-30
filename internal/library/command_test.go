package library

import (
	"context"
	"errors"
	"testing"
)

// TestCommandCRUD 覆盖指令的建/列/取/改/删全链路,正文换行必须原样保留。
func TestCommandCRUD(t *testing.T) {
	db := testDB(t)
	svc := NewCommandService(db)
	ctx := context.Background()

	created, err := svc.Create(ctx, CommandInput{
		Name:        "重启后端",
		Description: "systemd 重启 + 探活",
		Body:        "systemctl restart app\ncurl -fsS http://127.0.0.1:8080/healthz",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("Create: empty id")
	}
	if created.Body != "systemctl restart app\ncurl -fsS http://127.0.0.1:8080/healthz" {
		t.Fatalf("Create: body not preserved: %q", created.Body)
	}

	list, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Name != "重启后端" {
		t.Fatalf("List: unexpected %#v", list)
	}

	updated, err := svc.Update(ctx, created.ID, CommandInput{
		Name: "重启后端 v2",
		Body: "systemctl restart app",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "重启后端 v2" || updated.Body != "systemctl restart app" {
		t.Fatalf("Update: unexpected %#v", updated)
	}

	if err := svc.Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Get(ctx, created.ID); !errors.Is(err, ErrCommandNotFound) {
		t.Fatalf("Get after delete: want ErrCommandNotFound, got %v", err)
	}
}

// TestCommandValidation 覆盖名称/正文空校验与重名冲突。
func TestCommandValidation(t *testing.T) {
	db := testDB(t)
	svc := NewCommandService(db)
	ctx := context.Background()

	if _, err := svc.Create(ctx, CommandInput{Name: " ", Body: "echo hi"}); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("empty name: want ErrInvalidCommand, got %v", err)
	}
	if _, err := svc.Create(ctx, CommandInput{Name: "x", Body: "  \n\t"}); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("blank body: want ErrInvalidCommand, got %v", err)
	}

	if _, err := svc.Create(ctx, CommandInput{Name: "dup", Body: "echo hi"}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := svc.Create(ctx, CommandInput{Name: "dup", Body: "echo hi"}); !errors.Is(err, ErrCommandNameTaken) {
		t.Fatalf("dup name: want ErrCommandNameTaken, got %v", err)
	}
}

// TestCommandUpdateNotFound 校验更新不存在指令报 404,并拒绝空正文的更新。
func TestCommandUpdateNotFound(t *testing.T) {
	db := testDB(t)
	svc := NewCommandService(db)
	ctx := context.Background()

	if _, err := svc.Update(ctx, "missing", CommandInput{Name: "x", Body: "echo hi"}); !errors.Is(err, ErrCommandNotFound) {
		t.Fatalf("Update missing: want ErrCommandNotFound, got %v", err)
	}

	created, err := svc.Create(ctx, CommandInput{Name: "keep", Body: "echo hi"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Update(ctx, created.ID, CommandInput{Name: "keep", Body: ""}); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("blank body update: want ErrInvalidCommand, got %v", err)
	}
	if err := svc.Delete(ctx, "missing"); !errors.Is(err, ErrCommandNotFound) {
		t.Fatalf("Delete missing: want ErrCommandNotFound, got %v", err)
	}
}
