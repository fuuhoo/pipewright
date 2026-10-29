package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fuuhoo/pipewright/internal/access"
	"github.com/fuuhoo/pipewright/internal/pipeline"
	"github.com/fuuhoo/pipewright/internal/vault"
	"github.com/google/uuid"
)

// 变量组领域错误。错误体不含任何明文 secret。
var (
	// ErrVarGroupNotFound 表示引用的变量组不存在。
	ErrVarGroupNotFound = errors.New("library: variable group not found")
	// ErrInvalidVarGroup 表示变量组校验失败(名称空)。
	ErrInvalidVarGroup = errors.New("library: invalid variable group")
	// ErrVarGroupNameTaken 表示变量组名已被占用(唯一约束)。
	ErrVarGroupNameTaken = errors.New("library: variable group name already in use")
	// ErrGroupNotFound 表示 groupIds 指向不存在的资源分组(引用悬挂)。
	ErrGroupNotFound = errors.New("library: resource group not found")
)

// 透传 pipeline 的变量校验错误(供 handler 统一映射;变量组复用 settings.normalizeVars 语义)。
var (
	// ErrInvalidVar 表示变量校验失败(key 空 / 组内重复 / secret 缺 credentialId)。
	ErrInvalidVar = pipeline.ErrInvalidVar
	// ErrCredentialNotFound 表示 secret 引用的 credentialId 不存在于保险库。
	ErrCredentialNotFound = pipeline.ErrCredentialNotFound
	// ErrVaultUnconfigured 表示保险库未配置但存在 secret 引用,无法校验/掩码。
	ErrVaultUnconfigured = pipeline.ErrSettingsVaultUnconfigured
)

