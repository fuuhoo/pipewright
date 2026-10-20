package cert

// 本文件把「申请」这条线接起来:x/crypto/acme 的订单流程 + dnsprovider 的 TXT 能力 = 平台自签
// DNS-01 证书。
//
// 为什么不引 lego:lego 把「几十家 DNS 商各自怎么调 API」整个搬进来,连带一堆依赖与它自己的凭据
// 格式,而本平台已经有 dnsprovider 三家真实实现,需要的只是「写一条 TXT、删一条 TXT」两步。
// x/crypto/acme 本身就够薄(go.mod 里 x/crypto 早就在),包起来不到三百行,不多引一个模块。
//
// 加密纪律与 cert.go 同一条:账户私钥、证书私钥、EAB 对称密钥一律经 seal 封成密文才入库;
// 明文只活在函数栈上,错误文本里绝不含它们,也不含 DNS 提供商的 token。

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/acme"
)

// DNS-01 校验题的记录名前缀(RFC 8555 固定写法)。
const dns01Prefix = "_acme-challenge."

// userAgent 发给 CA 的身份标识:ACME 服务器普遍按 UA 做限流与排查,匿名请求不好沟通。
const userAgent = "pipewright-cert/1.0"

// 签发路径的错误。错误体一律人话,且绝不含密钥或 token 片段。
var (
	// ErrNoACMEAccount 表示该 CA 还没注册过账户(先去账户页填 EAB / 联系人)。
	ErrNoACMEAccount = errors.New("cert: ACME account is not registered for this CA")
	// ErrTXTWriterUnconfigured 表示平台没接上 DNS TXT 写入能力(装配缺 dnsprovider),
	// 此时签发根本做不了,必须明说而不是卡在网络超时上。
	ErrTXTWriterUnconfigured = errors.New("cert: DNS TXT writer is not configured")
	// ErrNoDNS01Challenge 表示 CA 给出的 authorization 里没有 dns-01 挑战项
	// (该 CA 只支持 http-01 之类,平台没有反代端口可验证)。
	ErrNoDNS01Challenge = errors.New("cert: CA does not offer a dns-01 challenge")
	// ErrIssueFailed 表示一次 ACME 下单/校验/出证失败(人话原因见 err 文本与证书的 last_error)。
	ErrIssueFailed = errors.New("cert: ACME issuance failed")
	// ErrRegisterFailed 表示 ACME 账户注册失败(EAB 填错、CA 不可达、条款没接受都归这里)。
	ErrRegisterFailed = errors.New("cert: ACME account registration failed")
	// ErrEABRequired 表示该 CA 需要 EAB 凭据而这里没给(ZeroSSL / GTS / SSL.com 三家)。
	ErrEABRequired = errors.New("cert: this CA requires EAB credentials")
	// ErrBadEABKey 表示 EAB 对称密钥不是合法 base64。各家控制台给的都是 base64 形态,
	// 解不开就直接拒绝:静默按原始字节用会让 CA 回一句 mac 校验失败,用户无从下手。
	ErrBadEABKey = errors.New("cert: EAB key must be base64 encoded")
)

// TXTWriter 是签发所需的 DNS TXT 读写能力(由 dnsprovider.Service 满足)。
//
// 为什么只声明这两个方法:cert 层不该看见 A 记录、区域校验、凭据这些 DNS 细节,
// 更不该拿到提供商的 token 明文。窄接口也让单测能塞一个记账用的假实现。
type TXTWriter interface {
	EnsureTXTRecord(ctx context.Context, providerID, name, value string) error
	DeleteTXTRecord(ctx context.Context, providerID, name string) error
}

// AccountInput 是注册/更新一个 CA 的 ACME 账户的入参。
type AccountInput struct {
	CA      string
	Contact string // 邮箱(LE 用它发到期与吊销通知);可空
	// EABKeyID / EABKey 仅需要 EAB 的 CA 用。Key 是 CA 控制台给的 base64 对称密钥。
	EABKeyID string
	EABKey   string
}

// Option 注入签发路径的外部能力。装配在 main.go 做一次,测试里塞假实现。
type Option func(*service)

