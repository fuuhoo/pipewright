// login.go 是「用 git 平台账号登录本平台」的那半 OAuth 领域。
//
// 与 oauth.go(取仓库凭据)的关键区别:
//   - 配置读 oauth_login_apps 而不是 oauth_apps —— 一个 OAuth 应用通常只能登记一个回调地址,
//     两种用途各配一份,互不牵制;
//   - scope 走 Provider.LoginScopes(只申请身份 read_user 一类):登录本身绝不需要仓库读权限。
//     唯一的例外是 sync_credentials 开关 —— 管理员明确要「登录即同步凭据」时,这次登录改按
//     DefaultScopes 申请(见 loginScopeFor),因为拿不到仓库读权限的令牌换回来也没用;
//   - 换来的 access_token 默认只在函数内活着:不落库、不进 vault、不回吐。
//     同样只有开了凭据同步才走 ExchangeLogin 那条出口,由调用方(identity)立即加密落库。
package oauth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fuuhoo/pipewright/internal/store"
	"github.com/fuuhoo/pipewright/internal/vault"
)

// ErrIdentityUnavailable 表示 provider 没能给出可判定的身份(缺稳定 ID)。
// 人读错误,绝不含 token;登录流据此拒绝,绝不退化成「按登录名认人」(那是接管面)。
var ErrIdentityUnavailable = errors.New("oauth: provider returned no stable user id")

// ErrInvalidDomain 表示邮箱域名白名单里有一项不是合法域名。
// 保存期就报错而不是清洗掉:白名单静默失效等于没白名单,而配错的人并不会知道自己失算了。
var ErrInvalidDomain = errors.New("oauth: invalid allowed domain")

// ErrCredentialSyncUnsupported 表示给一个给不了 git 令牌的身份源(钉钉 / 飞书 / 企业微信)
// 打开了「登录即同步凭据」。必须保存期就拒:静默忽略会让管理员以为开关生效了,
// 然后一直等不到凭据 —— 而页面上一切看起来都正常。
var ErrCredentialSyncUnsupported = errors.New("oauth: credential sync requires a git provider")

// ErrCredentialSyncNeedsGitPlatform 表示 OIDC 身份源开了凭据同步,却没声明它背后是哪家
// git 平台(留空 git_shape)。这两件事必须一起说清楚:平台无法自己判断一个通用 IdP 的
// 令牌能不能克隆仓库,而「声明了形态」才知道该往 scope 里追加哪几家各自的仓库读权限。
// 与上面那条分开给:开错身份源的人要的是「这条路没有」,漏填声明的人要的是「再填一格就好」。
var ErrCredentialSyncNeedsGitPlatform = errors.New("oauth: oidc credential sync requires a declared git platform")

// LoginAppConfig 是「登录用」身份源的对外视图:client_secret 仅掩码,绝无明文。
// 端点/scope/主键声明那几项只对非 git 身份源有意义:oidc 用全套,
// 钉钉/飞书用「端点覆盖 + scope 覆盖」,企业微信用 AgentID(其余平台一律为空)。
type LoginAppConfig struct {
	Provider       string
	ClientID       string
	BaseURL        string
	AllowedDomains []string
	Enabled        bool
	MaskedSecret   string
	Configured     bool
	UpdatedAt      *time.Time
	DiscoveryURL   string
	AuthorizeURL   string
	TokenURL       string
	UserinfoURL    string
	IDClaim        string
	Scopes         string
	AgentID        string
	// GitShape 有两种含义,取决于 provider:
	//   - custom(自建实例):按哪家 git 平台的地址形态推导端点(gitlab|gitea;空 = gitlab,存量语义)。
	//   - oidc:**声明**这个身份源背后同时是一家 git 平台。空 = 没声明,凭据同步不许打开。
	// 其余档位一律为空(它们的端点与 scope 本就由平台定死,没有可声明的东西)。
	GitShape string
	// SyncCredentials = 登录后把这次拿到的 git 令牌同步成该账号的个人凭据(仅能换出 git
	// 令牌的身份源可开:git 五家,或声明了 git_shape 的 OIDC)。
	// 开着它,登录申请的 scope 也随之从「只认身份」升到「能读仓库」(见 Provider.DefaultScopes)。
	SyncCredentials bool
}

