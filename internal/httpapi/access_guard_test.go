package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/access"
	"github.com/fuuhoo/pipewright/internal/approval"
	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/group"
	"github.com/fuuhoo/pipewright/internal/mask"
	"github.com/fuuhoo/pipewright/internal/project"
	"github.com/fuuhoo/pipewright/internal/role"
	"github.com/fuuhoo/pipewright/internal/run"
	"github.com/fuuhoo/pipewright/internal/storetest"
	"github.com/fuuhoo/pipewright/internal/target"
	"github.com/fuuhoo/pipewright/internal/users"
	"github.com/fuuhoo/pipewright/internal/vault"
	"github.com/google/uuid"
)

// access_guard_test.go —— v6.2 分组权限的 HTTP 收口。
//
// 覆盖四件容易写反的事:
//  1. 中间件认 URL 形态:读→View、写→Operate;私有组对局外人 403,未归组与公开组照旧放行。
//  2. 资源不存在必须是 404,不能被伪装成 403(否则用户以为有人藏了个资源)。
//  3. 列表(项目 / 运行)按可见分组收敛,total 与 items 同口径。
//  4. 改归属(归组 / 移出)要管得住两侧,组成员做不到。

const guardPass = "guard-pass-1234"

// loadRoleCatalog 按 main.go 的两行装配把角色仓储接进 access 的判定缓存。
//
// 0063 之后 'user' / 'developer' / 'ops' / 'viewer' 只活在库里:不装载就 fail closed,
// 这些角色的会话会在 RequireUser 上 401、请求会一律降到只读 —— 看着像权限设计坏了,
// 其实缺的是装配。凡是用非管理员身份发请求的测试 setup 都要先调它。
func loadRoleCatalog(t *testing.T, db *sql.DB) {
	t.Helper()
	roleSvc := role.New(db)
	access.SetRoleStore(roleSvc)
	if err := access.ReloadRoles(context.Background()); err != nil {
		t.Fatalf("装载角色目录: %v", err)
	}
	t.Cleanup(func() { access.SetRoleStore(nil) })
}

type guardEnv struct {
	srv *httptest.Server
	db  *sql.DB
	gs  *group.Service

	// 审批门装配:approve/reject 与列表同样归 runs/{id} 的权限管,测试要能真的挂上门。
	coord  *approval.Coordinator
	astore *approval.Store

	admin    *http.Client
	adminCS  string
	member   *http.Client
	memberCS string
	owner    *http.Client
	ownerCS  string
	other    *http.Client
	otherCS  string

	privateID   string
	publicID    string
	memberUID   string
	strangerUID string

	ungrouped    string // 未归组项目
	inPrivate    string // 私有组内项目
	inPublic     string // 公开组内项目
	runInPriv    string // 私有组项目下的一次运行
	serverCredID string // 服务器用的 SSH 凭据(与 git_token 分开)
	srvUngrouped string // 未归组服务器
	srvInPrivate string // 私有组内服务器
	srvInPublic  string // 公开组内服务器
}

