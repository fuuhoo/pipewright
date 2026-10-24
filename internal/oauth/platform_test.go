// platform_test.go 冻结钉钉 / 飞书 / 企业微信三家的报文形状与主键取法。
//
// 这三家没一家符合 RFC 6749:报文怎么发、令牌放哪、成败写在哪个字段、身份字段叫什么,
// 全是一家一套。任何一处写歪,用户看到的都是「授权页一切正常,回来登不进」——
// 所以每家按官方响应原文搭一个 mock 把形状钉死,改实现必须连带改这里。
//
// 三条跨家铁律:
//  1. 主键只取平台发的稳定 ID(钉钉 unionId、飞书 union_id、企微 corpid/userid);
//     昵称、手机号、邮箱一律不当身份 —— 那些可改可抢注,拿它们认人就是账号接管面。
//  2. HTTP 200 不等于成功:三家都把业务码放在报文里(code / errcode),非 0 即失败。
//  3. 拿不到稳定 ID 就是拒登(ErrIdentityUnavailable),绝不退化成按登录名认人。
package oauth

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

// savePlatformApp 存一个启用、端点全部指向 mock 的平台登录应用。
// mock 的 host 只能现拿,所以三家都把 authorize/token/userinfo 三项覆盖成 mock 地址。
func savePlatformApp(t *testing.T, svc Service, provider string, authorize, token, user *string, extra ...SaveLoginAppInput) *LoginAppConfig {
	t.Helper()
	in := SaveLoginAppInput{
		Provider:     provider,
		ClientID:     "platform-client-id",
		ClientSecret: strp("PLATFORM-SECRET-PLAINTEXT"),
		Enabled:      true,
	}
	for _, e := range extra {
		if e.ClientID != "" {
			in.ClientID = e.ClientID
		}
		if e.AgentID != "" {
			in.AgentID = e.AgentID
		}
		if e.Scopes != "" {
			in.Scopes = e.Scopes
		}
	}
	// authorize 覆盖成 mock 后,LoginAuthorizeURL 拼的就是 mock 地址(测试据此断言参数)。
	if authorize != nil {
		in.AuthorizeURL = *authorize
	}
	if token != nil {
		in.TokenURL = *token
	}
	if user != nil {
		in.UserinfoURL = *user
	}
	cfg, err := svc.SaveLoginApp(ctx(), in)
	if err != nil {
		t.Fatalf("SaveLoginApp(%s): %v", provider, err)
	}
	return cfg
}

func platformCallback(provider string) string {
	return "https://app.example.com/api/auth/oauth/" + provider + "/callback"
}

// ---- 钉钉 ----

// stubDingTalk 按新版扫码登录的接口形状搭:
// 兑换只认 JSON body、身份接口只认 x-acs-dingtalk-access-token 头,两处都是硬要求,
// mock 就在这些地方回 400 —— 实现写歪了当场失败,而不是"看起来跑通了"。
type stubDingTalk struct {
	srv        *httptest.Server
	userBody   string
	tokenBody  string
	tokenStat  int
	jsonBodies []map[string]any
	tokenSeen  []string
}

func newStubDingTalk(t *testing.T, userBody string) *stubDingTalk {
	t.Helper()
	m := &stubDingTalk{userBody: userBody, tokenStat: http.StatusOK}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.0/oauth2/userAccessToken", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			// 真实钉钉对 form 编码就是这一句:字段名/编码错了,报错也长得不一样。
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":"Missingbody","message":"Required request body is missing: PLATFORM-SECRET-PLAINTEXT"}`))
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.jsonBodies = append(m.jsonBodies, body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(m.tokenStat)
		if m.tokenBody != "" {
			_, _ = w.Write([]byte(m.tokenBody))
			return
		}
		_, _ = w.Write([]byte(`{"accessToken":"dd-access-token-PLAINTEXT","expireIn":7200}`))
	})
	mux.HandleFunc("/v1.0/contact/users/me", func(w http.ResponseWriter, r *http.Request) {
		// 这里刻意写字面头名而不是复用手里的常量:两边共用一个符号等于自我印证,
		// 实现哪天改用 Authorization: Bearer,断言照样"通过",而真钉钉会当场回 400。
		tok := r.Header.Get("x-acs-dingtalk-access-token")
		m.tokenSeen = append(m.tokenSeen, tok)
		if tok == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":"invalidParameter","message":"缺少参数：x-acs-dingtalk-access-token"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(m.userBody))
	})
	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

func (m *stubDingTalk) path(p string) string { return m.srv.URL + p }

