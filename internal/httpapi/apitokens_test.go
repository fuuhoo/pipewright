// apitokens_test.go 守的是 API 令牌这条机器凭据线:发放/撤销的 HTTP 契约、Bearer 鉴权
// 分支(含「带无效 Bearer 时不得回落到 cookie」这条防 confused deputy 的规矩)、以及
// 明文只回显一次的纪律。
package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/access"
	"github.com/fuuhoo/pipewright/internal/apitoken"
	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/group"
	"github.com/fuuhoo/pipewright/internal/mask"
	"github.com/fuuhoo/pipewright/internal/project"
	"github.com/fuuhoo/pipewright/internal/users"
	"github.com/google/uuid"
)

// apiTokenEnv 是一次装配好的测试环境:服务 + 已登录(admin)的 cookie 客户端 + csrf。
//
// 分组与访问判定也接上了:令牌的两把尺(工具面 / 数据面)最后要落到「带 Bearer 调业务端点
// 究竟看见什么」,不接这两样就只能验请求体存取,验不到收窄。
type apiTokenEnv struct {
	srv    *httptest.Server
	client *http.Client
	csrf   string
	audit  audit.Recorder
	db     *sql.DB
	gs     *group.Service
	admin  string // 管理员用户 id(建组时当组长)
}

func newAPITokenEnv(t *testing.T, withTokens bool) *apiTokenEnv {
	t.Helper()
	st := testStoreAuth(t)
	svc := auth.NewService(st.DB, nil, nil)
	if err := svc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	// 会话签发要走 auth 服务,users 表里的管理员行也得同步建出来(令牌归属按 users.id 判)。
	us := users.NewService(st.DB)
	if err := us.BootstrapAdminRow("admin", mustHash(t)); err != nil {
		t.Fatalf("bootstrap users row: %v", err)
	}
	var adminID string
	if err := st.DB.QueryRow(`SELECT id FROM users WHERE username = 'admin'`).Scan(&adminID); err != nil {
		t.Fatalf("取管理员 id: %v", err)
	}
	tok := apitoken.NewService(st.DB)
	gs := group.New(st.DB)
	rec := audit.New(st.DB, mask.NewMasker(), nil)
	opts := []Option{
		WithAudit(rec), WithUsers(us), WithProjects(project.New(st.DB, nil, nil)),
		WithGroups(gs), WithAccess(access.NewService(gs)),
	}
	if withTokens {
		opts = append(opts, WithAPITokens(tok))
	}
	srv := httptest.NewServer(New(testWebFSAuth(), svc, opts...))
	t.Cleanup(srv.Close)
	client := newTestClient(t)
	csrf := loginWithClient(t, client, srv.URL)
	return &apiTokenEnv{srv: srv, client: client, csrf: csrf, audit: rec, db: st.DB, gs: gs, admin: adminID}
}

// seedProject 直接落一行项目(不走 POST /api/projects):这里要验的是读路径上的可见性,
// 写入链路与分组无关。
func (e *apiTokenEnv) seedProject(t *testing.T, name, groupID string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := e.db.Exec(
		`INSERT INTO projects (id, name, repo_url, default_branch, group_id, created_at, updated_at)
		 VALUES (?, ?, 'https://gitee.com/acme/x.git', 'main', ?, ?, ?)`,
		id, name, groupID, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("seed project %s: %v", name, err)
	}
	return id
}

// seedGroup 建一个公开组(组长是管理员),返回其 id。
func (e *apiTokenEnv) seedGroup(t *testing.T, name string) string {
	t.Helper()
	g, err := e.gs.Create(context.Background(), group.CreateInput{
		Name: name, OwnerID: e.admin, Visibility: access.VisibilityPublic,
	})
	if err != nil {
		t.Fatalf("seed group %s: %v", name, err)
	}
	return g.ID
}

