// Package kube 是「Kubernetes 集群目标」的领域层:集群登记(CRUD)+ kubeconfig 解析 +
// 最小 API server 客户端。
//
// 它是「目标」这一概念的**第二条腿**:target 包管的是「一台能 SSH 的机器」,契约已冻结
// (Epic 4/6 都在消费),而一个集群不是一台机器 —— 没有 host/port/user,也没有 shell。
// 硬往 Server 上塞 platform 字段会让两条链路互相污染(容器页 / SSH 终端要开始防着集群行),
// 所以并列一个包,部署层按节点类型选一条腿走。
//
// 凭据纪律与 target 完全一致:kubeconfig 明文只在装配 HTTP 客户端的那几行存在于进程内,
// 用完即弃,绝不入库 / 日志 / 响应 / 错误体(AC-SEC-01)。
package kube

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/fuuhoo/pipewright/internal/access"
	"github.com/fuuhoo/pipewright/internal/store"
	"github.com/fuuhoo/pipewright/internal/vault"
	"github.com/google/uuid"
)

// 领域错误。错误体永不含 kubeconfig / token / 证书内容。
var (
	// ErrNotFound 表示集群不存在。
	ErrNotFound = errors.New("kube: cluster not found")
	// ErrEmptyName 表示集群名称为空。
	ErrEmptyName = errors.New("kube: name must not be empty")
	// ErrEmptyCredentialID 表示未选择 kubeconfig 凭据。
	ErrEmptyCredentialID = errors.New("kube: credential id must not be empty")
	// ErrCredentialNotFound 表示引用的凭据不存在。
	ErrCredentialNotFound = errors.New("kube: referenced credential not found")
	// ErrVaultUnconfigured 表示保险库未配置 master key。
	ErrVaultUnconfigured = errors.New("kube: vault unconfigured")
	// ErrBadNamespace 表示默认命名空间非法(k8s DNS-label:小写字母/数字/-,≤63)。
	ErrBadNamespace = errors.New("kube: invalid namespace")
	// ErrGroupNotFound 表示 group_id 指向的分组不存在(该列刻意不建外键,故在此校验)。
	ErrGroupNotFound = errors.New("kube: group not found")
)

