// grant.go 是「登录顺手把 git 令牌也带回来」那一小块协议层。
//
// 为什么单独一个文件:标准 OAuth 的令牌响应本来只被读一个 access_token,
// 现在要多读三件事(expires_in / refresh_token / 实际授予的 scope),而这三件
// 都是「凭据能不能长期用」的关键 —— 拿不到时效就不知道该不该续期,拿不到
// refresh_token 就只能等用户重新授权。形态各家的坑都收在这里,别散进兑换链路。
package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrRefreshUnsupported 表示这条令牌续不了:身份源本就不是 git 平台(OIDC / 钉钉 / 飞书 /
// 企业微信给不出 git 令牌),或该应用当初压根没发 refresh_token。
// 人读错误,绝不含 token —— 续期失败绝大多数是用户在平台上撤销了授权,重试无意义,
// 调用方据此把凭据标成「请重新登录」而不是每五分钟再撞一次墙。
var ErrRefreshUnsupported = errors.New("oauth: refresh token unavailable for this identity source")

// Grant 是一次令牌兑换(RFC 6749 §4.1.3)或续期(§6)的完整产出。
// 与 Identity 分开:那个回答「你是谁」,这个回答「你带来了什么凭证」——
// 只有开了凭据同步的 git 登录应用才会产出非空 Grant。
type Grant struct {
	// AccessToken 是访问令牌明文:调用方要么立即经 vault 加密落库,要么用完即弃。绝不日志/回显。
	AccessToken string
	// RefreshToken 为空 = 该应用只发短时访问令牌(公开客户端常见),到期只能重新登录。
	RefreshToken string
	// ExpiresAt 是访问令牌的到期时刻;零值表示 provider 没告知时效,按长期令牌处理(不续期、不告警)。
	ExpiresAt time.Time
	// Scope 是 provider **实际**授予的权限串(可能与申请的不同:平台可以收窄)。
	Scope string
}

// LongLived 报告这张令牌是否没有时效信息(自建 Gitea 常见:回 access_token 但不回 expires_in)。
func (g Grant) LongLived() bool { return g.ExpiresAt.IsZero() }

// expiringWithin 判断访问令牌是否已在 grace 窗口内(长期令牌恒 false,永不进续期队列)。
func (g Grant) expiringWithin(grace time.Duration, now time.Time) bool {
	if g.ExpiresAt.IsZero() {
		return false
	}
	return g.ExpiresAt.Sub(now) <= grace
}

// parseTokenResponse 解析令牌端点的 JSON 响应。
// 失败一律收敛成人读的 ErrExchangeFailed(绝不把 provider 原文往外抛:里面可能回显 token)。
func parseTokenResponse(body []byte) (Grant, error) {
	var parsed struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Scope        string `json:"scope"`
		// ExpiresIn 收 any:绝大多数平台回 JSON 数字,但个别实例(以及表单编码兼容路径)回字符串,
		// 写死 int64 会让那整张令牌被判成「没有时效」,于是永远不续期、到期当天突然全线 401。
		ExpiresIn any    `json:"expires_in"`
		Error     string `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Grant{}, ErrExchangeFailed
	}
	if strings.TrimSpace(parsed.AccessToken) == "" {
		return Grant{}, ErrExchangeFailed
	}
	g := Grant{
		AccessToken:  parsed.AccessToken,
		RefreshToken: strings.TrimSpace(parsed.RefreshToken),
		Scope:        strings.TrimSpace(parsed.Scope),
	}
	if secs := secondsField(parsed.ExpiresIn); secs > 0 {
		g.ExpiresAt = time.Now().UTC().Add(time.Duration(secs) * time.Second)
	}
	return g, nil
}

// secondsField 把 expires_in 归一成秒数;读不出(缺字段、非数字、负数)一律 0 = 按时效未知处理。
func secondsField(v any) int64 {
	switch n := v.(type) {
	case float64:
		if n > 0 {
			return int64(n)
		}
	case string:
		if i, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64); err == nil && i > 0 {
			return i
		}
	}
	return 0
}

// requestRefresh 用 refresh_token 换一张新令牌(RFC 6749 §6)。
// 与 requestToken 只差 grant_type 与那两个参数,其余(POST 形态、Accept 头、错误收敛)同一条流。
func requestRefresh(ctx context.Context, client *http.Client, p Provider, clientID, clientSecret, refreshToken string) (Grant, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Grant{}, ErrExchangeFailed
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return Grant{}, ErrExchangeFailed
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Grant{}, ErrExchangeFailed
	}
	g, err := parseTokenResponse(body)
	if err != nil {
		return Grant{}, err
	}
	// 很多平台续期时**不换发** refresh_token(RFC 6749 允许省略)。此时沿用旧的那张:
	// 留空会把库里的续期凭证弄丢,这个人下次就再也刷不动了,只能重新登录。
	if g.RefreshToken == "" {
		g.RefreshToken = refreshToken
	}
	if g.Scope == "" {
		g.Scope = p.scope()
	}
	return g, nil
}
