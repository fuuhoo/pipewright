package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/fuuhoo/pipewright/internal/access"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/users"
	"github.com/google/uuid"
)

// role_guard_test.go —— 功能轴(角色档位)在 HTTP 收口上的真机断言。
//
// 分组三态的判定由 access_guard_test.go 覆盖,这里全部用「未归组」资源(数据轴对全员开放),
// 好让 403 只能来自角色上限,不会与归属混淆。控制组是存量的 user:它必须逐字保持今天的权限。
//
// 下面用到的 viewer / developer / ops / user 都不是代码里的常量了 —— 它们是 0063 播种进 roles 表
// 的四档预置,由 setupGuard 装载进判定缓存。所以这一组用例同时也是那条播种的端到端验收:
// 少一行、少一个点,这里就会以 401/403 的形式炸出来。

// addRoleUser 直接落库建一个指定角色的账号并登录,返回 (client, csrf, userID)。
// 绕开 POST /api/admin/users 是为了让夹具与「建号」解耦——角色本身要能被独立设定。
func addRoleUser(t *testing.T, env *guardEnv, name, role string) (*http.Client, string, string) {
	t.Helper()
	hash, err := auth.HashPassword(guardPass)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	id := uuid.NewString()
	if _, err := env.db.Exec(
		`INSERT INTO users (id, username, password_hash, role, enabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 1, ?, ?)`,
		id, name, hash, role, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("seed %s 角色账号: %v", role, err)
	}
	c := newTestClient(t)
	cs := loginAs(t, c, env.srv.URL, name, guardPass)
	return c, cs, id
}

// wantStatus 断言状态码并回读响应体(便于失败时看清是哪一层拦的)。
func wantStatus(t *testing.T, c *http.Client, method, url, csrf, body string, want int, label string) {
	t.Helper()
	resp := doJSON(t, c, method, url, csrf, body)
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("%s %s → %s: status = %d(%s), want %d", method, url, label, resp.StatusCode, raw, want)
	}
}

// wantNotForbidden 断言「不是被权限层拦下的」:这些端点在夹具里没装配领域服务,
// 放行后会返回 503,断具体成功码会把测试绑到无关的装配细节上。
func wantNotForbidden(t *testing.T, c *http.Client, method, url, csrf, body, label string) {
	t.Helper()
	resp := doJSON(t, c, method, url, csrf, body)
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusForbidden {
		t.Fatalf("%s %s → %s: 不该被角色上限拦下(%s)", method, url, label, raw)
	}
}

func TestRoleCeilingOnHTTP(t *testing.T) {
	env := setupGuard(t)
	base := env.srv.URL

	viewer, viewerCS, _ := addRoleUser(t, env, "role-viewer", "viewer")
	dev, devCS, _ := addRoleUser(t, env, "role-dev", "developer")
	ops, opsCS, _ := addRoleUser(t, env, "role-ops", "ops")
	plain, plainCS, _ := addRoleUser(t, env, "role-user", access.RoleUser)

	projURL := "/api/projects/" + env.ungrouped
	srvURL := "/api/servers/" + env.srvUngrouped
	srvTestURL := srvURL + "/test"

	// 只读:未归组资源照样看得见,但任何写都是 403(数据轴是允许的,角色把它拦下)。
	wantStatus(t, viewer, http.MethodGet, base+srvURL, "", "", http.StatusOK, "viewer 看未归组主机")
	wantStatus(t, viewer, http.MethodGet, base+"/api/projects", "", "", http.StatusOK, "viewer 看项目列表")
	wantStatus(t, viewer, http.MethodPatch, base+projURL, viewerCS, `{"name":"改名"}`, http.StatusForbidden, "viewer 改项目")
	wantStatus(t, viewer, http.MethodPost, base+srvTestURL, viewerCS, "{}", http.StatusForbidden, "viewer 连主机")

	// 开发者:能动项目,不能碰落点。
	wantStatus(t, dev, http.MethodPatch, base+projURL, devCS, `{"name":"改名"}`, http.StatusOK, "developer 改项目")
	wantStatus(t, dev, http.MethodPost, base+srvTestURL, devCS, "{}", http.StatusForbidden, "developer 连主机")

	// 运维:能动落点与运行,不能改项目配置。运行同样用未归组项目下的那一次,
	// 免得私有组的名册判定(数据轴)混进来看似角色拦下的 403。
	runFree := seedGuardRun(t, env.db, env.ungrouped)
	wantNotForbidden(t, ops, http.MethodPost, base+srvTestURL, opsCS, "{}", "ops 连主机")
	wantStatus(t, ops, http.MethodPatch, base+projURL, opsCS, `{"name":"改名"}`, http.StatusForbidden, "ops 改项目")
	wantNotForbidden(t, ops, http.MethodPost, base+"/api/runs/"+runFree+"/cancel", opsCS, "{}", "ops 取消运行")

	// 控制组:存量 user 的权限逐字不变(功能轴对它不封顶)。
	wantStatus(t, plain, http.MethodPatch, base+projURL, plainCS, `{"name":"改名"}`, http.StatusOK, "user 改项目")
	wantNotForbidden(t, plain, http.MethodPost, base+srvTestURL, plainCS, "{}", "user 连主机")

	// 管理员不受上限约束。
	wantStatus(t, env.admin, http.MethodPatch, base+projURL, env.adminCS, `{"name":"改名"}`, http.StatusOK, "admin 改项目")
}

