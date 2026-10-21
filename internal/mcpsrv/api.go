package mcpsrv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 单次内部 REST 调用的兜底超时。取 60s:触发运行、保存流水线都是「立即返回」型端点,
// 真正耗时的构建由后台 worker 接手,不需要把 MCP 调用挂住更久。
const apiCallTimeout = 60 * time.Second

// maxResponseBytes 限制单次响应读入量。日志端点一次可回很长,超出即截断(工具会说明),
// 不能让一个工具调用把内存吃掉。
const maxResponseBytes = 8 << 20

// apiCall 是一次内部 REST 调用的原始结果。
//
// Body 保持服务端下发的字节原样:DTO 形状只在 httpapi 一处定义,工具面不复制结构,
// 契约漂移的风险就留在浏览器那条线上(那边有测试)。
type apiCall struct {
	Status int
	Header http.Header
	Body   json.RawMessage
}

// Endpoint 是「带着调用方令牌,打平台自己的 REST 接口」这条通道。
//
// 工具面只依赖这一个抽象:HTTP 形态(/mcp)用进程内回环,stdio 形态用真 HTTP,
// 同一份工具代码两边通用。权限判定、错误码、审计 actor 全部由被调用的 REST 端点
// 负责 —— 本包不重写任何鉴权逻辑。
type Endpoint interface {
	Do(ctx context.Context, method, path string, query url.Values, body any) (*apiCall, error)
}

// httpEndpoint 用 net/http 实现 Endpoint:基址 + 令牌 + 客户端,三者都由外层决定。
type httpEndpoint struct {
	base   string
	token  string
	client *http.Client
	// clientAddr 是最初那次 MCP 请求的来源地址。回环请求是自己造的,没有真 socket,
	// 不补这一笔,审计里 MCP 操作的 IP 就是空的(而 IP 正是「从哪台机器发起」的唯一线索);
	// 填上它,审计行记到的来源就是真正按下按钮的客户端,与浏览器那条线口径一致。
	clientAddr string
}

func (e *httpEndpoint) Do(ctx context.Context, method, path string, query url.Values, body any) (*apiCall, error) {
	ctx, cancel := context.WithTimeout(ctx, apiCallTimeout)
	defer cancel()

	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("请求体编码失败:%v", err)
		}
		payload = bytes.NewReader(encoded)
	}

	target := e.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, payload)
	if err != nil {
		return nil, fmt.Errorf("请求构造失败:%v", err)
	}
	// 只在客户端这边填来源地址,给后面的审计用;真发到远端的请求会忽略这个字段。
	req.RemoteAddr = e.clientAddr
	req.Header.Set("Authorization", "Bearer "+e.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("调用平台接口失败:%v", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("响应读取失败:%v", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, apiError(resp.StatusCode, raw)
	}
	return &apiCall{Status: resp.StatusCode, Header: resp.Header, Body: json.RawMessage(raw)}, nil
}

// apiError 把 REST 的错误信封({"error":{"code","message"}})翻成工具报错。
//
// code 与 message 都要保住:AI 靠 code 判断该怎么改(如 invalid_yaml 就该去修 YAML),
// 靠 message 知道改哪里。信封解不出来时退回状态码 + 截断后的正文,绝不回空错误。
func apiError(status int, raw []byte) error {
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Error.Code != "" {
		msg := envelope.Error.Message
		if msg == "" {
			msg = http.StatusText(status)
		}
		return fmt.Errorf("平台返回 %d(%s):%s", status, envelope.Error.Code, clipRunes(msg, 500))
	}
	body := strings.TrimSpace(string(raw))
	if body == "" {
		body = http.StatusText(status)
	}
	return fmt.Errorf("平台返回 %d:%s", status, clipRunes(body, 500))
}

// clipRunes 按字符数截断(不按字节):日志与错误正文都是中文,按字节切会切碎 UTF-8。
func clipRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…(已截断)"
}

// passthrough 把响应体作为工具输出原样交回;空体(204)归一成 {} 而不是 null,
// 免得 MCP 客户端把「没有内容」读成「调用失败」。
func passthrough(res *apiCall) any {
	if len(bytes.TrimSpace(res.Body)) == 0 {
		return map[string]any{}
	}
	return res.Body
}
