// oidc_test.go 冻结通用 OIDC 身份源那一支的契约:端点从哪来、主键从哪来、配错时在哪一步报错。
//
// 三条不许退化的线:
//  1. 主键只认配置的那个声明(默认 sub),缺失即拒 —— 按 preferred_username 认人是接管面。
//  2. 端点解析失败在「保存」这一刀就报错,而不是等访客点登录才被跳回一句说不清的原因。
//  3. oidc 永远进不了「取仓库凭据」那条流(它压根给不了 git token)。
package oauth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

// stubOidc 是一个 Keycloak 形态的 OIDC 服务器:发现文档 + token + userinfo。
// 端点地址一律指向自己,由 httptest 分配的 host 现拼 —— 发现文档里的地址若是别的 host,
// 测的就不是「照文档拨号」而是「照 host 猜」了。
type stubOidc struct {
	srv         *httptest.Server
	userBody    string
	tokenForms  []url.Values
	bearerSeen  []string
	discoveryOK bool
}

func newStubOidc(t *testing.T, userBody string) *stubOidc {
	t.Helper()
	m := &stubOidc{userBody: userBody, discoveryOK: true}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		if !m.discoveryOK {
			http.Error(w, "unavailable", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issuer":"` + m.srv.URL + `",` +
			`"authorization_endpoint":"` + m.srv.URL + `/auth/realms/master/protocol/openid-connect/auth",` +
			`"token_endpoint":"` + m.srv.URL + `/auth/realms/master/protocol/openid-connect/token",` +
			`"userinfo_endpoint":"` + m.srv.URL + `/auth/realms/master/protocol/openid-connect/userinfo",` +
			`"jwks_uri":"` + m.srv.URL + `/certs"}`))
	})
	mux.HandleFunc("/auth/realms/master/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		m.tokenForms = append(m.tokenForms, r.PostForm)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"oidc-access-token-PLAINTEXT","id_token":"unused"}`))
	})
	mux.HandleFunc("/auth/realms/master/protocol/openid-connect/userinfo", func(w http.ResponseWriter, r *http.Request) {
		m.bearerSeen = append(m.bearerSeen, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(m.userBody))
	})
	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

func (m *stubOidc) discoveryURL() string { return m.srv.URL + "/.well-known/openid-configuration" }

func (m *stubOidc) path(p string) string { return m.srv.URL + p }

// saveOidcApp 存一个启用、配了指向 mock 的 OIDC 登录应用。
func saveOidcApp(t *testing.T, svc Service, in SaveLoginAppInput) *LoginAppConfig {
	t.Helper()
	in.Provider = ProviderOIDC
	if in.ClientID == "" {
		in.ClientID = "oidc-client-id"
	}
	if in.ClientSecret == nil {
		in.ClientSecret = strp("OIDC-SECRET-PLAINTEXT")
	}
	in.Enabled = true
	cfg, err := svc.SaveLoginApp(ctx(), in)
	if err != nil {
		t.Fatalf("SaveLoginApp(oidc): %v", err)
	}
	return cfg
}

const oidcCallback = "https://app.example.com/api/auth/oauth/oidc/callback"

// ---- 端点来源:发现文档 / 显式覆盖 ----

