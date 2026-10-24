// login_test.go 覆盖「用 git 平台账号登录本平台」的那半 OAuth 领域:
// 登录应用配置的密文边界、身份 scope 的下限、以及 code→身份 这条兑换链。
//
// mock provider 走 custom 档:它的端点完全由 base_url 推导,因此是这些档里唯一能被
// httptest 接管的形态。内置品牌档(gitee/github/gitlab/gitea)只测端点与 scope 模板 ——
// 那些是与真实主机无关的静态契约,联网测它们只会让测试随外网抖动。
package oauth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/storetest"
	"github.com/fuuhoo/pipewright/internal/vault"
)

// newLoginService 构造接真实库(db 含全部迁移)的 Service,client 走默认超时。
func newLoginService(t *testing.T) (Service, *sql.DB, string) {
	t.Helper()
	db, path := storetest.OpenDBWithPath(t)
	return New(db, vault.New(db, testMasterKey()), &http.Client{Timeout: 5 * time.Second}), db, path
}

// stubProvider 起一个 GitLab/Gitea 形态的最小 OAuth provider:
// POST /oauth/token 回 access_token,GET /api/v4/user 回 userBody。
// 顺带记录 token 请求里收到的 form,供断言 client_secret 确实被送出(而不是留空蒙混)。
type stubProvider struct {
	srv         *httptest.Server
	tokenForms  []url.Values
	bearerSeen  []string
	calledEmail bool
}

func (m *stubProvider) baseURL() string { return m.srv.URL }