// issueScoped 用签发端点发一把带范围的令牌并返回明文;期望 201。
func (e *apiTokenEnv) issueScoped(t *testing.T, body string) (apiTokenDTO, string) {
	t.Helper()
	resp, raw := e.post(t, "/api/admin/api-tokens", body, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("签发应 201,得 %d: %s", resp.StatusCode, raw)
	}
	var out struct {
		Item      apiTokenDTO `json:"item"`
		Plaintext string      `json:"plaintext"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("解析签发响应: %v (%s)", err, raw)
	}
	return out.Item, out.Plaintext
}

func mustHash(t *testing.T) string {
	t.Helper()
	h, err := auth.HashPassword("seedpass")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	return h
}

// post 发一个 JSON 请求;bearer 非空则带 Authorization: Bearer,否则带 cookie 会话所需的 csrf。
func (e *apiTokenEnv) post(t *testing.T, path, body, bearer string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	} else {
		req.Header.Set(headerCsrf, e.csrf)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, string(raw)
}

func (e *apiTokenEnv) get(t *testing.T, path, bearer string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, string(raw)
}

func (e *apiTokenEnv) del(t *testing.T, path, bearer string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, e.srv.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	} else {
		req.Header.Set(headerCsrf, e.csrf)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, string(raw)
}

// patch 发一个 PATCH JSON 请求(管理端点只由浏览器会话调,固定带 csrf)。
func (e *apiTokenEnv) patch(t *testing.T, path, body string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPatch, e.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(headerCsrf, e.csrf)
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, string(raw)
}

// issueAdminToken 走真实端点给管理员自己签发一把令牌,返回明文。
func (e *apiTokenEnv) issueAdminToken(t *testing.T) string {
	t.Helper()
	resp, body := e.post(t, "/api/admin/api-tokens", `{"name":"测试令牌"}`, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("签发应 201,得 %d: %s", resp.StatusCode, body)
	}
	var out struct {
		Item      apiTokenDTO `json:"item"`
		Plaintext string      `json:"plaintext"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("解析签发响应: %v (%s)", err, body)
	}
	if out.Plaintext == "" || !strings.HasPrefix(out.Plaintext, "pwt_") {
		t.Fatalf("签发响应没带明文: %s", body)
	}
	return out.Plaintext
}

// TestAPIToken_RoundTrip 覆盖签发 → 列表 → 用 Bearer 调读端点(不需要 CSRF)。
func TestAPIToken_RoundTrip(t *testing.T) {
	e := newAPITokenEnv(t, true)
	plain := e.issueAdminToken(t)

	resp, body := e.get(t, "/api/admin/api-tokens", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("列表应 200,得 %d: %s", resp.StatusCode, body)
	}
	if strings.Contains(body, plain) {
		t.Fatalf("列表响应里出现了明文: %s", body)
	}

	resp, body = e.get(t, "/api/projects", plain)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Bearer 读端点应 200,得 %d: %s", resp.StatusCode, body)
	}
}

// TestAPIToken_WriteNeedsNoCSRF 是 MCP 能写的前提:令牌请求豁免 CSRF。
func TestAPIToken_WriteNeedsNoCSRF(t *testing.T) {
	e := newAPITokenEnv(t, true)
	plain := e.issueAdminToken(t)

	// 不绑仓库的项目:创建只需名字,不用先把 vault/git 也装配起来。
	resp, body := e.post(t, "/api/projects", `{"name":"令牌建的项目"}`, plain)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("Bearer 写端点应 201,得 %d: %s", resp.StatusCode, body)
	}
}

