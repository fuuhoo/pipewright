// Package oauth 是「多 provider OAuth 凭据接入」的领域层。
//
// 运营者一次性配置各 git 平台(gitee|github|gitlab|custom)的 OAuth 应用(client_id +
// client_secret〔加密〕+ base_url〔自建用〕),用户在页面点「连接」→ 跳 git 平台授权 →
// 平台回跳带 code → 后端用 client_id/client_secret 换 access_token → 拉登录名 → 把 token
// 经 vault.Create 存成 git_token 凭据。之后克隆/构建直接复用该凭据(BasicAuth token)。
//
// 安全铁律:client_secret 经 vault secretbox(master key)加密后仅以密文入库;access_token /
// client_secret 绝不出现在错误体/日志/响应。state CSRF 防伪造授权码注入。
package oauth

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
)

// provider 枚举(DB 存小写字串;路由 path 同名)。
const (
	// ProviderGitee 表示码云 gitee.com。
	ProviderGitee = "gitee"
	// ProviderGitHub 表示 github.com。
	ProviderGitHub = "github"
	// ProviderGitLab 表示 gitlab.com。
	ProviderGitLab = "gitlab"
	// ProviderCustom 表示自建实例(据 base_url 推导 GitLab/Gitea 风格端点)。
	ProviderCustom = "custom"
)

// 领域错误。错误体永不含 client_secret / access_token 明文。
var (
	// ErrInvalidProvider 表示 provider 枚举非法(非 gitee|github|gitlab|custom)。
	ErrInvalidProvider = errors.New("oauth: invalid provider")
	// ErrBaseURLRequired 表示 custom provider 缺 base_url(无从推导端点)。
	ErrBaseURLRequired = errors.New("oauth: base url required for custom provider")
)

// Identity 是从 provider user API 解析出的用户身份。
//   - ExternalID 是 provider 内的稳定主键(数字 id 的字符串形式)。登录账号匹配只用它 ——
//     username 可改、email 可换/可被他人抢注,拿它们认人就是账号接管漏洞。
//   - Login / Email / Name 仅用于展示、用户名建议与域名白名单判定,不参与身份判定。
//   - Provider 由调用方(LoginExchange)填入,便于下游记账。
type Identity struct {
	Provider   string
	ExternalID string
	Login      string
	Email      string
	Name       string
}

// Provider 描述一个 OAuth provider 的端点与解析策略。
// 内置预设为不可变模板;custom 经 resolve(baseURL) 据基址推导端点。
type Provider struct {
	// Name 是 provider 枚举值(gitee|github|gitlab|custom)。
	Name string
	// AuthorizeURL 是授权页地址(用户浏览器跳转目标)。
	AuthorizeURL string
	// TokenURL 是 code→token 兑换地址(后端 server-to-server POST)。
	TokenURL string
	// UserURL 是取当前用户信息的 API 地址(带 access_token)。
	UserURL string
	// EmailsURL 是取邮箱列表的 API 地址;为空表示 user API 已带 email。
	// GitHub 的 /user 在隐私邮箱场景会回 email=null,必须再打一次 /user/emails。
	EmailsURL string
	// DefaultScopes 是「连接仓库凭据」申请的权限(含仓库读)。
	DefaultScopes []string
	// LoginScopes 是「登录」申请的最小权限,只碰身份,不碰仓库内容。
	// 为空则回退 DefaultScopes。
	LoginScopes []string
	// parseIdentity 从 user API 的 JSON map 解析身份(各平台字段名不同)。
	parseIdentity func(m map[string]any) Identity
}

// scope 把 DefaultScopes 以空格连接(OAuth2 标准 scope 串)。
func (p Provider) scope() string { return strings.Join(p.DefaultScopes, " ") }

// loginScope 是登录流程用的 scope 串:拿不到身份就不许登进来。
func (p Provider) loginScope() string {
	if len(p.LoginScopes) == 0 {
		return p.scope()
	}
	return strings.Join(p.LoginScopes, " ")
}

