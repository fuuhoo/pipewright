package httpapi

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/projectcopy"
	"github.com/google/uuid"
)

// project_copy_test.go —— 「复制项目」两个端点的 HTTP 契约。
//
// 复制的内容对不对由 internal/projectcopy 的包测试逐表盯着,这里只管四件事:
//  1. 权限:单个复制靠 accessGuard 认 URL,批量复制靠 handler 逐条判 —— 两条路都要能拦住局外人和只读角色。
//  2. 归组三态:不传 = 沿用源组,传 "" = 落到未归组,传某组 = 要管得住那个组。
//  3. 错误码:领域错误映射成契约码,不被伪装成别的状态。
//  4. 审计:单个逐条记、一批记一行;detail 只有标识与名字。
//
// 夹具复用 setupGuard:它已经备好「未归组 / 私有组内 / 公开组内」三个项目
// 和管理员、组长、成员、局外人四个身份。

// copyReply 是一次复制调用的解析结果(成功是数组,失败是 {error:{code,message}})。
type copyReply struct {
	status int
	code   string
	copies []copiedProjectDTO
}

func doCopy(t *testing.T, c *http.Client, method, url, csrf, body string) copyReply {
	t.Helper()
	resp := doJSON(t, c, method, url, csrf, body)
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var errOut struct {
		Error *errDetail `json:"error"`
	}
	_ = json.Unmarshal(raw, &errOut)
	if errOut.Error != nil {
		return copyReply{status: resp.StatusCode, code: errOut.Error.Code}
	}
	var items []copiedProjectDTO
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatalf("解析复制回执失败 (%s %s → %d): %v / %s", method, url, resp.StatusCode, err, raw)
	}
	return copyReply{status: resp.StatusCode, copies: items}
}

// projectFields 是副本主行该继承的字段;凭据只比引用,不比明文(AC-SEC)。
type projectFields struct {
	name, repoURL, branch, credID, groupID string
	credNull                               bool
}

func readProject(t *testing.T, db *sql.DB, id string) (projectFields, bool) {
	t.Helper()
	var f projectFields
	var cred sql.NullString
	err := db.QueryRow(
		`SELECT name, repo_url, default_branch, credential_id, group_id FROM projects WHERE id = ?`, id).
		Scan(&f.name, &f.repoURL, &f.branch, &cred, &f.groupID)
	if err != nil {
		if err == sql.ErrNoRows {
			return f, false
		}
		t.Fatalf("读项目 %s: %v", id, err)
	}
	f.credNull = !cred.Valid
	f.credID = cred.String
	return f, true
}

func countProjects(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM projects`).Scan(&n); err != nil {
		t.Fatalf("数项目: %v", err)
	}
	return n
}

// copyAuditRows 取某个 action 的审计行(detail 已解好)。
func copyAuditRows(t *testing.T, db *sql.DB, action string) []struct {
	Actor    string
	TargetID string
	Detail   map[string]any
} {
	t.Helper()
	rows, err := db.Query(`SELECT actor, target_id, detail_json FROM audit_log WHERE action = ? ORDER BY timestamp, id`, action)
	if err != nil {
		t.Fatalf("查审计 %s: %v", action, err)
	}
	defer func() { _ = rows.Close() }()
	var out []struct {
		Actor    string
		TargetID string
		Detail   map[string]any
	}
	for rows.Next() {
		var actor, targetID, detail string
		if err := rows.Scan(&actor, &targetID, &detail); err != nil {
			t.Fatalf("扫审计行: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(detail), &m); err != nil {
			t.Fatalf("审计 detail 不是 JSON(%s): %v", detail, err)
		}
		out = append(out, struct {
			Actor    string
			TargetID string
			Detail   map[string]any
		}{actor, targetID, m})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历审计行: %v", err)
	}
	return out
}

// addViewer 建一个只读角色('viewer')账号并登录。
// 需要它是因为 0063 播种的 'user' 角色带 project.edit —— 用 setupGuard 那三个账号
// 试不出「角色功能点不够」这一档,只能再拿一个没有 edit 的角色。
func addViewer(t *testing.T, env *guardEnv) (*http.Client, string) {
	t.Helper()
	hash, err := auth.HashPassword(guardPass)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if _, err := env.db.Exec(
		`INSERT INTO users (id, username, password_hash, role, enabled, created_at, updated_at)
		 VALUES (?, ?, ?, 'viewer', 1, ?, ?)`,
		uuid.NewString(), "readonly", hash, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("seed viewer: %v", err)
	}
	c := newTestClient(t)
	csrf := loginAs(t, c, env.srv.URL, "readonly", guardPass)
	return c, csrf
}

func guardCopyURL(env *guardEnv, id string) string {
	return env.srv.URL + "/api/projects/" + id + "/copy"
}

const batchCopyURL = "/api/project-copies"

// seedGuardPipelineConfig 给项目挂一份流水线配置,用来证明副本真拿到了内容。
func seedGuardPipelineConfig(t *testing.T, db *sql.DB, projectID, specJSON string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO pipeline_configs (project_id, spec_json, spec_yaml, status, created_at, updated_at)
		 VALUES (?, ?, 'stages: []', 'draft', ?, ?)`,
		projectID, specJSON, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("seed pipeline_configs: %v", err)
	}
}