// TestAPIToken_InvalidBearerNoCookieFallback 是关键反向用例:同一请求既带无效 Bearer 又带
// 管理员的有效 cookie,必须 401。允许回落就等于「任何能诱导管理员浏览器发请求的地方,
// 都能冒充令牌持有者」。
func TestAPIToken_InvalidBearerNoCookieFallback(t *testing.T) {
	e := newAPITokenEnv(t, true)
	resp, body := e.get(t, "/api/projects", "pwt_"+strings.Repeat("0", 64))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无效 Bearer 应 401(且不回落 cookie),得 %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "invalid_api_token") {
		t.Fatalf("错误码应为 invalid_api_token: %s", body)
	}
	// 对照:同一个客户端的有效 cookie 走通(证明上面 401 是 Bearer 分支导致的,不是会话坏了)。
	resp, _ = e.get(t, "/api/projects", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("有效 cookie 应 200,得 %d", resp.StatusCode)
	}
}

// TestAPIToken_RevokeKillsNextCall 验撤销即时生效,并留下 api_token_revoke 审计。
func TestAPIToken_RevokeKillsNextCall(t *testing.T) {
	e := newAPITokenEnv(t, true)
	plain := e.issueAdminToken(t)
	listResp, body := e.get(t, "/api/admin/api-tokens", "")
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("列表应 200: %s", body)
	}
	var out struct {
		Items []apiTokenDTO `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("解析列表: %v", err)
	}
	if len(out.Items) != 1 {
		t.Fatalf("应有 1 条令牌: %s", body)
	}
	id := out.Items[0].ID

	resp, body := e.del(t, "/api/admin/api-tokens/"+id, "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("撤销应 204,得 %d: %s", resp.StatusCode, body)
	}
	if resp, body = e.get(t, "/api/projects", plain); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("撤销后应 401,得 %d: %s", resp.StatusCode, body)
	}

	// 撤销那条审计必须带着 name:删掉之后这是认出「是哪把」的唯一凭据。
	recs, err := e.audit.List(t.Context(), audit.ListFilter{Action: audit.ActionAPITokenRevoke})
	if err != nil {
		t.Fatalf("查审计: %v", err)
	}
	if len(recs.Entries) != 1 || recs.Entries[0].Detail["name"] != "测试令牌" {
		t.Fatalf("撤销审计缺失或没带名称: %+v", recs.Entries)
	}
	if recs.Entries[0].Actor == "" || recs.Entries[0].Actor == "anonymous" {
		t.Fatalf("审计 actor 没落到具体人: %q", recs.Entries[0].Actor)
	}
}

// TestAPIToken_AdminOnly 验普通用户的令牌不能进管理端点,但能用自己那把调业务端点。
func TestAPIToken_AdminOnly(t *testing.T) {
	e := newAPITokenEnv(t, true)
	// 管理员建一个普通用户,再替她签发令牌(userId 显式指向她)。
	resp, body := e.post(t, "/api/admin/users", `{"username":"dev01","password":"Dev#pass123","role":"user"}`, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("建用户应 201,得 %d: %s", resp.StatusCode, body)
	}
	var u struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &u); err != nil || u.ID == "" {
		t.Fatalf("解析用户响应: %v (%s)", err, body)
	}
	resp, body = e.post(t, "/api/admin/api-tokens", `{"name":"dev01 的","userId":"`+u.ID+`"}`, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("替他人签发应 201,得 %d: %s", resp.StatusCode, body)
	}
	var created struct {
		Item      apiTokenDTO `json:"item"`
		Plaintext string      `json:"plaintext"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("解析签发响应: %v", err)
	}
	if created.Item.UserID != u.ID || created.Item.OwnerName != "dev01" {
		t.Fatalf("归属人没落对: %+v", created.Item)
	}
	if created.Item.CreatedBy != "admin" {
		t.Fatalf("createdBy 应是签发操作人 admin: %+v", created.Item)
	}

	resp, body = e.get(t, "/api/admin/api-tokens", created.Plaintext)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("普通用户令牌不该进管理端点,得 %d: %s", resp.StatusCode, body)
	}
}

