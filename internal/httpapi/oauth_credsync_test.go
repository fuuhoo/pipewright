// oauth_credsync_test.go 是「登录即同步凭据」的 HTTP 端到端用例。
//
// 为什么单开一个文件而不是塞进 oauth_login_test.go:那一份测的是「谁能登进哪个账号」,
// 这一份测的是「登进来这一次顺手换走了什么」——多写进保险库一条凭据、多要一档仓库读权限、
// 多一条来路说明。断言的落点不同(凭据页、账户页、审计、管理端保存校验),混在一起
// 任何一处改动都要重读另一处的上下文。
//
// 装配仍然全是真的(oauth + identity + vault + users + auth + audit),只有 git 平台是
// httptest 假冒的 —— 「令牌明文到底落到哪一列、谁能取回它」只有过一遍真保险库才测得准。
package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/oauth"
	"github.com/fuuhoo/pipewright/internal/vault"
)

// syncTokenBody 是「开了凭据同步的 git 应用」会回的那一档:带时效与续期凭证。
// 三样都得有 —— 只回 access_token 是自建 Gitea 的形态,那种令牌进不了续期队列,
// 用它测「同步」只能证明写进去了一条凭据,证明不了到期之后还能自己续上。
const syncTokenBody = `{"access_token":"synced-token","refresh_token":"synced-refresh",` +
	`"expires_in":3600,"scope":"read_repository read_user"}`

// newSyncLoginTestEnv 与 newLoginTestEnv 同形,只差两点:登录应用开了凭据同步,
// 以及 mock 平台的令牌响应可控。
func newSyncLoginTestEnv(t *testing.T, userBody, tokenBody string) *loginTestEnv {
	t.Helper()
	stub := newStubGitProviderToken(t, userBody, tokenBody)
	e := buildLoginEnv(t, stub.srv.Client(), func(oauthSvc oauth.Service) {
		if _, err := oauthSvc.SaveLoginApp(context.Background(), oauth.SaveLoginAppInput{
			Provider: oauth.ProviderCustom, BaseURL: stub.srv.URL,
			ClientID: "login-cid", ClientSecret: sp("login-secret"),
			Enabled: true, SyncCredentials: true,
		}); err != nil {
			t.Fatalf("SaveLoginApp: %v", err)
		}
		// 绑定流读的是 oauth_apps;不开它两条已登录流程必然 422,与要测的行为无关。
		if _, err := oauthSvc.SaveApp(context.Background(), oauth.SaveAppInput{
			Provider: oauth.ProviderCustom, BaseURL: stub.srv.URL,
			ClientID: "app-cid", ClientSecret: sp("app-secret"), Enabled: true,
		}); err != nil {
			t.Fatalf("SaveApp: %v", err)
		}
	})
	e.stub = stub
	return e
}

// listCredentials 以会话客户端取回「自己看得见的凭据」(非 admin 只看自己的 personal)。
func (e *loginTestEnv) listCredentials(t *testing.T, c *http.Client) []credentialDTO {
	t.Helper()
	resp := e.get(t, c, e.srv.URL+"/api/credentials")
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET /api/credentials status = %d, want 200, body=%s", resp.StatusCode, raw)
	}
	var out []credentialDTO
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode credentials: %v", err)
	}
	return out
}

// listIdentities 取回当前会话的外部身份清单。
func (e *loginTestEnv) listIdentities(t *testing.T, c *http.Client) []identityBindingDTO {
	t.Helper()
	resp := e.get(t, c, e.srv.URL+"/api/account/identities")
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET /api/account/identities status = %d, want 200, body=%s", resp.StatusCode, raw)
	}
	var out []identityBindingDTO
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode identities: %v", err)
	}
	return out
}

