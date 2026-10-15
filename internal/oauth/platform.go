// platform.go 是登录流的「国内三家平台」这一支:钉钉 / 飞书 / 企业微信。
//
// 为什么不复用 oauth.go 那条标准兑换路径:这三家的报文形态各是一家一套,和 RFC 6749
// 那套「form POST + Bearer 头 + 顶层 access_token」都对不上 ——
//
//	钉钉   : 授权地址带 prompt=consent、回调参数名官方文档是 authCode;
//	         兑换要 **JSON body**(clientId/clientSecret/code/grantType)且只认 JSON,
//	         身份接口用自定义头 **x-acs-dingtalk-access-token** 而非 Authorization: Bearer。
//	飞书   : 兑换回文是扁平的 {code, access_token}(code==0 才算成功,HTTP 200 不代表成功),
//	         身份接口却把字段整个塞在 **data 信封**里。
//	企业微信: 根本是**三步** —— corpid+corpsecret 先换企业级 access_token,
//	         再用它把 code 换成 userid,最后 userid 查档案;令牌全程走 query 参数。
//	         且扫码地址不用 client_id 而是 appid(=corpid)+ agentid。
//
// 这些差异任何一处写错,表现都是「授权页正常,回来登不进」,所以每个形状都有对应的
// mock 契约测试兜着(见 platform_test.go)。
//
// 主键一律取平台侧的**稳定 ID**(钉钉 unionId、飞书 union_id、企微 corpid/userid),
// 绝不用昵称/手机号/邮箱认人 —— 那些可改可被抢注,拿它们认人就是账号接管面。
// 三家都可能拿不到邮箱(企微自建应用自 2022-06-20 起默认不再返回邮箱/手机号等敏感字段,
// 钉钉只有用户绑定了邮箱才有),邮箱为空时由域名白名单 fail closed,配置页把这写明。
package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// 三家官方端点。地址只有一个官方主机,故写成常量;运营者若要用代理/国际版地址,
// 用配置页的「端点覆盖」填(留空即用这里的默认值)。
const (
	dingtalkAuthorizeURL  = "https://login.dingtalk.com/oauth2/auth"
	dingtalkTokenURL      = "https://api.dingtalk.com/v1.0/oauth2/userAccessToken"
	dingtalkUserURL       = "https://api.dingtalk.com/v1.0/contact/users/me"
	dingtalkTokenHeader   = "x-acs-dingtalk-access-token"
	defaultDingTalkScopes = "openid"

	feishuAuthorizeURL = "https://accounts.feishu.cn/open-apis/authen/v1/authorize"
	feishuTokenURL     = "https://open.feishu.cn/open-apis/authen/v2/oauth/token"
	feishuUserURL      = "https://open.feishu.cn/open-apis/authen/v1/user_info"

	wecomAuthorizeURL = "https://login.work.weixin.qq.com/wwlogin/sso/login"
	wecomTokenURL     = "https://qyapi.weixin.qq.com/cgi-bin/gettoken"
	wecomUserURL      = "https://qyapi.weixin.qq.com/cgi-bin/auth/getuserinfo"
	// wecomProfilePath 只在「尽力拿昵称/邮箱」时用,失败不影响登录成败。
	// 它不是可配端点:企微三套 cgi 全在同一个 host 上,档案地址由配置的身份接口现算
	// (见 wecomProfileURL),否则配了代理/内网入口的实例会被这一步绕过。
	wecomProfilePath = "/cgi-bin/user/get"
)

// 平台专属错误。错误体绝不含 secret / access_token,也不回显上游原文。
var (
	// ErrAgentIDRequired 表示企业微信没填 AgentId:自建应用扫码地址没有它就换不出成员身份,
	// 配错的人在保存期就该看到,而不是点了登录被跳回一句看不懂的原因码。
	ErrAgentIDRequired = errors.New("oauth: agent id required for wecom login app")
	// ErrPlatformConfigInvalid 表示平台登录应用的端点覆盖形态不对(不是可外拨的 http(s) 地址)。
	ErrPlatformConfigInvalid = errors.New("oauth: platform login endpoints invalid")
)

// platformCreds 是一次兑换需要的凭据与配置。明文(clientSecret)只在调用栈里活着,
// 既不落库也不进错误体。
type platformCreds struct {
	clientID     string
	clientSecret string
	agentID      string
}

// platformFlow 是一条非标准 OAuth 报文的登录链路:自己拼授权地址、自己走完兑换。
type platformFlow interface {
	// authorizeURL 用平台自己的参数名拼出浏览器跳转地址。
	authorizeURL(p Provider, c platformCreds, state, redirectURI string) (string, error)
	// identity 从 code 一路走到身份信息。拿不到稳定 ID 就返回空 ExternalID,
	// 由调用方(ExchangeIdentity)统一落成 ErrIdentityUnavailable —— 绝不退化成按名字认人。
	identity(ctx context.Context, client *http.Client, p Provider, c platformCreds, code, redirectURI string) (Identity, error)
}

