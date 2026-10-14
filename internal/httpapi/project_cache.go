package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/project"
	"github.com/fuuhoo/pipewright/internal/projectcache"
	"github.com/fuuhoo/pipewright/internal/repocache"
	"github.com/go-chi/chi/v5"
)

// project_cache.go —— 项目本地缓存的清单与一键清除。
//
//	GET  /api/projects/{id}/cache        → { items:[{kind,path,bytes,exists}], totalBytes }
//	POST /api/projects/{id}/cache/clear  → { items:[{kind,path,bytes,existed}], totalBytes }(需 CSRF)
//
// 清的是**平台上按项目分格的磁盘缓存**,两处:
//   - repo:代码管理区的 bare 镜像(internal/repocache)。构建只增量 fetch,怀疑镜像和远程不一致时
//     需要一个「从干净全量重来」的出口。
//   - deps:每项目固定依赖缓存(internal/projectcache)。Maven 的 .m2 里存着 SNAPSHOT,私服重新
//     deploy 过同一个版本时本地那份未必被刷新,现象和「没拉到最新代码」一模一样。
//
// 不含制品库(那是部署要用的真字节)、也不含旧 cachePaths 那套按内容 key 寻址的构建缓存
// (key 里没有项目 ID,按项目清不出归属)。
//
// 读走分组权限的 ActView、清除走 ActOperate(由 accessGuardMiddleware 按 URL 统一判定,见
// access_guard.go);清除另记一条审计。仓库镜像按**地址**分格,可能被多个项目共用,清掉会影响
// 它们下一次拉取(只是变慢,不是变坏),前端文案如实说明。

// 缓存类别(对外契约字面量;前端按此翻译标签)。
const (
	CacheKindRepo = "repo"
	CacheKindDeps = "deps"
)

// repoMirrors 是代码管理区的能力切片(*repocache.Cache 满足)。
type repoMirrors interface {
	MirrorInfoOf(repoURL string) repocache.MirrorInfo
	DropMirror(repoURL string) (repocache.MirrorInfo, error)
}

// depCaches 是项目依赖缓存的能力切片(*projectcache.Layout 满足)。
type depCaches interface {
	InfoOf(projectID string) projectcache.CacheInfo
	Clear(projectID string) (projectcache.CacheInfo, error)
}

// cacheStoreSet 是两处缓存的注入包(nil 成员 = 本实例没启用那一处)。
type cacheStoreSet struct {
	mirrors repoMirrors
	deps    depCaches
}

// enabled 报告本实例至少启用了一处缓存(两处都没启用时端点直接 503)。
func (s cacheStoreSet) enabled() bool { return s.mirrors != nil || s.deps != nil }

// available 报告某一类缓存本实例是否用得上。
func (s cacheStoreSet) available(kind string) bool {
	switch kind {
	case CacheKindRepo:
		return s.mirrors != nil
	case CacheKindDeps:
		return s.deps != nil
	default:
		return false
	}
}

// defaultKinds 是「没点名要清哪几处」时的默认:清本实例启用的全部。
// 顺序先代码镜像后依赖缓存 —— 前者决定「拉到什么代码」。
func (s cacheStoreSet) defaultKinds() []string {
	var kinds []string
	if s.mirrors != nil {
		kinds = append(kinds, CacheKindRepo)
	}
	if s.deps != nil {
		kinds = append(kinds, CacheKindDeps)
	}
	return kinds
}

type projectCacheItemDTO struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	Exists bool   `json:"exists"`
}

type projectCacheDTO struct {
	Items      []projectCacheItemDTO `json:"items"`
	TotalBytes int64                 `json:"totalBytes"`
}

type cacheClearedItemDTO struct {
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Bytes   int64  `json:"bytes"`   // 删除前的体积 = 本次释放的字节数
	Existed bool   `json:"existed"` // false = 原本就是空的,没东西可删
}

type cacheClearedDTO struct {
	Items      []cacheClearedItemDTO `json:"items"`
	TotalBytes int64                 `json:"totalBytes"`
}

// displayPath 把缓存目录解析成绝对路径再对外给出。
// 代码镜像的根目录默认取 DB 同级,DB 走相对 DSN 时那一段就是相对进程 cwd 的裸「repos/xxx.git」——
// 容器形态下它落在具名卷里,用户在宿主上根本对不上号。这张清单的意义是「去哪儿核对/删了这一处」,
// 所以路径必须说全本实例看到的到底是哪儿(解析失败就原样给,不因显示问题报接口错)。
func displayPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

