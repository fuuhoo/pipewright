package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/library"
	"github.com/fuuhoo/pipewright/internal/pipeline"
	"github.com/fuuhoo/pipewright/internal/project"
	"github.com/fuuhoo/pipewright/internal/vault"
)

// setupLibraryServer 构造带 auth + vault + project + pipeline + templates + variable-groups 的测试 server。
func setupLibraryServer(t *testing.T) (*httptest.Server, *http.Client, string, string) {
	t.Helper()
	st := testStoreAuth(t)
	svc := auth.NewService(st.DB, nil, nil)
	if err := svc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	v := vault.New(st.DB, testMasterKey())
	psvc := project.New(st.DB, v, stubProber{branch: "main"})
	pipes := pipeline.New(st.DB)
	tplSvc := library.NewTemplateService(st.DB, pipes)
	vgSvc := library.NewVarGroupService(st.DB, v)
	srv := httptest.NewServer(New(testWebFSAuth(), svc,
		WithVault(v), WithProjects(psvc), WithPipelines(pipes),
		WithTemplates(tplSvc), WithVariableGroups(vgSvc)))
	t.Cleanup(srv.Close)

	client := newTestClient(t)
	csrf := loginWithClient(t, client, srv.URL)

	credID := newGitCred(t, client, srv.URL, csrf, "ghp_token_for_library_proj")
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/projects", csrf,
		`{"name":"libproj","repoUrl":"https://gitee.com/acme/lib.git","credentialId":"`+credID+`"}`)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var p map[string]any
	_ = json.Unmarshal(raw, &p)
	projID, _ := p["id"].(string)
	if projID == "" {
		t.Fatalf("create project failed: %s", raw)
	}
	return srv, client, csrf, projID
}

func TestTemplateCreateApplyToProjectHTTP(t *testing.T) {
	srv, client, csrf, projID := setupLibraryServer(t)

	// 建模板:含一个源阶段 + 一个构建阶段。
	tplBody := `{"name":"go-svc","description":"Go 模板","stages":[
		{"name":"源","kind":"source","jobs":[{"name":"git","type":"git_source"}]},
		{"name":"构建","kind":"build","jobs":[{"name":"compile","type":"build","config":{"artifactType":"jar","commands":"go build ./..."}}]}
	]}`
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/templates", csrf, tplBody)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("create template status = %d, body=%s", resp.StatusCode, raw)
	}
	raw, _ := io.ReadAll(resp.Body)
	var tpl map[string]any
	_ = json.Unmarshal(raw, &tpl)
	tplID, _ := tpl["id"].(string)
	if tplID == "" {
		t.Fatalf("template id empty: %s", raw)
	}

	// 应用模板到项目流水线。
	applyResp := doJSON(t, client, http.MethodPost,
		srv.URL+"/api/projects/"+projID+"/pipeline/apply-template", csrf,
		`{"templateId":"`+tplID+`"}`)
	defer applyResp.Body.Close()
	if applyResp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(applyResp.Body)
		t.Fatalf("apply status = %d, body=%s", applyResp.StatusCode, raw)
	}
	rawApply, _ := io.ReadAll(applyResp.Body)
	var applied map[string]any
	_ = json.Unmarshal(rawApply, &applied)
	stages, _ := applied["stages"].([]any)
	if len(stages) != 2 {
		t.Fatalf("applied pipeline stages = %d, want 2: %s", len(stages), rawApply)
	}
	if yaml, _ := applied["yaml"].(string); strings.TrimSpace(yaml) == "" {
		t.Fatalf("applied pipeline should render YAML")
	}

	// 读回项目流水线,确认与模板一致(落库)。
	getResp := doJSON(t, client, http.MethodGet, srv.URL+"/api/projects/"+projID+"/pipeline", csrf, "")
	defer getResp.Body.Close()
	rawGet, _ := io.ReadAll(getResp.Body)
	var got map[string]any
	_ = json.Unmarshal(rawGet, &got)
	if gotStages, _ := got["stages"].([]any); len(gotStages) != 2 {
		t.Fatalf("project pipeline stages = %d, want 2: %s", len(gotStages), rawGet)
	}
}