// presets 是内置 provider 预设注册表(map 注册,设计可扩展)。
// custom 不在此(需 base_url 动态推导),经 resolveProvider 单独处理。
var presets = map[string]Provider{
	ProviderGitee: {
		Name:          ProviderGitee,
		AuthorizeURL:  "https://gitee.com/oauth/authorize",
		TokenURL:      "https://gitee.com/oauth/token",
		UserURL:       "https://gitee.com/api/v5/user",
		DefaultScopes: []string{"user_info", "projects"},
		LoginScopes:   []string{"user_info"},
		parseIdentity: parseIdentityFields("login"),
	},
	ProviderGitHub: {
		Name:          ProviderGitHub,
		AuthorizeURL:  "https://github.com/login/oauth/authorize",
		TokenURL:      "https://github.com/login/oauth/access_token",
		UserURL:       "https://api.github.com/user",
		EmailsURL:     "https://api.github.com/user/emails",
		DefaultScopes: []string{"repo", "read:user"},
		LoginScopes:   []string{"read:user", "user:email"},
		parseIdentity: parseIdentityFields("login"),
	},
	ProviderGitLab: {
		Name:          ProviderGitLab,
		AuthorizeURL:  "https://gitlab.com/oauth/authorize",
		TokenURL:      "https://gitlab.com/oauth/token",
		UserURL:       "https://gitlab.com/api/v4/user",
		DefaultScopes: []string{"read_repository", "read_user"},
		LoginScopes:   []string{"read_user"},
		parseIdentity: parseIdentityFields("username"),
	},
}

// parseIdentityFields 返回一个身份解析器:主字段定登录名,external_id 恒取 "id"。
// primary 是各平台的登录名字段名(GitLab/Gitea 系 username,GitHub/Gitee 系 login)。
func parseIdentityFields(primary string) func(map[string]any) Identity {
	return func(m map[string]any) Identity {
		return Identity{
			ExternalID: idField(m),
			Login:      firstStringField(m, primary, "username", "login"),
			Email:      firstStringField(m, "email"),
			Name:       firstStringField(m, "name"),
		}
	}
}

// idField 取 provider 用户主键:JSON 数字(float64)与字符串都归一成字符串。
// 缺 id 时返回空串 —— 调用方据此拒绝,绝不退化成「用登录名当主键」。
func idField(m map[string]any) string {
	switch v := m["id"].(type) {
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case string:
		return strings.TrimSpace(v)
	default:
		return ""
	}
}

// firstStringField 按字段名优先级返回第一个非空字符串值。
func firstStringField(m map[string]any, fields ...string) string {
	for _, f := range fields {
		if v, ok := m[f].(string); ok {
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		}
	}
	return ""
}

// normalizeProvider 校验并归一 provider 枚举(空串非法)。
func normalizeProvider(p string) (string, error) {
	switch strings.TrimSpace(p) {
	case ProviderGitee:
		return ProviderGitee, nil
	case ProviderGitHub:
		return ProviderGitHub, nil
	case ProviderGitLab:
		return ProviderGitLab, nil
	case ProviderCustom:
		return ProviderCustom, nil
	default:
		return "", ErrInvalidProvider
	}
}

// resolveProvider 据 provider 名 + 配置的 baseURL 返回可用的 Provider 端点模板。
//   - gitee/github/gitlab:返回内置预设(忽略 baseURL,除非将来允许覆盖)。
//   - custom:据 baseURL 推导 GitLab/Gitea 风格端点(authorize/token/user)。
//     custom 缺 baseURL → ErrBaseURLRequired。
func resolveProvider(name, baseURL string) (Provider, error) {
	n, err := normalizeProvider(name)
	if err != nil {
		return Provider{}, err
	}
	if n == ProviderCustom {
		return customProvider(baseURL)
	}
	return presets[n], nil
}

// customProvider 据自建实例 baseURL 推导 GitLab/Gitea 风格 OAuth 端点。
// 约定(GitLab 与 Gitea 端点路径一致):
//
//	authorize: {base}/oauth/authorize
//	token    : {base}/oauth/token
//	user     : {base}/api/v4/user   (GitLab;Gitea 为 /api/v1/user,登录名字段均含 username/login)
//
// 解析器主字段取 username,回退 login,故同时兼容 GitLab(username)与 Gitea(login)。
// Gitea 的用户主键同样是 id,external_id 判定不受影响。
func customProvider(baseURL string) (Provider, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return Provider{}, ErrBaseURLRequired
	}
	if _, err := url.Parse(base); err != nil {
		return Provider{}, ErrBaseURLRequired
	}
	return Provider{
		Name:          ProviderCustom,
		AuthorizeURL:  base + "/oauth/authorize",
		TokenURL:      base + "/oauth/token",
		UserURL:       base + "/api/v4/user",
		DefaultScopes: []string{"read_repository", "read_user"},
		LoginScopes:   []string{"read_user"},
		parseIdentity: parseIdentityFields("username"),
	}, nil
}
