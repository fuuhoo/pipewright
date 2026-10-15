// Package httpapi — 「用 git 平台账号登录本平台」的 HTTP 层。
//
// 三条流、两套回调地址(与 internal/oauth/login.go 的分工一致):
//
//	登录(匿名)   GET /api/auth/oauth/providers            公开:登录页出哪些入口
//	              GET /api/auth/oauth/{provider}/authorize 公开:发 state + 写 bind cookie → 302
//	              GET /api/auth/oauth/{provider}/callback  公开:验 state → 认人 → 签会话 → 302
//	绑定(已登录) GET /api/oauth/{provider}/bind            认证:发 IntentBind 的 state → 302
//	                       (回调复用 /api/oauth/{provider}/callback,按 state 意图分流)
//	管理         GET/PUT /api/admin/oauth/login-apps*      管理员:配登录应用 + 域名白名单
//	自查         GET/DELETE /api/account/identities*       本人:列/解绑自己的外部身份
//
// 登录流为什么用「浏览器 nonce cookie」当 bindKey:匿名访客没有会话,而 state 必须绑到
// **发起者**而不是只躺在服务端内存里等匹配(Jpom 正是缺这一条,任何人拿到别人回调里的
// code+state 都能登进别人账号)。nonce 由服务端首次访问时生成并写 cookie,回调时同一浏览器
// 自动带回;攻击者拿不到别人的 cookie,就伪造不出可消费的 state。SameSite=Lax 保证 provider
// 的顶级 GET 回跳会带上它(Strict 会把登录流打断)。
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/identity"
	"github.com/fuuhoo/pipewright/internal/oauth"
	"github.com/go-chi/chi/v5"
)

const (
	// cookieOauthBind 是匿名 OAuth 登录的双提交 nonce(服务端签发、HttpOnly、仅用于绑 state)。
	cookieOauthBind = "pipewright_oauth_bind"
	// oauthBindTTL 与 oauth.stateTTL 同量级:超过 10 分钟的授权早该重发。
	oauthBindTTL = 10 * time.Minute
	// loginPagePath 是 OAuth 登录流出错后跳回的站内页面。
	loginPagePath = "/login"
	// accountPagePath 是绑定流出错后跳回的站内页面(账户设置:外部身份就展示在这)。
	accountPagePath = "/settings/account"
)

// identityLoginer 是 HTTP 层需要的「外部身份 ↔ 本地账号」判定能力(*identity.Service 实现)。
// 收成四个方法而不是直接依赖具体类型:测试可注入假实现,且本层看不到仓储细节。
type identityLoginer interface {
	Login(ctx context.Context, id *oauth.Identity) (*identity.Account, error)
	Bind(ctx context.Context, userID string, id *oauth.Identity) (*identity.Binding, error)
	Unbind(ctx context.Context, userID, bindingID string) error
	ListByUser(ctx context.Context, userID string) ([]*identity.Binding, error)
}

// sessionIssuer 为已验证身份的账号签发会话(*auth.Service 实现)。
// 独立于 auth.Authenticator:那条接口只认口令,OAuth 这条登录路径不该去碰口令校验。
type sessionIssuer interface {
	IssueSessionForUser(userID, username, role string) (*auth.Session, error)
}

// oauthLoginDeps 是登录流需要的协作者(任一为 nil → 端点回人读错误,不 panic)。
type oauthLoginDeps struct {
	oauth    oauth.Service
	ident    identityLoginer
	sessions sessionIssuer
	aud      audit.Recorder
}

// ---- DTO ----

// publicOAuthProviderDTO 是登录页看到的入口项(公开端点;绝不含 client_id / baseUrl 等配置细节)。
type publicOAuthProviderDTO struct {
	Provider string `json:"provider"`
	Label    string `json:"label"`
}

