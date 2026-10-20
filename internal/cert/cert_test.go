package cert

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/storetest"
)

// fakeSeal 是 vault 的替身:密文 = 前缀 + base64(明文)。
// 用它不是为了验密码学,而是为了在测试里**看得出**私钥到底有没有以密文形态入库
// (base64 后的正文里不再出现 "PRIVATE KEY" 这种明文特征,断言才有意义)。
type fakeSeal struct{}

const sealPrefix = "sealed:"

func (fakeSeal) SealSecret(p []byte) ([]byte, error) {
	return []byte(sealPrefix + base64.StdEncoding.EncodeToString(p)), nil
}

func (fakeSeal) OpenSecret(s []byte) ([]byte, error) {
	if !strings.HasPrefix(string(s), sealPrefix) {
		return nil, errors.New("fakeSeal: not sealed")
	}
	return base64.StdEncoding.DecodeString(strings.TrimPrefix(string(s), sealPrefix))
}

// nilSeal 模拟「master key 没配」的 vault:任何密封都失败。
type nilSeal struct{}

func (nilSeal) SealSecret([]byte) ([]byte, error) { return nil, ErrVaultUnconfigured }
func (nilSeal) OpenSecret([]byte) ([]byte, error) { return nil, ErrVaultUnconfigured }

var testNow = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

// newSvc 构造一个时间固定的 service(状态推导与续期窗口都吃 now,测试必须钉住它)。
func newSvc(t *testing.T, db *sql.DB, seal Seal) *service {
	t.Helper()
	s, ok := New(db, seal).(*service)
	if !ok {
		t.Fatal("New 应返回 *service")
	}
	s.now = func() time.Time { return testNow }
	return s
}

// newKey 生成一把测试私钥。
func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成私钥: %v", err)
	}
	return k
}

// selfCertPEM 自签一张含指定域名与有效期的证书,返回 PEM。
func selfCertPEM(t *testing.T, domains []string, notBefore, notAfter time.Time, key *ecdsa.PrivateKey, serial int64) string {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: domains[0], Organization: []string{"pipewright test"}},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		DNSNames:     domains,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("自签证书: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// keyPEMFor 把私钥编成指定形态的 PEM(pkcs8 / ec):两种形态都得被导入收下。
func keyPEMFor(t *testing.T, key *ecdsa.PrivateKey, form string) string {
	t.Helper()
	switch form {
	case "pkcs8":
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatalf("MarshalPKCS8: %v", err)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	case "ec":
		der, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			t.Fatalf("MarshalEC: %v", err)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
	default:
		t.Fatalf("未知私钥形态 %q", form)
		return ""
	}
}

// seedHost 插一条凭据 + 一台主机,返回 server id(落点表不建外键,但配置期要校验主机存在)。
func seedHost(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	now := testNow.Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO credentials (id, name, type, scope, ciphertext, masked_value, created_at, updated_at)
		 VALUES (?, 'c-test', 'ssh_key', '', X'00', '****', ?, ?)`, "cred-"+id, now, now); err != nil {
		t.Fatalf("插凭据: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO servers (id, name, host, port, user, credential_id, created_at, updated_at)
		 VALUES (?, ?, ?, 22, 'root', ?, ?, ?)`, id, "host-"+id, "10.0.0.8", "cred-"+id, now, now); err != nil {
		t.Fatalf("插主机: %v", err)
	}
}

