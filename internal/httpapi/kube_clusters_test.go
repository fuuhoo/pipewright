// kube_clusters_test.go 守的是集群这条腿的 HTTP 契约与凭据纪律(AC-SEC-01):
// 响应、错误体、审计里都不得出现 kubeconfig 正文或其 token;领域错误一律映射成
// 契约错误码,而不是把内部串原样抛给界面。
package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/kube"
	"github.com/fuuhoo/pipewright/internal/vault"
)

// kubeTokenMarker 冒充 SA token。它一旦出现在任何响应里就是泄漏,断言到处复用它。
const kubeTokenMarker = "LEAKMARKER-sa-token-abcdef"

// kubeConfigDoc 是一份能用的最小 kubeconfig:server 指向调用方给的地址。
func kubeConfigDoc(server string) string {
	return `apiVersion: v1
kind: Config
current-context: c1
clusters:
- name: k1
  cluster:
    server: ` + server + `
users:
- name: u1
  user:
    token: ` + kubeTokenMarker + `
contexts:
- name: c1
  context: {cluster: k1, user: u1, namespace: shop}
`
}

// setupKubeAPI 装配 vault + 集群服务(不装 access → 可见性不收窄,专注测契约与泄漏)。
func setupKubeAPI(t *testing.T) (*httptest.Server, *http.Client, string, vault.Vault) {
	t.Helper()
	st := testStoreAuth(t)
	svc := auth.NewService(st.DB, nil, nil)
	if err := svc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	v := vault.New(st.DB, testMasterKey())
	srv := httptest.NewServer(New(testWebFSAuth(), svc, WithVault(v), WithKubeClusters(kube.New(st.DB, v))))
	t.Cleanup(srv.Close)

	client := newTestClient(t)
	csrf := loginWithClient(t, client, srv.URL)
	return srv, client, csrf, v
}

func newKubeCred(t *testing.T, v vault.Vault, name, credType, secret string) string {
	t.Helper()
	c, err := v.Create(vault.CreateInput{Type: credType, Name: name, Secret: secret})
	if err != nil {
		t.Fatalf("create credential %s: %v", name, err)
	}
	return c.ID
}

// kubeBody 读完即关(包里的 readAll 不关,而这里每个用例都发好几个请求)。
func kubeBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(raw)
}

// assertNoKubeSecret 断言这段文本里没有凭据明文,也没有承载明文的字段名。
func assertNoKubeSecret(t *testing.T, label, body string) {
	t.Helper()
	if strings.Contains(body, kubeTokenMarker) {
		t.Fatalf("%s 泄漏 kubeconfig 凭据明文: %s", label, body)
	}
	for _, banned := range []string{"\"secret\"", "\"ciphertext\"", "\"kubeconfig\"", "\"token\"", "\"clientKeyData\"", "\"certData\""} {
		if strings.Contains(body, banned) {
			t.Fatalf("%s 响应含禁止字段 %s: %s", label, banned, body)
		}
	}
}

