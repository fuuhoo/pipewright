package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/fuuhoo/pipewright/internal/access"
	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/project"
	"github.com/go-chi/chi/v5"
)

// projectDTO 是项目对外响应体(冻结契约;camelCase;无明文/无密文)。
// lastRunStatus / targetServers 本期为占位(null / []),数据由后续 story 填。
type projectDTO struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	RepoURL         string `json:"repoUrl"`
	DefaultBranch   string `json:"defaultBranch"`
	CredentialID    string `json:"credentialId"`
	CredentialName  string `json:"credentialName"`
	PacEnabled      bool   `json:"pacEnabled"`
	PRStatusEnabled bool   `json:"prStatusEnabled"`
	// GroupID 是所属资源分组;'' = 未归组(全员可见可操作)。组名由前端用 /api/groups 映射,
	// 项目可见即其分组可见,故不必在服务端做名字 join。
	GroupID       string   `json:"groupId"`
	LastRunStatus *string  `json:"lastRunStatus"`
	TargetServers []string `json:"targetServers"`
	CreatedAt     string   `json:"createdAt"`
	UpdatedAt     string   `json:"updatedAt"`
}

// toProjectDTO 把领域 Project 转为契约 DTO。
func toProjectDTO(p *project.Project) projectDTO {
	return projectDTO{
		ID:              p.ID,
		Name:            p.Name,
		RepoURL:         p.RepoURL,
		DefaultBranch:   p.DefaultBranch,
		CredentialID:    p.CredentialID,
		CredentialName:  p.CredentialName,
		PacEnabled:      p.PacEnabled,
		PRStatusEnabled: p.PRStatusEnabled,
		GroupID:         p.GroupID,
		LastRunStatus:   nil,        // 本期占位:尚无运行
		TargetServers:   []string{}, // 本期占位:空集合
		CreatedAt:       p.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:       p.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// writeProjectError 把领域错误映射为契约错误码/状态码;绝不回显明文/凭据/栈。
func writeProjectError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, project.ErrVaultUnconfigured):
		writeError(w, http.StatusUnprocessableEntity, "vault_unconfigured", "保险库未配置 master key,无法校验仓库连通")
	case errors.Is(err, project.ErrCredentialError),
		errors.Is(err, project.ErrCredentialNotFound):
		writeError(w, http.StatusUnprocessableEntity, "credential_error", "凭据无效或无权限,无法访问该仓库")
	case errors.Is(err, project.ErrRepoUnreachable):
		writeError(w, http.StatusUnprocessableEntity, "repo_unreachable", "仓库地址不可达,请检查地址")
	case errors.Is(err, project.ErrNotFound):
		writeError(w, http.StatusNotFound, "project_not_found", "项目不存在")
	case errors.Is(err, project.ErrProjectHasActiveRuns):
		writeError(w, http.StatusConflict, "project_has_active_runs", "该项目有进行中的运行,无法删除")
	case errors.Is(err, project.ErrEmptyName):
		writeError(w, http.StatusBadRequest, "invalid_project", "项目名称不能为空")
	case errors.Is(err, project.ErrEmptyRepoURL):
		writeError(w, http.StatusBadRequest, "invalid_project", "仓库地址不能为空")
	case errors.Is(err, project.ErrCredentialWithoutRepo):
		writeError(w, http.StatusBadRequest, "invalid_project", "未填仓库地址,无需选择仓库凭据")
	case errors.Is(err, project.ErrRepoRequired):
		writeError(w, http.StatusBadRequest, "invalid_project", "该项目未绑定仓库,此项设置需要仓库")
	case errors.Is(err, project.ErrEmptyCredentialID):
		writeError(w, http.StatusBadRequest, "invalid_project", "请选择仓库凭据")
	default:
		// 内部错误:不泄漏细节。
		writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
	}
}

// atoiDefault 解析十进制整数;空串/非法值返回 def。
func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// makeListProjectsHandler 返回 GET /api/projects handler。
// 列表按分组可见性收敛:普通用户只看到未归组 + 公开组 + 自己所在私有组的项目。
func makeListProjectsHandler(svc project.Service, acc *access.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "项目服务未初始化")
			return
		}
		visible, err := visibleGroups(r, acc)
		if err != nil {
			writeAccessError(w, err)
			return
		}
		// 可选分页参数(默认第 1 页);响应保持冻结契约:仍是 projectDTO 数组。
		page := atoiDefault(r.URL.Query().Get("page"), 1)
		pageSize := atoiDefault(r.URL.Query().Get("pageSize"), project.DefaultPageSize)
		res, err := svc.ListPaged(r.Context(), page, pageSize, visible)
		if err != nil {
			writeProjectError(w, err)
			return
		}
		out := make([]projectDTO, 0, len(res.Items))
		for i := range res.Items {
			out = append(out, toProjectDTO(&res.Items[i]))
		}
		// 分页元信息经响应头暴露(不破坏数组契约;前端可选读取)。
		w.Header().Set("X-Total-Count", strconv.Itoa(res.Total))
		w.Header().Set("X-Page", strconv.Itoa(res.Page))
		w.Header().Set("X-Page-Size", strconv.Itoa(res.PageSize))
		writeJSON(w, http.StatusOK, out)
	}
}

