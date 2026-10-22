// Package audit 是审计地基(NFR-7 / AC-SEC-03)。
//
// 敏感操作(凭据增删改、trigger secret reset、项目增删改、手动触发运行)成功后,
// 同步写入本地 append-only 审计表(0009_audit.sql,SQLite trigger 硬拦 UPDATE/DELETE),
// 并可选地同步到远端 sink(env PIPEWRIGHT_AUDIT_SINK)。本地日志被删后,远端 sink
// 仍持完整记录(AC-SEC-03)。
//
// 脱敏铁律:Entry.Detail 写库前一律过 mask.Masker.ScrubMap,detail_json 绝不含
// 明文 secret/密文/master key。
//
// 降级铁律:远端 sink 不可达只降级记录(返回 nil,不阻断主操作),绝不让审计失败
// 回滚或阻断核心业务。本地写入失败同样不阻断业务(由调用方决定是否记日志)。
//
// 无 init 副作用、无包级重对象:Recorder 为轻量结构(避免抬高空载内存)。
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fuuhoo/pipewright/internal/mask"
	"github.com/google/uuid"
)

// Action 操作枚举(snake_case;DB 存字串)。只增不改语义。
const (
	ActionCredentialCreate = "credential_create"
	ActionCredentialUpdate = "credential_update"
	ActionCredentialDelete = "credential_delete"
	ActionCredentialReveal = "credential_reveal"
	// v6.2 阶段 7:admin 禁用 personal 凭据(enabled=0);不改密、不删。
	ActionCredentialDisable = "credential_disable"
	// v6.2 阶段 9:恢复被禁用的 personal 凭据。与 disable 分两个动作,是为了在审计里
	// 能还原「谁停用过、后来又谁放开的」——只记一个 toggle 会丢掉方向。
	ActionCredentialEnable   = "credential_enable"
	ActionTriggerSecretReset = "trigger_secret_reset"
	ActionProjectCreate      = "project_create"
	ActionProjectUpdate      = "project_update"
	ActionProjectDelete      = "project_delete"
	// ActionProjectCacheClear 清除项目的本地磁盘缓存(代码镜像 / 项目依赖缓存)。
	// 单列一个动作而不是塞进 project_update:它不改任何库内配置,改的是磁盘上的缓存目录,
	// 事后要看的是「谁在什么时候把谁的依赖清空了」(清错会让下一次构建慢很久)。
	ActionProjectCacheClear = "project_cache_clear"
	// 复制项目单独两个动作,而不是并回 project_create:副本的内容整份来自另一个项目,
	// 事后要分清「凭空配出来的」和「照着别人抄一份的」——后者才是排查复制扩散的入口。
	// 一批单独一条:一次点击出 N 个副本,逐行记录会把这一件事刷成几十条看不全的流水。
	ActionProjectCopy      = "project_copy"
	ActionProjectCopyBatch = "project_copy_batch"
	ActionRunTriggerManual = "run_trigger_manual"
	ActionPasswordChange   = "password_change"
	// ActionProfileUpdate 是本人改显示名/邮箱(账户设置里的本地档案)。
	// 与 user_update 分开:那是管理员改别人的账号行,actor 与改的东西都不是一回事。
	ActionProfileUpdate     = "profile_update"
	ActionSessionRevoke     = "session_revoke"
	ActionServiceOp         = "service_op"
	ActionContainerCreate   = "container_create"
	ActionImageOp           = "image_op"
	ActionStackOp           = "stack_op"
	ActionVolumeOp          = "volume_op"
	ActionNetworkOp         = "network_op"
	ActionSystemPrune       = "system_prune"
	ActionContainerTerminal = "container_terminal"
	ActionServerTerminal    = "server_terminal"
	// ActionServerCommand 是远程终端里 shell 真正执行掉的一条命令(远端提示符钩子回报,机制见
	// internal/httpapi/terminal_recorder.go)。开终端那一行只说明「谁连上了哪台机」,事后追责
	// 要看的是连上之后干了什么 —— 一条命令一行,按 session 串得起来。
	ActionServerCommand = "server_command"
	// 远程文件面板(「远程」弹窗下半屏):与 *_op 同族,一种资源一个 action,
	// 具体动作落在 detail.op(save/upload/download/mkdir/remove/rename)。
	// 单列而不并进 server_terminal,是因为事后追查「谁把这台机上的文件改了什么」
	// 要能只看文件线;detail 只带路径与字节数,绝不带正文(可能是密钥)。
	ActionServerFS = "server_fs"
	// 流水线模板 + 变量组(FR-8-13 复用基座)。
	ActionTemplateCreate = "template_create"
	ActionTemplateDelete = "template_delete"
	ActionTemplateApply  = "template_apply"
	ActionVarGroupCreate = "variable_group_create"
	ActionVarGroupUpdate = "variable_group_update"
	ActionVarGroupDelete = "variable_group_delete"
	// 自定义节点(复用库 Tier 2)。
	ActionCustomNodeCreate = "custom_node_create"
	ActionCustomNodeUpdate = "custom_node_update"
	ActionCustomNodeDelete = "custom_node_delete"
	// v6.2 阶段 7:构建环境管理 + 配置资源管理 + 用户管理。
	ActionBuildEnvCreate = "build_env_create"
	ActionBuildEnvUpdate = "build_env_update"
	ActionBuildEnvDelete = "build_env_delete"
	ActionBuildEnvToggle = "build_env_toggle" // enable/disable 切换
	ActionBuildEnvCheck  = "build_env_check"  // 手动/自动镜像检查
	// ActionBuildEnvPull 手动 pull 镜像。
	ActionBuildEnvPull = "build_env_pull"
	// ActionBuildEnvImport 整表导入(新建/覆盖/跳过逐行留痕在 detail 里)。
	ActionBuildEnvImport = "build_env_import"

	ActionConfigProfileCreate = "config_profile_create"
	ActionConfigProfileUpdate = "config_profile_update"
	ActionConfigProfileDelete = "config_profile_delete"
	ActionConfigProfileUpload = "config_profile_upload" // multipart 上传文件(新建)
	// ActionConfigProfileReplace 给已有配置资源重新上传文件:覆盖的是构建期真读的那份文件,
	// 与「新建一条」的风险不同,所以单独留痕而不是复用 upload。
	ActionConfigProfileReplace = "config_profile_replace"

	ActionUserInvite = "user_invite" // 创建邀请 token
	ActionUserCreate = "user_create" // 邀请注册落地
	ActionUserUpdate = "user_update" // 改描述/启用禁用
	ActionUserLogin  = "user_login"  // 口令登录成功
	// OAuth 登录与凭据接入是两件事,审计也分开:这条记的是「谁用哪个平台的哪个身份登进来了」。
	// 单独一个 action 是因为事后查入侵要能一眼把「外部身份换成的本地会话」和口令登录区分开。
	ActionOauthLogin = "oauth_login"
	// 绑定/解绑改的是「这个账号能由谁登录」,属于登录路径的变更,与改描述不同级。
	ActionIdentityBind   = "identity_bind"
	ActionIdentityUnbind = "identity_unbind"
	// v6.2 阶段 9:管理员直接建号 + 重置他人口令。与 password_change(改自己的)分开,
	// 因为「谁改的」和「被改的人有没有在场」完全不同。
	ActionUserAdminCreate   = "user_admin_create"
	ActionUserPasswordReset = "user_password_reset"
	ActionUserDisabled      = "user_disabled"
	ActionUserEnabled       = "user_enabled"
	// 删号单列一个 action,而不是复用 user_disabled:禁用随时能撤销,删除则连同会话
	// 一起抹掉,而且被删的那行再也读不回来 —— 审计里的 username 是事后唯一的凭据。
	ActionUserDelete = "user_delete"

	// v6.2 分组权限:分组本身 + 「项目/服务器归到哪个组」都要留痕。
	// 归组动作单独一个(而非并进 project_update),因为它是权限边界的变化,
	// 事后追查「谁把这个项目藏起来的」时是第一条要看的记录。
	ActionGroupCreate       = "group_create"
	ActionGroupUpdate       = "group_update"
	ActionGroupDelete       = "group_delete"
	ActionGroupMemberAdd    = "group_member_add"
	ActionGroupMemberRemove = "group_member_remove"
	ActionProjectReassign   = "project_reassign"
	// 服务器登记的是一台带 SSH 私钥的目标机,高危程度与项目归组同级,故同样留痕。
	ActionServerCreate   = "server_create"
	ActionServerUpdate   = "server_update"
	ActionServerDelete   = "server_delete"
	ActionServerReassign = "server_reassign"
	// K8s 集群登记:绑一条 kubeconfig 凭据,平台直连集群 API。与服务器同级高危。
	ActionKubeClusterCreate   = "kube_cluster_create"
	ActionKubeClusterUpdate   = "kube_cluster_update"
	ActionKubeClusterDelete   = "kube_cluster_delete"
	ActionKubeClusterReassign = "kube_cluster_reassign"
	ActionKubeClusterTest     = "kube_cluster_test"

	// P4 可配置角色:角色定义决定「谁能看见哪个入口」,改一个点就等于改一批人的权限,
	// 所以四个动作各自留痕(复制单独一个:它是「凭空多出一个角色」,和新建的追查路径不同)。
	ActionRoleCreate = "role_create"
	ActionRoleUpdate = "role_update"
	ActionRoleDelete = "role_delete"
	ActionRoleCopy   = "role_copy"

	// 证书这条线:登记/导入/替换/签发/删除各留一条,发布与取证书包单独留 —— 后两者分别是
	// 「往目标机写私钥并跑用户填的指令」和「把私钥放进出站响应」,事后追查泄露路径时
	// 只有这两类需要复盘到「谁在什么时候拿到了私钥」,不能藏在 cert_update 里。
	ActionCertCreate    = "cert_create"
	ActionCertImport    = "cert_import"
	ActionCertReplace   = "cert_replace"
	ActionCertIssue     = "cert_issue"
	ActionCertDelete    = "cert_delete"
	ActionCertAutoRenew = "cert_auto_renew"
	// ActionCertRenewWindow 改的是「到期前 N 天动手」这个阈值:它决定平台什么时候会去 CA
	// 那里下一单,和开关一样属于「会自己往外发请求」的配置变更,单独一条才查得到是谁改的。
	ActionCertRenewWindow     = "cert_renew_window"
	ActionCertTargetCreate    = "cert_target_create"
	ActionCertTargetUpdate    = "cert_target_update"
	ActionCertTargetDelete    = "cert_target_delete"
	ActionCertDeploy          = "cert_deploy"
	ActionCertAccountRegister = "cert_account_register"
	ActionCertBundleDownload  = "cert_bundle_download"
	// ActionCertCheck 是页面上的「立即检查」:它会就地续期并自动铺发,不只是一次只读扫描,
	// 所以要留痕 —— 事后看「这张证书什么时候被谁触发的续期」,这一条和后台扫描器能区分开。
	ActionCertCheck = "cert_check"

	// API 令牌(Bearer,机器客户端用)只记两件事:签发与撤销。
	// 每次用令牌调接口**不**逐条审计 —— 那些动作本来就以归属人的 actor 落在各自业务线上
	// (project_create / run_trigger_manual …),再补一层「谁用了哪把令牌」只会把流水刷满,
	// 而真正要复盘的是「这把令牌什么时候发出去、什么时候收回来」。
	// 撤销必须留痕且不只在列表里消失:出事后要能回答「这把泄露的令牌是哪天作废的」。
	ActionAPITokenCreate = "api_token_create"
	// 改范围必须留痕:端点收窄是「这把凭据能干什么」的变更,出事后第一个要问的就是
	// 「那会儿它是收窄的还是不限的、谁在什么时候解开的」。
	ActionAPITokenUpdate = "api_token_update"
	ActionAPITokenRevoke = "api_token_revoke"
)

