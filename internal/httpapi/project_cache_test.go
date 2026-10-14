// 项目本地缓存清单与一键清除(GET /api/projects/{id}/cache、POST .../cache/clear)。
//
//	清的两处:代码管理区的 bare 镜像(repo)、每项目固定依赖缓存(deps)。
//	用途是「怀疑本地和远程/私服不一致时从干净状态重来」—— 这里锁住它的对外契约:
//	体积口径、类别校验、构建占用时的 409、以及清除要有审计。
package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/mask"
	"github.com/fuuhoo/pipewright/internal/project"
	"github.com/fuuhoo/pipewright/internal/projectcache"
	"github.com/fuuhoo/pipewright/internal/repocache"
	"github.com/fuuhoo/pipewright/internal/vault"
	"github.com/go-chi/chi/v5"
)

// cacheTestEnv 是一次装配好的测试环境:真实两处缓存 + 一个带仓库的项目。
type cacheTestEnv struct {
	srv      *httptest.Server
	client   *http.Client
	csrf     string
	projID   string
	repoURL  string
	mirrors  *repocache.Cache
	deps     *projectcache.Layout
	recorder audit.Recorder
}

func setupProjectCacheServer(t *testing.T, repoURL string) *cacheTestEnv {
	t.Helper()
	st := testStoreAuth(t)
	authSvc := auth.NewService(st.DB, nil, nil)
	if err := authSvc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	v := vault.New(st.DB, testMasterKey())
	psvc := project.New(st.DB, v, stubProber{branch: "main"})
	projID := seedSourceProject(t, st.DB, repoURL)

	mirrors, err := repocache.New(filepath.Join(t.TempDir(), "repos"), nil)
	if err != nil {
		t.Fatalf("repocache.New: %v", err)
	}
	deps, err := projectcache.New(filepath.Join(t.TempDir(), "project-cache"))
	if err != nil {
		t.Fatalf("projectcache.New: %v", err)
	}
	rec := audit.New(st.DB, mask.NewMasker(), nil)

	srv := httptest.NewServer(New(testWebFSAuth(), authSvc,
		WithVault(v), WithProjects(psvc),
		WithProjectCache(mirrors, deps), WithAudit(rec)))
	t.Cleanup(srv.Close)

	client := newTestClient(t)
	csrf := loginWithClient(t, client, srv.URL)
	return &cacheTestEnv{srv: srv, client: client, csrf: csrf, projID: projID, repoURL: repoURL, mirrors: mirrors, deps: deps, recorder: rec}
}

// putFile 在某个缓存目录里造内容(等价于「这个项目以前构建过、磁盘上确实有东西」)。
func putFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func decodeCacheDTO(t *testing.T, resp *http.Response) projectCacheDTO {
	t.Helper()
	raw, _ := io.ReadAll(resp.Body)
	var dto projectCacheDTO
	if err := json.Unmarshal(raw, &dto); err != nil {
		t.Fatalf("decode: %v (%s)", err, raw)
	}
	return dto
}

