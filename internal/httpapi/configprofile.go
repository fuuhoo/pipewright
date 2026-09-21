// Package httpapi — 配置资源管理端点(v6.2 §3.2 + 阶段 9)。
//
// 路由(均 admin-only,RequireAdmin + CSRF):
//   GET    /api/admin/config-profiles             → List(过滤 language/configType/enabled)
//   POST   /api/admin/config-profiles             → Create(管理员手填 content)
//   GET    /api/admin/config-profiles/{id}        → GetByID
//   PUT    /api/admin/config-profiles/{id}        → Update(builtin 行只允许改 description/enabled)
//   DELETE /api/admin/config-profiles/{id}        → Delete(builtin 不可删)
//   POST   /api/admin/config-profiles/upload      → multipart 上传(扩展名白名单)
//
// 普通用户端点(RequireUser):
//   GET    /api/config-profiles?language=java     → 已启用配置列表
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/huangchengsir/pipewright/internal/audit"
	"github.com/huangchengsir/pipewright/internal/auth"
	"github.com/huangchengsir/pipewright/internal/configprofile"
)

// configProfileDTO 是配置资源对外响应体(camelCase)。
type configProfileDTO struct {
	ID          string `json:"id"`
	Language    string `json:"language"`
	ConfigType  string `json:"configType"`
	Name        string `json:"name"`
	TargetPath  string `json:"targetPath"`
	FilePath    string `json:"filePath"`
	IsDefault   bool   `json:"isDefault"`
	IsBuiltin   bool   `json:"isBuiltin"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	CreatedBy   string `json:"createdBy"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

func toConfigProfileDTO(p *configprofile.ConfigProfile) configProfileDTO {
	if p == nil {
		return configProfileDTO{}
	}
	return configProfileDTO{
		ID:          p.ID,
		Language:    p.Language,
		ConfigType:  p.ConfigType,
		Name:        p.Name,
		TargetPath:  p.TargetPath,
		FilePath:    p.FilePath,
		IsDefault:   p.IsDefault,
		IsBuiltin:   p.IsBuiltin,
		Description: p.Description,
		Enabled:     p.Enabled,
		CreatedBy:   p.CreatedBy,
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   p.UpdatedAt,
	}
}

// makeListConfigProfilesHandler GET /api/admin/config-profiles。
func makeListConfigProfilesHandler(svc *configprofile.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "configprofile_unavailable", "配置资源服务未初始化")
			return
		}
		q := r.URL.Query()
		list, err := svc.List(configprofile.ListFilter{
			Language:        q.Get("language"),
			ConfigType:      q.Get("configType"),
			IncludeBuiltin:  true,
			IncludeDisabled: true,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "查询配置资源失败")
			return
		}
		out := make([]configProfileDTO, 0, len(list))
		for _, p := range list {
			out = append(out, toConfigProfileDTO(p))
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// makeListEnabledConfigProfilesHandler GET /api/config-profiles?language=java。
func makeListEnabledConfigProfilesHandler(svc *configprofile.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "configprofile_unavailable", "配置资源服务未初始化")
			return
		}
		q := r.URL.Query()
		list, err := svc.List(configprofile.ListFilter{
			Language:        q.Get("language"),
			IncludeBuiltin:  true,
			IncludeDisabled: false, // 普通用户只看启用的
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "查询配置资源失败")
			return
		}
		out := make([]configProfileDTO, 0, len(list))
		for _, p := range list {
			if !p.Enabled {
				continue
			}
			out = append(out, toConfigProfileDTO(p))
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// makeGetConfigProfileHandler GET /api/admin/config-profiles/{id}。
func makeGetConfigProfileHandler(svc *configprofile.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "configprofile_unavailable", "配置资源服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		out, err := svc.GetByID(id)
		if err != nil {
			writeConfigProfileError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toConfigProfileDTO(out))
	}
}

// makeCreateConfigProfileHandler POST /api/admin/config-profiles。
func makeCreateConfigProfileHandler(svc *configprofile.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "configprofile_unavailable", "配置资源服务未初始化")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // content 可能较大,放宽到 1MB
		var req configProfileReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		cp := &configprofile.ConfigProfile{
			Language:    req.Language,
			ConfigType:  req.ConfigType,
			Name:        req.Name,
			TargetPath:  req.TargetPath,
			Content:     req.Content,
			IsDefault:   req.IsDefault,
			Description: req.Description,
			Enabled:     req.Enabled,
		}
		if actor := actorFromRequest(r, ac); actor != "" && actor != auditActorFallback {
			cp.CreatedBy = actor
		}
		out, err := svc.Create(cp)
		if err != nil {
			writeConfigProfileError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionConfigProfileCreate,
			TargetType: audit.TargetConfigProfile,
			TargetID:   out.ID,
			Detail: map[string]any{
				"language": out.Language, "configType": out.ConfigType,
				"name": out.Name, "targetPath": out.TargetPath,
			},
			IP: clientIP(r),
		})
		writeJSON(w, http.StatusCreated, toConfigProfileDTO(out))
	}
}

