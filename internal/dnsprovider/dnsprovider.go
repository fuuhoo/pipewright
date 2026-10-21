// Package dnsprovider 是「DNS 提供商集成层」(R3 E3.1–E3.4)的领域层。
//
// 一个 Provider 按引用绑定一条 vault 凭据(API token / key):DB 中只存 credential_id,
// 绝无任何明文 token(与 target.Server 同纪律)。需要 token 时经 vault.Reveal 在进程内取明文,
// 用完即弃,绝不入库 / 日志 / 响应 / 错误体。
//
// 能力:
//   - DNSClient 抽象「为某域建/改 A 记录」「列出凭据账号托管的根域」(注入便于单测,不触真网络)。
//     六家提供商均经 net/http 直连各自 API 真实实现(无 SDK):Cloudflare 用 v4 REST(Bearer token);
//     DNSPod 用经典 dnsapi.cn form-POST(login_token=id,token);阿里云用 RPC v1(HMAC-SHA1 签名);
//     腾讯云用 DNS 解析 API 3.0(JSON + TC3-HMAC-SHA256);百度用 BCE REST(bce-auth-v1);
//     火山用 RPC(HMAC-SHA256 签名,与 AWS SigV4 同构)。
//     六家的 DNS-01 通配符证书签发也都能用(走 Caddy 镜像内的 DNS 插件,与自动建 A 记录正交)——
//     例外是百度:镜像里没有可用插件,故它只能服务「平台自己签证书」,不能绑反代路由。
//   - AllocateSubdomain:瞬时挑一个可读子域名 app-<6char>.<zone>(zone 来自该凭据账号托管的域名清单,
//     由前端选定)→ 自动建 A 记录指向宿主机
//     公网 IP → 创建一条 DNS-01 的反代路由(证书即刻可签)。
//   - TXTClient / EnsureTXTRecord:为**平台自己**经 ACME DNS-01 签证书写校验用 TXT 记录
//     (internal/cert 那条线;六家提供商均经各自 API 真实实现)。
//
// AC-SEC:写入解析的 IP 是宿主机的(由调用方传入,非用户自由文本);zone/name 服务端严格校验;
// 命令 / API 入参严格归一,杜绝注入。
package dnsprovider

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
)

// 提供商类型枚举(DB 存小写字串;JSON 同名)。
const (
	// TypeCloudflare 是 Cloudflare DNS(自动 A 记录已真实实现)。
	TypeCloudflare = "cloudflare"
	// TypeDNSPod 是腾讯云 DNSPod(自动 A 记录已真实实现;凭据格式 "id,token")。
	TypeDNSPod = "dnspod"
	// TypeAliDNS 是阿里云 DNS(自动 A 记录已真实实现;凭据格式 "accessKeyId,accessKeySecret")。
	TypeAliDNS = "alidns"
	// TypeTencentCloud 是腾讯云「云解析」DNS 解析 API 3.0(凭据格式 "SecretId,SecretKey")。
	// 与 TypeDNSPod 分开保留:2.0(dnsapi.cn + login_token)只覆盖 DNSPod 控制台的域名,
	// 腾讯云控制台添加的域名用 2.0 拿不到,两套账号体系不同。
	TypeTencentCloud = "tencentcloud"
	// TypeBaiduDNS 是百度智能云智能云解析 DNS(公网 DNS,BCE REST;凭据格式 "AccessKeyID,SecretAccessKey")。
	// 只走「平台自己经 ACME 签证书」那条路:官方 Caddy 镜像里没有可靠的 baidudns DNS 插件,
	// 故不允许把它绑到反代路由(proxy 侧硬拒),详见 internal/proxy.validateDNSProviderRef。
	TypeBaiduDNS = "baidudns"
	// TypeVolcengine 是火山引擎云解析 TrafficRoute DNS(凭据格式 "AccessKeyID,SecretAccessKey")。
	TypeVolcengine = "volcengine"
)

