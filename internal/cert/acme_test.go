package cert

// acme_test.go 用 httptest 起一台**假 ACME 服务器**验签发契约:它按 RFC 8555 的话术回话,
// 并真的去查我们的 TXT 记录桩里「此刻」有没有该看到的值 —— 于是 DNS-01 那条最容易写错的
// 时序(通配与主名共用一个记录名)由 CA 侧来判,而不是由我们的实现自证。
//
// 假 CA 不验 JWS 签名:验签是 x/crypto/acme 自己的事(它的测试矩阵覆盖),这里要验的是
// pipewright 这一层的流程、正文处理与密封纪律。真签名握手留给 #349 的真机验证。

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/storetest"
)

// —— TXT 记录桩(TXTWriter 的假实现)———————————————————————————

// txtRecorder 假装自己是 DNS 提供商:记下写过/删过的记录,并能在任意时刻被问「现在这条是什么」。
type txtRecorder struct {
	mu             sync.Mutex
	records        map[string]string
	ensures        []string // "name=value" 序列(顺序即签发时序)
	deletes        []string
	providers      []string
	ctxErrAtDelete error // 删除时上下文已取消 → 记录那个错误

	failEnsure error
	blockName  string // 该名字的写入被吞掉(模拟「写不进去」)
}

func newTXTRecorder() *txtRecorder {
	return &txtRecorder{records: map[string]string{}}
}

func (r *txtRecorder) EnsureTXTRecord(_ context.Context, providerID, name, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers = append(r.providers, providerID)
	if r.failEnsure != nil {
		return r.failEnsure
	}
	if r.blockName != "" && r.blockName == name {
		return nil // 声称写成功,实际没落:CA 侧就会看不到。
	}
	r.records[name] = value
	r.ensures = append(r.ensures, name+"="+value)
	return nil
}

func (r *txtRecorder) DeleteTXTRecord(ctx context.Context, _, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deletes = append(r.deletes, name)
	if ctx.Err() != nil {
		r.ctxErrAtDelete = ctx.Err()
	}
	delete(r.records, name)
	return nil
}

func (r *txtRecorder) current(name string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.records[name]
}

func (r *txtRecorder) ensureSeq() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ensures...)
}

func (r *txtRecorder) deleted() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.deletes...)
}

func (r *txtRecorder) usedProviders() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.providers...)
}

// —— 假 ACME 服务器 ————————————————————————————————

// fakeAuthz 是一个域名标识的挑战状态。
type fakeAuthz struct {
	id        string
	domain    string
	token     string
	status    string
	errDetail string
}

// fakeIdentifier 是订单标识的**线上形态**({"type":"dns","value":...})。
// 不能图省事回一列裸字符串:客户端解不出来会当成「跳过并重试」,于是 WaitOrder 无限轮询。
type fakeIdentifier struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// fakeCA 是一台最小可用的 RFC 8555 服务器。
type fakeCA struct {
	t   *testing.T
	srv *httptest.Server
	txt *txtRecorder

	accountAlreadyRegistered bool // 首次注册回 200(「这把密钥早注册过了」)
	orderStatus              int  // new-order 的响应码(0 → 201);非 201 用来造下单失败

	mu            sync.Mutex
	thumb         string   // 账户 JWK 的 RFC 7638 指纹(算 DNS-01 期望值要用)
	hadEAB        bool     // 注册请求里带了 externalAccountBinding
	termsSet      bool     // 请求里 termsOfServiceAgreed=true
	contact       []string // 请求里的联系人
	identifiers   []fakeIdentifier
	csrNames      []string
	csrCommonName string
	authz         map[string]*fakeAuthz
	finalized     bool
	certPEM       string
	notAfter      time.Time

	caKey  *ecdsa.PrivateKey
	caCert *x509.Certificate
	caDER  []byte
}

func newFakeCA(t *testing.T, txt *txtRecorder) *fakeCA {
	t.Helper()
	f := &fakeCA{t: t, txt: txt, authz: map[string]*fakeAuthz{}}
	f.caKey, f.caCert, f.caDER = fakeAuthority(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/directory", f.handleDirectory)
	mux.HandleFunc("/nonce", f.handleNonce)
	mux.HandleFunc("/acme/new-acct", f.handleNewAccount)
	mux.HandleFunc("/acme/new-order", f.handleNewOrder)
	mux.HandleFunc("/authz/", f.handleAuthz)
	mux.HandleFunc("/chal/", f.handleChallenge)
	mux.HandleFunc("/order/", f.handleOrder)
	mux.HandleFunc("/cert/", f.handleCert)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.t.Errorf("假 CA 收到未预期的请求 %s %s", r.Method, r.URL.Path)
		writeProblem(w, http.StatusNotFound, "urn:ietf:params:acme:error:malformed", "no such endpoint")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f.srv = srv
	return f
}

func (f *fakeCA) directoryURL() string { return f.srv.URL + "/directory" }

func (f *fakeCA) url(p string) string { return f.srv.URL + p }

// fakeAuthority 造一台自签 CA:签出的叶子证书 Issuer CN 会被平台记进 issuer 字段。
func fakeAuthority(t *testing.T) (*ecdsa.PrivateKey, *x509.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成 CA 私钥: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(900001),
		Subject:               pkix.Name{CommonName: "Pipewright Fake CA"},
		NotBefore:             testNow.AddDate(-1, 0, 0),
		NotAfter:              testNow.AddDate(5, 0, 0),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("自签 CA: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("解析 CA 证书: %v", err)
	}
	return key, cert, der
}

// —— JWS 拆解与 DNS-01 期望值 ——————————————————————————

type jwsEnvelope struct {
	Protected string `json:"protected"`
	Payload   string `json:"payload"`
}

// openJWS 解出 JWS 的 protected 头与请求正文,并顺手记住账户 JWK 指纹。
// 不验签名(理由见文件头)。
func (f *fakeCA) openJWS(r *http.Request) (map[string]any, []byte) {
	f.t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		f.t.Fatalf("读 JWS 请求体: %v", err)
	}
	var env jwsEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		f.t.Fatalf("请求体不是 JWS: %v (%s)", err, body)
	}
	head, err := base64.RawURLEncoding.DecodeString(env.Protected)
	if err != nil {
		f.t.Fatalf("解 protected 头: %v", err)
	}
	var h map[string]any
	if err := json.Unmarshal(head, &h); err != nil {
		f.t.Fatalf("解 protected JSON: %v", err)
	}
	if jwk, ok := h["jwk"].(map[string]any); ok {
		f.mu.Lock()
		f.thumb = jwkThumbprint(jwk)
		f.mu.Unlock()
	}
	payload, err := base64.RawURLEncoding.DecodeString(env.Payload)
	if err != nil {
		f.t.Fatalf("解 payload: %v", err)
	}
	return h, payload
}

