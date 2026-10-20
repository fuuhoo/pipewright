// Package cert 是平台自持的 SSL 证书领域层:申请(ACME DNS-01)、监控(到期扫描)、发布(SSH 落盘)。
//
// 为什么需要这一层:反代线的证书确实签得出来,但它住在 Caddy 容器的卷里,proxy.go 刻意「绝不读
// .key」——平台手里从来没有私钥,于是既报不出可靠的到期时间,也发不到第二台机器。这张证书仓库补的
// 就是那半条:私钥以 vault secretbox 密文入库,明文只在进程内经 OpenSecret 取出即弃。
//
// 加密纪律与 credentials 同一条(AC-SEC-01):
//   - 只有私钥是秘密。key_sealed / account_key_sealed / eab_key_sealed 存密文 BLOB;
//     cert_pem / chain_pem / 域名 / 到期时间是公开材料,刻意存明文,便于备份与对账。
//   - 对外视图 Certificate 结构体**不含任何私钥字段**(私钥在仓储层走未导出字段),
//     所以 JSON 序列化这条路上不可能泄漏;要拿私钥只能显式调 Bundle(),由调用方负责不外传。
//
// 发布纪律与部署层同一条:目标机上执行的每条命令都必须来自用户填写的 post_command,留空就是
// 一条都不跑,平台绝不代替用户发明命令;用户文本也永不拼进脚本,只走位置参数(AC-SEC-02)。
package cert

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// 证书来源枚举(入库小写)。
const (
	// SourceACME 表示由平台经 DNS-01 自己签发。
	SourceACME = "acme"
	// SourceUpload 表示用户粘贴的已有证书(平台只监控与发布,不签)。
	SourceUpload = "upload"
)

// 证书生命周期状态(由签发流程与到期扫描器回写,不是用户可填字段)。
const (
	// StatusPending 表示记录已建、还没有可用证书正文(新建待签、或签发进行中)。
	StatusPending = "pending"
	// StatusIssued 表示有证书正文且在有效期之内。
	StatusIssued = "issued"
	// StatusFailed 表示最近一次签发失败(有正文但从未签成功过的情形不存在:失败即无正文)。
	StatusFailed = "failed"
	// StatusExpired 表示证书正文的 not_after 已过期。
	StatusExpired = "expired"
)

// 发布落点的最近一次结果。
const (
	// DeployOK 表示上一次发布成功。
	DeployOK = "success"
	// DeployFailed 表示上一次发布失败(原因见 LastError)。
	DeployFailed = "failed"
)

// 落点状态里的「从未发布」:库里就是空串,列在这里是为了让 service 层别到处写字面量 ”。
const DeployNone = ""

// 领域错误。错误体一律人话、且**绝不包含**私钥、账户密钥、EAB 密钥或 DNS token 的任何片段;
// httpapi 层据其映射状态码。
var (
	// ErrNotFound 表示证书/账户/落点不存在。
	ErrNotFound = errors.New("cert: not found")
	// ErrDomainExists 表示该域名已有一张证书(一个域名只留一条记录,重签是就地更新它)。
	ErrDomainExists = errors.New("cert: a certificate already exists for this domain")
	// ErrInvalidDomain 表示域名格式非法(含通配符形态)。
	ErrInvalidDomain = errors.New("cert: invalid domain name")
	// ErrNoCA 表示 ACME 来源未指定 CA,或 CA 枚举非法。
	ErrNoCA = errors.New("cert: unknown or missing ACME CA")
	// ErrNoDNSProvider 表示 ACME 签发缺 DNS 提供商(写不了 TXT 就完不成 DNS-01)。
	ErrNoDNSProvider = errors.New("cert: DNS provider is required for DNS-01 issuance")
	// ErrEmptyPEM 表示该填的 PEM 正文为空。
	ErrEmptyPEM = errors.New("cert: PEM content is empty")
	// ErrNoCertBlock 表示正文里没有 X.509 证书块(多半是粘错内容或换行丢了)。
	ErrNoCertBlock = errors.New("cert: no certificate block in PEM")
	// ErrBadKeyPEM 表示私钥块缺失、格式不支持或是加密私钥。
	ErrBadKeyPEM = errors.New("cert: unsupported private key PEM")
	// ErrKeyMismatch 表示私钥与证书公钥不匹配(填串了)。
	ErrKeyMismatch = errors.New("cert: private key does not match the certificate")
	// ErrDomainMismatch 表示证书的 SAN/CN 覆盖不到填写的域名(发出去会直接报域名不符)。
	ErrDomainMismatch = errors.New("cert: certificate does not cover the domain")
	// ErrNotACME 表示这张证书不是 ACME 签的(upload 来源),平台签不出也续不了。
	ErrNotACME = errors.New("cert: only ACME-issued certificates can be renewed by the platform")
	// ErrEmptyPath 表示落点路径为空或非绝对路径。
	ErrEmptyPath = errors.New("cert: certificate and key paths must be absolute")
	// ErrSamePath 表示证书与私钥落在同一个路径(会把公开证书覆盖成私钥,或反之)。
	ErrSamePath = errors.New("cert: certificate and key paths must differ")
	// ErrVaultUnconfigured 表示 master key 缺失,密封/解封都做不了(此时签发与导入都不可用)。
	ErrVaultUnconfigured = errors.New("cert: vault is not configured")
	// ErrServerNotFound 表示落点引用的目标主机不存在。
	ErrServerNotFound = errors.New("cert: target server not found")
	// ErrDeployFailed 表示发布执行失败(逐落点的 last_error 里存的是人话原因)。
	ErrDeployFailed = errors.New("cert: certificate deployment failed")
)