// newStubProvider 启动 mock;userBody 是 /api/v4/user 的响应原文。
// emailsBody 非空则额外挂 /api/v4/user/emails(仅 fetchIdentity 直测用)。
func newStubProvider(t *testing.T, userBody, emailsBody string) *stubProvider {
	t.Helper()
	m := &stubProvider{}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		m.tokenForms = append(m.tokenForms, r.PostForm)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"mock-access-token-PLAINTEXT"}`))
	})
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		m.bearerSeen = append(m.bearerSeen, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(userBody))
	})
	if emailsBody != "" {
		mux.HandleFunc("/api/v4/user/emails", func(w http.ResponseWriter, r *http.Request) {
			m.calledEmail = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(emailsBody))
		})
	}
	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

func saveCustomLoginApp(t *testing.T, svc Service, baseURL string, domains *[]string) {
	t.Helper()
	if _, err := svc.SaveLoginApp(context.Background(), SaveLoginAppInput{
		Provider:       ProviderCustom,
		BaseURL:        baseURL,
		ClientID:       "login-client-id",
		ClientSecret:   strp("LOGIN-SECRET-PLAINTEXT"),
		AllowedDomains: domains,
		Enabled:        true,
	}); err != nil {
		t.Fatalf("SaveLoginApp: %v", err)
	}
}

// ---- 登录应用配置:secret 边界 + 保存期校验 ----

func TestSaveLoginAppSecretNeverPlaintext(t *testing.T) {
	svc, _, dbPath := newLoginService(t)
	cfg, err := svc.SaveLoginApp(ctx(), SaveLoginAppInput{
		Provider: ProviderCustom, BaseURL: "https://git.example.com",
		ClientID: "cid", ClientSecret: strp("LOGIN-SECRET-PLAINTEXT"), Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveLoginApp: %v", err)
	}
	if !cfg.Configured || !cfg.Enabled {
		t.Fatalf("应 configured/enabled=true: %+v", cfg)
	}
	if cfg.MaskedSecret == "" || strings.Contains(cfg.MaskedSecret, "PLAINTEXT") {
		t.Fatalf("maskedSecret 不应为空/含明文: %q", cfg.MaskedSecret)
	}
	// 整库 dump 裸断言:密文入库这条不许有例外。
	raw, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read db: %v", err)
	}
	if strings.Contains(string(raw), "LOGIN-SECRET-PLAINTEXT") {
		t.Fatalf("库文件里出现明文 client_secret")
	}
}

func TestSaveLoginAppRequiresBaseURLForCustom(t *testing.T) {
	svc, _, _ := newLoginService(t)
	_, err := svc.SaveLoginApp(ctx(), SaveLoginAppInput{
		Provider: ProviderCustom, ClientID: "cid", ClientSecret: strp("s"), Enabled: true,
	})
	if !errors.Is(err, ErrBaseURLRequired) {
		t.Fatalf("custom 缺 base_url err = %v, want ErrBaseURLRequired", err)
	}
}

// TestSaveLoginAppRejectsMalformedDomain 是白名单的 fail-loud 面:配错的域名必须当场报错。
// 静默清洗的后果是「以为限了域名,其实谁都能登」。
func TestSaveLoginAppRejectsMalformedDomain(t *testing.T) {
	svc, _, _ := newLoginService(t)
	for _, bad := range []string{"https://corp.example.com", "corp.example.com/", " .com", "corp_ex", "-lead.example.com", "ok,.bad"} {
		in := SaveLoginAppInput{
			Provider: ProviderGitHub, ClientID: "cid", ClientSecret: strp("s"),
			AllowedDomains: &[]string{bad}, Enabled: true,
		}
		_, err := svc.SaveLoginApp(ctx(), in)
		if !errors.Is(err, ErrInvalidDomain) {
			t.Fatalf("域名 %q 应被拒,得到 err = %v", bad, err)
		}
	}
	// 合法项则应通过并归一(小写、去空白、去重)。
	cfg, err := svc.SaveLoginApp(ctx(), SaveLoginAppInput{
		Provider: ProviderGitHub, ClientID: "cid", ClientSecret: strp("s"),
		AllowedDomains: &[]string{" Corp.Example.COM ", "corp.example.com", "sub.example.com"}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("合法域名被拒: %v", err)
	}
	if got := strings.Join(cfg.AllowedDomains, ","); got != "corp.example.com,sub.example.com" {
		t.Fatalf("白名单归一结果 = %q", got)
	}
}

// TestSaveLoginAppOmittedSecretAndDomainsKeepExisting 保证前端漏传不会清空既有配置。
func TestSaveLoginAppOmittedSecretAndDomainsKeepExisting(t *testing.T) {
	svc, _, _ := newLoginService(t)
	if _, err := svc.SaveLoginApp(ctx(), SaveLoginAppInput{
		Provider: ProviderGitLab, ClientID: "cid", ClientSecret: strp("first-secret"),
		AllowedDomains: &[]string{"corp.example.com"}, Enabled: true,
	}); err != nil {
		t.Fatalf("首存: %v", err)
	}
	// 省略 secret 与 domains(nil):两者都应沿用既有,只改 enabled。
	cfg, err := svc.SaveLoginApp(ctx(), SaveLoginAppInput{
		Provider: ProviderGitLab, ClientID: "cid", Enabled: false,
	})
	if err != nil {
		t.Fatalf("二存: %v", err)
	}
	if !cfg.Configured {
		t.Fatalf("省略 secret 不该让应用变成未配置: %+v", cfg)
	}
	if len(cfg.AllowedDomains) != 1 || cfg.AllowedDomains[0] != "corp.example.com" {
		t.Fatalf("省略 domains 不该清空白名单: %+v", cfg.AllowedDomains)
	}
	if cfg.Enabled {
		t.Fatalf("enabled 应按入参关掉")
	}
}

// ---- 授权 URL:scope 下限 + 回调/state ----

func TestLoginAuthorizeURLUsesIdentityScopesOnly(t *testing.T) {
	stub := newStubProvider(t, `{"id":1,"username":"a"}`, "")
	svc, _, _ := newLoginService(t)
	saveCustomLoginApp(t, svc, stub.baseURL(), nil)

	authURL, err := svc.LoginAuthorizeURL(ctx(), ProviderCustom, "STATE-xyz", "https://app.example.com/api/auth/oauth/custom/callback")
	if err != nil {
		t.Fatalf("LoginAuthorizeURL: %v", err)
	}
	u, perr := url.Parse(authURL)
	if perr != nil {
		t.Fatalf("parse: %v", perr)
	}
	q := u.Query()
	if q.Get("state") != "STATE-xyz" {
		t.Fatalf("state 未带上: %q", authURL)
	}
	if q.Get("redirect_uri") != "https://app.example.com/api/auth/oauth/custom/callback" {
		t.Fatalf("redirect_uri = %q", q.Get("redirect_uri"))
	}
	if q.Get("client_id") != "login-client-id" {
		t.Fatalf("client_id = %q", q.Get("client_id"))
	}
	// 登录只需要身份:自建档的 read_repository 绝不该出现在登录 scope 里。
	if !strings.Contains(q.Get("scope"), "read_user") || strings.Contains(q.Get("scope"), "repository") {
		t.Fatalf("登录 scope = %q,应为只读身份且不含仓库权限", q.Get("scope"))
	}
}

func TestLoginAuthorizeURLRejectsDisabledOrUnconfigured(t *testing.T) {
	svc, _, _ := newLoginService(t)
	if _, err := svc.LoginAuthorizeURL(ctx(), ProviderGitHub, "s", "https://x/callback"); !errors.Is(err, ErrAppNotConfigured) {
		t.Fatalf("未配置 err = %v, want ErrAppNotConfigured", err)
	}
	// 配了但没启用 → 同样拒(登录页此时不该出这个入口,直接跳 URL 也不给过)。
	if _, err := svc.SaveLoginApp(ctx(), SaveLoginAppInput{
		Provider: ProviderGitHub, ClientID: "cid", ClientSecret: strp("s"), Enabled: false,
	}); err != nil {
		t.Fatalf("SaveLoginApp: %v", err)
	}
	if _, err := svc.LoginAuthorizeURL(ctx(), ProviderGitHub, "s", "https://x/callback"); !errors.Is(err, ErrAppNotConfigured) {
		t.Fatalf("未启用 err = %v, want ErrAppNotConfigured", err)
	}
}

// TestLoginScopesNeverAskForRepo 冻结五档的登录 scope 上限。
// 登录流拿到的 token 只用来读身份;申请 repo 权限的登录按钮是不必要的越权面 ——
// 用户以为「用 GitHub 登录」,实际授权了平台读写他的全部仓库。
func TestLoginScopesNeverAskForRepo(t *testing.T) {
	denied := map[string]bool{
		"repo": true, "public_repo": true, "projects": true, "read_repository": true,
		"write_repository": true, "api": true, "workflow": true, "admin:repo_hook": true,
		"read:repository": true, "write:repository": true,
	}
	for _, name := range []string{ProviderGitee, ProviderGitHub, ProviderGitLab, ProviderGitea} {
		p, err := resolveProvider(name, "", "")
		if err != nil {
			t.Fatalf("%s resolve: %v", name, err)
		}
		if len(p.LoginScopes) == 0 {
			t.Fatalf("%s 没有独立的登录 scope(会连带申请仓库权限)", name)
		}
		for _, s := range p.LoginScopes {
			if denied[s] {
				t.Fatalf("%s 登录 scope 含仓库权限 %q", name, s)
			}
		}
	}
	// custom 的 DefaultScopes 含仓库读权限,登录档必须把它摘掉 —— 两种形态都要。
	for _, shape := range []string{"", GitShapeGitLab, GitShapeGitea} {
		p, err := resolveProvider(ProviderCustom, "https://git.example.com", shape)
		if err != nil {
			t.Fatalf("custom resolve (shape %q): %v", shape, err)
		}
		if len(p.LoginScopes) == 0 {
			t.Fatalf("custom shape %q 没有独立的登录 scope", shape)
		}
		for _, s := range p.LoginScopes {
			if denied[s] {
				t.Fatalf("custom shape %q 登录 scope 含仓库权限 %q", shape, s)
			}
		}
	}
}

// ---- code → 身份 ----

func TestExchangeIdentityResolvesStableID(t *testing.T) {
	stub := newStubProvider(t, `{"id":42,"username":"alice","email":"alice@corp.example.com","name":"Alice"}`, "")
	svc, _, _ := newLoginService(t)
	saveCustomLoginApp(t, svc, stub.baseURL(), nil)

	id, err := svc.ExchangeIdentity(ctx(), ProviderCustom, "code-1", "https://app/api/auth/oauth/custom/callback")
	if err != nil {
		t.Fatalf("ExchangeIdentity: %v", err)
	}
	if id.ExternalID != "42" || id.Login != "alice" || id.Email != "alice@corp.example.com" {
		t.Fatalf("身份解析不符: %+v", id)
	}
	if id.Provider != ProviderCustom {
		t.Fatalf("Provider 应由兑换填入: %+v", id)
	}
	// 兑换确实带了 client_secret 与回调地址(provider 会校验两者)。
	if len(stub.tokenForms) != 1 {
		t.Fatalf("token 端点调用次数 = %d", len(stub.tokenForms))
	}
	f := stub.tokenForms[0]
	if f.Get("client_secret") != "LOGIN-SECRET-PLAINTEXT" || f.Get("grant_type") != "authorization_code" {
		t.Fatalf("token 请求 form 不符: %v", f)
	}
	if f.Get("redirect_uri") != "https://app/api/auth/oauth/custom/callback" {
		t.Fatalf("redirect_uri = %q", f.Get("redirect_uri"))
	}
	if len(stub.bearerSeen) != 1 || stub.bearerSeen[0] != "Bearer mock-access-token-PLAINTEXT" {
		t.Fatalf("身份请求的 Bearer 不符: %v", stub.bearerSeen)
	}
}

// TestExchangeIdentityRequiresStableID 是铁律 1 的领域侧落点:
// provider 没给稳定 ID 就拒,绝不退化成「按登录名认人」—— 登录名可改、可被抢注。
func TestExchangeIdentityRequiresStableID(t *testing.T) {
	cases := []struct{ name, body string }{
		{"缺 id", `{"username":"alice","email":"alice@corp.example.com"}`},
		{"id 为空串", `{"id":"","username":"alice"}`},
		{"id 是 null", `{"id":null,"username":"alice"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stub := newStubProvider(t, c.body, "")
			svc, _, _ := newLoginService(t)
			saveCustomLoginApp(t, svc, stub.baseURL(), nil)
			_, err := svc.ExchangeIdentity(ctx(), ProviderCustom, "code-1", "https://app/callback")
			if !errors.Is(err, ErrIdentityUnavailable) {
				t.Fatalf("err = %v, want ErrIdentityUnavailable", err)
			}
		})
	}
}

