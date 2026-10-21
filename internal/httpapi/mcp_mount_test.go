// mcp_mount_test.go 守的是「挂上去之后」这一段:内部调用真的回环穿过自己的路由(鉴权、
// 分页头、校验、审计都在真链路上跑一遍),以及没装配令牌时 /mcp 根本不存在。
// 工具自身的口径由 internal/mcpsrv 的测试负责,这里不重复。
package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/apitoken"
	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/mask"
	"github.com/fuuhoo/pipewright/internal/pipeline"
	"github.com/fuuhoo/pipewright/internal/project"
	"github.com/fuuhoo/pipewright/internal/run"
	"github.com/fuuhoo/pipewright/internal/users"
	"github.com/fuuhoo/pipewright/internal/vault"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpEnv 是「管理员会话 + 一把她的 API 令牌 + 已连接的 MCP 客户端」。
type mcpEnv struct {
	srv     *httptest.Server
	client  *http.Client
	csrf    string
	audit   audit.Recorder
	session *mcp.ClientSession
}

// newMCPEnv 装配一套带 project/pipeline/run 的真路由。跑构建用的是 run 包默认的桩执行器,
// 所以这里验的是「工具 → 路由 → 领域层」这条链通,不是构建本身。
func newMCPEnv(t *testing.T, withTokens bool) *mcpEnv {
	t.Helper()
	st := testStoreAuth(t)
	svc := auth.NewService(st.DB, nil, nil)
	if err := svc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	us := users.NewService(st.DB)
	if err := us.BootstrapAdminRow("admin", mustHash(t)); err != nil {
		t.Fatalf("bootstrap admin row: %v", err)
	}
	v := vault.New(st.DB, testMasterKey())
	psvc := project.New(st.DB, v, stubProber{branch: "main"})
	plsvc := pipeline.New(st.DB)
	rsvc := run.New(st.DB)
	pool := run.NewWorkerPool(rsvc)
	pool.Start()
	t.Cleanup(func() { pool.Stop(context.Background()) })
	rec := audit.New(st.DB, mask.NewMasker(), nil)

	opts := []Option{WithAudit(rec), WithUsers(us), WithVault(v), WithProjects(psvc),
		WithPipelines(plsvc), WithRuns(rsvc, pool)}
	if withTokens {
		opts = append(opts, WithAPITokens(apitoken.NewService(st.DB)))
	}
	srv := httptest.NewServer(New(testWebFSAuth(), svc, opts...))
	t.Cleanup(srv.Close)

	client := newTestClient(t)
	csrf := loginWithClient(t, client, srv.URL)
	e := &mcpEnv{srv: srv, client: client, csrf: csrf, audit: rec}
	if withTokens {
		e.session = e.dial(t, e.issueToken(t))
	}
	return e
}

