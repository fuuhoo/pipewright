// Package httpapi — 中间件层(v6.2 §5.9 + 阶段 8)。
//
// 阶段 8 把 requireAuth / requireCSRF 从 router.go 抽出到本文件;新增 RequireAdmin
// / RequireUser / RequireAuth(命名包装器)三个 RBAC 中间件,在阶段 9 的
// /api/admin/* 子组接入。
//
// 中间件栈约定(chi):
//
//	Use(requireAuth(svc))    → 必须最先,注入 *auth.Session 到 ctx
//	Use(requireCSRF)         → 必须 requireAuth 之后;写方法比对 csrf
//	Use(RequireAdmin)        → 必须 requireAuth 之后;非 admin → 403
//	Use(RequireUser)         → 必须 requireAuth 之后;未登录 → 401
//
// 为何 RequireAdmin/RequireUser 不内联 requireAuth:router.New 的 Option 模式让
// 新子组(/api/admin/* 与 /api/personal/*)可共用同一 svc,避免重复装配。
package httpapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/fuuhoo/pipewright/internal/access"
	"github.com/fuuhoo/pipewright/internal/apitoken"
	"github.com/fuuhoo/pipewright/internal/auth"
)

// requireAuth 中间件:校验会话 cookie → 注入 Session 到 context;未过 → 401 JSON。
//
// 阶段 8 抽出:原位于 router.go。
//
// 两条凭据通路,按请求头分流,互不回落:
//   - `Authorization: Bearer <API 令牌>` → 走 tokens(机器客户端:MCP、脚本)。
//     校验不过直接 401,**绝不退回试 cookie** —— 同一请求同时带两种凭据时若允许回落,
//     一个能诱导管理员发请求的页面就能借她的会话冒充「令牌持有者」做事(confused deputy)。
//   - 其余(带会话 cookie 的浏览器)→ 走 svc.Verify。
//
// tokens 为 nil(未装配令牌子系统)时,带 Bearer 的请求一律 401;不带 Bearer 的行为不变。
func requireAuth(svc auth.Authenticator, tokens bearerVerifier, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if plain, ok := bearerToken(r); ok {
			if tokens == nil {
				writeError(w, http.StatusUnauthorized, "unauthorized", "API 令牌未启用")
				return
			}
			p, err := tokens.Verify(r.Context(), plain)
			if err != nil {
				// 领域侧把「不存在 / 已撤销 / 归属人被禁用」抹平成同一个 ErrInvalid,
				// 这里也一律 401:无效令牌换不到任何关于原因的信息。
				writeError(w, http.StatusUnauthorized, "invalid_api_token", "API 令牌无效或已撤销")
				return
			}
			// 令牌会话不落 sessions 表:每次调用现查 users,权限改动即时生效,撤销也没有残留。
			sess := &auth.Session{UserID: p.UserID, Role: p.Role, Username: p.Username}
			ctx := context.WithValue(r.Context(), contextKeySession, sess)
			ctx = context.WithValue(ctx, contextKeyAPIToken, p.TokenID)
			// 端点的分组范围跟着这把令牌走,不是跟着归属人走:同一个人用限过组的令牌
			// 进来,能碰的资源就只剩那几个(空范围不必注入,「没有值」就是不限)。
			if len(p.GroupScope) > 0 {
				ctx = context.WithValue(ctx, contextKeyTokenGroups, p.GroupScope)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
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

// bearerVerifier 校验 API 令牌明文并取回归属人。由 *apitoken.Service 实现。
type bearerVerifier interface {
	Verify(ctx context.Context, plaintext string) (*apitoken.Principal, error)
}

// bearerForOption 把 Option 里的具体类型转成中间件要的接口值。
//
// 必须经这一手:nil 的 *apitoken.Service 直接赋给 bearerVerifier 得到的是「非 nil 接口 + nil 指针」,
// requireAuth 里 `tokens == nil` 那道降级判据就形同虚设,带 Bearer 的请求会在解引用处 panic 成 500。
func bearerForOption(svc *apitoken.Service) bearerVerifier {
	if svc == nil {
		return nil
	}
	return svc
}

// bearerScheme 是 Authorization 头里令牌凭据的scheme前缀。
const bearerScheme = "Bearer "

// bearerToken 取请求头里的 Bearer 凭据。第二个返回值为 false 表示这不是令牌请求。
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, bearerScheme) {
		return "", false
	}
	return strings.TrimSpace(h[len(bearerScheme):]), true
}

// apiTokenIDFromRequest 返回本次请求所用的 API 令牌 id;非令牌请求为空串。
//
// 令牌请求没有对应的 sessions 行,凡是拿 sess.Token 当身份用的端点(如 OAuth 绑定键)
// 必须先用它挡下来,否则会写出一个「谁都对不上」的绑定。
func apiTokenIDFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	id, _ := r.Context().Value(contextKeyAPIToken).(string)
	return id
}

// requireBrowserSession 挡下「用 API 令牌调用必须依赖浏览器会话的端点」。
//
// OAuth 授权流把 state 与会话 token 绑死(防 CSRF),而令牌请求根本没有 sessions 行:
// 放行只会签发一个绑定键为空、回调时谁也认不回来的 state,不如当场讲清楚该怎么做。
func requireBrowserSession(w http.ResponseWriter, r *http.Request) bool {
	if apiTokenIDFromRequest(r) != "" {
		writeError(w, http.StatusUnprocessableEntity, "browser_session_required",
			"该操作涉及浏览器跳转,需要用登录会话而不是 API 令牌")
		return false
	}
	return true
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
// API 令牌请求整体豁免:CSRF 防的是「浏览器被诱导时自动带上已有凭据」(cookie),而
// Authorization 头不会由浏览器跨站自动附带 —— 令牌要么调用方自己带,要么根本没有,
// 威胁模型不成立。更何况 MCP 这类客户端拿不到签发在 cookie 里的 CSRF token,不豁免就等于
// 把所有写端点锁死成只能浏览器用。
//
// 阶段 8 抽出:原位于 router.go。
func requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if apiTokenIDFromRequest(r) != "" {
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

// RequireAdmin 中间件:要求角色持有「进得了设置那一组」的功能点;否则 403。
//
// 阶段 8 新增:用于 /api/admin/* 子组(阶段 9 接入)。
//
// 为什么读 access.SettingsAllowed 而不是 Session.IsAdmin:左栏入口、路由落点与这层服务端门
// 必须出自同一张表(internal/access/perms.go)。今天两者取值完全等价(settings.access 只授予
// 管理员),但两套判据并排写就会飘 —— 某天给某个新角色开了设置点,页面进得去而端点全 403。
//
// 行为:
//   - 无 session(未过 requireAuth)→ 401("请先登录")——避免泄露路由存在性。
//   - 已登录但无设置点(user / developer / ops / viewer,以及认不出的角色名)→ 403("forbidden")。
//   - 旧会话(role=="")→ 放行(向后兼容旧部署;与 Session.IsAdmin 把 "" 视为 admin 同口径)。
//
// 该函数设计为 http.Handler 形式以直接 chi.Use 挂载;依赖 requireAuth 已先注入 session。
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := sessionFromContext(r.Context())
		if !ok || sess == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
			return
		}
		if !access.SettingsAllowed(sess.Role) {
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
