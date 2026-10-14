// login.go 是「用 git 平台账号登录本平台」的那半 OAuth 领域。
//
// 与 oauth.go(取仓库凭据)的关键区别:
//   - 配置读 oauth_login_apps 而不是 oauth_apps —— 一个 OAuth 应用通常只能登记一个回调地址,
//     两种用途各配一份,互不牵制;
//   - scope 走 Provider.LoginScopes(只申请身份 read_user 一类),绝不为登录申请 repo;
//   - 换来的 access_token 只在函数内活着:不落库、不进 vault、不回吐。登录只需要身份。
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

// LoginAppConfig 是「登录用」OAuth 应用的对外视图:client_secret 仅掩码,绝无明文。
type LoginAppConfig struct {
	Provider       string
	ClientID       string
	BaseURL        string
	AllowedDomains []string
	Enabled        bool
	MaskedSecret   string
	Configured     bool
	UpdatedAt      *time.Time
}

// SaveLoginAppInput 是 upsert 登录应用配置的入参。
// ClientSecret 只写:nil/空串 = 保留既有 secret;非空 = 轮换。
// AllowedDomains 为 nil 时保留既有白名单(与 secret 同语义,避免前端漏传即清空)。
type SaveLoginAppInput struct {
	Provider       string
	ClientID       string
	BaseURL        string
	ClientSecret   *string
	AllowedDomains *[]string
	Enabled        bool
}

// loginAppRow 是 oauth_login_apps 的一行内存镜像(secret 为密文)。
type loginAppRow struct {
	provider       string
	clientID       string
	sealed         []byte
	baseURL        string
	allowedDomains []string
	enabled        bool
	updatedAt      string
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
		`SELECT provider, client_id, client_secret_ciphertext, base_url, allowed_domains, enabled, updated_at
		 FROM oauth_login_apps ORDER BY provider`)
	if err != nil {
		return nil, fmt.Errorf("oauth: list login apps: %w", err)
	}
	defer func() { _ = rows.Close() }()

	apps := make([]LoginAppConfig, 0)
	for rows.Next() {
		var (
			provider   string
			clientID   string
			sealed     []byte
			baseURL    string
			domainsRaw string
			enabledInt int
			updatedStr string
		)
		if err := rows.Scan(&provider, &clientID, &sealed, &baseURL,
			&domainsRaw, &enabledInt, &updatedStr); err != nil {
			return nil, fmt.Errorf("oauth: scan login app: %w", err)
		}
		apps = append(apps, toLoginAppConfig(loginAppRow{
			provider:       provider,
			clientID:       clientID,
			sealed:         sealed,
			baseURL:        baseURL,
			allowedDomains: splitDomains(domainsRaw),
			enabled:        enabledInt != 0,
			updatedAt:      updatedStr,
		}))
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
	provider, err := normalizeProvider(in.Provider)
	if err != nil {
		return nil, err
	}
	clientID := strings.TrimSpace(in.ClientID)
	if clientID == "" {
		return nil, ErrClientIDRequired
	}
	baseURL := strings.TrimSpace(in.BaseURL)
	if provider == ProviderCustom {
		if _, derr := customProvider(baseURL); derr != nil {
			return nil, derr
		}
	}
	domains, err := normalizeDomains(in.AllowedDomains)
	if err != nil {
		return nil, err
	}

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
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO oauth_login_apps
		   (provider, client_id, client_secret_ciphertext, base_url, allowed_domains, enabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?) `+
			store.UpsertSuffix(store.DialectOf(s.db), []string{"provider"},
				[]string{"client_id", "client_secret_ciphertext", "base_url", "allowed_domains", "enabled", "updated_at"}),
		provider, clientID, sealed, baseURL, joinDomains(domains), enabled, now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("oauth: upsert login app: %w", err)
	}

	cfg := toLoginAppConfig(loginAppRow{
		provider: provider, clientID: clientID, sealed: sealed, baseURL: baseURL,
		allowedDomains: domains, enabled: in.Enabled, updatedAt: now,
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
	endpoints, err := resolveProvider(row.provider, row.baseURL)
	if err != nil {
		return "", err
	}
	return buildAuthorizeURL(endpoints, row.clientID, endpoints.loginScope(), state, redirectURI)
}

func (s *service) ExchangeIdentity(ctx context.Context, provider, code, redirectURI string) (*Identity, error) {
	row, err := s.loadLoginApp(ctx, provider)
	if err != nil {
		return nil, err
	}
	if row.clientID == "" || len(row.sealed) == 0 {
		return nil, ErrAppNotConfigured
	}
	if strings.TrimSpace(code) == "" {
		return nil, ErrExchangeFailed
	}
	endpoints, err := resolveProvider(row.provider, row.baseURL)
	if err != nil {
		return nil, err
	}

	secret, err := s.openSecret(row.sealed)
	if err != nil {
		return nil, err
	}
	accessToken, err := requestToken(ctx, s.client, endpoints, row.clientID, secret, code, redirectURI)
	secret = ""
	if err != nil {
		return nil, err
	}
	// accessToken 到此为止:解完身份就丢,既不返回也不落 vault。
	id, err := fetchIdentity(ctx, s.client, endpoints, accessToken)
	accessToken = ""
	if err != nil {
		return nil, err
	}
	if id.ExternalID == "" {
		return nil, ErrIdentityUnavailable
	}
	id.Provider = row.provider
	return &id, nil
}

// loadLoginApp 读单个登录应用行;无行返回零值行(provider 已归一),无错误。
func (s *service) loadLoginApp(ctx context.Context, provider string) (*loginAppRow, error) {
	prov, err := normalizeProvider(provider)
	if err != nil {
		return nil, err
	}
	var (
		domainsRaw string
		enabledInt int
		row        loginAppRow
	)
	err = s.db.QueryRowContext(ctx,
		`SELECT client_id, client_secret_ciphertext, base_url, allowed_domains, enabled, updated_at
		 FROM oauth_login_apps WHERE provider = ?`, prov,
	).Scan(&row.clientID, &row.sealed, &row.baseURL, &domainsRaw, &enabledInt, &row.updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &loginAppRow{provider: prov}, nil
		}
		return nil, fmt.Errorf("oauth: load login app: %w", err)
	}
	row.provider = prov
	row.enabled = enabledInt != 0
	row.allowedDomains = splitDomains(domainsRaw)
	return &row, nil
}

// toLoginAppConfig 组装对外视图:密文只以固定掩码呈现(不解密)。
func toLoginAppConfig(r loginAppRow) LoginAppConfig {
	cfg := LoginAppConfig{
		Provider:       r.provider,
		ClientID:       r.clientID,
		BaseURL:        r.baseURL,
		AllowedDomains: append([]string(nil), r.allowedDomains...),
		Enabled:        r.enabled,
		Configured:     r.clientID != "" && len(r.sealed) > 0,
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