func TestOidcEndpointsFromDiscovery(t *testing.T) {
	stub := newStubOidc(t, `{"sub":"keycloak-uuid-1","preferred_username":"alice","email":"alice@corp.example.com","name":"Alice"}`)
	svc, _, _ := newLoginService(t)
	saveOidcApp(t, svc, SaveLoginAppInput{DiscoveryURL: stub.discoveryURL()})

	authURL, err := svc.LoginAuthorizeURL(ctx(), ProviderOIDC, "STATE-1", oidcCallback)
	if err != nil {
		t.Fatalf("LoginAuthorizeURL: %v", err)
	}
	u, perr := url.Parse(authURL)
	if perr != nil {
		t.Fatalf("parse authorize url: %v", perr)
	}
	// 授权端点必须逐字来自发现文档,而不是据 issuer 猜一个。
	if got := u.Scheme + "://" + u.Host + u.Path; got != stub.path("/auth/realms/master/protocol/openid-connect/auth") {
		t.Fatalf("authorize 端点 = %q,应来自发现文档", got)
	}
	q := u.Query()
	if q.Get("state") != "STATE-1" || q.Get("redirect_uri") != oidcCallback || q.Get("client_id") != "oidc-client-id" {
		t.Fatalf("authorize 参数不符: %v", q)
	}
	if !strings.Contains(q.Get("scope"), "openid") {
		t.Fatalf("OIDC 登录 scope 必须含 openid: %q", q.Get("scope"))
	}

	id, err := svc.ExchangeIdentity(ctx(), ProviderOIDC, "code-1", oidcCallback)
	if err != nil {
		t.Fatalf("ExchangeIdentity: %v", err)
	}
	if id.ExternalID != "keycloak-uuid-1" || id.Login != "alice" || id.Email != "alice@corp.example.com" {
		t.Fatalf("身份解析不符: %+v", id)
	}
	if id.Provider != ProviderOIDC {
		t.Fatalf("Provider 应由兑换填入: %+v", id)
	}
	if len(stub.tokenForms) != 1 || stub.tokenForms[0].Get("client_secret") != "OIDC-SECRET-PLAINTEXT" {
		t.Fatalf("token 请求应带登录应用的 client_secret: %v", stub.tokenForms)
	}
	if len(stub.bearerSeen) != 1 || stub.bearerSeen[0] != "Bearer oidc-access-token-PLAINTEXT" {
		t.Fatalf("userinfo 的 Bearer 不符: %v", stub.bearerSeen)
	}
}

// TestOidcExplicitEndpointsOverrideDiscovery 覆盖「文档指向内网地址、平台只能走公网入口」这类
// 真实形态:手填的授权端点必须赢,没手填的两项仍从文档取。
func TestOidcExplicitEndpointsOverrideDiscovery(t *testing.T) {
	stub := newStubOidc(t, `{"sub":"s-1","preferred_username":"u"}`)
	svc, _, _ := newLoginService(t)
	override := "https://sso.example.net/auth"
	saveOidcApp(t, svc, SaveLoginAppInput{
		DiscoveryURL: stub.discoveryURL(),
		AuthorizeURL: override,
	})

	authURL, err := svc.LoginAuthorizeURL(ctx(), ProviderOIDC, "S", oidcCallback)
	if err != nil {
		t.Fatalf("LoginAuthorizeURL: %v", err)
	}
	if !strings.HasPrefix(authURL, override+"?") {
		t.Fatalf("显式授权端点应覆盖文档值: %q", authURL)
	}
	// 兑换/身份两项没手填 → 仍按发现文档打到 mock。
	if _, err := svc.ExchangeIdentity(ctx(), ProviderOIDC, "code-1", oidcCallback); err != nil {
		t.Fatalf("ExchangeIdentity: %v", err)
	}
	if len(stub.tokenForms) != 1 || len(stub.bearerSeen) != 1 {
		t.Fatalf("未覆盖的端点应回落发现文档: token=%v userinfo=%v", stub.tokenForms, stub.bearerSeen)
	}
}

// TestOidcWorksWithoutDiscovery 证明三个端点填全时完全不外拨文档 ——
// 有些 IDaaS 不提供发现地址,只有手抄的端点。
func TestOidcWorksWithoutDiscovery(t *testing.T) {
	stub := newStubOidc(t, `{"sub":"s-2"}`)
	svc, _, _ := newLoginService(t)
	cfg := saveOidcApp(t, svc, SaveLoginAppInput{
		AuthorizeURL: stub.path("/a"), TokenURL: stub.path("/t"), UserinfoURL: stub.path("/u"),
	})
	if cfg.DiscoveryURL != "" {
		t.Fatalf("没填发现文档就该留空: %q", cfg.DiscoveryURL)
	}
	// 保存期只校验地址形态(能不能外拨),不校验路径真假:
	// 发起登录时把这三个地址原样交给平台去跳,才是这里的契约。
	authURL, err := svc.LoginAuthorizeURL(ctx(), ProviderOIDC, "S", oidcCallback)
	if err != nil {
		t.Fatalf("LoginAuthorizeURL: %v", err)
	}
	if !strings.HasPrefix(authURL, stub.path("/a")+"?") {
		t.Fatalf("授权端点应逐字用配置值: %q", authURL)
	}
}