// oauthLoginAppDTO 是管理员侧的登录应用视图(冻结契约;camelCase;secret 仅掩码)。
// callbackUrl 是管理员要在 git 平台应用里登记的回调地址 —— 一个 OAuth 应用通常只能登记
// 一个回调,登记错了表现为「授权页一切正常,回来就报错」,所以直接把该填什么摆在配置旁边。
type oauthLoginAppDTO struct {
	Provider       string   `json:"provider"`
	ClientID       string   `json:"clientId"`
	BaseURL        string   `json:"baseUrl"`
	AllowedDomains []string `json:"allowedDomains"`
	Enabled        bool     `json:"enabled"`
	MaskedSecret   string   `json:"maskedSecret"`
	Configured     bool     `json:"configured"`
	UpdatedAt      *string  `json:"updatedAt"`
	CallbackURL    string   `json:"callbackUrl"`
	// 下面六项只对 provider=oidc 有值:端点(发现文档 + 三项覆盖)、主键声明、scope 串。
	// git 平台永远返回空串 —— 前端据此决定这组字段显不显示,不用自己再判一遍枚举。
	DiscoveryURL string `json:"discoveryUrl"`
	AuthorizeURL string `json:"authorizeUrl"`
	TokenURL     string `json:"tokenUrl"`
	UserinfoURL  string `json:"userinfoUrl"`
	IDClaim      string `json:"idClaim"`
	Scopes       string `json:"scopes"`
}

// identityBindingDTO 是一条外部身份映射(账户页;camelCase;无 token、无口令)。
type identityBindingDTO struct {
	ID         string `json:"id"`
	Provider   string `json:"provider"`
	ExternalID string `json:"externalId"`
	Username   string `json:"username"`
	Email      string `json:"email"`
	CreatedAt  string `json:"createdAt"`
	UpdatedAt  string `json:"updatedAt"`
}

