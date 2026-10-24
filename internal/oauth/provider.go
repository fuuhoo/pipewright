// Package oauth 是「多 provider OAuth 凭据接入」的领域层。
//
// 运营者一次性配置各 git 平台(gitee|github|gitlab|gitea|custom)的 OAuth 应用(client_id +
// client_secret〔加密〕+ base_url + git_shape〔自建用〕),用户在页面点「连接」→ 跳 git 平台授权 →
// 平台回跳带 code → 后端用 client_id/client_secret 换 access_token → 拉登录名 → 把 token
// 经 vault.Create 存成 git_token 凭据。之后克隆/构建直接复用该凭据(BasicAuth token)。
//
// 安全铁律:client_secret 经 vault secretbox(master key)加密后仅以密文入库;access_token /
// client_secret 绝不出现在错误体/日志/响应。state CSRF 防伪造授权码注入。
package oauth

import (
	"errors"
	"fmt"
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
	// ProviderGitea 表示 gitea.com(托管版 Gitea)。自建 Gitea 走 custom + 形态 gitea。
	ProviderGitea = "gitea"
	// ProviderCustom 表示自建实例(据 base_url + git_shape 推导端点)。
	ProviderCustom = "custom"
	// ProviderOIDC 是通用 OIDC 身份源。**只属于登录流**:仓库凭据那条流只认上面五家
	// (见 normalizeProvider 与 normalizeLoginProvider)。 OIDC 档唯一能参与 git 令牌的地方,
	// 是管理员显式声明「这个 IdP 背后同时是一家 GitLab/Gitea」(git_shape)后的凭据同步 ——
	// 声明只改变「这次登录顺手落不落凭据」,不会让它进仓库凭据那张表。
	ProviderOIDC = "oidc"
	// 下面三家同样是「只属于登录流」的身份源,取值与 internal/notify 的渠道名保持一致
	// (wecom / dingtalk / feishu),让同一套词汇在通知与登录两处不产生第二个拼法。
	// 它们都给不了 git token,且报文形态各家异形 —— 见 platform.go。
	// ProviderDingTalk 表示钉钉扫码登录。
	ProviderDingTalk = "dingtalk"
	// ProviderFeishu 表示飞书登录。
	ProviderFeishu = "feishu"
	// ProviderWeCom 表示企业微信扫码登录。
	ProviderWeCom = "wecom"
)

// git 平台形态。两处用得上,含义刻意不同一档:
//   - provider=custom:选自建实例按哪家平台的地址形态推导端点(空 = GitLab 形态,
//     与这个档位诞生以来的行为一致,存量配置不用改一个字);
//   - provider=oidc:**声明**这个身份源背后同时是一家 git 平台。空 = 没声明,
//     「登录即同步凭据」不许打开 —— 平台不能猜一个通用 IdP 的令牌能不能克隆仓库。
//
// 形态决定三件事:端点路径、申请的仓库读 scope、以及同步出来的凭据按哪家的规矩用。
const (
	GitShapeGitLab = "gitlab"
	GitShapeGitea  = "gitea"
)

// 领域错误。错误体永不含 client_secret / access_token 明文。
var (
	// ErrInvalidProvider 表示 provider 枚举非法(非 gitee|github|gitlab|gitea|custom)。
	ErrInvalidProvider = errors.New("oauth: invalid provider")
	// ErrBaseURLRequired 表示 custom provider 缺 base_url(无从推导端点)。
	ErrBaseURLRequired = errors.New("oauth: base url required for custom provider")
	// ErrInvalidGitShape 表示 git 平台形态填了认识以外的值。保存期就拒:形态错了
	// 不是「少一个功能」而是「整条授权地址都指向一个不存在的路径」,点了登录只会被跳回来说不明原因。
	ErrInvalidGitShape = errors.New("oauth: invalid git platform shape")
)

// gitShapeSpec 是一家 git 平台的地址与权限形态(自建实例按它推导,预置档按它拼装)。
type gitShapeSpec struct {
	// 三个端点相对实例根地址的路径。
	authorizePath string
	tokenPath     string
	userPath      string
	// 仓库凭据流的 scope 与「只登录」的 scope。Gitea 1.23 起才真按 scope 收窄,
	// 更早的版本压根忽略这个参数(授权即给全量),所以照发也不会失败。
	credScopes  []string
	loginScopes []string
}

// gitShapes 是已知形态注册表。key 即库里存的 git_shape 值。
var gitShapes = map[string]gitShapeSpec{
	GitShapeGitLab: {
		authorizePath: "/oauth/authorize",
		tokenPath:     "/oauth/token",
		userPath:      "/api/v4/user",
		credScopes:    []string{"read_repository", "read_user"},
		loginScopes:   []string{"read_user"},
	},
	GitShapeGitea: {
		// Gitea 用的是 GitHub 风格的那套路径,与 GitLab 的 /oauth/* 完全不同 ——
		// 这正是「自建 Gitea 按 GitLab 形态推导必然授权页 404」的地方。
		authorizePath: "/login/oauth/authorize",
		tokenPath:     "/login/oauth/access_token",
		userPath:      "/api/v1/user",
		credScopes:    []string{"read:repository", "read:user"},
		loginScopes:   []string{"read:user"},
	},
}

// isGitShape 判断一个 git_shape 值是否是已知形态(空串不算:它是「未声明/未选」)。
func isGitShape(s string) bool {
	_, ok := gitShapes[strings.TrimSpace(s)]
	return ok
}

// normalizeGitShape 清洗并校验形态取值。空串合法(语义见上面两个档位各自的说明)。
func normalizeGitShape(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "", GitShapeGitLab, GitShapeGitea:
		return s, nil
	default:
		return "", fmt.Errorf("%w: %s", ErrInvalidGitShape, s)
	}
}

