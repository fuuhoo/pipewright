// oidc.go 是登录流的「通用 OIDC 身份源」这一支(Keycloak / Authentik / 企业自建 IDaaS)。
//
// 与 git 四家的关键区别:git 平台的端点写在 presets 常量里(或据 base_url 推导),
// 而 OIDC 服务器的端点只能问它自己 —— 先读发现文档,运营者手填的端点再逐项覆盖。
//
// 本期只走 **userinfo 路线**:换到 access_token 后 GET userinfo_endpoint 取主键声明,
// 不验 id_token 的签名/issuer/audience。这条限制是有意的:验签要引 JWKS 缓存与一套
// JWT 校验,而拿不到 userinfo 端点的纯 id_token 服务器本就不支持(保存期即报错)。
// 设置页把这条写进文案,运营者知道自己换来的是什么。
//
// 信任边界同 git 平台的 base_url:发现文档地址由**管理员**填写,平台本就照配置的地址
// 外拨(git 仓库、k8s API、构建镜像仓库都一样),不在这里加"同源"猜测式限制。
package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	// defaultOidcScopes 是 OIDC 登录申请的最小 scope:openid 是协议要求,
	// profile/email 只为拿到可展示的登录名与域名白名单判定用的邮箱。
	defaultOidcScopes = "openid profile email"
	// defaultOidcIDClaim 是 OIDC 规范里的稳定用户主键声明。
	defaultOidcIDClaim = "sub"
)

// ErrOidcConfigInvalid 表示 OIDC 登录应用的端点解析不齐(既没发现文档也没填全三个端点,
// 或地址形态不对)。保存期与发起前都会判一次:配错的人在配置页就该看到,而不是点了登录
// 才被跳回一句"服务器内部错误"。
var ErrOidcConfigInvalid = errors.New("oauth: oidc endpoints not resolved")

// oidcEndpoints 是一套解析完成的 OIDC 端点。
type oidcEndpoints struct {
	authorizeURL string
	tokenURL     string
	userinfoURL  string
}

