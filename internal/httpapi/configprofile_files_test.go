// 配置资源的「查看文件正文 + 重新上传文件」HTTP 契约:
//   - 详情(GET /api/admin/config-profiles/{id})带 content + contentSource,列表不带
//   - 正文以磁盘权威副本为准(DB.content 只是冗余快照)
//   - POST /api/admin/config-profiles/{id}/upload 覆盖已有行的文件;内置行 403
package httpapi

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/huangchengsir/pipewright/internal/audit"
	"github.com/huangchengsir/pipewright/internal/auth"
	"github.com/huangchengsir/pipewright/internal/configprofile"
	"github.com/huangchengsir/pipewright/internal/mask"
	"github.com/huangchengsir/pipewright/internal/storetest"
	"github.com/huangchengsir/pipewright/internal/users"
	"github.com/huangchengsir/pipewright/internal/vault"
)

type cpFilesEnv struct {
	srv     *httptest.Server
	client  *http.Client
	csrf    string
	svc     *configprofile.Service
	dataDir string
	db      *sql.DB
}

func setupConfigProfileFiles(t *testing.T) *cpFilesEnv {
	t.Helper()
	st := storetest.Open(t)
	authSvc := auth.NewService(st.DB, nil, users.NewService(st.DB))
	if err := authSvc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	dataDir := t.TempDir()
	cpSvc := configprofile.NewService(configprofile.NewSQLiteRepo(st.DB), dataDir)
	srv := httptest.NewServer(New(testWebFSAuth(), authSvc,
		WithVault(vault.New(st.DB, testMasterKey())),
		WithAudit(audit.New(st.DB, mask.NewMasker(), nil)),
		WithConfigProfiles(cpSvc),
		WithUsers(users.NewService(st.DB)),
	))
	t.Cleanup(srv.Close)

	client := newTestClient(t)
	return &cpFilesEnv{
		srv:     srv,
		client:  client,
		csrf:    loginWithClient(t, client, srv.URL),
		svc:     cpSvc,
		dataDir: dataDir,
		db:      st.DB,
	}
}

// createProfileViaAPI 建一条非内置配置资源并返回其 id。
func createProfileViaAPI(t *testing.T, env *cpFilesEnv) string {
	t.Helper()
	body := `{"language":"java","configType":"maven","name":"公司私服","targetPath":"/root/.m2/settings.xml",` +
		`"content":"<settings>初始</settings>","description":"默认","enabled":true}`
	resp := doJSON(t, env.client, http.MethodPost, env.srv.URL+"/api/admin/config-profiles", env.csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d: %s", resp.StatusCode, readAll(t, resp))
	}
	var out configProfileDTO
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out.ID
}

