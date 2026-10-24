// oauth_login_test.go 是「OAuth 登录 / 绑定」这条面的反向用例集。
//
// 装配全部是真的(oauth + identity + users + auth + vault + audit),只有 git 平台是
// httptest 假冒的 —— 接管类漏洞恰好藏在真实协作的接缝上(state 意图、cookie 归属、
// 会话签发、审计),用桩对象替掉哪一块,那一块的漏洞就测不出来。
package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/access"
	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/identity"
	"github.com/fuuhoo/pipewright/internal/mask"
	"github.com/fuuhoo/pipewright/internal/oauth"
	"github.com/fuuhoo/pipewright/internal/role"
	"github.com/fuuhoo/pipewright/internal/users"
	"github.com/fuuhoo/pipewright/internal/vault"
)

// ---- 测试装配 ----

// stubGitProvider 是一个 GitLab 形态的最小 OAuth provider
// (custom 档的端点由 base_url + 平台形态推导,故它是唯一能被 httptest 完整接管的形态;
// 换成 gitea 形态要另起 /login/oauth/access_token 与 /api/v1/user 两条路径)。
type stubGitProvider struct {
	srv *httptest.Server
}

func newStubGitProvider(t *testing.T, userBody string) *stubGitProvider {
	t.Helper()
	return newStubGitProviderToken(t, userBody, `{"access_token":"stub-access-token"}`)
}

// newStubGitProviderToken 让令牌响应可控:凭据同步用例要的是带 expires_in / refresh_token
// 的那一档,而「平台只回 access_token」是自建 Gitea 的真实形态,两种都得能摆出来。
func newStubGitProviderToken(t *testing.T, userBody, tokenBody string) *stubGitProvider {
	t.Helper()
	m := &stubGitProvider{}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(tokenBody))
	})
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(userBody))
	})
	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

func sp(s string) *string      { return &s }
func sps(s []string) *[]string { return &s }

// loginTestEnv 是一次测试的真实装配。
type loginTestEnv struct {
	srv     *httptest.Server
	db      *sql.DB
	stub    *stubGitProvider
	oauth   oauth.Service
	ident   *identity.Service
	authSvc *auth.Service
	users   *users.Service
	rec     audit.Recorder
	v       vault.Vault
}

// buildLoginEnv 装配登录流的全部真件,只把「配哪一档登录应用」交给调用方:
// git 档用 mock 的 GitLab 形态,OIDC 档用 mock 的发现文档,两者共用这套骨架。
func buildLoginEnv(t *testing.T, client *http.Client, configure func(oauth.Service)) *loginTestEnv {
	t.Helper()
	st := testStoreAuth(t)
	usersSvc := users.NewService(st.DB)
	// 会话角色是从 users 行带进 sessions 的:userSyncer 不接上,口令登录签出的会话
	// role 就是空串,而空串按历史语义等于「管理员」—— 非管理员用例全会误判成放行。
	authSvc := auth.NewService(st.DB, nil, usersSvc)
	if err := authSvc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	// viewer 是 0063 播种进库的普通行,不在代码表里:不装载目录,identity 的默认角色判定
	// 与 RequireAdmin 都会 fail closed(main.go 里同样是这两行连着写)。
	roleSvc := role.New(st.DB)
	access.SetRoleStore(roleSvc)
	if err := access.ReloadRoles(context.Background()); err != nil {
		t.Fatalf("装载角色目录: %v", err)
	}
	t.Cleanup(func() { access.SetRoleStore(nil) })

	v := vault.New(st.DB, testMasterKey())
	oauthSvc := oauth.New(st.DB, v, client)
	configure(oauthSvc)
	// 凭据同步的开关在「登录应用」那一行上,默认关;这里把真 vault 注进来,
	// 让开了开关的用例能一路走到保险库(关着的用例则照旧一条凭据都不写)。
	ident := identity.NewService(st.DB, usersSvc, oauthSvc, v)
	rec := audit.New(st.DB, mask.NewMasker(), nil)

	srv := httptest.NewServer(New(testWebFSAuth(), authSvc,
		WithVault(v), WithOAuth(oauthSvc), WithUsers(usersSvc), WithRoles(roleSvc),
		WithOAuthLogin(ident, authSvc), WithAudit(rec)))
	t.Cleanup(srv.Close)
	return &loginTestEnv{
		srv: srv, db: st.DB, oauth: oauthSvc,
		ident: ident, authSvc: authSvc, users: usersSvc, rec: rec, v: v,
	}
}

// newLoginTestEnv 装配并登记一个启用的 custom 登录应用。allowedDomains 为 nil = 不设白名单。
func newLoginTestEnv(t *testing.T, userBody string, allowedDomains []string) *loginTestEnv {
	t.Helper()
	stub := newStubGitProvider(t, userBody)
	e := buildLoginEnv(t, stub.srv.Client(), func(oauthSvc oauth.Service) {
		if _, err := oauthSvc.SaveLoginApp(context.Background(), oauth.SaveLoginAppInput{
			Provider: oauth.ProviderCustom, BaseURL: stub.srv.URL,
			ClientID: "login-cid", ClientSecret: sp("login-secret"),
			AllowedDomains: sps(allowedDomains), Enabled: true,
		}); err != nil {
			t.Fatalf("SaveLoginApp: %v", err)
		}
		// 「已登录」的两条流(连接凭据 / 绑定账号)读的是 oauth_apps,与登录应用是两张表:
		// 只配登录应用时它们必然 422,那与要测的行为无关,所以这里也配一份。
		if _, err := oauthSvc.SaveApp(context.Background(), oauth.SaveAppInput{
			Provider: oauth.ProviderCustom, BaseURL: stub.srv.URL,
			ClientID: "app-cid", ClientSecret: sp("app-secret"), Enabled: true,
		}); err != nil {
			t.Fatalf("SaveApp: %v", err)
		}
	})
	e.stub = stub
	return e
}

