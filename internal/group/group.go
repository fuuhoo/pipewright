// Package group 是「资源分组」的领域层:v6.2 分组权限的落地载体。
//
// 分组回答两个问题:这条资源(项目 / 服务器)属于谁,以及谁能看见 / 操作它。
// 判定逻辑本身在 internal/access(纯函数 + 窄读接口),本包负责:
//   - 分组本身的 CRUD 与成员名册;
//   - 实现 access.Repository,把「资源 → 分组 → 快照」的读路径接上数据库。
//
// 三态语义(与 internal/store/migrations/0054~0057 一致):
//   - 资源 group_id = ”      → 未归组,全员可见可操作(存量数据即此态)
//   - visibility = 'public'   → 同上;只有组长/管理员能改其归属
//   - visibility = 'private'  → 组长 + 名册成员 + 管理员可见可操作,其余 403
//
// 删组刻意要求「先把组内资源移出」(ErrGroupNotEmpty):projects/servers.group_id 没有
// 外键约束(见 0056 注释),静默删组会留下一堆指向不存在分组的行,而 access.Service
// 对这种悬挂引用按最私有一档处理(fail closed)——那时普通用户连自己的项目都打不开。
package group

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/huangchengsir/pipewright/internal/access"
	"github.com/huangchengsir/pipewright/internal/store"
)

// 领域错误。HTTP 层按 errors.Is 映射状态码,包装一律用 %w。
var (
	// ErrNotFound 表示分组不存在。
	ErrNotFound = errors.New("group: not found")
	// ErrEmptyName 表示分组名为空。
	ErrEmptyName = errors.New("group: name must not be empty")
	// ErrDuplicateName 表示分组名已被占用(名字全局唯一,列表与归组下拉都按名字展示)。
	ErrDuplicateName = errors.New("group: duplicate name")
	// ErrInvalidVisibility 表示 visibility 不在 {public, private}。
	ErrInvalidVisibility = errors.New("group: invalid visibility")
	// ErrOwnerRequired 表示缺组长(组长是私有分组的唯一管理入口,不能为空)。
	ErrOwnerRequired = errors.New("group: owner required")
	// ErrUserNotFound 表示 owner / member 指向的 users.id 不存在。
	ErrUserNotFound = errors.New("group: user not found")
	// ErrGroupNotEmpty 表示组内仍有资源,须先移出再删。
	ErrGroupNotEmpty = errors.New("group: still has resources")
)

// Group 是分组领域模型。MemberIDs 只在 Get / List 时填充(名册是另一张表)。
type Group struct {
	ID          string
	Name        string
	Description string
	Visibility  string
	OwnerID     string
	// OwnerName 是冗余展示名(join users),仅读路径填充;非持久列。
	OwnerName string
	CreatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time

	MemberIDs []string
	// ProjectCount / ServerCount 只给管理页展示「组内有多少东西」;
	// 删组前的挡门检查另有 Delete 里的实查,这里不参与判定。
	ProjectCount int
	ServerCount  int
}

// Snapshot 转成 access.Group,供 access.Decide 直接判定(不碰数据库)。
func (g *Group) Snapshot() *access.Group {
	if g == nil {
		return nil
	}
	return &access.Group{ID: g.ID, Visibility: g.Visibility, OwnerID: g.OwnerID, MemberIDs: g.MemberIDs}
}

// IsPrivate 报告是否私有分组。
func (g *Group) IsPrivate() bool { return g != nil && g.Visibility == access.VisibilityPrivate }

// CreateInput 是建组入参。Visibility 为空按 private 处理(默认私有,公开是显式决定)。
type CreateInput struct {
	Name        string
	Description string
	Visibility  string
	OwnerID     string
	MemberIDs   []string
}

// UpdateInput 是改组入参;指针为 nil 表示不修改。
type UpdateInput struct {
	Name        *string
	Description *string
	Visibility  *string
	OwnerID     *string
}

// Service 是分组领域服务。db 由 store 提供,本包只以参数化 SQL 触库。
type Service struct {
	db *sql.DB
}

// New 构造 Service。
func New(db *sql.DB) *Service { return &Service{db: db} }

// normalizeVisibility 收敛入参可见性;非法返回 ErrInvalidVisibility。空串取默认 private。
func normalizeVisibility(v string) (string, error) {
	switch v = strings.TrimSpace(v); v {
	case "":
		return access.VisibilityPrivate, nil
	case access.VisibilityPublic, access.VisibilityPrivate:
		return v, nil
	default:
		return "", ErrInvalidVisibility
	}
}

