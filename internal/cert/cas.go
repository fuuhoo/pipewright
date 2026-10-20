package cert

import "strings"

// CA 是一个 ACME 证书颁发机构的预置项(硬编码下拉,不做「任意 directory URL」):
// 每家的账户注册端点、速率限制、是否要 EAB 都不一样,给一个自由填的框等于把签发失败的
// 排查成本全推给用户。要加一家就在这里加一行 + 补一条 cas_test 用例。
//
// directory URL 全部取自各 CA 自己的文档(核实日期 2026-10-20):
//   - Let's Encrypt  https://letsencrypt.org/docs/staging-environment/
//   - ZeroSSL        https://zerossl.com/documentation/acme
//   - Google Trust   https://docs.cloud.google.com/certificate-manager/docs/public-ca-tutorial
//   - SSL.com        https://www.ssl.com/products/website-security/acme/
type CA struct {
	// ID 入库(与 acme_accounts.ca、certificates.ca 同值)。
	ID string
	// Name 是下拉里的人读名(不本地化:厂商名与「测试环境」是专有名词,翻成八种语言只会对不上账)。
	Name string
	// DirectoryURL 是 ACME v2 directory 端点,注册账户时钉死用它(见 0071 注释)。
	DirectoryURL string
	// RequiresEAB 为真时,不填 EAB 凭据就注册不上(ZeroSSL / GTS / SSL.com 三家都要)。
	RequiresEAB bool
	// Staging 标记这是测试环境:签出来的证书不被浏览器信任,页面要显式提醒。
	Staging bool
}

// 预置 CA 清单。顺序即下拉顺序:Let's Encrypt 生产在前(绝大多数用户要的),
// 测试环境紧跟其后(LE 的速率限制坑几乎每个人都踩过),其余按是否需要 EAB 排。
var cas = []CA{
	{ID: CALetsEncrypt, Name: "Let's Encrypt", DirectoryURL: "https://acme-v02.api.letsencrypt.org/directory"},
	{ID: CALetsEncryptStaging, Name: "Let's Encrypt (测试环境)", DirectoryURL: "https://acme-staging-v02.api.letsencrypt.org/directory", Staging: true},
	{ID: CAZeroSSL, Name: "ZeroSSL", DirectoryURL: "https://acme.zerossl.com/v2/DV90", RequiresEAB: true},
	{ID: CAGoogle, Name: "Google Trust Services", DirectoryURL: "https://dv.acme-v02.api.pki.goog/directory", RequiresEAB: true},
	{ID: CASSLCom, Name: "SSL.com", DirectoryURL: "https://acme.ssl.com/sslcom-dv/directory", RequiresEAB: true},
}

// CA 枚举 ID。
const (
	// CALetsEncrypt 是 Let's Encrypt 生产环境(免费、90 天、无需 EAB)。
	CALetsEncrypt = "letsencrypt"
	// CALetsEncryptStaging 是 LE 测试环境:证书不被信任,但速率限制宽松,用来验通链路。
	CALetsEncryptStaging = "letsencrypt_staging"
	// CAZeroSSL 是 ZeroSSL(90 天,需在自己的控制台领 EAB 凭据)。
	CAZeroSSL = "zerossl"
	// CAGoogle 是 Google Trust Services(需 GCP 侧签发 EAB 凭据)。
	CAGoogle = "google"
	// CASSLCom 是 SSL.com 的 DV 目录(需 EAB;RSA/ECC 分目录的形态这里只取统一的 DV 端点)。
	CASSLCom = "sslcom"
)

// ListCAs 返回预置 CA 清单(下拉数据源;顺序即上表顺序)。
func ListCAs() []CA {
	out := make([]CA, len(cas))
	copy(out, cas)
	return out
}

// LookupCA 按枚举 ID 取 CA;未知 ID → ok=false。
func LookupCA(id string) (CA, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, c := range cas {
		if c.ID == id {
			return c, true
		}
	}
	return CA{}, false
}

// ValidCA 报告 id 是否为受支持的 CA 枚举。
func ValidCA(id string) bool { _, ok := LookupCA(id); return ok }
