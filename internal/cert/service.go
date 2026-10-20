package cert

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// Bundle 是一张证书的完整材料(含**明文私钥**),只在两条路径上出现:
// 下载证书包(httpapi 直接写响应体)与发布到目标机(deploy 层落盘)。
// 它刻意不是 Service 的返回类型之一 —— 只有 Bundle()/PrivateKey() 显式要,才会解封。
type Bundle struct {
	Domain   string
	CertPEM  string
	ChainPEM string
	KeyPEM   string
}

// Fullchain 返回「叶子 + 中间链」拼接正文(nginx/caddy 的 fullchain.pem 就是这个)。
func (b Bundle) Fullchain() string {
	if b.ChainPEM == "" {
		return b.CertPEM
	}
	return b.CertPEM + b.ChainPEM
}

// Service 是证书仓库对外接口(httpapi 与到期扫描器消费)。
//
// 「存 / 导入」(Issue 之外)与「签」(Issue / RegisterAccount)都在这里;发布(Deploy)由
// deploy.go 补,所以清单里没有 deploy 方法。
type Service interface {
	// List 返回全部证书视图(到期升序;不含任何密钥材料)。
	List(ctx context.Context) ([]Certificate, error)
	// Get 返回单张证书视图;不存在 → ErrNotFound。
	Get(ctx context.Context, id string) (*Certificate, error)
	// Import 校验并入库用户粘贴的已有证书;域名已有记录 → ErrDomainExists(就地替换请用 Replace)。
	Import(ctx context.Context, in ImportInput) (*Certificate, error)
	// Replace 用粘贴的正文**就地替换**一张已有证书(换证书是常态,不该先删再建丢掉落点配置)。
	Replace(ctx context.Context, id string, in ImportInput) (*Certificate, error)
	// Create 登记一张待签证书(source=acme):只落记录,真正签发由 Issue 负责。
	Create(ctx context.Context, in CreateInput) (*Certificate, error)
	// Issue 经 ACME DNS-01 签(或续)一张 acme 来源证书,成功后正文、私钥与到期时间就地更新。
	// 首次申请与到期续期是同一条路径(见 acme.go)。upload 来源 → ErrNotACME。
	Issue(ctx context.Context, id string) (*Certificate, error)
	// Delete 删除证书(其发布落点随行级联删除)。
	Delete(ctx context.Context, id string) error
	// SetAutoRenew 打开/关闭这张证书的自动续期。
	SetAutoRenew(ctx context.Context, id string, on bool) (*Certificate, error)
	// Bundle 解封私钥,返回可直接下载/落盘的证书包。无证书正文或无私钥 → 相应错误。
	Bundle(ctx context.Context, id string) (*Bundle, error)

	// Targets 返回一张证书的全部发布落点(创建序)。
	Targets(ctx context.Context, certID string) ([]DeployTarget, error)
	// AddTarget 为一张证书新增一个发布落点。
	AddTarget(ctx context.Context, certID string, in DeployTargetInput) (*DeployTarget, error)
	// UpdateTarget 修改一个落点配置(保留最近一次发布结果)。
	UpdateTarget(ctx context.Context, id string, in DeployTargetInput) (*DeployTarget, error)
	// DeleteTarget 删除一个落点配置。
	DeleteTarget(ctx context.Context, id string) error

	// Accounts 返回已注册的 ACME 账户视图(不含账户私钥)。
	Accounts(ctx context.Context) ([]Account, error)
	// RegisterAccount 注册(或就地更新)一个 CA 的 ACME 账户:账户密钥首次生成后长期复用。
	RegisterAccount(ctx context.Context, in AccountInput) (*Account, error)
}

// service 是 store + vault 密封能力支撑的实现。
type service struct {
	store *Store
	seal  Seal
	now   func() time.Time // 注入便于单测固定「当前时间」(状态推导与续期窗口都吃它)

	// —— 签发路径的注入点(acme.go;零值即「没接上」,Issue 会明确报错而不是卡住)——
	txt               TXTWriter                                                // 写/删 DNS-01 的 TXT 记录(dnsprovider.Service)
	httpClient        *http.Client                                             // 访问 CA 的客户端(nil = http.DefaultClient)
	lookupTXT         func(ctx context.Context, name string) ([]string, error) // 解析 TXT 判断记录是否已生效
	propagateTimeout  time.Duration                                            // 等记录可见的上限(0 = 不等)
	propagateInterval time.Duration                                            // 轮询间隔
	directory         func(ca CA) string                                       // CA → directory URL(测试里换成假 ACME 服务)
}

