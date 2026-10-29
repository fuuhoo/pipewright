package auth

import (
	"errors"
	"testing"

	"github.com/fuuhoo/pipewright/internal/storetest"
	"github.com/fuuhoo/pipewright/internal/users"
)

// TestSetInitialPassword_OAuthAccount 验证「首次设置口令」：
//   - 占位口令账号(OAuth 首登自动建号)无需当前口令即可设第一个口令，设完能用新口令登录
//   - 第二次调用被拒(已有本地口令 → ErrPasswordAlreadySet)，防止绕过当前口令校验
//   - 弱口令在先被拒
func TestSetInitialPassword_OAuthAccount(t *testing.T) {
	db := storetest.OpenDB(t)
	us := users.NewService(db)
	svc := NewService(db, nil, us)

	placeholder, err := HashPassword("nobody-knows-this")
	if err != nil {
		t.Fatalf("hash placeholder: %v", err)
	}
	u, err := us.Create(users.CreateInput{
		Username: "oidcuser", PasswordHash: placeholder, PlaceholderPassword: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sess, err := svc.sessions.Create(u.ID, "user")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := svc.SetInitialPassword(u.ID, "short", sess.Token); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("weak password: err = %v, want ErrWeakPassword", err)
	}
	if err := svc.SetInitialPassword(u.ID, "newpass1234", sess.Token); err != nil {
		t.Fatalf("set initial password: %v", err)
	}

	// 设完之后用户名+口令可登录。
	if _, err := svc.Login("oidcuser", "newpass1234"); err != nil {
		t.Fatalf("login with new password: %v", err)
	}
	got, err := us.GetByID(u.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if !got.HasLocalPassword {
		t.Fatalf("设置成功后 has_local_password 应为 true")
	}

	// 再走一次「首次设置」= 绕过当前口令改密，必须拒绝。
	if err := svc.SetInitialPassword(u.ID, "another1234", sess.Token); !errors.Is(err, ErrPasswordAlreadySet) {
		t.Fatalf("second initial set: err = %v, want ErrPasswordAlreadySet", err)
	}
}

// TestSetInitialPassword_DisabledAndMissing 禁用账号与不存在的账号都按 ErrUserNotFound 处理。
func TestSetInitialPassword_DisabledAndMissing(t *testing.T) {
	db := storetest.OpenDB(t)
	us := users.NewService(db)
	svc := NewService(db, nil, us)

	u, err := us.Create(users.CreateInput{
		Username: "disabledone", PasswordHash: mustHash(t, "whatever1234"), PlaceholderPassword: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sess, err := svc.sessions.Create(u.ID, "user")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := us.SetEnabled(u.ID, false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if err := svc.SetInitialPassword(u.ID, "newpass1234", sess.Token); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("disabled: err = %v, want ErrUserNotFound", err)
	}
	if err := svc.SetInitialPassword("nope", "newpass1234", sess.Token); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("missing: err = %v, want ErrUserNotFound", err)
	}
}

func mustHash(t *testing.T, pw string) string {
	t.Helper()
	h, err := HashPassword(pw)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	return h
}