// WithTXTWriter 接上 DNS TXT 写入(dnsprovider.Service)。不接则 Issue 直接报 ErrTXTWriterUnconfigured。
func WithTXTWriter(w TXTWriter) Option {
	return func(s *service) {
		s.txt = w
	}
}

// WithACMEHTTPClient 指定访问 CA 的 HTTP 客户端(测试指向假 ACME 服务;生产可给带超时的客户端)。
// 只影响证书/账户这条出网路径,不动平台其它请求。
func WithACMEHTTPClient(c *http.Client) Option {
	return func(s *service) {
		s.httpClient = c
	}
}

// WithTXTLookup 替换 TXT 解析实现(默认走本机递归解析器;测试用它模拟「记录已可见」)。
func WithTXTLookup(fn func(ctx context.Context, name string) ([]string, error)) Option {
	return func(s *service) {
		s.lookupTXT = fn
	}
}

// WithTXTPropagation 设定「写完 TXT 后等它可见」的超时与轮询间隔。
// 超时为 0 表示不做解析检查、写完直接让 CA 校验(某些环境里递归解析器不可达时用)。
func WithTXTPropagation(timeout, interval time.Duration) Option {
	return func(s *service) {
		s.propagateTimeout = timeout
		s.propagateInterval = interval
	}
}

// issueResult 是一次签发的产物(明文私钥只在这里存在到入库密封那一行)。
type issueResult struct {
	keyPEM   string
	algo     string
	certPEM  string
	chainPEM string
	leaf     *x509.Certificate
}

