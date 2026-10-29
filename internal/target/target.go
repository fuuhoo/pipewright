// Package target 是「目标服务器」的领域层 + 通用 SSH 执行/会话基座。
//
// 一台服务器按引用绑定一条 SSH 凭据:DB 中只存 credential_id,绝无任何明文私钥/口令
// (AC-SEC-01)。Exec/Test 时,平台经 vault.OpenSecret 取凭据明文(私钥或口令)在进程内
// 装配 ssh.ClientConfig,用完即弃,绝不入库/日志/响应/错误体。
//
// 本包**通用化**:对外只暴露 Exec(serverID, cmd []string) / Test(serverID) /
// CRUD,不含任何部署语义。Epic 4 部署(4-2)与 Epic 6 运维都在其上构建,只消费这些签名、
// 不改它们(冻结契约)。
//
// AC-SEC-02:命令以 []string(程序 + 参数)传入,经各参数 shell 转义后再交 SSH session,
// 绝不让调用方拼接原始 shell 字符串;杜绝命令注入。
package target

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/fuuhoo/pipewright/internal/access"
	"github.com/fuuhoo/pipewright/internal/store"
	"github.com/fuuhoo/pipewright/internal/vault"
	"github.com/google/uuid"
)

// 领域错误。错误体永不含明文/私钥/口令/master key/内部栈。
var (
	// ErrNotFound 表示服务器不存在。
	ErrNotFound = errors.New("target: server not found")
	// ErrEmptyName 表示服务器名称为空。
	ErrEmptyName = errors.New("target: name must not be empty")
	// ErrEmptyHost 表示主机地址为空。
	ErrEmptyHost = errors.New("target: host must not be empty")
	// ErrEmptyUser 表示登录用户为空。
	ErrEmptyUser = errors.New("target: user must not be empty")
	// ErrEmptyCredentialID 表示未选择 SSH 凭据。
	ErrEmptyCredentialID = errors.New("target: credential id must not be empty")
	// ErrInvalidPort 表示端口非法(非 1..65535)。
	ErrInvalidPort = errors.New("target: port must be in 1..65535")
	// ErrCredentialNotFound 表示引用的凭据不存在(下拉项已被删除等)。
	ErrCredentialNotFound = errors.New("target: referenced credential not found")
	// ErrVaultUnconfigured 表示保险库未配置 master key,无法取 SSH 凭据。
	ErrVaultUnconfigured = errors.New("target: vault unconfigured")
	// ErrAuth 表示 SSH 认证失败(密钥/口令无效或无权限)。错误体不含凭据明文。
	ErrAuth = errors.New("target: ssh authentication failed")
	// ErrUnreachable 表示无法连接(端口关闭/主机不可达/超时)。
	ErrUnreachable = errors.New("target: server unreachable")
	// ErrInvalidCredential 表示凭据明文不是可用的 SSH 私钥/口令(解析失败)。
	ErrInvalidCredential = errors.New("target: credential is not a usable ssh key or password")
	// ErrGroupNotFound 表示 group_id 指向的分组不存在。servers.group_id 刻意不建外键
	// (见 0057 注释),故归组合法性由这里校验,否则悬挂引用会被 access 按最私有一档挡死。
	ErrGroupNotFound = errors.New("target: group not found")
)

// DefaultPort 是未显式给定端口时的 SSH 默认端口。
const DefaultPort = 22

// probeCommand 是 Test 连通性所跑的只读探测命令(AC-SEC-02:array 形式)。
var probeCommand = []string{"uname", "-a"}

