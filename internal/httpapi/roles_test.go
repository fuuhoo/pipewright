package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huangchengsir/pipewright/internal/access"
	"github.com/huangchengsir/pipewright/internal/audit"
	"github.com/huangchengsir/pipewright/internal/auth"
	"github.com/huangchengsir/pipewright/internal/group"
	"github.com/huangchengsir/pipewright/internal/mask"
	"github.com/huangchengsir/pipewright/internal/role"
	"github.com/huangchengsir/pipewright/internal/storetest"
	"github.com/huangchengsir/pipewright/internal/users"
	"github.com/huangchengsir/pipewright/internal/vault"
)

// setupRoleServer 起一套「角色 + 用户 + 判定」齐全的测试 server。
// 角色服务必须同时接进 HTTP 与 access 的读端(SetRoleStore),否则自定义角色存得进库、
// 判定侧却认不出来 —— 那正是本功能最容易漏的接线,所以在这里一次装好。
func setupRoleServer(t *testing.T) *httptest.Server {
	t.Helper()
	st := storetest.Open(t)
	userSvc := users.NewService(st.DB)
	authSvc := auth.NewService(st.DB, nil, userSvc)
	if err := authSvc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	roleSvc := role.New(st.DB)
	access.SetRoleStore(roleSvc)
	// 装载一次才算装配完整:0063 播种的四档预置只存在于库里,不 Reload 就一律 fail closed,
	// 建号时 role='user' 会被当成非法角色(main.go 里就是这两行连着写)。
	if err := access.ReloadRoles(context.Background()); err != nil {
		t.Fatalf("装载角色目录: %v", err)
	}
	t.Cleanup(func() { access.SetRoleStore(nil) })

	groupSvc := group.New(st.DB)
	srv := httptest.NewServer(New(testWebFSAuth(), authSvc,
		WithVault(vault.New(st.DB, testMasterKey())),
		WithUsers(userSvc),
		WithRoles(roleSvc),
		WithGroups(groupSvc),
		WithAccess(access.NewService(groupSvc)),
		WithAudit(audit.New(st.DB, mask.NewMasker(), nil)),
	))
	t.Cleanup(srv.Close)
	return srv
}

// doRoleJSON 发一个角色请求并在用例结束时收体(角色用例常反复读同一响应,少写一遍 defer)。
func doRoleJSON(t *testing.T, client *http.Client, method, url, csrf, body string) *http.Response {
	t.Helper()
	resp := doJSON(t, client, method, url, csrf, body)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// decodeErrCode 取错误体的 code:光断状态码不够,409 之间会互相顶包
// (内置只读 / 重名 / 仍被使用是三条不同的规则)。
func decodeErrCode(t *testing.T, resp *http.Response) string {
	t.Helper()
	var out errBody
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("解析错误体: %v", err)
	}
	return out.Error.Code
}

func decodeRole(t *testing.T, resp *http.Response) roleDTO {
	t.Helper()
	var out roleDTO
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("解析角色: %v", err)
	}
	return out
}

// createRole 建自定义角色并断言 201。
func createRole(t *testing.T, admin *http.Client, csrf, srvURL, body string) roleDTO {
	t.Helper()
	resp := doRoleJSON(t, admin, http.MethodPost, srvURL+"/api/admin/roles", csrf, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("建角色 status = %d(%s), want 201", resp.StatusCode, readBody(t, resp))
	}
	return decodeRole(t, resp)
}

// assignRole 把某账号改派到指定角色。
func assignRole(t *testing.T, admin *http.Client, csrf, srvURL, userID, roleID string) {
	t.Helper()
	resp := doRoleJSON(t, admin, http.MethodPatch, srvURL+"/api/admin/users/"+userID, csrf,
		`{"role":"`+roleID+`"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("改派角色 status = %d(%s)", resp.StatusCode, readBody(t, resp))
	}
}

func sessionCaps(t *testing.T, client *http.Client, csrf, srvURL string) access.Capabilities {
	t.Helper()
	resp := doRoleJSON(t, client, http.MethodGet, srvURL+"/api/auth/session", csrf, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("读会话 status = %d(%s)", resp.StatusCode, readBody(t, resp))
	}
	var out struct {
		Capabilities access.Capabilities `json:"capabilities"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("解析会话: %v", err)
	}
	return out.Capabilities
}

