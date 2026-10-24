package oauth

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/storetest"
	"github.com/fuuhoo/pipewright/internal/vault"
)

// testMasterKey 返回确定性测试用 master key。
func testMasterKey() *[32]byte {
	var k [32]byte
	for i := range k {
		k[i] = byte(i + 7)
	}
	return &k
}

// testDB 打开临时 SQLite(含全部迁移),返回 *sql.DB 与库文件路径(供整库 dump 验明文)。
func testDB(t *testing.T) (*sql.DB, string) {
	return storetest.OpenDBWithPath(t)
}

func newService(t *testing.T, client *http.Client) (Service, vault.Vault, *sql.DB, string) {
	t.Helper()
	db, path := testDB(t)
	v := vault.New(db, testMasterKey())
	return New(db, v, client), v, db, path
}

func ctx() context.Context { return context.Background() }

// ---- provider URL 构造 -------------------------------------------------

func TestResolveProviderPresets(t *testing.T) {
	cases := []struct {
		name      string
		authorize string
		token     string
		user      string
		scopeHas  string
	}{
		{ProviderGitee, "https://gitee.com/oauth/authorize", "https://gitee.com/oauth/token", "https://gitee.com/api/v5/user", "projects"},
		{ProviderGitHub, "https://github.com/login/oauth/authorize", "https://github.com/login/oauth/access_token", "https://api.github.com/user", "repo"},
		{ProviderGitLab, "https://gitlab.com/oauth/authorize", "https://gitlab.com/oauth/token", "https://gitlab.com/api/v4/user", "read_repository"},
		{ProviderGitea, "https://gitea.com/login/oauth/authorize", "https://gitea.com/login/oauth/access_token", "https://gitea.com/api/v1/user", "read:repository"},
	}
	for _, c := range cases {
		p, err := resolveProvider(c.name, "", "")
		if err != nil {
			t.Fatalf("%s resolve: %v", c.name, err)
		}
		if p.AuthorizeURL != c.authorize || p.TokenURL != c.token || p.UserURL != c.user {
			t.Fatalf("%s endpoints mismatch: %+v", c.name, p)
		}
		if !strings.Contains(p.scope(), c.scopeHas) {
			t.Fatalf("%s scope %q missing %q", c.name, p.scope(), c.scopeHas)
		}
	}
}

// 自建实例的两种地址形态:Gitea 用的不是 GitLab 那套 /oauth/* 路径,
// 拿 GitLab 形态去推 Gitea 会拼出一个根本不存在的授权页(这正是加这一列的原因)。
func TestResolveCustomDerivesEndpoints(t *testing.T) {
	cases := []struct {
		shape                     string
		authorize, token, userURL string
		scopeHas                  string
	}{
		{"", "https://git.internal.example.com/oauth/authorize",
			"https://git.internal.example.com/oauth/token",
			"https://git.internal.example.com/api/v4/user", "read_repository"},
		{GitShapeGitLab, "https://git.internal.example.com/oauth/authorize",
			"https://git.internal.example.com/oauth/token",
			"https://git.internal.example.com/api/v4/user", "read_repository"},
		{GitShapeGitea, "https://git.internal.example.com/login/oauth/authorize",
			"https://git.internal.example.com/login/oauth/access_token",
			"https://git.internal.example.com/api/v1/user", "read:repository"},
	}
	for _, c := range cases {
		p, err := resolveProvider(ProviderCustom, "https://git.internal.example.com/", c.shape)
		if err != nil {
			t.Fatalf("custom resolve (shape %q): %v", c.shape, err)
		}
		if p.AuthorizeURL != c.authorize || p.TokenURL != c.token || p.UserURL != c.userURL {
			t.Fatalf("custom shape %q endpoints mismatch: %+v", c.shape, p)
		}
		if !strings.Contains(p.scope(), c.scopeHas) {
			t.Fatalf("custom shape %q scope %q missing %q", c.shape, p.scope(), c.scopeHas)
		}
	}
}

func TestResolveCustomRejectsBadShape(t *testing.T) {
	if _, err := resolveProvider(ProviderCustom, "https://git.internal.example.com", "bitbucket"); !errors.Is(err, ErrInvalidGitShape) {
		t.Fatalf("bad shape err = %v, want ErrInvalidGitShape", err)
	}
}

func TestResolveCustomRequiresBaseURL(t *testing.T) {
	if _, err := resolveProvider(ProviderCustom, "", ""); err != ErrBaseURLRequired {
		t.Fatalf("custom no baseURL err = %v, want ErrBaseURLRequired", err)
	}
}