func pipelineSpec(t *testing.T, db *sql.DB, projectID string) (string, bool) {
	t.Helper()
	var spec string
	err := db.QueryRow(`SELECT spec_json FROM pipeline_configs WHERE project_id = ?`, projectID).Scan(&spec)
	if err == sql.ErrNoRows {
		return "", false
	}
	if err != nil {
		t.Fatalf("读 pipeline_configs %s: %v", projectID, err)
	}
	return spec, true
}

// ---- 单个复制 ---------------------------------------------------------------

func TestCopyProject_DefaultNameAndInheritsConfig(t *testing.T) {
	env := setupGuard(t)
	spec := `{"stages":[{"id":"s1","name":"构建","jobs":[{"id":"j1","type":"script"}]}]}`
	seedGuardPipelineConfig(t, env.db, env.ungrouped, spec)

	src, _ := readProject(t, env.db, env.ungrouped)
	got := doCopy(t, env.admin, http.MethodPost, guardCopyURL(env, env.ungrouped), env.adminCS, `{}`)
	if got.status != http.StatusCreated {
		t.Fatalf("复制状态 = %d(%s), want 201", got.status, got.code)
	}
	if len(got.copies) != 1 {
		t.Fatalf("回执 %d 条, want 1", len(got.copies))
	}
	one := got.copies[0]
	if one.SourceID != env.ungrouped {
		t.Fatalf("sourceId = %q, want %q", one.SourceID, env.ungrouped)
	}
	if want := src.name + projectcopy.DefaultSuffix; one.Name != want {
		t.Fatalf("默认名 = %q, want %q", one.Name, want)
	}
	// 没传 targetGroupId → 沿用源项目分组(未归组就是空串)。
	if one.GroupID != src.groupID {
		t.Fatalf("groupId = %q, want 沿用源组 %q", one.GroupID, src.groupID)
	}

	cp, ok := readProject(t, env.db, one.ID)
	if !ok {
		t.Fatalf("库里查不到副本 %s", one.ID)
	}
	// 回执说了不算:库里那行也得叫这个名字(曾经 INSERT 漏带算好的名字,副本和源同名)。
	if cp.name != one.Name {
		t.Fatalf("库里的副本名 = %q, 回执说 %q", cp.name, one.Name)
	}
	if cp.repoURL != src.repoURL || cp.branch != src.branch {
		t.Fatalf("副本仓库 = %+v, want 与源一致 %+v", cp, src)
	}
	if cp.credNull || cp.credID != src.credID {
		t.Fatalf("副本凭据引用 = %q(空=%v), want 与源同引用 %q", cp.credID, cp.credNull, src.credID)
	}
	// 内容真搬过来了:副本有自己的一行流水线配置,正文与源相同。
	gotSpec, has := pipelineSpec(t, env.db, one.ID)
	if !has || gotSpec != spec {
		t.Fatalf("副本流水线配置 = (%q, %v), want 与源同正文", gotSpec, has)
	}
	if oldSpec, _ := pipelineSpec(t, env.db, env.ungrouped); oldSpec != spec {
		t.Fatalf("源项目配置被改动: %q", oldSpec)
	}

	// 审计:一行,记的是副本的 id,detail 指出抄自谁。
	entries := copyAuditRows(t, env.db, "project_copy")
	if len(entries) != 1 {
		t.Fatalf("project_copy 审计 %d 行, want 1", len(entries))
	}
	e := entries[0]
	if e.Actor != "admin:admin" || e.TargetID != one.ID {
		t.Fatalf("审计 actor/target = %q/%q, want admin:admin/%q", e.Actor, e.TargetID, one.ID)
	}
	if e.Detail["sourceId"] != env.ungrouped || e.Detail["name"] != one.Name {
		t.Fatalf("审计 detail = %v, want 含 sourceId 与 name", e.Detail)
	}

	// 再点一次:名字避让到 2,不会顶掉前一份。
	again := doCopy(t, env.admin, http.MethodPost, guardCopyURL(env, env.ungrouped), env.adminCS, `{}`)
	if again.status != http.StatusCreated {
		t.Fatalf("第二次复制 = %d(%s)", again.status, again.code)
	}
	if want := src.name + projectcopy.DefaultSuffix + " 2"; again.copies[0].Name != want {
		t.Fatalf("第二份名 = %q, want %q", again.copies[0].Name, want)
	}
}