func setupGuard(t *testing.T) *guardEnv {
	t.Helper()
	st := storetest.Open(t)
	db := st.DB
	userSvc := users.NewService(db)
	authSvc := auth.NewService(db, nil, userSvc)
	if err := authSvc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap admin: %v", err)
	}
	// 角色仓储要接进判定缓存,和 main.go 的装配逐字同形。
	loadRoleCatalog(t, db)

	hash, err := auth.HashPassword(guardPass)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	// 项目 / 运行的归属链路与 ls-remote 无关,直接落库把注意力留在权限上;
	// 探测行为由 internal/project 的包测试覆盖。
	mkUser := func(name string) string {
		t.Helper()
		id := uuid.NewString()
		if _, err := db.Exec(
			`INSERT INTO users (id, username, password_hash, role, enabled, created_at, updated_at)
			 VALUES (?, ?, ?, 'user', 1, ?, ?)`,
			id, name, hash, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
			t.Fatalf("seed user %s: %v", name, err)
		}
		return id
	}
	ownerID := mkUser("owner")
	memberID := mkUser("member")
	strangerID := mkUser("stranger")

	gs := group.New(db)
	ctx := context.Background()
	pub, err := gs.Create(ctx, group.CreateInput{
		Name: "公开组", OwnerID: ownerID, Visibility: access.VisibilityPublic,
	})
	if err != nil {
		t.Fatalf("create public group: %v", err)
	}
	priv, err := gs.Create(ctx, group.CreateInput{
		Name: "私有组", OwnerID: ownerID, MemberIDs: []string{memberID}, Visibility: access.VisibilityPrivate,
	})
	if err != nil {
		t.Fatalf("create private group: %v", err)
	}

	v := vault.New(db, testMasterKey())
	credID := seedGuardCredential(t, v, "guard cred")
	proj := func(name, groupID string) string {
		t.Helper()
		id := uuid.NewString()
		if _, err := db.Exec(
			`INSERT INTO projects (id, name, repo_url, default_branch, credential_id, group_id, created_at, updated_at)
			 VALUES (?, ?, 'https://gitee.com/acme/x.git', 'main', ?, ?, ?, ?)`,
			id, name, credID, groupID, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
			t.Fatalf("seed project %s: %v", name, err)
		}
		return id
	}
	ungrouped := proj("free-proj", access.Ungrouped)
	inPrivate := proj("secret-proj", priv.ID)
	inPublic := proj("open-proj", pub.ID)
	runID := seedGuardRun(t, db, inPrivate)

	// 服务器三态各一台:主机/容器端的权限与项目共用同一套判定,故复用这套夹具。
	// 直接落库而不走 target.Create —— 分组收敛发生在读路径,与 SSH 无关。
	srvCredID, err := v.Create(vault.CreateInput{
		Name: "guard ssh", Type: vault.TypeSSHKey, Secret: "guard-ssh-secret",
	})
	if err != nil {
		t.Fatalf("seed ssh credential: %v", err)
	}
	mkSrv := func(name, groupID string) string {
		t.Helper()
		id := uuid.NewString()
		if _, err := db.Exec(
			`INSERT INTO servers (id, name, host, port, user, credential_id, group_id, created_at, updated_at)
			 VALUES (?, ?, '10.0.0.1', 22, 'deploy', ?, ?, ?, ?)`,
			id, name, srvCredID.ID, groupID, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
			t.Fatalf("seed server %s: %v", name, err)
		}
		return id
	}
	srvUngrouped := mkSrv("free-srv", access.Ungrouped)
	srvInPrivate := mkSrv("secret-srv", priv.ID)
	srvInPublic := mkSrv("open-srv", pub.ID)

	approvalCoord, approvalStore := approval.New(), approval.NewStore(db)
	srv := httptest.NewServer(New(testWebFSAuth(), authSvc,
		WithVault(v),
		WithProjects(project.New(db, v, nil)),
		WithRuns(run.New(db), nil),
		WithAudit(audit.New(db, mask.NewMasker(), nil)),
		WithUsers(userSvc),
		WithGroups(gs),
		WithAccess(access.NewService(gs)),
		WithApprovals(approvalCoord, approvalStore),
		WithServers(target.New(db, v, stubDialer{res: &target.ExecResult{Stdout: "ok"}})),
	))
	t.Cleanup(srv.Close)

	env := &guardEnv{
		srv: srv, db: db, gs: gs, privateID: priv.ID, publicID: pub.ID,
		coord: approvalCoord, astore: approvalStore,
		memberUID: memberID, strangerUID: strangerID,
		ungrouped: ungrouped, inPrivate: inPrivate, inPublic: inPublic, runInPriv: runID,
		serverCredID: srvCredID.ID, srvUngrouped: srvUngrouped, srvInPrivate: srvInPrivate, srvInPublic: srvInPublic,
	}
	env.admin = newTestClient(t)
	env.adminCS = loginAs(t, env.admin, srv.URL, "admin", "testpass")
	env.member = newTestClient(t)
	env.memberCS = loginAs(t, env.member, srv.URL, "member", guardPass)
	env.owner = newTestClient(t)
	env.ownerCS = loginAs(t, env.owner, srv.URL, "owner", guardPass)
	env.other = newTestClient(t)
	env.otherCS = loginAs(t, env.other, srv.URL, "stranger", guardPass)
	return env
}

func seedGuardCredential(t *testing.T, v vault.Vault, name string) string {
	t.Helper()
	c, err := v.Create(vault.CreateInput{Name: name, Type: vault.TypeGitToken, Secret: "guard-token-value"})
	if err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	return c.ID
}

