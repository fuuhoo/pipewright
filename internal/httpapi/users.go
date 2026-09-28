// Package httpapi — 用户管理端点(v6.2 §3.5 + 阶段 9)。
//
// 路由(均 admin-only,RequireAdmin + CSRF):
//
//	GET    /api/admin/users                  → List(role/includeDisabled/limit/offset)
//	GET    /api/admin/users/{id}             → GetByID
//	POST   /api/admin/users                  → 建号(用户名 + 初始口令 + 角色)
//	POST   /api/admin/users/{id}/password    → 重置口令
//	PATCH  /api/admin/users/{id}             → 改描述 / 启用禁用
//
// 不在这里的:内置管理员(bootstrap admin)那一行——它的口令与启用状态由
// admin_user + 「账户设置」管,上述写端点对它一律 409,理由见 isBootstrapAdminRow。
// 邀请注册(邀请 token + 公开注册端点)仍是后续 story。
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/huangchengsir/pipewright/internal/audit"
	"github.com/huangchengsir/pipewright/internal/auth"
	"github.com/huangchengsir/pipewright/internal/users"
)

// userDTO 是用户对外响应体(camelCase;不含 password_hash)。
type userDTO struct {
	ID          string  `json:"id"`
	Username    string  `json:"username"`
	Role        string  `json:"role"`
	Enabled     bool    `json:"enabled"`
	Description string  `json:"description"`
	CreatedAt   string  `json:"createdAt"`
	UpdatedAt   string  `json:"updatedAt"`
	LastLoginAt *string `json:"lastLoginAt"`
}

func toUserDTO(u *users.User) userDTO {
	if u == nil {
		return userDTO{}
	}
	var lastLogin *string
	if u.LastLoginAt != nil {
		s := u.LastLoginAt.UTC().Format("2006-01-02T15:04:05Z07:00")
		lastLogin = &s
	}
	return userDTO{
		ID:          u.ID,
		Username:    u.Username,
		Role:        u.Role,
		Enabled:     u.Enabled,
		Description: u.Description,
		CreatedAt:   u.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:   u.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		LastLoginAt: lastLogin,
	}
}

