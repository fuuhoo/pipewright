// Package role 是「可配置角色」的领域层:P4 自定义角色的存储、校验与判定侧接线。
//
// 与内置档的分工(权威说明见 docs/权限架构说明.md §2):
//   - 内置五档 admin / user / developer / ops / viewer 的点集是 internal/access/perms.go 的代码表,
//     页面上当**模板**呈现:不可改、不可删、可复制成自定义角色。升级时新加的功能点自动跟着
//     内置档位走,内置 admin 也永远改不掉(自锁兜底)。
//   - 本包只管自定义角色:存 0062 的 roles / role_perms,users.role 存这里的 id(uuid v4)。
//
// 判定不经过本包:access 通过 RoleStore 窄接口读本包的仓储实现(access.SetRoleStore + ReloadRoles),
// 所以「这个角色能不能做某事」永远只有 access 一个出口,不会出现两套口径。
//
// 三条硬边界(都在写路径上挡,不靠前端自觉):
//   - settings.access 不许分给自定义角色:有了它就能进设置页(建号、改角色、看审计、
//     拿全局凭据明文),那等于自制一个管理员,和「细粒度角色」是反的。
//   - 内置 id 不可写(404/409 二选一:改 → 409 builtin_role)。
//   - 仍被账号使用的角色不可删(409 role_in_use,带回人数),免得留下指向不存在角色的脏行。
package role

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/huangchengsir/pipewright/internal/access"
)

// 领域错误。HTTP 层按 errors.Is 映射状态码,包装一律用 %w;错误体不带 SQL 细节。
var (
	ErrNotFound   = errors.New("role: not found")
	ErrValidation = errors.New("role: invalid input")
	// ErrDuplicateName 名字大小写不敏感全局唯一(列表与下拉都按名字读)。
	ErrDuplicateName = errors.New("role: duplicate name")
	// ErrBuiltin 表示试图改 / 删内置角色。
	ErrBuiltin = errors.New("role: builtin role is read-only")
	// ErrInUse 表示角色仍被账号使用,须先改派再删(包装里带人数)。
	ErrInUse = errors.New("role: still assigned to users")
	// ErrSettingsPoint 表示点集里含 settings.access。
	ErrSettingsPoint = errors.New("role: settings.access is not assignable")
	// ErrUnknownPoint 表示点集里有字典外的点(前端名单过期或手拼请求)。
	ErrUnknownPoint = errors.New("role: unknown permission point")
)

// maxNameLen 是角色展示名长度上限;与「一眼读得完」对齐,而不是贴着 DB 列宽。
const maxNameLen = 40

// Role 是一个角色的领域视图。内置档与自定义档共用这一结构:Builtin 决定页面能否编辑,
// Perms 是判定的唯一输入(内置档的点集从代码表取,不落库)。
type Role struct {
	ID          string
	Name        string
	Description string
	// BaseRole 是复制来源的内置档 id('' = 手建);只用于页面提示「基于运维模板」,无判定语义。
	BaseRole string
	Builtin  bool
	Perms    []string
	// UserCount 是该角色当前的账号数(删除挡门与列表展示)。
	UserCount int
	CreatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Service 是角色领域接口,同时充当 access.RoleStore。
type Service struct {
	db *sql.DB
}

// New 构造服务;db 与其余领域层一样是裸 *sql.DB(占位符双方言都用 ?)。
func New(db *sql.DB) *Service { return &Service{db: db} }

// 编译期确认 satisfies 判定侧的读端窄接口。
var _ access.RoleStore = (*Service)(nil)

// ListCustomRoles 实现 access.RoleStore:返回库里全部自定义角色及其点集。
// access 的缓存装载走这条路径,所以返回的点集已经按字典序过滤过。
func (s *Service) ListCustomRoles(ctx context.Context) ([]access.CustomRole, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name FROM roles ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("role: 列自定义角色: %w", err)
	}
	defer rows.Close()
	type slot struct {
		id, name string
		perms    []string
		order    int
	}
	var out []slot
	i := 0
	for rows.Next() {
		var sl slot
		if err := rows.Scan(&sl.id, &sl.name); err != nil {
			return nil, fmt.Errorf("role: 读自定义角色行: %w", err)
		}
		sl.order = i
		out = append(out, sl)
		i++
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("role: 遍历自定义角色: %w", err)
	}
	// 点集一次取全再按角色分桶:角色数量是个位数,N 次单查只是多几轮往返。
	permRows, err := s.db.QueryContext(ctx,
		`SELECT role_id, perm_id FROM role_perms`)
	if err != nil {
		return nil, fmt.Errorf("role: 列角色点集: %w", err)
	}
	defer permRows.Close()
	byRole := make(map[string][]string)
	for permRows.Next() {
		var roleID, permID string
		if err := permRows.Scan(&roleID, &permID); err != nil {
			return nil, fmt.Errorf("role: 读角色点行: %w", err)
		}
		byRole[roleID] = append(byRole[roleID], permID)
	}
	if err := permRows.Err(); err != nil {
		return nil, fmt.Errorf("role: 遍历角色点集: %w", err)
	}
	// 自定义角色一律不落内置 id:access.ReloadRoles 也会忽略,这里提前剔掉免得页面看到脏行。
	outRoles := make([]access.CustomRole, 0, len(out))
	for _, sl := range out {
		if access.IsBuiltinRole(sl.id) {
			continue
		}
		outRoles = append(outRoles, access.CustomRole{ID: sl.id, Name: sl.name, Perms: orderPerms(byRole[sl.id])})
	}
	return outRoles, nil
}