// RegisterAccount 注册(或就地更新)一个 CA 的 ACME 账户。
//
// 一个 CA 一辈子只有一个账户:账户密钥首次注册时生成、之后长期复用。换密钥在 LE 侧要走红簿
// (key-change)流程,收益只有密钥轮换,代价是既有证书与待完成订单全部作废 —— 本期不做。
// 重复调用是安全的:同一把账户密钥再注册,CA 返回「已存在」,我们改走 GET 把账户 URL 取回来。
func (s *service) RegisterAccount(ctx context.Context, in AccountInput) (*Account, error) {
	ca, ok := LookupCA(in.CA)
	if !ok {
		return nil, ErrNoCA
	}
	if s.seal == nil {
		return nil, ErrVaultUnconfigured
	}
	kid := strings.TrimSpace(in.EABKeyID)
	eabKey, err := decodeEABKey(in.EABKey)
	if err != nil {
		return nil, err
	}
	if ca.RequiresEAB && (kid == "" || len(eabKey) == 0) {
		return nil, ErrEABRequired
	}
	contact := strings.TrimSpace(in.Contact)
	if contact != "" && !strings.Contains(contact, "@") {
		return nil, fmt.Errorf("cert: contact must be an email address")
	}

	// 已有账户 → 复用那把账户密钥(绝不因为这次没带密钥就重新生成一把,那等于换了身份)。
	existing, err := s.store.getAccount(ctx, ca.ID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	var (
		key            crypto.Signer
		accountKeySeal []byte
		eabSeal        []byte
		id             = uuid.NewString()
		created        = s.now().UTC()
	)
	if existing != nil {
		id = existing.ID
		created = existing.CreatedAt
		accountKeySeal = existing.accountKeySealed
		key, err = s.openAccountKey(existing.accountKeySealed)
		if err != nil {
			return nil, err
		}
		// EAB:这次没给就沿用库里那份(注册过之后再改 EAB 等于换绑,CA 那边会拒绝)。
		if len(eabKey) == 0 {
			eabSeal = existing.eabKeySealed
			if kid == "" {
				kid = existing.EABKeyID
			}
		}
	} else {
		key, err = newAccountKey()
		if err != nil {
			return nil, fmt.Errorf("cert: generate account key: %w", err)
		}
		der, merr := x509.MarshalPKCS8PrivateKey(key)
		if merr != nil {
			return nil, fmt.Errorf("cert: marshal account key: %w", merr)
		}
		sealed, serr := s.seal.SealSecret(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
		if serr != nil {
			return nil, fmt.Errorf("cert: seal account key: %w", serr)
		}
		accountKeySeal = sealed
	}
	if len(eabKey) > 0 {
		sealed, serr := s.seal.SealSecret(eabKey)
		if serr != nil {
			return nil, fmt.Errorf("cert: seal EAB key: %w", serr)
		}
		eabSeal = sealed
	}

	client := s.newACMEClient(ca, key, "")
	want := &acme.Account{Contact: contactSlice(contact)}
	if len(eabKey) > 0 {
		want.ExternalAccountBinding = &acme.ExternalAccountBinding{KID: kid, Key: eabKey}
	}
	// prompt 恒为 true:用户在页面上从下拉里选中这家 CA 就是同意其条款的动作,
	// 签发流程里没法再问一次。它只在首次注册时被调用一次。
	acct, err := client.Register(ctx, want, func(string) bool { return true })
	if errors.Is(err, acme.ErrAccountAlreadyExists) {
		// 这把账户密钥在 CA 那边已注册(多半是上一版库里没记下账户 URL):取回身份继续用。
		acct, err = client.GetReg(ctx, "")
	}
	if err != nil {
		return nil, fmt.Errorf("%w:%s:%s", ErrRegisterFailed, ca.Name, humanErr(err))
	}
	if acct.URI == "" {
		return nil, fmt.Errorf("%w:%s 未返回账户地址", ErrRegisterFailed, ca.Name)
	}
	now := s.now().UTC()
	row := &accountRow{Account: Account{
		ID: id, CA: ca.ID, DirectoryURL: s.directoryURL(ca), Contact: contact,
		AccountURL: acct.URI, EABKeyID: kid, HasEABKey: len(eabSeal) > 0,
		RegisteredAt: now, CreatedAt: created, UpdatedAt: now,
	}, accountKeySealed: accountKeySeal, eabKeySealed: eabSeal}
	if err := s.store.putAccount(ctx, row); err != nil {
		return nil, err
	}
	out := row.Account
	return &out, nil
}

// Issue 经 DNS-01 为一张证书签(或续)出新的正文。首次签发与续期是同一条路径:
// 每次都重新下单、重新校验域名控制、重新生成叶子密钥(续期顺带换密钥是 certbot 的默认行为,
// 目标机上的文件本来就是整体替换,不换反而少一次密钥轮换)。
//
// 失败一定回写记录(status / last_error),页面与到期扫描都靠它说话。
func (s *service) Issue(ctx context.Context, id string) (*Certificate, error) {
	r, err := s.store.getCert(ctx, id)
	if err != nil {
		return nil, err
	}
	// upload 来源是用户手工粘进来的证书:平台既没有该 CA 的域名控制权证明,也不该把用户的证书签掉。
	if r.Source != SourceACME {
		return nil, ErrNotACME
	}
	res, err := s.issueOnce(ctx, r)
	if err != nil {
		s.markIssueFailure(ctx, r, err)
		return nil, err
	}
	sealed, err := s.sealKey(res.keyPEM)
	if err != nil {
		s.markIssueFailure(ctx, r, err)
		return nil, err
	}
	now := s.now().UTC()
	r.CertPEM = res.certPEM
	r.ChainPEM = res.chainPEM
	r.keySealed = sealed
	r.HasKey = true
	r.KeyAlgo = res.algo
	r.SANs = LeafDomains(res.leaf)
	r.NotBefore = res.leaf.NotBefore.UTC()
	r.NotAfter = res.leaf.NotAfter.UTC()
	r.Issuer = strings.TrimSpace(res.leaf.Issuer.CommonName)
	r.Serial = res.leaf.SerialNumber.String()
	r.Status = DeriveStatus(true, r.NotAfter, now)
	r.IssuedAt = now
	r.CheckedAt = now
	r.LastError = ""
	r.UpdatedAt = now
	if err := s.store.updateCert(ctx, r); err != nil {
		return nil, err
	}
	out := r.Certificate
	return &out, nil
}

// markIssueFailure 把失败原因回写进记录。
//
// 状态只在「手上没有可用正文」时落成 failed:续期失败不代表正在服务的那张证书失效了,
// 把它标成 failed 会让页面报警,而实际上一切正常 —— 那种假警报比不报更糟。
func (s *service) markIssueFailure(ctx context.Context, r *certRow, cause error) {
	r.LastError = humanErr(cause)
	r.CheckedAt = s.now().UTC()
	r.UpdatedAt = r.CheckedAt
	if strings.TrimSpace(r.CertPEM) == "" {
		r.Status = StatusFailed
	}
	_ = s.store.updateCert(ctx, r) // 回写本身失败不再往上抛:调用方拿到的已经是签发错误。
}

// issueOnce 跑完一次 ACME 订单:下单 → 逐条 authorization 做 DNS-01 → 出证 → 解析正文。
func (s *service) issueOnce(ctx context.Context, r *certRow) (*issueResult, error) {
	if s.txt == nil {
		return nil, ErrTXTWriterUnconfigured
	}
	if s.seal == nil {
		return nil, ErrVaultUnconfigured
	}
	providerID := strings.TrimSpace(r.DNSProviderID)
	if providerID == "" {
		return nil, ErrNoDNSProvider
	}
	ca, ok := LookupCA(r.CA)
	if !ok {
		return nil, ErrNoCA
	}
	client, err := s.accountClient(ctx, ca)
	if err != nil {
		return nil, err
	}

	domains := r.SANs
	if len(domains) == 0 {
		domains = []string{r.Domain}
	}
	order, err := client.AuthorizeOrder(ctx, acme.DomainIDs(domains...))
	if err != nil {
		return nil, fmt.Errorf("%w:下单失败:%s", ErrIssueFailed, humanErr(err))
	}

	// DNS-01 严格逐条做:写 TXT → 等可见 → Accept → 等判定。
	// 为什么不能「先把所有 TXT 都写好、再一起校验」:通配 *.example.com 与 example.com
	// 用的是**同一个**记录名(_acme-challenge.example.com),值却各自不同。两条都写下去
	// 等于后一条把前一条覆盖掉,CA 校第一条必然失败。
	var namesSeen []string
	defer func() { s.cleanupTXT(ctx, providerID, namesSeen) }()
	for _, authzURL := range order.AuthzURLs {
		authz, err := client.GetAuthorization(ctx, authzURL)
		if err != nil {
			return nil, fmt.Errorf("%w:取授权失败:%s", ErrIssueFailed, humanErr(err))
		}
		if authz.Status == acme.StatusValid {
			continue // CA 认为这条已经验过(重复下单时会遇到),不用再来一遍。
		}
		chal := dns01Challenge(authz)
		if chal == nil {
			return nil, fmt.Errorf("%w:%s", ErrNoDNS01Challenge, authz.Identifier.Value)
		}
		name := dns01RecordName(authz.Identifier.Value)
		value, err := client.DNS01ChallengeRecord(chal.Token)
		if err != nil {
			return nil, fmt.Errorf("%w:算校验值失败:%s", ErrIssueFailed, humanErr(err))
		}
		if err := s.txt.EnsureTXTRecord(ctx, providerID, name, value); err != nil {
			return nil, fmt.Errorf("%w:写 TXT 记录失败:%s", ErrIssueFailed, humanErr(err))
		}
		// 只把**写成功过**的记录名登记进清理清单:没写成就去删,可能删掉别人(另一次签发或
		// 运维自己)的 TXT;而真正需要清理的部分由下一次签发的 EnsureTXTRecord 收敛。
		// 通配与主名共用一个记录名,清单里不必出现两次。
		seen := false
		for _, n := range namesSeen {
			seen = seen || n == name
		}
		if !seen {
			namesSeen = append(namesSeen, name)
		}
		s.waitForTXT(ctx, name, value)
		if _, err := client.Accept(ctx, chal); err != nil {
			return nil, fmt.Errorf("%w:提交挑战失败:%s", ErrIssueFailed, humanErr(err))
		}
		if _, err := client.WaitAuthorization(ctx, authz.URI); err != nil {
			return nil, fmt.Errorf("%w:%s 域名校验未通过:%s", ErrIssueFailed, authz.Identifier.Value, humanErr(err))
		}
	}

	if _, err := client.WaitOrder(ctx, order.URI); err != nil {
		return nil, fmt.Errorf("%w:订单未就绪:%s", ErrIssueFailed, humanErr(err))
	}

	key, err := newLeafKey()
	if err != nil {
		return nil, fmt.Errorf("cert: generate leaf key: %w", err)
	}
	csr, err := buildCSR(key, domains)
	if err != nil {
		return nil, fmt.Errorf("cert: build CSR: %w", err)
	}
	der, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		return nil, fmt.Errorf("%w:出证失败:%s", ErrIssueFailed, humanErr(err))
	}
	certPEM, chainPEM, leaf, err := decodeBundle(der)
	if err != nil {
		return nil, err
	}
	for _, d := range domains {
		// CA 少签一个名字(它的策略与我们的预期不一致)必须当场发现:发出去就是「域名不符」,
		// 而那通常要到用户浏览器报错才看得见。
		if !CoversDomain(leaf, d) {
			return nil, fmt.Errorf("%w:%s 不在新证书覆盖范围内", ErrDomainMismatch, d)
		}
	}
	keyPEM, algo, _, err := ParseKey(marshalKeyPEM(key))
	if err != nil {
		return nil, err
	}
	return &issueResult{keyPEM: keyPEM, algo: algo, certPEM: certPEM, chainPEM: chainPEM, leaf: leaf}, nil
}

// accountClient 按 CA 取回账户密钥并造好可用的 ACME 客户端。
func (s *service) accountClient(ctx context.Context, ca CA) (*acme.Client, error) {
	row, err := s.store.getAccount(ctx, ca.ID)
	if errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("%w:%s", ErrNoACMEAccount, ca.Name)
	}
	if err != nil {
		return nil, err
	}
	key, err := s.openAccountKey(row.accountKeySealed)
	if err != nil {
		return nil, err
	}
	return s.newACMEClient(ca, key, row.AccountURL), nil
}

