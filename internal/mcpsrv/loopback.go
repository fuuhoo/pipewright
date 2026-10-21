package mcpsrv

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"time"
)

// loopbackTransport 让工具直接调用本进程的路由器:字节不出网卡,但要完整过一遍
// 路由、Bearer 鉴权、角色/分组权限与审计 —— 这正是选它的原因:MCP 层不必重写权限判定,
// 「这台令牌能做什么」在两条入口上是同一个答案。
//
// 只服务进程内形态(/mcp);stdio 子命令连的是远端实例,走真 http.Transport。
type loopbackTransport struct {
	router http.Handler
}

func (t loopbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// 回环请求带的 context 是从 `/mcp` 那次 MCP 调用继承下来的,里面已经有 chi 路由器
	// 自己写的路由状态(chi.Context:已匹配的 /mcp 路由与「方法不匹配」判定)。chi 的
	// Mux.ServeHTTP 见到带着 RouteContext 的请求,认定「上层 Mux 已经把我路由到了子路由」,
	// 直接从那份状态接着往下匹配 —— 于是 GET /api/runs/{id} 从 /mcp 的路由栈出发,匹配不上,
	// 回的是 chi 的纯文本 405(实测)。真 HTTP 那条线不会遇到:路由状态活在连接里,不跨请求。
	// 所以只在这个边界上剥掉继承来的值,让内层请求和从网卡进来的新请求一样干净。
	// 取消与超时照常传递,工具那边一断,内层处理同样终止。
	clone := req.Clone(contextWithoutValues(req.Context()))
	// 合成请求没有真 socket 托底:GET/DELETE 在 client 侧 Body 是 nil,而路由那端一律按
	// 「服务器收到的请求」待客(io.ReadAll(r.Body)、json.NewDecoder(r.Body)),nil 直接 panic。
	// 补一个空 body,让回环请求和真 HTTP 进来的形状一致。
	if clone.Body == nil {
		clone.Body = http.NoBody
	}
	req = clone

	rec := &bufferWriter{header: http.Header{}, status: http.StatusOK}
	t.router.ServeHTTP(rec, req)
	return &http.Response{
		StatusCode:    rec.status,
		Header:        rec.header,
		Body:          io.NopCloser(bytes.NewReader(rec.body.Bytes())),
		ContentLength: int64(rec.body.Len()),
		Request:       req,
		Proto:         "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
	}, nil
}

// valuesStrippedContext 只向上报父级的取消、错误与超时,任何 context 值都查不到,
// 等同于「一个从网络上新到的请求」的 context。Go 标准库没有剥值的现成 API,只能自己实现这个接口。
type valuesStrippedContext struct{ parent context.Context }

// contextWithoutValues 把 ctx 降级成「只传递取消,不传递值」。ctx.Done() 为 nil(不可取消)时
// 原样返回,免得把 min(int,int) 的 0 当成一个真实的截止时间。
func contextWithoutValues(ctx context.Context) context.Context {
	if ctx.Done() == nil {
		return ctx
	}
	return valuesStrippedContext{parent: ctx}
}

func (c valuesStrippedContext) Deadline() (time.Time, bool) { return c.parent.Deadline() }
func (c valuesStrippedContext) Done() <-chan struct{}       { return c.parent.Done() }
func (c valuesStrippedContext) Err() error                  { return c.parent.Err() }
func (c valuesStrippedContext) Value(any) any               { return nil }

// bufferWriter 是最小的 ResponseWriter:工具用到的端点一律回 JSON,
// 不涉及 SSE/流式,所以无需实现 Flush、Hijack。
type bufferWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
	// started 记录状态码是否已定:net/http 的规矩是「写过头之后再 WriteHeader 无效」,
	// 少这一步会把 handler 的隐式 200 覆盖掉。
	started bool
}

func (b *bufferWriter) Header() http.Header { return b.header }

func (b *bufferWriter) Write(p []byte) (int, error) {
	b.started = true
	return b.body.Write(p)
}

func (b *bufferWriter) WriteHeader(status int) {
	if b.started {
		return
	}
	b.started = true
	b.status = status
}
