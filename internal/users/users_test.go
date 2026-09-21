package users_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/huangchengsir/pipewright/internal/storetest"
	"github.com/huangchengsir/pipewright/internal/users"
)

const (
	dummyHash = "$argon2id$v=19$m=65536,t=1,p=4$WyWRpzFnab6vlgFLECjL9g$ZJhWU5lKWEo1T7NhxBxQf70Gk+We+sUQ8SO52b8nnxQ"
)

// TestBootstrapAdminRow_Basic 验证首次 Bootstrap 行为。
func TestBootstrapAdminRow_Basic(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := users.NewService(db)
	ctx := context.Background()

	if err := svc.BootstrapAdminRow("admin", dummyHash); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	u, err := svc.GetByUsername("admin")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if u.ID != users.BootstrapAdminRegularUserID {
		t.Fatalf("ID 错误: got %q want %q", u.ID, users.BootstrapAdminRegularUserID)
	}
	if u.Role != users.RoleAdmin {
		t.Fatalf("role 错误: got %q want %q", u.Role, users.RoleAdmin)
	}
	if !u.Enabled {
		t.Fatalf("enabled 应为 true")
	}
	if u.PasswordHash != dummyHash {
		t.Fatalf("hash 未持久化: got %q", u.PasswordHash)
	}

	// created_at 与 updated_at 应当非常接近(都是 now)
	now := time.Now().UTC()
	if u.CreatedAt.IsZero() || u.UpdatedAt.IsZero() {
		t.Fatalf("created/updated_at 为零: %+v", u)
	}
	if abs(now.Sub(u.CreatedAt)) > time.Minute {
		t.Fatalf("created_at 异常: %v", u.CreatedAt)
	}
	_ = ctx
}

func abs(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// TestBootstrapAdminRow_Idempotent 多次 Bootstrap 必须保持单行 + 不变 id。
func TestBootstrapAdminRow_Idempotent(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := users.NewService(db)

	if err := svc.BootstrapAdminRow("admin", dummyHash); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := svc.BootstrapAdminRow("admin", dummyHash); err != nil {
		t.Fatalf("second: %v", err)
	}
	if err := svc.BootstrapAdminRow("admin", dummyHash+"x"); err != nil {
		t.Fatalf("third: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM users WHERE role = ?`, users.RoleAdmin).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("重复 Bootstrap 应保持 1 行 admin,got %d", n)
	}

	u, err := svc.GetByUsername("admin")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if u.PasswordHash != dummyHash+"x" {
		t.Fatalf("hash 未被第三轮覆盖: got %q", u.PasswordHash)
	}
}

// TestSyncAdminPasswordChange_UpdatesHash 验证同步逻辑。
func TestSyncAdminPasswordChange_UpdatesHash(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := users.NewService(db)

	if err := svc.BootstrapAdminRow("admin", dummyHash); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	newHash := dummyHash + "_new"
	if err := svc.SyncAdminPasswordChange("admin", newHash); err != nil {
		t.Fatalf("sync: %v", err)
	}

	u, err := svc.GetByUsername("admin")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if u.PasswordHash != newHash {
		t.Fatalf("hash 未同步: got %q", u.PasswordHash)
	}
}

// TestSyncAdminPasswordChange_NoAdminRow 验证未 Bootstrap 时返回 ErrNotFound。
func TestSyncAdminPasswordChange_NoAdminRow(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := users.NewService(db)

	err := svc.SyncAdminPasswordChange("admin", dummyHash)
	if !errors.Is(err, users.ErrNotFound) {
		t.Fatalf("预期 ErrNotFound,got %v", err)
	}
}

// TestGetByUsername_NotFound 验证 ErrNotFound 路径。
func TestGetByUsername_NotFound(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := users.NewService(db)

	_, err := svc.GetByUsername("ghost")
	if !errors.Is(err, users.ErrNotFound) {
		t.Fatalf("预期 ErrNotFound,got %v", err)
	}
}

// TestGetByID_NotFound 验证 GetByID 找不到。
func TestGetByID_NotFound(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := users.NewService(db)

	_, err := svc.GetByID("00000000-0000-0000-0000-000000000000")
	if !errors.Is(err, users.ErrNotFound) {
		t.Fatalf("预期 ErrNotFound,got %v", err)
	}
}