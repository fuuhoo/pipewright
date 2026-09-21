package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huangchengsir/pipewright/internal/audit"
	"github.com/huangchengsir/pipewright/internal/auth"
	"github.com/huangchengsir/pipewright/internal/mask"
	"github.com/huangchengsir/pipewright/internal/project"
	"github.com/huangchengsir/pipewright/internal/repocache"
	"github.com/huangchengsir/pipewright/internal/vault"
)

// ----- sync refs tests ------------------------------------------------------
//
// 写操作 → 必过 auth + CSRF;失败/成功均留 audit(ActionRepoSync,NFR-8 取证)。
// sync 与 ListRefs 共用 loadProjectRefs(repocache 每次 ListRefs 隐式 fetch),所以 handler
// 在数据流上同款;这里聚焦 sync 独有的契约:
//   1) POST 缺 CSRF → 403,audit 不写;
//   2) POST 成功 → 200 + refsResponse + audit ok=true + 计数;
//   3) POST 失败(代码管理区未启用 / 仓库不可达 / 项目不存在)→ 503 / 502 / 404,
//      且 audit ok=false + 短 reason 串(便于取证,无凭据明文)。

// syncRefsLister 是 ListRefs / ListCommits 共用接口的最小测试替身:支持独立注入错误。
type syncRefsLister struct {
	refs    *repocache.Refs
	commits []repocache.Commit
	err     error
}

func (l syncRefsLister) ListRefs(_ context.Context, _, _, _ string) (*repocache.Refs, error) {
	if l.err != nil {
		return nil, l.err
	}
	return l.refs, nil
}
func (l syncRefsLister) ListCommits(_ context.Context, _, _, _, _ string, _ int) ([]repocache.Commit, error) {
	return l.commits, nil
}

// setupSyncRefsServer 在 setupRefsServer 基础上多注入一个 audit Recorder(读 audit 表断言)。
func setupSyncRefsServer(t *testing.T, lister RefsLister) (string, *http.Client, string, string, audit.Recorder) {
	t.Helper()
	st := testStoreAuth(t)
	svc := auth.NewService(st.DB, nil, nil)
	if err := svc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	v := vault.New(st.DB, testMasterKey())
	psvc := project.New(st.DB, v, stubProber{branch: "main"})
	projID := seedSourceProject(t, st.DB, "https://example.com/p.git")

	rec := audit.New(st.DB, mask.NewMasker(), nil)
	srv := httptest.NewServer(New(testWebFSAuth(), svc, WithVault(v), WithProjects(psvc), WithRefs(lister), WithAudit(rec)))
	t.Cleanup(srv.Close)
	client := newTestClient(t)
	csrf := loginWithClient(t, client, srv.URL)
	return srv.URL, client, csrf, projID, rec
}

// querySyncAudit 读 audit 表里所有 repo_sync 记录,断言用。
func querySyncAudit(t *testing.T, rec audit.Recorder) []audit.Record {
	t.Helper()
	res, err := rec.List(context.Background(), audit.ListFilter{Action: audit.ActionRepoSync, Limit: 10})
	if err != nil {
		t.Fatalf("audit.List: %v", err)
	}
	return res.Entries
}

// TestSyncRefsSuccess POST 200 + refsResponse + audit ok=true + branch/tag 计数正确。
func TestSyncRefsSuccess(t *testing.T) {
	lister := syncRefsLister{refs: &repocache.Refs{
		Branches: []repocache.Ref{{Name: "main", Commit: "aaa111"}, {Name: "dev", Commit: "bbb222"}},
		Tags:     []repocache.Ref{{Name: "v1.0", Commit: "ccc333", IsTag: true}},
	}}
	srv, client, csrf, projID, aud := setupSyncRefsServer(t, lister)

	resp := doJSON(t, client, http.MethodPost, srv+"/api/projects/"+projID+"/refs/sync", csrf, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body refsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Branches) != 2 || body.Tags[0].Name != "v1.0" {
		t.Fatalf("body 不对: %+v", body)
	}

	entries := querySyncAudit(t, aud)
	if len(entries) != 1 {
		t.Fatalf("audit 应 1 条,实际 %d", len(entries))
	}
	e := entries[0]
	if e.Action != audit.ActionRepoSync || e.TargetType != audit.TargetProject || e.TargetID != projID {
		t.Fatalf("audit 字段不对: %+v", e)
	}
	if ok, _ := e.Detail["ok"].(bool); !ok {
		t.Fatalf("audit.detail.ok 应 true,实际 %+v", e.Detail)
	}
	if bc, ok := e.Detail["branchCount"].(float64); !ok || bc != 2 {
		t.Fatalf("audit.detail.branchCount 应 2,实际 %v", e.Detail["branchCount"])
	}
	if tc, ok := e.Detail["tagCount"].(float64); !ok || tc != 1 {
		t.Fatalf("audit.detail.tagCount 应 1,实际 %v", e.Detail["tagCount"])
	}
}

