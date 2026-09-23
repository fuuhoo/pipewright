package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huangchengsir/pipewright/internal/audit"
	"github.com/huangchengsir/pipewright/internal/auth"
	"github.com/huangchengsir/pipewright/internal/mask"
	"github.com/huangchengsir/pipewright/internal/store"
	"github.com/huangchengsir/pipewright/internal/storetest"
	"github.com/huangchengsir/pipewright/internal/users"
	"github.com/huangchengsir/pipewright/internal/vault"
)

// setupAccessServer 起一套「认证 + 用户服务 + 保险库」齐全的测试 server。
// 返回 admin 客户端、普通用户客户端构造器与裸 DB。
func setupAccessServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	st := storetest.Open(t)
	userSvc := users.NewService(st.DB)
	svc := auth.NewService(st.DB, nil, userSvc)
	if err := svc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	v := vault.New(st.DB, testMasterKey())
	srv := httptest.NewServer(New(testWebFSAuth(), svc,
		WithVault(v),
		WithUsers(userSvc),
		WithAudit(audit.New(st.DB, mask.NewMasker(), nil)),
	))
	t.Cleanup(srv.Close)
	return srv, st
}

// loginNewClient 起一个独立 cookie jar 登录,返回 (client, csrf)。
func loginNewClient(t *testing.T, srvURL, username, password string) (*http.Client, string) {
	t.Helper()
	client := newTestClient(t)
	return client, loginAs(t, client, srvURL, username, password)
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读响应体: %v", err)
	}
	return string(raw)
}