// 目标类型枚举(供 TargetType 填值;非强制白名单,便于后续 story 扩展)。
const (
	TargetCredential = "credential"
	TargetProject    = "project"
	TargetTrigger    = "trigger"
	TargetRun        = "run"
	TargetAccount    = "account"
	TargetSession    = "session"
	TargetServer     = "server"
	TargetTemplate   = "template"
	TargetVarGroup   = "variable_group"
	TargetCustomNode = "custom_node"
	// v6.2 阶段 7:新增 target 类型。
	TargetBuildEnv       = "build_env"
	TargetConfigProfile  = "config_profile"
	TargetUser           = "user"
	TargetUserInvitation = "user_invitation"
	TargetResourceGroup  = "resource_group"
	TargetKubeCluster    = "kube_cluster"
	TargetRole           = "role"
	// 证书三档:证书本身、它的一个发布落点(cert_deployments 行)、一个 CA 的 ACME 账户。
	// 落点单独一类是因为它有自己的主机坐标与路径 —— 出事后要查的是「哪台机的哪个路径被写了」。
	TargetCert           = "cert"
	TargetCertDeployment = "cert_target"
	// TargetACMEAccount 与既有 TargetAccount(用户账户)不通用:这里记的是 CA 侧的账户密钥归属。
	TargetACMEAccount = "acme_account"
	// TargetAPIToken 是一枚 API 令牌。与 TargetAccount 分开:令牌能离开浏览器长期活着,
	// 出事时要单独把「哪把凭据」这条线筛出来,而不是混在账号流水里。
	TargetAPIToken = "api_token"
)

