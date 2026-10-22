package mcpsrv

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewRemote 装配「连远端实例」形态:MCP 走 stdio 跑在 AI 客户端那一侧,
// 工具调用则用真 HTTP 打到 baseURL,统一带上配置里那把 API 令牌。
//
// 与 New 的差别只有 endpoint 的造法 —— 一个回环进本进程的路由,一个出网卡;
// 工具注册、instructions、输出策略全是同一份。这正是当初选「一套工具、两种形态」
// 要买的账:远端那端的平台照常做鉴权、判权限、落审计,本进程里没有任何权限逻辑。
//
// 这里不需要 TokenVerifier:令牌不是拿来「验」的,而是拿来「出门」的 —— 它是用户
// 填进来的配置,判它能不能做某件事的权力全在 baseURL 那端。
func NewRemote(baseURL, token string) (*Service, error) {
	base, err := normalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	plain := strings.TrimSpace(token)
	if plain == "" {
		return nil, errors.New("API 令牌不能为空(网页「设置 → API 令牌」里发放)")
	}
	// 不设 client.Timeout:单次调用的上限由 httpEndpoint 用 context 管,
	// 免得两处超时互相遮盖(连接慢时被外层先到期,报出来的错就不是真原因)。
	fixed := &httpEndpoint{base: base, token: plain, client: &http.Client{}}
	return &Service{
		// 工具按「全量」注册:范围写在远端那把令牌的库里,这里既验不了令牌也读不到它的范围。
		// 越界的调用到了对面会被逐次拦下(tools/call 报未知工具),所以这不是放行口子,
		// 只是本地 tools/list 会多显示几个用不了的工具。
		stdioServer: buildServer(nil),
		// 令牌已经在 fixed 里,不随请求变;这两个入参只有进程内形态用得到。
		endpoint: func(string, string) Endpoint { return fixed },
		stdio:    fixed,
	}, nil
}

// RunStdio 用 stdio 传输跑起来:stdin 收 JSON-RPC,stdout 回。
//
// stdout 是协议地盘,一个多余字节都会让客户端解帧失败 —— 所以这条线上的日志与报错
// 一律走 stderr(调用方 pipewright mcp 负责),这里只把 SDK 的退出原因原样交回。
// 客户端关闭 stdin 时 Run 正常返回 nil,不需要调用方特判。
//
// 通道是在这里塞进 ctx 的,不是在建服务时:SDK 把 Run 的 ctx 当作每个请求 ctx 的父
// (internal/jsonrpc2 的 NewConnection 只做「屏蔽 Done」这一步,Value 照旧往下传),
// 工具处理函数于是和 /mcp 那条线取到同一份 context 值,读出 Endpoint 的方式两边一致。
func (s *Service) RunStdio(ctx context.Context) error {
	if s.stdio == nil {
		return errors.New("stdio 形态只用于连远端平台;这个实例是进程内形态,请把 /mcp 挂到路由上")
	}
	return s.stdioServer.Run(s.withStdioEndpoint(ctx), &mcp.StdioTransport{})
}

// withStdioEndpoint 把「一份通道管整条会话」这件事收到一处:RunStdio 用它,
// 测试也用同一个它,免得测的是另一条拼装路径。
func (s *Service) withStdioEndpoint(ctx context.Context) context.Context {
	return withEndpoint(ctx, s.stdio)
}

// normalizeBaseURL 把用户填的平台地址收成能安全拼接的形状。
//
// 允许带路径(平台常挂在反代子路径下,工具拼的是 /api/...,接在后面正好对);
// 但拒绝用户名、查询串与 fragment:它们要么会把令牌漏进地址里,要么会拼出个
// 谁也认不出的 URL —— 与其等到第一次调用才炸,不如在启动时就说明白。
func normalizeBaseURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("平台地址不能为空")
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		// 「127.0.0.1:8080」这类漏了前缀的写法在这里就解析不过去,所以把该长什么样也写进报错。
		return "", fmt.Errorf("平台地址解析失败:%v(要带 http:// 或 https:// 前缀,例如 http://127.0.0.1:8080)", err)
	}
	// 漏前缀的写法有一半能被 url.Parse 蒙混过去(「不是地址」当成路径、「localhost:8080」
	// 把 localhost 当成协议),所以协议要单独按「有没有 / 是不是这两家」判,不能只看报错。
	if u.Scheme == "" {
		return "", fmt.Errorf("平台地址要带 http:// 或 https:// 前缀:%q", trimmed)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("平台地址要用 http/https,得 %q", trimmed)
	}
	if u.Host == "" {
		return "", fmt.Errorf("平台地址缺主机名:%q", trimmed)
	}
	if u.User != nil {
		return "", errors.New("平台地址里不要带用户名(令牌用 --token 传)")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("平台地址不要带查询串或锚点:%q", trimmed)
	}
	return strings.TrimSuffix(trimmed, "/"), nil
}