// TestRolePointsDict 验证功能点字典端点:字典全列出,settings.access 标成内置专属。
func TestRolePointsDict(t *testing.T) {
	srv := setupRoleServer(t)
	client, csrf := adminSession(t, srv)
	resp := doRoleJSON(t, client, http.MethodGet, srv.URL+"/api/admin/roles/points", csrf, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("points status = %d(%s)", resp.StatusCode, readBody(t, resp))
	}
	var out struct {
		Items []permPointDTO `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("解析点字典: %v", err)
	}
	if len(out.Items) != len(access.Perms()) {
		t.Fatalf("点数量 = %d, want %d", len(out.Items), len(access.Perms()))
	}
	var found bool
	for i := range out.Items {
		it := out.Items[i]
		if it.ID == access.PermSettingsAccess {
			found = true
			if !it.BuiltinOnly {
				t.Error("settings.access 应标为内置专属(编辑器里禁用勾选)")
			}
			continue
		}
		if it.BuiltinOnly {
			t.Errorf("%s 被误标为内置专属", it.ID)
		}
	}
	if !found {
		t.Fatal("点字典缺 settings.access")
	}
}

// TestRoleListIncludesBuiltinTemplates 验证列表把内置档当模板排在最前(带点集),
// 而 0063 播种的四档预置以「库里普通角色」的身份跟着后面 —— 前端角色下拉读的就是这份名单,
// 预置档少了或还被标成内置,页面上都会变成「改不动、删不掉」。
func TestRoleListIncludesBuiltinTemplates(t *testing.T) {
	srv := setupRoleServer(t)
	client, csrf := adminSession(t, srv)
	resp := doRoleJSON(t, client, http.MethodGet, srv.URL+"/api/admin/roles", csrf, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d(%s)", resp.StatusCode, readBody(t, resp))
	}
	var out struct {
		Items []roleDTO `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("解析角色列表: %v", err)
	}
	if len(out.Items) < len(access.Roles()) {
		t.Fatalf("列表 %d 条,少于内置 %d 档", len(out.Items), len(access.Roles()))
	}
	for i, id := range access.Roles() {
		if !out.Items[i].Builtin {
			t.Fatalf("第 %d 条应是内置档,got %s", i, out.Items[i].ID)
		}
		if out.Items[i].ID != id {
			t.Errorf("内置顺序 = %s, want %s", out.Items[i].ID, id)
		}
		if got, want := strings.Join(out.Items[i].Perms, ","), strings.Join(access.PermsFor(id), ","); got != want {
			t.Errorf("%s 点集与代码表不一致:\n got %s\nwant %s", id, got, want)
		}
	}
	// 四档预置:来自库里,点集非空,Builtin=false(前端据此放开编辑与删除)。
	byID := map[string]roleDTO{}
	for _, it := range out.Items {
		byID[it.ID] = it
	}
	for _, id := range []string{"user", "developer", "ops", "viewer"} {
		it, ok := byID[id]
		if !ok {
			t.Fatalf("预置档 %q 不在角色名单里", id)
		}
		if it.Builtin {
			t.Errorf("预置档 %q 仍标为内置,页面改不动它", id)
		}
		if len(it.Perms) == 0 {
			t.Errorf("预置档 %q 点集为空,该角色的账号会一个入口都看不到", id)
		}
	}
}

// TestRoleEndpointsRequireAdmin 验证角色端点全收在 RequireAdmin 后面:
// 普通用户(哪怕有设置外的所有点)读不到名单,也写不进角色。
func TestRoleEndpointsRequireAdmin(t *testing.T) {
	srv := setupRoleServer(t)
	admin, adminCSRF := adminSession(t, srv)
	u := createUser(t, admin, adminCSRF, srv.URL, "plain-bob", "bobpass123", "user")
	_ = u

	bob, bobCSRF := loginNewClient(t, srv.URL, "plain-bob", "bobpass123")
	for _, c := range []struct {
		method, path, body string
	}{
		{http.MethodGet, "/api/admin/roles", ""},
		{http.MethodGet, "/api/admin/roles/points", ""},
		{http.MethodPost, "/api/admin/roles", `{"name":"越权角色","perms":["run.view"]}`},
		{http.MethodPatch, "/api/admin/roles/viewer", `{"description":"x"}`},
		{http.MethodDelete, "/api/admin/roles/viewer", ""},
		{http.MethodPost, "/api/admin/roles/viewer/copy", `{"name":"越权副本"}`},
	} {
		resp := doRoleJSON(t, bob, c.method, srv.URL+c.path, bobCSRF, c.body)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s status = %d, want 403(%s)", c.method, c.path, resp.StatusCode, readBody(t, resp))
		}
	}
}

