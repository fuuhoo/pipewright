// Package apitoken 提供平台自持的 API 令牌(Bearer)—— 给 MCP 客户端与脚本这类机器调用方用。
//
// 与会话(auth.SessionStore)的分工:会话是浏览器侧的短期身份,带 CSRF 与滑动过期;
// 令牌是长期、可单独撤销的机器凭据。校验结果由 httpapi 铸成同一个会话形态喂给中间件与
// 业务 handler —— 权限判定(角色管功能 / 分组管数据)因此对机器调用完全等价。
//
// 安全纪律:
//   - 库里只存 sha256 摘要,明文只在 Create 的那一次响应里出现,之后任何人都读不回来。
//   - 校验失败的原因一律抹平成 ErrInvalid:枚举「令牌不存在」与「用户被禁用」需要的是
//     一个有效令牌,而这套系统里回得去的信息只有「这把不行」。
//   - 撤销 = 删行。状态不落列,校验路径就不存在「忘了判 revoked」这种错法。
package apitoken

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// tokenPrefix 让令牌一眼可辨(便于在日志/工单里认出这是平台令牌,而不是别的密钥)。
const tokenPrefix = "pwt_"

// tokenBytes 随机字节数(32B → 64 hex chars)。
const tokenBytes = 32

// MaxNameLen 令牌名称长度上限(与迁移里的 VARCHAR(128) 对齐;按字符计,中文算一个)。
const MaxNameLen = 128

// lastUsedWriteInterval 是 last_used_at 的最小写库间隔,口径与 auth 会话的滑动刷新一致:
// 机器调用往往每几秒一次,每条请求都 UPDATE 会在单写 SQLite 上撞锁竞争。
const lastUsedWriteInterval = 60 * time.Second

// 领域错误。错误体不含令牌明文与摘要。
var (
	// ErrInvalid 表示令牌不可用(不存在 / 已撤销 / 归属用户不存在或被禁用)。
	ErrInvalid  = errors.New("apitoken: invalid token")
	ErrNotFound = errors.New("apitoken: not found")
	// ErrValidation 标记可直接回传给调用方的入参问题(名称为空或过长、用户不存在)。
	ErrValidation = errors.New("apitoken: invalid input")
	// ErrUserNotFound 单列一条:创建时选错了归属人,UI 要说「该用户不存在」而不是泛泛的 400。
	ErrUserNotFound = errors.New("apitoken: user not found")
)

// Token 是一条令牌的对外视图(绝不含明文与摘要)。
type Token struct {
	ID        string
	Name      string
	UserID    string
	OwnerName string // 归属用户登录名(JOIN users)
	CreatedBy string
	CreatedAt time.Time
	// LastUsedAt 为零值表示从未使用过。
	LastUsedAt time.Time
}

// Principal 是一次令牌校验的成功结果:令牌背后那个人的身份(每次从 users 现查,
// 所以改角色、禁用账号都在下一次调用即刻生效,不存在「令牌还带着旧权限」的窗口)。
type Principal struct {
	TokenID  string
	UserID   string
	Username string
	Role     string
}

// CreateInput 是签发入参。
type CreateInput struct {
	Name      string
	UserID    string
	CreatedBy string
}

// CreatedToken 是签发结果:Plaintext 只在这里出现一次。
type CreatedToken struct {
	Token     Token
	Plaintext string
}

// Service 操作 api_tokens 表。
type Service struct {
	db *sql.DB
}

// NewService 构造 Service;db 须已由调用方保证非 nil(与 users/vault 同一约定)。
func NewService(db *sql.DB) *Service { return &Service{db: db} }