// TestGetProjectCacheListsBothKinds 清单要按类别报出路径与体积,未启用的类别不出现。
func TestGetProjectCacheListsBothKinds(t *testing.T) {
	e := setupProjectCacheServer(t, "https://example.com/acme/api.git")
	mirrorPath := e.mirrors.MirrorInfoOf(e.repoURL).Path
	if mirrorPath == "" {
		t.Fatal("镜像路径应给出(即便还没拉过)")
	}
	depDir := e.deps.ProjectDir(e.projID)
	// 依赖缓存:两个子目录共 10+4 字节。
	putFile(t, filepath.Join(depDir, "maven", "lib.jar"), "0123456789")
	putFile(t, filepath.Join(depDir, "npm", "index"), "abcd")
	// 代码镜像:7 字节。
	putFile(t, filepath.Join(mirrorPath, "HEAD"), "ref: refs")

	resp, err := e.client.Get(e.srv.URL + "/api/projects/" + e.projID + "/cache")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	dto := decodeCacheDTO(t, resp)
	if len(dto.Items) != 2 {
		t.Fatalf("items = %+v, want 2 类", dto.Items)
	}
	byKind := map[string]projectCacheItemDTO{}
	for _, it := range dto.Items {
		byKind[it.Kind] = it
	}
	repo, ok := byKind[CacheKindRepo]
	if !ok {
		t.Fatalf("缺 repo 类:%+v", dto.Items)
	}
	if !repo.Exists || repo.Bytes != 9 || repo.Path != mirrorPath {
		t.Fatalf("repo = %+v, want Exists=true Bytes=9 Path=%s", repo, mirrorPath)
	}
	deps, ok := byKind[CacheKindDeps]
	if !ok {
		t.Fatalf("缺 deps 类:%+v", dto.Items)
	}
	if !deps.Exists || deps.Bytes != 14 || deps.Path != depDir {
		t.Fatalf("deps = %+v, want Exists=true Bytes=14 Path=%s", deps, depDir)
	}
	if dto.TotalBytes != 23 {
		t.Fatalf("totalBytes = %d, want 23", dto.TotalBytes)
	}
}

// TestGetProjectCacheNoRepo 未绑仓库的项目没有代码镜像可看,只报依赖缓存。
func TestGetProjectCacheNoRepo(t *testing.T) {
	e := setupProjectCacheServer(t, "")
	putFile(t, filepath.Join(e.deps.ProjectDir(e.projID), "go-mod", "x"), "yy")

	resp, err := e.client.Get(e.srv.URL + "/api/projects/" + e.projID + "/cache")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	dto := decodeCacheDTO(t, resp)
	if len(dto.Items) != 1 || dto.Items[0].Kind != CacheKindDeps {
		t.Fatalf("items = %+v, want 只有 deps", dto.Items)
	}
}

// TestClearProjectCacheDefaultsToBoth 不带 body 的清除 = 清本实例启用的全部,并留下审计。
func TestClearProjectCacheDefaultsToBoth(t *testing.T) {
	e := setupProjectCacheServer(t, "https://example.com/acme/web.git")
	mirrorPath := e.mirrors.MirrorInfoOf(e.repoURL).Path
	depDir := e.deps.ProjectDir(e.projID)
	putFile(t, filepath.Join(mirrorPath, "HEAD"), "ref: refs")
	putFile(t, filepath.Join(depDir, "maven", "lib.jar"), "0123456789")

	resp := doJSON(t, e.client, http.MethodPost, e.srv.URL+"/api/projects/"+e.projID+"/cache/clear", e.csrf, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200 (%s)", resp.StatusCode, raw)
	}
	var out cacheClearedDTO
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if len(out.Items) != 2 {
		t.Fatalf("items = %+v, want 2 类", out.Items)
	}
	if out.TotalBytes != 19 {
		t.Fatalf("totalBytes = %d, want 9+10", out.TotalBytes)
	}
	for _, it := range out.Items {
		if !it.Existed {
			t.Fatalf("清掉的类应 existed=true:%+v", it)
		}
		if _, err := os.Stat(it.Path); !os.IsNotExist(err) {
			t.Fatalf("%s 清除后目录仍在:%s", it.Kind, it.Path)
		}
	}
	// 顺序契约:先代码镜像后依赖缓存(前者决定「拉到什么代码」)。
	if out.Items[0].Kind != CacheKindRepo || out.Items[1].Kind != CacheKindDeps {
		t.Fatalf("items 顺序 = %+v", out.Items)
	}

	// 清除是改磁盘的动作,必须有审计(谁在什么时候把谁的缓存清空了)。
	res, err := e.recorder.List(context.Background(), audit.ListFilter{Action: audit.ActionProjectCacheClear})
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(res.Entries) != 1 {
		t.Fatalf("审计条数 = %d, want 1", len(res.Entries))
	}
	rec := res.Entries[0]
	if rec.TargetType != string(audit.TargetProject) || rec.TargetID != e.projID {
		t.Fatalf("审计目标 = %s/%s, want project/%s", rec.TargetType, rec.TargetID, e.projID)
	}
	if rec.Actor != "admin:admin" {
		t.Fatalf("审计 actor = %q", rec.Actor)
	}
	detail, _ := json.Marshal(rec.Detail)
	if !strings.Contains(string(detail), "freedBytes") || !strings.Contains(string(detail), "repo") {
		t.Fatalf("审计 detail 应含类别与释放字节:%s", detail)
	}

	// 清完再读清单:两处都标记为不存在、体积归零。
	listResp, err := e.client.Get(e.srv.URL + "/api/projects/" + e.projID + "/cache")
	if err != nil {
		t.Fatalf("GET after clear: %v", err)
	}
	defer listResp.Body.Close()
	after := decodeCacheDTO(t, listResp)
	if after.TotalBytes != 0 {
		t.Fatalf("清除后 totalBytes = %d, want 0", after.TotalBytes)
	}
	for _, it := range after.Items {
		if it.Exists {
			t.Fatalf("清除后 %s 仍显示存在", it.Kind)
		}
	}
}