// seedProvider 插一条 DNS 提供商,返回其 id。
func seedProvider(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	now := testNow.Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO dns_providers (id, type, name, credential_id, base_domain, created_at, updated_at)
		 VALUES (?, 'cloudflare', ?, 'cred-x', 'example.com', ?, ?)`, id, "dns-"+id, now, now); err != nil {
		t.Fatalf("插 DNS 提供商: %v", err)
	}
}

// —— 纯逻辑:域名与通配匹配 ——————————————————————————————————

func TestCoversDomain(t *testing.T) {
	cases := []struct {
		certDomain string // 证书里的名字
		domain     string // 要覆盖的名字
		want       bool
	}{
		{"example.com", "example.com", true},
		{"example.com", "www.example.com", false},
		{"*.example.com", "a.example.com", true},
		{"*.example.com", "example.com", false},
		{"*.example.com", "a.b.example.com", false},
		{"*.a.example.com", "b.a.example.com", true},
		{"EXAMPLE.COM", "example.com", true},
	}
	for _, c := range cases {
		tmpl := &x509.Certificate{DNSNames: []string{strings.ToLower(c.certDomain)}}
		if got := CoversDomain(tmpl, c.domain); got != c.want {
			t.Errorf("CoversDomain(cert=%q, domain=%q) = %v, 期望 %v", c.certDomain, c.domain, got, c.want)
		}
	}
	// 无 SAN 时退回 CN:老证书就是这么签的,不能一律判不覆盖。
	noSAN := &x509.Certificate{Subject: pkix.Name{CommonName: "legacy.example.com"}}
	if !CoversDomain(noSAN, "legacy.example.com") {
		t.Error("无 SAN 证书应按 CN 判覆盖")
	}
}

func TestDeriveStatusAndRenew(t *testing.T) {
	day := 24 * time.Hour
	cases := []struct {
		name     string
		src      string
		notAfter time.Time
		renew    bool
		wantSt   string
		wantRen  bool
	}{
		{"还有 90 天", SourceACME, testNow.Add(90 * day), true, StatusIssued, false},
		{"剩 29 天进窗口", SourceACME, testNow.Add(29 * day), true, StatusIssued, true},
		{"剩 31 天不进", SourceACME, testNow.Add(31 * day), true, StatusIssued, false},
		{"已过期", SourceACME, testNow.Add(-day), true, StatusExpired, true},
		{"过期但关了自动续期", SourceACME, testNow.Add(-day), false, StatusExpired, false},
		{"导入的证书平台续不了", SourceUpload, testNow.Add(3 * day), true, StatusIssued, false},
	}
	for _, c := range cases {
		leaf := &x509.Certificate{NotAfter: c.notAfter}
		if got := DeriveStatus(true, leaf.NotAfter, testNow); got != c.wantSt {
			t.Errorf("%s: 状态 = %s, 期望 %s", c.name, got, c.wantSt)
		}
		cert := &Certificate{Source: c.src, NotAfter: c.notAfter, AutoRenew: c.renew}
		if got := NeedsRenew(cert, testNow); got != c.wantRen {
			t.Errorf("%s: NeedsRenew = %v, 期望 %v", c.name, got, c.wantRen)
		}
	}
	// 没有正文(待签)只能是 pending,不能假装在有效期里。
	if got := DeriveStatus(false, time.Time{}, testNow); got != StatusPending {
		t.Errorf("无正文状态 = %s, 期望 %s", got, StatusPending)
	}
	// DaysLeft 的取整口径:剩 1 小时算 1 天,过期 1 天算 -1 天(页面据此显示「今天到期」/「已过期 N 天」)。
	if got := (&Certificate{NotAfter: testNow.Add(time.Hour)}).DaysLeft(testNow); got != 1 {
		t.Errorf("DaysLeft(剩 1h) = %d, 期望 1", got)
	}
	if got := (&Certificate{NotAfter: testNow.Add(-25 * time.Hour)}).DaysLeft(testNow); got != -2 {
		t.Errorf("DaysLeft(过期 25h) = %d, 期望 -2", got)
	}
}

// —— 导入:解析、校验、密封 ————————————————————————————

func TestImportValid(t *testing.T) {
	st := storetest.Open(t)
	svc := newSvc(t, st.DB, fakeSeal{})
	key := newKey(t)
	certPEM := selfCertPEM(t, []string{"shop.example.com", "www.shop.example.com"},
		testNow.AddDate(0, 0, -1), testNow.AddDate(0, 0, 89), key, 1001)

	c, err := svc.Import(context.Background(), ImportInput{
		Domain: "SHOP.example.com", CertPEM: certPEM, KeyPEM: keyPEMFor(t, key, "pkcs8"),
	})
	if err != nil {
		t.Fatalf("导入合法证书失败: %v", err)
	}
	if c.Domain != "shop.example.com" {
		t.Errorf("域名应归一为小写,实得 %q", c.Domain)
	}
	if c.Status != StatusIssued || c.Source != SourceUpload {
		t.Errorf("状态/来源 = %s/%s, 期望 %s/%s", c.Status, c.Source, StatusIssued, SourceUpload)
	}
	if !c.HasKey || len(c.SANs) != 2 {
		t.Errorf("HasKey/SAN 解析异常: %+v (sans=%v)", c.HasKey, c.SANs)
	}
	if c.KeyAlgo != "ECDSA-P256" || c.Serial != "1001" {
		t.Errorf("算法/序列号 = %q/%q, 期望 ECDSA-P256/1001", c.KeyAlgo, c.Serial)
	}
	if c.NotAfter.Sub(c.NotBefore) < 89*24*time.Hour {
		t.Errorf("有效期解析异常: %s → %s", c.NotBefore, c.NotAfter)
	}

	// 私钥必须以密文形态躺在库里:明文出现即 AC-SEC-01 破防。
	var raw string
	if err := st.DB.QueryRow(`SELECT key_sealed FROM certificates WHERE id = ?`, c.ID).Scan(&raw); err != nil {
		t.Fatalf("读回 key_sealed: %v", err)
	}
	if !strings.HasPrefix(raw, sealPrefix) || strings.Contains(raw[len(sealPrefix):], "PRIVATE KEY") {
		t.Errorf("key_sealed 应是密文,实得前缀 %q", firstChars(raw, 24))
	}
	// 证书正文是公开材料,刻意明文,便于备份/对账直接读到期时间。
	var pemCol string
	if err := st.DB.QueryRow(`SELECT cert_pem FROM certificates WHERE id = ?`, c.ID).Scan(&pemCol); err != nil {
		t.Fatalf("读回 cert_pem: %v", err)
	}
	if !strings.Contains(pemCol, "BEGIN CERTIFICATE") {
		t.Error("cert_pem 应存明文正文")
	}

	// Bundle 是唯一会吐出明文私钥的路径,解封结果必须与原始私钥等价。
	b, err := svc.Bundle(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("取证书包: %v", err)
	}
	if _, _, signer, err := ParseKey(b.KeyPEM); err != nil || !KeyMatchesCert(mustParseLeaf(t, b.CertPEM), signer) {
		t.Errorf("导出的私钥与证书不匹配: err=%v", err)
	}
	if b.Fullchain() != b.CertPEM {
		t.Error("无链时 Fullchain 应等于证书正文本身")
	}
}

func TestImportRejects(t *testing.T) {
	st := storetest.Open(t)
	svc := newSvc(t, st.DB, fakeSeal{})
	key := newKey(t)
	other := newKey(t)
	valid := selfCertPEM(t, []string{"api.example.com"}, testNow.AddDate(0, 0, -1), testNow.AddDate(0, 0, 89), key, 2001)

	cases := []struct {
		name string
		in   ImportInput
		want error
	}{
		{"正文为空", ImportInput{Domain: "api.example.com", CertPEM: "", KeyPEM: "x"}, ErrEmptyPEM},
		{"私钥为空", ImportInput{Domain: "api.example.com", CertPEM: valid, KeyPEM: "  "}, ErrEmptyPEM},
		{"不是 PEM", ImportInput{Domain: "api.example.com", CertPEM: "hello", KeyPEM: keyPEMFor(t, key, "pkcs8")}, ErrNoCertBlock},
		{"私钥不是 PEM", ImportInput{Domain: "api.example.com", CertPEM: valid, KeyPEM: "garbage"}, ErrBadKeyPEM},
		{"私钥与证书不匹配", ImportInput{Domain: "api.example.com", CertPEM: valid, KeyPEM: keyPEMFor(t, other, "pkcs8")}, ErrKeyMismatch},
		{"证书不覆盖该域名", ImportInput{Domain: "other.example.com", CertPEM: valid, KeyPEM: keyPEMFor(t, key, "pkcs8")}, ErrDomainMismatch},
		{"域名形态非法", ImportInput{Domain: "api_ex ample", CertPEM: valid, KeyPEM: keyPEMFor(t, key, "pkcs8")}, ErrInvalidDomain},
		{"链里夹垃圾", ImportInput{Domain: "api.example.com", CertPEM: valid, KeyPEM: keyPEMFor(t, key, "pkcs8"), ChainPEM: "not a pem"}, ErrNoCertBlock},
	}
	for _, c := range cases {
		_, err := svc.Import(context.Background(), c.in)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: 错误 = %v, 期望 %v", c.name, err, c.want)
		}
	}
	// 反向确认:一次也没落库(校验全在插入之前)。
	var n int
	if err := st.DB.QueryRow(`SELECT COUNT(1) FROM certificates`).Scan(&n); err != nil || n != 0 {
		t.Errorf("非法导入不该落库,实得 %d 行(err=%v)", n, err)
	}
}

func TestImportKeyFormsAndChain(t *testing.T) {
	st := storetest.Open(t)
	svc := newSvc(t, st.DB, fakeSeal{})
	key := newKey(t)
	ctx := context.Background()

	// SEC1("EC PRIVATE KEY")与 PKCS#8 都得收下,入库统一编成 PKCS#8。
	certPEM := selfCertPEM(t, []string{"ec.example.com"}, testNow.AddDate(0, 0, -1), testNow.AddDate(0, 0, 89), key, 3001)
	c, err := svc.Import(ctx, ImportInput{CertPEM: certPEM, KeyPEM: keyPEMFor(t, key, "ec")})
	if err != nil {
		t.Fatalf("导入 SEC1 私钥失败: %v", err)
	}
	if c.Domain != "ec.example.com" {
		t.Errorf("未填域名时应取 SAN 首项兜底,实得 %q", c.Domain)
	}
	b, err := svc.Bundle(ctx, c.ID)
	if err != nil {
		t.Fatalf("取包: %v", err)
	}
	if !strings.Contains(b.KeyPEM, "BEGIN PRIVATE KEY") {
		t.Errorf("私钥应统一编成 PKCS#8,实得 %s", firstChars(b.KeyPEM, 40))
	}

	// 链带进去之后,Fullchain 是叶子 + 链两段拼接(nginx 的 fullchain.pem 就是这个)。
	leafPEM := selfCertPEM(t, []string{"chain.example.com"}, testNow.AddDate(0, 0, -1), testNow.AddDate(0, 0, 89), key, 3002)
	chainPEM := selfCertPEM(t, []string{"ca.example.com"}, testNow.AddDate(-1, 0, 0), testNow.AddDate(9, 0, 0), newKey(t), 3003)
	c2, err := svc.Import(ctx, ImportInput{
		Domain: "chain.example.com", CertPEM: leafPEM, KeyPEM: keyPEMFor(t, key, "pkcs8"), ChainPEM: chainPEM,
	})
	if err != nil {
		t.Fatalf("带链导入失败: %v", err)
	}
	b2, err := svc.Bundle(ctx, c2.ID)
	if err != nil {
		t.Fatalf("取带链包: %v", err)
	}
	if got := strings.Count(b2.Fullchain(), "BEGIN CERTIFICATE"); got != 2 {
		t.Errorf("Fullchain 应含叶子 + 链共 2 块,实得 %d", got)
	}
}

func TestImportExpiredAndDuplicate(t *testing.T) {
	st := storetest.Open(t)
	svc := newSvc(t, st.DB, fakeSeal{})
	key := newKey(t)
	expired := selfCertPEM(t, []string{"old.example.com"}, testNow.AddDate(0, 0, -120), testNow.AddDate(0, 0, -30), key, 4001)

	c, err := svc.Import(context.Background(), ImportInput{CertPEM: expired, KeyPEM: keyPEMFor(t, key, "pkcs8")})
	if err != nil {
		t.Fatalf("过期证书也应收下(状态如实落成 expired): %v", err)
	}
	if c.Status != StatusExpired {
		t.Errorf("状态 = %s, 期望 %s", c.Status, StatusExpired)
	}
	// 同域名再来一次:唯一索引 → ErrDomainExists(攒历史行会让「这张发到了哪台」失去唯一答案)。
	if _, err := svc.Import(context.Background(), ImportInput{CertPEM: expired, KeyPEM: keyPEMFor(t, key, "pkcs8")}); !errors.Is(err, ErrDomainExists) {
		t.Errorf("重复域名 = %v, 期望 %v", err, ErrDomainExists)
	}
}

func TestImportWithoutVault(t *testing.T) {
	st := storetest.Open(t)
	svc := newSvc(t, st.DB, nilSeal{})
	key := newKey(t)
	certPEM := selfCertPEM(t, []string{"novault.example.com"}, testNow.AddDate(0, 0, -1), testNow.AddDate(0, 0, 89), key, 5001)
	_, err := svc.Import(context.Background(), ImportInput{CertPEM: certPEM, KeyPEM: keyPEMFor(t, key, "pkcs8")})
	if !errors.Is(err, ErrVaultUnconfigured) {
		t.Errorf("master key 缺失时应明确拒绝: %v", err)
	}
	var n int
	if err := st.DB.QueryRow(`SELECT COUNT(1) FROM certificates`).Scan(&n); err != nil || n != 0 {
		t.Errorf("密封失败不该留半条记录,实得 %d 行", n)
	}
}

// —— 就地替换:保留落点,改写来源 ——————————————————————————

func TestReplaceKeepsTargets(t *testing.T) {
	st := storetest.Open(t)
	svc := newSvc(t, st.DB, fakeSeal{})
	ctx := context.Background()
	seedHost(t, st.DB, "srv-1")

	oldKey := newKey(t)
	c, err := svc.Import(ctx, ImportInput{
		Domain:  "web.example.com",
		CertPEM: selfCertPEM(t, []string{"web.example.com"}, testNow.AddDate(0, 0, -80), testNow.AddDate(0, 0, 10), oldKey, 6001),
		KeyPEM:  keyPEMFor(t, oldKey, "pkcs8"),
	})
	if err != nil {
		t.Fatalf("首次导入: %v", err)
	}
	target, err := svc.AddTarget(ctx, c.ID, DeployTargetInput{
		ServerID: "srv-1", CertPath: "/etc/ssl/web.crt", KeyPath: "/etc/ssl/web.key",
		PostCommand: "nginx -s reload", AutoDeploy: true,
	})
	if err != nil {
		t.Fatalf("加落点: %v", err)
	}
	if err := svc.store.markTarget(ctx, target.ID, DeployOK, "", testNow); err != nil {
		t.Fatalf("置发布结果: %v", err)
	}

	newKey2 := newKey(t)
	replaced, err := svc.Replace(ctx, c.ID, ImportInput{
		CertPEM: selfCertPEM(t, []string{"web.example.com"}, testNow.AddDate(0, 0, -1), testNow.AddDate(0, 0, 89), newKey2, 6002),
		KeyPEM:  keyPEMFor(t, newKey2, "pkcs8"),
	})
	if err != nil {
		t.Fatalf("替换证书: %v", err)
	}
	if replaced.ID != c.ID || !replaced.CreatedAt.Equal(c.CreatedAt) {
		t.Error("替换应就地更新同一条记录、保留创建时间")
	}
	if replaced.Serial != "6002" || replaced.Status != StatusIssued {
		t.Errorf("替换后正文没刷新: serial=%s status=%s", replaced.Serial, replaced.Status)
	}
	// 手工正文替换掉 ACME 记录后不能再自动续:平台会把用户换的证书签掉。
	if replaced.Source != SourceUpload || replaced.AutoRenew {
		t.Errorf("替换后来源/续期 = %s/%v, 期望 upload/false", replaced.Source, replaced.AutoRenew)
	}

	targets, err := svc.Targets(ctx, c.ID)
	if err != nil {
		t.Fatalf("读落点: %v", err)
	}
	if len(targets) != 1 || targets[0].LastStatus != "success" || targets[0].PostCommand != "nginx -s reload" {
		t.Errorf("落点配置与最近结果应原样留着,实得 %+v", targets)
	}
	// 新私钥确实换了:旧私钥解不开新证书。
	b, err := svc.Bundle(ctx, c.ID)
	if err != nil {
		t.Fatalf("取包: %v", err)
	}
	if _, _, signer, _ := ParseKey(b.KeyPEM); !KeyMatchesCert(mustParseLeaf(t, b.CertPEM), signer) {
		t.Error("替换后的私钥与新证书不匹配")
	}
}

// —— 待签记录:配置期把错都挡住 ————————————————————————

func TestCreatePending(t *testing.T) {
	st := storetest.Open(t)
	svc := newSvc(t, st.DB, fakeSeal{})
	ctx := context.Background()
	seedProvider(t, st.DB, "dns-1")

	c, err := svc.Create(ctx, CreateInput{
		Domain: "*.example.com", SANs: []string{"*.example.com", "example.com", "*.example.com", ""},
		CA: CALetsEncryptStaging, DNSProviderID: "dns-1", AutoRenew: true,
	})
	if err != nil {
		t.Fatalf("登记待签证书: %v", err)
	}
	if c.Status != StatusPending || c.Source != SourceACME || c.CA != CALetsEncryptStaging {
		t.Errorf("记录初始态异常: %+v", c)
	}
	if len(c.SANs) != 2 {
		t.Errorf("SAN 应去重去空,实得 %v", c.SANs)
	}
	if c.HasKey {
		t.Error("待签记录还没有私钥")
	}
	// 待签记录没有正文:取包必须说清楚「还没有证书」,而不是返回一个空包。
	if _, err := svc.Bundle(ctx, c.ID); !errors.Is(err, ErrNoCertBlock) {
		t.Errorf("待签记录取包 = %v, 期望 %v", err, ErrNoCertBlock)
	}

	// upload 来源没有「自动续期」可言:开关必须拒,而不是让它开着却永不生效。
	key := newKey(t)
	uploaded, err := svc.Import(ctx, ImportInput{
		Domain:  "imp.example.com",
		CertPEM: selfCertPEM(t, []string{"imp.example.com"}, testNow.AddDate(0, 0, -1), testNow.AddDate(0, 0, 89), key, 7001),
		KeyPEM:  keyPEMFor(t, key, "pkcs8"),
	})
	if err != nil {
		t.Fatalf("导入: %v", err)
	}
	if _, err := svc.SetAutoRenew(ctx, uploaded.ID, true); !errors.Is(err, ErrNotACME) {
		t.Errorf("给导入证书开自动续期 = %v, 期望 %v", err, ErrNotACME)
	}
	// ACME 记录可关可开,且关完能再开回来。
	if got, err := svc.SetAutoRenew(ctx, c.ID, false); err != nil || got.AutoRenew {
		t.Errorf("关自动续期 = %v(err=%v), 期望 false", got.AutoRenew, err)
	}
	if got, err := svc.SetAutoRenew(ctx, c.ID, true); err != nil || !got.AutoRenew {
		t.Errorf("开自动续期 = %v(err=%v), 期望 true", got.AutoRenew, err)
	}
}

func TestCreateRejects(t *testing.T) {
	st := storetest.Open(t)
	svc := newSvc(t, st.DB, fakeSeal{})
	ctx := context.Background()
	seedProvider(t, st.DB, "dns-ok")

	cases := []struct {
		name string
		in   CreateInput
		want error
	}{
		{"域名非法", CreateInput{Domain: "not a domain", CA: CALetsEncrypt, DNSProviderID: "dns-ok"}, ErrInvalidDomain},
		{"CA 未知", CreateInput{Domain: "a.example.com", CA: "buysign", DNSProviderID: "dns-ok"}, ErrNoCA},
		{"CA 为空", CreateInput{Domain: "a.example.com", DNSProviderID: "dns-ok"}, ErrNoCA},
		{"没选 DNS 提供商", CreateInput{Domain: "a.example.com", CA: CALetsEncrypt}, ErrNoDNSProvider},
		{"提供商不存在", CreateInput{Domain: "a.example.com", CA: CALetsEncrypt, DNSProviderID: "dns-missing"}, ErrNoDNSProvider},
	}
	for _, c := range cases {
		if _, err := svc.Create(ctx, c.in); !errors.Is(err, c.want) {
			t.Errorf("%s: = %v, 期望 %v", c.name, err, c.want)
		}
	}
	// 同域名登记两次:第二次必须撞唯一。
	if _, err := svc.Create(ctx, CreateInput{Domain: "dup.example.com", CA: CALetsEncrypt, DNSProviderID: "dns-ok"}); err != nil {
		t.Fatalf("首次登记: %v", err)
	}
	if _, err := svc.Create(ctx, CreateInput{Domain: "dup.example.com", CA: CALetsEncrypt, DNSProviderID: "dns-ok"}); !errors.Is(err, ErrDomainExists) {
		t.Errorf("重复登记 = %v, 期望 %v", err, ErrDomainExists)
	}
}

// —— 发布落点 ————————————————————————————————————————————

func TestDeployTargets(t *testing.T) {
	st := storetest.Open(t)
	svc := newSvc(t, st.DB, fakeSeal{})
	ctx := context.Background()
	seedHost(t, st.DB, "srv-a")
	seedHost(t, st.DB, "srv-b")

	key := newKey(t)
	c, err := svc.Import(ctx, ImportInput{
		Domain:  "two.example.com",
		CertPEM: selfCertPEM(t, []string{"two.example.com"}, testNow.AddDate(0, 0, -1), testNow.AddDate(0, 0, 89), key, 8001),
		KeyPEM:  keyPEMFor(t, key, "pkcs8"),
	})
	if err != nil {
		t.Fatalf("导入: %v", err)
	}

	// post_command 留空是完全合法的:平台绝不代替用户发明命令。auto_deploy 原样透传,不替用户猜。
	first, err := svc.AddTarget(ctx, c.ID, DeployTargetInput{
		ServerID: "srv-a", CertPath: "/etc/ssl/a.crt", KeyPath: "/etc/ssl/a.key", AutoDeploy: true,
	})
	if err != nil {
		t.Fatalf("加落点(无发布后指令): %v", err)
	}
	if first.PostCommand != "" || first.LastStatus != DeployNone || !first.DeployedAt.IsZero() || !first.AutoDeploy {
		t.Errorf("新落点初值异常: %+v", first)
	}
	second, err := svc.AddTarget(ctx, c.ID, DeployTargetInput{ServerID: "srv-b", CertPath: "/etc/ssl/b.crt", KeyPath: "/etc/ssl/b.key"})
	if err != nil {
		t.Fatalf("加第二台: %v", err)
	}
	if second.AutoDeploy {
		t.Error("auto_deploy 关就是关,不该被默认成开(续期后会被静默铺到这台机器)")
	}
	list, err := svc.Targets(ctx, c.ID)
	if err != nil || len(list) != 2 {
		t.Fatalf("落点清单 = %d(err=%v), 期望 2", len(list), err)
	}

	// 改配置保留最近结果:否则用户挪一次路径就把失败记录抹平了。
	if err := svc.store.markTarget(ctx, first.ID, DeployFailed, "连不上", time.Time{}); err != nil {
		t.Fatalf("记失败: %v", err)
	}
	updated, err := svc.UpdateTarget(ctx, first.ID, DeployTargetInput{
		ServerID: "srv-a", CertPath: "/etc/ssl/new-a.crt", KeyPath: "/etc/ssl/new-a.key", PostCommand: "systemctl reload nginx", AutoDeploy: true,
	})
	if err != nil {
		t.Fatalf("改落点: %v", err)
	}
	if updated.CertPath != "/etc/ssl/new-a.crt" || updated.LastStatus != DeployFailed || updated.PostCommand != "systemctl reload nginx" {
		t.Errorf("更新后 = %+v,配置应刷新而最近结果应留着", updated)
	}

	// 成功后再失败:失败只改状态与原因,deployed_at 留着上一次真正铺成功的时间。
	deployedAt := testNow.Add(time.Hour)
	if err := svc.store.markTarget(ctx, first.ID, DeployOK, "", deployedAt); err != nil {
		t.Fatalf("记成功: %v", err)
	}
	if err := svc.store.markTarget(ctx, first.ID, DeployFailed, "reload 返回非零", time.Time{}); err != nil {
		t.Fatalf("再记失败: %v", err)
	}
	after, err := svc.store.getTarget(ctx, first.ID)
	if err != nil {
		t.Fatalf("读回: %v", err)
	}
	if after.LastStatus != DeployFailed || !after.DeployedAt.Equal(deployedAt.UTC()) {
		t.Errorf("失败不该覆盖上次成功时间: %+v (期望 %s)", after, deployedAt)
	}
	// 从未成功过的落点:失败之后 deployed_at 仍是空,页面才分得清「没发过」和「发过但这次坏了」。
	if err := svc.store.markTarget(ctx, second.ID, DeployFailed, "连不上", time.Time{}); err != nil {
		t.Fatalf("给第二台记失败: %v", err)
	}
	if after2, err := svc.store.getTarget(ctx, second.ID); err != nil || !after2.DeployedAt.IsZero() {
		t.Errorf("未成功过的落点 deployed_at 应为空: %+v (err=%v)", after2, err)
	}

	// 路径校验:相对路径、空、两条一样,都在配置期拒掉。
	for _, bad := range []DeployTargetInput{
		{ServerID: "srv-a", CertPath: "etc/a.crt", KeyPath: "/etc/a.key"},
		{ServerID: "srv-a", CertPath: "/etc/a.crt", KeyPath: ""},
		{ServerID: "srv-a", CertPath: "/etc/same", KeyPath: "/etc/same"},
	} {
		if _, err := svc.AddTarget(ctx, c.ID, bad); err == nil {
			t.Errorf("非法路径落点没被拒: %+v", bad)
		}
	}
	// 主机不存在必须现在炸,而不是等发布。
	if _, err := svc.AddTarget(ctx, c.ID, DeployTargetInput{ServerID: "ghost", CertPath: "/etc/g.crt", KeyPath: "/etc/g.key"}); !errors.Is(err, ErrServerNotFound) {
		t.Errorf("幽灵主机 = %v, 期望 %v", err, ErrServerNotFound)
	}
	// 证书不存在。
	if _, err := svc.AddTarget(ctx, "no-such-cert", DeployTargetInput{ServerID: "srv-a", CertPath: "/etc/x.crt", KeyPath: "/etc/x.key"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("幽灵证书 = %v, 期望 %v", err, ErrNotFound)
	}

	if err := svc.DeleteTarget(ctx, list[1].ID); err != nil {
		t.Fatalf("删落点: %v", err)
	}
	if err := svc.DeleteTarget(ctx, list[1].ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("重复删除 = %v, 期望 %v", err, ErrNotFound)
	}
	// 删证书级联删落点(用户不该看到指向已不存在证书的落点)。
	if err := svc.Delete(ctx, c.ID); err != nil {
		t.Fatalf("删证书: %v", err)
	}
	var left int
	if err := st.DB.QueryRow(`SELECT COUNT(1) FROM cert_deployments`).Scan(&left); err != nil || left != 0 {
		t.Errorf("删证书后落点应清零,实得 %d(err=%v)", left, err)
	}
	if _, err := svc.Get(ctx, c.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("删后读取 = %v, 期望 %v", err, ErrNotFound)
	}
}

// —— 清单顺序 & 账户 —— //

func TestListOrdersAndAccounts(t *testing.T) {
	st := storetest.Open(t)
	svc := newSvc(t, st.DB, fakeSeal{})
	ctx := context.Background()

	// 到期升序:最紧的排最前,页面与扫描器都据此优先处理。
	key := newKey(t)
	for _, d := range []struct {
		domain string
		left   time.Duration
		serial int64
	}{{"far.example.com", 80 * 24 * time.Hour, 9001}, {"soon.example.com", 3 * 24 * time.Hour, 9002}} {
		if _, err := svc.Import(ctx, ImportInput{
			Domain:  d.domain,
			CertPEM: selfCertPEM(t, []string{d.domain}, testNow, testNow.Add(d.left), key, d.serial),
			KeyPEM:  keyPEMFor(t, key, "pkcs8"),
		}); err != nil {
			t.Fatalf("导入 %s: %v", d.domain, err)
		}
	}
	list, err := svc.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("清单 = %d(err=%v)", len(list), err)
	}
	if list[0].Domain != "soon.example.com" {
		t.Errorf("应按到期升序,实得 %s 在前", list[0].Domain)
	}
	if list[0].DaysLeft(testNow) != 3 {
		t.Errorf("DaysLeft = %d, 期望 3", list[0].DaysLeft(testNow))
	}
	// 对外清单里绝不能有任何密钥材料(结构体压根没这字段,这里再钉一次 JSON 也不该带私钥)。
	for _, c := range list {
		if strings.Contains(fmt.Sprintf("%+v", c), "PRIVATE KEY") {
			t.Errorf("视图里出现了私钥正文: %+v", c)
		}
	}

	// 一个 CA 只留一行:第二次写必须覆盖同一行,而不是攒出两条账户。
	acct := &accountRow{Account: Account{
		ID: "acct-1", CA: CALetsEncrypt, DirectoryURL: "https://acme-v02.api.letsencrypt.org/directory",
		Contact: "ops@example.com", CreatedAt: testNow, UpdatedAt: testNow,
	}, accountKeySealed: []byte(sealPrefix + "account-key")}
	if err := svc.store.putAccount(ctx, acct); err != nil {
		t.Fatalf("首个账户入库: %v", err)
	}
	acct.AccountURL = "https://acme-v02.api.letsencrypt.org/acct/reg/123"
	acct.RegisteredAt = testNow.Add(time.Minute)
	if err := svc.store.putAccount(ctx, acct); err != nil {
		t.Fatalf("更新账户: %v", err)
	}
	var n int
	if err := st.DB.QueryRow(`SELECT COUNT(1) FROM acme_accounts WHERE ca = ?`, CALetsEncrypt).Scan(&n); err != nil {
		t.Fatalf("数账户: %v", err)
	}
	var acctURL, register string
	var eabKey sql.NullString
	if err := st.DB.QueryRow(`SELECT account_url, eab_key_sealed, registered_at FROM acme_accounts WHERE ca = ?`, CALetsEncrypt).
		Scan(&acctURL, &eabKey, &register); err != nil {
		t.Fatalf("读账户: %v", err)
	}
	if n != 1 {
		t.Errorf("同 CA 应只有一行,实得 %d", n)
	}
	if acctURL == "" || register == "" {
		t.Errorf("upsert 没把账户 URL 与注册时间写进去: url=%q registered=%q", acctURL, register)
	}
	// LE 不要 EAB:这一列必须是 NULL,不能是零长度密文。
	if eabKey.Valid {
		t.Error("LE 账户的 eab_key_sealed 应为 NULL")
	}

	// 第二家 CA(带 EAB)进来:清单按 CA 稳定排列,视图不含任何密钥。
	ssl := &accountRow{Account: Account{
		ID: "acct-2", CA: CASSLCom, DirectoryURL: "https://acme.ssl.com/sslcom-dv/directory",
		EABKeyID: "kid-1", HasEABKey: true, CreatedAt: testNow.Add(time.Second), UpdatedAt: testNow,
	}, accountKeySealed: []byte(sealPrefix + "k2"), eabKeySealed: []byte(sealPrefix + "eab")}
	if err := svc.store.putAccount(ctx, ssl); err != nil {
		t.Fatalf("第二家账户: %v", err)
	}
	accounts, err := svc.Accounts(ctx)
	if err != nil || len(accounts) != 2 {
		t.Fatalf("账户清单 = %d(err=%v)", len(accounts), err)
	}
	for _, a := range accounts {
		if strings.Contains(fmt.Sprintf("%+v", a), "account-key") || strings.Contains(fmt.Sprintf("%+v", a), "eab") {
			t.Errorf("账户视图里出现了密钥材料: %+v", a)
		}
	}
	if got, err := svc.store.getAccount(ctx, CALetsEncrypt); err != nil || got.CA != CALetsEncrypt || !strings.Contains(string(got.accountKeySealed), "account-key") {
		t.Errorf("按 CA 回读账户异常: %+v err=%v", got, err)
	}
	if _, err := svc.store.getAccount(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("读不存在的账户 = %v, 期望 %v", err, ErrNotFound)
	}
}

// —— CA 下拉:地址不能凭记忆写 ————————————————————————

func TestCARegistry(t *testing.T) {
	list := ListCAs()
	if len(list) != 5 {
		t.Fatalf("本期预置 5 家 CA,实得 %d", len(list))
	}
	want := map[string]struct {
		url    string
		eab    bool
		stages bool
	}{
		CALetsEncrypt:        {"https://acme-v02.api.letsencrypt.org/directory", false, false},
		CALetsEncryptStaging: {"https://acme-staging-v02.api.letsencrypt.org/directory", false, true},
		CAZeroSSL:            {"https://acme.zerossl.com/v2/DV90", true, false},
		CAGoogle:             {"https://dv.acme-v02.api.pki.goog/directory", true, false},
		CASSLCom:             {"https://acme.ssl.com/sslcom-dv/directory", true, false},
	}
	for _, id := range []string{CALetsEncrypt, CALetsEncryptStaging, CAZeroSSL, CAGoogle, CASSLCom} {
		ca, ok := LookupCA(id)
		if !ok {
			t.Fatalf("查不到 CA %s", id)
		}
		w := want[id]
		if ca.DirectoryURL != w.url {
			t.Errorf("%s directory = %q, 期望 %q", id, ca.DirectoryURL, w.url)
		}
		if ca.RequiresEAB != w.eab {
			t.Errorf("%s RequiresEAB = %v, 期望 %v", id, ca.RequiresEAB, w.eab)
		}
		if ca.Staging != w.stages {
			t.Errorf("%s Staging = %v, 期望 %v", id, ca.Staging, w.stages)
		}
		if !strings.HasPrefix(ca.DirectoryURL, "https://") {
			t.Errorf("%s 不是 https 端点: %q", id, ca.DirectoryURL)
		}
		if !ValidCA(id) {
			t.Errorf("ValidCA(%s) 应为真", id)
		}
	}
	// 枚举大小写不敏感(用户在 API 里传 Letz 之类不该算合法,但 LE 传大写应该认)。
	if !ValidCA("LetsEncrypt") || ValidCA("buysign") || ValidCA("") {
		t.Error("CA 枚举判定异常")
	}
	// ListCAs 返回副本:改它不该污染预置表。
	cp := ListCAs()
	cp[0].DirectoryURL = "https://evil.example"
	if ListCAs()[0].DirectoryURL == cp[0].DirectoryURL {
		t.Error("ListCAs 返回了内部切片")
	}
}

// —— 小工具 ————————————————————————————————————————————

func mustParseLeaf(t *testing.T, certPEM string) *x509.Certificate {
	t.Helper()
	_, leaf, err := ParseLeaf(certPEM)
	if err != nil {
		t.Fatalf("解析证书: %v", err)
	}
	return leaf
}

func firstChars(s string, n int) string {
	if len(s) < n {
		return s
	}
	return s[:n]
}
