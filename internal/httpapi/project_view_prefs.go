package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/fuuhoo/pipewright/internal/projectpref"
)

// 项目页卡片的每用户手动顺序(GET/PUT /api/account/project-view)。
//
// 与 /api/account/server-view 同一口径:写在会话所属的 user_id 上,只作用于本人,
// 不涉及资产变更,所以不做 access 判定也不写审计。GET 只读过 auth;PUT 由 CSRF 中间件统一校验。

// projectViewPrefsDTO 是请求与响应的体。空的是 [] 而不是 null。
type projectViewPrefsDTO struct {
	Order []string `json:"order"`
}

func toProjectViewPrefsDTO(p projectpref.Prefs) projectViewPrefsDTO {
	out := projectViewPrefsDTO{Order: p.Order}
	if out.Order == nil {
		out.Order = []string{}
	}
	return out
}

// makeGetProjectViewPrefsHandler 返回 GET /api/account/project-view handler。
// 没设置过的人拿到空顺序(前端据此回落到默认排序),不是 404。
func makeGetProjectViewPrefsHandler(svc *projectpref.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "偏好服务未初始化")
			return
		}
		p, err := svc.Get(r.Context(), currentUserIDFromRequest(r))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
			return
		}
		writeJSON(w, http.StatusOK, toProjectViewPrefsDTO(p))
	}
}

// makePutProjectViewPrefsHandler 返回 PUT /api/account/project-view handler(整份覆盖,幂等)。
// 会话没绑账号(迁移 0053 之前的旧会话)→ 409,让人重登。
func makePutProjectViewPrefsHandler(svc *projectpref.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "偏好服务未初始化")
			return
		}
		userID := currentUserIDFromRequest(r)
		if userID == "" {
			writeError(w, http.StatusConflict, "session_without_user", "当前会话未绑定账号,请重新登录后再保存")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		var in projectViewPrefsDTO
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		saved, err := svc.Save(r.Context(), userID, projectpref.Prefs{Order: in.Order})
		if err != nil {
			if errors.Is(err, projectpref.ErrNoUser) {
				writeError(w, http.StatusConflict, "session_without_user", "当前会话未绑定账号,请重新登录后再保存")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
			return
		}
		writeJSON(w, http.StatusOK, toProjectViewPrefsDTO(saved))
	}
}