func TestExchangeIdentityErrorsAreHumanReadable(t *testing.T) {
	// token 端点回 500:错误体必须是人读码,绝不含 client_secret / 响应原文。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal detail LOGIN-SECRET-PLAINTEXT", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	svc, _, _ := newLoginService(t)
	saveCustomLoginApp(t, svc, srv.URL, nil)
	_, err := svc.ExchangeIdentity(ctx(), ProviderCustom, "code-1", "https://app/callback")
	if !errors.Is(err, ErrExchangeFailed) {
		t.Fatalf("err = %v, want ErrExchangeFailed", err)
	}
	if strings.Contains(err.Error(), "PLAINTEXT") {
		t.Fatalf("错误泄漏 provider 原文: %v", err)
	}
}

func TestExchangeIdentityRejectsEmptyCode(t *testing.T) {
	stub := newStubProvider(t, `{"id":1,"username":"a"}`, "")
	svc, _, _ := newLoginService(t)
	saveCustomLoginApp(t, svc, stub.baseURL(), nil)
	if _, err := svc.ExchangeIdentity(ctx(), ProviderCustom, "", "https://app/callback"); !errors.Is(err, ErrExchangeFailed) {
		t.Fatalf("空 code err = %v", err)
	}
	if len(stub.tokenForms) != 0 {
		t.Fatalf("空 code 不该打到 provider")
	}
}

