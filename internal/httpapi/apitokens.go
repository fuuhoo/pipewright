package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/fuuhoo/pipewright/internal/apitoken"
	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/go-chi/chi/v5"
)

// apiTokenDTO 是令牌对外响应体(camelCase)。
//
// 刻意没有明文字段:明文只在 Create 的那一次响应里通过 plaintext 出现(见下),
// 列表与撤销的响应体里任何路径都拿不到它 —— 拿得到就等于库里存了,而库里只有摘要。
type apiTokenDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	UserID    string `json:"userId"`
	OwnerName string `json:"ownerName"`
	CreatedBy string `json:"createdBy"`
	CreatedAt string `json:"createdAt"`
	// LastUsedAt 为空串表示这把令牌从没被用过(发出去没动过,通常是该删的那种)。
	LastUsedAt string `json:"lastUsedAt"`
}

func toAPITokenDTO(t *apitoken.Token) apiTokenDTO {
	return apiTokenDTO{
		ID:         t.ID,
		Name:       t.Name,
		UserID:     t.UserID,
		OwnerName:  t.OwnerName,
		CreatedBy:  t.CreatedBy,
		CreatedAt:  t.CreatedAt.UTC().Format(time.RFC3339),
		LastUsedAt: formatMaybeTime(t.LastUsedAt),
	}
}

// formatMaybeTime 把零值时间渲染成空串(两列可空语义:last_used_at / last_login_at)。
func formatMaybeTime(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return ts.UTC().Format(time.RFC3339)
}

// writeAPITokenError 领域错误 → 状态码/契约码。绝不回显摘要、栈或 SQL 细节。
func writeAPITokenError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, apitoken.ErrUserNotFound):
		writeError(w, http.StatusUnprocessableEntity, "user_not_found", "归属用户不存在")
	case errors.Is(err, apitoken.ErrValidation):
		writeError(w, http.StatusBadRequest, "invalid_token_request", err.Error())
	case errors.Is(err, apitoken.ErrNotFound):
		writeError(w, http.StatusNotFound, "api_token_not_found", "令牌不存在或已被撤销")
	default:
		writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
	}
}

// requireAPITokenSvc 未装配令牌子系统时回 503(与 users/roles 等端点同一手法)。
func requireAPITokenSvc(w http.ResponseWriter, svc *apitoken.Service) bool {
	if svc == nil {
		writeError(w, http.StatusServiceUnavailable, "internal", "API 令牌服务未初始化")
		return false
	}
	return true
}

// makeListAPITokensHandler 返回 GET /api/admin/api-tokens handler → { items: [...] }。
func makeListAPITokensHandler(svc *apitoken.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAPITokenSvc(w, svc) {
			return
		}
		tokens, err := svc.List(r.Context())
		if err != nil {
			writeAPITokenError(w, err)
			return
		}
		out := make([]apiTokenDTO, 0, len(tokens))
		for i := range tokens {
			out = append(out, toAPITokenDTO(&tokens[i]))
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// makeCreateAPITokenHandler 返回 POST /api/admin/api-tokens handler。
//
// 请求体 {name, userId}:userId 留空 = 签给当前登录者自己。这个默认值不是省事 ——
// 「管理员替某人签」与「管理员给自己签」在事后追查里是两条完全不同的线,而机器凭据真正
// 该问的是「谁用它跑过构建」,所以归属人必须是一个具体账号,不能是「管理员」这个抽象。
func makeCreateAPITokenHandler(svc *apitoken.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAPITokenSvc(w, svc) {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		var req struct {
			Name   string `json:"name"`
			UserID string `json:"userId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		sess, ok := sessionFromContext(r.Context())
		if !ok || sess == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
			return
		}
		userID := req.UserID
		if userID == "" {
			userID = sess.UserID
		}
		created, err := svc.Create(r.Context(), apitoken.CreateInput{
			Name:      req.Name,
			UserID:    userID,
			CreatedBy: sess.Username,
		})
		if err != nil {
			writeAPITokenError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionAPITokenCreate,
			TargetType: audit.TargetAPIToken,
			TargetID:   created.Token.ID,
			// detail 只留「哪把、给谁」;明文只进响应体一次,绝不进审计。
			Detail: map[string]any{"name": created.Token.Name, "userId": created.Token.UserID},
			IP:     clientIP(r),
		})
		writeJSON(w, http.StatusCreated, map[string]any{
			"item":      toAPITokenDTO(&created.Token),
			"plaintext": created.Plaintext,
		})
	}
}

// makeRevokeAPITokenHandler 返回 DELETE /api/admin/api-tokens/{id} handler。
// 撤销 = 删行:下一次带这把令牌的请求直接 401,不存在「已撤销但还能用」的中间态。
func makeRevokeAPITokenHandler(svc *apitoken.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAPITokenSvc(w, svc) {
			return
		}
		id := chi.URLParam(r, "id")
		// 先读一次:删掉之后这行的名称再也查不回来,而审计里的 name 是事后认出「是哪把」的唯一线索。
		t, err := svc.Get(r.Context(), id)
		if err != nil {
			writeAPITokenError(w, err)
			return
		}
		if err := svc.Revoke(r.Context(), id); err != nil {
			writeAPITokenError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionAPITokenRevoke,
			TargetType: audit.TargetAPIToken,
			TargetID:   id,
			Detail:     map[string]any{"name": t.Name, "userId": t.UserID},
			IP:         clientIP(r),
		})
		w.WriteHeader(http.StatusNoContent)
	}
}
