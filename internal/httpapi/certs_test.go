// certs_test.go 守证书这条线的 HTTP 契约,三件事:
//   - 权限两条轴:读要 cert.view、写要 cert.manage;凡往目标机写文件的动作,还要那台机器的
//     ActOperate —— 少任何一道,都能拿到「往管不着的机器上铺私钥」的能力。
//   - 私钥纪律(AC-SEC-01):除 GET /certs/{id}/bundle,任何响应体、错误体与审计都不得出现私钥,
//     连承载它的字段名都不许出现。
//   - 领域错误 → 契约码的映射,以及逐落点发布报告的形状。
//
// PEM 校验、ACME 下单、SSH 落位这些领域行为由 internal/cert 的包测试覆盖,这里只测边界上的契约。
package httpapi

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/access"
	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/cert"
	"github.com/fuuhoo/pipewright/internal/group"
	"github.com/fuuhoo/pipewright/internal/mask"
	"github.com/fuuhoo/pipewright/internal/storetest"
	"github.com/fuuhoo/pipewright/internal/target"
	"github.com/fuuhoo/pipewright/internal/users"
	"github.com/fuuhoo/pipewright/internal/vault"
	"github.com/google/uuid"
)

const certPass = "cert-pass-1234"

// fakeCertPublisher 是 cert.Publisher 的替身:把「上传了什么内容到哪个名字」与「跑了哪些命令」
// 都记下来。证书这条线的核心断言是私钥明文只出现在落盘那一步,所以要能看见它收到了什么。
type fakeCertPublisher struct {
	mu         sync.Mutex
	uploads    map[string]string // "serverID|临时名" → 内容
	cmds       []string          // 按序记录的执行(含 chmod / mv / 用户的生效指令)
	failUpload bool
}

func (f *fakeCertPublisher) Upload(_ context.Context, serverID string, content io.Reader, remotePath string) error {
	raw, err := io.ReadAll(content)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failUpload {
		return errors.New("假目标机:写不进去")
	}
	f.uploads[serverID+"|"+remotePath] = string(raw)
	return nil
}

func (f *fakeCertPublisher) Exec(_ context.Context, serverID string, cmd []string) (*target.ExecResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds = append(f.cmds, serverID+" "+strings.Join(cmd, " "))
	return &target.ExecResult{ExitCode: 0}, nil
}

// saw 断言某个最终路径确实收到了指定内容(上传走的是临时名,再由 mv 落位,所以按内容找)。
func (f *fakeCertPublisher) saw(content string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, v := range f.uploads {
		if v == content {
			return true
		}
	}
	return false
}

func (f *fakeCertPublisher) uploadCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.uploads)
}

func (f *fakeCertPublisher) ran(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.cmds {
		if strings.Contains(c, prefix) {
			return true
		}
	}
	return false
}

type certEnv struct {
	srv       *httptest.Server
	db        *sql.DB
	pub       *fakeCertPublisher
	v         vault.Vault
	admin     *http.Client
	adminCS   string
	viewer    *http.Client
	viewerCS  string
	normal    *http.Client // role=user:有 cert.manage,但不在私有组名册里
	normalCS  string
	freeSrv   string // 未归组主机:任何有 server.exec 的角色都能操作
	secretSrv string // 私有组内主机:normal 碰不到
}

// setupCertsAPI 装配证书端点。withPublisher=false 时不接目标机执行能力(用来验 503 分支);
// TXT 写入能力**任何情况下都不接**,所以签发路径恒为「装配缺失」,不会在测试里真去访问 CA。
func setupCertsAPI(t *testing.T, withPublisher bool) *certEnv {
	t.Helper()
	st := storetest.Open(t)
	db := st.DB
	userSvc := users.NewService(db)
	authSvc := auth.NewService(db, nil, userSvc)
	if err := authSvc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap admin: %v", err)
	}
	loadRoleCatalog(t, db)

	hash, err := auth.HashPassword(certPass)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	mkUser := func(name, role string) string {
		t.Helper()
		id := uuid.NewString()
		if _, err := db.Exec(
			`INSERT INTO users (id, username, password_hash, role, enabled, created_at, updated_at)
			 VALUES (?, ?, ?, ?, 1, ?, ?)`,
			id, name, hash, role, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
			t.Fatalf("seed user %s: %v", name, err)
		}
		return id
	}
	mkUser("verna", "viewer")
	bossID := mkUser("boss", "user") // 只当私有组组长用,不登录
	mkUser("una", "user")

	gs := group.New(db)
	priv, err := gs.Create(context.Background(), group.CreateInput{
		Name: "证书私有组", OwnerID: bossID, Visibility: access.VisibilityPrivate,
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}

	v := vault.New(db, testMasterKey())
	sshCred, err := v.Create(vault.CreateInput{Name: "cert ssh", Type: vault.TypeSSHKey, Secret: "cert-ssh-secret"})
	if err != nil {
		t.Fatalf("seed ssh credential: %v", err)
	}
	// DNS-01 的提供商引用必须落得下:登记路径会查 dns_providers,引用悬空等于必然签不出。
	// (TXT 写入能力始终不接,所以签发仍在「装配缺失」处停下,不会因为这里有行记录就去碰网络。)
	if _, err := db.Exec(
		`INSERT INTO dns_providers (id, type, name, credential_id, created_at, updated_at)
		 VALUES ('dns-1', 'cloudflare', 'dns-1', ?, ?, ?)`,
		sshCred.ID, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("seed dns provider: %v", err)
	}
	mkSrv := func(name, groupID string) string {
		t.Helper()
		id := uuid.NewString()
		if _, err := db.Exec(
			`INSERT INTO servers (id, name, host, port, user, credential_id, group_id, created_at, updated_at)
			 VALUES (?, ?, '10.0.0.7', 22, 'deploy', ?, ?, ?, ?)`,
			id, name, sshCred.ID, groupID, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
			t.Fatalf("seed server %s: %v", name, err)
		}
		return id
	}
	freeSrv := mkSrv("free-srv", access.Ungrouped)
	secretSrv := mkSrv("secret-srv", priv.ID)

	pub := &fakeCertPublisher{uploads: map[string]string{}}
	opts := []Option{
		WithVault(v),
		WithAudit(audit.New(db, mask.NewMasker(), nil)),
		WithGroups(gs),
		WithAccess(access.NewService(gs)),
	}
	if withPublisher {
		opts = append(opts, WithCerts(cert.New(db, v, cert.WithPublisher(pub))))
	} else {
		opts = append(opts, WithCerts(cert.New(db, v)))
	}
	srv := httptest.NewServer(New(testWebFSAuth(), authSvc, opts...))
	t.Cleanup(srv.Close)

	env := &certEnv{srv: srv, db: db, pub: pub, v: v, freeSrv: freeSrv, secretSrv: secretSrv}
	env.admin = newTestClient(t)
	env.adminCS = loginAs(t, env.admin, srv.URL, "admin", "testpass")
	env.viewer = newTestClient(t)
	env.viewerCS = loginAs(t, env.viewer, srv.URL, "verna", certPass)
	env.normal = newTestClient(t)
	env.normalCS = loginAs(t, env.normal, srv.URL, "una", certPass)
	return env
}

// —— 测试材料:自签一张能过导入校验的证书 ——————————————————

type certMaterial struct {
	domain string
	cert   string
	key    string
	chain  string
}