// openAccountKey 解封账户私钥明文(只在进程内出现,调用完即弃)。
func (s *service) openAccountKey(sealed []byte) (crypto.Signer, error) {
	if s.seal == nil {
		return nil, ErrVaultUnconfigured
	}
	if len(sealed) == 0 {
		return nil, ErrNoACMEAccount
	}
	plain, err := s.seal.OpenSecret(sealed)
	if err != nil {
		return nil, fmt.Errorf("cert: open account key: %w", err)
	}
	_, _, signer, err := ParseKey(string(plain))
	if err != nil {
		return nil, fmt.Errorf("cert: account key is unusable: %w", err)
	}
	return signer, nil
}

// newACMEClient 造 ACME 客户端。accountURL 非空时直接当 kid 用,省掉一次「取账户地址」的往返
// (LE 对未缓存账户的请求会多打一轮,速率限制是按注册数算的,少打一次少一次风险)。
func (s *service) newACMEClient(ca CA, key crypto.Signer, accountURL string) *acme.Client {
	c := &acme.Client{Key: key, DirectoryURL: s.directoryURL(ca), UserAgent: userAgent}
	if s.httpClient != nil {
		c.HTTPClient = s.httpClient
	}
	if accountURL != "" {
		c.KID = acme.KeyID(accountURL)
	}
	return c
}

