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

// setupAdminServer 构造带 admin 子组服务的测试 server。
// 注入 buildenv.Service + configprofile.Service + users.Service + vault。
// 返回 srv + 已登录 admin session 的 http.Client + csrfToken。
// 调用方负责 srv.Close(t.Cleanup 已自动)。
func setupAdminServer(t *testing.T) (*httptest.Server, *http.Client, string) {
	t.Helper()
	st := storetest.Open(t)
	svc := auth.NewService(st.DB, nil, users.NewService(st.DB))
	if err := svc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	v := vault.New(st.DB, testMasterKey())
	rec := audit.New(st.DB, mask.NewMasker(), nil)

	// buildenv.Service 需要 Repo;这里用 sqliteRepo
	bRepo := buildenv.NewSQLiteRepo(st.DB)
	bSvc := buildenv.NewService(bRepo)
	cpRepo := configprofile.NewSQLiteRepo(st.DB)
	cpSvc := configprofile.NewService(cpRepo, t.TempDir())
	uSvc := users.NewService(st.DB)

	srv := httptest.NewServer(New(testWebFSAuth(), svc,
		WithVault(v),
		WithAudit(rec),
		WithBuildEnvs(bSvc, nil), // checker nil → check/pull 端点 503,但 list/create OK
		WithConfigProfiles(cpSvc),
		WithUsers(uSvc),
	))
	t.Cleanup(srv.Close)

	client := newTestClient(t)
	csrf := loginWithClient(t, client, srv.URL)
	return srv, client, csrf
}

