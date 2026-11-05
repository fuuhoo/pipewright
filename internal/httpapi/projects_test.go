package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/project"
	"github.com/fuuhoo/pipewright/internal/target"
	"github.com/fuuhoo/pipewright/internal/vault"
)

// stubProber 让 HTTP 层测试可控仓库探测结果,不触网。
type stubProber struct {
	branch   string
	err      error
	branches []string
	tags     []string
}

func (s stubProber) Probe(_ context.Context, _ string, _ string, _ string) (string, error) {
	return s.branch, s.err
}

func (s stubProber) ProbeRefs(_ context.Context, _ string, _ string, _ string) (string, []string, []string, error) {
	if s.err != nil {
		return "", nil, nil, s.err
	}
	return s.branch, s.branches, s.tags, nil
}

// setupProjectServer 构造带 auth + vault + project 的测试 server。
func setupProjectServer(t *testing.T, pr project.RemoteProber) (*httptest.Server, *http.Client, string, vault.Vault) {
	t.Helper()
	st := testStoreAuth(t)
	svc := auth.NewService(st.DB, nil, nil)
	if err := svc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	v := vault.New(st.DB, testMasterKey())
	psvc := project.New(st.DB, v, pr)
	srv := httptest.NewServer(New(testWebFSAuth(), svc, WithVault(v), WithProjects(psvc)))
	t.Cleanup(srv.Close)

	client := newTestClient(t)
	csrf := loginWithClient(t, client, srv.URL)
	return srv, client, csrf, v
}

// newGitCred 经 API 建一个 git_token 凭据,返回 id。
func newGitCred(t *testing.T, client *http.Client, srvURL, csrf, secret string) string {
	t.Helper()
	resp := doJSON(t, client, http.MethodPost, srvURL+"/api/credentials", csrf,
		`{"name":"ci","type":"git_token","secret":"`+secret+`"}`)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	id, _ := c["id"].(string)
	if id == "" {
		t.Fatalf("create credential failed: %s", raw)
	}
	return id
}

func TestProjectCreateAndList(t *testing.T) {
	srv, client, csrf, _ := setupProjectServer(t, stubProber{branch: "main"})
	credID := newGitCred(t, client, srv.URL, csrf, "ghp_LEAKMARKER_secret")

	body := `{"name":"shop","repoUrl":"https://gitee.com/acme/shop.git","credentialId":"` + credID + `"}`
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/projects", csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(raw), "LEAKMARKER") {
		t.Fatalf("create 响应泄漏凭据明文: %s", raw)
	}
	var p map[string]any
	_ = json.Unmarshal(raw, &p)
	if p["defaultBranch"] != "main" {
		t.Fatalf("defaultBranch = %v, want main(探测)", p["defaultBranch"])
	}
	if p["credentialName"] != "ci" {
		t.Fatalf("credentialName = %v, want ci", p["credentialName"])
	}
	// 新建的项目既没跑过、也没存过流水线 → 两项摘要都是空态(不是「未实现」)。
	if p["lastRunStatus"] != nil {
		t.Fatalf("lastRunStatus 新应为 null, got %v", p["lastRunStatus"])
	}
	ts, ok := p["targetServers"].([]any)
	if !ok || len(ts) != 0 {
		t.Fatalf("targetServers 新应为 [], got %v", p["targetServers"])
	}

	// 列表。
	listResp := doJSON(t, client, http.MethodGet, srv.URL+"/api/projects", "", "")
	defer listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d, want 200", listResp.StatusCode)
	}
	listRaw, _ := io.ReadAll(listResp.Body)
	if strings.Contains(string(listRaw), "LEAKMARKER") {
		t.Fatalf("list 泄漏凭据明文: %s", listRaw)
	}
	var list []map[string]any
	_ = json.Unmarshal(listRaw, &list)
	if len(list) != 1 {
		t.Fatalf("list len = %d, want 1", len(list))
	}
}