// Server 是服务器领域模型。绝不含凭据明文,只持 credentialId 引用。
type Server struct {
	ID           string
	Name         string
	Host         string
	Port         int
	User         string
	CredentialID string
	// CredentialName 是冗余只读展示名(join credentials),便于列表展示;非持久列。
	CredentialName string
	// GroupIDs 是所属资源分组(server_groups,可多个,按 ID 升序);空 = 未归组 =
	// 全员可见可操作。可见性取并集,归属级动作要求每个组都管得住(见 internal/access)。
	GroupIDs  []string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CreateInput 是登记服务器的入参。
type CreateInput struct {
	Name         string
	Host         string
	Port         int // <=0 时归一为 DefaultPort
	User         string
	CredentialID string
	// GroupIDs 可空(空 = 未归组)。能否放进这些组由 HTTP 层判定(access.ActManage)。
	GroupIDs []string
}

// UpdateInput 是更新服务器的入参;指针字段为 nil 表示不修改。
type UpdateInput struct {
	Name         *string
	Host         *string
	Port         *int
	User         *string
	CredentialID *string
	// GroupIDs 整体替换归属;nil = 不动,空切片 = 移出所有分组。两侧 Manage 由 HTTP 层把关。
	GroupIDs *[]string
}

// ListFilter 是列表的分组可见性过滤。
//
// Visible 是零值 ListFilter{} 时表示「只见未归组」(fail closed),不是「不受限」;
// 内部系统调用方(部署、代理、异常检测)请走 List,它等价于 Unrestricted。
type ListFilter struct {
	Visible access.ListFilter
}

// ExecResult 是通用 Exec 的结果(冻结契约;Epic 4/6 消费)。
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// 流式回调的通道标识(ExecStreamWithExit 的 onChunk 第一参)。取值即日志流名,与 build/run
// 两侧的 "stdout"/"stderr" 口径一致,便于上层直接透传。
const (
	StreamStdout = "stdout"
	StreamStderr = "stderr"
)

// TestResult 是测试连接的结果(冻结契约)。Output 为探测命令的截断输出。
type TestResult struct {
	OK        bool
	LatencyMs int64
	Output    string
	// Err 为人读错误(失败时非空);绝不含凭据明文。成功时为空。
	Err string
}

// Service 定义目标服务器领域 + 通用 SSH 执行层对外接口(冻结契约,Epic 4/6 import)。
type Service interface {
	// Get 返回单个服务器视图(无明文)。
	Get(ctx context.Context, id string) (*Server, error)
	// List 返回所有服务器(join credentials 取展示名;无明文)。
	// 这是「系统视角」的全量列表,供部署/代理/异常检测等内部调用方使用;
	// 面向已登录用户的列表请用 ListScoped,否则会绕过分组可见性。
	List(ctx context.Context) ([]*Server, error)
	// ListScoped 按分组可见性返回服务器(同 join;无明文)。
	ListScoped(ctx context.Context, f ListFilter) ([]*Server, error)
	// Create 校验入参后入库;返回服务器视图(无明文)。不在此触网(纯登记)。
	Create(ctx context.Context, in CreateInput) (*Server, error)
	// Update 改名/改 host/port/user/改绑凭据;返回更新后视图。
	Update(ctx context.Context, id string, in UpdateInput) (*Server, error)
	// Delete 删除服务器。
	Delete(ctx context.Context, id string) error
	// Test 经 SSH 实连并跑只读探测命令(uname -a),返回成功/延迟/输出或人读错误。
	Test(ctx context.Context, id string) (*TestResult, error)
	// Exec 通用执行:对指定服务器经 SSH 跑 cmd(程序 + 参数,**array 不拼 shell**,AC-SEC-02),
	// 返回 stdout/stderr/exitCode。超时经 ctx。供 Epic 4 部署 / Epic 6 运维复用。
	Exec(ctx context.Context, serverID string, cmd []string) (*ExecResult, error)
	// ExecBatch 在同一条 SSH 连接上依次跑多条 array 命令(语义同 Exec:array 化、脱敏、
	// 凭据即用即弃),返回与 cmds 同序的结果。供指标采集这类「一台机器连跑数条只读命令」使用:
	// 逐条 Exec 会重复拨号 + 握手数次,复用连接只剩一次。连接层面失败时返回已完成的部分结果
	// + error;命令非零退出不是 error,照常落在那条结果的 ExitCode 里。
	ExecBatch(ctx context.Context, serverID string, cmds [][]string) ([]*ExecResult, error)
	// ExecStream 流式执行(Story 6.2 append):经 SSH session 跑 cmd 并返回 stdout 的流(供
	// 实时 tail -f / journalctl -f)。调用方读到底或关闭 ReadCloser 即释放 SSH session;ctx
	// 取消亦关 session(防泄漏/挂死)。cmd 同样是 array,经各参数 shell 转义(AC-SEC-02)。
	ExecStream(ctx context.Context, serverID string, cmd []string) (io.ReadCloser, error)
	// ExecInteractive 双向交互会话(Story 6.4 append;FR-18):经 SSH 开 PTY 跑 cmd(array,
	// 典型为 `docker exec -it <id> <shell>`),返回可写 stdin / 可读 stdout+stderr / resize /
	// close 的 Session。供容器内交互终端(WS ↔ SSH)使用。cmd 同样 array 化、各参数 shell 转义
	// (AC-SEC-02);凭据经 vault 即用即弃,绝不日志。调用方关闭 Session(或 ctx 取消)即释放远端
	// PTY + SSH session/client(不泄漏)。
	ExecInteractive(ctx context.Context, serverID string, cmd []string) (Session, error)
	// Upload 把 content 的字节流式写到目标机的 remotePath(Story 8-16 / FR-8-16:制品库部署真字节)。
	// 实现:经 SSH session 把 content 接到 `cat > <remotePath>` 的 stdin —— 流式、无 argv 长度上限、
	// 大文件友好;remotePath 作 array 参数 shell 转义(AC-SEC-02),不拼 shell。远端非零退出 / 连接失败
	// → 人读错误(绝无凭据明文)。供部署把制品库里的 jar/dist 真字节落到目标机。
	Upload(ctx context.Context, serverID string, content io.Reader, remotePath string) error
	// ExecWithStdin 经 SSH 跑 cmd,并把 stdin 的字节流接到远端命令的标准输入,返回
	// stdout/stderr/exitCode(语义同 Exec:array 化、脱敏、凭据即用即弃)。
	//
	// 存在的理由:Upload 已经把这条通路走通了,但它把命令钉死成 `cat > 路径`(落盘)。
	// 还有一类需求是「字节进管道,而不是进文件」:远程构建机上 `docker login --password-stdin`
	// 的口令、把中控机镜像 `docker load` 进远端 daemon。口令这类内容绝不能进 argv(会留在
	// 远端进程表和日志里),只能走 stdin。
	ExecWithStdin(ctx context.Context, serverID string, cmd []string, stdin io.Reader) (*ExecResult, error)
	// ExecStreamWithExit 流式跑 cmd,stdout/stderr **分流**逐块交给 onChunk,并在命令结束后
	// 回传真退出码。与 ExecStream 的区别:后者只回 stdout 且没有退出码(供 tail -f 这类
	// 「只看不问」的场合);这里有退出码,可用于「远端跑一条会失败的命令」的判定(如 docker run)。
	//
	// onChunk 只会在单个 goroutine 里被串行调用(两路读在内部合流),上层无需自己加锁,
	// 也不需要按行切块 —— 块的边界是 TCP 读到的边界,不是行边界。
	// ctx 取消即 SIGKILL 远端进程并释放 session/client,返回 ctx 的错误。
	ExecStreamWithExit(ctx context.Context, serverID string, cmd []string, onChunk func(stream string, chunk []byte)) (int, error)
}

// Session 是一个双向交互式 SSH/PTY 会话(Story 6.4;FR-18)。
//
// 它把远端伪终端的 stdin(写)/ stdout+stderr 合流(读)/ 窗口尺寸(resize)/ 生命周期
// (close)抽象为最小接口,供上层(WS 终端泵)消费。实现须保证:
//   - Read 阻塞直至有 PTY 输出或会话结束(EOF / 远端进程退出);
//   - Close 幂等,且关闭后释放远端 PTY + 底层 SSH session/client(不泄漏);
//   - Resize 在窗口尺寸变化时通知远端(WindowChange),失败可忽略(非致命)。
type Session interface {
	io.ReadWriteCloser
	// Resize 通知远端 PTY 调整为 cols 列 rows 行(窗口变化)。失败不致命(忽略)。
	Resize(cols, rows int) error
}

// SSHDialer 抽象「用装配好的 SSH 配置连服务器并跑一条 array 命令」的能力。
// 注入便于测试(可用 stub 替换真实网络拨号),也便于 4-2/Epic 6 在不触网下做单测。
type SSHDialer interface {
	// Run 连 addr(host:port)、以 cfg 认证、跑 cmd(已是程序+参数 array),返回结果。
	// 连接/认证失败映射为 ErrUnreachable / ErrAuth(绝不含凭据明文)。
	Run(ctx context.Context, addr string, cfg SSHConfig, cmd []string) (*ExecResult, error)
	// RunStream 连 addr、以 cfg 认证、跑 cmd 并返回 stdout 的流(供实时 tail)。
	// 返回的 ReadCloser 被关闭或 ctx 取消时,底层 SSH session/client 一并关闭(不泄漏)。
	RunStream(ctx context.Context, addr string, cfg SSHConfig, cmd []string) (io.ReadCloser, error)
	// RunInteractive 连 addr、以 cfg 认证、开 PTY 并启动 cmd(array;典型容器交互 shell),
	// 返回双向 Session(stdin 写 / stdout+stderr 读 / resize / close)。Session 关闭或 ctx
	// 取消时,远端 PTY + 底层 SSH session/client 一并关闭(不泄漏)。cmd 经各参数 shell 转义。
	RunInteractive(ctx context.Context, addr string, cfg SSHConfig, cmd []string) (Session, error)
	// RunWithStdin 同 Run,但把 stdin 接到远端命令的标准输入(供 `cat > file` 流式上传)。
	// stdin 读尽即关闭远端 stdin;返回退出码/输出。连接/认证失败映射为 ErrUnreachable / ErrAuth。
	RunWithStdin(ctx context.Context, addr string, cfg SSHConfig, cmd []string, stdin io.Reader) (*ExecResult, error)
}

// batchDialer 是 SSHDialer 的可选扩展:在**一条**连接(TCP 拨号 + 密钥交换 + 认证各一次)上
// 依次开多个 session 跑多条命令。支持它的拨号器让 Service.ExecBatch 免去重复拨号;
// 不支持的(如测试里的假拨号器)由 ExecBatch 自动退回逐条 Run。
// 契约同 Run:非零退出不算 error;连接/上下文层面的失败才算 error。
type batchDialer interface {
	RunBatch(ctx context.Context, addr string, cfg SSHConfig, cmds [][]string) ([]*ExecResult, error)
}

// streamExitDialer 是 SSHDialer 的可选扩展:流式跑 cmd,stdout/stderr 分流回调,并回传退出码。
// 真拨号器(sshDialer)实现它;测试里的假拨号器没有,Service 会退回「Run 后整段回调」——
// 内容、退出码、分流都一样,只是不实时。契约同 Run:非零退出不是 error,落在返回的退出码里。
type streamExitDialer interface {
	RunStreamWithExit(ctx context.Context, addr string, cfg SSHConfig, cmd []string, onChunk func(stream string, chunk []byte)) (int, error)
}

// SSHConfig 是拨号所需的最小认证材料(进程内,用完即弃)。
// 二选一:PrivateKey(PEM 私钥明文)或 Password;两者皆空则无可用认证法。
type SSHConfig struct {
	User       string
	PrivateKey string // PEM 私钥明文(经 vault 取出,用完即弃)
	Password   string // 口令明文(经 vault 取出,用完即弃)
}

// service 是 store + vault + dialer 支撑的 Service 实现。
type service struct {
	db     *sql.DB
	vault  vault.Vault
	dialer SSHDialer
}

// New 构造 Service。
//   - db:仅供本包通过参数化 SQL 触库。
//   - v:凭据保险库;v 为 nil 或未配置 master key 时,Exec/Test 返回 ErrVaultUnconfigured。
//   - dialer:SSH 拨号器;为 nil 时使用默认 x/crypto/ssh 实现(sshDialer)。
//
// 不在此做任何重活(无 init() 副作用,避免抬高空载内存)。
func New(db *sql.DB, v vault.Vault, dialer SSHDialer) Service {
	if dialer == nil {
		dialer = sshDialer{}
	}
	return &service{db: db, vault: v, dialer: dialer}
}

// normalizePort 归一端口:<=0 → DefaultPort。
func normalizePort(p int) int {
	if p <= 0 {
		return DefaultPort
	}
	return p
}

// validateCreate 校验登记入参的必填项与端口范围。
func validateCreate(in CreateInput) error {
	if in.Name == "" {
		return ErrEmptyName
	}
	if in.Host == "" {
		return ErrEmptyHost
	}
	if in.User == "" {
		return ErrEmptyUser
	}
	if in.CredentialID == "" {
		return ErrEmptyCredentialID
	}
	if p := normalizePort(in.Port); p < 1 || p > 65535 {
		return ErrInvalidPort
	}
	return nil
}

func (s *service) Create(ctx context.Context, in CreateInput) (*Server, error) {
	if err := validateCreate(in); err != nil {
		return nil, err
	}
	port := normalizePort(in.Port)

	id := uuid.NewString()
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)

	groupIDs := NormalizeGroupIDs(in.GroupIDs)
	if err := s.validateGroups(ctx, groupIDs); err != nil {
		return nil, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("target: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO servers (id, name, host, port, user, credential_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, in.Name, in.Host, port, in.User, in.CredentialID, nowStr, nowStr,
	); err != nil {
		if isForeignKeyErr(err) {
			return nil, ErrCredentialNotFound
		}
		return nil, fmt.Errorf("target: insert: %w", err)
	}
	if err := insertGroups(ctx, tx, id, groupIDs, nowStr); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("target: commit: %w", err)
	}
	return s.Get(ctx, id)
}