func TestExchangeIdentityUnconfiguredApp(t *testing.T) {
	svc, _, _ := newLoginService(t)
	if _, err := svc.ExchangeIdentity(ctx(), ProviderGitHub, "code", "https://app/callback"); !errors.Is(err, ErrAppNotConfigured) {
		t.Fatalf("err = %v, want ErrAppNotConfigured", err)
	}
}

// ---- 身份字段名 / 邮箱回落(各平台形态差异)----

func TestParseIdentityFieldNamesPerProvider(t *testing.T) {
	cases := []struct {
		provider, body, wantLogin, wantID string
	}{
		{ProviderGitHub, `{"id":7,"login":"octo","name":"Octo"}`, "octo", "7"},
		{ProviderGitee, `{"id":8,"login":"miku"}`, "miku", "8"},
		{ProviderGitLab, `{"id":9,"username":"fuuhoo"}`, "fuuhoo", "9"},
		// 字符串 id 也要归一(GitHub 企业版与部分实例回字符串)。
		{ProviderGitLab, `{"id":"10","username":"str"}`, "str", "10"},
		// login 缺失时回退 username(Gitea 形态)。
		{ProviderGitHub, `{"id":11,"username":"fallback"}`, "fallback", "11"},
	}
	for _, c := range cases {
		p, err := resolveProvider(c.provider, "https://git.example.com", "")
		if err != nil {
			t.Fatalf("%s resolve: %v", c.provider, err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(c.body), &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		got := p.parseIdentity(m)
		if got.Login != c.wantLogin || got.ExternalID != c.wantID {
			t.Fatalf("%s %s → %+v, want login=%s id=%s", c.provider, c.body, got, c.wantLogin, c.wantID)
		}
	}
}

// TestFetchIdentityFallsBackToEmailsURL 覆盖 GitHub 的隐私邮箱形态:
// /user 里 email 为 null,必须再打一次 /user/emails 才拿得到域名白名单要用的邮箱。
func TestFetchIdentityFallsBackToEmailsURL(t *testing.T) {
	stub := newStubProvider(t, `{"id":12,"login":"priv","email":null}`,
		`[{"email":"other@example.com","primary":false,"verified":false},{"email":"Bob@Corp.Example.com","primary":true,"verified":true}]`)
	p, err := resolveProvider(ProviderGitHub, "", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// 端点指向 mock,其余(GitHub 的 EmailsURL 非空)保持预设语义。
	p.UserURL = stub.baseURL() + "/api/v4/user"
	p.EmailsURL = stub.baseURL() + "/api/v4/user/emails"

	id, err := fetchIdentity(ctx(), &http.Client{Timeout: 5 * time.Second}, p, "tok")
	if err != nil {
		t.Fatalf("fetchIdentity: %v", err)
	}
	if !stub.calledEmail {
		t.Fatalf("/user 无邮箱时应请求邮箱列表端点")
	}
	if got := strings.ToLower(id.Email); got != "bob@corp.example.com" {
		t.Fatalf("应挑 primary+verified 邮箱,得到 %q", id.Email)
	}
}

// TestFetchIdentitySurvivesEmailsFailure 保证拿不到邮箱不炸整条兑换:
// 登录侧会因缺邮箱被白名单拒(fail closed),连接侧本就不看邮箱。
func TestFetchIdentitySurvivesEmailsFailure(t *testing.T) {
	stub := newStubProvider(t, `{"id":13,"login":"nope"}`, "")
	p, err := resolveProvider(ProviderGitHub, "", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	p.UserURL = stub.baseURL() + "/api/v4/user"
	p.EmailsURL = stub.baseURL() + "/does-not-exist"

	id, err := fetchIdentity(ctx(), &http.Client{Timeout: 5 * time.Second}, p, "tok")
	if err != nil {
		t.Fatalf("fetchIdentity: %v", err)
	}
	if id.ExternalID != "13" || id.Email != "" {
		t.Fatalf(" %+v", id)
	}
}

func TestPickEmailShape(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"字符串数组", `["a@x.com","b@y.com"]`, "a@x.com"},
		{"无 primary 取 verified", `[{"email":"a@x.com","verified":false},{"email":"b@y.com","verified":true}]`, "b@y.com"},
		{"都没有则首个", `[{"email":"a@x.com"},{"email":"b@y.com"}]`, "a@x.com"},
		{"空数组", `[]`, ""},
	}
	for _, c := range cases {
		var items []any
		if err := json.Unmarshal([]byte(c.body), &items); err != nil {
			t.Fatalf("%s unmarshal: %v", c.name, err)
		}
		if got := pickEmail(items); got != c.want {
			t.Errorf("%s: pickEmail = %q, want %q", c.name, got, c.want)
		}
	}
}

// ---- 白名单归一串的往返 ----

func TestDomainsRoundTrip(t *testing.T) {
	in := &[]string{"Corp.Example.com", "sub.example.com", " Corp.Example.com "}
	got, err := normalizeDomains(in)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if strings.Join(splitDomains(joinDomains(got)), ",") != "corp.example.com,sub.example.com" {
		t.Fatalf("往返不符: %+v", got)
	}
}
