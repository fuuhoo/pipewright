// Package httpapi — 构建环境管理端点(v6.2 §3.1 + 阶段 9)。
//
// 路由:
//   GET    /api/admin/build-envs                   → List(过滤)
//   POST   /api/admin/build-envs                   → Create
//   GET    /api/admin/build-envs/{id}              → GetByID
//   PUT    /api/admin/build-envs/{id}              → Update
//   DELETE /api/admin/build-envs/{id}              → Delete
//   POST   /api/admin/build-envs/{id}/toggle      → SetEnabled(P0 #4 三态)
//   POST   /api/admin/build-envs/{id}/check       → 手动镜像检查
//   POST   /api/admin/build-envs/{id}/pull        → 手动 pull 镜像
//   POST   /api/admin/build-envs/check-all        → 一键检查全部
//   GET    /api/build-envs/languages              → 已启用环境去重语言列表(普通用户可访问)
//
// 所有写端点套 RequireAdmin + CSRF;所有 admin 端点走 RequireAdmin。
// 普通用户端点走 RequireUser(阶段 9 接入 router)。
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/huangchengsir/pipewright/internal/audit"
	"github.com/huangchengsir/pipewright/internal/auth"
	"github.com/huangchengsir/pipewright/internal/buildenv"
)

// buildEnvDTO 是构建环境对外响应体(camelCase;不含任何密文)。
type buildEnvDTO struct {
	ID               string  `json:"id"`
	Language         string  `json:"language"`
	Version          string  `json:"version"`
	DisplayName      string  `json:"displayName"`
	Description      string  `json:"description"`
	SourceType       string  `json:"sourceType"`
	Image            string  `json:"image"`
	CredentialID     string  `json:"credentialId"`
	ImageCheckStatus string  `json:"imageCheckStatus"`
	ImageCheckError  string  `json:"imageCheckError"`
	ImageCheckedAt   *string `json:"imageCheckedAt"`
	Enabled          bool    `json:"enabled"`
	SortOrder        int     `json:"sortOrder"`
	CreatedBy        string  `json:"createdBy"`
	CreatedAt        string  `json:"createdAt"`
	UpdatedAt        string  `json:"updatedAt"`
}

func toBuildEnvDTO(e *buildenv.BuildEnv) buildEnvDTO {
	if e == nil {
		return buildEnvDTO{}
	}
	var checkedAt *string
	if e.ImageCheckedAt != nil {
		s := e.ImageCheckedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
		checkedAt = &s
	}
	return buildEnvDTO{
		ID:               e.ID,
		Language:         e.Language,
		Version:          e.Version,
		DisplayName:      e.DisplayName,
		Description:      e.Description,
		SourceType:       e.SourceType,
		Image:            e.Image,
		CredentialID:     e.CredentialID,
		ImageCheckStatus: e.ImageCheckStatus,
		ImageCheckError:  e.ImageCheckError,
		ImageCheckedAt:   checkedAt,
		Enabled:          e.Enabled,
		SortOrder:        e.SortOrder,
		CreatedBy:        e.CreatedBy,
		CreatedAt:        e.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:        e.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
}

// makeListBuildEnvsHandler GET /api/admin/build-envs。
// query: language / sourceType / includeDisabled。
func makeListBuildEnvsHandler(svc *buildenv.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "buildenv_unavailable", "构建环境服务未初始化")
			return
		}
		q := r.URL.Query()
		includeDisabled := q.Get("includeDisabled") == "1" || q.Get("includeDisabled") == "true"
		list, err := svc.List(buildenv.ListFilter{
			Language:        q.Get("language"),
			SourceType:      q.Get("sourceType"),
			IncludeDisabled: includeDisabled,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "查询构建环境失败")
			return
		}
		out := make([]buildEnvDTO, 0, len(list))
		for _, e := range list {
			out = append(out, toBuildEnvDTO(e))
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// makeListBuildEnvLanguagesHandler GET /api/build-envs/languages(普通用户可访问)。
func makeListBuildEnvLanguagesHandler(svc *buildenv.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "buildenv_unavailable", "构建环境服务未初始化")
			return
		}
		langs, err := svc.ListEnabledLanguages(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "查询语言列表失败")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"languages": langs})
	}
}

