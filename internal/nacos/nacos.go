// Package nacos 是 Nacos 配置中心的只读客户端:构建时按 Data ID 取一份配置正文当环境变量。
//
// 只走 v1 OpenAPI(GET /nacos/v1/cs/configs)。2.x 官方承诺兼容 1.x 的 HTTP 接口,而 v1 直接返回
// 配置原文,不需要再解一层 {"code":0,"data":…} 信封 —— 少一层解析就少一处会错的地方。
//
// BaseURL 要填到「服务根 + 上下文路径」(如 http://127.0.0.1:8848/nacos),因为 Nacos 默认
// contextPath 就是 /nacos,而挂在网关/反向代理后面时前缀又是另一套。这里不猜前缀:地址错了会返回
// 404,而 404 与「配置不存在」看着一样,所以那条错误把两种可能都点名。
//
// 鉴权是可选的(nacos.core.auth.enabled 默认关):给了 Auth 就先 POST /v1/auth/login 换 accessToken,
// 只在这一条请求的查询串里带回去。token 不进日志、不进 step.Env、不落库;传输层错误也只报主机不报
// URL —— Go 的 http 错误串里带完整 URL,而那次请求的查询串里有 accessToken。
//
// 错误一律是人读中文(调用方直接拼进构建日志),分类靠 errors.Is 认下面几个哨兵,不靠字符串。
// 失败不缓存、也不监听推送:构建要的是「这次拿到的是哪份配置」,静默复用上一次的值会让人查不完。
package nacos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fuuhoo/pipewright/internal/netguard"
)

// 错误分类:执行侧用 errors.Is 判类,错误文本本身就是给人看的那句(不含下面这些标识)。
var (
	// ErrConfigNotFound 表示服务端没有这份配置(或地址少了上下文路径)。
	ErrConfigNotFound = errors.New("nacos: config not found")
	// ErrUnauthorized 表示鉴权没过去:凭据不对、没给凭据、或账号没有该命名空间的读权限。
	ErrUnauthorized = errors.New("nacos: unauthorized")
	// ErrTransport 表示连不上 / 超时 / 服务端 5xx:与配置内容无关,重试过仍失败才返回。
	ErrTransport = errors.New("nacos: transport failure")
	// ErrBadTarget 表示地址本身不能用:形状非法,或被出网收口(internal/netguard)拒了。
	ErrBadTarget = errors.New("nacos: invalid or blocked target")
	// ErrTooLarge 表示正文超出额度(拿它当 env 文件用,超大就是拿错了配置)。
	ErrTooLarge = errors.New("nacos: config content too large")
)

// Policy 是一次客户端的时长与额度;零值字段取默认。
type Policy struct {
	DialTimeout     time.Duration // 建连上限,默认 3s
	RequestTimeout  time.Duration // 单个请求(含读体)上限,默认 8s
	Attempts        int           // 一轮「登录 + 读取」的尝试次数,默认 2(即失败重试 1 次)
	MaxContentBytes int64         // 配置正文上限,默认 64 KiB(与 pipeline.EnvMaxBytes 同额度)
}

const (
	defaultDialTimeout     = 3 * time.Second
	defaultRequestTimeout  = 8 * time.Second
	defaultAttempts        = 2
	defaultMaxContentBytes = int64(64 << 10)
	// maxAuthRespBytes 限登录响应:那是一段小 JSON,给 8 KiB 已经宽得离谱。
	maxAuthRespBytes = 8 << 10
)

// Auth 是 Nacos 的用户名/密码(开了鉴权才需要)。
type Auth struct {
	Username string
	Password string
}

// Empty 报告这份凭据其实什么都没能带上(选了条空的、或保险库解出空值)。
func (a Auth) Empty() bool { return a.Username == "" && a.Password == "" }

// Request 是一次配置读取坐标。Group 必填:默认值归契约层(internal/pipeline)管,
// 两处各写一个 DEFAULT_GROUP 就会出现「表单存了空、执行按另一个默认读到了别的配置」。
type Request struct {
	BaseURL     string
	NamespaceID string // 空 = public 命名空间(v1 里叫 tenant)
	DataID      string
	Group       string
	Auth        *Auth // nil = 匿名读取
}

