package httpapi

import (
	"net/http"
	"time"

	"github.com/fuuhoo/pipewright/internal/buildsnap"
	"github.com/fuuhoo/pipewright/internal/run"
	"github.com/go-chi/chi/v5"
)

// run_snapshots.go 提供 GET /api/runs/{id}/snapshots —— 运行详情「构建目录」面板的数据源。
//
// 快照只在两种情况下产生:script 节点执行失败、或节点声明的产物路径没命中(产物根本没生成)。
// 它记录的是**当时的容器工作区目录清单**,用来回答"我是不是把产物目录配错了":
// 失败时能看出命令实际产出在哪;产物未命中时能看出声明路径的最近祖先目录里到底有什么。
// 快照默认只保留 2 小时(见 buildsnap.Sweeper),过期后端点返回空列表 —— 面板要如实说明这点。

// snapshotEntryDTO 是目录清单里的一项(路径相对容器工作区根,始终带尾斜杠表示目录)。
type snapshotEntryDTO struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Dropped int    `json:"dropped"` // 该目录内因单目录上限未列出的条目数
}

// declaredPathDTO 回显节点声明的一条产物路径,并给出未命中时的现场证据。
type declaredPathDTO struct {
	Path         string   `json:"path"`
	Status       string   `json:"status"` // ok | missing | unmatched | rejected
	Parent       string   `json:"parent,omitempty"`
	Siblings     []string `json:"siblings,omitempty"`
	SiblingsMore int      `json:"siblingsMore"`
}

// workspaceSnapshotDTO 是一份快照(绑定到一个 job 节点)。
type workspaceSnapshotDTO struct {
	ID        string             `json:"id"`
	Stage     string             `json:"stage"`
	JobID     string             `json:"jobId"`
	JobName   string             `json:"jobName"`
	Reason    string             `json:"reason"` // failed | artifact_miss
	Declared  []declaredPathDTO  `json:"declared"`
	Entries   []snapshotEntryDTO `json:"entries"`
	Truncated bool               `json:"truncated"` // 撞总条数上限,清单不完整
	CreatedAt string             `json:"createdAt"`
}

// runSnapshotsDTO 是 GET /api/runs/{id}/snapshots 响应体。
type runSnapshotsDTO struct {
	Snapshots []workspaceSnapshotDTO `json:"snapshots"`
	// RetentionSeconds 是快照保留窗口,供面板如实标注"只保留最近 N 小时"。
	RetentionSeconds int `json:"retentionSeconds"`
}

func toSnapshotEntryDTO(e buildsnap.Entry) snapshotEntryDTO {
	return snapshotEntryDTO{Path: e.Path, Size: e.Size, Dropped: e.Dropped}
}

func toDeclaredDTO(d buildsnap.Declared) declaredPathDTO {
	siblings := d.Siblings
	if siblings == nil {
		siblings = []string{}
	}
	return declaredPathDTO{Path: d.Path, Status: d.Status, Parent: d.Parent, Siblings: siblings, SiblingsMore: d.SiblingsMore}
}

func toSnapshotDTOs(snaps []buildsnap.Snapshot) []workspaceSnapshotDTO {
	out := make([]workspaceSnapshotDTO, 0, len(snaps))
	for _, s := range snaps {
		entries := make([]snapshotEntryDTO, 0, len(s.Entries))
		for _, e := range s.Entries {
			entries = append(entries, toSnapshotEntryDTO(e))
		}
		declared := make([]declaredPathDTO, 0, len(s.Declared))
		for _, d := range s.Declared {
			declared = append(declared, toDeclaredDTO(d))
		}
		out = append(out, workspaceSnapshotDTO{
			ID: s.ID, Stage: s.Stage, JobID: s.JobID, JobName: s.JobName, Reason: s.Reason,
			Declared: declared, Entries: entries, Truncated: s.Truncated,
			CreatedAt: s.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return out
}

// makeRunSnapshotsHandler 返回 GET /api/runs/{id}/snapshots handler(认证 + 分组收口中间件;只读)。
// store 为 nil(PIPEWRIGHT_SNAPSHOT=0)→ 503;run 不存在 → 404(与 /artifacts 同口径)。
func makeRunSnapshotsHandler(svc run.Service, store *buildsnap.Store, ttl time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "运行服务未初始化")
			return
		}
		if store == nil {
			writeError(w, http.StatusServiceUnavailable, "snapshots_unavailable", "工作区目录快照未启用")
			return
		}
		id := chi.URLParam(r, "id")
		if _, err := svc.Get(r.Context(), id); err != nil {
			writeRunError(w, err)
			return
		}
		snaps, err := store.ListByRun(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
			return
		}
		writeJSON(w, http.StatusOK, runSnapshotsDTO{
			Snapshots:        toSnapshotDTOs(snaps),
			RetentionSeconds: int(ttl.Seconds()),
		})
	}
}
