// Package project 是「被纳管代码仓库」的领域层。
//
// 一个项目按引用绑定一个仓库凭据:DB 中只存 credential_id,绝无任何明文/令牌
// (AC-SEC-01 / FR-3)。创建/测试连接时,平台用所选凭据对仓库做克隆连通校验
// (go-git ListRemote = ls-remote 语义,纯 Go,不要求宿主装 git);校验所需的
// token 经 vault.Get(credentialId) 在进程内取明文,用完即弃,绝不入库/日志/响应/错误体。
//
// 校验失败统一映射为干净的领域错误(ErrCredentialError / ErrRepoUnreachable /
// ErrVaultUnconfigured),其文本与 %w 链中绝不含明文、凭据、URL 中的密钥或内部栈。
package project

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
	"github.com/huangchengsir/pipewright/internal/vault"
)

// 领域错误。错误体永不含明文/凭据/master key/内部栈。
var (
	// ErrNotFound 表示项目不存在。
	ErrNotFound = errors.New("project: not found")
	// ErrEmptyName 表示项目名称为空。
	ErrEmptyName = errors.New("project: name must not be empty")
	// ErrEmptyRepoURL 表示仓库地址为空(仅在需要仓库的操作上适用,如测试连接)。
	ErrEmptyRepoURL = errors.New("project: repo url must not be empty")
	// ErrCredentialWithoutRepo 表示给了凭据却没给仓库地址:没仓库可访问却挂着凭据,
	// 会让人误以为这把凭据参与了什么(纯发布项目应当两样都留空)。
	ErrCredentialWithoutRepo = errors.New("project: credential id requires a repo url")
	// ErrRepoRequired 表示这项设置离不开项目仓库(如流水线即代码、PR 状态回写),
	// 而项目是纯发布用的(没绑仓库)。
	ErrRepoRequired = errors.New("project: repo url required")
	// ErrEmptyCredentialID 表示未选择凭据。
	ErrEmptyCredentialID = errors.New("project: credential id must not be empty")
	// ErrCredentialError 表示凭据无效/缺失/无权限(克隆鉴权失败)。
	ErrCredentialError = errors.New("project: credential error")
	// ErrRepoUnreachable 表示仓库地址不可达/不存在/网络错误。
	ErrRepoUnreachable = errors.New("project: repo unreachable")
	// ErrVaultUnconfigured 表示保险库未配置 master key,无法取凭据做校验。
	ErrVaultUnconfigured = errors.New("project: vault unconfigured")
	// ErrCredentialNotFound 表示引用的凭据不存在(下拉项已被删除等)。
	ErrCredentialNotFound = errors.New("project: referenced credential not found")
	// ErrProjectHasActiveRuns 表示项目有进行中的运行(queued/running),不可删除。
	ErrProjectHasActiveRuns = errors.New("project: has active runs")
	// ErrGroupNotFound 表示 group_id 指向的分组不存在。projects.group_id 有意不建外键
	// (见迁移 0056),所以这里必须自己挡:悬挂引用会让 access 按最私有一档收敛,
	// 结果是全组人都打不开这个项目。
	ErrGroupNotFound = errors.New("project: group not found")
)

// 分页上限常量:防全量返回拖垮内存/响应。
const (
	// DefaultPageSize 是 List 默认每页条数。
	DefaultPageSize = 50
	// MaxPageSize 是单页硬上限(请求 pageSize 超过则收敛到此)。
	MaxPageSize = 500
)