func toIdentityBindingDTO(b *identity.Binding) identityBindingDTO {
	return identityBindingDTO{
		ID: b.ID, Provider: b.Provider, ExternalID: b.ExternalID,
		Username: b.Username, Email: b.Email,
		CreatedAt: b.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: b.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// loginProviderLabel 给登录页一个不必前端拼的名字:内置档用官方名,自建档用实例主机名。
func loginProviderLabel(cfg oauth.LoginAppConfig) string {
	switch cfg.Provider {
	case oauth.ProviderGitee:
		return "Gitee"
	case oauth.ProviderGitHub:
		return "GitHub"
	case oauth.ProviderGitLab:
		return "GitLab"
	case oauth.ProviderOIDC:
		// OIDC 没有品牌名可借,也不该拿 baseURL 当名字(它压根不用 baseURL)。
		return "OIDC"
	}
	if u, err := url.Parse(cfg.BaseURL); err == nil && u.Host != "" {
		return u.Host
	}
	return "自定义"
}

// ---- 公开端点:匿名 OAuth 登录 ----

// makePublicOAuthProvidersHandler 返回 GET /api/auth/oauth/providers handler(**公开**)。
// 只列出「已配全且已启用」的登录应用;未启用即不出现在响应里(登录页据此决定出不出现按钮)。
func makePublicOAuthProvidersHandler(svc oauth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeJSON(w, http.StatusOK, []publicOAuthProviderDTO{})
			return
		}
		apps, err := svc.ListLoginApps(r.Context())
		if err != nil {
			// 登录页不该因为领域故障白屏:出错就当作「没配任何入口」。
			writeJSON(w, http.StatusOK, []publicOAuthProviderDTO{})
			return
		}
		out := make([]publicOAuthProviderDTO, 0, len(apps))
		for _, a := range apps {
			if !a.Enabled || !a.Configured {
				continue
			}
			out = append(out, publicOAuthProviderDTO{Provider: a.Provider, Label: loginProviderLabel(a)})
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// makeOAuthLoginStartHandler 返回 GET /api/auth/oauth/{provider}/authorize handler(**公开**)。
// 生成/复用浏览器 nonce → 发 IntentLogin 的 state → 302 跳 provider 授权页。
// provider 未配置/未启用 → 跳回 /login 带人读原因码(绝不回显配置细节)。
func makeOAuthLoginStartHandler(d oauthLoginDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provider := chi.URLParam(r, "provider")
		// 三块拼图缺任一都不发起授权:走到回调才发现登不进,用户只会看到「服务器内部错误」。
		if d.oauth == nil || d.ident == nil || d.sessions == nil {
			redirectLoginError(w, r, provider, "login_unavailable")
			return
		}
		bindKey := oauthBindKey(w, r)
		if bindKey == "" {
			redirectLoginError(w, r, provider, "login_unavailable")
			return
		}
		state, err := d.oauth.IssueState(oauth.StateIssue{
			Intent:   oauth.IntentLogin,
			Provider: provider,
			BindKey:  bindKey,
			Redirect: safeRedirectPath(r.URL.Query().Get("redirect"), ""),
		})
		if err != nil {
			redirectLoginError(w, r, provider, "login_unavailable")
			return
		}
		authURL, err := d.oauth.LoginAuthorizeURL(r.Context(), provider, state, oauthLoginCallbackURL(r, provider))
		if err != nil {
			redirectLoginError(w, r, provider, loginStartErrorReason(err))
			return
		}
		http.Redirect(w, r, authURL, http.StatusFound)
	}
}

// makeOAuthLoginCallbackHandler 返回 GET /api/auth/oauth/{provider}/callback handler(**公开**)。
// 验 state(绑浏览器 nonce)→ 换身份 → 认人/建号 → 签会话 → 302 回站内。
// 任一步失败都跳回 /login 带原因码;失败也记审计(「有人拿别人的身份试登」正是最该看到的)。
func makeOAuthLoginCallbackHandler(d oauthLoginDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provider := chi.URLParam(r, "provider")
		q := r.URL.Query()
		if d.oauth == nil || d.ident == nil || d.sessions == nil {
			redirectLoginError(w, r, provider, "login_unavailable")
			return
		}
		nonce, err := r.Cookie(cookieOauthBind)
		if err != nil || nonce.Value == "" {
			recordOAuthLoginFailure(d.aud, r, provider, "", "invalid_state")
			redirectLoginError(w, r, provider, "invalid_state")
			return
		}
		// state 一次性消费:无论后面成不成,这条 state 与浏览器 nonce 都不再有效。
		st, ok := d.oauth.ConsumeState(q.Get("state"), nonce.Value)
		clearOauthBindCookie(w, r)
		if !ok || st.Intent != oauth.IntentLogin || st.Provider != provider {
			// 这一条正是「有人看见了别人的回调 URL」的信号:state 对不上、已消费过、
			// 或意图/平台不匹配。不记审计的话,一次未遂的账号接管在库里不留任何痕迹。
			recordOAuthLoginFailure(d.aud, r, provider, "", "invalid_state")
			redirectLoginError(w, r, provider, "invalid_state")
			return
		}
		code := q.Get("code")
		if code == "" {
			// provider 侧「用户拒绝授权」也走这条:带 error 参数回来,但没有任何 code。
			reason := "missing_code"
			if q.Get("error") != "" {
				reason = "provider_denied"
			}
			recordOAuthLoginFailure(d.aud, r, provider, "", reason)
			redirectLoginError(w, r, provider, reason)
			return
		}

		id, err := d.oauth.ExchangeIdentity(r.Context(), provider, code, oauthLoginCallbackURL(r, provider))
		if err != nil {
			reason := loginExchangeReason(err)
			recordOAuthLoginFailure(d.aud, r, provider, "", reason)
			redirectLoginError(w, r, provider, reason)
			return
		}
		acct, err := d.ident.Login(r.Context(), id)
		if err != nil {
			reason := identityErrorReason(err)
			recordOAuthLoginFailure(d.aud, r, provider, id.ExternalID, reason)
			redirectLoginError(w, r, provider, reason)
			return
		}
		sess, err := d.sessions.IssueSessionForUser(acct.UserID, acct.Username, acct.Role)
		if err != nil {
			recordOAuthLoginFailure(d.aud, r, provider, id.ExternalID, "session_unavailable")
			redirectLoginError(w, r, provider, "session_unavailable")
			return
		}
		setSessionCookies(w, r, sess)
		recordAudit(r.Context(), d.aud, audit.Entry{
			Actor:      acct.Username,
			Action:     audit.ActionOauthLogin,
			TargetType: audit.TargetUser,
			TargetID:   acct.UserID,
			Detail: map[string]any{
				"provider": provider,
				// externalId 是判定依据:事后要看「这个会话是哪个平台身份换的」,
				// 而不是只看一个可能已被改掉的登录名。
				"externalId":  id.ExternalID,
				"login":       id.Login,
				"provisioned": acct.Provisioned,
			},
			IP: clientIP(r),
		})
		dest := st.Redirect
		if dest == "" {
			dest = "/"
		}
		http.Redirect(w, r, dest, http.StatusFound)
	}
}

// ---- 已登录:把 git 账号绑到自己 ----

// makeOAuthBindStartHandler 返回 GET /api/oauth/{provider}/bind handler(认证 + 会话 state)。
// 刻意复用「连接凭据」那条流的 authorize 与 callback(client_id 与回调地址都是同一份,
// OAuth 应用通常只允许登记一个回调地址),只差 state 里的意图。
func makeOAuthBindStartHandler(svc oauth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provider := chi.URLParam(r, "provider")
		sess, ok := sessionFromContext(r.Context())
		if !ok || sess == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
			return
		}
		if sess.UserID == "" {
			// 旧部署会话没有 user_id:绑了也没法判定归属,直接拒(而不是写出无主映射)。
			writeError(w, http.StatusUnprocessableEntity, "session_unbound", "当前会话无法绑定外部身份,请重新登录")
			return
		}
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "OAuth 服务未初始化")
			return
		}
		state, err := svc.IssueState(oauth.StateIssue{
			Intent:   oauth.IntentBind,
			Provider: provider,
			BindKey:  sess.Token,
			Redirect: safeRedirectPath(r.URL.Query().Get("redirect"), accountPagePath),
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
			return
		}
		authURL, err := svc.AuthorizeURL(r.Context(), provider, state, oauthVaultCallbackURL(r, provider))
		if err != nil {
			writeOAuthError(w, err)
			return
		}
		http.Redirect(w, r, authURL, http.StatusFound)
	}
}