func seedGuardRun(t *testing.T, db *sql.DB, projectID string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := db.Exec(
		`INSERT INTO pipeline_runs (id, project_id, status, trigger_type, created_at) VALUES (?, ?, 'success', 'manual', ?)`,
		id, projectID, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	return id
}

// statusOf 发一次请求并丢掉响应体,只关心状态码。
func statusOf(t *testing.T, c *http.Client, method, url, csrf, body string) int {
	t.Helper()
	resp := doJSON(t, c, method, url, csrf, body)
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

// ---- URL 形态识别(不碰库)--------------------------------------------------

func TestResolveGuardTarget(t *testing.T) {
	cases := []struct {
		path   string
		wantOK bool
		kind   access.Kind
		id     string
	}{
		{"/api/projects/p1", true, access.KindProject, "p1"},
		{"/api/projects/p1/pipeline", true, access.KindProject, "p1"},
		// 创建与 test-clone 没有既成资源可判:交给 handler 用可见范围/入参校验。
		{"/api/projects", false, "", ""},
		{"/api/projects/test-clone", false, "", ""},
		{"/api/runs/r9", true, access.KindRun, "r9"},
		{"/api/runs/r9/logs", true, access.KindRun, "r9"},
		// 服务器:每台机器的所有子路径(容器/镜像/终端…)都挂在同一条前缀下,一次识别全覆盖。
		{"/api/servers/s1", true, access.KindServer, "s1"},
		{"/api/servers/s1/containers", true, access.KindServer, "s1"},
		{"/api/servers/s1/containers/c7/terminal", true, access.KindServer, "s1"},
		// 聚合总览是字面段:没有「某一台」可判,由 handler 按可见分组收敛整表。
		{"/api/servers", false, "", ""},
		{"/api/servers/metrics", false, "", ""},
		{"/api/servers/containers", false, "", ""},
		// 不归分组权限管的路径。
		{"/api/credentials", false, "", ""},
		{"/api/groups/g1", false, "", ""},
		{"/api/admin/users", false, "", ""},
	}
	for _, tc := range cases {
		got, ok := resolveGuardTarget(tc.path)
		if ok != tc.wantOK {
			t.Fatalf("%s ok = %v, want %v", tc.path, ok, tc.wantOK)
		}
		if ok && (got.kind != tc.kind || got.id != tc.id) {
			t.Fatalf("%s → %+v, want %s/%s", tc.path, got, tc.kind, tc.id)
		}
	}
}

// ---- 中间件档位矩阵 ---------------------------------------------------------

func TestAccessGuard_Matrix(t *testing.T) {
	env := setupGuard(t)
	prefix := env.srv.URL + "/api/projects/"

	// 局外人:私有组项目的写操作 403;未归组与公开组照旧可操作。
	if got := statusOf(t, env.other, http.MethodPatch, prefix+env.inPrivate, env.otherCS, `{"name":"hijack"}`); got != http.StatusForbidden {
		t.Fatalf("局外人写私有组项目 = %d, want 403", got)
	}
	if got := statusOf(t, env.other, http.MethodPatch, prefix+env.ungrouped, env.otherCS, `{"name":"ok"}`); got != http.StatusOK {
		t.Fatalf("局外人写未归组项目 = %d, want 200", got)
	}
	if got := statusOf(t, env.other, http.MethodPatch, prefix+env.inPublic, env.otherCS, `{"name":"ok"}`); got != http.StatusOK {
		t.Fatalf("局外人写公开组项目 = %d, want 200", got)
	}

	// 读:运行详情随项目走。局外人 403,成员 200,管理员 200。
	runURL := env.srv.URL + "/api/runs/" + env.runInPriv
	if got := statusOf(t, env.other, http.MethodGet, runURL, "", ""); got != http.StatusForbidden {
		t.Fatalf("局外人读私有组运行 = %d, want 403", got)
	}
	if got := statusOf(t, env.member, http.MethodGet, runURL, "", ""); got != http.StatusOK {
		t.Fatalf("成员读私有组运行 = %d, want 200", got)
	}
	if got := statusOf(t, env.admin, http.MethodGet, runURL, "", ""); got != http.StatusOK {
		t.Fatalf("管理员读私有组运行 = %d, want 200", got)
	}
	// 组长不靠名册也有权限(owner_id 即管理权)。
	if got := statusOf(t, env.owner, http.MethodGet, runURL, "", ""); got != http.StatusOK {
		t.Fatalf("组长读私有组运行 = %d, want 200", got)
	}

	// 不存在 → 404,不能被伪装成 403。
	if got := statusOf(t, env.other, http.MethodGet, env.srv.URL+"/api/runs/no-such-run", "", ""); got != http.StatusNotFound {
		t.Fatalf("读不存在的运行 = %d, want 404", got)
	}
	if got := statusOf(t, env.other, http.MethodPatch, prefix+"no-such-proj", env.otherCS, `{"name":"x"}`); got != http.StatusNotFound {
		t.Fatalf("写不存在的项目 = %d, want 404", got)
	}
}

// ---- 列表收敛 --------------------------------------------------------------

// projectRow 只解列表里用得上的字段。
type projectRow struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	GroupID string `json:"groupId"`
}

func listProjects(t *testing.T, env *guardEnv, c *http.Client) []projectRow {
	t.Helper()
	resp := doJSON(t, c, http.MethodGet, env.srv.URL+"/api/projects", "", "")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/projects = %d", resp.StatusCode)
	}
	var rows []projectRow
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		t.Fatalf("decode projects: %v", err)
	}
	return rows
}

