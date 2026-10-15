package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/users"
)

// createUserForDeleteTest 用管理员端点建号并回读 DTO,省掉每个用例重复一遍解析。
func createUserForDeleteTest(t *testing.T, admin *http.Client, csrf, srvURL, body string) userDTO {
	t.Helper()
	resp := doJSON(t, admin, http.MethodPost, srvURL+"/api/admin/users", csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("建号 status = %d(%s), want 201", resp.StatusCode, readBody(t, resp))
	}
	var u userDTO
	if err := json.Unmarshal([]byte(readBody(t, resp)), &u); err != nil {
		t.Fatalf("解析建号响应: %v", err)
	}
	return u
}

// TestAdminDeletesUser 删除走通:204 → 列表里没有 → 该账号的会话立刻失效 → 登录被拒 → 审计留痕。
func TestAdminDeletesUser(t *testing.T) {
	srv, st := setupAccessServer(t)
	admin, adminCSRF := loginNewClient(t, srv.URL, "admin", "testpass")

	victim := createUserForDeleteTest(t, admin, adminCSRF, srv.URL,
		`{"username":"dev-erin","password":"erinpass1","role":"user","displayName":"Erin","email":"erin@example.com"}`)

	// 受害者先登录一次,好验证「删号必须踢掉活会话」这条安全边界。
	erin, _ := loginNewClient(t, srv.URL, "dev-erin", "erinpass1")
	if got := doJSON(t, erin, http.MethodGet, srv.URL+"/api/auth/session", "", ""); got.StatusCode != http.StatusOK {
		body := readBody(t, got)
		got.Body.Close()
		t.Fatalf("删除前会话应有效,got %d(%s)", got.StatusCode, body)
	}

	del := doJSON(t, admin, http.MethodDelete, srv.URL+"/api/admin/users/"+victim.ID, adminCSRF, "")
	if del.StatusCode != http.StatusNoContent {
		body := readBody(t, del)
		del.Body.Close()
		t.Fatalf("删除 status = %d(%s), want 204", del.StatusCode, body)
	}
	del.Body.Close()

	// 会话行是删号里唯一「不删就出事」的牵连,单独断言而不是靠列表推断。
	var liveSessions int
	if err := st.DB.QueryRow(`SELECT COUNT(1) FROM sessions WHERE user_id = ?`, victim.ID).Scan(&liveSessions); err != nil {
		t.Fatalf("数会话: %v", err)
	}
	if liveSessions != 0 {
		t.Fatalf("删号后仍有 %d 条活会话", liveSessions)
	}
	revoked := doJSON(t, erin, http.MethodGet, srv.URL+"/api/auth/session", "", "")
	revoked.Body.Close()
	if revoked.StatusCode == http.StatusOK {
		t.Fatal("被删账号的旧会话仍可用")
	}

	// 列表(含 includeDisabled)与登录都查不到这个人
	list := doJSON(t, admin, http.MethodGet, srv.URL+"/api/admin/users?includeDisabled=1", "", "")
	listBody := readBody(t, list)
	list.Body.Close()
	if strings.Contains(listBody, "dev-erin") {
		t.Fatalf("列表里仍有该账号:%s", listBody)
	}
	relogin := doJSON(t, newTestClient(t), http.MethodPost, srv.URL+"/api/auth/login", "",
		`{"username":"dev-erin","password":"erinpass1"}`)
	relogin.Body.Close()
	if relogin.StatusCode == http.StatusOK {
		t.Fatal("被删账号仍能登录")
	}

	// 用户名从库里释放了:同名可以重建(与「禁用」的区别正在这里)
	reborn := doJSON(t, admin, http.MethodPost, srv.URL+"/api/admin/users", adminCSRF,
		`{"username":"dev-erin","password":"erinpass2","role":"user"}`)
	rebornStatus := reborn.StatusCode
	reborn.Body.Close()
	if rebornStatus != http.StatusCreated {
		t.Fatalf("删号后同名应可重建,got %d", rebornStatus)
	}

	// 审计:账号行已经不在了,username 只能从这条记录里查回来
	aud := doJSON(t, admin, http.MethodGet, srv.URL+"/api/audit", "", "")
	audBody := readBody(t, aud)
	aud.Body.Close()
	if !strings.Contains(audBody, "user_delete") || !strings.Contains(audBody, "dev-erin") {
		t.Fatalf("审计缺少删号记录:%s", audBody)
	}
}

