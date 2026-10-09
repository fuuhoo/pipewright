// 构建环境整表导入/导出 + 批量检查的 HTTP 契约(v6.2 §3.1 之外的运维能力)。
//
// 守住四件事:
//   - 导出文件里没有凭据引用,也没有镜像检查态(跨实例搬运只会指错)。
//   - 导入的 dryRun 预览一行都不写;落库路径才写。
//   - skip 保住现有行(含 credentialId),overwrite 沿用同一行 id(流水线引用不断)。
//   - 两个端点都是 admin-only;check-batch 空选 400、无 checker 503。
package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/buildenv"
	"github.com/fuuhoo/pipewright/internal/mask"
	"github.com/fuuhoo/pipewright/internal/storetest"
	"github.com/fuuhoo/pipewright/internal/users"
	"github.com/fuuhoo/pipewright/internal/vault"
)

// setupTransferServer 构造带构建环境服务的 admin server;withChecker=false 时
// 传 nil checker(导入的「先检查再启用」路径自然跳过,断言只看落库结果)。
func setupTransferServer(t *testing.T, withChecker bool) (*httptest.Server, *buildenv.Service) {
	t.Helper()
	st := storetest.Open(t)
	svc := auth.NewService(st.DB, nil, users.NewService(st.DB))
	if err := svc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	repo := buildenv.NewSQLiteRepo(st.DB)
	bSvc := buildenv.NewService(repo)
	var chk *buildenv.Checker
	if withChecker {
		// bin 指向不存在的命令:任何真探测都会失败,但测试只走「行不存在」分支,不会 exec。
		chk = buildenv.NewChecker(repo, nil, "definitely-not-a-real-binary", 2, 0)
	}
	srv := httptest.NewServer(New(testWebFSAuth(), svc,
		WithVault(vault.New(st.DB, testMasterKey())),
		WithAudit(audit.New(st.DB, mask.NewMasker(), nil)),
		WithBuildEnvs(bSvc, chk),
		WithUsers(users.NewService(st.DB)),
	))
	t.Cleanup(srv.Close)
	return srv, bSvc
}

// adminSession 登录 admin 并返回 (client, csrf)。
func adminSession(t *testing.T, srv *httptest.Server) (*http.Client, string) {
	t.Helper()
	client := newTestClient(t)
	return client, loginAs(t, client, srv.URL, "admin", "testpass")
}

func createEnvViaAPI(t *testing.T, client *http.Client, srv *httptest.Server, csrf, body string) map[string]any {
	t.Helper()
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/admin/build-envs", csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create env: %d %s", resp.StatusCode, readAll(t, resp))
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(readAll(t, resp)), &out); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	return out
}