func projectNamesOf(rows []projectRow) map[string]bool {
	out := map[string]bool{}
	for _, r := range rows {
		out[r.Name] = true
	}
	return out
}

func TestListProjects_ScopedByGroup(t *testing.T) {
	env := setupGuard(t)

	// 局外人:未归组 + 公开组,看不到他人私有组的项目。
	names := projectNamesOf(listProjects(t, env, env.other))
	if !names["free-proj"] || !names["open-proj"] {
		t.Fatalf("局外人项目列表 = %v, want 含 free-proj / open-proj", names)
	}
	if names["secret-proj"] {
		t.Fatalf("局外人不该看到 secret-proj")
	}

	// 成员:多看到自己所在私有组的那一条。
	names = projectNamesOf(listProjects(t, env, env.member))
	if !names["secret-proj"] {
		t.Fatalf("成员列表 = %v, want 含 secret-proj", names)
	}

	// 管理员:全见。
	if got := len(listProjects(t, env, env.admin)); got != 3 {
		t.Fatalf("管理员看到 %d 条, want 3", got)
	}

	// 列表要回 groupId,前端才能标「属于哪个组」并决定归组下拉的默认值。
	for _, r := range listProjects(t, env, env.member) {
		if r.Name == "secret-proj" && r.GroupID != env.privateID {
			t.Fatalf("secret-proj 的 groupId = %q, want %q", r.GroupID, env.privateID)
		}
	}
}

func TestListRuns_ScopedByGroup(t *testing.T) {
	env := setupGuard(t)

	total := func(c *http.Client) int {
		t.Helper()
		resp := doJSON(t, c, http.MethodGet, env.srv.URL+"/api/runs", "", "")
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /api/runs = %d", resp.StatusCode)
		}
		var out struct {
			Total int `json:"total"`
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode runs: %v", err)
		}
		// total 必须与 items 同口径,否则前端按全量算页数。
		if out.Total != len(out.Items) {
			t.Fatalf("runs total/items = %d/%d, 不同口径", out.Total, len(out.Items))
		}
		return out.Total
	}

	if got := total(env.other); got != 0 {
		t.Fatalf("局外人看到 %d 次运行, want 0(唯一的运行在私有组)", got)
	}
	if got := total(env.member); got != 1 {
		t.Fatalf("成员看到 %d 次运行, want 1", got)
	}
	if got := total(env.admin); got != 1 {
		t.Fatalf("管理员看到 %d 次运行, want 1", got)
	}
}

// ---- 归组 / 移出:要求两侧 Manage ------------------------------------------