// fetchOidcDiscovery GET 发现文档并取三个端点。
// 只要文档可解且三个端点齐全就用;任何一环缺失即 ErrOidcConfigInvalid(不带上游原文)。
func fetchOidcDiscovery(ctx context.Context, client *http.Client, discoveryURL string) (oidcEndpoints, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return oidcEndpoints{}, ErrOidcConfigInvalid
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return oidcEndpoints{}, ErrOidcConfigInvalid
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return oidcEndpoints{}, ErrOidcConfigInvalid
	}
	var doc struct {
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
		UserinfoEndpoint      string `json:"userinfo_endpoint"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return oidcEndpoints{}, ErrOidcConfigInvalid
	}
	out := oidcEndpoints{
		authorizeURL: strings.TrimSpace(doc.AuthorizationEndpoint),
		tokenURL:     strings.TrimSpace(doc.TokenEndpoint),
		userinfoURL:  strings.TrimSpace(doc.UserinfoEndpoint),
	}
	for _, u := range []string{out.authorizeURL, out.tokenURL, out.userinfoURL} {
		if !isHTTPEndpoint(u) {
			return oidcEndpoints{}, ErrOidcConfigInvalid
		}
	}
	return out, nil
}

// resolveOidcEndpoints 合成一套可用端点:显式填写优先,缺哪一项就回落到发现文档。
// 三者全空且没 discovery → ErrOidcConfigInvalid。
func resolveOidcEndpoints(ctx context.Context, client *http.Client, row loginAppRow) (oidcEndpoints, error) {
	explicit := oidcEndpoints{
		authorizeURL: strings.TrimSpace(row.authorizeURL),
		tokenURL:     strings.TrimSpace(row.tokenURL),
		userinfoURL:  strings.TrimSpace(row.userinfoURL),
	}
	for _, u := range []string{explicit.authorizeURL, explicit.tokenURL, explicit.userinfoURL} {
		if u != "" && !isHTTPEndpoint(u) {
			return oidcEndpoints{}, ErrOidcConfigInvalid
		}
	}
	discovery := strings.TrimSpace(row.discoveryURL)
	if discovery == "" {
		if explicit.authorizeURL == "" || explicit.tokenURL == "" || explicit.userinfoURL == "" {
			return oidcEndpoints{}, ErrOidcConfigInvalid
		}
		return explicit, nil
	}
	if !isHTTPEndpoint(discovery) {
		return oidcEndpoints{}, ErrOidcConfigInvalid
	}
	found, err := fetchOidcDiscovery(ctx, client, discovery)
	if err != nil {
		return oidcEndpoints{}, err
	}
	merged := explicit
	if merged.authorizeURL == "" {
		merged.authorizeURL = found.authorizeURL
	}
	if merged.tokenURL == "" {
		merged.tokenURL = found.tokenURL
	}
	if merged.userinfoURL == "" {
		merged.userinfoURL = found.userinfoURL
	}
	return merged, nil
}

// oidcProvider 把解析好的端点 + 配置的 scope/主键声明装进 Provider。
// 登录流只读 AuthorizeURL/TokenURL/UserURL 与 loginScope()/parseIdentity,
// EmailsURL 留空:OIDC 永远不参与「取仓库凭据」那条流(它自己的令牌不是给 git API 用的)。
//
// gitShape 是管理员在 OIDC 卡上的声明:「这个身份源背后同时是一家 GitLab / Gitea」。
// 只有声明了才把该形态的仓库读 scope 追加进 DefaultScopes —— 于是「登录即同步凭据」
// 打开时申请到的令牌真能克隆仓库,没打开时(loginScope)依旧只申请身份那几项。
// 没声明的 OIDC(Keycloak / Authentik / 企业 IDaaS)DefaultScopes 与 LoginScopes 相同:
// 多要一层没有对应东西的权限,只会让授权页多一句让人困惑的同意条款。
func oidcProvider(endpoints oidcEndpoints, scopes, idClaim, gitShape string) Provider {
	scopeList := strings.Fields(scopes)
	if len(scopeList) == 0 {
		scopeList = strings.Fields(defaultOidcScopes)
	}
	claim := strings.TrimSpace(idClaim)
	if claim == "" {
		claim = defaultOidcIDClaim
	}
	credScopes := scopeList
	if isGitShape(gitShape) {
		credScopes = mergeScopes(scopeList, gitShapes[gitShape].credScopes)
	}
	return Provider{
		Name:          ProviderOIDC,
		AuthorizeURL:  endpoints.authorizeURL,
		TokenURL:      endpoints.tokenURL,
		UserURL:       endpoints.userinfoURL,
		DefaultScopes: credScopes,
		LoginScopes:   scopeList,
		parseIdentity: func(m map[string]any) Identity {
			return oidcIdentity(m, claim)
		},
	}
}

// mergeScopes 把要追加的 scope 并进来,已申请过的不重复(GitLab 的 OIDC 应用本就常带 read_user,
// 重复一次串在 scope 里会被有些实现当成非法请求)。
func mergeScopes(base, extra []string) []string {
	out := append([]string(nil), base...)
	seen := make(map[string]bool, len(base))
	for _, s := range base {
		seen[s] = true
	}
	for _, s := range extra {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// oidcIdentity 从 userinfo 的 JSON 解出身份。
// 主键恒取 idClaim(默认 sub)且**不回退到登录名**:声明缺失就交给调用方的
// ErrIdentityUnavailable —— 按 preferred_username 认人正是 Jpom 那套的接管面。
//
// Login 只是展示名与建号建议,不参与认人,所以各家叫法都得认:OIDC 标准叫
// preferred_username(Keycloak/Authentik),GitLab 的 claims_supported 里根本没有这一项,
// 登录名放在 nickname。少了 nickname 就会一路回退到 email,把「邮箱去掉 @」当成账号。
func oidcIdentity(m map[string]any, idClaim string) Identity {
	email := firstStringField(m, "email")
	return Identity{
		ExternalID: claimString(m, idClaim),
		Login:      firstStringField(m, "preferred_username", "nickname", "email", "name"),
		Email:      email,
		Name:       firstStringField(m, "name", "nickname"),
	}
}

// claimString 把任意声明值归一成字符串:数字型 sub 也认,布尔/嵌套一律当没有。
func claimString(m map[string]any, key string) string {
	switch v := m[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatInt(int64(v), 10)
	default:
		return ""
	}
}

// isHTTPEndpoint 判断是否可外拨的 http(s) 地址:拒 file/自定义 scheme、拒带凭据的 URL、拒无 host。
func isHTTPEndpoint(raw string) bool {
	if strings.TrimSpace(raw) == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return false
	}
	return u.Scheme == "https" || u.Scheme == "http"
}
