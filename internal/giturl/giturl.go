// Package giturl 统一解析代码仓库地址,识别 http(s) 与 SSH 两类,
// 并把 scp 语法(git@host:path.git)归一化为 go-git 可消费的 ssh:// URL。
//
// 背景:平台此前的仓库地址校验在 project/build/ai/httpapi 四处各自复制,
// 且只放行 http/https——自建 GitLab 的 SSH 地址(git@host:path)被误判不可达。
// 本包收敛「解析 + SSRF 判定」单一真源;go-git 的 ssh 传输只认带 scheme 的
// URL,故 scp 语法必须在此归一化。
//
// 安全:SSRF 收口策略与全平台一致——拒回环/链路本地(含云元数据)/未指定地址,
// 私网(RFC1918 / fc00::/7)放行(自托管内网 Git 友好)。
package giturl

import (
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Kind 是仓库地址的协议类别。
type Kind int

const (
	// KindInvalid 表示无法识别的地址(空串 / file:// / git:// / ftp:// 等)。
	KindInvalid Kind = iota
	// KindHTTP 表示 http/https 仓库。
	KindHTTP
	// KindSSH 表示 ssh:// 或 scp 语法(git@host:path.git)仓库。
	KindSSH
)

const (
	defaultSSHUser = "git"
	defaultSSHPort = 22
)

// Repo 是解析后的仓库地址视图。
type Repo struct {
	// Raw 是用户输入的原始地址(去首尾空白)。
	Raw string
	// Kind 是协议类别。
	Kind Kind
	// URL 是归一化后的地址:http(s) 原样;scp/ssh 语法统一为 ssh://user@host[:port]/path。
	// 直接交给 go-git 消费。
	URL string
	// Host 是主机名(不含端口;IPv6 不含括号)。
	Host string
	// Port 是 SSH 端口;未显式指定为 0(调用方按 22 处理)。http 恒为 0。
	Port int
	// User 是生效的 SSH 用户名(URL 显式 > 默认 git)。
	User string
	// UserExplicit 标记 User 是否来自 URL 显式书写(scp 语法恒为 true)。
	UserExplicit bool
	// Path 是仓库路径(ssh:// 的 u.Path 去前导斜杠;scp 的 path 段)。
	Path string
}

// IsSSH 报告是否为 SSH 类仓库。
func (r Repo) IsSSH() bool { return r.Kind == KindSSH }

// IsHTTP 报告是否为 HTTP(S) 类仓库。
func (r Repo) IsHTTP() bool { return r.Kind == KindHTTP }

// Parse 解析仓库地址。无法识别时 ok=false(调用方按不可达/拒绝处理)。
func Parse(raw string) (Repo, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Repo{}, false
	}
	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"):
		u, err := url.Parse(s)
		if err != nil || u.Hostname() == "" {
			return Repo{}, false
		}
		return Repo{Raw: s, Kind: KindHTTP, URL: s, Host: u.Hostname()}, true
	case strings.HasPrefix(lower, "ssh://"):
		return parseSSHURL(s)
	default:
		return parseSCP(s)
	}
}

// parseSSHURL 解析 ssh://[user@]host[:port]/path;user 缺省 "git"。
func parseSSHURL(s string) (Repo, bool) {
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" {
		return Repo{}, false
	}
	user, explicit := u.User.Username(), false
	if user != "" {
		explicit = true
	} else {
		user = defaultSSHUser
	}
	port := 0
	if p := u.Port(); p != "" {
		n, perr := strconv.Atoi(p)
		if perr != nil || n <= 0 || n > 65535 {
			return Repo{}, false
		}
		port = n
	}
	norm := "ssh://" + user + "@" + u.Hostname()
	if port > 0 {
		norm += ":" + strconv.Itoa(port)
	}
	norm += u.Path
	return Repo{
		Raw: s, Kind: KindSSH, URL: norm,
		Host: u.Hostname(), Port: port,
		User: user, UserExplicit: explicit,
		Path: strings.TrimPrefix(u.Path, "/"),
	}, true
}

// parseSCP 解析 scp 语法 user@host:path.git(无 scheme)。
// 判定规则(对齐 git 客户端启发式):首个 ':' 左侧必须含 '@' 且不含路径分隔符,
// 右侧为非空且不以 '/' 开头(排除本地绝对路径与 Windows 盘符)。
func parseSCP(s string) (Repo, bool) {
	colon := strings.IndexByte(s, ':')
	if colon <= 0 {
		return Repo{}, false
	}
	left, path := s[:colon], s[colon+1:]
	if strings.ContainsAny(left, "/\\") {
		return Repo{}, false
	}
	at := strings.IndexByte(left, '@')
	if at <= 0 || at == len(left)-1 {
		return Repo{}, false
	}
	user, host := left[:at], left[at+1:]
	if host == "" || path == "" || strings.HasPrefix(path, "/") {
		return Repo{}, false
	}
	norm := "ssh://" + user + "@" + host + "/" + path
	return Repo{
		Raw: s, Kind: KindSSH, URL: norm,
		Host: host, User: user, UserExplicit: true, Path: path,
	}, true
}

// BlockedHost 判定主机名是否落在 SSRF 禁止区:回环、链路本地(含云元数据
// 169.254.169.254)、未指定地址。私网(RFC1918 / fc00::/7)不在此列(放行)。
// 主机名解析失败时不拦截(留给连接层失败,避免误拒临时 DNS 抖动)。
func BlockedHost(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return true
	}
	// 容错 "host:port" / "[v6]:port" 形式(Hostname() 已剥离时的双保险)。
	if h, _, err := net.SplitHostPort(host); err == nil && h != "" {
		host = h
	}
	host = strings.Trim(host, "[]")
	if ip := net.ParseIP(host); ip != nil {
		return blockedIP(ip)
	}
	addrs, err := net.LookupIP(host)
	if err != nil || len(addrs) == 0 {
		return false
	}
	for _, ip := range addrs {
		if blockedIP(ip) {
			return true
		}
	}
	return false
}

func blockedIP(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified()
}
