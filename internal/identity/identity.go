// Package identity 把「外部 git 身份」落到「本地账号」:映射仓储 + 自动建号策略。
//
// 为什么单独成一个包:这条判定是平台最容易出账号接管事故的地方,值得一个自己的边界,
// 让「谁能在什么条件下拿到哪个账号」集中在一处可读、可测。
//
// 三条铁律(与 Jpom 的 autoCreteUser + 按用户名/邮箱认人相反,那是接管面):
//  1. 认人只认 (provider, external_id) —— provider 侧的稳定数字 ID。
//     username 可以改、email 可以被别人抢注、跨 provider 还可能重名,拿它们认人都不安全。
//     email 只用来过域名白名单,绝不参与账号匹配。
//  2. 自动建号绝不接管既有账号:同名账号已存在 → 直接拒登并指向「口令登录后再绑定」,
//     不建影子账号、不加后缀把人分成两个。
//  3. 建出来的账号权限取最小档(viewer),且口令位填的是「连系统都不知道」的随机哈希 ——
//     这类账号只能经由 OAuth 登录,直到管理员显式给它设口令。
package identity

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/fuuhoo/pipewright/internal/access"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/oauth"
	"github.com/fuuhoo/pipewright/internal/users"
)

// DefaultRole 是自动建号落的角色:0063 播种的四档预置里权限最小的一档(只读)。
// 落库前必用 access.ValidRole 现查 —— 管理员把这档删了就得拒绝建号,
// 绝不能像 users.Create 那样静默回退到 'user'(那一档权限明显更大)。
const DefaultRole = "viewer"

// 领域错误。错误体绝不含 token / client_secret;Reason 码由 HTTP 层映射成人读文案。
var (
	// ErrNotFound 表示映射不存在。
	ErrNotFound = errors.New("identity: binding not found")
	// ErrIdentityIncomplete 表示 provider 没给出可判定的身份(缺稳定 ID)。
	ErrIdentityIncomplete = errors.New("identity: incomplete external identity")
	// ErrLoginDisabled 表示该 provider 的登录应用已停用或未配全(回调途中被关)。
	ErrLoginDisabled = errors.New("identity: oauth login disabled for provider")
	// ErrDomainNotAllowed 表示邮箱域名不在白名单内(或压根没拿到邮箱 —— fail closed)。
	ErrDomainNotAllowed = errors.New("identity: email domain not allowed")
	// ErrNameTaken 表示同名本地账号已存在:不接管、不建影子账号,拒登。
	ErrNameTaken = errors.New("identity: local username already taken")
	// ErrAccountUnavailable 表示映射指向的账号已被禁用或删除。
	ErrAccountUnavailable = errors.New("identity: account not usable")
	// ErrRoleUnavailable 表示默认角色(viewer)已不在角色枚举内。
	ErrRoleUnavailable = errors.New("identity: default role unavailable")
	// ErrAlreadyBound 表示该外部身份已绑到**别的**本地账号。
	ErrAlreadyBound = errors.New("identity: external identity already bound elsewhere")
	// ErrProviderBound 表示该本地账号在这个 provider 上已绑过一条身份。
	ErrProviderBound = errors.New("identity: provider already bound for this account")
)

