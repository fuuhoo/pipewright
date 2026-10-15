// oauth_login_oidc_test.go 把 OIDC 身份源放到**整条 HTTP 链**上验一遍:
// 配置 → 登录页入口 → 授权 → 回调 → 会话/审计。
//
// 领域侧的端点解析与主键契约在 internal/oauth/oidc_test.go 已经冻结;这里要测的是
// 那些契约**接上装配之后**还成立 —— 公开端点认不认 oidc 这个 provider 名、
// 配置页的字段回不回得来、失败时跳回登录页带的是哪个原因码。
package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/identity"
	"github.com/fuuhoo/pipewright/internal/oauth"
)

// stubOidcIdP 是 Keycloak 形态的 OIDC 身份源:发现文档 + token + userinfo。
// 三个端点都指向自己,由 httptest 分配的地址现拼 —— 测的就是「照文档拨号」而非「按 issuer 猜」。
type stubOidcIdP struct {
	srv      *httptest.Server
	userBody string
	// discoveryDown 模拟 IdP 升级后把发现文档挪走了 —— 配置时好的,用的时候取不到。
	discoveryDown bool
	// userinfoCalls 记录每次身份请求的 Bearer,用于断言「真打了 userinfo 端点」。
	userinfoCalls []string
}

func newStubOidcIdP(t *testing.T, userBody string) *stubOidcIdP {
	t.Helper()
	m := &stubOidcIdP{userBody: userBody}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		if m.discoveryDown {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issuer":"` + m.srv.URL + `",` +
			`"authorization_endpoint":"` + m.srv.URL + `/realms/master/protocol/openid-connect/auth",` +
			`"token_endpoint":"` + m.srv.URL + `/realms/master/protocol/openid-connect/token",` +
			`"userinfo_endpoint":"` + m.srv.URL + `/realms/master/protocol/openid-connect/userinfo"}`))
	})
	mux.HandleFunc("/realms/master/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"oidc-access-token"}`))
	})
	mux.HandleFunc("/realms/master/protocol/openid-connect/userinfo", func(w http.ResponseWriter, r *http.Request) {
		m.userinfoCalls = append(m.userinfoCalls, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(m.userBody))
	})
	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

func (m *stubOidcIdP) discoveryURL() string { return m.srv.URL + "/.well-known/openid-configuration" }

// setUserBody 换掉 userinfo 的响应(模拟平台侧改了登录名,而 sub 不变)。
// 请求严格串行,所以直接改字段即可。
func (m *stubOidcIdP) setUserBody(body string) { m.userBody = body }

// newOidcLoginTestEnv 装配一个只配了 OIDC 登录应用的实例。
// 刻意不配 git 应用:OIDC 本就不属于仓库凭据那条流,配上了等于测试自己给它开门。
func newOidcLoginTestEnv(t *testing.T, userBody string, allowedDomains []string) (*loginTestEnv, *stubOidcIdP) {
	t.Helper()
	idp := newStubOidcIdP(t, userBody)
	e := buildLoginEnv(t, idp.srv.Client(), func(svc oauth.Service) {
		if _, err := svc.SaveLoginApp(context.Background(), oauth.SaveLoginAppInput{
			Provider: oauth.ProviderOIDC, DiscoveryURL: idp.discoveryURL(),
			ClientID: "oidc-cid", ClientSecret: sp("oidc-secret"),
			AllowedDomains: sps(allowedDomains), Enabled: true,
		}); err != nil {
			t.Fatalf("SaveLoginApp(oidc): %v", err)
		}
	})
	return e, idp
}