// userID 按登录名取本地账号 id(断言凭据归属要看 id,不能只看用户名 —— 用户名可改)。
func (e *loginTestEnv) userID(t *testing.T, username string) string {
	t.Helper()
	var id string
	if err := e.db.QueryRow(`SELECT id FROM users WHERE username = ?`, username).Scan(&id); err != nil {
		t.Fatalf("取用户 %s 的 id: %v", username, err)
	}
	return id
}

// authorizeScope 读这次登录向平台申请的 scope 串(权限升级是否真的发生,就看它)。
func (e *loginTestEnv) authorizeScope(t *testing.T, c *http.Client) string {
	t.Helper()
	resp := e.get(t, c, e.srv.URL+"/api/auth/oauth/"+oauth.ProviderCustom+"/authorize")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize status = %d, want 302", resp.StatusCode)
	}
	return locationOf(t, resp).Query().Get("scope")
}

// TestCredentialSyncProducesPersonalCredential 是主路径:登录 → 一条 personal 凭据 →
// 凭据页看得到、账户页带出时效、审计记下凭据 id、明文只进保险库。
func TestCredentialSyncProducesPersonalCredential(t *testing.T) {
	e := newSyncLoginTestEnv(t, `{"id":7100,"username":"gina","email":"gina@corp.example.com","name":"吉娜"}`, syncTokenBody)
	c := newTestClient(t)

	// 开了同步 = 管理员明确要求「顺带把仓库读权限也带回来」:scope 必须升到那档。
	if got := e.authorizeScope(t, c); got != "read_repository read_user" {
		t.Fatalf("同步打开时的授权 scope = %q, want read_repository read_user", got)
	}

	if resp := e.loginOnce(t, c, oauth.ProviderCustom); resp.StatusCode != http.StatusFound {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("callback status = %d, want 302, body=%s", resp.StatusCode, raw)
	}

	creds := e.listCredentials(t, c)
	if len(creds) != 1 {
		t.Fatalf("同步后凭据条数 = %d, want 1(每人每次登录只该轮换同一条)", len(creds))
	}
	got := creds[0]
	// 作用域必须是 personal:令牌进了全局池 = 全站流水线都能拿别人的授权克隆私有仓库。
	if got.Scope != "personal" || got.Type != string(vault.TypeGitToken) {
		t.Fatalf("同步出来的凭据必须本人私有且是 git 令牌: %+v", got)
	}
	if got.OwnerID != e.userID(t, "gina") {
		t.Fatalf("凭据归属 = %q, want 本次登录落到的账号 id", got.OwnerID)
	}
	// 用户名位带真实账号名:Gitee 用 token 克隆时用户名必须是本人账号,填 "git" 会被拒。
	if got.Username != "gina" {
		t.Fatalf("凭据用户名 = %q, want gina(Gitee 要真实账号名)", got.Username)
	}
	// 来路说明:这条凭据用户没建过,列表里必须说清是谁给的。
	if !strings.Contains(got.Description, "自动同步") || !strings.Contains(got.Description, "gina") {
		t.Fatalf("凭据备注应写明来路: %q", got.Description)
	}
	if strings.Contains(got.MaskedValue, "synced-token") {
		t.Fatalf("掩码位泄漏了令牌明文: %q", got.MaskedValue)
	}

	// 账户页:这条绑定同步过哪条凭据、什么时候到期、平台实授了什么。
	bindings := e.listIdentities(t, c)
	if len(bindings) != 1 {
		t.Fatalf("身份清单条数 = %d, want 1", len(bindings))
	}
	b := bindings[0]
	if b.CredentialID != got.ID {
		t.Fatalf("清单里的 credentialId = %q, want %q", b.CredentialID, got.ID)
	}
	if exp, err := time.Parse(time.RFC3339, b.ExpiresAt); err != nil {
		t.Fatalf("expiresAt 不是可读时刻: %q (%v)", b.ExpiresAt, err)
	} else if d := time.Until(exp); d < 55*time.Minute || d > 61*time.Minute {
		t.Fatalf("expiresAt = %v, 应以令牌 3600s 时效为基准(偏差 %v)", exp, d)
	}
	if b.Scope != "read_repository read_user" {
		t.Fatalf("清单里的 scope = %q, want 平台实授串", b.Scope)
	}

	// 审计:一次登录多拿走了一份仓库权限,事后要能追到那条凭据。
	entries := e.auditOf(t, audit.ActionOauthLogin)
	if len(entries) != 1 {
		t.Fatalf("登录审计条数 = %d, want 1", len(entries))
	}
	if entries[0].Detail["credentialId"] != got.ID {
		t.Fatalf("登录审计应带 credentialId: %+v", entries[0].Detail)
	}

	// 明文确实进了保险库(而不是随手丢在别处):只有 admin 的 reveal 端点取得回它。
	admin, adminCSRF := e.adminClient(t)
	reveal := doJSON(t, admin, http.MethodPost, e.srv.URL+"/api/credentials/"+got.ID+"/reveal", adminCSRF, "")
	if reveal.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(reveal.Body)
		t.Fatalf("reveal status = %d, want 200, body=%s", reveal.StatusCode, raw)
	}
	var secret struct {
		Secret string `json:"secret"`
	}
	if err := json.NewDecoder(reveal.Body).Decode(&secret); err != nil {
		t.Fatalf("decode reveal: %v", err)
	}
	if secret.Secret != "synced-token" {
		t.Fatalf("凭据明文 = %q, want 这次授权换来的那张令牌", secret.Secret)
	}

	// 续期材料同库同法加密:库里既有密文也查不到明文,才能确认没有第二处落点。
	if n := e.count(t, `SELECT COUNT(1) FROM credentials WHERE ciphertext LIKE '%synced-token%'`); n != 0 {
		t.Fatalf("凭据密文列里出现了令牌明文(%d 行)", n)
	}
	if n := e.count(t, `SELECT COUNT(1) FROM user_identities WHERE git_refresh_ciphertext LIKE '%synced-refresh%'`); n != 0 {
		t.Fatalf("续期凭证明文落库(%d 行)", n)
	}
}