// waitForTXT 轮询记录直到看见目标值,返回是否等到。
//
// 超时**不**判失败:这里的解析走本机递归解析器,它可能缓存了旧答案、也可能被网络策略挡住,
// 而 CA 用的是权威 NS 查询。把它当硬门槛会让平台在某些机器上永远签不出证书,而实际那些
// CA 完全看得见记录。所以「没等到」只是少了一道提前止损,后面照样让 CA 去验。
func (s *service) waitForTXT(ctx context.Context, name, want string) bool {
	if s.lookupTXT == nil || s.propagateTimeout <= 0 {
		return false
	}
	deadline := time.Now().Add(s.propagateTimeout)
	interval := s.propagateInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	for {
		records, err := s.lookupTXT(ctx, name)
		if err == nil {
			for _, rec := range records {
				if strings.Trim(strings.TrimSpace(rec), `"`) == want {
					return true
				}
			}
		}
		if !time.Now().Before(deadline) {
			return false
		}
		t := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return false
		case <-t.C:
		}
	}
}

// cleanupTXT 删除本次签发写过的全部 TXT 记录。
//
// 用独立 context:签发常因上下文取消(超时/用户中断)而失败,而残留的旧 TXT 记录会让
// **下一次**签发直接失败(DNS-01 同名只认一条),清理必须在最该清理的那条路径上也执行。
// 删失败只能忽略:那是 DNS 侧的临时故障,重签时 EnsureTXTRecord 的「改第一条 + 删其余」会再收敛一次。
func (s *service) cleanupTXT(ctx context.Context, providerID string, names []string) {
	if s.txt == nil || len(names) == 0 {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	for _, n := range names {
		_ = s.txt.DeleteTXTRecord(cleanupCtx, providerID, n)
	}
}

// dns01RecordName 把待验证域名换成校验题的记录名。
// 通配 "*.example.com" 去掉 "*." 才是正确形态(RFC 8555 通配验证题落在 apex 的 _acme-challenge 上)。
func dns01RecordName(domain string) string {
	d := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	d = strings.TrimPrefix(d, "*.")
	return dns01Prefix + d
}

// dns01Challenge 从 authorization 里挑出 dns-01 挑战项;没有 → nil。
func dns01Challenge(a *acme.Authorization) *acme.Challenge {
	for _, c := range a.Challenges {
		if c.Type == "dns-01" {
			return c
		}
	}
	return nil
}

// newLeafKey 生成叶子密钥。统一用 ECDSA P-256:证书链更小、签发更快,
// nginx/caddy/java truststore 这些年都支持(要 RSA 的场景走「导入已有证书」)。
func newLeafKey() (crypto.Signer, error) {
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}

// marshalKeyPEM 把私钥封成 PKCS#8 PEM(与入库正文同形)。
func marshalKeyPEM(key crypto.Signer) string {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return ""
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// buildCSR 生成证书请求。主名放 CN、全部域名放 SAN:现代 CA 与客户端只认 SAN,
// 但 nginx 之类会把 CN 显示在日志里,填上更好排查。
func buildCSR(key crypto.Signer, domains []string) ([]byte, error) {
	tmpl := &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: domains[0]},
		DNSNames: domains,
	}
	return x509.CreateCertificateRequest(rand.Reader, tmpl, key)
}