func TestCopyProject_NameSuffixAndGroupThreeStates(t *testing.T) {
	env := setupGuard(t)

	// 手指定名直接生效,不再拼后缀。
	got := doCopy(t, env.admin, http.MethodPost, guardCopyURL(env, env.ungrouped), env.adminCS, `{"name":"发布演练专用"}`)
	if got.status != http.StatusCreated || got.copies[0].Name != "发布演练专用" {
		t.Fatalf("指定名 = %d(%s) %v", got.status, got.code, got.copies)
	}
	// 自定义后缀原样接在源名后(含用户自己写的分隔符)。
	src, _ := readProject(t, env.db, env.ungrouped)
	got = doCopy(t, env.admin, http.MethodPost, guardCopyURL(env, env.ungrouped), env.adminCS, `{"suffix":"-bak"}`)
	if got.status != http.StatusCreated || got.copies[0].Name != src.name+"-bak" {
		t.Fatalf("自定义后缀 = %d(%s) %v, want %q", got.status, got.code, got.copies, src.name+"-bak")
	}

	// 复制进某组:管理员与组长都管得住私有组。
	for name, c := range map[string]*http.Client{"admin": env.admin, "owner": env.owner} {
		csrf := map[string]string{"admin": env.adminCS, "owner": env.ownerCS}[name]
		got := doCopy(t, c, http.MethodPost, guardCopyURL(env, env.ungrouped), csrf,
			`{"targetGroupId":"`+env.privateID+`"}`)
		if got.status != http.StatusCreated || got.copies[0].GroupID != env.privateID {
			t.Fatalf("%s 复制进私有组 = %d(%s) %v", name, got.status, got.code, got.copies)
		}
	}
	// 落到未归组:源在私有组里,副本被甩出来。
	got = doCopy(t, env.owner, http.MethodPost, guardCopyURL(env, env.inPrivate), env.ownerCS, `{"targetGroupId":""}`)
	if got.status != http.StatusCreated || got.copies[0].GroupID != "" {
		t.Fatalf("复制到未归组 = %d(%s) %v", got.status, got.code, got.copies)
	}
	// 局外人管不住私有组 → 归属改动这一侧先拦下(他倒是复制得动未归组项目)。
	got = doCopy(t, env.other, http.MethodPost, guardCopyURL(env, env.ungrouped), env.otherCS,
		`{"targetGroupId":"`+env.privateID+`"}`)
	if got.status != http.StatusForbidden || got.code != "forbidden" {
		t.Fatalf("局外人复制进私有组 = %d(%s), want 403 forbidden", got.status, got.code)
	}
	if n := countProjects(t, env.db); n != 3+5 {
		t.Fatalf("被拒的请求不该建出副本:项目数 %d, want 8", n)
	}
	// 指向不存在的组是入参错误(422),与 PATCH /projects/{id} 归组同一口径。
	got = doCopy(t, env.admin, http.MethodPost, guardCopyURL(env, env.ungrouped), env.adminCS,
		`{"targetGroupId":"no-such-group"}`)
	if got.status != http.StatusUnprocessableEntity || got.code != "group_not_found" {
		t.Fatalf("复制到不存在的组 = %d(%s), want 422 group_not_found", got.status, got.code)
	}
}