// ---- 主键声明 ----

// TestOidcRequiresStableClaim 是铁律 1 的 OIDC 落点:缺 sub 就拒,
// 绝不因为 preferred_username 看着像人而放行 —— 那正是 Jpom 按登录名认人的接管面。
func TestOidcRequiresStableClaim(t *testing.T) {
	cases := []struct{ name, body string }{
		{"缺 sub", `{"preferred_username":"alice","email":"alice@corp.example.com"}`},
		{"sub 为空串", `{"sub":"","preferred_username":"alice"}`},
		{"sub 是 null", `{"sub":null,"preferred_username":"alice"}`},
		{"sub 是对象", `{"sub":{"a":1},"preferred_username":"alice"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stub := newStubOidc(t, c.body)
			svc, _, _ := newLoginService(t)
			saveOidcApp(t, svc, SaveLoginAppInput{DiscoveryURL: stub.discoveryURL()})
			_, err := svc.ExchangeIdentity(ctx(), ProviderOIDC, "code-1", oidcCallback)
			if !errors.Is(err, ErrIdentityUnavailable) {
				t.Fatalf("err = %v, want ErrIdentityUnavailable", err)
			}
		})
	}
}

// TestOidcCustomIDClaim 证明主键声明可配:企业 IdP 常把工号放进自定义声明,
// 而 sub 是个没人认得的 uuid。这里必须取 employee_id,不是 sub。
func TestOidcCustomIDClaim(t *testing.T) {
	stub := newStubOidc(t, `{"sub":"ignore-me","employee_id":"E-1001","preferred_username":"bob"}`)
	svc, _, _ := newLoginService(t)
	saveOidcApp(t, svc, SaveLoginAppInput{DiscoveryURL: stub.discoveryURL(), IDClaim: "employee_id"})

	id, err := svc.ExchangeIdentity(ctx(), ProviderOIDC, "code-1", oidcCallback)
	if err != nil {
		t.Fatalf("ExchangeIdentity: %v", err)
	}
	if id.ExternalID != "E-1001" {
		t.Fatalf("主键应取配置的 employee_id,得到 %q", id.ExternalID)
	}
	if id.Login != "bob" {
		t.Fatalf("展示名应取 preferred_username,得到 %q", id.Login)
	}
}

// TestOidcGitlabNicknameIsTheLogin:GitLab 的 claims_supported 里没有 preferred_username,
// 登录名放在 nickname。少了这一档就会回退到 email,把「邮箱去掉 @」当成本地账号名。
// 主键仍只认 sub —— nickname 可以被本人改,拿它认人才是接管面。
func TestOidcGitlabNicknameIsTheLogin(t *testing.T) {
	stub := newStubOidc(t, `{"sub":"gid-77","nickname":"fuuhoo","name":"付洪彬","email":"fuhongbin@swinfo.com.cn"}`)
	svc, _, _ := newLoginService(t)
	saveOidcApp(t, svc, SaveLoginAppInput{DiscoveryURL: stub.discoveryURL()})

	id, err := svc.ExchangeIdentity(ctx(), ProviderOIDC, "code-1", oidcCallback)
	if err != nil {
		t.Fatalf("ExchangeIdentity: %v", err)
	}
	if id.ExternalID != "gid-77" {
		t.Fatalf("主键应取 sub,得到 %q", id.ExternalID)
	}
	if id.Login != "fuuhoo" {
		t.Fatalf("登录名应取 nickname,得到 %q", id.Login)
	}
	if id.Name != "付洪彬" || id.Email != "fuhongbin@swinfo.com.cn" {
		t.Fatalf("显示名/邮箱不该串档: name=%q email=%q", id.Name, id.Email)
	}
}

// TestOidcPreferredUsernameWinsOverNickname:两家都给时按 OIDC 标准声明优先,
// nickname 只是 GitLab 那一路的补位。
func TestOidcPreferredUsernameWinsOverNickname(t *testing.T) {
	stub := newStubOidc(t, `{"sub":"s-9","preferred_username":"std","nickname":"alt","email":"e@corp.example.com"}`)
	svc, _, _ := newLoginService(t)
	saveOidcApp(t, svc, SaveLoginAppInput{DiscoveryURL: stub.discoveryURL()})

	id, err := svc.ExchangeIdentity(ctx(), ProviderOIDC, "code-1", oidcCallback)
	if err != nil {
		t.Fatalf("ExchangeIdentity: %v", err)
	}
	if id.Login != "std" {
		t.Fatalf("preferred_username 应压过 nickname,得到 %q", id.Login)
	}
}

// TestOidcNumericSubNormalized 与 git 档同形:数字型主键归一成字符串,别写出 "1" vs "1.0"。
func TestOidcNumericSubNormalized(t *testing.T) {
	stub := newStubOidc(t, `{"sub":12345,"name":"Num"}`)
	svc, _, _ := newLoginService(t)
	saveOidcApp(t, svc, SaveLoginAppInput{DiscoveryURL: stub.discoveryURL()})
	id, err := svc.ExchangeIdentity(ctx(), ProviderOIDC, "code-1", oidcCallback)
	if err != nil {
		t.Fatalf("ExchangeIdentity: %v", err)
	}
	if id.ExternalID != "12345" {
		t.Fatalf("数字 sub = %q, want \"12345\"", id.ExternalID)
	}
	if id.Login != "Num" {
		t.Fatalf("缺 preferred_username/email 时应回退 name,得到 %q", id.Login)
	}
}

// ---- scope ----

func TestOidcScopesConfigurableAndMinimal(t *testing.T) {
	stub := newStubOidc(t, `{"sub":"s"}`)
	svc, _, _ := newLoginService(t)

	// 不填 → 落最小默认值,并原样回显给配置页。
	cfg := saveOidcApp(t, svc, SaveLoginAppInput{DiscoveryURL: stub.discoveryURL()})
	if cfg.Scopes != defaultOidcScopes || cfg.IDClaim != defaultOidcIDClaim {
		t.Fatalf("默认值不符: scopes=%q idClaim=%q", cfg.Scopes, cfg.IDClaim)
	}

	authURL, err := svc.LoginAuthorizeURL(ctx(), ProviderOIDC, "S", oidcCallback)
	if err != nil {
		t.Fatalf("LoginAuthorizeURL: %v", err)
	}
	scope := firstQuery(t, authURL, "scope")
	if !strings.Contains(scope, "openid") || !strings.Contains(scope, "profile") {
		t.Fatalf("默认 scope 应含 openid profile,得到 %q", scope)
	}

	// 填了 → 完全按配置(包括故意只给 openid 的紧凑形态)。
	saveOidcApp(t, svc, SaveLoginAppInput{DiscoveryURL: stub.discoveryURL(), Scopes: "openid email"})
	authURL2, err := svc.LoginAuthorizeURL(ctx(), ProviderOIDC, "S2", oidcCallback)
	if err != nil {
		t.Fatalf("LoginAuthorizeURL(自定义 scope): %v", err)
	}
	if got := firstQuery(t, authURL2, "scope"); !strings.Contains(got, "email") || strings.Contains(got, "profile") {
		t.Fatalf("自定义 scope 应逐字生效,得到 %q", got)
	}
}

// TestOidcDeclaredGitShapeUpgradesLoginScope 冻结「声明同为 git 平台」这一动作的全部后果:
// 只有同时满足「声明了 git 形态」和「开了登录即同步凭据」,授权才从只认身份升到能读仓库,
// 兑换回来的令牌才会作为 grant 交给调用方落库。缺任何一个条件都只拿到身份,不多要权限。
func TestOidcDeclaredGitShapeUpgradesLoginScope(t *testing.T) {
	stub := newStubOidc(t, `{"sub":"gl-42","preferred_username":"fan","email":"fan@corp.example.com"}`)
	svc, _, _ := newLoginService(t)

	authScope := func(state string) string {
		t.Helper()
		u, err := svc.LoginAuthorizeURL(ctx(), ProviderOIDC, state, oidcCallback)
		if err != nil {
			t.Fatalf("LoginAuthorizeURL(%s): %v", state, err)
		}
		return firstQuery(t, u, "scope")
	}

	// 1) 没声明也没开同步:只有身份 scope,兑换不产出 grant。
	saveOidcApp(t, svc, SaveLoginAppInput{DiscoveryURL: stub.discoveryURL()})
	if scope := authScope("S-plain"); strings.Contains(scope, "read_repository") {
		t.Fatalf("未声明形态不该要仓库读权限,scope=%q", scope)
	}
	if _, grant, err := svc.ExchangeLogin(ctx(), ProviderOIDC, "code-1", oidcCallback); err != nil {
		t.Fatalf("ExchangeLogin(未声明): %v", err)
	} else if grant != nil {
		t.Fatal("没开同步不该回吐 grant")
	}

	// 2) 声明了形态但没开同步:scope 依旧只认身份 —— 多要的权限换不来东西。
	saveOidcApp(t, svc, SaveLoginAppInput{DiscoveryURL: stub.discoveryURL(), GitShape: GitShapeGitLab})
	if scope := authScope("S-shape-only"); strings.Contains(scope, "read_repository") {
		t.Fatalf("开关没打开时不该升 scope,scope=%q", scope)
	}

	// 3) 声明 + 开同步:两者齐活才升到 GitLab 那档仓库读权限,且身份 scope 一个不丢。
	cfg := saveOidcApp(t, svc, SaveLoginAppInput{
		DiscoveryURL: stub.discoveryURL(), GitShape: GitShapeGitLab, SyncCredentials: true,
	})
	if cfg.GitShape != GitShapeGitLab || !cfg.SyncCredentials {
		t.Fatalf("声明与开关未原样回显: gitShape=%q sync=%v", cfg.GitShape, cfg.SyncCredentials)
	}
	scope := authScope("S-sync")
	for _, want := range []string{"openid", "profile", "read_repository", "read_user"} {
		if !strings.Contains(scope, want) {
			t.Fatalf("声明 GitLab 后 scope 应含 %q,得到 %q", want, scope)
		}
	}
	if _, grant, err := svc.ExchangeLogin(ctx(), ProviderOIDC, "code-2", oidcCallback); err != nil {
		t.Fatalf("ExchangeLogin(声明+同步): %v", err)
	} else if grant == nil || grant.AccessToken == "" {
		t.Fatal("声明+同步应把令牌作为 grant 交回调用方")
	}

	// 4) 声明成 Gitea 时按 Gitea 的 scope 命名,不复用 GitLab 的下划线写法。
	saveOidcApp(t, svc, SaveLoginAppInput{
		DiscoveryURL: stub.discoveryURL(), GitShape: GitShapeGitea, SyncCredentials: true,
	})
	if giteaScope := authScope("S-gitea"); !strings.Contains(giteaScope, "read:repository") ||
		strings.Contains(giteaScope, "read_repository") {
		t.Fatalf("Gitea 形态应申请 read:repository,得到 %q", giteaScope)
	}

	// 5) 没声明就想开同步 → 保存期硬拒,绝不静默把开关关掉。
	_, err := svc.SaveLoginApp(ctx(), SaveLoginAppInput{
		Provider: ProviderOIDC, ClientID: "oidc-client-id", ClientSecret: strp("OIDC-SECRET-PLAINTEXT"),
		DiscoveryURL: stub.discoveryURL(), Enabled: true, SyncCredentials: true,
	})
	if !errors.Is(err, ErrCredentialSyncNeedsGitPlatform) {
		t.Fatalf("未声明形态开同步应拒为 ErrCredentialSyncNeedsGitPlatform,得到 %v", err)
	}
}

func firstQuery(t *testing.T, rawURL, key string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	return u.Query().Get(key)
}

// ---- 保存期校验:配错当场说清楚 ----

func TestSaveLoginAppOidcValidatesEndpoints(t *testing.T) {
	stub := newStubOidc(t, `{"sub":"s"}`)
	svc, _, _ := newLoginService(t)

	cases := []struct {
		name string
		in   SaveLoginAppInput
	}{
		{"什么都没填", SaveLoginAppInput{}},
		{"只有发现文档但不是 http(s)", SaveLoginAppInput{DiscoveryURL: "ftp://sso.example.com/.well-known"}},
		{"发现文档带凭据", SaveLoginAppInput{DiscoveryURL: "http://admin:pass@sso.example.com/.well-known"}},
		{"三个端点凑不齐", SaveLoginAppInput{AuthorizeURL: "https://sso/a", TokenURL: "https://sso/t"}},
		{"显式端点形态不对", SaveLoginAppInput{AuthorizeURL: "sso:a", TokenURL: "https://sso/t", UserinfoURL: "https://sso/u"}},
		{"发现文档取不到", SaveLoginAppInput{DiscoveryURL: stub.path("/nope")}},
		{"非 http(s) 的显式端点", SaveLoginAppInput{AuthorizeURL: "https://sso/a", TokenURL: "file:///etc/passwd", UserinfoURL: "https://sso/u"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := c.in
			in.Provider = ProviderOIDC
			in.ClientID = "oidc-client-id"
			in.ClientSecret = strp("s")
			in.Enabled = true
			_, err := svc.SaveLoginApp(ctx(), in)
			if !errors.Is(err, ErrOidcConfigInvalid) {
				t.Fatalf("err = %v, want ErrOidcConfigInvalid", err)
			}
			if strings.Contains(err.Error(), "PLAINTEXT") || strings.Contains(err.Error(), "pass") {
				t.Fatalf("错误不该带上游原文或凭据: %v", err)
			}
		})
	}

	// 发现文档可达但三个端点缺一项,同样算配错(不猜、不补)。
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"authorization_endpoint":"https://sso/a","token_endpoint":"https://sso/t"}`))
	}))
	t.Cleanup(broken.Close)
	if _, err := svc.SaveLoginApp(ctx(), SaveLoginAppInput{
		Provider: ProviderOIDC, ClientID: "c", ClientSecret: strp("s"), Enabled: true,
		DiscoveryURL: broken.URL,
	}); !errors.Is(err, ErrOidcConfigInvalid) {
		t.Fatalf("文档缺 userinfo_endpoint err = %v, want ErrOidcConfigInvalid", err)
	}

	// 校验失败绝不能留下半成品行:否则配置页出现一张点了就报错的卡。
	row, err := svc.LoginApp(ctx(), ProviderOIDC)
	if err != nil {
		t.Fatalf("LoginApp: %v", err)
	}
	if row.Configured {
		t.Fatalf("全部保存都失败时不该有已配置行: %+v", row)
	}
}