func TestReassign_RequiresManageOnBothSides(t *testing.T) {
	env := setupGuard(t)
	prefix := env.srv.URL + "/api/projects/"

	// 组成员能干活,但不能决定项目属于哪个组:旧侧(私有组)的 Manage 就过不去。
	if got := statusOf(t, env.member, http.MethodPatch, prefix+env.inPrivate, env.memberCS,
		`{"groupId":"`+env.publicID+`"}`); got != http.StatusForbidden {
		t.Fatalf("成员把私有组项目移到公开组 = %d, want 403", got)
	}
	// 局外人连 Operate 都没有,更早被挡。
	if got := statusOf(t, env.other, http.MethodPatch, prefix+env.inPrivate, env.otherCS,
		`{"groupId":"`+env.publicID+`"}`); got != http.StatusForbidden {
		t.Fatalf("局外人移组 = %d, want 403", got)
	}
	// 组长管得动自己组的归属。
	if got := statusOf(t, env.owner, http.MethodPatch, prefix+env.inPrivate, env.ownerCS,
		`{"groupId":""}`); got != http.StatusOK {
		t.Fatalf("组长把项目移出私有组 = %d, want 200", got)
	}
	// 移入一个与自己无关的私有组也不行(新侧 Manage 把关)。
	if got := statusOf(t, env.other, http.MethodPatch, prefix+env.ungrouped, env.otherCS,
		`{"groupId":"`+env.privateID+`"}`); got != http.StatusForbidden {
		t.Fatalf("局外人把未归组项目塞进别人私有组 = %d, want 403", got)
	}
	// 管理员可以双向移动,并留下 project_reassign 审计。
	if got := statusOf(t, env.admin, http.MethodPatch, prefix+env.inPublic, env.adminCS,
		`{"groupId":"`+env.privateID+`"}`); got != http.StatusOK {
		t.Fatalf("管理员把公开组项目移入私有组 = %d, want 200", got)
	}
	var n int
	if err := env.db.QueryRow(`SELECT COUNT(1) FROM audit_log WHERE action = ?`, audit.ActionProjectReassign).Scan(&n); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if n == 0 {
		t.Fatalf("移组未留审计")
	}
	// 建组时指定的组员不该因为一次移组就被静默丢掉。
	fresh, err := env.gs.Get(context.Background(), env.privateID)
	if err != nil {
		t.Fatalf("get private group: %v", err)
	}
	if len(fresh.MemberIDs) != 1 || fresh.MemberIDs[0] != env.memberUID {
		t.Fatalf("名册被改坏: %v", fresh.MemberIDs)
	}
}

// ---- 分组端点自身的权限 -----------------------------------------------------

func TestGroupEndpoints(t *testing.T) {
	env := setupGuard(t)

	// 局外人:只看到公开组。
	resp := doJSON(t, env.other, http.MethodGet, env.srv.URL+"/api/groups", "", "")
	var rows []struct {
		ID         string `json:"id"`
		Visibility string `json:"visibility"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		t.Fatalf("decode groups: %v", err)
	}
	for _, r := range rows {
		if r.ID == env.privateID {
			t.Fatalf("局外人看到了私有组")
		}
	}

	// 私有组详情:局外人 403,成员 200。
	detail := env.srv.URL + "/api/groups/" + env.privateID
	if got := statusOf(t, env.other, http.MethodGet, detail, "", ""); got != http.StatusForbidden {
		t.Fatalf("局外人读私有组详情 = %d, want 403", got)
	}
	if got := statusOf(t, env.member, http.MethodGet, detail, "", ""); got != http.StatusOK {
		t.Fatalf("成员读私有组详情 = %d, want 200", got)
	}

	// 建组 / 删组是设置类动作:仅管理员。
	if got := statusOf(t, env.other, http.MethodPost, env.srv.URL+"/api/groups", env.otherCS,
		`{"name":"野生组"}`); got != http.StatusForbidden {
		t.Fatalf("普通用户建组 = %d, want 403", got)
	}
	respCreate := doJSON(t, env.admin, http.MethodPost, env.srv.URL+"/api/groups", env.adminCS, `{"name":"新建组","visibility":"private"}`)
	created := readAll(t, respCreate)
	_ = respCreate.Body.Close()
	if respCreate.StatusCode != http.StatusCreated {
		t.Fatalf("管理员建组 = %d: %s", respCreate.StatusCode, created)
	}

	// 名册:组长可加人,成员不可。
	addURL := env.srv.URL + "/api/groups/" + env.privateID + "/members"
	if got := statusOf(t, env.member, http.MethodPost, addURL, env.memberCS,
		`{"userId":"`+env.strangerUID+`"}`); got != http.StatusForbidden {
		t.Fatalf("成员加人名册 = %d, want 403", got)
	}
	if got := statusOf(t, env.owner, http.MethodPost, addURL, env.ownerCS,
		`{"userId":"`+env.strangerUID+`"}`); got != http.StatusOK {
		t.Fatalf("组长加人名册 = %d, want 200", got)
	}
	// 加完局外人就能进来了——名册是私有组唯一的入口。
	if got := statusOf(t, env.other, http.MethodGet, detail, "", ""); got != http.StatusOK {
		t.Fatalf("入册后局外人(现成员)读详情 = %d, want 200", got)
	}

	// 换组长只有管理员能做。
	if got := statusOf(t, env.owner, http.MethodPatch, detail, env.ownerCS,
		`{"ownerId":"`+env.memberUID+`"}`); got != http.StatusForbidden {
		t.Fatalf("组长自行转交组长 = %d, want 403", got)
	}
}
