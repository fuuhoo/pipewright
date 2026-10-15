// profile_test.go 守「本地档案」这条 HTTP 面:会话载荷带不带得显示名,以及本人改档的
// 鉴权(CSRF)、归属(只能改自己那一行)与降级(旧会话没有 user_id 时不猜人)。
package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/mask"
	"github.com/fuuhoo/pipewright/internal/store"
	"github.com/fuuhoo/pipewright/internal/users"
)

// setupProfileServer 起一套「认证 + 用户服务 + 审计」的 server。
// userSyncer 传 users 服务,好让 Bootstrap 把内置管理员同步成 users 那一行 ——
// 会话载荷里的档案是靠 user_id 查 users 得来的,没有那一行就只能回退到用户名。
func setupProfileServer(t *testing.T) (*httptest.Server, *http.Client, string, *store.Store, *users.Service) {
	t.Helper()
	st := testStoreAuth(t)
	us := users.NewService(st.DB)
	svc := auth.NewService(st.DB, nil, us)
	if err := svc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	rec := audit.New(st.DB, mask.NewMasker(), nil)
	srv := httptest.NewServer(New(testWebFSAuth(), svc,
		WithAccount(svc), WithAudit(rec), WithUsers(us)))
	t.Cleanup(srv.Close)

	client := newTestClient(t)
	csrf := loginWithClient(t, client, srv.URL)
	return srv, client, csrf, st, us
}

func getProfile(t *testing.T, client *http.Client, url string) sessionPayload {
	t.Helper()
	resp := doJSON(t, client, http.MethodGet, url, "", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s status = %d, want 200; body=%s", url, resp.StatusCode, raw)
	}
	var p sessionPayload
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return p
}

