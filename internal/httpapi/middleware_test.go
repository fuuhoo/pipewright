package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huangchengsir/pipewright/internal/auth"
)

// fakeOK 是「下一个 handler 已通过」的哨兵,用于测试中间件是否放行/拒绝。
var fakeOK = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
})

// runMiddleware 跑单个中间件 + fakeOK,返回响应。
func runMiddleware(mw http.Handler, req *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)
	return w
}

// makeReq 构造带指定 method/path 的 *http.Request。
func makeReq(method, path string) *http.Request {
	return httptest.NewRequest(method, path, nil)
}

// withSession 直接构造带 session 的 ctx(绕过 requireAuth),注入到 context。
// 使用与 requireAuth 完全相同的 contextKeySession key(同包访问)。
func withSession(req *http.Request, sess *auth.Session) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), contextKeySession, sess))
}

// TestRequireAdmin_BlocksUser 验证 RequireAdmin 对 user role 拒绝。
func TestRequireAdmin_BlocksUser(t *testing.T) {
	req := makeReq(http.MethodGet, "/api/admin/foo")
	req = withSession(req, &auth.Session{Role: "user", UserID: "u-1"})

	w := runMiddleware(RequireAdmin(fakeOK), req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("user 应被 403, got %d", w.Code)
	}
}

// TestRequireAdmin_AllowsAdmin 验证 RequireAdmin 对 admin role 放行。
func TestRequireAdmin_AllowsAdmin(t *testing.T) {
	req := makeReq(http.MethodGet, "/api/admin/foo")
	req = withSession(req, &auth.Session{Role: "admin", UserID: "00000000-0000-0000-0000-000000000001"})

	w := runMiddleware(RequireAdmin(fakeOK), req)
	if w.Code != http.StatusOK {
		t.Fatalf("admin 应放行, got %d", w.Code)
	}
}

// TestRequireAdmin_AllowsLegacyEmptyRole 验证旧会话(role='')向后兼容通过。
func TestRequireAdmin_AllowsLegacyEmptyRole(t *testing.T) {
	req := makeReq(http.MethodGet, "/api/admin/foo")
	req = withSession(req, &auth.Session{Role: "", UserID: ""})

	w := runMiddleware(RequireAdmin(fakeOK), req)
	if w.Code != http.StatusOK {
		t.Fatalf("旧会话(role='')应放行, got %d", w.Code)
	}
}

// TestRequireAdmin_RejectsNoSession 验证 requireAuth 未跑过的情况下 401。
func TestRequireAdmin_RejectsNoSession(t *testing.T) {
	req := makeReq(http.MethodGet, "/api/admin/foo")

	w := runMiddleware(RequireAdmin(fakeOK), req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("无 session 应 401, got %d", w.Code)
	}
}

// TestRequireUser_AllowsAdmin 验证 admin 可访问普通用户端点。
func TestRequireUser_AllowsAdmin(t *testing.T) {
	req := makeReq(http.MethodGet, "/api/personal/profile")
	req = withSession(req, &auth.Session{Role: "admin", UserID: "00000000-0000-0000-0000-000000000001"})

	w := runMiddleware(RequireUser(fakeOK), req)
	if w.Code != http.StatusOK {
		t.Fatalf("admin 应被 RequireUser 放行, got %d", w.Code)
	}
}

// TestRequireUser_AllowsUser 验证 user 可访问。
func TestRequireUser_AllowsUser(t *testing.T) {
	req := makeReq(http.MethodGet, "/api/personal/profile")
	req = withSession(req, &auth.Session{Role: "user", UserID: "u-1"})

	w := runMiddleware(RequireUser(fakeOK), req)
	if w.Code != http.StatusOK {
		t.Fatalf("user 应被 RequireUser 放行, got %d", w.Code)
	}
}

// TestRequireUser_RejectsLegacyEmptyRole 验证 RequireUser 不接受旧会话(role='')。
//
// 旧会话在 RequireAdmin 通过(向后兼容),但 RequireUser 拒绝(普通用户端点要求明确 role)。
// 旧会话需登出重登一次才能激活 RequireUser。
func TestRequireUser_RejectsLegacyEmptyRole(t *testing.T) {
	req := makeReq(http.MethodGet, "/api/personal/profile")
	req = withSession(req, &auth.Session{Role: "", UserID: ""})

	w := runMiddleware(RequireUser(fakeOK), req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("旧会话(role='')不应通过 RequireUser, got %d", w.Code)
	}
}

// TestRequireUser_RejectsNoSession 验证未登录 → 401。
func TestRequireUser_RejectsNoSession(t *testing.T) {
	req := makeReq(http.MethodGet, "/api/personal/profile")

	w := runMiddleware(RequireUser(fakeOK), req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("无 session 应 401, got %d", w.Code)
	}
}

// TestRequireCSRF_GetExempt 验证 GET 请求豁免 CSRF。
func TestRequireCSRF_GetExempt(t *testing.T) {
	req := makeReq(http.MethodGet, "/api/credentials")
	// GET 豁免 → fakeOK 放行。
	w := runMiddleware(requireCSRF(fakeOK), req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET 应豁免 CSRF, got %d", w.Code)
	}
}

// TestRequireCSRF_PostRequiresHeader 验证 POST 写方法在无 session 时 403。
func TestRequireCSRF_PostRequiresHeader(t *testing.T) {
	req := makeReq(http.MethodPost, "/api/credentials")

	w := runMiddleware(requireCSRF(fakeOK), req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("POST 无 session 应 403, got %d", w.Code)
	}
}

// TestRequireCSRF_PostRequiresMatchingToken 验证 POST 写方法需匹配的 csrf token。
func TestRequireCSRF_PostRequiresMatchingToken(t *testing.T) {
	req := makeReq(http.MethodPost, "/api/credentials")
	req.Header.Set(headerCsrf, "wrong-token")
	req = withSession(req, &auth.Session{Role: "admin", CSRFToken: "right-token"})

	w := runMiddleware(requireCSRF(fakeOK), req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("csrf 不匹配应 403, got %d", w.Code)
	}
}

// TestRequireCSRF_PostMatchingToken 验证 csrf 匹配 → 放行。
func TestRequireCSRF_PostMatchingToken(t *testing.T) {
	req := makeReq(http.MethodPost, "/api/credentials")
	req.Header.Set(headerCsrf, "right-token")
	req = withSession(req, &auth.Session{Role: "admin", CSRFToken: "right-token"})

	w := runMiddleware(requireCSRF(fakeOK), req)
	if w.Code != http.StatusOK {
		t.Fatalf("csrf 匹配应放行, got %d", w.Code)
	}
}
