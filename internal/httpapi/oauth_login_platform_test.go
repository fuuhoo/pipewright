// oauth_login_platform_test.go 把钉钉 / 飞书 / 企业微信接到**整条 HTTP 链**上。
//
// 报文形状(谁只认自定义令牌头、谁的成败写在业务码里、主键取哪个字段)已经在
// internal/oauth/platform_test.go 冻结;这里只管接上装配之后才成立的几件事:
//  1. 登录页拿到的入口名 —— 名字由服务端给,前端不许自己硬编码;
//  2. 钉钉回调的参数名 authCode(官方文档不是 code)—— 这条只有 HTTP 层能测到;
//  3. 三家只能登录、不能走「连接 git 凭据」那条流(它们压根给不出 git 令牌);
//  4. 配置页 agentId 的往返,以及企微少填 AgentId 时的 422 错误码(前端要能翻译)。
package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/identity"
	"github.com/fuuhoo/pipewright/internal/oauth"
)

// platformHTTPClient 是领域层外拨用的客户端。mock 是明文本地端口,默认 Transport 足够。
func platformHTTPClient() *http.Client { return &http.Client{Timeout: 5 * time.Second} }

// ---- mock:钉钉 ----

type stubDing struct {
	srv         *httptest.Server
	userBody    string
	tokenHeader []string
	lastCode    string
}

func newStubDing(t *testing.T, userBody string) *stubDing {
	t.Helper()
	m := &stubDing{userBody: userBody}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.0/oauth2/userAccessToken", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if s, _ := body["code"].(string); s != "" {
			m.lastCode = s
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accessToken":"dd-token","expireIn":7200}`))
	})
	mux.HandleFunc("/v1.0/contact/users/me", func(w http.ResponseWriter, r *http.Request) {
		m.tokenHeader = append(m.tokenHeader, r.Header.Get("x-acs-dingtalk-access-token"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(m.userBody))
	})
	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

func (m *stubDing) input() oauth.SaveLoginAppInput {
	return oauth.SaveLoginAppInput{
		Provider: oauth.ProviderDingTalk, ClientID: "dd-cid", ClientSecret: sp("dd-secret"),
		AuthorizeURL: m.srv.URL + "/oauth2/auth",
		TokenURL:     m.srv.URL + "/v1.0/oauth2/userAccessToken",
		UserinfoURL:  m.srv.URL + "/v1.0/contact/users/me",
		Enabled:      true,
	}
}

// ---- mock:企业微信 ----

type stubWeCom struct {
	srv         *httptest.Server
	whoBody     string
	profileBody string
	profileHits int
}

func newStubWeCom(t *testing.T, whoBody, profileBody string) *stubWeCom {
	t.Helper()
	m := &stubWeCom{whoBody: whoBody, profileBody: profileBody}
	mux := http.NewServeMux()
	mux.HandleFunc("/cgi-bin/gettoken", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errcode":0,"access_token":"wc-token","expires_in":7200}`))
	})
	mux.HandleFunc("/cgi-bin/auth/getuserinfo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(m.whoBody))
	})
	// 档案地址由身份接口的 host 现算,所以这里注册的正是官方那段 path。
	mux.HandleFunc("/cgi-bin/user/get", func(w http.ResponseWriter, r *http.Request) {
		m.profileHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(m.profileBody))
	})
	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

func (m *stubWeCom) input(corpid string) oauth.SaveLoginAppInput {
	return oauth.SaveLoginAppInput{
		Provider: oauth.ProviderWeCom, ClientID: corpid, ClientSecret: sp("wc-secret"),
		AgentID:      "1000002",
		AuthorizeURL: m.srv.URL + "/wwlogin/sso/login",
		TokenURL:     m.srv.URL + "/cgi-bin/gettoken",
		UserinfoURL:  m.srv.URL + "/cgi-bin/auth/getuserinfo",
		Enabled:      true,
	}
}

// ---- mock:飞书 ----

type stubFeishu struct {
	srv      *httptest.Server
	userBody string
}

func newStubFeishu(t *testing.T, userBody string) *stubFeishu {
	t.Helper()
	m := &stubFeishu{userBody: userBody}
	mux := http.NewServeMux()
	mux.HandleFunc("/open-apis/authen/v2/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"access_token":"fs-token","expires_in":7200}`))
	})
	mux.HandleFunc("/open-apis/authen/v1/user_info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(m.userBody))
	})
	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

func (m *stubFeishu) input() oauth.SaveLoginAppInput {
	return oauth.SaveLoginAppInput{
		Provider: oauth.ProviderFeishu, ClientID: "fs-cid", ClientSecret: sp("fs-secret"),
		AuthorizeURL: m.srv.URL + "/open-apis/authen/v1/authorize",
		TokenURL:     m.srv.URL + "/open-apis/authen/v2/oauth/token",
		UserinfoURL:  m.srv.URL + "/open-apis/authen/v1/user_info",
		Enabled:      true,
	}
}

