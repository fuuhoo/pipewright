// Package httpapi —— 角色管理端点(P4 可配置角色)。
//
// 路由(均挂在 /api/admin 的 RequireAdmin 子树里,写方法吃 CSRF):
//
//	GET    /api/admin/roles          → 内置模板 + 自定义角色(带点集与账号数)
//	GET    /api/admin/roles/points   → 功能点字典(编辑器渲染勾选框)
//	POST   /api/admin/roles          → 建自定义角色
//	GET    /api/admin/roles/{id}     → 单个角色
//	PATCH  /api/admin/roles/{id}     → 改名 / 改描述 / 改点集
//	DELETE /api/admin/roles/{id}     → 删自定义角色
//	POST   /api/admin/roles/{id}/copy→ 以某角色为模板复制成新角色
//
// 端点自己不判「这个角色能做什么」:校验全在 internal/role 的写路径上(内置只读、
// settings.access 不许分配、仍被使用不许删),这里只把领域错误翻译成状态码。
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/fuuhoo/pipewright/internal/access"
	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/role"
	"github.com/go-chi/chi/v5"
)

// roleDTO 是角色对外响应体(camelCase)。Builtin 决定前端能否给出编辑/删除入口。
type roleDTO struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	BaseRole    string   `json:"baseRole"`
	Builtin     bool     `json:"builtin"`
	Perms       []string `json:"perms"`
	// UserCount 是当前挂在这个角色上的账号数(删除挡门与列表提示)。
	UserCount int    `json:"userCount"`
	CreatedBy string `json:"createdBy"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

func toRoleDTO(r *role.Role) roleDTO {
	if r == nil {
		return roleDTO{}
	}
	return roleDTO{
		ID: r.ID, Name: r.Name, Description: r.Description, BaseRole: r.BaseRole,
		Builtin: r.Builtin, Perms: r.Perms, UserCount: r.UserCount, CreatedBy: r.CreatedBy,
		CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: r.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// permPointDTO 是功能点字典的一项。Kind 是资源类别(project / run / server /
// kube_cluster / platform),Act 是需要的档位(view / operate)。
// BuiltinOnly 标出 settings.access:编辑器把它渲染成禁用项,而不是让人勾上以后才吃到 422。
type permPointDTO struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Act         string `json:"act"`
	BuiltinOnly bool   `json:"builtinOnly"`
}

// makeListRolesHandler GET /api/admin/roles。
func makeListRolesHandler(svc *role.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "roles_unavailable", "角色服务未初始化")
			return
		}
		roles, err := svc.List(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "查询角色列表失败")
			return
		}
		out := make([]roleDTO, 0, len(roles))
		for i := range roles {
			out = append(out, toRoleDTO(&roles[i]))
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// makeRolePointsHandler GET /api/admin/roles/points。字典在代码里(access/perms.go),
// 不落库,所以这里无依赖、纯读。
func makeRolePointsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dict := access.Perms()
		out := make([]permPointDTO, 0, len(dict))
		for _, p := range dict {
			out = append(out, permPointDTO{
				ID: p.ID, Kind: string(p.Kind), Act: p.Act.String(),
				BuiltinOnly: p.ID == access.PermSettingsAccess,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// makeGetRoleHandler GET /api/admin/roles/{id}。
func makeGetRoleHandler(svc *role.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "roles_unavailable", "角色服务未初始化")
			return
		}
		ro, err := svc.Get(r.Context(), chi.URLParam(r, "id"))
		if err != nil {
			writeRoleError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toRoleDTO(ro))
	}
}

// makeCreateRoleHandler POST /api/admin/roles。body:{name, description, baseRole, perms}。
func makeCreateRoleHandler(svc *role.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "roles_unavailable", "角色服务未初始化")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		var req struct {
			Name        string   `json:"name"`
			Description string   `json:"description"`
			BaseRole    string   `json:"baseRole"`
			Perms       []string `json:"perms"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		ro, err := svc.Create(r.Context(), role.CreateInput{
			Name: req.Name, Description: req.Description, BaseRole: req.BaseRole,
			Perms: req.Perms, CreatedBy: sessionUserID(r),
		})
		if err != nil {
			writeRoleError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionRoleCreate,
			TargetType: audit.TargetRole,
			TargetID:   ro.ID,
			Detail:     map[string]any{"name": ro.Name, "perms": ro.Perms},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusCreated, toRoleDTO(ro))
	}
}