// TestAdminBuildEnvsList 验证 /api/admin/build-envs 在 admin session 下能列出。
func TestAdminBuildEnvsList(t *testing.T) {
	srv, client, csrf := setupAdminServer(t)
	resp := doJSON(t, client, http.MethodGet, srv.URL+"/api/admin/build-envs", csrf, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin 列表 status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Items []buildEnvDTO `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// 空列表起步
	if len(out.Items) != 0 {
		t.Fatalf("新建 DB 应为空, got %d", len(out.Items))
	}
}

// TestAdminBuildEnvsCreate 验证 admin 创建构建环境能成功。
func TestAdminBuildEnvsCreate(t *testing.T) {
	srv, client, csrf := setupAdminServer(t)
	body := `{"language":"node","version":"20","displayName":"Node 20","sourceType":"official","image":"node:20-alpine","enabled":true}`
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/admin/build-envs", csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		raw := readAll(t, resp)
		t.Fatalf("create status = %d, want 201: %s", resp.StatusCode, raw)
	}
	var out buildEnvDTO
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Language != "node" || out.Version != "20" || out.Image != "node:20-alpine" {
		t.Fatalf("字段错: %+v", out)
	}
	if !out.Enabled {
		t.Fatalf("enabled 应为 true")
	}
}

// TestAdminBuildEnvsCreate_Validation 验证缺 image → 400。
func TestAdminBuildEnvsCreate_Validation(t *testing.T) {
	srv, client, csrf := setupAdminServer(t)
	body := `{"language":"node","version":"20","displayName":"X","sourceType":"official"}` // 缺 image
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/admin/build-envs", csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("缺 image 应 400, got %d", resp.StatusCode)
	}
}

// TestAdminBuildEnvsLanguages_NonAdminAllowed 验证 /api/build-envs/languages
// 普通用户(admin 也算 RequireUser 通过)能访问。
func TestAdminBuildEnvsLanguages_NonAdminAllowed(t *testing.T) {
	srv, client, csrf := setupAdminServer(t)
	resp := doJSON(t, client, http.MethodGet, srv.URL+"/api/build-envs/languages", csrf, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/api/build-envs/languages status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Languages []string `json:"languages"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	// 无 build_env 时返回空 slice 或 nil 都可以
}

// TestAdminConfigProfilesList 验证 /api/admin/config-profiles。
func TestAdminConfigProfilesList(t *testing.T) {
	srv, client, csrf := setupAdminServer(t)
	resp := doJSON(t, client, http.MethodGet, srv.URL+"/api/admin/config-profiles", csrf, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Items []configProfileDTO `json:"items"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
}

// TestAdminConfigProfilesCreate 验证 admin 创建配置资源。
func TestAdminConfigProfilesCreate(t *testing.T) {
	srv, client, csrf := setupAdminServer(t)
	body := `{"language":"java","configType":"settings","name":"settings.xml","targetPath":"/root/.m2/settings.xml","content":"<settings/>","enabled":true}`
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/admin/config-profiles", csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		raw := readAll(t, resp)
		t.Fatalf("create status = %d: %s", resp.StatusCode, raw)
	}
	var out configProfileDTO
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.Name != "settings.xml" || out.Language != "java" {
		t.Fatalf("字段错: %+v", out)
	}
}

// TestAdminUsersList 验证 /api/admin/users(阶段 9 占位空列表)。
func TestAdminUsersList(t *testing.T) {
	srv, client, csrf := setupAdminServer(t)
	resp := doJSON(t, client, http.MethodGet, srv.URL+"/api/admin/users", csrf, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

// TestAdminBuildEnvs_NonAdminForbidden 验证非 admin session(未登录)被 401。
//
// 严格说,我们用 noauth 客户端(无 cookie)打 admin 端点 → requireAuth 应先 401。
// 真要测 403,需构造 role="user" 的 session;本测试只覆盖 401。
func TestAdminBuildEnvs_NonAdminForbidden(t *testing.T) {
	srv, _, _ := setupAdminServer(t)
	// 用一个全新的、未登录的 client 调 admin 端点。
	client := newTestClient(t)
	resp := doJSON(t, client, http.MethodGet, srv.URL+"/api/admin/build-envs", "", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无 session 应 401, got %d", resp.StatusCode)
	}
}

// readAll helper for tests
func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	var sb strings.Builder
	buf := make([]byte, 1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			sb.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return sb.String()
}

// 回归:toggle 启用 unchecked 环境曾返回 500(SetEnabled 返回 *ValidationError,
// writeBuildEnvError 只匹配 Err* sentinel → 落 default 500)。
func TestRegression_ToggleUncheckedReturns409(t *testing.T) {
	srv, client, csrf := setupAdminServer(t)
	body := `{"language":"go","version":"9.9-regress","displayName":"r","sourceType":"official","image":"golang:9999-nope"}`
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/admin/build-envs", csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	var created buildEnvDTO
	_ = json.NewDecoder(resp.Body).Decode(&created)
	if created.ImageCheckStatus != "unchecked" {
		t.Fatalf("新环境应 unchecked, got %q", created.ImageCheckStatus)
	}

	// toggle 启用 → 必须 409(不是 500)
	resp2 := doJSON(t, client, http.MethodPost,
		srv.URL+"/api/admin/build-envs/"+created.ID+"/toggle", csrf, `{"enabled":true}`)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("unchecked 启用应 409, got %d", resp2.StatusCode)
	}
	var out struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.NewDecoder(resp2.Body).Decode(&out)
	if out.Error.Code != "IMAGE_NOT_CHECKED" {
		t.Fatalf("code = %q want IMAGE_NOT_CHECKED", out.Error.Code)
	}
}

// 回归:PUT build_env 曾把 image_check_status 覆盖成空串(handler 构造时未带该字段),
// 导致三态校验被绕过。
func TestRegression_UpdateBuildEnvKeepsCheckStatus(t *testing.T) {
	srv, client, csrf := setupAdminServer(t)
	body := `{"language":"go","version":"9.9-keep","displayName":"k","sourceType":"official","image":"golang:9999-nope"}`
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/admin/build-envs", csrf, body)
	defer resp.Body.Close()
	var created buildEnvDTO
	_ = json.NewDecoder(resp.Body).Decode(&created)

	// PUT(不带 imageCheckStatus,模拟前端完整替换)
	up := `{"language":"go","version":"9.9-keep","displayName":"k2","sourceType":"official","image":"golang:9999-nope"}`
	resp2 := doJSON(t, client, http.MethodPut,
		srv.URL+"/api/admin/build-envs/"+created.ID, csrf, up)
	defer resp2.Body.Close()
	var updated buildEnvDTO
	_ = json.NewDecoder(resp2.Body).Decode(&updated)
	if updated.ImageCheckStatus != "unchecked" {
		t.Fatalf("PUT 后 status 应仍为 unchecked, got %q(被清空则三态校验失效)", updated.ImageCheckStatus)
	}

	// 进而 toggle 启用必须仍被拒
	resp3 := doJSON(t, client, http.MethodPost,
		srv.URL+"/api/admin/build-envs/"+created.ID+"/toggle", csrf, `{"enabled":true}`)
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusConflict {
		t.Fatalf("PUT 后 toggle 启用应仍 409, got %d", resp3.StatusCode)
	}
}