// 领域错误(人话由 httpapi 层映射状态码;错误体绝不含敏感信息)。
var (
	// ErrNotFound 表示 DNS 提供商不存在。
	ErrNotFound = errors.New("dnsprovider: provider not found")
	// ErrInvalidType 表示提供商类型枚举非法。
	ErrInvalidType = errors.New("dnsprovider: invalid provider type")
	// ErrEmptyName 表示展示名为空。
	ErrEmptyName = errors.New("dnsprovider: name must not be empty")
	// ErrEmptyCredentialID 表示未选择凭据。
	ErrEmptyCredentialID = errors.New("dnsprovider: credential id must not be empty")
	// ErrCredentialNotFound 表示引用的凭据不存在。
	ErrCredentialNotFound = errors.New("dnsprovider: referenced credential not found")
	// ErrVaultUnconfigured 表示保险库未配置 master key,无法取 DNS token。
	ErrVaultUnconfigured = errors.New("dnsprovider: vault unconfigured")

	// ErrProviderNotImplemented 表示该提供商的某能力(如自动建 A 记录)尚未实现。
	// 注意:DNS-01 通配符证书签发对三种提供商都可用(走 Caddy 镜像内 DNS 插件),不受此影响。
	ErrProviderNotImplemented = errors.New("dnsprovider: provider capability not implemented")
	// ErrInvalidCredential 表示保险库里存的凭据格式不符合该提供商约定(如 DNSPod 须 "id,token"、
	// 阿里云须 "accessKeyId,accessKeySecret")。错误体绝不含凭据任何片段。httpapi 层可映射 400。
	ErrInvalidCredential = errors.New("dnsprovider: invalid provider credential format")
	// ErrVerifyFailed 表示凭据校验失败(token 无效 / 账号无可管理的域 / API 不可达)。
	ErrVerifyFailed = errors.New("dnsprovider: zone verification failed")
	// ErrEnsureRecord 表示建/改 A 记录失败。
	ErrEnsureRecord = errors.New("dnsprovider: ensure A record failed")
	// ErrDeleteRecord 表示删除 A 记录失败(回收预览/子域名时清 DNS)。
	ErrDeleteRecord = errors.New("dnsprovider: delete A record failed")
	// ErrAllocate 表示子域名分配失败(多次随机仍冲突 / 建记录失败 / 建路由失败)。
	ErrAllocate = errors.New("dnsprovider: allocate subdomain failed")

	// ErrEnsureTXT 表示建/改 TXT 记录失败(DNS-01 校验题写不进域)。
	ErrEnsureTXT = errors.New("dnsprovider: ensure TXT record failed")
	// ErrDeleteTXT 表示删除 TXT 记录失败。与 A 记录那两个错误分开:失败原因要能指对记录类型,
	// 否则证书页上「发布失败:ensure A record failed」这种话没人看得懂。
	ErrDeleteTXT = errors.New("dnsprovider: delete TXT record failed")
	// ErrNoHostedZone 表示要写的记录名不在该凭据账号托管的任何一个域名下。这条是权限边界:
	// 提供商凭据能管的只有账号里那些区(签发/分配时按记录名**推导**落在哪个区),拿它去写
	// 别的域(哪怕只是拼错)必须硬失败,否则一份只该动 example.com 的凭据会被用于任意域。
	ErrNoHostedZone = errors.New("dnsprovider: no hosted zone covers the record name")
	// ErrEmptyTXTValue 表示 TXT 内容为空(DNS-01 的校验值是算出来的,空说明上游传错了,不该发到 API)。
	ErrEmptyTXTValue = errors.New("dnsprovider: TXT value must not be empty")
)