// platformDefaults 是三家平台的内置模板(端点默认值 + 异形链路 + 主键字段)。
// 刻意与 presets 分开:这三家永远不参与「取仓库凭据」那条流。
var platformDefaults = map[string]Provider{
	ProviderDingTalk: {
		Name:         ProviderDingTalk,
		AuthorizeURL: dingtalkAuthorizeURL,
		TokenURL:     dingtalkTokenURL,
		UserURL:      dingtalkUserURL,
		LoginScopes:  strings.Fields(defaultDingTalkScopes),
		flow:         dingTalkFlow{},
	},
	ProviderFeishu: {
		Name:         ProviderFeishu,
		AuthorizeURL: feishuAuthorizeURL,
		TokenURL:     feishuTokenURL,
		UserURL:      feishuUserURL,
		// 默认不申请任何 scope:基础身份字段(open_id/union_id/name)本就不需要权限,
		// 而申请一个应用没开通的 scope 会被飞书直接打回(错误码 20027),
		// 等于给「默认配置」埋一个必然失败的坑。要邮箱就在配置页显式加 scope。
		LoginScopes: nil,
		flow:        feishuFlow{},
	},
	ProviderWeCom: {
		Name:         ProviderWeCom,
		AuthorizeURL: wecomAuthorizeURL,
		TokenURL:     wecomTokenURL,
		UserURL:      wecomUserURL,
		// 扫码地址没有 scope 参数,身份范围由企业微信侧的「应用可见范围」决定。
		LoginScopes: nil,
		flow:        weComFlow{},
	},
}

// resolvePlatformProvider 取平台模板并把配置页填的端点覆盖/scope 覆盖套上去。
// 非平台档返回 ErrInvalidProvider(调用方 loginProvider 已先分好流,这里只是兜底)。
func resolvePlatformProvider(row loginAppRow) (Provider, error) {
	tpl, ok := platformDefaults[row.provider]
	if !ok {
		return Provider{}, ErrInvalidProvider
	}
	p := tpl
	if u := strings.TrimSpace(row.authorizeURL); u != "" {
		p.AuthorizeURL = u
	}
	if u := strings.TrimSpace(row.tokenURL); u != "" {
		p.TokenURL = u
	}
	if u := strings.TrimSpace(row.userinfoURL); u != "" {
		p.UserURL = u
	}
	p.AgentID = strings.TrimSpace(row.agentID)
	// scope 覆盖只对有 scope 概念的两家生效(企微扫码地址压根不带这参数)。
	if s := strings.TrimSpace(row.scopes); s != "" && row.provider != ProviderWeCom {
		p.LoginScopes = strings.Fields(s)
	}
	return p, nil
}

// validatePlatformApp 保存期校验:覆盖地址形态要对,企微必须有 AgentId。
// 在这一步拦住的意义同上 —— 配错的人该在配置页看到红线,而不是访客点登录后才失败。
func validatePlatformApp(row loginAppRow) error {
	if row.provider == ProviderWeCom && strings.TrimSpace(row.agentID) == "" {
		return ErrAgentIDRequired
	}
	for _, u := range []string{
		strings.TrimSpace(row.authorizeURL),
		strings.TrimSpace(row.tokenURL),
		strings.TrimSpace(row.userinfoURL),
	} {
		if u != "" && !isHTTPEndpoint(u) {
			return ErrPlatformConfigInvalid
		}
	}
	return nil
}

// platformScope 拼 scope 串;空则返回空串(调用方据此省略该参数)。
func platformScope(p Provider) string { return strings.Join(p.LoginScopes, " ") }

// ---- 钉钉 ----

type dingTalkFlow struct{}

func (dingTalkFlow) authorizeURL(p Provider, c platformCreds, state, redirectURI string) (string, error) {
	q := url.Values{}
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("client_id", c.clientID)
	q.Set("scope", platformScope(p))
	// prompt=consent 是新版扫码登录要求的参数:不给它就跳老版免确认页,回调字段名都不一样。
	q.Set("prompt", "consent")
	q.Set("state", state)
	return withQuery(p.AuthorizeURL, q)
}