// TestOidcDiscoveryFailsAtUseTimeRejected 覆盖「存的时候好好的,IdP 后来挂了」:
// 发起登录时解析失败要返回 ErrOidcConfigInvalid,而不是带着空端点跳出去。
func TestOidcDiscoveryFailsAtUseTimeRejected(t *testing.T) {
	stub := newStubOidc(t, `{"sub":"s"}`)
	svc, _, _ := newLoginService(t)
	saveOidcApp(t, svc, SaveLoginAppInput{DiscoveryURL: stub.discoveryURL()})

	stub.discoveryOK = false // IdP 的发现文档下线
	if _, err := svc.LoginAuthorizeURL(ctx(), ProviderOIDC, "S", oidcCallback); !errors.Is(err, ErrOidcConfigInvalid) {
		t.Fatalf("err = %v, want ErrOidcConfigInvalid", err)
	}
	if _, err := svc.ExchangeIdentity(ctx(), ProviderOIDC, "code-1", oidcCallback); !errors.Is(err, ErrOidcConfigInvalid) {
		t.Fatalf("err = %v, want ErrOidcConfigInvalid", err)
	}
}

// ---- 边界:OIDC 不许出现在「取仓库凭据」那条流 ----

func TestOidcRejectedByGitTokenFlow(t *testing.T) {
	if _, err := normalizeProvider(ProviderOIDC); !errors.Is(err, ErrInvalidProvider) {
		t.Fatalf("git 枚举该拒 oidc,得到 %v", err)
	}
	if _, err := resolveProvider(ProviderOIDC, "https://sso.example.com", ""); !errors.Is(err, ErrInvalidProvider) {
		t.Fatalf("resolveProvider 该拒 oidc,得到 %v", err)
	}
	svc, _, _ := newLoginService(t)
	// 仓库凭据应用(oauth_apps + vault)那张表:同一 provider 名走进去必须是非法枚举。
	if _, err := svc.SaveApp(ctx(), SaveAppInput{
		Provider: ProviderOIDC, ClientID: "c", ClientSecret: strp("s"), Enabled: true,
	}); !errors.Is(err, ErrInvalidProvider) {
		t.Fatalf("SaveApp(oidc) err = %v, want ErrInvalidProvider", err)
	}
	// 反过来:登录流认得 oidc。
	if p, err := normalizeLoginProvider(ProviderOIDC); err != nil || p != ProviderOIDC {
		t.Fatalf("normalizeLoginProvider(oidc) = %q, %v", p, err)
	}
}