// TestRoleAssignFollowsCustomRoleOnRefresh 是本功能的实效契约:
// 角色**定义**改了,在线用户下次拉会话(刷新页面)就跟着变;换**角色归属**才需要重登。
func TestRoleAssignFollowsCustomRoleOnRefresh(t *testing.T) {
	srv := setupRoleServer(t)
	admin, adminCSRF := adminSession(t, srv)

	created := createRole(t, admin, adminCSRF, srv.URL,
		`{"name":"编排只读","description":"看得到编排与运行","perms":["dashboard.view","project.view","run.view"]}`)
	if created.Builtin {
		t.Error("新建角色不该是内置档")
	}

	u := createUser(t, admin, adminCSRF, srv.URL, "flow-carol", "carolpass1", "user")
	assignRole(t, admin, adminCSRF, srv.URL, u.ID, created.ID)

	carol, carolCSRF := loginNewClient(t, srv.URL, "flow-carol", "carolpass1")
	caps := sessionCaps(t, carol, carolCSRF, srv.URL)
	if got := strings.Join(caps.Perms, ","); got != "dashboard.view,project.view,run.view" {
		t.Fatalf("会话点集 = %s, want 三个点", got)
	}
	if caps.Settings {
		t.Error("自定义角色不该有设置入口")
	}
	// 四类上限从点集派生:编排与运行都只到 view。
	if caps.Kinds["project"] != "view" || caps.Kinds["run"] != "view" {
		t.Errorf("档位派生异常:kinds = %v", caps.Kinds)
	}

	// 改定义(去掉 run.view)—— 同一会话不重登,下次拉会话就少一个入口。
	updated := doRoleJSON(t, admin, http.MethodPatch, srv.URL+"/api/admin/roles/"+created.ID, adminCSRF,
		`{"perms":["dashboard.view","project.view"]}`)
	if updated.StatusCode != http.StatusOK {
		t.Fatalf("改点集 status = %d(%s)", updated.StatusCode, readBody(t, updated))
	}
	caps2 := sessionCaps(t, carol, carolCSRF, srv.URL)
	if got := strings.Join(caps2.Perms, ","); got != "dashboard.view,project.view" {
		t.Fatalf("改定义后点集 = %s, want 少掉 run.view(不重登就该跟上)", got)
	}
}

// TestRoleWriteGuards 覆盖写路径边界:settings.access、字典外的点、空名、非法模板、重名。
func TestRoleWriteGuards(t *testing.T) {
	srv := setupRoleServer(t)
	admin, adminCSRF := adminSession(t, srv)

	cases := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{"设置点", `{"name":"半个管理员","perms":["project.view","settings.access"]}`,
			http.StatusUnprocessableEntity, "settings_perm_not_assignable"},
		{"字典外的点", `{"name":"幽灵角色","perms":["project.view","made.up"]}`,
			http.StatusUnprocessableEntity, "unknown_perm"},
		{"空名", `{"name":"   ","perms":["project.view"]}`, http.StatusBadRequest, "invalid_role"},
		{"非法模板", `{"name":"坏模板","baseRole":"nobody","perms":["project.view"]}`,
			http.StatusBadRequest, "invalid_role"},
	}
	for _, c := range cases {
		resp := doRoleJSON(t, admin, http.MethodPost, srv.URL+"/api/admin/roles", adminCSRF, c.body)
		if resp.StatusCode != c.wantStatus {
			t.Errorf("%s: status = %d, want %d(%s)", c.name, resp.StatusCode, c.wantStatus, readBody(t, resp))
			continue
		}
		if code := decodeErrCode(t, resp); code != c.wantCode {
			t.Errorf("%s: code = %s, want %s", c.name, code, c.wantCode)
		}
	}

	// 重名按大小写不敏感:否则下拉里出现两条只差大小写的角色,谁也分不清选的是哪个。
	createRole(t, admin, adminCSRF, srv.URL, `{"name":"Release Duty","perms":["run.view"]}`)
	dup := doRoleJSON(t, admin, http.MethodPost, srv.URL+"/api/admin/roles", adminCSRF,
		`{"name":"release duty","perms":["run.view"]}`)
	if dup.StatusCode != http.StatusConflict {
		t.Fatalf("重名 status = %d, want 409(%s)", dup.StatusCode, readBody(t, dup))
	}
	if code := decodeErrCode(t, dup); code != "duplicate_name" {
		t.Errorf("重名 code = %s, want duplicate_name", code)
	}
}