// SaveLoginAppInput 是 upsert 登录应用配置的入参。
// ClientSecret 只写:nil/空串 = 保留既有 secret;非空 = 轮换。
// AllowedDomains 为 nil 时保留既有白名单(与 secret 同语义,避免前端漏传即清空)。
// 端点/scope/主键声明/AgentID/git 形态/凭据同步开关是**整份覆盖**语义:端点那组只对非 git 身份源生效
// (git 档位落空值),而凭据同步反过来 —— 只有换得出 git 令牌的身份源能开,开不了就报错而不是静默落 false。
type SaveLoginAppInput struct {
	Provider        string
	ClientID        string
	BaseURL         string
	ClientSecret    *string
	AllowedDomains  *[]string
	Enabled         bool
	DiscoveryURL    string
	AuthorizeURL    string
	TokenURL        string
	UserinfoURL     string
	IDClaim         string
	Scopes          string
	AgentID         string
	GitShape        string
	SyncCredentials bool
}

// loginAppRow 是 oauth_login_apps 的一行内存镜像(secret 为密文)。
type loginAppRow struct {
	provider        string
	clientID        string
	sealed          []byte
	baseURL         string
	allowedDomains  []string
	enabled         bool
	updatedAt       string
	discoveryURL    string
	authorizeURL    string
	tokenURL        string
	userinfoURL     string
	idClaim         string
	scopes          string
	agentID         string
	gitShape        string
	syncCredentials bool
}

// loginAppColumns 是读一行的列序(与 scanLoginApp 严格对应;两处 SELECT 共用,防漂移)。
const loginAppColumns = `provider, client_id, client_secret_ciphertext, base_url, allowed_domains,
 enabled, updated_at, discovery_url, authorize_url, token_url, userinfo_url, id_claim, scopes, agent_id,
 git_shape, sync_credentials`

// scanLoginApp 把一行结果扫成 loginAppRow。
func scanLoginApp(scan func(dest ...any) error) (loginAppRow, error) {
	var (
		row        loginAppRow
		domainsRaw string
		enabledInt int
		syncInt    int
	)
	err := scan(&row.provider, &row.clientID, &row.sealed, &row.baseURL, &domainsRaw,
		&enabledInt, &row.updatedAt, &row.discoveryURL, &row.authorizeURL,
		&row.tokenURL, &row.userinfoURL, &row.idClaim, &row.scopes, &row.agentID,
		&row.gitShape, &syncInt)
	if err != nil {
		return loginAppRow{}, err
	}
	row.enabled = enabledInt != 0
	row.syncCredentials = syncInt != 0
	row.allowedDomains = splitDomains(domainsRaw)
	return row, nil
}

// LoginService 是登录流的领域接口(由同一个 service 实现,经 Service 门面暴露)。
type LoginService interface {
	// ListLoginApps 返回所有已配置的登录应用视图(secret 仅掩码)。
	ListLoginApps(ctx context.Context) ([]LoginAppConfig, error)
	// LoginApp 读单个登录应用视图。无行 → 返回零值视图(Configured=false),无错误:
	// 「没配过」是常态而非异常,登录页要据此决定出不出现入口。
	LoginApp(ctx context.Context, provider string) (*LoginAppConfig, error)
	// SaveLoginApp 校验并 upsert 登录应用配置(secret 只写,语义同 SaveApp)。
	SaveLoginApp(ctx context.Context, in SaveLoginAppInput) (*LoginAppConfig, error)
	// LoginAuthorizeURL 用登录应用自己的 client_id 与身份 scope 构造授权 URL。
	// 未配置/未启用 → ErrAppNotConfigured。
	LoginAuthorizeURL(ctx context.Context, provider, state, redirectURI string) (string, error)
	// ExchangeIdentity 用 code 换 token 并取回身份信息。
	// 与 Exchange 的分野:这里不返回 access_token(登录用不上),
	// 但要求 provider 给出稳定 ID —— 缺 ID 即 ErrIdentityUnavailable,拒绝登录。
	ExchangeIdentity(ctx context.Context, provider, code, redirectURI string) (*Identity, error)
	// ExchangeLogin 同 ExchangeIdentity,但额外带回 git 令牌包(见 Grant)。
	// grant 为 nil 表示这次登录不该产生凭据:该登录应用没开凭据同步,
	// 或身份源压根给不出 git 令牌(OIDC / 钉钉 / 飞书 / 企业微信)。
	ExchangeLogin(ctx context.Context, provider, code, redirectURI string) (*Identity, *Grant, error)
	// RefreshGrant 用之前存下的 refresh_token 向该登录应用续一张 git 令牌(RFC 6749 §6)。
	// 非 git 身份源 / 应用被删 → ErrRefreshUnsupported;平台拒绝(授权已被撤销)→ ErrExchangeFailed。
	RefreshGrant(ctx context.Context, provider, refreshToken string) (*Grant, error)
}