// New 构造 Service。
//   - db:参数化 SQL 触库。
//   - seal:vault(密封/解封私钥);nil 时任何需要密钥材料的路径返回 ErrVaultUnconfigured。
//   - opts:签发路径的注入点(不接 TXTWriter 时 Issue 报 ErrTXTWriterUnconfigured)。
func New(db *sql.DB, seal Seal, opts ...Option) Service {
	s := &service{
		store:             NewStore(db),
		seal:              seal,
		now:               time.Now,
		lookupTXT:         net.DefaultResolver.LookupTXT,
		propagateTimeout:  2 * time.Minute,
		propagateInterval: 5 * time.Second,
		directory:         func(ca CA) string { return ca.DirectoryURL },
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// directoryURL 取某 CA 的 ACME directory 地址。
// 生产恒为 cas.go 里的预置值(用户不能自由填:每家的端点、限流、是否要 EAB 都不一样);
// 走这一层是为了让契约测试能把同一家指向假 ACME 服务。
func (s *service) directoryURL(ca CA) string {
	if s.directory == nil {
		return ca.DirectoryURL
	}
	return s.directory(ca)
}

func (s *service) List(ctx context.Context) ([]Certificate, error) {
	rows, err := s.store.listCerts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Certificate, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Certificate)
	}
	return out, nil
}

func (s *service) Get(ctx context.Context, id string) (*Certificate, error) {
	r, err := s.store.getCert(ctx, id)
	if err != nil {
		return nil, err
	}
	c := r.Certificate
	return &c, nil
}