func newCertMaterial(t *testing.T, domain string, serial int64) certMaterial {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成私钥: %v", err)
	}
	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: domain},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(90 * 24 * time.Hour),
		DNSNames:     []string{domain},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("自签: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalPKCS8: %v", err)
	}
	return certMaterial{
		domain: domain,
		cert:   string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		key:    string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})),
		chain:  string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
	}
}

// certCall 发一个请求并读完即关(包里现成的 readAll 不关 body,这里每个用例会连打好几个请求)。
func certCall(t *testing.T, c *http.Client, cs, method, url, body string) (int, string) {
	t.Helper()
	resp := doJSON(t, c, method, url, cs, body)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读响应: %v", err)
	}
	return resp.StatusCode, string(raw)
}

func certBody(t *testing.T, m map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("编码请求体: %v", err)
	}
	return string(raw)
}

// decodeJSONBody 把响应读成 map,顺带断言它是 JSON。
// (名字要带后缀:包里 ai_generate_test.go 已有一个吃 *http.Response 的 decode。)
func decodeJSONBody(t *testing.T, body string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("响应不是 JSON(%v): %s", err, body)
	}
	return out
}

// certItems 取响应里的 items 数组(全站清单端点的统一形状)。
func certItems(t *testing.T, body string) []any {
	t.Helper()
	items, ok := decodeJSONBody(t, body)["items"].([]any)
	if !ok {
		t.Fatalf("响应无 items 数组: %s", body)
	}
	return items
}

// assertNoPrivateKeyMaterial 是 AC-SEC-01 的执行器:既不许出现私钥正文,也不许出现承载它的字段名。
func assertNoPrivateKeyMaterial(t *testing.T, label, body, keyPEM string) {
	t.Helper()
	for _, banned := range []string{keyPEM, "PRIVATE KEY", `"privateKey"`, `"keyPem"`, `"keySealed"`} {
		if strings.Contains(body, banned) {
			t.Fatalf("%s 出现私钥材料或密钥字段名 %q:\n%s", label, banned, body)
		}
	}
}