func TestKubeClusterCRUDContractAndNoLeak(t *testing.T) {
	srv, client, csrf, v := setupKubeAPI(t)
	credID := newKubeCred(t, v, "prod-kube", vault.TypeKubeConfig, kubeConfigDoc("https://10.0.0.9:6443"))

	// 登记 → 201,endpoint 从 kubeconfig 现读出来(库里不存这份地址)。
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/kube-clusters", csrf,
		`{"name":"prod","credentialId":"`+credID+`","namespaceDefault":"shop"}`)
	created := kubeBody(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201: %s", resp.StatusCode, created)
	}
	assertNoKubeSecret(t, "create", created)
	var c map[string]any
	if err := json.Unmarshal([]byte(created), &c); err != nil {
		t.Fatalf("create body 不是 JSON: %v", err)
	}
	id, _ := c["id"].(string)
	if id == "" {
		t.Fatalf("响应无 id: %s", created)
	}
	if c["endpoint"] != "https://10.0.0.9:6443" {
		t.Errorf("endpoint = %v, want 从 kubeconfig 读出的地址", c["endpoint"])
	}
	if c["credentialName"] != "prod-kube" {
		t.Errorf("credentialName = %v, want join 出的凭据名", c["credentialName"])
	}
	if c["namespaceDefault"] != "shop" {
		t.Errorf("namespaceDefault = %v, want shop", c["namespaceDefault"])
	}

	// 列表 → { items: [...] }
	lresp := doJSON(t, client, http.MethodGet, srv.URL+"/api/kube-clusters", csrf, "")
	lraw := kubeBody(t, lresp)
	assertNoKubeSecret(t, "list", lraw)
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(lraw), &list); err != nil {
		t.Fatalf("list body 不是 JSON: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("list len = %d, want 1: %s", len(list.Items), lraw)
	}

	// 详情
	gresp := doJSON(t, client, http.MethodGet, srv.URL+"/api/kube-clusters/"+id, csrf, "")
	graw := kubeBody(t, gresp)
	assertNoKubeSecret(t, "get", graw)
	if gresp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d: %s", gresp.StatusCode, graw)
	}

	// 改名 + 改命名空间
	uresp := doJSON(t, client, http.MethodPut, srv.URL+"/api/kube-clusters/"+id, csrf,
		`{"name":"prod-2","namespaceDefault":"checkout"}`)
	uraw := kubeBody(t, uresp)
	assertNoKubeSecret(t, "update", uraw)
	if uresp.StatusCode != http.StatusOK {
		t.Fatalf("update status = %d: %s", uresp.StatusCode, uraw)
	}
	if !strings.Contains(uraw, `"name":"prod-2"`) || !strings.Contains(uraw, `"namespaceDefault":"checkout"`) {
		t.Errorf("update 未生效: %s", uraw)
	}

	// 删除 → 204,再取 → 404 cluster_not_found(不是 500)。
	dresp := doJSON(t, client, http.MethodDelete, srv.URL+"/api/kube-clusters/"+id, csrf, "")
	kubeBody(t, dresp)
	if dresp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", dresp.StatusCode)
	}
	aresp := doJSON(t, client, http.MethodGet, srv.URL+"/api/kube-clusters/"+id, csrf, "")
	araw := kubeBody(t, aresp)
	if aresp.StatusCode != http.StatusNotFound || !strings.Contains(araw, "cluster_not_found") {
		t.Fatalf("删除后取 = %d %s, want 404 cluster_not_found", aresp.StatusCode, araw)
	}
}

func TestKubeClusterValidationMapsToContractErrors(t *testing.T) {
	srv, client, csrf, v := setupKubeAPI(t)
	goodCred := newKubeCred(t, v, "ok-kube", vault.TypeKubeConfig, kubeConfigDoc("https://10.0.0.9:6443"))
	sshCred := newKubeCred(t, v, "not-a-kubeconfig", vault.TypeSSHKey, "-----BEGIN OPENSSH PRIVATE KEY-----\njunk\n")

	cases := []struct {
		name           string
		body           string
		wantStatus     int
		wantCodeInBody string
	}{
		{"空名", `{"name":"  ","credentialId":"` + goodCred + `"}`, http.StatusBadRequest, "invalid_cluster"},
		{"无凭据", `{"name":"c1"}`, http.StatusBadRequest, "invalid_cluster"},
		{"命名空间非法", `{"name":"c1","credentialId":"` + goodCred + `","namespaceDefault":"Shop_1"}`, http.StatusBadRequest, "invalid_cluster"},
		{"凭据不存在", `{"name":"c1","credentialId":"00000000-0000-4000-8000-000000000000"}`, http.StatusUnprocessableEntity, "credential_error"},
		// 拿错文件不能静静登记成功:那会把病根推到三个月后一次莫名其妙的「集群连不上」。
		{"凭据不是 kubeconfig", `{"name":"c1","credentialId":"` + sshCred + `"}`, http.StatusUnprocessableEntity, "kubeconfig_invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/kube-clusters", csrf, tc.body)
			raw := kubeBody(t, resp)
			assertNoKubeSecret(t, "create/"+tc.name, raw)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", resp.StatusCode, tc.wantStatus, raw)
			}
			if !strings.Contains(raw, tc.wantCodeInBody) {
				t.Fatalf("错误码应为 %q: %s", tc.wantCodeInBody, raw)
			}
		})
	}
}