// createServerNamedAPI 经 API 登记一台指定名字的机器,返回 id。
func createServerNamedAPI(t *testing.T, client *http.Client, srvURL, csrf, credID, name, host string) string {
	t.Helper()
	resp := doJSON(t, client, http.MethodPost, srvURL+"/api/servers", csrf,
		`{"name":"`+name+`","host":"`+host+`","port":22,"user":"deploy","credentialId":"`+credID+`"}`)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("create server %s: status=%d %s", name, resp.StatusCode, raw)
	}
	var s map[string]any
	_ = json.Unmarshal(raw, &s)
	id, _ := s["id"].(string)
	if id == "" {
		t.Fatalf("create server failed: %s", raw)
	}
	return id
}

// seedPipelineSpecJSON 直插一条流水线配置(spec 原样入库),绕过保存期校验。
func seedPipelineSpecJSON(t *testing.T, db *sql.DB, projectID, specJSON string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(
		`INSERT INTO pipeline_configs (project_id, spec_json, spec_yaml, status, created_at, updated_at)
		 VALUES (?, ?, '', 'draft', ?, ?)`,
		projectID, specJSON, now, now,
	); err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
}

// TestProjectListRunAndTargetSummary 验证卡片摘要填的是真数据:
// 上次运行取该项目最近一条;目标服务器取部署节点勾选的机器(按声明顺序、去重、不含 K8s 节点)。
func TestProjectListRunAndTargetSummary(t *testing.T) {
	st := testStoreAuth(t)
	asvc := auth.NewService(st.DB, nil, nil)
	if err := asvc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	v := vault.New(st.DB, testMasterKey())
	psvc := project.New(st.DB, v, stubProber{branch: "main"})
	tsvc := target.New(st.DB, v, stubDialer{res: &target.ExecResult{Stdout: "ok"}})
	srv := httptest.NewServer(New(testWebFSAuth(), asvc,
		WithVault(v), WithProjects(psvc), WithServers(tsvc)))
	t.Cleanup(srv.Close)
	client := newTestClient(t)
	csrf := loginWithClient(t, client, srv.URL)

	gitCred := newGitCred(t, client, srv.URL, csrf, "ghp_LEAKMARKER_secret")
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/projects", csrf,
		`{"name":"shop","repoUrl":"https://gitee.com/acme/shop.git","credentialId":"`+gitCred+`"}`)
	shopRaw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var shopCreated map[string]any
	_ = json.Unmarshal(shopRaw, &shopCreated)
	shopID, _ := shopCreated["id"].(string)
	if shopID == "" {
		t.Fatalf("create project failed: %s", shopRaw)
	}
	projID := seedDiffProject(t, st.DB, "https://gitee.com/acme/other.git")

	sshCred := newSSHCredAPI(t, client, srv.URL, csrf, "LEAKMARKER_priv")
	web1 := createServerNamedAPI(t, client, srv.URL, csrf, sshCred, "web-1", "10.0.0.1")
	web2 := createServerNamedAPI(t, client, srv.URL, csrf, sshCred, "web-2", "10.0.0.2")
	k8sOnly := createServerNamedAPI(t, client, srv.URL, csrf, sshCred, "k8s-node", "10.0.0.3")

	// 部署节点勾 web-2、web-1(勾选顺序=发布顺序)+ 一个查不到的幽灵 id;K8s 节点勾 k8s-node。
	seedPipelineSpecJSON(t, st.DB, projID,
		`{"stages":[{"id":"s1","name":"发布","kind":"deploy","jobs":[`+
			`{"id":"j1","name":"发机器","type":"deploy_ssh","config":{"serverIds":"`+web2+`,`+web1+`,ghost-not-exist"}},`+
			`{"id":"j2","name":"发集群","type":"deploy_k8s","config":{"serverIds":"`+k8sOnly+`"}}]}]}`)

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	seedDiffRun(t, st.DB, projID, "main", "c1", "success", base.Add(-time.Hour))
	seedDiffRun(t, st.DB, projID, "main", "c2", "failed", base)

	listResp := doJSON(t, client, http.MethodGet, srv.URL+"/api/projects", "", "")
	defer listResp.Body.Close()
	raw, _ := io.ReadAll(listResp.Body)
	if strings.Contains(string(raw), "LEAKMARKER") {
		t.Fatalf("list 泄漏明文: %s", raw)
	}
	var list []map[string]any
	_ = json.Unmarshal(raw, &list)
	byID := map[string]map[string]any{}
	for _, p := range list {
		byID[p["id"].(string)] = p
	}

	shop := byID[shopID]
	if shop == nil {
		t.Fatalf("新建项目缺席: %s", raw)
	}
	if shop["lastRunStatus"] != nil {
		t.Fatalf("新建项目 lastRunStatus 应为 null, got %v", shop["lastRunStatus"])
	}

	seeded := byID[projID]
	if seeded == nil {
		t.Fatalf("直插项目缺席: %s", raw)
	}
	if seeded["lastRunStatus"] != "failed" {
		t.Fatalf("lastRunStatus = %v, want failed(最近一条)", seeded["lastRunStatus"])
	}
	got, _ := seeded["targetServers"].([]any)
	want := []string{"web-2", "web-1"}
	if len(got) != len(want) {
		t.Fatalf("targetServers = %v, want %v", got, want)
	}
	for i, s := range got {
		if s.(string) != want[i] {
			t.Fatalf("targetServers[%d] = %v, want %v(按勾选顺序,不含 k8s/幽灵 id): %s", i, s, want[i], raw)
		}
	}
}