// makeListUsersHandler GET /api/admin/users。
// v6.2 §5.2:列出所有用户(视图,绝不含 password_hash)。
// query:role(admin|user)· includeDisabled(1|true)。
// 分页:limit 默认 100、上限 500(与 audit 包一致)。
func makeListUsersHandler(us *users.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if us == nil {
			writeError(w, http.StatusServiceUnavailable, "users_unavailable", "用户服务未初始化")
			return
		}
		q := r.URL.Query()
		includeDisabled := q.Get("includeDisabled") == "1" || q.Get("includeDisabled") == "true"
		list, err := us.List(users.ListFilter{
			Role:            strings.TrimSpace(q.Get("role")),
			IncludeDisabled: includeDisabled,
			Limit:           atoiDefault(q.Get("limit"), users.DefaultListLimit),
			Offset:          atoiDefault(q.Get("offset"), 0),
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "查询用户列表失败")
			return
		}
		out := make([]userDTO, 0, len(list))
		for _, u := range list {
			out = append(out, toUserDTO(u))
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// makeGetUserHandler GET /api/admin/users/{id}。
func makeGetUserHandler(us *users.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if us == nil {
			writeError(w, http.StatusServiceUnavailable, "users_unavailable", "用户服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		u, err := us.GetByID(id)
		if err != nil {
			writeUsersError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toUserDTO(u))
	}
}

// writeUsersError 把领域错误映射到 HTTP 状态码。
func writeUsersError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, users.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, users.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	default:
		// 领域校验错误文案是中文可操作提示(可直接回传);其余(SQL/驱动)细节不外泄。
		if errors.Is(err, users.ErrValidation) {
			writeError(w, http.StatusBadRequest, "invalid_user", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
	}
}

// isBootstrapAdminRow 报告目标是否为 admin_user 在 users 表里的同步行。
//
// 这一行的口令/启用状态不由用户管理端点管:auth.Login 命中同名 admin_user 时优先走
// admin_user 路径,只改 users 的 hash 或 enabled 看起来成功、实际不影响登录。所以
// 重置口令与禁用都必须挡掉,否则管理员会以为已经停用了一个超管。
func isBootstrapAdminRow(id string) bool {
	return id == users.BootstrapAdminRegularUserID
}

// sessionUserID 取当前会话的 users.id;无会话返回 ""。
func sessionUserID(r *http.Request) string {
	if sess, ok := sessionFromContext(r.Context()); ok && sess != nil {
		return sess.UserID
	}
	return ""
}

// makeCreateUserHandler 返回 POST /api/admin/users handler(管理员建号)。
// body:{username, password, role, description}。审计只记用户名与角色,绝不含口令。
func makeCreateUserHandler(us *users.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if us == nil {
			writeError(w, http.StatusServiceUnavailable, "users_unavailable", "用户服务未初始化")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		var req struct {
			Username    string `json:"username"`
			Password    string `json:"password"`
			Role        string `json:"role"`
			Description string `json:"description"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		if len(req.Password) < users.MinPasswordLen {
			writeError(w, http.StatusUnprocessableEntity, "weak_password", "口令至少 8 位")
			return
		}
		hash, err := auth.HashPassword(req.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
			return
		}
		u, err := us.Create(users.CreateInput{
			Username:     req.Username,
			PasswordHash: hash,
			Role:         req.Role,
			Description:  req.Description,
		})
		if err != nil {
			writeUsersError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionUserAdminCreate,
			TargetType: audit.TargetUser,
			TargetID:   u.ID,
			Detail:     map[string]any{"username": u.Username, "role": u.Role},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusCreated, toUserDTO(u))
	}
}

// makeResetUserPasswordHandler 返回 POST /api/admin/users/{id}/password handler。
// 管理员重置他人口令;不改会话——「改了 hash 但别人会话还活着」是既有 admin 改密的
// 同一取舍,这里保持一致而不自创半套撤销。
func makeResetUserPasswordHandler(us *users.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if us == nil {
			writeError(w, http.StatusServiceUnavailable, "users_unavailable", "用户服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		if isBootstrapAdminRow(id) {
			writeError(w, http.StatusConflict, "bootstrap_admin_row",
				"内置管理员的口令请在「账户设置」里改(会同步 admin_user),此处只管普通账号")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		var req struct {
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		if len(req.Password) < users.MinPasswordLen {
			writeError(w, http.StatusUnprocessableEntity, "weak_password", "口令至少 8 位")
			return
		}
		target, err := us.GetByID(id)
		if err != nil {
			writeUsersError(w, err)
			return
		}
		hash, err := auth.HashPassword(req.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
			return
		}
		if err := us.SetPassword(id, hash); err != nil {
			writeUsersError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionUserPasswordReset,
			TargetType: audit.TargetUser,
			TargetID:   target.ID,
			Detail:     map[string]any{"username": target.Username},
			IP:         clientIP(r),
		})
		w.WriteHeader(http.StatusNoContent)
	}
}

// makePatchUserHandler 返回 PATCH /api/admin/users/{id} handler。
// 支持改描述与启用/禁用;改口令走 /password(审计动作不同)。
func makePatchUserHandler(us *users.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if us == nil {
			writeError(w, http.StatusServiceUnavailable, "users_unavailable", "用户服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		if isBootstrapAdminRow(id) {
			writeError(w, http.StatusConflict, "bootstrap_admin_row",
				"内置管理员不可在此禁用(口令也用「账户设置」改)")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		var req struct {
			Description *string `json:"description"`
			Enabled     *bool   `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		if req.Description == nil && req.Enabled == nil {
			writeError(w, http.StatusBadRequest, "bad_request", "至少提供 description 或 enabled")
			return
		}
		// 禁用自己在写库前拦下:把唯一的管理员账号禁用掉之后再没人能改回来。
		if req.Enabled != nil && !*req.Enabled && id == sessionUserID(r) {
			writeError(w, http.StatusConflict, "self_disable", "不能禁用自己的账号")
			return
		}
		if _, err := us.GetByID(id); err != nil {
			writeUsersError(w, err)
			return
		}
		if req.Description != nil {
			if err := us.SetDescription(id, *req.Description); err != nil {
				writeUsersError(w, err)
				return
			}
		}
		if req.Enabled != nil {
			if err := us.SetEnabled(id, *req.Enabled); err != nil {
				writeUsersError(w, err)
				return
			}
		}
		fresh, err := us.GetByID(id)
		if err != nil {
			writeUsersError(w, err)
			return
		}
		action := audit.ActionUserUpdate
		if req.Enabled != nil {
			action = audit.ActionUserEnabled
			if !*req.Enabled {
				action = audit.ActionUserDisabled
			}
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     action,
			TargetType: audit.TargetUser,
			TargetID:   fresh.ID,
			Detail:     map[string]any{"username": fresh.Username, "role": fresh.Role, "enabled": fresh.Enabled},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusOK, toUserDTO(fresh))
	}
}