// 测试连接的两条出口都要走 200:连得上给版本号,连不上给 ok=false + 人读原因。
// 502/500 会把「这个集群现在不通」当成接口故障,界面上只剩一句服务器内部错误。
func TestKubeClusterTestEndpointReportsBothOutcomes(t *testing.T) {
	deny := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"kind":"Status","message":"unauthorized"}`, http.StatusUnauthorized)
	}))
	defer deny.Close()

	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+kubeTokenMarker {
			t.Errorf("假 API server 收到的 Authorization = %q", got)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, `{"gitVersion":"v1.29.2"}`)
	}))
	defer ok.Close()

	srv, client, csrf, v := setupKubeAPI(t)
	for _, tc := range []struct {
		name     string
		server   string
		wantOK   bool
		wantPart string
	}{
		{"连得上", ok.URL, true, "v1.29.2"},
		{"凭据被拒", deny.URL, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			credID := newKubeCred(t, v, "kube-"+tc.name, vault.TypeKubeConfig, kubeConfigDoc(tc.server))
			resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/kube-clusters", csrf,
				`{"name":"`+tc.name+`","credentialId":"`+credID+`"}`)
			raw := kubeBody(t, resp)
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("create status = %d: %s", resp.StatusCode, raw)
			}
			var created map[string]any
			_ = json.Unmarshal([]byte(raw), &created)
			id, _ := created["id"].(string)

			tresp := doJSON(t, client, http.MethodPost, srv.URL+"/api/kube-clusters/"+id+"/test", csrf, "")
			traw := kubeBody(t, tresp)
			assertNoKubeSecret(t, "test", traw)
			if tresp.StatusCode != http.StatusOK {
				t.Fatalf("test status = %d, want 200: %s", tresp.StatusCode, traw)
			}
			var res struct {
				OK     bool    `json:"ok"`
				Output string  `json:"output"`
				Error  *string `json:"error"`
			}
			if err := json.Unmarshal([]byte(traw), &res); err != nil {
				t.Fatalf("test body 不是 JSON: %v (%s)", err, traw)
			}
			if res.OK != tc.wantOK {
				t.Fatalf("ok = %v, want %v: %s", res.OK, tc.wantOK, traw)
			}
			if tc.wantOK && !strings.Contains(res.Output, tc.wantPart) {
				t.Errorf("output = %q, want 含 %q", res.Output, tc.wantPart)
			}
			if !tc.wantOK && (res.Error == nil || strings.TrimSpace(*res.Error) == "") {
				t.Errorf("连不上必须给人读原因,得到: %s", traw)
			}
		})
	}
}

// 未装配集群服务(关了这条腿)时接口要 503 说清楚,而不是 500 或空列表装成「没有集群」。
func TestKubeClustersUnavailableWithoutService(t *testing.T) {
	st := testStoreAuth(t)
	svc := auth.NewService(st.DB, nil, nil)
	if err := svc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	srv := httptest.NewServer(New(testWebFSAuth(), svc))
	t.Cleanup(srv.Close)
	client := newTestClient(t)
	csrf := loginWithClient(t, client, srv.URL)

	resp := doJSON(t, client, http.MethodGet, srv.URL+"/api/kube-clusters", csrf, "")
	raw := kubeBody(t, resp)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", resp.StatusCode, raw)
	}
}