// TestCredentialSyncIdempotentAcrossLogins 幂等:重复登录只轮换那一条凭据。
// 每次登录新建一条会把人的凭据页淹掉,而多条同令牌更难判断哪条被流水线用着。
func TestCredentialSyncIdempotentAcrossLogins(t *testing.T) {
	e := newSyncLoginTestEnv(t, `{"id":7101,"username":"hank"}`, syncTokenBody)
	c := newTestClient(t)
	_ = e.loginOnce(t, c, oauth.ProviderCustom)
	afterFirst := e.listCredentials(t, c)
	if len(afterFirst) != 1 {
		t.Fatalf("首登后凭据条数 = %d, want 1", len(afterFirst))
	}
	_ = e.loginOnce(t, c, oauth.ProviderCustom)
	afterSecond := e.listCredentials(t, c)
	if len(afterSecond) != 1 {
		t.Fatalf("二次登录又新建了凭据:%d 条, want 1", len(afterSecond))
	}
	if afterSecond[0].ID != afterFirst[0].ID {
		t.Fatalf("二次登录应轮换同一条凭据,id 从 %s 变成了 %s", afterFirst[0].ID, afterSecond[0].ID)
	}
	if n := e.count(t, `SELECT COUNT(1) FROM credentials`); n != 1 {
		t.Fatalf("库里凭据总数 = %d, want 1", n)
	}
	if n := e.count(t, `SELECT COUNT(1) FROM user_identities`); n != 1 {
		t.Fatalf("重复登录不该多出一条映射:现有 %d 条", n)
	}
}