// makePatchRoleHandler PATCH /api/admin/roles/{id}。
// body 字段缺省表示不动;perms 传空数组是明确的「一个入口都不给」,与缺省不同,所以用指针。
func makePatchRoleHandler(svc *role.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "roles_unavailable", "角色服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		var req struct {
			Name        *string   `json:"name"`
			Description *string   `json:"description"`
			Perms       *[]string `json:"perms"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		// 改名前的名字进审计:改完之后按 id 已经查不到旧名了。
		before, err := svc.Get(r.Context(), id)
		if err != nil {
			writeRoleError(w, err)
			return
		}
		ro, err := svc.Update(r.Context(), id, role.UpdateInput{
			Name: req.Name, Description: req.Description, Perms: req.Perms,
		})
		if err != nil {
			writeRoleError(w, err)
			return
		}
		detail := map[string]any{"name": ro.Name, "perms": ro.Perms}
		if before.Name != ro.Name {
			detail["previousName"] = before.Name
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionRoleUpdate,
			TargetType: audit.TargetRole,
			TargetID:   ro.ID,
			Detail:     detail,
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusOK, toRoleDTO(ro))
	}
}

// makeDeleteRoleHandler DELETE /api/admin/roles/{id}。
func makeDeleteRoleHandler(svc *role.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "roles_unavailable", "角色服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		before, err := svc.Get(r.Context(), id)
		if err != nil {
			writeRoleError(w, err)
			return
		}
		if err := svc.Delete(r.Context(), id); err != nil {
			writeRoleError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionRoleDelete,
			TargetType: audit.TargetRole,
			TargetID:   id,
			Detail:     map[string]any{"name": before.Name, "perms": before.Perms},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
	}
}

// makeCopyRoleHandler POST /api/admin/roles/{id}/copy。body:{name}。
// 源角色的 settings.access 会被剥掉(role.Copy),所以复制出来的永远是「进不了设置」的角色。
func makeCopyRoleHandler(svc *role.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "roles_unavailable", "角色服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		ro, err := svc.Copy(r.Context(), id, req.Name, sessionUserID(r))
		if err != nil {
			writeRoleError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionRoleCopy,
			TargetType: audit.TargetRole,
			TargetID:   ro.ID,
			Detail:     map[string]any{"name": ro.Name, "source": id, "perms": ro.Perms},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusCreated, toRoleDTO(ro))
	}
}

// writeRoleError 把角色领域错误映射到状态码。
// 422 给「点集语义不合法」(未知点 / settings.access):请求体格式没错,是内容越界;
// 409 给「与既有数据冲突」(内置只读 / 重名 / 仍被使用)。
func writeRoleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, role.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, role.ErrBuiltin):
		writeError(w, http.StatusConflict, "builtin_role", err.Error())
	case errors.Is(err, role.ErrDuplicateName):
		writeError(w, http.StatusConflict, "duplicate_name", err.Error())
	case errors.Is(err, role.ErrInUse):
		writeError(w, http.StatusConflict, "role_in_use", err.Error())
	case errors.Is(err, role.ErrSettingsPoint):
		writeError(w, http.StatusUnprocessableEntity, "settings_perm_not_assignable", err.Error())
	case errors.Is(err, role.ErrUnknownPoint):
		writeError(w, http.StatusUnprocessableEntity, "unknown_perm", err.Error())
	case errors.Is(err, role.ErrValidation):
		writeError(w, http.StatusBadRequest, "invalid_role", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
	}
}