// TestAPIToken_BadRequests 覆盖入参门与未知令牌撤销。
func TestAPIToken_BadRequests(t *testing.T) {
	e := newAPITokenEnv(t, true)
	if resp, body := e.post(t, "/api/admin/api-tokens", `{"name":"  "}`, ""); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("空名称应 400,得 %d: %s", resp.StatusCode, body)
	}
	if resp, body := e.post(t, "/api/admin/api-tokens", `{"name":"n","userId":"no-such"}`, ""); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("未知归属人应 422,得 %d: %s", resp.StatusCode, body)
	}
	if resp, body := e.del(t, "/api/admin/api-tokens/no-such-id", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("撤销未知 id 应 404,得 %d: %s", resp.StatusCode, body)
	}
}

// TestAPIToken_NotWired 验优雅降级:没装配令牌服务时,带 Bearer 回 401 而不是 panic,
// 管理端点回 503。
func TestAPIToken_NotWired(t *testing.T) {
	e := newAPITokenEnv(t, false)
	if resp, _ := e.get(t, "/api/projects", "pwt_x"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未装配时 Bearer 应 401,得 %d", resp.StatusCode)
	}
	if resp, body := e.get(t, "/api/admin/api-tokens", ""); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未装配时管理端点应 503,得 %d: %s", resp.StatusCode, body)
	}
	// cookie 那条线不受影响。
	if resp, _ := e.get(t, "/api/projects", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("cookie 读端点应仍 200,得 %d", resp.StatusCode)
	}
}

// TestAPIToken_BrowserOnlyEndpoints 验 OAuth 绑定这类「必须有真实浏览器会话」的端点
// 用令牌调用会被挡下来,而不是写出一个绑定键为空的幽灵 state。
func TestAPIToken_BrowserOnlyEndpoints(t *testing.T) {
	e := newAPITokenEnv(t, true)
	plain := e.issueAdminToken(t)
	resp, body := e.get(t, "/api/oauth/gitlab/authorize", plain)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("令牌调 OAuth 授权入口应 422,得 %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "browser_session_required") {
		t.Fatalf("错误码应为 browser_session_required: %s", body)
	}
}

// TestAPIToken_ScopeRoundTrip 验两把尺从签发到生效的整条线:
// 请求体 → 库存列 → 响应回显 → 带这把令牌调业务端点时真的看不见范围外资源。
func TestAPIToken_ScopeRoundTrip(t *testing.T) {
	e := newAPITokenEnv(t, true)
	gA := e.seedGroup(t, "甲组")
	gB := e.seedGroup(t, "乙组")
	e.seedProject(t, "in-a", gA)
	e.seedProject(t, "in-b", gB)
	e.seedProject(t, "free", access.Ungrouped)

	item, plain := e.issueScoped(t, `{"name":"只看甲组","allowedTools":["get_run","list_projects"],"groupScope":["`+gA+`"]}`)
	if strings.Join(item.AllowedTools, ",") != "get_run,list_projects" {
		t.Fatalf("工具范围没回显: %+v", item.AllowedTools)
	}
	if strings.Join(item.GroupScope, ",") != gA {
		t.Fatalf("分组范围没回显: %+v", item.GroupScope)
	}

	// 数据面:限了甲组 → 乙组与未归组都不该出现(与 Decide 同一把尺,别漂成列表看得见)。
	resp, body := e.get(t, "/api/projects", plain)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Bearer 列表应 200,得 %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "in-a") || strings.Contains(body, "in-b") || strings.Contains(body, `"free"`) {
		t.Fatalf("范围没收窄列表: %s", body)
	}

	// 对照:同一人的浏览器会话仍看全量 —— 范围属于令牌,不属于人。
	resp, body = e.get(t, "/api/projects", "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "in-b") || !strings.Contains(body, `"free"`) {
		t.Fatalf("cookie 会话应看全量,得 %d: %s", resp.StatusCode, body)
	}

	// 列表端点回显范围,设置页才标得出「这把被收窄过」。
	resp, body = e.get(t, "/api/admin/api-tokens", "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"groupScope":["`+gA+`"]`) {
		t.Fatalf("列表应回显分组范围,得 %d: %s", resp.StatusCode, body)
	}

	// 审计里留下范围(但不留明文)。
	recs, err := e.audit.List(t.Context(), audit.ListFilter{Action: audit.ActionAPITokenCreate})
	if err != nil || len(recs.Entries) != 1 {
		t.Fatalf("签发审计缺失: %v %+v", err, recs.Entries)
	}
	if got := recs.Entries[0].Detail["groupScope"]; got == nil {
		t.Fatalf("签发审计该带分组范围: %+v", recs.Entries[0].Detail)
	}
}