// domainRe 校验单个域名(允许最左标签为 * 的通配形态)。与 dnsprovider.baseDomainRe 同形,
// 但 ACME 的 SAN 里 "*.example.com" 合法,故这里多了通配分支。
var domainRe = regexp.MustCompile(`^(\*\.|)([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// ValidDomain 报告 s 是否为合法域名(小写、含可选通配前缀、总长 ≤253)。
func ValidDomain(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s != "" && len(s) <= 253 && domainRe.MatchString(s)
}

// Certificate 是一张证书的对外视图。**结构体里没有私钥**,也没有任何账户密钥材料:
// 私钥住在仓储层的未导出字段里,只有 Bundle()/PrivateKey() 会显式取出。
type Certificate struct {
	ID            string
	Domain        string
	SANs          []string
	Source        string // acme | upload
	Status        string // pending | issued | failed | expired
	CA            string // ACME CA 枚举;upload 为 ''
	DNSProviderID string
	AutoRenew     bool
	KeyAlgo       string // 展示用(ECDSA-P256 / RSA-2048 …)
	NotBefore     time.Time
	NotAfter      time.Time
	Issuer        string
	Serial        string
	CertPEM       string
	ChainPEM      string
	HasKey        bool // 有私钥才能发布/导出完整证书包;upload 时必为 true,acme 签成功后也是
	LastError     string
	IssuedAt      time.Time
	CheckedAt     time.Time
	// RemindAt 是到期扫描器上次为这张证书发提醒的时刻(零值 = 从没发过)。
	// 只有「反复会出现」的提醒(即将到期、续期失败)受它节流;续期成功一次就是一次,不拦。
	RemindAt  time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

// DaysLeft 返回距到期还有多少天(向上取整;已过期为负)。无正文时按 0 天处理。
func (c *Certificate) DaysLeft(now time.Time) int {
	if c.NotAfter.IsZero() {
		return 0
	}
	d := c.NotAfter.Sub(now)
	if d < 0 {
		return -int((-d + 24*time.Hour - 1) / (24 * time.Hour))
	}
	return int((d + 24*time.Hour - 1) / (24 * time.Hour))
}

// ReminderDue 报告这张证书现在能否再发一条「即将到期 / 续期失败」提醒:距上次提醒满一个
// ReminderInterval 才放行。两类提醒共用同一道闸门 —— 一张证书同一时刻只该有一种声音,
// 否则「快到期了」和「续期没成功」两条通知前后脚发出去,用户看不出到底该做什么。
func (c *Certificate) ReminderDue(now time.Time) bool {
	return c.RemindAt.IsZero() || !now.Before(c.RemindAt.Add(ReminderInterval))
}

// DeployTarget 是一张证书的一个发布落点(可复用配置 + 最近一次结果)。
type DeployTarget struct {
	ID          string
	CertID      string
	ServerID    string
	CertPath    string
	KeyPath     string
	PostCommand string
	AutoDeploy  bool
	LastStatus  string
	LastError   string
	DeployedAt  time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Account 是一个 CA 的 ACME 账户视图(同样不含账户私钥,只有kid/邮箱这些非秘密)。
type Account struct {
	ID           string
	CA           string
	DirectoryURL string
	Contact      string
	AccountURL   string
	EABKeyID     string
	HasEABKey    bool
	RegisteredAt time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ImportInput 是「导入已有证书」的入参(用户粘贴正文)。
type ImportInput struct {
	Domain   string
	CertPEM  string
	KeyPEM   string
	ChainPEM string
	// DNSProviderID 可选:仅为让后续 ACME 重签沿用同一提供商而留下,不参与本次校验。
	DNSProviderID string
	AutoRenew     bool
}

// CreateInput 是「新建一张待签证书」的入参(source=acme)。
type CreateInput struct {
	Domain        string
	SANs          []string
	CA            string
	DNSProviderID string
	AutoRenew     bool
}

// DeployTargetInput 是新建/更新一个发布落点的入参。
type DeployTargetInput struct {
	ServerID    string
	CertPath    string
	KeyPath     string
	PostCommand string
	AutoDeploy  bool
}

// Seal 抽象 vault 的密封/解封最小能力(注入便于单测:测试用恒等或固定前缀的实现)。
// 明文只在进程内出现,密封后的密文才入库。
type Seal interface {
	SealSecret(plaintext []byte) ([]byte, error)
	OpenSecret(sealed []byte) ([]byte, error)
}

// ParseLeaf 从证书 PEM 里取**第一块**叶子证书并解析。返回归一化后的 PEM(重新 Encode 过,
// 入库的正文与用户粘贴的缩进/换行差异无关)与 x509 证书。
// 后面还跟着证书块也不报错:用户把 fullchain 整体粘进「证书」框是常见误用,内容与链框等价。
func ParseLeaf(certPEM string) (string, *x509.Certificate, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(certPEM)))
	if block == nil || block.Type != "CERTIFICATE" {
		return "", nil, ErrNoCertBlock
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", nil, fmt.Errorf("cert: %w: %s", ErrNoCertBlock, err.Error())
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: block.Bytes})), leaf, nil
}

// ParseChain 归一化中间证书链正文(可空:空即无链)。逐块校验确为证书,遇到解不开的内容直接拒绝 ——
// 链里少一块或多一块垃圾,发出去就可能在部分客户端上建不了链。
func ParseChain(chainPEM string) (string, error) {
	body := []byte(strings.TrimSpace(chainPEM))
	if len(body) == 0 {
		return "", nil
	}
	var out strings.Builder
	for len(body) > 0 {
		block, rest := pem.Decode(body)
		if block == nil || block.Type != "CERTIFICATE" {
			return "", ErrNoCertBlock
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return "", fmt.Errorf("cert: %w: %s", ErrNoCertBlock, err.Error())
		}
		out.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: block.Bytes}))
		body = bytes.TrimSpace(rest)
	}
	return out.String(), nil
}

// ParseKey 解析私钥 PEM,返回归一化正文(PKCS#8)、算法展示名与 crypto.Signer。
// 支持 PKCS#8(明文)、PKCS#1 RSA、SEC1 EC 三种常见形态;加密私钥直接拒绝——平台没有口令可以解它。
func ParseKey(keyPEM string) (normalized string, algo string, signer crypto.Signer, err error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(keyPEM)))
	if block == nil || !strings.HasSuffix(block.Type, "PRIVATE KEY") {
		return "", "", nil, ErrBadKeyPEM
	}
	if strings.Contains(block.Type, "ENCRYPTED") {
		return "", "", nil, ErrBadKeyPEM
	}
	k, algo, perr := parseKeyBlock(block)
	if perr != nil {
		return "", "", nil, perr
	}
	signer, ok := k.(crypto.Signer)
	if !ok {
		return "", "", nil, ErrBadKeyPEM
	}
	// 统一重编为 PKCS#8:目标机上的 nginx/caddy 都认,且重新编码后入库的正文与用户粘贴的
	// 缩进/换行差异无关,回读导出时不会每次都不一样。
	der, merr := x509.MarshalPKCS8PrivateKey(signer)
	if merr != nil {
		return "", "", nil, ErrBadKeyPEM
	}
	normalized = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	return normalized, algo, signer, nil
}

// parseKeyBlock 按块类型分派解析,并给出算法展示名。
func parseKeyBlock(block *pem.Block) (any, string, error) {
	switch block.Type {
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, "", ErrBadKeyPEM
		}
		return k, KeyAlgoName(k), nil
	case "RSA PRIVATE KEY":
		k, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, "", ErrBadKeyPEM
		}
		return k, KeyAlgoName(k), nil
	case "EC PRIVATE KEY":
		k, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, "", ErrBadKeyPEM
		}
		return k, KeyAlgoName(k), nil
	default:
		return nil, "", ErrBadKeyPEM
	}
}

// KeyAlgoName 给出私钥算法的人读展示名(入库到 key_algo,只用于页面显示)。
func KeyAlgoName(k crypto.PrivateKey) string {
	switch t := k.(type) {
	case *rsa.PrivateKey:
		return fmt.Sprintf("RSA-%d", t.N.BitLen())
	case *ecdsa.PrivateKey:
		return "ECDSA-P" + curveSize(t)
	case ed25519.PrivateKey:
		return "Ed25519"
	default:
		return ""
	}
}

// curveSize 把 EC 曲线名收敛成 256/384/521 这种展示形态(拼进 "ECDSA-P256")。
func curveSize(t *ecdsa.PrivateKey) string {
	if t.Curve == nil {
		return "?"
	}
	name := t.Curve.Params().Name
	if name == "" {
		return "?"
	}
	return strings.TrimPrefix(name, "P-")
}

// KeyMatchesCert 校验私钥与证书公钥是否为同一把(比较 PKIX 字节,避免类型断言漏掉 Ed25519)。
func KeyMatchesCert(leaf *x509.Certificate, signer crypto.Signer) bool {
	a, err := x509.MarshalPKIXPublicKey(leaf.PublicKey)
	if err != nil {
		return false
	}
	b, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil {
		return false
	}
	return string(a) == string(b)
}

// CoversDomain 报告证书 SAN(无 SAN 时退回 CN)是否覆盖域名。通配证书按 RFC 6125 的最左单标签匹配。
func CoversDomain(leaf *x509.Certificate, domain string) bool {
	domain = strings.ToLower(strings.TrimSpace(domain))
	names := leaf.DNSNames
	if len(names) == 0 {
		names = []string{leaf.Subject.CommonName}
	}
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == domain {
			return true
		}
		if before, ok := strings.CutPrefix(n, "*."); ok && before != "" {
			// *.example.com 覆盖 a.example.com,但不覆盖 a.b.example.com,也不覆盖 example.com 本身。
			if labels := strings.Split(domain, "."); len(labels) > 1 && strings.HasSuffix(domain, "."+before) {
				if len(labels) == len(strings.Split(before, "."))+1 {
					return true
				}
			}
		}
	}
	return false
}

// LeafDomains 返回证书的域名清单(SAN;无 SAN 时用 CN),用于导入时未填域名的兜底。
func LeafDomains(leaf *x509.Certificate) []string {
	out := make([]string, 0, len(leaf.DNSNames)+1)
	for _, n := range leaf.DNSNames {
		if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		if cn := strings.ToLower(strings.TrimSpace(leaf.Subject.CommonName)); cn != "" {
			out = append(out, cn)
		}
	}
	return out
}

// DeriveStatus 依据证书正文与当前时间给出状态。签发流程与到期扫描共用它,免得两边规则漂移。
func DeriveStatus(hasCert bool, notAfter, now time.Time) string {
	if !hasCert {
		return StatusPending
	}
	if !notAfter.IsZero() && now.After(notAfter) {
		return StatusExpired
	}
	return StatusIssued
}

// IssuanceWindow 是自动续期的触发窗口:剩余寿命不足该时长就续期。
// 取 30 天是 LE/ZeroSSL 90 天证书的行业惯例(certbot 默认同为 30 天),
// 给 DNS-01 失败留够重试余地,又不会让 LE 的续期窗口(30 天)提前太多。
const IssuanceWindow = 30 * 24 * time.Hour

// ReminderInterval 是「同一件事多久再提醒一次」的节流窗口。
//
// 为什么必须有:一张剩 29 天的证书在整个续期窗口里每轮都会被扫到,不节流就是每天几条
// 一模一样的通知,结局是用户把证书通知整体关掉 —— 那连真出事也没人看了。
// 取 7 天:30 天窗口里最多提醒 4 次,足够让人注意到,又不至于刷屏。续期成功不发第二次
// (窗口随新正文重置),所以那一类不节流。
const ReminderInterval = 7 * 24 * time.Hour

// NeedsRenew 报告这张证书是否该由到期扫描器尝试续期:
// 必须是 ACME 来源、开了自动续期、有证书正文且落在续期窗口内(或已过期)。
// upload 来源一律 false —— 平台签不出来,提醒用户手工换就是了(那由监控事件负责,不由这里)。
func NeedsRenew(c *Certificate, now time.Time) bool {
	if c.Source != SourceACME || !c.AutoRenew || c.NotAfter.IsZero() {
		return false
	}
	return now.Add(IssuanceWindow).After(c.NotAfter)
}

// Import 校验并解析「用户粘贴的已有证书」,产出一张可直接入库的证书。
// 三处硬校验各有理由:私钥与证书不匹配 → 发出去服务起不来;域名覆盖不到 → 浏览器直接报域名不符;
// 链里有垃圾正文 → 部分客户端建链失败。过期**不**拦,状态如实落成 expired。
func Import(in ImportInput, now time.Time) (*Certificate, string, error) {
	domain := strings.ToLower(strings.TrimSpace(in.Domain))
	certPEM := in.CertPEM
	if strings.TrimSpace(certPEM) == "" || strings.TrimSpace(in.KeyPEM) == "" {
		return nil, "", ErrEmptyPEM
	}
	normalized, leaf, err := ParseLeaf(certPEM)
	if err != nil {
		return nil, "", err
	}
	if domain == "" {
		// 允许不填域名:拿 SAN 首项兜底,免得用户从别处复制来还要手抄一遍。
		if ds := LeafDomains(leaf); len(ds) > 0 {
			domain = ds[0]
		}
	}
	if !ValidDomain(domain) {
		return nil, "", ErrInvalidDomain
	}
	if !CoversDomain(leaf, domain) {
		return nil, "", ErrDomainMismatch
	}
	chain, err := ParseChain(in.ChainPEM)
	if err != nil {
		return nil, "", err
	}
	keyPEM, algo, signer, err := ParseKey(in.KeyPEM)
	if err != nil {
		return nil, "", err
	}
	if !KeyMatchesCert(leaf, signer) {
		return nil, "", ErrKeyMismatch
	}

	c := &Certificate{
		Domain:        domain,
		SANs:          LeafDomains(leaf),
		Source:        SourceUpload,
		Status:        DeriveStatus(true, leaf.NotAfter, now),
		CA:            "",
		DNSProviderID: strings.TrimSpace(in.DNSProviderID),
		AutoRenew:     in.AutoRenew,
		KeyAlgo:       algo,
		NotBefore:     leaf.NotBefore.UTC(),
		NotAfter:      leaf.NotAfter.UTC(),
		Issuer:        strings.TrimSpace(leaf.Issuer.CommonName),
		Serial:        leaf.SerialNumber.String(),
		CertPEM:       normalized,
		ChainPEM:      chain,
		HasKey:        true,
		IssuedAt:      leaf.NotBefore.UTC(),
		CreatedAt:     now.UTC(),
		UpdatedAt:     now.UTC(),
	}
	return c, keyPEM, nil
}

// NewPending 建一张待签证书记录(ACME 来源)。私钥此刻还不存在,签发成功才回填;
// 返回的 keyPEM 为空串,由调用方与 acme.go 的签发流程约定。
func NewPending(in CreateInput, now time.Time) (*Certificate, error) {
	domain := strings.ToLower(strings.TrimSpace(in.Domain))
	if !ValidDomain(domain) {
		return nil, ErrInvalidDomain
	}
	caID, ok := LookupCA(in.CA)
	if !ok {
		return nil, ErrNoCA
	}
	if strings.TrimSpace(in.DNSProviderID) == "" {
		return nil, ErrNoDNSProvider
	}
	sans := normalizeDomains(in.SANs)
	if len(sans) == 0 {
		sans = []string{domain}
	} else if !containsDomain(sans, domain) {
		// 主名一定在 SAN 里:LE 签的是 SAN 列表,主名漏了就等于签了一张不含它的证书。
		sans = append([]string{domain}, sans...)
	}
	return &Certificate{
		Domain:        domain,
		SANs:          sans,
		Source:        SourceACME,
		Status:        StatusPending,
		CA:            caID.ID,
		DNSProviderID: strings.TrimSpace(in.DNSProviderID),
		AutoRenew:     in.AutoRenew,
		CreatedAt:     now.UTC(),
		UpdatedAt:     now.UTC(),
	}, nil
}

// normalizeDomains 去重、转小写、剔除非法域名,保持输入顺序(主名恒在首,由调用方保证)。
func normalizeDomains(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, d := range in {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" || seen[d] || !ValidDomain(d) {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

// containsDomain 报告清单里是否有该域名(大小写已归一)。
func containsDomain(list []string, domain string) bool {
	for _, d := range list {
		if d == domain {
			return true
		}
	}
	return false
}

// ValidateDeployTarget 校验落点入参:两条路径都必须是绝对路径且不能相同,
// post_command 完全不校验内容 —— 那是用户要在自己机器上跑的东西。
func ValidateDeployTarget(in DeployTargetInput) error {
	certPath := strings.TrimSpace(in.CertPath)
	keyPath := strings.TrimSpace(in.KeyPath)
	if certPath == "" || keyPath == "" || !strings.HasPrefix(certPath, "/") || !strings.HasPrefix(keyPath, "/") {
		return ErrEmptyPath
	}
	if certPath == keyPath {
		return ErrSamePath
	}
	return nil
}