// List 是全量视角(内部调用方);等价于 ListScoped{Unrestricted}。
func (s *service) List(ctx context.Context) ([]*Server, error) {
	return s.ListScoped(ctx, ListFilter{Visible: access.ListFilter{Unrestricted: true}})
}

func (s *service) ListScoped(ctx context.Context, f ListFilter) ([]*Server, error) {
	query := `SELECT s.id, s.name, s.host, s.port, s.user, s.credential_id,
		        COALESCE(c.name, ''), s.created_at, s.updated_at
		 FROM servers s
		 LEFT JOIN credentials c ON c.id = s.credential_id`
	var args []any
	// 归属在 server_groups 里(可多个):「未归组」是没有成员行,而不是列值 ''。
	if clause, clauseArgs := f.Visible.MemberClause("s.id", "server_groups", "server_id", "group_id"); clause != "" {
		query += " WHERE " + clause
		args = clauseArgs
	}
	query += " ORDER BY s.created_at DESC, s.id"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("target: list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]*Server, 0)
	for rows.Next() {
		srv, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, srv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("target: iterate: %w", err)
	}
	if err := s.attachGroups(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

// validateGroups 校验每个分组 ID 都存在(server_groups 无外键,故在此挡悬挂引用)。
func (s *service) validateGroups(ctx context.Context, groupIDs []string) error {
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
		return fmt.Errorf("target: check groups: %w", err)
	}
	if n != len(groupIDs) {
		return ErrGroupNotFound
	}
	return nil
}

// NormalizeGroupIDs 统一归属集合的形态:去空白、丢空项、去重并按 ID 升序。
// 排序是给读路径一个确定顺序(前端标签、审计 detail 不会因为插入顺序不同而抖动)。
// HTTP 层也用它,好让「能不能放进这些组」的判定与真正入库的集合完全一致。
func NormalizeGroupIDs(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
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

// insertGroups 写入一台机器的归属边(调用方负责事务)。
func insertGroups(ctx context.Context, tx *sql.Tx, serverID string, groupIDs []string, now string) error {
	for _, groupID := range groupIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO server_groups (server_id, group_id, created_at) VALUES (?, ?, ?)`,
			serverID, groupID, now,
		); err != nil {
			return fmt.Errorf("target: insert group: %w", err)
		}
	}
	return nil
}

// attachGroups 批量补齐 servers 的 GroupIDs(避免列表页每行一次查询)。
func (s *service) attachGroups(ctx context.Context, servers []*Server) error {
	if len(servers) == 0 {
		return nil
	}
	ids := make([]any, 0, len(servers))
	byID := make(map[string]*Server, len(servers))
	for _, srv := range servers {
		srv.GroupIDs = []string{}
		ids = append(ids, srv.ID)
		byID[srv.ID] = srv
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	rows, err := s.db.QueryContext(ctx,
		`SELECT server_id, group_id FROM server_groups WHERE server_id IN (`+placeholders+`)
		 ORDER BY server_id, group_id`, ids...)
	if err != nil {
		return fmt.Errorf("target: load groups: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var serverID, groupID string
		if err := rows.Scan(&serverID, &groupID); err != nil {
			return fmt.Errorf("target: scan group: %w", err)
		}
		if srv, ok := byID[serverID]; ok {
			srv.GroupIDs = append(srv.GroupIDs, groupID)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("target: iterate groups: %w", err)
	}
	return nil
}

func (s *service) Get(ctx context.Context, id string) (*Server, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT s.id, s.name, s.host, s.port, s.user, s.credential_id,
		        COALESCE(c.name, ''), s.created_at, s.updated_at
		 FROM servers s
		 LEFT JOIN credentials c ON c.id = s.credential_id
		 WHERE s.id = ?`, id,
	)
	srv, err := scanServer(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := s.attachGroups(ctx, []*Server{srv}); err != nil {
		return nil, err
	}
	return srv, nil
}

func (s *service) Update(ctx context.Context, id string, in UpdateInput) (*Server, error) {
	// 先取当前行。
	var name, host, user, credentialID string
	var port int
	err := s.db.QueryRowContext(ctx,
		`SELECT name, host, port, user, credential_id FROM servers WHERE id = ?`, id,
	).Scan(&name, &host, &port, &user, &credentialID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("target: load: %w", err)
	}

	if in.Name != nil {
		if *in.Name == "" {
			return nil, ErrEmptyName
		}
		name = *in.Name
	}
	if in.Host != nil {
		if *in.Host == "" {
			return nil, ErrEmptyHost
		}
		host = *in.Host
	}
	if in.User != nil {
		if *in.User == "" {
			return nil, ErrEmptyUser
		}
		user = *in.User
	}
	if in.Port != nil {
		p := normalizePort(*in.Port)
		if p < 1 || p > 65535 {
			return nil, ErrInvalidPort
		}
		port = p
	}
	if in.CredentialID != nil {
		if *in.CredentialID == "" {
			return nil, ErrEmptyCredentialID
		}
		credentialID = *in.CredentialID
	}
	// 指针非 nil 才动归属:显式空切片 = 移出所有分组,与「不传」区分开。
	var groupIDs []string
	if in.GroupIDs != nil {
		groupIDs = NormalizeGroupIDs(*in.GroupIDs)
		if err := s.validateGroups(ctx, groupIDs); err != nil {
			return nil, err
		}
	}

	nowStr := time.Now().UTC().Format(time.RFC3339)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("target: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`UPDATE servers SET name = ?, host = ?, port = ?, user = ?, credential_id = ?, updated_at = ? WHERE id = ?`,
		name, host, port, user, credentialID, nowStr, id,
	); err != nil {
		if isForeignKeyErr(err) {
			return nil, ErrCredentialNotFound
		}
		return nil, fmt.Errorf("target: update: %w", err)
	}
	if in.GroupIDs != nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM server_groups WHERE server_id = ?`, id); err != nil {
			return nil, fmt.Errorf("target: clear groups: %w", err)
		}
		if err := insertGroups(ctx, tx, id, groupIDs, nowStr); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("target: commit: %w", err)
	}
	return s.Get(ctx, id)
}

func (s *service) Delete(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("target: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// 归属边没有外键(见 0087),删除机器必须在同一事务里自己清掉。
	if _, err := tx.ExecContext(ctx, `DELETE FROM server_groups WHERE server_id = ?`, id); err != nil {
		return fmt.Errorf("target: delete groups: %w", err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM servers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("target: delete: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("target: commit: %w", err)
	}
	return nil
}

func (s *service) Test(ctx context.Context, id string) (*TestResult, error) {
	start := time.Now()
	res, err := s.Exec(ctx, id, probeCommand)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		// 服务器/凭据不存在等定位类错误:上抛供 HTTP 层映射 404/422/503。
		if errors.Is(err, ErrNotFound) ||
			errors.Is(err, ErrCredentialNotFound) ||
			errors.Is(err, ErrVaultUnconfigured) {
			return nil, err
		}
		// 连接/认证/执行类错误:映射为 TestResult.ok=false + 人读错误(绝不含凭据明文)。
		return &TestResult{OK: false, LatencyMs: latency, Err: humanError(err)}, nil
	}
	// 探测命令非零退出(code-review P4):SSH 连通但远端命令失败(受限 shell / PATH / command
	// not found)不应谎报「连接成功」。ExitCode≠0 → ok=false + 人读(纳入 stderr,不泄密)。
	if res.ExitCode != 0 {
		detail := strings.TrimSpace(truncate(res.Stderr, 512))
		msg := "已连接,但探测命令返回非零退出码"
		if detail != "" {
			msg = msg + ":" + detail
		}
		return &TestResult{OK: false, LatencyMs: latency, Err: msg}, nil
	}
	return &TestResult{
		OK:        true,
		LatencyMs: latency,
		Output:    truncate(res.Stdout, 4096),
	}, nil
}

// Exec 取凭据明文装配 SSH 配置并跑 array 命令;凭据明文仅进程内存在,用完即弃。
func (s *service) Exec(ctx context.Context, serverID string, cmd []string) (*ExecResult, error) {
	if len(cmd) == 0 {
		return nil, fmt.Errorf("target: empty command")
	}
	addr, cfg, err := s.sshTarget(ctx, serverID)
	if err != nil {
		return nil, err
	}

	res, runErr := s.dialer.Run(ctx, addr, cfg, cmd)

	// 显式清明文引用,尽早不可达(明文不留)。
	cfg.PrivateKey = ""
	cfg.Password = ""

	if runErr != nil {
		return nil, runErr
	}
	return res, nil
}

// ExecBatch 在一次 SSH 连接上依次跑多条 array 命令(每条命令的 array 化、脱敏、凭据即用即弃
// 语义与 Exec 完全一致),结果与 cmds 同序。
//
// 存在的理由:一条 Exec 等于一整轮 TCP 拨号 + 密钥交换 + 认证。像指标采集那样对同一台机器连跑
// 六七条只读命令时,逐条 Exec 会把这台机器拨号六七次,握手时间远超命令本身 —— 复用一条连接后
// 只剩一次拨号,凭据也只解密一次。
//
// 拨号器不支持连接复用(测试里的假拨号器)时自动退回逐条 Run:结果一致,只是省不掉重复拨号。
// 连接层面失败时返回已完成的部分结果 + error(单条命令非零退出不是错误,照常落在那条结果里)。
func (s *service) ExecBatch(ctx context.Context, serverID string, cmds [][]string) ([]*ExecResult, error) {
	if len(cmds) == 0 {
		return nil, fmt.Errorf("target: empty command batch")
	}
	addr, cfg, err := s.sshTarget(ctx, serverID)
	if err != nil {
		return nil, err
	}
	defer func() { cfg.PrivateKey = ""; cfg.Password = "" }()

	if bd, ok := s.dialer.(batchDialer); ok {
		return bd.RunBatch(ctx, addr, cfg, cmds)
	}

	results := make([]*ExecResult, 0, len(cmds))
	for _, cmd := range cmds {
		res, runErr := s.dialer.Run(ctx, addr, cfg, cmd)
		if runErr != nil {
			return results, runErr
		}
		results = append(results, res)
	}
	return results, nil
}

// sshTarget 按 serverID 取登记信息与凭据明文,装配拨号所需的 (addr, SSHConfig)。
// 明文只存在于返回的 cfg 中,调用方用完必须清(见 Exec / ExecBatch 的收尾)。
func (s *service) sshTarget(ctx context.Context, serverID string) (string, SSHConfig, error) {
	srv, err := s.Get(ctx, serverID)
	if err != nil {
		return "", SSHConfig{}, err
	}
	if s.vault == nil {
		return "", SSHConfig{}, ErrVaultUnconfigured
	}

	// 取凭据明文(私钥或口令)。明文仅进程内,返回前由调用方清引用。
	secret, err := s.vault.Get(srv.CredentialID)
	if err != nil {
		switch {
		case errors.Is(err, vault.ErrVaultUnconfigured):
			return "", SSHConfig{}, ErrVaultUnconfigured
		case errors.Is(err, vault.ErrNotFound):
			return "", SSHConfig{}, ErrCredentialNotFound
		default:
			// 解密等内部错误:不泄漏细节,统一按认证错误对待。
			return "", SSHConfig{}, ErrAuth
		}
	}

	cfg := SSHConfig{User: srv.User}
	if looksLikePEM(secret) {
		cfg.PrivateKey = secret
	} else {
		cfg.Password = secret
	}
	secret = ""

	return fmt.Sprintf("%s:%d", srv.Host, srv.Port), cfg, nil
}

// Upload 把 content 流式写到目标机 remotePath(Story 8-16:制品库部署真字节)。
// 经 SSH 把 content 接到远端 `mkdir -p <dir> && cat > <remotePath>` 的 stdin —— 流式、无 argv 长度限、
// 大文件友好;remotePath 作位置参数 shell 转义(AC-SEC-02 不拼 shell);凭据明文用完即清。
// 远端非零退出 → 人读错误(含 stderr 摘要,绝无凭据明文);连接/认证失败 → ErrUnreachable / ErrAuth。
func (s *service) Upload(ctx context.Context, serverID string, content io.Reader, remotePath string) error {
	if content == nil {
		return fmt.Errorf("target: nil content")
	}
	if strings.TrimSpace(remotePath) == "" {
		return fmt.Errorf("target: empty remote path")
	}
	srv, err := s.Get(ctx, serverID)
	if err != nil {
		return err
	}
	if s.vault == nil {
		return ErrVaultUnconfigured
	}
	secret, err := s.vault.Get(srv.CredentialID)
	if err != nil {
		switch {
		case errors.Is(err, vault.ErrVaultUnconfigured):
			return ErrVaultUnconfigured
		case errors.Is(err, vault.ErrNotFound):
			return ErrCredentialNotFound
		default:
			return ErrAuth
		}
	}
	cfg := SSHConfig{User: srv.User}
	if looksLikePEM(secret) {
		cfg.PrivateKey = secret
	} else {
		cfg.Password = secret
	}

	// remotePath 作 $0(位置参数),绝不拼进脚本体;dir 先建好再写,写整文件经 stdin 流入。
	cmd := []string{"sh", "-c", `mkdir -p "$(dirname "$0")" && cat > "$0"`, remotePath}
	addr := fmt.Sprintf("%s:%d", srv.Host, srv.Port)
	res, runErr := s.dialer.RunWithStdin(ctx, addr, cfg, cmd, content)

	secret = ""
	cfg.PrivateKey = ""
	cfg.Password = ""
	_ = secret

	if runErr != nil {
		return runErr
	}
	if res != nil && res.ExitCode != 0 {
		return fmt.Errorf("target: upload 失败(退出码 %d):%s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return nil
}

// ExecWithStdin 经 SSH 跑 cmd 并把 stdin 接到远端标准输入(语义同 Exec,详见接口注释)。
// 凭据即用即弃:明文只在 sshTarget 返回的 cfg 里,调用拨号器前用完立即清引用。
func (s *service) ExecWithStdin(ctx context.Context, serverID string, cmd []string, stdin io.Reader) (*ExecResult, error) {
	if len(cmd) == 0 {
		return nil, fmt.Errorf("target: empty command")
	}
	if stdin == nil {
		return nil, fmt.Errorf("target: nil stdin")
	}
	addr, cfg, err := s.sshTarget(ctx, serverID)
	if err != nil {
		return nil, err
	}

	res, runErr := s.dialer.RunWithStdin(ctx, addr, cfg, cmd, stdin)

	cfg.PrivateKey = ""
	cfg.Password = ""

	if runErr != nil {
		return nil, runErr
	}
	return res, nil
}

// ExecStreamWithExit 流式跑 cmd(stdout/stderr 分流 + 真退出码,详见接口注释)。
//
// 拨号器不支持分流流式(测试里的假拨号器)时退回 Run + 整段回调:结果一致,只是不实时。
func (s *service) ExecStreamWithExit(ctx context.Context, serverID string, cmd []string, onChunk func(stream string, chunk []byte)) (int, error) {
	if len(cmd) == 0 {
		return -1, fmt.Errorf("target: empty command")
	}
	addr, cfg, err := s.sshTarget(ctx, serverID)
	if err != nil {
		return -1, err
	}
	defer func() { cfg.PrivateKey = ""; cfg.Password = "" }()

	if sd, ok := s.dialer.(streamExitDialer); ok {
		return sd.RunStreamWithExit(ctx, addr, cfg, cmd, onChunk)
	}

	res, runErr := s.dialer.Run(ctx, addr, cfg, cmd)
	if runErr != nil {
		return -1, runErr
	}
	if res == nil {
		return -1, nil
	}
	if onChunk != nil {
		if res.Stdout != "" {
			onChunk(StreamStdout, []byte(res.Stdout))
		}
		if res.Stderr != "" {
			onChunk(StreamStderr, []byte(res.Stderr))
		}
	}
	return res.ExitCode, nil
}

// ExecStream 取凭据明文装配 SSH 配置并流式跑 array 命令(供实时 tail)。
// 与 Exec 同样的凭据取用/清引用纪律:明文仅进程内,装配完 cfg 后立即清零本地引用。
// 注意 cfg.PrivateKey/Password 的副本随 RunStream 进入 dialer 持有的 session 生命周期,
// 用于建连;连接建立后由 dialer 持有的 client/session 负责;调用方读完/关闭流即释放。
func (s *service) ExecStream(ctx context.Context, serverID string, cmd []string) (io.ReadCloser, error) {
	if len(cmd) == 0 {
		return nil, fmt.Errorf("target: empty command")
	}
	srv, err := s.Get(ctx, serverID)
	if err != nil {
		return nil, err
	}
	if s.vault == nil {
		return nil, ErrVaultUnconfigured
	}

	secret, err := s.vault.Get(srv.CredentialID)
	if err != nil {
		switch {
		case errors.Is(err, vault.ErrVaultUnconfigured):
			return nil, ErrVaultUnconfigured
		case errors.Is(err, vault.ErrNotFound):
			return nil, ErrCredentialNotFound
		default:
			return nil, ErrAuth
		}
	}

	cfg := SSHConfig{User: srv.User}
	if looksLikePEM(secret) {
		cfg.PrivateKey = secret
	} else {
		cfg.Password = secret
	}

	addr := fmt.Sprintf("%s:%d", srv.Host, srv.Port)
	rc, runErr := s.dialer.RunStream(ctx, addr, cfg, cmd)

	// 清本地明文引用(dialer 已用 cfg 完成建连/装配;此处本地引用不再需要)。
	secret = ""
	cfg.PrivateKey = ""
	cfg.Password = ""
	_ = secret

	if runErr != nil {
		return nil, runErr
	}
	return rc, nil
}

// ExecInteractive 取凭据明文装配 SSH 配置并开 PTY 跑 array 命令(供容器交互终端,Story 6.4)。
// 与 Exec/ExecStream 同样的凭据取用/清引用纪律:明文仅进程内,装配完 cfg 后立即清零本地引用。
// 返回的 Session 持有底层 SSH client/session 生命周期;调用方 Close()(或 ctx 取消)即释放。
func (s *service) ExecInteractive(ctx context.Context, serverID string, cmd []string) (Session, error) {
	if len(cmd) == 0 {
		return nil, fmt.Errorf("target: empty command")
	}
	srv, err := s.Get(ctx, serverID)
	if err != nil {
		return nil, err
	}
	if s.vault == nil {
		return nil, ErrVaultUnconfigured
	}

	secret, err := s.vault.Get(srv.CredentialID)
	if err != nil {
		switch {
		case errors.Is(err, vault.ErrVaultUnconfigured):
			return nil, ErrVaultUnconfigured
		case errors.Is(err, vault.ErrNotFound):
			return nil, ErrCredentialNotFound
		default:
			return nil, ErrAuth
		}
	}

	cfg := SSHConfig{User: srv.User}
	if looksLikePEM(secret) {
		cfg.PrivateKey = secret
	} else {
		cfg.Password = secret
	}

	addr := fmt.Sprintf("%s:%d", srv.Host, srv.Port)
	sess, runErr := s.dialer.RunInteractive(ctx, addr, cfg, cmd)

	// 清本地明文引用(dialer 已用 cfg 完成建连/装配)。
	secret = ""
	cfg.PrivateKey = ""
	cfg.Password = ""
	_ = secret

	if runErr != nil {
		return nil, runErr
	}
	return sess, nil
}

// looksLikePEM 判定凭据明文是否为 PEM 私钥(决定走私钥还是口令认证)。
func looksLikePEM(secret string) bool {
	return strings.Contains(secret, "-----BEGIN") && strings.Contains(secret, "PRIVATE KEY")
}

// humanError 把领域错误映射为人读文案(绝不含凭据明文/内部栈)。
func humanError(err error) string {
	switch {
	case errors.Is(err, ErrAuth):
		return "SSH 认证失败:密钥或口令无效,或无登录权限"
	case errors.Is(err, ErrUnreachable):
		return "无法连接服务器:端口未开放、主机不可达或超时"
	case errors.Is(err, ErrInvalidCredential):
		return "凭据不是可用的 SSH 私钥或口令"
	case errors.Is(err, context.DeadlineExceeded):
		return "连接超时"
	default:
		return "连接失败"
	}
}

// truncate 截断字符串到 max 字节(防超大输出撑爆响应/内存)。
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…(已截断)"
}

// scanner 抽象 *sql.Row 与 *sql.Rows 的 Scan。
type scanner interface {
	Scan(dest ...any) error
}

// scanServer 把一行扫描为 Server(永不读任何密文/明文列);归属由 attachGroups 补齐。
func scanServer(sc scanner) (*Server, error) {
	var srv Server
	var createdStr, updatedStr string
	if err := sc.Scan(
		&srv.ID, &srv.Name, &srv.Host, &srv.Port, &srv.User, &srv.CredentialID,
		&srv.CredentialName, &createdStr, &updatedStr,
	); err != nil {
		return nil, err
	}
	created, err := time.Parse(time.RFC3339, createdStr)
	if err != nil {
		return nil, fmt.Errorf("target: parse created_at: %w", err)
	}
	updated, err := time.Parse(time.RFC3339, updatedStr)
	if err != nil {
		return nil, fmt.Errorf("target: parse updated_at: %w", err)
	}
	srv.CreatedAt = created
	srv.UpdatedAt = updated
	return &srv, nil
}

// isForeignKeyErr 判断错误是否为外键约束失败(modernc sqlite 文本含 FOREIGN KEY)。
func isForeignKeyErr(err error) bool { return store.IsForeignKeyErr(err) }