// Project 是项目领域模型。绝不含凭据明文,只持 credentialId 引用。
type Project struct {
	ID            string
	Name          string
	RepoURL       string
	DefaultBranch string
	CredentialID  string
	// CredentialName 是冗余只读展示名(join credentials),便于列表展示;非持久列。
	CredentialName string
	// PacEnabled 是「流水线即代码」开关(GitOps · FR-8-12):开启后运行时按运行分支读仓库根
	// .pipewright.yml 驱动该 run(缺失/非法回退库内配置)。默认 false。
	PacEnabled bool
	// PRStatusEnabled 是「PR 状态检查」开关(Story 8-9 / FR-8-9):开启后 run 终态据项目仓库识别
	// GitHub/Gitee,经项目凭据回写该 commit 的提交状态(PR 检查)。默认 false。
	PRStatusEnabled bool
	// GroupID 是所属资源分组(resource_groups.id);'' = 未归组 = 全员可见可操作(存量即此态)。
	// 权限判定见 internal/access + internal/group。
	GroupID   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CreateInput 是创建项目的入参。
type CreateInput struct {
	Name          string
	RepoURL       string
	CredentialID  string
	DefaultBranch string // 可空;为空时由 ls-remote 探测远端 HEAD 填充
	// GroupID 可空('' = 未归组)。归组权限(能否放进这个组)由 HTTP 层校验,
	// 本包只管落库——分组是否存在不该由仓库连通性校验来回答。
	GroupID string
}

// UpdateInput 是更新项目的入参;指针字段为 nil 表示不修改。
type UpdateInput struct {
	Name          *string
	DefaultBranch *string
	CredentialID  *string
	// RepoURL 改绑仓库;指向 "" 表示解绑(项目退化为纯发布用)。解绑时凭据一并清空,
	// 否则会留下一把没人访问的凭据引用。
	RepoURL *string
	// PacEnabled 切换「流水线即代码」开关;nil 表示不修改。
	PacEnabled *bool
	// PRStatusEnabled 切换「PR 状态检查」开关;nil 表示不修改。
	PRStatusEnabled *bool
	// GroupID 改归属;nil = 不动,指向 '' = 移出分组。归组要求两侧的 Manage,
	// 由 HTTP 层把关(access.ActManage on 旧组与新组)。
	GroupID *string
}

// TestCloneResult 是测试连接的结果(成功时携探测到的默认分支与远端引用列表)。
type TestCloneResult struct {
	DefaultBranch string
	// Branches/Tags 是远端全部分支/tag 短名(供前端「新建项目」弹窗默认分支下拉)。
	Branches []string
	Tags     []string
}

// ListResult 是分页列表结果(契约可携带分页元信息;DTO 兼容)。
type ListResult struct {
	Items    []Project
	Page     int
	PageSize int
	Total    int
}

// RemoteProber 抽象「用凭据明文对仓库做 ls-remote 校验」的能力。
// 注入便于测试(可用 stub 替换真实网络探测)。返回干净领域错误。
type RemoteProber interface {
	// Probe 用 token 对 repoURL 做 ListRemote(HTTPS + token auth),
	// 成功返回远端默认分支(可能为空字符串);失败返回 ErrCredentialError /
	// ErrRepoUnreachable(绝不含明文/凭据)。
	Probe(ctx context.Context, repoURL, username, token string) (defaultBranch string, err error)
}

// RefProber 是 RemoteProber 的可选扩展:探测时额外带回全部分支/tag 短名
// (供前端「新建项目」弹窗的默认分支下拉)。TestClone 优先使用;prober 未实现时
// 自动回退到 Probe(列表为空)。
type RefProber interface {
	ProbeRefs(ctx context.Context, repoURL, username, token string) (defaultBranch string, branches, tags []string, err error)
}

// Service 定义项目领域对外接口。
type Service interface {
	// Create 先用引用凭据做 ls-remote 校验,成功才入库;返回项目视图(无明文)。
	Create(ctx context.Context, in CreateInput) (*Project, error)
	// List 返回第 1 页项目(硬上限 MaxPageSize;join credentials 取展示名;无明文)。
	// visible 是分组可见性过滤(access.Service.VisibleGroups 的产物);
	// 传零值 ListFilter{} 即「GroupIDs=[未归组]」,会把已归组项目挡在列表外,
	// 管理员/不受限请传 ListFilter{Unrestricted: true}。
	List(ctx context.Context, visible access.ListFilter) ([]Project, error)
	// ListPaged 按页返回项目(page 从 1 起;pageSize<=0 用默认;超过 MaxPageSize 收敛),
	// 并返回总数供前端分页。total 同样受 visible 约束——否则前端会按全量算页数。
	ListPaged(ctx context.Context, page, pageSize int, visible access.ListFilter) (*ListResult, error)
	// Get 返回单个项目(无明文)。
	Get(ctx context.Context, id string) (*Project, error)
	// Update 改名/改默认分支/改绑凭据;改绑凭据时重做 ls-remote 校验。
	Update(ctx context.Context, id string, in UpdateInput) (*Project, error)
	// Delete 删除项目及其引用关系。
	Delete(ctx context.Context, id string) error
	// TestClone 用所选凭据对仓库做 ls-remote 校验(不落库)。
	TestClone(ctx context.Context, repoURL, credentialID string) (*TestCloneResult, error)
}

// service 是 store + vault 支撑的 Service 实现。
type service struct {
	db     *sql.DB
	vault  vault.Vault
	prober RemoteProber
}

// New 构造 Service。
//   - db:仅供本包通过参数化 SQL 触库(不与 store 内部状态耦合)。
//   - v:凭据保险库;v 为 nil 或未配置 master key 时,克隆校验返回 ErrVaultUnconfigured。
//   - prober:远端探测器;为 nil 时使用默认 go-git ListRemote 实现。
//
// 不在此做任何重活(无 init() 副作用,避免抬高空载内存)。
func New(db *sql.DB, v vault.Vault, prober RemoteProber) Service {
	if prober == nil {
		prober = goGitProber{}
	}
	return &service{db: db, vault: v, prober: prober}
}

// validateCreate 校验创建入参的必填项。
//
// 仓库地址可以留空:这种项目只用来发布(产物/镜像从别处来,不进 CI 工作区)。
// 留空时凭据也必须留空 —— 没有仓库要访问,挂着一把凭据只会让人以为它起作用。
func validateCreate(in CreateInput) error {
	if in.Name == "" {
		return ErrEmptyName
	}
	if in.RepoURL == "" {
		if in.CredentialID != "" {
			return ErrCredentialWithoutRepo
		}
		return nil
	}
	if in.CredentialID == "" {
		return ErrEmptyCredentialID
	}
	return nil
}

// groupExists 校验 resource_groups.id 存在。projects.group_id 没有外键(迁移 0056 说明了
// 为什么不建),所以写入前必须自己挡一次,否则会留下悬挂引用。
func (s *service) groupExists(ctx context.Context, groupID string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM resource_groups WHERE id = ?`, groupID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("project: check group: %w", err)
	}
	return true, nil
}

// gitAuthOrErr 取凭据明文并映射干净领域错误;token 仅进程内存在。
// 凭据不存在 → ErrCredentialNotFound;保险库未配置 → ErrVaultUnconfigured。
func (s *service) gitAuthOrErr(credentialID string) (vault.GitAuth, error) {
	if s.vault == nil {
		return vault.GitAuth{}, ErrVaultUnconfigured
	}
	auth, err := s.vault.GetGitAuth(credentialID)
	if err != nil {
		switch {
		case errors.Is(err, vault.ErrVaultUnconfigured):
			return vault.GitAuth{}, ErrVaultUnconfigured
		case errors.Is(err, vault.ErrNotFound):
			return vault.GitAuth{}, ErrCredentialNotFound
		default:
			// 解密等内部错误:不泄漏细节,统一按凭据错误对待。
			return vault.GitAuth{}, ErrCredentialError
		}
	}
	return auth, nil
}

// probe 取凭据明文并对仓库做 ls-remote 校验;token 仅进程内存在,用完即弃。
func (s *service) probe(ctx context.Context, repoURL, credentialID string) (string, error) {
	auth, err := s.gitAuthOrErr(credentialID)
	if err != nil {
		return "", err
	}
	token := auth.Token
	branch, perr := s.prober.Probe(ctx, repoURL, auth.Username, token)
	token = "" // 显式清引用,尽早不可达(明文不留)
	_ = token
	return branch, perr
}

// probeRefs 与 probe 相同,但 prober 实现 RefProber 时额外带回分支/tag 列表;
// 未实现时回退 Probe(列表为 nil,handler 侧保证输出 [])。
func (s *service) probeRefs(ctx context.Context, repoURL, credentialID string) (string, []string, []string, error) {
	auth, err := s.gitAuthOrErr(credentialID)
	if err != nil {
		return "", nil, nil, err
	}
	token := auth.Token
	defer func() { token = "" }() // 显式清引用,尽早不可达(明文不留)
	if rp, ok := s.prober.(RefProber); ok {
		return rp.ProbeRefs(ctx, repoURL, auth.Username, token)
	}
	branch, perr := s.prober.Probe(ctx, repoURL, auth.Username, token)
	return branch, nil, nil, perr
}

func (s *service) Create(ctx context.Context, in CreateInput) (*Project, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.RepoURL = strings.TrimSpace(in.RepoURL)
	in.CredentialID = strings.TrimSpace(in.CredentialID)
	in.DefaultBranch = strings.TrimSpace(in.DefaultBranch)
	if err := validateCreate(in); err != nil {
		return nil, err
	}

	// 归组标记先归一化空白,校验与入库用同一个值。
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
	// 无仓库项目(纯发布)不探测:既没有地址可连,也就不该因为网络失败而建不出来。
	var branch string
	if in.RepoURL != "" {
		var perr error
		branch, perr = s.probe(ctx, in.RepoURL, in.CredentialID)
		if perr != nil {
			return nil, perr
		}
	}
	defaultBranch := in.DefaultBranch
	if defaultBranch == "" {
		defaultBranch = branch // ls-remote 探测到的远端 HEAD(可能为空)
	}

	id := uuid.NewString()
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO projects (id, name, repo_url, default_branch, credential_id, group_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, in.Name, in.RepoURL, defaultBranch, credArg(in.CredentialID), groupID, nowStr, nowStr,
	)
	if err != nil {
		// 外键失败(凭据在校验后被删等竞态)归为凭据不存在;其余为内部错误。
		if isForeignKeyErr(err) {
			return nil, ErrCredentialNotFound
		}
		return nil, fmt.Errorf("project: insert: %w", err)
	}

	return s.Get(ctx, id)
}

func (s *service) List(ctx context.Context, visible access.ListFilter) ([]Project, error) {
	res, err := s.ListPaged(ctx, 1, MaxPageSize, visible)
	if err != nil {
		return nil, err
	}
	return res.Items, nil
}

// normalizePage 归一分页参数:page<1→1;pageSize<=0→默认;pageSize>上限→上限。
func normalizePage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = DefaultPageSize
	}
	if pageSize > MaxPageSize {
		pageSize = MaxPageSize
	}
	return page, pageSize
}

func (s *service) ListPaged(ctx context.Context, page, pageSize int, visible access.ListFilter) (*ListResult, error) {
	page, pageSize = normalizePage(page, pageSize)

	// 分组可见性条件同时作用于 COUNT 与列表,否则分页总数会把别人的项目算进来。
	where, whereArgs := visible.Clause("p.group_id")

	var total int
	countSQL := `SELECT COUNT(1) FROM projects p`
	if where != "" {
		countSQL += " WHERE " + where
	}
	if err := s.db.QueryRowContext(ctx, countSQL, whereArgs...).Scan(&total); err != nil {
		return nil, fmt.Errorf("project: count: %w", err)
	}

	offset := (page - 1) * pageSize
	listSQL := `SELECT p.id, p.name, p.repo_url, p.default_branch, COALESCE(p.credential_id, ''),
	        COALESCE(c.name, ''), p.pac_enabled, p.pr_status_enabled, p.group_id, p.created_at, p.updated_at
		 FROM projects p
		 LEFT JOIN credentials c ON c.id = p.credential_id`
	if where != "" {
		listSQL += " WHERE " + where
	}
	listSQL += " ORDER BY p.created_at DESC, p.id LIMIT ? OFFSET ?"
	listArgs := append(append([]any{}, whereArgs...), pageSize, offset)
	rows, err := s.db.QueryContext(ctx, listSQL, listArgs...)
	if err != nil {
		return nil, fmt.Errorf("project: list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]Project, 0)
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("project: iterate: %w", err)
	}
	return &ListResult{Items: out, Page: page, PageSize: pageSize, Total: total}, nil
}

func (s *service) Get(ctx context.Context, id string) (*Project, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT p.id, p.name, p.repo_url, p.default_branch, COALESCE(p.credential_id, ''),
		        COALESCE(c.name, ''), p.pac_enabled, p.pr_status_enabled, p.group_id, p.created_at, p.updated_at
		 FROM projects p
		 LEFT JOIN credentials c ON c.id = p.credential_id
		 WHERE p.id = ?`, id,
	)
	p, err := scanProject(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return p, nil
}

func (s *service) Update(ctx context.Context, id string, in UpdateInput) (*Project, error) {
	// 先取当前行(需 repo_url + credential_id 以便在改绑凭据时重做校验)。
	var name, repoURL, defaultBranch, credentialID, groupID string
	var pacInt, prStatusInt int
	err := s.db.QueryRowContext(ctx,
		`SELECT name, repo_url, default_branch, COALESCE(credential_id, ''), pac_enabled, pr_status_enabled, group_id FROM projects WHERE id = ?`, id,
	).Scan(&name, &repoURL, &defaultBranch, &credentialID, &pacInt, &prStatusInt, &groupID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("project: load: %w", err)
	}
	pacEnabled := pacInt != 0
	prStatusEnabled := prStatusInt != 0

	if in.Name != nil {
		if *in.Name == "" {
			return nil, ErrEmptyName
		}
		name = strings.TrimSpace(*in.Name)
	}
	if in.DefaultBranch != nil {
		defaultBranch = strings.TrimSpace(*in.DefaultBranch)
	}
	var repoTouched bool
	if in.RepoURL != nil {
		repoTouched = true
		next := strings.TrimSpace(*in.RepoURL)
		if next == "" {
			// 解绑仓库:凭据引用与「读仓库」的两个开关一并撤掉,否则留着的就是
			// 一把没人访问的凭据 + 两个永远失败的开关。
			repoURL, credentialID = "", ""
			defaultBranch = ""
			pacEnabled, prStatusEnabled = false, false
		} else {
			cred := credentialID
			if in.CredentialID != nil {
				cred = strings.TrimSpace(*in.CredentialID)
			}
			if cred == "" {
				return nil, ErrEmptyCredentialID
			}
			// 换/首次绑仓库:新地址必须探得通,探不通就不改(不给项目留个坏引用)。
			remoteBranch, perr := s.probe(ctx, next, cred)
			if perr != nil {
				return nil, perr
			}
			repoURL = next
			credentialID = cred
			if defaultBranch == "" {
				defaultBranch = remoteBranch
			}
		}
	}
	if in.CredentialID != nil && !repoTouched {
		cred := strings.TrimSpace(*in.CredentialID)
		if cred == "" {
			return nil, ErrEmptyCredentialID
		}
		if repoURL == "" {
			return nil, ErrCredentialWithoutRepo
		}
		// 改绑凭据:用新凭据对当前仓库做 ls-remote 校验。
		if _, err := s.probe(ctx, repoURL, cred); err != nil {
			return nil, err
		}
		credentialID = cred
	}
	if in.PacEnabled != nil {
		pacEnabled = *in.PacEnabled
	}
	if in.PRStatusEnabled != nil {
		prStatusEnabled = *in.PRStatusEnabled
	}
	// 这两个开关都以「读得到项目仓库」为前提,没仓库就是永远做不到的事,别让它开着。
	if repoURL == "" && (pacEnabled || prStatusEnabled) {
		return nil, ErrRepoRequired
	}
	if in.GroupID != nil {
		groupID = strings.TrimSpace(*in.GroupID)
		if groupID != "" {
			ok, err := s.groupExists(ctx, groupID)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, ErrGroupNotFound
			}
		}
	}

	pacInt = 0
	if pacEnabled {
		pacInt = 1
	}
	prStatusInt = 0
	if prStatusEnabled {
		prStatusInt = 1
	}
	nowStr := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx,
		`UPDATE projects SET name = ?, repo_url = ?, default_branch = ?, credential_id = ?, pac_enabled = ?, pr_status_enabled = ?, group_id = ?, updated_at = ? WHERE id = ?`,
		name, repoURL, defaultBranch, credArg(credentialID), pacInt, prStatusInt, groupID, nowStr, id,
	)
	if err != nil {
		if isForeignKeyErr(err) {
			return nil, ErrCredentialNotFound
		}
		return nil, fmt.Errorf("project: update: %w", err)
	}
	return s.Get(ctx, id)
}