// Entry 是一条审计写入入参(冻结契约)。Detail 写库前过 Masker,绝不含明文 secret。
type Entry struct {
	Actor      string
	Action     string
	TargetType string
	TargetID   string
	Detail     map[string]any
	IP         string
}

// Record 是审计记录的对外只读视图(供查询端点返回)。
type Record struct {
	ID         string
	Timestamp  time.Time
	Actor      string
	Action     string
	TargetType string
	TargetID   string
	Detail     map[string]any
	IP         string
}

// ListFilter 是审计列表查询的过滤/分页参数。
type ListFilter struct {
	Limit      int    // 单页条数;<=0 或过大时归一到默认/上限
	Before     string // 游标:上一页最后一条的 id;空表示从头
	Action     string // 按 action 过滤;空表示不过滤
	TargetType string // 按 target_type 过滤;空表示不过滤
}

// ListResult 是审计列表查询结果。
type ListResult struct {
	Entries    []Record
	NextBefore string // 下一页游标(当前页最后一条 id);到底时为空
}

const (
	defaultLimit = 50
	maxLimit     = 200
)

// Recorder 定义审计写入与查询对外接口。
type Recorder interface {
	// Record 把 Entry 脱敏后追加到本地 append-only 审计表,并(若配置)同步到远端 sink。
	// 业务调用方应仅在业务成功后调用;返回 error 仅表示本地写入失败(供调用方记日志,
	// 不应据此回滚业务)。远端 sink 失败不影响返回值(降级记录)。
	Record(ctx context.Context, e Entry) error
	// List 按过滤 + 游标分页返回审计记录(timestamp DESC, id DESC)。只读。
	List(ctx context.Context, f ListFilter) (*ListResult, error)
}

