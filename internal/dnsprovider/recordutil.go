package dnsprovider

import (
	"errors"
	"fmt"
	"strings"
)

// subDomain 把 name(FQDN 或已是相对子域)算成相对 zone 的子域名(DNSPod / 阿里云的 RR/sub_domain)。
//
// 规则(大小写不敏感,统一小写处理后比较):
//   - name == zone(域名顶点) → "@"
//   - name 以 ".zone" 结尾 → 去掉该后缀的前缀部分(如 app-x.example.com / example.com → "app-x")
//   - name 不含 zone 后缀(已是相对子域,如 "app-x") → 原样 trim 后返回;空 → "@"
//
// 返回值不带末尾点,顶点固定为 "@"(DNSPod 与 阿里云 AddDomainRecord 均以 "@" 表顶点)。
func subDomain(name, zone string) string {
	n := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(name, ".")))
	z := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(zone, ".")))
	if n == "" || n == z {
		return "@"
	}
	if z != "" && strings.HasSuffix(n, "."+z) {
		rr := strings.TrimSuffix(n, "."+z)
		if rr == "" {
			return "@"
		}
		return rr
	}
	// 已是相对子域(不含 zone 后缀)。
	return n
}

// rrEqual 比较 API 回显的主机头与目标 sub 是否同一条记录。
//
// 顶点写法各家不统一:腾讯云 3.0 的 RecordList[].Name 对根域记录回空串,DNSPod / 阿里云回 "@",
// 火山回 "@"。归一成「@ 与空串等价」,否则顶点上的 TXT/A 记录会被判成不存在而重复创建一条。
func rrEqual(existing, want string) bool {
	e := strings.ToLower(strings.TrimSpace(existing))
	w := strings.ToLower(strings.TrimSpace(want))
	if w == "@" {
		return e == "@" || e == ""
	}
	return e == w
}

// txtEqual 比较既有 TXT 内容是否已是目标值:各家 API 回显时可能带外层引号或多余空白
// (Cloudflare 会回 `"text"` 形式),归一后再比,免得每次 renew 都白发一次 PUT/Modify。
func txtEqual(existing, want string) bool {
	return strings.Trim(strings.TrimSpace(existing), `"`) == want
}

// wrapEnsure 把 verify 路径的错误重打成 ErrEnsureRecord 家族(保留人话尾巴,不泄漏凭据)。
// post() 内部统一用 ErrVerifyFailed 包网络/解析错误;在建记录上下文里改挂 ErrEnsureRecord 以便上层归类。
func wrapEnsure(err error) error { return wrapAs(err, ErrEnsureRecord) }

// wrapAs 同 wrapEnsure,但指定目标错误家族(A 记录 / TXT 记录各有各的错误,见 dnsprovider.go)。
// 凭据格式错误(ErrInvalidCredential)是确定性的,原样上抛供 httpapi 映射 400;其余非
// ErrVerifyFailed 的错误也原样返回(已经是本层抛出的具体错误,不再二次包装)。
func wrapAs(err, base error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrInvalidCredential) {
		return err
	}
	if errors.Is(err, ErrVerifyFailed) {
		tail := strings.TrimPrefix(err.Error(), ErrVerifyFailed.Error())
		tail = strings.TrimPrefix(tail, ":")
		return fmt.Errorf("%w:%s", base, strings.TrimSpace(tail))
	}
	return err
}
