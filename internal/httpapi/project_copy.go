package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/fuuhoo/pipewright/internal/access"
	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/projectcopy"
	"github.com/go-chi/chi/v5"
)

// project_copy.go —— 「复制项目」的两个端点:单个项目与一批项目。
//
// 为什么单个复制挂在 /api/projects/{id}/copy:accessGuardMiddleware 按 URL 第二段认出资源,
// /api/projects/<任何一段> 都算项目 ID(access_guard.go 的 resolveGuardTarget),于是写方法
// 自动判到 ActOperate —— 也就是角色功能点 project.edit + 源项目的分组归属。这条路径漏挂
// 判定就等于开一个越权口子,所以宁可让 URL 形状去迁就中间件,而不是在 handler 里再判一遍。
//
// 批量复制反过来不能挂在这个前缀下:它的入参是「一批项目」,没有单一 ID 可认。
// 也不挂 /api/groups/{id}/copy-projects —— 因为「未归组」在本平台是空串而非路径段
// (access.Ungrouped == "",前端分组视图拿它当那一段的 key),URL 里塞不出这个 id,
// 「把未归组那批复制一份」就会 404。所以走集合式 POST /api/project-copies,
// 源项目 ID 全在请求体里,逐条判权限 —— 与 POST /api/projects 创建同理(该路径同样不被中间件识别)。

// maxCopyBatch 是一次批量复制的上限。一批 = 一个事务,摊开几十张表的 INSERT,
// 上限是为了不让一个请求把库按在那里;真要多于这个数,分几批点。
const maxCopyBatch = 200

// copyOneRequest 是 POST /api/projects/{id}/copy 的请求体。
type copyOneRequest struct {
	// Name 非空 = 直接用这个名字;空 = 源名 + Suffix。
	Name string `json:"name"`
	// Suffix 仅在 Name 为空时生效(原样接在源名后面,含分隔符)。
	Suffix string `json:"suffix"`
	// TargetGroupID 三态:缺字段 = 沿用源项目分组;指向 "" = 落到未归组;指向某组 = 复制进该组。
	TargetGroupID *string `json:"targetGroupId"`
}

// copyBatchRequest 是 POST /api/project-copies 的请求体。
type copyBatchRequest struct {
	// ProjectIDs 是要复制的源项目。由前端把「勾中的项目」原样提交,服务端不重新枚举:
	// 逐条判定权限才是这件事的正解,少判一条就是拿别人的项目多切一份副本。
	ProjectIDs []string `json:"projectIds"`
	Suffix     string   `json:"suffix"`
	// TargetGroupID 同单复制的三态;批里每条都用它。
	TargetGroupID *string `json:"targetGroupId"`
}

// copiedProjectDTO 是一份副本的回执(前端拿到就刷新列表,不在本地拼卡片)。
type copiedProjectDTO struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	GroupID  string `json:"groupId"`
	SourceID string `json:"sourceId"`
}

func toCopiedDTO(made []projectcopy.Created) []copiedProjectDTO {
	out := make([]copiedProjectDTO, 0, len(made))
	for _, c := range made {
		out = append(out, copiedProjectDTO{ID: c.ID, Name: c.Name, GroupID: c.GroupID, SourceID: c.SourceID})
	}
	return out
}

// writeCopyError 把复制的领域错误映射成契约码。未列出的错误按内部错误处理,不回显细节。
func writeCopyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, projectcopy.ErrNotFound):
		writeError(w, http.StatusNotFound, "project_not_found", "项目不存在")
	case errors.Is(err, projectcopy.ErrGroupNotFound):
		writeError(w, http.StatusUnprocessableEntity, "group_not_found", "引用的分组不存在")
	case errors.Is(err, projectcopy.ErrEmptyBatch):
		writeError(w, http.StatusBadRequest, "nothing_to_copy", "没有要复制的项目")
	case errors.Is(err, projectcopy.ErrTooManyCopies):
		writeError(w, http.StatusConflict, "too_many_copies", "同名副本已经太多,请手动指定名称")
	default:
		writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
	}
}