type configProfileReq struct {
	Language    string `json:"language"`
	ConfigType  string `json:"configType"`
	Name        string `json:"name"`
	TargetPath  string `json:"targetPath"`
	Content     string `json:"content"`
	IsDefault   bool   `json:"isDefault"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
}

// makeUpdateConfigProfileHandler PUT /api/admin/config-profiles/{id}。
func makeUpdateConfigProfileHandler(svc *configprofile.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "configprofile_unavailable", "配置资源服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var req configProfileReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		cp := &configprofile.ConfigProfile{
			ID:          id,
			Language:    req.Language,
			ConfigType:  req.ConfigType,
			Name:        req.Name,
			TargetPath:  req.TargetPath,
			Content:     req.Content,
			IsDefault:   req.IsDefault,
			Description: req.Description,
			Enabled:     req.Enabled,
		}
		out, err := svc.Update(cp)
		if err != nil {
			writeConfigProfileError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionConfigProfileUpdate,
			TargetType: audit.TargetConfigProfile,
			TargetID:   out.ID,
			Detail: map[string]any{
				"language": out.Language, "configType": out.ConfigType,
				"name": out.Name,
			},
			IP: clientIP(r),
		})
		writeJSON(w, http.StatusOK, toConfigProfileDTO(out))
	}
}

// makeDeleteConfigProfileHandler DELETE /api/admin/config-profiles/{id}。
func makeDeleteConfigProfileHandler(svc *configprofile.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "configprofile_unavailable", "配置资源服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		if err := svc.Delete(id); err != nil {
			writeConfigProfileError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionConfigProfileDelete,
			TargetType: audit.TargetConfigProfile,
			TargetID:   id,
			IP:         clientIP(r),
		})
		w.WriteHeader(http.StatusNoContent)
	}
}

// makeUploadConfigProfileHandler POST /api/admin/config-profiles/upload。
// multipart/form-data:file (content) + language/configType/name/targetPath。
func makeUploadConfigProfileHandler(svc *configprofile.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "configprofile_unavailable", "配置资源服务未初始化")
			return
		}
		const maxUpload = 1 << 20 // 1MB
		r.Body = http.MaxBytesReader(w, r.Body, maxUpload+4096)
		if err := r.ParseMultipartForm(maxUpload); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "multipart 解析失败: "+err.Error())
			return
		}
		f, header, err := r.FormFile("file")
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "缺少 file 字段")
			return
		}
		defer func() { _ = f.Close() }()
		content, err := io.ReadAll(f)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "读上传文件失败: "+err.Error())
			return
		}
		in := &configprofile.UploadInput{
			Language:    r.FormValue("language"),
			ConfigType:  r.FormValue("configType"),
			Name:        r.FormValue("name"),
			TargetPath:  r.FormValue("targetPath"),
			Filename:    header.Filename,
			Content:     content,
			Description: r.FormValue("description"),
			IsDefault:   r.FormValue("isDefault") == "true",
		}
		if actor := actorFromRequest(r, ac); actor != "" && actor != auditActorFallback {
			in.CreatedBy = actor
		}
		out, err := svc.Upload(in)
		if err != nil {
			writeConfigProfileError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionConfigProfileUpload,
			TargetType: audit.TargetConfigProfile,
			TargetID:   out.ID,
			Detail: map[string]any{
				"language":   out.Language,
				"configType": out.ConfigType,
				"name":       out.Name,
				"filename":   header.Filename,
				"size":       len(content),
			},
			IP: clientIP(r),
		})
		writeJSON(w, http.StatusCreated, toConfigProfileDTO(out))
	}
}

// writeConfigProfileError 把领域错误映射到 HTTP 状态码。
func writeConfigProfileError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, configprofile.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, configprofile.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, configprofile.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "invalid_input", err.Error())
	case errors.Is(err, configprofile.ErrBuiltinReadonly):
		writeError(w, http.StatusForbidden, "builtin_readonly", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal", fmt.Sprintf("内部错误: %v", err))
	}
}
