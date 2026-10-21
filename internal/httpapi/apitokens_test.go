// apitokens_test.go 守的是 API 令牌这条机器凭据线:发放/撤销的 HTTP 契约、Bearer 鉴权
// 分支(含「带无效 Bearer 时不得回落到 cookie」这条防 confused deputy 的规矩)、以及
// 明文只回显一次的纪律。
package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/apitoken"
	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/mask"
	"github.com/fuuhoo/pipewright/internal/project"
	"github.com/fuuhoo/pipewright/internal/users"
)

// apiTokenEnv 是一次装配好的测试环境:服务 + 已登录(admin)的 cookie 客户端 + csrf。
type apiTokenEnv struct {
	srv    *httptest.Server
	client *http.Client
	csrf   string
	audit  audit.Recorder
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
	tok := apitoken.NewService(st.DB)
	rec := audit.New(st.DB, mask.NewMasker(), nil)
	opts := []Option{WithAudit(rec), WithUsers(us), WithProjects(project.New(st.DB, nil, nil))}
	if withTokens {
		opts = append(opts, WithAPITokens(tok))
	}
	srv := httptest.NewServer(New(testWebFSAuth(), svc, opts...))
	t.Cleanup(srv.Close)
	client := newTestClient(t)
	csrf := loginWithClient(t, client, srv.URL)
	return &apiTokenEnv{srv: srv, client: client, csrf: csrf, audit: rec}
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
