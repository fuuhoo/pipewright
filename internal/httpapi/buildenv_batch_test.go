// 构建环境批量新增 / 批量删除的 HTTP 契约(v6.2 §3.1 之外的运维能力)。
//
// 守住三件事:
//   - 逐行结果原样回带:一行失败或已存在,同批其它行照常写库,整单 200 不 500。
//   - 三态门不因批量入口松动:文件/表单里想启用的新行,没检查过就保持禁用并说明原因。
//   - 入参先校验再谈业务:空选 400、超上限 400;两个端点都 admin-only + 吃 CSRF。
package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/buildenv"
)

// batchReport 是批量写响应体(与 buildenv.BatchReport 同形,这里按 JSON 字段收)。
type batchReport struct {
	Summary struct {
		Total   int `json:"total"`
		Success int `json:"success"`
		Skipped int `json:"skipped"`
		Failed  int `json:"failed"`
	} `json:"summary"`
	Results []buildenv.BatchRowResult `json:"results"`
}

func decodeBatchReport(t *testing.T, raw string) batchReport {
	t.Helper()
	var report batchReport
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		t.Fatalf("decode 批量响应: %v\n%s", err, raw)
	}
	return report
}

// TestBatchCreateBuildEnvs_HTTP 四种行各走各的结局,并且新行的启用意图仍受三态门约束。
func TestBatchCreateBuildEnvs_HTTP(t *testing.T) {
	srv, bSvc := setupTransferServer(t, false)
	client, csrf := adminSession(t, srv)

	body := `{"items":[
		{"language":"bhttp","version":"1","displayName":"B 1","sourceType":"custom","image":"reg.internal/bhttp:1","credentialId":"cred-batch","sortOrder":7},
		{"language":"bhttp","version":"1","displayName":"同键再来","sourceType":"official","image":"bhttp:1"},
		{"language":"bhttp","version":"2","displayName":"","sourceType":"official","image":"bhttp:2"},
		{"language":"bhttp","version":"3","displayName":"B 3","sourceType":"custom","image":"reg.internal/bhttp:3","enabled":true}
	]}`
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/admin/build-envs/batch-create", csrf, body)
	raw := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("batch-create status = %d: %s", resp.StatusCode, raw)
	}

	report := decodeBatchReport(t, raw)
	if report.Summary.Total != 4 || report.Summary.Success != 2 ||
		report.Summary.Skipped != 1 || report.Summary.Failed != 1 {
		t.Fatalf("汇总应为 4/2/1/1,实际 %+v", report.Summary)
	}
	byIndex := map[int]buildenv.BatchRowResult{}
	for _, r := range report.Results {
		byIndex[r.Index] = r
	}
	if byIndex[1].Action != buildenv.ResultActionSkipped || byIndex[1].ID == "" {
		t.Fatalf("同键行要 skipped 并指回现有行 id: %+v", byIndex[1])
	}
	if byIndex[2].Action != buildenv.ResultActionFailed || !strings.Contains(byIndex[2].Reason, "display_name") {
		t.Fatalf("坏行要带人读原因: %+v", byIndex[2])
	}
	// 表单里写了 enabled:true,但镜像没检查过 → 行建成、保持禁用,并解释原因。
	if byIndex[3].Action != buildenv.ResultActionCreated || byIndex[3].Enabled {
		t.Fatalf("未检查的镜像不该被批量入口启用: %+v", byIndex[3])
	}
	if !strings.Contains(byIndex[3].Reason, "未检查") {
		t.Fatalf("要说清为什么没启用: %q", byIndex[3].Reason)
	}

	list, err := bSvc.List(buildenv.ListFilter{Language: "bhttp", IncludeDisabled: true})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("bhttp 只该落 2 行,实际 %d", len(list))
	}
	for _, e := range list {
		if e.Version == "1" && e.CredentialID != "cred-batch" {
			t.Fatalf("批量新增要能带拉取凭据: %+v", e)
		}
		if e.Version == "1" && e.SortOrder != 7 {
			t.Fatalf("sortOrder 没落库: %+v", e)
		}
	}
}

// TestBatchDeleteBuildEnvs_HTTP 删所选:重复 id 去重,已被别的会话删掉的行记 skipped 不算失败。
func TestBatchDeleteBuildEnvs_HTTP(t *testing.T) {
	srv, bSvc := setupTransferServer(t, false)
	client, csrf := adminSession(t, srv)

	a := createEnvViaAPI(t, client, srv, csrf,
		`{"language":"bdel","version":"1","displayName":"D 1","sourceType":"official","image":"bdel:1"}`)
	b := createEnvViaAPI(t, client, srv, csrf,
		`{"language":"bdel","version":"2","displayName":"D 2","sourceType":"official","image":"bdel:2"}`)

	body := fmt.Sprintf(`{"ids":[%q,"id-not-here",%q,%q]}`,
		toString(a["id"]), toString(b["id"]), toString(a["id"]))
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/admin/build-envs/batch-delete", csrf, body)
	raw := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("batch-delete status = %d: %s", resp.StatusCode, raw)
	}
	report := decodeBatchReport(t, raw)
	// 4 个 id 去重成 3 个(idA 出现两次),其中一个是幻影行。
	if report.Summary.Total != 3 || report.Summary.Success != 2 || report.Summary.Skipped != 1 {
		t.Fatalf("汇总应为 3/2/1,实际 %+v", report.Summary)
	}
	if !strings.Contains(raw, buildenv.ResultActionDeleted) {
		t.Fatalf("删除结果要用 deleted 标出: %s", raw)
	}
	list, err := bSvc.List(buildenv.ListFilter{Language: "bdel", IncludeDisabled: true})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("两行都该删掉,剩 %+v", list)
	}
}

