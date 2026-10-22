// mcpsrv_test.go 守 MCP 工具面两条命门:①/mcp 边界的 Bearer 鉴权(没有令牌、令牌无效、
// 令牌未启用都是 401,且绝不回落到会话 cookie);②工具以「调用方那把令牌」打内部 REST,
// 于是不必在本包重写任何权限判定。
package mcpsrv

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/apitoken"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---- 测试替身 ----

const goodToken = "pwt_good"

// stubRoute 是一条假的内部 REST 响应。
type stubRoute struct {
	method string
	path   string
	status int
	body   string
	header http.Header
}

// stubCall 记录工具实际发出的那次内部调用。
type stubCall struct {
	method     string
	path       string
	query      string
	auth       string
	body       string
	clientAddr string
}

// stubAPI 是「平台自己的 REST」的替身:按 (method, path) 命中预设响应,同时记下每一笔调用,
// 用来断言 Authorization 头带的就是调用方那把令牌。
type stubAPI struct {
	routes []stubRoute
	calls  []stubCall
}

func (s *stubAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.calls = append(s.calls, stubCall{
		method: r.Method, path: r.URL.Path, query: r.URL.RawQuery,
		auth: r.Header.Get("Authorization"), body: string(body),
		clientAddr: r.RemoteAddr,
	})
	for _, rt := range s.routes {
		if rt.method != r.Method || rt.path != r.URL.Path {
			continue
		}
		for k, vs := range rt.header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		status := rt.status
		if status == 0 {
			status = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, rt.body)
		return
	}
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"没有这条假路由"}}`)
}

// fakeVerifier 只认 goodToken,其余一律 apitoken.ErrInvalid(与真实服务同口径)。
// allowed 模拟这把令牌所属端点的工具白名单:留空即「不限」,与 apitoken 同口径。
type fakeVerifier struct {
	allowed []string
}

func (v fakeVerifier) Verify(_ context.Context, plain string) (*apitoken.Principal, error) {
	if plain != goodToken {
		return nil, apitoken.ErrInvalid
	}
	return &apitoken.Principal{TokenID: "tok-1", UserID: "u-1", Username: "admin", Role: "admin",
		AllowedTools: v.allowed}, nil
}

// errVerifier 模拟「校验器本身故障」(库连不上):同样只能判失败,不能放行。
type errVerifier struct{}

func (errVerifier) Verify(context.Context, string) (*apitoken.Principal, error) {
	return nil, errors.New("apitoken: db down")
}

// newEnv 装配「假 REST + 真 MCP handler」并起一个 httptest 服务器。
func newEnv(t *testing.T, routes []stubRoute, tokens TokenVerifier) (*stubAPI, *httptest.Server) {
	t.Helper()
	api := &stubAPI{routes: routes}
	svc := New(tokens, api)
	srv := httptest.NewServer(svc.Handler())
	t.Cleanup(srv.Close)
	return api, srv
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// dial 用真 MCP 客户端连上 /mcp;token 为 Authorization 里要带的令牌明文。
// 连上就意味着 initialize 握手已过 —— 鉴权挡不住时才会连上。
func dial(t *testing.T, url, token string) *mcp.ClientSession {
	t.Helper()
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return http.DefaultTransport.RoundTrip(req)
	})
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   url,
		HTTPClient: &http.Client{Transport: rt},
	}, nil)
	if err != nil {
		t.Fatalf("连接 /mcp 失败: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

// call 调一个工具并把 structuredContent 解进 out;out 传 nil 表示只关心错误。
func call(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any, out any) *mcp.CallToolResult {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("调用 %s: %v", tool, err)
	}
	if res.IsError {
		return res
	}
	if out == nil {
		return res
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("重新编码 %s 的输出: %v", tool, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("解析 %s 的输出 %s: %v", tool, raw, err)
	}
	return res
}

// toolErrorText 取出 IsError 结果里的可读文本,用来断言错误码确实透传给了 AI。
func toolErrorText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if !res.IsError {
		t.Fatalf("期望工具报错,实际成功")
	}
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// ---- 边界鉴权 ----

// TestMCP_AuthDenied 覆盖三条 401 分支。都用裸 JSON-RPC 打,因为客户端在这里连不上才是重点。
func TestMCP_AuthDenied(t *testing.T) {
	routes := []stubRoute{{method: http.MethodGet, path: "/api/projects", body: `[]`}}

	cases := []struct {
		name       string
		routes     []stubRoute
		tokens     TokenVerifier
		auth       string
		cookie     string
		wantCode   string
		wantHeader string
	}{
		{name: "没带令牌", routes: routes, tokens: fakeVerifier{}, wantCode: "unauthorized"},
		{name: "空 Bearer", routes: routes, tokens: fakeVerifier{}, auth: "Bearer    ", wantCode: "unauthorized"},
		{name: "非 Bearer 方案", routes: routes, tokens: fakeVerifier{}, auth: "Basic zzz", wantCode: "unauthorized"},
		{name: "令牌无效", routes: routes, tokens: fakeVerifier{}, auth: "Bearer pwt_wrong", wantCode: "invalid_api_token"},
		{name: "校验器故障", routes: routes, tokens: errVerifier{}, auth: "Bearer " + goodToken, wantCode: "invalid_api_token"},
		// 这套实例没装配令牌服务:New 传进去的是 nil 接口,任何令牌都换不到通道。
		{name: "令牌未启用", routes: routes, tokens: nil, auth: "Bearer " + goodToken, wantCode: "unauthorized"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api, srv := newEnv(t, tc.routes, tc.tokens)
			req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(
				`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"x","version":"1"}}}`))
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			if tc.cookie != "" {
				req.Header.Set("Cookie", tc.cookie)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			defer resp.Body.Close()
			raw, _ := io.ReadAll(resp.Body)

			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("应 401,得 %d: %s", resp.StatusCode, raw)
			}
			if !strings.Contains(resp.Header.Get("WWW-Authenticate"), `error="`+tc.wantCode+`"`) {
				t.Fatalf("WWW-Authenticate 缺错误码: %q", resp.Header.Get("WWW-Authenticate"))
			}
			var env struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(raw, &env); err != nil {
				t.Fatalf("正文应是平台错误信封: %v (%s)", err, raw)
			}
			if env.Error.Code != tc.wantCode || env.Error.Message == "" {
				t.Fatalf("错误信封不对: %+v", env.Error)
			}
			// 关键反向断言:被拒的请求一个字节都不该进到内部 REST。
			if len(api.calls) != 0 {
				t.Fatalf("401 之前就有内部调用: %+v", api.calls)
			}
		})
	}
}

// TestMCP_NoCookieFallback 是防 confused deputy 的那条规矩:MCP 边界只认 Bearer,
// 带着一份有效会话 cookie 也进不来(否则任何能诱导管理员发请求的页面都能借她的会话建项目)。
func TestMCP_NoCookieFallback(t *testing.T) {
	api, srv := newEnv(t, []stubRoute{{method: http.MethodGet, path: "/api/projects", body: `[]`}}, fakeVerifier{})
	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Cookie", "pipewright_session=valid-looking; pipewright_csrf=abc")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("只带 cookie 应 401,得 %d: %s", resp.StatusCode, raw)
	}
	if len(api.calls) != 0 {
		t.Fatalf("cookie 请求不该产生内部调用: %+v", api.calls)
	}
}

func TestBearerToken(t *testing.T) {
	cases := []struct {
		header      string
		wantPlain   string
		wantPresent bool
	}{
		{header: "", wantPresent: false},
		{header: "Bearer", wantPresent: false},
		{header: "bearer pwt_x", wantPresent: false}, // scheme 大小写敏感:与 requireAuth 同一把尺
		{header: "Bearer ", wantPresent: false},
		{header: "Bearer pwt_x", wantPlain: "pwt_x", wantPresent: true},
		{header: "Bearer   pwt_x  ", wantPlain: "pwt_x", wantPresent: true},
	}
	for _, tc := range cases {
		got, ok := bearerToken(tc.header)
		if ok != tc.wantPresent || got != tc.wantPlain {
			t.Fatalf("bearerToken(%q) = %q,%v,期望 %q,%v", tc.header, got, ok, tc.wantPlain, tc.wantPresent)
		}
	}
}

// ---- 工具面 ----

// TestMCP_ToolsListed 冻结工具清单:8 个,一个不多一个不少。新增工具必须同时补
// instructions 与文档,这个断言就是为了逼这一步不被忘掉。
func TestMCP_ToolsListed(t *testing.T) {
	_, srv := newEnv(t, nil, fakeVerifier{})
	session := dial(t, srv.URL, goodToken)
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	want := []string{"list_projects", "create_project", "set_pipeline", "list_build_envs",
		"trigger_run", "get_run", "get_run_logs", "cancel_run"}
	if len(res.Tools) != len(want) {
		t.Fatalf("工具数量 %d,期望 %d: %+v", len(res.Tools), len(want), res.Tools)
	}
	got := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		got[tool.Name] = tool
	}
	for _, name := range want {
		tool, ok := got[name]
		if !ok {
			t.Fatalf("缺工具 %s", name)
		}
		if tool.Description == "" {
			t.Fatalf("%s 没有描述,AI 无从判断何时调用", name)
		}
		if tool.Annotations == nil || tool.Annotations.Title == "" {
			t.Fatalf("%s 缺标注", name)
		}
	}
	// 只读位决定客户端是否自动放行,标错比不标更危险。
	for _, name := range []string{"list_projects", "list_build_envs", "get_run", "get_run_logs"} {
		if !got[name].Annotations.ReadOnlyHint {
			t.Fatalf("%s 应标为只读", name)
		}
	}
	for _, name := range []string{"create_project", "set_pipeline", "trigger_run", "cancel_run"} {
		if got[name].Annotations.ReadOnlyHint {
			t.Fatalf("%s 是写操作,不该标只读", name)
		}
	}
}

// scopedVerifier 按令牌明文给出不同范围,模拟「两把属于不同端点的令牌打同一个 /mcp」。
// 这是 per-请求建 server 的反向守卫:一旦有人把 server 收成单例,第二个连上来的端点
// 就会沿用第一个的工具面。
type scopedVerifier struct {
	byToken map[string][]string
}

func (v scopedVerifier) Verify(_ context.Context, plain string) (*apitoken.Principal, error) {
	allowed, ok := v.byToken[plain]
	if !ok {
		return nil, apitoken.ErrInvalid
	}
	return &apitoken.Principal{TokenID: "tok-" + plain, UserID: "u-1", Username: "admin",
		Role: "admin", AllowedTools: allowed}, nil
}

// dialInMemory 用内存管道把真客户端接到一份裸 server 上(绕开 HTTP 边界与鉴权),
// 只用来问「这份 server 注册了哪些工具」。
func dialInMemory(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { serverSession.Wait() })
	client := mcp.NewClient(&mcp.Implementation{Name: "catalog-client", Version: "v0"}, nil)
	session, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func listTools(t *testing.T, session *mcp.ClientSession) []*mcp.Tool {
	t.Helper()
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	return res.Tools
}

func namesOf(tools []*mcp.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Name)
	}
	return out
}

func listedNames(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()
	return namesOf(listTools(t, session))
}

// TestMCP_CatalogMatchesServer 防 catalog.go 与 registerTools 两处漂移。清单是签发接口
// 校验「勾的工具名确实存在」和设置页画勾选框的唯一依据:少一项,那个工具就没法被限;
// 多一项,就能签出一把指向不存在工具的令牌。标题与只读位一起比,因为前端直接展示它们。
func TestMCP_CatalogMatchesServer(t *testing.T) {
	tools := listTools(t, dialInMemory(t, buildServer(nil)))
	catalog := ToolCatalog()
	if len(tools) != len(catalog) {
		t.Fatalf("注册了 %d 个工具,catalog.go 写了 %d 个:%v", len(tools), len(catalog), namesOf(tools))
	}
	byName := map[string]*mcp.Tool{}
	for _, tool := range tools {
		byName[tool.Name] = tool
	}
	for _, info := range catalog {
		tool, ok := byName[info.Name]
		if !ok {
			t.Fatalf("清单有 %s 但 server 没注册(registerTools 漏了?)", info.Name)
		}
		if tool.Annotations == nil {
			t.Fatalf("%s 缺标注", info.Name)
		}
		if tool.Annotations.Title != info.Title {
			t.Fatalf("%s 标题漂移:注册 %q,清单 %q", info.Name, tool.Annotations.Title, info.Title)
		}
		if tool.Annotations.ReadOnlyHint != info.ReadOnly {
			t.Fatalf("%s 只读位漂移:注册 %v,清单 %v", info.Name, tool.Annotations.ReadOnlyHint, info.ReadOnly)
		}
	}
}

// TestMCP_ToolFilter 验白名单落在「注册」这一步:没勾的工具既不在 tools/list 里,
// 也没有 handler 可被调到 —— 于是不需要在每个工具内部再判一次权限(多一处会忘记同步的分支)。
func TestMCP_ToolFilter(t *testing.T) {
	cases := []struct {
		name    string
		allowed []string
		want    []string
	}{
		// 空 = 不限,这是 0078 之前那些存量令牌的形状,必须维持「八个全在」。
		{name: "不限", allowed: nil, want: ToolNames()},
		{name: "只给两个读工具", allowed: []string{"get_run", "get_run_logs"}, want: []string{"get_run", "get_run_logs"}},
		{name: "只给一个写工具", allowed: []string{"trigger_run"}, want: []string{"trigger_run"}},
		// 不认识的名字匹配不到任何工具,自然被忽略;拦住这种输入是签发接口的事。
		{name: "混入未知名字", allowed: []string{"get_run", "no_such_tool"}, want: []string{"get_run"}},
		{name: "去重后只剩一个", allowed: []string{"get_run", "get_run"}, want: []string{"get_run"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := listedNames(t, dialInMemory(t, buildServer(tc.allowed)))
			if len(got) != len(tc.want) {
				t.Fatalf("暴露了 %v,期望 %v", got, tc.want)
			}
			set := map[string]bool{}
			for _, n := range tc.want {
				set[n] = true
			}
			for _, n := range got {
				if !set[n] {
					t.Fatalf("多暴露了 %s(期望 %v)", n, tc.want)
				}
			}
		})
	}
}

// TestMCP_EndpointScopeEnforced 走完整边界:一把只勾了 get_run 的令牌连上 /mcp 之后,
// tools/list 只剩那一个、白名单外的调用命不中 handler,而且一笔内部 REST 都不许多发。
// 最后一段是关键:同一个 handler 上两把不同范围的令牌必须各看各的工具面。
func TestMCP_EndpointScopeEnforced(t *testing.T) {
	t.Run("窄令牌看不见也调不到", func(t *testing.T) {
		api, srv := newEnv(t, []stubRoute{
			{method: http.MethodGet, path: "/api/runs/r1", body: `{"id":"r1","status":"running"}`},
			{method: http.MethodGet, path: "/api/projects", body: `[]`},
		}, fakeVerifier{allowed: []string{"get_run"}})
		session := dial(t, srv.URL, goodToken)

		if got := listedNames(t, session); len(got) != 1 || got[0] != "get_run" {
			t.Fatalf("端点只该露 get_run,得 %v", got)
		}
		var detail map[string]any
		call(t, session, "get_run", map[string]any{"runId": "r1"}, &detail)
		if detail["status"] != "running" {
			t.Fatalf("白名单内的工具该照常工作: %+v", detail)
		}
		if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "list_projects", Arguments: map[string]any{}}); err == nil ||
			!strings.Contains(err.Error(), "unknown tool") {
			t.Fatalf("白名单外的调用应命不中 handler,得 %v", err)
		}
		if len(api.calls) != 1 || api.calls[0].path != "/api/runs/r1" {
			t.Fatalf("越界调用不该打到内部 REST: %+v", api.calls)
		}
	})

	t.Run("同一端点上两把令牌互不继承", func(t *testing.T) {
		api, srv := newEnv(t, []stubRoute{{method: http.MethodGet, path: "/api/projects", body: `[]`}},
			scopedVerifier{byToken: map[string][]string{
				"pwt_narrow": {"get_run"},
				"pwt_full":   nil,
			}})
		narrow := dial(t, srv.URL, "pwt_narrow")
		if got := listedNames(t, narrow); len(got) != 1 || got[0] != "get_run" {
			t.Fatalf("窄令牌看到 %v,期望只有 get_run", got)
		}
		// 反过来连一次:宽令牌必须仍然是八个,不能沿用上一个请求的裁剪结果。
		full := dial(t, srv.URL, "pwt_full")
		if got := listedNames(t, full); len(got) != len(ToolNames()) {
			t.Fatalf("宽令牌看到 %v,期望 %v", got, ToolNames())
		}
		if len(api.calls) != 0 {
			t.Fatalf("只是列出工具不该产生内部调用: %+v", api.calls)
		}
	})
}

// TestMCP_ProjectAndPipelineFlow 走「建项目 → 配流水线 → 触发 → 轮询 → 读日志 → 取消」全链,
// 全部经真实的工具实现 + 假 REST,并逐笔核对内部调用带的就是调用方令牌。
func TestMCP_ProjectAndPipelineFlow(t *testing.T) {
	listBody := `[{"id":"p1","name":"商店","repoUrl":"","defaultBranch":"main"}]`
	envsBody := `{"items":[{"id":"be-node","language":"node","displayName":"Node 22","image":"node:22"}]}`
	api, srv := newEnv(t, []stubRoute{
		{method: http.MethodGet, path: "/api/projects", body: listBody, header: http.Header{"X-Total-Count": {"1"}}},
		{method: http.MethodPost, path: "/api/projects", status: http.StatusCreated,
			body: `{"id":"p2","name":"新项目","defaultBranch":"main"}`},
		{method: http.MethodPost, path: "/api/projects/p2/pipeline/import",
			body: `{"stages":[{"id":"stg_build","name":"构建"}],"yaml":"version: 1\n"}`},
		{method: http.MethodGet, path: "/api/build-envs", body: envsBody},
		{method: http.MethodPost, path: "/api/projects/p2/runs", status: http.StatusCreated,
			body: `{"id":"r1","projectId":"p2","status":"queued","steps":[]}`},
		{method: http.MethodGet, path: "/api/runs/r1", body: `{"id":"r1","status":"success","steps":[{"name":"打包","status":"success"}]}`},
		{method: http.MethodGet, path: "/api/runs/r1/logs", body: `{"lines":[
			{"seq":1,"ts":"2026-10-21T00:00:00Z","stream":"stdout","stepOrdinal":1,"text":"第一步"},
			{"seq":2,"ts":"2026-10-21T00:00:01Z","stream":"stderr","stepOrdinal":1,"text":"警告一行"},
			{"seq":3,"ts":"2026-10-21T00:00:02Z","stream":"stdout","stepOrdinal":2,"text":"结束"}],
			"nextSeq":4,"complete":true}`},
		{method: http.MethodPost, path: "/api/runs/r1/cancel", body: `{"id":"r1","status":"canceled"}`},
	}, fakeVerifier{})

	session := dial(t, srv.URL, goodToken)

	t.Run("list_projects", func(t *testing.T) {
		var out struct {
			Projects []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"projects"`
			Page     int    `json:"page"`
			PageSize int    `json:"pageSize"`
			Total    string `json:"total"`
		}
		call(t, session, "list_projects", map[string]any{}, &out)
		if len(out.Projects) != 1 || out.Projects[0].ID != "p1" {
			t.Fatalf("项目列表没透传 REST 数组: %+v", out)
		}
		if out.Page != 1 || out.PageSize != 50 {
			t.Fatalf("分页回显不对: %+v", out)
		}
		if out.Total != "1" {
			t.Fatalf("总数应取自 X-Total-Count: %+v", out)
		}
	})

	t.Run("create_project 不带仓库", func(t *testing.T) {
		var out map[string]any
		call(t, session, "create_project", map[string]any{"name": "新项目"}, &out)
		if out["id"] != "p2" {
			t.Fatalf("应透传新建项目 DTO: %+v", out)
		}
	})

	t.Run("set_pipeline", func(t *testing.T) {
		var out map[string]any
		call(t, session, "set_pipeline", map[string]any{"projectId": "p2", "pipelineYaml": "version: 1\nstages: []\n"}, &out)
		if out["stages"] == nil {
			t.Fatalf("没拿到规范化流水线: %+v", out)
		}
	})

	t.Run("list_build_envs", func(t *testing.T) {
		var out struct {
			Items []map[string]any `json:"items"`
		}
		call(t, session, "list_build_envs", map[string]any{"language": "node"}, &out)
		if len(out.Items) != 1 || out.Items[0]["id"] != "be-node" {
			t.Fatalf("构建环境列表不对: %+v", out)
		}
	})

	t.Run("trigger_run 到 cancel_run", func(t *testing.T) {
		var triggered map[string]any
		call(t, session, "trigger_run", map[string]any{"projectId": "p2", "params": map[string]string{"K": "V"}}, &triggered)
		if triggered["id"] != "r1" {
			t.Fatalf("触发没回运行详情: %+v", triggered)
		}

		var detail map[string]any
		call(t, session, "get_run", map[string]any{"runId": "r1"}, &detail)
		if detail["status"] != "success" {
			t.Fatalf("运行详情状态不对: %+v", detail)
		}

		var logs runLogsOutput
		call(t, session, "get_run_logs", map[string]any{"runId": "r1"}, &logs)
		if logs.Lines != 3 || logs.Truncated {
			t.Fatalf("预算内应全量返回: %+v", logs)
		}
		if !strings.Contains(logs.Text, "[step1 stderr] 警告一行") {
			t.Fatalf("日志没标步骤与流: %q", logs.Text)
		}
		if logs.NextSeq != 4 || !logs.Complete {
			t.Fatalf("续读位/终态位不对: %+v", logs)
		}

		var canceled map[string]any
		call(t, session, "cancel_run", map[string]any{"runId": "r1"}, &canceled)
		if canceled["status"] != "canceled" {
			t.Fatalf("取消没透传: %+v", canceled)
		}
	})

	// 逐笔核对内部调用:方法/路径/查询串/令牌。这套工具的全部价值就落在「以调用方身份打 REST」。
	want := []struct {
		method, path, query string
		contains            string // 请求体里应含的片段
	}{
		{http.MethodGet, "/api/projects", "page=1&pageSize=50", ""},
		{http.MethodPost, "/api/projects", "", `"name":"新项目"`},
		{http.MethodPost, "/api/projects/p2/pipeline/import", "", `"save":true`},
		{http.MethodGet, "/api/build-envs", "language=node", ""},
		{http.MethodPost, "/api/projects/p2/runs", "", `"params":{"K":"V"}`},
		{http.MethodGet, "/api/runs/r1", "", ""},
		{http.MethodGet, "/api/runs/r1/logs", "sinceSeq=0", ""},
		{http.MethodPost, "/api/runs/r1/cancel", "", ""},
	}
	if len(api.calls) != len(want) {
		t.Fatalf("内部调用笔数 %d,期望 %d: %+v", len(api.calls), len(want), api.calls)
	}
	for i, c := range api.calls {
		w := want[i]
		if c.method != w.method || c.path != w.path || c.query != w.query {
			t.Fatalf("第 %d 笔调用 = %s %s?%s,期望 %s %s?%s", i, c.method, c.path, c.query, w.method, w.path, w.query)
		}
		if c.auth != "Bearer "+goodToken {
			t.Fatalf("第 %d 笔没带调用方令牌: %q", i, c.auth)
		}
		// 来源地址要一路带到内部请求上:审计里的 IP 只能从这里来,回环请求自己没有 socket。
		if c.clientAddr == "" {
			t.Fatalf("第 %d 笔内部调用没带来源地址", i)
		}
		if w.contains != "" && !strings.Contains(c.body, w.contains) {
			t.Fatalf("第 %d 笔请求体缺 %q: %s", i, w.contains, c.body)
		}
	}
}