// TestExportBuildEnvs 导出:文本下发 + 附件名 + 不含凭据/检查态;includeDisabled=0 只要启用行。
func TestExportBuildEnvs(t *testing.T) {
	srv, _ := setupTransferServer(t, false)
	client, csrf := adminSession(t, srv)
	createEnvViaAPI(t, client, srv, csrf,
		`{"language":"impx","version":"1","displayName":"Imp X 1","description":"带凭据的启用行","sourceType":"custom","image":"registry.internal/impx:1","credentialId":"cred-secret-id","enabled":true}`)
	createEnvViaAPI(t, client, srv, csrf,
		`{"language":"impx","version":"2","displayName":"Imp X 2","sourceType":"official","image":"impx:2","enabled":false}`)

	resp := doJSON(t, client, http.MethodGet, srv.URL+"/api/admin/build-envs/export", csrf, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export status = %d: %s", resp.StatusCode, readAll(t, resp))
	}
	if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "application/json") {
		t.Fatalf("导出应走文本下发,否则前端拿不到原文: %q", ct)
	}
	cd := resp.Header.Get("Content-Disposition")
	if !strings.HasPrefix(cd, `attachment; filename="build-envs-`) ||
		(!strings.HasSuffix(cd, ".yaml\"")) {
		t.Fatalf("附件名不符: %q", cd)
	}
	text := readAll(t, resp)
	for _, banned := range []string{"cred-secret-id", "credential", "imageCheck", "imageCheckedAt", "\"id\"", "id:"} {
		if strings.Contains(text, banned) {
			t.Fatalf("导出不应含 %q:\n%s", banned, text)
		}
	}
	if !strings.Contains(text, "registry.internal/impx:1") || !strings.Contains(text, "impx:2") {
		t.Fatalf("默认应含禁用条目:\n%s", text)
	}

	resp2 := doJSON(t, client, http.MethodGet, srv.URL+"/api/admin/build-envs/export?format=json&includeDisabled=0", csrf, "")
	defer resp2.Body.Close()
	text2 := readAll(t, resp2)
	if strings.Contains(text2, "impx:2") || !strings.Contains(text2, "impx:1") {
		t.Fatalf("includeDisabled=0 应只导启用行:\n%s", text2)
	}
	var doc struct {
		Version   int `json:"version"`
		BuildEnvs []struct {
			Language string `json:"language"`
			Enabled  bool   `json:"enabled"`
		} `json:"buildEnvs"`
	}
	if err := json.Unmarshal([]byte(text2), &doc); err != nil {
		t.Fatalf("JSON 导出应可解析: %v\n%s", err, text2)
	}
	if doc.Version != buildenv.TransferVersion || len(doc.BuildEnvs) != 1 || !doc.BuildEnvs[0].Enabled {
		t.Fatalf("JSON 导出结构不符: %+v", doc)
	}
}

// TestImportBuildEnvs_DryRunWritesNothing 预览逐行给计划,但一行都不落库。
func TestImportBuildEnvs_DryRunWritesNothing(t *testing.T) {
	srv, bSvc := setupTransferServer(t, false)
	client, csrf := adminSession(t, srv)

	body := `{"dryRun":true,"content":"version: 1\nbuildEnvs:\n  - language: impx\n    version: \"9\"\n    displayName: Imp X 9\n    image: impx:9\n"}`
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/admin/build-envs/import", csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dryRun status = %d: %s", resp.StatusCode, readAll(t, resp))
	}
	var report struct {
		DryRun  bool `json:"dryRun"`
		Summary struct {
			Total   int `json:"total"`
			Created int `json:"created"`
		} `json:"summary"`
		Results []struct {
			Action   string `json:"action"`
			Language string `json:"language"`
		} `json:"results"`
	}
	raw := readAll(t, resp)
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		t.Fatalf("decode: %v\n%s", err, raw)
	}
	if !report.DryRun || report.Summary.Total != 1 || report.Summary.Created != 1 {
		t.Fatalf("预览汇总不符: %s", raw)
	}
	if report.Results[0].Action != buildenv.ResultActionCreated {
		t.Fatalf("新行应计划为 created: %s", raw)
	}
	if n := countEnvLanguage(t, bSvc, "impx"); n != 0 {
		t.Fatalf("dryRun 不该写库,现有 %d 行", n)
	}
}

// TestImportBuildEnvs_SkipKeepsRow 跳过模式不动现有行(id、镜像、凭据都不变)。
func TestImportBuildEnvs_SkipKeepsRow(t *testing.T) {
	srv, bSvc := setupTransferServer(t, false)
	client, csrf := adminSession(t, srv)
	created := createEnvViaAPI(t, client, srv, csrf,
		`{"language":"impx","version":"1","displayName":"Keep me","sourceType":"custom","image":"registry.internal/impx:1","credentialId":"cred-keep","enabled":true}`)

	content := "version: 1\nbuildEnvs:\n  - language: impx\n    version: \"1\"\n    displayName: 覆盖我\n    sourceType: official\n    image: evil.example.com/x:1\n"
	body, err := json.Marshal(map[string]any{"content": content})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/admin/build-envs/import", csrf, string(body))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("import status = %d: %s", resp.StatusCode, readAll(t, resp))
	}
	var report buildenv.ImportReport
	if err := json.Unmarshal([]byte(readAll(t, resp)), &report); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if report.Summary.Skipped != 1 || report.Results[0].ID != created["id"] {
		t.Fatalf("应为 skipped 且回带现有 id: %+v", report.Results)
	}
	if n := countEnvLanguage(t, bSvc, "impx"); n != 1 {
		t.Fatalf("跳过模式不该新增行: %d", n)
	}
	got, err := bSvc.GetByID(toString(created["id"]))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Image != "registry.internal/impx:1" || got.DisplayName != "Keep me" || got.CredentialID != "cred-keep" {
		t.Fatalf("跳过模式改动了现有行: %+v", got)
	}
}