// ---- helpers ----

// authorize 发起匿名授权,返回 provider 302 目标里的 state(顺带让 client 拿到 bind cookie)。
func (e *loginTestEnv) authorize(t *testing.T, c *http.Client, provider, redirect string) string {
	t.Helper()
	u := e.srv.URL + "/api/auth/oauth/" + provider + "/authorize"
	if redirect != "" {
		u += "?redirect=" + url.QueryEscape(redirect)
	}
	resp := e.get(t, c, u)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize status = %d, want 302", resp.StatusCode)
	}
	return locationOf(t, resp).Query().Get("state")
}

func (e *loginTestEnv) loginCallbackURL(provider, code, state string) string {
	return e.srv.URL + "/api/auth/oauth/" + provider + "/callback?" + codeStateQuery(code, state)
}

// vaultCallbackURL 是「已登录」两条流(连接凭据 / 绑定账号)共用的回调地址。
func (e *loginTestEnv) vaultCallbackURL(provider, code, state string) string {
	return e.srv.URL + "/api/oauth/" + provider + "/callback?" + codeStateQuery(code, state)
}

func codeStateQuery(code, state string) string {
	q := url.Values{}
	if code != "" {
		q.Set("code", code)
	}
	if state != "" {
		q.Set("state", state)
	}
	return q.Encode()
}