func TestDingTalkLoginRoundTrip(t *testing.T) {
	stub := newStubDingTalk(t, `{"nick":"张三","unionId":"u-union-1","openId":"o-app-1","email":"zhangsan@corp.example.com","mobile":"13800000000"}`)
	svc, _, dbPath := newLoginService(t)
	cfg := savePlatformApp(t, svc, ProviderDingTalk,
		strp(stub.path("/auth")), strp(stub.path("/v1.0/oauth2/userAccessToken")), strp(stub.path("/v1.0/contact/users/me")))
	if cfg.Scopes != "" {
		t.Fatalf("平台档的 scope 该留空(= 用平台默认,便于日后改默认而不洗数据): %q", cfg.Scopes)
	}

	authURL, err := svc.LoginAuthorizeURL(ctx(), ProviderDingTalk, "STATE-1", platformCallback(ProviderDingTalk))
	if err != nil {
		t.Fatalf("LoginAuthorizeURL: %v", err)
	}
	q := mustParseQuery(t, authURL)
	// 新版扫码地址的必填参数:prompt=consent 缺了会跳老版页,回调参数名都不一样。
	if q.Get("response_type") != "code" || q.Get("prompt") != "consent" ||
		q.Get("client_id") != "platform-client-id" || q.Get("state") != "STATE-1" ||
		q.Get("redirect_uri") != platformCallback(ProviderDingTalk) {
		t.Fatalf("钉钉授权参数不符: %v", q)
	}
	if q.Get("scope") != "openid" {
		t.Fatalf("库里 scope 留空时应落平台默认 openid,得到 %q", q.Get("scope"))
	}

	id, err := svc.ExchangeIdentity(ctx(), ProviderDingTalk, "dd-code", platformCallback(ProviderDingTalk))
	if err != nil {
		t.Fatalf("ExchangeIdentity: %v", err)
	}
	if id.ExternalID != "u-union-1" || id.Login != "张三" || id.Email != "zhangsan@corp.example.com" {
		t.Fatalf("钉钉身份不符: %+v", id)
	}
	if id.Provider != ProviderDingTalk {
		t.Fatalf("Provider 应由兑换填入: %+v", id)
	}
	if len(stub.jsonBodies) != 1 {
		t.Fatalf("兑换请求数 = %d", len(stub.jsonBodies))
	}
	body := stub.jsonBodies[0]
	// 字段名是小驼峰且 key 叫 grantType(不是 grant_type):写错了钉钉回 Missingbody。
	if body["clientId"] != "platform-client-id" || body["clientSecret"] != "PLATFORM-SECRET-PLAINTEXT" ||
		body["code"] != "dd-code" || body["grantType"] != "authorization_code" {
		t.Fatalf("钉钉兑换体不符: %v", body)
	}
	if len(stub.tokenSeen) != 1 || stub.tokenSeen[0] != "dd-access-token-PLAINTEXT" {
		t.Fatalf("身份接口应只靠 x-acs-dingtalk-access-token 头拿到令牌: %v", stub.tokenSeen)
	}
	raw, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read db: %v", err)
	}
	if strings.Contains(string(raw), "PLATFORM-SECRET-PLAINTEXT") || strings.Contains(string(raw), "dd-access-token-PLAINTEXT") {
		t.Fatalf("库文件里出现明文 secret 或 access_token")
	}
}

// TestDingTalkIdentityNeedsStableID 是铁律 1 的钉钉落点:
// nick/mobile 再像人也不能拿来认人 —— 只有 unionId/openId 是平台发的不可变 ID。
func TestDingTalkIdentityNeedsStableID(t *testing.T) {
	cases := []struct{ name, body, wantID string }{
		{"有 unionId 就用 unionId", `{"nick":"A","unionId":"u-1","openId":"o-1"}`, "u-1"},
		{"没 unionId 退到 openId", `{"nick":"A","openId":"o-1"}`, "o-1"},
		{"unionId 空串不算", `{"nick":"A","unionId":"","openId":"o-2"}`, "o-2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stub := newStubDingTalk(t, c.body)
			svc, _, _ := newLoginService(t)
			savePlatformApp(t, svc, ProviderDingTalk, nil,
				strp(stub.path("/v1.0/oauth2/userAccessToken")), strp(stub.path("/v1.0/contact/users/me")))
			id, err := svc.ExchangeIdentity(ctx(), ProviderDingTalk, "code", platformCallback(ProviderDingTalk))
			if err != nil {
				t.Fatalf("ExchangeIdentity: %v", err)
			}
			if id.ExternalID != c.wantID {
				t.Fatalf("主键 = %q, want %q", id.ExternalID, c.wantID)
			}
		})
	}

	for _, body := range []string{
		`{"nick":"张三","mobile":"13800000000"}`,
		`{"nick":"张三","unionId":"","openId":""}`,
		`{"email":"zhangsan@corp.example.com"}`,
	} {
		stub := newStubDingTalk(t, body)
		svc, _, _ := newLoginService(t)
		savePlatformApp(t, svc, ProviderDingTalk, nil,
			strp(stub.path("/v1.0/oauth2/userAccessToken")), strp(stub.path("/v1.0/contact/users/me")))
		if _, err := svc.ExchangeIdentity(ctx(), ProviderDingTalk, "code", platformCallback(ProviderDingTalk)); !errors.Is(err, ErrIdentityUnavailable) {
			t.Fatalf("只有昵称/手机/邮箱时该拒登,err = %v body=%s", err, body)
		}
	}
}

// TestDingTalkNicknameChangeSamePerson:改昵称后仍认得是同一个人 —— 这条是「为什么不按名字认人」
// 的正向证明(反过来按 nick 认,改个昵称就变成另一个人,或撞上别人的名字被拒)。
func TestDingTalkNicknameChangeSamePerson(t *testing.T) {
	stub := newStubDingTalk(t, `{"nick":"张三","unionId":"u-1"}`)
	svc, _, _ := newLoginService(t)
	savePlatformApp(t, svc, ProviderDingTalk, nil,
		strp(stub.path("/v1.0/oauth2/userAccessToken")), strp(stub.path("/v1.0/contact/users/me")))
	first, err := svc.ExchangeIdentity(ctx(), ProviderDingTalk, "code", platformCallback(ProviderDingTalk))
	if err != nil {
		t.Fatalf("首次兑换: %v", err)
	}
	stub.userBody = `{"nick":"李四","unionId":"u-1"}`
	second, err := svc.ExchangeIdentity(ctx(), ProviderDingTalk, "code2", platformCallback(ProviderDingTalk))
	if err != nil {
		t.Fatalf("改昵称后兑换: %v", err)
	}
	if first.ExternalID != second.ExternalID {
		t.Fatalf("同一 unionId 换昵称后主键漂移: %q → %q", first.ExternalID, second.ExternalID)
	}
	if second.Login != "李四" {
		t.Fatalf("展示名应跟着平台改: %q", second.Login)
	}
}