func TestNormalizeProviderRejectsUnknown(t *testing.T) {
	if _, err := normalizeProvider("bitbucket"); err != ErrInvalidProvider {
		t.Fatalf("unknown provider err = %v, want ErrInvalidProvider", err)
	}
}

// ---- app 配置:secret 只写 + 掩码 + configured ------------------------

func TestSaveAppMaskedNoPlaintextAndConfigured(t *testing.T) {
	svc, _, _, dbPath := newService(t, http.DefaultClient)
	cfg, err := svc.SaveApp(ctx(), SaveAppInput{
		Provider:     ProviderGitHub,
		ClientID:     "iv1.client",
		ClientSecret: strp("super-secret-PLAINTEXT"),
		Enabled:      true,
	})
	if err != nil {
		t.Fatalf("SaveApp: %v", err)
	}
	if !cfg.Configured || !cfg.Enabled {
		t.Fatalf("应 configured/enabled=true: %+v", cfg)
	}
	if cfg.MaskedSecret == "" || strings.Contains(cfg.MaskedSecret, "PLAINTEXT") {
		t.Fatalf("maskedSecret 不应含明文: %q", cfg.MaskedSecret)
	}
	// 整库 dump 绝无明文 secret(裸断言)。
	if dbPath != "" {
		raw, err := os.ReadFile(dbPath)
		if err != nil {
			t.Fatalf("read db: %v", err)
		}
		if strings.Contains(string(raw), "super-secret-PLAINTEXT") {
			t.Fatalf("DB dump 含明文 client_secret!")
		}
	}
}

func TestSaveAppSecretWriteOnlyPreservesOnOmit(t *testing.T) {
	svc, v, _, _ := newService(t, http.DefaultClient)
	if _, err := svc.SaveApp(ctx(), SaveAppInput{
		Provider: ProviderGitee, ClientID: "cid1", ClientSecret: strp("secret-1"), Enabled: true,
	}); err != nil {
		t.Fatalf("save 1: %v", err)
	}
	// 第二次保存省略 clientSecret(nil)→ 保留旧 secret;改 clientID/enabled。
	if _, err := svc.SaveApp(ctx(), SaveAppInput{
		Provider: ProviderGitee, ClientID: "cid2", ClientSecret: nil, Enabled: false,
	}); err != nil {
		t.Fatalf("save 2: %v", err)
	}
	// 省略 secret 的二次保存未报 ErrClientSecretRequired,且 ListApps 仍 configured=true、
	// clientID 已更新、enabled 已更新 —— 证明旧 secret 被保留(未清空)、其余字段已轮换。
	apps, err := svc.ListApps(ctx())
	if err != nil || len(apps) != 1 {
		t.Fatalf("ListApps: %v len=%d", err, len(apps))
	}
	a := apps[0]
	if !a.Configured {
		t.Fatalf("省略 secret 后应仍 configured(旧 secret 保留): %+v", a)
	}
	if a.ClientID != "cid2" || a.Enabled {
		t.Fatalf("clientID/enabled 应已更新: %+v", a)
	}
	_ = v
}

func TestSaveAppRejectsEmptyClientID(t *testing.T) {
	svc, _, _, _ := newService(t, http.DefaultClient)
	if _, err := svc.SaveApp(ctx(), SaveAppInput{Provider: ProviderGitee, ClientID: "  ", ClientSecret: strp("s")}); err != ErrClientIDRequired {
		t.Fatalf("empty clientID err = %v, want ErrClientIDRequired", err)
	}
}

func TestSaveAppFirstTimeRequiresSecret(t *testing.T) {
	svc, _, _, _ := newService(t, http.DefaultClient)
	if _, err := svc.SaveApp(ctx(), SaveAppInput{Provider: ProviderGitee, ClientID: "cid", ClientSecret: nil}); err != ErrClientSecretRequired {
		t.Fatalf("first save no secret err = %v, want ErrClientSecretRequired", err)
	}
}

func TestSaveAppCustomRequiresBaseURL(t *testing.T) {
	svc, _, _, _ := newService(t, http.DefaultClient)
	if _, err := svc.SaveApp(ctx(), SaveAppInput{Provider: ProviderCustom, ClientID: "cid", ClientSecret: strp("s")}); err != ErrBaseURLRequired {
		t.Fatalf("custom no baseURL err = %v, want ErrBaseURLRequired", err)
	}
}

