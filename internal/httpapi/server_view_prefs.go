package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/fuuhoo/pipewright/internal/serverpref"
)

// 容器页主机卡片的每用户偏好(置顶 + 手动顺序)。
//
// 这两个端点都是「本人」的:写在会话所属的那个 user_id 上,任何人都只能读到/改到自己的那份,
// 所以不需要 access 判定,也不需要审计(它不是资产变更,是界面习惯,记进审计只会淹掉真信号)。
//
// GET 只读 → 过 auth、豁免 CSRF;PUT 是写方法 → 由 requireCSRF 中间件统一要 X-CSRF-Token。

// serverViewPrefsDTO 是请求与响应的体(冻结契约)。两个列表都按顺序说话,空的是 [] 而不是 null。
type serverViewPrefsDTO struct {
	Pinned []string `json:"pinned"`
	Order  []string `json:"order"`
}

func toServerViewPrefsDTO(p serverpref.Prefs) serverViewPrefsDTO {
	out := serverViewPrefsDTO{Pinned: p.Pinned, Order: p.Order}
	if out.Pinned == nil {
		out.Pinned = []string{}
	}
	if out.Order == nil {
		out.Order = []string{}
	}
	return out
}

// makeGetServerViewPrefsHandler 返回 GET /api/account/server-view handler。
// 没设置过的人拿到空偏好(前端据此回落到默认排序),不是 404。
func makeGetServerViewPrefsHandler(svc *serverpref.Service) http.HandlerFunc {
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
		writeJSON(w, http.StatusOK, toServerViewPrefsDTO(p))
	}
}

// makePutServerViewPrefsHandler 返回 PUT /api/account/server-view handler(整份覆盖,幂等)。
// 会话没绑账号(迁移 0053 之前的旧会话)→ 409,让人重登,和改档案同一手法。
func makePutServerViewPrefsHandler(svc *serverpref.Service) http.HandlerFunc {
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
		var in serverViewPrefsDTO
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		saved, err := svc.Save(r.Context(), userID, serverpref.Prefs{Pinned: in.Pinned, Order: in.Order})
		if err != nil {
			if errors.Is(err, serverpref.ErrNoUser) {
				writeError(w, http.StatusConflict, "session_without_user", "当前会话未绑定账号,请重新登录后再保存")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
			return
		}
		// 回存下来的那份(已去空去重),前端据此把界面归一到和库里一致。
		writeJSON(w, http.StatusOK, toServerViewPrefsDTO(saved))
	}
}