// TestSessionCarriesCapabilities 验能力位随会话回传,且改角色后重新登录就换一档。
func TestSessionCarriesCapabilities(t *testing.T) {
	env := setupGuard(t)
	base := env.srv.URL

	viewer, _, viewerID := addRoleUser(t, env, "cap-viewer", "viewer")

	var caps sessionPayload
	resp := doJSON(t, viewer, http.MethodGet, base+"/api/auth/session", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session status = %d", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err := json.Unmarshal(raw, &caps); err != nil {
		t.Fatalf("解析 session 响应: %v(%s)", err, raw)
	}
	if caps.Role != "viewer" {
		t.Fatalf("role = %q, want viewer", caps.Role)
	}
	if caps.Capabilities.Settings {
		t.Fatalf("只读角色不该有设置位")
	}
	if got := caps.Capabilities.Kinds[string(access.KindServer)]; got != "view" {
		t.Fatalf("viewer 主机档位 = %q, want view", got)
	}
	// 只读角色也进不了 admin 面(与能力位同源的另一半证据)。
	wantStatus(t, viewer, http.MethodGet, base+"/api/audit", "", "", http.StatusForbidden, "viewer 查审计")

	// 管理员把它改成开发者:重新登录后档位立刻跟着走(角色是登录快照,不重登不变)。
	patch := doJSON(t, env.admin, http.MethodPatch, base+"/api/admin/users/"+viewerID, env.adminCS,
		`{"role":"developer"}`)
	if patch.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(patch.Body)
		_ = patch.Body.Close()
		t.Fatalf("改角色 status = %d(%s)", patch.StatusCode, body)
	}
	_ = patch.Body.Close()

	relogin := newTestClient(t)
	loginAs(t, relogin, base, "cap-viewer", guardPass)
	resp2 := doJSON(t, relogin, http.MethodGet, base+"/api/auth/session", "", "")
	raw2, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	var caps2 sessionPayload
	if err := json.Unmarshal(raw2, &caps2); err != nil {
		t.Fatalf("解析第二次 session: %v(%s)", err, raw2)
	}
	if got := caps2.Capabilities.Kinds[string(access.KindProject)]; got != "operate" {
		t.Fatalf("改角色后项目档位 = %q, want operate", got)
	}
	if got := caps2.Capabilities.Kinds[string(access.KindServer)]; got != "view" {
		t.Fatalf("改角色后主机档位 = %q, want view", got)
	}
	// 旧会话仍然带着旧档位 —— 这是「改角色不撤销已签发会话」的既有取舍,断言钉住现状。
	respOld := doJSON(t, viewer, http.MethodGet, base+"/api/auth/session", "", "")
	rawOld, _ := io.ReadAll(respOld.Body)
	_ = respOld.Body.Close()
	var capsOld sessionPayload
	if err := json.Unmarshal(rawOld, &capsOld); err != nil {
		t.Fatalf("解析旧会话: %v", err)
	}
	if capsOld.Role != "viewer" {
		t.Fatalf("旧会话角色应仍是登录时的 viewer, got %q", capsOld.Role)
	}
}

// TestUserAPIRoleValidation 验建号与改角色的入参口径。
func TestUserAPIRoleValidation(t *testing.T) {
	env := setupGuard(t)
	base := env.srv.URL

	// 建号:未知名静默按 user 落库(与历史一致,不因为新枚举就报错)。
	resp := doJSON(t, env.admin, http.MethodPost, base+"/api/admin/users", env.adminCS,
		`{"username":"role-unknown","password":"rolepass123","role":"root"}`)
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("建号 status = %d(%s)", resp.StatusCode, body)
	}
	var created userDTO
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("解析建号响应: %v", err)
	}
	if created.Role != access.RoleUser {
		t.Fatalf("未知名应落 user, got %q", created.Role)
	}

	// 新枚举内的角色可用。
	resp2 := doJSON(t, env.admin, http.MethodPost, base+"/api/admin/users", env.adminCS,
		`{"username":"role-ops","password":"rolepass123","role":"ops"}`)
	if resp2.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp2.Body)
		_ = resp2.Body.Close()
		t.Fatalf("建 ops 号 status = %d(%s)", resp2.StatusCode, body)
	}
	raw2, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	var opsUser userDTO
	if err := json.Unmarshal(raw2, &opsUser); err != nil {
		t.Fatalf("解析 ops 建号响应: %v", err)
	}
	if opsUser.Role != "ops" {
		t.Fatalf("role = %q, want ops", opsUser.Role)
	}

	// 改角色:枚举外的值 → 400(领域层 ErrValidation),而不是静默变成 user。
	bad := doJSON(t, env.admin, http.MethodPatch, base+"/api/admin/users/"+created.ID, env.adminCS,
		`{"role":"superuser"}`)
	if bad.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(bad.Body)
		_ = bad.Body.Close()
		t.Fatalf("非法角色应 400, got %d(%s)", bad.StatusCode, body)
	}
	_ = bad.Body.Close()

	// 内置管理员那一行仍然改不动(角色也不例外)。
	blocked := doJSON(t, env.admin, http.MethodPatch,
		base+"/api/admin/users/"+users.BootstrapAdminRegularUserID, env.adminCS, `{"role":"viewer"}`)
	if blocked.StatusCode != http.StatusConflict {
		t.Fatalf("改内置管理员角色应 409, got %d", blocked.StatusCode)
	}
	_ = blocked.Body.Close()
}