// TestNoCredentialWhenSyncDisabled 反向守卫:开关关着(默认)时,登录一个字都不往保险库写。
// 这条用例的意义在于「打开它之前登录过的人」不会被升级出新凭据 —— 权限升级必须是显式的。
func TestNoCredentialWhenSyncDisabled(t *testing.T) {
	e := newLoginTestEnv(t, `{"id":7102,"username":"iris"}`, nil)
	c := newTestClient(t)

	// 默认只申请身份 scope:没开同步就白要仓库读权限,是最难被发现的越权。
	if got := e.authorizeScope(t, c); got != "read_user" {
		t.Fatalf("同步关闭时的授权 scope = %q, want read_user", got)
	}
	if resp := e.loginOnce(t, c, oauth.ProviderCustom); resp.StatusCode != http.StatusFound {
		t.Fatalf("登录应照常成功,回调状态 = %d", resp.StatusCode)
	}
	if creds := e.listCredentials(t, c); len(creds) != 0 {
		t.Fatalf("未开同步却写出了凭据:%+v", creds)
	}
	if n := e.count(t, `SELECT COUNT(1) FROM credentials`); n != 0 {
		t.Fatalf("库里凭据总数 = %d, want 0", n)
	}
	if b := e.listIdentities(t, c); len(b) != 1 || b[0].CredentialID != "" || b[0].ExpiresAt != "" {
		t.Fatalf("未同步时清单不该带凭据与时效: %+v", b)
	}
	entries := e.auditOf(t, audit.ActionOauthLogin)
	if len(entries) != 1 {
		t.Fatalf("登录审计条数 = %d, want 1", len(entries))
	}
	if _, ok := entries[0].Detail["credentialId"]; ok {
		t.Fatalf("没同步凭据却记了 credentialId: %+v", entries[0].Detail)
	}
}

// TestAdminSaveValidatesCredentialSyncSwitch 是管理端保存这个开关时的两头:
// 钉钉 / 飞书 / 企业微信给不出可用于克隆仓库的令牌,开同步必须 422,而不是静默收下 ——
// 静默忽略会让人以为开了,然后一直等不到凭据;反过来 git 档必须原样回显,
// 且「省略」要等于关闭(否则一次权限升级就关不掉)。
func TestAdminSaveValidatesCredentialSyncSwitch(t *testing.T) {
	e := newLoginTestEnv(t, `{"id":7103,"username":"jay"}`, nil)
	admin, csrf := e.adminClient(t)

	cases := []struct {
		provider string
		body     string
	}{
		{oauth.ProviderDingTalk, `{"clientId":"dt-cid","clientSecret":"s","syncCredentials":true}`},
		{oauth.ProviderFeishu, `{"clientId":"fs-cid","clientSecret":"s","syncCredentials":true}`},
		{oauth.ProviderWeCom, `{"clientId":"wc-cid","clientSecret":"s","agentId":"1000002","syncCredentials":true}`},
	}
	for _, tc := range cases {
		resp := doJSON(t, admin, http.MethodPut,
			e.srv.URL+"/api/admin/oauth/login-apps/"+tc.provider, csrf, tc.body)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			raw, _ := io.ReadAll(resp.Body)
			t.Fatalf("%s 开同步 status = %d, want 422, body=%s", tc.provider, resp.StatusCode, raw)
		}
		if code := decodeErrCode(t, resp); code != "sync_unsupported" {
			t.Fatalf("%s 开同步错误码 = %q, want sync_unsupported", tc.provider, code)
		}
	}

	// git 档则应原样收下并回显 —— 前端那张开关只有在这四档上才点得动。
	ok := doJSON(t, admin, http.MethodPut,
		e.srv.URL+"/api/admin/oauth/login-apps/"+oauth.ProviderCustom, csrf,
		`{"clientId":"login-cid","baseUrl":"`+e.stub.srv.URL+`","clientSecret":"login-secret","enabled":true,"syncCredentials":true}`)
	if ok.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(ok.Body)
		t.Fatalf("custom 开同步 status = %d, want 200, body=%s", ok.StatusCode, raw)
	}
	var dto oauthLoginAppDTO
	if err := json.NewDecoder(ok.Body).Decode(&dto); err != nil {
		t.Fatalf("decode login app: %v", err)
	}
	if !dto.SyncCredentials {
		t.Fatalf("git 档保存后应回显 syncCredentials=true: %+v", dto)
	}
	// 关回去也必须是「整份覆盖」语义(省略即关闭),否则权限升级关不掉。
	off := doJSON(t, admin, http.MethodPut,
		e.srv.URL+"/api/admin/oauth/login-apps/"+oauth.ProviderCustom, csrf,
		`{"clientId":"login-cid","baseUrl":"`+e.stub.srv.URL+`","clientSecret":"login-secret","enabled":true}`)
	var offDTO oauthLoginAppDTO
	if err := json.NewDecoder(off.Body).Decode(&offDTO); err != nil {
		t.Fatalf("decode login app: %v", err)
	}
	if offDTO.SyncCredentials {
		t.Fatalf("省略 syncCredentials 应视为关闭: %+v", offDTO)
	}
}