// TestOidcLoginEndToEnd 是 OIDC 的主路径:登录页出入口 → 匿名授权 → 回调自动建号 → 真会话。
func TestOidcLoginEndToEnd(t *testing.T) {
	e, idp := newOidcLoginTestEnv(t,
		`{"sub":"keycloak-uuid-77","preferred_username":"carol","email":"carol@corp.example.com","name":"Carol"}`, nil)

	// 1) 公开入口清单:label 由服务端给,前端不必自己拼名字。
	provs := e.get(t, newTestClient(t), e.srv.URL+"/api/auth/oauth/providers")
	var items []publicOAuthProviderDTO
	if err := json.NewDecoder(provs.Body).Decode(&items); err != nil {
		t.Fatalf("decode providers: %v", err)
	}
	if len(items) != 1 || items[0].Provider != oauth.ProviderOIDC || items[0].Label != "OIDC" {
		t.Fatalf("登录页入口 = %+v, want 一条 oidc/OIDC", items)
	}

	// 2) 发起授权:302 的落点由服务端拼好(带 state/redirect_uri),前端只负责跳。
	c := newTestClient(t)
	state := e.authorize(t, c, oauth.ProviderOIDC, "/projects")
	if state == "" {
		t.Fatalf("授权 URL 未带上 state")
	}

	// 3) 回调:自动建号(viewer)+ 签会话 + 落到 state 里记的原始页面。
	resp := e.get(t, c, e.loginCallbackURL(oauth.ProviderOIDC, "oidc-code", state))
	if resp.StatusCode != http.StatusFound {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("callback status = %d, want 302, body=%s", resp.StatusCode, raw)
	}
	if got := locationOf(t, resp).String(); got != "/projects" {
		t.Fatalf("落地页 = %q, want 回跳原页面", got)
	}
	if cookieValue(resp, cookieSession) == "" {
		t.Fatalf("回调应下发会话 cookie")
	}
	if got := e.sessionOf(t, c); got.Username != "carol" || got.Role != identity.DefaultRole {
		t.Fatalf("会话身份 = %+v, want carol/%s", got, identity.DefaultRole)
	}
	if len(idp.userinfoCalls) != 1 || !strings.HasPrefix(idp.userinfoCalls[0], "Bearer ") {
		t.Fatalf("应带 Bearer 请求 userinfo 端点: %v", idp.userinfoCalls)
	}

	entries := e.auditOf(t, audit.ActionOauthLogin)
	if len(entries) != 1 {
		t.Fatalf("登录审计条数 = %d, want 1", len(entries))
	}
	// externalId 记的是 sub(主键声明),不是可能被人改掉的 preferred_username。
	if entries[0].Detail["externalId"] != "keycloak-uuid-77" || entries[0].Detail["provider"] != oauth.ProviderOIDC {
		t.Fatalf("审计 detail 不符: %+v", entries[0].Detail)
	}
}

// TestOidcSecondLoginReusesAccount 证明认人靠 (provider, sub):
// 平台侧改了登录名既不会多出一个账号,也不会认成别人。
func TestOidcSecondLoginReusesAccount(t *testing.T) {
	e, idp := newOidcLoginTestEnv(t,
		`{"sub":"stable-1","preferred_username":"old-name","email":"a@corp.example.com"}`, nil)

	first := newTestClient(t)
	if cookieValue(e.loginOnce(t, first, oauth.ProviderOIDC), cookieSession) == "" {
		t.Fatalf("首次 OIDC 登录应签发会话")
	}

	idp.setUserBody(`{"sub":"stable-1","preferred_username":"new-name","email":"a@corp.example.com"}`)
	second := newTestClient(t)
	if cookieValue(e.loginOnce(t, second, oauth.ProviderOIDC), cookieSession) == "" {
		t.Fatalf("平台改名后仍应能登录(sub 没变)")
	}
	if got := e.sessionOf(t, second); got.Username != "old-name" {
		t.Fatalf("认人应按 sub 命中既有账号,本地用户名不该跟着平台改名走: %+v", got)
	}
	if n := e.count(t, `SELECT COUNT(1) FROM users WHERE username IN ('old-name','new-name')`); n != 1 {
		t.Fatalf("改名不该多出一个账号,现有 %d 个", n)
	}
}