// Create 签发一枚新令牌,返回明文(仅此一次)与元数据视图。
func (s *Service) Create(ctx context.Context, in CreateInput) (*CreatedToken, error) {
	name := strings.Join(strings.Fields(in.Name), " ")
	if name == "" {
		return nil, fmt.Errorf("%w:令牌名称不能为空", ErrValidation)
	}
	if len([]rune(name)) > MaxNameLen {
		return nil, fmt.Errorf("%w:令牌名称过长(最多 %d 字)", ErrValidation, MaxNameLen)
	}
	userID := strings.TrimSpace(in.UserID)
	if userID == "" {
		return nil, fmt.Errorf("%w:缺少归属用户", ErrValidation)
	}
	// 归属人必须真实存在。禁用中的账号当场拒签:否则这把令牌发出去永远是 ErrInvalid,
	// 还不如在创建那一刻告诉操作人「这个人被禁用了」。
	var enabled int
	err := s.db.QueryRowContext(ctx, `SELECT enabled FROM users WHERE id = ?`, userID).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w:归属用户不存在", ErrUserNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("apitoken: read owner: %w", err)
	}
	if enabled == 0 {
		return nil, fmt.Errorf("%w:该用户已被禁用,无法为其签发令牌", ErrValidation)
	}

	plain, err := newPlaintext()
	if err != nil {
		return nil, err
	}
	hash := digest(plain)
	now := time.Now().UTC()
	id := uuid.NewString()

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO api_tokens (id, name, token_hash, user_id, created_by, last_used_at, created_at)
		 VALUES (?, ?, ?, ?, ?, '', ?)`,
		id, name, hash, userID, strings.TrimSpace(in.CreatedBy), now.Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("apitoken: insert token: %w", err)
	}
	t, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return &CreatedToken{Token: *t, Plaintext: plain}, nil
}

// Get 按 id 取一条令牌视图(供签发后回读同一份元数据,不重复拼 JOIN)。
func (s *Service) Get(ctx context.Context, id string) (*Token, error) {
	row := s.db.QueryRowContext(ctx, tokenSelect+` WHERE t.id = ?`, id)
	t, err := scanToken(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("apitoken: get token: %w", err)
	}
	return t, nil
}

// List 返回全部令牌(按创建时间倒序)。管理页只看得到这些:明文与摘要都不在返回体里。
func (s *Service) List(ctx context.Context) ([]Token, error) {
	rows, err := s.db.QueryContext(ctx, tokenSelect+` ORDER BY t.created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("apitoken: list tokens: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Token
	for rows.Next() {
		t, serr := scanToken(rows)
		if serr != nil {
			return nil, fmt.Errorf("apitoken: scan token: %w", serr)
		}
		out = append(out, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("apitoken: iterate tokens: %w", err)
	}
	return out, nil
}

// Revoke 删除一条令牌。未知 id → ErrNotFound。
func (s *Service) Revoke(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("apitoken: revoke: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Verify 按明文取回归属人身份。任何不可用的原因(不含摘要、不存在、归属用户被删或被禁用)
// 一律回 ErrInvalid。成功时按节流刷新 last_used_at —— 这一步失败不影响本次校验结果。
func (s *Service) Verify(ctx context.Context, plaintext string) (*Principal, error) {
	plain := strings.TrimSpace(plaintext)
	if plain == "" {
		return nil, ErrInvalid
	}
	var p Principal
	err := s.db.QueryRowContext(ctx,
		`SELECT t.id, t.user_id, u.username, u.role
		   FROM api_tokens t JOIN users u ON u.id = t.user_id
		  WHERE t.token_hash = ? AND u.enabled = 1`,
		digest(plain),
	).Scan(&p.TokenID, &p.UserID, &p.Username, &p.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("apitoken: verify: %w", err)
	}
	// 条件 UPDATE 自带节流:只有超过间隔的行才写,因此不必先读一遍 last_used_at。
	cutoff := time.Now().UTC().Add(-lastUsedWriteInterval).Format(time.RFC3339)
	if _, uerr := s.db.ExecContext(ctx,
		`UPDATE api_tokens SET last_used_at = ? WHERE id = ? AND (last_used_at = '' OR last_used_at < ?)`,
		time.Now().UTC().Format(time.RFC3339), p.TokenID, cutoff); uerr != nil {
		// 「最近用过」只是运维线索,拿不到它不该把一次合法调用判成失败。
		_ = uerr
	}
	return &p, nil
}

// tokenSelect 是列表/取单条共用的投影(明文与摘要刻意不选)。
const tokenSelect = `SELECT t.id, t.name, t.user_id, u.username, t.created_by,
		                    t.last_used_at, t.created_at
		           FROM api_tokens t JOIN users u ON u.id = t.user_id`

// rowScanner 让 *sql.Row 与 *sql.Rows 共用同一段投影解析(两类型的 Scan 签名一致)。
type rowScanner interface {
	Scan(dest ...any) error
}

func scanToken(row rowScanner) (*Token, error) {
	var t Token
	var lastUsed, created string
	if err := row.Scan(&t.ID, &t.Name, &t.UserID, &t.OwnerName, &t.CreatedBy, &lastUsed, &created); err != nil {
		return nil, err
	}
	return fillTimes(&t, lastUsed, created)
}

func fillTimes(t *Token, lastUsed, created string) (*Token, error) {
	if lastUsed != "" {
		ts, err := time.Parse(time.RFC3339, lastUsed)
		if err != nil {
			return nil, fmt.Errorf("apitoken: parse last_used_at: %w", err)
		}
		t.LastUsedAt = ts
	}
	ts, err := time.Parse(time.RFC3339, created)
	if err != nil {
		return nil, fmt.Errorf("apitoken: parse created_at: %w", err)
	}
	t.CreatedAt = ts
	return t, nil
}

// newPlaintext 生成随机令牌明文(prefix + 64 hex)。
func newPlaintext() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("apitoken: generate token: %w", err)
	}
	return tokenPrefix + hex.EncodeToString(b), nil
}

// digest 返回明文令牌的 sha256 hex。
func digest(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}
