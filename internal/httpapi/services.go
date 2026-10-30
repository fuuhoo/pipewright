// 归属服务:项目上的纯展示 / 筛选属性,与资源分组(权限轴)正交。
//
// 读对所有登录用户开放 —— 它不带任何权限语义,看得见项目就看得见它的服务;
// 写是管理员动作:改名会改所有人看到的标签,建 / 删更是全局变更(见路由注册)。
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/servicecatalog"
)

// serviceDTO 是归属服务的契约形状。projectCount 只给管理页展示与删前提示。
type serviceDTO struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	ProjectCount int    `json:"projectCount"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
}

func toServiceDTO(s *servicecatalog.Service) serviceDTO {
	return serviceDTO{
		ID:           s.ID,
		Name:         s.Name,
		Description:  s.Description,
		ProjectCount: s.ProjectCount,
		CreatedAt:    s.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:    s.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// writeServiceCatalogError 把领域错误映射为契约错误码/状态码。
func writeServiceCatalogError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, servicecatalog.ErrNotFound):
		writeError(w, http.StatusNotFound, "service_not_found", "归属服务不存在")
	case errors.Is(err, servicecatalog.ErrDuplicateName):
		writeError(w, http.StatusConflict, "service_name_taken", "服务名已被占用")
	case errors.Is(err, servicecatalog.ErrEmptyName):
		writeError(w, http.StatusBadRequest, "invalid_service", "服务名不能为空")
	case errors.Is(err, servicecatalog.ErrInUse):
		writeError(w, http.StatusConflict, "service_in_use", "仍有项目归属该服务,请先移出")
	default:
		writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
	}
}

// makeListServicesHandler 返回 GET /api/services(按名字升序,含项目数)。
func makeListServicesHandler(sc *servicecatalog.Catalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if sc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "归属服务未初始化")
			return
		}
		list, err := sc.List(r.Context())
		if err != nil {
			writeServiceCatalogError(w, err)
			return
		}
		out := make([]serviceDTO, 0, len(list))
		for i := range list {
			out = append(out, toServiceDTO(&list[i]))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// makeCreateServiceHandler 返回 POST /api/services(管理员,见路由注册)。
func makeCreateServiceHandler(sc *servicecatalog.Catalog, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if sc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "归属服务未初始化")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<15)
		var req struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		s, err := sc.Create(r.Context(), servicecatalog.CreateInput{
			Name: req.Name, Description: req.Description,
		})
		if err != nil {
			writeServiceCatalogError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionServiceCreate,
			TargetType: audit.TargetService,
			TargetID:   s.ID,
			Detail:     map[string]any{"name": s.Name},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusCreated, toServiceDTO(s))
	}
}

// makeUpdateServiceHandler 返回 PATCH /api/services/{id}(管理员)。字段省略 = 不动。
func makeUpdateServiceHandler(sc *servicecatalog.Catalog, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if sc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "归属服务未初始化")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<15)
		var req struct {
			Name        *string `json:"name"`
			Description *string `json:"description"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		s, err := sc.Update(r.Context(), chi.URLParam(r, "id"), servicecatalog.UpdateInput{
			Name: req.Name, Description: req.Description,
		})
		if err != nil {
			writeServiceCatalogError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionServiceUpdate,
			TargetType: audit.TargetService,
			TargetID:   s.ID,
			Detail:     map[string]any{"name": s.Name},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusOK, toServiceDTO(s))
	}
}

// makeDeleteServiceHandler 返回 DELETE /api/services/{id}(管理员)。仍有项目挂着 → 409。
func makeDeleteServiceHandler(sc *servicecatalog.Catalog, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if sc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "归属服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		if err := sc.Delete(r.Context(), id); err != nil {
			writeServiceCatalogError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionServiceDelete,
			TargetType: audit.TargetService,
			TargetID:   id,
			IP:         clientIP(r),
		})
		w.WriteHeader(http.StatusNoContent)
	}
}