// auditRow 取某个 action 最近一条审计的 detail JSON(审计是这张表里唯一的旁观者,
// 泄漏与「谁在什么时候拿到了私钥」都靠它复盘,所以每条写路径都要断言它)。
func auditRow(t *testing.T, db *sql.DB, action string) (int, string) {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM audit_log WHERE action = ?`, action).Scan(&n); err != nil {
		t.Fatalf("数审计 %s: %v", action, err)
	}
	var detail string
	err := db.QueryRow(`SELECT detail_json FROM audit_log WHERE action = ? ORDER BY id DESC LIMIT 1`, action).Scan(&detail)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ""
	}
	if err != nil {
		t.Fatalf("读审计 %s: %v", action, err)
	}
	return n, detail
}

// createPendingCert 登记一张待签证书并返回其 id(签发路径的夹具)。
func createPendingCert(t *testing.T, env *certEnv, domain string) string {
	t.Helper()
	code, body := certCall(t, env.admin, env.adminCS, http.MethodPost, env.srv.URL+"/api/certs", certBody(t, map[string]any{
		"domain": domain, "ca": "letsencrypt_staging", "dnsProviderId": "dns-1", "autoRenew": true,
	}))
	if code != http.StatusCreated {
		t.Fatalf("登记待签证书 status = %d, want 201: %s", code, body)
	}
	id, _ := decodeJSONBody(t, body)["id"].(string)
	if id == "" {
		t.Fatalf("登记响应无 id: %s", body)
	}
	return id
}

// importCert 导入一张已有证书并返回其 id。
func importCert(t *testing.T, env *certEnv, c *http.Client, cs string, m certMaterial) string {
	t.Helper()
	code, body := certCall(t, c, cs, http.MethodPost, env.srv.URL+"/api/certs/import", certBody(t, map[string]any{
		"domain": m.domain, "certPem": m.cert, "keyPem": m.key, "chainPem": m.chain,
	}))
	if code != http.StatusCreated {
		t.Fatalf("导入证书 status = %d, want 201: %s", code, body)
	}
	id, _ := decodeJSONBody(t, body)["id"].(string)
	if id == "" {
		t.Fatalf("导入响应无 id: %s", body)
	}
	return id
}

// addCertTarget 给一张证书加一个发布落点,返回落点 id。
func addCertTarget(t *testing.T, env *certEnv, c *http.Client, cs, certID, serverID, base string) string {
	t.Helper()
	code, body := certCall(t, c, cs, http.MethodPost, env.srv.URL+"/api/certs/"+certID+"/targets", certBody(t, map[string]any{
		"serverId": serverID, "certPath": "/etc/ssl/" + base + ".pem", "keyPath": "/etc/ssl/" + base + ".key",
		"postCommand": "systemctl reload nginx", "autoDeploy": true,
	}))
	if code != http.StatusCreated {
		t.Fatalf("新增落点 status = %d, want 201: %s", code, body)
	}
	id, _ := decodeJSONBody(t, body)["id"].(string)
	if id == "" {
		t.Fatalf("落点响应无 id: %s", body)
	}
	return id
}

// —— 鉴权与功能点 ————————————————————————————————————————

// TestCerts_NoSessionRejected 验证书端点没有匿名入口:未登录拿不到任何证书清单。
func TestCerts_NoSessionRejected(t *testing.T) {
	env := setupCertsAPI(t, true)
	resp, err := newTestClient(t).Get(env.srv.URL + "/api/certs")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("匿名 GET /api/certs = %d, want 401", resp.StatusCode)
	}
}

// TestCerts_PermGating 是功能轴的那条线:cert.view 只够看,cert.manage 才动得了。
// viewer 档没有 cert.manage(0073 刻意不播),所以所有写端点对它都是 403 而不是 404 ——
// 「不存在」和「不许动」必须区分开,否则页面会把权限问题报成数据问题。
func TestCerts_PermGating(t *testing.T) {
	env := setupCertsAPI(t, true)

	code, body := certCall(t, env.viewer, env.viewerCS, http.MethodGet, env.srv.URL+"/api/certs", "")
	if code != http.StatusOK {
		t.Fatalf("viewer 读清单 = %d, want 200: %s", code, body)
	}
	code, body = certCall(t, env.viewer, env.viewerCS, http.MethodGet, env.srv.URL+"/api/certs/cas", "")
	if code != http.StatusOK {
		t.Fatalf("viewer 读 CA 名单 = %d, want 200: %s", code, body)
	}
	code, body = certCall(t, env.viewer, env.viewerCS, http.MethodGet, env.srv.URL+"/api/certs/accounts", "")
	if code != http.StatusOK {
		t.Fatalf("viewer 读 ACME 账户 = %d, want 200: %s", code, body)
	}

	for _, c := range []struct {
		name, method, path, body string
	}{
		{"登记", http.MethodPost, "/api/certs", `{"domain":"a.example.com","ca":"letsencrypt_staging","dnsProviderId":"dns-1"}`},
		{"导入", http.MethodPost, "/api/certs/import", `{"domain":"a.example.com"}`},
		{"签发", http.MethodPost, "/api/certs/x/issue", ""},
		{"发布", http.MethodPost, "/api/certs/x/deploy", ""},
		{"取证书包", http.MethodGet, "/api/certs/x/bundle", ""},
		{"注册账户", http.MethodPost, "/api/certs/accounts", `{"ca":"letsencrypt_staging"}`},
	} {
		code, body = certCall(t, env.viewer, env.viewerCS, c.method, env.srv.URL+c.path, c.body)
		if code != http.StatusForbidden {
			t.Errorf("viewer %s = %d, want 403: %s", c.name, code, body)
			continue
		}
		if got := decodeJSONBody(t, body); decodeCode(t, got) != "role_denied" {
			t.Errorf("viewer %s 错误码 = %v, want role_denied", c.name, got["error"])
		}
	}

	// una 是 role=user:0073 给她播了 cert.manage,登记应当过功能轴。
	// (这里落在未归组主机之外,不涉数据轴,故 201。)
	code, body = certCall(t, env.normal, env.normalCS, http.MethodPost, env.srv.URL+"/api/certs", certBody(t, map[string]any{
		"domain": "una.example.com", "ca": "letsencrypt_staging", "dnsProviderId": "dns-1",
	}))
	if code != http.StatusCreated {
		t.Errorf("role=user 登记证书 = %d, want 201: %s", code, body)
	}
}

func decodeCode(t *testing.T, obj map[string]any) string {
	t.Helper()
	d, ok := obj["error"].(map[string]any)
	if !ok {
		t.Fatalf("错误响应没有 error 对象: %v", obj)
	}
	s, _ := d["code"].(string)
	return s
}

// TestCerts_ServiceUnconfigured 验「main 没注入证书服务」时端点全体 503,而不是 panic
// 或回一份空清单让用户以为「一张证书也没有」。
func TestCerts_ServiceUnconfigured(t *testing.T) {
	st := storetest.Open(t)
	authSvc := auth.NewService(st.DB, nil, nil)
	if err := authSvc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	srv := httptest.NewServer(New(testWebFSAuth(), authSvc, WithVault(vault.New(st.DB, testMasterKey()))))
	t.Cleanup(srv.Close)
	c := newTestClient(t)
	cs := loginAs(t, c, srv.URL, "admin", "testpass")

	code, body := certCall(t, c, cs, http.MethodGet, srv.URL+"/api/certs", "")
	if code != http.StatusServiceUnavailable || decodeCode(t, decodeJSONBody(t, body)) != "internal" {
		t.Fatalf("未装配时 GET = %d %s, want 503 internal", code, body)
	}
	code, body = certCall(t, c, cs, http.MethodPost, srv.URL+"/api/certs", `{"domain":"a.example.com"}`)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("未装配时 POST = %d %s, want 503", code, body)
	}
	// 「立即检查」也在这条线上:它会当场续期,更不能因为没装配就 panic 或静默返回空汇总。
	code, body = certCall(t, c, cs, http.MethodPost, srv.URL+"/api/certs/check", "")
	if code != http.StatusServiceUnavailable || decodeCode(t, decodeJSONBody(t, body)) != "internal" {
		t.Fatalf("未装配时 POST /certs/check = %d %s, want 503 internal", code, body)
	}
}

// —— 登记 / 清单 / 详情 ——————————————————————————————————

func TestCertCreate_ListAndDetailContract(t *testing.T) {
	env := setupCertsAPI(t, true)
	code, body := certCall(t, env.admin, env.adminCS, http.MethodPost, env.srv.URL+"/api/certs", certBody(t, map[string]any{
		"domain": "shop.example.com", "sans": []string{"www.shop.example.com"},
		"ca": "letsencrypt_staging", "dnsProviderId": "dns-1", "autoRenew": true,
	}))
	if code != http.StatusCreated {
		t.Fatalf("登记 = %d, want 201: %s", code, body)
	}
	created := decodeJSONBody(t, body)
	id, _ := created["id"].(string)
	if created["source"] != "acme" || created["status"] != "pending" {
		t.Errorf("来源/状态 = %v/%v, want acme/pending", created["source"], created["status"])
	}
	if created["hasKey"] != false {
		t.Errorf("待签记录 hasKey = %v, want false(还没拿到私钥)", created["hasKey"])
	}
	if created["ca"] != "letsencrypt_staging" || created["dnsProviderId"] != "dns-1" || created["autoRenew"] != true {
		t.Errorf("回填的配置不对: %v", created)
	}
	if created["notAfter"] != "" || created["issuedAt"] != "" {
		t.Errorf("零值时间应回空串,拿到 %v / %v(前端会渲成「距今 6000 天」)", created["notAfter"], created["issuedAt"])
	}
	if _, ok := created["certPem"]; ok {
		t.Errorf("登记响应不该带正文字段: %v", created)
	}

	// 清单:{items} 形状,且不带正文 —— 列表页要的是几十张卡的元数据,不是 PEM。
	code, body = certCall(t, env.admin, env.adminCS, http.MethodGet, env.srv.URL+"/api/certs", "")
	if code != http.StatusOK {
		t.Fatalf("清单 = %d: %s", code, body)
	}
	items := certItems(t, body)
	if len(items) != 1 {
		t.Fatalf("清单 %d 条, want 1", len(items))
	}
	if strings.Contains(body, "certPem") {
		t.Errorf("清单里出现了 certPem 字段: %s", body)
	}

	// 详情:待签记录没有正文,字段应缺席(omitempty)而不是空串。
	code, body = certCall(t, env.admin, env.adminCS, http.MethodGet, env.srv.URL+"/api/certs/"+id, "")
	if code != http.StatusOK {
		t.Fatalf("详情 = %d: %s", code, body)
	}
	if _, ok := decodeJSONBody(t, body)["certPem"]; ok {
		t.Errorf("待签证书详情带了正文字段: %s", body)
	}

	if n, detail := auditRow(t, env.db, audit.ActionCertCreate); n != 1 {
		t.Errorf("cert_create 审计 %d 条, want 1", n)
	} else if !strings.Contains(detail, "shop.example.com") {
		t.Errorf("审计 detail 没留域名: %s", detail)
	}

	// 一个域名一条记录:重登记要撞 409,而不是静默多出一行。
	code, body = certCall(t, env.admin, env.adminCS, http.MethodPost, env.srv.URL+"/api/certs", certBody(t, map[string]any{
		"domain": "shop.example.com", "ca": "letsencrypt_staging", "dnsProviderId": "dns-1",
	}))
	if code != http.StatusConflict || decodeCode(t, decodeJSONBody(t, body)) != "domain_exists" {
		t.Errorf("重复域名 = %d %s, want 409 domain_exists", code, body)
	}

	code, body = certCall(t, env.admin, env.adminCS, http.MethodGet, env.srv.URL+"/api/certs/no-such-id", "")
	if code != http.StatusNotFound || decodeCode(t, decodeJSONBody(t, body)) != "cert_not_found" {
		t.Errorf("不存在的证书 = %d %s, want 404 cert_not_found", code, body)
	}
}

// TestCertCreate_RejectsBadConfig 验配置期就把错拦住:这些错误如果留到签发时才发现,
// 代价是 CA 侧的速率限制,不是重试一次。
func TestCertCreate_RejectsBadConfig(t *testing.T) {
	env := setupCertsAPI(t, true)
	cases := []struct {
		name       string
		body       map[string]any
		wantStatus int
		wantCode   string
	}{
		{"域名不合法", map[string]any{"domain": "Not A Domain", "ca": "letsencrypt_staging", "dnsProviderId": "dns-1"}, http.StatusBadRequest, "invalid_cert"},
		{"通配位置不对", map[string]any{"domain": "a.*.example.com", "ca": "letsencrypt_staging", "dnsProviderId": "dns-1"}, http.StatusBadRequest, "invalid_cert"},
		{"CA 不在名单", map[string]any{"domain": "ok.example.com", "ca": "my-bunny-ca", "dnsProviderId": "dns-1"}, http.StatusBadRequest, "invalid_cert"},
		{"没选 DNS 提供商", map[string]any{"domain": "ok.example.com", "ca": "letsencrypt_staging"}, http.StatusBadRequest, "invalid_cert"},
		{"提供商引用悬空", map[string]any{"domain": "ok.example.com", "ca": "letsencrypt_staging", "dnsProviderId": "dns-missing"}, http.StatusBadRequest, "invalid_cert"},
	}
	for _, tc := range cases {
		code, body := certCall(t, env.admin, env.adminCS, http.MethodPost, env.srv.URL+"/api/certs", certBody(t, tc.body))
		if code != tc.wantStatus || decodeCode(t, decodeJSONBody(t, body)) != tc.wantCode {
			t.Errorf("%s:= %d %s, want %d %s", tc.name, code, body, tc.wantStatus, tc.wantCode)
		}
	}
	if _, body := certCall(t, env.admin, env.adminCS, http.MethodGet, env.srv.URL+"/api/certs", ""); len(certItems(t, body)) != 0 {
		t.Errorf("全部被拒后清单应有 0 条: %s", body)
	}
}

// TestCertCAsList 验 CA 下拉与代码同源:页面能选的就是平台认识的几家,不多不少。
func TestCertCAsList(t *testing.T) {
	env := setupCertsAPI(t, true)
	code, body := certCall(t, env.admin, env.adminCS, http.MethodGet, env.srv.URL+"/api/certs/cas", "")
	if code != http.StatusOK {
		t.Fatalf("GET /certs/cas = %d: %s", code, body)
	}
	items := certItems(t, body)
	if len(items) != 5 {
		t.Fatalf("内置 CA %d 家, want 5: %s", len(items), body)
	}
	seen := map[string]bool{}
	for _, raw := range items {
		row := raw.(map[string]any)
		seen[row["id"].(string)] = true
		if row["directoryUrl"] == "" {
			t.Errorf("CA %v 没有 directoryUrl,前端无法提示用户这是哪端点", row["id"])
		}
	}
	for _, want := range []string{"letsencrypt", "letsencrypt_staging", "zerossl", "google", "sslcom"} {
		if !seen[want] {
			t.Errorf("名单缺 %s: %v", want, seen)
		}
	}
}

// —— 导入 / 替换 ————————————————————————————————————————

// TestCertImport_MapsPEMErrors 验领域错误到契约码的映射。表里每一条都是用户真实会粘错的形态:
// 粘成私钥、粘半截、正文与密钥不配对、域名对不上。
func TestCertImport_MapsPEMErrors(t *testing.T) {
	env := setupCertsAPI(t, true)
	good := newCertMaterial(t, "good.example.com", 21)
	other := newCertMaterial(t, "other.example.com", 22)

	cases := []struct {
		name       string
		body       map[string]any
		wantStatus int
		wantCode   string
	}{
		{"正文空", map[string]any{"domain": "a.example.com", "certPem": "", "keyPem": good.key}, http.StatusUnprocessableEntity, "invalid_pem"},
		{"没给私钥", map[string]any{"domain": "a.example.com", "certPem": good.cert, "keyPem": ""}, http.StatusUnprocessableEntity, "invalid_pem"},
		{"粘成了私钥", map[string]any{"domain": "a.example.com", "certPem": good.key, "keyPem": good.key}, http.StatusUnprocessableEntity, "invalid_pem"},
		{"私钥解不开", map[string]any{"domain": good.domain, "certPem": good.cert, "keyPem": "-----BEGIN PRIVATE KEY-----\nZZZZ\n-----END PRIVATE KEY-----"}, http.StatusUnprocessableEntity, "invalid_pem"},
		{"正文与密钥不配对", map[string]any{"domain": good.domain, "certPem": good.cert, "keyPem": other.key}, http.StatusUnprocessableEntity, "key_mismatch"},
		{"域名与正文不符", map[string]any{"domain": "elsewhere.example.com", "certPem": good.cert, "keyPem": good.key}, http.StatusUnprocessableEntity, "domain_mismatch"},
	}
	for _, tc := range cases {
		code, body := certCall(t, env.admin, env.adminCS, http.MethodPost, env.srv.URL+"/api/certs/import", certBody(t, tc.body))
		if code != tc.wantStatus || decodeCode(t, decodeJSONBody(t, body)) != tc.wantCode {
			t.Errorf("%s = %d %s, want %d %s", tc.name, code, body, tc.wantStatus, tc.wantCode)
		}
	}

	// 导入成功后再导同一个域名 → 409:换正文请走「替换」,别静默多出第二条记录。
	importCert(t, env, env.admin, env.adminCS, good)
	code, body := certCall(t, env.admin, env.adminCS, http.MethodPost, env.srv.URL+"/api/certs/import", certBody(t, map[string]any{
		"domain": good.domain, "certPem": good.cert, "keyPem": good.key,
	}))
	if code != http.StatusConflict || decodeCode(t, decodeJSONBody(t, body)) != "domain_exists" {
		t.Errorf("重复导入 = %d %s, want 409 domain_exists", code, body)
	}
}

// TestCertPrivateKeyOnlyLeavesViaBundle 是 AC-SEC-01 的门面测试:私钥只有一个出口。
func TestCertPrivateKeyOnlyLeavesViaBundle(t *testing.T) {
	env := setupCertsAPI(t, true)
	m := newCertMaterial(t, "secure.example.com", 31)
	id := importCert(t, env, env.admin, env.adminCS, m)

	// 详情:公钥材料可以回显,私钥不行。
	code, detailBody := certCall(t, env.admin, env.adminCS, http.MethodGet, env.srv.URL+"/api/certs/"+id, "")
	if code != http.StatusOK {
		t.Fatalf("详情 = %d: %s", code, detailBody)
	}
	assertNoPrivateKeyMaterial(t, "详情", detailBody, m.key)
	if !strings.Contains(detailBody, "BEGIN CERTIFICATE") {
		t.Errorf("详情没带回正文(前端「查看证书」按钮就是空的): %s", detailBody)
	}
	if decodeJSONBody(t, detailBody)["hasKey"] != true {
		t.Errorf("导入后 hasKey 应为 true: %s", detailBody)
	}

	// 清单 / 错误体 / 审计同样不许带出去。
	code, listBody := certCall(t, env.viewer, env.viewerCS, http.MethodGet, env.srv.URL+"/api/certs", "")
	if code != http.StatusOK {
		t.Fatalf("清单 = %d: %s", code, listBody)
	}
	assertNoPrivateKeyMaterial(t, "清单", listBody, m.key)

	// 错误路径:导入失败时不回显请求体(用户刚粘的私钥就在里面)。
	code, badBody := certCall(t, env.admin, env.adminCS, http.MethodPost, env.srv.URL+"/api/certs/import", certBody(t, map[string]any{
		"domain": "secure.example.com", "certPem": m.cert, "keyPem": m.key,
	}))
	if code != http.StatusConflict {
		t.Fatalf("重复导入 = %d, want 409: %s", code, badBody)
	}
	assertNoPrivateKeyMaterial(t, "错误体", badBody, m.key)

	if _, detail := auditRow(t, env.db, audit.ActionCertImport); detail != "" {
		assertNoPrivateKeyMaterial(t, "导入审计", detail, m.key)
	}

	// 证书包:唯一出口,要 cert.manage、要审计、要不落盘缓存。
	resp := doJSON(t, env.admin, http.MethodGet, env.srv.URL+"/api/certs/"+id+"/bundle", env.adminCS, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("取证书包 = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("证书包响应 Cache-Control = %q, want no-store", got)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读证书包: %v", err)
	}
	bundle := decodeJSONBody(t, string(raw))
	if bundle["privateKey"] != m.key {
		t.Errorf("证书包私钥与导入的不是同一把: %v", bundle["privateKey"])
	}
	if bundle["fullchain"] != m.cert+m.chain {
		t.Errorf("fullchain 不是「叶子+链」拼接: %v", bundle["fullchain"])
	}
	if n, detail := auditRow(t, env.db, audit.ActionCertBundleDownload); n != 1 {
		t.Errorf("cert_bundle_download 审计 %d 条, want 1(谁拿过私钥必须能复盘)", n)
	} else if strings.Contains(detail, "PRIVATE KEY") {
		t.Errorf("取包审计里出现了私钥材料: %s", detail)
	}

	// viewer 有 cert.view 没 cert.manage → 拿不到包。
	code, body := certCall(t, env.viewer, env.viewerCS, http.MethodGet, env.srv.URL+"/api/certs/"+id+"/bundle", "")
	if code != http.StatusForbidden {
		t.Errorf("viewer 取证书包 = %d, want 403: %s", code, body)
	}
}

// TestCertReplace_KeepsTargetsAndDowngradesSource 验「就地换正文」的两条语义:
// 落点配置一行都不动(换证书是常态,不该顺手把发布配置清掉);来源改写为 upload
// (平台再自动续期会把用户换的证书签掉,那比误签更糟)。
func TestCertReplace_KeepsTargetsAndDowngradesSource(t *testing.T) {
	env := setupCertsAPI(t, true)
	domain := "renew.example.com"
	id := createPendingCert(t, env, domain)
	targetID := addCertTarget(t, env, env.admin, env.adminCS, id, env.freeSrv, "renew")

	m := newCertMaterial(t, domain, 41)
	code, body := certCall(t, env.admin, env.adminCS, http.MethodPost, env.srv.URL+"/api/certs/"+id+"/replace", certBody(t, map[string]any{
		"domain": domain, "certPem": m.cert, "keyPem": m.key, "chainPem": m.chain,
	}))
	if code != http.StatusOK {
		t.Fatalf("替换 = %d, want 200: %s", code, body)
	}
	after := decodeJSONBody(t, body)
	if after["id"] != id {
		t.Errorf("替换换了 id(%v → %v),落点会成孤儿", id, after["id"])
	}
	if after["source"] != "upload" || after["autoRenew"] != false {
		t.Errorf("替换后 source/autoRenew = %v/%v, want upload/false(平台不能再代签它)", after["source"], after["autoRenew"])
	}
	assertNoPrivateKeyMaterial(t, "替换响应", body, m.key)

	code, body = certCall(t, env.admin, env.adminCS, http.MethodGet, env.srv.URL+"/api/certs/"+id+"/targets", "")
	if code != http.StatusOK || len(certItems(t, body)) != 1 {
		t.Fatalf("替换后落点应还在(%d 条): %s", len(certItems(t, body)), body)
	}
	if items := certItems(t, body); len(items) == 1 {
		if row := items[0].(map[string]any); row["id"] != targetID {
			t.Errorf("落点 id 变了: %v, want %v", row["id"], targetID)
		}
	}

	// 替换到不存在的证书 → 404,而不是 422 之类的「你以为有」
	code, body = certCall(t, env.admin, env.adminCS, http.MethodPost, env.srv.URL+"/api/certs/no-such/replace", certBody(t, map[string]any{
		"domain": domain, "certPem": m.cert, "keyPem": m.key,
	}))
	if code != http.StatusNotFound {
		t.Errorf("替换不存在的证书 = %d %s, want 404", code, body)
	}

	if _, detail := auditRow(t, env.db, audit.ActionCertReplace); strings.Contains(detail, "PRIVATE KEY") {
		t.Errorf("替换审计里出现了私钥材料: %s", detail)
	}
}

// —— 落点与数据轴 ————————————————————————————————————————

// TestCertTarget_CRUDContract 验落点这一层的契约:路径形态、主机存在性、URL 归属校验。
func TestCertTarget_CRUDContract(t *testing.T) {
	env := setupCertsAPI(t, true)
	id := importCert(t, env, env.admin, env.adminCS, newCertMaterial(t, "edge.example.com", 51))
	other := importCert(t, env, env.admin, env.adminCS, newCertMaterial(t, "other-edge.example.com", 52))

	code, body := certCall(t, env.admin, env.adminCS, http.MethodPost, env.srv.URL+"/api/certs/"+id+"/targets", certBody(t, map[string]any{
		"serverId": env.freeSrv, "certPath": "/etc/ssl/edge.pem", "keyPath": "/etc/ssl/edge.key",
		"postCommand": "nginx -s reload", "autoDeploy": false,
	}))
	if code != http.StatusCreated {
		t.Fatalf("新增落点 = %d, want 201: %s", code, body)
	}
	tid := decodeJSONBody(t, body)["id"].(string)
	if decodeJSONBody(t, body)["certId"] != id || decodeJSONBody(t, body)["serverId"] != env.freeSrv {
		t.Errorf("落点响应归属不对: %s", body)
	}
	if decodeJSONBody(t, body)["autoDeploy"] != false {
		t.Errorf("autoDeploy 回填错: %s", body)
	}

	for _, tc := range []struct {
		name     string
		body     map[string]any
		wantCode string
	}{
		{"相对路径", map[string]any{"serverId": env.freeSrv, "certPath": "edge.pem", "keyPath": "/etc/ssl/edge.key"}, "invalid_target"},
		{"正文与密钥同路径", map[string]any{"serverId": env.freeSrv, "certPath": "/etc/ssl/same", "keyPath": "/etc/ssl/same"}, "invalid_target"},
	} {
		c, b := certCall(t, env.admin, env.adminCS, http.MethodPost, env.srv.URL+"/api/certs/"+id+"/targets", certBody(t, tc.body))
		if c != http.StatusBadRequest && c != http.StatusUnprocessableEntity {
			t.Errorf("%s = %d %s, want 4xx", tc.name, c, b)
		}
		if decodeCode(t, decodeJSONBody(t, b)) != tc.wantCode {
			t.Errorf("%s 错误码 = %v, want %s", tc.name, b, tc.wantCode)
		}
	}

	// 主机不存在:数据轴(能不能操作那台机器)先于领域校验,所以这里出的是全站统一的
	// 404 not_found,而不是证书专属的 422 server_not_found —— 后者是未接权限服务时的兜底码。
	// 对不存在的 id 回 not_found 也不泄露这台机器到底有没有。
	c, b := certCall(t, env.admin, env.adminCS, http.MethodPost, env.srv.URL+"/api/certs/"+id+"/targets", certBody(t, map[string]any{
		"serverId": "no-such-server", "certPath": "/etc/ssl/a.pem", "keyPath": "/etc/ssl/a.key",
	}))
	if c != http.StatusNotFound || decodeCode(t, decodeJSONBody(t, b)) != "not_found" {
		t.Errorf("主机不存在 = %d %s, want 404 not_found", c, b)
	}

	// URL 里的证书与落点归属必须对上:PUT /certs/别的/targets/这个 要 404,
	// 否则拿一张证书的入口就能改到另一张的落点上去。
	c, b = certCall(t, env.admin, env.adminCS, http.MethodPut, env.srv.URL+"/api/certs/"+other+"/targets/"+tid, certBody(t, map[string]any{
		"serverId": env.freeSrv, "certPath": "/etc/ssl/hijack.pem", "keyPath": "/etc/ssl/hijack.key",
	}))
	if c != http.StatusNotFound {
		t.Fatalf("跨证书改落点 = %d %s, want 404", c, b)
	}

	// 改到同一张证书名下:换路径生效,最近一次结果保留(那是这台机器的历史)。
	c, b = certCall(t, env.admin, env.adminCS, http.MethodPut, env.srv.URL+"/api/certs/"+id+"/targets/"+tid, certBody(t, map[string]any{
		"serverId": env.freeSrv, "certPath": "/etc/ssl/edge-new.pem", "keyPath": "/etc/ssl/edge.key",
		"postCommand": "nginx -t && nginx -s reload", "autoDeploy": true,
	}))
	if c != http.StatusOK {
		t.Fatalf("修改落点 = %d %s, want 200", c, b)
	}
	if decodeJSONBody(t, b)["certPath"] != "/etc/ssl/edge-new.pem" || decodeJSONBody(t, b)["autoDeploy"] != true {
		t.Errorf("落点改动没生效: %s", b)
	}

	c, b = certCall(t, env.admin, env.adminCS, http.MethodDelete, env.srv.URL+"/api/certs/"+id+"/targets/"+tid, "")
	if c != http.StatusNoContent {
		t.Fatalf("删除落点 = %d %s, want 204", c, b)
	}
	c, b = certCall(t, env.admin, env.adminCS, http.MethodDelete, env.srv.URL+"/api/certs/"+id+"/targets/"+tid, "")
	if c != http.StatusNotFound {
		t.Errorf("重复删除 = %d %s, want 404", c, b)
	}
}

// TestCertTarget_DataAxis 是这道防线的正证:有 cert.manage 但不在那台机器的分组名册里,
// 就不许把私钥铺过去 —— 功能点管入口,分组管数据,两轴各自拦一件事。
func TestCertTarget_DataAxis(t *testing.T) {
	env := setupCertsAPI(t, true)
	id := importCert(t, env, env.admin, env.adminCS, newCertMaterial(t, "axis.example.com", 61))

	// una(role=user,有 cert.manage)对未归组主机可以加落点。
	c, b := certCall(t, env.normal, env.normalCS, http.MethodPost, env.srv.URL+"/api/certs/"+id+"/targets", certBody(t, map[string]any{
		"serverId": env.freeSrv, "certPath": "/etc/ssl/axis.pem", "keyPath": "/etc/ssl/axis.key",
	}))
	if c != http.StatusCreated {
		t.Fatalf("未归组主机加落点 = %d %s, want 201", c, b)
	}
	// 私有组内的主机(una 不在名册)→ 403,且不是 500 也不是 404。
	c, b = certCall(t, env.normal, env.normalCS, http.MethodPost, env.srv.URL+"/api/certs/"+id+"/targets", certBody(t, map[string]any{
		"serverId": env.secretSrv, "certPath": "/etc/ssl/axis.pem", "keyPath": "/etc/ssl/axis.key",
	}))
	if c != http.StatusForbidden {
		t.Fatalf("私有组主机加落点 = %d %s, want 403", c, b)
	}
	if env.pub.uploadCount() != 0 {
		t.Errorf("权限没过去却已经往目标机写了东西(%d 个上传)", env.pub.uploadCount())
	}
}

// —— 发布 ————————————————————————————————————————

// TestCertDeploy_PerTargetReport 验逐落点报告的形状与「先私钥后正文、权限先于改名」这条纪律。
func TestCertDeploy_PerTargetReport(t *testing.T) {
	env := setupCertsAPI(t, true)
	m := newCertMaterial(t, "pub.example.com", 71)
	id := importCert(t, env, env.admin, env.adminCS, m)
	addCertTarget(t, env, env.admin, env.adminCS, id, env.freeSrv, "pub-a")

	c, b := certCall(t, env.admin, env.adminCS, http.MethodPost, env.srv.URL+"/api/certs/"+id+"/deploy", "")
	if c != http.StatusOK {
		t.Fatalf("发布 = %d %s, want 200", c, b)
	}
	report := decodeJSONBody(t, b)
	if report["certId"] != id || report["deployed"] != float64(1) || report["failed"] != float64(0) {
		t.Errorf("发布报告计数不对: %s", b)
	}
	results, ok := report["results"].([]any)
	if !ok || len(results) != 1 {
		t.Fatalf("发布报告没有逐落点结果: %s", b)
	}
	row := results[0].(map[string]any)
	if row["ok"] != true || row["error"] != "" || row["serverId"] != env.freeSrv {
		t.Errorf("逐落点结果不对: %v", row)
	}
	if row["deployedAt"] == "" {
		t.Errorf("成功却没记发布时间: %v", row)
	}

	// 内容纪律:私钥与「叶子+链」各自被上传过(落位走临时名,所以按内容找),
	// 且私钥那份是 0600、证书那份是 0644。
	if !env.pub.saw(m.key) {
		t.Errorf("目标机上没收到私钥内容(上传记录 %d 条)", env.pub.uploadCount())
	}
	if !env.pub.saw(m.cert + m.chain) {
		t.Errorf("目标机上没收到 fullchain 正文")
	}
	if !env.pub.ran("chmod 0600") || !env.pub.ran("chmod 0644") {
		t.Errorf("没按私钥 0600 / 正文 0644 设权限: %v", env.pub.cmds)
	}
	if !env.pub.ran("mv -f") {
		t.Errorf("没走「临时名 → mv 落位」: %v", env.pub.cmds)
	}
	// 用户填的生效指令逐字进 sh -c,平台不代替用户猜服务名。
	if !env.pub.ran("systemctl reload nginx") {
		t.Errorf("用户填的生效指令没被执行: %v", env.pub.cmds)
	}

	// 落点上的最近一次结果回写到配置行,列表页直接能显示「上次发布成功」。
	c, b = certCall(t, env.admin, env.adminCS, http.MethodGet, env.srv.URL+"/api/certs/"+id+"/targets", "")
	if c != http.StatusOK {
		t.Fatalf("发布后读落点 = %d: %s", c, b)
	}
	if row := certItems(t, b)[0].(map[string]any); row["lastStatus"] != "success" {
		t.Errorf("落点 lastStatus = %v, want success", row["lastStatus"])
	}
	if n, _ := auditRow(t, env.db, audit.ActionCertDeploy); n != 1 {
		t.Errorf("cert_deploy 审计 %d 条, want 1", n)
	}
}

// TestCertDeploy_RefusesWhenAnyTargetIsOutOfWorkspace 是「宁可整批不发」那条纪律:
// 只要有一个落点不在 actor 的操作范围内,整次发布都不许开始 —— 半途而废会在别人机器上
// 留下一份库里没有对应记录的私钥。
func TestCertDeploy_RefusesWhenAnyTargetIsOutOfWorkspace(t *testing.T) {
	env := setupCertsAPI(t, true)
	m := newCertMaterial(t, "batch.example.com", 81)
	id := importCert(t, env, env.admin, env.adminCS, m)
	addCertTarget(t, env, env.admin, env.adminCS, id, env.freeSrv, "batch-a")
	addCertTarget(t, env, env.admin, env.adminCS, id, env.secretSrv, "batch-b") // 管理员建的,una 管不着

	c, b := certCall(t, env.normal, env.normalCS, http.MethodPost, env.srv.URL+"/api/certs/"+id+"/deploy", "")
	if c != http.StatusForbidden {
		t.Fatalf("una 发布 = %d %s, want 403", c, b)
	}
	if env.pub.uploadCount() != 0 {
		t.Errorf("被拒的发布仍然写了目标机(%d 个上传),这是密钥泄漏事故", env.pub.uploadCount())
	}
}

// TestCertDeploy_WithoutPublisherIs503 验装配缺失说清楚:不是「发布失败」,是这台实例发不了。
func TestCertDeploy_WithoutPublisherIs503(t *testing.T) {
	env := setupCertsAPI(t, false)
	m := newCertMaterial(t, "nopub.example.com", 91)
	id := importCert(t, env, env.admin, env.adminCS, m)
	addCertTarget(t, env, env.admin, env.adminCS, id, env.freeSrv, "nopub")

	c, b := certCall(t, env.admin, env.adminCS, http.MethodPost, env.srv.URL+"/api/certs/"+id+"/deploy", "")
	if c != http.StatusServiceUnavailable || decodeCode(t, decodeJSONBody(t, b)) != "publisher_unconfigured" {
		t.Fatalf("没接执行能力时发布 = %d %s, want 503 publisher_unconfigured", c, b)
	}
}

// —— 签发 / 开关 / 删除 ——————————————————————————————————

// TestCertIssue_MapsMissingCapabilities 验签发路径的三种「做不了」各自说清是哪一件:
// 没接 DNS 写入(装配缺失)、这张不是平台签的(来源不对)、记录不存在。
func TestCertIssue_MapsMissingCapabilities(t *testing.T) {
	env := setupCertsAPI(t, true)
	id := createPendingCert(t, env, "issue.example.com")

	c, b := certCall(t, env.admin, env.adminCS, http.MethodPost, env.srv.URL+"/api/certs/"+id+"/issue", "")
	if c != http.StatusServiceUnavailable || decodeCode(t, decodeJSONBody(t, b)) != "dns_writer_unconfigured" {
		t.Fatalf("未接 TXT 写入时签发 = %d %s, want 503 dns_writer_unconfigured", c, b)
	}
	// 失败也要留痕:谁在什么时候点了签发、为什么没成。
	if n, detail := auditRow(t, env.db, audit.ActionCertIssue); n != 1 {
		t.Errorf("cert_issue 审计 %d 条, want 1", n)
	} else if strings.Contains(detail, `"ok":true`) {
		t.Errorf("签发失败却记了成功: %s", detail)
	}
	if _, detail := auditRow(t, env.db, audit.ActionCertIssue); !strings.Contains(detail, "TXT writer") {
		t.Errorf("审计原因里看不出缺的是 DNS 写入能力: %s", detail)
	}

	// 导入的证书平台不能代签(既没有该 CA 的域名控制权证明,也不该把用户的证书签掉)。
	imported := importCert(t, env, env.admin, env.adminCS, newCertMaterial(t, "upload-only.example.com", 101))
	c, b = certCall(t, env.admin, env.adminCS, http.MethodPost, env.srv.URL+"/api/certs/"+imported+"/issue", "")
	if c != http.StatusConflict || decodeCode(t, decodeJSONBody(t, b)) != "not_acme" {
		t.Errorf("对导入证书签发 = %d %s, want 409 not_acme", c, b)
	}

	c, b = certCall(t, env.admin, env.adminCS, http.MethodPost, env.srv.URL+"/api/certs/no-such/issue", "")
	if c != http.StatusNotFound {
		t.Errorf("签发不存在的证书 = %d %s, want 404", c, b)
	}
}

func TestCertAutoRenewAndDelete(t *testing.T) {
	env := setupCertsAPI(t, true)
	id := createPendingCert(t, env, "toggle.example.com") // 登记时 autoRenew=true

	c, b := certCall(t, env.admin, env.adminCS, http.MethodPost, env.srv.URL+"/api/certs/"+id+"/auto-renew", `{"autoRenew":false}`)
	if c != http.StatusOK || decodeJSONBody(t, b)["autoRenew"] != false {
		t.Fatalf("关自动续期 = %d %s, want 200 autoRenew=false", c, b)
	}
	if n, detail := auditRow(t, env.db, audit.ActionCertAutoRenew); n != 1 {
		t.Errorf("cert_auto_renew 审计 %d 条, want 1(平台会周期性拿 DNS 权限去签新证书,得能查到是谁开的)", n)
	} else if !strings.Contains(detail, "toggle.example.com") {
		t.Errorf("续期开关审计没留域名: %s", detail)
	}

	c, b = certCall(t, env.admin, env.adminCS, http.MethodPost, env.srv.URL+"/api/certs/"+id+"/auto-renew", `{"notJson`)
	if c != http.StatusBadRequest || decodeCode(t, decodeJSONBody(t, b)) != "bad_request" {
		t.Errorf("坏请求体 = %d %s, want 400 bad_request", c, b)
	}

	c, b = certCall(t, env.admin, env.adminCS, http.MethodDelete, env.srv.URL+"/api/certs/"+id, "")
	if c != http.StatusNoContent {
		t.Fatalf("删除证书 = %d %s, want 204", c, b)
	}
	if n, detail := auditRow(t, env.db, audit.ActionCertDelete); n != 1 {
		t.Errorf("cert_delete 审计 %d 条, want 1", n)
	} else if !strings.Contains(detail, "toggle.example.com") {
		t.Errorf("删除审计必须留域名(删完就再也读不回来了): %s", detail)
	}
	c, b = certCall(t, env.admin, env.adminCS, http.MethodGet, env.srv.URL+"/api/certs/"+id, "")
	if c != http.StatusNotFound {
		t.Errorf("删除后详情 = %d %s, want 404", c, b)
	}
	// 落点随行级联删除:再读那张证书的落点应是空清单(证书已没了 → 404 也算收口)。
	c, _ = certCall(t, env.admin, env.adminCS, http.MethodGet, env.srv.URL+"/api/certs/"+id+"/targets", "")
	if c != http.StatusNotFound && c != http.StatusOK {
		t.Errorf("删除后读落点 = %d, want 404 或 200 空清单", c)
	}
}

// TestCertRenewWindow 验「到期前 N 天自动续期」:登记带得进、改得动、读得回,越界与导入来源当场拒。
// 这个数决定平台什么时候去 CA 下一单,所以改它必须有一条审计 —— 和开关同理。
func TestCertRenewWindow(t *testing.T) {
	env := setupCertsAPI(t, true)

	code, body := certCall(t, env.admin, env.adminCS, http.MethodPost, env.srv.URL+"/api/certs", certBody(t, map[string]any{
		"domain": "window.example.com", "ca": "letsencrypt_staging", "dnsProviderId": "dns-1",
		"autoRenew": true, "renewBeforeDays": 45,
	}))
	if code != http.StatusCreated {
		t.Fatalf("登记 = %d %s, want 201", code, body)
	}
	created := decodeJSONBody(t, body)
	id, _ := created["id"].(string)
	if created["renewBeforeDays"] != float64(45) {
		t.Fatalf("登记响应 renewBeforeDays = %v, want 45", created["renewBeforeDays"])
	}
	// 列表也带这个字段:页面上的阈值回显读的是列表那一趟。
	code, body = certCall(t, env.admin, env.adminCS, http.MethodGet, env.srv.URL+"/api/certs", "")
	if code != http.StatusOK || !strings.Contains(body, `"renewBeforeDays":45`) {
		t.Fatalf("列表未回显阈值: %d %s", code, body)
	}

	code, body = certCall(t, env.admin, env.adminCS, http.MethodPost, env.srv.URL+"/api/certs/"+id+"/renew-window", `{"renewBeforeDays":7}`)
	if code != http.StatusOK || decodeJSONBody(t, body)["renewBeforeDays"] != float64(7) {
		t.Fatalf("改阈值 = %d %s, want 200 renewBeforeDays=7", code, body)
	}
	if n, detail := auditRow(t, env.db, audit.ActionCertRenewWindow); n != 1 {
		t.Errorf("cert_renew_window 审计 %d 条, want 1", n)
	} else if !strings.Contains(detail, "window.example.com") {
		t.Errorf("阈值审计没留域名: %s", detail)
	}

	// 0 = 交回默认窗口(页面显示「30 天(默认)」)。
	if code, body = certCall(t, env.admin, env.adminCS, http.MethodPost, env.srv.URL+"/api/certs/"+id+"/renew-window", `{"renewBeforeDays":0}`); code != http.StatusOK ||
		decodeJSONBody(t, body)["renewBeforeDays"] != float64(0) {
		t.Fatalf("恢复默认 = %d %s, want 200 renewBeforeDays=0", code, body)
	}

	for _, bad := range []string{`{"renewBeforeDays":400}`, `{"renewBeforeDays":-1}`} {
		code, body = certCall(t, env.admin, env.adminCS, http.MethodPost, env.srv.URL+"/api/certs/"+id+"/renew-window", bad)
		if code != http.StatusBadRequest || decodeCode(t, decodeJSONBody(t, body)) != "invalid_cert" {
			t.Errorf("越界 %s = %d %s, want 400 invalid_cert", bad, code, body)
		}
	}

	imported := importCert(t, env, env.admin, env.adminCS, newCertMaterial(t, "window-upload.example.com", 112))
	code, body = certCall(t, env.admin, env.adminCS, http.MethodPost, env.srv.URL+"/api/certs/"+imported+"/renew-window", `{"renewBeforeDays":7}`)
	if code != http.StatusConflict || decodeCode(t, decodeJSONBody(t, body)) != "not_acme" {
		t.Errorf("给导入证书设阈值 = %d %s, want 409 not_acme", code, body)
	}
}

// TestCertTargets_CascadeOnDelete 验删证书把落点配置一并带走(领域层级联,HTTP 层要能观测到)。
func TestCertTargets_CascadeOnDelete(t *testing.T) {
	env := setupCertsAPI(t, true)
	id := importCert(t, env, env.admin, env.adminCS, newCertMaterial(t, "cascade.example.com", 111))
	addCertTarget(t, env, env.admin, env.adminCS, id, env.freeSrv, "cascade")

	var before int
	if err := env.db.QueryRow(`SELECT COUNT(1) FROM cert_deployments WHERE cert_id = ?`, id).Scan(&before); err != nil {
		t.Fatalf("数落点: %v", err)
	}
	if before != 1 {
		t.Fatalf("落点数 = %d, want 1", before)
	}
	if c, b := certCall(t, env.admin, env.adminCS, http.MethodDelete, env.srv.URL+"/api/certs/"+id, ""); c != http.StatusNoContent {
		t.Fatalf("删除证书 = %d %s, want 204", c, b)
	}
	var after int
	if err := env.db.QueryRow(`SELECT COUNT(1) FROM cert_deployments WHERE cert_id = ?`, id).Scan(&after); err != nil {
		t.Fatalf("数落点(删后): %v", err)
	}
	if after != 0 {
		t.Errorf("删证书后残留 %d 条落点配置,级联没生效", after)
	}
}

// —— ACME 账户 ————————————————————————————————————————

// TestCertAccounts_EABRulesAndWriteOnlyKey 验账户注册:需要 EAB 的 CA 没给就当场拒,
// 而 EAB key 是只写字段 —— 响应里只有「有没有」,绝不回正文。
// (这几条分支都在出网之前,所以测试不会真去访问任何 CA。)
func TestCertAccounts_EABRulesAndWriteOnlyKey(t *testing.T) {
	env := setupCertsAPI(t, true)

	c, b := certCall(t, env.viewer, env.viewerCS, http.MethodPost, env.srv.URL+"/api/certs/accounts", `{"ca":"letsencrypt_staging"}`)
	if c != http.StatusForbidden {
		t.Fatalf("viewer 注册账户 = %d %s, want 403", c, b)
	}

	cases := []struct {
		name       string
		body       map[string]any
		wantStatus int
		wantCode   string
	}{
		{"CA 不在名单", map[string]any{"ca": "my-bunny-ca"}, http.StatusBadRequest, "invalid_cert"},
		{"ZeroSSL 不给 EAB", map[string]any{"ca": "zerossl", "contact": "ops@example.com"}, http.StatusUnprocessableEntity, "eab_required"},
		{"EAB key 不是 base64", map[string]any{"ca": "zerossl", "eabKeyId": "kid-1", "eabKey": "@@@@ 不是 base64 @@@"}, http.StatusBadRequest, "invalid_eab"},
	}
	for _, tc := range cases {
		c, b = certCall(t, env.admin, env.adminCS, http.MethodPost, env.srv.URL+"/api/certs/accounts", certBody(t, tc.body))
		if c != tc.wantStatus || decodeCode(t, decodeJSONBody(t, b)) != tc.wantCode {
			t.Errorf("%s = %d %s, want %d %s", tc.name, c, b, tc.wantStatus, tc.wantCode)
		}
	}

	// 清单形状与「无账户」这一初始态。
	c, b = certCall(t, env.admin, env.adminCS, http.MethodGet, env.srv.URL+"/api/certs/accounts", "")
	if c != http.StatusOK {
		t.Fatalf("账户清单 = %d: %s", c, b)
	}
	if len(certItems(t, b)) != 0 {
		t.Errorf("上面全部被拒,不该有账户落库: %s", b)
	}
	for _, banned := range []string{`"eabKey"`, "PRIVATE KEY"} {
		if strings.Contains(b, banned) {
			t.Errorf("账户清单出现了只写字段或密钥材料 %q: %s", banned, b)
		}
	}
}

// TestCertListOrdering 验清单口径:到期近的排前面(页面第一屏要回答的是「哪张快过期」)。
func TestCertListOrdering(t *testing.T) {
	env := setupCertsAPI(t, true)
	importCert(t, env, env.admin, env.adminCS, newCertMaterial(t, "long.example.com", 121))
	// 待签记录没有到期时间,排在最前(它是最该被处理的一条)。
	createPendingCert(t, env, "pending.example.com")

	c, b := certCall(t, env.admin, env.adminCS, http.MethodGet, env.srv.URL+"/api/certs", "")
	if c != http.StatusOK {
		t.Fatalf("清单 = %d: %s", c, b)
	}
	items := certItems(t, b)
	if len(items) != 2 {
		t.Fatalf("清单 %d 条, want 2", len(items))
	}
	if got := items[0].(map[string]any)["domain"]; got != "pending.example.com" {
		t.Errorf("第一条 = %v, want 待签的那张(没有到期时间的排最前)", got)
	}
}