// userExists 校验 users.id 存在(组长/成员不能指向已删用户)。
func (s *Service) userExists(ctx context.Context, userID string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM users WHERE id = ?`, userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("group: check user: %w", err)
	}
	return true, nil
}

// Create 建组并写入名册。名字唯一;owner 与成员必须是真实用户。
func (s *Service) Create(ctx context.Context, in CreateInput) (*Group, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, ErrEmptyName
	}
	visibility, err := normalizeVisibility(in.Visibility)
	if err != nil {
		return nil, err
	}
	ownerID := strings.TrimSpace(in.OwnerID)
	if ownerID == "" {
		return nil, ErrOwnerRequired
	}
	ok, err := s.userExists(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrUserNotFound
	}

	id := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO resource_groups (id, name, description, visibility, owner_id, created_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, name, strings.TrimSpace(in.Description), visibility, ownerID, ownerID, now, now,
	); err != nil {
		if store.IsUniqueErr(err) {
			return nil, ErrDuplicateName
		}
		return nil, fmt.Errorf("group: insert: %w", err)
	}
	if err := s.replaceMembers(ctx, id, in.MemberIDs, ownerID); err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// replaceMembers 用入参名册整体覆盖(去重、剔除组长本人——组长身份来自 owner_id,
// 名册里再写一份会造成两处真相)。addedBy 记入 created_by 供审计回溯。
func (s *Service) replaceMembers(ctx context.Context, groupID string, memberIDs []string, addedBy string) error {
	if len(memberIDs) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(memberIDs))
	now := time.Now().UTC().Format(time.RFC3339)
	for _, raw := range memberIDs {
		uid := strings.TrimSpace(raw)
		if uid == "" || seen[uid] {
			continue
		}
		seen[uid] = true
		var owner string
		if err := s.db.QueryRowContext(ctx,
			`SELECT owner_id FROM resource_groups WHERE id = ?`, groupID).Scan(&owner); err != nil {
			return fmt.Errorf("group: load owner: %w", err)
		}
		if uid == owner {
			continue
		}
		ok, err := s.userExists(ctx, uid)
		if err != nil {
			return err
		}
		if !ok {
			return ErrUserNotFound
		}
		// 名册主键 (group_id, user_id) 天然去重;冲突即「已是成员」,按幂等成功处理。
		// 不用 ON CONFLICT / INSERT IGNORE:前者 MySQL 不支持,后者 SQLite 不支持。
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO resource_group_members (group_id, user_id, created_at, created_by)
			 VALUES (?, ?, ?, ?)`, groupID, uid, now, addedBy); err != nil && !store.IsUniqueErr(err) {
			return fmt.Errorf("group: add member: %w", err)
		}
	}
	return nil
}

// groupCols 是读路径统一的列顺序(OwnerName 走 LEFT JOIN users)。
const groupCols = `g.id, g.name, g.description, g.visibility, g.owner_id,
	COALESCE(u.username, ''), g.created_by, g.created_at, g.updated_at,
	(SELECT COUNT(1) FROM projects pr WHERE pr.group_id = g.id),
	(SELECT COUNT(1) FROM servers sv WHERE sv.group_id = g.id)`

func scanGroup(row scanner) (*Group, error) {
	var g Group
	var createdStr, updatedStr, ownerName string
	if err := row.Scan(&g.ID, &g.Name, &g.Description, &g.Visibility, &g.OwnerID,
		&ownerName, &g.CreatedBy, &createdStr, &updatedStr, &g.ProjectCount, &g.ServerCount); err != nil {
		return nil, err
	}
	g.OwnerName = ownerName
	var err error
	if g.CreatedAt, err = time.Parse(time.RFC3339, createdStr); err != nil {
		return nil, fmt.Errorf("group: parse created_at: %w", err)
	}
	if g.UpdatedAt, err = time.Parse(time.RFC3339, updatedStr); err != nil {
		return nil, fmt.Errorf("group: parse updated_at: %w", err)
	}
	return &g, nil
}

type scanner interface {
	Scan(dest ...any) error
}

// Get 返回单个分组(含名册)。不存在返回 ErrNotFound。
func (s *Service) Get(ctx context.Context, id string) (*Group, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+groupCols+`
		 FROM resource_groups g LEFT JOIN users u ON u.id = g.owner_id WHERE g.id = ?`, id)
	g, err := scanGroup(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("group: get: %w", err)
	}
	members, err := s.members(ctx, id)
	if err != nil {
		return nil, err
	}
	g.MemberIDs = members
	return g, nil
}

