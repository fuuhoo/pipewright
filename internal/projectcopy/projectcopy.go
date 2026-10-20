// Package projectcopy 复制项目:把「配置」整套搬给一个新项目,历史一概不带。
//
// 为什么要有这个包:用户要把一条调好的流水线挪去别处用(新环境、新服务、给别的组复制一整套),
// 重配一遍的成本远高于「先复制再改两笔」。本包只负责「搬」,不解释任何一列的含义 ——
// 所有子表都按原值复制,语义仍归各自的领域包。
//
// 三条硬规矩:
//   - 只搬配置,不搬历史:pipeline_runs 及其子表、webhook_deliveries、run_promotions、
//     preview_envs 记的是「发生过什么」,复制出来的项目该有自己的空历史。
//   - 一处都不能出现明文密钥:子表里的 secret 一律是 credentials 表的引用(credential_id)
//     或 vault 密文,原样搬走引用即可,绝不解封再封。
//   - 一个事务:整组复制要么全成要么全不,不能留下「三个项目里少了一个」的半成品。
package projectcopy

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// DefaultSuffix 是自动命名时接在源项目名后面的字面量(「下单服务」→「下单服务 副本」)。
// 它是「原样拼接」的尾巴而不是用空格连接的词,所以自带前导空格:用户想写 -staging
// 就得到「服务-staging」,规则只有一条,不必按首字符猜该不该加空格。
const DefaultSuffix = " 副本"

// 领域错误:HTTP 层据此映射状态码。
var (
	// ErrNotFound 表示源项目不存在。
	ErrNotFound = errors.New("projectcopy: project not found")
	// ErrGroupNotFound 表示目标分组不存在。projects.group_id 有意不建外键(见迁移 0056),
	// 悬挂引用会让 access 按最私有一档收敛,结果是全组人打不开这个副本,所以写入前必须自挡。
	ErrGroupNotFound = errors.New("projectcopy: group not found")
	// ErrEmptyBatch 表示一个要复制的项目都没给。
	ErrEmptyBatch = errors.New("projectcopy: nothing to copy")
	// ErrTooManyCopies 表示「源名 + 后缀」的同名副本多到数不过来(防御性上限)。
	ErrTooManyCopies = errors.New("projectcopy: too many copies of this name")
)

// Input 是复制一个项目的入参。
type Input struct {
	SourceID string
	// Name 非空 = 直接用这个名字(单个复制的弹窗里用户会改);空 = 源名 + Suffix 自动生成。
	Name string
	// Suffix 仅在 Name 为空时生效,原样接在源名后面(自带分隔符,见 DefaultSuffix);
	// 空 = DefaultSuffix。整组复制用它给每个副本统一加尾巴。
	Suffix string
	// GroupID 指向目标分组;nil = 沿用源项目的分组,指向 "" = 落到未归组。
	GroupID *string
}

// Created 是一次复制的产物。
type Created struct {
	SourceID string
	ID       string
	Name     string
	GroupID  string
}

// Service 是复制服务。与 project 包同律:直接持 *sql.DB,不做仓储抽象。
type Service struct {
	db *sql.DB
}

// New 构造复制服务。
func New(db *sql.DB) *Service { return &Service{db: db} }

// Copy 复制单个项目(等价于 CopyMany 一条)。
func (s *Service) Copy(ctx context.Context, in Input) (*Created, error) {
	out, err := s.CopyMany(ctx, []Input{in})
	if err != nil {
		return nil, err
	}
	c := out[0]
	return &c, nil
}