// shapeProviderAt 按形态拼出一家 git 平台的端点与 scope。
// base 是实例根地址(不带尾斜杠);预置档与自建档共用它,免得 gitea.com 与自建 Gitea 两套写法漂移。
func shapeProviderAt(name, base, shape string) Provider {
	spec := gitShapes[shape]
	return Provider{
		Name:          name,
		AuthorizeURL:  base + spec.authorizePath,
		TokenURL:      base + spec.tokenPath,
		UserURL:       base + spec.userPath,
		DefaultScopes: append([]string(nil), spec.credScopes...),
		LoginScopes:   append([]string(nil), spec.loginScopes...),
		// GitLab 的登录名字段是 username,Gitea 也是 username(/api/v1/user 没有 login)。
		parseIdentity: parseIdentityFields("username"),
	}
}

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
	// Name 是 provider 枚举值(gitee|github|gitlab|gitea|custom)。
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
	// AgentID 只有企业微信用得上(自建应用的 AgentId:扫码地址与可见范围判定都要它);
	// 其余身份源恒为空。
	AgentID string
	// flow 非 nil 表示这个身份源不走标准 OAuth 报文 —— 钉钉/飞书/企业微信各自的参数名、
	// 请求体编码、令牌头、响应信封、兑换步数都不是一套。git 四家与 OIDC 恒为 nil,
	// 继续走 buildAuthorizeURL / requestToken / fetchIdentity 那条标准路径。
	flow platformFlow
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
	// GitLab 与 Gitea 的预置档直接从形态表拼出:自建实例填同一个根地址得到的端点与 scope
	// 与官方站完全一致,加一家不用改两处。
	ProviderGitLab: shapeProviderAt(ProviderGitLab, "https://gitlab.com", GitShapeGitLab),
	ProviderGitea:  shapeProviderAt(ProviderGitea, "https://gitea.com", GitShapeGitea),
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

// normalizeProvider 校验并归一 **git** provider 枚举(空串非法)。
// 仓库凭据流(oauth_apps)只认这五家 —— 只有它们能换出可用的 git 令牌。
func normalizeProvider(p string) (string, error) {
	switch strings.TrimSpace(p) {
	case ProviderGitee:
		return ProviderGitee, nil
	case ProviderGitHub:
		return ProviderGitHub, nil
	case ProviderGitLab:
		return ProviderGitLab, nil
	case ProviderGitea:
		return ProviderGitea, nil
	case ProviderCustom:
		return ProviderCustom, nil
	default:
		return "", ErrInvalidProvider
	}
}

// normalizeLoginProvider 校验并归一「登录用」身份源枚举:git 五家 + OIDC + 钉钉/飞书/企业微信。
// 两条流刻意用不同枚举:把 oidc 塞进 oauth_apps 会得到一个「能配但永远换不出 git token」
// 的死条目,而登录侧需要的是「任意能证明身份的提供方」。
func normalizeLoginProvider(p string) (string, error) {
	switch strings.TrimSpace(p) {
	case ProviderOIDC, ProviderDingTalk, ProviderFeishu, ProviderWeCom:
		return strings.TrimSpace(p), nil
	}
	return normalizeProvider(p)
}

// isPlatformProvider 判断该身份源是否走 platform.go 那套异形报文(钉钉/飞书/企业微信)。
// 三条流在这里分叉:标准 OAuth 那两条(git 五家、OIDC)继续共用 oauth.go 的兑换实现。
func isPlatformProvider(name string) bool {
	switch strings.TrimSpace(name) {
	case ProviderDingTalk, ProviderFeishu, ProviderWeCom:
		return true
	default:
		return false
	}
}

// issuesGitToken 判断该身份源**档位上**能否换出可用于 git 仓库的令牌:只有五家 git 平台能。
// OIDC 是例外,它还看该应用有没有声明 git_shape —— 判这条请用 supportsGitCredential。
// 凭据同步的开关只能在这些档打开:钉钉/飞书/企微压根没有 git 端点,OIDC 没声明时
// 平台无从知道它的令牌能不能克隆仓库,拿去用只会换来一个说不清的 401。
func issuesGitToken(name string) bool {
	_, err := normalizeProvider(name)
	return err == nil
}

// resolveProvider 据 provider 名 + 配置的 baseURL/gitShape 返回可用的 Provider 端点模板。
//   - gitee/github/gitlab/gitea:返回内置预设(忽略 baseURL 与 shape)。
//   - custom:据 baseURL 与形态(GitLab / Gitea)推导端点。缺 baseURL → ErrBaseURLRequired。
func resolveProvider(name, baseURL, shape string) (Provider, error) {
	n, err := normalizeProvider(name)
	if err != nil {
		return Provider{}, err
	}
	if n == ProviderCustom {
		return customProvider(baseURL, shape)
	}
	return presets[n], nil
}

// customProvider 据自建实例的 baseURL 与平台形态推导 OAuth 端点。
// shape 为空按 GitLab 形态处理:那是这个档位从第一天起的推导方式,存量配置不用重填。
// 登录名解析器两种形态共用(GitLab 与 Gitea 的用户 API 都回 username,主键都叫 id)。
func customProvider(baseURL, shape string) (Provider, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return Provider{}, ErrBaseURLRequired
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return Provider{}, ErrBaseURLRequired
	}
	sh, err := normalizeGitShape(shape)
	if err != nil {
		return Provider{}, err
	}
	if sh == "" {
		sh = GitShapeGitLab
	}
	return shapeProviderAt(ProviderCustom, base, sh), nil
}