// TestClearProjectCacheKindValidation 类别是白名单:乱写要拒绝,多余空白要放行。
func TestClearProjectCacheKindValidation(t *testing.T) {
	e := setupProjectCacheServer(t, "https://example.com/acme/valid.git")
	putFile(t, filepath.Join(e.deps.ProjectDir(e.projID), "go-mod", "x"), "yy")

	resp := doJSON(t, e.client, http.MethodPost, e.srv.URL+"/api/projects/"+e.projID+"/cache/clear", e.csrf, `{"kinds":["artifacts"]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "unknown_cache_kind") {
		t.Fatalf("body 应含 unknown_cache_kind:%s", raw)
	}

	// 类别名带空白也要放行(前端拼请求时多一个空格不该变成报错)。
	resp2 := doJSON(t, e.client, http.MethodPost, e.srv.URL+"/api/projects/"+e.projID+"/cache/clear", e.csrf, `{"kinds":[" deps "]}`)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		raw2, _ := io.ReadAll(resp2.Body)
		t.Fatalf("trim 后应合法,status = %d (%s)", resp2.StatusCode, raw2)
	}
	var out cacheClearedDTO
	if err := json.NewDecoder(resp2.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Items) != 1 || out.Items[0].Kind != CacheKindDeps || !out.Items[0].Existed {
		t.Fatalf("items = %+v, want 只清 deps", out.Items)
	}
}

// TestClearProjectCacheKindUnavailable 点名清一处本实例没启用的缓存 → 明确拒绝(而不是假装清了)。
func TestClearProjectCacheKindUnavailable(t *testing.T) {
	st := testStoreAuth(t)
	authSvc := auth.NewService(st.DB, nil, nil)
	if err := authSvc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	v := vault.New(st.DB, testMasterKey())
	psvc := project.New(st.DB, v, stubProber{branch: "main"})
	projID := seedSourceProject(t, st.DB, "https://example.com/acme/partial.git")
	deps, err := projectcache.New(filepath.Join(t.TempDir(), "project-cache"))
	if err != nil {
		t.Fatalf("projectcache.New: %v", err)
	}
	// 只启用依赖缓存(PIPEWRIGHT_NO_REPO_CACHE=1 的形态)。
	srv := httptest.NewServer(New(testWebFSAuth(), authSvc,
		WithVault(v), WithProjects(psvc), WithProjectCache(nil, deps)))
	defer srv.Close()
	client := newTestClient(t)
	csrf := loginWithClient(t, client, srv.URL)

	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/projects/"+projID+"/cache/clear", csrf, `{"kinds":["repo"]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "cache_kind_unavailable") {
		t.Fatalf("body 应含 cache_kind_unavailable:%s", raw)
	}

	// 同一实例不点名清除照常工作:只报 deps 一类。
	ok := doJSON(t, client, http.MethodPost, srv.URL+"/api/projects/"+projID+"/cache/clear", csrf, "")
	defer ok.Body.Close()
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", ok.StatusCode)
	}
	var out cacheClearedDTO
	_ = json.NewDecoder(ok.Body).Decode(&out)
	if len(out.Items) != 1 || out.Items[0].Kind != CacheKindDeps {
		t.Fatalf("items = %+v, want 只有 deps", out.Items)
	}
}