func (e *loginTestEnv) get(t *testing.T, c *http.Client, u string) *http.Response {
	t.Helper()
	resp, err := c.Get(u)
	if err != nil {
		t.Fatalf("get %s: %v", u, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// loginOnce 走完「发起 → 回调」,返回回调响应。
func (e *loginTestEnv) loginOnce(t *testing.T, c *http.Client, provider string) *http.Response {
	t.Helper()
	return e.get(t, c, e.loginCallbackURL(provider, "stub-code", e.authorize(t, c, provider, "")))
}

// bindOnce 以已登录会话发起绑定并跑完回调,返回回调响应。
func (e *loginTestEnv) bindOnce(t *testing.T, c *http.Client, provider string) *http.Response {
	t.Helper()
	resp := e.get(t, c, e.srv.URL+"/api/oauth/"+provider+"/bind")
	if resp.StatusCode != http.StatusFound {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("bind status = %d, want 302, body=%s", resp.StatusCode, raw)
	}
	state := locationOf(t, resp).Query().Get("state")
	return e.get(t, c, e.vaultCallbackURL(provider, "stub-code", state))
}

func cookieValue(resp *http.Response, name string) string {
	for _, ck := range resp.Cookies() {
		if ck.Name == name {
			return ck.Value
		}
	}
	return ""
}

func locationOf(t *testing.T, resp *http.Response) *url.URL {
	t.Helper()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse location %q: %v", resp.Header.Get("Location"), err)
	}
	return loc
}

// errorReason 取 302 落点上的原因码(/login 或 /settings/account 的 oauth_error)。
func errorReason(t *testing.T, resp *http.Response) string {
	t.Helper()
	return locationOf(t, resp).Query().Get("oauth_error")
}

// csrfOf 从 client 的 cookie jar 取当前 CSRF token(调用前须已登录)。
func (e *loginTestEnv) csrfOf(t *testing.T, c *http.Client) string {
	t.Helper()
	u, _ := url.Parse(e.srv.URL)
	for _, ck := range c.Jar.Cookies(u) {
		if ck.Name == cookieCsrf && ck.Value != "" {
			return ck.Value
		}
	}
	t.Fatal("cookie jar 里没有 csrf token")
	return ""
}

func (e *loginTestEnv) auditOf(t *testing.T, action string) []audit.Record {
	t.Helper()
	res, err := e.rec.List(context.Background(), audit.ListFilter{Action: action, Limit: 50})
	if err != nil {
		t.Fatalf("audit list: %v", err)
	}
	return res.Entries
}

func (e *loginTestEnv) count(t *testing.T, query string) int {
	t.Helper()
	var n int
	if err := e.db.QueryRow(query).Scan(&n); err != nil {
		t.Fatalf("count (%s): %v", query, err)
	}
	return n
}

// createUser 直接建一个本地账号(绕过 HTTP,免得测试依赖管理端接口形态)。
func (e *loginTestEnv) createUser(t *testing.T, username, password, role string) {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if _, err := e.users.Create(users.CreateInput{Username: username, PasswordHash: hash, Role: role}); err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
}

// loginAs 用指定账号口令登录,返回 CSRF token。
// 与 loginWithClient(写死 admin)分开:越权用例要的是「另一个身份」的会话。
func (e *loginTestEnv) loginAs(t *testing.T, c *http.Client, username, password string) string {
	t.Helper()
	body := strings.NewReader(`{"username":"` + username + `","password":"` + password + `"}`)
	resp, err := c.Post(e.srv.URL+"/api/auth/login", "application/json", body)
	if err != nil {
		t.Fatalf("login %s: %v", username, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("login %s status = %d, want 200, body=%s", username, resp.StatusCode, raw)
	}
	return cookieValue(resp, cookieCsrf)
}

// sessionOf 用当前会话探测 /api/auth/session,回出账号身份(会话是否真的是「可用会话」的判据)。
func (e *loginTestEnv) sessionOf(t *testing.T, c *http.Client) sessionPayload {
	t.Helper()
	resp := e.get(t, c, e.srv.URL+"/api/auth/session")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session probe status = %d, want 200", resp.StatusCode)
	}
	var payload sessionPayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	return payload
}

// ---- 匿名 OAuth 登录 ----

// TestOAuthLoginIssuesRealSessionForProvisionedViewer 是首登主路径:
// 平台身份 → 自动建号(viewer)→ 签真会话 → 审计记下判定依据与「首次建号」。
func TestOAuthLoginIssuesRealSessionForProvisionedViewer(t *testing.T) {
	e := newLoginTestEnv(t, `{"id":4242,"username":"octo","email":"octo@corp.example.com","name":"Octo"}`, nil)
	c := newTestClient(t)

	resp := e.loginOnce(t, c, oauth.ProviderCustom)
	if resp.StatusCode != http.StatusFound {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("callback status = %d, want 302, body=%s", resp.StatusCode, raw)
	}
	if got := locationOf(t, resp).String(); got != "/" {
		t.Fatalf("默认落地页 = %q, want /", got)
	}
	if cookieValue(resp, cookieSession) == "" {
		t.Fatalf("回调应下发会话 cookie")
	}
	// 会话是真的:拿它去探测会话端点,必须认得这个自动建出来的账号与它的最小角色。
	if got := e.sessionOf(t, c); got.Username != "octo" || got.Role != identity.DefaultRole {
		t.Fatalf("会话身份 = %+v, want octo/%s", got, identity.DefaultRole)
	}
	// bind nonce 是一次性的:回调后即作废,否则同一浏览器能反复消费旧 state。
	if got := cookieValue(resp, cookieOauthBind); got != "" {
		t.Fatalf("回调后 bind cookie 应清空,得到 %q", got)
	}

	entries := e.auditOf(t, audit.ActionOauthLogin)
	if len(entries) != 1 {
		t.Fatalf("登录审计条数 = %d, want 1", len(entries))
	}
	got := entries[0]
	if got.Actor != "octo" || got.TargetType != audit.TargetUser || got.TargetID == "" {
		t.Fatalf("审计条目不符: %+v", got)
	}
	// externalId 是判定依据;provisioned 让事后能分清「首次建号」与「既有登录」。
	if got.Detail["externalId"] != "4242" || got.Detail["provisioned"] != true || got.Detail["login"] != "octo" {
		t.Fatalf("审计 detail 应记 externalId/provisioned/login: %+v", got.Detail)
	}
}

// TestOAuthLoginReplayIsRejected 验证 state 一次性:重放同一条回调不再签发会话。
func TestOAuthLoginReplayIsRejected(t *testing.T) {
	e := newLoginTestEnv(t, `{"id":1,"username":"once"}`, nil)
	c := newTestClient(t)
	state := e.authorize(t, c, oauth.ProviderCustom, "")

	if cookieValue(e.get(t, c, e.loginCallbackURL(oauth.ProviderCustom, "stub-code", state)), cookieSession) == "" {
		t.Fatalf("首次回调应签发会话")
	}
	second := e.get(t, c, e.loginCallbackURL(oauth.ProviderCustom, "stub-code", state))
	if cookieValue(second, cookieSession) != "" {
		t.Fatalf("重放不应再签发会话")
	}
	if loc := locationOf(t, second); loc.Path != loginPagePath || errorReason(t, second) != "invalid_state" {
		t.Fatalf("重放落点 = %q, want /login?oauth_error=invalid_state", loc.String())
	}
}

// TestOAuthLoginStateIsBoundToInitiatingBrowser 是本功能最关键的一条反向用例:
// 攻击者拿到受害者的 code + state(整条回调 URL 被人看见并不罕见:代理日志、浏览器历史、
// IM 链接预览),但拿不到受害者的 bind cookie,于是登不进去。
func TestOAuthLoginStateIsBoundToInitiatingBrowser(t *testing.T) {
	e := newLoginTestEnv(t, `{"id":7,"username":"victim"}`, nil)
	victim := newTestClient(t)
	state := e.authorize(t, victim, oauth.ProviderCustom, "")

	attacker := newTestClient(t)
	// 攻击者先跑一次自己的流(拿到属于自己的 bind cookie),再用受害者的 state+code 回跳。
	e.authorize(t, attacker, oauth.ProviderCustom, "")
	resp := e.get(t, attacker, e.loginCallbackURL(oauth.ProviderCustom, "stub-code", state))
	if errorReason(t, resp) != "invalid_state" {
		t.Fatalf("跨浏览器重放落点 = %q, want invalid_state", locationOf(t, resp).String())
	}
	if cookieValue(resp, cookieSession) != "" {
		t.Fatalf("跨浏览器重放绝不能签发会话")
	}

	entries := e.auditOf(t, audit.ActionOauthLogin)
	if len(entries) == 0 {
		t.Fatalf("失败的 OAuth 登录应留审计痕迹")
	}
	if entries[0].Detail["reason"] != "invalid_state" {
		t.Fatalf("审计原因 = %v", entries[0].Detail["reason"])
	}
	// actor 必须是兜底值:写进 append-only 审计的「谁」不能是攻击者自称的登录名。
	if entries[0].Actor != auditActorFallback {
		t.Fatalf("失败审计的 actor = %q, want %q", entries[0].Actor, auditActorFallback)
	}
	// 也不该顺手给受害者建号:判定失败在认人之前。
	if n := e.count(t, `SELECT COUNT(1) FROM user_identities`); n != 0 {
		t.Fatalf("被劫持的回调不该写入身份映射,现有 %d 条", n)
	}
}

// TestOAuthLoginCallbackWithoutBindCookieIsRejected 覆盖直接跳回调(压根没发起过)。
func TestOAuthLoginCallbackWithoutBindCookieIsRejected(t *testing.T) {
	e := newLoginTestEnv(t, `{"id":8,"username":"direct"}`, nil)
	resp := e.get(t, newTestClient(t), e.loginCallbackURL(oauth.ProviderCustom, "stub-code", "forged-state"))
	if errorReason(t, resp) != "invalid_state" {
		t.Fatalf("落点 = %q, want invalid_state", locationOf(t, resp).String())
	}
	if cookieValue(resp, cookieSession) != "" {
		t.Fatalf("伪造 state 不该拿到会话")
	}
}

// TestOAuthLoginProviderMismatchIsRejected:拿 A 平台的授权码去触发 B 平台的回调。
// 两档都配好并启用(否则先撞 app_not_configured),这样拒的才是 provider 一致性这条判定。
func TestOAuthLoginProviderMismatchIsRejected(t *testing.T) {
	e := newLoginTestEnv(t, `{"id":9,"username":"cross"}`, nil)
	if _, err := e.oauth.SaveLoginApp(context.Background(), oauth.SaveLoginAppInput{
		Provider: oauth.ProviderGitLab, ClientID: "gl-cid", ClientSecret: sp("gl-secret"), Enabled: true,
	}); err != nil {
		t.Fatalf("启用 gitlab 登录应用: %v", err)
	}
	c := newTestClient(t)
	state := e.authorize(t, c, oauth.ProviderCustom, "")

	resp := e.get(t, c, e.loginCallbackURL(oauth.ProviderGitLab, "stub-code", state))
	if got := locationOf(t, resp); errorReason(t, resp) != "invalid_state" || got.Query().Get("provider") != oauth.ProviderGitLab {
		t.Fatalf("跨 provider 回调落点 = %q, want invalid_state", got.String())
	}
	if cookieValue(resp, cookieSession) != "" {
		t.Fatalf("跨 provider 回调绝不能签发会话")
	}
}

// TestOAuthLoginRedirectIsSameSiteOnly:发起时带的 redirect 随 state 一路带回回调 ——
// 站内绝对路径要生效,协议相对 URL(//evil)那类必须退到默认落地页。
func TestOAuthLoginRedirectIsSameSiteOnly(t *testing.T) {
	e := newLoginTestEnv(t, `{"id":10,"username":"redir"}`, nil)

	t.Run("站内路径生效", func(t *testing.T) {
		c := newTestClient(t)
		state := e.authorize(t, c, oauth.ProviderCustom, "/projects/123?tab=runs")
		resp := e.get(t, c, e.loginCallbackURL(oauth.ProviderCustom, "stub-code", state))
		if got := locationOf(t, resp).String(); got != "/projects/123?tab=runs" {
			t.Fatalf("落地 = %q", got)
		}
	})
	for _, evil := range []string{"//evil.example.com", `/\evil.example.com`, "https://evil.example.com", "javascript:alert(1)"} {
		t.Run("拒绝 "+evil, func(t *testing.T) {
			c := newTestClient(t)
			state := e.authorize(t, c, oauth.ProviderCustom, evil)
			resp := e.get(t, c, e.loginCallbackURL(oauth.ProviderCustom, "stub-code", state))
			if got := locationOf(t, resp); got.String() != "/" {
				t.Fatalf("开放重定向未被拦:落地 = %q", got.String())
			}
		})
	}
}

// TestOAuthLoginNameTakenRefusesWithoutShadowAccount 是「不接管、不劈人」这条铁律的端到端面:
// 同名本地账号已存在 → 拒登,既不接管它,也不建 alice-2 这种影子账号。
func TestOAuthLoginNameTakenRefusesWithoutShadowAccount(t *testing.T) {
	e := newLoginTestEnv(t, `{"id":11,"username":"alice","email":"alice@corp.example.com"}`, nil)
	e.createUser(t, "alice", "local-pass-123", "user")

	c := newTestClient(t)
	resp := e.loginOnce(t, c, oauth.ProviderCustom)
	if errorReason(t, resp) != "name_taken" {
		t.Fatalf("落点 = %q, want name_taken", locationOf(t, resp).String())
	}
	if cookieValue(resp, cookieSession) != "" {
		t.Fatalf("同名账号存在时绝不能签发会话(那等于按名字接管了别人)")
	}
	if n := e.count(t, `SELECT COUNT(1) FROM users WHERE username LIKE 'alice%'`); n != 1 {
		t.Fatalf("不该建影子账号,alice* 行数 = %d", n)
	}
	if n := e.count(t, `SELECT COUNT(1) FROM user_identities`); n != 0 {
		t.Fatalf("被拒的登录不该留下映射,现有 %d 条", n)
	}
	entries := e.auditOf(t, audit.ActionOauthLogin)
	if len(entries) != 1 || entries[0].Detail["reason"] != "name_taken" {
		t.Fatalf("被拒登录应有一条 name_taken 审计: %+v", entries)
	}
}

// TestOAuthLoginDomainWhitelistFailsClosed 覆盖白名单三条路径。
// 「拿不到邮箱」这一条尤其重要 —— 不能把查不到当成「不限制」。
func TestOAuthLoginDomainWhitelistFailsClosed(t *testing.T) {
	t.Run("域名不在白名单", func(t *testing.T) {
		e := newLoginTestEnv(t, `{"id":12,"username":"outsider","email":"me@other.example.org"}`, []string{"corp.example.com"})
		c := newTestClient(t)
		resp := e.loginOnce(t, c, oauth.ProviderCustom)
		if errorReason(t, resp) != "domain_not_allowed" {
			t.Fatalf("落点 = %q", locationOf(t, resp).String())
		}
		if cookieValue(resp, cookieSession) != "" {
			t.Fatalf("白名单外的域名不该拿到会话")
		}
	})
	t.Run("拿不到邮箱", func(t *testing.T) {
		e := newLoginTestEnv(t, `{"id":13,"username":"noemail"}`, []string{"corp.example.com"})
		c := newTestClient(t)
		if cookieValue(e.loginOnce(t, c, oauth.ProviderCustom), cookieSession) != "" {
			t.Fatalf("缺邮箱时白名单必须 fail closed")
		}
	})
	t.Run("域名归一后命中则放行", func(t *testing.T) {
		e := newLoginTestEnv(t, `{"id":14,"username":"insider","email":"Bob@CORP.example.com"}`, []string{"corp.example.com"})
		c := newTestClient(t)
		if cookieValue(e.loginOnce(t, c, oauth.ProviderCustom), cookieSession) == "" {
			t.Fatalf("大小写归一后命中的邮箱应放行")
		}
	})
}

// TestOAuthLoginRefusedWhenAppDisabledMidflight:授权往返之间管理员关掉了入口 ——
// 回调必须现查,不信发起时的判定。
func TestOAuthLoginRefusedWhenAppDisabledMidflight(t *testing.T) {
	e := newLoginTestEnv(t, `{"id":15,"username":"late"}`, nil)
	c := newTestClient(t)
	state := e.authorize(t, c, oauth.ProviderCustom, "")

	if _, err := e.oauth.SaveLoginApp(context.Background(), oauth.SaveLoginAppInput{
		Provider: oauth.ProviderCustom, BaseURL: e.stub.srv.URL,
		ClientID: "login-cid", Enabled: false,
	}); err != nil {
		t.Fatalf("停用登录应用: %v", err)
	}
	resp := e.get(t, c, e.loginCallbackURL(oauth.ProviderCustom, "stub-code", state))
	if errorReason(t, resp) != "login_disabled" {
		t.Fatalf("落点 = %q, want login_disabled", locationOf(t, resp).String())
	}
	if cookieValue(resp, cookieSession) != "" {
		t.Fatalf("已停用的入口不该还能登录")
	}
}

// TestOAuthLoginStartFallsBackToLoginPageWhenUnconfigured 验证没配的档位回原因码而不是 500。
func TestOAuthLoginStartFallsBackToLoginPageWhenUnconfigured(t *testing.T) {
	e := newLoginTestEnv(t, `{"id":16,"username":"x"}`, nil)
	resp := e.get(t, newTestClient(t), e.srv.URL+"/api/auth/oauth/github/authorize")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302(带原因码跳回登录页)", resp.StatusCode)
	}
	if loc := locationOf(t, resp); loc.Path != loginPagePath || errorReason(t, resp) != "app_not_configured" {
		t.Fatalf("落点 = %q", loc.String())
	}
}

// TestOAuthLoginUnavailableWithoutIdentityWiring:只配了 OAuth 应用、没接上 identity/会话签发
// 时,登录流要回人读原因码,不 panic 成 500。
func TestOAuthLoginUnavailableWithoutIdentityWiring(t *testing.T) {
	st := testStoreAuth(t)
	authSvc := auth.NewService(st.DB, nil, nil)
	if err := authSvc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	v := vault.New(st.DB, testMasterKey())
	srv := httptest.NewServer(New(testWebFSAuth(), authSvc, WithVault(v), WithOAuth(oauth.New(st.DB, v, nil))))
	t.Cleanup(srv.Close)

	c := newTestClient(t)
	start := eMustGet(t, c, srv.URL+"/api/auth/oauth/github/authorize")
	_ = start.Body.Close()
	if got := locationOf(t, start); got.Path != loginPagePath || got.Query().Get("oauth_error") != "login_unavailable" {
		t.Fatalf("未装配登录依赖时落点 = %q", got.String())
	}
	cb := eMustGet(t, c, srv.URL+"/api/auth/oauth/github/callback?code=x&state=y")
	_ = cb.Body.Close()
	if locationOf(t, cb).Query().Get("oauth_error") != "login_unavailable" {
		t.Fatalf("回调落点 = %q", locationOf(t, cb).String())
	}
}

func eMustGet(t *testing.T, c *http.Client, u string) *http.Response {
	t.Helper()
	resp, err := c.Get(u)
	if err != nil {
		t.Fatalf("get %s: %v", u, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// TestPublicOAuthProvidersOnlyListsEnabledConfigured 是登录页入口的数据源契约:
// 没配 / 没启用的档位都不出现;响应里绝不含配置细节。
// 「配了 client_id 却没 secret」这一档在写入侧就被 ErrClientSecretRequired 挡掉了(见 oauth 层),
// 库里根本存不出这种半套配置 —— 所以清单只需按 enabled 筛,不必再防半套行。
func TestPublicOAuthProvidersOnlyListsEnabledConfigured(t *testing.T) {
	e := newLoginTestEnv(t, `{"id":17,"username":"y"}`, nil)
	if _, err := e.oauth.SaveLoginApp(context.Background(), oauth.SaveLoginAppInput{
		Provider: oauth.ProviderGitHub, ClientID: "gh-cid", ClientSecret: sp("gh-secret"), Enabled: false,
	}); err != nil {
		t.Fatalf("存 github(未启用): %v", err)
	}
	if _, err := e.oauth.SaveLoginApp(context.Background(), oauth.SaveLoginAppInput{
		Provider: oauth.ProviderGitee, ClientID: "gitee-cid", ClientSecret: sp("gitee-secret"), Enabled: true,
	}); err != nil {
		t.Fatalf("存 gitee: %v", err)
	}
	// 启用的是 custom + gitee;github 配全了但没启用,必须不出现。
	resp := e.get(t, newTestClient(t), e.srv.URL+"/api/auth/oauth/providers")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("公开端点应 200,status = %d", resp.StatusCode)
	}
	var out []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("入口清单 = %v, want custom + gitee 两条", out)
	}
	for _, item := range out {
		if item["provider"] == oauth.ProviderGitHub {
			t.Fatalf("未启用的 github 出现在公开清单里: %v", out)
		}
		if label, _ := item["label"].(string); label == "" {
			t.Fatalf("入口应带展示名: %v", item)
		}
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "gh-cid") || strings.Contains(string(raw), "login-cid") {
		t.Fatalf("公开端点泄漏 client_id: %s", raw)
	}
}

// ---- 已登录:绑定 ----

// TestOAuthBindAttachesIdentityToLoggedInAccount 是绑定主路径:
// 已登录账号绑上外部身份后,换一台浏览器用同一身份「登录」,拿到的必须还是同一个账号、同一个角色。
func TestOAuthBindAttachesIdentityToLoggedInAccount(t *testing.T) {
	e := newLoginTestEnv(t, `{"id":21,"username":"binder","email":"binder@corp.example.com"}`, nil)
	admin := newTestClient(t)
	loginWithClient(t, admin, e.srv.URL)

	resp := e.bindOnce(t, admin, oauth.ProviderCustom)
	if loc := locationOf(t, resp); loc.Path != accountPagePath || loc.Query().Get("bound") != oauth.ProviderCustom {
		t.Fatalf("绑定落点 = %q", loc.String())
	}
	entries := e.auditOf(t, audit.ActionIdentityBind)
	if len(entries) != 1 || entries[0].Actor != "admin" || entries[0].Detail["externalId"] != "21" {
		t.Fatalf("绑定审计不符: %+v", entries)
	}

	listResp := e.get(t, admin, e.srv.URL+"/api/account/identities")
	var bindings []map[string]any
	if err := json.NewDecoder(listResp.Body).Decode(&bindings); err != nil {
		t.Fatalf("decode identities: %v", err)
	}
	if len(bindings) != 1 || bindings[0]["externalId"] != "21" {
		t.Fatalf("身份清单 = %v", bindings)
	}

	// 绑定不改角色:admin 绑上去之后仍然以 admin 登进来(而不是被自动建号那档 viewer 顶掉)。
	other := newTestClient(t)
	if cookieValue(e.loginOnce(t, other, oauth.ProviderCustom), cookieSession) == "" {
		t.Fatalf("已绑定的身份应能直接登录")
	}
	if got := e.sessionOf(t, other); got.Username != "admin" || got.Role != "admin" {
		t.Fatalf("登录后应落到绑定前的账号,得到 %+v", got)
	}
}

// TestBindIntentStaysWithinItsOwnFlow:「绑定账号」与「连接凭据」两条流共用同一个回调地址,
// state 里记的意图是唯一分隔。这条用例测的就是分隔本身的两半:
//   - 绑定意图的回调只做绑定,绝不落到「把 access_token 存进保险库」那一支;
//   - 登录意图(匿名流)的 state 拿到已登录回调里去用 → 直接拒(意图不匹配,也消费不掉)。
func TestBindIntentStaysWithinItsOwnFlow(t *testing.T) {
	e := newLoginTestEnv(t, `{"id":22,"username":"cross"}`, nil)
	admin := newTestClient(t)
	loginWithClient(t, admin, e.srv.URL)

	before := e.count(t, `SELECT COUNT(1) FROM credentials`)
	resp := e.get(t, admin, e.srv.URL+"/api/oauth/"+oauth.ProviderCustom+"/bind")
	state := locationOf(t, resp).Query().Get("state")
	done := e.get(t, admin, e.vaultCallbackURL(oauth.ProviderCustom, "stub-code", state))

	if loc := locationOf(t, done); loc.Path != accountPagePath || loc.Query().Get("bound") == "" {
		t.Fatalf("绑定意图的回调应落账户页,得到 %q", loc.String())
	}
	if after := e.count(t, `SELECT COUNT(1) FROM credentials`); after != before {
		t.Fatalf("绑定意图不该往保险库写凭据:%d → %d", before, after)
	}

	// 登录流的 state(绑浏览器 nonce,不绑会话 token)拿到认证回调里用:消费不掉,必须拒。
	anon := newTestClient(t)
	loginState := e.authorize(t, anon, oauth.ProviderCustom, "")
	crossed := e.get(t, admin, e.vaultCallbackURL(oauth.ProviderCustom, "stub-code", loginState))
	if errorReason(t, crossed) != "invalid_state" {
		t.Fatalf("登录 state 走认证回调的落点 = %q", locationOf(t, crossed).String())
	}
	if after := e.count(t, `SELECT COUNT(1) FROM credentials`); after != before {
		t.Fatalf("被拒的回调不该写凭据:%d → %d", before, after)
	}
	if n := e.count(t, `SELECT COUNT(1) FROM user_identities`); n != 1 {
		t.Fatalf("被拒的回调不该再多写身份映射,现有 %d 条(只有上面那次绑定)", n)
	}
}

// TestVaultStateCannotBindIdentity 是另一半:凭据意图的 state 在同一回调地址上只落凭据,
// 不许顺手把外部身份绑到当前账号上(那等于用户只是想加个 git 令牌就多一条登录路径)。
func TestVaultStateCannotBindIdentity(t *testing.T) {
	e := newLoginTestEnv(t, `{"id":23,"username":"vaultonly"}`, nil)
	admin := newTestClient(t)
	loginWithClient(t, admin, e.srv.URL)

	resp := e.get(t, admin, e.srv.URL+"/api/oauth/"+oauth.ProviderCustom+"/authorize")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("凭据流 authorize status = %d", resp.StatusCode)
	}
	state := locationOf(t, resp).Query().Get("state")
	e.get(t, admin, e.vaultCallbackURL(oauth.ProviderCustom, "stub-code", state))

	if e.count(t, `SELECT COUNT(1) FROM credentials`) == 0 {
		t.Fatalf("(前置)凭据流本身应已存下一条 git_token")
	}
	if n := e.count(t, `SELECT COUNT(1) FROM user_identities`); n != 0 {
		t.Fatalf("凭据 state 不该产出身份映射,得到 %d 条", n)
	}
}

// TestOAuthBindRequiresSession 验证未登录时绑定入口直接 401(而不是发一条无主 state)。
func TestOAuthBindRequiresSession(t *testing.T) {
	e := newLoginTestEnv(t, `{"id":24,"username":"anon"}`, nil)
	resp, err := http.Get(e.srv.URL + "/api/oauth/" + oauth.ProviderCustom + "/bind")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未登录绑定 status = %d, want 401", resp.StatusCode)
	}
}