// TestDingTalkTokenFailureBecomesHumanError 证明上游原文不回吐:
// 钉钉的错误体里可能带 clientSecret(它会把请求参数打回),错误必须只剩人读的那一份。
func TestDingTalkTokenFailureBecomesHumanError(t *testing.T) {
	stub := newStubDingTalk(t, `{"nick":"A","unionId":"u-1"}`)
	stub.tokenStat = http.StatusBadRequest
	stub.tokenBody = `{"code":"invalidParameter","message":"不合法的临时授权码 PLATFORM-SECRET-PLAINTEXT"}`
	svc, _, _ := newLoginService(t)
	savePlatformApp(t, svc, ProviderDingTalk, nil,
		strp(stub.path("/v1.0/oauth2/userAccessToken")), strp(stub.path("/v1.0/contact/users/me")))
	_, err := svc.ExchangeIdentity(ctx(), ProviderDingTalk, "bad-code", platformCallback(ProviderDingTalk))
	if !errors.Is(err, ErrExchangeFailed) {
		t.Fatalf("err = %v, want ErrExchangeFailed", err)
	}
	if strings.Contains(err.Error(), "PLAINTEXT") || strings.Contains(err.Error(), "invalidParameter") {
		t.Fatalf("错误不该带上游原文: %v", err)
	}
}

// ---- 飞书 ----

// stubFeishu 冻结飞书那套「HTTP 200 + 业务码」的形态:
// 兑换回文扁平({code, access_token}),身份接口却整个塞在 data 信封里,
// 而失败照样回 200 —— 只看状态码的实现会拿着空 token 继续往下走。
type stubFeishu struct {
	srv        *httptest.Server
	tokenBody  string
	userBody   string
	jsonBodies []map[string]any
	bearerSeen []string
	tokenHits  int
	userHits   int
}