// TestAPIToken_ScopeRejections 钉住签发层的入参门 —— 尤其「显式空数组」必须拒签:
// 存进库里读回来是「不限」,想什么都不给的令牌反而会拿到全量。
func TestAPIToken_ScopeRejections(t *testing.T) {
	e := newAPITokenEnv(t, true)
	gA := e.seedGroup(t, "甲组")

	cases := []struct {
		name, body, wantContains string
	}{
		{"工具显式空数组", `{"name":"n","allowedTools":[]}`, "至少给一个"},
		{"分组显式空数组", `{"name":"n","groupScope":[]}`, "至少给一个"},
		{"未知工具名", `{"name":"n","allowedTools":["no_such_tool"]}`, "no_such_tool"},
		{"未知分组 id", `{"name":"n","groupScope":["g-does-not-exist"]}`, "g-does-not-exist"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := e.post(t, "/api/admin/api-tokens", tc.body, "")
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("应 400,得 %d: %s", resp.StatusCode, body)
			}
			if !strings.Contains(body, tc.wantContains) {
				t.Fatalf("响应应点名 %q,得 %s", tc.wantContains, body)
			}
		})
	}

	// 对照:不填这两项仍然合法(存量客户端与「不限」都走这条)。
	item, _ := e.issueScoped(t, `{"name":"不限范围的"}`)
	if len(item.AllowedTools) != 0 || len(item.GroupScope) != 0 {
		t.Fatalf("缺省应是不限: %+v", item)
	}
	// 填合法值同样过。
	if _, plain := e.issueScoped(t, `{"name":"只读工具","allowedTools":["get_run"],"groupScope":["`+gA+`"]}`); plain == "" {
		t.Fatal("合法范围应签发成功")
	}
}

