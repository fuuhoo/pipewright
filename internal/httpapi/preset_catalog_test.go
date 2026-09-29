// 预置目录读取端点(v6.2 R4/R13:流水线只能从预置目录选):
//
//	GET /api/build-envs     → 仅已启用环境,且不回运维字段
//	GET /api/config-profiles → 仅已启用配置,且不回宿主文件路径
//
// 关键不变式:普通用户(role=user)拿到 200,且看不到 disabled 条目 —— 否则被管理员
// 下架的环境会继续出现在流水线下拉里。
package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huangchengsir/pipewright/internal/audit"
	"github.com/huangchengsir/pipewright/internal/auth"
	"github.com/huangchengsir/pipewright/internal/buildenv"
	"github.com/huangchengsir/pipewright/internal/configprofile"
	"github.com/huangchengsir/pipewright/internal/mask"
	"github.com/huangchengsir/pipewright/internal/storetest"
	"github.com/huangchengsir/pipewright/internal/users"
	"github.com/huangchengsir/pipewright/internal/vault"
)

// setupPresetServer 构造带 buildenv/configprofile 服务的 server,并预置一个普通用户。
func setupPresetServer(t *testing.T) (*httptest.Server, *http.Client) {
	t.Helper()
	st := storetest.Open(t)
	svc := auth.NewService(st.DB, nil, users.NewService(st.DB))
	if err := svc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	loadRoleCatalog(t, st.DB) // alice 是 'user':不装载目录她的会话会在 RequireUser 上 401
	hash, err := auth.HashPassword("alice-pass-1234")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if _, err := st.DB.Exec(
		`INSERT INTO users (id, username, password_hash, role, enabled, created_at, updated_at)
		 VALUES (?, 'alice', ?, 'user', 1, ?, ?)`,
		"aaaaaaaa-1111-1111-1111-111111111111", hash,
		"2024-01-01T00:00:00Z", "2024-01-01T00:00:00Z",
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	bSvc := buildenv.NewService(buildenv.NewSQLiteRepo(st.DB))
	cpSvc := configprofile.NewService(configprofile.NewSQLiteRepo(st.DB), t.TempDir())
	srv := httptest.NewServer(New(testWebFSAuth(), svc,
		WithVault(vault.New(st.DB, testMasterKey())),
		WithAudit(audit.New(st.DB, mask.NewMasker(), nil)),
		WithBuildEnvs(bSvc, nil),
		WithConfigProfiles(cpSvc),
		WithUsers(users.NewService(st.DB)),
	))
	t.Cleanup(srv.Close)
	return srv, newTestClient(t)
}

// loginAs 登录并返回 csrf;username/password 任意角色。
func loginAs(t *testing.T, client *http.Client, srvURL, username, password string) string {
	t.Helper()
	body := strings.NewReader(`{"username":"` + username + `","password":"` + password + `"}`)
	resp, err := client.Post(srvURL+"/api/auth/login", "application/json", body)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login %s status = %d, want 200", username, resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == cookieCsrf {
			return c.Value
		}
	}
	t.Fatal("登录后无 csrf cookie")
	return ""
}

// TestPresetCatalog_EnabledOnlyAndSanitized 验证普通用户看到的预置目录:
// 只含已启用条目,且不含 credentialId / imageCheckError / createdBy / filePath。
func TestPresetCatalog_EnabledOnlyAndSanitized(t *testing.T) {
	srv, admin := setupPresetServer(t)
	adminCsrf := loginAs(t, admin, srv.URL, "admin", "testpass")

	// 两个环境:一个启用(带凭据 + 检查失败文本),一个禁用。
	resp := doJSON(t, admin, http.MethodPost, srv.URL+"/api/admin/build-envs", adminCsrf,
		`{"language":"node","version":"20","displayName":"Node 20","sourceType":"custom","image":"registry.internal/node:20","credentialId":"cred-1","enabled":true}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create env(启用)status = %d: %s", resp.StatusCode, readAll(t, resp))
	}
	resp2 := doJSON(t, admin, http.MethodPost, srv.URL+"/api/admin/build-envs", adminCsrf,
		`{"language":"node","version":"18","displayName":"Node 18(已下架)","sourceType":"official","image":"node:18-alpine","enabled":false}`)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusCreated {
		t.Fatalf("create env(禁用)status = %d", resp2.StatusCode)
	}

	resp3 := doJSON(t, admin, http.MethodPost, srv.URL+"/api/admin/config-profiles", adminCsrf,
		`{"language":"node","configType":"npmrc","name":".npmrc","targetPath":"/root/.npmrc","content":"registry=https://internal","enabled":true}`)
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusCreated {
		t.Fatalf("create profile status = %d: %s", resp3.StatusCode, readAll(t, resp3))
	}

	// 换成普通用户身份。
	alice := newTestClient(t)
	aliceCsrf := loginAs(t, alice, srv.URL, "alice", "alice-pass-1234")

	raw := readAll(t, mustGet(t, alice, srv.URL+"/api/build-envs", aliceCsrf))
	if strings.Contains(raw, "cred-1") || strings.Contains(raw, "Node 18") {
		t.Fatalf("普通用户 build-envs 泄漏运维字段或禁用条目: %s", raw)
	}
	var envs struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &envs); err != nil {
		t.Fatalf("decode: %v (%s)", err, raw)
	}
	if len(envs.Items) != 1 {
		t.Fatalf("只应看到 1 个已启用环境, got %d: %s", len(envs.Items), raw)
	}
	got := envs.Items[0]
	if got["image"] != "registry.internal/node:20" || got["language"] != "node" {
		t.Fatalf("预置条目字段错: %+v", got)
	}
	for _, k := range []string{"credentialId", "imageCheckError", "createdBy"} {
		if v, ok := got[k]; ok && v != "" {
			t.Fatalf("普通用户不应看到 %s, got %v", k, v)
		}
	}

	rawCP := readAll(t, mustGet(t, alice, srv.URL+"/api/config-profiles?language=node", aliceCsrf))
	var cps struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(rawCP), &cps); err != nil {
		t.Fatalf("decode: %v (%s)", err, rawCP)
	}
	if len(cps.Items) != 1 {
		t.Fatalf("只应看到 1 个配置, got %d: %s", len(cps.Items), rawCP)
	}
	if v, ok := cps.Items[0]["filePath"]; ok && v != "" {
		t.Fatalf("普通用户不应看到宿主路径 filePath, got %v", v)
	}
	if cps.Items[0]["targetPath"] != "/root/.npmrc" {
		t.Fatalf("targetPath 缺失(注入容器要用): %+v", cps.Items[0])
	}
}

// TestPresetCatalog_RequiresAuth 验证未登录访问预置目录 → 401。
func TestPresetCatalog_RequiresAuth(t *testing.T) {
	srv, _ := setupPresetServer(t)
	for _, path := range []string{"/api/build-envs", "/api/build-envs/languages", "/api/config-profiles"} {
		resp := doJSON(t, newTestClient(t), http.MethodGet, srv.URL+path, "", "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s 未登录应 401, got %d", path, resp.StatusCode)
		}
	}
}

// mustGet 以 GET 取回响应(带 session + csrf cookie 上下文)。
func mustGet(t *testing.T, client *http.Client, url, csrf string) *http.Response {
	t.Helper()
	resp := doJSON(t, client, http.MethodGet, url, csrf, "")
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}