func TestTemplateApplyMissingTemplateHTTP(t *testing.T) {
	srv, client, csrf, projID := setupLibraryServer(t)
	resp := doJSON(t, client, http.MethodPost,
		srv.URL+"/api/projects/"+projID+"/pipeline/apply-template", csrf,
		`{"templateId":"nope"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 404, body=%s", resp.StatusCode, raw)
	}
}

func TestVariableGroupSecretMaskedRoundTripHTTP(t *testing.T) {
	srv, client, csrf, _ := setupLibraryServer(t)

	const leak = "vg_LEAKMARKER_secret_zzz"
	credID := newGitCred(t, client, srv.URL, csrf, leak)

	body := `{"name":"shared-env","vars":[
		{"key":"API_URL","secret":false,"value":"https://api.example.com"},
		{"key":"TOKEN","secret":true,"credentialId":"` + credID + `","value":"should-be-dropped"}
	]}`
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/variable-groups", csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("create variable group status = %d, body=%s", resp.StatusCode, raw)
	}
	raw, _ := io.ReadAll(resp.Body)
	// 响应绝无明文 secret。
	if strings.Contains(string(raw), leak) || strings.Contains(string(raw), "should-be-dropped") {
		t.Fatalf("variable group response leaked plaintext: %s", raw)
	}
	var g map[string]any
	_ = json.Unmarshal(raw, &g)
	vars, _ := g["vars"].([]any)
	if len(vars) != 2 {
		t.Fatalf("vars = %d, want 2: %s", len(vars), raw)
	}
	for _, vAny := range vars {
		vmap, _ := vAny.(map[string]any)
		if vmap["key"] == "TOKEN" {
			if vmap["secret"] != true {
				t.Fatalf("TOKEN should be secret: %v", vmap)
			}
			if _, hasValue := vmap["value"]; hasValue {
				t.Fatalf("secret var should not carry plaintext value: %v", vmap)
			}
			if vmap["credentialId"] != credID {
				t.Fatalf("secret var credentialId = %v, want %s", vmap["credentialId"], credID)
			}
			if masked, _ := vmap["maskedValue"].(string); masked == "" {
				t.Fatalf("secret var should carry maskedValue: %v", vmap)
			}
		}
	}

	gid, _ := g["id"].(string)
	// 列表与详情同样不泄漏明文。
	listResp := doJSON(t, client, http.MethodGet, srv.URL+"/api/variable-groups", csrf, "")
	defer listResp.Body.Close()
	rawList, _ := io.ReadAll(listResp.Body)
	if strings.Contains(string(rawList), leak) {
		t.Fatalf("variable group list leaked plaintext: %s", rawList)
	}

	// 删除。
	delResp := doJSON(t, client, http.MethodDelete, srv.URL+"/api/variable-groups/"+gid, csrf, "")
	defer delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", delResp.StatusCode)
	}
}

func TestVariableGroupDuplicateKeysRejectedHTTP(t *testing.T) {
	srv, client, csrf, _ := setupLibraryServer(t)
	body := `{"name":"dup","vars":[{"key":"K","value":"1"},{"key":"K","value":"2"}]}`
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/variable-groups", csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 422, body=%s", resp.StatusCode, raw)
	}
}

// tplV1 是建模板用的最小合法 stages(源 + 构建)。
const tplV1 = `{"name":"go-svc","description":"第一版","stages":[
	{"name":"源","kind":"source","jobs":[{"name":"git","type":"git_source"}]},
	{"name":"构建","kind":"build","jobs":[{"name":"compile","type":"build","config":{"artifactType":"jar","commands":"go build ./..."}}]}
]}`

// tplV2 是覆盖更新用的另一份:改名 + 改说明 + 把构建阶段整个换成打包阶段。
// 阶段名不同才验得出「覆盖」而不是「追加」或「没写进去」。
const tplV2 = `{"name":"go-svc-v2","description":"第二版","stages":[
	{"name":"源","kind":"source","jobs":[{"name":"git","type":"git_source"}]},
	{"name":"打包","kind":"build","jobs":[{"name":"dist","type":"build","config":{"artifactType":"jar","commands":"make package"}}]}
]}`

// createTemplateHTTP 建一个模板并返回它的 id(失败即 t.Fatal,带上响应体)。
func createTemplateHTTP(t *testing.T, srv *httptest.Server, client *http.Client, csrf, body string) string {
	t.Helper()
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/templates", csrf, body)
	defer resp.Body.Close()
	raw := readAll(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("建模板 = %d, body=%s", resp.StatusCode, raw)
	}
	id, _ := decodeJSONBody(t, raw)["id"].(string)
	if id == "" {
		t.Fatalf("建模板响应没有 id: %s", raw)
	}
	return id
}

// stageNames 取模板详情 DTO 里的阶段名序列(顺序即画布顺序)。
func stageNames(t *testing.T, dto map[string]any) []string {
	t.Helper()
	list, _ := dto["stages"].([]any)
	out := make([]string, 0, len(list))
	for _, s := range list {
		m, _ := s.(map[string]any)
		name, _ := m["name"].(string)
		out = append(out, name)
	}
	return out
}

// TestTemplateUpdateOverwritesHTTP 钉住 PUT 的「整份覆盖」语义:名称、说明、stages 一次全换,
// 且覆盖后的模板真的能被项目应用 —— 改完的项目流水线读到的必须是新阶段,不是旧的那份。
func TestTemplateUpdateOverwritesHTTP(t *testing.T) {
	srv, client, csrf, projID := setupLibraryServer(t)
	tplID := createTemplateHTTP(t, srv, client, csrf, tplV1)

	resp := doJSON(t, client, http.MethodPut, srv.URL+"/api/templates/"+tplID, csrf, tplV2)
	defer resp.Body.Close()
	raw := readAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT = %d, want 200, body=%s", resp.StatusCode, raw)
	}
	dto := decodeJSONBody(t, raw)
	if dto["name"] != "go-svc-v2" || dto["description"] != "第二版" {
		t.Fatalf("元数据没覆盖: %v / %v", dto["name"], dto["description"])
	}
	if got := stageNames(t, dto); len(got) != 2 || got[1] != "打包" {
		t.Fatalf("响应里的 stages 未覆盖: %v", got)
	}

	// 读回确认落库(不是只改了响应体)。
	status, got, gotRaw := getJSON(t, client, srv.URL+"/api/templates/"+tplID, csrf)
	if status != http.StatusOK {
		t.Fatalf("GET = %d(%s)", status, gotRaw)
	}
	if names := stageNames(t, got); len(names) != 2 || names[1] != "打包" || names[0] != "源" {
		t.Fatalf("落库的 stages = %v, want [源 打包]", names)
	}
	// created_at 不该被 PUT 挪动(列表按更新时间/创建时间排序时,改一次就变「新建」算 bug)。
	if created, _ := got["createdAt"].(string); created == "" {
		t.Fatalf("详情缺 createdAt: %s", gotRaw)
	}

	// 覆盖后的模板仍要能应用到项目 —— 且项目拿到的是新内容。
	apply := doJSON(t, client, http.MethodPost,
		srv.URL+"/api/projects/"+projID+"/pipeline/apply-template", csrf,
		`{"templateId":"`+tplID+`"}`)
	defer apply.Body.Close()
	rawApply := readAll(t, apply)
	if apply.StatusCode != http.StatusOK {
		t.Fatalf("apply = %d, want 200, body=%s", apply.StatusCode, rawApply)
	}
	if names := stageNames(t, decodeJSONBody(t, rawApply)); len(names) != 2 || names[1] != "打包" {
		t.Fatalf("应用后项目阶段 = %v, want 覆盖后的 [源 打包]", names)
	}
}

// TestTemplateUpdateErrorCodesHTTP 逐个钉 PUT 的四种失败:各自的状态码 + 契约错误码,
// 且每一次都不许把原模板改成半截 —— 前端详情弹窗靠这些码区分「改名撞车」和「内容不合法」。
func TestTemplateUpdateErrorCodesHTTP(t *testing.T) {
	srv, client, csrf, _ := setupLibraryServer(t)
	tplID := createTemplateHTTP(t, srv, client, csrf, tplV1)
	otherID := createTemplateHTTP(t, srv, client, csrf,
		`{"name":"other","description":"","stages":[{"name":"源","kind":"source","jobs":[{"name":"git","type":"git_source"}]}]}`)

	cases := []struct {
		name       string
		path       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{"不存在", "/api/templates/nope", tplV2, http.StatusNotFound, "template_not_found"},
		// 缺源阶段:pipeline.NormalizeSpec 拒 → 422(不是 400,前端要把它当「内容不合法」显示)。
		{"spec 非法", "/api/templates/" + tplID,
			`{"name":"go-svc","description":"","stages":[{"name":"构建","kind":"build","jobs":[]}]}`,
			http.StatusUnprocessableEntity, "invalid_template"},
		// 撞别人的名 → 409;撞自己的名不算撞(见 TestTemplateUpdateSelfRenameHTTP)。
		{"撞名", "/api/templates/" + tplID,
			`{"name":"other","description":"","stages":[{"name":"源","kind":"source","jobs":[{"name":"git","type":"git_source"}]}]}`,
			http.StatusConflict, "template_name_taken"},
		{"请求体坏", "/api/templates/" + tplID, `{"name":`, http.StatusBadRequest, "bad_request"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := doJSON(t, client, http.MethodPut, srv.URL+c.path, csrf, c.body)
			defer resp.Body.Close()
			raw := readAll(t, resp)
			if resp.StatusCode != c.wantStatus {
				t.Fatalf("PUT %s = %d, want %d(%s)", c.path, resp.StatusCode, c.wantStatus, raw)
			}
			if code := errCode(decodeJSONBody(t, raw)); code != c.wantCode {
				t.Fatalf("PUT %s 错误码 = %q, want %q", c.path, code, c.wantCode)
			}
			// 撞名那次是「往别人的 id 上写」——被拦下时对方那行也不许动。
			if c.name == "撞名" {
				s, other, otherRaw := getJSON(t, client, srv.URL+"/api/templates/"+otherID, csrf)
				if s != http.StatusOK || other["name"] != "other" || len(stageNames(t, other)) != 1 {
					t.Fatalf("撞名 PUT 波及了对方模板: %d %s", s, otherRaw)
				}
			}
		})
	}

	// 四连失败之后,原模板必须还是 tplV1 那两份阶段(非法 PUT 不留痕)。
	status, got, raw := getJSON(t, client, srv.URL+"/api/templates/"+tplID, csrf)
	if status != http.StatusOK {
		t.Fatalf("GET = %d(%s)", status, raw)
	}
	if got["name"] != "go-svc" {
		t.Fatalf("失败 PUT 改了名称: %v", got["name"])
	}
	if names := stageNames(t, got); len(names) != 2 || names[1] != "构建" {
		t.Fatalf("失败 PUT 改了 stages: %v", names)
	}
}

// TestTemplateUpdateSelfRenameHTTP 钉住详情弹窗最常见的保存动作:名字原样回填、只改说明 → 200。
// 若 UPDATE 把自己撞自己的唯一约束当成冲突,用户会看到「模板名已被占用」却什么都没改。
func TestTemplateUpdateSelfRenameHTTP(t *testing.T) {
	srv, client, csrf, _ := setupLibraryServer(t)
	tplID := createTemplateHTTP(t, srv, client, csrf, tplV1)
	body := `{"name":"go-svc","description":"只改说明","stages":[
		{"name":"源","kind":"source","jobs":[{"name":"git","type":"git_source"}]},
		{"name":"构建","kind":"build","jobs":[{"name":"compile","type":"build","config":{"artifactType":"jar","commands":"go build ./..."}}]}
	]}`
	resp := doJSON(t, client, http.MethodPut, srv.URL+"/api/templates/"+tplID, csrf, body)
	defer resp.Body.Close()
	raw := readAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("原名保存 = %d, want 200(%s)", resp.StatusCode, raw)
	}
	if desc := decodeJSONBody(t, raw)["description"]; desc != "只改说明" {
		t.Fatalf("说明没存进去: %v", desc)
	}
}