// TestAdminCreatesAndControlsUser 覆盖管理员建号 → 该号可登录 → 被限制 → 重置口令。
func TestAdminCreatesAndControlsUser(t *testing.T) {
	srv, _ := setupAccessServer(t)
	admin, adminCSRF := loginNewClient(t, srv.URL, "admin", "testpass")

	// 建号
	resp := doJSON(t, admin, http.MethodPost, srv.URL+"/api/admin/users", adminCSRF,
		`{"username":"dev-alice","password":"alicepass1","role":"user","description":"后端组"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("建号 status = %d(%s), want 201", resp.StatusCode, readBody(t, resp))
	}
	var created userDTO
	if err := json.Unmarshal([]byte(readBody(t, resp)), &created); err != nil {
		t.Fatalf("解析建号响应: %v", err)
	}
	resp.Body.Close()
	if created.ID == "" || created.Username != "dev-alice" || created.Role != "user" || !created.Enabled {
		t.Fatalf("建号响应不对:%+v", created)
	}
	if created.Description != "后端组" {
		t.Fatalf("description = %q", created.Description)
	}

	// 重名 → 409
	dup := doJSON(t, admin, http.MethodPost, srv.URL+"/api/admin/users", adminCSRF,
		`{"username":"dev-alice","password":"alicepass1","role":"user"}`)
	if dup.StatusCode != http.StatusConflict {
		t.Fatalf("重名 status = %d, want 409", dup.StatusCode)
	}
	dup.Body.Close()

	// 弱口令 → 422(且不该已落库)
	weak := doJSON(t, admin, http.MethodPost, srv.URL+"/api/admin/users", adminCSRF,
		`{"username":"dev-bob","password":"short"}`)
	if weak.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("弱口令 status = %d, want 422", weak.StatusCode)
	}
	weak.Body.Close()

	// 用户名非法 → 400
	bad := doJSON(t, admin, http.MethodPost, srv.URL+"/api/admin/users", adminCSRF,
		`{"username":"有 空格","password":"alicepass1"}`)
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法用户名 status = %d, want 400", bad.StatusCode)
	}
	bad.Body.Close()

	// 新用户可登录,但不能进 admin 面
	alice, aliceCSRF := loginNewClient(t, srv.URL, "dev-alice", "alicepass1")
	noGo := doJSON(t, alice, http.MethodPost, srv.URL+"/api/admin/users", aliceCSRF,
		`{"username":"dev-carol","password":"carolpass1"}`)
	if noGo.StatusCode != http.StatusForbidden {
		t.Fatalf("普通用户建号 status = %d, want 403", noGo.StatusCode)
	}
	noGo.Body.Close()

	// 审计只读:普通用户 403,管理员 200
	auditAsAlice := doJSON(t, alice, http.MethodGet, srv.URL+"/api/audit", "", "")
	if auditAsAlice.StatusCode != http.StatusForbidden {
		t.Fatalf("普通用户查审计 status = %d, want 403", auditAsAlice.StatusCode)
	}
	auditAsAlice.Body.Close()
	auditAsAdmin := doJSON(t, admin, http.MethodGet, srv.URL+"/api/audit", "", "")
	if auditAsAdmin.StatusCode != http.StatusOK {
		t.Fatalf("管理员查审计 status = %d, want 200", auditAsAdmin.StatusCode)
	}
	auditBody := readBody(t, auditAsAdmin)
	auditAsAdmin.Body.Close()
	if auditAsAdmin.StatusCode == http.StatusOK && !strings.Contains(auditBody, "user_admin_create") {
		t.Fatalf("审计里找不到建号记录:%s", auditBody)
	}

	// 管理员重置口令:旧口令即刻失效
	reset := doJSON(t, admin, http.MethodPost, srv.URL+"/api/admin/users/"+created.ID+"/password", adminCSRF,
		`{"password":"newpass123"}`)
	if reset.StatusCode != http.StatusNoContent {
		t.Fatalf("重置口令 status = %d(%s), want 204", reset.StatusCode, readBody(t, reset))
	}
	reset.Body.Close()
	oldPw := doJSON(t, newTestClient(t), http.MethodPost, srv.URL+"/api/auth/login", "",
		`{"username":"dev-alice","password":"alicepass1"}`)
	if oldPw.StatusCode == http.StatusOK {
		t.Fatal("旧口令在重置后仍可登录")
	}
	oldPw.Body.Close()
	loginNewClient(t, srv.URL, "dev-alice", "newpass123") // 新口令可登录(失败内部 t.Fatal)

	// 禁用 → 不能再登录;重新启用 → 恢复
	disable := doJSON(t, admin, http.MethodPatch, srv.URL+"/api/admin/users/"+created.ID, adminCSRF,
		`{"enabled":false}`)
	if disable.StatusCode != http.StatusOK {
		t.Fatalf("禁用 status = %d, want 200", disable.StatusCode)
	}
	if body := readBody(t, disable); !strings.Contains(body, `"enabled":false`) {
		t.Fatalf("禁用响应未体现状态:%s", body)
	}
	disable.Body.Close()
	blocked := doJSON(t, newTestClient(t), http.MethodPost, srv.URL+"/api/auth/login", "",
		`{"username":"dev-alice","password":"newpass123"}`)
	if blocked.StatusCode == http.StatusOK {
		t.Fatal("已禁用账号仍可登录")
	}
	blocked.Body.Close()

	enable := doJSON(t, admin, http.MethodPatch, srv.URL+"/api/admin/users/"+created.ID, adminCSRF,
		`{"enabled":true}`)
	if enable.StatusCode != http.StatusOK {
		t.Fatalf("启用 status = %d, want 200", enable.StatusCode)
	}
	enable.Body.Close()
	loginNewClient(t, srv.URL, "dev-alice", "newpass123") // 重新启用后恢复登录
}

// TestBootstrapAdminRowIsReadOnlyHere 盯住那个静默陷阱:内置管理员行的口令/启用状态
// 由 admin_user 决定,用户管理端点必须拒绝而不是「改成功但没生效」。
func TestBootstrapAdminRowIsReadOnlyHere(t *testing.T) {
	srv, _ := setupAccessServer(t)
	admin, adminCSRF := loginNewClient(t, srv.URL, "admin", "testpass")
	id := users.BootstrapAdminRegularUserID

	pw := doJSON(t, admin, http.MethodPost, srv.URL+"/api/admin/users/"+id+"/password", adminCSRF,
		`{"password":"whatever123"}`)
	if pw.StatusCode != http.StatusConflict {
		t.Fatalf("重置内置管理员口令 status = %d, want 409", pw.StatusCode)
	}
	pw.Body.Close()

	patch := doJSON(t, admin, http.MethodPatch, srv.URL+"/api/admin/users/"+id, adminCSRF,
		`{"enabled":false}`)
	if patch.StatusCode != http.StatusConflict {
		t.Fatalf("禁用内置管理员 status = %d, want 409", patch.StatusCode)
	}
	patch.Body.Close()

	// 改完仍然能用原口令登录(证明 409 不是「拦了但已经改坏」)
	loginNewClient(t, srv.URL, "admin", "testpass")
}

// TestAdminCreatedAdminCannotDisableSelf 额外管理员可以建同行,但不能把自己禁用掉。
func TestAdminCreatedAdminCannotDisableSelf(t *testing.T) {
	srv, st := setupAccessServer(t)
	admin, adminCSRF := loginNewClient(t, srv.URL, "admin", "testpass")

	resp := doJSON(t, admin, http.MethodPost, srv.URL+"/api/admin/users", adminCSRF,
		`{"username":"second-admin","password":"adminpass1","role":"admin"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("建管理员 status = %d(%s)", resp.StatusCode, readBody(t, resp))
	}
	var created userDTO
	_ = json.Unmarshal([]byte(readBody(t, resp)), &created)
	resp.Body.Close()
	if created.Role != "admin" {
		t.Fatalf("role = %q, want admin", created.Role)
	}

	second, secondCSRF := loginNewClient(t, srv.URL, "second-admin", "adminpass1")
	suicide := doJSON(t, second, http.MethodPatch, srv.URL+"/api/admin/users/"+created.ID, secondCSRF,
		`{"enabled":false}`)
	if suicide.StatusCode != http.StatusConflict {
		t.Fatalf("自助禁用 status = %d, want 409", suicide.StatusCode)
	}
	suicide.Body.Close()

	// 但能禁用别人
	other := doJSON(t, admin, http.MethodPost, srv.URL+"/api/admin/users", adminCSRF,
		`{"username":"dev-dave","password":"davepass1"}`)
	var dave userDTO
	_ = json.Unmarshal([]byte(readBody(t, other)), &dave)
	other.Body.Close()

	disable := doJSON(t, second, http.MethodPatch, srv.URL+"/api/admin/users/"+dave.ID, secondCSRF,
		`{"enabled":false,"description":"离职"}`)
	if disable.StatusCode != http.StatusOK {
		t.Fatalf("他助禁用 status = %d, want 200", disable.StatusCode)
	}
	disable.Body.Close()

	// 新建的额外管理员也必须出现在 users 列表里(证明走的是 users 表,不是 admin_user)
	list := doJSON(t, admin, http.MethodGet, srv.URL+"/api/admin/users?includeDisabled=1", "", "")
	if list.StatusCode != http.StatusOK {
		t.Fatalf("用户列表 status = %d, want 200", list.StatusCode)
	}
	var out struct {
		Items []userDTO `json:"items"`
	}
	if err := json.Unmarshal([]byte(readBody(t, list)), &out); err != nil {
		t.Fatalf("解析用户列表: %v", err)
	}
	list.Body.Close()
	var daveRow *userDTO
	for i := range out.Items {
		if out.Items[i].Username == "dev-dave" {
			daveRow = &out.Items[i]
		}
	}
	if daveRow == nil || daveRow.Enabled {
		t.Fatalf("列表里 dev-dave 状态不对:%+v", daveRow)
	}
	// DB 侧兜底断言:响应里没有 hash 字段
	var hashCount int
	if err := st.DB.QueryRow(`SELECT COUNT(1) FROM users WHERE username IN ('second-admin','dev-dave')`).Scan(&hashCount); err != nil {
		t.Fatalf("数 users 行: %v", err)
	}
	if hashCount != 2 {
		t.Fatalf("users 行数 = %d, want 2", hashCount)
	}
}

// createUser 以管理员身份建号,返回用户视图(非 201 即失败)。
func createUser(t *testing.T, admin *http.Client, adminCSRF, srvURL, username, password, role string) userDTO {
	t.Helper()
	resp := doJSON(t, admin, http.MethodPost, srvURL+"/api/admin/users", adminCSRF,
		fmt.Sprintf(`{"username":%q,"password":%q,"role":%q}`, username, password, role))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("建号 %s status = %d(%s), want 201", username, resp.StatusCode, readBody(t, resp))
	}
	var u userDTO
	if err := json.Unmarshal([]byte(readBody(t, resp)), &u); err != nil {
		t.Fatalf("解析建号响应: %v", err)
	}
	resp.Body.Close()
	return u
}

