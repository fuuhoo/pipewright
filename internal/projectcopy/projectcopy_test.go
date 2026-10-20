package projectcopy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/storetest"
	"github.com/google/uuid"
)

// 复制功能的测试口径:先把源项目「配满」(每张配置子表都塞一行可识别的值)+
// 「跑过」(有运行历史与投递记录),再复制一次,断言三件事:
// 配置全带上、历史一条不带、该换的标识(webhook token)真的换了。

func now() string { return time.Now().UTC().Format(time.RFC3339) }

type fixture struct {
	t   *testing.T
	db  *sql.DB
	ctx context.Context
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return &fixture{t: t, db: storetest.OpenDB(t), ctx: context.Background()}
}

func (f *fixture) exec(q string, args ...any) {
	f.t.Helper()
	if _, err := f.db.ExecContext(f.ctx, q, args...); err != nil {
		f.t.Fatalf("夹具落库失败: %v\nSQL: %s", err, q)
	}
}

func (f *fixture) queryRow(q string, args ...any) *sql.Row {
	return f.db.QueryRowContext(f.ctx, q, args...)
}

// addProject 建一个项目行(repoURL 传 "" 即纯发布项目,凭据留 NULL)。
func (f *fixture) addProject(name, repoURL, credentialID, groupID string) string {
	f.t.Helper()
	id := uuid.NewString()
	t := now()
	var cred any
	if credentialID != "" {
		cred = credentialID
	}
	f.exec(`INSERT INTO projects (id, name, repo_url, default_branch, credential_id,
	                              pac_enabled, pr_status_enabled, group_id, created_at, updated_at)
	            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, name, repoURL, "main", cred, 1, 0, groupID, t, t)
	return id
}

// addCredential 塞一把仓库凭据(ciphertext 是夹具,不经 vault)。
func (f *fixture) addCredential(name string) string {
	f.t.Helper()
	id := uuid.NewString()
	t := now()
	f.exec(`INSERT INTO credentials (id, name, type, scope, ciphertext, masked_value, created_at, updated_at)
	            VALUES (?, ?, 'git_token', 'personal', ?, ?, ?, ?)`,
		id, name, []byte("密文夹具"), "ghp_****1234", t, t)
	return id
}

func (f *fixture) addGroup(name string) string {
	f.t.Helper()
	id := uuid.NewString()
	t := now()
	f.exec(`INSERT INTO resource_groups (id, name, description, visibility, owner_id, created_by, created_at, updated_at)
	            VALUES (?, ?, '', 'private', 'u-owner', 'u-owner', ?, ?)`, id, name, t, t)
	return id
}

// addChannel 塞一条通知渠道:notification_routes.channel_id 有外键,夹具不能凭空写个 id。
func (f *fixture) addChannel(name string) string {
	f.t.Helper()
	id := uuid.NewString()
	t := now()
	f.exec(`INSERT INTO notification_channels (id, name, type, enabled, config_json, created_at, updated_at)
	            VALUES (?, ?, 'webhook', 1, '{}', ?, ?)`, id, name, t, t)
	return id
}

// fillConfig 给项目塞满「每项目一行」的配置,每张表的值都带项目名以便事后比对。
// downstream 非空时挂一条链式下游、channelID 非空时挂一条指向它的通知路由:两处都有外键,
// 必须指向真存在的行。
func (f *fixture) fillConfig(projectID, tag, downstream, channelID string) {
	f.t.Helper()
	t := now()
	f.exec(`INSERT INTO pipeline_configs (project_id, spec_json, spec_yaml, status, created_at, updated_at)
	            VALUES (?, ?, ?, 'active', ?, ?)`, projectID, `{"stages":[{"id":"s1"}]}`, tag+"-yaml", t, t)
	f.exec(`INSERT INTO pipeline_settings (project_id, build_json, environments_json, steps_json, created_at, updated_at)
	            VALUES (?, ?, ?, ?, ?, ?)`, projectID, `{"model":"dockerfile"}`, `[{"id":"env1"}]`, `[{"id":"st1"}]`, t, t)
	f.exec(`INSERT INTO pipeline_triggers (project_id, webhook_token, webhook_secret_ciphertext,
	                                       events_json, branch_mappings_json, unmatched_policy, path_filters_json, created_at, updated_at)
	            VALUES (?, ?, ?, ?, ?, 'ignore', ?, ?, ?)`,
		projectID, "tok-"+projectID, []byte("sealed-secret-"+tag), `{"push":true}`, `[{"id":"m1"}]`, `["a/**"]`, t, t)
	f.exec(`INSERT INTO project_crons (project_id, expression, branch, enabled, created_at, updated_at)
	            VALUES (?, ?, 'release/1', 1, ?, ?)`, projectID, "0 3 * * *", t, t)
	f.exec(`INSERT INTO project_parameters (project_id, defs_json, created_at, updated_at)
	            VALUES (?, '[{"key":"K"}]', ?, ?)`, projectID, t, t)
	f.exec(`INSERT INTO project_runners (project_id, runner_server_id, created_at, updated_at)
	            VALUES (?, 'srv-1', ?, ?)`, projectID, t, t)
	f.exec(`INSERT INTO project_concurrency (project_id, max_concurrent, created_at, updated_at)
	            VALUES (?, 3, ?, ?)`, projectID, t, t)
	f.exec(`INSERT INTO project_environments (project_id, chain_json, updated_at)
	            VALUES (?, '[{"name":"dev"}]', ?)`, projectID, t)
	f.exec(`INSERT INTO preview_configs (project_id, enabled, dns_provider_id, base_domain, created_at, updated_at)
	            VALUES (?, 1, 'dns-1', 'preview.example.com', ?, ?)`, projectID, t, t)

	// 每项目多行:环境变量(secret 行只存凭据引用)、通知路由与模板(无外键,必须显式搬)。
	f.exec(`INSERT INTO environment_variables (id, project_id, environment, var_key, value, is_secret, credential_id, updated_at)
	            VALUES (?, ?, 'dev', 'PLAIN', 'v1', 0, '', ?)`, uuid.NewString(), projectID, t)
	f.exec(`INSERT INTO environment_variables (id, project_id, environment, var_key, value, is_secret, credential_id, updated_at)
	            VALUES (?, ?, 'prod', 'TOKEN', '', 1, 'cred-secret', ?)`, uuid.NewString(), projectID, t)
	if channelID != "" {
		// notification_routes.channel_id 是 NOT NULL(见迁移 0018),两行都得指向真渠道。
		f.exec(`INSERT INTO notification_routes (id, project_id, event, channel_id, enabled, created_at)
		            VALUES (?, ?, 'build_failed', ?, 1, ?)`, uuid.NewString(), projectID, channelID, t)
		f.exec(`INSERT INTO notification_routes (id, project_id, event, channel_id, enabled, created_at)
		            VALUES (?, ?, 'build_succeeded', ?, 0, ?)`, uuid.NewString(), projectID, channelID, t)
	}
	// notification_templates.channel_id 可空(NULL = 该事件所有渠道通用),这里就留 NULL。
	f.exec(`INSERT INTO notification_templates (id, project_id, event, channel_id, title_template, body_template, created_at)
	            VALUES (?, ?, 'build_failed', NULL, ?, ?, ?)`, uuid.NewString(), projectID, "标题", "正文", t)

	// 链式下游:指向调用方给的外部项目,便于验证「跨批引用照搬」。
	if downstream != "" {
		f.exec(`INSERT INTO project_chain_targets (id, project_id, downstream_project_id, branch, enabled, created_at, updated_at)
		            VALUES (?, ?, ?, 'main', 1, ?, ?)`, uuid.NewString(), projectID, downstream, t, t)
	}
}

// fillHistory 给项目塞「发生过什么」:运行、投递、晋级记录、已分配的 PR 预览环境。
// 这些都不该被复制。
func (f *fixture) fillHistory(projectID string) {
	f.t.Helper()
	t := now()
	runID := uuid.NewString()
	f.exec(`INSERT INTO pipeline_runs (id, project_id, status, trigger_type, trigger_branch, created_at)
	            VALUES (?, ?, 'success', 'manual', 'main', ?)`, runID, projectID, t)
	f.exec(`INSERT INTO webhook_deliveries (id, project_id, delivery_id, event, branch, created_at)
	            VALUES (?, ?, 'dlv-1', 'push', 'main', ?)`, uuid.NewString(), projectID, t)
	f.exec(`INSERT INTO run_promotions (id, project_id, source_run_id, from_environment, target_environment, status, created_at)
	            VALUES (?, ?, ?, 'dev', 'prod', 'promoted', ?)`, uuid.NewString(), projectID, runID, t)
	f.exec(`INSERT INTO preview_envs (id, project_id, pipeline_id, pr_number, branch, subdomain, status, created_at)
	            VALUES (?, ?, '', 7, 'feat/x', 'pr-7.demo.example.com', 'active', ?)`, uuid.NewString(), projectID, t)
}

// count 数一张表里属于某项目的行(历史表按 project_id,配置表同理)。
func (f *fixture) count(table, projectID string) int {
	f.t.Helper()
	var n int
	col := "project_id"
	if table == "projects" {
		col = "id"
	}
	if err := f.queryRow("SELECT COUNT(1) FROM "+table+" WHERE "+col+" = ?", projectID).Scan(&n); err != nil {
		f.t.Fatalf("数 %s: %v", table, err)
	}
	return n
}

func (f *fixture) str(query string, args ...any) string {
	f.t.Helper()
	var s string
	if err := f.queryRow(query, args...).Scan(&s); err != nil {
		f.t.Fatalf("读 %q: %v", query, err)
	}
	return s
}

func (f *fixture) int(query string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.queryRow(query, args...).Scan(&n); err != nil {
		f.t.Fatalf("读 %q: %v", query, err)
	}
	return n
}

// TestCopyCarriesConfigAndDropsHistory 是主用例:配置全带上、历史全不带、token 换新。
func TestCopyCarriesConfigAndDropsHistory(t *testing.T) {
	f := newFixture(t)
	credID := f.addCredential("仓库令牌")
	outside := f.addProject("外部依赖", "", "", "")
	chanID := f.addChannel("飞书群")
	src := f.addProject("下单服务", "https://example.com/order.git", credID, "")
	f.fillConfig(src, "src", outside, chanID)
	f.fillHistory(src)

	svc := New(f.db)
	created, err := svc.Copy(f.ctx, Input{SourceID: src})
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if created.Name != "下单服务 副本" {
		t.Errorf("自动名 = %q, 想要 「下单服务 副本」", created.Name)
	}
	if created.GroupID != "" {
		t.Errorf("未归组项目复制后 group = %q, 想要沿用(空)", created.GroupID)
	}

	// 主行继承:仓库、分支、凭据引用、两个开关。
	var repo, branch, group string
	var pac, prStatus int
	var gotCred sql.NullString
	if err := f.queryRow(`SELECT repo_url, default_branch, credential_id, pac_enabled, pr_status_enabled, group_id
	                       FROM projects WHERE id = ?`, created.ID).
		Scan(&repo, &branch, &gotCred, &pac, &prStatus, &group); err != nil {
		t.Fatalf("读副本主行: %v", err)
	}
	if repo != "https://example.com/order.git" || branch != "main" {
		t.Errorf("仓库继承 = %q/%q", repo, branch)
	}
	if !gotCred.Valid || gotCred.String != credID {
		t.Errorf("凭据引用继承 = %q(valid=%v), 想要 %q", gotCred.String, gotCred.Valid, credID)
	}
	if pac != 1 || prStatus != 0 {
		t.Errorf("开关继承 pac=%d prStatus=%d, 想要 1/0", pac, prStatus)
	}

	// 每张配置子表都要有 1 行,且带得走源值(值在下面逐张比对)。
	for _, table := range []string{
		"pipeline_configs", "pipeline_settings", "pipeline_triggers", "project_crons",
		"project_parameters", "project_runners", "project_concurrency",
		"project_environments", "preview_configs",
	} {
		if got := f.count(table, created.ID); got != 1 {
			t.Errorf("%s 副本行数 = %d, 想要 1", table, got)
		}
	}
	if got := f.str(`SELECT spec_yaml FROM pipeline_configs WHERE project_id = ?`, created.ID); got != "src-yaml" {
		t.Errorf("spec_yaml = %q, 想要 src-yaml", got)
	}
	if got := f.str(`SELECT expression FROM project_crons WHERE project_id = ?`, created.ID); got != "0 3 * * *" {
		t.Errorf("cron = %q", got)
	}
	if got := f.str(`SELECT unmatched_policy FROM pipeline_triggers WHERE project_id = ?`, created.ID); got != "ignore" {
		t.Errorf("unmatched_policy = %q, 想要 ignore", got)
	}
	if got := f.str(`SELECT path_filters_json FROM pipeline_triggers WHERE project_id = ?`, created.ID); got != `["a/**"]` {
		t.Errorf("path_filters = %q", got)
	}
	if got := f.str(`SELECT base_domain FROM preview_configs WHERE project_id = ?`, created.ID); got != "preview.example.com" {
		t.Errorf("preview base_domain = %q", got)
	}

	// webhook:token 必须换新(撞 UNIQUE 就是整笔失败),签名密钥沿用(用户在 Git 侧填的那串不用重填)。
	srcToken := f.str(`SELECT webhook_token FROM pipeline_triggers WHERE project_id = ?`, src)
	dstToken := f.str(`SELECT webhook_token FROM pipeline_triggers WHERE project_id = ?`, created.ID)
	if srcToken == dstToken {
		t.Errorf("webhook_token 没换:两边都是 %q", dstToken)
	}
	if len(dstToken) != 32 || strings.Contains(dstToken, "-") {
		t.Errorf("新 token %q 不像 32 位 hex", dstToken)
	}
	var srcSecret, dstSecret []byte
	if err := f.queryRow(`SELECT webhook_secret_ciphertext FROM pipeline_triggers WHERE project_id = ?`, src).Scan(&srcSecret); err != nil {
		t.Fatalf("读源密钥: %v", err)
	}
	if err := f.queryRow(`SELECT webhook_secret_ciphertext FROM pipeline_triggers WHERE project_id = ?`, created.ID).Scan(&dstSecret); err != nil {
		t.Fatalf("读副本密钥: %v", err)
	}
	if string(srcSecret) != string(dstSecret) {
		t.Errorf("签名密钥没沿用: %q vs %q", srcSecret, dstSecret)
	}

	// 环境变量:两行都带上,secret 行仍只带凭据引用(值列本来就该是空串)。
	if got := f.count("environment_variables", created.ID); got != 2 {
		t.Errorf("环境变量副本行数 = %d, 想要 2", got)
	}
	if got := f.str(`SELECT credential_id FROM environment_variables WHERE project_id = ? AND var_key = 'TOKEN'`, created.ID); got != "cred-secret" {
		t.Errorf("secret 变量的凭据引用 = %q, 想要 cred-secret", got)
	}
	if got := f.str(`SELECT value FROM environment_variables WHERE project_id = ? AND var_key = 'TOKEN'`, created.ID); got != "" {
		t.Errorf("secret 变量带出了值 %q, 这里必须仍是空串", got)
	}

	// 通知覆盖:路由 2 行 + 模板 1 行;路由沿用同一渠道引用(不新造渠道),
	// 模板的 channel_id 原本 NULL,副本也得仍是 NULL(不能被写成空串)。
	if got := f.count("notification_routes", created.ID); got != 2 {
		t.Errorf("通知路由副本行数 = %d, 想要 2", got)
	}
	if got := f.count("notification_templates", created.ID); got != 1 {
		t.Errorf("通知模板副本行数 = %d, 想要 1", got)
	}
	if got := f.str(`SELECT channel_id FROM notification_routes WHERE project_id = ? AND event = 'build_succeeded'`, created.ID); got != chanID {
		t.Errorf("通知路由渠道引用 = %q, 想要沿用 %q", got, chanID)
	}
	if got := f.int(`SELECT enabled FROM notification_routes WHERE project_id = ? AND event = 'build_succeeded'`, created.ID); got != 0 {
		t.Errorf("通知路由 enabled = %d, 想要 0(源就是停用)", got)
	}
	var ch sql.NullString
	if err := f.queryRow(`SELECT channel_id FROM notification_templates WHERE project_id = ?`, created.ID).Scan(&ch); err != nil {
		t.Fatalf("读副本通知模板: %v", err)
	}
	if ch.Valid {
		t.Errorf("模板原本 NULL 的 channel_id 复制成了 %q", ch.String)
	}
	if got := f.str(`SELECT title_template FROM notification_templates WHERE project_id = ?`, created.ID); got != "标题" {
		t.Errorf("模板标题 = %q, 想要 标题", got)
	}

	// 历史一条都不带。
	for _, table := range []string{"pipeline_runs", "webhook_deliveries", "run_promotions", "preview_envs"} {
		if got := f.count(table, created.ID); got != 0 {
			t.Errorf("%s 被带进副本了(%d 行),复制只搬配置", table, got)
		}
	}
	// 源项目自己不受影响。
	if got := f.count("pipeline_runs", src); got != 1 {
		t.Errorf("源项目运行数 = %d, 想要 1", got)
	}
}

// TestCopyNameAvoidsTakenNames 验自动命名避让:已有「X 副本」就给「X 副本 2」。
func TestCopyNameAvoidsTakenNames(t *testing.T) {
	f := newFixture(t)
	src := f.addProject("网关", "", "", "")
	f.addProject("网关 副本", "", "", "")
	f.addProject("网关 副本 2", "", "", "")

	created, err := New(f.db).Copy(f.ctx, Input{SourceID: src})
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if created.Name != "网关 副本 3" {
		t.Errorf("自动名 = %q, 想要 网关 副本 3", created.Name)
	}
	// 回执说了不算,库里那行必须真的叫这个名:主行的 name 是唯一不照搬源行的列,
	// 一旦 INSERT 里漏掉它,副本就和源同名,列表里两份「网关」谁也分不清。
	if stored := f.str(`SELECT name FROM projects WHERE id = ?`, created.ID); stored != created.Name {
		t.Errorf("库里的副本名 = %q, 回执说 %q", stored, created.Name)
	}
}

// TestCopyExplicitNameAndSuffix 验显式名直接采用(同名不管,projects.name 本就无唯一约束),
// 后缀则替换默认的「副本」二字。
func TestCopyExplicitNameAndSuffix(t *testing.T) {
	f := newFixture(t)
	src := f.addProject("搜索", "", "", "")
	svc := New(f.db)

	c1, err := svc.Copy(f.ctx, Input{SourceID: src, Name: "搜索-灰度"})
	if err != nil {
		t.Fatalf("Copy 显式名: %v", err)
	}
	if c1.Name != "搜索-灰度" {
		t.Errorf("显式名 = %q", c1.Name)
	}
	c2, err := svc.Copy(f.ctx, Input{SourceID: src, Suffix: "-staging"})
	if err != nil {
		t.Fatalf("Copy 后缀: %v", err)
	}
	if c2.Name != "搜索-staging" {
		t.Errorf("后缀名 = %q, 想要 「搜索-staging」(后缀原样拼接,不额外补空格)", c2.Name)
	}
	for _, c := range []*Created{c1, c2} {
		if stored := f.str(`SELECT name FROM projects WHERE id = ?`, c.ID); stored != c.Name {
			t.Errorf("库里的副本名 = %q, 回执说 %q", stored, c.Name)
		}
	}
}

// TestCopyGroupRewiresChains 是整组复制的关键一笔:批内 A→B 的链式下游要改指 A'→B',
// 指向批外项目的链维持原样。
func TestCopyGroupRewiresChains(t *testing.T) {
	f := newFixture(t)
	outside := f.addProject("外部依赖", "", "", "")

	g := f.addGroup("交易组")
	a := f.addProject("A 服务", "", "", g)
	b := f.addProject("B 服务", "", "", g)
	// A、B 各带一条指向批外「外部依赖」的链(fillConfig 塞的),A 再多一条指向批内 B 的链。
	f.fillConfig(a, "a", outside, "")
	f.fillConfig(b, "b", outside, "")
	f.exec(`INSERT INTO project_chain_targets (id, project_id, downstream_project_id, branch, enabled, created_at, updated_at)
	            VALUES (?, ?, ?, '', 1, ?, ?)`, uuid.NewString(), a, b, now(), now())

	svc := New(f.db)
	made, err := svc.CopyMany(f.ctx, []Input{{SourceID: a}, {SourceID: b}})
	if err != nil {
		t.Fatalf("CopyMany: %v", err)
	}
	if len(made) != 2 {
		t.Fatalf("产出 = %d, 想要 2", len(made))
	}
	var copyA, copyB string
	for _, c := range made {
		switch c.Name {
		case "A 服务 副本":
			copyA = c.ID
		case "B 服务 副本":
			copyB = c.ID
		default:
			t.Fatalf("副本名不在预期里: %q", c.Name)
		}
		if c.GroupID != g {
			t.Errorf("副本没沿用分组: group = %q, 想要 %q", c.GroupID, g)
		}
	}
	if copyA == "" || copyB == "" {
		t.Fatalf("两个副本没都建出来: A=%q B=%q", copyA, copyB)
	}

	// 全表读一遍:既验副本的链,也验源的链没被改写。
	downs := f.chainEdges()
	// A' 应有两条下游:B'(批内改写)与外部依赖(跨批照搬)。
	if !downs.has(copyA, copyB) {
		t.Errorf("A'→B' 没接上,链 = %v", downs)
	}
	if !downs.has(copyA, outside) {
		t.Errorf("跨批链没照搬,缺少 %s→外部依赖", copyA)
	}
	// B' 只有一条指向外部依赖的链。
	if !downs.has(copyB, outside) {
		t.Errorf("B' 的跨批链丢了")
	}
	if n := downs.countOf(copyA); n != 2 {
		t.Errorf("A' 链数 = %d, 想要 2", n)
	}
	if n := downs.countOf(copyB); n != 1 {
		t.Errorf("B' 链数 = %d, 想要 1", n)
	}
	// 源项目两侧的链一条都不能少、一条都不能改。
	if !downs.has(a, b) || !downs.has(a, outside) || !downs.has(b, outside) {
		t.Errorf("源项目的链被动过,链 = %v", downs)
	}
}

// chainEdge 是一条链式下游的「谁指向谁」。
type chainEdge struct{ from, to string }

// chainSet 是全表链式下游的边集,便于同时比对源侧与副本侧。
type chainSet map[chainEdge]bool

func (c chainSet) has(from, to string) bool { return c[chainEdge{from, to}] }

func (c chainSet) countOf(from string) int {
	var n int
	for e := range c {
		if e.from == from {
			n++
		}
	}
	return n
}

// chainEdges 读出全表链式下游。
func (f *fixture) chainEdges() chainSet {
	f.t.Helper()
	out := chainSet{}
	rows, err := f.db.QueryContext(f.ctx, `SELECT project_id, downstream_project_id FROM project_chain_targets`)
	if err != nil {
		f.t.Fatalf("读链式下游: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var e chainEdge
		if err := rows.Scan(&e.from, &e.to); err != nil {
			f.t.Fatalf("扫链式下游: %v", err)
		}
		out[e] = true
	}
	if err := rows.Err(); err != nil {
		f.t.Fatalf("遍历链式下游: %v", err)
	}
	return out
}

// TestCopyTargetGroupAndErrors 覆盖目标分组与三类入参错误。
func TestCopyTargetGroupAndErrors(t *testing.T) {
	f := newFixture(t)
	src := f.addProject("订单", "", "", "")
	g := f.addGroup("新组")
	svc := New(f.db)

	toGroup := g
	c, err := svc.Copy(f.ctx, Input{SourceID: src, GroupID: &toGroup})
	if err != nil {
		t.Fatalf("复制到分组: %v", err)
	}
	if c.GroupID != g {
		t.Errorf("目标分组没生效: %q", c.GroupID)
	}

	ungrouped := ""
	src2 := f.addProject("库存", "", "", g)
	c2, err := svc.Copy(f.ctx, Input{SourceID: src2, GroupID: &ungrouped})
	if err != nil {
		t.Fatalf("复制到未归组: %v", err)
	}
	if c2.GroupID != "" {
		t.Errorf("指向未归组却得到 %q", c2.GroupID)
	}

	if _, err := svc.Copy(f.ctx, Input{SourceID: src, GroupID: ptr("no-such-group")}); !errors.Is(err, ErrGroupNotFound) {
		t.Errorf("目标分组不存在 err = %v, 想要 ErrGroupNotFound", err)
	}
	if _, err := svc.Copy(f.ctx, Input{SourceID: "no-such-project"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("源不存在 err = %v, 想要 ErrNotFound", err)
	}
	if _, err := svc.CopyMany(f.ctx, nil); !errors.Is(err, ErrEmptyBatch) {
		t.Errorf("空批次 err = %v, 想要 ErrEmptyBatch", err)
	}
	// 名位被占满时要有界退出,不能无限转。
	f.takeNames("订单 副本", maxNameAttempts)
	if _, err := svc.Copy(f.ctx, Input{SourceID: src}); !errors.Is(err, ErrTooManyCopies) {
		t.Errorf("名位耗尽 err = %v, 想要 ErrTooManyCopies", err)
	}
}

// takeNames 把「首个名 + 递增编号」连续 N 个名字全占住,用来逼命名避让走到上界。
func (f *fixture) takeNames(first string, n int) {
	f.t.Helper()
	tx, err := f.db.BeginTx(f.ctx, nil)
	if err != nil {
		f.t.Fatalf("开夹具事务: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(f.ctx,
		`INSERT INTO projects (id, name, repo_url, default_branch, group_id, created_at, updated_at)
		 VALUES (?, ?, '', '', '', ?, ?)`)
	if err != nil {
		f.t.Fatalf("准备夹具插入: %v", err)
	}
	defer func() { _ = stmt.Close() }()
	t := now()
	for i := 1; i <= n; i++ {
		name := first
		if i > 1 {
			name = fmt.Sprintf("%s %d", first, i)
		}
		if _, err := stmt.ExecContext(f.ctx, uuid.NewString(), name, t, t); err != nil {
			f.t.Fatalf("占名字 %q: %v", name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		f.t.Fatalf("提交夹具事务: %v", err)
	}
}

// TestCopyProjectWithoutRepo 验纯发布项目(无仓库、凭据 NULL)也复制得动 ——
// 这类项目现在是一等公民,复制不能因为没仓库就建不出来。
func TestCopyProjectWithoutRepo(t *testing.T) {
	f := newFixture(t)
	src := f.addProject("只发布", "", "", "")
	svc := New(f.db)
	c, err := svc.Copy(f.ctx, Input{SourceID: src})
	if err != nil {
		t.Fatalf("复制纯发布项目: %v", err)
	}
	var repo string
	var cred sql.NullString
	if err := f.queryRow(`SELECT repo_url, credential_id FROM projects WHERE id = ?`, c.ID).Scan(&repo, &cred); err != nil {
		t.Fatalf("读副本: %v", err)
	}
	if repo != "" || cred.Valid {
		t.Errorf("纯发布副本带上仓库/凭据: %q / %q", repo, cred.String)
	}
	// 源没有配置行时,副本也不该被凭空造出一行(惰性默认配置归各页自己管)。
	for _, table := range []string{"pipeline_configs", "pipeline_settings", "pipeline_triggers", "project_crons"} {
		if got := f.count(table, c.ID); got != 0 {
			t.Errorf("%s 凭空多出 %d 行,复制不该替用户建默认配置", table, got)
		}
	}
}

// TestCopyRollsBackWholeBatch 验整组的事务性:批次里有一条源不存在,前面已经建出来的
// 副本也必须一起回滚 —— 不能留「三个里少一个」。
func TestCopyRollsBackWholeBatch(t *testing.T) {
	f := newFixture(t)
	a := f.addProject("甲", "", "", "")
	if _, err := New(f.db).CopyMany(f.ctx, []Input{{SourceID: a}, {SourceID: "missing"}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("批次 err = %v, 想要 ErrNotFound", err)
	}
	var n int
	if err := f.queryRow(`SELECT COUNT(1) FROM projects WHERE name = ?`, "甲 副本").Scan(&n); err != nil {
		t.Fatalf("数回滚后的副本: %v", err)
	}
	if n != 0 {
		t.Errorf("整组复制失败后仍留下 %d 个「甲 副本」,事务没兜住", n)
	}
}

func ptr(s string) *string { return &s }