// TestMCP_PlatformErrorsReachTheAI 验平台错误码原样进工具错误文本 —— instructions 让 AI
// 「按码修正再重试」,码丢了那条指引就是空话。
func TestMCP_PlatformErrorsReachTheAI(t *testing.T) {
	api, srv := newEnv(t, []stubRoute{
		{method: http.MethodPost, path: "/api/projects/p9/pipeline/import", status: http.StatusUnprocessableEntity,
			body: `{"error":{"code":"invalid_yaml","message":"第 3 行缺 stages"}}`},
		{method: http.MethodPost, path: "/api/runs/r9/cancel", status: http.StatusConflict,
			body: `{"error":{"code":"run_not_cancelable","message":"运行已经结束"}}`},
	}, fakeVerifier{})
	session := dial(t, srv.URL, goodToken)

	res := call(t, session, "set_pipeline", map[string]any{"projectId": "p9", "pipelineYaml": "bad:"}, nil)
	text := toolErrorText(t, res)
	for _, frag := range []string{"422", "invalid_yaml", "第 3 行缺 stages"} {
		if !strings.Contains(text, frag) {
			t.Fatalf("错误文本缺 %q: %s", frag, text)
		}
	}

	res = call(t, session, "cancel_run", map[string]any{"runId": "r9"}, nil)
	if text = toolErrorText(t, res); !strings.Contains(text, "run_not_cancelable") {
		t.Fatalf("取消错误应带 run_not_cancelable: %s", text)
	}
	if len(api.calls) != 2 {
		t.Fatalf("应有两笔内部调用: %+v", api.calls)
	}
}