// Binding 是 user_identities 的一行:一条外部身份 ↔ 本地账号的映射。
type Binding struct {
	ID         string
	UserID     string
	Provider   string
	ExternalID string
	Username   string
	Email      string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Account 是登录判定出来的本地账号(会话据此签发)。
type Account struct {
	UserID   string
	Username string
	Role     string
	Provider string
	// Provisioned 为 true 表示这次登录新建了账号 —— 审计要能分清「首次建号」与「既有登录」。
	Provisioned bool
}

// LoginApps 是本包需要的 oauth 能力子集(注入用,便于测试)。
type LoginApps interface {
	LoginApp(ctx context.Context, provider string) (*oauth.LoginAppConfig, error)
}

// Service 是外部身份映射与自动建号策略。
type Service struct {
	db    *sql.DB
	users *users.Service
	apps  LoginApps
	// provisionMu 串行化自动建号:同一外部身份并发首登时只应产生一个账号。
	// 单二进制部署下进程内互斥足够;库里 (provider, external_id) 唯一约束是最后兜底。
	provisionMu sync.Mutex
}

// NewService 构造 Service。users / apps 由入口装配时注入。
func NewService(db *sql.DB, u *users.Service, apps LoginApps) *Service {
	return &Service{db: db, users: u, apps: apps}
}

// Find 按 (provider, external_id) 取映射;无则 ErrNotFound。
func (s *Service) Find(ctx context.Context, provider, externalID string) (*Binding, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, provider, external_id, username, email, created_at, updated_at
		 FROM user_identities WHERE provider = ? AND external_id = ?`, provider, externalID)
	return scanBinding(row)
}

// ListByUser 列某本地账号的全部绑定(账户页用)。
func (s *Service) ListByUser(ctx context.Context, userID string) ([]*Binding, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, provider, external_id, username, email, created_at, updated_at
		 FROM user_identities WHERE user_id = ? ORDER BY provider`, userID)
	if err != nil {
		return nil, fmt.Errorf("identity: list bindings: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]*Binding, 0)
	for rows.Next() {
		b, err := scanBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Login 是登录判定:已绑定 → 用那个账号;未绑定 → 按白名单自动建号。
// 绝不按 username / email 猜账号(见包注释铁律 1)。
func (s *Service) Login(ctx context.Context, id *oauth.Identity) (*Account, error) {
	if id == nil || strings.TrimSpace(id.ExternalID) == "" {
		return nil, ErrIdentityIncomplete
	}
	app, err := s.apps.LoginApp(ctx, id.Provider)
	if err != nil {
		return nil, err
	}
	// 回调途中管理员可能已关掉这个入口:每请求现查,不信发起时的判定。
	if !app.Enabled || !app.Configured {
		return nil, ErrLoginDisabled
	}

	b, err := s.Find(ctx, id.Provider, id.ExternalID)
	if err == nil {
		return s.accountFromBinding(ctx, b, id)
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	return s.provision(ctx, app, id)
}

// Bind 给**已登录**的本地账号绑一条外部身份。刻意不建号、不改角色。
// 重复绑定同一身份是幂等的(回原绑定);身份属于别人 → ErrAlreadyBound。
func (s *Service) Bind(ctx context.Context, userID string, id *oauth.Identity) (*Binding, error) {
	if id == nil || strings.TrimSpace(id.ExternalID) == "" {
		return nil, ErrIdentityIncomplete
	}
	if strings.TrimSpace(userID) == "" {
		return nil, fmt.Errorf("identity: bind requires user id")
	}
	existing, err := s.Find(ctx, id.Provider, id.ExternalID)
	if err == nil {
		if existing.UserID == userID {
			return existing, nil
		}
		return nil, ErrAlreadyBound
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	bound, err := s.ListByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, b := range bound {
		if b.Provider == id.Provider {
			return nil, ErrProviderBound
		}
	}
	b, err := s.insert(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	// 与自动登录同理:这条账号原本没有本地档案时,把平台给的名字补进去;已有的一律不动。
	_ = s.users.FillProfileIfEmpty(userID, id.Name, id.Email)
	return b, nil
}

// Unbind 解绑:只删属于该账号的那条。0 行命中 → ErrNotFound。
func (s *Service) Unbind(ctx context.Context, userID, bindingID string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM user_identities WHERE id = ? AND user_id = ?`, bindingID, userID)
	if err != nil {
		return fmt.Errorf("identity: unbind: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("identity: rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// accountFromBinding 走「已绑定」这条路:账号必须存在、启用、且角色不是空串。
// role="" 是 0053 之前旧会话的取值,语义是「按管理员」—— 任何新签发的会话都不该带它,
// OAuth 建出来/绑上去的账号若查出空串,一律拒(绝不因脏数据白拿管理员)。
func (s *Service) accountFromBinding(ctx context.Context, b *Binding, id *oauth.Identity) (*Account, error) {
	u, err := s.users.GetByID(b.UserID)
	if err != nil {
		if errors.Is(err, users.ErrNotFound) {
			return nil, ErrAccountUnavailable
		}
		return nil, err
	}
	if !u.Enabled {
		return nil, ErrAccountUnavailable
	}
	role := strings.TrimSpace(u.Role)
	if role == "" || !access.ValidRole(role) {
		return nil, ErrAccountUnavailable
	}
	// 展示字段每次登录刷新一次(平台上改了昵称/邮箱,这边跟着走)。
	// 失败不阻断登录:那只是页面上多显示一个旧名字。
	_, _ = s.db.ExecContext(ctx,
		`UPDATE user_identities SET username = ?, email = ?, updated_at = ? WHERE id = ?`,
		id.Login, id.Email, time.Now().UTC().Format(time.RFC3339), b.ID)
	// 本地档案(users 那两列)反过来:**只补空**。上面那张表是平台快照,每次登录照抄;
	// 这里的显示名是「首次建号写入、之后本地可编辑」的,无脑覆盖会把人自己改的名字冲掉。
	// 需要它的原因是 0069 之前建出来的账号这两列全空,不回填就永远只显示一个用户名。
	// 同样不阻断登录。
	_ = s.users.FillProfileIfEmpty(u.ID, id.Name, id.Email)

	return &Account{
		UserID: u.ID, Username: u.Username, Role: role, Provider: b.Provider,
	}, nil
}

// provision 走「未绑定 → 自动建号」这条路。
func (s *Service) provision(ctx context.Context, app *oauth.LoginAppConfig, id *oauth.Identity) (*Account, error) {
	s.provisionMu.Lock()
	defer s.provisionMu.Unlock()

	// 拿锁后再查一次:并发的另一个首登请求可能刚好建好了同一条映射。
	if b, err := s.Find(ctx, id.Provider, id.ExternalID); err == nil {
		return s.accountFromBinding(ctx, b, id)
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	if err := checkDomain(app.AllowedDomains, id.Email); err != nil {
		return nil, err
	}
	username := suggestUsername(id)
	if _, err := s.users.GetByUsername(username); err == nil {
		// 同名本地账号已存在(多半是管理员先建好的)。不接管它 —— 那是按名字猜人;
		// 也不建 bob-2 这种影子账号 —— 那会把同一个人劈成两个。明确拒登,让「口令登录 + 绑定」这条路走。
		return nil, ErrNameTaken
	} else if !errors.Is(err, users.ErrNotFound) {
		return nil, err
	}
	if !access.ValidRole(DefaultRole) {
		return nil, ErrRoleUnavailable
	}

	// 占位口令:对 32 字节随机数做 argon2id,这个明文从未存在过、也无处可查,
	// 于是这个账号只能经 OAuth 登录,直到管理员给它设口令。users.password_hash 是 NOT NULL,
	// 留空会让建号失败,而放宽约束要重建表 —— 占位哈希是这里代价最小且最安全的做法。
	placeholderPlain, err := randomSecret()
	if err != nil {
		return nil, err
	}
	placeholder, err := auth.HashPassword(placeholderPlain)
	if err != nil {
		return nil, fmt.Errorf("identity: placeholder password: %w", err)
	}
	u, err := s.users.Create(users.CreateInput{
		Username:     username,
		PasswordHash: placeholder,
		Role:         DefaultRole,
		Description:  fmt.Sprintf("OAuth 自动建号(%s:%s)", id.Provider, displayHandle(id)),
		DisplayName:  id.Name,
		Email:        id.Email,
	})
	if err != nil {
		if errors.Is(err, users.ErrConflict) {
			return nil, ErrNameTaken
		}
		return nil, err
	}
	b, err := s.insert(ctx, u.ID, id)
	if err != nil {
		// 账号已建但映射没落库:留着它也没用(没人能登进去),但它占着这个用户名。
		// 不静默吞掉 —— 报错让人来查,比悄悄给出一个「下次登录又建一个」的循环好。
		return nil, fmt.Errorf("identity: 已建账号 %s 但绑定写入失败: %w", u.ID, err)
	}
	return &Account{
		UserID: u.ID, Username: u.Username, Role: u.Role,
		Provider: b.Provider, Provisioned: true,
	}, nil
}

// insert 写一条映射。
func (s *Service) insert(ctx context.Context, userID string, id *oauth.Identity) (*Binding, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	idStr := users.NewID()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO user_identities
		   (id, user_id, provider, external_id, username, email, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		idStr, userID, id.Provider, id.ExternalID, id.Login, id.Email, now, now,
	)
	if err != nil {
		if isUniqueViolation(err) {
			// 与并发请求抢同一身份:让调用方看到「已绑给别人」而不是 SQL 细节。
			return nil, ErrAlreadyBound
		}
		return nil, fmt.Errorf("identity: insert binding: %w", err)
	}
	return &Binding{
		ID: idStr, UserID: userID, Provider: id.Provider, ExternalID: id.ExternalID,
		Username: id.Login, Email: id.Email, CreatedAt: parseOrZero(now), UpdatedAt: parseOrZero(now),
	}, nil
}

// checkDomain 判邮箱域名白名单。
//   - 白名单为空 = 不限域名(登录应用 enabled 就是管理员开的口子,默认角色仍是最小档);
//   - 白名单非空时拿不到邮箱一律拒:身份来自公开 git 平台,不能因为「查不到邮箱」就放行,
//     那是把白名单变成摆设的最短路径。
func checkDomain(allowed []string, email string) error {
	if len(allowed) == 0 {
		return nil
	}
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 {
		return ErrDomainNotAllowed
	}
	domain := strings.ToLower(strings.TrimSpace(email[at+1:]))
	for _, d := range allowed {
		if strings.EqualFold(strings.TrimSpace(d), domain) {
			return nil
		}
	}
	return ErrDomainNotAllowed
}

// suggestUsername 把平台登录名洗成本地用户名:只留 [A-Za-z0-9._-],截到 64 字符。
// 洗空了(平台昵称全是符号/中文)则退成 provider+外部 ID 前缀 —— 稳定的 ID 派生,
// 不拿别人的名字赌运气。退路那一路同样要洗字符:见下。
func suggestUsername(id *oauth.Identity) string {
	name := sanitizeUsernameChars(id.Login)
	if len(name) < 2 {
		// 企业微信的主键形如 corpid/userid,照原文截断会带出 '/',建号当场被 users 的
		// 字符集校验打回,用户只看到一个说不清的登录失败。
		short := sanitizeUsernameChars(id.ExternalID)
		if len(short) > 12 {
			short = short[:12]
		}
		name = id.Provider + "-" + short
	}
	return name
}

// sanitizeUsernameChars 逐字过滤成 users.username 认的字符集(字母数字与 . _ -),
// 截到 64 字符 —— 与 validateUsername 同一套规则,免得建号时才被撞回来。
func sanitizeUsernameChars(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b.WriteRune(r)
		}
		if b.Len() >= 64 {
			break
		}
	}
	return b.String()
}

// displayHandle 给建号备注用:优先登录名,没有就用外部 ID(备注只是给人看的线索)。
func displayHandle(id *oauth.Identity) string {
	if s := strings.TrimSpace(id.Login); s != "" {
		return s
	}
	return id.ExternalID
}

// randomSecret 生成 32 字节随机十六进制串,用作占位口令的明文(不落任何地方,立即丢弃)。
// 拿不到随机数就报错拒建号:占位口令的全部意义在于「无人知道明文」,
// 退化成一个写在源码里的常量等于给这类账号开了条后门(知道常量的人能用它登录)。
func randomSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("identity: placeholder password entropy: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func scanBinding(sc scanner) (*Binding, error) {
	var (
		b                      Binding
		createdStr, updatedStr string
		username, email        string
	)
	if err := sc.Scan(&b.ID, &b.UserID, &b.Provider, &b.ExternalID, &username, &email,
		&createdStr, &updatedStr); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("identity: scan binding: %w", err)
	}
	b.Username, b.Email = username, email
	b.CreatedAt, b.UpdatedAt = parseOrZero(createdStr), parseOrZero(updatedStr)
	return &b, nil
}

// scanner 抽象 *sql.Row / *sql.Rows。
type scanner interface {
	Scan(dest ...any) error
}

func parseOrZero(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// isUniqueViolation 跨方言判唯一约束冲突(与 users 包同口径)。
func isUniqueViolation(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") || strings.Contains(msg, "Duplicate entry")
}
