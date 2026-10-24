package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/buildsnap"
	"github.com/fuuhoo/pipewright/internal/project"
	"github.com/fuuhoo/pipewright/internal/run"
	"github.com/fuuhoo/pipewright/internal/vault"
)

// setupSnapshotServer 构造带 run 服务 + **同库**工作区快照仓储的测试 server(回传该仓储供用例写入)。
// enabled=false 模拟 PIPEWRIGHT_SNAPSHOT=0 的装配(仓储为 nil → 端点 503)。
// 快照必须与 run 同库(表有指向 pipeline_runs 的外键),故仓储在 st.DB 上现构造后回传。
func setupSnapshotServer(t *testing.T, enabled bool) (*httptest.Server, *http.Client, string, string, run.Service, *buildsnap.Store) {
	t.Helper()
	st := testStoreAuth(t)
	var snapStore *buildsnap.Store
	if enabled {
		snapStore = buildsnap.New(st.DB, buildsnap.Limits{})
	}
	svc := auth.NewService(st.DB, nil, nil)
	if err := svc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	v := vault.New(st.DB, testMasterKey())
	psvc := project.New(st.DB, v, stubProber{branch: "main"})
	rsvc := run.New(st.DB)

	srv := httptest.NewServer(New(testWebFSAuth(), svc,
		WithVault(v), WithProjects(psvc), WithRuns(rsvc, run.NewWorkerPool(rsvc)),
		WithSnapshots(snapStore, 2*time.Hour)))
	t.Cleanup(srv.Close)

	client := newTestClient(t)
	csrf := loginWithClient(t, client, srv.URL)

	credID := newGitCred(t, client, srv.URL, csrf, "ghp_token_for_snap")
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/projects", csrf,
		`{"name":"acme","repoUrl":"https://gitee.com/acme/shop.git","credentialId":"`+credID+`"}`)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var p map[string]any
	_ = json.Unmarshal(raw, &p)
	projID, _ := p["id"].(string)
	if projID == "" {
		t.Fatalf("create project failed: %s", raw)
	}
	return srv, client, csrf, projID, rsvc, snapStore
}

// newTestRun 建一条运行(不入池执行,只为拿到一个真实 run id 挂快照)。
func newTestRun(t *testing.T, rsvc run.Service, projID string) string {
	t.Helper()
	r, err := rsvc.Create(context.Background(), projID,
		run.Trigger{Type: run.TriggerManual, Branch: "main", Commit: "a1b2c3d4", Actor: "admin"})
	if err != nil {
		t.Fatalf("Create run: %v", err)
	}
	return r.ID
}

// TestRunSnapshotsEndpoint 验证 GET /api/runs/{id}/snapshots 的响应形状:快照按节点归位、
// 声明路径带未命中证据、目录清单带路径/字节/截断数、保留窗口如实回传。
func TestRunSnapshotsEndpoint(t *testing.T) {
	srv, client, csrf, projID, rsvc, st := setupSnapshotServer(t, true)
	runID := newTestRun(t, rsvc, projID)

	if err := st.Save(context.Background(), buildsnap.Snapshot{
		RunID:   runID,
		Stage:   "构建",
		JobID:   "job-1",
		JobName: "后端构建",
		Reason:  buildsnap.ReasonArtifactMiss,
		Declared: []buildsnap.Declared{{
			Path: "dist", Status: buildsnap.StatusMissing, Siblings: []string{"target/", "pom.xml"}, SiblingsMore: 2,
		}},
		Entries:   []buildsnap.Entry{{Path: "target/"}, {Path: "target/app.jar", Size: 1024}},
		Truncated: true,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Save snapshot: %v", err)
	}

	resp := doJSON(t, client, http.MethodGet, srv.URL+"/api/runs/"+runID+"/snapshots", csrf, "")
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("snapshots status = %d: %s", resp.StatusCode, raw)
	}
	var body runSnapshotsDTO
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode snapshots: %v: %s", err, raw)
	}
	if body.RetentionSeconds != int((2 * time.Hour).Seconds()) {
		t.Fatalf("retentionSeconds = %d,应如实回传生效窗口 7200", body.RetentionSeconds)
	}
	if len(body.Snapshots) != 1 {
		t.Fatalf("应有 1 份快照: %s", raw)
	}
	s := body.Snapshots[0]
	if s.Reason != buildsnap.ReasonArtifactMiss || s.JobName != "后端构建" || s.Stage != "构建" {
		t.Fatalf("快照节点归位不对: %#v", s)
	}
	if !s.Truncated {
		t.Fatalf("truncated 应透传为 true")
	}
	if len(s.Declared) != 1 || s.Declared[0].Status != buildsnap.StatusMissing ||
		len(s.Declared[0].Siblings) != 2 || s.Declared[0].SiblingsMore != 2 {
		t.Fatalf("声明路径的未命中证据丢失: %#v", s.Declared)
	}
	if len(s.Entries) != 2 || s.Entries[1].Path != "target/app.jar" || s.Entries[1].Size != 1024 {
		t.Fatalf("目录清单不对: %#v", s.Entries)
	}
	if s.CreatedAt == "" {
		t.Fatalf("createdAt 应回传 RFC3339 时间")
	}
}

// TestRunSnapshotsEmptyIsArray 验证无快照时返回空数组(不是 null)—— 前端据此区分「没有快照」。
func TestRunSnapshotsEmptyIsArray(t *testing.T) {
	srv, client, csrf, projID, rsvc, _ := setupSnapshotServer(t, true)
	runID := newTestRun(t, rsvc, projID)

	resp := doJSON(t, client, http.MethodGet, srv.URL+"/api/runs/"+runID+"/snapshots", csrf, "")
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, raw)
	}
	var body struct {
		Snapshots *[]json.RawMessage `json:"snapshots"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode: %v: %s", err, raw)
	}
	if body.Snapshots == nil || len(*body.Snapshots) != 0 {
		t.Fatalf("无快照应返回空数组, got %s", raw)
	}
}

// TestRunSnapshotsNotFound 验证不存在的 run → 404 run_not_found(与 /artifacts 同口径)。
func TestRunSnapshotsNotFound(t *testing.T) {
	srv, client, csrf, _, _, _ := setupSnapshotServer(t, true)

	resp := doJSON(t, client, http.MethodGet, srv.URL+"/api/runs/no-such-run/snapshots", csrf, "")
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("不存在 run 应 404, got %d: %s", resp.StatusCode, raw)
	}
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	if e.Error.Code != "run_not_found" {
		t.Fatalf("错误码应为 run_not_found, got %q: %s", e.Error.Code, raw)
	}
}

// TestRunSnapshotsUnavailable 验证快照仓储未注入(PIPEWRIGHT_SNAPSHOT=0)→ 503 明确错误码,
// 而不是空列表骗前端「这次构建没有快照」。
func TestRunSnapshotsUnavailable(t *testing.T) {
	srv, client, csrf, projID, rsvc, _ := setupSnapshotServer(t, false)
	runID := newTestRun(t, rsvc, projID)

	resp := doJSON(t, client, http.MethodGet, srv.URL+"/api/runs/"+runID+"/snapshots", csrf, "")
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未启用应 503, got %d: %s", resp.StatusCode, raw)
	}
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	if e.Error.Code != "snapshots_unavailable" {
		t.Fatalf("错误码应为 snapshots_unavailable, got %q: %s", e.Error.Code, raw)
	}
}