// TestMCP_LocalGuards 验工具自己那两道门(空项目名 / 空 YAML)挡在发请求之前,
// 免得把一个必然 400 的请求打到 REST 再落进审计。
func TestMCP_LocalGuards(t *testing.T) {
	api, srv := newEnv(t, nil, fakeVerifier{})
	session := dial(t, srv.URL, goodToken)

	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"create_project", map[string]any{"name": "   "}},
		{"set_pipeline", map[string]any{"projectId": "p1", "pipelineYaml": ""}},
	} {
		res := call(t, session, tc.tool, tc.args, nil)
		if text := toolErrorText(t, res); text == "" {
			t.Fatalf("%s 没给出错误说明", tc.tool)
		}
	}
	if len(api.calls) != 0 {
		t.Fatalf("本地就该挡住的请求打到了 REST: %+v", api.calls)
	}
}

// TestMCP_BadArguments 验必填缺失由入参 schema 挡下(SDK 生成 required),
// 而不是让工具拿到一个空 projectId 去拼出一个怪路径。
func TestMCP_BadArguments(t *testing.T) {
	api, srv := newEnv(t, nil, fakeVerifier{})
	session := dial(t, srv.URL, goodToken)

	res := call(t, session, "get_run", map[string]any{}, nil)
	if text := toolErrorText(t, res); !strings.Contains(text, "runId") {
		t.Fatalf("应提示缺 runId: %s", text)
	}
	if len(api.calls) != 0 {
		t.Fatalf("入参不合法却打了 REST: %+v", api.calls)
	}
}