// Client 是带策略的只读客户端。用 New 构造;每次构建现造一个也不要紧(只是个 http.Client)。
type Client struct {
	httpc *http.Client
	pol   Policy
}

// New 按策略造客户端。零值字段走默认;Attempts 最小 1。
func New(p Policy) *Client {
	pol := p
	if pol.DialTimeout <= 0 {
		pol.DialTimeout = defaultDialTimeout
	}
	if pol.RequestTimeout <= 0 {
		pol.RequestTimeout = defaultRequestTimeout
	}
	if pol.Attempts <= 0 {
		pol.Attempts = defaultAttempts
	}
	if pol.MaxContentBytes <= 0 {
		pol.MaxContentBytes = defaultMaxContentBytes
	}
	dialer := &net.Dialer{Timeout: pol.DialTimeout}
	return &Client{
		httpc: &http.Client{
			Timeout: pol.RequestTimeout,
			Transport: &http.Transport{
				DialContext:         dialer.DialContext,
				TLSHandshakeTimeout: pol.DialTimeout,
				// 一个客户端只服务一次读取,不攒连接池。
				DisableKeepAlives: true,
			},
			// 跟随重定向时每一跳重新过一遍收口:否则一次 302 就把 169.254.169.254 让回来了。
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return errors.New("重定向次数过多")
				}
				return netguard.Check(req.URL.String(), true)
			},
		},
		pol: pol,
	}
}

// Fetch 读一份配置正文。ctx 由调用方给(整次构建的取消要能传下来),每轮尝试再套一层请求超时。
func (c *Client) Fetch(ctx context.Context, r Request) (string, error) {
	base, host, err := normalizeBase(r.BaseURL)
	if err != nil {
		return "", err
	}
	if err := netguard.Check(base, true); err != nil {
		return "", fail(ErrBadTarget, "这个地址被出网收口拒了:%v", err)
	}
	if r.DataID == "" {
		return "", fail(ErrBadTarget, "没有指定 Data ID")
	}
	if r.Group == "" {
		return "", fail(ErrBadTarget, "没有指定分组(默认分组应由调用方归一好后传进来)")
	}

	var last error
	for attempt := 1; attempt <= c.pol.Attempts; attempt++ {
		text, err := c.fetchOnce(ctx, base, host, r)
		if err == nil {
			return text, nil
		}
		last = err
		// 只重试「重来一次可能不一样」的:鉴权、404、形状问题重来还是那样;ctx 都取消了也不重。
		if !errors.Is(err, ErrTransport) || ctx.Err() != nil {
			return "", err
		}
	}
	return "", last
}

// fetchOnce 跑一轮「(可选)登录 → 读取」。
func (c *Client) fetchOnce(ctx context.Context, base, host string, r Request) (string, error) {
	token := ""
	if r.Auth != nil {
		if r.Auth.Empty() {
			return "", fail(ErrUnauthorized, "选中的凭据里用户名和密码都是空的(这条凭据可能已被删除,或保险库还没配 master key)")
		}
		t, err := c.login(ctx, base, host, *r.Auth)
		if err != nil {
			return "", err
		}
		token = t
	}

	q := url.Values{"dataId": {r.DataID}, "group": {r.Group}}
	if ns := r.NamespaceID; ns != "" {
		q.Set("tenant", ns)
	}
	if token != "" {
		q.Set("accessToken", token)
	}
	target := base + "/v1/cs/configs?" + q.Encode()

	reqCtx, cancel := context.WithTimeout(ctx, c.pol.RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, target, nil)
	if err != nil {
		return "", fail(ErrBadTarget, "读取请求构造不了(地址形状非法)")
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return "", c.transportError("读取配置", host, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxAuthRespBytes))
		_ = resp.Body.Close()
	}()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		// 坐标由调用方补进话术(它才知道该显示成什么样的 Data ID / 分组 / 命名空间)。
		return "", fail(ErrConfigNotFound, "服务端返回 404:配置不存在,或地址少了 /nacos 这类上下文路径,两种都核对一下")
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized:
		if r.Auth == nil {
			return "", fail(ErrUnauthorized, "服务端要求鉴权(HTTP %d),这个节点还没选 Nacos 凭据", resp.StatusCode)
		}
		return "", fail(ErrUnauthorized, "服务端拒绝读取(HTTP %d):用户名/密码不对,或这个账号没有该命名空间的读权限", resp.StatusCode)
	case resp.StatusCode >= 500:
		return "", fail(ErrTransport, "服务端返回 HTTP %d", resp.StatusCode)
	case resp.StatusCode/100 != 2:
		return "", fail(ErrBadTarget, "读取配置返回 HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, c.pol.MaxContentBytes+1))
	if err != nil {
		return "", c.transportError("读取配置正文", host, err)
	}
	if int64(len(data)) > c.pol.MaxContentBytes {
		return "", fail(ErrTooLarge, "正文超过 %d KiB 上限:拿它当环境变量文件用的配置不该这么大,确认 Data ID 指的是那份 properties", c.pol.MaxContentBytes>>10)
	}
	return string(data), nil
}

