package oauth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"sync"
	"time"
)

// stateTTL 是 OAuth state 的有效期。授权来回(用户在 git 平台点同意)通常秒级~分钟级,
// 10 分钟足够且不让陈旧 state 长期可用(限制 CSRF 重放窗口)。
const stateTTL = 10 * time.Minute

// 授权意图:同一条 state 存储服务三条流,回调据此决定「登进谁」或「把 token 存给谁」。
const (
	// IntentVault 已登录用户连接 git 凭据(access_token → vault)。
	IntentVault = "vault"
	// IntentLogin 匿名访客用 git 账号登录本平台(不碰 vault,不落 token)。
	IntentLogin = "login"
	// IntentBind 已登录用户把 git 账号绑到自己的平台用户(只写映射,不建号)。
	IntentBind = "bind"
)

// StateIssue 是发起一次授权跳转时记录的上下文。
type StateIssue struct {
	// Intent 是 IntentVault|IntentLogin|IntentBind 之一。
	Intent string
	// Provider 是本次流绑定的 provider;回调须带回同一个(防跨 provider 换包)。
	Provider string
	// BindKey 把 state 绑到发起方:已登录的流用会话 token,匿名登录用浏览器 nonce
	// (双提交 cookie)。只有持有同一个 bindKey 的请求能消费这条 state。
	BindKey string
	// Redirect 是回调成功后的站内落地路径(仅登录/绑定流用;空 = 默认页)。
	Redirect string
}

// StateData 是回调消费 state 后取回的发起上下文。
type StateData struct {
	Intent   string
	Provider string
	Redirect string
}

// stateEntry 是一条待校验的 state 记录:发起上下文 + 过期时刻。
type stateEntry struct {
	StateIssue
	expiresAt time.Time
}

// stateStore 是 OAuth state 的短期内存存储(CSRF 防护)。
//
// 发起 authorize 时生成高熵随机 state、绑定发起方(bindKey)与意图存入;callback 校验
// state 存在、未过期、且 bindKey 与当前请求一致 —— 三者皆满足才放行 Exchange。这样攻击者
// 即便诱导受害者点开伪造的 callback,其授权码也因 state 不匹配(或不绑该会话)被拒,
// 防止 CSRF 注入伪造授权码把攻击者账号写成受害者会话(Jpom 的 state 只放内存不绑发起方,
// 这条是本平台必须补齐的差)。
//
// 单进程内存即可(平台为单二进制);消费即删(一次性);惰性清理过期项。
type stateStore struct {
	mu      sync.Mutex
	entries map[string]stateEntry
	now     func() time.Time
}

// newStateStore 构造内存 state 存储。
func newStateStore() *stateStore {
	return &stateStore{
		entries: make(map[string]stateEntry),
		now:     time.Now,
	}
}

// issue 生成并登记一个绑定发起上下文的随机 state,返回供放进 authorize URL。
func (s *stateStore) issue(in StateIssue) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	state := base64.RawURLEncoding.EncodeToString(buf)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked()
	s.entries[state] = stateEntry{StateIssue: in, expiresAt: s.now().Add(stateTTL)}
	return state, nil
}

// consume 校验并一次性消费 state:存在 + 未过期 + bindKey 匹配 → 回吐发起上下文;否则 false。
// 无论校验结果如何都删除该条(一次性,防重放)。bindKey 用常量时间比较(防时序侧信道)。
func (s *stateStore) consume(state, bindKey string) (StateData, bool) {
	if state == "" {
		return StateData{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[state]
	if !ok {
		return StateData{}, false
	}
	delete(s.entries, state)
	if s.now().After(e.expiresAt) {
		return StateData{}, false
	}
	if subtle.ConstantTimeCompare([]byte(e.BindKey), []byte(bindKey)) != 1 {
		return StateData{}, false
	}
	return StateData{Intent: e.Intent, Provider: e.Provider, Redirect: e.Redirect}, true
}

// gcLocked 惰性清理过期项(调用方须持锁)。
func (s *stateStore) gcLocked() {
	now := s.now()
	for k, e := range s.entries {
		if now.After(e.expiresAt) {
			delete(s.entries, k)
		}
	}
}