// Sink 是可选远端审计 sink:本地写入成功后,审计条目同步推送一份到远端,
// 使「本地被删后远端仍完整」(AC-SEC-03)。实现须自行容错;Send 失败由 Recorder
// 降级吞掉(不阻断主操作)。
type Sink interface {
	// Send 推送一条已脱敏的审计记录到远端。
	Send(ctx context.Context, r Record) error
}

// recorder 是 store 支撑的 Recorder 实现。
type recorder struct {
	db     *sql.DB
	masker *mask.Masker
	sink   Sink // 可为 nil(无远端 sink)
	// onSinkError 仅供观测远端 sink 降级(默认 nil = 静默);测试可注入。
	onSinkError func(error)
}

// New 构造 Recorder。
//   - db:经参数化 SQL 触 audit_log。
//   - masker:Detail 落库前脱敏;为 nil 时退化为不脱敏(调用方应始终传入)。
//   - sink:可选远端 sink;为 nil 表示仅本地。
//
// 不做任何重活(无 init 副作用,避免抬高空载内存)。
func New(db *sql.DB, masker *mask.Masker, sink Sink) Recorder {
	return &recorder{db: db, masker: masker, sink: sink}
}

func (r *recorder) Record(ctx context.Context, e Entry) error {
	detail := e.Detail
	if r.masker != nil {
		detail = r.masker.ScrubMap(detail)
	}
	detailJSON := "{}"
	if detail != nil {
		b, err := json.Marshal(detail)
		if err != nil {
			return fmt.Errorf("audit: marshal detail: %w", err)
		}
		detailJSON = string(b)
	}

	id := uuid.NewString()
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339Nano)

	var targetID, ip any
	if e.TargetID != "" {
		targetID = e.TargetID
	}
	if e.IP != "" {
		ip = e.IP
	}

	_, err := r.db.ExecContext(ctx,
		`INSERT INTO audit_log (id, timestamp, actor, action, target_type, target_id, detail_json, ip, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, nowStr, e.Actor, e.Action, e.TargetType, targetID, detailJSON, ip, nowStr,
	)
	if err != nil {
		return fmt.Errorf("audit: insert: %w", err)
	}

	// 远端 sink:本地成功后同步一份;失败仅降级记录,不阻断主操作。
	if r.sink != nil {
		rec := Record{
			ID:         id,
			Timestamp:  now,
			Actor:      e.Actor,
			Action:     e.Action,
			TargetType: e.TargetType,
			TargetID:   e.TargetID,
			Detail:     detail,
			IP:         e.IP,
		}
		if serr := r.sink.Send(ctx, rec); serr != nil && r.onSinkError != nil {
			r.onSinkError(serr)
		}
	}
	return nil
}

func (r *recorder) List(ctx context.Context, f ListFilter) (*ListResult, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	var (
		conds []string
		args  []any
	)
	if f.Action != "" {
		conds = append(conds, "action = ?")
		args = append(args, f.Action)
	}
	if f.TargetType != "" {
		conds = append(conds, "target_type = ?")
		args = append(args, f.TargetType)
	}
	// 游标:before=上一页最后一条 id。以 (timestamp,id) 复合次序定位,稳定分页
	// (同毫秒多条时仍按 id 二级排序确定边界)。
	if f.Before != "" {
		var ts string
		err := r.db.QueryRowContext(ctx,
			`SELECT timestamp FROM audit_log WHERE id = ?`, f.Before,
		).Scan(&ts)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			// 游标失效:按从头处理(不报错)。
		case err != nil:
			return nil, fmt.Errorf("audit: resolve cursor: %w", err)
		default:
			conds = append(conds, "(timestamp < ? OR (timestamp = ? AND id < ?))")
			args = append(args, ts, ts, f.Before)
		}
	}

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	// 多取一条以判断是否还有下一页。
	args = append(args, limit+1)
	query := `SELECT id, timestamp, actor, action, target_type, target_id, detail_json, ip
	          FROM audit_log ` + where + `
	          ORDER BY timestamp DESC, id DESC
	          LIMIT ?`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("audit: list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	entries := make([]Record, 0, limit)
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, *rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("audit: iterate: %w", err)
	}

	res := &ListResult{Entries: entries}
	if len(entries) > limit {
		// 截断到 limit,游标取本页最后一条 id。
		res.Entries = entries[:limit]
		res.NextBefore = res.Entries[len(res.Entries)-1].ID
	}
	return res, nil
}

// scanRecord 把一行扫描为 Record;detail_json 反序列化为 map(空/坏数据 → 空 map)。
func scanRecord(rows *sql.Rows) (*Record, error) {
	var (
		rec        Record
		tsStr      string
		targetID   sql.NullString
		detailJSON string
		ip         sql.NullString
	)
	if err := rows.Scan(&rec.ID, &tsStr, &rec.Actor, &rec.Action, &rec.TargetType, &targetID, &detailJSON, &ip); err != nil {
		return nil, fmt.Errorf("audit: scan: %w", err)
	}
	ts, err := time.Parse(time.RFC3339Nano, tsStr)
	if err != nil {
		// 兼容历史无纳秒格式。
		ts, err = time.Parse(time.RFC3339, tsStr)
		if err != nil {
			return nil, fmt.Errorf("audit: parse timestamp: %w", err)
		}
	}
	rec.Timestamp = ts
	if targetID.Valid {
		rec.TargetID = targetID.String
	}
	if ip.Valid {
		rec.IP = ip.String
	}
	rec.Detail = map[string]any{}
	if strings.TrimSpace(detailJSON) != "" {
		_ = json.Unmarshal([]byte(detailJSON), &rec.Detail)
		if rec.Detail == nil {
			rec.Detail = map[string]any{}
		}
	}
	return &rec, nil
}
