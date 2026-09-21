// Package httpapi — 用户管理端点(v6.2 §3.5 + 阶段 9 最小骨架)。
//
// 路由(均 admin-only,RequireAdmin + CSRF):
//   GET    /api/admin/users            → List(阶段 9 最小集:含 enabled 列)
//   GET    /api/admin/users/{id}       → GetByID
//
// 完整用户管理(邀请/注册/启用禁用/密码重置)留到后续 story:
//   POST   /api/admin/users/invitations → 创建邀请 token
//   POST   /api/auth/register           → 邀请注册(公开端点)
//   DELETE /api/admin/users/{id}        → 禁用(enabled=0)
package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
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
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
	}
}
