package auth

import (
	"errors"
	"testing"

	"github.com/huangchengsir/pipewright/internal/storetest"
	"github.com/huangchengsir/pipewright/internal/users"
)

// [R15] 首次启动 Bootstrap:admin_user 空 + 提供口令 → 建 admin,同步 users role='admin' 行。
func TestVerify_R15_BootstrapSyncsToUsers(t *testing.T) {
	db := storetest.OpenDB(t)
	us := users.NewService(db)
	svc := NewService(db, nil, us)

	// Bootstrap 前:admin_user 与 users 均空
	var n int
	_ = db.QueryRow(`SELECT COUNT(1) FROM admin_user`).Scan(&n)
	if n != 0 {
		t.Fatalf("空库应无 admin, got %d", n)
	}
	_ = db.QueryRow(`SELECT COUNT(1) FROM users`).Scan(&n)
	if n != 0 {
		t.Fatalf("空库应无 users, got %d", n)
	}

	if err := svc.Bootstrap("admin", "testpass1234"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	// admin_user.id=1 行
	_ = db.QueryRow(`SELECT COUNT(1) FROM admin_user`).Scan(&n)
	if n != 1 {
		t.Fatalf("bootstrap 后 admin_user 应 1 行, got %d", n)
	}
	// users.role='admin' 行 + 固定 UUID
	u, err := us.GetByID(users.BootstrapAdminRegularUserID)
	if err != nil {
		t.Fatalf("users 应有 admin 行: %v", err)
	}
	if u.Role != users.RoleAdmin {
		t.Fatalf("role = %q want admin", u.Role)
	}
	if u.Username != "admin" {
		t.Fatalf("username = %q want admin", u.Username)
	}
	if !u.Enabled {
		t.Fatal("admin 行应 enabled")
	}
	// users.password_hash 与 admin_user 一致(双向同步)
	var ah string
	_ = db.QueryRow(`SELECT password_hash FROM admin_user WHERE id=1`).Scan(&ah)
	iu, err := us.GetByUsername("admin")
	mustV(t, err, "get by username")
	if ah == "" || iu.PasswordHash != ah {
		t.Fatal("users.admin.password_hash 应与 admin_user 同步")
	}
}

// [R15] 第二次启动(Bootstrap 幂等):不重复插 admin。
func TestVerify_R15_BootstrapIdempotent(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := NewService(db, nil, users.NewService(db))
	mustV(t, svc.Bootstrap("admin", "testpass1234"), "b1")
	mustV(t, svc.Bootstrap("admin", "someother"), "b2")
	var n int
	_ = db.QueryRow(`SELECT COUNT(1) FROM admin_user`).Scan(&n)
	if n != 1 {
		t.Fatalf("重复 bootstrap 不应新增 admin, got %d", n)
	}
	_ = db.QueryRow(`SELECT COUNT(1) FROM users`).Scan(&n)
	if n != 1 {
		t.Fatalf("userSyncer 幂等(ON CONFLICT), got %d", n)
	}
}

// [R16] 管理员改口令 → users.role='admin' 行 hash 同步 + 其它会话失效。
func TestVerify_R16_ChangePasswordSyncsAndRevokesOthers(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := NewService(db, nil, users.NewService(db))
	mustV(t, svc.Bootstrap("admin", "testpass1234"), "b")

	// 建两个会话:current + other
	cur, err := svc.Login("admin", "testpass1234")
	mustV(t, err, "login1")
	other, err := svc.Login("admin", "testpass1234")
	mustV(t, err, "login2")

	mustV(t, svc.ChangePassword("testpass1234", "newpassword999", cur.Token), "change")
	// other 会话应被删
	if _, err := svc.Verify(other.Token); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("改口令后 other 会话应失效, got %v", err)
	}
	// users 表 hash 同步
	iu, err := users.NewService(db).GetByUsername("admin")
	mustV(t, err, "get user")
	var ah string
	_ = db.QueryRow(`SELECT password_hash FROM admin_user WHERE id=1`).Scan(&ah)
	if iu.PasswordHash != ah {
		t.Fatal("ChangePassword 应同步 users.password_hash")
	}
	// 新口令可登录
	if _, err := svc.Login("admin", "newpassword999"); err != nil {
		t.Fatalf("新口令登录失败: %v", err)
	}
}

// [R15] 普通用户通过 users 表登录;会话带 UserID + Role='user'。
func TestVerify_R15_RegularUserLoginViaUsers(t *testing.T) {
	db := storetest.OpenDB(t)
	us := users.NewService(db)
	svc := NewService(db, nil, us)
	mustV(t, svc.Bootstrap("admin", "testpass1234"), "b")

	hash, err := HashPassword("alice-pass-1234")
	mustV(t, err, "hash")
	// 直接插 users 行(邀请注册流程是后续 story)
	_, err = db.Exec(`INSERT INTO users (id, username, password_hash, role, enabled, created_at, updated_at)
	                  VALUES ('u-alice','alice',?,'user',1,'2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`, hash)
	mustV(t, err, "seed alice")

	sess, err := svc.Login("alice", "alice-pass-1234")
	mustV(t, err, "login alice")
	if sess.UserID != "u-alice" {
		t.Fatalf("Session.UserID = %q want u-alice", sess.UserID)
	}
	if sess.Role != "user" {
		t.Fatalf("Session.Role = %q want user", sess.Role)
	}
	if sess.IsAdmin() {
		t.Fatal("普通用户会话不应是 admin")
	}
	// 验证 Verify 读回 role
	got, err := svc.Verify(sess.Token)
	mustV(t, err, "verify")
	if got.Role != "user" || got.UserID != "u-alice" {
		t.Fatalf("Verify 读回 user_id/role 失真: %+v", got)
	}
	// 管理员用户名不能被普通用户账号冒用(用户名不存在 → 401)
	if _, err := svc.Login("nobody", "x"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("不存在用户应 401, got %v", err)
	}
}

// [R15] 禁用用户(enabled=0)不可登录。
func TestVerify_R15_DisabledUserCannotLogin(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := NewService(db, nil, users.NewService(db))
	mustV(t, svc.Bootstrap("admin", "testpass1234"), "b")
	hash, _ := HashPassword("bob-pass-1234")
	_, _ = db.Exec(`INSERT INTO users (id, username, password_hash, role, enabled, created_at, updated_at)
	                VALUES ('u-bob','bob',?,'user',0,'2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`, hash)
	if _, err := svc.Login("bob", "bob-pass-1234"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("禁用用户应 401, got %v", err)
	}
}

// [§5.9] 旧会话(role='')向后兼容:IsAdmin()=true(RequireAdmin 放行)。
func TestVerify_LegacySessionBackwardCompatible(t *testing.T) {
	legacy := &Session{Role: "", UserID: ""}
	if !legacy.IsAdmin() {
		t.Fatal("旧会话(role='')应兼容按 admin 放行")
	}
	if IsUserSession(legacy) {
		t.Fatal("RequireUser 应拒旧会话(需明确 role)")
	}
}

// [§3.5] P1 不变式:BootstrapAdminRegularUserID 永不变更。
func TestVerify_BootstrapAdminIDInvariant(t *testing.T) {
	if users.BootstrapAdminRegularUserID != "00000000-0000-0000-0000-000000000001" {
		t.Fatalf("invariant 被改: %q", users.BootstrapAdminRegularUserID)
	}
}

func mustV(t *testing.T, err error, step string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", step, err)
	}
}
