package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/mask"
)

// fakeAuthenticator 实现 auth.Authenticator,用于测试 actorFromSession 的 username 派生。
type fakeAuthenticator struct {
	username string
	err      error
}

func (f *fakeAuthenticator) Login(username, password string) (*auth.Session, error) {
	return nil, errors.New("unused")
}
func (f *fakeAuthenticator) Verify(token string) (*auth.Session, error) {
	return nil, errors.New("unused")
}
func (f *fakeAuthenticator) Logout(token string) error      { return nil }
func (f *fakeAuthenticator) AdminUsername() (string, error) { return f.username, f.err }

// withSessionContext 把 *auth.Session 注入到 ctx,模拟 requireAuth 后的请求上下文。
func withSessionContext(sess *auth.Session) context.Context {
	return context.WithValue(context.Background(), contextKeySession, sess)
}

func TestActorFromSession_NoSession(t *testing.T) {
	if got := actorFromSession(context.Background(), &fakeAuthenticator{username: "admin"}); got != auditActorFallback {
		t.Fatalf("无 session 应回退到 %q, got %q", auditActorFallback, got)
	}
}

func TestActorFromSession_AdminWithUsername(t *testing.T) {
	ctx := withSessionContext(&auth.Session{Role: "admin", UserID: "uid-admin"})
	got := actorFromSession(ctx, &fakeAuthenticator{username: "admin"})
	if got != "admin:admin" {
		t.Fatalf("admin 路径派生 = %q, want admin:admin", got)
	}
}

func TestActorFromSession_AdminUsernameFallback(t *testing.T) {
	// ac 返回 error → fallback 到 "admin"
	ctx := withSessionContext(&auth.Session{Role: "admin", UserID: "uid-admin"})
	got := actorFromSession(ctx, &fakeAuthenticator{err: errors.New("db down")})
	if got != "admin" {
		t.Fatalf("AdminUsername 失败应 fallback 到 admin, got %q", got)
	}
}

func TestActorFromSession_LegacySessionEmptyRole(t *testing.T) {
	// 旧会话 row(role='')→ IsAdmin 兼容,Actor 走 admin:<username>
	ctx := withSessionContext(&auth.Session{Role: "", UserID: ""})
	got := actorFromSession(ctx, &fakeAuthenticator{username: "admin"})
	if got != "admin:admin" {
		t.Fatalf("旧会话(role='')应派生 admin:<username>, got %q", got)
	}
}

func TestActorFromSession_UserRole(t *testing.T) {
	ctx := withSessionContext(&auth.Session{Role: "user", UserID: "user-uuid-1"})
	got := actorFromSession(ctx, nil)
	if got != "user:user-uuid-1" {
		t.Fatalf("普通 user 派生 = %q, want user:user-uuid-1", got)
	}
}

// 自定义角色的 Session.Role 是 roles.id(uuid)。把它当 actor 落库等于没记(认不出人),
// 所以按「登录名 → UserID」派生,只有两者都空(旧会话)才回落角色串。
func TestActorFromSession_CustomRoleUsesUsername(t *testing.T) {
	ctx := withSessionContext(&auth.Session{Role: "7ded3a10-3a5b-4c2e-9a1f-0b7c1d2e3f44", UserID: "u-2", Username: "alice"})
	got := actorFromSession(ctx, nil)
	if got != "user:alice" {
		t.Fatalf("自定义角色应派生 user:<登录名>, got %q", got)
	}
}

func TestActorFromSession_CustomRoleNoUsernameFallsBackToUserID(t *testing.T) {
	ctx := withSessionContext(&auth.Session{Role: "7ded3a10-3a5b-4c2e-9a1f-0b7c1d2e3f44", UserID: "u-2"})
	got := actorFromSession(ctx, nil)
	if got != "user:u-2" {
		t.Fatalf("无登录名时应回落 user:<UserID>, got %q", got)
	}
}

func TestActorFromSession_UnknownRole(t *testing.T) {
	// 登录名与 UserID 皆空(极旧会话)才原样返回角色串。
	ctx := withSessionContext(&auth.Session{Role: "strange-role"})
	got := actorFromSession(ctx, nil)
	if got != "strange-role" {
		t.Fatalf("未知 role 应原样返回, got %q", got)
	}
}

// TestRecordAuditFromRequest_Integration 端到端:写审计后,Entry.Actor 由 session 派生。
// 通过真实 HTTP server 走 recordAuditFromRequest 路径,确认 actor 字段最终入库为派生值。
func TestRecordAuditFromRequest_Integration(t *testing.T) {
	st := testStoreAuth(t)
	svc := auth.NewService(st.DB, nil, nil)
	if err := svc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	rec := audit.New(st.DB, mask.NewMasker(), nil)

	// 伪造带 admin session 的请求(不走 HTTP,直接调用 recordAuditFromRequest)。
	r := httptest.NewRequest(http.MethodPost, "/api/credentials", nil)
	ctx := context.WithValue(r.Context(), contextKeySession, &auth.Session{Role: "admin", UserID: "00000000-0000-0000-0000-000000000001"})
	r = r.WithContext(ctx)
	recordAuditFromRequest(r, rec, svc, audit.Entry{
		Action:     "credential_create",
		TargetType: "credential",
		TargetID:   "test-id",
	})

	res, err := rec.List(context.Background(), audit.ListFilter{})
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(res.Entries) != 1 {
		t.Fatalf("应有 1 条审计, got %d", len(res.Entries))
	}
	if res.Entries[0].Actor != "admin:admin" {
		t.Fatalf("Actor = %q, want admin:admin", res.Entries[0].Actor)
	}
}