// TestBindWithSyncProducesCredentialForOwner 覆盖「已登录 + 绑定」这条路:
// 管理员把自己的 git 账号绑到本地账号,同样顺手拿到一条归他本人的凭据。
// 归属必须是**当前会话这个账号**,而不是平台身份带进来的某个名字 —— 那是接管面。
func TestBindWithSyncProducesCredentialForOwner(t *testing.T) {
	e := newSyncLoginTestEnv(t, `{"id":7104,"username":"kate","name":"凯特"}`, syncTokenBody)
	admin := newTestClient(t)
	e.loginAs(t, admin, "admin", "testpass")

	resp := e.bindOnce(t, admin, oauth.ProviderCustom)
	if resp.StatusCode != http.StatusFound {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("bind 回调 status = %d, want 302, body=%s", resp.StatusCode, raw)
	}

	creds := e.listCredentials(t, admin)
	if len(creds) != 1 {
		t.Fatalf("绑定后凭据条数 = %d, want 1", len(creds))
	}
	if creds[0].OwnerID != e.userID(t, "admin") {
		t.Fatalf("凭据归属 = %q, want 当前会话账号(admin)的 id", creds[0].OwnerID)
	}
	if creds[0].Username != "kate" {
		t.Fatalf("凭据用户名应取平台登录名: %q", creds[0].Username)
	}
	bindings := e.listIdentities(t, admin)
	if len(bindings) != 1 || bindings[0].CredentialID != creds[0].ID {
		t.Fatalf("绑定清单应回指这条凭据: %+v", bindings)
	}
	entries := e.auditOf(t, audit.ActionIdentityBind)
	if len(entries) != 1 || entries[0].Detail["credentialId"] != creds[0].ID {
		t.Fatalf("绑定审计应带 credentialId: %+v", entries)
	}
}

// adminClient 登录一个管理员会话并带回可复用的 CSRF token(reveal、管理端写入都要它)。
func (e *loginTestEnv) adminClient(t *testing.T) (*http.Client, string) {
	t.Helper()
	c := newTestClient(t)
	csrf := e.loginAs(t, c, "admin", "testpass")
	if csrf == "" {
		t.Fatal("管理员登录没拿到 CSRF token")
	}
	return c, csrf
}