func newStubFeishu(t *testing.T, userBody string) *stubFeishu {
	t.Helper()
	m := &stubFeishu{userBody: userBody}
	mux := http.NewServeMux()
	mux.HandleFunc("/open-apis/authen/v2/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		m.tokenHits++
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.jsonBodies = append(m.jsonBodies, body)
		w.Header().Set("Content-Type", "application/json")
		if m.tokenBody != "" {
			_, _ = w.Write([]byte(m.tokenBody))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","access_token":"fs-access-token-PLAINTEXT","expires_in":7200,"token_type":"bearer","scope":""}`))
	})
	mux.HandleFunc("/open-apis/authen/v1/user_info", func(w http.ResponseWriter, r *http.Request) {
		m.userHits++
		m.bearerSeen = append(m.bearerSeen, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(m.userBody))
	})
	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

func (m *stubFeishu) path(p string) string { return m.srv.URL + p }

func (m *stubFeishu) save(t *testing.T, svc Service, extra ...SaveLoginAppInput) {
	t.Helper()
	savePlatformApp(t, svc, ProviderFeishu,
		strp(m.path("/open-apis/authen/v1/authorize")),
		strp(m.path("/open-apis/authen/v2/oauth/token")),
		strp(m.path("/open-apis/authen/v1/user_info")), extra...)
}

func TestFeishuLoginRoundTrip(t *testing.T) {
	stub := newStubFeishu(t, `{"code":0,"msg":"success","data":{"name":"张三","en_name":"zhangsan","open_id":"ou_1","union_id":"on_1","avatar_url":"https://s/a.png"}}`)
	svc, _, _ := newLoginService(t)
	stub.save(t, svc)

	authURL, err := svc.LoginAuthorizeURL(ctx(), ProviderFeishu, "S-1", platformCallback(ProviderFeishu))
	if err != nil {
		t.Fatalf("LoginAuthorizeURL: %v", err)
	}
	q := mustParseQuery(t, authURL)
	if q.Get("client_id") != "platform-client-id" || q.Get("response_type") != "code" ||
		q.Get("state") != "S-1" || q.Get("redirect_uri") != platformCallback(ProviderFeishu) {
		t.Fatalf("飞书授权参数不符: %v", q)
	}
	// 默认一个 scope 都不申请:申请应用没开通的 scope 会被飞书打回(20027),
	// 等于给"默认配置"埋一个必然失败的坑。这条断言就是为了挡住"顺手加个 email scope"。
	if _, hasScope := q["scope"]; hasScope {
		t.Fatalf("默认不该带 scope(会被没开通的应用打回 20027): %v", q)
	}

	id, err := svc.ExchangeIdentity(ctx(), ProviderFeishu, "fs-code", platformCallback(ProviderFeishu))
	if err != nil {
		t.Fatalf("ExchangeIdentity: %v", err)
	}
	if id.ExternalID != "on_1" || id.Login != "张三" {
		t.Fatalf("飞书身份不符: %+v", id)
	}
	if id.Email != "" {
		t.Fatalf("默认 scope 下拿不到邮箱,应为空: %q", id.Email)
	}
	if len(stub.jsonBodies) != 1 {
		t.Fatalf("兑换请求数 = %d", len(stub.jsonBodies))
	}
	body := stub.jsonBodies[0]
	if body["grant_type"] != "authorization_code" || body["client_id"] != "platform-client-id" ||
		body["client_secret"] != "PLATFORM-SECRET-PLAINTEXT" || body["code"] != "fs-code" ||
		body["redirect_uri"] != platformCallback(ProviderFeishu) {
		t.Fatalf("飞书兑换体不符: %v", body)
	}
	if len(stub.bearerSeen) != 1 || stub.bearerSeen[0] != "Bearer fs-access-token-PLAINTEXT" {
		t.Fatalf("user_info 用标准 Bearer: %v", stub.bearerSeen)
	}
}

// TestFeishuScopeOverrideApplies:要邮箱就得显式配 scope(并在开放平台开通该权限)。
func TestFeishuScopeOverrideApplies(t *testing.T) {
	stub := newStubFeishu(t, `{"code":0,"data":{"name":"张三","union_id":"on_1","email":"zhangsan@corp.example.com"}}`)
	svc, _, _ := newLoginService(t)
	stub.save(t, svc, SaveLoginAppInput{Scopes: "contact:user.email:readonly"})

	authURL, err := svc.LoginAuthorizeURL(ctx(), ProviderFeishu, "S", platformCallback(ProviderFeishu))
	if err != nil {
		t.Fatalf("LoginAuthorizeURL: %v", err)
	}
	if got := mustParseQuery(t, authURL).Get("scope"); got != "contact:user.email:readonly" {
		t.Fatalf("scope 覆盖应逐字生效: %q", got)
	}
	id, err := svc.ExchangeIdentity(ctx(), ProviderFeishu, "code", platformCallback(ProviderFeishu))
	if err != nil {
		t.Fatalf("ExchangeIdentity: %v", err)
	}
	if id.Email != "zhangsan@corp.example.com" {
		t.Fatalf("邮箱应来自 data.email: %q", id.Email)
	}
}

// TestFeishuBusinessCodeMeansFailure 是铁律 2:两个接口都在 HTTP 200 里回非 0 业务码。
func TestFeishuBusinessCodeMeansFailure(t *testing.T) {
	cases := []struct{ name, tokenBody, userBody string }{
		{"兑换被 scope 打回 20027", `{"code":20027,"msg":"scope 未开通"}`, `{"code":0,"data":{"union_id":"on_1"}}`},
		{"兑换业务码是字符串非 0", `{"code":"20003","msg":"invalid code"}`, `{"code":0,"data":{"union_id":"on_1"}}`},
		{"兑换成功但没 token", `{"code":0,"msg":"success"}`, `{"code":0,"data":{"union_id":"on_1"}}`},
		{"身份接口业务码非 0", `{"code":0,"access_token":"t"}`, `{"code":99991663,"msg":"token expired"}`},
		// 业务码非 0 却照样带着 data:这一形最阴 —— 只看"有没有 data"就放行,
		// 等于把平台明确判为无效的载荷当身份用。
		{"业务码非 0 但带 data", `{"code":0,"access_token":"t"}`, `{"code":99991668,"msg":"invalid authorization code","data":{"union_id":"on-victim","name":"管理员"}}`},
		{"身份接口没有 data 信封", `{"code":0,"access_token":"t"}`, `{"code":0,"msg":"success"}`},
		{"身份接口 data 不是对象", `{"code":0,"access_token":"t"}`, `{"code":0,"data":"unexpected"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stub := newStubFeishu(t, c.userBody)
			stub.tokenBody = c.tokenBody
			svc, _, _ := newLoginService(t)
			stub.save(t, svc)
			_, err := svc.ExchangeIdentity(ctx(), ProviderFeishu, "code", platformCallback(ProviderFeishu))
			if !errors.Is(err, ErrExchangeFailed) {
				t.Fatalf("err = %v, want ErrExchangeFailed", err)
			}
			if strings.Contains(err.Error(), "token expired") || strings.Contains(err.Error(), "scope") {
				t.Fatalf("错误不该带上游原文: %v", err)
			}
		})
	}
	// 成功路径上不该有任何一击落空:两个接口各打一次。
	stub := newStubFeishu(t, `{"code":0,"data":{"union_id":"on_1"}}`)
	svc, _, _ := newLoginService(t)
	stub.save(t, svc)
	if _, err := svc.ExchangeIdentity(ctx(), ProviderFeishu, "code", platformCallback(ProviderFeishu)); err != nil {
		t.Fatalf("ExchangeIdentity: %v", err)
	}
	if stub.tokenHits != 1 || stub.userHits != 1 {
		t.Fatalf("兑换/身份各一次: token=%d user=%d", stub.tokenHits, stub.userHits)
	}
}

// TestFeishuIdentityNeedsStableID:union_id 优先,退到 open_id,都没有就拒登。
func TestFeishuIdentityNeedsStableID(t *testing.T) {
	stub := newStubFeishu(t, `{"code":0,"data":{"name":"张三","open_id":"ou_1","enterprise_email":"z@corp.example.com"}}`)
	svc, _, _ := newLoginService(t)
	stub.save(t, svc)
	id, err := svc.ExchangeIdentity(ctx(), ProviderFeishu, "code", platformCallback(ProviderFeishu))
	if err != nil {
		t.Fatalf("ExchangeIdentity: %v", err)
	}
	if id.ExternalID != "ou_1" {
		t.Fatalf("缺 union_id 时应退到 open_id: %q", id.ExternalID)
	}
	if id.Email != "z@corp.example.com" {
		t.Fatalf("email 缺失时应退到 enterprise_email: %q", id.Email)
	}

	for _, body := range []string{
		`{"code":0,"data":{"name":"张三","email":"z@corp.example.com"}}`,
		`{"code":0,"data":{"union_id":"","open_id":""}}`,
	} {
		stub := newStubFeishu(t, body)
		svc, _, _ := newLoginService(t)
		stub.save(t, svc)
		if _, err := svc.ExchangeIdentity(ctx(), ProviderFeishu, "code", platformCallback(ProviderFeishu)); !errors.Is(err, ErrIdentityUnavailable) {
			t.Fatalf("只有昵称/邮箱时该拒登,err = %v body=%s", err, body)
		}
	}
}

// ---- 企业微信 ----

// stubWeCom 按 cgi 那套形状搭:三步全用 query 传令牌,一个请求头都没有;
// 成败看 errcode;非本企业成员只回 openid/external_userid。
type stubWeCom struct {
	srv           *httptest.Server
	tokenQuery    []url.Values
	whoQuery      []url.Values
	profileQuery  []url.Values
	whoBody       string
	profileBody   string
	profileStatus int
	tokenBody     string
	bearerSeen    []string
}

func newStubWeCom(t *testing.T, whoBody, profileBody string) *stubWeCom {
	t.Helper()
	m := &stubWeCom{whoBody: whoBody, profileBody: profileBody, profileStatus: http.StatusOK}
	mux := http.NewServeMux()
	mux.HandleFunc("/cgi-bin/gettoken", func(w http.ResponseWriter, r *http.Request) {
		m.tokenQuery = append(m.tokenQuery, r.URL.Query())
		m.bearerSeen = append(m.bearerSeen, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		if m.tokenBody != "" {
			_, _ = w.Write([]byte(m.tokenBody))
			return
		}
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok","access_token":"wc-access-token-PLAINTEXT","expires_in":7200}`))
	})
	mux.HandleFunc("/cgi-bin/auth/getuserinfo", func(w http.ResponseWriter, r *http.Request) {
		m.whoQuery = append(m.whoQuery, r.URL.Query())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(m.whoBody))
	})
	mux.HandleFunc("/cgi-bin/user/get", func(w http.ResponseWriter, r *http.Request) {
		m.profileQuery = append(m.profileQuery, r.URL.Query())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(m.profileStatus)
		if m.profileBody != "" {
			_, _ = w.Write([]byte(m.profileBody))
			return
		}
		_, _ = w.Write([]byte(`{"errcode":48002,"errmsg":"api forbidden for self-built app"}`))
	})
	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