// TestImportBuildEnvs_OverwriteKeepsIDAndCredential 覆盖模式沿用同一行 id 与凭据引用;
// 未检查的镜像仍不能直接启用(P0 #4 不因导入开后门)。
func TestImportBuildEnvs_OverwriteKeepsIDAndCredential(t *testing.T) {
	srv, bSvc := setupTransferServer(t, false)
	client, csrf := adminSession(t, srv)
	created := createEnvViaAPI(t, client, srv, csrf,
		`{"language":"impx","version":"1","displayName":"Old","sourceType":"custom","image":"registry.internal/impx:1","credentialId":"cred-x","enabled":true}`)

	content := "version: 1\nbuildEnvs:\n  - language: impx\n    version: \"1\"\n    displayName: New\n    description: 导入改的\n    sourceType: custom\n    image: registry.internal/impx:2\n    enabled: true\n"
	body, _ := json.Marshal(map[string]any{"content": content, "mode": "overwrite"})
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/admin/build-envs/import", csrf, string(body))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("import status = %d: %s", resp.StatusCode, readAll(t, resp))
	}
	var report buildenv.ImportReport
	if err := json.Unmarshal([]byte(readAll(t, resp)), &report); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if report.Summary.Updated != 1 || report.Results[0].ID != created["id"] {
		t.Fatalf("应为 updated 且 id 不变: %+v", report.Results)
	}
	if report.Results[0].Enabled {
		t.Fatalf("镜像未检查过不该被导入启用: %+v", report.Results[0])
	}
	if !strings.Contains(report.Results[0].Reason, "未检查") {
		t.Fatalf("要说明为什么没启用: %q", report.Results[0].Reason)
	}
	got, err := bSvc.GetByID(toString(created["id"]))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.DisplayName != "New" || got.Image != "registry.internal/impx:2" {
		t.Fatalf("覆盖更新未落库: %+v", got)
	}
	if got.CredentialID != "cred-x" {
		t.Fatalf("凭据引用必须保住(文件里没有这个概念): %q", got.CredentialID)
	}
	if got.ImageCheckStatus != buildenv.StatusUnchecked {
		t.Fatalf("镜像变了,检查态应重置: %q", got.ImageCheckStatus)
	}
}

// TestImportBuildEnvs_BadRequests 坏内容/坏 mode/未登录/非 admin 的入口表现。
func TestImportBuildEnvs_BadRequests(t *testing.T) {
	srv, _ := setupTransferServer(t, false)
	client, csrf := adminSession(t, srv)

	cases := []struct {
		name string
		body string
		want int
		code string
	}{
		{"空内容", `{"content":""}`, http.StatusBadRequest, "invalid_input"},
		{"未知字段", `{"content":"version: 1\nbuildEnvs:\n  - language: impx\n    version: \"1\"\n    display_name: 下划线\n"}`, http.StatusBadRequest, "invalid_content"},
		{"坏 mode", `{"content":"version: 1\nbuildEnvs:\n  - language: impx\n    version: \"1\"\n    displayName: A\n    image: impx:1\n","mode":"force"}`, http.StatusBadRequest, "invalid_mode"},
	}
	for _, c := range cases {
		resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/admin/build-envs/import", csrf, c.body)
		raw := readAll(t, resp)
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Fatalf("%s: status = %d want %d: %s", c.name, resp.StatusCode, c.want, raw)
		}
		if !strings.Contains(raw, c.code) {
			t.Fatalf("%s: 错误码应含 %q,实际 %s", c.name, c.code, raw)
		}
	}

	// 无 CSRF 的写请求应被拦下(与其他 admin 写端点一致)。
	noCSRF := doJSON(t, client, http.MethodPost, srv.URL+"/api/admin/build-envs/import", "", `{"content":"x"}`)
	noCSRF.Body.Close()
	if noCSRF.StatusCode != http.StatusForbidden {
		t.Fatalf("导入缺 CSRF 应 403, got %d", noCSRF.StatusCode)
	}

	// 普通用户/未登录够不着这两个端点。
	anon := newTestClient(t)
	resp := doJSON(t, anon, http.MethodGet, srv.URL+"/api/admin/build-envs/export", "", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未登录导出应 401, got %d", resp.StatusCode)
	}
	resp2 := doJSON(t, anon, http.MethodPost, srv.URL+"/api/admin/build-envs/import", "", `{"content":"x"}`)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未登录导入应 401, got %d", resp2.StatusCode)
	}
}