// Provider 是一个 DNS 提供商接入的领域模型(冻结契约)。绝不含 token 明文,只持 CredentialID 引用。
//
// 这里**没有**「根域」字段:该凭据能管哪些域,答案在服务商账号里而不是我们库里,
// 每次要用时经 DNSClient.ListZones 实时取,再按记录名推导落在哪个区(见 zone.go)。
type Provider struct {
	ID           string
	Type         string // cloudflare | dnspod | alidns | tencentcloud | baidudns | volcengine
	Name         string
	CredentialID string // 指向 vault 凭据(API token / key);明文绝不入此结构体
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// CreateInput 是登记 DNS 提供商的入参。
type CreateInput struct {
	Type         string
	Name         string
	CredentialID string
}

// DNSClient 抽象一个具体提供商的 DNS 写/校验能力(注入便于单测,不触真网络)。
// EnsureARecord 建或就地 upsert 一条 A 记录(name → ip);ListZones 列出该凭据账号托管的根域。
type DNSClient interface {
	// EnsureARecord 为 zone(根域)下的 name(FQDN)建/改 A 记录指向 ip(幂等 upsert)。
	EnsureARecord(ctx context.Context, zone, name, ip string) error
	// DeleteARecord 删除 zone(根域)下 name(FQDN)的 A 记录(回收子域名时清 DNS;幂等:无记录视为成功)。
	DeleteARecord(ctx context.Context, zone, name string) error
	// ListZones 列出当前凭据可管理的根域清单(如 example.com;归一小写、翻页取全)。
	// 失败返回人话错误(绝不含 token)。这份清单同时是「凭据是否可用」的判据与 zone 推导的依据。
	ListZones(ctx context.Context) ([]string, error)
}

// TXTClient 抽象 DNS-01 需要的 TXT 记录读写。
//
// 为什么单开一个接口而不是往 DNSClient 上加两个方法:A 记录那条线(自动分配子域名 / 预览环境)
// 一辈子用不到 TXT,TXT 那条线(平台自己经 ACME 签证书)用不到 A 记录。合在一起等于让每个测试假
// 客户端都得长出与己无关的方法,还把「这个提供商支持哪些写入」揉成一团。
// 真实客户端同时实现两个接口,service 经 txtTarget 取;没实现的(如未知类型)得到
// ErrProviderNotImplemented,而不是静默走 A 记录那条路径。
type TXTClient interface {
	// EnsureTXTRecord 为 zone 下的 name 落一条 TXT 记录,内容归一为 value(幂等:同名多条时收敛成一条)。
	// DNS-01 只允许 _acme-challenge.<domain> 上有**一条** TXT:上一次清理失败留下的旧值会让 CA
	// 直接判失败,所以这里不是「有就跳过」,而是「改成 value 并删掉其余」。
	EnsureTXTRecord(ctx context.Context, zone, name, value string) error
	// DeleteTXTRecord 删除 zone 下 name 的**全部** TXT 记录(幂等:无记录视为成功)。
	// 与 DeleteARecord 不同 —— 那里删掉一条就返回,这里必须清干净:残留一条就等于下次签发作废。
	DeleteTXTRecord(ctx context.Context, zone, name string) error
}

// 各家真实客户端必须同时实现 DNSClient 与 TXTClient。编译期钉住:漏一个方法不会等到签证书
// 那天才由类型断言失败暴露,而是 `go build` 直接不过。
var (
	_ DNSClient = (*cloudflareClient)(nil)
	_ DNSClient = (*dnspodClient)(nil)
	_ DNSClient = (*alidnsClient)(nil)
	_ DNSClient = (*tencentCloudClient)(nil)
	_ DNSClient = (*baiduDNSClient)(nil)
	_ DNSClient = (*volcengineClient)(nil)

	_ TXTClient = (*cloudflareClient)(nil)
	_ TXTClient = (*dnspodClient)(nil)
	_ TXTClient = (*alidnsClient)(nil)
	_ TXTClient = (*tencentCloudClient)(nil)
	_ TXTClient = (*baiduDNSClient)(nil)
	_ TXTClient = (*volcengineClient)(nil)
)

// Service 定义 DNS 提供商领域对外接口(冻结契约;httpapi 消费)。
type Service interface {
	// List 返回全部 DNS 提供商(创建时间倒序;无 token)。
	List(ctx context.Context) ([]Provider, error)
	// Get 返回单个提供商(无 token);不存在 → ErrNotFound。
	Get(ctx context.Context, id string) (*Provider, error)
	// Create 校验入参(含凭据存在性)→ 落库;返回提供商视图(无 token)。
	Create(ctx context.Context, in CreateInput) (*Provider, error)
	// Delete 删除提供商;不存在 → ErrNotFound。
	Delete(ctx context.Context, id string) error
	// Zones 实时取该提供商凭据在服务商账号里托管的全部根域(归一小写、去重、字典序)。
	// 一次 API 列表调用同时回答两件事:凭据能不能用(报错即不可用),以及能管哪些域
	// (zone 推导与「一键分配子域名」的候选都来自这里)。token 用完即弃,绝不外泄。
	// 域名清单不是敏感值(用户在自己控制台看得见),但只在认证 + CSRF 的设置页端点回显。
	Zones(ctx context.Context, id string) ([]string, error)
	// DeleteSubdomainRecord 删除 fqdn 所属区(按 fqdn 从账号区清单推导)的 A 记录
	// (回收预览/子域名时清 DNS;幂等)。
	DeleteSubdomainRecord(ctx context.Context, providerID, fqdn string) error
	// AllocateSubdomain 瞬时分配子域名(E3.3 + E3.4):在 in.Zone 下挑 app-<6char>.<zone> →
	// 建 A 记录指向 hostIP → 创建一条 DNS-01 反代路由(证书即刻可签)。返回新建的反代路由。
	AllocateSubdomain(ctx context.Context, in AllocateInput) (*RouteRef, error)

	// AllocateFQDN 为一个**指定的** FQDN(in.Subdomain)建 A 记录指向 hostIP → 创建一条 DNS-01
	// 反代路由(R4 E4.1 预览环境用:子域名确定而非随机)。返回新建路由引用。
	// in.Subdomain 缺省 → ErrAllocate;它的 zone 由账号区清单推导(与签发 TXT 同一套,见 zone.go)。
	AllocateFQDN(ctx context.Context, in AllocateInput) (*RouteRef, error)

	// ProviderType 返回某提供商的类型(供 proxy 校验 DNS-01 引用,不取 token)。
	// 不存在 → ok=false。供 proxy.DNSResolver 适配。
	ProviderType(ctx context.Context, providerID string) (providerType string, ok bool, err error)

	// EnsureTXTRecord 为 name(FQDN)落一条 TXT 记录,内容归一为 value(幂等收敛成一条)。
	// 供平台自己经 ACME DNS-01 签证书时写校验题:cert 层算出 `_acme-challenge.<域名>` 与校验值,
	// 这里负责推导它属于该凭据账号里的哪个区。name 不被账号里任何区覆盖 → ErrNoHostedZone(凭据越权)。
	// providerID 不存在 → ErrNotFound;vault 未配/凭据缺失 → 相应错误;错误体绝不含 token。
	EnsureTXTRecord(ctx context.Context, providerID, name, value string) error
	// DeleteTXTRecord 删除 name 的全部 TXT 记录(ACME 校验通过后清理;幂等)。
	DeleteTXTRecord(ctx context.Context, providerID, name string) error

	// ResolveToken 取某提供商的 (类型, token 明文)(供 proxy 在 apply 时渲染 DNS-01)。
	// token 仅供调用方即时注入 0600 临时 Caddyfile,绝不日志/回库/回 API。
	// 不存在 → ok=false;vault 未配/凭据缺失 → err。供 proxy.DNSResolver 适配。
	ResolveToken(ctx context.Context, providerID string) (providerType, token string, ok bool, err error)
}

// AllocateInput 是瞬时子域名分配的入参。
type AllocateInput struct {
	ProviderID        string
	ServerID          string
	UpstreamContainer string
	UpstreamPort      int
	// HostIP 是宿主机公网 IP(由调用方/上层据 server 解析得出,非用户自由文本)。写入 A 记录指向它。
	HostIP string
	// Subdomain 是 AllocateFQDN 用的**指定** FQDN(如 pr-12-abcd.preview.example.com);
	// AllocateSubdomain 不用此字段(它随机挑后缀)。它所属的区由账号区清单推导(见 zone.go)。
	Subdomain string
	// Zone 是 AllocateSubdomain 要落到的根域(前端从 Service.Zones 返回的账号域名清单里选)。
	// 随机后缀 `app-xxxxxx` 本身推不出区,必须由调用方显式给定;AllocateFQDN 不用此字段。
	Zone string
}

// RouteRef 是 AllocateSubdomain 创建出的反代路由引用(避免本包 import proxy 形成环;
// 由上层用其 RouteID 取完整 proxy.Route DTO)。
type RouteRef struct {
	RouteID    string
	Domain     string
	ProviderID string
}

// baseDomainRe 校验根域 FQDN(每段字母数字/连字符,段间点分,顶级域 ≥2 字母)。不接受裸 IP / 端口 / 通配符。
var baseDomainRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// ValidType 报告提供商类型是否为受支持枚举(供 proxy 校验 DNS-01 引用时复用)。
// 注意:这里只管「登记得过」,不管「Caddy 镜像里有没有对应的 DNS 插件」——后者由
// proxy.validateDNSProviderRef 把关(百度就是登记得起、但不能绑路由的类型)。
func ValidType(t string) bool {
	switch t {
	case TypeCloudflare, TypeDNSPod, TypeAliDNS, TypeTencentCloud, TypeBaiduDNS, TypeVolcengine:
		return true
	default:
		return false
	}
}

// validateCreate 校验登记入参。
func validateCreate(in CreateInput) error {
	if !ValidType(in.Type) {
		return ErrInvalidType
	}
	if strings.TrimSpace(in.Name) == "" {
		return ErrEmptyName
	}
	if strings.TrimSpace(in.CredentialID) == "" {
		return ErrEmptyCredentialID
	}
	return nil
}

// ValidBaseDomain 报告 s 是否为合法根域 FQDN。
func ValidBaseDomain(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s != "" && len(s) <= 253 && baseDomainRe.MatchString(s)
}