// TestAdminDeleteUserGuards 三类不该真删的目标:内置管理员行、自己、未知 id;外加普通用户无权。
func TestAdminDeleteUserGuards(t *testing.T) {
	srv, _ := setupAccessServer(t)
	admin, adminCSRF := loginNewClient(t, srv.URL, "admin", "testpass")

	// 内置管理员行:它的口令与启停归「账户设置」,删掉会让 admin personal 凭据归属悬空
	bootstrap := doJSON(t, admin, http.MethodDelete,
		srv.URL+"/api/admin/users/"+users.BootstrapAdminRegularUserID, adminCSRF, "")
	bootstrapBody := readBody(t, bootstrap)
	bootstrap.Body.Close()
	if bootstrap.StatusCode != http.StatusConflict || !strings.Contains(bootstrapBody, "bootstrap_admin_row") {
		t.Fatalf("删内置管理员行应回 409 bootstrap_admin_row,got %d(%s)", bootstrap.StatusCode, bootstrapBody)
	}

	// 第二个管理员:删自己 → 409(与 self_disable 同一先例)
	self := createUserForDeleteTest(t, admin, adminCSRF, srv.URL,
		`{"username":"admin-two","password":"adminpass1","role":"admin"}`)
	selfAdmin, selfCSRF := loginNewClient(t, srv.URL, "admin-two", "adminpass1")
	got := doJSON(t, selfAdmin, http.MethodDelete, srv.URL+"/api/admin/users/"+self.ID, selfCSRF, "")
	if got.StatusCode != http.StatusConflict {
		body := readBody(t, got)
		got.Body.Close()
		t.Fatalf("删自己 status = %d(%s), want 409", got.StatusCode, body)
	}
	got.Body.Close()

	// 未知 id → 404
	missing := doJSON(t, admin, http.MethodDelete, srv.URL+"/api/admin/users/nonexistent-id", adminCSRF, "")
	if missing.StatusCode != http.StatusNotFound {
		body := readBody(t, missing)
		missing.Body.Close()
		t.Fatalf("未知 id status = %d(%s), want 404", missing.StatusCode, body)
	}
	missing.Body.Close()

	// 普通用户 → 403(端点在 admin 子组里,RequireAdmin 先拦)
	target := createUserForDeleteTest(t, admin, adminCSRF, srv.URL,
		`{"username":"dev-alice","password":"alicepass1","role":"user"}`)
	alice, aliceCSRF := loginNewClient(t, srv.URL, "dev-alice", "alicepass1")
	noGo := doJSON(t, alice, http.MethodDelete, srv.URL+"/api/admin/users/"+target.ID, aliceCSRF, "")
	noGo.Body.Close()
	if noGo.StatusCode != http.StatusForbidden {
		t.Fatalf("普通用户删号 status = %d, want 403", noGo.StatusCode)
	}
}

// TestAdminDeleteUserBlockedByOwnedData 组长身份与个人凭据都要先处置:回 409 并带上原因。
func TestAdminDeleteUserBlockedByOwnedData(t *testing.T) {
	srv, st := setupAccessServer(t)
	admin, adminCSRF := loginNewClient(t, srv.URL, "admin", "testpass")

	owner := createUserForDeleteTest(t, admin, adminCSRF, srv.URL,
		`{"username":"dev-owner","password":"ownerpass1","role":"user"}`)

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := st.DB.Exec(
		`INSERT INTO resource_groups (id, name, description, visibility, owner_id, created_by, created_at, updated_at)
		 VALUES ('grp-owned', 'owned-group', '', 'private', ?, ?, ?, ?)`,
		owner.ID, owner.ID, now, now,
	); err != nil {
		t.Fatalf("建组夹具: %v", err)
	}

	blocked := doJSON(t, admin, http.MethodDelete, srv.URL+"/api/admin/users/"+owner.ID, adminCSRF, "")
	body := readBody(t, blocked)
	blocked.Body.Close()
	if blocked.StatusCode != http.StatusConflict || !strings.Contains(body, "user_has_groups") {
		t.Fatalf("组长未移交时应 409 user_has_groups,got %d(%s)", blocked.StatusCode, body)
	}
	if _, err := st.DB.Exec(`DELETE FROM resource_groups WHERE id = 'grp-owned'`); err != nil {
		t.Fatalf("清理组夹具: %v", err)
	}

	cred := doJSON(t, admin, http.MethodPost, srv.URL+"/api/credentials", adminCSRF,
		`{"name":"erin-token","type":"git_token","scope":"personal","ownerId":"`+owner.ID+`","secret":"ghp_0123456789abcdefa91f"}`)
	credBody := readBody(t, cred)
	cred.Body.Close()
	if cred.StatusCode != http.StatusCreated {
		t.Fatalf("建个人凭据夹具 status = %d(%s)", cred.StatusCode, credBody)
	}

	blocked2 := doJSON(t, admin, http.MethodDelete, srv.URL+"/api/admin/users/"+owner.ID, adminCSRF, "")
	body2 := readBody(t, blocked2)
	blocked2.Body.Close()
	if blocked2.StatusCode != http.StatusConflict || !strings.Contains(body2, "user_has_credentials") {
		t.Fatalf("仍有个人凭据时应 409 user_has_credentials,got %d(%s)", blocked2.StatusCode, body2)
	}

	// 账号必须一行没少:阻断不是「删了一半」
	check := doJSON(t, admin, http.MethodGet, srv.URL+"/api/admin/users/"+owner.ID, "", "")
	check.Body.Close()
	if check.StatusCode != http.StatusOK {
		t.Fatalf("被阻断后账号应仍在,got %d", check.StatusCode)
	}
}