// List 返回内置档 + 自定义角色的完整名单(内置在前,按 access.Roles() 的声明序)。
// 设置页与「账号与角色」的角色下拉都读它,所以 UserCount 两类都要有。
func (s *Service) List(ctx context.Context) ([]Role, error) {
	counts, err := s.userCounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Role, 0, len(access.Roles()))
	for _, id := range access.Roles() {
		out = append(out, Role{
			ID:          id,
			Name:        id,
			Builtin:     true,
			Perms:       access.PermsFor(id),
			UserCount:   counts[id],
			Description: builtinHint(id),
		})
	}
	custom, err := s.listCustomRows(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range custom {
		r.UserCount = counts[r.ID]
		out = append(out, r)
	}
	return out, nil
}

// builtinHint 给内置档一句固定说明(它是代码表里的定位,不是库里的自由文本)。
func builtinHint(id string) string {
	switch id {
	case access.RoleAdmin:
		return "全部功能点,含设置类"
	case access.RoleUser:
		return "四类落点可操作,进不了设置"
	case access.RoleDeveloper:
		return "编排与运行可改,落点只读"
	case access.RoleOps:
		return "落点可操作,编排只读"
	case access.RoleViewer:
		return "全部只读"
	default:
		return ""
	}
}

// Get 取单个角色(内置或自定义)。
func (s *Service) Get(ctx context.Context, id string) (*Role, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: 缺角色 ID", ErrValidation)
	}
	counts, err := s.userCounts(ctx)
	if err != nil {
		return nil, err
	}
	if access.IsBuiltinRole(id) {
		r := Role{ID: id, Name: id, Builtin: true, Perms: access.PermsFor(id),
			UserCount: counts[id], Description: builtinHint(id)}
		return &r, nil
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT name, description, base_role, created_by, created_at, updated_at
		 FROM roles WHERE id = ?`, id)
	var (
		name, desc, base, createdBy, created, updated string
	)
	if err := row.Scan(&name, &desc, &base, &createdBy, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return nil, fmt.Errorf("role: 读角色: %w", err)
	}
	perms, err := s.permsOf(ctx, id)
	if err != nil {
		return nil, err
	}
	createdAt, _ := time.Parse(time.RFC3339, created)
	updatedAt, _ := time.Parse(time.RFC3339, updated)
	return &Role{ID: id, Name: name, Description: desc, BaseRole: base,
		Perms: perms, UserCount: counts[id], CreatedBy: createdBy,
		CreatedAt: createdAt, UpdatedAt: updatedAt}, nil
}

// CreateInput 是建自定义角色的入参。Perms 为 nil 视为空集(一个入口都不亮,由调用方补)。
type CreateInput struct {
	Name        string
	Description string
	BaseRole    string
	Perms       []string
	CreatedBy   string
}

// Create 落一条自定义角色 + 它的点集,成功后刷新判定缓存。
// BaseRole 只接受内置档 id(页面「从模板复制」带过来);非法值报错而不是静默清空,
// 免得用户在页面上看到「基于 developer 模板」而库里存着空串。
func (s *Service) Create(ctx context.Context, in CreateInput) (*Role, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, fmt.Errorf("%w: 角色名不能为空", ErrValidation)
	}
	if len([]rune(name)) > maxNameLen {
		return nil, fmt.Errorf("%w: 角色名最多 %d 个字符", ErrValidation, maxNameLen)
	}
	base := strings.TrimSpace(in.BaseRole)
	if base != "" && !access.IsBuiltinRole(base) {
		return nil, fmt.Errorf("%w: 模板角色 %q 不是内置档", ErrValidation, base)
	}
	perms, err := normalizePerms(in.Perms)
	if err != nil {
		return nil, err
	}
	if dup, err := s.nameTaken(ctx, name, ""); err != nil {
		return nil, err
	} else if dup {
		return nil, fmt.Errorf("%w: 已有同名角色 %q", ErrDuplicateName, name)
	}

	id := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("role: 开事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO roles (id, name, description, base_role, created_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, name, strings.TrimSpace(in.Description), base, in.CreatedBy, now, now); err != nil {
		return nil, fmt.Errorf("role: 建角色: %w", err)
	}
	if err := insertPerms(ctx, tx, id, perms); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("role: 提交: %w", err)
	}
	if err := access.ReloadRoles(ctx); err != nil {
		return nil, err
	}
	createdAt, _ := time.Parse(time.RFC3339, now)
	return &Role{ID: id, Name: name, Description: strings.TrimSpace(in.Description),
		BaseRole: base, Perms: perms, CreatedBy: in.CreatedBy,
		CreatedAt: createdAt, UpdatedAt: createdAt}, nil
}

// UpdateInput 是改名 / 改描述 / 改点集的入参:nil 字段表示不动。
type UpdateInput struct {
	Name        *string
	Description *string
	Perms       *[]string
}

// Update 修改自定义角色;内置档一律拒绝。点集变更后刷新缓存 —— 在线用户下次拉
// /api/auth/session(含页面刷新)就拿到新能力位,换角色才需要重登。
func (s *Service) Update(ctx context.Context, id string, in UpdateInput) (*Role, error) {
	if access.IsBuiltinRole(id) {
		return nil, fmt.Errorf("%w: %s 是内置角色,要改请先复制成自定义角色", ErrBuiltin, id)
	}
	cur, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	name := cur.Name
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
		if name == "" {
			return nil, fmt.Errorf("%w: 角色名不能为空", ErrValidation)
		}
		if len([]rune(name)) > maxNameLen {
			return nil, fmt.Errorf("%w: 角色名最多 %d 个字符", ErrValidation, maxNameLen)
		}
	}
	desc := cur.Description
	if in.Description != nil {
		desc = strings.TrimSpace(*in.Description)
	}
	perms := cur.Perms
	if in.Perms != nil {
		normalized, err := normalizePerms(*in.Perms)
		if err != nil {
			return nil, err
		}
		perms = normalized
	}
	if name != cur.Name {
		if dup, err := s.nameTaken(ctx, name, id); err != nil {
			return nil, err
		} else if dup {
			return nil, fmt.Errorf("%w: 已有同名角色 %q", ErrDuplicateName, name)
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("role: 开事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`UPDATE roles SET name = ?, description = ?, updated_at = ? WHERE id = ?`,
		name, desc, now, id); err != nil {
		return nil, fmt.Errorf("role: 改角色: %w", err)
	}
	if in.Perms != nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM role_perms WHERE role_id = ?`, id); err != nil {
			return nil, fmt.Errorf("role: 清旧点集: %w", err)
		}
		if err := insertPerms(ctx, tx, id, perms); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("role: 提交: %w", err)
	}
	if err := access.ReloadRoles(ctx); err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// Copy 以某个角色(内置或自定义)的点集为起点建一个新角色。内置档不可改,
