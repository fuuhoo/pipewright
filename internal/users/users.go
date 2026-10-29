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
//   - SetProfile / FillProfileIfEmpty:本地档案(显示名 + 邮箱)的改写与「仅空则回填」
//   - Delete:删号(先清点牵连,再在一个事务里清引用行 + 删账号)
//   - GetByUsername / GetByID / List:查询
//
// 后续 story:邀请注册(邀请 token + URL)。
package users

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/fuuhoo/pipewright/internal/access"
	"github.com/fuuhoo/pipewright/internal/store"
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

// Role 枚举的别名。权威定义在 internal/access/roles.go(功能档位表按角色查),
// 这里只是让账户域内读起来顺手;新增角色只需改 access 一处。
const (
	RoleAdmin = access.RoleAdmin
	RoleUser  = access.RoleUser
)

// 领域错误(错误体不含敏感数据:不打印 hash / 密码 / 内部栈)。
var (
	ErrNotFound = errors.New("users: not found")
	ErrConflict = errors.New("users: conflict")
	// ErrValidation 标记「入参不合规则」这类可直接回传给用户的错误;HTTP 层据此
	// 回 400 而不是 500(SQL 细节一律不外泄)。
	ErrValidation = errors.New("users: invalid input")
	// 下面三条是删号前的阻断,语义都是「请求没错,但删下去会毁掉真数据或把管理员
	// 锁在门外」,HTTP 层一律回 409。前两条由 DeleteBlocked 携带计数(见该类型),
	// 它的 Error() 会直接进 UI,所以是中文可操作提示而不是内部标识。
	ErrHasGroups              = errors.New("users: still owns resource groups")
	ErrHasPersonalCredentials = errors.New("users: still owns personal credentials")
	ErrLastAdmin              = errors.New("users: last enabled admin")
)

// DeleteBlocked 表示「这个账号还有真数据挂在名下,先处置再删」。
//
// 为什么要带计数而不是只回一个哨兵:阻断的价值在于告诉管理员**去哪几条**动手。
// 「还是组长」这种话他看完仍然要自己去数,而数错一次的代价是删掉一个无主组。
// Error() 是给人读的(UI 直接回显这段),故用中文;errors.Is 靠下面的 Is 方法
// 仍能命中 ErrHasGroups / ErrHasPersonalCredentials,HTTP 层按档分错误码。
type DeleteBlocked struct {
	OwnedGroups         int
	PersonalCredentials int
}

func (b DeleteBlocked) Error() string {
	if b.OwnedGroups > 0 {
		return fmt.Sprintf("%d 个分组的组长仍是该账号,请先在「分组与权限」移交组长", b.OwnedGroups)
	}
	return fmt.Sprintf("%d 条个人凭据仍归属该账号,请先删除这些凭据", b.PersonalCredentials)
}

// Is 让 errors.Is(err, ErrHasGroups / ErrHasPersonalCredentials) 成立。
// 两类同时存在时 OwnedGroups 优先(组长要先移交,否则凭据删完仍删不掉账号)。
func (b DeleteBlocked) Is(target error) bool {
	switch target {
	case ErrHasGroups:
		return b.OwnedGroups > 0
	case ErrHasPersonalCredentials:
		return b.OwnedGroups == 0 && b.PersonalCredentials > 0
	}
	return false
}

