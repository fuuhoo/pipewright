// Package mcpsrv 把平台已有的 REST 能力包成 MCP 工具面,让 AI 客户端能建项目、
// 配流水线、跑构建、看日志。
//
// 设计取向(为什么是「转发」而不是「另写一套 service 调用」):
//   - 工具只依赖 Endpoint 这一个抽象 —— 一次「带着调用方令牌打平台 REST」的调用。
//     于是角色功能档位、分组数据范围、审计 actor、错误码全部沿用浏览器那条线,
//     本包不重写任何权限判定,也不会出现「网页上做不了、AI 却做得了」的口子。
//     形态上先只做 /mcp(Streamable HTTP,挂在平台自己的路由上,内部调用走进程内回环)。
//     工具与 Endpoint 解耦,所以 stdio 形态只是换一种 Endpoint 造法,不必改工具代码。
//   - 鉴权在 /mcp 边界一次做完:Bearer 令牌交 apitoken 校验,通过后把「这把令牌的
//     REST 通道」注入请求 context,工具处理函数从 context 取。取不到就是装配错误,
//     宁可报错也不放行。
//
// 输出策略:除 get_run_logs 外一律把 REST 响应体原样透传(DTO 只在 httpapi 一处定义,
// 契约漂移的代价由那边已有的测试承担);日志需要压成文本并按字符预算截断。
//
// 本包无 init() 副作用、无包级重对象。
package mcpsrv

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/fuuhoo/pipewright/internal/apitoken"
	"github.com/fuuhoo/pipewright/internal/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ServerName 是 MCP 客户端里显示的服务名。
const ServerName = "pipewright"

// TokenVerifier 校验 API 令牌明文并取回归属人,由 *apitoken.Service 实现。
type TokenVerifier interface {
	Verify(ctx context.Context, plaintext string) (*apitoken.Principal, error)
}

// Service 是一装配好的 MCP 服务:一份工具注册 + 一个可挂载的 HTTP handler。
type Service struct {
	server   *mcp.Server
	tokens   TokenVerifier
	endpoint func(token, clientAddr string) Endpoint
	// stdio 是 stdio 形态专用的出门通道:那份令牌来自启动参数,不属于任何一个请求,
	// 所以只能在建服务时定下来。进程内形态这里是 nil —— 它的身份跟着请求走,
	// 没有「一份配置管全程」可言,拿 stdio 跑它等于把调用方令牌焊死成空串。
	stdio Endpoint
}

// instructions 写给 AI:先讲清「典型链路」,再点出两个最容易踩的坑(空流水线假绿灯、
// 触发不等于跑完)。这些约束在工具描述里也各写了一遍,因为客户端未必展示 server 级说明。
const instructions = `PipeRight 是一个 CI/CD 平台(流水线 / 构建 / 发布)。推荐按这条链路用工具:

1. list_projects 找目标项目;没有就 create_project(不绑仓库也能建,适合只做发布)。
2. list_build_envs 拿可用构建环境的 id,然后用 set_pipeline 写整条流水线。
3. trigger_run 只负责发车,立刻返回 runId;构建在后台跑,要用 get_run 轮询到终态。
4. 失败时 get_run_logs 定位到具体步骤(按 nextSeq 续读),必要时 cancel_run 停掉运行。

两个必须知道的坑:
- 项目没配过流水线就直接 trigger_run,会按默认空流水线跑完并判「成功」——那是假绿灯,先 set_pipeline。
- 报错里带平台错误码(如 invalid_yaml / build_env_unknown / run_not_cancelable),按码修正参数再重试,别原样重发。

写操作都会落审计,记录的操作人是这把令牌的归属人。`

// New 装配「进程内形态」的 MCP 服务:工具调用直接进本平台自己的路由。
//
// router 传的就是 httpapi.New 返回的那个路由;回环调用只在请求发生时发生,不存在自引用。
// 参数类型是 http.Handler 而不是 *chi.Mux —— 这层薄接口不是装饰:chi 会把路由状态塞进
// 请求 context,把带这份状态的请求再喂回同一个 Mux 会路由错乱,RoundTrip 必须先剥掉它
// (标准库自己把请求转给带 RouteCtxKey 的 handler 时也是这么处理的,见 loopback.go)。
//
// tokens 为 nil 表示这套实例没装配 API 令牌,任何 MCP 请求都只能 401。
func New(tokens TokenVerifier, router http.Handler) *Service {
	// 回环基址只是占位:chi 按 path 路由,不会看 Host。真实请求一个字节都不出进程。
	const loopbackBase = "http://pipewright.internal"
	client := &http.Client{Transport: loopbackTransport{router: router}}
	endpoint := func(token, clientAddr string) Endpoint {
		return &httpEndpoint{base: loopbackBase, token: token, client: client, clientAddr: clientAddr}
	}
	return &Service{server: buildServer(), tokens: tokens, endpoint: endpoint}
}

// buildServer 造一份「工具已注册好」的 SDK server:两种形态(进程内 / 连远端)共用同一份
// 工具注册与 instructions,差别只在往下的 endpoint 怎么造。
func buildServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: ServerName, Version: version.Version},
		&mcp.ServerOptions{Instructions: instructions})
	registerTools(server)
	return server
}

// Handler 返回挂在 /mcp 上的 http.Handler。
//
// 每一条 MCP 请求都要先过 Bearer 校验(与 requireAuth 同口径:没有令牌、令牌无效、
// 令牌未启用都是 401,且不回落到会话 cookie —— 回落就等于允许一个能诱导管理员发请求的
// 页面借她的会话冒充令牌持有者)。校验通过才把「这把令牌专属的 REST 通道」写进 context,
// SDK 会把请求 context 传给工具处理函数,于是每次工具调用天然以调用方身份执行。
//
// 只有进程内形态该挂它:NewRemote 造出来的 Service 没有 TokenVerifier(令牌是配置进来的,
// 不是在这里验的),挂上去只会得到一个对任何请求都回 401 的端点。
func (s *Service) Handler() http.Handler {
	inner := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return s.server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plain, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok {
			denyMCP(w, "unauthorized", "需要 API 令牌:请在 Authorization 头带 Bearer <pwt_…>")
			return
		}
		if s.tokens == nil {
			denyMCP(w, "unauthorized", "API 令牌未启用")
			return
		}
		if _, err := s.tokens.Verify(r.Context(), plain); err != nil {
			// 与 requireAuth 一样:无效令牌换不到任何关于原因的信息(不存在/已撤销/归属人被
			// 禁用都被抹平成同一个错误),这里也不区分。
			denyMCP(w, "invalid_api_token", "API 令牌无效或已撤销")
			return
		}
		ctx := withEndpoint(r.Context(), s.endpoint(plain, r.RemoteAddr))
		inner.ServeHTTP(w, r.WithContext(ctx))
	})
}

// denyMCP 回 401。带 WWW-Authenticate 是因为部分 MCP 客户端把它当作「该走认证流程」的信号;
// 正文用平台的错误信封形状({"error":{"code","message"}}),让直接 curl 的人也看得懂。
func denyMCP(w http.ResponseWriter, code, message string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="pipewright", error="`+code+`"`)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}

// bearerToken 取 Authorization 头里的 Bearer 凭据。第二个返回值为 false 表示没带令牌。
func bearerToken(header string) (string, bool) {
	const scheme = "Bearer "
	if !strings.HasPrefix(header, scheme) {
		return "", false
	}
	plain := strings.TrimSpace(header[len(scheme):])
	if plain == "" {
		return "", false
	}
	return plain, true
}