// TestAdminCredentialSyncGateOnOidc 冻结 OIDC 那一档的两头:
// 没声明「背后是哪家 git 平台」→ 422 sync_needs_git_platform(而不是笼统的 sync_unsupported,
// 因为这里不是「这条路走不通」而是「你还差一个回答」);声明了 → 保存成功并原样回显。
// OIDC 单列还有一个原因:它在保存期会真取一次发现文档(端点填错该在「保存」这一刀说清楚),
// 所以要配一个能拨通的 IdP,拒因也不能被端点校验顶掉。
func TestAdminCredentialSyncGateOnOidc(t *testing.T) {
	e, idp := newOidcLoginTestEnv(t, `{"sub":"keycloak-uuid-88","preferred_username":"li"}`, nil)
	admin, csrf := e.adminClient(t)
	put := func(body string) *http.Response {
		return doJSON(t, admin, http.MethodPut,
			e.srv.URL+"/api/admin/oauth/login-apps/"+oauth.ProviderOIDC, csrf, body)
	}

	// 未声明形态:明确拒绝,而不是静默把开关关掉。
	resp := put(`{"clientId":"oidc-cid","clientSecret":"oidc-secret","discoveryUrl":"` + idp.discoveryURL() +
		`","enabled":true,"syncCredentials":true}`)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("未声明形态开同步 status = %d, want 422, body=%s", resp.StatusCode, raw)
	}
	if code := decodeErrCode(t, resp); code != "sync_needs_git_platform" {
		t.Fatalf("未声明形态开同步错误码 = %q, want sync_needs_git_platform", code)
	}

	// 声明形态只填一半(形态对但没给 gitShape)仍然不通;填了才通。
	resp = put(`{"clientId":"oidc-cid","clientSecret":"oidc-secret","discoveryUrl":"` + idp.discoveryURL() +
		`","enabled":true,"gitShape":"bitbucket","syncCredentials":true}`)
	if code := decodeErrCode(t, resp); resp.StatusCode != http.StatusUnprocessableEntity || code != "git_shape_invalid" {
		t.Fatalf("未知形态应回 422 git_shape_invalid,得到 %d / %q", resp.StatusCode, code)
	}

	declared := put(`{"clientId":"oidc-cid","clientSecret":"oidc-secret","discoveryUrl":"` + idp.discoveryURL() +
		`","enabled":true,"gitShape":"gitlab","syncCredentials":true}`)
	raw, _ := io.ReadAll(declared.Body)
	_ = declared.Body.Close()
	if declared.StatusCode != http.StatusOK {
		t.Fatalf("声明 gitlab 后开同步应保存成功,得到 %d, body=%s", declared.StatusCode, raw)
	}
	var dto oauthLoginAppDTO
	if err := json.Unmarshal(raw, &dto); err != nil {
		t.Fatalf("decode dto: %v", err)
	}
	if dto.GitShape != oauth.GitShapeGitLab || !dto.SyncCredentials {
		t.Fatalf("声明与开关都应原样回显: %+v", dto)
	}
}

// TestAdminRejectsCredentialSyncOnPlatformIDs 覆盖另外三家:钉钉/飞书/企微压根没有 git 端点,
// 开同步必须是 sync_unsupported —— 「这个身份源给不出令牌」和「OIDC 还差一个声明」是两回事,
// 错误码混用了前端就只能给出一句猜错的解释。
func TestAdminRejectsCredentialSyncOnPlatformIDs(t *testing.T) {
	e := newLoginTestEnv(t, `{"id":1,"username":"u"}`, nil)
	admin, csrf := e.adminClient(t)
	for _, p := range []string{oauth.ProviderDingTalk, oauth.ProviderFeishu, oauth.ProviderWeCom} {
		// 企微硬性要 AgentId,不带就会先撞上那条校验,测不到同步这道门。
		extra := ""
		if p == oauth.ProviderWeCom {
			extra = `,"agentId":"1000001"`
		}
		resp := doJSON(t, admin, http.MethodPut,
			e.srv.URL+"/api/admin/oauth/login-apps/"+p, csrf,
			`{"clientId":"cid","clientSecret":"secret","enabled":true`+extra+`,"syncCredentials":true}`)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			raw, _ := io.ReadAll(resp.Body)
			t.Fatalf("%s 开同步 status = %d, want 422, body=%s", p, resp.StatusCode, raw)
		}
		if code := decodeErrCode(t, resp); code != "sync_unsupported" {
			t.Fatalf("%s 开同步错误码 = %q, want sync_unsupported", p, code)
		}
	}
}