func (m *stubWeCom) path(p string) string { return m.srv.URL + p }

func (m *stubWeCom) save(t *testing.T, svc Service, corpid string, extra ...SaveLoginAppInput) *LoginAppConfig {
	t.Helper()
	in := SaveLoginAppInput{AgentID: "1000002"}
	in.ClientID = corpid
	if len(extra) > 0 && extra[0].AgentID != "" {
		in.AgentID = extra[0].AgentID
	}
	return savePlatformApp(t, svc, ProviderWeCom,
		strp(m.path("/wwlogin/sso/login")),
		strp(m.path("/cgi-bin/gettoken")),
		strp(m.path("/cgi-bin/auth/getuserinfo")), in)
}

func TestWeComLoginRoundTrip(t *testing.T) {
	stub := newStubWeCom(t,
		`{"errcode":0,"errmsg":"ok","userid":"alice","user_ticket":"ticket-1","expires_in":7200}`,
		`{"errcode":0,"errmsg":"ok","userid":"alice","name":"张三","email":"zhangsan@corp.example.com","mobile":"13800000000"}`)
	svc, _, dbPath := newLoginService(t)
	cfg := stub.save(t, svc, "wp-corpid-1")
	if cfg.AgentID != "1000002" {
		t.Fatalf("企微 AgentId 应回显供配置页续填: %q", cfg.AgentID)
	}
	if cfg.Scopes != "" {
		t.Fatalf("企微扫码地址压根没有 scope 参数,库里不该留残渣: %q", cfg.Scopes)
	}

	authURL, err := svc.LoginAuthorizeURL(ctx(), ProviderWeCom, "S-9", platformCallback(ProviderWeCom))
	if err != nil {
		t.Fatalf("LoginAuthorizeURL: %v", err)
	}
	q := mustParseQuery(t, authURL)
	// 参数名整套换掉:appid 装 corpid、还要带 agentid,压根没有 client_id/response_type/scope。
	if q.Get("login_type") != "CorpApp" || q.Get("appid") != "wp-corpid-1" || q.Get("agentid") != "1000002" ||
		q.Get("redirect_uri") != platformCallback(ProviderWeCom) || q.Get("state") != "S-9" {
		t.Fatalf("企微扫码参数不符: %v", q)
	}
	if _, has := q["client_id"]; has {
		t.Fatalf("企微没有 client_id 参数: %v", q)
	}
	if _, has := q["scope"]; has {
		t.Fatalf("企微扫码地址不接受 scope: %v", q)
	}

	id, err := svc.ExchangeIdentity(ctx(), ProviderWeCom, "wc-code", platformCallback(ProviderWeCom))
	if err != nil {
		t.Fatalf("ExchangeIdentity: %v", err)
	}
	if id.ExternalID != "wp-corpid-1/alice" || id.Login != "张三" || id.Email != "zhangsan@corp.example.com" {
		t.Fatalf("企微身份不符: %+v", id)
	}
	// 三步顺序 + 令牌全程在 query(不是请求头)。
	if len(stub.tokenQuery) != 1 || stub.tokenQuery[0].Get("corpid") != "wp-corpid-1" ||
		stub.tokenQuery[0].Get("corpsecret") != "PLATFORM-SECRET-PLAINTEXT" {
		t.Fatalf("gettoken 参数不符: %v", stub.tokenQuery)
	}
	if len(stub.whoQuery) != 1 || stub.whoQuery[0].Get("access_token") != "wc-access-token-PLAINTEXT" ||
		stub.whoQuery[0].Get("code") != "wc-code" {
		t.Fatalf("getuserinfo 参数不符: %v", stub.whoQuery)
	}
	if len(stub.profileQuery) != 1 || stub.profileQuery[0].Get("access_token") != "wc-access-token-PLAINTEXT" ||
		stub.profileQuery[0].Get("userid") != "alice" {
		t.Fatalf("user/get 参数不符: %v", stub.profileQuery)
	}
	for _, seen := range stub.bearerSeen {
		if seen != "" {
			t.Fatalf("企微这套 cgi 不用请求头传令牌: %q", seen)
		}
	}
	// 档案地址跟着配置的 userinfo host 走(否则配了内网入口的实例会被这一步绕过官方域名)。
	if !strings.HasPrefix(stub.profileQuery[0].Get("access_token"), "wc-") {
		t.Fatalf("档案请求应打到同一个入口: %v", stub.profileQuery)
	}
	raw, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read db: %v", err)
	}
	if strings.Contains(string(raw), "PLATFORM-SECRET-PLAINTEXT") || strings.Contains(string(raw), "wc-access-token-PLAINTEXT") {
		t.Fatalf("库文件里出现明文 corpsecret 或 access_token")
	}
}