// login 换取 accessToken。失败只报主机与状态码:URL 与响应体都可能带出敏感内容。
func (c *Client) login(ctx context.Context, base, host string, a Auth) (string, error) {
	form := url.Values{"username": {a.Username}, "password": {a.Password}}
	reqCtx, cancel := context.WithTimeout(ctx, c.pol.RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, base+"/v1/auth/login", strings.NewReader(form.Encode()))
	if err != nil {
		return "", fail(ErrBadTarget, "登录请求构造不了(地址形状非法)")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpc.Do(req)
	if err != nil {
		return "", c.transportError("登录", host, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode/100 != 2 {
		return "", fail(ErrUnauthorized, "登录失败(HTTP %d):用户名/密码不对,或者这个 Nacos 压根没开鉴权(那就不用选凭据)", resp.StatusCode)
	}
	var out struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAuthRespBytes)).Decode(&out); err != nil {
		return "", fail(ErrUnauthorized, "登录响应不是预期的 JSON,读不到 accessToken")
	}
	if out.AccessToken == "" {
		return "", fail(ErrUnauthorized, "登录返回了空的 accessToken")
	}
	return out.AccessToken, nil
}

// normalizeBase 去掉尾部斜杠与查询串,返回根地址与「主机:端口」(错误话术里只用后者,
// 绝不带上查询串 —— 那一串里可能有 accessToken)。
func normalizeBase(raw string) (base, host string, err error) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "", "", fail(ErrBadTarget, "Nacos 地址是空的")
	}
	u, perr := url.Parse(p)
	if perr != nil || u.Host == "" {
		return "", "", fail(ErrBadTarget, "Nacos 地址「%s」解析不出主机名", p)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), u.Host, nil
}

// transportError 把传输层错误收成人读一句话。
//
// 刻意不 %v 原始错误:Go 的 http 错误串里带完整 URL,而那次的查询串里有 accessToken。
// 超时/连不上分得开就够定位了:超时 = 服务活着但不答,连不上 = 地址或端口不对/服务没起。
// 重定向被出网收口拒掉是唯一的例外:那不是「连不上」,得照实说是被拒了,否则人会去查网络。
func (c *Client) transportError(action, host string, err error) error {
	if errors.Is(err, netguard.ErrBlocked) {
		return fail(ErrBadTarget, "%s %s 时被出网收口拒绝:%v", action, host, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fail(ErrTransport, "%s %s 超时(建连 %s、整个请求 %s)", action, host, c.pol.DialTimeout, c.pol.RequestTimeout)
	}
	if errors.Is(err, context.Canceled) {
		return fail(ErrTransport, "%s %s 被取消", action, host)
	}
	return fail(ErrTransport, "连不上 %s(%s):地址或端口不对,或者服务没起", host, action)
}

// classified 把「给人看的那句」与「给代码判的那类」分开:Error() 只有中文,
// 分类经 Unwrap 仍能被 errors.Is 认出来。
type classified struct {
	class error
	msg   string
}

func (e *classified) Error() string { return e.msg }
func (e *classified) Unwrap() error { return e.class }

func fail(class error, format string, args ...any) error {
	return &classified{class: class, msg: fmt.Sprintf(format, args...)}
}