func (dingTalkFlow) identity(ctx context.Context, client *http.Client, p Provider, c platformCreds, code, _ string) (Identity, error) {
	// 兑换体是 JSON 且字段名小驼峰 —— 用标准 form 编码会被钉钉回 Missingbody。
	tok, err := postJSON(ctx, client, p.TokenURL, map[string]any{
		"clientId":     c.clientID,
		"clientSecret": c.clientSecret,
		"code":         code,
		"grantType":    "authorization_code",
	})
	if err != nil {
		return Identity{}, err
	}
	accessToken := firstStringField(tok, "accessToken")
	if accessToken == "" {
		return Identity{}, ErrExchangeFailed
	}
	// 身份接口只认自定义头;带 Authorization: Bearer 会被回「缺少参数:x-acs-dingtalk-access-token」。
	m, err := getJSONWithHeader(ctx, client, p.UserURL, dingtalkTokenHeader, accessToken)
	if err != nil {
		return Identity{}, err
	}
	return dingTalkIdentity(m), nil
}

// dingTalkIdentity 解析 /contact/users/me 的响应。
// 主键优先 unionId(开发者维度全局稳定),退到 openId(仅本应用稳定)——
// 两者都是平台发的不透明 ID,换手机号/改昵称都不变,可以拿来认人;昵称 nick 不行。
func dingTalkIdentity(m map[string]any) Identity {
	nick := firstStringField(m, "nick")
	return Identity{
		ExternalID: firstStringField(m, "unionId", "openId"),
		Login:      nick,
		Email:      firstStringField(m, "email"),
		Name:       nick,
	}
}

// ---- 飞书 ----

type feishuFlow struct{}

func (feishuFlow) authorizeURL(p Provider, c platformCreds, state, redirectURI string) (string, error) {
	q := url.Values{}
	q.Set("client_id", c.clientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", redirectURI)
	if s := platformScope(p); s != "" {
		q.Set("scope", s)
	}
	q.Set("state", state)
	return withQuery(p.AuthorizeURL, q)
}

func (feishuFlow) identity(ctx context.Context, client *http.Client, p Provider, c platformCreds, code, redirectURI string) (Identity, error) {
	tok, err := postJSON(ctx, client, p.TokenURL, map[string]any{
		"grant_type":    "authorization_code",
		"client_id":     c.clientID,
		"client_secret": c.clientSecret,
		"code":          code,
		"redirect_uri":  redirectURI,
	})
	if err != nil {
		return Identity{}, err
	}
	// 飞书的失败也回 HTTP 200,真答案在顶层 code:非 0 就是没换到 token。
	if !envelopeOK(tok) {
		return Identity{}, ErrExchangeFailed
	}
	accessToken := firstStringField(tok, "access_token")
	if accessToken == "" {
		return Identity{}, ErrExchangeFailed
	}
	raw, err := getJSONWithHeader(ctx, client, p.UserURL, "Authorization", "Bearer "+accessToken)
	if err != nil {
		return Identity{}, err
	}
	// user_info 同样用顶层 code 表成败,把字段塞在 data 信封里。
	if !envelopeOK(raw) {
		return Identity{}, ErrExchangeFailed
	}
	data, _ := raw["data"].(map[string]any)
	if data == nil {
		return Identity{}, ErrExchangeFailed
	}
	return feishuIdentity(data), nil
}

// feishuIdentity 解析 user_info 的 data 段。主键优先级同钉钉:union_id 稳,open_id 次之。
func feishuIdentity(m map[string]any) Identity {
	name := firstStringField(m, "name", "en_name")
	return Identity{
		ExternalID: firstStringField(m, "union_id", "open_id"),
		Login:      name,
		Email:      firstStringField(m, "email", "enterprise_email"),
		Name:       name,
	}
}

// ---- 企业微信 ----

type weComFlow struct{}

func (weComFlow) authorizeURL(p Provider, c platformCreds, state, redirectURI string) (string, error) {
	q := url.Values{}
	// 注意参数名:这里叫 appid(值 = corpid),没有 client_id / response_type / scope 这套。
	q.Set("login_type", "CorpApp")
	q.Set("appid", c.clientID)
	if c.agentID != "" {
		q.Set("agentid", c.agentID)
	}
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	return withQuery(p.AuthorizeURL, q)
}

func (weComFlow) identity(ctx context.Context, client *http.Client, p Provider, c platformCreds, code, _ string) (Identity, error) {
	// 第一步:corpid + corpsecret 换企业级 access_token。令牌全程挂在 query 上,没有请求头。
	tok, err := getJSONWithQuery(ctx, client, p.TokenURL, url.Values{
		"corpid":     {c.clientID},
		"corpsecret": {c.clientSecret},
	})
	if err != nil {
		return Identity{}, err
	}
	if !envelopeOK(tok) {
		return Identity{}, ErrExchangeFailed
	}
	accessToken := firstStringField(tok, "access_token")
	if accessToken == "" {
		return Identity{}, ErrExchangeFailed
	}

	// 第二步:code → 成员身份。非本企业成员只会拿到 external_userid/openid,
	// 那种人本来不在「应用可见范围」里,ExternalID 留空即等价于拒登(调用方转成 identity_unavailable)。
	who, err := getJSONWithQuery(ctx, client, p.UserURL, url.Values{
		"access_token": {accessToken},
		"code":         {code},
	})
	if err != nil {
		return Identity{}, err
	}
	if !envelopeOK(who) {
		return Identity{}, ErrExchangeFailed
	}
	userID := firstStringField(who, "userid")
	if userID == "" {
		return Identity{}, nil
	}

	id := Identity{
		// corpid 参与主键:库里这个 provider 只有一行配置,但管理员换个 corpid 重新保存时,
		// 裸 userid 会把新企业的成员直接接到旧企业同 userid 的账号上 —— 那不是巧合,是接管。
		ExternalID: c.clientID + "/" + userID,
		Login:      userID,
	}
	// 第三步(尽力):查档案拿昵称与邮箱。企微自建应用自 2022-06-20 起默认不再返回邮箱/手机号,
	// 拿不到属正常,绝不为它失败整次登录。
	if prof, perr := getJSONWithQuery(ctx, client, wecomProfileURL(p.UserURL), url.Values{
		"access_token": {accessToken},
		"userid":       {userID},
	}); perr == nil && envelopeOK(prof) {
		if name := firstStringField(prof, "name"); name != "" {
			id.Login = name
			id.Name = name
		}
		id.Email = firstStringField(prof, "email", "biz_mail")
	}
	return id, nil
}

// wecomProfileURL 按配置的身份接口现算档案地址(同 scheme + 同 host,路径换成 user/get)。
// 地址解析不出来时退回官方常量,让这一步顶多拿不到昵称,而不是整次登录失败。
func wecomProfileURL(userURL string) string {
	u, err := url.Parse(strings.TrimSpace(userURL))
	if err != nil || u.Host == "" {
		return "https://qyapi.weixin.qq.com" + wecomProfilePath
	}
	scheme := u.Scheme
	if scheme == "" {
		scheme = "https"
	}
	u.Scheme = scheme
	u.Path = wecomProfilePath
	u.RawQuery, u.Fragment = "", ""
	return u.String()
}

// ---- 报文小工具(三家异形报文共用;错误一律归成人读的 ErrExchangeFailed) ----

// withQuery 把参数拼到端点上。地址本身形态不对 → ErrAppNotConfigured(配置问题,不是兑换失败)。
func withQuery(endpoint string, q url.Values) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return "", ErrAppNotConfigured
	}
	m := u.Query()
	for k, vs := range q {
		for _, v := range vs {
			if v == "" {
				continue
			}
			m.Set(k, v)
		}
	}
	u.RawQuery = m.Encode()
	return u.String(), nil
}