// countLanguageViaAPI 从 admin 列表数某语言的行数(含禁用)。
// 走 HTTP 而不是 svc:超上限的分支要在「响应说 400」之外,再证明库里一行都没多。
func countLanguageViaAPI(t *testing.T, client *http.Client, srvURL, language string) int {
	t.Helper()
	resp := doJSON(t, client, http.MethodGet,
		srvURL+"/api/admin/build-envs?includeDisabled=1&language="+language, "", "")
	raw := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %s", resp.StatusCode, raw)
	}
	var out struct {
		Items []buildenv.BuildEnv `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode list: %v\n%s", err, raw)
	}
	return len(out.Items)
}

// TestBatchBuildEnvs_Guards 空选、超上限、缺 CSRF、未登录四道入口把关。
func TestBatchBuildEnvs_Guards(t *testing.T) {
	srv, _ := setupTransferServer(t, false)
	client, csrf := adminSession(t, srv)

	cases := []struct {
		name string
		path string
		body string
		code string
	}{
		{"新增空选", "batch-create", `{"items":[]}`, "empty_selection"},
		{"删除空选", "batch-delete", `{"ids":[]}`, "empty_selection"},
		{"删除只有空串", "batch-delete", `{"ids":[""]}`, "empty_selection"},
	}
	for _, c := range cases {
		resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/admin/build-envs/"+c.path, csrf, c.body)
		raw := readAll(t, resp)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(raw, c.code) {
			t.Fatalf("%s: 应 400 含 %q,实际 %d %s", c.name, c.code, resp.StatusCode, raw)
		}
	}

	// 超上限:比批量档多一条就拒,别默默删一半。
	many := make([]string, buildenv.MaxBatchRows+1)
	for i := range many {
		many[i] = fmt.Sprintf("missing-env-%04d", i)
	}
	overBody, err := json.Marshal(map[string]any{"ids": many})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/admin/build-envs/batch-delete", csrf, string(overBody))
	raw := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(raw, "too_many_ids") {
		t.Fatalf("超上限应 400 too_many_ids: %d %s", resp.StatusCode, raw)
	}
	items := make([]map[string]any, buildenv.MaxBatchRows+1)
	for i := range items {
		items[i] = map[string]any{
			"language": "bover", "version": fmt.Sprintf("%d", i),
			"displayName": "over", "sourceType": "official", "image": "bover:1",
		}
	}
	overCreate, _ := json.Marshal(map[string]any{"items": items})
	resp2 := doJSON(t, client, http.MethodPost, srv.URL+"/api/admin/build-envs/batch-create", csrf, string(overCreate))
	raw2 := readAll(t, resp2)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest || !strings.Contains(raw2, "too_many_items") {
		t.Fatalf("新增超上限应 400 too_many_items: %d %s", resp2.StatusCode, raw2)
	}
	if n := countLanguageViaAPI(t, client, srv.URL, "bover"); n != 0 {
		t.Fatalf("超上限不该写进任何行,实际 %d", n)
	}

	// 缺 CSRF 的写请求拦下;未登录够不着。
	noCSRF := doJSON(t, client, http.MethodPost, srv.URL+"/api/admin/build-envs/batch-delete", "", `{"ids":["x"]}`)
	noCSRF.Body.Close()
	if noCSRF.StatusCode != http.StatusForbidden {
		t.Fatalf("批量删除缺 CSRF 应 403, got %d", noCSRF.StatusCode)
	}
	noCSRFC := doJSON(t, client, http.MethodPost, srv.URL+"/api/admin/build-envs/batch-create", "", `{"items":[{"language":"x"}]}`)
	noCSRFC.Body.Close()
	if noCSRFC.StatusCode != http.StatusForbidden {
		t.Fatalf("批量新增缺 CSRF 应 403, got %d", noCSRFC.StatusCode)
	}
	anon := newTestClient(t)
	for _, path := range []string{"batch-create", "batch-delete"} {
		respA := doJSON(t, anon, http.MethodPost, srv.URL+"/api/admin/build-envs/"+path, "", `{}`)
		respA.Body.Close()
		if respA.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s 未登录应 401, got %d", path, respA.StatusCode)
		}
	}
}