func TestCopyProject_Permissions(t *testing.T) {
	env := setupGuard(t)
	viewer, viewerCS := addViewer(t, env)

	// 局外人:私有组项目连 Operate 都没有 → 403,而且是归属轴(不是角色轴)。
	got := doCopy(t, env.other, http.MethodPost, guardCopyURL(env, env.inPrivate), env.otherCS, `{}`)
	if got.status != http.StatusForbidden || got.code != "forbidden" {
		t.Fatalf("局外人复制私有组项目 = %d(%s), want 403 forbidden", got.status, got.code)
	}
	// 成员与组长都在名册里 → 放行。
	for _, tc := range []struct {
		name string
		c    *http.Client
		csrf string
	}{{"member", env.member, env.memberCS}, {"owner", env.owner, env.ownerCS}} {
		got := doCopy(t, tc.c, http.MethodPost, guardCopyURL(env, env.inPrivate), tc.csrf, `{}`)
		if got.status != http.StatusCreated {
			t.Fatalf("%s 复制私有组项目 = %d(%s), want 201", tc.name, got.status, got.code)
		}
	}
	// 只读角色:功能点不够,与分组无关 → role_denied(提示该找管理员调角色)。
	got = doCopy(t, viewer, http.MethodPost, guardCopyURL(env, env.ungrouped), viewerCS, `{}`)
	if got.status != http.StatusForbidden || got.code != "role_denied" {
		t.Fatalf("只读角色复制 = %d(%s), want 403 role_denied", got.status, got.code)
	}
	// 源项目不存在 → 404,不能被伪装成 403。
	got = doCopy(t, env.admin, http.MethodPost, guardCopyURL(env, "no-such-proj"), env.adminCS, `{}`)
	if got.status != http.StatusNotFound || got.code != "not_found" {
		t.Fatalf("复制不存在的项目 = %d(%s), want 404 not_found", got.status, got.code)
	}
}

func TestCopyProject_AuthAndCSRF(t *testing.T) {
	env := setupGuard(t)
	url := guardCopyURL(env, env.ungrouped)

	// 没会话。
	anon := newTestClient(t)
	got := doCopy(t, anon, http.MethodPost, url, "", `{}`)
	if got.status != http.StatusUnauthorized || got.code != "unauthorized" {
		t.Fatalf("未登录复制 = %d(%s), want 401 unauthorized", got.status, got.code)
	}
	// 有会话但缺 CSRF 头:写方法必须带。
	got = doCopy(t, env.admin, http.MethodPost, url, "", `{}`)
	if got.status != http.StatusForbidden || got.code != "csrf_invalid" {
		t.Fatalf("缺 CSRF 复制 = %d(%s), want 403 csrf_invalid", got.status, got.code)
	}
	if n := countProjects(t, env.db); n != 3 {
		t.Fatalf("被拦的请求不该留下副本:项目数 %d, want 3", n)
	}
	// 请求体不是 JSON。
	got = doCopy(t, env.admin, http.MethodPost, url, env.adminCS, `not-json`)
	if got.status != http.StatusBadRequest || got.code != "bad_request" {
		t.Fatalf("坏请求体 = %d(%s), want 400 bad_request", got.status, got.code)
	}
}

// ---- 一批复制 -------------------------------------------------------------

