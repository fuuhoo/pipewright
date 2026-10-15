package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/users"
	"github.com/go-chi/chi/v5"
)

// accountService 是账户设置 handler 依赖的窄接口(改口令 / 会话列表 / 撤销)。
// 由 *auth.Service 实现;窄接口便于测试替身。
type accountService interface {
	ChangePassword(current, newPassword, currentToken string) error
	// ChangeUserPassword 改 users 表账号(普通用户 / 额外管理员)自己的口令。
	ChangeUserPassword(userID, current, newPassword, currentToken string) error
	ListSessions(userID, currentToken string) ([]auth.SessionMeta, error)
	RevokeSession(userID, id string) error
}

// sessionDTO 是活跃会话对外响应体(冻结契约;**绝不含原始 token**)。
type sessionDTO struct {
	ID         string `json:"id"` // token 的 sha256 前缀(非原 token)
	CreatedAt  string `json:"createdAt"`
	LastSeenAt string `json:"lastSeenAt"`
	ExpiresAt  string `json:"expiresAt"`
	Current    bool   `json:"current"`
}

// sessionListDTO 是会话列表响应体。
type sessionListDTO struct {
	Sessions []sessionDTO `json:"sessions"`
}

func toSessionDTO(m auth.SessionMeta) sessionDTO {
	return sessionDTO{
		ID:         m.ID,
		CreatedAt:  m.CreatedAt.UTC().Format(time.RFC3339),
		LastSeenAt: m.LastSeenAt.UTC().Format(time.RFC3339),
		ExpiresAt:  m.ExpiresAt.UTC().Format(time.RFC3339),
		Current:    m.Current,
	}
}

// currentSessionToken 从请求 cookie 取当前会话 token(已过 requireAuth,cookie 必在)。
func currentSessionToken(r *http.Request) string {
	if c, err := r.Cookie(cookieSession); err == nil {
		return c.Value
	}
	return ""
}

// currentUserIDFromRequest 取 requireAuth 注入的会话所属用户 id。
// 迁移 0053 之前签发的旧会话没有 user_id,此时回空串,由 auth 层按「看不见别人」处理。
func currentUserIDFromRequest(r *http.Request) string {
	if sess, ok := sessionFromContext(r.Context()); ok && sess != nil {
		return sess.UserID
	}
	return ""
}