// jwkThumbprint 按 RFC 7638 算 JWK 指纹:键按字典序排列的规范 JSON 再取 SHA-256。
func jwkThumbprint(jwk map[string]any) string {
	keys := make([]string, 0, len(jwk))
	for k := range jwk {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%q:%q", k, fmt.Sprint(jwk[k])))
	}
	sum := sha256.Sum256([]byte("{" + strings.Join(parts, ",") + "}"))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// expectedTXT 算某条挑战应当出现在 TXT 里的值(keyAuth = token.指纹,记录值 = 其 SHA-256 的 base64url)。
func (f *fakeCA) expectedTXT(token string) string {
	f.mu.Lock()
	thumb := f.thumb
	f.mu.Unlock()
	if thumb == "" {
		f.t.Errorf("挑战 %s 之前没有收到账户 JWK", token)
		return ""
	}
	sum := sha256.Sum256([]byte(token + "." + thumb))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// —— 端点处理 ————————————————————————————————

func (f *fakeCA) handleDirectory(w http.ResponseWriter, r *http.Request) {
	f.nonce(w)
	jsonResp(w, http.StatusOK, map[string]any{
		"newNonce":   f.url("/nonce"),
		"newAccount": f.url("/acme/new-acct"),
		"newOrder":   f.url("/acme/new-order"),
		"revokeCert": f.url("/acme/revoke-cert"),
		"keyChange":  f.url("/acme/key-change"),
		"meta":       map[string]any{"termsOfService": "https://ca.invalid/tos"},
	})
}

func (f *fakeCA) handleNonce(w http.ResponseWriter, _ *http.Request) {
	f.nonce(w)
	w.WriteHeader(http.StatusOK)
}

func (f *fakeCA) handleNewAccount(w http.ResponseWriter, r *http.Request) {
	_, payload := f.openJWS(r) // 顺手把账户 JWK 指纹记下来(算 DNS-01 期望值要用)
	var req struct {
		Contact                []string         `json:"contact"`
		TermsOfServiceAgreed   bool             `json:"termsOfServiceAgreed"`
		ExternalAccountBinding *json.RawMessage `json:"externalAccountBinding"`
		OnlyReturnExisting     bool             `json:"onlyReturnExisting"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		f.t.Errorf("解注册请求: %v", err)
	}
	f.mu.Lock()
	f.contact = req.Contact
	f.termsSet = f.termsSet || req.TermsOfServiceAgreed
	f.hadEAB = f.hadEAB || req.ExternalAccountBinding != nil
	f.mu.Unlock()

	status := http.StatusCreated
	if req.OnlyReturnExisting || f.accountAlreadyRegistered {
		// 「只回已有账户」的查询本来就是 200;密钥已注册过也回 200 → 客户端据此改走 GET。
		status = http.StatusOK
	}
	f.nonce(w)
	w.Header().Set("Location", f.url("/acct/1"))
	jsonResp(w, status, map[string]any{"status": "valid", "contact": req.Contact})
}

func (f *fakeCA) handleNewOrder(w http.ResponseWriter, r *http.Request) {
	_, payload := f.openJWS(r)
	if f.orderStatus != 0 && f.orderStatus != http.StatusCreated {
		f.nonce(w)
		writeProblem(w, f.orderStatus, "urn:ietf:params:acme:error:rejectedIdentifier", "这串标识被 CA 拒了")
		return
	}
	var req struct {
		Identifiers []struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"identifiers"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		f.t.Errorf("解订单请求: %v", err)
	}
	f.mu.Lock()
	f.authz = map[string]*fakeAuthz{}
	urls := make([]string, 0, len(req.Identifiers))
	ids := make([]fakeIdentifier, 0, len(req.Identifiers))
	for i, id := range req.Identifiers {
		a := &fakeAuthz{id: fmt.Sprintf("a%d", i+1), domain: id.Value, token: fmt.Sprintf("tok-%d-%d", i+1, time.Now().UnixNano()), status: "pending"}
		f.authz[a.id] = a
		ids = append(ids, fakeIdentifier{Type: id.Type, Value: id.Value})
		urls = append(urls, f.url("/authz/"+a.id))
	}
	f.identifiers = ids
	f.mu.Unlock()

	f.nonce(w)
	w.Header().Set("Location", f.url("/order/1"))
	jsonResp(w, http.StatusCreated, map[string]any{
		"status":         "pending",
		"identifiers":    ids,
		"authorizations": urls,
		"finalize":       f.url("/order/1/finalize"),
	})
}

func (f *fakeCA) handleAuthz(w http.ResponseWriter, r *http.Request) {
	f.openJWS(r)
	a := f.authzFrom(r.URL.Path, "/authz/")
	if a == nil {
		writeProblem(w, http.StatusNotFound, "urn:ietf:params:acme:error:malformed", "no such authz")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nonce(w)
	body := map[string]any{
		"identifier": map[string]any{"type": "dns", "value": a.domain},
		"status":     a.status,
		"challenges": []map[string]any{{
			"type":   "dns-01",
			"url":    f.url("/chal/" + a.id),
			"token":  a.token,
			"status": a.status,
		}},
	}
	if a.status == "invalid" {
		body["error"] = map[string]any{
			"type":   "urn:ietf:params:acme:error:dns",
			"detail": a.errDetail,
		}
	}
	jsonResp(w, http.StatusOK, body)
}

// handleChallenge 是 DNS-01 的「服务器去验证」这一步:真去 TXT 桩里查此刻的记录值对不对。
// 这一步是整个契约测试的要点 —— 它把「先写完所有 TXT 再一起校验」那种写法直接判死。
func (f *fakeCA) handleChallenge(w http.ResponseWriter, r *http.Request) {
	f.openJWS(r)
	a := f.authzFrom(r.URL.Path, "/chal/")
	if a == nil {
		writeProblem(w, http.StatusNotFound, "urn:ietf:params:acme:error:malformed", "no such challenge")
		return
	}
	name := challengeName(a.domain)
	want := f.expectedTXT(a.token)
	got := f.txt.current(name)

	f.mu.Lock()
	switch {
	case got == want:
		a.status = "valid"
	default:
		a.status = "invalid"
		a.errDetail = fmt.Sprintf("TXT %q 期望 %q 实得 %q", name, want, got)
	}
	status := a.status
	f.mu.Unlock()

	f.nonce(w)
	jsonResp(w, http.StatusOK, map[string]any{
		"type":   "dns-01",
		"url":    f.url("/chal/" + a.id),
		"token":  a.token,
		"status": status,
	})
}

func (f *fakeCA) handleOrder(w http.ResponseWriter, r *http.Request) {
	_, payload := f.openJWS(r)
	finalize := strings.HasSuffix(r.URL.Path, "/finalize")
	if finalize {
		var req struct {
			CSR string `json:"csr"`
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			f.t.Errorf("解 finalize 请求: %v", err)
		}
		der, err := base64.RawURLEncoding.DecodeString(req.CSR)
		if err != nil {
			f.t.Errorf("CSR 不是 base64url: %v", err)
		}
		csr, err := x509.ParseCertificateRequest(der)
		if err != nil {
			f.t.Errorf("CSR 解析失败: %v", err)
		}
		if err := csr.CheckSignature(); err != nil {
			f.t.Errorf("CSR 自签校验失败: %v", err)
		}
		f.mu.Lock()
		f.csrNames = csr.DNSNames
		f.csrCommonName = csr.Subject.CommonName
		f.notAfter = testNow.AddDate(0, 0, 90)
		f.certPEM = f.signLeaf(csr)
		f.finalized = true
		f.mu.Unlock()
	}

	f.mu.Lock()
	allValid := true
	urls := make([]string, 0, len(f.authz))
	for i := 1; i <= len(f.authz); i++ {
		a := f.authz[fmt.Sprintf("a%d", i)]
		urls = append(urls, f.url("/authz/"+a.id))
		if a.status != "valid" {
			allValid = false
		}
	}
	status := "pending"
	switch {
	case f.finalized:
		status = "valid"
	case allValid && len(f.authz) > 0:
		status = "ready"
	}
	body := map[string]any{
		"status":         status,
		"identifiers":    f.identifiers,
		"authorizations": urls,
		"finalize":       f.url("/order/1/finalize"),
	}
	if status == "valid" {
		body["certificate"] = f.url("/cert/1")
	}
	f.mu.Unlock()

	f.nonce(w)
	w.Header().Set("Location", f.url("/order/1"))
	jsonResp(w, http.StatusOK, body)
}

func (f *fakeCA) handleCert(w http.ResponseWriter, r *http.Request) {
	f.openJWS(r)
	f.mu.Lock()
	chain := f.certPEM + string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.caDER}))
	f.mu.Unlock()
	f.nonce(w)
	w.Header().Set("Content-Type", "application/pem-certificate-chain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(chain))
}

// signLeaf 按 CSR 里的名字签一张叶子证书(有效期固定在 testNow 附近,便于断言状态推导)。
// 调用方已持锁。
func (f *fakeCA) signLeaf(csr *x509.CertificateRequest) string {
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(4242),
		Subject:      csr.Subject,
		DNSNames:     csr.DNSNames,
		NotBefore:    testNow.Add(-time.Hour),
		NotAfter:     testNow.AddDate(0, 0, 90),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, f.caCert, csr.PublicKey, f.caKey)
	if err != nil {
		f.t.Fatalf("假 CA 签发失败: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func (f *fakeCA) authzFrom(path, prefix string) *fakeAuthz {
	id := strings.TrimPrefix(path, prefix)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.authz[id]
}

// challengeName 是假 CA 侧**独立**算出的 DNS-01 记录名。
// 不复用生产的 dns01RecordName:否则「前缀写错 / 忘去 *./ 大小写」这类错误会让测试与实现一起错,
// 而真正会发现它的只有真 CA。
func challengeName(domain string) string {
	d := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	d = strings.TrimPrefix(d, "*.")
	return "_acme-challenge." + d
}

func (f *fakeCA) nonce(w http.ResponseWriter) {
	w.Header().Set("Replay-Nonce", fmt.Sprintf("n-%d", time.Now().UnixNano()))
}

func jsonResp(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeProblem(w http.ResponseWriter, status int, typ, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": typ, "detail": detail})
}

// —— 装配辅助 ————————————————————————————————

// newAcmeSvc 组一台「接好 DNS 桩 + 指向假 CA」的 service:签发路径的两个外部依赖都换成假的。
func newAcmeSvc(t *testing.T, db *sql.DB, txt *txtRecorder, dirURL string) *service {
	t.Helper()
	s := newSvc(t, db, fakeSeal{})
	s.txt = txt
	s.directory = func(CA) string { return dirURL }
	s.lookupTXT = func(_ context.Context, name string) ([]string, error) {
		v := txt.current(name)
		if v == "" {
			return nil, nil
		}
		return []string{v}, nil
	}
	s.propagateTimeout = 2 * time.Second
	s.propagateInterval = 5 * time.Millisecond
	return s
}

// seedAndRegister 登记一条待签证书 + 注册对应 CA 的账户,把公共前置一次做完。
func seedAndRegister(t *testing.T, s *service, caID, domain string, sans []string) string {
	t.Helper()
	ctx := context.Background()
	if _, err := s.RegisterAccount(ctx, AccountInput{CA: caID, Contact: "ops@example.com"}); err != nil {
		t.Fatalf("注册 ACME 账户: %v", err)
	}
	c, err := s.Create(ctx, CreateInput{
		Domain: domain, SANs: sans, CA: caID, DNSProviderID: "dns-1", AutoRenew: true,
	})
	if err != nil {
		t.Fatalf("登记待签证书: %v", err)
	}
	return c.ID
}

// —— 用例 ——————————————————————————————————

func TestDNS01RecordName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"example.com", "_acme-challenge.example.com"},
		{"*.example.com", "_acme-challenge.example.com"}, // 通配的校验题落在 apex,不是 "*.example.com" 前面
		{"Shop.Example.COM.", "_acme-challenge.shop.example.com"},
		{"**bad", "_acme-challenge.**bad"}, // 归一化即可,合法性由上游 ValidDomain 把关
	}
	for _, c := range cases {
		if got := dns01RecordName(c.in); got != c.want {
			t.Errorf("dns01RecordName(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

func TestDecodeEABKey(t *testing.T) {
	raw := []byte{0x00, 0x01, 0xfe, 0xff}
	for _, form := range []string{
		base64.StdEncoding.EncodeToString(raw),
		base64.RawURLEncoding.EncodeToString(raw),
		base64.URLEncoding.EncodeToString(raw),
	} {
		got, err := decodeEABKey(form)
		if err != nil || string(got) != string(raw) {
			t.Errorf("解 %q = %x(err=%v), 期望 %x", form, got, err, raw)
		}
	}
	if got, err := decodeEABKey("  "); got != nil || err != nil {
		t.Errorf("空 EAB 应得 (nil, nil),实得 %v/%v", got, err)
	}
	// 解不开就明说:静默按原始字节用会让 CA 回一句 mac 不符,用户无从下手。
	if _, err := decodeEABKey("not base64!!"); !errors.Is(err, ErrBadEABKey) {
		t.Errorf("坏 EAB = %v, 期望 %v", err, ErrBadEABKey)
	}
}

func TestRegisterAccountSealsKey(t *testing.T) {
	st := storetest.Open(t)
	txt := newTXTRecorder()
	f := newFakeCA(t, txt)
	s := newAcmeSvc(t, st.DB, txt, f.directoryURL())
	ctx := context.Background()

	a, err := s.RegisterAccount(ctx, AccountInput{CA: CALetsEncryptStaging, Contact: "ops@example.com"})
	if err != nil {
		t.Fatalf("注册账户: %v", err)
	}
	if a.AccountURL != f.url("/acct/1") || a.CA != CALetsEncryptStaging {
		t.Errorf("账户视图异常: %+v", a)
	}
	if a.DirectoryURL != f.directoryURL() {
		t.Errorf("directory 地址没记进库: %q", a.DirectoryURL)
	}
	// 假 CA 侧看到的:条款同意 + 联系人形态。平台没弹问题,选 CA 就是同意。
	f.mu.Lock()
	terms, contact := f.termsSet, f.contact
	f.mu.Unlock()
	if !terms {
		t.Error("注册请求没带 termsOfServiceAgreed,LE 会直接拒")
	}
	if len(contact) != 1 || contact[0] != "mailto:ops@example.com" {
		t.Errorf("联系人 = %v, 期望 [mailto:ops@example.com]", contact)
	}

	// 账户私钥只以密文入库。
	var sealed []byte
	if err := st.DB.QueryRow(`SELECT account_key_sealed FROM acme_accounts WHERE ca = ?`, CALetsEncryptStaging).Scan(&sealed); err != nil {
		t.Fatalf("读账户密钥: %v", err)
	}
	if len(sealed) == 0 || !strings.HasPrefix(string(sealed), sealPrefix) {
		t.Errorf("账户密钥没密封,前缀 %q", firstBytes(sealed, 12))
	}
	if strings.Contains(string(sealed), "PRIVATE KEY") {
		t.Error("账户私钥明文入库了")
	}

	// 再注册一次(改联系人):密钥必须原样复用,否则等于在 CA 那边换了个身份。
	first := append([]byte(nil), sealed...)
	a2, err := s.RegisterAccount(ctx, AccountInput{CA: CALetsEncryptStaging, Contact: "new@example.com"})
	if err != nil {
		t.Fatalf("二次注册: %v", err)
	}
	if err := st.DB.QueryRow(`SELECT account_key_sealed FROM acme_accounts WHERE ca = ?`, CALetsEncryptStaging).Scan(&sealed); err != nil {
		t.Fatalf("二次读账户密钥: %v", err)
	}
	if string(sealed) != string(first) {
		t.Error("二次注册换了账户密钥")
	}
	if a2.ID != a.ID {
		t.Errorf("同一 CA 应就地更新那一行,实得新 id %s", a2.ID)
	}
	var n int
	if err := st.DB.QueryRow(`SELECT COUNT(1) FROM acme_accounts`).Scan(&n); err != nil || n != 1 {
		t.Errorf("acme_accounts 应只 1 行,实得 %d(err=%v)", n, err)
	}
}

func TestRegisterAccountAlreadyExistsOnCA(t *testing.T) {
	st := storetest.Open(t)
	txt := newTXTRecorder()
	f := newFakeCA(t, txt)
	f.accountAlreadyRegistered = true
	s := newAcmeSvc(t, st.DB, txt, f.directoryURL())

	// CA 说「这把密钥早注册过了」(返回 200):平台改走只读查询把账户地址取回来,而不是放弃。
	a, err := s.RegisterAccount(context.Background(), AccountInput{CA: CALetsEncrypt})
	if err != nil {
		t.Fatalf("已注册账户该被接住,实得错误: %v", err)
	}
	if a.AccountURL == "" {
		t.Error("没取回账户地址")
	}
	accounts, err := s.Accounts(context.Background())
	if err != nil || len(accounts) != 1 {
		t.Fatalf("账户清单 = %v(err=%v)", accounts, err)
	}
}

func TestRegisterAccountEABRules(t *testing.T) {
	st := storetest.Open(t)
	txt := newTXTRecorder()
	f := newFakeCA(t, txt)
	s := newAcmeSvc(t, st.DB, txt, f.directoryURL())
	ctx := context.Background()

	// 要 EAB 的 CA 不给 EAB:直接拒,不让用户去猜 CA 回的那句 accountDoesNotExist。
	if _, err := s.RegisterAccount(ctx, AccountInput{CA: CAZeroSSL}); !errors.Is(err, ErrEABRequired) {
		t.Errorf("ZeroSSL 无 EAB = %v, 期望 %v", err, ErrEABRequired)
	}
	if _, err := s.RegisterAccount(ctx, AccountInput{CA: CAZeroSSL, EABKeyID: "kid-1", EABKey: "!!!not base64!!!"}); !errors.Is(err, ErrBadEABKey) {
		t.Errorf("坏 EAB 密钥 = %v, 期望 %v", err, ErrBadEABKey)
	}
	if _, err := s.RegisterAccount(ctx, AccountInput{CA: CALetsEncrypt, Contact: "not-an-email"}); err == nil {
		t.Error("联系人不是邮箱该被拒(填错只会收到 CA 的一串号码)")
	}
	// 带上 EAB:注册成,且请求里真的带了绑定材料。
	a, err := s.RegisterAccount(ctx, AccountInput{CA: CAZeroSSL, EABKeyID: "kid-1", EABKey: base64.RawURLEncoding.EncodeToString([]byte("secret-key"))})
	if err != nil {
		t.Fatalf("带 EAB 注册: %v", err)
	}
	if !a.HasEABKey || a.EABKeyID != "kid-1" {
		t.Errorf("EAB 视图异常: %+v", a)
	}
	f.mu.Lock()
	hadEAB := f.hadEAB
	f.mu.Unlock()
	if !hadEAB {
		t.Error("EAB 材料没发给 CA")
	}
	var eabSealed []byte
	if err := st.DB.QueryRow(`SELECT eab_key_sealed FROM acme_accounts WHERE ca = ?`, CAZeroSSL).Scan(&eabSealed); err != nil {
		t.Fatalf("读 EAB: %v", err)
	}
	if strings.Contains(string(eabSealed), "secret-key") {
		t.Error("EAB 对称密钥明文入库了")
	}
}

func TestIssueDNS01HappyPath(t *testing.T) {
	st := storetest.Open(t)
	txt := newTXTRecorder()
	f := newFakeCA(t, txt)
	s := newAcmeSvc(t, st.DB, txt, f.directoryURL())
	ctx := context.Background()
	seedProvider(t, st.DB, "dns-1")

	id := seedAndRegister(t, s, CALetsEncryptStaging, "shop.example.com", []string{"shop.example.com"})
	c, err := s.Issue(ctx, id)
	if err != nil {
		t.Fatalf("签发: %v", err)
	}
	if c.Status != StatusIssued || !c.HasKey {
		t.Errorf("签发后状态 = %s hasKey=%v", c.Status, c.HasKey)
	}
	if c.Issuer != "Pipewright Fake CA" || c.Serial != "4242" {
		t.Errorf("正文解析异常: issuer=%q serial=%q", c.Issuer, c.Serial)
	}
	if c.KeyAlgo != "ECDSA-P256" {
		t.Errorf("密钥算法 = %q, 期望 ECDSA-P256", c.KeyAlgo)
	}
	if want := testNow.AddDate(0, 0, 90); !c.NotAfter.Equal(want) {
		t.Errorf("到期时间 = %s, 期望 %s", c.NotAfter, want)
	}
	if strings.Count(c.ChainPEM, "BEGIN CERTIFICATE") != 1 {
		t.Errorf("中间链应 1 块,实得 %d", strings.Count(c.ChainPEM, "BEGIN CERTIFICATE"))
	}
	// 用的是登记时选的那个 DNS 提供商,不是随便挑一个。
	used := txt.usedProviders()
	if len(used) == 0 {
		t.Fatal("一次 TXT 都没写")
	}
	for _, p := range used {
		if p != "dns-1" {
			t.Errorf("用了别的提供商: %q", p)
		}
	}
	// 校验题写对了地方,且验完就清干净(残留会让下次签发直接失败)。
	name := "_acme-challenge.shop.example.com"
	if len(txt.ensureSeq()) != 1 || !strings.HasPrefix(txt.ensureSeq()[0], name+"=") {
		t.Errorf("TXT 写入序列 = %v", txt.ensureSeq())
	}
	if got := txt.current(name); got != "" {
		t.Errorf("签发完还留着 TXT: %q", got)
	}
	if d := txt.deleted(); len(d) != 1 || d[0] != name {
		t.Errorf("TXT 清理 = %v, 期望 [%s]", d, name)
	}
	// CA 收到的订单标识与 CSR 名字都由平台填的域名来。
	f.mu.Lock()
	ids, csrNames, cn := f.identifiers, f.csrNames, f.csrCommonName
	f.mu.Unlock()
	if len(ids) != 1 || ids[0].Value != "shop.example.com" {
		t.Errorf("订单标识 = %v", ids)
	}
	if len(csrNames) != 1 || csrNames[0] != "shop.example.com" || cn != "shop.example.com" {
		t.Errorf("CSR 名字异常: san=%v cn=%q", csrNames, cn)
	}

	// 取包能拿到配套私钥,且库里只有密文。
	b, err := s.Bundle(ctx, id)
	if err != nil {
		t.Fatalf("取证书包: %v", err)
	}
	if strings.Count(b.Fullchain(), "BEGIN CERTIFICATE") != 2 {
		t.Errorf("Fullchain 应 2 块,实得 %d", strings.Count(b.Fullchain(), "BEGIN CERTIFICATE"))
	}
	_, leaf, err := ParseLeaf(b.CertPEM)
	if err != nil {
		t.Fatalf("解析叶子: %v", err)
	}
	_, _, signer, err := ParseKey(b.KeyPEM)
	if err != nil {
		t.Fatalf("解析私钥: %v", err)
	}
	if !KeyMatchesCert(leaf, signer) {
		t.Error("入库的私钥与证书公钥不配套")
	}
	var keySealed []byte
	if err := st.DB.QueryRow(`SELECT key_sealed FROM certificates WHERE id = ?`, id).Scan(&keySealed); err != nil {
		t.Fatalf("读私钥列: %v", err)
	}
	if strings.Contains(string(keySealed), "PRIVATE KEY") {
		t.Error("私钥明文入库了")
	}
	// 签发成功要把上一次的失败原因清空,否则页面永远挂着一条过期告警。
	if c.LastError != "" {
		t.Errorf("LastError = %q, 期望空", c.LastError)
	}
}

func TestIssueWildcardAndApexAreSequential(t *testing.T) {
	st := storetest.Open(t)
	txt := newTXTRecorder()
	f := newFakeCA(t, txt)
	s := newAcmeSvc(t, st.DB, txt, f.directoryURL())
	ctx := context.Background()
	seedProvider(t, st.DB, "dns-1")

	// 通配与主名共用同一个记录名,值却各自不同:假 CA 在校验那一刻去查记录,
	// 只要实现是「先把两条 TXT 都写下、再一起校验」,第一条必然被第二条覆盖掉 → invalid。
	id := seedAndRegister(t, s, CALetsEncryptStaging, "*.example.com", []string{"*.example.com", "example.com"})
	c, err := s.Issue(ctx, id)
	if err != nil {
		t.Fatalf("签发通配证书: %v", err)
	}
	seq := txt.ensureSeq()
	if len(seq) != 2 {
		t.Fatalf("两条 authorization 该各写一次 TXT,实得 %v", seq)
	}
	for _, e := range seq {
		if !strings.HasPrefix(e, "_acme-challenge.example.com=") {
			t.Errorf("通配与主名的记录名都应收敛到 apex,实得 %q", e)
		}
	}
	if seq[0] == seq[1] {
		t.Error("两次写了同一个值:token 没跟着挑战走")
	}
	// 清理只做一次(同名去重):重复删除没有收益,只会多打一次 API。
	if d := txt.deleted(); len(d) != 1 || d[0] != "_acme-challenge.example.com" {
		t.Errorf("TXT 清理 = %v", d)
	}
	if len(c.SANs) != 2 {
		t.Errorf("SAN 应落全,实得 %v", c.SANs)
	}
}

func TestIssueFailsWhenTXTNotVisible(t *testing.T) {
	st := storetest.Open(t)
	txt := newTXTRecorder()
	f := newFakeCA(t, txt)
	s := newAcmeSvc(t, st.DB, txt, f.directoryURL())
	ctx := context.Background()
	seedProvider(t, st.DB, "dns-1")

	name := "_acme-challenge.bad.example.com"
	txt.blockName = name // 提供商回执成功、实际没落库(DNS API 偶发行为)
	id := seedAndRegister(t, s, CALetsEncryptStaging, "bad.example.com", nil)
	_, err := s.Issue(ctx, id)
	if !errors.Is(err, ErrIssueFailed) {
		t.Fatalf("CA 判失败时该报签发失败,实得 %v", err)
	}
	if !strings.Contains(err.Error(), "域名校验未通过") {
		t.Errorf("错误要指名是哪一步: %v", err)
	}
	r, err := s.store.getCert(ctx, id)
	if err != nil {
		t.Fatalf("读回记录: %v", err)
	}
	// 没正文 + 签发失败 → 状态 failed,原因落进 last_error(页面与告警都读它)。
	if r.Status != StatusFailed || r.LastError == "" {
		t.Errorf("失败记录 = %s/%q", r.Status, r.LastError)
	}
	// 失败也要把校验题删掉:残留旧值会让下一次签发以同样的理由再失败一次。
	if got := txt.current(name); got != "" {
		t.Errorf("失败后 TXT 没清理: %q", got)
	}
}

func TestIssueKeepsWorkingCertificateOnFailure(t *testing.T) {
	st := storetest.Open(t)
	txt := newTXTRecorder()
	f := newFakeCA(t, txt)
	s := newAcmeSvc(t, st.DB, txt, f.directoryURL())
	ctx := context.Background()
	seedProvider(t, st.DB, "dns-1")

	id := seedAndRegister(t, s, CALetsEncryptStaging, "keep.example.com", nil)
	first, err := s.Issue(ctx, id)
	if err != nil {
		t.Fatalf("首次签发: %v", err)
	}
	// 续期时 CA 直接拒单(域名被下架、账号被限流都长这样)。
	f.orderStatus = http.StatusBadRequest

	_, err = s.Issue(ctx, id)
	if err == nil {
		t.Fatal("下单被拒该报错")
	}
	c, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("读回记录: %v", err)
	}
	// 正在服务的那张证书没坏:状态仍按正文推导,不能被一次续期失败打成 failed。
	if c.Status != StatusIssued || c.Serial != first.Serial {
		t.Errorf("续期失败把可用证书的视图改坏了: %s serial=%s(原 %s)", c.Status, c.Serial, first.Serial)
	}
	if c.LastError == "" {
		t.Error("失败原因没落到 last_error")
	}
}

func TestIssueGuards(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	seedProvider(t, st.DB, "dns-1")
	txt := newTXTRecorder()

	// 没接 DNS 写入能力(装配漏了):要立刻说清楚,而不是等网络超时。
	s := newSvc(t, st.DB, fakeSeal{})
	s.directory = func(CA) string { return "https://ca.invalid/directory" }
	noTXT := &service{store: s.store, seal: s.seal, now: s.now, directory: s.directory}
	c, err := noTXT.Create(ctx, CreateInput{Domain: "a.example.com", CA: CALetsEncrypt, DNSProviderID: "dns-1"})
	if err != nil {
		t.Fatalf("登记: %v", err)
	}
	if _, err := noTXT.Issue(ctx, c.ID); !errors.Is(err, ErrTXTWriterUnconfigured) {
		t.Errorf("缺 TXTWriter = %v, 期望 %v", err, ErrTXTWriterUnconfigured)
	}

	// 有写入能力但还没注册账户:免 EAB 的 CA 会在签发时当场自动注册,所以这里用需要 EAB 的
	// ZeroSSL 来验「注册不了就报没账户」这条护栏(它没法替用户造 EAB 凭据)。
	f := newFakeCA(t, txt)
	s2 := newAcmeSvc(t, st.DB, txt, f.directoryURL())
	seedProvider(t, st.DB, "dns-2")
	c2, err := s2.Create(ctx, CreateInput{Domain: "b.example.com", CA: CAZeroSSL, DNSProviderID: "dns-1"})
	if err != nil {
		t.Fatalf("登记 2: %v", err)
	}
	if _, err := s2.Issue(ctx, c2.ID); !errors.Is(err, ErrNoACMEAccount) {
		t.Errorf("需要 EAB 的 CA 未注册账户 = %v, 期望 %v", err, ErrNoACMEAccount)
	}

	// 导入的证书不归平台签。
	key := newKey(t)
	up, err := s2.Import(ctx, ImportInput{
		Domain:  "imp.example.com",
		CertPEM: selfCertPEM(t, []string{"imp.example.com"}, testNow.AddDate(0, 0, -1), testNow.AddDate(0, 0, 89), key, 8101),
		KeyPEM:  keyPEMFor(t, key, "pkcs8"),
	})
	if err != nil {
		t.Fatalf("导入: %v", err)
	}
	if _, err := s2.Issue(ctx, up.ID); !errors.Is(err, ErrNotACME) {
		t.Errorf("导入证书签发 = %v, 期望 %v", err, ErrNotACME)
	}
}

// TestIssueAutoRegistersAccount 锁住 Certimate 式一步到位:免 EAB 的 CA,登记证书后不先注册账户,
// 直接签发就应该当场把账户注册好并签出证书。
func TestIssueAutoRegistersAccount(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	seedProvider(t, st.DB, "dns-1")
	txt := newTXTRecorder()
	f := newFakeCA(t, txt)
	s := newAcmeSvc(t, st.DB, txt, f.directoryURL())

	c, err := s.Create(ctx, CreateInput{Domain: "auto.example.com", CA: CALetsEncrypt, DNSProviderID: "dns-1"})
	if err != nil {
		t.Fatalf("登记: %v", err)
	}
	// 签发前账户表里确实什么都没有。
	if _, err := s.store.getAccount(ctx, CALetsEncrypt); !errors.Is(err, ErrNotFound) {
		t.Fatalf("前置: 期望未注册账户, 实得 err=%v", err)
	}
	got, err := s.Issue(ctx, c.ID)
	if err != nil {
		t.Fatalf("未先注册账户的签发 = %v, 期望自动注册后签成", err)
	}
	if got.Status != StatusIssued || !got.HasKey {
		t.Errorf("签发结果 = %s hasKey=%v, 期望 issued/true", got.Status, got.HasKey)
	}
	// 自动注册应该把 LE 账户落库,并替用户在 CA 侧接受了条款。
	if _, err := s.store.getAccount(ctx, CALetsEncrypt); err != nil {
		t.Errorf("签发后账户应已入库: %v", err)
	}
	f.mu.Lock()
	terms := f.termsSet
	f.mu.Unlock()
	if !terms {
		t.Errorf("自动注册应向 CA 提交条款同意")
	}
}

func TestIssueWritesAreCleanedUpEvenWhenTXTFails(t *testing.T) {
	st := storetest.Open(t)
	txt := newTXTRecorder()
	f := newFakeCA(t, txt)
	s := newAcmeSvc(t, st.DB, txt, f.directoryURL())
	ctx := context.Background()
	seedProvider(t, st.DB, "dns-1")

	txt.failEnsure = fmt.Errorf("dnsprovider: 令牌被吊销")
	id := seedAndRegister(t, s, CALetsEncryptStaging, "wr.tf.example.com", nil)
	_, err := s.Issue(ctx, id)
	if !errors.Is(err, ErrIssueFailed) {
		t.Fatalf("写 TXT 失败该报签发失败,实得 %v", err)
	}
	if !strings.Contains(err.Error(), "令牌被吊销") {
		t.Errorf("人话原因没带上游错误: %v", err)
	}
	r, err := s.Get(ctx, id)
	if err != nil || r.Status != StatusFailed {
		t.Errorf("记录状态 = %v err=%v", r, err)
	}
	// 一条都没写成,清理也不该凭空发删除请求。
	if d := txt.deleted(); len(d) != 0 {
		t.Errorf("没写过却要删:%v", d)
	}
}

func TestIssueProceedsWhenPropagationCheckTimesOut(t *testing.T) {
	st := storetest.Open(t)
	txt := newTXTRecorder()
	f := newFakeCA(t, txt)
	s := newAcmeSvc(t, st.DB, txt, f.directoryURL())
	// 本机解析器什么都看不见(缓存 / 网络策略 / 容器里没 DNS 都可能出现)。
	lookups := 0
	s.lookupTXT = func(context.Context, string) ([]string, error) {
		lookups++
		return nil, errors.New("dns: server misbehaving")
	}
	s.propagateTimeout = 30 * time.Millisecond
	s.propagateInterval = 5 * time.Millisecond
	ctx := context.Background()
	seedProvider(t, st.DB, "dns-1")

	// 解析检查只是提前止损,不是门槛:超时必须照样让 CA 去验,否则这类机器永远签不出证书。
	id := seedAndRegister(t, s, CALetsEncryptStaging, "slow.example.com", nil)
	if _, err := s.Issue(ctx, id); err != nil {
		t.Fatalf("解析不可见不该拦住签发: %v", err)
	}
	if lookups < 2 {
		t.Errorf("没等到值时该重试到超时,实得 %d 次解析", lookups)
	}
}

func TestIssueCleansEarlierRecordsWhenLaterDomainFails(t *testing.T) {
	st := storetest.Open(t)
	txt := newTXTRecorder()
	f := newFakeCA(t, txt)
	s := newAcmeSvc(t, st.DB, txt, f.directoryURL())
	ctx := context.Background()
	seedProvider(t, st.DB, "dns-1")

	// 第二个域名的写入被吞掉 → 整单失败;但**第一个**域名已经写下去的记录必须清掉,
	// 否则它带着上一次的旧值留在那里,下一次签发会以同样的理由再失败一次。
	a, b := "_acme-challenge.p1.example.com", "_acme-challenge.p2.example.com"
	txt.blockName = b
	id := seedAndRegister(t, s, CALetsEncryptStaging, "p1.example.com", []string{"p1.example.com", "p2.example.com"})
	if _, err := s.Issue(ctx, id); !errors.Is(err, ErrIssueFailed) {
		t.Fatalf("期望签发失败,实得 %v", err)
	}
	if seq := txt.ensureSeq(); len(seq) != 1 || !strings.HasPrefix(seq[0], a+"=") {
		t.Errorf("只该写成第一条,实得 %v", seq)
	}
	d := txt.deleted()
	if len(d) != 2 || d[0] != a || d[1] != b {
		t.Errorf("两条都该进清理清单(含没写成的那条的名字之外那份已成功的),实得 %v", d)
	}
	if got := txt.current(a); got != "" {
		t.Errorf("第一条记录没清掉: %q", got)
	}
}

func TestCleanupTXTIgnoresCancelledContext(t *testing.T) {
	txt := newTXTRecorder()
	s := &service{txt: txt}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 签发因超时/中断失败的最常见情形

	s.cleanupTXT(ctx, "dns-1", []string{"_acme-challenge.example.com"})
	if d := txt.deleted(); len(d) != 1 {
		t.Fatalf("清理没跑: %v", d)
	}
	if txt.ctxErrAtDelete != nil {
		t.Errorf("清理必须换个不受牵连的上下文,实得 %v", txt.ctxErrAtDelete)
	}
}

func TestACMEClientUsesStoredAccountURL(t *testing.T) {
	txt := newTXTRecorder()
	f := newFakeCA(t, txt)
	s := newAcmeSvc(t, storetest.Open(t).DB, txt, f.directoryURL())
	key, err := newAccountKey()
	if err != nil {
		t.Fatalf("账户密钥: %v", err)
	}
	ca, _ := LookupCA(CALetsEncrypt)
	c := s.newACMEClient(ca, key, "https://ca.invalid/acct/42")
	if string(c.KID) != "https://ca.invalid/acct/42" {
		t.Errorf("已知账户地址要直接当 kid 用(省一次往返),实得 %q", c.KID)
	}
	if c.DirectoryURL != f.directoryURL() {
		t.Errorf("directory 地址没走注入点: %q", c.DirectoryURL)
	}
	if !strings.Contains(c.UserAgent, "pipewright") {
		t.Errorf("CA 侧要能认出是谁在打请求,实得 %q", c.UserAgent)
	}
}

// firstBytes 取前 n 个字节做可读片段(断言失败时打印用,绝不用于错误文本)。
func firstBytes(b []byte, n int) string {
	if len(b) < n {
		n = len(b)
	}
	return string(b[:n])
}