// issueToken 用管理员会话签一把令牌并返回明文。
func (e *mcpEnv) issueToken(t *testing.T) string {
	t.Helper()
	resp := doJSON(t, e.client, http.MethodPost, e.srv.URL+"/api/admin/api-tokens", e.csrf, `{"name":"MCP 测试"}`)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("签发令牌应 201,得 %d: %s", resp.StatusCode, raw)
	}
	var out struct {
		Plaintext string `json:"plaintext"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Plaintext == "" {
		t.Fatalf("解析签发响应: %v (%s)", err, raw)
	}
	return out.Plaintext
}

// dial 用真 MCP 客户端连 /mcp。连不上即测试失败 —— initialize 握手就是一次带令牌的 POST。
func (e *mcpEnv) dial(t *testing.T, token string) *mcp.ClientSession {
	t.Helper()
	rt := roundTripWithBearer(token)
	client := mcp.NewClient(&mcp.Implementation{Name: "mcp-mount-test", Version: "v0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   e.srv.URL + "/mcp",
		HTTPClient: &http.Client{Transport: rt},
	}, nil)
	if err != nil {
		t.Fatalf("连接 /mcp: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func roundTripWithBearer(token string) roundTripperFunc {
	return func(req *http.Request) (*http.Response, error) {
		req.Header.Set("Authorization", "Bearer "+token)
		return http.DefaultTransport.RoundTrip(req)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// mcpCall 调一个工具并把 structuredContent 解进 out(传 nil 表示只关心是否报错)。
func mcpCall(t *testing.T, s *mcp.ClientSession, tool string, args map[string]any, out any) *mcp.CallToolResult {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("调用 %s: %v", tool, err)
	}
	if res.IsError {
		return res
	}
	if out == nil {
		return res
	}
	raw, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("解析 %s 输出 %s: %v", tool, raw, err)
	}
	return res
}

func mcpErrorText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if !res.IsError {
		t.Fatalf("期望工具报错")
	}
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, " / ")
}

// TestMCP_MountedEndToEnd 是一次完整走通:建项目 → 配流水线 → 触发 → 轮询到终态 → 读日志。
// 每一环都落在真实端点上,所以它也守住了「MCP 与浏览器共用同一套判定」这个架构前提。
func TestMCP_MountedEndToEnd(t *testing.T) {
	e := newMCPEnv(t, true)

	// 先经 REST 建一条仓库凭据:MCP 工具面刻意不含凭据管理,绑仓库的项目得有真凭据 id。
	credID := newGitCred(t, e.client, e.srv.URL, e.csrf, "ghp_token_for_mcp")

	var created struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	res := mcpCall(t, e.session, "create_project", map[string]any{
		"name":          "MCP 建的项目",
		"repoUrl":       "https://gitee.com/acme/shop.git",
		"credentialId":  credID,
		"defaultBranch": "main",
	}, &created)
	if res.IsError {
		t.Fatalf("create_project 失败: %s", mcpErrorText(t, res))
	}
	if created.ID == "" {
		t.Fatalf("没拿到项目 id: %+v", created)
	}

	// set_pipeline 打的是真实的保存期校验:这份 YAML 通不过,后面全是空谈。
	res = mcpCall(t, e.session, "set_pipeline", map[string]any{"projectId": created.ID, "pipelineYaml": importYAMLDoc}, nil)
	if res.IsError {
		t.Fatalf("set_pipeline 失败: %s", mcpErrorText(t, res))
	}

	// 非法 YAML 必须把平台的 422 与错误码带回给 AI。
	res = mcpCall(t, e.session, "set_pipeline", map[string]any{"projectId": created.ID, "pipelineYaml": "version: 9999\n"}, nil)
	if text := mcpErrorText(t, res); !strings.Contains(text, "422") {
		t.Fatalf("非法流水线应报 422: %s", text)
	}

	var triggered map[string]any
	res = mcpCall(t, e.session, "trigger_run", map[string]any{"projectId": created.ID, "branch": "main"}, &triggered)
	if res.IsError {
		t.Fatalf("trigger_run 失败: %s", mcpErrorText(t, res))
	}
	runID, _ := triggered["id"].(string)
	if runID == "" {
		t.Fatalf("没拿到运行 id: %+v", triggered)
	}

	// trigger 只发车,状态靠轮询 —— 这条就是给 AI 的「不要只看返回值」。
	var detail struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Steps  []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"steps"`
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if res := mcpCall(t, e.session, "get_run", map[string]any{"runId": runID}, &detail); res.IsError {
			t.Fatalf("get_run 失败: %s", mcpErrorText(t, res))
		}
		if run.IsTerminal(detail.Status) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("运行 %s 未到终态,最后状态 %q", runID, detail.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if detail.Status != run.StatusSuccess {
		t.Fatalf("期望 success,得 %q(步骤 %+v)", detail.Status, detail.Steps)
	}

	var logs struct {
		Text      string `json:"text"`
		Lines     int    `json:"lines"`
		NextSeq   int    `json:"nextSeq"`
		Complete  bool   `json:"complete"`
		Truncated bool   `json:"truncated"`
	}
	if res := mcpCall(t, e.session, "get_run_logs", map[string]any{"runId": runID}, &logs); res.IsError {
		t.Fatalf("get_run_logs 失败: %s", mcpErrorText(t, res))
	}
	if !logs.Complete {
		t.Fatalf("运行已终态,日志的 complete 却为 false: %+v", logs)
	}

	// 已结束的运行取消不了:平台错误码必须原样回到 AI 面前(否则它会一直重试)。
	res = mcpCall(t, e.session, "cancel_run", map[string]any{"runId": runID}, nil)
	if text := mcpErrorText(t, res); !strings.Contains(text, "run_not_cancelable") {
		t.Fatalf("取消已结束运行应报 run_not_cancelable: %s", text)
	}

	// 列表走的是真实分页:总数取自 X-Total-Count(回环的 bufferWriter 必须把响应头带回来)。
	var listed struct {
		Projects []struct {
			ID string `json:"id"`
		} `json:"projects"`
		Total string `json:"total"`
	}
	if res := mcpCall(t, e.session, "list_projects", map[string]any{"pageSize": 5}, &listed); res.IsError {
		t.Fatalf("list_projects 失败: %s", mcpErrorText(t, res))
	}
	if listed.Total != "1" || len(listed.Projects) != 1 || listed.Projects[0].ID != created.ID {
		t.Fatalf("列表/分页头不对: %+v", listed)
	}

	// 写操作的审计必须认得出人:同一套路由、同一个归属人,MCP 这条入口落的 actor
	// 应与浏览器那条入口逐字相同(不是 anonymous/system 之类的占位)。
	browserActor := createProjectViaCookie(t, e, "浏览器建的项目")
	recs, err := e.audit.List(t.Context(), audit.ListFilter{Action: audit.ActionProjectCreate})
	if err != nil {
		t.Fatalf("查审计: %v", err)
	}
	var mcpActors []string
	for _, r := range recs.Entries {
		if r.Detail["name"] == "MCP 建的项目" {
			mcpActors = append(mcpActors, r.Actor)
			// 来源 IP 也要有:回环请求是自己造的、没有真 socket,不补这一笔,MCP 操作的
			// 审计就只有「谁做的」没有「从哪做的」。
			if r.IP == "" {
				t.Fatalf("MCP 审计行没带来源 IP: %+v", r)
			}
		}
	}
	if len(mcpActors) != 1 {
		t.Fatalf("MCP 建项目应留下一条可认的 project_create 审计,得 %v", mcpActors)
	}
	if mcpActors[0] != browserActor {
		t.Fatalf("两条入口的 actor 不一致:MCP %q,浏览器 %q", mcpActors[0], browserActor)
	}
	if strings.Contains(mcpActors[0], "system") || strings.Contains(mcpActors[0], "anonymous") {
		t.Fatalf("actor 落成了占位值: %q", mcpActors[0])
	}
}

// createProjectViaCookie 用管理员的会话 cookie 建一个项目,返回它在审计里落下的 actor。
func createProjectViaCookie(t *testing.T, e *mcpEnv, name string) string {
	t.Helper()
	resp := doJSON(t, e.client, http.MethodPost, e.srv.URL+"/api/projects", e.csrf, `{"name":`+strconv.Quote(name)+`}`)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("cookie 建项目应 201,得 %d: %s", resp.StatusCode, raw)
	}
	recs, err := e.audit.List(t.Context(), audit.ListFilter{Action: audit.ActionProjectCreate})
	if err != nil {
		t.Fatalf("查审计: %v", err)
	}
	for _, r := range recs.Entries {
		if r.Detail["name"] == name {
			return r.Actor
		}
	}
	t.Fatalf("浏览器建的项目没留下审计: %s", name)
	return ""
}

func TestMCP_SessionOnlyWithoutTokens(t *testing.T) {
	// 没装配 API 令牌服务:连 /mcp 这条路由都不存在(请求落到 SPA 回退),
	// 而不是留一个「永远 401、看起来像坏了」的端点。
	e := newMCPEnv(t, false)
	req, err := http.NewRequest(http.MethodPost, e.srv.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode >= 500 {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("未装配令牌时 /mcp 不该被挂上,得 %d: %s", resp.StatusCode, raw)
	}
}

func TestMCP_CookieAloneCannotEnter(t *testing.T) {
	// 带着管理员的有效会话 cookie 打 /mcp 仍然 401:回落 cookie 等于把
	// 「诱导管理员浏览器发一次请求」变成一次以她名义的构建。
	e := newMCPEnv(t, true)
	req, err := http.NewRequest(http.MethodPost, e.srv.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := e.client.Do(req) // e.client 自带 cookie jar
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("只带 cookie 应 401,得 %d: %s", resp.StatusCode, raw)
	}
	if !strings.Contains(resp.Header.Get("WWW-Authenticate"), "Bearer") {
		t.Fatalf("应带 WWW-Authenticate: %q", resp.Header.Get("WWW-Authenticate"))
	}
}