// postJSON POST 一个 JSON 体(钉钉/飞书的兑换接口只认 JSON)。
func postJSON(ctx context.Context, client *http.Client, endpoint string, body map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, ErrExchangeFailed
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, ErrExchangeFailed
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return doJSON(ctx, client, req)
}

// getJSONWithHeader GET 并带一个自定义头(钉钉的令牌头 / 飞书的 Bearer)。
func getJSONWithHeader(ctx context.Context, client *http.Client, endpoint, header, value string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, ErrExchangeFailed
	}
	req.Header.Set(header, value)
	req.Header.Set("Accept", "application/json")
	return doJSON(ctx, client, req)
}

// getJSONWithQuery GET 并把令牌等参数放 query(企业微信这套 cgi 全这样)。
func getJSONWithQuery(ctx context.Context, client *http.Client, endpoint string, q url.Values) (map[string]any, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, ErrExchangeFailed
	}
	m := u.Query()
	for k, vs := range q {
		for _, v := range vs {
			m.Set(k, v)
		}
	}
	u.RawQuery = m.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, ErrExchangeFailed
	}
	req.Header.Set("Accept", "application/json")
	return doJSON(ctx, client, req)
}

// doJSON 发请求并解 JSON map。非 2xx、超限、非 JSON 全部归一为 ErrExchangeFailed:
// 上游原文可能含 corpid / 令牌片段,一律不回吐、不写日志。
func doJSON(ctx context.Context, client *http.Client, req *http.Request) (map[string]any, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, ErrExchangeFailed
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, ErrExchangeFailed
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return nil, ErrExchangeFailed
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, ErrExchangeFailed
	}
	return m, nil
}

// envelopeOK 判平台信封里的业务码是否表示成功:没有 code/errcode 字段就当成功
// (钉钉那两个接口压根不带业务码);有则必须为 0。
func envelopeOK(m map[string]any) bool {
	for _, key := range []string{"code", "errcode"} {
		v, ok := m[key]
		if !ok {
			continue
		}
		switch n := v.(type) {
		case float64:
			if n != 0 {
				return false
			}
		case string:
			if i, err := strconv.Atoi(strings.TrimSpace(n)); err != nil || i != 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}