// makeChangePasswordHandler 返回 POST /api/account/password handler(认证 + CSRF)。
// 校验当前口令 → 更新新 argon2id 哈希 → 删除其它会话(当前保留)→ 204。
// 错当前 → 401 invalid_current_password;弱新口令 → 422 weak_password。
// 成功后追加 password_change 审计(detail 仅元数据,绝无明文口令)。
func makeChangePasswordHandler(svc accountService, aud audit.Recorder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "no_auth", "认证服务未初始化")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		var req struct {
			CurrentPassword string `json:"currentPassword"`
			NewPassword     string `json:"newPassword"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}

		token := currentSessionToken(r)
		// 分两条改密路径:bootstrap admin 的真身在 admin_user(只改 users 同步行不生效),
		// 其余账号(普通用户 + 管理员建出来的额外管理员)口令只在 users 里。
		var err error
		if sess, ok := sessionFromContext(r.Context()); ok && sess != nil &&
			sess.UserID != "" && sess.UserID != users.BootstrapAdminRegularUserID {
			err = svc.ChangeUserPassword(sess.UserID, req.CurrentPassword, req.NewPassword, token)
		} else {
			err = svc.ChangePassword(req.CurrentPassword, req.NewPassword, token)
		}
		switch {
		case err == nil:
			// continue
		case errors.Is(err, auth.ErrInvalidCurrentPassword):
			writeError(w, http.StatusUnauthorized, "invalid_current_password", "当前口令错误")
			return
		case errors.Is(err, auth.ErrWeakPassword):
			writeError(w, http.StatusUnprocessableEntity, "weak_password", "新口令至少 8 位")
			return
		case errors.Is(err, auth.ErrUserNotFound):
			writeError(w, http.StatusNotFound, "user_not_found", "账号不存在或已被禁用")
			return
		default:
			writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
			return
		}

		recordAudit(r.Context(), aud, audit.Entry{
			Actor:      auditActor,
			Action:     audit.ActionPasswordChange,
			TargetType: audit.TargetAccount,
			TargetID:   auditActor,
			Detail:     map[string]any{"otherSessionsRevoked": true},
			IP:         clientIP(r),
		})
		w.WriteHeader(http.StatusNoContent)
	}
}

// profileDTO 是本地档案的响应体(camelCase;只回归一化后的两列)。
type profileDTO struct {
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
}

// makeUpdateProfileHandler 返回 POST /api/account/profile handler(认证 + CSRF)。
//
// 本人改「本地档案」(users.display_name / email,迁移 0069):显示名给顶栏与账户页用,
// 为空时前端回退到用户名。改的是本地值而不是 user_identities 里的平台快照 —— 后者每次
// 登录都会被平台覆盖,把昵称写在那儿等于下次登录就丢。
//
// 只允许改自己:别人的档案归「用户管理」的 PATCH 端点管,那条路有管理员鉴权和不同的审计动作。
// 成功后回服务端归一化后的值(空白折叠、按 rune 截断都可能改掉提交内容),前端据此回显。
func makeUpdateProfileHandler(us *users.Service, aud audit.Recorder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if us == nil {
			writeError(w, http.StatusServiceUnavailable, "users_unavailable", "用户服务未初始化")
			return
		}
		id := currentUserIDFromRequest(r)
		if id == "" {
			// 0053 之前签发的旧会话没有 user_id:档案写不到确定的那一行,让人重登一次
			// 也比按名字猜个账号写过去安全。
			writeError(w, http.StatusConflict, "session_without_user", "当前会话未绑定账号,请重新登录后再修改")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		var req struct {
			DisplayName *string `json:"displayName"`
			Email       *string `json:"email"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		if req.DisplayName == nil && req.Email == nil {
			writeError(w, http.StatusBadRequest, "bad_request", "至少提供 displayName 或 email")
			return
		}
		target, err := us.GetByID(id)
		if err != nil {
			writeUsersError(w, err)
			return
		}
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
		fresh, err := us.GetByID(id)
		if err != nil {
			writeUsersError(w, err)
			return
		}
		recordAudit(r.Context(), aud, audit.Entry{
			Actor:      auditActor,
			Action:     audit.ActionProfileUpdate,
			TargetType: audit.TargetAccount,
			TargetID:   fresh.ID,
			// 记新值而不是只记「改了」:自我改名可以冒充别人(比如改成 Administrator),
			// 事后要能看出改成了什么。这两项都不是密钥,页面上本来就看得见。
			Detail: map[string]any{
				"username":    fresh.Username,
				"displayName": fresh.DisplayName,
				"email":       fresh.Email,
			},
			IP: clientIP(r),
		})
		writeJSON(w, http.StatusOK, profileDTO{DisplayName: fresh.DisplayName, Email: fresh.Email})
	}
}

// makeListSessionsHandler 返回 GET /api/account/sessions handler(认证保护;只读)。
// 只返回**当前登录用户自己**的未过期会话;**绝不回传原 token**;current 标识本请求所属会话。
func makeListSessionsHandler(svc accountService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "no_auth", "认证服务未初始化")
			return
		}
		metas, err := svc.ListSessions(currentUserIDFromRequest(r), currentSessionToken(r))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
			return
		}
		out := make([]sessionDTO, 0, len(metas))
		for _, m := range metas {
			out = append(out, toSessionDTO(m))
		}
		writeJSON(w, http.StatusOK, sessionListDTO{Sessions: out})
	}
}

// makeRevokeSessionHandler 返回 DELETE /api/account/sessions/{id} handler(认证 + CSRF)。
// 在**当前用户自己名下**按 id(sha256 前缀)定位并删除;别人的会话与不存在同样回 404;
// 撤销当前会话亦允许(返回 204,前端随后 401 跳登录)。成功后追加 session_revoke 审计。
func makeRevokeSessionHandler(svc accountService, aud audit.Recorder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "no_auth", "认证服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		err := svc.RevokeSession(currentUserIDFromRequest(r), id)
		switch {
		case err == nil:
			// continue
		case errors.Is(err, auth.ErrSessionNotFound):
			writeError(w, http.StatusNotFound, "session_not_found", "会话不存在")
			return
		default:
			writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
			return
		}

		recordAudit(r.Context(), aud, audit.Entry{
			Actor:      auditActor,
			Action:     audit.ActionSessionRevoke,
			TargetType: audit.TargetSession,
			TargetID:   id,
			Detail:     map[string]any{},
			IP:         clientIP(r),
		})
		w.WriteHeader(http.StatusNoContent)
	}
}