// makeCreateBuildEnvHandler POST /api/admin/build-envs。
func makeCreateBuildEnvHandler(svc *buildenv.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "buildenv_unavailable", "构建环境服务未初始化")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		var req buildEnvCreateReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		env := &buildenv.BuildEnv{
			Language:     req.Language,
			Version:      req.Version,
			DisplayName:  req.DisplayName,
			Description:  req.Description,
			SourceType:   req.SourceType,
			Image:        req.Image,
			CredentialID: req.CredentialID,
			SortOrder:    req.SortOrder,
			Enabled:      req.Enabled,
		}
		if actor := actorFromRequest(r, ac); actor != "" && actor != auditActorFallback {
			env.CreatedBy = actor
		}
		out, err := svc.Create(env)
		if err != nil {
			writeBuildEnvError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionBuildEnvCreate,
			TargetType: audit.TargetBuildEnv,
			TargetID:   out.ID,
			Detail: map[string]any{
				"language": out.Language, "version": out.Version,
				"image": out.Image, "enabled": out.Enabled,
			},
			IP: clientIP(r),
		})
		writeJSON(w, http.StatusCreated, toBuildEnvDTO(out))
	}
}

type buildEnvCreateReq struct {
	Language     string `json:"language"`
	Version      string `json:"version"`
	DisplayName  string `json:"displayName"`
	Description  string `json:"description"`
	SourceType   string `json:"sourceType"`
	Image        string `json:"image"`
	CredentialID string `json:"credentialId"`
	SortOrder    int    `json:"sortOrder"`
	Enabled      bool   `json:"enabled"`
}

// makeUpdateBuildEnvHandler PUT /api/admin/build-envs/{id}。
func makeUpdateBuildEnvHandler(svc *buildenv.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "buildenv_unavailable", "构建环境服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		var req buildEnvCreateReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		env := &buildenv.BuildEnv{
			ID:           id,
			Language:     req.Language,
			Version:      req.Version,
			DisplayName:  req.DisplayName,
			Description:  req.Description,
			SourceType:   req.SourceType,
			Image:        req.Image,
			CredentialID: req.CredentialID,
			SortOrder:    req.SortOrder,
			Enabled:      req.Enabled,
		}
		out, err := svc.Update(env)
		if err != nil {
			writeBuildEnvError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionBuildEnvUpdate,
			TargetType: audit.TargetBuildEnv,
			TargetID:   out.ID,
			Detail: map[string]any{
				"language": out.Language, "version": out.Version,
				"image": out.Image, "enabled": out.Enabled,
			},
			IP: clientIP(r),
		})
		writeJSON(w, http.StatusOK, toBuildEnvDTO(out))
	}
}

// makeDeleteBuildEnvHandler DELETE /api/admin/build-envs/{id}。
func makeDeleteBuildEnvHandler(svc *buildenv.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "buildenv_unavailable", "构建环境服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		if err := svc.Delete(id); err != nil {
			writeBuildEnvError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionBuildEnvDelete,
			TargetType: audit.TargetBuildEnv,
			TargetID:   id,
			IP:         clientIP(r),
		})
		w.WriteHeader(http.StatusNoContent)
	}
}

// makeGetBuildEnvHandler GET /api/admin/build-envs/{id}。
func makeGetBuildEnvHandler(svc *buildenv.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "buildenv_unavailable", "构建环境服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		out, err := svc.GetByID(id)
		if err != nil {
			writeBuildEnvError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toBuildEnvDTO(out))
	}
}

// makeToggleBuildEnvHandler POST /api/admin/build-envs/{id}/toggle。
// body: {"enabled": true|false};响应:{"enabled":bool,"status":string}
func makeToggleBuildEnvHandler(svc *buildenv.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "buildenv_unavailable", "构建环境服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		if err := svc.SetEnabled(id, req.Enabled); err != nil {
			writeBuildEnvError(w, err)
			return
		}
		// 回读用于响应。
		out, err := svc.GetByID(id)
		if err != nil {
			writeBuildEnvError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionBuildEnvToggle,
			TargetType: audit.TargetBuildEnv,
			TargetID:   id,
			Detail:     map[string]any{"enabled": out.Enabled},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled": out.Enabled,
			"status":  out.ImageCheckStatus,
		})
	}
}