func (s *Service) members(ctx context.Context, groupID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT user_id FROM resource_group_members WHERE group_id = ? ORDER BY created_at, user_id`, groupID)
	if err != nil {
		return nil, fmt.Errorf("group: list members: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []string{}
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return nil, fmt.Errorf("group: scan member: %w", err)
		}
		out = append(out, uid)
	}
	return out, rows.Err()
}

// List 返回全部分组(含名册)。调用方必须是管理员:普通用户可见范围走 ListVisible。
func (s *Service) List(ctx context.Context) ([]Group, error) {
	return s.queryGroups(ctx, "", nil)
}

// ListVisible 返回 actor 能看见的分组:所有 public + 自己当组长的 + 自己在名册里的。
// 管理员不需要调用它(直接 List)。
func (s *Service) ListVisible(ctx context.Context, userID string) ([]Group, error) {
	return s.queryGroups(ctx,
		`g.visibility = ? OR g.owner_id = ? OR EXISTS
			(SELECT 1 FROM resource_group_members m WHERE m.group_id = g.id AND m.user_id = ?)`,
		[]any{access.VisibilityPublic, userID, userID})
}

func (s *Service) queryGroups(ctx context.Context, where string, args []any) ([]Group, error) {
	q := `SELECT ` + groupCols + `
	     FROM resource_groups g LEFT JOIN users u ON u.id = g.owner_id`
	if where != "" {
		q += " WHERE " + where
	}
	q += " ORDER BY g.name"
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("group: list: %w", err)
	}
	out := []Group{}
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("group: scan: %w", err)
		}
		out = append(out, *g)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("group: list: %w", err)
	}
	// 名册必须在 rows 关闭之后再取:store 对 SQLite 开了 MaxOpenConns(1),
	// 在 Next() 循环里再发一条查询会等不到第二个连接(死锁)。
	for i := range out {
		members, err := s.members(ctx, out[i].ID)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		out[i].MemberIDs = members
	}
	return out, rows.Close()
}

// Update 改组名/描述/可见性/组长。可见性从 private 改 public 会放大可见范围,
// 由 HTTP 层要求 Manage 权限(组长或管理员)。
func (s *Service) Update(ctx context.Context, id string, in UpdateInput) (*Group, error) {
	var name, description, visibility, ownerID string
	err := s.db.QueryRowContext(ctx,
		`SELECT name, description, visibility, owner_id FROM resource_groups WHERE id = ?`, id,
	).Scan(&name, &description, &visibility, &ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("group: load: %w", err)
	}

	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
		if name == "" {
			return nil, ErrEmptyName
		}
	}
	if in.Description != nil {
		description = strings.TrimSpace(*in.Description)
	}
	if in.Visibility != nil {
		if visibility, err = normalizeVisibility(*in.Visibility); err != nil {
			return nil, err
		}
	}
	if in.OwnerID != nil {
		ownerID = strings.TrimSpace(*in.OwnerID)
		if ownerID == "" {
			return nil, ErrOwnerRequired
		}
		ok, err := s.userExists(ctx, ownerID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrUserNotFound
		}
	}

	if _, err := s.db.ExecContext(ctx,
		`UPDATE resource_groups SET name = ?, description = ?, visibility = ?, owner_id = ?, updated_at = ? WHERE id = ?`,
		name, description, visibility, ownerID, time.Now().UTC().Format(time.RFC3339), id); err != nil {
		if store.IsUniqueErr(err) {
			return nil, ErrDuplicateName
		}
		return nil, fmt.Errorf("group: update: %w", err)
	}
	return s.Get(ctx, id)
}

// Delete 删组。组内仍有项目 / 服务器时返回 ErrGroupNotEmpty(见包头注释)。
func (s *Service) Delete(ctx context.Context, id string) error {
	var projects, servers int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM projects WHERE group_id = ?`, id).Scan(&projects); err != nil {
		return fmt.Errorf("group: count projects: %w", err)
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM servers WHERE group_id = ?`, id).Scan(&servers); err != nil {
		return fmt.Errorf("group: count servers: %w", err)
	}
	if projects > 0 || servers > 0 {
		return fmt.Errorf("%w:%d 个项目 / %d 台服务器仍在组内", ErrGroupNotEmpty, projects, servers)
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM resource_group_members WHERE group_id = ?`, id); err != nil {
		return fmt.Errorf("group: delete members: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM resource_groups WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("group: delete: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// AddMember 把用户加入名册(幂等)。组长本人不入名册:其身份已由 owner_id 授予全部权限。
func (s *Service) AddMember(ctx context.Context, groupID, userID string, addedBy string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ErrUserNotFound
	}
	ok, err := s.userExists(ctx, userID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrUserNotFound
	}
	var owner string
	if err := s.db.QueryRowContext(ctx,
		`SELECT owner_id FROM resource_groups WHERE id = ?`, groupID).Scan(&owner); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("group: load owner: %w", err)
	}
	if userID == owner {
		return nil
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO resource_group_members (group_id, user_id, created_at, created_by)
		 VALUES (?, ?, ?, ?)`,
		groupID, userID, time.Now().UTC().Format(time.RFC3339), addedBy); err != nil && !store.IsUniqueErr(err) {
		return fmt.Errorf("group: add member: %w", err)
	}
	return nil
}

// RemoveMember 把用户移出名册。不存在该成员时静默成功(幂等)。
func (s *Service) RemoveMember(ctx context.Context, groupID, userID string) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM resource_group_members WHERE group_id = ? AND user_id = ?`, groupID, userID); err != nil {
		return fmt.Errorf("group: remove member: %w", err)
	}
	return nil
}