func TestCopyProjects_Batch(t *testing.T) {
	env := setupGuard(t)
	body := `{"projectIds":["` + env.ungrouped + `","` + env.inPrivate + `","` + env.inPublic + `"]}`
	got := doCopy(t, env.admin, http.MethodPost, env.srv.URL+batchCopyURL, env.adminCS, body)
	if got.status != http.StatusCreated {
		t.Fatalf("批量复制 = %d(%s), want 201", got.status, got.code)
	}
	if len(got.copies) != 3 {
		t.Fatalf("回执 %d 条, want 3: %+v", len(got.copies), got.copies)
	}
	// 每条各自沿用源的分组:整组复制不是「把副本塞进同一个组」。
	wantGroup := map[string]string{env.ungrouped: "", env.inPrivate: env.privateID, env.inPublic: env.publicID}
	srcNames := map[string]string{}
	for _, c := range got.copies {
		src, ok := readProject(t, env.db, c.SourceID)
		if !ok {
			t.Fatalf("源项目 %s 不见了", c.SourceID)
		}
		srcNames[c.SourceID] = src.name
		if c.GroupID != wantGroup[c.SourceID] {
			t.Fatalf("%s 的副本组 = %q, want %q", c.SourceID, c.GroupID, wantGroup[c.SourceID])
		}
		if c.Name != src.name+projectcopy.DefaultSuffix {
			t.Fatalf("副本名 = %q, want %q", c.Name, src.name+projectcopy.DefaultSuffix)
		}
		if c.ID == c.SourceID {
			t.Fatalf("副本 id 与源相同: %s", c.ID)
		}
	}
	// 一批只记一行审计,明细里说清抄了哪些、叫什么。
	entries := copyAuditRows(t, env.db, "project_copy_batch")
	if len(entries) != 1 {
		t.Fatalf("project_copy_batch 审计 %d 行, want 1", len(entries))
	}
	e := entries[0]
	if e.Actor != "admin:admin" || e.TargetID != "batch" {
		t.Fatalf("审计 actor/target = %q/%q, want admin:admin/batch", e.Actor, e.TargetID)
	}
	ids, _ := e.Detail["sourceIds"].([]any)
	copies, _ := e.Detail["copies"].([]any)
	if len(ids) != 3 || len(copies) != 3 {
		t.Fatalf("审计 detail = %v, want sourceIds 与 copies 各 3 条", e.Detail)
	}

	// 同一份重复提交 + 空串:去重后只出一个副本。
	before := countProjects(t, env.db)
	dup := doCopy(t, env.admin, http.MethodPost, env.srv.URL+batchCopyURL, env.adminCS,
		`{"projectIds":["`+env.ungrouped+`"," `+env.ungrouped+`","","  "]}`)
	if dup.status != http.StatusCreated || len(dup.copies) != 1 {
		t.Fatalf("去重 = %d(%s) %+v, want 201 一条", dup.status, dup.code, dup.copies)
	}
	if n := countProjects(t, env.db); n != before+1 {
		t.Fatalf("去重后项目数 %d, want %d", n, before+1)
	}

	// 整批落进指定组。
	got = doCopy(t, env.admin, http.MethodPost, env.srv.URL+batchCopyURL, env.adminCS,
		`{"projectIds":["`+env.ungrouped+`","`+env.inPublic+`"],"targetGroupId":"`+env.publicID+`"}`)
	if got.status != http.StatusCreated {
		t.Fatalf("整批归组复制 = %d(%s)", got.status, got.code)
	}
	for _, c := range got.copies {
		if c.GroupID != env.publicID {
			t.Fatalf("%s(%s) 没落到目标组: %q", c.Name, c.ID, c.GroupID)
		}
	}
}

func TestCopyProjects_BadInput(t *testing.T) {
	env := setupGuard(t)
	url := env.srv.URL + batchCopyURL

	for _, tc := range []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{"空数组", `{"projectIds":[]}`, http.StatusBadRequest, "nothing_to_copy"},
		{"只有空串", `{"projectIds":["","  "]}`, http.StatusBadRequest, "nothing_to_copy"},
		{"缺字段", `{"suffix":"x"}`, http.StatusBadRequest, "nothing_to_copy"},
		{"坏 JSON", `{"projectIds":`, http.StatusBadRequest, "bad_request"},
	} {
		got := doCopy(t, env.admin, http.MethodPost, url, env.adminCS, tc.body)
		if got.status != tc.wantStatus || got.code != tc.wantCode {
			t.Fatalf("%s = %d(%s), want %d(%s)", tc.name, got.status, got.code, tc.wantStatus, tc.wantCode)
		}
	}

	// 超上限(一批一个事务,别把库按住)。
	ids := make([]string, 0, maxCopyBatch+1)
	for i := 0; i < maxCopyBatch+1; i++ {
		ids = append(ids, uuid.NewString())
	}
	got := doCopy(t, env.admin, http.MethodPost, url, env.adminCS, `{"projectIds":[`+quoteJoin(ids)+`]}`)
	if got.status != http.StatusUnprocessableEntity || got.code != "too_many_to_copy" {
		t.Fatalf("超出上限 = %d(%s), want 422 too_many_to_copy", got.status, got.code)
	}
	if n := countProjects(t, env.db); n != 3 {
		t.Fatalf("被拒的批次留下副本:项目数 %d, want 3", n)
	}
}

func quoteJoin(in []string) string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, `"`+s+`"`)
	}
	return strings.Join(out, ",")
}