// TestGitProvidersNeverPersistOidcFields 保证两套配置不会互相污染:
// 前端表单在档位间切换时可能残留上一档的端点,保存期一律抹掉。
func TestGitProvidersNeverPersistOidcFields(t *testing.T) {
	svc, db, _ := newLoginService(t)
	cfg, err := svc.SaveLoginApp(ctx(), SaveLoginAppInput{
		Provider: ProviderGitHub, ClientID: "gh", ClientSecret: strp("s"), Enabled: true,
		DiscoveryURL: "https://sso/.well-known/openid-configuration", IDClaim: "employee_id", Scopes: "openid",
	})
	if err != nil {
		t.Fatalf("SaveLoginApp(github): %v", err)
	}
	if cfg.DiscoveryURL != "" || cfg.AuthorizeURL != "" || cfg.TokenURL != "" ||
		cfg.UserinfoURL != "" || cfg.IDClaim != "" || cfg.Scopes != "" {
		t.Fatalf("git 档不该留下 OIDC 字段: %+v", cfg)
	}
	var claim string
	if err := db.QueryRowContext(ctx(), `SELECT id_claim FROM oauth_login_apps WHERE provider = 'github'`).Scan(&claim); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if claim != "" {
		t.Fatalf("库里的 id_claim 应为空,得到 %q", claim)
	}
}