// reNamespace 是 k8s 命名空间的 DNS-label 规则。
var reNamespace = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// Cluster 是集群目标的领域模型。持 credentialId 引用,不含任何明文凭据;
// API server 地址同样不入库 —— 它是 kubeconfig 的一部分(见 0059 迁移注释)。
type Cluster struct {
	ID           string
	Name         string
	CredentialID string
	// NamespaceDefault 是该集群的默认命名空间(部署任务未填时兜底;可为空)。
	NamespaceDefault string
	GroupID          string
	// CredentialName 是冗余只读展示名(join credentials);非持久列。
	CredentialName string
	// Endpoint 来自 kubeconfig,仅供列表/详情展示;非持久列,取不到时为空
	// (凭据被删/换成了非 kubeconfig 类型时不阻断列表)。
	Endpoint  string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CreateInput 是登记集群的入参。
type CreateInput struct {
	Name             string
	CredentialID     string
	NamespaceDefault string
	GroupID          string
}

// UpdateInput 是更新集群的入参;指针字段为 nil 表示不修改。
type UpdateInput struct {
	Name             *string
	CredentialID     *string
	NamespaceDefault *string
	GroupID          *string
}

// ListFilter 是集群列表的分组可见性过滤(零值 = 只见未归组,与 target 同约定)。
type ListFilter struct {
	Visible access.ListFilter
}

// TestResult 是「测试连接」的结果。Output/Err 绝不含 kubeconfig 或 token。
type TestResult struct {
	OK        bool
	LatencyMs int64
	Endpoint  string
	Output    string
	Err       string
}

// Service 是集群目标对外接口(供 httpapi 集群管理与 deploy 的 k8s 链路消费)。
type Service interface {
	Get(ctx context.Context, id string) (*Cluster, error)
	List(ctx context.Context) ([]*Cluster, error)
	ListScoped(ctx context.Context, f ListFilter) ([]*Cluster, error)
	Create(ctx context.Context, in CreateInput) (*Cluster, error)
	Update(ctx context.Context, id string, in UpdateInput) (*Cluster, error)
	Delete(ctx context.Context, id string) error
	// Test 真连一次 API server(GET /version);连接/认证失败 → ok=false + 人读原因。
	Test(ctx context.Context, id string) (*TestResult, error)
	// ClientFor 为指定集群装配 API server 客户端(取 kubeconfig 明文 → 解析 → 用完即弃)。
	// 定位类错误(集群/凭据不存在、保险库未配)上抛;kubeconfig 不可解析也上抛
	// —— 那属于「这个目标根本没法用」,部署必须停在开始执行之前。
	ClientFor(ctx context.Context, id string) (*Client, error)
}

type service struct {
	db    *sql.DB
	vault vault.Vault
}

// New 构造 Service。v 为 nil 或未配置 master key 时,取凭据的路径返回 ErrVaultUnconfigured。
func New(db *sql.DB, v vault.Vault) Service {
	return &service{db: db, vault: v}
}

func validateCreate(in CreateInput) error {
	if strings.TrimSpace(in.Name) == "" {
		return ErrEmptyName
	}
	if strings.TrimSpace(in.CredentialID) == "" {
		return ErrEmptyCredentialID
	}
	return validateNamespace(in.NamespaceDefault)
}

// validateNamespace 空值合法(不预设默认),非空必须是合法 DNS-label。
func validateNamespace(ns string) error {
	ns = strings.TrimSpace(ns)
	if ns == "" {
		return nil
	}
	if len(ns) > 63 || !reNamespace.MatchString(ns) {
		return fmt.Errorf("%w: %q(只允许小写字母、数字与 -,且首尾必须是字母或数字)", ErrBadNamespace, ns)
	}
	return nil
}

func (s *service) Create(ctx context.Context, in CreateInput) (*Cluster, error) {
	if err := validateCreate(in); err != nil {
		return nil, err
	}
	if err := s.checkKubeConfig(in.CredentialID); err != nil {
		return nil, err
	}
	groupID := strings.TrimSpace(in.GroupID)
	if groupID != "" {
		ok, err := s.groupExists(ctx, groupID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrGroupNotFound
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	id := uuid.NewString()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO kube_clusters (id, name, credential_id, namespace_default, group_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, strings.TrimSpace(in.Name), strings.TrimSpace(in.CredentialID), strings.TrimSpace(in.NamespaceDefault), groupID, now, now,
	)
	if err != nil {
		if store.IsForeignKeyErr(err) {
			return nil, ErrCredentialNotFound
		}
		return nil, fmt.Errorf("kube: insert: %w", err)
	}
	return s.Get(ctx, id)
}

// checkKubeConfig 在入库前确认这条凭据当得起「集群凭据」:能取到、能解析出地址与身份。
// 晚失败会表现为一次莫名其妙的「部署连不上」,而病根是三个月前粘错的一份文件。
func (s *service) checkKubeConfig(credentialID string) error {
	_, err := s.accessFor(strings.TrimSpace(credentialID))
	return err
}

func (s *service) accessFor(credentialID string) (clusterAccess, error) {
	if s.vault == nil {
		return clusterAccess{}, ErrVaultUnconfigured
	}
	raw, err := s.vault.Get(credentialID)
	if err != nil {
		switch {
		case errors.Is(err, vault.ErrVaultUnconfigured):
			return clusterAccess{}, ErrVaultUnconfigured
		case errors.Is(err, vault.ErrNotFound):
			return clusterAccess{}, ErrCredentialNotFound
		default:
			return clusterAccess{}, err
		}
	}
	a, perr := parseKubeConfig(raw)
	raw = "" // 明文用完即弃(错误体与后续链路都不再持有它)
	return a, perr
}

func (s *service) List(ctx context.Context) ([]*Cluster, error) {
	return s.ListScoped(ctx, ListFilter{Visible: access.ListFilter{Unrestricted: true}})
}

func (s *service) ListScoped(ctx context.Context, f ListFilter) ([]*Cluster, error) {
	query := `SELECT k.id, k.name, k.credential_id, k.namespace_default, k.group_id,
			        COALESCE(c.name, ''), k.created_at, k.updated_at
		 FROM kube_clusters k
		 LEFT JOIN credentials c ON c.id = k.credential_id`
	var args []any
	if clause, clauseArgs := f.Visible.Clause("k.group_id"); clause != "" {
		query += " WHERE " + clause
		args = clauseArgs
	}
	query += " ORDER BY k.created_at DESC, k.id"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("kube: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]*Cluster, 0)
	for rows.Next() {
		c, err := scanCluster(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("kube: iterate: %w", err)
	}
	// 地址是「顺带能显示」的信息,取不到不该让整页列表失败;逐个集群 best-effort 补上。
	for _, c := range out {
		if a, err := s.accessFor(c.CredentialID); err == nil {
			c.Endpoint = a.server
		}
	}
	return out, nil
}

func (s *service) Get(ctx context.Context, id string) (*Cluster, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT k.id, k.name, k.credential_id, k.namespace_default, k.group_id,
			        COALESCE(c.name, ''), k.created_at, k.updated_at
		 FROM kube_clusters k
		 LEFT JOIN credentials c ON c.id = k.credential_id
		 WHERE k.id = ?`, id)
	c, err := scanCluster(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if a, aerr := s.accessFor(c.CredentialID); aerr == nil {
		c.Endpoint = a.server
	}
	return c, nil
}

func (s *service) Update(ctx context.Context, id string, in UpdateInput) (*Cluster, error) {
	var name, credentialID, ns, groupID string
	err := s.db.QueryRowContext(ctx,
		`SELECT name, credential_id, namespace_default, group_id FROM kube_clusters WHERE id = ?`, id,
	).Scan(&name, &credentialID, &ns, &groupID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("kube: load: %w", err)
	}
	if in.Name != nil {
		if strings.TrimSpace(*in.Name) == "" {
			return nil, ErrEmptyName
		}
		name = strings.TrimSpace(*in.Name)
	}
	if in.NamespaceDefault != nil {
		if verr := validateNamespace(*in.NamespaceDefault); verr != nil {
			return nil, verr
		}
		ns = strings.TrimSpace(*in.NamespaceDefault)
	}
	if in.CredentialID != nil {
		next := strings.TrimSpace(*in.CredentialID)
		if next == "" {
			return nil, ErrEmptyCredentialID
		}
		if next != credentialID {
			if cerr := s.checkKubeConfig(next); cerr != nil {
				return nil, cerr
			}
			credentialID = next
		}
	}
	if in.GroupID != nil {
		groupID = strings.TrimSpace(*in.GroupID)
		if groupID != "" {
			ok, gerr := s.groupExists(ctx, groupID)
			if gerr != nil {
				return nil, gerr
			}
			if !ok {
				return nil, ErrGroupNotFound
			}
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx,
		`UPDATE kube_clusters SET name = ?, credential_id = ?, namespace_default = ?, group_id = ?, updated_at = ? WHERE id = ?`,
		name, credentialID, ns, groupID, now, id)
	if err != nil {
		if store.IsForeignKeyErr(err) {
			return nil, ErrCredentialNotFound
		}
		return nil, fmt.Errorf("kube: update: %w", err)
	}
	return s.Get(ctx, id)
}

func (s *service) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM kube_clusters WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("kube: delete: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *service) groupExists(ctx context.Context, groupID string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM resource_groups WHERE id = ?`, groupID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("kube: check group: %w", err)
	}
	return true, nil
}

func (s *service) ClientFor(ctx context.Context, id string) (*Client, error) {
	c, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	a, err := s.accessFor(c.CredentialID)
	if err != nil {
		return nil, err
	}
	return newClient(a), nil
}

func (s *service) Test(ctx context.Context, id string) (*TestResult, error) {
	start := time.Now()
	client, err := s.ClientFor(ctx, id)
	if err != nil {
		// 定位/kubeconfig 不可解析:上抛,让 HTTP 层映射 404/422/503(不是「连不上」)。
		return nil, err
	}
	version, err := client.Version(ctx)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return &TestResult{OK: false, LatencyMs: latency, Endpoint: client.Endpoint(), Err: humanError(err)}, nil
	}
	return &TestResult{OK: true, LatencyMs: latency, Endpoint: client.Endpoint(), Output: version}, nil
}

// humanError 把客户端错误折成人读文案(与 target 同风格;绝不含凭据)。
func humanError(err error) string {
	switch {
	case errors.Is(err, ErrDenied):
		return "API server 拒绝了请求:凭据无效,或该 ServiceAccount 没有相应权限"
	case errors.Is(err, ErrUnreachable):
		return "无法连接 API server:地址不可达、证书不受信任或超时"
	case errors.Is(err, ErrKubeConfigInvalid):
		return "凭据里的 kubeconfig 不可用"
	case errors.Is(err, ErrWorkloadNotFound):
		return "目标资源不存在"
	default:
		return "连接失败"
	}
}

// scanner 抽象 *sql.Row 与 *sql.Rows。
type scanner interface {
	Scan(dest ...any) error
}

func scanCluster(sc scanner) (*Cluster, error) {
	var c Cluster
	var createdStr, updatedStr string
	if err := sc.Scan(&c.ID, &c.Name, &c.CredentialID, &c.NamespaceDefault, &c.GroupID,
		&c.CredentialName, &createdStr, &updatedStr); err != nil {
		return nil, err
	}
	created, err := time.Parse(time.RFC3339, createdStr)
	if err != nil {
		return nil, fmt.Errorf("kube: parse created_at: %w", err)
	}
	updated, err := time.Parse(time.RFC3339, updatedStr)
	if err != nil {
		return nil, fmt.Errorf("kube: parse updated_at: %w", err)
	}
	c.CreatedAt, c.UpdatedAt = created, updated
	return &c, nil
}