// ---- 本人:身份清单与解绑 ----

// TestUnbindIdentityIsScopedToOwner 覆盖越权解绑(403/404)、CSRF 门禁与自己那条解得掉。
func TestUnbindIdentityIsScopedToOwner(t *testing.T) {
	e := newLoginTestEnv(t, `{"id":25,"username":"owner","email":"o@corp.example.com"}`, nil)
	admin := newTestClient(t)
	loginWithClient(t, admin, e.srv.URL)
	e.bindOnce(t, admin, oauth.ProviderCustom)

	var bindingID string
	if err := e.db.QueryRow(`SELECT id FROM user_identities WHERE external_id = '25'`).Scan(&bindingID); err != nil {
		t.Fatalf("读绑定行: %v", err)
	}

	e.createUser(t, "peer", "peer-pass-123", "user")
	peer := newTestClient(t)
	peerCSRF := e.loginAs(t, peer, "peer", "peer-pass-123")

	// 解别人的绑定 → 404(identity.Unbind 带 user_id 条件,不暴露存在性)。
	resp := doJSON(t, peer, http.MethodDelete, e.srv.URL+"/api/account/identities/"+bindingID, peerCSRF, "")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("解别人的绑定 status = %d, want 404", resp.StatusCode)
	}
	if n := e.count(t, `SELECT COUNT(1) FROM user_identities`); n != 1 {
		t.Fatalf("越权解绑不该删掉任何东西,现有 %d 条", n)
	}

	// 带错 CSRF → 403(解绑是写操作)。
	badCSRF := doJSON(t, admin, http.MethodDelete, e.srv.URL+"/api/account/identities/"+bindingID, "wrong-csrf", "")
	_ = badCSRF.Body.Close()
	if badCSRF.StatusCode != http.StatusForbidden {
		t.Fatalf("缺/错 CSRF 的解绑 status = %d, want 403", badCSRF.StatusCode)
	}

	// 本人解绑 → 204,并记下是谁解的。
	own := doJSON(t, admin, http.MethodDelete, e.srv.URL+"/api/account/identities/"+bindingID, e.csrfOf(t, admin), "")
	_ = own.Body.Close()
	if own.StatusCode != http.StatusNoContent {
		t.Fatalf("本人解绑 status = %d, want 204", own.StatusCode)
	}
	if n := e.count(t, `SELECT COUNT(1) FROM user_identities`); n != 0 {
		t.Fatalf("解绑后仍有 %d 条映射", n)
	}
	entries := e.auditOf(t, audit.ActionIdentityUnbind)
	if len(entries) != 1 || entries[0].Actor != "admin" || entries[0].Detail["bindingId"] != bindingID {
		t.Fatalf("解绑审计不符: %+v", entries)
	}
}