// TestOidcLoginDomainWhitelistApplies:通用身份源最容易「谁都能登」,
// 所以域名白名单在 OIDC 档必须和 git 档一样硬。
func TestOidcLoginDomainWhitelistApplies(t *testing.T) {
	e, _ := newOidcLoginTestEnv(t,
		`{"sub":"outsider","preferred_username":"outsider","email":"me@other.example.org"}`,
		[]string{"corp.example.com"})

	c := newTestClient(t)
	resp := e.loginOnce(t, c, oauth.ProviderOIDC)
	if errorReason(t, resp) != "domain_not_allowed" {
		t.Fatalf("落点 = %q, want domain_not_allowed", locationOf(t, resp).String())
	}
	if cookieValue(resp, cookieSession) != "" {
		t.Fatalf("白名单外的身份不该拿到会话")
	}
	if n := e.count(t, `SELECT COUNT(1) FROM user_identities`); n != 0 {
		t.Fatalf("被白名单拒了的身份不该留下映射,现有 %d 条", n)
	}
}

// TestOidcLoginMissingSubFailsClosed 是「按登录名认人」这条退路的反面用例:
// userinfo 没有 sub 时宁可登不进去,也不能拿 preferred_username 当主键。
func TestOidcLoginMissingSubFailsClosed(t *testing.T) {
	e, _ := newOidcLoginTestEnv(t, `{"preferred_username":"alice","email":"alice@corp.example.com"}`, nil)

	c := newTestClient(t)
	resp := e.loginOnce(t, c, oauth.ProviderOIDC)
	if cookieValue(resp, cookieSession) != "" {
		t.Fatalf("缺主键声明绝不该签发会话")
	}
	if loc := locationOf(t, resp); loc.Path != loginPagePath || errorReason(t, resp) != "identity_unavailable" {
		t.Fatalf("落点 = %q, want /login?oauth_error=identity_unavailable", loc.String())
	}
	if n := e.count(t, `SELECT COUNT(1) FROM users`); n != 1 {
		t.Fatalf("失败的登录不该建号,库里 %d 个账号(期望只有 bootstrap admin)", n)
	}
}

// TestOidcBindRouteIsRejected 冻结 OIDC 的能力边界:绑定走的是仓库凭据那条流,
// OIDC 给不了 git token,所以 /api/oauth/oidc/bind 必须是明确的拒绝,而不是 500。
func TestOidcBindRouteIsRejected(t *testing.T) {
	e, _ := newOidcLoginTestEnv(t, `{"sub":"s-1","preferred_username":"u"}`, nil)

	admin := newTestClient(t)
	loginWithClient(t, admin, e.srv.URL)
	resp := e.get(t, admin, e.srv.URL+"/api/oauth/"+oauth.ProviderOIDC+"/bind")
	if resp.StatusCode == http.StatusFound {
		t.Fatalf("oidc 不该能走 git 凭据那条绑定流")
	}
	if resp.StatusCode >= 500 {
		t.Fatalf("拒绝该给人读错误码,得到 %d", resp.StatusCode)
	}
}

