package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/fuuhoo/pipewright/internal/apitoken"
	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/group"
	"github.com/fuuhoo/pipewright/internal/mcpsrv"
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
	// AllowedTools / GroupScope 回显这把端点的收窄范围,供设置页列表标注与再编辑时回填。
	// 两者都保证是数组(不限时输出 []),否则 null 会让前端为「不限」和「读不到」写两套判断。
	AllowedTools []string `json:"allowedTools"`
	GroupScope   []string `json:"groupScope"`
}

// scopeForDTO 把领域侧的 nil(不限)归一成空切片,只为 JSON 输出形态服务。
func scopeForDTO(items []string) []string {
	if len(items) == 0 {
		return []string{}
	}
	return items
}

func toAPITokenDTO(t *apitoken.Token) apiTokenDTO {
	return apiTokenDTO{
		ID:           t.ID,
		Name:         t.Name,
		UserID:       t.UserID,
		OwnerName:    t.OwnerName,
		CreatedBy:    t.CreatedBy,
		CreatedAt:    t.CreatedAt.UTC().Format(time.RFC3339),
		LastUsedAt:   formatMaybeTime(t.LastUsedAt),
		AllowedTools: scopeForDTO(t.AllowedTools),
		GroupScope:   scopeForDTO(t.GroupScope),
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

// unknownTools 返回勾选名单里不存在于名册的工具名(清单见 mcpsrv.ToolNames)。
//
// 为什么校验放在 HTTP 层而不是 apitoken:apitoken 是 mcpsrv 的被依赖方,让它认识工具名
// 就成了反向依赖。名单为空(= 不限)由调用方在进来之前已排除显式空列表。
func unknownTools(names []string) []string {
	set := make(map[string]bool)
	for _, name := range mcpsrv.ToolNames() {
		set[name] = true
	}
	var bad []string
	for _, name := range names {
		if !set[strings.TrimSpace(name)] {
			bad = append(bad, name)
		}
	}
	return bad
}

// unknownGroups 返回勾选名单里并不存在的分组 ID。引用一个已删的组不会越权(判定按归属
// 解析失败处理),但它会让这把令牌永远看不见该组,签发当场就该说清楚。
//
// 第二个返回值只在「读库真的失败」时非空 —— 它不能和「名字不存在」混成同一个错误,
// 否则一次 DB 抖动会被回成 400 入参错误,把运维引去改请求体而不是查库。
func unknownGroups(ctx context.Context, gs *group.Service, ids []string) ([]string, error) {
	all, err := gs.List(ctx)
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(all))
	for i := range all {
		set[all[i].ID] = true
	}
	var bad []string
	for _, id := range ids {
		if !set[strings.TrimSpace(id)] {
			bad = append(bad, id)
		}
	}
	return bad, nil
}

// makeCreateAPITokenHandler 返回 POST /api/admin/api-tokens handler。
//
// 请求体 {name, userId, allowedTools, groupScope}:userId 留空 = 签给当前登录者自己。
// 这个默认值不是省事 ——「管理员替某人签」与「管理员给自己签」在事后追查里是两条完全不同的线,
// 而机器凭据真正该问的是「谁用它跑过构建」,所以归属人必须是一个具体账号,不能是「管理员」这个抽象。
//
// allowedTools / groupScope 是这把端点的两把尺(工具面与数据面),口径见 apitoken/scopes.go:
// 缺省与空数组都归一成「不限」,但**显式写成 [] 当场拒签** —— 存进库里读回来就是「不限」,
// 一把想「什么都不给」的令牌反而会拿到全量,这是全站最不该出错的方向。
func makeCreateAPITokenHandler(svc *apitoken.Service, gs *group.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAPITokenSvc(w, svc) {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		var req struct {
			Name         string   `json:"name"`
			UserID       string   `json:"userId"`
			AllowedTools []string `json:"allowedTools"`
			GroupScope   []string `json:"groupScope"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		// 缺省 → nil;显式 [] → 非 nil 且 len 0。这两态必须分开判。
		if req.AllowedTools != nil && len(req.AllowedTools) == 0 {
			writeError(w, http.StatusBadRequest, "invalid_token_request", "工具范围要么不限制,至少给一个;一个都不给等于签发一把不能用的凭据")
			return
		}
		if req.GroupScope != nil && len(req.GroupScope) == 0 {
			writeError(w, http.StatusBadRequest, "invalid_token_request", "分组范围要么不限制,至少给一个;一个都不给等于签发一把不能用的凭据")
			return
		}
		if len(req.AllowedTools) > 0 {
			if bad := unknownTools(req.AllowedTools); len(bad) > 0 {
				writeError(w, http.StatusBadRequest, "invalid_token_request", "不存在的 MCP 工具:"+strings.Join(bad, "、"))
				return
			}
		}
		if len(req.GroupScope) > 0 {
			if gs == nil {
				writeError(w, http.StatusServiceUnavailable, "internal", "分组服务未初始化,无法校验分组范围")
				return
			}
			bad, err := unknownGroups(r.Context(), gs, req.GroupScope)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal", "校验分组范围时读库失败")
				return
			}
			if len(bad) > 0 {
				writeError(w, http.StatusBadRequest, "invalid_token_request", "不存在的分组:"+strings.Join(bad, "、"))
				return
			}
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
			Name:         req.Name,
			UserID:       userID,
			CreatedBy:    sess.Username,
			AllowedTools: req.AllowedTools,
			GroupScope:   req.GroupScope,
		})
		if err != nil {
			writeAPITokenError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionAPITokenCreate,
			TargetType: audit.TargetAPIToken,
			TargetID:   created.Token.ID,
			// detail 只留「哪把、给谁、收窄到哪」;明文只进响应体一次,绝不进审计。
			Detail: map[string]any{
				"name":         created.Token.Name,
				"userId":       created.Token.UserID,
				"allowedTools": created.Token.AllowedTools,
				"groupScope":   created.Token.GroupScope,
			},
			IP: clientIP(r),
		})
		writeJSON(w, http.StatusCreated, map[string]any{
			"item":      toAPITokenDTO(&created.Token),
			"plaintext": created.Plaintext,
		})
	}
}

// makeUpdateAPITokenScopesHandler 返回 PATCH /api/admin/api-tokens/{id} handler。
//
// 请求体里两个键都可以省:省 = 这一把尺不动;给非空数组 = 收窄成这些;给 [] = 改回不限。
// 最后一条与 POST 的口径相反,而且是故意的:签发时「什么都不给」是笔误,该拒;
// 编辑时「清空」只有一个意思 —— 把上次套上的尺解开(见 apitoken.SetScopesInput)。
//
// 返回体是改后的整条令牌视图,前端不必再拉一次列表才知道改成了什么。
func makeUpdateAPITokenScopesHandler(svc *apitoken.Service, gs *group.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAPITokenSvc(w, svc) {
			return
		}
		id := chi.URLParam(r, "id")
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		var req struct {
			AllowedTools *[]string `json:"allowedTools"`
			GroupScope   *[]string `json:"groupScope"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		if req.AllowedTools != nil && len(*req.AllowedTools) > 0 {
			if bad := unknownTools(*req.AllowedTools); len(bad) > 0 {
				writeError(w, http.StatusBadRequest, "invalid_token_request", "不存在的 MCP 工具:"+strings.Join(bad, "、"))
				return
			}
		}
		if req.GroupScope != nil && len(*req.GroupScope) > 0 {
			if gs == nil {
				writeError(w, http.StatusServiceUnavailable, "internal", "分组服务未初始化,无法校验分组范围")
				return
			}
			bad, err := unknownGroups(r.Context(), gs, *req.GroupScope)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal", "校验分组范围时读库失败")
				return
			}
			if len(bad) > 0 {
				writeError(w, http.StatusBadRequest, "invalid_token_request", "不存在的分组:"+strings.Join(bad, "、"))
				return
			}
		}
		updated, err := svc.SetScopes(r.Context(), id, apitoken.SetScopesInput{
			AllowedTools: req.AllowedTools,
			GroupScope:   req.GroupScope,
		})
		if err != nil {
			writeAPITokenError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionAPITokenUpdate,
			TargetType: audit.TargetAPIToken,
			TargetID:   updated.ID,
			// 记的是改完之后的范围:事后要回答的是「这把当时被收窄到哪」,而不是「原本是多少」。
			Detail: map[string]any{
				"name":         updated.Name,
				"userId":       updated.UserID,
				"allowedTools": updated.AllowedTools,
				"groupScope":   updated.GroupScope,
			},
			IP: clientIP(r),
		})
		writeJSON(w, http.StatusOK, map[string]any{"item": toAPITokenDTO(updated)})
	}
}

// makeListMCPToolsHandler 返回 GET /api/admin/mcp-tools handler → { items: [...] }。
//
// 它只吐名字、标题与只读位(不含 handler 与描述正文),给设置页画勾选框用。
// 清单来自 mcpsrv 那一份权威名册,签发接口校验工具名也读同一份 —— 两处读同一份才不会有
// 「页面上勾了但后端不认」的漂移(名册与真 server 的 tools/list 由 TestMCP_CatalogMatchesServer 钉住)。
func makeListMCPToolsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cat := mcpsrv.ToolCatalog()
		out := make([]map[string]any, 0, len(cat))
		for _, t := range cat {
			out = append(out, map[string]any{"name": t.Name, "title": t.Title, "readOnly": t.ReadOnly})
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": out})
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