// VarGroup 是一份命名、可复用的变量集合(镜像每流水线变量模型)。Vars 复用 pipeline.BuildVar:
// 非 secret 存明文 Value;secret 只存 CredentialID 引用(明文绝不入库),MaskedValue 仅响应里回算。
//
// GroupIDs 是所属资源分组(variable_group_groups,可多个,按 ID 升序);空 = 未归组 ——
// 存量变量组都是这个状态,全员可见可改。归属只收窄「谁能看见 / 谁能改这一份」,
// 流水线引用它跑构建不受影响(执行在后端,与凭据引用同一口径)。
type VarGroup struct {
	ID          string
	Name        string
	Description string
	Vars        []pipeline.BuildVar
	GroupIDs    []string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// VarGroupInput 是创建/更新变量组的入参(secret 项只传 credentialId,不传明文/掩码)。
//
// GroupIDs 用指针区分「不改」与「清空」:nil = 不动(建组时等价于未归组);
// 空切片 = 移出所有分组。与服务器归属同一条口径,免得只发名称/变量的调用方把归属顺手清掉。
type VarGroupInput struct {
	Name        string
	Description string
	Vars        []pipeline.BuildVar
	GroupIDs    *[]string
}

// VarGroupService 定义变量组领域对外接口。
type VarGroupService interface {
	// List 返回 visible 范围内的变量组(按名称升序;secret 项回掩码)。
	List(ctx context.Context, visible access.ListFilter) ([]VarGroup, error)
	// Get 返回单个变量组(secret 项回掩码);不存在 → ErrVarGroupNotFound。
	Get(ctx context.Context, id string) (*VarGroup, error)
	// Create 校验(名称非空 + 变量经 pipeline.NormalizeVars:key 非空不重复、secret 引用存在性)→
	// 持久化(只存引用,绝无明文 secret)→ 回读(回掩码)。名称重复 → ErrVarGroupNameTaken。
	Create(ctx context.Context, in VarGroupInput) (*VarGroup, error)
	// Update 同 Create 的校验/持久化,按 id 覆盖;不存在 → ErrVarGroupNotFound。
	Update(ctx context.Context, id string, in VarGroupInput) (*VarGroup, error)
	// Delete 删除变量组(连同归属边);不存在 → ErrVarGroupNotFound。
	Delete(ctx context.Context, id string) error
}

type varGroupService struct {
	db    *sql.DB
	vault vault.Vault
}

// NewVarGroupService 构造 VarGroupService。
//   - db:经参数化 SQL 触库 + 回算掩码。
//   - v:凭据保险库,校验 secret 引用存在性;nil/未配置且存在 secret 引用 → ErrVaultUnconfigured。
//
// 不在此做任何重活(无 init() 副作用,避免抬高空载内存)。
func NewVarGroupService(db *sql.DB, v vault.Vault) VarGroupService {
	return &varGroupService{db: db, vault: v}
}

func (s *varGroupService) List(ctx context.Context, visible access.ListFilter) ([]VarGroup, error) {
	query := `SELECT vg.id, vg.name, vg.description, vg.vars_json, vg.created_at, vg.updated_at
		 FROM variable_groups vg`
	var args []any
	// 归属可多个:「未归组」是成员表里没有这一行,不是列值 ''(见 access.MemberClause)。
	if clause, clauseArgs := visible.MemberClause("vg.id", "variable_group_groups", "variable_group_id", "group_id"); clause != "" {
		query += " WHERE " + clause
		args = clauseArgs
	}
	query += " ORDER BY vg.name ASC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("library: list variable groups: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// 先把所有行扫完(不在持有 rows 的同时再查库:单连接 SQLite 会自死锁)。
	out := make([]VarGroup, 0)
	for rows.Next() {
		g, err := scanGroupRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("library: iterate variable groups: %w", err)
	}
	_ = rows.Close()

	if err := s.attachGroups(ctx, out); err != nil {
		return nil, err
	}
	// rows 关闭后再回算掩码(每个 secret 引用单独查 credentials.masked_value)。
	for i := range out {
		if err := pipeline.ApplyVarMasks(ctx, s.db, out[i].Vars); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *varGroupService) Get(ctx context.Context, id string) (*VarGroup, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, description, vars_json, created_at, updated_at
		 FROM variable_groups WHERE id = ?`, strings.TrimSpace(id))
	g, err := scanGroupRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrVarGroupNotFound
	}
	if err != nil {
		return nil, err
	}
	one := []VarGroup{*g}
	if err := s.attachGroups(ctx, one); err != nil {
		return nil, err
	}
	*g = one[0]
	if err := pipeline.ApplyVarMasks(ctx, s.db, g.Vars); err != nil {
		return nil, err
	}
	return g, nil
}

func (s *varGroupService) Create(ctx context.Context, in VarGroupInput) (*VarGroup, error) {
	name, varsJSON, err := s.validate(in)
	if err != nil {
		return nil, err
	}
	groupIDs := NormalizeGroupIDs(in.GroupIDs)
	if err := s.validateGroups(ctx, groupIDs); err != nil {
		return nil, err
	}

	id := uuid.NewString()
	nowStr := time.Now().UTC().Format(time.RFC3339)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("library: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO variable_groups (id, name, description, vars_json, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, name, strings.TrimSpace(in.Description), varsJSON, nowStr, nowStr,
	); err != nil {
		if isUniqueErr(err) {
			return nil, ErrVarGroupNameTaken
		}
		return nil, fmt.Errorf("library: insert variable group: %w", err)
	}
	if err := insertGroups(ctx, tx, id, groupIDs, nowStr); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("library: commit: %w", err)
	}
	return s.Get(ctx, id)
}

func (s *varGroupService) Update(ctx context.Context, id string, in VarGroupInput) (*VarGroup, error) {
	id = strings.TrimSpace(id)
	// 先确认存在(给明确 404,而非静默 0 行)。
	if _, err := s.Get(ctx, id); err != nil {
		return nil, err
	}
	name, varsJSON, err := s.validate(in)
	if err != nil {
		return nil, err
	}
	var groupIDs []string
	if in.GroupIDs != nil {
		groupIDs = NormalizeGroupIDs(in.GroupIDs)
		if err := s.validateGroups(ctx, groupIDs); err != nil {
			return nil, err
		}
	}

	nowStr := time.Now().UTC().Format(time.RFC3339)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("library: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`UPDATE variable_groups SET name = ?, description = ?, vars_json = ?, updated_at = ?
		 WHERE id = ?`,
		name, strings.TrimSpace(in.Description), varsJSON, nowStr, id,
	); err != nil {
		if isUniqueErr(err) {
			return nil, ErrVarGroupNameTaken
		}
		return nil, fmt.Errorf("library: update variable group: %w", err)
	}
	if in.GroupIDs != nil {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM variable_group_groups WHERE variable_group_id = ?`, id); err != nil {
			return nil, fmt.Errorf("library: clear variable group groups: %w", err)
		}
		if err := insertGroups(ctx, tx, id, groupIDs, nowStr); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("library: commit: %w", err)
	}
	return s.Get(ctx, id)
}