// TestCopyProjects_PerProjectChecks 是这次改动最要紧的一条:
// 批量端点不被 accessGuard 识别,源项目的权限全靠 handler 逐条判,
// 所以「从别的组的入口提交别人项目的 id」必须 403,而且一个副本都不留。
func TestCopyProjects_PerProjectChecks(t *testing.T) {
	env := setupGuard(t)
	url := env.srv.URL + batchCopyURL

	// 局外人拿私有组项目当源。
	got := doCopy(t, env.other, http.MethodPost, url, env.otherCS, `{"projectIds":["`+env.inPrivate+`"]}`)
	if got.status != http.StatusForbidden || got.code != "forbidden" {
		t.Fatalf("局外人批量复制私有组项目 = %d(%s), want 403 forbidden", got.status, got.code)
	}
	// 混在一个能碰的项目里也不行:一条不过,整批不过(不做「跳过没权限的」)。
	got = doCopy(t, env.other, http.MethodPost, url, env.otherCS,
		`{"projectIds":["`+env.ungrouped+`","`+env.inPrivate+`"]}`)
	if got.status != http.StatusForbidden || got.code != "forbidden" {
		t.Fatalf("混一条私有组项目 = %d(%s), want 403 forbidden", got.status, got.code)
	}
	if n := countProjects(t, env.db); n != 3 {
		t.Fatalf("被拒的批次留下副本:项目数 %d, want 3", n)
	}

	// 目标组这一侧同样要管得住。
	got = doCopy(t, env.other, http.MethodPost, url, env.otherCS,
		`{"projectIds":["`+env.ungrouped+`"],"targetGroupId":"`+env.privateID+`"}`)
	if got.status != http.StatusForbidden || got.code != "forbidden" {
		t.Fatalf("局外人把副本塞进私有组 = %d(%s), want 403 forbidden", got.status, got.code)
	}

	// 只读角色:功能点不够,与归属无关。
	viewer, viewerCS := addViewer(t, env)
	got = doCopy(t, viewer, http.MethodPost, url, viewerCS, `{"projectIds":["`+env.ungrouped+`"]}`)
	if got.status != http.StatusForbidden || got.code != "role_denied" {
		t.Fatalf("只读角色批量复制 = %d(%s), want 403 role_denied", got.status, got.code)
	}

	// 没会话 / 缺 CSRF。
	anon := newTestClient(t)
	if got := doCopy(t, anon, http.MethodPost, url, "", `{}`); got.status != http.StatusUnauthorized {
		t.Fatalf("未登录批量复制 = %d(%s), want 401", got.status, got.code)
	}
	if got := doCopy(t, env.admin, http.MethodPost, url, "", `{}`); got.code != "csrf_invalid" {
		t.Fatalf("缺 CSRF 批量复制 = %d(%s), want 403 csrf_invalid", got.status, got.code)
	}
}

// TestCopyProjects_DeadSourceLeavesNothing 验「勾中的批里有一个已经被别人删掉」时
// HTTP 层的表现:整批拒绝,一个副本都不留,也不写审计。
// (事务真的回滚由 internal/projectcopy 的 TestCopyRollsBackWholeBatch 盯着;
// 这里先被 handler 的逐条判定拦下,所以只看副作用为零。)
func TestCopyProjects_DeadSourceLeavesNothing(t *testing.T) {
	env := setupGuard(t)
	before := countProjects(t, env.db)
	got := doCopy(t, env.admin, http.MethodPost, env.srv.URL+batchCopyURL, env.adminCS,
		fmt.Sprintf(`{"projectIds":["%s","%s","no-such-proj"]}`, env.ungrouped, env.inPublic))
	if got.status != http.StatusNotFound {
		t.Fatalf("含不存在源的批次 = %d(%s), want 404", got.status, got.code)
	}
	if n := countProjects(t, env.db); n != before {
		t.Fatalf("项目数 %d, want 仍为 %d", n, before)
	}
	if entries := copyAuditRows(t, env.db, "project_copy_batch"); len(entries) != 0 {
		t.Fatalf("失败的批次写了 %d 行审计, want 0", len(entries))
	}
}

// TestCopyHandlers_ServiceAbsent 验证没装配复制服务时明确 503,而不是 panic 或静默成功。
func TestCopyHandlers_ServiceAbsent(t *testing.T) {
	for _, tc := range []struct {
		name string
		h    http.HandlerFunc
		url  string
	}{
		{"单个", makeCopyProjectHandler(nil, nil, nil, nil), "/api/projects/p1/copy"},
		{"一批", makeCopyProjectsHandler(nil, nil, nil, nil), "/api/project-copies"},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, tc.url, strings.NewReader(`{"projectIds":["p1"]}`))
		req.Header.Set("Content-Type", "application/json")
		tc.h(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s 未装配 = %d, want 503(%s)", tc.name, rec.Code, rec.Body.String())
		}
	}
}
