package httpapi

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/project"
	"github.com/fuuhoo/pipewright/internal/run"
	"github.com/fuuhoo/pipewright/internal/target"
	"github.com/fuuhoo/pipewright/internal/trigger"
	"github.com/fuuhoo/pipewright/internal/vault"
)

// run_hosts_test.go —— 手动触发的「主机」候选与运行记录标注。
//
// 候选口径:该(库内)流水线部署节点勾选过的主机 ∩ 本次可见;触发时 serverId 必须落在这个
// 集合里(422 否则),且只写运行记录,不改变部署落点。

type runHostsEnv struct {
	srv    *httptest.Server
	client *http.Client
	csrf   string
	db     *sql.DB
	projID string
	// web1 / web2 是部署节点勾过的两台(声明顺序 web2 → web1);ghost 只存在于配置里。
	web1, web2, ghost string
}

func setupRunHostsEnv(t *testing.T) runHostsEnv {
	t.Helper()
	st := testStoreAuth(t)
	svc := auth.NewService(st.DB, nil, nil)
	if err := svc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	v := vault.New(st.DB, testMasterKey())
	psvc := project.New(st.DB, v, stubProber{branch: "main"})
	tsvc := target.New(st.DB, v, stubDialer{res: &target.ExecResult{Stdout: "ok"}})
	rsvc := run.New(st.DB)

	srv := httptest.NewServer(New(testWebFSAuth(), svc,
		WithVault(v), WithProjects(psvc), WithServers(tsvc),
		WithTriggers(trigger.New(st.DB, v)), WithRuns(rsvc, nil)))
	t.Cleanup(srv.Close)

	client := newTestClient(t)
	csrf := loginWithClient(t, client, srv.URL)

	credID := newGitCred(t, client, srv.URL, csrf, "ghp_token_for_run_hosts")
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/projects", csrf,
		`{"name":"acme","repoUrl":"https://gitee.com/acme/shop.git","credentialId":"`+credID+`"}`)
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var p map[string]any
	_ = json.Unmarshal(raw, &p)
	projID, _ := p["id"].(string)
	if projID == "" {
		t.Fatalf("create project failed: %s", raw)
	}

	sshCred := newSSHCredAPI(t, client, srv.URL, csrf, "PRIVATEKEY_MARKER")
	web1 := createServerNamedAPI(t, client, srv.URL, csrf, sshCred, "web-1", "10.0.0.1")
	web2 := createServerNamedAPI(t, client, srv.URL, csrf, sshCred, "web-2", "10.0.0.2")
	ghost := "srv-ghost-not-exist"

	seedPipelineSpecJSON(t, st.DB, projID,
		`{"stages":[{"id":"s1","name":"发布","kind":"deploy","jobs":[`+
			`{"id":"j1","name":"发机器","type":"deploy_ssh","config":{"serverIds":"`+web2+`,`+web1+`,`+ghost+`"}}]}]}`)

	return runHostsEnv{srv: srv, client: client, csrf: csrf, db: st.DB, projID: projID,
		web1: web1, web2: web2, ghost: ghost}
}

// TestRunHostsCandidatesFromPipeline 验证候选 = 配置顺序、去重、只留在可见且存在的机器。
func TestRunHostsCandidatesFromPipeline(t *testing.T) {
	env := setupRunHostsEnv(t)

	resp := doJSON(t, env.client, http.MethodGet, env.srv.URL+"/api/projects/"+env.projID+"/run-hosts", "", "")
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, raw)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var out struct {
		Hosts []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"hosts"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析候选失败: %v / %s", err, raw)
	}
	if len(out.Hosts) != 2 {
		t.Fatalf("候选应为 2 台(ghost 不在服务器表里), got %v", out.Hosts)
	}
	if out.Hosts[0].ID != env.web2 || out.Hosts[0].Name != "web-2" {
		t.Fatalf("候选[0] = %+v, want web-2(配置声明顺序)", out.Hosts[0])
	}
	if out.Hosts[1].ID != env.web1 || out.Hosts[1].Name != "web-1" {
		t.Fatalf("候选[1] = %+v, want web-1", out.Hosts[1])
	}

	// 项目不存在 → 404 project_not_found。
	missing := doJSON(t, env.client, http.MethodGet, env.srv.URL+"/api/projects/nope/run-hosts", "", "")
	raw, _ = io.ReadAll(missing.Body)
	_ = missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("missing project expected 404, got %d: %s", missing.StatusCode, raw)
	}
}