func (s *varGroupService) Delete(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("library: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM variable_group_groups WHERE variable_group_id = ?`, id); err != nil {
		return fmt.Errorf("library: delete variable group groups: %w", err)
	}
	res, err := tx.ExecContext(ctx,
		`DELETE FROM variable_groups WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("library: delete variable group: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("library: delete variable group rows: %w", err)
	}
	if n == 0 {
		return ErrVarGroupNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("library: commit: %w", err)
	}
	return nil
}

// NormalizeGroupIDs 统一归属集合的形态:去空白、丢空项、去重并按 ID 升序。
// 排序是给读路径一个确定顺序(前端标签、审计 detail 不会因为插入顺序不同而抖动)。
// HTTP 层也用它,好让「能不能放进这些组」的判定与真正入库的集合完全一致。
func NormalizeGroupIDs(in *[]string) []string {
	if in == nil || len(*in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(*in))
	out := make([]string, 0, len(*in))
	for _, raw := range *in {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// validateGroups 校验每个分组 ID 都存在(variable_group_groups 无外键,故在此挡悬挂引用)。
func (s *varGroupService) validateGroups(ctx context.Context, groupIDs []string) error {
	if len(groupIDs) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(groupIDs)), ",")
	args := make([]any, 0, len(groupIDs))
	for _, id := range groupIDs {
		args = append(args, id)
	}
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM resource_groups WHERE id IN (`+placeholders+`)`, args...).Scan(&n); err != nil {
		return fmt.Errorf("library: check groups: %w", err)
	}
	if n != len(groupIDs) {
		return ErrGroupNotFound
	}
	return nil
}

// insertGroups 写入变量组的归属边(调用方负责事务)。
func insertGroups(ctx context.Context, tx *sql.Tx, varGroupID string, groupIDs []string, now string) error {
	for _, groupID := range groupIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO variable_group_groups (variable_group_id, group_id, created_at)
			 VALUES (?, ?, ?)`, varGroupID, groupID, now); err != nil {
			return fmt.Errorf("library: insert variable group group: %w", err)
		}
	}
	return nil
}

// attachGroups 批量补齐 GroupIDs(列表页不让每行再查一次库)。
func (s *varGroupService) attachGroups(ctx context.Context, groups []VarGroup) error {
	if len(groups) == 0 {
		return nil
	}
	ids := make([]any, 0, len(groups))
	byID := make(map[string]*VarGroup, len(groups))
	for i := range groups {
		groups[i].GroupIDs = []string{}
		ids = append(ids, groups[i].ID)
		byID[groups[i].ID] = &groups[i]
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	rows, err := s.db.QueryContext(ctx,
		`SELECT variable_group_id, group_id FROM variable_group_groups
		 WHERE variable_group_id IN (`+placeholders+`)
		 ORDER BY variable_group_id, group_id`, ids...)
	if err != nil {
		return fmt.Errorf("library: load variable group groups: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var vgID, groupID string
		if err := rows.Scan(&vgID, &groupID); err != nil {
			return fmt.Errorf("library: scan variable group group: %w", err)
		}
		if g, ok := byID[vgID]; ok {
			g.GroupIDs = append(g.GroupIDs, groupID)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("library: iterate variable group groups: %w", err)
	}
	return nil
}

// validate 校验名称 + 经 pipeline.NormalizeVars 规范化变量(key 非空不重复;secret 引用存在性),
// 返回 trim 后名称 + 持久化形状 JSON(剥离掩码;明文 secret 从不入库)。
func (s *varGroupService) validate(in VarGroupInput) (string, string, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return "", "", fmt.Errorf("%w: name must not be empty", ErrInvalidVarGroup)
	}
	vars, err := pipeline.NormalizeVars(s.vault, in.Vars)
	if err != nil {
		return "", "", err
	}
	varsJSON, err := pipeline.ToStoredVarsJSON(vars)
	if err != nil {
		return "", "", fmt.Errorf("library: marshal variable group vars: %w", err)
	}
	return name, string(varsJSON), nil
}

// scanGroupRow 把一行扫成 VarGroup(纯扫描,不回算掩码——掩码须在 rows 关闭后做,
// 否则单连接 SQLite 会因「持有游标时再查库」自死锁)。
func scanGroupRow(row rowScanner) (*VarGroup, error) {
	var (
		id, name, desc, varsJSON string
		createdStr, updatedStr   string
	)
	if err := row.Scan(&id, &name, &desc, &varsJSON, &createdStr, &updatedStr); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("library: scan variable group: %w", err)
	}

	var vars []pipeline.BuildVar
	if strings.TrimSpace(varsJSON) != "" {
		if err := json.Unmarshal([]byte(varsJSON), &vars); err != nil {
			return nil, fmt.Errorf("library: parse variable group vars: %w", err)
		}
	}
	if vars == nil {
		vars = []pipeline.BuildVar{}
	}

	created, _ := time.Parse(time.RFC3339, createdStr)
	updated, _ := time.Parse(time.RFC3339, updatedStr)
	return &VarGroup{
		ID:          id,
		Name:        name,
		Description: desc,
		Vars:        vars,
		CreatedAt:   created,
		UpdatedAt:   updated,
	}, nil
}
