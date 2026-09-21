// Package httpapi — 中间件层(v6.2 §5.9 + 阶段 8)。
//
// 阶段 8 把 requireAuth / requireCSRF 从 router.go 抽出到本文件;新增 RequireAdmin
// / RequireUser / RequireAuth(命名包装器)三个 RBAC 中间件,在阶段 9 的
// /api/admin/* 子组接入。
//
// 中间件栈约定(chi):
//   Use(requireAuth(svc))    → 必须最先,注入 *auth.Session 到 ctx
//   Use(requireCSRF)         → 必须 requireAuth 之后;写方法比对 csrf
//   Use(RequireAdmin)        → 必须 requireAuth 之后;非 admin → 403
//   Use(RequireUser)         → 必须 requireAuth 之后;未登录 → 401
//
// 为何 RequireAdmin/RequireUser 不内联 requireAuth:router.New 的 Option 模式让
// 新子组(/api/admin/* 与 /api/personal/*)可共用同一 svc,避免重复装配。
package httpapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"

	"github.com/huangchengsir/pipewright/internal/auth"
)

// requireAuth 中间件:校验会话 cookie → 注入 Session 到 context;未过 → 401 JSON。
//
// 阶段 8 抽出:原位于 router.go。
func requireAuth(svc auth.Authenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
			return
		}
		cookie, err := r.Cookie(cookieSession)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
			return
		}
		sess, err := svc.Verify(cookie.Value)
		if err != nil {
			if errors.Is(err, auth.ErrSessionNotFound) {
				writeError(w, http.StatusUnauthorized, "unauthorized", "会话已过期,请重新登录")
				return
			}
			// 其它错误(如 DB 故障)不应被当作「会话过期」掩盖。
			writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
			return
		}
		ctx := context.WithValue(r.Context(), contextKeySession, sess)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// sessionFromContext 从已认证的 context 取 Session。
func sessionFromContext(ctx context.Context) (*auth.Session, bool) {
	sess, ok := ctx.Value(contextKeySession).(*auth.Session)
	return sess, ok
}

// requireCSRF 中间件:写方法需 X-CSRF-Token header == session.CSRFToken。
//
// GET/HEAD/OPTIONS 豁免;未注入 session → 403。
//
// 阶段 8 抽出:原位于 router.go。
func requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		sess, ok := sessionFromContext(r.Context())
		if !ok || sess == nil {
			// 未经 requireAuth 注入 Session:无法校验 CSRF。
			writeError(w, http.StatusForbidden, "csrf_invalid", "CSRF token 缺失或不匹配")
			return
		}
		headerVal := r.Header.Get(headerCsrf)
		if headerVal == "" || subtle.ConstantTimeCompare([]byte(headerVal), []byte(sess.CSRFToken)) != 1 {
			writeError(w, http.StatusForbidden, "csrf_invalid", "CSRF token 缺失或不匹配")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAdmin 中间件:要求 Session.IsAdmin() == true;否则 403。
//
// 阶段 8 新增:用于 /api/admin/* 子组(阶段 9 接入)。
//
// 行为:
//   - 无 session(未过 requireAuth)→ 401("请先登录")——避免泄露路由存在性。
//   - 已登录但非 admin(role=="user")→ 403("forbidden")。
//   - 旧会话(role=='')→ 放行(向后兼容旧部署;Session.IsAdmin 把 '' 视为 admin)。
//
// 该函数设计为 http.Handler 形式以直接 chi.Use 挂载;依赖 requireAuth 已先注入 session。
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := sessionFromContext(r.Context())
		if !ok || sess == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
			return
		}
		if !auth.IsAdminSession(sess) {
			writeError(w, http.StatusForbidden, "forbidden", "需要管理员权限")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireUser 中间件:要求 Session 已登录(admin 或 user);未登录 → 401。
//
// 阶段 8 新增:用于 /api/personal/* 与 /api/build-envs/languages 等普通用户可访问
// 端点(阶段 9 接入)。admin 也视为合法用户。
func RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := sessionFromContext(r.Context())
		if !ok || sess == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
			return
		}
		if !auth.IsUserSession(sess) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "会话无效")
			return
		}
		next.ServeHTTP(w, r)
	})
}