// ---- 登录页入口名 ----

// TestPlatformLoginProviderLabels 冻结三家的对外名字由服务端给这件事:
// 前端直接渲染这个 label,所以它绝不能是硬编码在 Go 里的中文之外的形态(比如主机名回退)。
func TestPlatformLoginProviderLabels(t *testing.T) {
	ding, wecom, feishu := newStubDing(t, `{"nick":"张三","unionId":"u-1"}`),
		newStubWeCom(t, `{"errcode":0,"userid":"alice"}`, `{"errcode":0,"name":"张三"}`),
		newStubFeishu(t, `{"code":0,"data":{"union_id":"on_1","name":"张三"}}`)
	e := buildLoginEnv(t, platformHTTPClient(), func(svc oauth.Service) {
		for _, in := range []oauth.SaveLoginAppInput{ding.input(), wecom.input("wp-corp"), feishu.input()} {
			if _, err := svc.SaveLoginApp(context.Background(), in); err != nil {
				t.Fatalf("SaveLoginApp(%s): %v", in.Provider, err)
			}
		}
	})

	resp := e.get(t, newTestClient(t), e.srv.URL+"/api/auth/oauth/providers")
	var items []publicOAuthProviderDTO
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decode providers: %v", err)
	}
	want := map[string]string{
		oauth.ProviderDingTalk: "钉钉",
		oauth.ProviderFeishu:   "飞书",
		oauth.ProviderWeCom:    "企业微信",
	}
	if len(items) != len(want) {
		t.Fatalf("入口清单 = %+v, want %d 条", items, len(want))
	}
	for _, it := range items {
		if want[it.Provider] != it.Label {
			t.Fatalf("%s 的入口名 = %q, want %q", it.Provider, it.Label, want[it.Provider])
		}
	}
}

// ---- 钉钉的 authCode 回调参数(HTTP 层独有) ----

// TestDingTalkCallbackAcceptsAuthCode 是这条改动的正身:
// 钉钉新版扫码回的是 authCode,不是标准 code。只认 code 的话,用户在钉钉授权页扫完码、
// 回到本站,看到的是「缺少授权码」—— 而平台明明把码送到了。
func TestDingTalkCallbackAcceptsAuthCode(t *testing.T) {
	stub := newStubDing(t, `{"nick":"张三","unionId":"u-authcode-1","email":"zhangsan@corp.example.com"}`)
	e := buildLoginEnv(t, platformHTTPClient(), func(svc oauth.Service) {
		if _, err := svc.SaveLoginApp(context.Background(), stub.input()); err != nil {
			t.Fatalf("SaveLoginApp(dingtalk): %v", err)
		}
	})

	c := newTestClient(t)
	state := e.authorize(t, c, oauth.ProviderDingTalk, "/projects")
	cb := e.srv.URL + "/api/auth/oauth/" + oauth.ProviderDingTalk + "/callback?" +
		url.Values{"authCode": {"dd-auth-code"}, "state": {state}}.Encode()
	resp := e.get(t, c, cb)
	if resp.StatusCode != http.StatusFound {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("authCode 回调 status = %d, want 302, body=%s", resp.StatusCode, raw)
	}
	if got := locationOf(t, resp).String(); got != "/projects" {
		t.Fatalf("落地页 = %q, want 回跳原页面", got)
	}
	if cookieValue(resp, cookieSession) == "" {
		t.Fatalf("authCode 应能换到会话")
	}
	if stub.lastCode != "dd-auth-code" {
		t.Fatalf("钉钉兑换应收到 authCode 的值: %q", stub.lastCode)
	}
	if got := e.sessionOf(t, c); got.Role != identity.DefaultRole {
		t.Fatalf("自动建的账号只该给最小角色: %+v", got)
	}
}

// TestCallbackWithoutAnyCodeReasonsMissing 是反向用例:
// 两种参数名都接受之后,"没有码"仍然要说清楚是 missing_code(而不是崩在兑换里)。
// provider 侧用户拒绝授权回的就是 error 参数 + 一个 code 都别带。
func TestCallbackWithoutAnyCodeReasonsMissing(t *testing.T) {
	stub := newStubDing(t, `{"nick":"张三","unionId":"u-1"}`)
	e := buildLoginEnv(t, platformHTTPClient(), func(svc oauth.Service) {
		if _, err := svc.SaveLoginApp(context.Background(), stub.input()); err != nil {
			t.Fatalf("SaveLoginApp(dingtalk): %v", err)
		}
	})

	c := newTestClient(t)
	state := e.authorize(t, c, oauth.ProviderDingTalk, "")
	resp := e.get(t, c, e.srv.URL+"/api/auth/oauth/"+oauth.ProviderDingTalk+"/callback?"+
		url.Values{"state": {state}, "error": {"access_denied"}}.Encode())
	if got := errorReason(t, resp); got != "provider_denied" {
		t.Fatalf("落点原因码 = %q, want provider_denied", got)
	}
	if stub.tokenHits() != 0 {
		t.Fatalf("没有授权码就不该外拨兑换接口")
	}
}