// TestOidcSaveViaHttpRoundTripsEndpoints 覆盖配置页关心的三件事:
// 字段回得来、回调地址给得对、配错时是 422 + 可翻译的 code(而不是留下一张坏卡)。
func TestOidcSaveViaHttpRoundTripsEndpoints(t *testing.T) {
	e, idp := newOidcLoginTestEnv(t, `{"sub":"s-2","preferred_username":"u"}`, nil)
	admin := newTestClient(t)
	loginWithClient(t, admin, e.srv.URL)
	csrf := e.csrfOf(t, admin)

	body := `{"clientId":"cid2","clientSecret":"OIDC-SECRET-PLAINTEXT","enabled":true,` +
		`"discoveryUrl":"` + idp.discoveryURL() + `","scopes":"openid profile",` +
		`"idClaim":"employee_id","allowedDomains":["corp.example.com"]}`
	resp := doJSON(t, admin, http.MethodPut,
		e.srv.URL+"/api/admin/oauth/login-apps/"+oauth.ProviderOIDC, csrf, body)
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, body=%s", resp.StatusCode, raw)
	}
	if strings.Contains(string(raw), "OIDC-SECRET-PLAINTEXT") {
		t.Fatalf("响应泄漏明文 client_secret: %s", raw)
	}
	var dto oauthLoginAppDTO
	if err := json.Unmarshal(raw, &dto); err != nil {
		t.Fatalf("decode dto: %v", err)
	}
	if dto.DiscoveryURL != idp.discoveryURL() || dto.Scopes != "openid profile" || dto.IDClaim != "employee_id" {
		t.Fatalf("OIDC 字段应原样回显: %+v", dto)
	}
	// 回调地址是管理员要去 IdP 登记的那一条:一个应用通常只能登记一个,填错最难排查。
	if !strings.HasSuffix(dto.CallbackURL, "/api/auth/oauth/oidc/callback") {
		t.Fatalf("callbackUrl = %q", dto.CallbackURL)
	}

	// 发现文档填成取不到的地址:422 + oidc_config_invalid,库里仍是上一份好配置。
	bad := doJSON(t, admin, http.MethodPut,
		e.srv.URL+"/api/admin/oauth/login-apps/"+oauth.ProviderOIDC, csrf,
		`{"clientId":"cid2","enabled":true,"discoveryUrl":"https://sso.invalid/.well-known/openid-configuration"}`)
	badBody, _ := io.ReadAll(bad.Body)
	_ = bad.Body.Close()
	if bad.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("坏端点 status = %d, want 422, body=%s", bad.StatusCode, badBody)
	}
	if !strings.Contains(string(badBody), "oidc_config_invalid") {
		t.Fatalf("坏端点应回 oidc_config_invalid,得到 %s", badBody)
	}

	list := e.get(t, admin, e.srv.URL+"/api/admin/oauth/login-apps")
	var apps []oauthLoginAppDTO
	if err := json.NewDecoder(list.Body).Decode(&apps); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(apps) != 1 || apps[0].Scopes != "openid profile" {
		t.Fatalf("保存失败不该改动既有配置: %+v", apps)
	}
	all, _ := json.Marshal(apps)
	if strings.Contains(string(all), "OIDC-SECRET-PLAINTEXT") {
		t.Fatalf("清单泄漏明文 secret: %s", all)
	}
}

// TestOidcLoginStartReportsBadEndpoints 覆盖「存的时候好的,IdP 后来把文档挪走了」:
// 点登录要跳回登录页带 oidc_config_invalid,而不是 500 或跳去一个空地址。
func TestOidcLoginStartReportsBadEndpoints(t *testing.T) {
	e, idp := newOidcLoginTestEnv(t, `{"sub":"s-3"}`, nil)
	// 先确认可用状态(否则这条用例可能在测一个从没通过过的配置)。
	if _, err := e.oauth.LoginAuthorizeURL(context.Background(), oauth.ProviderOIDC, "S", "https://app/cb"); err != nil {
		t.Fatalf("配置本应可用: %v", err)
	}
	idp.discoveryDown = true

	resp := e.get(t, newTestClient(t), e.srv.URL+"/api/auth/oauth/"+oauth.ProviderOIDC+"/authorize")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}
	if loc := locationOf(t, resp); loc.Path != loginPagePath || errorReason(t, resp) != "oidc_config_invalid" {
		t.Fatalf("落点 = %q, want /login?oauth_error=oidc_config_invalid", loc.String())
	}
	// 公开入口清单不该跟着炸:登录页仍要能打开(它没有别的登录方式时可回退口令登录)。
	provs := e.get(t, newTestClient(t), e.srv.URL+"/api/auth/oauth/providers")
	var items []publicOAuthProviderDTO
	if err := json.NewDecoder(provs.Body).Decode(&items); err != nil {
		t.Fatalf("decode providers: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("清单只看启用/配全,不该被 IdP 故障清空: %+v", items)
	}
}
