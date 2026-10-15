// Package httpapi — 用户管理端点(v6.2 §3.5 + 阶段 9)。
//
// 路由(均 admin-only,RequireAdmin + CSRF):
//
//	GET    /api/admin/users                  → List(role/includeDisabled/limit/offset)
//	GET    /api/admin/users/{id}             → GetByID
//	POST   /api/admin/users                  → 建号(用户名 + 初始口令 + 角色,可带显示名/邮箱)
//	POST   /api/admin/users/{id}/password    → 重置口令
//	PATCH  /api/admin/users/{id}             → 改描述 / 显示名 / 邮箱 / 启用禁用 / 改角色
//	DELETE /api/admin/users/{id}             → 删号(不可逆;牵连清点见 users.Service.Delete)
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

	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/users"
	"github.com/go-chi/chi/v5"
)

// userDTO 是用户对外响应体(camelCase;不含 password_hash)。
//
// displayName / email 是本地档案(迁移 0069):列表页拿 display_name 当主标签、
// username 当登录名并列显示。两者可为空(老账号/没走 OAuth 建号的),前端回退到 username。
type userDTO struct {
	ID          string  `json:"id"`
	Username    string  `json:"username"`
	DisplayName string  `json:"displayName"`
	Email       string  `json:"email"`
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
		DisplayName: u.DisplayName,
		Email:       u.Email,
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
	// 删号的三档阻断都是 409:请求没错,是删下去的代价不对。前两条的 message 来自
	// users.DeleteBlocked,带计数(管理员据此知道要先移交几个组、先清几条凭据);
	// 最后一条没有可变信息,文案就固定在这一层写。
	case errors.Is(err, users.ErrHasGroups):
		writeError(w, http.StatusConflict, "user_has_groups", err.Error())
	case errors.Is(err, users.ErrHasPersonalCredentials):
		writeError(w, http.StatusConflict, "user_has_credentials", err.Error())
	case errors.Is(err, users.ErrLastAdmin):
		writeError(w, http.StatusConflict, "last_admin", "这是最后一个启用的管理员账号,删掉后再没人能进用户管理")
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
// body:{username, password, role, description, displayName, email}。
// 显示名/邮箱可留空:留空就是「本地档案还没填」,页面回退到用户名,以后随时能补。
// 审计只记用户名与角色,绝不含口令。
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
			DisplayName string `json:"displayName"`
			Email       string `json:"email"`
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
			DisplayName:  req.DisplayName,
			Email:        req.Email,
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
// 支持改描述、本地档案(显示名/邮箱)、启用/禁用与改角色(功能轴);改口令走 /password(审计动作不同)。
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
			DisplayName *string `json:"displayName"`
			Email       *string `json:"email"`
			Enabled     *bool   `json:"enabled"`
			Role        *string `json:"role"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		if req.Description == nil && req.DisplayName == nil && req.Email == nil &&
			req.Enabled == nil && req.Role == nil {
			writeError(w, http.StatusBadRequest, "bad_request",
				"至少提供 description、displayName、email、enabled 或 role")
			return
		}
		// 禁用自己在写库前拦下:把唯一的管理员账号禁用掉之后再没人能改回来。
		if req.Enabled != nil && !*req.Enabled && id == sessionUserID(r) {
			writeError(w, http.StatusConflict, "self_disable", "不能禁用自己的账号")
			return
		}
		target, err := us.GetByID(id)
		if err != nil {
			writeUsersError(w, err)
			return
		}
		if req.Description != nil {
			if err := us.SetDescription(id, *req.Description); err != nil {
				writeUsersError(w, err)
				return
			}
		}
		// 显示名/邮箱是一行 UPDATE 同时写两列,所以只提交其中一个时要把另一个现值带回去,
		// 否则 SET 会把没提交的那列清空。SetProfile 负责归一化(空白折叠 + 按 rune 截断)。
		if req.DisplayName != nil || req.Email != nil {
			name, email := target.DisplayName, target.Email
			if req.DisplayName != nil {
				name = *req.DisplayName
			}
			if req.Email != nil {
				email = *req.Email
			}
			if err := us.SetProfile(id, name, email); err != nil {
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
		// 改角色只有真正变了才写库与记账;角色名不在枚举内 → 400(领域层 ErrValidation)。
		roleChanged := false
		if req.Role != nil && *req.Role != target.Role {
			if err := us.SetRole(id, *req.Role); err != nil {
				writeUsersError(w, err)
				return
			}
			roleChanged = true
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
		detail := map[string]any{"username": fresh.Username, "role": fresh.Role, "enabled": fresh.Enabled}
		if roleChanged {
			detail["role_from"] = target.Role
		}
		// 档案改过就把新值留下:改显示名可以冒充别人,只记「改过」事后看不出改成了什么。
		if req.DisplayName != nil || req.Email != nil {
			detail["displayName"] = fresh.DisplayName
			detail["email"] = fresh.Email
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     action,
			TargetType: audit.TargetUser,
			TargetID:   fresh.ID,
			Detail:     detail,
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusOK, toUserDTO(fresh))
	}
}

// makeDeleteUserHandler 返回 DELETE /api/admin/users/{id} handler(永久删号)。
//
// 与「禁用」的分工:禁用是可撤销的开关,删除会把账号连同会话、外部身份绑定和分组名册
// 一起抹掉。所以这里先挡两类目标,再交给 users.Service.Delete 做牵连清点:
//   - 内置管理员行:auth.Login 按 admin_user 优先,删了 users 这行既不阻止管理员登录,
//     又让 personal 凭据的归属(BootstrapAdminRegularUserID)悬空。
//   - 自己:与 self_disable 同一理由 —— 把唯一的入口删掉之后没人能恢复。
//
// 审计必须在删除**前**取到 username:这一行删掉后就再也读不回来了,而「谁在什么时候
// 删了哪个账号」正是事后最可能要查的一条。
func makeDeleteUserHandler(us *users.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if us == nil {
			writeError(w, http.StatusServiceUnavailable, "users_unavailable", "用户服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		if isBootstrapAdminRow(id) {
			writeError(w, http.StatusConflict, "bootstrap_admin_row",
				"内置管理员不可删除(它是登录与「账户设置」的锚点)")
			return
		}
		if id == sessionUserID(r) {
			writeError(w, http.StatusConflict, "self_delete", "不能删除自己的账号")
			return
		}
		target, err := us.GetByID(id)
		if err != nil {
			writeUsersError(w, err)
			return
		}
		if err := us.Delete(id); err != nil {
			writeUsersError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionUserDelete,
			TargetType: audit.TargetUser,
			TargetID:   target.ID,
			Detail: map[string]any{
				"username":    target.Username,
				"role":        target.Role,
				"displayName": target.DisplayName,
				"email":       target.Email,
			},
			IP: clientIP(r),
		})
		w.WriteHeader(http.StatusNoContent)
	}
}