// User 是对外可见的用户视图(不含 password_hash;敏感字段不暴露)。
type User struct {
	ID       string
	Username string
	// Role 是**主角色**(users.role),用于展示与旧口径回退;判定一律读 Roles 的并集。
	Role string
	// Roles 是这个账号挂的全部角色(0084 user_roles),顺序即展示顺序(Role 在首位)。
	// 读不到 join 行时回退成 [Role],保证旧库/迁移未跑全时账号仍可用。
	Roles       []string
	Enabled     bool
	Description string
	// DisplayName 是展示用的昵称/全名;为空时各处回退到 Username。
	// 它是**本地档案**:OAuth 首次建号时从平台写入,之后以本地值为准(见 FillProfileIfEmpty)。
	DisplayName string
	// Email 是档案邮箱,仅用于展示与联系。登录判定与域名白名单都不看它
	// (白名单读 user_identities.email 那份平台快照,见 internal/identity)。
	Email string
	// HasLocalPassword 表示这个账号有一个「本人知道的」本地口令。OAuth/OIDC 首登自动建号时
	// 写的是随机占位哈希(明文从未存在),这里为 false:账户页据此提供「首次设置口令」,
	// 而不是要求输入一个没人知道的当前口令。
	HasLocalPassword bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
	LastLoginAt      *time.Time
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
	// 角色集合也要有 admin 这一行:0084 之后判定读 user_roles,只写 users.role 会让
	// 内置管理员在「账号与角色」里显示成没有角色(会话仍按 users.role 兜底,但计数难看)。
	_, err = s.db.Exec(
		`INSERT INTO user_roles (user_id, role_id, created_at) VALUES (?, ?, ?)`+
			store.DoNothingSuffix(store.DialectOf(s.db), []string{"user_id", "role_id"}),
		BootstrapAdminRegularUserID, RoleAdmin, now,
	)
	if err != nil {
		return fmt.Errorf("users: bootstrap admin role: %w", err)
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
		`UPDATE users SET password_hash = ?, has_local_password = 1, updated_at = ? WHERE username = ? AND role = ?`,
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
		`SELECT id, username, password_hash, role, enabled, description, display_name, email,
		        has_local_password, created_at, updated_at, last_login_at
		 FROM users WHERE username = ?`, username)
	u, err := scanInternal(row)
	if err != nil {
		return nil, err
	}
	if err := s.attachRoles(&u.User); err != nil {
		return nil, err
	}
	return u, nil
}