// TestAPIToken_ScopesPatch 验「改范围不用撤销重发」这条线,重点是三种入参形态各有不同结果:
// 带键 = 改这一把尺,[] = 解开这一把尺(与签发的「[] 拒签」相反),不带键 = 另一把尺一动不动。
func TestAPIToken_ScopesPatch(t *testing.T) {
	e := newAPITokenEnv(t, true)
	gA := e.seedGroup(t, "甲组")
	gB := e.seedGroup(t, "乙组")
	e.seedProject(t, "in-a", gA)
	e.seedProject(t, "in-b", gB)

	item, plain := e.issueScoped(t, `{"name":"改范围","allowedTools":["get_run"],"groupScope":["`+gA+`"]}`)

	// 只改分组:工具那把尺必须保持原样(「不带键 = 不动」是这次改动最容易写错的一条)。
	resp, body := e.patch(t, "/api/admin/api-tokens/"+item.ID, `{"groupScope":["`+gB+`"]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH 应 200,得 %d: %s", resp.StatusCode, body)
	}
	var out struct {
		Item apiTokenDTO `json:"item"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("解析响应: %v (%s)", err, body)
	}
	if strings.Join(out.Item.GroupScope, ",") != gB {
		t.Fatalf("分组没改过来: %+v", out.Item)
	}
	if strings.Join(out.Item.AllowedTools, ",") != "get_run" {
		t.Fatalf("没带工具这个键,范围不该被动: %+v", out.Item)
	}

	// 改完立刻生效:下一次带这把令牌调业务端点,看见的就是新组。
	// 这一步才是「撤销重发」与「就地改」的真正差别 —— 令牌没换,客户端不用重配。
	resp, body = e.get(t, "/api/projects", plain)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "in-b") || strings.Contains(body, "in-a") {
		t.Fatalf("分组范围没跟着改: %d %s", resp.StatusCode, body)
	}

	// 显式 [] = 解开这把尺,回到不限(签发时同样的写法会被拒,这里是反方向)。
	resp, body = e.patch(t, "/api/admin/api-tokens/"+item.ID, `{"groupScope":[]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("解开分组应 200,得 %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, `"groupScope":[]`) {
		t.Fatalf("解开后应回显空数组: %s", body)
	}
	resp, body = e.get(t, "/api/projects", plain)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "in-a") {
		t.Fatalf("解开分组后甲组项目又该看得见: %d %s", resp.StatusCode, body)
	}
}

// TestAPIToken_PatchRejections 钉住改范围的入参门。
func TestAPIToken_PatchRejections(t *testing.T) {
	e := newAPITokenEnv(t, true)
	gA := e.seedGroup(t, "甲组")
	item, _ := e.issueScoped(t, `{"name":"待改","groupScope":["`+gA+`"]}`)

	cases := []struct {
		name, path, body string
		wantStatus       int
		wantContains     string
	}{
		{"两个键都没带", item.ID, `{}`, http.StatusBadRequest, "没有要修改的范围"},
		{"未知工具名", item.ID, `{"allowedTools":["no_such_tool"]}`, http.StatusBadRequest, "no_such_tool"},
		{"未知分组 id", item.ID, `{"groupScope":["g-does-not-exist"]}`, http.StatusBadRequest, "g-does-not-exist"},
		{"令牌不存在", "00000000-0000-4000-8000-000000000000", `{"groupScope":[]}`, http.StatusNotFound, "api_token_not_found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := e.patch(t, "/api/admin/api-tokens/"+tc.path, tc.body)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("应 %d,得 %d: %s", tc.wantStatus, resp.StatusCode, body)
			}
			if !strings.Contains(body, tc.wantContains) {
				t.Fatalf("响应应点名 %q,得 %s", tc.wantContains, body)
			}
		})
	}

	// 改一次就该有一条能追的痕,而且绝不落明文。
	if _, body := e.patch(t, "/api/admin/api-tokens/"+item.ID, `{"allowedTools":["get_run"]}`); body == "" {
		t.Fatal("PATCH 响应体为空")
	}
	recs, err := e.audit.List(t.Context(), audit.ListFilter{Action: audit.ActionAPITokenUpdate})
	if err != nil || len(recs.Entries) != 1 {
		t.Fatalf("改范围审计缺失: %v %+v", err, recs.Entries)
	}
	if recs.Entries[0].Detail["name"] != "待改" {
		t.Fatalf("改范围审计该带令牌名: %+v", recs.Entries[0].Detail)
	}
}

// TestMCPToolsCatalog 验工具名册端点:它是设置页勾选项与签发校验的同一份来源。
func TestMCPToolsCatalog(t *testing.T) {
	e := newAPITokenEnv(t, true)
	resp, body := e.get(t, "/api/admin/mcp-tools", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("应 200,得 %d: %s", resp.StatusCode, body)
	}
	var out struct {
		Items []struct {
			Name     string `json:"name"`
			Title    string `json:"title"`
			ReadOnly bool   `json:"readOnly"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("解析名册: %v (%s)", err, body)
	}
	if len(out.Items) != 8 {
		t.Fatalf("名册应有 8 条工具,得 %d: %s", len(out.Items), body)
	}
	for _, it := range out.Items {
		if it.Name == "" || it.Title == "" {
			t.Fatalf("名册条目缺字段: %+v", it)
		}
	}
}