// mustCreateCred 建凭据并断言 201,返回掩码视图。
func mustCreateCred(t *testing.T, c *http.Client, csrf, srvURL, body string) credentialDTO {
	t.Helper()
	resp := doJSON(t, c, http.MethodPost, srvURL+"/api/credentials", csrf, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("建凭据 status = %d(%s), want 201", resp.StatusCode, readBody(t, resp))
	}
	var cred credentialDTO
	if err := json.Unmarshal([]byte(readBody(t, resp)), &cred); err != nil {
		t.Fatalf("解析凭据响应: %v", err)
	}
	resp.Body.Close()
	return cred
}

// listCreds 列出当前会话可见的凭据。
func listCreds(t *testing.T, c *http.Client, srvURL string) []credentialDTO {
	t.Helper()
	resp := doJSON(t, c, http.MethodGet, srvURL+"/api/credentials", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("凭据列表 status = %d, want 200", resp.StatusCode)
	}
	var out []credentialDTO
	if err := json.Unmarshal([]byte(readBody(t, resp)), &out); err != nil {
		t.Fatalf("解析凭据列表: %v", err)
	}
	resp.Body.Close()
	return out
}

// namesOf 取凭据名列表(排序无关,按插入顺序),便于断言可见范围。
func namesOf(creds []credentialDTO) []string {
	out := make([]string, 0, len(creds))
	for _, c := range creds {
		out = append(out, c.Name)
	}
	return out
}