// makeCheckBuildEnvHandler POST /api/admin/build-envs/{id}/check。
func makeCheckBuildEnvHandler(svc *buildenv.Service, c *buildenv.Checker, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c == nil {
			writeError(w, http.StatusServiceUnavailable, "checker_unavailable", "镜像检查器未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		// 加载 env → 调 Check。
		ctx := r.Context()
		env, err := svc.GetByID(id)
		if err != nil {
			writeBuildEnvError(w, err)
			return
		}
		res, err := c.Check(ctx, env)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				writeError(w, 499, "client_closed", "客户端断开")
				return
			}
			writeError(w, http.StatusInternalServerError, "check_failed", "检查失败: "+err.Error())
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionBuildEnvCheck,
			TargetType: audit.TargetBuildEnv,
			TargetID:   id,
			Detail:     map[string]any{"status": res.Status, "error": res.Error},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusOK, res)
	}
}

// makePullBuildEnvHandler POST /api/admin/build-envs/{id}/pull。
// 异步:立即返回 202 + status=checking,前端轮询列表直到 available/unavailable。
func makePullBuildEnvHandler(c *buildenv.Checker, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c == nil {
			writeError(w, http.StatusServiceUnavailable, "checker_unavailable", "镜像检查器未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		res, err := c.ManualPull(r.Context(), id)
		if err != nil {
			if errors.Is(err, buildenv.ErrConflict) {
				writeError(w, http.StatusConflict, "pull_in_progress", "该镜像正在拉取中,请等待完成")
				return
			}
			writeError(w, http.StatusInternalServerError, "pull_failed", "拉取失败: "+err.Error())
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionBuildEnvPull,
			TargetType: audit.TargetBuildEnv,
			TargetID:   id,
			Detail:     map[string]any{"status": res.Status, "queued": true},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusAccepted, res)
	}
}

// makeCheckAllBuildEnvsHandler POST /api/admin/build-envs/check-all。
func makeCheckAllBuildEnvsHandler(c *buildenv.Checker, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c == nil {
			writeError(w, http.StatusServiceUnavailable, "checker_unavailable", "镜像检查器未初始化")
			return
		}
		ok, total, err := c.CheckAll(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "check_all_failed", "一键检查失败: "+err.Error())
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionBuildEnvCheck,
			TargetType: audit.TargetBuildEnv,
			TargetID:   "all",
			Detail:     map[string]any{"ok": ok, "total": total},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "total": total})
	}
}

// writeBuildEnvError 把领域错误映射到 HTTP 状态码。
// 注意:SetEnabled 等入口返回 *buildenv.ValidationError(带 Code),不是 Err* sentinel,
// 必须先匹配 ValidationError 再匹配 sentinel,否则会落 default 500。
func writeBuildEnvError(w http.ResponseWriter, err error) {
	var ve *buildenv.ValidationError
	if errors.As(err, &ve) {
		switch ve.Code {
		case "ENV_NOT_FOUND":
			writeError(w, http.StatusNotFound, ve.Code, ve.Message)
		case "ENV_DISABLED", "IMAGE_UNAVAILABLE", "IMAGE_NOT_CHECKED":
			// 三态校验(P0 #4)拒绝:409 Conflict,消息已人读化。
			writeError(w, http.StatusConflict, ve.Code, ve.Message)
		default:
			writeError(w, http.StatusBadRequest, ve.Code, ve.Message)
		}
		return
	}
	switch {
	case errors.Is(err, buildenv.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, buildenv.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, buildenv.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "invalid_input", err.Error())
	case errors.Is(err, buildenv.ErrImageUnavail):
		writeError(w, http.StatusConflict, "image_unavailable", err.Error())
	case errors.Is(err, buildenv.ErrImageNotChecked):
		writeError(w, http.StatusConflict, "image_not_checked", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
	}
}