// tokenHits 数一下 mock 被打了多少次兑换接口(上面的 stub 只记录身份接口)。
func (m *stubDing) tokenHits() int { return len(m.tokenHeader) }

// ---- 企微:整条链 + 可见范围之外拒登 ----

func TestWeComLoginEndToEnd(t *testing.T) {
	stub := newStubWeCom(t,
		`{"errcode":0,"userid":"alice","user_ticket":"tk"}`,
		`{"errcode":0,"userid":"alice","name":"张三","email":"zhangsan@corp.example.com"}`)
	e := buildLoginEnv(t, platformHTTPClient(), func(svc oauth.Service) {
		if _, err := svc.SaveLoginApp(context.Background(), stub.input("wp-corp-9")); err != nil {
			t.Fatalf("SaveLoginApp(wecom): %v", err)
		}
	})

	c := newTestClient(t)
	resp := e.loginOnce(t, c, oauth.ProviderWeCom)
	if cookieValue(resp, cookieSession) == "" {
		t.Fatalf("企微登录应签发会话,落点 = %s", locationOf(t, resp).String())
	}
	// 用户名取的是档案里的 name(平台侧可改),但认人的主键必须是 corpid/userid。
	if got := e.sessionOf(t, c); got.Username == "" {
		t.Fatalf("会话应带用户名: %+v", got)
	}
	if stub.profileHits != 1 {
		t.Fatalf("档案接口应被调用一次(它落在 mock 的 host 上): %d", stub.profileHits)
	}
	n := e.count(t, `SELECT COUNT(1) FROM user_identities WHERE provider = 'wecom' AND external_id = 'wp-corp-9/alice'`)
	if n != 1 {
		t.Fatalf("企微映射的主键应是 corpid/userid,现有 %d 条", n)
	}
}

// TestWeComNonMemberGetsNoAccount:扫码进来但不是本企业成员(只回 openid/external_userid),
// 既不能建号也不能留映射 —— 企微侧的「应用可见范围」是唯一的准入依据,平台必须照它拒。
func TestWeComNonMemberGetsNoAccount(t *testing.T) {
	stub := newStubWeCom(t, `{"errcode":0,"openid":"o-outside","external_userid":"wm_ext"}`, `{"errcode":0}`)
	e := buildLoginEnv(t, platformHTTPClient(), func(svc oauth.Service) {
		if _, err := svc.SaveLoginApp(context.Background(), stub.input("wp-corp")); err != nil {
			t.Fatalf("SaveLoginApp(wecom): %v", err)
		}
	})

	resp := e.loginOnce(t, newTestClient(t), oauth.ProviderWeCom)
	if cookieValue(resp, cookieSession) != "" {
		t.Fatalf("可见范围之外的人不该拿到会话")
	}
	if got := errorReason(t, resp); got != "identity_unavailable" {
		t.Fatalf("落点原因码 = %q, want identity_unavailable", got)
	}
	if stub.profileHits != 0 {
		t.Fatalf("既然拒登就不该再查档案: %d 次", stub.profileHits)
	}
	// 库里只有 bootstrap admin:失败的登录一律不建号。
	if n := e.count(t, `SELECT COUNT(1) FROM users`); n != 1 {
		t.Fatalf("被拒的登录不该建号,库里 %d 个账号", n)
	}
	if n := e.count(t, `SELECT COUNT(1) FROM user_identities`); n != 0 {
		t.Fatalf("被拒的登录不该留映射,现有 %d 条", n)
	}
}

// ---- 配置页:agentId 往返 + 错误码 ----