func TestListAppsMaskedOnly(t *testing.T) {
	svc, _, _, _ := newService(t, http.DefaultClient)
	_, _ = svc.SaveApp(ctx(), SaveAppInput{Provider: ProviderGitee, ClientID: "g", ClientSecret: strp("sec-PLAINTEXT"), Enabled: true})
	apps, err := svc.ListApps(ctx())
	if err != nil {
		t.Fatalf("ListApps: %v", err)
	}
	if len(apps) != 1 {
		t.Fatalf("want 1 app, got %d", len(apps))
	}
	if strings.Contains(apps[0].MaskedSecret, "PLAINTEXT") {
		t.Fatalf("masked secret leaks plaintext: %q", apps[0].MaskedSecret)
	}
}

// ---- AuthorizeURL ------------------------------------------------------

func TestAuthorizeURLUnconfiguredProviderErrors(t *testing.T) {
	svc, _, _, _ := newService(t, http.DefaultClient)
	if _, err := svc.AuthorizeURL(ctx(), ProviderGitHub, "st", "https://x/cb"); err != ErrAppNotConfigured {
		t.Fatalf("unconfigured authorize err = %v, want ErrAppNotConfigured", err)
	}
}

func TestAuthorizeURLBuildsParams(t *testing.T) {
	svc, _, _, _ := newService(t, http.DefaultClient)
	_, _ = svc.SaveApp(ctx(), SaveAppInput{Provider: ProviderGitHub, ClientID: "CID123", ClientSecret: strp("s"), Enabled: true})
	raw, err := svc.AuthorizeURL(ctx(), ProviderGitHub, "STATE-XYZ", "https://app.example/api/oauth/github/callback")
	if err != nil {
		t.Fatalf("AuthorizeURL: %v", err)
	}
	u, _ := url.Parse(raw)
	if u.Host != "github.com" {
		t.Fatalf("authorize host = %q", u.Host)
	}
	q := u.Query()
	if q.Get("client_id") != "CID123" || q.Get("state") != "STATE-XYZ" {
		t.Fatalf("params mismatch: %v", q)
	}
	if q.Get("redirect_uri") != "https://app.example/api/oauth/github/callback" {
		t.Fatalf("redirect_uri mismatch: %q", q.Get("redirect_uri"))
	}
	if q.Get("response_type") != "code" || !strings.Contains(q.Get("scope"), "repo") {
		t.Fatalf("response_type/scope mismatch: %v", q)
	}
}

// ---- Exchange(httptest mock provider)---------------------------------