// TestSessionPayloadCarriesProfile 是这次需求的核心:登录与会话探测都得把显示名/邮箱
// 交给前端,否则「用户名、昵称、邮箱三个都用到」在页面上无从谈起。
func TestSessionPayloadCarriesProfile(t *testing.T) {
	srv, client, csrf, _, us := setupProfileServer(t)

	// 内置管理员刚同步出来时档案是空的 —— 载荷照常返回,只是没有昵称。
	first := getProfile(t, client, srv.URL+"/api/auth/session")
	if first.Username != "admin" || first.DisplayName != "" || first.Email != "" {
		t.Fatalf("初始载荷错: %+v", first)
	}

	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/account/profile", csrf,
		`{"displayName":"  傅   兵\n测试 ","email":" fubin@corp.co "}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("改档 status = %d, want 200; body=%s", resp.StatusCode, raw)
	}
	var out profileDTO
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.DisplayName != "傅 兵 测试" || out.Email != "fubin@corp.co" {
		t.Fatalf("应回服务端归一化后的值: %+v", out)
	}

	// 会话探测现查 users,所以改完立刻生效,不需要重新登录。
	got := getProfile(t, client, srv.URL+"/api/auth/session")
	if got.DisplayName != "傅 兵 测试" || got.Email != "fubin@corp.co" {
		t.Fatalf("会话载荷没带上档案: %+v", got)
	}

	// 口令登录走同一个组装函数。
	loginResp := doLogin(t, srv, "admin", "testpass")
	defer loginResp.Body.Close()
	raw, _ := io.ReadAll(loginResp.Body)
	if !strings.Contains(string(raw), `"displayName":"傅 兵 测试"`) {
		t.Fatalf("登录响应没带上档案: %s", raw)
	}

	// 档案确实落在 users 那一行(而不是只活在响应里)。
	u, err := us.GetByID(users.BootstrapAdminRegularUserID)
	if err != nil {
		t.Fatalf("get admin row: %v", err)
	}
	if u.DisplayName != "傅 兵 测试" || u.Email != "fubin@corp.co" {
		t.Fatalf("库里档案失真: %+v", u)
	}
}

// TestUpdateProfileGuards 把「本人端点」该有几道闸一次验齐:CSRF、登录、部分提交、坏值。
func TestUpdateProfileGuards(t *testing.T) {
	srv, client, csrf, _, us := setupProfileServer(t)

	// 无 CSRF 头 → 403(写操作)。
	noCsrf := doJSON(t, client, http.MethodPost, srv.URL+"/api/account/profile", "", `{"displayName":"越权名"}`)
	_ = noCsrf.Body.Close()
	if noCsrf.StatusCode != http.StatusForbidden {
		t.Fatalf("缺 CSRF status = %d, want 403", noCsrf.StatusCode)
	}

	// 未登录 → 401,且不能被 requireAuth 之外的路径绕过。
	anon := newTestClient(t)
	anonResp := doJSON(t, anon, http.MethodPost, srv.URL+"/api/account/profile", "fake-csrf", `{"displayName":"匿名"}`)
	_ = anonResp.Body.Close()
	if anonResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未登录 status = %d, want 401", anonResp.StatusCode)
	}

	// 两个字段都不提交 → 400(避免一个空 body 悄悄把档案清空)。
	empty := doJSON(t, client, http.MethodPost, srv.URL+"/api/account/profile", csrf, `{}`)
	_ = empty.Body.Close()
	if empty.StatusCode != http.StatusBadRequest {
		t.Fatalf("空提交 status = %d, want 400", empty.StatusCode)
	}

	// 坏邮箱 → 400,并且不能顺手把显示名写脏(两列一条 UPDATE)。
	mustOK(t, doJSON(t, client, http.MethodPost, srv.URL+"/api/account/profile", csrf,
		`{"displayName":"保留的名字","email":"keep@corp.co"}`), http.StatusOK)
	bad := doJSON(t, client, http.MethodPost, srv.URL+"/api/account/profile", csrf,
		`{"displayName":"冒充别人","email":"not-an-email"}`)
	_ = bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("坏邮箱 status = %d, want 400", bad.StatusCode)
	}
	admin, err := us.GetByID(users.BootstrapAdminRegularUserID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if admin.DisplayName != "保留的名字" || admin.Email != "keep@corp.co" {
		t.Fatalf("校验失败仍改了档案: %+v", admin)
	}

	// 只提交邮箱时显示名保持原值(不是被 SET 成空串)。
	mustOK(t, doJSON(t, client, http.MethodPost, srv.URL+"/api/account/profile", csrf,
		`{"email":"new@corp.co"}`), http.StatusOK)
	admin, err = us.GetByID(users.BootstrapAdminRegularUserID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if admin.DisplayName != "保留的名字" || admin.Email != "new@corp.co" {
		t.Fatalf("部分提交把另一列清了: %+v", admin)
	}
}

// TestUpdateProfileWithLegacySession:0053 之前签发的会话没有 user_id。
// 此时宁可让人重登一次,也不能按用户名猜一个账号把档案写过去。
func TestUpdateProfileWithLegacySession(t *testing.T) {
	srv, client, csrf, st, us := setupProfileServer(t)

	var token string
	for _, c := range client.Jar.Cookies(mustURL(t, srv.URL)) {
		if c.Name == cookieSession {
			token = c.Value
		}
	}
	if token == "" {
		t.Fatal("setup: 会话 cookie 缺失")
	}
	if _, err := st.DB.Exec(`UPDATE sessions SET user_id = '' WHERE token = ?`, token); err != nil {
		t.Fatalf("模拟旧会话: %v", err)
	}

	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/account/profile", csrf, `{"displayName":"写给我?"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("旧会话 status = %d, want 409", resp.StatusCode)
	}
	if code := errorCode(t, resp); code != "session_without_user" {
		t.Fatalf("error code = %q, want session_without_user", code)
	}
	admin, err := us.GetByID(users.BootstrapAdminRegularUserID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if admin.DisplayName != "" {
		t.Fatalf("旧会话仍写进了档案: %+v", admin)
	}

	// 同一条旧会话读会话探测不该炸:档案查不到就只回用户名。
	p := getProfile(t, client, srv.URL+"/api/auth/session")
	if p.Username != "admin" || p.DisplayName != "" {
		t.Fatalf("旧会话载荷应降级而不报错: %+v", p)
	}
}

// TestProfileAuditKeepsNoSecrets 档案改动要留痕(自我改名可以冒充别人),
// 但这条链路挨着口令与会话,顺手确认审计里既没有 token 也没有明文口令。
func TestProfileAuditKeepsNoSecrets(t *testing.T) {
	srv, client, csrf, st, _ := setupProfileServer(t)
	mustOK(t, doJSON(t, client, http.MethodPost, srv.URL+"/api/account/profile", csrf,
		`{"displayName":" Administrator ","email":"admin@corp.co"}`), http.StatusOK)

	var action, detail string
	if err := st.DB.QueryRow(
		`SELECT action, detail_json FROM audit_log WHERE action = ? ORDER BY id DESC LIMIT 1`,
		audit.ActionProfileUpdate,
	).Scan(&action, &detail); err != nil {
		t.Fatalf("profile_update 审计缺失: %v", err)
	}
	if !strings.Contains(detail, "Administrator") || !strings.Contains(detail, "admin@corp.co") {
		t.Fatalf("审计应记下改成的新值: %s", detail)
	}
	if strings.Contains(detail, "testpass") {
		t.Fatalf("审计泄露口令: %s", detail)
	}
}

// TestAdminUserEndpointsCarryProfile 「用户管理」那条路也得读写同一个档案,
// 否则管理员建号/改名时看不见也改不了,前端就只能显示用户名。
func TestAdminUserEndpointsCarryProfile(t *testing.T) {
	srv, client, csrf := setupAdminServer(t)

	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/admin/users", csrf,
		`{"username":"erin","password":"erp12345","role":"user","displayName":"恩然","email":"erin@corp.co"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("建号 status = %d, want 201; body=%s", resp.StatusCode, raw)
	}
	var created userDTO
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.DisplayName != "恩然" || created.Email != "erin@corp.co" {
		t.Fatalf("建号响应没带回档案: %+v", created)
	}

	// 只改邮箱:显示名保持(合并语义,和本人端点一致)。
	patch := doJSON(t, client, http.MethodPatch, srv.URL+"/api/admin/users/"+created.ID, csrf,
		`{"email":"erin2@corp.co"}`)
	defer patch.Body.Close()
	if patch.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(patch.Body)
		t.Fatalf("PATCH status = %d, want 200; body=%s", patch.StatusCode, raw)
	}
	var patched userDTO
	if err := json.NewDecoder(patch.Body).Decode(&patched); err != nil {
		t.Fatalf("decode patch: %v", err)
	}
	if patched.DisplayName != "恩然" || patched.Email != "erin2@corp.co" {
		t.Fatalf("PATCH 档案合并错: %+v", patched)
	}

	// 列表也要带出来 —— 用户管理页的主标签读的是列表接口。
	list := doJSON(t, client, http.MethodGet, srv.URL+"/api/admin/users", csrf, "")
	defer list.Body.Close()
	raw, _ := io.ReadAll(list.Body)
	if !strings.Contains(string(raw), `"displayName":"恩然"`) {
		t.Fatalf("列表没带显示名: %s", raw)
	}

	// 坏邮箱按领域校验回 400,而不是 500。
	bad := doJSON(t, client, http.MethodPatch, srv.URL+"/api/admin/users/"+created.ID, csrf,
		`{"email":"two@@at"}`)
	_ = bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("坏邮箱 status = %d, want 400", bad.StatusCode)
	}
}

func mustOK(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != want {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want %d; body=%s", resp.StatusCode, want, raw)
	}
}