// bindViaCallback 是「已登录绑定」在凭据回调里的分支(见 makeOAuthCallbackHandler)。
// 为什么单列一个错误出口:绑定失败要跳回账户页,而不是把错误报给完全不相干的保险库页。
func bindViaCallback(w http.ResponseWriter, r *http.Request, d oauthCallbackDeps, sess *auth.Session, provider string) {
	code := r.URL.Query().Get("code")
	if code == "" {
		redirectAccountError(w, r, provider, "missing_code")
		return
	}
	if d.ident == nil {
		redirectAccountError(w, r, provider, "identity_unavailable")
		return
	}
	id, err := d.svc.ExchangeIdentity(r.Context(), provider, code, oauthVaultCallbackURL(r, provider))
	if err != nil {
		redirectAccountError(w, r, provider, loginExchangeReason(err))
		return
	}
	// 绑定**不建号、不改角色**:把 git 身份挂到当前这个已登录账号上。
	// 拿到稳定 ID 是硬要求 —— 没有它就无法保证「下次还是同一个人」,绑了等于没绑。
	b, err := d.ident.Bind(r.Context(), sess.UserID, id)
	if err != nil {
		redirectAccountError(w, r, provider, identityErrorReason(err))
		return
	}
	recordAudit(r.Context(), d.aud, audit.Entry{
		Actor:      sess.Username,
		Action:     audit.ActionIdentityBind,
		TargetType: audit.TargetUser,
		TargetID:   sess.UserID,
		Detail: map[string]any{
			"provider":   provider,
			"externalId": b.ExternalID,
			"login":      b.Username,
		},
		IP: clientIP(r),
	})
	http.Redirect(w, r, accountPagePath+"?bound="+url.QueryEscape(provider), http.StatusFound)
}

// ---- 本人:外部身份清单 / 解绑 ----