// makeCopyProjectHandler 返回 POST /api/projects/{id}/copy handler。
// 归属判定已由 accessGuard 中间件按 URL 做完(写方法 → ActOperate),这里只补「复制到哪个组」:
// 那是归属改动,要求新组侧的 ActManage,与 PATCH /api/projects/{id} 的归组同一条规则。
func makeCopyProjectHandler(svc *projectcopy.Service, acc *access.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "项目复制服务未初始化")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<14)
		var req copyOneRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		if req.TargetGroupID != nil && acc != nil && !requireGroupPlacement(w, r, acc, *req.TargetGroupID) {
			return
		}
		created, err := svc.Copy(r.Context(), projectcopy.Input{
			SourceID: chi.URLParam(r, "id"),
			Name:     req.Name,
			Suffix:   req.Suffix,
			GroupID:  req.TargetGroupID,
		})
		if err != nil {
			writeCopyError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionProjectCopy,
			TargetType: audit.TargetProject,
			TargetID:   created.ID,
			// detail 只带标识与名字:副本内容整份都在库里,审计里写一遍只会把配置抄进流水。
			Detail: map[string]any{"sourceId": created.SourceID, "name": created.Name, "groupId": created.GroupID},
			IP:     clientIP(r),
		})
		writeJSON(w, http.StatusCreated, toCopiedDTO([]projectcopy.Created{*created}))
	}
}

// makeCopyProjectsHandler 返回 POST /api/project-copies handler —— 「一次复制 N 个」。
//
// 这条路径不被 accessGuard 识别(见文件头),所以每个源项目都要在这里自己判 Operate。
// 不做「跳过没权限的那几条」:一次点击 = 一个事务,静默少一个副本比直接 403 更难发现,
// 而且勾中的这批通常是一个组里的全部项目,漏一个就意味着副本和源不同构。
func makeCopyProjectsHandler(svc *projectcopy.Service, acc *access.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "项目复制服务未初始化")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		var req copyBatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		ids := dedupeNonEmpty(req.ProjectIDs)
		if len(ids) == 0 {
			writeError(w, http.StatusBadRequest, "nothing_to_copy", "没有要复制的项目")
			return
		}
		if len(ids) > maxCopyBatch {
			writeError(w, http.StatusUnprocessableEntity, "too_many_to_copy", "一次最多复制 200 个项目,请分几批")
			return
		}
		if acc != nil {
			actor, ok := accessActorFromRequest(r)
			if !ok {
				writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
				return
			}
			for _, id := range ids {
				if err := acc.Can(r.Context(), actor, access.KindProject, id, access.ActOperate); err != nil {
					writeAccessError(w, err)
					return
				}
			}
			// 落进别的组 = 归属改动,和 PATCH /api/projects/{id} 的新侧同一条规则(要管得住那个组)。
			if req.TargetGroupID != nil && !requireGroupPlacement(w, r, acc, *req.TargetGroupID) {
				return
			}
		}
		ins := make([]projectcopy.Input, 0, len(ids))
		for _, id := range ids {
			ins = append(ins, projectcopy.Input{SourceID: id, Suffix: req.Suffix, GroupID: req.TargetGroupID})
		}
		made, err := svc.CopyMany(r.Context(), ins)
		if err != nil {
			writeCopyError(w, err)
			return
		}
		// 一批一条审计:逐条记录会让一次点击刷出几十行,查的人反而看不出这是同一件事。
		// 「batch」沿用批量端点的既有口径(见 buildenv.go 的 batch 审计)。
		// 副本各自叫什么、属于哪个组,由 projects 表本身说清楚。
		names := make([]string, 0, len(made))
		for _, c := range made {
			names = append(names, c.Name)
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionProjectCopyBatch,
			TargetType: audit.TargetProject,
			TargetID:   "batch",
			Detail: map[string]any{
				"sourceIds": ids,
				"copies":    names,
				"groupId":   made[0].GroupID,
			},
			IP: clientIP(r),
		})
		writeJSON(w, http.StatusCreated, toCopiedDTO(made))
	}
}

// dedupeNonEmpty 去掉空串与重复 ID:同一项目在一批里出现两次会让命名避让白转一圈,
// 且第二份副本没有任何用户意图支撑。
func dedupeNonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