// supportsGitCredentialFor 判断这一档 + 这一份声明换出来的令牌能不能当 git 凭据用。
// git 五家天然能;OIDC 只在管理员显式声明了 git_shape 之后才算能 —— 声明是**运营者的断言**
// (「这个 IdP 其实是一家 GitLab/Gitea,它的令牌直接能克隆仓库」),平台不去猜。
// 猜的代价是一条永远克隆不动、却每轮都去撞令牌端点的凭据。
func supportsGitCredentialFor(provider, gitShape string) bool {
	if issuesGitToken(provider) {
		return true
	}
	return provider == ProviderOIDC && isGitShape(gitShape)
}

// supportsGitCredential 是行上的版本(读库里已保存的声明)。
func supportsGitCredential(row loginAppRow) bool {
	return supportsGitCredentialFor(row.provider, row.gitShape)
}

// loginScopeFor 决定这次登录向 provider 申请哪档 scope。
// 默认是最小身份 scope;只有管理员明确开了「登录即同步凭据」才升到能读仓库那档 ——
// 多要的权限必须换来东西,否则只是把用户的仓库读权限白要一遍。
func loginScopeFor(row loginAppRow, p Provider) string {
	if row.syncCredentials && supportsGitCredential(row) {
		return p.scope()
	}
	return p.loginScope()
}

// NewBindKey 生成一次性的高熵 bind key(匿名登录流用它做双提交 cookie 的值:
// state 记录它,回调比对 cookie)。与会话 token 同量级随机,不复用任何既有凭据。
func NewBindKey() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("oauth: generate bind key: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func (s *service) ListLoginApps(ctx context.Context) ([]LoginAppConfig, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+loginAppColumns+` FROM oauth_login_apps ORDER BY provider`)
	if err != nil {
		return nil, fmt.Errorf("oauth: list login apps: %w", err)
	}
	defer func() { _ = rows.Close() }()

	apps := make([]LoginAppConfig, 0)
	for rows.Next() {
		row, err := scanLoginApp(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("oauth: scan login app: %w", err)
		}
		apps = append(apps, toLoginAppConfig(row))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("oauth: iterate login apps: %w", err)
	}
	return apps, nil
}

func (s *service) LoginApp(ctx context.Context, provider string) (*LoginAppConfig, error) {
	row, err := s.loadLoginApp(ctx, provider)
	if err != nil {
		return nil, err
	}
	cfg := toLoginAppConfig(*row)
	return &cfg, nil
}

