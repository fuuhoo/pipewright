package users

import (
	"testing"

	"github.com/huangchengsir/pipewright/internal/storetest"
)

// 回归:`GetByID` 曾因 scanView 复用 scanInternal(9 列)而永远失败
// (scan: expected 9 destination, not 8)。锁住修复。
func TestRegression_GetByIDWorks(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := NewService(db)
	if err := svc.BootstrapAdminRow("admin", "phc_hash_value"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	u, err := svc.GetByID(BootstrapAdminRegularUserID)
	if err != nil {
		t.Fatalf("GetByID 应成功: %v", err)
	}
	if u.Username != "admin" || u.Role != RoleAdmin || !u.Enabled {
		t.Fatalf("字段错: %+v", u)
	}
	if u.CreatedAt.IsZero() || u.UpdatedAt.IsZero() {
		t.Fatal("CreatedAt/UpdatedAt 应被解析")
	}
	// 不存在 → ErrNotFound
	if _, err := svc.GetByID("no-such-id"); err == nil {
		t.Fatal("不存在应报错")
	}
}
