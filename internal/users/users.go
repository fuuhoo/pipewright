// Package users 提供普通用户 + 管理员个人行的领域层。
//
// 业务含义(v6.2 §3.5):
//   - users 表存两类用户:role='user' 的普通用户 + role='admin' 的"管理员个人行"
//     (与 admin_user.id=1 业务同一实体的 users 视角)。
//   - personal 凭据 owner_id 指向 users.id(UUID v4);管理员自己的 personal 凭据
//     owner_id 指向该管理员在 users 中对应记录。
//
// 当前实现:
//   - BootstrapAdminRow:首次启动在 users 中同步 admin_user 的管理员行
//   - SyncAdminPasswordChange:admin_user 改口令后同步到 users.role='admin' 行
//   - Create / SetPassword / SetEnabled:管理员建号与重置口令(/api/admin/users/*)
//   - GetByUsername / GetByID / List:查询
//
// 后续 story:邀请注册(邀请 token + URL)。
package users

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
)

// BootstrapAdminRegularUserID 是 admin 在 users 表中对应的固定 UUID。
//
// 业务上与 admin_user.id=1 是同一实体;凡涉及 personal 凭据 owner_id、审计 actor、
// 创建者引用等所有场景,必须使用本常量——不要在调用点硬编码。
//
// 永不变更(invariant 测试见 consts_test.go);若改动,会破坏所有已签发 admin personal
// 凭据的归属,导致解密 key 与 owner 错位。
const BootstrapAdminRegularUserID = "00000000-0000-0000-0000-000000000001"

// Role 枚举。
const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// 领域错误(错误体不含敏感数据:不打印 hash / 密码 / 内部栈)。
var (
	ErrNotFound = errors.New("users: not found")
	ErrConflict = errors.New("users: conflict")
	// ErrValidation 标记「入参不合规则」这类可直接回传给用户的错误;HTTP 层据此
	// 回 400 而不是 500(SQL 细节一律不外泄)。
	ErrValidation = errors.New("users: invalid input")
)

// User 是对外可见的用户视图(不含 password_hash;敏感字段不暴露)。
type User struct {
	ID          string
	Username    string
	Role        string
	Enabled     bool
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	LastLoginAt *time.Time
}

// Service 是 users 领域对外接口。
type Service struct {
	db *sql.DB
}

// NewService 构造 Service;db 为 nil 时调用任何方法返回 panic(由调用方保证注入)。
func NewService(db *sql.DB) *Service { return &Service{db: db} }

// BootstrapAdminRow 在 users 表中插入或更新 admin 的"个人行"。
//   - 首次启动时由 auth.Bootstrap 调用,username/password_hash 与 admin_user 同步
//   - id 永远为 BootstrapAdminRegularUserID
//   - ON CONFLICT(id) DO UPDATE 保证幂等(同库多次启动不会创建多个 admin 行)
//   - role='admin' + enabled=1 是硬约束,不接受入参覆盖
//
// 错误:db 为 nil → panic;SQL 执行失败 → fmt.Errorf 包装。
func (s *Service) BootstrapAdminRow(username, passwordHash string) error {
	if username == "" || passwordHash == "" {
		return fmt.Errorf("users: bootstrap admin requires username and password_hash")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`
		INSERT INTO users (id, username, password_hash, role, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			username       = excluded.username,
			password_hash  = excluded.password_hash,
			role           = ?,
			updated_at     = excluded.updated_at
	`, BootstrapAdminRegularUserID, username, passwordHash, RoleAdmin, now, now, RoleAdmin)
	if err != nil {
		return fmt.Errorf("users: bootstrap admin: %w", err)
	}
	return nil
}