// TestRoleBuiltinReadOnly 验证内置管理员档改不掉也删不掉 —— 它是模板,也是「永远有个管理员档」
// 的兜底。四档预置自 0063 起已是库里的普通角色,同一份端点就能改、能删(见末尾两条)。
func TestRoleBuiltinReadOnly(t *testing.T) {
	srv := setupRoleServer(t)
	admin, adminCSRF := adminSession(t, srv)

	patch := doRoleJSON(t, admin, http.MethodPatch, srv.URL+"/api/admin/roles/"+access.RoleAdmin, adminCSRF,
		`{"perms":["dashboard.view"]}`)
	if patch.StatusCode != http.StatusConflict {
		t.Fatalf("改内置 status = %d, want 409(%s)", patch.StatusCode, readBody(t, patch))
	}
	if code := decodeErrCode(t, patch); code != "builtin_role" {
		t.Errorf("code = %s, want builtin_role", code)
	}
	del := doRoleJSON(t, admin, http.MethodDelete, srv.URL+"/api/admin/roles/"+access.RoleAdmin, adminCSRF, "")
	if del.StatusCode != http.StatusConflict {
		t.Fatalf("删内置 status = %d, want 409(%s)", del.StatusCode, readBody(t, del))
	}
	// 预置档已可编辑:改名走同一条 PATCH,200 而不是 409。
	presetPatch := doRoleJSON(t, admin, http.MethodPatch, srv.URL+"/api/admin/roles/ops", adminCSRF,
		`{"description":"运维值班"}`)
	if presetPatch.StatusCode != http.StatusOK {
		t.Fatalf("改预置档 ops status = %d, want 200(%s)", presetPatch.StatusCode, readBody(t, presetPatch))
	}
	// 内置档读得到,未知 id 是 404 而不是 500。
	missing := doRoleJSON(t, admin, http.MethodGet, srv.URL+"/api/admin/roles/no-such-role", adminCSRF, "")
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("查不存在角色 status = %d, want 404", missing.StatusCode)
	}
}