func TestProjectCreateMissingCredential422(t *testing.T) {
	srv, client, csrf, _ := setupProjectServer(t, stubProber{branch: "main"})
	body := `{"name":"x","repoUrl":"https://gitee.com/a/b.git","credentialId":"no-such"}`
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/projects", csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	assertErrCode(t, resp, "credential_error")
}

func TestTestCloneCredentialError422(t *testing.T) {
	srv, client, csrf, _ := setupProjectServer(t, stubProber{err: project.ErrCredentialError})
	credID := newGitCred(t, client, srv.URL, csrf, "badtoken_LEAK")
	body := `{"repoUrl":"https://gitee.com/a/b.git","credentialId":"` + credID + `"}`
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/projects/test-clone", csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(raw), "LEAK") {
		t.Fatalf("错误体泄漏凭据明文: %s", raw)
	}
	var body2 struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &body2)
	if body2.Error.Code != "credential_error" {
		t.Fatalf("code = %q, want credential_error", body2.Error.Code)
	}
}

func TestTestCloneRepoUnreachable422(t *testing.T) {
	srv, client, csrf, _ := setupProjectServer(t, stubProber{err: project.ErrRepoUnreachable})
	credID := newGitCred(t, client, srv.URL, csrf, "tok")
	body := `{"repoUrl":"https://bad.invalid/a/b.git","credentialId":"` + credID + `"}`
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/projects/test-clone", csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	assertErrCode(t, resp, "repo_unreachable")
}

func TestTestCloneSuccess(t *testing.T) {
	srv, client, csrf, _ := setupProjectServer(t, stubProber{branch: "develop"})
	credID := newGitCred(t, client, srv.URL, csrf, "tok")
	body := `{"repoUrl":"https://gitee.com/a/b.git","credentialId":"` + credID + `"}`
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/projects/test-clone", csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out["ok"] != true || out["defaultBranch"] != "develop" {
		t.Fatalf("test-clone success body = %v", out)
	}
}

func TestProjectVaultUnconfigured422(t *testing.T) {
	// 项目服务持有未配置的 vault → 创建/test-clone 应 422 vault_unconfigured。
	st := testStoreAuth(t)
	svc := auth.NewService(st.DB, nil, nil)
	if err := svc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	v := vault.New(st.DB, nil) // 未配置
	psvc := project.New(st.DB, v, stubProber{branch: "main"})
	srv := httptest.NewServer(New(testWebFSAuth(), svc, WithVault(v), WithProjects(psvc)))
	t.Cleanup(srv.Close)
	client := newTestClient(t)
	csrf := loginWithClient(t, client, srv.URL)

	body := `{"repoUrl":"https://gitee.com/a/b.git","credentialId":"any"}`
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/projects/test-clone", csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	assertErrCode(t, resp, "vault_unconfigured")
}