func (s *service) SaveLoginApp(ctx context.Context, in SaveLoginAppInput) (*LoginAppConfig, error) {
	provider, err := normalizeLoginProvider(in.Provider)
	if err != nil {
		return nil, err
	}
	clientID := strings.TrimSpace(in.ClientID)
	if clientID == "" {
		return nil, ErrClientIDRequired
	}
	baseURL := strings.TrimSpace(in.BaseURL)
	gitShape, sherr := normalizeGitShape(in.GitShape)
	if sherr != nil {
		return nil, sherr
	}
	if provider == ProviderCustom {
		if _, derr := customProvider(baseURL, gitShape); derr != nil {
			return nil, derr
		}
	}
	// 非 git 身份源才有的这组字段:git 平台一律清空(否则配置页会残留一组永远不生效的端点),
	// provider=oidc 则先把端点解析一遍再落库 —— 发现文档填错、地址不是 http(s)、
	// 三个端点凑不齐,都该在「保存」这一刀就说清楚,而不是等访客点登录被跳回来说不明原因。
	// 钉钉/飞书/企业微信同理:覆盖地址形态不对、企微少 AgentId,也都在保存期就报。
	discoveryURL := strings.TrimSpace(in.DiscoveryURL)
	authorizeURL := strings.TrimSpace(in.AuthorizeURL)
	tokenURL := strings.TrimSpace(in.TokenURL)
	userinfoURL := strings.TrimSpace(in.UserinfoURL)
	idClaim := strings.TrimSpace(in.IDClaim)
	scopes := strings.TrimSpace(in.Scopes)
	agentID := strings.TrimSpace(in.AgentID)
	switch {
	case provider == ProviderOIDC:
		probe := loginAppRow{
			discoveryURL: discoveryURL, authorizeURL: authorizeURL,
			tokenURL: tokenURL, userinfoURL: userinfoURL,
		}
		if _, eerr := resolveOidcEndpoints(ctx, s.client, probe); eerr != nil {
			return nil, eerr
		}
		if idClaim == "" {
			idClaim = defaultOidcIDClaim
		}
		if scopes == "" {
			scopes = defaultOidcScopes
		}
		agentID = ""
	case isPlatformProvider(provider):
		// 这三家的地址与主键字段名由平台定死:发现文档、自定义主键声明都用不上,清空别留残渣。
		// scope 留空 = 用平台默认(见 resolvePlatformProvider),不把默认值写进库里,
		// 将来改默认才不用洗数据。企业微信的扫码地址压根没有 scope 参数,一并清空。
		// git 形态声明同样清掉:这三家的令牌永远换不成仓库权限,留着只会让人以为能开同步。
		gitShape = ""
		discoveryURL, idClaim = "", ""
		if provider != ProviderWeCom {
			agentID = ""
		} else {
			scopes = ""
		}
		probe := loginAppRow{
			provider: provider, authorizeURL: authorizeURL, tokenURL: tokenURL,
			userinfoURL: userinfoURL, agentID: agentID,
		}
		if verr := validatePlatformApp(probe); verr != nil {
			return nil, verr
		}
	default:
		discoveryURL, authorizeURL, tokenURL, userinfoURL, idClaim, scopes, agentID = "", "", "", "", "", "", ""
		if provider != ProviderCustom {
			gitShape = ""
		}
	}
	domains, err := normalizeDomains(in.AllowedDomains)
	if err != nil {
		return nil, err
	}
	// 「登录即同步凭据」只对换得出 git 令牌的身份源有意义:git 五家天然能;OIDC 要运营者显式
	// 声明 git_shape(「这个 IdP 其实是一家 GitLab/Gitea」)才算能;钉钉/飞书/企微压根没有 git 端点。
	// 开错了必须在保存期报错而不是静默忽略 —— 静默忽略会让管理员以为开关生效了,
	// 然后一直等不到凭据,而页面上一切看起来都正常。
	if in.SyncCredentials && !supportsGitCredentialFor(provider, gitShape) {
		if provider == ProviderOIDC {
			return nil, fmt.Errorf("%w: %s", ErrCredentialSyncNeedsGitPlatform, provider)
		}
		return nil, fmt.Errorf("%w: %s", ErrCredentialSyncUnsupported, provider)
	}
	syncCredentials := in.SyncCredentials && supportsGitCredentialFor(provider, gitShape)

	existing, err := s.loadLoginApp(ctx, provider)
	if err != nil {
		return nil, err
	}

	// 保留语义:secret 省略/空 → 沿用既有密文;domains 省略(nil)→ 沿用既有白名单。
	sealed := existing.sealed
	if in.ClientSecret != nil && *in.ClientSecret != "" {
		if s.vault == nil {
			return nil, ErrVaultUnconfigured
		}
		newSealed, serr := s.vault.SealSecret([]byte(*in.ClientSecret))
		if serr != nil {
			if errors.Is(serr, vault.ErrVaultUnconfigured) {
				return nil, ErrVaultUnconfigured
			}
			return nil, fmt.Errorf("oauth: seal login client secret: %w", serr)
		}
		sealed = newSealed
	}
	if len(sealed) == 0 {
		return nil, ErrClientSecretRequired
	}
	if in.AllowedDomains == nil {
		domains = existing.allowedDomains
	}

	enabled := 0
	if in.Enabled {
		enabled = 1
	}
	sync := 0
	if syncCredentials {
		sync = 1
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO oauth_login_apps
		   (provider, client_id, client_secret_ciphertext, base_url, allowed_domains, enabled,
		    created_at, updated_at, discovery_url, authorize_url, token_url, userinfo_url, id_claim, scopes, agent_id,
		    git_shape, sync_credentials)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) `+
			store.UpsertSuffix(store.DialectOf(s.db), []string{"provider"},
				[]string{"client_id", "client_secret_ciphertext", "base_url", "allowed_domains", "enabled",
					"updated_at", "discovery_url", "authorize_url", "token_url", "userinfo_url", "id_claim",
					"scopes", "agent_id", "git_shape", "sync_credentials"}),
		provider, clientID, sealed, baseURL, joinDomains(domains), enabled, now, now,
		discoveryURL, authorizeURL, tokenURL, userinfoURL, idClaim, scopes, agentID, gitShape, sync,
	)
	if err != nil {
		return nil, fmt.Errorf("oauth: upsert login app: %w", err)
	}

	cfg := toLoginAppConfig(loginAppRow{
		provider: provider, clientID: clientID, sealed: sealed, baseURL: baseURL,
		allowedDomains: domains, enabled: in.Enabled, updatedAt: now,
		discoveryURL: discoveryURL, authorizeURL: authorizeURL, tokenURL: tokenURL,
		userinfoURL: userinfoURL, idClaim: idClaim, scopes: scopes, agentID: agentID,
		gitShape: gitShape, syncCredentials: syncCredentials,
	})
	return &cfg, nil
}

func (s *service) LoginAuthorizeURL(ctx context.Context, provider, state, redirectURI string) (string, error) {
	row, err := s.loadLoginApp(ctx, provider)
	if err != nil {
		return "", err
	}
	if row.clientID == "" || len(row.sealed) == 0 || !row.enabled {
		return "", ErrAppNotConfigured
	}
	endpoints, err := s.loginProvider(ctx, *row)
	if err != nil {
		return "", err
	}
	if endpoints.flow != nil {
		// 平台自己的授权地址:参数名与标准 RFC 6749 那套不一样(企微用 appid/agentid,
		// 钉钉还要 prompt=consent)。这里刻意不解密 secret —— 跳转地址用不着它。
		return endpoints.flow.authorizeURL(endpoints, platformCreds{
			clientID: row.clientID, agentID: endpoints.AgentID,
		}, state, redirectURI)
	}
	return buildAuthorizeURL(endpoints, row.clientID, loginScopeFor(*row, endpoints), state, redirectURI)
}

func (s *service) ExchangeIdentity(ctx context.Context, provider, code, redirectURI string) (*Identity, error) {
	id, _, err := s.ExchangeLogin(ctx, provider, code, redirectURI)
	return id, err
}

// ExchangeLogin 是登录流的兑换实现:身份是硬要求,git 令牌是「开了凭据同步才有」的副产品。
func (s *service) ExchangeLogin(ctx context.Context, provider, code, redirectURI string) (*Identity, *Grant, error) {
	row, err := s.loadLoginApp(ctx, provider)
	if err != nil {
		return nil, nil, err
	}
	if row.clientID == "" || len(row.sealed) == 0 {
		return nil, nil, ErrAppNotConfigured
	}
	if strings.TrimSpace(code) == "" {
		return nil, nil, ErrExchangeFailed
	}
	endpoints, err := s.loginProvider(ctx, *row)
	if err != nil {
		return nil, nil, err
	}

	secret, err := s.openSecret(row.sealed)
	if err != nil {
		return nil, nil, err
	}

	var (
		id    Identity
		grant *Grant
	)
	if endpoints.flow != nil {
		// 钉钉/飞书/企业微信:换令牌与取身份都在平台自己的链路里走完(形态各家一套,
		// 见 platform.go),access_token 只在 flow 调用栈内活着,同样不落库、不回吐。
		id, err = endpoints.flow.identity(ctx, s.client, endpoints, platformCreds{
			clientID: row.clientID, clientSecret: secret, agentID: endpoints.AgentID,
		}, code, redirectURI)
	} else {
		var g Grant
		g, err = requestToken(ctx, s.client, endpoints, row.clientID, secret, code, redirectURI)
		if err == nil {
			// 解完身份就把访问令牌收进 grant:未开同步时下面直接丢弃,
			// 开了同步则交给调用方立即加密落库 —— 中间没有任何一处会日志或回显它。
			id, err = fetchIdentity(ctx, s.client, endpoints, g.AccessToken)
			if err == nil && row.syncCredentials && supportsGitCredential(*row) {
				grant = &g
			}
		}
	}
	// 明文 secret 用完即弃(防 GC 前残留被 core dump / swap 捕获)。
	secret = ""
	if err != nil {
		return nil, nil, err
	}
	if id.ExternalID == "" {
		return nil, nil, ErrIdentityUnavailable
	}
	id.Provider = row.provider
	return &id, grant, nil
}

// RefreshGrant 续期一条 git 令牌(§6)。落点只有一个:凭据续期扫描器(见 identity.RefreshGrants)。
// 这里刻意不看 enabled —— 管理员关掉登录入口只是「不让新的人从这条路进来」,
// 已经同步出去的凭据该继续被人用,直到它自己过期或被人删掉。
// 但要看 git_shape:没声明成 git 平台的 OIDC 应用没有仓库读语义,续出来的令牌当 git 凭据用是幻觉;
// 管理员关掉声明后,已经落库的那条凭据到期就只能等用户重新登录。
func (s *service) RefreshGrant(ctx context.Context, provider, refreshToken string) (*Grant, error) {
	prov, err := normalizeLoginProvider(provider)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(refreshToken) == "" {
		return nil, ErrRefreshUnsupported
	}
	row, err := s.loadLoginApp(ctx, prov)
	if err != nil {
		return nil, err
	}
	if !supportsGitCredential(*row) {
		return nil, fmt.Errorf("%w: %s", ErrRefreshUnsupported, prov)
	}
	if row.clientID == "" || len(row.sealed) == 0 {
		return nil, ErrAppNotConfigured
	}
	endpoints, err := s.loginProvider(ctx, *row)
	if err != nil {
		return nil, err
	}
	// 异形报文那三家既不是 git 档(上面已拦),也不该走到这里 —— 兜一层免得将来加档位时漏。
	if endpoints.flow != nil {
		return nil, fmt.Errorf("%w: %s", ErrRefreshUnsupported, prov)
	}
	secret, err := s.openSecret(row.sealed)
	if err != nil {
		return nil, err
	}
	g, err := requestRefresh(ctx, s.client, endpoints, row.clientID, secret, refreshToken)
	secret = ""
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// loadLoginApp 读单个登录应用行;无行返回零值行(provider 已归一),无错误。
func (s *service) loadLoginApp(ctx context.Context, provider string) (*loginAppRow, error) {
	prov, err := normalizeLoginProvider(provider)
	if err != nil {
		return nil, err
	}
	row, err := scanLoginApp(s.db.QueryRowContext(ctx,
		`SELECT `+loginAppColumns+` FROM oauth_login_apps WHERE provider = ?`, prov).Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &loginAppRow{provider: prov}, nil
		}
		return nil, fmt.Errorf("oauth: load login app: %w", err)
	}
	row.provider = prov
	return &row, nil
}

// loginProvider 据登录应用行解析出可用的端点模板。
//   - 品牌 git 平台(gitee/github/gitlab/gitea):沿用 presets;custom 按 base_url + git_shape 推导,
//     与「取仓库凭据」那条流同一套地址。
//   - oidc:端点只能问服务器自己(发现文档 + 显式覆盖),所以这里要 ctx 与 http.Client;
//     声明了 git_shape 时才把该家的仓库读 scope 追加进去(见 oidcProvider)。
//   - 钉钉/飞书/企业微信:端点用平台预设(配置页可逐项覆盖),并带上各自的异形链路 flow。
func (s *service) loginProvider(ctx context.Context, row loginAppRow) (Provider, error) {
	switch {
	case row.provider == ProviderOIDC:
		endpoints, err := resolveOidcEndpoints(ctx, s.client, row)
		if err != nil {
			return Provider{}, err
		}
		return oidcProvider(endpoints, row.scopes, row.idClaim, row.gitShape), nil
	case isPlatformProvider(row.provider):
		return resolvePlatformProvider(row)
	default:
		return resolveProvider(row.provider, row.baseURL, row.gitShape)
	}
}

// toLoginAppConfig 组装对外视图:密文只以固定掩码呈现(不解密)。
func toLoginAppConfig(r loginAppRow) LoginAppConfig {
	cfg := LoginAppConfig{
		Provider:        r.provider,
		ClientID:        r.clientID,
		BaseURL:         r.baseURL,
		AllowedDomains:  append([]string(nil), r.allowedDomains...),
		Enabled:         r.enabled,
		Configured:      r.clientID != "" && len(r.sealed) > 0,
		DiscoveryURL:    r.discoveryURL,
		AuthorizeURL:    r.authorizeURL,
		TokenURL:        r.tokenURL,
		UserinfoURL:     r.userinfoURL,
		IDClaim:         r.idClaim,
		Scopes:          r.scopes,
		AgentID:         r.agentID,
		GitShape:        r.gitShape,
		SyncCredentials: r.syncCredentials,
	}
	if len(r.sealed) > 0 {
		cfg.MaskedSecret = "••••••"
	}
	if t, perr := time.Parse(time.RFC3339, r.updatedAt); perr == nil {
		cfg.UpdatedAt = &t
	}
	return cfg
}

// normalizeDomains 清洗白名单:去空白、转小写、去重,并校验每项确是「a.b」形态的域名。
// 校验的意义在于:配置写错(如误填 "gitlab.com/" 或带空格)会让白名单静默失效,
// 而静默失效的白名单等于没白名单 —— 宁可保存期就报错。
func normalizeDomains(in *[]string) ([]string, error) {
	if in == nil {
		return nil, nil
	}
	out := make([]string, 0, len(*in))
	seen := make(map[string]bool, len(*in))
	for _, raw := range *in {
		d := strings.ToLower(strings.TrimSpace(raw))
		if d == "" {
			continue
		}
		if !validDomain(d) {
			return nil, fmt.Errorf("%w: %q", ErrInvalidDomain, d)
		}
		if seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out, nil
}

// validDomain 只接受「标签.标签[.…]」形态(字母数字与中划线),不含协议/路径/端口/空格。
func validDomain(d string) bool {
	if !strings.Contains(d, ".") || strings.HasPrefix(d, ".") || strings.HasSuffix(d, ".") {
		return false
	}
	for _, label := range strings.Split(d, ".") {
		if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, r := range label {
			switch {
			case r >= 'a' && r <= 'z':
			case r >= '0' && r <= '9':
			case r == '-':
			default:
				return false
			}
		}
	}
	return true
}

// splitDomains 解析库里逗号分隔的白名单。
func splitDomains(raw string) []string {
	if raw = strings.TrimSpace(raw); raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// joinDomains 把白名单序列化成库里的逗号串。
func joinDomains(domains []string) string { return strings.Join(domains, ",") }