// decodeBundle 把 CA 返回的 DER 链拆成「叶子正文 + 中间链正文」。
// 链走 ParseChain 归一化:顺手逐块校验确为证书,CA 返回垃圾正文在这里就拦住。
func decodeBundle(der [][]byte) (certPEM, chainPEM string, leaf *x509.Certificate, err error) {
	if len(der) == 0 {
		return "", "", nil, ErrNoCertBlock
	}
	leaf, err = x509.ParseCertificate(der[0])
	if err != nil {
		return "", "", nil, fmt.Errorf("cert: CA returned an unparsable certificate: %w", err)
	}
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der[0]}))
	var chain strings.Builder
	for _, c := range der[1:] {
		chain.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c}))
	}
	chainPEM, err = ParseChain(chain.String())
	if err != nil {
		return "", "", nil, err
	}
	return certPEM, chainPEM, leaf, nil
}

// decodeEABKey 把控制台给的 EAB 对称密钥解成字节。
// 三种 base64 变体都试:ZeroSSL 给 base64url、SSL.com 给带填充的 base64、GTS 给的常不带填充。
func decodeEABKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, ErrBadEABKey
}

// contactSlice 把邮箱换成 ACME 要的 ["mailto:..."] 形态;空串 → nil(注册时不带联系人)。
func contactSlice(email string) []string {
	if email == "" {
		return nil
	}
	return []string{"mailto:" + email}
}

// humanErr 把错误压成一行、限长的人话文本,供 last_error 入库与页面展示。
// 压掉换行是为了审计日志一行一条;限长是因为 CA 的 problem document 偶尔带一大段说明。
func humanErr(err error) string {
	if err == nil {
		return ""
	}
	s := strings.Join(strings.Fields(err.Error()), " ")
	const maxRunes = 300
	if r := []rune(s); len(r) > maxRunes {
		return string(r[:maxRunes]) + "…"
	}
	return s
}