// Import 走「解析校验 → 密封私钥 → 落库」。校验全在密封之前:正文不合法时既不落库也不留密文。
func (s *service) Import(ctx context.Context, in ImportInput) (*Certificate, error) {
	c, keyPEM, err := Import(in, s.now())
	if err != nil {
		return nil, err
	}
	if _, err := s.store.getCertByDomain(ctx, c.Domain); err == nil {
		return nil, ErrDomainExists
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	sealed, err := s.sealKey(keyPEM)
	if err != nil {
		return nil, err
	}
	r := &certRow{Certificate: *c, keySealed: sealed}
	if err := s.store.insertCert(ctx, r); err != nil {
		return nil, err
	}
	out := r.Certificate
	return &out, nil
}

// Replace 就地换正文:保留 id、创建时间与全部发布落点(落点表按 cert_id 关联,换证书不动它们),
// 只把证书材料与派生状态刷成新那份。落点上的 last_status 刻意不清:那是这台机器的历史。
func (s *service) Replace(ctx context.Context, id string, in ImportInput) (*Certificate, error) {
	old, err := s.store.getCert(ctx, id)
	if err != nil {
		return nil, err
	}
	// 域名由正文说了算:粘贴进来的证书签的是什么就是什么,用户填的只作兜底。
	in.Domain = firstNonEmpty(strings.TrimSpace(in.Domain), old.Domain)
	c, keyPEM, err := Import(in, s.now())
	if err != nil {
		return nil, err
	}
	if c.Domain != old.Domain {
		// 换到了别的域名:先确认那个域名没被别的证书占着,别撞唯一索引。
		if _, err := s.store.getCertByDomain(ctx, c.Domain); err == nil {
			return nil, ErrDomainExists
		} else if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	sealed, err := s.sealKey(keyPEM)
	if err != nil {
		return nil, err
	}
	// 来源随之改写:ACME 记录被手工正文替换后,平台再自动续期会把用户换的证书签掉 ——
	// 那比误签更糟。所以这里 source 落成 upload,续期开关同时失效(见 NeedsRenew)。
	c.Source = SourceUpload
	c.AutoRenew = false
	c.CreatedAt = old.CreatedAt
	r := &certRow{Certificate: *c, keySealed: sealed}
	r.ID = old.ID
	if err := s.store.updateCert(ctx, r); err != nil {
		return nil, err
	}
	out := r.Certificate
	return &out, nil
}

// Create 登记一条待签记录:域名合法性、CA 枚举、DNS 提供商与主机引用都在这一步校严,
// 免得排到签发时才发现配错了(签发失败的代价是 LE 的速率限制,不是重试一次)。
func (s *service) Create(ctx context.Context, in CreateInput) (*Certificate, error) {
	c, err := NewPending(in, s.now())
	if err != nil {
		return nil, err
	}
	if ok, err := s.store.providerExists(ctx, c.DNSProviderID); err != nil {
		return nil, err
	} else if !ok {
		return nil, ErrNoDNSProvider
	}
	if _, err := s.store.getCertByDomain(ctx, c.Domain); err == nil {
		return nil, ErrDomainExists
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	r := &certRow{Certificate: *c}
	if err := s.store.insertCert(ctx, r); err != nil {
		return nil, err
	}
	out := r.Certificate
	return &out, nil
}

func (s *service) Delete(ctx context.Context, id string) error { return s.store.delCert(ctx, id) }

func (s *service) SetAutoRenew(ctx context.Context, id string, on bool) (*Certificate, error) {
	r, err := s.store.getCert(ctx, id)
	if err != nil {
		return nil, err
	}
	// upload 来源没有「自动续期」可言:平台签不出来。这里明确拒绝,而不是让它开着却永不生效。
	if r.Source != SourceACME {
		return nil, ErrNotACME
	}
	r.AutoRenew = on
	r.UpdatedAt = s.now().UTC()
	if err := s.store.updateCert(ctx, r); err != nil {
		return nil, err
	}
	out := r.Certificate
	return &out, nil
}

// Bundle 解封私钥。只在这一个方法里出现明文,故也是唯一需要 seal 的读取路径。
func (s *service) Bundle(ctx context.Context, id string) (*Bundle, error) {
	r, err := s.store.getCert(ctx, id)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(r.CertPEM) == "" {
		return nil, ErrNoCertBlock
	}
	if len(r.keySealed) == 0 {
		return nil, ErrBadKeyPEM
	}
	keyPEM, err := s.openKey(r.keySealed)
	if err != nil {
		return nil, err
	}
	return &Bundle{Domain: r.Domain, CertPEM: r.CertPEM, ChainPEM: r.ChainPEM, KeyPEM: keyPEM}, nil
}

func (s *service) Targets(ctx context.Context, certID string) ([]DeployTarget, error) {
	rows, err := s.store.listTargets(ctx, certID)
	if err != nil {
		return nil, err
	}
	out := make([]DeployTarget, 0, len(rows))
	for _, t := range rows {
		out = append(out, *t)
	}
	return out, nil
}

// AddTarget 新增落点。校验三件事:路径形态、目标主机存在、证书存在(靠 cert_id 的外键兜)。
func (s *service) AddTarget(ctx context.Context, certID string, in DeployTargetInput) (*DeployTarget, error) {
	if _, err := s.store.getCert(ctx, certID); err != nil {
		return nil, err
	}
	if err := ValidateDeployTarget(in); err != nil {
		return nil, err
	}
	if err := s.checkServer(ctx, in.ServerID); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	t := &DeployTarget{
		CertID: certID, ServerID: strings.TrimSpace(in.ServerID),
		CertPath: strings.TrimSpace(in.CertPath), KeyPath: strings.TrimSpace(in.KeyPath),
		PostCommand: in.PostCommand, AutoDeploy: in.AutoDeploy,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.insertTarget(ctx, t); err != nil {
		return nil, err
	}
	return t, nil
}

func (s *service) UpdateTarget(ctx context.Context, id string, in DeployTargetInput) (*DeployTarget, error) {
	t, err := s.store.getTarget(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := ValidateDeployTarget(in); err != nil {
		return nil, err
	}
	if err := s.checkServer(ctx, in.ServerID); err != nil {
		return nil, err
	}
	t.ServerID = strings.TrimSpace(in.ServerID)
	t.CertPath = strings.TrimSpace(in.CertPath)
	t.KeyPath = strings.TrimSpace(in.KeyPath)
	t.PostCommand = in.PostCommand
	t.AutoDeploy = in.AutoDeploy
	t.UpdatedAt = s.now().UTC()
	if err := s.store.updateTarget(ctx, t); err != nil {
		return nil, err
	}
	return t, nil
}

func (s *service) DeleteTarget(ctx context.Context, id string) error {
	return s.store.delTarget(ctx, id)
}

func (s *service) Accounts(ctx context.Context) ([]Account, error) {
	rows, err := s.store.listAccounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Account, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Account)
	}
	return out, nil
}

// checkServer 校验落点引用的主机存在。server_id 刻意不建外键(与 proxy_routes 同手法:
// 删主机由上层收敛),所以这里必须自己挡一次,否则错拼的 id 要到发布时才炸。
func (s *service) checkServer(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return ErrServerNotFound
	}
	ok, err := s.store.serverExists(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrServerNotFound
	}
	return nil
}

// sealKey 把私钥明文封成密文。seal 未配置时明确报错:证书没私钥等于发不出去、也导不出来,
// 存一半(有正文没密钥)的记录只会把失败推迟到用户最需要它的时候。
func (s *service) sealKey(keyPEM string) ([]byte, error) {
	if s.seal == nil {
		return nil, ErrVaultUnconfigured
	}
	sealed, err := s.seal.SealSecret([]byte(keyPEM))
	if err != nil {
		return nil, fmt.Errorf("cert: seal private key: %w", err)
	}
	return sealed, nil
}

// openKey 解封私钥明文。错误体不带任何密文/明文片段。
func (s *service) openKey(sealed []byte) (string, error) {
	if s.seal == nil {
		return "", ErrVaultUnconfigured
	}
	plain, err := s.seal.OpenSecret(sealed)
	if err != nil {
		return "", fmt.Errorf("cert: open private key: %w", err)
	}
	return string(plain), nil
}

// firstNonEmpty 返回第一个非空串(用于「用户填的域名优先,否则取记录原域名」)。
func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return strings.TrimSpace(a)
	}
	return b
}

// newAccountKey 生成一把 ACME 账户密钥(P-256,与 lego/certbot 默认一致)。
// 账户密钥只在首次注册时生成、之后长期复用,故由 acme.go 在「库里还没有这一行」时调用它。
func newAccountKey() (crypto.Signer, error) {
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}