func (s *service) Delete(ctx context.Context, id string) error {
	// 拒绝删除有进行中运行(queued/running)的项目。自包含在本包查 pipeline_runs(不改 run 包)。
	var active int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM pipeline_runs WHERE project_id = ? AND status IN ('queued','running')`,
		id,
	).Scan(&active); err != nil {
		return fmt.Errorf("project: check active runs: %w", err)
	}
	if active > 0 {
		return ErrProjectHasActiveRuns
	}

	res, err := s.db.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("project: delete: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *service) TestClone(ctx context.Context, repoURL, credentialID string) (*TestCloneResult, error) {
	if repoURL == "" {
		return nil, ErrEmptyRepoURL
	}
	if credentialID == "" {
		return nil, ErrEmptyCredentialID
	}
	branch, branches, tags, err := s.probeRefs(ctx, repoURL, credentialID)
	if err != nil {
		return nil, err
	}
	return &TestCloneResult{DefaultBranch: branch, Branches: branches, Tags: tags}, nil
}

// credArg 把「无凭据」写成 NULL 而不是空串:credential_id 上挂着引用完整性
// (删掉还在用的凭据要被挡住),空串会被当成一个指向不存在凭据的引用而直接被拒。
func credArg(credentialID string) any {
	if strings.TrimSpace(credentialID) == "" {
		return nil
	}
	return credentialID
}

// scanner 抽象 *sql.Row 与 *sql.Rows 的 Scan。
type scanner interface {
	Scan(dest ...any) error
}

// scanProject 把一行扫描为 Project(永不读任何密文/明文列)。
func scanProject(sc scanner) (*Project, error) {
	var p Project
	var createdStr, updatedStr string
	var pacInt, prStatusInt int
	if err := sc.Scan(
		&p.ID, &p.Name, &p.RepoURL, &p.DefaultBranch, &p.CredentialID,
		&p.CredentialName, &pacInt, &prStatusInt, &p.GroupID, &createdStr, &updatedStr,
	); err != nil {
		return nil, err
	}
	p.PacEnabled = pacInt != 0
	p.PRStatusEnabled = prStatusInt != 0
	created, err := time.Parse(time.RFC3339, createdStr)
	if err != nil {
		return nil, fmt.Errorf("project: parse created_at: %w", err)
	}
	updated, err := time.Parse(time.RFC3339, updatedStr)
	if err != nil {
		return nil, fmt.Errorf("project: parse updated_at: %w", err)
	}
	p.CreatedAt = created
	p.UpdatedAt = updated
	return &p, nil
}

// isForeignKeyErr 判断错误是否为外键约束失败(modernc sqlite 文本含 FOREIGN KEY)。
func isForeignKeyErr(err error) bool { return store.IsForeignKeyErr(err) }