// TestListIdentitiesRequiresAuth 验证身份清单是认证端点(匿名拿不到)。
func TestListIdentitiesRequiresAuth(t *testing.T) {
	e := newLoginTestEnv(t, `{"id":26,"username":"z"}`, nil)
	resp, err := http.Get(e.srv.URL + "/api/account/identities")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

// ---- 管理员:登录应用配置 ----

// TestSaveLoginAppHttpBoundaries 覆盖管理员侧四件事:
// 响应无明文 secret、回调地址随响应给出、域名配错回可翻译的 422、非管理员一律 403。
func TestSaveLoginAppHttpBoundaries(t *testing.T) {
	e := newLoginTestEnv(t, `{"id":27,"username":"cfg"}`, nil)
	admin := newTestClient(t)
	loginWithClient(t, admin, e.srv.URL)
	csrf := e.csrfOf(t, admin)

	resp := doJSON(t, admin, http.MethodPut,
		e.srv.URL+"/api/admin/oauth/login-apps/"+oauth.ProviderGitHub, csrf,
		`{"clientId":"gh-cid","clientSecret":"ADMIN-SECRET-PLAINTEXT","allowedDomains":["corp.example.com"],"enabled":true}`)
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, body=%s", resp.StatusCode, raw)
	}
	if strings.Contains(string(raw), "ADMIN-SECRET-PLAINTEXT") {
		t.Fatalf("响应泄漏明文 client_secret: %s", raw)
	}
	var dto oauthLoginAppDTO
	if err := json.Unmarshal(raw, &dto); err != nil {
		t.Fatalf("decode dto: %v", err)
	}
	if !dto.Configured || !dto.Enabled || len(dto.AllowedDomains) != 1 || dto.MaskedSecret == "" {
		t.Fatalf("DTO 不符: %+v", dto)
	}
	// 回调地址要跟着响应给出:一个 OAuth 应用通常只能登记一个回调,填错了最难排查。
	if !strings.HasSuffix(dto.CallbackURL, "/api/auth/oauth/github/callback") {
		t.Fatalf("callbackUrl = %q,应以登录回调路径结尾", dto.CallbackURL)
	}
	if strings.Contains(dto.CallbackURL, "/api/oauth/") {
		t.Fatalf("登录回调不该指到凭据那条流: %q", dto.CallbackURL)
	}

	// 域名写错:422 + 可判定的 code(前端据此翻译),而不是 500。
	bad := doJSON(t, admin, http.MethodPut,
		e.srv.URL+"/api/admin/oauth/login-apps/"+oauth.ProviderGitLab, csrf,
		`{"clientId":"gl-cid","clientSecret":"s","allowedDomains":["https://corp.example.com"],"enabled":true}`)
	badBody, _ := io.ReadAll(bad.Body)
	_ = bad.Body.Close()
	if bad.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("非法域名 status = %d, want 422, body=%s", bad.StatusCode, badBody)
	}
	if !strings.Contains(string(badBody), "invalid_domain") {
		t.Fatalf("非法域名应回 invalid_domain,得到 %s", badBody)
	}
	if n := e.count(t, `SELECT COUNT(1) FROM oauth_login_apps WHERE provider = 'gitlab'`); n != 0 {
		t.Fatalf("校验失败不该落库,gitlab 行数 = %d", n)
	}

	// 普通用户:列表与保存都 403。
	e.createUser(t, "viewer1", "viewer-pass-123", identity.DefaultRole)
	peer := newTestClient(t)
	peerCSRF := e.loginAs(t, peer, "viewer1", "viewer-pass-123")
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/admin/oauth/login-apps", ""},
		{http.MethodPut, "/api/admin/oauth/login-apps/github", `{}`},
	} {
		r := doJSON(t, peer, c.method, e.srv.URL+c.path, peerCSRF, c.body)
		_ = r.Body.Close()
		if r.StatusCode != http.StatusForbidden {
			t.Fatalf("%s %s status = %d, want 403(非管理员)", c.method, c.path, r.StatusCode)
		}
	}

	// 管理员清单:custom(装配时存的)+ github(本轮存的);失败的 gitlab 不在内。
	list := e.get(t, admin, e.srv.URL+"/api/admin/oauth/login-apps")
	var apps []map[string]any
	if err := json.NewDecoder(list.Body).Decode(&apps); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(apps) != 2 {
		t.Fatalf("登录应用清单 = %d 条, want 2: %+v", len(apps), apps)
	}
	all, _ := json.Marshal(apps)
	if strings.Contains(string(all), "ADMIN-SECRET-PLAINTEXT") {
		t.Fatalf("清单泄漏明文 secret: %s", all)
	}
}