// TestRoleCopyStripsSettingsPoint 验证「从 admin 模板复制」带出来的副本进不了设置:
// 否则复制内置档就等于批量造管理员。
func TestRoleCopyStripsSettingsPoint(t *testing.T) {
	srv := setupRoleServer(t)
	admin, adminCSRF := adminSession(t, srv)

	resp := doRoleJSON(t, admin, http.MethodPost,
		srv.URL+"/api/admin/roles/"+access.RoleAdmin+"/copy", adminCSRF, `{"name":"管理员副本"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("copy status = %d(%s)", resp.StatusCode, readBody(t, resp))
	}
	copied := decodeRole(t, resp)
	if copied.BaseRole != access.RoleAdmin {
		t.Errorf("baseRole = %q, want admin", copied.BaseRole)
	}
	for _, p := range copied.Perms {
		if p == access.PermSettingsAccess {
			t.Fatal("副本带了 settings.access,那等于又一个管理员")
		}
	}
	if len(copied.Perms) != len(access.PermsFor(access.RoleAdmin))-1 {
		t.Errorf("副本点数 = %d, want 全字典减一", len(copied.Perms))
	}
	if !access.SettingsAllowed(access.RoleAdmin) {
		t.Error("内置 admin 应可进设置")
	}
	if access.SettingsAllowed(copied.ID) {
		t.Error("判定侧缓存没跟上:副本不该有设置入口")
	}
}

// TestRoleDeleteGuards 验证「仍被使用不许删」,以及删掉之后判定立刻收起。
func TestRoleDeleteGuards(t *testing.T) {
	srv := setupRoleServer(t)
	admin, adminCSRF := adminSession(t, srv)

	created := createRole(t, admin, adminCSRF, srv.URL, `{"name":"临时值班","perms":["run.view"]}`)
	u := createUser(t, admin, adminCSRF, srv.URL, "del-dave", "davepass1", "user")
	assignRole(t, admin, adminCSRF, srv.URL, u.ID, created.ID)

	inUse := doRoleJSON(t, admin, http.MethodDelete, srv.URL+"/api/admin/roles/"+created.ID, adminCSRF, "")
	if inUse.StatusCode != http.StatusConflict {
		t.Fatalf("在用删除 status = %d, want 409", inUse.StatusCode)
	}
	body := readBody(t, inUse)
	if !strings.Contains(body, "1 个账号") {
		t.Errorf("409 文案该带人数(要先告诉管理员得改派几个),got %s", body)
	}

	// 改派到预置档(库里的一行)再删。
	assignRole(t, admin, adminCSRF, srv.URL, u.ID, "viewer")
	ok := doRoleJSON(t, admin, http.MethodDelete, srv.URL+"/api/admin/roles/"+created.ID, adminCSRF, "")
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("删除 status = %d(%s)", ok.StatusCode, readBody(t, ok))
	}
	if len(access.PermsFor(created.ID)) != 0 {
		t.Error("删掉的角色仍有点集,判定缓存没跟上")
	}
	if access.HasPerm(created.ID, "run.view") {
		t.Error("删掉的角色还能持有功能点")
	}
}

// TestRoleAuditRecorded 验证角色写操作都留痕(改角色定义等于改一批人的入口可见性)。
func TestRoleAuditRecorded(t *testing.T) {
	srv := setupRoleServer(t)
	admin, adminCSRF := adminSession(t, srv)

	created := createRole(t, admin, adminCSRF, srv.URL, `{"name":"审计样本","perms":["run.view"]}`)
	patch := doRoleJSON(t, admin, http.MethodPatch, srv.URL+"/api/admin/roles/"+created.ID, adminCSRF,
		`{"perms":["run.view","run.operate"]}`)
	if patch.StatusCode != http.StatusOK {
		t.Fatalf("改点集 status = %d(%s)", patch.StatusCode, readBody(t, patch))
	}
	copyResp := doRoleJSON(t, admin, http.MethodPost, srv.URL+"/api/admin/roles/"+created.ID+"/copy", adminCSRF,
		`{"name":"审计样本副本"}`)
	if copyResp.StatusCode != http.StatusCreated {
		t.Fatalf("copy status = %d(%s)", copyResp.StatusCode, readBody(t, copyResp))
	}
	del := doRoleJSON(t, admin, http.MethodDelete, srv.URL+"/api/admin/roles/"+created.ID, adminCSRF, "")
	if del.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d(%s)", del.StatusCode, readBody(t, del))
	}

	for _, action := range []string{
		audit.ActionRoleCreate, audit.ActionRoleUpdate, audit.ActionRoleCopy, audit.ActionRoleDelete,
	} {
		resp := doRoleJSON(t, admin, http.MethodGet, srv.URL+"/api/audit?action="+action, adminCSRF, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("读 %s 审计 status = %d(%s)", action, resp.StatusCode, readBody(t, resp))
		}
		var out struct {
			Entries []auditEntryDTO `json:"entries"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("解析审计: %v", err)
		}
		if len(out.Entries) == 0 {
			t.Errorf("%s 没有审计记录", action)
			continue
		}
		for _, e := range out.Entries {
			if e.TargetType != audit.TargetRole {
				t.Errorf("%s 的 targetType = %s, want role", action, e.TargetType)
			}
			if e.Actor == "" {
				t.Errorf("%s 的 actor 为空", action)
			}
		}
	}
}

// TestRoleServiceUnavailable 验证未装配角色服务时端点 503 而不是 panic。
func TestRoleServiceUnavailable(t *testing.T) {
	st := storetest.Open(t)
	authSvc := auth.NewService(st.DB, nil, users.NewService(st.DB))
	if err := authSvc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	srv := httptest.NewServer(New(testWebFSAuth(), authSvc,
		WithAudit(audit.New(st.DB, mask.NewMasker(), nil))))
	t.Cleanup(srv.Close)

	client, csrf := adminSession(t, srv)
	resp := doRoleJSON(t, client, http.MethodGet, srv.URL+"/api/admin/roles", csrf, "")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}