func TestProjectPatchAndDelete(t *testing.T) {
	srv, client, csrf, _ := setupProjectServer(t, stubProber{branch: "main"})
	credID := newGitCred(t, client, srv.URL, csrf, "tok")
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/projects", csrf,
		`{"name":"old","repoUrl":"https://g/a.git","credentialId":"`+credID+`"}`)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var created map[string]any
	_ = json.Unmarshal(raw, &created)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("no id: %s", raw)
	}

	patchResp := doJSON(t, client, http.MethodPatch, srv.URL+"/api/projects/"+id, csrf, `{"name":"renamed"}`)
	defer patchResp.Body.Close()
	if patchResp.StatusCode != http.StatusOK {
		t.Fatalf("patch status = %d, want 200", patchResp.StatusCode)
	}
	pr, _ := io.ReadAll(patchResp.Body)
	var patched map[string]any
	_ = json.Unmarshal(pr, &patched)
	if patched["name"] != "renamed" {
		t.Fatalf("name not updated: %s", pr)
	}

	delResp := doJSON(t, client, http.MethodDelete, srv.URL+"/api/projects/"+id, csrf, "")
	defer delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", delResp.StatusCode)
	}
	delAgain := doJSON(t, client, http.MethodDelete, srv.URL+"/api/projects/"+id, csrf, "")
	defer delAgain.Body.Close()
	if delAgain.StatusCode != http.StatusNotFound {
		t.Fatalf("delete-again status = %d, want 404", delAgain.StatusCode)
	}
	assertErrCode(t, delAgain, "project_not_found")
}

func TestProjectRequiresCSRF(t *testing.T) {
	srv, client, _, _ := setupProjectServer(t, stubProber{branch: "main"})
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/projects", "",
		`{"name":"x","repoUrl":"https://g/a.git","credentialId":"c"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (missing CSRF)", resp.StatusCode)
	}
}

func TestProjectRequiresAuth(t *testing.T) {
	srv, _, _, _ := setupProjectServer(t, stubProber{branch: "main"})
	resp, err := http.Get(srv.URL + "/api/projects")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

// TestTestCloneReturnsBranchList 验证 test-clone 在响应里同时带回全部分支/tag 列表(供前端
// 「新建项目」弹窗的默认分支下拉)。失败时列表保证为 [] 而非 null,前端可安全迭代。
func TestTestCloneReturnsBranchList(t *testing.T) {
	srv, client, csrf, _ := setupProjectServer(t, stubProber{
		branch:   "main",
		branches: []string{"main", "develop", "release/1.0"},
		tags:     []string{"v1.0.0", "v1.1.0"},
	})
	credID := newGitCred(t, client, srv.URL, csrf, "tok")
	body := `{"repoUrl":"https://gitee.com/a/b.git","credentialId":"` + credID + `"}`
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/projects/test-clone", csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		OK            bool     `json:"ok"`
		DefaultBranch string   `json:"defaultBranch"`
		Branches      []string `json:"branches"`
		Tags          []string `json:"tags"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.OK || out.DefaultBranch != "main" {
		t.Fatalf("ok/defaultBranch 不对: %+v", out)
	}
	if got, want := out.Branches, []string{"main", "develop", "release/1.0"}; !equalStrings(got, want) {
		t.Fatalf("branches 不对: got %v, want %v", got, want)
	}
	if got, want := out.Tags, []string{"v1.0.0", "v1.1.0"}; !equalStrings(got, want) {
		t.Fatalf("tags 不对: got %v, want %v", got, want)
	}
}

// TestTestCloneFailureEmptyLists 验证 test-clone 失败时(422 repo_unreachable)响应正确。
// branches/tags 字段在此路径上不被返回(走 writeProjectError),前端 catch 后会走纯文本输入。
func TestTestCloneFailureEmptyLists(t *testing.T) {
	srv, client, csrf, _ := setupProjectServer(t, stubProber{err: project.ErrRepoUnreachable})
	credID := newGitCred(t, client, srv.URL, csrf, "tok")
	body := `{"repoUrl":"https://gitee.com/a/b.git","credentialId":"` + credID + `"}`
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/projects/test-clone", csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (repo_unreachable)", resp.StatusCode)
	}
}

// equalStrings 比较两个 []string 是否完全相等(顺序敏感)。
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