// ─── access.Repository 实现 ────────────────────────────────────────────────────

// compile-time 断言:本包就是 internal/access 的仓储实现体。
var _ access.Repository = (*Service)(nil)

// GroupIDOf 返回资源所属分组 ID(” = 未归组)。资源不存在返回 ErrNotFound(→ 404),
// 不能返回 access.ErrForbidden——否则「项目不存在」会被伪装成「无权限」。
//
// KindRun 走 run → project 两跳:运行本身不持分组,它的权限完全随所属项目。
func (s *Service) GroupIDOf(ctx context.Context, kind access.Kind, id string) (string, error) {
	var q string
	switch kind {
	case access.KindProject:
		q = `SELECT group_id FROM projects WHERE id = ?`
	case access.KindServer:
		q = `SELECT group_id FROM servers WHERE id = ?`
	case access.KindRun:
		// 运行没有自己的分组,归属来自所属项目;项目行已删(留存策略清掉了)时 group_id
		// 是 NULL,按 fail closed 当作「资源不存在」,而不是退化成未归组让全员可读他人日志。
		q = `SELECT p.group_id FROM pipeline_runs r LEFT JOIN projects p ON p.id = r.project_id WHERE r.id = ?`
	default:
		return "", fmt.Errorf("group: 未知资源类型 %q", kind)
	}
	var groupID sql.NullString
	err := s.db.QueryRowContext(ctx, q, id).Scan(&groupID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err == nil && !groupID.Valid {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("group: resolve group of %s/%s: %w", kind, id, err)
	}
	return groupID.String, nil
}

// GroupSnapshot 取判定所需的最小快照。groupID 为空(未归组)返回 (nil, nil):
// access.Decide 把 nil 快照当作「公开」。
func (s *Service) GroupSnapshot(ctx context.Context, groupID string) (*access.Group, error) {
	if groupID == access.Ungrouped {
		return nil, nil
	}
	var visibility, ownerID string
	err := s.db.QueryRowContext(ctx,
		`SELECT visibility, owner_id FROM resource_groups WHERE id = ?`, groupID).
		Scan(&visibility, &ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		// 悬挂引用:交给 access 侧按最私有一档收敛,这里不静默放行。
		return nil, access.ErrGroupNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("group: snapshot: %w", err)
	}
	members, err := s.members(ctx, groupID)
	if err != nil {
		return nil, err
	}
	return &access.Group{ID: groupID, Visibility: visibility, OwnerID: ownerID, MemberIDs: members}, nil
}

// PublicAndJoinedGroupIDs 返回该用户可见的分组 ID(public + 自己当组长或在名册里的)。
// 不含未归组(”),那是调用方固定加上的白名单项。
func (s *Service) PublicAndJoinedGroupIDs(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT g.id FROM resource_groups g
		 WHERE g.visibility = ? OR g.owner_id = ? OR
		       EXISTS (SELECT 1 FROM resource_group_members m WHERE m.group_id = g.id AND m.user_id = ?)`,
		access.VisibilityPublic, userID, userID)
	if err != nil {
		return nil, fmt.Errorf("group: visible ids: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("group: scan visible id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
