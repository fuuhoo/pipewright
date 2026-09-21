package users_test

import (
	"context"
	"testing"
	"time"

	"github.com/huangchengsir/pipewright/internal/auth"
	"github.com/huangchengsir/pipewright/internal/storetest"
	"github.com/huangchengsir/pipewright/internal/users"
)

const testAdminPwd = "testpass_12345"

// TestAuthUsersSync_Bootstrap_Login_ChangePassword 验证 auth ↔ users 同步:
//   1) Bootstrap 创建 admin_user 行 → 同时 users 表有 admin 行
//   2) Login 后 users.last_login_at 被更新
//   3) ChangePassword 后 users.password_hash 同步
func TestAuthUsersSync_Bootstrap_Login_ChangePassword(t *testing.T) {
	db := storetest.OpenDB(t)
	usersSvc := users.NewService(db)
	authSvc := auth.NewService(db, nil, usersSvc)

	// 1. Bootstrap
	if err := authSvc.Bootstrap("admin", testAdminPwd); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	// 验证 admin_user 有 id=1 行
	var adminPwdHash string
	if err := db.QueryRow(`SELECT password_hash FROM admin_user WHERE id = 1`).Scan(&adminPwdHash); err != nil {
		t.Fatalf("admin_user.id=1 缺: %v", err)
	}

	// 验证 users 表有 role='admin' 行(同步生效)
	u, err := usersSvc.GetByUsername("admin")
	if err != nil {
		t.Fatalf("users.GetByUsername: %v", err)
	}
	if u.PasswordHash != adminPwdHash {
		t.Fatalf("hash 未同步: users=%q admin_user=%q", u.PasswordHash, adminPwdHash)
	}
	if u.LastLoginAt != nil {
		t.Fatalf("Bootstrap 不该写 last_login_at: %v", u.LastLoginAt)
	}

	// 2. Login
	beforeLogin := time.Now().UTC().Add(-time.Second)
	sess, err := authSvc.Login("admin", testAdminPwd)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if sess == nil {
		t.Fatal("session is nil")
	}

	// 验证 last_login_at 已写入
	u2, err := usersSvc.GetByUsername("admin")
	if err != nil {
		t.Fatalf("get2: %v", err)
	}
	if u2.LastLoginAt == nil {
		t.Fatal("Login 后 last_login_at 应被写入")
	}
	if u2.LastLoginAt.Before(beforeLogin) {
		t.Fatalf("last_login_at 异常早: %v", u2.LastLoginAt)
	}

	// 3. ChangePassword
	newPwd := "newpass_67890"
	if err := authSvc.ChangePassword(testAdminPwd, newPwd, sess.Token); err != nil {
		t.Fatalf("change: %v", err)
	}

	// 验证 admin_user.password_hash 已更新
	if err := db.QueryRow(`SELECT password_hash FROM admin_user WHERE id = 1`).Scan(&adminPwdHash); err != nil {
		t.Fatalf("re-query: %v", err)
	}
	// 验证 users.password_hash 已同步
	u3, err := usersSvc.GetByUsername("admin")
	if err != nil {
		t.Fatalf("get3: %v", err)
	}
	if u3.PasswordHash != adminPwdHash {
		t.Fatalf("ChangePassword 后 hash 未同步: users=%q admin_user=%q", u3.PasswordHash, adminPwdHash)
	}

	// 4. 新口令能登录
	if _, err := authSvc.Login("admin", newPwd); err != nil {
		t.Fatalf("login with new pwd: %v", err)
	}
}

// TestAuthUsersSync_Bootstrap_NoPwd 验证无 password 时不创建任何 user。
func TestAuthUsersSync_Bootstrap_NoPwd(t *testing.T) {
	db := storetest.OpenDB(t)
	usersSvc := users.NewService(db)
	authSvc := auth.NewService(db, nil, usersSvc)

	if err := authSvc.Bootstrap("admin", ""); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	// admin_user 表为空
	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM admin_user`).Scan(&n); err != nil {
		t.Fatalf("count admin_user: %v", err)
	}
	if n != 0 {
		t.Fatalf("无 password 时不应创建 admin_user 行: got %d", n)
	}

	// users 表也为空
	if err := db.QueryRow(`SELECT COUNT(1) FROM users`).Scan(&n); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if n != 0 {
		t.Fatalf("无 password 时不应创建 users 行: got %d", n)
	}
	_ = context.Background()
}

// TestAuthUsersSync_BootstrapIdempotent 验证重启时(users 表已有 admin 行)Bootstrap 不重复创建。
func TestAuthUsersSync_BootstrapIdempotent(t *testing.T) {
	db := storetest.OpenDB(t)
	usersSvc := users.NewService(db)

	// 模拟已有 admin_user 行但 users 表为空(理论不可能,但测边界)
	if _, err := db.Exec(
		`INSERT INTO admin_user (id, username, password_hash, created_at, updated_at)
		 VALUES (1, 'admin', 'hash_v1', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("seed admin_user: %v", err)
	}

	authSvc := auth.NewService(db, nil, usersSvc)
	if err := authSvc.Bootstrap("admin", "ignored"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	// users 表应有 1 行 admin,hash 与 admin_user.id=1 一致
	u, err := usersSvc.GetByUsername("admin")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if u.PasswordHash != "hash_v1" {
		t.Fatalf("hash 不一致: got %q want hash_v1", u.PasswordHash)
	}
}