// TestSaveLoginAppOidcSecretNeverPlaintext 与 git 档同一条铁律,只是换了个 provider:
// 新增一档不等于新增一个明文落库的口子。
func TestSaveLoginAppOidcSecretNeverPlaintext(t *testing.T) {
	stub := newStubOidc(t, `{"sub":"s"}`)
	svc, _, dbPath := newLoginService(t)
	cfg := saveOidcApp(t, svc, SaveLoginAppInput{DiscoveryURL: stub.discoveryURL()})
	if cfg.MaskedSecret == "" || !cfg.Configured {
		t.Fatalf("应 configured 且带掩码: %+v", cfg)
	}
	roundTrip, err := svc.LoginApp(ctx(), ProviderOIDC)
	if err != nil {
		t.Fatalf("LoginApp: %v", err)
	}
	if roundTrip.DiscoveryURL != stub.discoveryURL() {
		t.Fatalf("发现文档地址应回显: %q", roundTrip.DiscoveryURL)
	}
	raw, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read db: %v", err)
	}
	if strings.Contains(string(raw), "OIDC-SECRET-PLAINTEXT") {
		t.Fatalf("库文件里出现明文 client_secret")
	}
	// 列表视图同样只给掩码,且带上 OIDC 端点(配置页靠它决定表单初始值)。
	apps, err := svc.ListLoginApps(ctx())
	if err != nil {
		t.Fatalf("ListLoginApps: %v", err)
	}
	if len(apps) != 1 || apps[0].Provider != ProviderOIDC || apps[0].Scopes != defaultOidcScopes {
		t.Fatalf("清单不符: %+v", apps)
	}
	if strings.Contains(apps[0].MaskedSecret, "PLAINTEXT") {
		t.Fatalf("清单里的掩码含明文: %q", apps[0].MaskedSecret)
	}
}