// mockProvider 返回一个假冒 provider:/token 回 access_token、/user 回 login;
// 记录是否收到 client_secret(验证 server-to-server 真带上了 secret),但绝不要求回显它。
func mockProvider(t *testing.T, wantSecret string) (*httptest.Server, *bool) {
	t.Helper()
	gotSecret := new(bool)
	mux := http.NewServeMux()
	// custom provider 据 baseURL 推导端点为 /oauth/token 与 /api/v4/user。
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.FormValue("client_secret") == wantSecret {
			*gotSecret = true
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("token request missing Accept: application/json")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"gho_FAKE_TOKEN_123","token_type":"bearer"}`))
	})
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gho_FAKE_TOKEN_123" {
			t.Errorf("user request bad auth header: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"login":"octocat","username":"octocat","id":1}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, gotSecret
}

func TestExchangeViaCustomProviderStoresToken(t *testing.T) {
	mock, gotSecret := mockProvider(t, "the-client-secret")
	svc, v, _, _ := newService(t, mock.Client())

	// custom 据 mock baseURL 推导端点;但 mock 端点路径是 /token、/user,
	// customProvider 推导为 /oauth/token、/api/v4/user。改用直接覆盖端点的方式不便,
	// 故注册 mock 在这两条路径上。
	if _, err := svc.SaveApp(ctx(), SaveAppInput{
		Provider: ProviderCustom, ClientID: "cid", ClientSecret: strp("the-client-secret"),
		BaseURL: mock.URL, Enabled: true,
	}); err != nil {
		t.Fatalf("SaveApp: %v", err)
	}

	res, err := svc.Exchange(ctx(), ProviderCustom, "auth-code-xyz", mock.URL+"/cb")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if res.AccessToken != "gho_FAKE_TOKEN_123" || res.Login != "octocat" {
		t.Fatalf("exchange result: %+v", res)
	}
	if !*gotSecret {
		t.Fatalf("provider 未收到 client_secret(server-to-server 应带上)")
	}

	// 经 vault.Create 存成 git_token 凭据,验证可解密回 token。
	cred, err := v.Create(vault.CreateInput{
		Name: ProviderCustom + ":" + res.Login, Type: vault.TypeGitToken, Scope: "global", Secret: res.AccessToken,
	})
	if err != nil {
		t.Fatalf("vault.Create: %v", err)
	}
	got, err := v.Reveal(cred.ID)
	if err != nil {
		t.Fatalf("Reveal: %v", err)
	}
	if got != "gho_FAKE_TOKEN_123" {
		t.Fatalf("stored token mismatch: %q", got)
	}
}

func TestExchangeUnconfiguredProviderErrors(t *testing.T) {
	svc, _, _, _ := newService(t, http.DefaultClient)
	if _, err := svc.Exchange(ctx(), ProviderGitee, "code", "https://x/cb"); err != ErrAppNotConfigured {
		t.Fatalf("unconfigured exchange err = %v, want ErrAppNotConfigured", err)
	}
}

func TestExchangeProviderErrorMapsToExchangeFailed(t *testing.T) {
	// provider /token 回 4xx → ErrExchangeFailed(绝不泄漏 provider 原文)。
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad_verification_code","secret_leak":"the-client-secret"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	svc, _, _, _ := newService(t, srv.Client())
	_, _ = svc.SaveApp(ctx(), SaveAppInput{Provider: ProviderCustom, ClientID: "cid", ClientSecret: strp("the-client-secret"), BaseURL: srv.URL, Enabled: true})

	_, err := svc.Exchange(ctx(), ProviderCustom, "stale-code", srv.URL+"/cb")
	if err != ErrExchangeFailed {
		t.Fatalf("exchange err = %v, want ErrExchangeFailed", err)
	}
	if strings.Contains(err.Error(), "the-client-secret") || strings.Contains(err.Error(), "bad_verification_code") {
		t.Fatalf("错误体泄漏了 provider 原文/secret: %v", err)
	}
}

// ---- 自建 Gitea(仓库凭据流)--------------------------------------------

// TestSelfHostedGiteaCredentialFlow 是加 git_shape 的原始动机:自建 Gitea 用
// /login/oauth/* 与 /api/v1/user,GitLab 那套 /oauth/* 推导在 Gitea 上必然 404,
// 而失败发生在用户授权之后,页面只会回一句看不懂的 exchange_failed。
// 这里把「存形态 → 按形态拼授权地址 → 按形态换令牌与登录名 → 清单回显」四步钉死。
func TestSelfHostedGiteaCredentialFlow(t *testing.T) {
	mux := http.NewServeMux()
	var gotTokenPath string
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		gotTokenPath = r.URL.Path
		_, _ = w.Write([]byte(`{"access_token":"gitea-tok-1","token_type":"bearer","expires_in":3600}`))
	})
	// Gitea 的身份接口用 username,没有 GitLab 那套 login 字段。
	mux.HandleFunc("/api/v1/user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gitea-tok-1" {
			t.Errorf("gitea user request bad auth header: %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"id":9,"username":"gitea-fan","full_name":"Gitea Fan"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	svc, _, _, _ := newService(t, srv.Client())
	cfg, err := svc.SaveApp(ctx(), SaveAppInput{
		Provider: ProviderCustom, ClientID: "cid", ClientSecret: strp("sec"),
		BaseURL: srv.URL, GitShape: GitShapeGitea, Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveApp: %v", err)
	}
	if cfg.GitShape != GitShapeGitea {
		t.Fatalf("保存响应应回显形态,得到 %q", cfg.GitShape)
	}
	apps, err := svc.ListApps(ctx())
	if err != nil || len(apps) != 1 || apps[0].GitShape != GitShapeGitea {
		t.Fatalf("清单要带出形态(配置页靠它回填选择器): %+v err=%v", apps, err)
	}

	raw, err := svc.AuthorizeURL(ctx(), ProviderCustom, "ST", srv.URL+"/cb")
	if err != nil {
		t.Fatalf("AuthorizeURL: %v", err)
	}
	u, _ := url.Parse(raw)
	if u.Path != "/login/oauth/authorize" {
		t.Fatalf("authorize path = %q, want /login/oauth/authorize", u.Path)
	}
	if !strings.Contains(u.Query().Get("scope"), "read:repository") {
		t.Fatalf("gitea 形态该申请仓库读权限: %q", u.Query().Get("scope"))
	}

	res, err := svc.Exchange(ctx(), ProviderCustom, "code-1", srv.URL+"/cb")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if gotTokenPath != "/login/oauth/access_token" {
		t.Fatalf("token path = %q, want /login/oauth/access_token", gotTokenPath)
	}
	if res.Login != "gitea-fan" {
		t.Fatalf("exchange login = %q, want gitea-fan(username 字段)", res.Login)
	}
}

// TestSaveAppShapeOnlyPersistedForCustom 冻结形态列的第二半:内置档位端点由平台定死,
// 传了形态也必须清空落库 —— 留着只会让人以为 gitee.com 能被改成别的地址形态。
func TestSaveAppShapeOnlyPersistedForCustom(t *testing.T) {
	svc, _, db, _ := newService(t, http.DefaultClient)
	for _, p := range []string{ProviderGitea, ProviderGitLab} {
		if _, err := svc.SaveApp(ctx(), SaveAppInput{
			Provider: p, ClientID: "cid", ClientSecret: strp("sec"), GitShape: GitShapeGitea, Enabled: true,
		}); err != nil {
			t.Fatalf("SaveApp %s: %v", p, err)
		}
		var shape string
		if err := db.QueryRow(`SELECT git_shape FROM oauth_apps WHERE provider = ?`, p).Scan(&shape); err != nil {
			t.Fatalf("读 %s 形态: %v", p, err)
		}
		if shape != "" {
			t.Fatalf("内置档 %s 不该存形态,库里是 %q", p, shape)
		}
	}
}

// ---- state CSRF --------------------------------------------------------

func TestStateRoundTrip(t *testing.T) {
	svc, _, _, _ := newService(t, http.DefaultClient)
	st, err := svc.IssueState(StateIssue{Intent: IntentVault, Provider: ProviderGitHub, BindKey: "session-A"})
	if err != nil {
		t.Fatalf("IssueState: %v", err)
	}
	if st == "" {
		t.Fatal("state empty")
	}
	data, ok := svc.ConsumeState(st, "session-A")
	if !ok {
		t.Fatal("匹配会话应消费成功")
	}
	// 发起上下文必须原样回吐:回调靠它认意图、认 provider、认落地页。
	if data.Intent != IntentVault || data.Provider != ProviderGitHub {
		t.Fatalf("回吐上下文 = %+v,期望 vault/github", data)
	}
	// 一次性:重复消费应失败(防重放)。
	if _, ok := svc.ConsumeState(st, "session-A"); ok {
		t.Fatal("state 应一次性,重复消费应拒")
	}
}

func TestStateWrongSessionRejected(t *testing.T) {
	svc, _, _, _ := newService(t, http.DefaultClient)
	st, _ := svc.IssueState(StateIssue{Intent: IntentLogin, Provider: ProviderGitLab, BindKey: "session-A"})
	if _, ok := svc.ConsumeState(st, "session-B"); ok {
		t.Fatal("不同会话的 state 应拒(CSRF 防护)")
	}
	// 即便会话错被拒,state 也已被消费,正确会话再用也应失败。
	if _, ok := svc.ConsumeState(st, "session-A"); ok {
		t.Fatal("被错误会话触碰后 state 应已失效")
	}
}

func TestStateUnknownRejected(t *testing.T) {
	svc, _, _, _ := newService(t, http.DefaultClient)
	if _, ok := svc.ConsumeState("never-issued", "session-A"); ok {
		t.Fatal("未签发的 state 应拒")
	}
	if _, ok := svc.ConsumeState("", "session-A"); ok {
		t.Fatal("空 state 应拒")
	}
}

func TestStateRedirectRoundTrip(t *testing.T) {
	svc, _, _, _ := newService(t, http.DefaultClient)
	st, err := svc.IssueState(StateIssue{
		Intent: IntentLogin, Provider: ProviderCustom, BindKey: "nonce-1", Redirect: "/projects",
	})
	if err != nil {
		t.Fatalf("IssueState: %v", err)
	}
	data, ok := svc.ConsumeState(st, "nonce-1")
	if !ok {
		t.Fatal("匹配 bindKey 应消费成功")
	}
	if data.Redirect != "/projects" || data.Intent != IntentLogin {
		t.Fatalf("回吐 = %+v,期望 login + /projects", data)
	}
}

func strp(s string) *string { return &s }