// TestWeComExternalIDCarriesCorpID 挡住一个具体接管路径:库里这个 provider 只有一行配置,
// 管理员换个 corpid 重新保存后,裸 userid 会把新企业的成员接到旧企业同 userid 的账号上。
func TestWeComExternalIDCarriesCorpID(t *testing.T) {
	stub := newStubWeCom(t, `{"errcode":0,"userid":"alice"}`, `{"errcode":0,"name":"张三"}`)
	svc, _, _ := newLoginService(t)
	stub.save(t, svc, "corp-A")
	first, err := svc.ExchangeIdentity(ctx(), ProviderWeCom, "code", platformCallback(ProviderWeCom))
	if err != nil {
		t.Fatalf("corp-A 兑换: %v", err)
	}

	stub.save(t, svc, "corp-B")
	second, err := svc.ExchangeIdentity(ctx(), ProviderWeCom, "code", platformCallback(ProviderWeCom))
	if err != nil {
		t.Fatalf("corp-B 兑换: %v", err)
	}
	if first.ExternalID == second.ExternalID {
		t.Fatalf("换企业后主键必须变,否则 corp-B 的 alice 直接登进 corp-A 的账号: %q", first.ExternalID)
	}
	if first.ExternalID != "corp-A/alice" || second.ExternalID != "corp-B/alice" {
		t.Fatalf("主键应为 corpid/userid: %q / %q", first.ExternalID, second.ExternalID)
	}
}

// TestWeComRejectsNonMember:非本企业成员(扫码的外部联系人)只拿到 openid/external_userid,
// 本来就不在应用可见范围里 → 拒登,而不是给个 openid 当主键建个号。
func TestWeComRejectsNonMember(t *testing.T) {
	cases := []string{
		`{"errcode":0,"openid":"o-external-1","external_userid":"wm_ext_1"}`,
		`{"errcode":0,"userid":""}`,
	}
	for _, who := range cases {
		stub := newStubWeCom(t, who, `{"errcode":0,"name":"外部联系人"}`)
		svc, _, _ := newLoginService(t)
		stub.save(t, svc, "wp-corpid")
		_, err := svc.ExchangeIdentity(ctx(), ProviderWeCom, "code", platformCallback(ProviderWeCom))
		if !errors.Is(err, ErrIdentityUnavailable) {
			t.Fatalf("err = %v, want ErrIdentityUnavailable body=%s", err, who)
		}
		if len(stub.profileQuery) != 0 {
			t.Fatalf("既然拒登就不该再查档案: %v", stub.profileQuery)
		}
	}
}

// TestWeComProfileIsBestEffort:2022-06-20 起自建应用默认不再返回邮箱/手机号,
// user/get 被拒(48002)属正常形态 —— 登录照样成,只是没有昵称/邮箱。
func TestWeComProfileIsBestEffort(t *testing.T) {
	cases := []struct {
		name, profileBody string
		status            int
	}{
		{"档案接口回无权限", `{"errcode":48002,"errmsg":"This ip is not in the whitelist"}`, http.StatusOK},
		{"档案接口回 500", `{"errcode":-1,"errmsg":"system error"}`, http.StatusInternalServerError},
		{"档案接口回非 JSON", `<html>gateway</html>`, http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stub := newStubWeCom(t, `{"errcode":0,"userid":"alice"}`, c.profileBody)
			stub.profileStatus = c.status
			svc, _, _ := newLoginService(t)
			stub.save(t, svc, "wp-corpid")
			id, err := svc.ExchangeIdentity(ctx(), ProviderWeCom, "code", platformCallback(ProviderWeCom))
			if err != nil {
				t.Fatalf("档案拿不到不该毁掉登录: %v", err)
			}
			if id.ExternalID != "wp-corpid/alice" || id.Login != "alice" || id.Email != "" {
				t.Fatalf("应退回 userid 当展示名且无邮箱: %+v", id)
			}
		})
	}
}