// countEnvLanguage 数一下某个 language 的行数(测试用独立 language 前缀,避开 seed 干扰)。
func countEnvLanguage(t *testing.T, svc *buildenv.Service, language string) int {
	t.Helper()
	list, err := svc.List(buildenv.ListFilter{Language: language, IncludeDisabled: true})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	return len(list)
}

func toString(v any) string {
	s, _ := v.(string)
	return s
}

// TestCheckBatchBuildEnvs 批量检查:入参先校验,再谈 checker 是否装配。
func TestCheckBatchBuildEnvs(t *testing.T) {
	srv, _ := setupTransferServer(t, true)
	client, csrf := adminSession(t, srv)

	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/admin/build-envs/check-batch", csrf, `{"ids":[]}`)
	raw := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(raw, "empty_selection") {
		t.Fatalf("空选应 400 empty_selection: %d %s", resp.StatusCode, raw)
	}

	// 重复 id 去重后只查一条;不存在的行以「构建环境不存在」回显,不整单报错。
	resp1 := doJSON(t, client, http.MethodPost, srv.URL+"/api/admin/build-envs/check-batch", csrf,
		`{"ids":["nope-1","nope-1"]}`)
	raw1 := readAll(t, resp1)
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("check-batch status = %d: %s", resp1.StatusCode, raw1)
	}
	var batch struct {
		Items []buildenv.BatchCheckItem `json:"items"`
		OK    int                       `json:"ok"`
		Total int                       `json:"total"`
	}
	if err := json.Unmarshal([]byte(raw1), &batch); err != nil {
		t.Fatalf("decode: %v\n%s", err, raw1)
	}
	if batch.Total != 1 || len(batch.Items) != 1 || batch.OK != 0 {
		t.Fatalf("去重/汇总不符: %s", raw1)
	}
	if batch.Items[0].Status != buildenv.StatusUnavailable || !strings.Contains(batch.Items[0].Error, "不存在") {
		t.Fatalf("缺行要说明原因: %+v", batch.Items[0])
	}

	many := make([]string, maxBatchCheckIDs+1)
	for i := range many {
		many[i] = fmt.Sprintf("missing-env-%04d", i)
	}
	body, _ := json.Marshal(map[string]any{"ids": many})
	resp2 := doJSON(t, client, http.MethodPost, srv.URL+"/api/admin/build-envs/check-batch", csrf, string(body))
	raw2 := readAll(t, resp2)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest || !strings.Contains(raw2, "too_many_ids") {
		t.Fatalf("超上限应 400 too_many_ids: %d %s", resp2.StatusCode, raw2)
	}

	// checker 未装配 → 503(与其它依赖缺失端点一致)。
	noChkSrv, _ := setupTransferServer(t, false)
	c2, csrf2 := adminSession(t, noChkSrv)
	resp3 := doJSON(t, c2, http.MethodPost, noChkSrv.URL+"/api/admin/build-envs/check-batch", csrf2, `{"ids":["x"]}`)
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("checker 未装配应 503, got %d", resp3.StatusCode)
	}
}