// TestPlatformSaveViaHttpAgentIDRoundTrip 覆盖管理员在配置页看到的三件事:
// 企微的 agentId 存得回、明文 secret 绝不外泄、少填 agentId 是 422 + 可翻译的 code。
func TestPlatformSaveViaHttpAgentIDRoundTrip(t *testing.T) {
	ding := newStubDing(t, `{"nick":"A","unionId":"u"}`)
	wecom := newStubWeCom(t, `{"errcode":0,"userid":"alice"}`, `{"errcode":0,"name":"张三"}`)
	e := buildLoginEnv(t, platformHTTPClient(), func(svc oauth.Service) {
		if _, err := svc.SaveLoginApp(context.Background(), wecom.input("wp-corp-7")); err != nil {
			t.Fatalf("SaveLoginApp(wecom): %v", err)
		}
	})
	admin := newTestClient(t)
	loginWithClient(t, admin, e.srv.URL)
	csrf := e.csrfOf(t, admin)

	resp := doJSON(t, admin, http.MethodPut,
		e.srv.URL+"/api/admin/oauth/login-apps/"+oauth.ProviderWeCom, csrf,
		`{"clientId":"wp-corp-7","clientSecret":"WECOM-SECRET-PLAINTEXT","enabled":true,"agentId":"1000002"}`)
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT(wecom) status = %d, body=%s", resp.StatusCode, raw)
	}
	if strings.Contains(string(raw), "WECOM-SECRET-PLAINTEXT") {
		t.Fatalf("响应泄漏明文 corpsecret: %s", raw)
	}
	var dto oauthLoginAppDTO
	if err := json.Unmarshal(raw, &dto); err != nil {
		t.Fatalf("decode dto: %v", err)
	}
	if dto.AgentID != "1000002" || !dto.Configured {
		t.Fatalf("agentId 应原样回显供配置页续填: %+v", dto)
	}
	// 回调地址是管理员要在企微应用里登记的那一条(一个应用通常只能登记一个)。
	if !strings.HasSuffix(dto.CallbackURL, "/api/auth/oauth/wecom/callback") {
		t.Fatalf("callbackUrl = %q", dto.CallbackURL)
	}

	// 少填 agentId:422 + agent_id_required,既有配置一个字都不动。
	bad := doJSON(t, admin, http.MethodPut,
		e.srv.URL+"/api/admin/oauth/login-apps/"+oauth.ProviderWeCom, csrf,
		`{"clientId":"wp-corp-7","enabled":true}`)
	badBody, _ := io.ReadAll(bad.Body)
	_ = bad.Body.Close()
	if bad.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(badBody), "agent_id_required") {
		t.Fatalf("缺 agentId status = %d, body=%s", bad.StatusCode, badBody)
	}
	after := e.get(t, admin, e.srv.URL+"/api/admin/oauth/login-apps")
	var apps []oauthLoginAppDTO
	if err := json.NewDecoder(after.Body).Decode(&apps); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(apps) != 1 || apps[0].AgentID != "1000002" {
		t.Fatalf("保存失败不该改动既有配置: %+v", apps)
	}

	// 钉钉压根没有 agentId 这个概念:前端表单复用一张壳,残留的值服务端一律抹掉。
	dd := doJSON(t, admin, http.MethodPut,
		e.srv.URL+"/api/admin/oauth/login-apps/"+oauth.ProviderDingTalk, csrf,
		`{"clientId":"dd-cid","clientSecret":"DD-SECRET","enabled":true,"agentId":"1000002","tokenUrl":"`+
			ding.srv.URL+`/v1.0/oauth2/userAccessToken","userinfoUrl":"`+ding.srv.URL+`/v1.0/contact/users/me"}`)
	ddBody, _ := io.ReadAll(dd.Body)
	_ = dd.Body.Close()
	if dd.StatusCode != http.StatusOK {
		t.Fatalf("PUT(dingtalk) status = %d, body=%s", dd.StatusCode, ddBody)
	}
	var ddDTO oauthLoginAppDTO
	if err := json.Unmarshal(ddBody, &ddDTO); err != nil {
		t.Fatalf("decode dingtalk dto: %v", err)
	}
	if ddDTO.AgentID != "" {
		t.Fatalf("非企微档位不该存住 agentId: %+v", ddDTO)
	}
}

// TestPlatformAppsCannotBindGitCredentials 是三家的能力边界(与 OIDC 同一条例):
// 「绑定 git 账号」读的是仓库凭据那张表,这三家给不了 git token,必须是明确的拒绝而不是 500。
func TestPlatformAppsCannotBindGitCredentials(t *testing.T) {
	ding := newStubDing(t, `{"nick":"A","unionId":"u"}`)
	e := buildLoginEnv(t, platformHTTPClient(), func(svc oauth.Service) {
		if _, err := svc.SaveLoginApp(context.Background(), ding.input()); err != nil {
			t.Fatalf("SaveLoginApp(dingtalk): %v", err)
		}
	})
	admin := newTestClient(t)
	loginWithClient(t, admin, e.srv.URL)

	for _, provider := range []string{oauth.ProviderDingTalk, oauth.ProviderFeishu, oauth.ProviderWeCom} {
		resp := e.get(t, admin, e.srv.URL+"/api/oauth/"+provider+"/bind")
		if resp.StatusCode == http.StatusFound {
			t.Fatalf("%s 不该能走 git 凭据那条绑定流", provider)
		}
		if resp.StatusCode >= 500 {
			t.Fatalf("%s 的拒绝该给人读错误码,得到 %d", provider, resp.StatusCode)
		}
	}
}