// makeListIdentitiesHandler 返回 GET /api/account/identities handler(认证;只读)。
func makeListIdentitiesHandler(ident identityLoginer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := sessionFromContext(r.Context())
		if !ok || sess == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
			return
		}
		if ident == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "身份映射服务未初始化")
			return
		}
		if sess.UserID == "" {
			writeJSON(w, http.StatusOK, []identityBindingDTO{})
			return
		}
		bindings, err := ident.ListByUser(r.Context(), sess.UserID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
			return
		}
		out := make([]identityBindingDTO, 0, len(bindings))
		for _, b := range bindings {
			out = append(out, toIdentityBindingDTO(b))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// makeUnbindIdentityHandler 返回 DELETE /api/account/identities/{bindingId} handler
// (认证 + CSRF + 审计)。只能解自己的绑定(identity.Unbind 带 user_id 条件)。
func makeUnbindIdentityHandler(ident identityLoginer, aud audit.Recorder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := sessionFromContext(r.Context())
		if !ok || sess == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
			return
		}
		if ident == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "身份映射服务未初始化")
			return
		}
		bindingID := chi.URLParam(r, "bindingId")
		err := ident.Unbind(r.Context(), sess.UserID, bindingID)
		if err != nil {
			if errors.Is(err, identity.ErrNotFound) {
				writeError(w, http.StatusNotFound, "binding_not_found", "绑定不存在或不属于当前账号")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
			return
		}
		// 解绑后自己的会话**不撤销**:掉线不该是解绑的副作用(可能只剩这一条登录路径)。
		recordAudit(r.Context(), aud, audit.Entry{
			Actor:      sess.Username,
			Action:     audit.ActionIdentityUnbind,
			TargetType: audit.TargetUser,
			TargetID:   sess.UserID,
			Detail:     map[string]any{"bindingId": bindingID},
			IP:         clientIP(r),
		})
		w.WriteHeader(http.StatusNoContent)
	}
}

// ---- 管理员:登录应用配置 ----