// CopyMany 在一个事务里复制多个项目。
//
// 分两趟:先把所有新项目行落库并攒出 源 ID → 新 ID 的映射,再搬各自的配置子表。
// 分趟是因为链式触发(project_chain_targets)要改写:一批里的 A→B,复制完应当是 A'→B',
// 而不是让新项目和老项目共用一条下游链(同一件事被跑两遍)。
func (s *Service) CopyMany(ctx context.Context, ins []Input) ([]Created, error) {
	if len(ins) == 0 {
		return nil, ErrEmptyBatch
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("projectcopy: 开事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC().Format(time.RFC3339)
	made := make([]Created, 0, len(ins))
	idMap := make(map[string]string, len(ins))
	for _, in := range ins {
		c, err := copyProjectRow(ctx, tx, in, now)
		if err != nil {
			return nil, err
		}
		made = append(made, *c)
		idMap[c.SourceID] = c.ID
	}
	for _, c := range made {
		if err := copyChildren(ctx, tx, c.SourceID, c.ID, idMap, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("projectcopy: 提交: %w", err)
	}
	return made, nil
}

// copyProjectRow 落新项目主行:仓库地址、默认分支、凭据引用、两个开关都原样继承。
// 不重做 ls-remote 校验:源项目本来就能拉,复制不该因为这一刻网络抖动而建不出副本。
func copyProjectRow(ctx context.Context, tx *sql.Tx, in Input, now string) (*Created, error) {
	var srcID, srcName, srcGroup string
	err := tx.QueryRowContext(ctx, `SELECT id, name, group_id FROM projects WHERE id = ?`,
		strings.TrimSpace(in.SourceID)).Scan(&srcID, &srcName, &srcGroup)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("projectcopy: 读源项目: %w", err)
	}

	groupID := srcGroup
	if in.GroupID != nil {
		groupID = strings.TrimSpace(*in.GroupID)
	}
	if groupID != "" && !groupExists(ctx, tx, groupID) {
		return nil, ErrGroupNotFound
	}

	name, err := resolveName(ctx, tx, in, srcName)
	if err != nil {
		return nil, err
	}

	id := uuid.NewString()
	// name 是这条 INSERT 里唯一不照搬源行的列:照搬会让副本和源同名,
	// 上面算好的避让序号全丢(库里两份「甲」、回执却说「甲 副本」「甲 副本 2」)。
	// credential_id 可空(纯发布项目),其余列用 SELECT 原样带过去:NULL 就是 NULL,不必在 Go 里区分。
	res, err := tx.ExecContext(ctx,
		`INSERT INTO projects (id, name, repo_url, default_branch, credential_id,
		                       pac_enabled, pr_status_enabled, group_id, created_at, updated_at)
		 SELECT ?, ?, repo_url, default_branch, credential_id,
		        pac_enabled, pr_status_enabled, ?, ?, ?
		 FROM projects WHERE id = ?`,
		id, name, groupID, now, now, srcID,
	)
	if err != nil {
		return nil, fmt.Errorf("projectcopy: 建副本项目: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return &Created{SourceID: srcID, ID: id, Name: name, GroupID: groupID}, nil
}

func groupExists(ctx context.Context, tx *sql.Tx, groupID string) bool {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM resource_groups WHERE id = ?`, groupID).Scan(&one)
	return err == nil
}

// resolveName 定名:用户给了就用他的;没给就把后缀原样接在源名后面,撞名再追 2、3……
// projects.name 没有唯一约束(存量里就有同名),这里避让只是为了列表和搜索分得清谁是谁。
// 查重跑在同一事务里,所以整组复制时本批刚建出来的名字也算「已被占」。
func resolveName(ctx context.Context, tx *sql.Tx, in Input, srcName string) (string, error) {
	if explicit := strings.TrimSpace(in.Name); explicit != "" {
		return explicit, nil
	}
	suffix := strings.TrimRight(in.Suffix, " \t")
	if suffix == "" {
		suffix = DefaultSuffix
	}
	base := srcName + suffix
	for i := 1; i <= maxNameAttempts; i++ {
		candidate := base
		if i > 1 {
			candidate = fmt.Sprintf("%s %d", base, i)
		}
		taken, err := nameTaken(ctx, tx, candidate)
		if err != nil {
			return "", err
		}
		if !taken {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%w: %s", ErrTooManyCopies, base)
}

const maxNameAttempts = 1000

func nameTaken(ctx context.Context, tx *sql.Tx, name string) (bool, error) {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM projects WHERE name = ?`, name).Scan(&n); err != nil {
		return false, fmt.Errorf("projectcopy: 查重名: %w", err)
	}
	return n > 0, nil
}

// tableCopy 描述一张「每项目一行」的配置表怎么搬。
type tableCopy struct {
	// head 是复制时不能照搬、必须现造值的列(列名 → 值),顺序插在 project_id 之后。
	head []namedArg
	// cols 是原样搬走的列名。
	cols []string
	// hasCreated 表示这张表有 created_at 列(需要填当下时刻)。
	hasCreated bool
}

type namedArg struct {
	col string
	val any
}

// copyChildren 把一个项目的全部配置子表搬到新项目名下。
func copyChildren(ctx context.Context, tx *sql.Tx, srcID, dstID string, idMap map[string]string, now string) error {
	// 触发配置单列出来:webhook_token 带 UNIQUE,照搬必然撞,必须新造;签名密钥是密文列、
	// 没有唯一性,原样带走最省事 —— 用户在 Git 侧填的那串密钥不用重填,只换回调地址。
	if err := copySingleRow(ctx, tx, srcID, dstID, now, tableCopy{
		head:       []namedArg{{"webhook_token", newWebhookToken()}},
		cols:       []string{"webhook_secret_ciphertext", "events_json", "branch_mappings_json", "unmatched_policy", "path_filters_json"},
		hasCreated: true,
	}, "pipeline_triggers"); err != nil {
		return err
	}
	for _, t := range []struct {
		table string
		copy  tableCopy
	}{
		{"pipeline_configs", tableCopy{cols: []string{"spec_json", "spec_yaml", "status"}, hasCreated: true}},
		{"pipeline_settings", tableCopy{cols: []string{"build_json", "environments_json", "steps_json"}, hasCreated: true}},
		{"project_crons", tableCopy{cols: []string{"expression", "branch", "enabled"}, hasCreated: true}},
		{"project_parameters", tableCopy{cols: []string{"defs_json"}, hasCreated: true}},
		{"project_runners", tableCopy{cols: []string{"runner_server_id"}, hasCreated: true}},
		{"project_concurrency", tableCopy{cols: []string{"max_concurrent"}, hasCreated: true}},
		{"project_environments", tableCopy{cols: []string{"chain_json"}}},
		// preview_configs 没有指向 projects 的外键(见迁移 0047),不显式搬就会永远留在源项目名下。
		{"preview_configs", tableCopy{cols: []string{"enabled", "dns_provider_id", "base_domain"}, hasCreated: true}},
	} {
		if err := copySingleRow(ctx, tx, srcID, dstID, now, t.copy, t.table); err != nil {
			return err
		}
	}
	return copyMultiRow(ctx, tx, srcID, dstID, idMap, now)
}

// copySingleRow 搬一张每项目一行的表:INSERT ... SELECT,源没有行就一条都不插。
// 这些行多数是「打开过那页才惰性建出来」的,给副本凭空造一行是替用户做主。
func copySingleRow(ctx context.Context, tx *sql.Tx, srcID, dstID, now string, t tableCopy, table string) error {
	cols := []string{"project_id"}
	sel := []string{"?"}
	args := []any{dstID}
	for _, h := range t.head {
		cols = append(cols, h.col)
		sel = append(sel, "?")
		args = append(args, h.val)
	}
	cols = append(cols, t.cols...)
	sel = append(sel, t.cols...)
	if t.hasCreated {
		cols = append(cols, "created_at")
		sel = append(sel, "?")
		args = append(args, now)
	}
	cols = append(cols, "updated_at")
	sel = append(sel, "?")
	args = append(args, now)
	args = append(args, srcID) // WHERE project_id = ?

	q := fmt.Sprintf(`INSERT INTO %s (%s) SELECT %s FROM %s WHERE project_id = ?`,
		table, strings.Join(cols, ", "), strings.Join(sel, ", "), table)
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("projectcopy: 复制 %s: %w", table, err)
	}
	return nil
}

// copyMultiRow 搬三处「每项目多行」的数据:环境变量、链式下游、通知路由与模板。
// 它们的行有各自的主键,必须在 Go 里逐行造新 ID,不能 INSERT ... SELECT 一把梭。
func copyMultiRow(ctx context.Context, tx *sql.Tx, srcID, dstID string, idMap map[string]string, now string) error {
	// 1) 逐环境作用域变量:UNIQUE(project_id, environment, var_key),每行换 uuid。
	type envVar struct {
		env, key, value, cred string
		secret                int
	}
	var vars []envVar
	if err := eachRow(ctx, tx,
		`SELECT environment, var_key, value, is_secret, credential_id FROM environment_variables WHERE project_id = ?`,
		srcID, func(row *sql.Rows) error {
			var v envVar
			if err := row.Scan(&v.env, &v.key, &v.value, &v.secret, &v.cred); err != nil {
				return fmt.Errorf("projectcopy: 扫环境变量: %w", err)
			}
			vars = append(vars, v)
			return nil
		}); err != nil {
		return err
	}
	for _, v := range vars {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO environment_variables (id, project_id, environment, var_key, value, is_secret, credential_id, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			uuid.NewString(), dstID, v.env, v.key, v.value, v.secret, v.cred, now); err != nil {
			return fmt.Errorf("projectcopy: 复制环境变量: %w", err)
		}
	}

	// 2) 链式触发:下游在这批副本里就改指它的新项目,否则维持原下游(跨批引用照搬)。
	type chain struct {
		down, branch string
		enabled      int
	}
	var chains []chain
	if err := eachRow(ctx, tx,
		`SELECT downstream_project_id, branch, enabled FROM project_chain_targets WHERE project_id = ?`,
		srcID, func(row *sql.Rows) error {
			var c chain
			if err := row.Scan(&c.down, &c.branch, &c.enabled); err != nil {
				return fmt.Errorf("projectcopy: 扫链式下游: %w", err)
			}
			chains = append(chains, c)
			return nil
		}); err != nil {
		return err
	}
	for _, c := range chains {
		down := c.down
		if mapped, ok := idMap[c.down]; ok {
			down = mapped
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO project_chain_targets (id, project_id, downstream_project_id, branch, enabled, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			uuid.NewString(), dstID, down, c.branch, c.enabled, now, now); err != nil {
			return fmt.Errorf("projectcopy: 复制链式下游: %w", err)
		}
	}

	// 3) 通知路由与模板:这两张表按 project_id 做「每流水线覆盖」(Story 5-4),同样没有
	//    指向 projects 的外键 —— 不搬就是副本静默回落到全局默认,和源项目行为不一致。
	//    routes.channel_id 是 NOT NULL(迁移 0018),templates.channel_id 可空(迁移 0019)。
	type route struct {
		event, channel string
		enabled        int
	}
	var routes []route
	if err := eachRow(ctx, tx,
		`SELECT event, channel_id, enabled FROM notification_routes WHERE project_id = ?`, srcID,
		func(row *sql.Rows) error {
			var r route
			if err := row.Scan(&r.event, &r.channel, &r.enabled); err != nil {
				return fmt.Errorf("projectcopy: 扫通知路由: %w", err)
			}
			routes = append(routes, r)
			return nil
		}); err != nil {
		return err
	}
	for _, r := range routes {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO notification_routes (id, project_id, event, channel_id, enabled, created_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			uuid.NewString(), dstID, r.event, r.channel, r.enabled, now); err != nil {
			return fmt.Errorf("projectcopy: 复制通知路由: %w", err)
		}
	}

	type tmpl struct {
		event       string
		channel     sql.NullString
		title, body string
	}
	var tmpls []tmpl
	if err := eachRow(ctx, tx,
		`SELECT event, channel_id, title_template, body_template FROM notification_templates WHERE project_id = ?`,
		srcID, func(row *sql.Rows) error {
			var v tmpl
			if err := row.Scan(&v.event, &v.channel, &v.title, &v.body); err != nil {
				return fmt.Errorf("projectcopy: 扫通知模板: %w", err)
			}
			tmpls = append(tmpls, v)
			return nil
		}); err != nil {
		return err
	}
	for _, v := range tmpls {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO notification_templates (id, project_id, event, channel_id, title_template, body_template, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			uuid.NewString(), dstID, v.event, nullArg(v.channel), v.title, v.body, now); err != nil {
			return fmt.Errorf("projectcopy: 复制通知模板: %w", err)
		}
	}
	return nil
}

// eachRow 把一条查询跑完并把结果交给 scan。先收齐再插入:不让「读这张表」的游标
// 和「往这张表写」挤在同一个事务连接上交错,免得踩到驱动的自读自写限制。
func eachRow(ctx context.Context, tx *sql.Tx, query string, arg any, scan func(*sql.Rows) error) error {
	rows, err := tx.QueryContext(ctx, query, arg)
	if err != nil {
		return fmt.Errorf("projectcopy: 查询: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("projectcopy: 遍历: %w", err)
	}
	return nil
}

func nullArg(s sql.NullString) any {
	if s.Valid {
		return s.String
	}
	return nil
}

// newWebhookToken 造一个 webhook 回调路径标识(与 internal/trigger 同格式:16 字节 hex)。
func newWebhookToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 在本机契约下不会返错;真出错也得分个唯一值出来,否则 UNIQUE 必撞。
		return hex.EncodeToString([]byte(fmt.Sprintf("pw%d", time.Now().UnixNano())))
	}
	return hex.EncodeToString(b)
}