// requireGroupPlacement 校验「把项目放进/移出 groupID」这一动作。
//
// 归组是归属改动而不是普通编辑,故要求 ActManage(管理员或组长)——组成员能跑项目,
// 但不能决定项目属于哪个组。移出(空串)只需管得住原组,由 caller 的旧侧判定把关。
func requireGroupPlacement(w http.ResponseWriter, r *http.Request, acc *access.Service, groupID string) bool {
	if groupID == access.Ungrouped {
		return true
	}
	actor, ok := accessActorFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
		return false
	}
	if err := acc.CanGroup(r.Context(), actor, groupID, access.ActManage); err != nil {
		// 这里的 groupID 来自请求体:指向不存在的组是入参错误(422),不同于
		// 「资源挂在被人删掉的组上」那种越权收敛(403 group_missing)。
		if errors.Is(err, access.ErrGroupNotFound) {
			writeError(w, http.StatusUnprocessableEntity, "group_not_found", "引用的分组不存在")
			return false
		}
		writeAccessError(w, err)
		return false
	}
	return true
}

// makeCreateProjectHandler 返回 POST /api/projects handler。
// 创建成功后追加 project_create 审计(detail 仅项目元数据)。
func makeCreateProjectHandler(svc project.Service, acc *access.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "项目服务未初始化")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		var req struct {
			Name          string `json:"name"`
			RepoURL       string `json:"repoUrl"`
			CredentialID  string `json:"credentialId"`
			DefaultBranch string `json:"defaultBranch"`
			GroupID       string `json:"groupId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		if acc != nil && !requireGroupPlacement(w, r, acc, req.GroupID) {
			return
		}
		p, err := svc.Create(r.Context(), project.CreateInput{
			Name:          req.Name,
			RepoURL:       req.RepoURL,
			CredentialID:  req.CredentialID,
			DefaultBranch: req.DefaultBranch,
			GroupID:       req.GroupID,
		})
		if err != nil {
			writeProjectError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionProjectCreate,
			TargetType: audit.TargetProject,
			TargetID:   p.ID,
			Detail:     map[string]any{"name": p.Name, "repoUrl": p.RepoURL, "defaultBranch": p.DefaultBranch, "groupId": p.GroupID},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusCreated, toProjectDTO(p))
	}
}

// makeUpdateProjectHandler 返回 PATCH /api/projects/{id} handler。
// 更新成功后追加 project_update 审计;改归属时额外追加 project_reassign(记录新旧组)。
func makeUpdateProjectHandler(svc project.Service, acc *access.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "项目服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		var req struct {
			Name            *string `json:"name"`
			DefaultBranch   *string `json:"defaultBranch"`
			CredentialID    *string `json:"credentialId"`
			RepoURL         *string `json:"repoUrl"`
			PacEnabled      *bool   `json:"pacEnabled"`
			PRStatusEnabled *bool   `json:"prStatusEnabled"`
			GroupID         *string `json:"groupId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		// 改归属要管得住两侧:旧组(把它甩出去)与新组(把它接进来)。
		// 中间件对本请求只判到 ActOperate,故这里补 ActManage 这一档。
		var fromGroupID string
		if req.GroupID != nil {
			if acc == nil {
				writeError(w, http.StatusServiceUnavailable, "internal", "权限服务未初始化")
				return
			}
			old, err := svc.Get(r.Context(), id)
			if err != nil {
				writeProjectError(w, err)
				return
			}
			actor, ok := accessActorFromRequest(r)
			if !ok {
				writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
				return
			}
			if err := acc.Can(r.Context(), actor, access.KindProject, id, access.ActManage); err != nil {
				writeAccessError(w, err)
				return
			}
			if !requireGroupPlacement(w, r, acc, *req.GroupID) {
				return
			}
			fromGroupID = old.GroupID
		}
		p, err := svc.Update(r.Context(), id, project.UpdateInput{
			Name:            req.Name,
			DefaultBranch:   req.DefaultBranch,
			CredentialID:    req.CredentialID,
			RepoURL:         req.RepoURL,
			PacEnabled:      req.PacEnabled,
			PRStatusEnabled: req.PRStatusEnabled,
			GroupID:         req.GroupID,
		})
		if err != nil {
			writeProjectError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionProjectUpdate,
			TargetType: audit.TargetProject,
			TargetID:   p.ID,
			Detail:     map[string]any{"name": p.Name, "repoUrl": p.RepoURL, "defaultBranch": p.DefaultBranch},
			IP:         clientIP(r),
		})
		if req.GroupID != nil {
			recordAuditFromRequest(r, aud, ac, audit.Entry{
				Action:     audit.ActionProjectReassign,
				TargetType: audit.TargetProject,
				TargetID:   p.ID,
				Detail:     map[string]any{"from": fromGroupID, "to": p.GroupID},
				IP:         clientIP(r),
			})
		}
		writeJSON(w, http.StatusOK, toProjectDTO(p))
	}
}

// makeDeleteProjectHandler 返回 DELETE /api/projects/{id} handler。
// 删除成功后追加 project_delete 审计。
func makeDeleteProjectHandler(svc project.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "项目服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		if err := svc.Delete(r.Context(), id); err != nil {
			writeProjectError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionProjectDelete,
			TargetType: audit.TargetProject,
			TargetID:   id,
			IP:         clientIP(r),
		})
		w.WriteHeader(http.StatusNoContent)
	}
}

// makeTestCloneHandler 返回 POST /api/projects/test-clone handler(不落库)。
func makeTestCloneHandler(svc project.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "项目服务未初始化")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		var req struct {
			RepoURL      string `json:"repoUrl"`
			CredentialID string `json:"credentialId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		res, err := svc.TestClone(r.Context(), req.RepoURL, req.CredentialID)
		if err != nil {
			writeProjectError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":            true,
			"defaultBranch": res.DefaultBranch,
			// 空列表保证为 [] 而非 null,前端可安全迭代。
			"branches": orEmptySlice(res.Branches),
			"tags":     orEmptySlice(res.Tags),
		})
	}
}
