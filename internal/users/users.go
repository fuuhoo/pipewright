// Package users 提供普通用户 + 管理员个人行的领域层。
//
// 业务含义(v6.2 §3.5):
//   - users 表存两类用户:role='user' 的普通用户 + role='admin' 的"管理员个人行"
//     (与 admin_user.id=1 业务同一实体的 users 视角)。
//   - personal 凭据 owner_id 指向 users.id(UUID v4);管理员自己的 personal 凭据
//     owner_id 指向该管理员在 users 中对应记录。
//
// 本期(v6.2 阶段 1)实现最小集:
//   - BootstrapAdminRow:首次启动在 users 中同步 admin_user 的管理员行
//   - SyncAdminPasswordChange:admin_user 改口令后同步到 users.role='admin' 行
//   - GetByUsername / GetByID:GetUser 错误用
//
// 后续 story:邀请注册(邀请 token + URL)、Disable / List / GetByID 等管理 API。
package users

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

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
	Role           string // "admin" | "user" | ""(不限)
	IncludeDisabled bool   // false=仅 enabled=1(默认)
	Limit          int    // <=0 → 默认 100;上限 500
	Offset         int    // >=0
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