// TestManualTriggerRecordsSelectedHost 验证选定的主机随运行落库,详情与列表都带回来。
func TestManualTriggerRecordsSelectedHost(t *testing.T) {
	env := setupRunHostsEnv(t)

	resp := doJSON(t, env.client, http.MethodPost, env.srv.URL+"/api/projects/"+env.projID+"/runs", env.csrf,
		`{"branch":"main","serverId":"`+env.web1+`"}`)
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, raw)
	}
	var dto map[string]any
	_ = json.Unmarshal(raw, &dto)
	runID, _ := dto["id"].(string)
	if runID == "" {
		t.Fatalf("created run has no id: %s", raw)
	}
	trig, _ := dto["trigger"].(map[string]any)
	if trig["serverId"] != env.web1 || trig["serverName"] != "web-1" {
		t.Fatalf("create 回执的 trigger 主机不对: %v", trig)
	}

	// 详情:同一台(名字是触发时刻快照)。
	detail := doJSON(t, env.client, http.MethodGet, env.srv.URL+"/api/runs/"+runID, "", "")
	raw, _ = io.ReadAll(detail.Body)
	_ = detail.Body.Close()
	var d map[string]any
	_ = json.Unmarshal(raw, &d)
	dtrig, _ := d["trigger"].(map[string]any)
	if dtrig["serverId"] != env.web1 || dtrig["serverName"] != "web-1" {
		t.Fatalf("详情 trigger 主机不对: %v", dtrig)
	}

	// 列表:运行记录里也带主机。
	list := doJSON(t, env.client, http.MethodGet, env.srv.URL+"/api/runs?projectId="+env.projID, "", "")
	raw, _ = io.ReadAll(list.Body)
	_ = list.Body.Close()
	var ls struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal(raw, &ls)
	if len(ls.Items) != 1 {
		t.Fatalf("列表条数 = %d, want 1: %s", len(ls.Items), raw)
	}
	ltrig, _ := ls.Items[0]["trigger"].(map[string]any)
	if ltrig["serverId"] != env.web1 || ltrig["serverName"] != "web-1" {
		t.Fatalf("列表 trigger 主机不对: %v", ltrig)
	}

	// 不选主机:照常创建,字段为空(可选项)。
	resp = doJSON(t, env.client, http.MethodPost, env.srv.URL+"/api/projects/"+env.projID+"/runs", env.csrf,
		`{"branch":"main"}`)
	raw, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("no host expected 201, got %d: %s", resp.StatusCode, raw)
	}
	var plain map[string]any
	_ = json.Unmarshal(raw, &plain)
	ptr, _ := plain["trigger"].(map[string]any)
	if ptr["serverId"] != "" || ptr["serverName"] != "" {
		t.Fatalf("未选主机应为空: %v", ptr)
	}
}

// TestManualTriggerRejectsNonCandidateHost 验证非流水线配置过 / 不存在的机器 → 422,不创建运行。
func TestManualTriggerRejectsNonCandidateHost(t *testing.T) {
	env := setupRunHostsEnv(t)

	// 存在的机器但不在该流水线配置里。
	other := createServerNamedAPI(t, env.client, env.srv.URL, env.csrf,
		newSSHCredAPI(t, env.client, env.srv.URL, env.csrf, "PRIVATEKEY_MARKER2"), "other", "10.0.0.9")
	for _, serverID := range []string{other, env.ghost} {
		resp := doJSON(t, env.client, http.MethodPost, env.srv.URL+"/api/projects/"+env.projID+"/runs", env.csrf,
			`{"branch":"main","serverId":"`+serverID+`"}`)
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("serverId=%s expected 422, got %d: %s", serverID, resp.StatusCode, raw)
		}
		var out struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &out)
		if out.Error.Code != "invalid_server" {
			t.Fatalf("serverId=%s expected invalid_server, got %q: %s", serverID, out.Error.Code, raw)
		}
	}

	// 一条运行都没建。
	list := doJSON(t, env.client, http.MethodGet, env.srv.URL+"/api/runs?projectId="+env.projID, "", "")
	raw, _ := io.ReadAll(list.Body)
	_ = list.Body.Close()
	var ls struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal(raw, &ls)
	if len(ls.Items) != 0 {
		t.Fatalf("422 后不应创建运行, got %s", raw)
	}
}