// TestMCP_AssemblyFailure 验「拿不到内部调用通道」时宁可报错:绕过 Handler 直连 server
// 就没有 withEndpoint,工具必须回一条错误,而不是静默成功 —— 后者等于放行一次
// 没有任何身份的调用。
func TestMCP_AssemblyFailure(t *testing.T) {
	server := buildServer(nil)
	api := &stubAPI{routes: []stubRoute{{method: http.MethodGet, path: "/api/runs/r1", body: `{"id":"r1"}`}}}
	sink := &stubAPI{}

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer serverSession.Wait()

	client := mcp.NewClient(&mcp.Implementation{Name: "bare-client", Version: "v0"}, nil)
	session, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer session.Close()

	res := call(t, session, "get_run", map[string]any{"runId": "r1"}, nil)
	if text := toolErrorText(t, res); !strings.Contains(text, "装配") {
		t.Fatalf("缺通道时应当报装配错误: %s", text)
	}
	if len(api.calls) != 0 || len(sink.calls) != 0 {
		t.Fatalf("没有身份却打了 REST: %+v / %+v", api.calls, sink.calls)
	}
}

// ---- 纯函数 ----

func TestFoldLogs(t *testing.T) {
	payload := runLogsPayload{
		Lines: []struct {
			Seq         int    `json:"seq"`
			Stream      string `json:"stream"`
			StepOrdinal int    `json:"stepOrdinal"`
			Text        string `json:"text"`
		}{
			{Seq: 5, Stream: "stdout", StepOrdinal: 1, Text: "aaaa"},
			{Seq: 6, Stream: "stderr", StepOrdinal: 1, Text: "bbbb"},
			{Seq: 7, Stream: "stdout", StepOrdinal: 2, Text: "cccc"},
		},
		NextSeq:  8,
		Complete: true,
	}

	// 预算刚好够前两行半:第三行被丢,nextSeq 必须回退到 7(最后一行保留的是 6),
	// 否则「用同一个 sinceSeq 配更大预算重取」会把第 7 行永久吞掉。
	twoLines := len([]rune("[step1 stdout] aaaa\n")) + len([]rune("[step1 stderr] bbbb\n"))
	got := foldLogs(payload, 5, twoLines+1)
	if got.Lines != 2 || !got.Truncated || got.NextSeq != 7 {
		t.Fatalf("截断口径不对: %+v", got)
	}
	if !got.Complete {
		t.Fatalf("complete 应如实透传 REST 的终态位")
	}

	if all := foldLogs(payload, 5, 4000); all.Truncated || all.NextSeq != 8 || all.Lines != 3 {
		t.Fatalf("预算充足时不该截断: %+v", all)
	}

	// 首行就放不下:一行都不保留,nextSeq 退回请求里的 sinceSeq,让调用方换大预算重取。
	none := foldLogs(payload, 5, 3)
	if none.Lines != 0 || none.Text != "" || !none.Truncated || none.NextSeq != 5 {
		t.Fatalf("预算过小时口径不对: %+v", none)
	}

	// 空日志(运行刚创建)不该报错,也不该把 complete 编成 true。
	empty := foldLogs(runLogsPayload{}, 0, 100)
	if empty.Lines != 0 || empty.NextSeq != 0 || empty.Truncated {
		t.Fatalf("空日志处理不对: %+v", empty)
	}

	// 中文按 rune 计数:截断点不该切碎一个汉字。
	cn := runLogsPayload{Lines: []struct {
		Seq         int    `json:"seq"`
		Stream      string `json:"stream"`
		StepOrdinal int    `json:"stepOrdinal"`
		Text        string `json:"text"`
	}{{Seq: 1, Stream: "stdout", StepOrdinal: 1, Text: "构建失败:中文错误信息"}}}
	cnOut := foldLogs(cn, 1, 4000)
	if !strings.Contains(cnOut.Text, "构建失败:中文错误信息") {
		t.Fatalf("中文丢了: %q", cnOut.Text)
	}
}