// postConfigProfileFile 以 multipart 上传文件字段 file + 若干普通字段。
func postConfigProfileFile(t *testing.T, env *cpFilesEnv, url, filename string, content []byte, values map[string]string) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("form body: %v", err)
	}
	for k, v := range values {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatalf("form field %s: %v", k, err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, url, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-CSRF-Token", env.csrf)
	resp, err := env.client.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	return resp
}

// TestConfigProfileDetailShowsContent 详情带正文(且以磁盘副本为准),列表不带正文。
func TestConfigProfileDetailShowsContent(t *testing.T) {
	env := setupConfigProfileFiles(t)
	id := createProfileViaAPI(t, env)

	resp := doJSON(t, env.client, http.MethodGet, env.srv.URL+"/api/admin/config-profiles/"+id, env.csrf, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("detail status = %d: %s", resp.StatusCode, readAll(t, resp))
	}
	detail := decodeMap(t, resp)
	if detail["content"] != "<settings>初始</settings>" {
		t.Fatalf("详情未带正文: %v", detail["content"])
	}
	if detail["contentSource"] != "disk" {
		t.Fatalf("正文来源应为磁盘: %v", detail["contentSource"])
	}
	// 详情响应仍要保住列表侧字段
	if detail["targetPath"] != "/root/.m2/settings.xml" {
		t.Fatalf("targetPath 错: %v", detail["targetPath"])
	}

	// 磁盘副本被运维直接改过时,详情读的是磁盘那份(运行时注入容器的也就是它)
	row, err := env.svc.GetByID(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := configprofile.AtomicWriteFile(row.FilePath, []byte("<settings>磁盘上改过</settings>"), 0o644); err != nil {
		t.Fatalf("write disk: %v", err)
	}
	resp2 := doJSON(t, env.client, http.MethodGet, env.srv.URL+"/api/admin/config-profiles/"+id, env.csrf, "")
	defer resp2.Body.Close()
	if got := decodeMap(t, resp2)["content"]; got != "<settings>磁盘上改过</settings>" {
		t.Fatalf("应回磁盘正文, got %v", got)
	}

	// 列表保持精简:不带 content
	listResp := doJSON(t, env.client, http.MethodGet, env.srv.URL+"/api/admin/config-profiles", env.csrf, "")
	defer listResp.Body.Close()
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("列表条数 = %d, want 1", len(list.Items))
	}
	if _, ok := list.Items[0]["content"]; ok {
		t.Fatal("列表不应回传正文")
	}
}

// TestConfigProfileReplaceFile 重新上传覆盖已有行的文件,并留审计。
func TestConfigProfileReplaceFile(t *testing.T) {
	env := setupConfigProfileFiles(t)
	id := createProfileViaAPI(t, env)

	resp := postConfigProfileFile(t, env, env.srv.URL+"/api/admin/config-profiles/"+id+"/upload",
		"settings.xml", []byte("<settings><mirror>私服</mirror></settings>"), nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replace status = %d: %s", resp.StatusCode, readAll(t, resp))
	}
	out := decodeMap(t, resp)
	if out["name"] != "公司私服" || out["language"] != "java" {
		t.Fatalf("替换后身份字段应不变: %v", out)
	}
	if filepath.Base(out["filePath"].(string)) != "settings.xml" {
		t.Fatalf("落盘文件名错: %v", out["filePath"])
	}

	detailResp := doJSON(t, env.client, http.MethodGet, env.srv.URL+"/api/admin/config-profiles/"+id, env.csrf, "")
	defer detailResp.Body.Close()
	if got := decodeMap(t, detailResp)["content"]; got != "<settings><mirror>私服</mirror></settings>" {
		t.Fatalf("正文未替换: %v", got)
	}

	var n int
	if err := env.db.QueryRow(`SELECT COUNT(1) FROM audit_log WHERE action = ?`, audit.ActionConfigProfileReplace).Scan(&n); err != nil {
		t.Fatalf("audit query: %v", err)
	}
	if n != 1 {
		t.Fatalf("审计条数 = %d, want 1", n)
	}
}

// TestConfigProfileReplaceFileRejects 内置行 403 / 扩展名白名单 400 / 缺 file 400 / 不存在 404。
func TestConfigProfileReplaceFileRejects(t *testing.T) {
	env := setupConfigProfileFiles(t)
	id := createProfileViaAPI(t, env)

	builtin := &configprofile.ConfigProfile{
		ID: "sec-builtin-http", Language: "node", ConfigType: "npmrc", Name: "内置 npmrc",
		TargetPath: "/root/.npmrc", FilePath: configprofile.ProfilePath(env.dataDir, "sec-builtin-http", ".npmrc"),
		Content:    "registry=官方", IsBuiltin: true, Enabled: true,
	}
	if _, err := env.svc.Create(builtin); err == nil {
		t.Fatal("Create 应拒绝内置行")
	}
	if err := configprofile.NewSQLiteRepo(env.db).Create(builtin); err != nil {
		t.Fatalf("seed builtin: %v", err)
	}

	cases := []struct {
		name, url, filename string
		status              int
	}{
		{"内置行只读", env.srv.URL + "/api/admin/config-profiles/sec-builtin-http/upload", "x.xml", http.StatusForbidden},
		{"扩展名不在白名单", env.srv.URL + "/api/admin/config-profiles/" + id + "/upload", "payload.exe", http.StatusBadRequest},
		{"不存在的 id", env.srv.URL + "/api/admin/config-profiles/no-such-id/upload", "x.xml", http.StatusNotFound},
	}
	for _, c := range cases {
		resp := postConfigProfileFile(t, env, c.url, c.filename, []byte("body"), nil)
		got := resp.StatusCode
		_ = resp.Body.Close()
		if got != c.status {
			t.Fatalf("%s: status = %d, want %d", c.name, got, c.status)
		}
	}

	// 缺 file 字段
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("targetPath", "/root/.m2/settings.xml")
	_ = mw.Close()
	req, _ := http.NewRequest(http.MethodPost, env.srv.URL+"/api/admin/config-profiles/"+id+"/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-CSRF-Token", env.csrf)
	resp, err := env.client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("缺 file 字段 status = %d, want 400", resp.StatusCode)
	}

	// 失败的请求不能改动原正文
	detailResp := doJSON(t, env.client, http.MethodGet, env.srv.URL+"/api/admin/config-profiles/"+id, env.csrf, "")
	defer detailResp.Body.Close()
	if got := decodeMap(t, detailResp)["content"]; got != "<settings>初始</settings>" {
		t.Fatalf("原正文被改动: %v", got)
	}
}

// TestConfigProfileUpdateWithoutContentKeepsFile PUT 不带正文(编辑弹窗只改说明)时
// 既不能 400,也不能把文件清空。
func TestConfigProfileUpdateWithoutContentKeepsFile(t *testing.T) {
	env := setupConfigProfileFiles(t)
	id := createProfileViaAPI(t, env)

	body := `{"language":"java","configType":"maven","name":"公司私服","targetPath":"/root/.m2/settings.xml",` +
		`"content":"","description":"只改说明","enabled":true}`
	resp := doJSON(t, env.client, http.MethodPut, env.srv.URL+"/api/admin/config-profiles/"+id, env.csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update status = %d: %s", resp.StatusCode, readAll(t, resp))
	}
	detailResp := doJSON(t, env.client, http.MethodGet, env.srv.URL+"/api/admin/config-profiles/"+id, env.csrf, "")
	defer detailResp.Body.Close()
	detail := decodeMap(t, detailResp)
	if detail["content"] != "<settings>初始</settings>" {
		t.Fatalf("空 content 的 PUT 动了正文: %v", detail["content"])
	}
	if detail["description"] != "只改说明" {
		t.Fatalf("description 未更新: %v", detail["description"])
	}
}
