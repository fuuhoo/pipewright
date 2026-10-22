// remote_test.go 守 stdio 那条线的两件事:①配置错在启动时就说明白(不要起来一个
// 什么都连不上的服务);②工具调用出网卡时,带的就是配置里那把令牌 —— 平台那端据此判权限、
// 落审计,本进程里没有任何权限逻辑。
package mcpsrv

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestNewRemoteRejectsBadConfig(t *testing.T) {
	for _, tc := range []struct {
		name    string
		baseURL string
		token   string
		wantErr string
	}{
		{"地址为空", "", "pwt_x", "平台地址不能为空"},
		{"不是 URL", "不是地址", "pwt_x", "要带 http:// 或 https:// 前缀"},
		{"漏前缀的 IP", "127.0.0.1:8080", "pwt_x", "解析失败"},
		{"协议不对", "ftp://pipewright.internal", "pwt_x", "要用 http/https"},
		{"漏前缀的主机名", "localhost:8080", "pwt_x", "要用 http/https"},
		{"缺主机名", "http://", "pwt_x", "缺主机名"},
		{"地址里带用户名", "https://admin:pwn@pipewright.internal", "pwt_x", "不要带用户名"},
		{"带查询串", "https://pipewright.internal?a=b", "pwt_x", "查询串"},
		{"带锚点", "https://pipewright.internal#x", "pwt_x", "锚点"},
		{"令牌为空", "https://pipewright.internal", "   ", "令牌不能为空"},
		{"令牌是别的方案", "https://pipewright.internal", "Bearer pwt_x", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, err := NewRemote(tc.baseURL, tc.token)
			if tc.wantErr == "" {
				// 「Bearer pwt_x」这种整串当令牌的写法不该在启动时被拒:平台那端验不过,
				// 报的是 401 invalid_api_token —— 那是运行期事实,比本地猜格式可靠。
				if err != nil {
					t.Fatalf("不该报错: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("错误里应含 %q,得 %v", tc.wantErr, err)
			}
			if svc != nil {
				t.Fatalf("配置不过关却返回了服务")
			}
		})
	}
}

// TestNewRemoteCarriesConfiguredToken 用真 HTTP 打一个假平台:验令牌原样带出、地址拼接正确
// (含尾斜杠与子路径部署),并且工具输出与进程内形态一致。
func TestNewRemoteCarriesConfiguredToken(t *testing.T) {
	type restCall struct {
		path string
		auth string
	}
	var calls []restCall
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, restCall{path: r.URL.Path, auth: r.Header.Get("Authorization")})
		if r.Header.Get("Authorization") != "Bearer pwt_configured" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"code":"invalid_api_token","message":"令牌无效"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("X-Total-Count", "1")
		_, _ = io.WriteString(w, `[{"id":"p1","name":"子路径下的项目"}]`)
	}))
	defer platform.Close()

	// 故意带尾斜杠:拼出来不能是 //api/...,那是 404 且没人看得出为什么。
	svc, err := NewRemote(platform.URL+"/", "pwt_configured")
	if err != nil {
		t.Fatalf("NewRemote: %v", err)
	}

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	// 与 RunStdio 同一处注入:那条线没有 HTTP 请求,身份不跟着请求走,是启动时定好的一份配置。
	serverSession, err := svc.stdioServer.Connect(svc.withStdioEndpoint(context.Background()), serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer serverSession.Wait()
	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-side", Version: "v0"}, nil)
	session, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer session.Close()

	var out struct {
		Projects []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"projects"`
		Total string `json:"total"`
	}
	res := call(t, session, "list_projects", map[string]any{}, &out)
	if res.IsError {
		t.Fatalf("list_projects 失败: %s", toolErrorText(t, res))
	}
	if len(calls) != 1 || calls[0].path != "/api/projects" {
		t.Fatalf("出网卡的请求不对: %+v", calls)
	}
	if calls[0].auth != "Bearer pwt_configured" {
		t.Fatalf("没带配置里那把令牌: %q", calls[0].auth)
	}
	if len(out.Projects) != 1 || out.Projects[0].ID != "p1" || out.Total != "1" {
		t.Fatalf("输出与 REST 不一致: %+v", out)
	}
}

// TestRunStdioRejectsInProcess 进程内形态的身份跟着请求走,stdio 没有请求可跟:
// 拿它跑只会得到一把空令牌,每一次工具调用都 401,而报错看起来像「令牌坏了」。
// 与其让人查半天,不如起不来。
func TestRunStdioRejectsInProcess(t *testing.T) {
	svc := New(fakeVerifier{}, &stubAPI{})
	err := svc.RunStdio(context.Background())
	if err == nil || !strings.Contains(err.Error(), "进程内") {
		t.Fatalf("进程内形态不该能起 stdio,得 %v", err)
	}
}

// TestClientAddrDoesNotTravelOnRealHTTP 钉住「来源地址」这一笔的边界:进程内形态把 MCP
// 请求的来源地址塞进内部请求,纯粹是给本地审计用的;而真出网卡这条线上,这个字段绝不能
// 变成「客户端自称从哪来」的通道 —— 远端平台只能看到真实 TCP 连接的地址。
// 用 TEST-NET-3(203.0.113.0/24,文档保留段)当一个永不存在的来源地址,它必须整笔消失在出网的方向上。
func TestClientAddrDoesNotTravelOnRealHTTP(t *testing.T) {
	var seen string
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.RemoteAddr
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer platform.Close()

	e := &httpEndpoint{base: platform.URL, token: "pwt_x", client: platform.Client(), clientAddr: "203.0.113.9:41060"}
	if _, err := e.Do(t.Context(), http.MethodGet, "/api/ping", nil, nil); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if !strings.HasPrefix(seen, "127.0.0.1:") {
		t.Fatalf("远端看到的来源地址被自称的值污染了:%q", seen)
	}
}

// TestNormalizeBaseURLKeepsSubpath 平台挂在反代子路径下是常态:base 的路径要留着,
// 工具拼的 /api/... 接在后面正好对。
func TestNormalizeBaseURLKeepsSubpath(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://pipewright.internal", "https://pipewright.internal"},
		{"https://pipewright.internal/", "https://pipewright.internal"},
		{"  http://127.0.0.1:8080/pipewright/  ", "http://127.0.0.1:8080/pipewright"},
	} {
		got, err := normalizeBaseURL(tc.in)
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("%q 收成 %q,期望 %q", tc.in, got, tc.want)
		}
	}
}