// items 汇总两处缓存的实况;未启用的那一处不出现在结果里(前端据此整块不渲染)。
// 没绑仓库的项目也没有代码镜像可看,同理只报依赖缓存。
func (s cacheStoreSet) items(repoURL, projectID string) projectCacheDTO {
	out := projectCacheDTO{Items: []projectCacheItemDTO{}}
	add := func(kind, path string, bytes int64, exists bool) {
		out.Items = append(out.Items, projectCacheItemDTO{Kind: kind, Path: displayPath(path), Bytes: bytes, Exists: exists})
		out.TotalBytes += bytes
	}
	if s.mirrors != nil && strings.TrimSpace(repoURL) != "" {
		mi := s.mirrors.MirrorInfoOf(repoURL)
		add(CacheKindRepo, mi.Path, mi.Bytes, mi.Exists)
	}
	if s.deps != nil {
		di := s.deps.InfoOf(projectID)
		add(CacheKindDeps, di.Path, di.Bytes, di.Exists)
	}
	return out
}

func writeProjectCacheError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, project.ErrNotFound):
		writeError(w, http.StatusNotFound, "project_not_found", "项目不存在")
	default:
		writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
	}
}

// makeGetProjectCacheHandler 返回 GET /api/projects/{id}/cache handler(认证;只读)。
func makeGetProjectCacheHandler(projects project.Service, stores cacheStoreSet) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !stores.enabled() {
			writeError(w, http.StatusServiceUnavailable, "cache_disabled", "本实例没有启用任何项目本地缓存,无需清除")
			return
		}
		id := chi.URLParam(r, "id")
		proj, err := projects.Get(r.Context(), id)
		if err != nil {
			writeProjectCacheError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, stores.items(proj.RepoURL, id))
	}
}

// makeClearProjectCacheHandler 返回 POST /api/projects/{id}/cache/clear handler(认证 + CSRF + 审计)。
// 请求体 `{"kinds":["repo","deps"]}` 可省略(含空 body) = 清除本实例启用的全部类别。
//
// 逐类清除:一类失败(如镜像正被构建占用)不回滚另一类已完成的清除 —— 清除幂等,用户按提示
// 重试即可,不值得为两个目录引入事务。
func makeClearProjectCacheHandler(projects project.Service, stores cacheStoreSet, rec audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !stores.enabled() {
			writeError(w, http.StatusServiceUnavailable, "cache_disabled", "本实例没有启用任何项目本地缓存,无需清除")
			return
		}
		id := chi.URLParam(r, "id")
		r.Body = http.MaxBytesReader(w, r.Body, 1<<14)
		var req struct {
			Kinds []string `json:"kinds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		kinds := req.Kinds
		proj, err := projects.Get(r.Context(), id)
		if err != nil {
			writeProjectCacheError(w, err)
			return
		}
		if len(kinds) == 0 {
			// 没点名 = 清这个项目**实际有**的那几处。未绑仓库的项目压根没有代码镜像,
			// 默认集里就不能带 repo —— 否则「一键清除」在纯发布项目上必然 400(见 items 同款判定)。
			for _, kind := range stores.defaultKinds() {
				if kind == CacheKindRepo && strings.TrimSpace(proj.RepoURL) == "" {
					continue
				}
				kinds = append(kinds, kind)
			}
		}
		out := cacheClearedDTO{Items: []cacheClearedItemDTO{}}
		clearedKinds := []string{}
		for _, raw := range kinds {
			kind := strings.TrimSpace(raw)
			if !stores.available(kind) {
				if kind == CacheKindRepo || kind == CacheKindDeps {
					writeError(w, http.StatusBadRequest, "cache_kind_unavailable", "本实例没有启用这一处缓存")
					return
				}
				writeError(w, http.StatusBadRequest, "unknown_cache_kind", "缓存类别只支持 repo(代码镜像)与 deps(依赖缓存)")
				return
			}
			switch kind {
			case CacheKindRepo:
				if strings.TrimSpace(proj.RepoURL) == "" {
					writeError(w, http.StatusBadRequest, "repo_not_bound", "该项目未绑定仓库,没有代码镜像可清")
					return
				}
				mi, derr := stores.mirrors.DropMirror(proj.RepoURL)
				if errors.Is(derr, repocache.ErrBusy) {
					writeError(w, http.StatusConflict, "cache_busy", "该项目正在构建,代码镜像暂时被占用,等这次构建结束再清")
					return
				}
				if derr != nil {
					writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
					return
				}
				out.Items = append(out.Items, cacheClearedItemDTO{Kind: kind, Path: displayPath(mi.Path), Bytes: mi.Bytes, Existed: mi.Exists})
				out.TotalBytes += mi.Bytes
			case CacheKindDeps:
				di, cerr := stores.deps.Clear(id)
				if cerr != nil {
					writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
					return
				}
				out.Items = append(out.Items, cacheClearedItemDTO{Kind: kind, Path: displayPath(di.Path), Bytes: di.Bytes, Existed: di.Exists})
				out.TotalBytes += di.Bytes
			}
			clearedKinds = append(clearedKinds, kind)
		}
		recordAuditFromRequest(r, rec, ac, audit.Entry{
			Action:     audit.ActionProjectCacheClear,
			TargetType: audit.TargetProject,
			TargetID:   id,
			Detail:     map[string]any{"kinds": clearedKinds, "freedBytes": out.TotalBytes},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusOK, out)
	}
}