// TestWeComGetTokenFailure:corpid/corpsecret 错在第一步就断,不该继续往下打。
func TestWeComGetTokenFailure(t *testing.T) {
	stub := newStubWeCom(t, `{"errcode":0,"userid":"alice"}`, "")
	stub.tokenBody = `{"errcode":40013,"errmsg":"invalid corpid PLATFORM-SECRET-PLAINTEXT"}`
	svc, _, _ := newLoginService(t)
	stub.save(t, svc, "bad-corpid")
	_, err := svc.ExchangeIdentity(ctx(), ProviderWeCom, "code", platformCallback(ProviderWeCom))
	if !errors.Is(err, ErrExchangeFailed) {
		t.Fatalf("err = %v, want ErrExchangeFailed", err)
	}
	if strings.Contains(err.Error(), "invalid corpid") || strings.Contains(err.Error(), "40013") {
		t.Fatalf("错误不该带上游原文: %v", err)
	}
	if len(stub.whoQuery) != 0 {
		t.Fatalf("第一步失败就该停,不该再拿空 token 去换身份: %v", stub.whoQuery)
	}
}

// ---- 保存期校验与枚举边界 ----

func TestSavePlatformAppValidation(t *testing.T) {
	svc, _, _ := newLoginService(t)
	cases := []struct {
		name string
		in   SaveLoginAppInput
		want error
	}{
		{"企微缺 AgentId", SaveLoginAppInput{
			Provider: ProviderWeCom, ClientID: "wp-corp", ClientSecret: strp("s"), Enabled: true,
		}, ErrAgentIDRequired},
		{"企微 AgentId 全空格", SaveLoginAppInput{
			Provider: ProviderWeCom, ClientID: "wp-corp", ClientSecret: strp("s"), Enabled: true, AgentID: "   ",
		}, ErrAgentIDRequired},
		{"钉钉端点覆盖不是 http(s)", SaveLoginAppInput{
			Provider: ProviderDingTalk, ClientID: "dd", ClientSecret: strp("s"), Enabled: true,
			TokenURL: "ftp://dingtalk-proxy.internal/token",
		}, ErrPlatformConfigInvalid},
		{"企微端点覆盖带账号密码", SaveLoginAppInput{
			Provider: ProviderWeCom, ClientID: "wp", ClientSecret: strp("s"), Enabled: true,
			AgentID: "1", UserinfoURL: "https://admin:pass@proxy.internal/cgi-bin/auth/getuserinfo",
		}, ErrPlatformConfigInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := svc.SaveLoginApp(ctx(), c.in); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}

	// 三家都校验失败过,库里一行都不该留下(否则配置页出现一张点了就报错的卡)。
	for _, p := range []string{ProviderDingTalk, ProviderFeishu, ProviderWeCom} {
		row, err := svc.LoginApp(ctx(), p)
		if err != nil {
			t.Fatalf("LoginApp(%s): %v", p, err)
		}
		if row.Configured {
			t.Fatalf("%s 全部保存都失败时不该有已配置行: %+v", p, row)
		}
	}
}

// TestPlatformProvidersNeverEnterGitCredentialFlow 是三家的归属边界:
// 它们给不了 git 令牌,所以只能出现在「平台登录」档;「仓库凭据」那条流一律不认识这个名字。
func TestPlatformProvidersNeverEnterGitCredentialFlow(t *testing.T) {
	for _, p := range []string{ProviderDingTalk, ProviderFeishu, ProviderWeCom} {
		if _, err := normalizeProvider(p); !errors.Is(err, ErrInvalidProvider) {
			t.Fatalf("git 枚举该拒 %s,得到 %v", p, err)
		}
		if _, err := resolveProvider(p, "https://example.com", ""); !errors.Is(err, ErrInvalidProvider) {
			t.Fatalf("resolveProvider 该拒 %s,得到 %v", p, err)
		}
		svc, _, _ := newLoginService(t)
		if _, err := svc.SaveApp(ctx(), SaveAppInput{
			Provider: p, ClientID: "c", ClientSecret: strp("s"), Enabled: true,
		}); !errors.Is(err, ErrInvalidProvider) {
			t.Fatalf("SaveApp(%s) err = %v, want ErrInvalidProvider", p, err)
		}
		if got, err := normalizeLoginProvider(p); err != nil || got != p {
			t.Fatalf("normalizeLoginProvider(%s) = %q, %v", p, got, err)
		}
	}
}

// TestPlatformFieldsNeverLeakBetweenPurposes 保证档位切换不互相污染:
// 前端表单在四张卡间复用同一份壳,切档时残留的 AgentId / scope 必须被抹掉。
func TestPlatformFieldsNeverLeakBetweenPurposes(t *testing.T) {
	svc, db, _ := newLoginService(t)

	// git 档:七项非 git 字段全空,包括 agent_id。
	if _, err := svc.SaveLoginApp(ctx(), SaveLoginAppInput{
		Provider: ProviderGitHub, ClientID: "gh", ClientSecret: strp("s"), Enabled: true,
		AgentID: "1000002", Scopes: "openid", AuthorizeURL: "https://evil/token",
	}); err != nil {
		t.Fatalf("SaveLoginApp(github): %v", err)
	}
	// oidc 档:agent_id 同样落空(它压根没有企业应用这一说)。
	stubOidc := newStubOidc(t, `{"sub":"s-1"}`)
	if _, err := svc.SaveLoginApp(ctx(), SaveLoginAppInput{
		Provider: ProviderOIDC, ClientID: "oidc", ClientSecret: strp("s"), Enabled: true,
		DiscoveryURL: stubOidc.discoveryURL(), AgentID: "1000002",
	}); err != nil {
		t.Fatalf("SaveLoginApp(oidc): %v", err)
	}
	// 钉钉:AgentId 不属于它,存进去也是永远不生效的残渣。
	dd := newStubDingTalk(t, `{"nick":"A","unionId":"u"}`)
	if _, err := svc.SaveLoginApp(ctx(), SaveLoginAppInput{
		Provider: ProviderDingTalk, ClientID: "dd", ClientSecret: strp("s"), Enabled: true,
		TokenURL: dd.path("/v1.0/oauth2/userAccessToken"), UserinfoURL: dd.path("/v1.0/contact/users/me"),
		AgentID: "1000002", DiscoveryURL: "https://sso/.well-known/openid-configuration", IDClaim: "employee_id",
	}); err != nil {
		t.Fatalf("SaveLoginApp(dingtalk): %v", err)
	}

	for _, provider := range []string{ProviderGitHub, ProviderOIDC, ProviderDingTalk} {
		var agentID string
		if err := db.QueryRowContext(ctx(), `SELECT agent_id FROM oauth_login_apps WHERE provider = ?`, provider).Scan(&agentID); err != nil {
			t.Fatalf("read %s: %v", provider, err)
		}
		if agentID != "" {
			t.Fatalf("%s 不该留下 agent_id: %q", provider, agentID)
		}
	}

	var ddScopes, ddDiscovery, ddClaim string
	if err := db.QueryRowContext(ctx(),
		`SELECT scopes, discovery_url, id_claim FROM oauth_login_apps WHERE provider = 'dingtalk'`).
		Scan(&ddScopes, &ddDiscovery, &ddClaim); err != nil {
		t.Fatalf("read dingtalk: %v", err)
	}
	if ddDiscovery != "" || ddClaim != "" {
		t.Fatalf("钉钉不该留 OIDC 字段: discovery=%q claim=%q", ddDiscovery, ddClaim)
	}
	if ddScopes != "" {
		t.Fatalf("钉钉 scope 留空即平台默认,不该把默认值写进库: %q", ddScopes)
	}
}

func mustParseQuery(t *testing.T, rawURL string) url.Values {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	return u.Query()
}

// TestPlatformDefaultEndpoints 钉住三家在生产环境真正要拨的官方地址与默认 scope。
// 上面所有 mock 用例都把端点覆盖成了本地地址,等于完全测不到这几个常量 ——
// 而它们写歪的表现正是「开发机上一切正常,用户那台登不进」,只能靠这条断言兜。
func TestPlatformDefaultEndpoints(t *testing.T) {
	cases := []struct{ provider, authorize, token, user, scope string }{
		{
			ProviderDingTalk,
			"https://login.dingtalk.com/oauth2/auth",
			"https://api.dingtalk.com/v1.0/oauth2/userAccessToken",
			"https://api.dingtalk.com/v1.0/contact/users/me",
			"openid",
		},
		{
			ProviderFeishu,
			"https://accounts.feishu.cn/open-apis/authen/v1/authorize",
			"https://open.feishu.cn/open-apis/authen/v2/oauth/token",
			"https://open.feishu.cn/open-apis/authen/v1/user_info",
			"", // 默认不申请 scope:申请没开通的会被打回 20027
		},
		{
			ProviderWeCom,
			"https://login.work.weixin.qq.com/wwlogin/sso/login",
			"https://qyapi.weixin.qq.com/cgi-bin/gettoken",
			"https://qyapi.weixin.qq.com/cgi-bin/auth/getuserinfo",
			"", // 扫码地址压根没有 scope 参数
		},
	}
	for _, c := range cases {
		t.Run(c.provider, func(t *testing.T) {
			p, err := resolvePlatformProvider(loginAppRow{provider: c.provider})
			if err != nil {
				t.Fatalf("resolvePlatformProvider(%s): %v", c.provider, err)
			}
			if p.AuthorizeURL != c.authorize || p.TokenURL != c.token || p.UserURL != c.user {
				t.Fatalf("%s 默认端点不符: authorize=%q token=%q user=%q", c.provider, p.AuthorizeURL, p.TokenURL, p.UserURL)
			}
			if got := platformScope(p); got != c.scope {
				t.Fatalf("%s 默认 scope = %q, want %q", c.provider, got, c.scope)
			}
			if p.flow == nil {
				t.Fatalf("%s 必须带自己的链路实现,否则会被当成标准 RFC 6749 走", c.provider)
			}
		})
	}

	// 企微第三步的档案地址由配置的身份接口现算:默认就落在官方 cgi host 上。
	if got := wecomProfileURL(cases[2].user); got != "https://qyapi.weixin.qq.com/cgi-bin/user/get" {
		t.Fatalf("默认档案地址 = %q", got)
	}
	// 地址解析不出来时退回官方值,而不是返回空串让这一步静默不发。
	if got := wecomProfileURL("not a url"); got != "https://qyapi.weixin.qq.com/cgi-bin/user/get" {
		t.Fatalf("坏地址应退回官方: %q", got)
	}
}
