// Package httpapi — 配置资源管理端点(v6.2 §3.2 + 阶段 9)。
//
// 路由(均 admin-only,RequireAdmin + CSRF):
//
//	GET    /api/admin/config-profiles             → List(过滤 language/configType/enabled)
//	POST   /api/admin/config-profiles             → Create(管理员手填 content)
//	GET    /api/admin/config-profiles/{id}        → GetByID(详情多带 content + contentSource)
//	PUT    /api/admin/config-profiles/{id}        → Update(builtin 行只允许改 description/enabled;content 留空=不动文件)
//	DELETE /api/admin/config-profiles/{id}        → Delete(builtin 不可删)
//	POST   /api/admin/config-profiles/upload      → multipart 上传(新建,扩展名白名单)
//	POST   /api/admin/config-profiles/{id}/upload → multipart 重新上传(覆盖已有行的文件)
//
// 普通用户端点(RequireUser):
//
//	GET    /api/config-profiles?language=java     → 已启用配置列表
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/configprofile"
	"github.com/go-chi/chi/v5"
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

// configProfileDetailDTO 是详情响应体:列表字段 + 文件正文。
// contentSource = "disk"(权威副本)/ "db"(磁盘文件读不到,回退到 DB 冗余快照)。
type configProfileDetailDTO struct {
	configProfileDTO
	Content       string `json:"content"`
	ContentSource string `json:"contentSource"`
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
// 普通用户视角:只回启用条目,且不回宿主机路径(file_path 是宿主目录,选配置用不到)。
func makeListEnabledConfigProfilesHandler(svc *configprofile.Service) http.HandlerFunc {
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
			dto := toConfigProfileDTO(p)
			dto.FilePath = ""
			out = append(out, dto)
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// makeGetConfigProfileHandler GET /api/admin/config-profiles/{id}。
// 详情比列表多带文件正文(content 只在 admin 详情暴露;列表保持精简)。
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
		content, fromDisk := svc.ContentForView(out)
		source := "db"
		if fromDisk {
			source = "disk"
		}
		writeJSON(w, http.StatusOK, configProfileDetailDTO{
			configProfileDTO: toConfigProfileDTO(out),
			Content:          content,
			ContentSource:    source,
		})
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

// readConfigProfileFileForm 解析 multipart 并取出 file 字段(新建上传 / 重新上传共用)。
// 已写错误响应时返回 ok=false。
func readConfigProfileFileForm(w http.ResponseWriter, r *http.Request) (string, []byte, bool) {
	const maxUpload = 1 << 20 // 1MB
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload+4096)
	if err := r.ParseMultipartForm(maxUpload); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "multipart 解析失败: "+err.Error())
		return "", nil, false
	}
	f, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "缺少 file 字段")
		return "", nil, false
	}
	defer func() { _ = f.Close() }()
	content, err := io.ReadAll(f)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "读上传文件失败: "+err.Error())
		return "", nil, false
	}
	return header.Filename, content, true
}

// makeUploadConfigProfileHandler POST /api/admin/config-profiles/upload。
// multipart/form-data:file (content) + language/configType/name/targetPath。
func makeUploadConfigProfileHandler(svc *configprofile.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "configprofile_unavailable", "配置资源服务未初始化")
			return
		}
		filename, content, ok := readConfigProfileFileForm(w, r)
		if !ok {
			return
		}
		in := &configprofile.UploadInput{
			Language:    r.FormValue("language"),
			ConfigType:  r.FormValue("configType"),
			Name:        r.FormValue("name"),
			TargetPath:  r.FormValue("targetPath"),
			Filename:    filename,
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
				"filename":   filename,
				"size":       len(content),
			},
			IP: clientIP(r),
		})
		writeJSON(w, http.StatusCreated, toConfigProfileDTO(out))
	}
}

// makeReplaceConfigProfileFileHandler POST /api/admin/config-profiles/{id}/upload。
// multipart/form-data:file(新正文)+ 可选 targetPath;language/configType/name 等保持行内原值。
// 内置行(is_builtin=1)走不到这里:403 builtin_readonly。
func makeReplaceConfigProfileFileHandler(svc *configprofile.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "configprofile_unavailable", "配置资源服务未初始化")
			return
		}
		filename, content, ok := readConfigProfileFileForm(w, r)
		if !ok {
			return
		}
		out, err := svc.ReplaceFile(&configprofile.ReplaceFileInput{
			ID:         chi.URLParam(r, "id"),
			Filename:   filename,
			Content:    content,
			TargetPath: r.FormValue("targetPath"),
		})
		if err != nil {
			writeConfigProfileError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionConfigProfileReplace,
			TargetType: audit.TargetConfigProfile,
			TargetID:   out.ID,
			Detail: map[string]any{
				"language":   out.Language,
				"configType": out.ConfigType,
				"name":       out.Name,
				"filename":   filename,
				"targetPath": out.TargetPath,
				"size":       len(content),
			},
			IP: clientIP(r),
		})
		writeJSON(w, http.StatusOK, toConfigProfileDTO(out))
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