func TestClampPage(t *testing.T) {
	cases := []struct{ inPage, inSize, wantPage, wantSize int }{
		{0, 0, 1, 50},
		{-3, -1, 1, 50},
		{2, 10, 2, 10},
		{1, 1000, 1, 200},
	}
	for _, tc := range cases {
		p, s := clampPage(tc.inPage, tc.inSize)
		if p != tc.wantPage || s != tc.wantSize {
			t.Fatalf("clampPage(%d,%d) = %d,%d,期望 %d,%d", tc.inPage, tc.inSize, p, s, tc.wantPage, tc.wantSize)
		}
	}
}

// TestPassthroughKeepsArrayBodies 守一个容易踩的坑:GET /api/projects 回的是裸数组,
// 若把它当对象解再编码就会丢数据 —— 透传必须是「原始字节」。
func TestPassthroughKeepsArrayBodies(t *testing.T) {
	raw := `[{"id":"p1"},{"id":"p2"}]`
	res := &apiCall{Status: 200, Body: json.RawMessage(raw)}
	got, err := json.Marshal(map[string]any{"projects": passthrough(res)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(got), `"id":"p1"`) {
		t.Fatalf("数组正文丢了: %s", got)
	}
	if empty := passthrough(&apiCall{Status: http.StatusNoContent}); empty == nil {
		t.Fatalf("空正文也该有个可编码的值")
	}
}

// TestAPIErrorCarriesCode 验错误信封解不出来时仍回可读信息(网关 HTML、500 空正文都不该变成空错误)。
func TestAPIErrorCarriesCode(t *testing.T) {
	e := apiError(http.StatusTeapot, []byte(`{"error":{"code":"nope","message":"短"}}`))
	if !strings.Contains(e.Error(), "418") || !strings.Contains(e.Error(), "nope") {
		t.Fatalf("错误码丢了: %v", e)
	}
	if e := apiError(http.StatusBadGateway, []byte("<html>boom</html>")); !strings.Contains(e.Error(), "502") {
		t.Fatalf("非信封正文没退化成状态码: %v", e)
	}
	long := strings.Repeat("这是一段很长的中文错误说明，", 60)
	e = apiError(http.StatusBadRequest, []byte(`{"error":{"code":"x","message":"`+long+`"}}`))
	if r := []rune(e.Error()); len(r) > 700 {
		t.Fatalf("长正文没截断: %d 字符", len(r))
	}
}

// inheritedKey 只是「外层请求往 context 里塞了东西」的替身:真平台上塞的是 chi 的路由状态。
type inheritedKey struct{}

// TestLoopbackStripsInheritedContextValues 守 405 那个坑的成因:内层请求继承的是 /mcp
// 那次调用的 context,而路由框架会把「这次请求已经匹配到哪条路由」写进 context。带着这份
// 状态再进同一个路由器,新请求会被当成子路由调用、沿用已匹配的路径,什么端点都命不中
// (真路由上的实测表现是 GET /api/runs/{id} 回 405,httpapi 的挂载测试守着那一条)。
// 这里用替身键把规矩钉住:值一律不继承,取消信号必须照传。
func TestLoopbackStripsInheritedContextValues(t *testing.T) {
	var (
		sawInherited bool
		cancels      <-chan struct{}
	)
	router := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawInherited = r.Context().Value(inheritedKey{}) != nil
		cancels = r.Context().Done()
		_, _ = io.WriteString(w, `{}`)
	})

	client := &http.Client{Transport: loopbackTransport{router: router}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(context.WithValue(ctx, inheritedKey{}, "路由状态"),
		http.MethodGet, "http://pipewright.internal/api/projects", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	if sawInherited {
		t.Fatalf("内层请求继承了外层 context 的值,路由状态会串")
	}
	if cancels == nil {
		t.Fatalf("取消信号没传进内层:客户端断开时内部处理不会停")
	}
}