// TestClearProjectCacheUnboundRepo 没绑仓库的项目点名清 repo → 明确拒绝,而不是默默什么都不做。
func TestClearProjectCacheUnboundRepo(t *testing.T) {
	e := setupProjectCacheServer(t, "")
	resp := doJSON(t, e.client, http.MethodPost, e.srv.URL+"/api/projects/"+e.projID+"/cache/clear", e.csrf, `{"kinds":["repo"]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "repo_not_bound") {
		t.Fatalf("body 应含 repo_not_bound:%s", raw)
	}

	// 但「一键清除」(不点名)对这个项目必须能用:默认集不能替它带上它没有的代码镜像。
	ok := doJSON(t, e.client, http.MethodPost, e.srv.URL+"/api/projects/"+e.projID+"/cache/clear", e.csrf, "")
	defer ok.Body.Close()
	if ok.StatusCode != http.StatusOK {
		rawOK, _ := io.ReadAll(ok.Body)
		t.Fatalf("默认清除 status = %d, want 200 (%s)", ok.StatusCode, rawOK)
	}
	var out cacheClearedDTO
	_ = json.NewDecoder(ok.Body).Decode(&out)
	if len(out.Items) != 1 || out.Items[0].Kind != CacheKindDeps {
		t.Fatalf("items = %+v, want 只有 deps", out.Items)
	}
}

// TestProjectCacheNotFound 项目不存在 → 404(不能退化成 500,前端要能区分)。
func TestProjectCacheNotFound(t *testing.T) {
	e := setupProjectCacheServer(t, "https://example.com/acme/nf.git")
	resp, err := e.client.Get(e.srv.URL + "/api/projects/00000000-0000-0000-0000-0000000000zz/cache")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}

	clearResp := doJSON(t, e.client, http.MethodPost, e.srv.URL+"/api/projects/00000000-0000-0000-0000-0000000000zz/cache/clear", e.csrf, "")
	defer clearResp.Body.Close()
	if clearResp.StatusCode != http.StatusNotFound {
		t.Fatalf("clear status = %d, want 404", clearResp.StatusCode)
	}
}

// TestProjectCacheDisabledWhenNoStores 两处都没启用(PIPEWRIGHT_NO_* 全关)时端点明说无需清除。
func TestProjectCacheDisabledWhenNoStores(t *testing.T) {
	st := testStoreAuth(t)
	authSvc := auth.NewService(st.DB, nil, nil)
	if err := authSvc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	v := vault.New(st.DB, testMasterKey())
	psvc := project.New(st.DB, v, stubProber{branch: "main"})
	projID := seedSourceProject(t, st.DB, "https://example.com/acme/off.git")

	srv := httptest.NewServer(New(testWebFSAuth(), authSvc, WithVault(v), WithProjects(psvc), WithProjectCache(nil, nil)))
	defer srv.Close()
	client := newTestClient(t)
	csrf := loginWithClient(t, client, srv.URL)

	resp, err := client.Get(srv.URL + "/api/projects/" + projID + "/cache")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}

	clearResp := doJSON(t, client, http.MethodPost, srv.URL+"/api/projects/"+projID+"/cache/clear", csrf, "")
	defer clearResp.Body.Close()
	if clearResp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("clear status = %d, want 503", clearResp.StatusCode)
	}
}

// fakeMirrors 只用来造「镜像正被构建占用」—— 真锁在 repocache 内部,测试够不着。
type fakeMirrors struct{ err error }

func (f fakeMirrors) MirrorInfoOf(repoURL string) repocache.MirrorInfo {
	return repocache.MirrorInfo{Path: "/data/repos/whatever.git", Bytes: 4096, Exists: true}
}

func (f fakeMirrors) DropMirror(string) (repocache.MirrorInfo, error) {
	return repocache.MirrorInfo{Path: "/data/repos/whatever.git", Bytes: 4096, Exists: true}, f.err
}

// TestClearProjectCacheBusyDuringBuild 构建持着仓库锁时清除必须 409 回绝(不排队等构建),
// 并且绝不删任何东西。
func TestClearProjectCacheBusyDuringBuild(t *testing.T) {
	st := testStoreAuth(t)
	authSvc := auth.NewService(st.DB, nil, nil)
	if err := authSvc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	v := vault.New(st.DB, testMasterKey())
	psvc := project.New(st.DB, v, stubProber{branch: "main"})
	projID := seedSourceProject(t, st.DB, "https://example.com/acme/busy.git")

	handler := makeClearProjectCacheHandler(psvc,
		cacheStoreSet{mirrors: fakeMirrors{err: repocache.ErrBusy}, deps: nil},
		audit.New(st.DB, mask.NewMasker(), nil), authSvc)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projID+"/cache/clear", strings.NewReader(`{"kinds":["repo"]}`))
	req.Header.Set("Content-Type", "application/json")
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", projID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))

	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "cache_busy") {
		t.Fatalf("body 应含 cache_busy:%s", rec.Body.String())
	}
}

// 清单与清除结果里的路径一律绝对化:相对 DSN 起步的裸实例,镜像根是相对 cwd 的「repos/…」,
// 容器形态下那截落在具名卷里 —— 用户在宿主上对不上号,这张表就没起到「去哪儿核对」的作用。
func TestCachePathsAreAbsolute(t *testing.T) {
	rel := filepath.Join("repos", "823d0ff5cf28f384.git")
	want, err := filepath.Abs(rel)
	if err != nil {
		t.Fatalf("filepath.Abs(%q): %v", rel, err)
	}
	if got := displayPath(rel); got != want {
		t.Errorf("displayPath(%q) = %q, want %q", rel, got, want)
	}
	if got := displayPath(want); got != want {
		t.Errorf("绝对路径不该被改写:%q -> %q", want, got)
	}

	// 端到端:根目录按相对路径起步(裸实例用相对 DSN 时就是这样),清单仍须给出能照着核对的绝对路径。
	dir := t.TempDir()
	t.Chdir(dir)
	mirrors, err := repocache.New("repos", nil)
	if err != nil {
		t.Fatalf("repocache.New: %v", err)
	}
	relMirror := mirrors.MirrorInfoOf("https://example.com/acme/api.git").Path
	if filepath.IsAbs(relMirror) {
		t.Fatalf("测试前提不成立:相对根应得出相对路径,得到 %q", relMirror)
	}
	items := cacheStoreSet{mirrors: mirrors}.items("https://example.com/acme/api.git", "p-1")
	if len(items.Items) != 1 {
		t.Fatalf("items = %+v, want 1 类", items.Items)
	}
	if got := items.Items[0].Path; !filepath.IsAbs(got) {
		t.Errorf("repo 路径不是绝对路径:%q", got)
	} else if want := filepath.Join(dir, relMirror); got != want {
		t.Errorf("repo 路径 = %q, want %q", got, want)
	}
	// 清除结果同样绝对化:用户按这条回看「刚才删的是哪儿」。
	cleared := cacheClearedItemDTO{Kind: CacheKindRepo, Path: displayPath(relMirror)}
	if !filepath.IsAbs(cleared.Path) {
		t.Errorf("清除结果路径不是绝对路径:%q", cleared.Path)
	}
}
