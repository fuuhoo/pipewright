package gitauth

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	gogitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/huangchengsir/pipewright/internal/giturl"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/go-git/go-git/v5/plumbing/transport"
)

// 地址/凭据层面的干净错误(不含 token/私钥内容),由调用方映射为领域错误。
var (
	// ErrInvalidURL 表示地址无法解析或协议不被支持(file://、git://、ftp:// 等)。
	ErrInvalidURL = errors.New("gitauth: 不支持的仓库地址")
	// ErrBlockedHost 表示 SSRF 收口拒绝的主机(回环/链路本地/未指定)。
	ErrBlockedHost = errors.New("gitauth: 主机被 SSRF 策略拒绝")
	// ErrInvalidPrivateKey 表示 SSH 凭据里存的不是可解析的私钥 PEM。
	ErrInvalidPrivateKey = errors.New("gitauth: 私钥解析失败")
	// ErrEncryptedPrivateKey 表示私钥带口令(passphrase)——非交互克隆无法解锁。
	ErrEncryptedPrivateKey = errors.New("gitauth: 不支持带口令的私钥")
)

// host key 校验策略:默认跳过(内网自托管 Git 开箱可用);
// 设置 PIPEWRIGHT_GIT_SSH_KNOWN_HOSTS 指向 known_hosts 文件后转为严格校验。
// 生产环境若仓库暴露到不可信网络,应显式配置该变量。
var (
	hostKeyMu       sync.RWMutex
	hostKeyCallback ssh.HostKeyCallback = ssh.InsecureIgnoreHostKey()
)

// SetSSHHostKeyFile 设置 SSH 主机密钥校验策略。path 为空表示跳过校验(默认)。
// 由进程启动时调用一次。
func SetSSHHostKeyFile(path string) error {
	path = strings.TrimSpace(path)
	hostKeyMu.Lock()
	defer hostKeyMu.Unlock()
	if path == "" {
		hostKeyCallback = ssh.InsecureIgnoreHostKey()
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("gitauth: known_hosts 不可读: %w", err)
	}
	cb, err := knownhosts.New(path)
	if err != nil {
		return fmt.Errorf("gitauth: known_hosts 解析失败: %w", err)
	}
	hostKeyCallback = cb
	return nil
}

func currentHostKeyCallback() ssh.HostKeyCallback {
	hostKeyMu.RLock()
	defer hostKeyMu.RUnlock()
	return hostKeyCallback
}

// ResolveRepoURL 归一化仓库地址并做 SSRF 收口(全平台单一真源)。
// scp 语法(git@host:path)归一化为 ssh://user@host/path——go-git 的 ssh 传输只认带 scheme 的 URL。
// 返回值里的 Repo.URL 才是交给 go-git 的地址。
func ResolveRepoURL(repoURL string) (giturl.Repo, error) {
	r, ok := giturl.Parse(repoURL)
	if !ok {
		return r, ErrInvalidURL
	}
	if giturl.BlockedHost(r.Host) {
		return r, ErrBlockedHost
	}
	return r, nil
}

// Transport 按地址协议构造 go-git 认证方式:
//   - http(s) → BasicAuth(密码=token;用户名按平台启发式选取,见 BasicAuth);
//   - ssh / scp 语法 → PublicKeys(secret 为私钥 PEM,用户名取凭据用户名,缺省用 URL 里的用户)。
//
// secret 对 HTTP 是令牌,对 SSH 是私钥;两者都不会进 URL/日志/错误文本。
func Transport(repoURL, username, secret string) (transport.AuthMethod, error) {
	_, auth, err := Resolve(repoURL, username, secret)
	return auth, err
}

// Resolve 一次完成「SSRF 收口 + 地址归一化 + 认证构造」,返回可直接交给 go-git 的
// (URL, AuthMethod)。调用方必须用返回的 URL 而非用户原始输入——scp 语法(git@host:path)
// 只有归一化成 ssh:// 才能被 go-git 的 ssh 传输消费。
func Resolve(repoURL, username, secret string) (string, transport.AuthMethod, error) {
	r, err := ResolveRepoURL(repoURL)
	if err != nil {
		return "", nil, err
	}
	auth, err := TransportFor(r, username, secret)
	if err != nil {
		return "", nil, err
	}
	return r.URL, auth, nil
}

// HTTPAuth 构造 http(s) 的 BasicAuth,并把「无 token」收敛为接口 nil。
// 必须如此:go-git 见到非 nil 的 AuthMethod 就会调用其 SetAuth,
// 而 (*githttp.BasicAuth)(nil) 是**非 nil 接口**持有 nil 指针,会在调用时 panic。
func HTTPAuth(repoURL, username, token string) transport.AuthMethod {
	ba := BasicAuth(repoURL, username, token)
	if ba == nil {
		return nil
	}
	return ba
}

// TransportFor 对已解析(且已过 SSRF 收口)的地址构造认证方式。
func TransportFor(r giturl.Repo, username, secret string) (transport.AuthMethod, error) {
	if !r.IsSSH() {
		return HTTPAuth(r.URL, username, secret), nil
	}
	if strings.TrimSpace(secret) == "" {
		return nil, ErrInvalidPrivateKey
	}
	user := strings.TrimSpace(username)
	if user == "" {
		user = r.User
	}
	// 先用 x/crypto 分类私钥:带口令必须明确报 ErrEncryptedPrivateKey。
	// (go-git 在空口令下会走「尝试解密」分支,只吐出 "bcrypt_pbkdf: empty password" 之类的
	//  底层文本,无法区分「密钥坏了」与「密钥有口令」。)
	if _, perr := ssh.ParseRawPrivateKey([]byte(secret)); perr != nil {
		if isPassphraseErr(perr) {
			return nil, ErrEncryptedPrivateKey
		}
		return nil, ErrInvalidPrivateKey
	}
	keys, kerr := gogitssh.NewPublicKeys(user, []byte(secret), "")
	if kerr != nil {
		return nil, ErrInvalidPrivateKey
	}
	// 必须显式设置:go-git 在 callback 为 nil 时会回退去读宿主 ~/.ssh/known_hosts,
	// 容器/服务账号下该文件通常不存在,会导致首次握手直接失败。
	keys.HostKeyCallback = currentHostKeyCallback()
	return keys, nil
}

// isPassphraseErr 判定私钥解析失败是否因「带口令」。
// x/crypto 对老式 PEM 返回 *PassphraseMissingError,对 OpenSSH 新格式只返回普通错误串,
// 故两者都要认(仅用于错误归类;误判最坏结果也只是提示语不同)。
func isPassphraseErr(err error) bool {
	var pme *ssh.PassphraseMissingError
	if errors.As(err, &pme) {
		return true
	}
	return strings.Contains(err.Error(), "passphrase protected")
}

// ValidateSecret 校验 secret 与地址协议是否匹配(建凭据/建项目时的前置把关):
// SSH 地址要求 secret 是可解析的无口令私钥;HTTP 地址不做额外约束。
func ValidateSecret(repoURL, secret string) error {
	r, err := ResolveRepoURL(repoURL)
	if err != nil {
		return err
	}
	if !r.IsSSH() {
		return nil
	}
	if strings.TrimSpace(secret) == "" {
		return ErrInvalidPrivateKey
	}
	_, terr := Transport(r.URL, "", secret)
	return terr
}