func hasName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// TestCredentialActorScoping 盯住凭据的归属收敛:普通用户只看得到自己的 personal,
// 明文回传与禁用/启用只归管理员。
func TestCredentialActorScoping(t *testing.T) {
	srv, _ := setupAccessServer(t)
	admin, adminCSRF := loginNewClient(t, srv.URL, "admin", "testpass")
	alice := createUser(t, admin, adminCSRF, srv.URL, "cred-alice", "alicepass1", "user")
	bob := createUser(t, admin, adminCSRF, srv.URL, "cred-bob", "bobpass1", "user")
	aliceC, aliceCSRF := loginNewClient(t, srv.URL, "cred-alice", "alicepass1")
	bobC, bobCSRF := loginNewClient(t, srv.URL, "cred-bob", "bobpass1")

	// 管理员建 global 凭据
	globalCred := mustCreateCred(t, admin, adminCSRF, srv.URL,
		`{"name":"shared-git","type":"git_token","scope":"global","secret":"glpat-shared-value"}`)
	if globalCred.Scope != "global" || globalCred.OwnerID != "" {
		t.Fatalf("global 凭据归属不对:%+v", globalCred)
	}

	// 普通用户建 personal:即使谎报 scope/ownerId 也会被强制改成「自己的 personal」
	sneaky := mustCreateCred(t, aliceC, aliceCSRF, srv.URL,
		fmt.Sprintf(`{"name":"alice-token","type":"git_token","scope":"global","ownerId":%q,"secret":"alice-secret-value"}`, bob.ID))
	if sneaky.Scope != "personal" || sneaky.OwnerID != alice.ID {
		t.Fatalf("越权建凭据未被收敛:%+v(want personal + owner %s)", sneaky, alice.ID)
	}
	if !sneaky.Enabled {
		t.Fatalf("新建凭据应为可用:%+v", sneaky)
	}
	if strings.Contains(fmt.Sprint(sneaky), "alice-secret-value") {
		t.Fatal("响应里出现了明文凭据")
	}
	mustCreateCred(t, bobC, bobCSRF, srv.URL, `{"name":"bob-token","type":"git_token","secret":"bob-secret-value"}`)

	// 列表收敛:user 看得到共享的 global(存量凭据全是 global,藏起来会把老项目打死),
	// 但只看得到自己的 personal——别人的 personal 与创建者 id 都不该出现。
	if got := namesOf(listCreds(t, aliceC, srv.URL)); len(got) != 2 || !hasName(got, "alice-token") || !hasName(got, "shared-git") {
		t.Fatalf("alice 可见凭据 = %v, want [shared-git alice-token]", got)
	}
	if got := namesOf(listCreds(t, bobC, srv.URL)); len(got) != 2 || hasName(got, "alice-token") {
		t.Fatalf("bob 可见凭据 = %v, want 不含 alice-token", got)
	}
	adminView := listCreds(t, admin, srv.URL)
	if len(adminView) != 3 {
		t.Fatalf("admin 可见凭据数 = %d(%v), want 3", len(adminView), namesOf(adminView))
	}

	// 明文只归管理员:本人也读不出明文
	if resp := doJSON(t, aliceC, http.MethodPost, srv.URL+"/api/credentials/"+sneaky.ID+"/reveal", aliceCSRF, ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("普通用户 reveal status = %d, want 403", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	reveal := doJSON(t, admin, http.MethodPost, srv.URL+"/api/credentials/"+sneaky.ID+"/reveal", adminCSRF, "")
	if reveal.StatusCode != http.StatusOK {
		t.Fatalf("管理员 reveal status = %d(%s), want 200", reveal.StatusCode, readBody(t, reveal))
	}
	var revealed struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal([]byte(readBody(t, reveal)), &revealed); err != nil {
		t.Fatalf("解析 reveal 响应: %v", err)
	}
	reveal.Body.Close()
	if revealed.Secret != "alice-secret-value" {
		t.Fatalf("管理员 reveal 未取回原文(长度 %d)", len(revealed.Secret))
	}

	// 普通用户不能碰 global,也不能借 PATCH 把 personal 升成 global
	if resp := doJSON(t, aliceC, http.MethodPatch, srv.URL+"/api/credentials/"+globalCred.ID, aliceCSRF, `{"name":"hijacked"}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("普通用户改 global status = %d, want 403", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	if resp := doJSON(t, aliceC, http.MethodPatch, srv.URL+"/api/credentials/"+sneaky.ID, aliceCSRF, `{"scope":"global"}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("普通用户升 global status = %d, want 403", resp.StatusCode)
	} else {
		resp.Body.Close()
	}

	// 禁用/启用是 admin 面;禁用后取用路径立刻失败(此处以 reveal 为证)
	disableURL := srv.URL + "/api/admin/credentials/" + sneaky.ID + "/disable"
	if resp := doJSON(t, aliceC, http.MethodPost, disableURL, aliceCSRF, ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("普通用户禁用凭据 status = %d, want 403", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	if resp := doJSON(t, admin, http.MethodPost, disableURL, adminCSRF, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("管理员禁用 status = %d(%s), want 200", resp.StatusCode, readBody(t, resp))
	} else {
		resp.Body.Close()
	}
	if resp := doJSON(t, admin, http.MethodPost, srv.URL+"/api/credentials/"+sneaky.ID+"/reveal", adminCSRF, ""); resp.StatusCode != http.StatusConflict {
		t.Fatalf("禁用后 reveal status = %d, want 409", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	// 禁用不改归属,列表仍看得见(带 enabled=false)
	after := listCreds(t, aliceC, srv.URL)
	if len(after) != 2 {
		t.Fatalf("禁用后 alice 列表 %d 条, want 2:%v", len(after), namesOf(after))
	}
	for _, c := range after {
		if c.Name == "alice-token" && c.Enabled {
			t.Fatalf("禁用后列表仍标为可用:%+v", c)
		}
	}
	if resp := doJSON(t, admin, http.MethodPost, srv.URL+"/api/admin/credentials/"+sneaky.ID+"/enable", adminCSRF, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("管理员启用 status = %d, want 200", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	if resp := doJSON(t, admin, http.MethodPost, srv.URL+"/api/credentials/"+sneaky.ID+"/reveal", adminCSRF, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("启用后 reveal status = %d, want 200", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	// global 凭据不可被个别禁用
	if resp := doJSON(t, admin, http.MethodPost, srv.URL+"/api/admin/credentials/"+globalCred.ID+"/disable", adminCSRF, ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("禁用 global status = %d, want 403", resp.StatusCode)
	} else {
		resp.Body.Close()
	}

	// 删除:越权 403,本人可删
	if resp := doJSON(t, bobC, http.MethodDelete, srv.URL+"/api/credentials/"+sneaky.ID, bobCSRF, ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("他人删凭据 status = %d, want 403", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	if resp := doJSON(t, aliceC, http.MethodDelete, srv.URL+"/api/credentials/"+sneaky.ID, aliceCSRF, ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("本人删凭据 status = %d, want 204", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	if got := namesOf(listCreds(t, aliceC, srv.URL)); len(got) != 1 || hasName(got, "alice-token") {
		t.Fatalf("删除后 alice 仍看得到自己的凭据:%v", got)
	}
}

// TestCredentialRevealAndAuditAreAdminOnly 确认「本期收紧」的两条:明文回传与审计查询
// 都在中间件层就挡住普通用户(响应体里也不能泄漏细节)。
func TestCredentialRevealAndAuditAreAdminOnly(t *testing.T) {
	srv, _ := setupAccessServer(t)
	admin, adminCSRF := loginNewClient(t, srv.URL, "admin", "testpass")
	createUser(t, admin, adminCSRF, srv.URL, "audit-alice", "alicepass1", "user")
	aliceC, aliceCSRF := loginNewClient(t, srv.URL, "audit-alice", "alicepass1")

	cred := mustCreateCred(t, aliceC, aliceCSRF, srv.URL, `{"name":"a-t","type":"git_token","secret":"value-value-value"}`)
	// 未登录 → 401:先证明普通用户的 403 来自角色收敛,而非缺会话
	if resp := doJSON(t, newTestClient(t), http.MethodPost, srv.URL+"/api/credentials/"+cred.ID+"/reveal", "", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("匿名 reveal status = %d, want 401", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	if resp := doJSON(t, aliceC, http.MethodPost, srv.URL+"/api/credentials/"+cred.ID+"/reveal", aliceCSRF, ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("普通用户 reveal status = %d, want 403", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	if resp := doJSON(t, aliceC, http.MethodGet, srv.URL+"/api/audit", "", ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("普通用户查审计 status = %d, want 403", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	auditResp := doJSON(t, admin, http.MethodGet, srv.URL+"/api/audit", "", "")
	if auditResp.StatusCode != http.StatusOK {
		t.Fatalf("管理员查审计 status = %d, want 200", auditResp.StatusCode)
	}
	raw := readBody(t, auditResp)
	auditResp.Body.Close()
	if !strings.Contains(raw, "credential_create") {
		t.Fatalf("审计里找不到建凭据记录:%.200s", raw)
	}
	if strings.Contains(raw, "value-value-value") {
		t.Fatal("审计响应里出现了明文凭据")
	}
}
