// Package netguard 是「平台主动出网」的地址收口判定。
//
// 用它的两处姿态一致:通知渠道的 webhook 投递,和构建时去配置中心(Nacos)拉一份配置当环境变量。
// 两处的目标地址都是用户自己填的,所以都必须先过一遍这道门,否则流水线编辑器就成了一条
// 让平台替你访问任意地址的通道(含云厂商元数据 169.254.169.254)。
//
// 口径:只认 http/https;恒拒链路本地(含 169.254.169.254)、组播、未指定地址(0.0.0.0/::);
// 私网放行 —— 自托管平台本来就跑在内网,管理员对这些地址本就有访问权,不构成提权。
// 回环由调用方决定:webhook 与配置中心常常和平台跑在同一台机器上(本机起服务自测、
// sidecar 部署),拒回环会直接把这两种正常用法打死;而 git 克隆那条路用的是
// internal/giturl 的更严口径(回环亦拒),那是「仓库地址」这种外部输入的约定,不要混用。
package netguard

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ErrBlocked 是「地址被出网收口拒了」的分类标识。调用方拿它区分「是这道门拒的」和「压根连不上」:
// Nacos 客户端跟随重定向时,拒掉的那一跳会以 http 传输错误的形式回来,不区分就会说成「连不上」。
// 标识本身不参与文案 —— 拒的原因见 blockedErr.Error(),那是直接给人看的一句中文。
var ErrBlocked = errors.New("netguard: address not allowed")

// blockedErr 是拒址错误:文本给人读,分类靠 errors.Is(err, ErrBlocked)。
type blockedErr struct{ msg string }

func (e *blockedErr) Error() string        { return e.msg }
func (e *blockedErr) Is(target error) bool { return target == ErrBlocked }

func blocked(format string, args ...any) error {
	return &blockedErr{msg: fmt.Sprintf(format, args...)}
}

// Check 报告这个出网目标是否放行,不放行时返回可直接给人看的原因(不含查询串,避免带上凭据)。
func Check(raw string, allowLoopback bool) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return blocked("地址无法解析")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return blocked("只允许 http/https,当前协议是「%s」", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return blocked("地址缺少主机名")
	}
	blockedIP := func(ip net.IP) string {
		switch {
		case ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast():
			return "链路本地地址(云厂商元数据 169.254.169.254 就在这一段),已拒绝"
		case ip.IsUnspecified():
			return "未指定地址(0.0.0.0 / ::),已拒绝"
		case ip.IsLoopback() && !allowLoopback:
			return "本机回环地址,按当前用途已拒绝"
		}
		return ""
	}
	if ip := net.ParseIP(host); ip != nil {
		if why := blockedIP(ip); why != "" {
			return blocked("地址 %s 是%s", host, why)
		}
		return nil
	}
	addrs, lerr := net.LookupIP(host)
	if lerr != nil || len(addrs) == 0 {
		// DNS 解不出来不在这里下结论:请求自己会失败,那时的错误更准。
		return nil
	}
	for _, ip := range addrs {
		if why := blockedIP(ip); why != "" {
			return blocked("主机名 %s 解析到的 %s 是%s", host, ip, why)
		}
	}
	return nil
}