// GetByID 取用户 view(无 hash)。
func (s *Service) GetByID(id string) (*User, error) {
	row := s.db.QueryRow(
		`SELECT id, username, role, enabled, description, display_name, email,
		        has_local_password, created_at, updated_at, last_login_at
		 FROM users WHERE id = ?`, id)
	u, err := scanView(row)
	if err != nil {
		return nil, err
	}
	if err := s.attachRoles(u); err != nil {
		return nil, err
	}
	return u, nil
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
		// 多角色:命中主角色或 user_roles 里的任一条都算。
		conds = append(conds, `(role = ? OR EXISTS (
			SELECT 1 FROM user_roles ur WHERE ur.user_id = users.id AND ur.role_id = ?))`)
		args = append(args, f.Role, f.Role)
	}
	if !f.IncludeDisabled {
		conds = append(conds, "enabled = 1")
	}
	args = append(args, limit, f.Offset)

	rows, err := s.db.Query(
		`SELECT id, username, role, enabled, description, display_name, email,
		        has_local_password, created_at, updated_at, last_login_at
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := s.attachRolesFor(out); err != nil {
		return nil, err
	}
	return out, nil
}

// attachRoles 读单个账号的角色集合并装进视图。
func (s *Service) attachRoles(u *User) error {
	sets, err := s.rolesForUsers([]string{u.ID})
	if err != nil {
		return err
	}
	applyRoles(u, sets[u.ID])
	return nil
}

// attachRolesFor 一次读回一批账号的角色集合(列表用,避免 N+1)。
func (s *Service) attachRolesFor(users []*User) error {
	if len(users) == 0 {
		return nil
	}
	ids := make([]string, 0, len(users))
	for _, u := range users {
		ids = append(ids, u.ID)
	}
	sets, err := s.rolesForUsers(ids)
	if err != nil {
		return err
	}
	for _, u := range users {
		applyRoles(u, sets[u.ID])
	}
	return nil
}

// rolesForUsers 批量读 user_roles(userID → 角色 id 集合)。
func (s *Service) rolesForUsers(ids []string) (map[string][]string, error) {
	if len(ids) == 0 {
		return map[string][]string{}, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.Query(
		`SELECT user_id, role_id FROM user_roles WHERE user_id IN (`+ph+`)
		 ORDER BY created_at ASC, role_id ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("users: load roles: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string][]string, len(ids))
	for rows.Next() {
		var uid, rid string
		if err := rows.Scan(&uid, &rid); err != nil {
			return nil, fmt.Errorf("users: scan role: %w", err)
		}
		if rid == "" {
			continue
		}
		out[uid] = append(out[uid], rid)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("users: iterate roles: %w", err)
	}
	return out, nil
}

// applyRoles 把 join 行装进视图:主角色排首位(展示口径),其余按查询顺序。
// join 行为空且 users.role 也为空 → 角色集合为空(不是「默认 user」):调用方据此 fail closed,
// 绝不拿一个猜出来的角色去签会话。
func applyRoles(u *User, set []string) {
	roles := make([]string, 0, len(set)+1)
	if u.Role != "" {
		roles = append(roles, u.Role)
	}
	for _, r := range set {
		if !slices.Contains(roles, r) {
			roles = append(roles, r)
		}
	}
	u.Roles = roles
}

// RoleSelectorForUser 把某账号的角色集合拼成会话/令牌用的选择器(主角色在首位)。
// 只拿到 *sql.DB 的调用方(auth 口令登录、apitoken 校验)复用它,不必各自抄一遍。
// 读不到 join 行时回退 primary —— 旧库只有 users.role 也照样能用。
func RoleSelectorForUser(ctx context.Context, db *sql.DB, userID, primary string) string {
	primary = strings.TrimSpace(primary)
	if strings.TrimSpace(userID) == "" {
		return primary
	}
	rows, err := db.QueryContext(ctx,
		`SELECT role_id FROM user_roles WHERE user_id = ? ORDER BY created_at ASC, role_id ASC`, userID)
	if err != nil {
		return primary
	}
	defer func() { _ = rows.Close() }()
	roles := make([]string, 0, 2)
	if primary != "" {
		roles = append(roles, primary)
	}
	for rows.Next() {
		var r string
		if rows.Scan(&r) != nil {
			continue
		}
		r = strings.TrimSpace(r)
		if r != "" && !slices.Contains(roles, r) {
			roles = append(roles, r)
		}
	}
	if len(roles) == 0 {
		return primary
	}
	return access.JoinRoles(roles)
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
	// Role 是单角色入参(旧调用点);Roles 非空时以 Roles 为准,Role 忽略。
	Role string
	// Roles 是这个账号要挂的全部角色(0084)。空 → 用 Role;两者都空 / Role 不认识 → user。
	Roles       []string
	Description string
	DisplayName string // 展示名;空则各处回退到 Username
	Email       string // 档案邮箱;仅展示,不参与任何判定
	// PlaceholderPassword = PasswordHash 是随机占位(明文无人知道,OAuth 首登自动建号)。
	// 这类账号标记 has_local_password=0,账户页给「首次设置口令」而不是「输入当前口令」。
	// 零值 = 真实口令(管理员建号 / bootstrap 同步行)。
	PlaceholderPassword bool
}

// Create 建普通用户或管理员账号(管理员建号,v6.2 阶段 9)。
//   - username 归一化后需 2~64 字符,仅允许字母/数字/._-;唯一冲突 → ErrConflict
//   - displayName / email 见 normalizeDisplayName / normalizeEmail(留空合法)
//   - role 不在 access 的角色枚举内(含空)一律按 user 处理
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
	displayName, err := normalizeDisplayName(in.DisplayName)
	if err != nil {
		return nil, err
	}
	email, err := normalizeEmail(in.Email)
	if err != nil {
		return nil, err
	}
	roles, err := createRoles(in)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	id := NewID()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("users: begin create: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	hasLocalPw := 1
	if in.PlaceholderPassword {
		hasLocalPw = 0
	}
	_, err = tx.Exec(
		`INSERT INTO users (id, username, password_hash, role, enabled, description,
		                   display_name, email, has_local_password, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?)`,
		id, username, in.PasswordHash, roles[0], strings.TrimSpace(in.Description),
		displayName, email, hasLocalPw, now, now,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w:用户名 %s 已存在", ErrConflict, username)
		}
		return nil, fmt.Errorf("users: create: %w", err)
	}
	for _, role := range roles {
		if _, err := tx.Exec(
			`INSERT INTO user_roles (user_id, role_id, created_at) VALUES (?, ?, ?)`,
			id, role, now,
		); err != nil {
			return nil, fmt.Errorf("users: create role %s: %w", role, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("users: commit create: %w", err)
	}
	return s.GetByID(id)
}

// createRoles 归一化建号入参的角色集合;空 / 不认识的历史单角色路径仍落 user。
func createRoles(in CreateInput) ([]string, error) {
	if len(in.Roles) == 0 {
		role := strings.TrimSpace(in.Role)
		if !access.ValidRole(role) {
			// 空串与未知名都落 user。注意:users.role 的空串只属于 0053 之前的旧**会话**行,
			// 语义是「按管理员」;建号时若把空串写进 users.role,下次登录就会继承那个含义。
			return []string{RoleUser}, nil
		}
		return []string{role}, nil
	}
	roles := access.SplitRoles(access.JoinRoles(in.Roles))
	if len(roles) == 0 {
		return []string{RoleUser}, nil
	}
	if err := validateRoleIDs(roles); err != nil {
		return nil, err
	}
	return roles, nil
}

// validateRoleIDs 确认每个角色 id 都在枚举内(含已装载的自定义角色)。
func validateRoleIDs(roles []string) error {
	for _, role := range roles {
		if !access.ValidRole(role) {
			return fmt.Errorf("%w:角色 %q 不在枚举内", ErrValidation, role)
		}
	}
	return nil
}

// SetPassword 按用户 ID 重置口令(hash 由调用方生成)。
// 影响 0 行 → ErrNotFound。刻意不改 username/role,重置口令不提权。
func (s *Service) SetPassword(id, passwordHash string) error {
	if strings.TrimSpace(passwordHash) == "" {
		return fmt.Errorf("users: set password requires hash")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	return s.execOneRow("set password",
		`UPDATE users SET password_hash = ?, has_local_password = 1, updated_at = ? WHERE id = ?`,
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

// SetRole 改单一角色(保留旧签名;等价于 SetRoles(id, []string{role}))。
func (s *Service) SetRole(id, role string) error {
	return s.SetRoles(id, []string{role})
}

// SetRoles 改写账号的完整角色集合(0084)。roles 去空去重,至少一个;首个成为主角色
// (users.role,展示与旧口径回退用)。任一 id 不在枚举内 → ErrValidation。
//
// 生效时机:角色是建会话时快照进 sessions.role 的,改库不会让已签发的会话立刻变色,
// 对方重新登录才拿到新档位(与「禁用账号不撤销会话」同一取舍,见 docs/权限架构说明.md §11.2)。
// 内置管理员那一行由 HTTP 层的 isBootstrapAdminRow 挡在门外,不在这里重复判。
func (s *Service) SetRoles(id string, roles []string) error {
	normalized := access.SplitRoles(access.JoinRoles(roles))
	if len(normalized) == 0 {
		return fmt.Errorf("%w:至少需要一个角色", ErrValidation)
	}
	if err := validateRoleIDs(normalized); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("users: begin set roles: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC().Format(time.RFC3339)
	res, err := tx.Exec(`UPDATE users SET role = ?, updated_at = ? WHERE id = ?`,
		normalized[0], now, id)
	if err != nil {
		return fmt.Errorf("users: set role: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("users: rows affected: %w", err)
	} else if n == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(`DELETE FROM user_roles WHERE user_id = ?`, id); err != nil {
		return fmt.Errorf("users: clear roles: %w", err)
	}
	for _, role := range normalized {
		if _, err := tx.Exec(
			`INSERT INTO user_roles (user_id, role_id, created_at) VALUES (?, ?, ?)`,
			id, role, now,
		); err != nil {
			return fmt.Errorf("users: set role %s: %w", role, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("users: commit set roles: %w", err)
	}
	return nil
}

// SetProfile 改本地档案:显示名与邮箱。两个字段都由调用方决定,允许清空
// (空显示名 = 回退展示用户名;空邮箱 = 未填)。改这个不动角色,也不影响登录。
func (s *Service) SetProfile(id, displayName, email string) error {
	name, err := normalizeDisplayName(displayName)
	if err != nil {
		return err
	}
	mail, err := normalizeEmail(email)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	return s.execOneRow("set profile",
		`UPDATE users SET display_name = ?, email = ?, updated_at = ? WHERE id = ?`,
		name, mail, now, id,
	)
}

// FillProfileIfEmpty 只在本地档案为空时填入平台给的值,两个字段各自独立判定。
//
// 为什么不干脆每次登录都覆盖:显示名是「首次建号写入、之后本地可编辑」的那一类
// —— 平台改了昵称不该把用户在本地改好的名字冲掉。同时它又得能给本迁移之前
// 建出来的老账号补上值(那些行 display_name 是空串),否则首登后前端依旧只有一个名字。
// 只动空值正好同时满足这两条。
//
// 两处都已有值时语句命中 0 行 —— 那是预期结果,不当失败处理,故不走 execOneRow。
func (s *Service) FillProfileIfEmpty(id, displayName, email string) error {
	if strings.TrimSpace(id) == "" {
		return nil
	}
	name, err := normalizeDisplayName(displayName)
	if err != nil {
		return err
	}
	mail, err := normalizeEmail(email)
	if err != nil {
		return err
	}
	if name == "" && mail == "" {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.Exec(
		`UPDATE users
		    SET display_name = CASE WHEN display_name = '' THEN ? ELSE display_name END,
		        email        = CASE WHEN email = '' THEN ? ELSE email END,
		        updated_at   = ?
		  WHERE id = ? AND (display_name = '' OR email = '')`,
		name, mail, now, id,
	)
	if err != nil {
		return fmt.Errorf("users: fill profile: %w", err)
	}
	return nil
}

// Delete 永久删除账号,并在同一个事务里清掉三类纯引用行。
//
// 删前先挡三件事,挡的都是「删下去会静默毁掉真数据」而不是「入参写错了」:
//   - 组长身份:resource_groups.owner_id NOT NULL 且没有外键,账号删了组不会消失,
//     只是变成谁都不认识的孤儿组;移交组长是管理员的一次明确改派,不该由删号代做。
//   - personal 凭据:密文一删不可恢复,而当前没有转移 owner 的端点。
//   - 最后一个启用的 admin:删掉就没人能再进「用户管理」把账号建回来。
//     (内置管理员行本来就由 HTTP 层的 isBootstrapAdminRow 挡在门外,这条是给手工
//     改过库、或从旧备份恢复出来的实例兜底。)
//
// 事务里清四张表:sessions.user_id、resource_group_members.user_id 与 api_tokens.user_id 都没有外键,
// 不显式删就会留下「账号已不存在但会话还能用」的活会话(这是安全边界,不是整洁问题)
// 和名册脏行;user_identities 虽然有 ON DELETE CASCADE,MySQL 与 SQLite 的外键开关
// 时机不同,这里照 role.Delete 的先例再显式删一次,保证两个方言清得一样干净。
//
// 刻意不动的历史:audit_log.actor、pipeline_runs.trigger_actor 与各表的 created_by。
// 那些记的是「当初谁干的」,账号没了也要查得出来;把它们改写成占位串等于烧掉线索。
func (s *Service) Delete(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("users: begin delete: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var role string
	var enabled int
	err = tx.QueryRow(`SELECT role, enabled FROM users WHERE id = ?`, id).Scan(&role, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("users: read account: %w", err)
	}

	var ownedGroups int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM resource_groups WHERE owner_id = ?`, id).Scan(&ownedGroups); err != nil {
		return fmt.Errorf("users: count owned groups: %w", err)
	}
	var personalCreds int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM credentials WHERE scope = 'personal' AND owner_id = ?`, id).Scan(&personalCreds); err != nil {
		return fmt.Errorf("users: count personal credentials: %w", err)
	}
	if ownedGroups > 0 || personalCreds > 0 {
		return DeleteBlocked{OwnedGroups: ownedGroups, PersonalCredentials: personalCreds}
	}

	// 角色集合:主角色 + user_roles;join 行为空时回退主角色(旧库)。
	roleRows, err := tx.Query(`SELECT role_id FROM user_roles WHERE user_id = ?`, id)
	if err != nil {
		return fmt.Errorf("users: read roles: %w", err)
	}
	roles := make([]string, 0, 2)
	for roleRows.Next() {
		var r string
		if err := roleRows.Scan(&r); err != nil {
			_ = roleRows.Close()
			return fmt.Errorf("users: scan role: %w", err)
		}
		if r != "" {
			roles = append(roles, r)
		}
	}
	if err := roleRows.Err(); err != nil {
		_ = roleRows.Close()
		return fmt.Errorf("users: iterate roles: %w", err)
	}
	_ = roleRows.Close()
	if len(roles) == 0 && role != "" {
		roles = []string{role}
	}

	if access.IsAdminRole(access.JoinRoles(roles)) && enabled != 0 {
		var otherAdmins int
		if err := tx.QueryRow(
			`SELECT COUNT(DISTINCT u.id) FROM users u
			  WHERE u.enabled = 1 AND u.id <> ?
			    AND (u.role = ? OR EXISTS (
			        SELECT 1 FROM user_roles ur WHERE ur.user_id = u.id AND ur.role_id = ?))`,
			id, RoleAdmin, RoleAdmin,
		).Scan(&otherAdmins); err != nil {
			return fmt.Errorf("users: count other admins: %w", err)
		}
		if otherAdmins == 0 {
			return ErrLastAdmin
		}
	}

	for _, q := range []string{
		`DELETE FROM sessions WHERE user_id = ?`,
		`DELETE FROM user_identities WHERE user_id = ?`,
		`DELETE FROM resource_group_members WHERE user_id = ?`,
		// API 令牌同样没有外键:留行就是留一把「谁也认不出归属、但可能被某台机器还在用」的凭据。
		`DELETE FROM api_tokens WHERE user_id = ?`,
		// 角色挂靠行没有外键,不删就留下指向已消失账号的脏行。
		`DELETE FROM user_roles WHERE user_id = ?`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return fmt.Errorf("users: clear references: %w", err)
		}
	}
	res, err := tx.Exec(`DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("users: delete account: %w", err)
	}
	// 上面已按 id 读过这一行,0 行只会是并发删除;照样兜住并回 ErrNotFound。
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("users: rows affected: %w", err)
	} else if n == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("users: commit delete: %w", err)
	}
	return nil
}

// MaxDisplayNameLen / MaxEmailLen 是档案两字段的长度上限(按字符计,中文算一个)。
// 迁移里的 VARCHAR 宽度与这里必须一致,否则 MySQL 侧会静默截断。
const (
	MaxDisplayNameLen = 64
	MaxEmailLen       = 254
)

// normalizeDisplayName 去首尾空白并按**字符**截断。
// 刻意不校验字符集:显示名要能写中文、日文、带空格的全名,它是给人看的不是给人查的。
func normalizeDisplayName(v string) (string, error) {
	v = strings.Join(strings.Fields(v), " ") // 折叠内部连续空白(含换行),避免脏值挤坏表格
	if len(v) > MaxDisplayNameLen {
		runes := []rune(v)
		if len(runes) > MaxDisplayNameLen {
			v = string(runes[:MaxDisplayNameLen])
		}
	}
	return v, nil
}

// normalizeEmail 去空白 + 长度上限 + 最低限度形状校验(空串合法 = 未填)。
//
// 刻意不做 RFC 完整校验:这个字段只用于展示与联系,不参与登录、白名单或任何账号匹配,
// 严格校验只会让 OAuth 回填时因平台给的怪值而整条链路报错。但「压根不是个邮箱」的要拒:
// 管理员手输错值时,一句人读提示比页面上一直挂着一串乱码有用。
func normalizeEmail(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	if strings.IndexFunc(v, unicode.IsSpace) >= 0 {
		return "", fmt.Errorf("%w:邮箱不能含空格", ErrValidation)
	}
	at := strings.LastIndex(v, "@")
	if at <= 0 || at == len(v)-1 || strings.Count(v, "@") != 1 {
		return "", fmt.Errorf("%w:邮箱格式不正确", ErrValidation)
	}
	if len([]rune(v)) > MaxEmailLen {
		return "", fmt.Errorf("%w:邮箱过长", ErrValidation)
	}
	return v, nil
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
	var hasLocalPw int
	var createdStr, updatedStr string
	if err := sc.Scan(
		&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.Enabled, &u.Description,
		&u.DisplayName, &u.Email, &hasLocalPw, &createdStr, &updatedStr, &lastLogin,
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
	u.HasLocalPassword = hasLocalPw != 0
	if lastLogin.Valid && lastLogin.String != "" {
		t, err := time.Parse(time.RFC3339, lastLogin.String)
		if err != nil {
			return nil, fmt.Errorf("users: parse last_login_at: %w", err)
		}
		u.LastLoginAt = &t
	}
	return &u, nil
}

// scanView 把「11 列视图 SELECT(id, username, role, enabled, description, display_name,
// email, has_local_password, created_at, updated_at, last_login_at,不含 password_hash)」扫描为 User。
//
// 不复用 scanInternal(后者要求 password_hash 在 SELECT 里),否则 GetByID 的
// 11 列查询会因 destination 数不匹配直接报错(scan: expected 12 destination, not 11)。
func scanView(sc scanner) (*User, error) {
	var (
		u            User
		enabled      int
		hasLocalPw   int
		createdStr   string
		updatedStr   string
		lastLoginStr sql.NullString
	)
	if err := sc.Scan(
		&u.ID, &u.Username, &u.Role, &enabled, &u.Description, &u.DisplayName, &u.Email,
		&hasLocalPw, &createdStr, &updatedStr, &lastLoginStr,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("users: scan view: %w", err)
	}
	u.Enabled = enabled != 0
	u.HasLocalPassword = hasLocalPw != 0
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
