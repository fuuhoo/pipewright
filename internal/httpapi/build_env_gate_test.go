// 流水线保存期的构建环境白名单(#8 · R4):
//
//	PUT /api/projects/{id}/pipeline 必须拒绝「没选预置环境 / 选了已删或已禁用环境 /
//	旧配置镜像不在目录内」的构建类节点,错误码 build_env_required(422)。
//
// 这是唯一的前门 —— 前端选择器只是把这件事变得容易,真正的「不留后门」在这里。
package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/buildenv"
	"github.com/fuuhoo/pipewright/internal/configprofile"
	"github.com/fuuhoo/pipewright/internal/pipeline"
	"github.com/fuuhoo/pipewright/internal/project"
	"github.com/fuuhoo/pipewright/internal/users"
	"github.com/fuuhoo/pipewright/internal/vault"
)

// setupBuildEnvGateServer 装配流水线 + 预置目录(1 个 Node 20 环境 + 1 个已禁用环境)。
func setupBuildEnvGateServer(t *testing.T) (*httptest.Server, *http.Client, string, string, string) {
	t.Helper()
	st := testStoreAuth(t)
	authSvc := auth.NewService(st.DB, nil, users.NewService(st.DB))
	if err := authSvc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	v := vault.New(st.DB, testMasterKey())
	psvc := project.New(st.DB, v, stubProber{branch: "main"})

	bSvc := buildenv.NewService(buildenv.NewSQLiteRepo(st.DB))
	env, err := bSvc.Create(&buildenv.BuildEnv{
		Language: "node", Version: "20", DisplayName: "Node 20",
		Image: "node:20-alpine", SourceType: buildenv.SourceOfficial, ImageCheckStatus: buildenv.StatusAvailable, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create build env: %v", err)
	}
	off, err := bSvc.Create(&buildenv.BuildEnv{
		Language: "node", Version: "16", DisplayName: "Node 16(已下架)",
		Image: "node:16-alpine", SourceType: buildenv.SourceOfficial, ImageCheckStatus: buildenv.StatusAvailable, Enabled: false,
	})
	if err != nil {
		t.Fatalf("create disabled env: %v", err)
	}
	cpSvc := configprofile.NewService(configprofile.NewSQLiteRepo(st.DB), t.TempDir())

	plsvc := pipeline.New(st.DB, pipeline.WithBuildEnvGate(NewBuildEnvGate(bSvc, cpSvc)))
	srv := httptest.NewServer(New(testWebFSAuth(), authSvc,
		WithVault(v), WithProjects(psvc), WithPipelines(plsvc),
		WithBuildEnvs(bSvc, nil), WithConfigProfiles(cpSvc), WithUsers(users.NewService(st.DB))))
	t.Cleanup(srv.Close)

	client := newTestClient(t)
	csrf := loginWithClient(t, client, srv.URL)
	credID := newGitCred(t, client, srv.URL, csrf, "ghp_token_for_gate")
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/projects", csrf,
		`{"name":"gate","repoUrl":"https://gitee.com/acme/gate.git","credentialId":"`+credID+`"}`)
	defer resp.Body.Close()
	var p map[string]any
	raw, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("decode project: %v (%s)", err, raw)
	}
	projID, _ := p["id"].(string)
	if projID == "" {
		t.Fatalf("create project failed: %s", raw)
	}
	return srv, client, csrf, projID, env.ID + "|" + off.ID
}

// putPipeline 提交一个含单个 script 构建节点的流水线并返回响应。
func putPipeline(t *testing.T, client *http.Client, srvURL, csrf, projID, jobConfig string) *http.Response {
	t.Helper()
	body := `{"stages":[` +
		`{"id":"stg_src","name":"流水线源","kind":"source","jobs":[{"id":"job_src","name":"源","type":"git_source","config":{}}]},` +
		`{"id":"stg_build","name":"构建","kind":"build","jobs":[{"id":"job_b","name":"前端构建","type":"script","config":` + jobConfig + `}]}` +
		`]}`
	resp := doJSON(t, client, http.MethodPut, srvURL+"/api/projects/"+projID+"/pipeline", csrf, body)
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func decodeErr(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

func TestSavePipeline_BuildEnvWhitelist(t *testing.T) {
	srv, client, csrf, projID, ids := setupBuildEnvGateServer(t)
	envID, offID := ids[:strings.Index(ids, "|")], ids[strings.Index(ids, "|")+1:]

	cases := []struct {
		name    string
		cfg     string
		wantOK  bool
		wantMsg string
	}{
		{"选中预置环境", `{"buildEnvId":"` + envID + `","commands":"npm run build"}`, true, ""},
		{"旧镜像恰在目录内", `{"image":"node:20-alpine","commands":"npm run build"}`, true, ""},
		{"没选环境", `{"commands":"npm run build"}`, false, "未选择构建环境"},
		{"镜像不在目录", `{"image":"evil.io/node:1","commands":"npm run build"}`, false, "不在预置目录内"},
		{"引用已删除环境", `{"buildEnvId":"env-gone","commands":"npm run build"}`, false, "已不存在"},
		{"引用已禁用环境", `{"buildEnvId":"` + offID + `","commands":"npm run build"}`, false, "已被禁用"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := putPipeline(t, client, srv.URL, csrf, projID, c.cfg)
			if c.wantOK {
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("status = %d, want 200 (%v)", resp.StatusCode, decodeErr(t, resp))
				}
				return
			}
			m := decodeErr(t, resp)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422 (%v)", resp.StatusCode, m)
			}
			detail, _ := m["error"].(map[string]any)
			if detail["code"] != "build_env_required" {
				t.Fatalf("code = %v, want build_env_required (%v)", detail["code"], m)
			}
			if msg, _ := detail["message"].(string); !strings.Contains(msg, c.wantMsg) || !strings.Contains(msg, "前端构建") {
				t.Fatalf("message = %q, 应点名节点并含 %q", msg, c.wantMsg)
			}
		})
	}
}

// TestSavePipeline_ServiceImageExempt 钉住豁免边界:阶段旁挂服务的镜像不经白名单
// (被测件依赖 DB/redis,不是构建环境),手输仍要能通过保存。
func TestSavePipeline_ServiceImageExempt(t *testing.T) {
	srv, client, csrf, projID, ids := setupBuildEnvGateServer(t)
	envID := ids[:strings.Index(ids, "|")]
	body := `{"stages":[` +
		`{"id":"stg_src","name":"流水线源","kind":"source","jobs":[{"id":"job_src","name":"源","type":"git_source","config":{}}]},` +
		`{"id":"stg_build","name":"构建","kind":"build","services":[{"name":"pg","image":"postgres:16-alpine"}],` +
		`"jobs":[{"id":"job_b","name":"测试","type":"script","config":{"buildEnvId":"` + envID + `","commands":"mvn test"}}]}` +
		`]}`
	resp := doJSON(t, client, http.MethodPut, srv.URL+"/api/projects/"+projID+"/pipeline", csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("旁挂服务手输镜像应放行, status = %d (%v)", resp.StatusCode, decodeErr(t, resp))
	}
}