// TestSyncRefsRequiresCSRF POST 缺 CSRF → 403,audit 不写(失败的写尝试仍不算留痕事件,因为根本没进入 handler)。
func TestSyncRefsRequiresCSRF(t *testing.T) {
	lister := syncRefsLister{refs: &repocache.Refs{}}
	srv, client, _, projID, aud := setupSyncRefsServer(t, lister)

	resp := doJSON(t, client, http.MethodPost, srv+"/api/projects/"+projID+"/refs/sync", "wrong-csrf", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("缺/错 CSRF 应 403,实际 %d", resp.StatusCode)
	}

	if n := querySyncAudit(t, aud); len(n) != 0 {
		t.Fatalf("403 不应写 audit,实际 %d 条", len(n))
	}
}

// TestSyncRefsDisabled POST 在代码管理区未启用时 → 503 + audit ok=false reason=repocache_disabled。
func TestSyncRefsDisabled(t *testing.T) {
	srv, client, csrf, projID, aud := setupSyncRefsServer(t, nil) // lister=nil → 503

	resp := doJSON(t, client, http.MethodPost, srv+"/api/projects/"+projID+"/refs/sync", csrf, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("代码管理区未启用应 503,实际 %d", resp.StatusCode)
	}

	entries := querySyncAudit(t, aud)
	if len(entries) != 1 {
		t.Fatalf("audit 应 1 条,实际 %d", len(entries))
	}
	if ok, _ := entries[0].Detail["ok"].(bool); ok {
		t.Fatalf("audit.detail.ok 应 false,实际 %+v", entries[0].Detail)
	}
	if r, _ := entries[0].Detail["reason"].(string); r != "repocache_disabled" {
		t.Fatalf("audit.detail.reason 应 repocache_disabled,实际 %v", entries[0].Detail["reason"])
	}
}

// TestSyncRefsProjectNotFound POST 在项目不存在时 → 404 + audit ok=false reason=project_not_found。
func TestSyncRefsProjectNotFound(t *testing.T) {
	lister := syncRefsLister{refs: &repocache.Refs{}}
	srv, client, csrf, _, aud := setupSyncRefsServer(t, lister)

	resp := doJSON(t, client, http.MethodPost, srv+"/api/projects/nope-not-exist/refs/sync", csrf, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("项目不存在应 404,实际 %d", resp.StatusCode)
	}

	entries := querySyncAudit(t, aud)
	if len(entries) != 1 {
		t.Fatalf("audit 应 1 条,实际 %d", len(entries))
	}
	if r, _ := entries[0].Detail["reason"].(string); r != "project_not_found" {
		t.Fatalf("audit.detail.reason 应 project_not_found,实际 %v", entries[0].Detail["reason"])
	}
}

// TestSyncRefsUnavailable POST 在仓库不可达/凭据无效时 → 502 + audit ok=false reason=refs_unavailable。
func TestSyncRefsUnavailable(t *testing.T) {
	lister := syncRefsLister{err: errors.New("simulated fetch failure")}
	srv, client, csrf, projID, aud := setupSyncRefsServer(t, lister)

	resp := doJSON(t, client, http.MethodPost, srv+"/api/projects/"+projID+"/refs/sync", csrf, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("refs 不可用应 502,实际 %d", resp.StatusCode)
	}

	entries := querySyncAudit(t, aud)
	if len(entries) != 1 {
		t.Fatalf("audit 应 1 条,实际 %d", len(entries))
	}
	if r, _ := entries[0].Detail["reason"].(string); r != "refs_unavailable" {
		t.Fatalf("audit.detail.reason 应 refs_unavailable,实际 %v", entries[0].Detail["reason"])
	}
}

// 占位,防止 strings 包被 goimports 误删(后续若用到 keep 即可移除)。
var _ = strings.TrimSpace