// SyncAdminPasswordChange 在 admin_user.password_hash 变更后,把新 hash 同步到
// users.role='admin' 对应行(username 不变)。
//
// 触发点:auth.ChangePassword 成功后调用。
// 影响行数:正常 1;若 users 中无 role='admin' 行(理论不可能,Bootstrap 必先跑),
// 返回 ErrNotFound 提示调用方先 Bootstrap。
func (s *Service) SyncAdminPasswordChange(username, newPasswordHash string) error {
	if username == "" || newPasswordHash == "" {
		return fmt.Errorf("users: sync password requires username and hash")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.Exec(
		`UPDATE users SET password_hash = ?, updated_at = ? WHERE username = ? AND role = ?`,
		newPasswordHash, now, username, RoleAdmin,
	)
	if err != nil {
		return fmt.Errorf("users: sync admin password: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("users: rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetByUsername 取用户的 hash + 元数据(供 auth.Login 复用校验)。
//
// 返回的 PasswordHash 仅供内部 auth.Login 校验使用;绝不入响应体、不入日志。
type InternalUser struct {
	User
	PasswordHash string
}

func (s *Service) GetByUsername(username string) (*InternalUser, error) {
	row := s.db.QueryRow(
		`SELECT id, username, password_hash, role, enabled, description, created_at, updated_at, last_login_at
		 FROM users WHERE username = ?`, username)
	return scanInternal(row)
}

// GetByID 取用户 view(无 hash)。
func (s *Service) GetByID(id string) (*User, error) {
	row := s.db.QueryRow(
		`SELECT id, username, role, enabled, description, created_at, updated_at, last_login_at
		 FROM users WHERE id = ?`, id)
	return scanView(row)
}

// ListFilter 用户列表过滤/分页(v6.2 阶段 9:admin 用户管理页)。
type ListFilter struct {
	Role            string // "admin" | "user" | ""(不限)
	IncludeDisabled bool   // false=仅 enabled=1(默认)
	Limit           int    // <=0 → 默认 100;上限 500
	Offset          int    // >=0
}

// DefaultListLimit / MaxListLimit 列表分页边界。
const (
	DefaultListLimit = 100
	MaxListLimit     = 500
)

// List 按 filter 返回用户视图(不含 password_hash),按 username 字典序稳定排序。
// 分页边界与 audit 包一致:Limit 归一化到 [1, MaxListLimit]。
func (s *Service) List(f ListFilter) ([]*User, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultListLimit
	}
	if limit > MaxListLimit {
		limit = MaxListLimit
	}
	conds := []string{"1=1"}
	args := []any{}
	if f.Role != "" {
		conds = append(conds, "role = ?")
		args = append(args, f.Role)
	}
	if !f.IncludeDisabled {
		conds = append(conds, "enabled = 1")
	}
	args = append(args, limit, f.Offset)

	rows, err := s.db.Query(
		`SELECT id, username, role, enabled, description, created_at, updated_at, last_login_at
		 FROM users WHERE `+strings.Join(conds, " AND ")+`
		 ORDER BY username ASC
		 LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("users: list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]*User, 0, limit)
	for rows.Next() {
		u, err := scanView(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// NewID 生成新用户 ID(UUID v4);供后续 story(邀请注册)使用。
func NewID() string { return uuid.NewString() }

// MinPasswordLen 是口令最小长度,与 auth.ErrWeakPassword 的规则一致。
const MinPasswordLen = 8

// CreateInput 是建号入参。PasswordHash 由调用方用 auth.HashPassword 生成——
// 本包不 import internal/auth(会被 auth→users 的同步调用反向成环)。
type CreateInput struct {
	Username     string
	PasswordHash string
	Role         string // RoleAdmin | RoleUser;空 → RoleUser
	Description  string
}

// Create 建普通用户或管理员账号(管理员建号,v6.2 阶段 9)。
//   - username 归一化后需 2~64 字符,仅允许字母/数字/._-;唯一冲突 → ErrConflict
//   - role 非 admin 时一律按 user 处理
//   - 返回视图(不含 hash)
//
// 注意:role='admin' 的既有同步行由 BootstrapAdminRow 管 id 固定值;此处新建的
// 管理员是额外管理员账号,id 为新生成的 UUID。
func (s *Service) Create(in CreateInput) (*User, error) {
	username := strings.TrimSpace(in.Username)
	if err := validateUsername(username); err != nil {
		return nil, err
	}
	if in.PasswordHash == "" {
		return nil, fmt.Errorf("%w:建号需要口令哈希", ErrValidation)
	}
	role := strings.TrimSpace(in.Role)
	if role != RoleAdmin {
		role = RoleUser
	}
	now := time.Now().UTC().Format(time.RFC3339)
	id := NewID()
	_, err := s.db.Exec(
		`INSERT INTO users (id, username, password_hash, role, enabled, description, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 1, ?, ?, ?)`,
		id, username, in.PasswordHash, role, strings.TrimSpace(in.Description), now, now,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w:用户名 %s 已存在", ErrConflict, username)
		}
		return nil, fmt.Errorf("users: create: %w", err)
	}
	return s.GetByID(id)
}

// SetPassword 按用户 ID 重置口令(hash 由调用方生成)。
// 影响 0 行 → ErrNotFound。刻意不改 username/role,重置口令不提权。
func (s *Service) SetPassword(id, passwordHash string) error {
	if strings.TrimSpace(passwordHash) == "" {
		return fmt.Errorf("users: set password requires hash")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	return s.execOneRow("set password",
		`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
		passwordHash, now, id,
	)
}

// SetEnabled 启用/禁用账号。禁用即时生效:auth.Login 只在 enabled=1 时放行。
func (s *Service) SetEnabled(id string, enabled bool) error {
	n := 0
	if enabled {
		n = 1
	}
	now := time.Now().UTC().Format(time.RFC3339)
	return s.execOneRow("set enabled",
		`UPDATE users SET enabled = ?, updated_at = ? WHERE id = ?`,
		n, now, id,
	)
}

// SetDescription 改备注(用途、归属团队等),不影响登录。
func (s *Service) SetDescription(id, description string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	return s.execOneRow("set description",
		`UPDATE users SET description = ?, updated_at = ? WHERE id = ?`,
		strings.TrimSpace(description), now, id,
	)
}

// execOneRow 执行必须命中恰好一行的 UPDATE;0 行 → ErrNotFound。op 仅用于错误文案。
func (s *Service) execOneRow(op, query string, args ...any) error {
	res, err := s.db.Exec(query, args...)
	if err != nil {
		return fmt.Errorf("users: %s: %w", op, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("users: rows affected: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// validateUsername 校验并归一化后的用户名字符集。
//
// 不用邮箱作用户名:登录路径按 users.username 精确匹配,窄字符集可避免
// 「大小写/空白差异导致同一个人两个账号」。
func validateUsername(u string) error {
	if len(u) < 2 || len(u) > 64 {
		return fmt.Errorf("%w:用户名需 2~64 字符", ErrValidation)
	}
	for _, r := range u {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '.' || r == '_' || r == '-'
		if !ok {
			return fmt.Errorf("%w:用户名只允许字母、数字与 . _ -", ErrValidation)
		}
	}
	return nil
}

// isUniqueViolation 跨方言判定唯一约束冲突:MySQL 1062,SQLite 文案含 UNIQUE constraint。
func isUniqueViolation(err error) bool {
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number == 1062
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") || strings.Contains(msg, "Duplicate entry")
}

// scanner 抽象 *sql.Row / *sql.Rows。
type scanner interface {
	Scan(dest ...any) error
}

func scanInternal(sc scanner) (*InternalUser, error) {
	var u InternalUser
	var lastLogin sql.NullString
	var createdStr, updatedStr string
	if err := sc.Scan(
		&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.Enabled, &u.Description,
		&createdStr, &updatedStr, &lastLogin,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("users: scan: %w", err)
	}
	created, err := time.Parse(time.RFC3339, createdStr)
	if err != nil {
		return nil, fmt.Errorf("users: parse created_at: %w", err)
	}
	updated, err := time.Parse(time.RFC3339, updatedStr)
	if err != nil {
		return nil, fmt.Errorf("users: parse updated_at: %w", err)
	}
	u.CreatedAt = created
	u.UpdatedAt = updated
	if lastLogin.Valid && lastLogin.String != "" {
		t, err := time.Parse(time.RFC3339, lastLogin.String)
		if err != nil {
			return nil, fmt.Errorf("users: parse last_login_at: %w", err)
		}
		u.LastLoginAt = &t
	}
	return &u, nil
}

// scanView 把「8 列视图 SELECT(id, username, role, enabled, description, created_at,
// updated_at, last_login_at,不含 password_hash)」扫描为 User。
//
// 不复用 scanInternal(后者要求 password_hash 在 SELECT 里),否则 GetByID 的
// 8 列查询会因 destination 数不匹配直接报错(scan: expected 9 destination, not 8)。
func scanView(sc scanner) (*User, error) {
	var (
		u            User
		enabled      int
		createdStr   string
		updatedStr   string
		lastLoginStr sql.NullString
	)
	if err := sc.Scan(
		&u.ID, &u.Username, &u.Role, &enabled, &u.Description,
		&createdStr, &updatedStr, &lastLoginStr,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("users: scan view: %w", err)
	}
	u.Enabled = enabled != 0
	created, err := time.Parse(time.RFC3339, createdStr)
	if err != nil {
		return nil, fmt.Errorf("users: parse created_at: %w", err)
	}
	updated, err := time.Parse(time.RFC3339, updatedStr)
	if err != nil {
		return nil, fmt.Errorf("users: parse updated_at: %w", err)
	}
	u.CreatedAt = created
	u.UpdatedAt = updated
	if lastLoginStr.Valid && lastLoginStr.String != "" {
		t, err := time.Parse(time.RFC3339, lastLoginStr.String)
		if err != nil {
			return nil, fmt.Errorf("users: parse last_login_at: %w", err)
		}
		u.LastLoginAt = &t
	}
	return &u, nil
}
