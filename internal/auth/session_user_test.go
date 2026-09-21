package auth

import (
	"errors"
	"testing"

	"github.com/huangchengsir/pipewright/internal/storetest"
	"github.com/huangchengsir/pipewright/internal/users"
)

// TestSessionStore_BindUserAndRole 验证 v6.2 阶段 6 引入的 user_id / role 字段
// 能正确写入与读出。
func TestSessionStore_BindUserAndRole(t *testing.T) {
	ss := NewSessionStore(storetest.OpenDB(t))

	sess, err := ss.Create(users.BootstrapAdminRegularUserID, "admin")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if sess.UserID != users.BootstrapAdminRegularUserID {
		t.Fatalf("Session.UserID = %q, want %q", sess.UserID, users.BootstrapAdminRegularUserID)
	}
	if sess.Role != "admin" {
		t.Fatalf("Session.Role = %q, want admin", sess.Role)
	}

	got, err := ss.Get(sess.Token)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.UserID != users.BootstrapAdminRegularUserID || got.Role != "admin" {
		t.Fatalf("回读 user_id/role 失真: got=%+v", got)
	}
}

// TestSessionStore_BackwardCompatibleEmptyUserAndRole 验证旧会话行 user_id/role
// 为空(0053 迁移前的数据形态)能正确读出且 IsAdmin() 返回 true(兼容)。
//
// 通过直接 INSERT 一行 user_id=''/role='' 的会话行模拟旧数据。
func TestSessionStore_BackwardCompatibleEmptyUserAndRole(t *testing.T) {
	db := storetest.OpenDB(t)
	ss := NewSessionStore(db)

	const (
		oldToken    = "old-session-token-0000000000000000"
		oldCSRF     = "old-csrf-0000000000000000000000000"
		createdAt   = "2020-01-01T00:00:00Z"
		expiresAt   = "2099-01-01T00:00:00Z"
		lastSeenAt  = "2020-01-01T00:00:00Z"
	)
	if _, err := db.Exec(
		`INSERT INTO sessions (token, csrf_token, user_id, role, created_at, expires_at, last_seen_at)
		 VALUES (?, ?, '', '', ?, ?, ?)`,
		oldToken, oldCSRF, createdAt, expiresAt, lastSeenAt,
	); err != nil {
		t.Fatalf("seed 旧会话: %v", err)
	}

	got, err := ss.Get(oldToken)
	if err != nil {
		t.Fatalf("get 旧会话: %v", err)
	}
	if got.UserID != "" || got.Role != "" {
		t.Fatalf("旧会话 user_id/role 应为空, got=%+v", got)
	}
	if !got.IsAdmin() {
		t.Fatalf("旧会话(role='')IsAdmin 应为 true(向后兼容)")
	}
}

// TestIsAdminSession_AllRoles 覆盖 IsAdminSession 的 5 个分支。
func TestIsAdminSession_AllRoles(t *testing.T) {
	if IsAdminSession(nil) {
		t.Fatal("nil session 不应为 admin")
	}
	if !IsAdminSession(&Session{Role: ""}) {
		t.Fatal("role='' 应兼容按 admin 放行")
	}
	if !IsAdminSession(&Session{Role: "admin"}) {
		t.Fatal("role=admin 应为 admin")
	}
	if IsAdminSession(&Session{Role: "user"}) {
		t.Fatal("role=user 不应为 admin")
	}
}

// TestIsUserSession_AllRoles 覆盖 IsUserSession 的 4 个分支。
func TestIsUserSession_AllRoles(t *testing.T) {
	if IsUserSession(nil) {
		t.Fatal("nil 不应通过")
	}
	if IsUserSession(&Session{Role: ""}) {
		t.Fatal("role='' 不应通过(RequireUser 要求明确 role)")
	}
	if !IsUserSession(&Session{Role: "admin"}) {
		t.Fatal("admin 应通过 RequireUser(admin 可访问普通用户端点)")
	}
	if !IsUserSession(&Session{Role: "user"}) {
		t.Fatal("user 应通过 RequireUser")
	}
}

// TestLogin_AsRegularUser 验证普通用户走 users 表登录路径:
//   - admin_user 表无此用户名(或 username 不匹配)
//   - users 表命中 + enabled=1 → 创建 Session{UserID, Role="user"}
//   - 口令错 → ErrInvalidCredentials
func TestLogin_AsRegularUser(t *testing.T) {
	// 重要:OpenDB(t) 每次调用返回新建 db,所有共享 db 的对象必须用同一份。
	db := storetest.OpenDB(t)
	us := users.NewService(db)
	svc := NewService(db, nil, us)

	// 构造普通用户。passwordHash 直接用 HashPassword 算。
	const username = "alice"
	hash, err := HashPassword("alice-pass-1234")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO users (id, username, password_hash, role, enabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 1, ?, ?)`,
		"11111111-1111-1111-1111-111111111111",
		username, hash, "user",
		"2024-01-01T00:00:00Z", "2024-01-01T00:00:00Z",
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	// 用户名错 → ErrInvalidCredentials
	if _, err := svc.Login("nobody", "alice-pass-1234"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("用户不存在应 ErrInvalidCredentials, got %v", err)
	}
	// 口令错 → ErrInvalidCredentials
	if _, err := svc.Login(username, "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("口令错应 ErrInvalidCredentials, got %v", err)
	}
	// 正确口令 → 拿到 Session{UserID, Role="user"}
	sess, err := svc.Login(username, "alice-pass-1234")
	if err != nil {
		t.Fatalf("登录失败: %v", err)
	}
	if sess.UserID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("Session.UserID = %q", sess.UserID)
	}
	if sess.Role != "user" {
		t.Fatalf("Session.Role = %q want=user", sess.Role)
	}
}

// TestLogin_DisabledUser 验证 enabled=0 的用户无法登录。
func TestLogin_DisabledUser(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := NewService(db, nil, users.NewService(db))

	hash, _ := HashPassword("alice-pass-1234")
	if _, err := db.Exec(
		`INSERT INTO users (id, username, password_hash, role, enabled, created_at, updated_at)
		 VALUES (?, 'bob', ?, 'user', 0, ?, ?)`,
		"22222222-2222-2222-2222-222222222222",
		hash, "2024-01-01T00:00:00Z", "2024-01-01T00:00:00Z",
	); err != nil {
		t.Fatalf("seed disabled user: %v", err)
	}

	_, err := svc.Login("bob", "alice-pass-1234")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("禁用用户应 ErrInvalidCredentials, got %v", err)
	}
}