// makeListLoginAppsHandler 返回 GET /api/admin/oauth/login-apps handler(认证 + admin;只读)。
// secret 仅掩码;callbackUrl 按当前请求 origin 现算(部署换域名就不用猜)。
func makeListLoginAppsHandler(svc oauth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "OAuth 服务未初始化")
			return
		}
		apps, err := svc.ListLoginApps(r.Context())
		if err != nil {
			writeOAuthError(w, err)
			return
		}
		out := make([]oauthLoginAppDTO, 0, len(apps))
		for _, a := range apps {
			out = append(out, toLoginAppDTO(a, r))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// makeSaveLoginAppHandler 返回 PUT /api/admin/oauth/login-apps/{provider} handler(认证 + admin + CSRF)。
// clientSecret 只写:省略/空保留既有,非空轮换;allowedDomains 省略(nil)保留既有,
// 空数组 = 清空白名单(不限域名)。绝不在响应回明文。
func makeSaveLoginAppHandler(svc oauth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "OAuth 服务未初始化")
			return
		}
		provider := chi.URLParam(r, "provider")
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		var req struct {
			ClientID       string    `json:"clientId"`
			BaseURL        string    `json:"baseUrl"`
			ClientSecret   *string   `json:"clientSecret"`
			AllowedDomains *[]string `json:"allowedDomains"`
			Enabled        bool      `json:"enabled"`
			// OIDC 端点是「整份覆盖」语义:每次保存都以这几个值为准,省略即清空。
			// 只有 provider=oidc 会用到,git 档位服务端一律忽略并落空串。
			DiscoveryURL string `json:"discoveryUrl"`
			AuthorizeURL string `json:"authorizeUrl"`
			TokenURL     string `json:"tokenUrl"`
			UserinfoURL  string `json:"userinfoUrl"`
			IDClaim      string `json:"idClaim"`
			Scopes       string `json:"scopes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		cfg, err := svc.SaveLoginApp(r.Context(), oauth.SaveLoginAppInput{
			Provider:       provider,
			ClientID:       req.ClientID,
			BaseURL:        req.BaseURL,
			ClientSecret:   req.ClientSecret,
			AllowedDomains: req.AllowedDomains,
			Enabled:        req.Enabled,
			DiscoveryURL:   req.DiscoveryURL,
			AuthorizeURL:   req.AuthorizeURL,
			TokenURL:       req.TokenURL,
			UserinfoURL:    req.UserinfoURL,
			IDClaim:        req.IDClaim,
			Scopes:         req.Scopes,
		})
		if err != nil {
			// 白名单项非法是保存期校验,报错带原文(哪一项写错了,配的人一眼能看到)。
			if errors.Is(err, oauth.ErrInvalidDomain) {
				writeError(w, http.StatusUnprocessableEntity, "invalid_domain", "邮箱域名白名单含非法项:只填域名本身(如 corp.example.com),不要带协议、路径或空格")
				return
			}
			writeOAuthError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toLoginAppDTO(*cfg, r))
	}
}

// ---- helpers ----

func toLoginAppDTO(c oauth.LoginAppConfig, r *http.Request) oauthLoginAppDTO {
	dto := oauthLoginAppDTO{
		Provider:       c.Provider,
		ClientID:       c.ClientID,
		BaseURL:        c.BaseURL,
		AllowedDomains: append([]string{}, c.AllowedDomains...),
		Enabled:        c.Enabled,
		MaskedSecret:   c.MaskedSecret,
		Configured:     c.Configured,
		CallbackURL:    oauthLoginCallbackURL(r, c.Provider),
		DiscoveryURL:   c.DiscoveryURL,
		AuthorizeURL:   c.AuthorizeURL,
		TokenURL:       c.TokenURL,
		UserinfoURL:    c.UserinfoURL,
		IDClaim:        c.IDClaim,
		Scopes:         c.Scopes,
	}
	if c.UpdatedAt != nil {
		s := c.UpdatedAt.UTC().Format(time.RFC3339)
		dto.UpdatedAt = &s
	}
	return dto
}

// oauthLoginCallbackURL 是匿名登录流的回调地址(公开,无需会话)。
func oauthLoginCallbackURL(r *http.Request, provider string) string {
	return requestOrigin(r) + "/api/auth/oauth/" + url.PathEscape(provider) + "/callback"
}

// oauthVaultCallbackURL 是「已登录」两条流(连接凭据 / 绑定账号)共用的回调地址。
func oauthVaultCallbackURL(r *http.Request, provider string) string {
	return requestOrigin(r) + "/api/oauth/" + url.PathEscape(provider) + "/callback"
}

// oauthBindKey 取回浏览器上的登录 nonce;没有就现生成一个并写 cookie。
// 返回空串 = 随机数不可用(此时宁可不发起授权,也不发一条没有 bindKey 的 state)。
func oauthBindKey(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(cookieOauthBind); err == nil && c.Value != "" {
		return c.Value
	}
	key, err := oauth.NewBindKey()
	if err != nil {
		return ""
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieOauthBind,
		Value:    key,
		Path:     "/",
		MaxAge:   int(oauthBindTTL.Seconds()),
		HttpOnly: true,
		// Lax:provider 回跳是跨站顶级 GET,Strict 会让 nonce 带不回来、登录流必然失败。
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	})
	return key
}

// clearOauthBindCookie 让 nonce 一次性的那半落地(state 已被消费,cookie 也随之作废)。
func clearOauthBindCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieOauthBind,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
		Expires:  time.Unix(0, 0),
	})
}

// safeRedirectPath 只放行站内绝对路径,用于 state 里带回跳目标。
//
// 拒绝的形态各有原因:"//" 开头是协议相对 URL(浏览器按 http://外部域 解析,等于开放重定向);
// "/\" 同理(部分浏览器把反斜杠当斜杠);含 CR/LF 会被响应头截断;超长不是正常路由。
// 不合规一律回退 fallback(空串表示「用默认落地页」)。
func safeRedirectPath(raw, fallback string) string {
	p := strings.TrimSpace(raw)
	if len(p) > 512 || strings.ContainsAny(p, "\r\n") {
		return fallback
	}
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, `/\`) {
		return fallback
	}
	return p
}

// redirectLoginError 302 回登录页并带人读、无 secret 的原因码。
func redirectLoginError(w http.ResponseWriter, r *http.Request, provider, reason string) {
	dest := loginPagePath + "?oauth_error=" + url.QueryEscape(reason) + "&provider=" + url.QueryEscape(provider)
	http.Redirect(w, r, dest, http.StatusFound)
}

// redirectAccountError 302 回账户页并带原因码(绑定流的失败落点)。
func redirectAccountError(w http.ResponseWriter, r *http.Request, provider, reason string) {
	dest := accountPagePath + "?oauth_error=" + url.QueryEscape(reason) + "&provider=" + url.QueryEscape(provider)
	http.Redirect(w, r, dest, http.StatusFound)
}

// loginStartErrorReason 把「发起授权」阶段的领域错误映射为原因码。
// vault_unavailable 在发起阶段只有一种成因:master key 换过/密文被篡改,导致存着的
// client_secret 根本解不开(openSecret 对所有解密失败都归到 ErrVaultUnconfigured)。
// 此时兑换必然失败,与其让用户授权完再回来看到一句看不懂的话,不如发起时就拒。
func loginStartErrorReason(err error) string {
	switch {
	case errors.Is(err, oauth.ErrAppNotConfigured):
		return "app_not_configured"
	case errors.Is(err, oauth.ErrInvalidProvider):
		return "invalid_provider"
	case errors.Is(err, oauth.ErrVaultUnconfigured):
		return "vault_unavailable"
	case errors.Is(err, oauth.ErrOidcConfigInvalid):
		return "oidc_config_invalid"
	default:
		return "authorize_failed"
	}
}

// loginExchangeReason 把「换身份」阶段的领域错误映射为原因码(与凭据流同一套码,前端只翻译一次)。
func loginExchangeReason(err error) string {
	if errors.Is(err, oauth.ErrIdentityUnavailable) {
		return "identity_unavailable"
	}
	return oauthErrorReason(err)
}

// identityErrorReason 把「认人 / 建号」阶段的领域错误映射为人读原因码。
// 每个分支都对应一条明确的拒绝理由 —— 尤其 name_taken,必须把「为什么不给你建」说清楚,
// 否则用户只会看到一个没有任何解释的失败。
func identityErrorReason(err error) string {
	switch {
	case errors.Is(err, identity.ErrDomainNotAllowed):
		return "domain_not_allowed"
	case errors.Is(err, identity.ErrNameTaken):
		return "name_taken"
	case errors.Is(err, identity.ErrAccountUnavailable):
		return "account_unavailable"
	case errors.Is(err, identity.ErrRoleUnavailable):
		return "role_unavailable"
	case errors.Is(err, identity.ErrLoginDisabled):
		return "login_disabled"
	case errors.Is(err, identity.ErrAlreadyBound):
		return "already_bound"
	case errors.Is(err, identity.ErrProviderBound):
		return "provider_bound"
	case errors.Is(err, identity.ErrIdentityIncomplete):
		return "identity_unavailable"
	default:
		return "login_failed"
	}
}

// recordOAuthLoginFailure 记一条失败的 OAuth 登录审计。
//
// actor 用 auditActorFallback:失败时「这个人是谁」根本没有可信答案 —— 填上攻击者自称的
// 登录名,等于往 append-only 的审计里写一个看起来像真账号的名字,事后排查反被误导。
// 判定依据(provider / externalId)与原因码进 detail,足够复盘。
func recordOAuthLoginFailure(rec audit.Recorder, r *http.Request, provider, externalID, reason string) {
	if rec == nil {
		return
	}
	detail := map[string]any{"provider": provider, "reason": reason, "success": false}
	if externalID != "" {
		detail["externalId"] = externalID
	}
	recordAudit(r.Context(), rec, audit.Entry{
		Actor:      auditActorFallback,
		Action:     audit.ActionOauthLogin,
		TargetType: audit.TargetSession,
		Detail:     detail,
		IP:         clientIP(r),
	})
}