// 「复制一份再调」就是它作为模板的用法。
//
// 源角色的 settings.access 在这里剥掉(而不是报错):从 admin 模板复制一份是常见起手式,
// 而那个点本来就不许给自定义角色 —— 报错只会让人以为复制坏了。UI 里它显示为
// 「仅内置管理员」的禁用项,复制走的那份自然没有它。
func (s *Service) Copy(ctx context.Context, id, name, createdBy string) (*Role, error) {
	src, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	base := ""
	if src.Builtin {
		base = src.ID
	}
	perms := make([]string, 0, len(src.Perms))
	for _, p := range src.Perms {
		if p == access.PermSettingsAccess {
			continue
		}
		perms = append(perms, p)
	}
	return s.Create(ctx, CreateInput{
		Name:        name,
		Description: src.Description,
		BaseRole:    base,
		Perms:       perms,
		CreatedBy:   createdBy,
	})
}

// Delete 删除自定义角色。内置档拒绝;仍有账号在用时拒绝(留下指向不存在角色的
// users.role 会变成一个点都不给的脏账号,与其静默降级不如让人先改派)。
func (s *Service) Delete(ctx context.Context, id string) error {
	if access.IsBuiltinRole(id) {
		return fmt.Errorf("%w: %s 是内置角色,不可删除", ErrBuiltin, id)
	}
	counts, err := s.userCounts(ctx)
	if err != nil {
		return err
	}
	if counts[id] > 0 {
		return fmt.Errorf("%w: 仍有 %d 个账号在使用", ErrInUse, counts[id])
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM roles WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("role: 删角色: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	// role_perms 靠外键 CASCADE 清,但 MySQL 与 SQLite 的 pragma 生效时机不同,
	// 这里显式删一次保证两侧一致(幂等)。
	if _, err := s.db.ExecContext(ctx, `DELETE FROM role_perms WHERE role_id = ?`, id); err != nil {
		return fmt.Errorf("role: 清角色点集: %w", err)
	}
	return access.ReloadRoles(ctx)
}

// permsOf 取某角色的点集(字典序)。
func (s *Service) permsOf(ctx context.Context, roleID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT perm_id FROM role_perms WHERE role_id = ?`, roleID)
	if err != nil {
		return nil, fmt.Errorf("role: 读点集: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("role: 读点集行: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("role: 遍历点集: %w", err)
	}
	return orderPerms(ids), nil
}

// listCustomRows 读自定义角色主体(不含点数)。
func (s *Service) listCustomRows(ctx context.Context) ([]Role, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, description, base_role, created_by, created_at, updated_at
		 FROM roles ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("role: 列角色: %w", err)
	}
	defer rows.Close()
	var out []Role
	for rows.Next() {
		var (
			r                Role
			created, updated string
		)
		if err := rows.Scan(&r.ID, &r.Name, &r.Description, &r.BaseRole,
			&r.CreatedBy, &created, &updated); err != nil {
			return nil, fmt.Errorf("role: 读角色行: %w", err)
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339, created)
		r.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("role: 遍历角色: %w", err)
	}
	// 点集按一次查询分桶,免得每个角色各打一趟。
	permRows, err := s.db.QueryContext(ctx, `SELECT role_id, perm_id FROM role_perms`)
	if err != nil {
		return nil, fmt.Errorf("role: 列角色点集: %w", err)
	}
	defer permRows.Close()
	byRole := make(map[string][]string)
	for permRows.Next() {
		var roleID, permID string
		if err := permRows.Scan(&roleID, &permID); err != nil {
			return nil, fmt.Errorf("role: 读角色点行: %w", err)
		}
		byRole[roleID] = append(byRole[roleID], permID)
	}
	if err := permRows.Err(); err != nil {
		return nil, fmt.Errorf("role: 遍历角色点集: %w", err)
	}
	for i := range out {
		out[i].Perms = orderPerms(byRole[out[i].ID])
	}
	return out, nil
}

// userCounts 一次拿到所有角色的账号数(users.role → 计数)。
func (s *Service) userCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT role, COUNT(*) FROM users GROUP BY role`)
	if err != nil {
		return nil, fmt.Errorf("role: 统计账号数: %w", err)
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var role string
		var n int
		if err := rows.Scan(&role, &n); err != nil {
			return nil, fmt.Errorf("role: 读账号数: %w", err)
		}
		out[role] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("role: 遍历账号数: %w", err)
	}
	return out, nil
}

// nameTaken 报告展示名是否已被占用(大小写不敏感;excludeID 用于改名时放过自己)。
// 不靠 DB 的 UNIQUE:两侧方言的大小写敏感性不一致,查一遍才有统一口径。
func (s *Service) nameTaken(ctx context.Context, name, excludeID string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name FROM roles`)
	if err != nil {
		return false, fmt.Errorf("role: 查重名: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, existing string
		if err := rows.Scan(&id, &existing); err != nil {
			return false, fmt.Errorf("role: 读重名行: %w", err)
		}
		if id == excludeID {
			continue
		}
		if strings.EqualFold(existing, name) {
			return true, nil
		}
	}
	return false, rows.Err()
}

// normalizePerms 去空去重、拒绝字典外的点与 settings.access,并按字典序排列。
func normalizePerms(ids []string) ([]string, error) {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] {
			continue
		}
		if !access.KnownPerm(id) {
			return nil, fmt.Errorf("%w: %s", ErrUnknownPoint, id)
		}
		if id == access.PermSettingsAccess {
			return nil, fmt.Errorf("%w: 它是「进得了设置」的总闸,只给内置管理员", ErrSettingsPoint)
		}
		seen[id] = true
		out = append(out, id)
	}
	return orderPerms(out), nil
}

// orderPerms 把点集排成字典序(前端列表与测试比对都要稳定顺序)。
func orderPerms(ids []string) []string {
	dict := access.Perms()
	order := make(map[string]int, len(dict))
	for i, p := range dict {
		order[p.ID] = i
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := order[id]; ok {
			out = append(out, id)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return order[out[i]] < order[out[j]] })
	return out
}

// insertPerms 在事务内写入点集。
func insertPerms(ctx context.Context, tx *sql.Tx, roleID string, perms []string) error {
	for _, id := range perms {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO role_perms (role_id, perm_id) VALUES (?, ?)`, roleID, id); err != nil {
			return fmt.Errorf("role: 写点 %s: %w", id, err)
		}
	}
	return nil
}
