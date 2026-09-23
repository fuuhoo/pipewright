package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/huangchengsir/pipewright/internal/access"
	"github.com/huangchengsir/pipewright/internal/audit"
	"github.com/huangchengsir/pipewright/internal/auth"
	"github.com/huangchengsir/pipewright/internal/group"
	"github.com/huangchengsir/pipewright/internal/users"
)

// groups.go —— v6.2 分组权限的管理端点。
//
// 权限矩阵(与 access.Decide 一致,不另立一套):
//   - 建组 / 删组            → 仅管理员(分组是「设置类」,普通用户只被归组,不建组)
//   - 改组 / 管成员          → 管理员或该组组长
//   - 看组                   → 管理员、组长、成员;public 组全员可见
//
// 这些路由都挂在 /api/groups 下,不经过 accessGuardMiddleware(它只管 projects/runs),
// 权限在 handler 内用 access.Decide 就地判定。

// groupMemberDTO 是名册成员(id + 展示名)。
type groupMemberDTO struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

// groupDTO 是分组对外响应体(camelCase)。
type groupDTO struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	Description  string           `json:"description"`
	Visibility   string           `json:"visibility"`
	OwnerID      string           `json:"ownerId"`
	OwnerName    string           `json:"ownerName"`
	Members      []groupMemberDTO `json:"members"`
	ProjectCount int              `json:"projectCount"`
	ServerCount  int              `json:"serverCount"`
	// CanManage 是「当前请求者能否管这个组(改名册/改可见性/把资源归进来)」。
	// 由 access.Decide 算出而不是让前端重推一遍:判定只有一处真相,UI 只读结论。
	CanManage bool   `json:"canManage"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// toGroupDTO 把领域分组转成 DTO。members 是名册 id → 展示名的解析结果(可为 nil,
// 缺失的 id 用 id 前缀兜底,保证 UI 上不会出现空白行)。
func toGroupDTO(g *group.Group, userNames map[string]string) groupDTO {
	members := make([]groupMemberDTO, 0, len(g.MemberIDs))
	for _, id := range g.MemberIDs {
		name := userNames[id]
		if name == "" {
			name = id
		}
		members = append(members, groupMemberDTO{ID: id, Username: name})
	}
	return groupDTO{
		ID:           g.ID,
		Name:         g.Name,
		Description:  g.Description,
		Visibility:   g.Visibility,
		OwnerID:      g.OwnerID,
		OwnerName:    g.OwnerName,
		Members:      members,
		ProjectCount: g.ProjectCount,
		ServerCount:  g.ServerCount,
		CreatedAt:    g.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:    g.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// writeGroupError 把领域错误映射为契约错误码/状态码。
func writeGroupError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, group.ErrNotFound):
		writeError(w, http.StatusNotFound, "group_not_found", "分组不存在")
	case errors.Is(err, group.ErrDuplicateName):
		writeError(w, http.StatusConflict, "group_duplicate_name", "分组名已被占用")
	case errors.Is(err, group.ErrEmptyName):
		writeError(w, http.StatusBadRequest, "invalid_group", "分组名不能为空")
	case errors.Is(err, group.ErrInvalidVisibility):
		writeError(w, http.StatusBadRequest, "invalid_group", "可见性只能是 public 或 private")
	case errors.Is(err, group.ErrOwnerRequired):
		writeError(w, http.StatusBadRequest, "invalid_group", "必须指定组长")
	case errors.Is(err, group.ErrUserNotFound):
		writeError(w, http.StatusBadRequest, "group_user_not_found", "组长或成员不是有效用户")
	case errors.Is(err, group.ErrGroupNotEmpty):
		writeError(w, http.StatusConflict, "group_not_empty", "组内仍有项目或服务器,请先移出")
	case errors.Is(err, access.ErrUnauthenticated):
		writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
	case errors.Is(err, access.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "无权操作该分组:只有管理员或组长可以")
	default:
		writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
	}
}

// groupActor 取判定用 Actor;失败时已写好响应,调用方直接 return。
func groupActor(w http.ResponseWriter, r *http.Request) (*access.Actor, bool) {
	actor, ok := accessActorFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
		return nil, false
	}
	return actor, true
}

// makeListGroupsHandler 返回 GET /api/groups handler:管理员看全部,其余看可见子集。
func makeListGroupsHandler(gs *group.Service, usersSvc *users.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if gs == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "分组服务未初始化")
			return
		}
		actor, ok := groupActor(w, r)
		if !ok {
			return
		}
		var (
			list []group.Group
			err  error
		)
		if actor.IsAdmin() {
			list, err = gs.List(r.Context())
		} else {
			list, err = gs.ListVisible(r.Context(), actor.UserID)
		}
		if err != nil {
			writeGroupError(w, err)
			return
		}
		names := resolveUserNames(usersSvc, groupMemberIDs(list))
		out := make([]groupDTO, 0, len(list))
		for i := range list {
			out = append(out, stampGroupManage(toGroupDTO(&list[i], names), &list[i], actor))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// stampGroupManage 给 DTO 打上「当前 actor 能否管这个组」的结论。
func stampGroupManage(d groupDTO, g *group.Group, actor *access.Actor) groupDTO {
	d.CanManage = access.Decide(actor, g.Snapshot(), access.ActManage) == nil
	return d
}

// groupMemberIDs 汇总多个分组的名册 id(去重),供一次性解析展示名。
func groupMemberIDs(list []group.Group) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, g := range list {
		for _, id := range g.MemberIDs {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}

// resolveUserNames 把 users.id 换成用户名。usersSvc 未注入时返回空表,
// 调用方(toGroupDTO)会以 id 兜底显示——名字缺失不该让整页报错。
// 名册一次整表取回而不是逐个查:用户量级很小(单机实例),而 N+1 在请求里更贵。
func resolveUserNames(usersSvc *users.Service, ids []string) map[string]string {
	out := map[string]string{}
	if usersSvc == nil || len(ids) == 0 {
		return out
	}
	list, err := usersSvc.List(users.ListFilter{IncludeDisabled: true, Limit: users.MaxListLimit})
	if err != nil {
		return out
	}
	for _, u := range list {
		out[u.ID] = u.Username
	}
	return out
}

// makeCreateGroupHandler 返回 POST /api/groups handler(仅管理员,见路由注册)。
func makeCreateGroupHandler(gs *group.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if gs == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "分组服务未初始化")
			return
		}
		actor, ok := groupActor(w, r)
		if !ok {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		var req struct {
			Name        string   `json:"name"`
			Description string   `json:"description"`
			Visibility  string   `json:"visibility"`
			OwnerID     string   `json:"ownerId"`
			MemberIDs   []string `json:"memberIds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		// 组长缺省为创建者本人:管理员建的组若不留组长,就变成「只有管理员能管」的孤儿组,
		// 更符合直觉的是「你建的就是你带」——管理员想指定别人时显式传 ownerId。
		ownerID := req.OwnerID
		if ownerID == "" {
			ownerID = actor.UserID
		}
		g, err := gs.Create(r.Context(), group.CreateInput{
			Name:        req.Name,
			Description: req.Description,
			Visibility:  req.Visibility,
			OwnerID:     ownerID,
			MemberIDs:   req.MemberIDs,
		})
		if err != nil {
			writeGroupError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionGroupCreate,
			TargetType: audit.TargetResourceGroup,
			TargetID:   g.ID,
			Detail:     map[string]any{"name": g.Name, "visibility": g.Visibility, "ownerId": g.OwnerID},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusCreated, toGroupDTO(g, nil))
	}
}

// loadGroupForActor 取分组并按 act 判定;失败时已写好响应。
func loadGroupForActor(w http.ResponseWriter, r *http.Request, gs *group.Service, act access.Act) (*group.Group, *access.Actor, bool) {
	actor, ok := groupActor(w, r)
	if !ok {
		return nil, nil, false
	}
	g, err := gs.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeGroupError(w, err)
		return nil, nil, false
	}
	if err := access.Decide(actor, g.Snapshot(), act); err != nil {
		writeGroupError(w, err)
		return nil, nil, false
	}
	return g, actor, true
}

// makeGetGroupHandler 返回 GET /api/groups/{id} handler。
func makeGetGroupHandler(gs *group.Service, usersSvc *users.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if gs == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "分组服务未初始化")
			return
		}
		g, actor, ok := loadGroupForActor(w, r, gs, access.ActView)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, stampGroupManage(toGroupDTO(g, resolveUserNames(usersSvc, g.MemberIDs)), g, actor))
	}
}

// makeUpdateGroupHandler 返回 PATCH /api/groups/{id} handler(管理员或组长)。
func makeUpdateGroupHandler(gs *group.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if gs == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "分组服务未初始化")
			return
		}
		_, actor, ok := loadGroupForActor(w, r, gs, access.ActManage)
		if !ok {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		var req struct {
			Name        *string `json:"name"`
			Description *string `json:"description"`
			Visibility  *string `json:"visibility"`
			OwnerID     *string `json:"ownerId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		// 换组长是权限边界的改动:组长本人说了不算,只有管理员能转交。
		if req.OwnerID != nil && !actor.IsAdmin() {
			writeError(w, http.StatusForbidden, "forbidden", "只有管理员能更换组长")
			return
		}
		g, err := gs.Update(r.Context(), chi.URLParam(r, "id"), group.UpdateInput{
			Name:        req.Name,
			Description: req.Description,
			Visibility:  req.Visibility,
			OwnerID:     req.OwnerID,
		})
		if err != nil {
			writeGroupError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionGroupUpdate,
			TargetType: audit.TargetResourceGroup,
			TargetID:   g.ID,
			Detail:     map[string]any{"name": g.Name, "visibility": g.Visibility, "ownerId": g.OwnerID},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusOK, toGroupDTO(g, nil))
	}
}

// makeDeleteGroupHandler 返回 DELETE /api/groups/{id} handler(仅管理员)。
func makeDeleteGroupHandler(gs *group.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if gs == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "分组服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		if err := gs.Delete(r.Context(), id); err != nil {
			writeGroupError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionGroupDelete,
			TargetType: audit.TargetResourceGroup,
			TargetID:   id,
			IP:         clientIP(r),
		})
		w.WriteHeader(http.StatusNoContent)
	}
}

// makeGroupMemberHandler 返回 POST/DELETE /api/groups/{id}/members[/...] handler。
// 加/删成员都要求对该组有 Manage 权(管理员或组长)。
func makeGroupMemberHandler(gs *group.Service, aud audit.Recorder, ac auth.Authenticator, add bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if gs == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "分组服务未初始化")
			return
		}
		g, actor, ok := loadGroupForActor(w, r, gs, access.ActManage)
		if !ok {
			return
		}
		memberID := chi.URLParam(r, "userId")
		if add {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
			var req struct {
				UserID string `json:"userId"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
				return
			}
			if req.UserID != "" {
				memberID = req.UserID
			}
			if err := gs.AddMember(r.Context(), g.ID, memberID, actor.UserID); err != nil {
				writeGroupError(w, err)
				return
			}
		} else if err := gs.RemoveMember(r.Context(), g.ID, memberID); err != nil {
			writeGroupError(w, err)
			return
		}
		action := audit.ActionGroupMemberAdd
		if !add {
			action = audit.ActionGroupMemberRemove
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     action,
			TargetType: audit.TargetResourceGroup,
			TargetID:   g.ID,
			Detail:     map[string]any{"memberId": memberID},
			IP:         clientIP(r),
		})
		fresh, err := gs.Get(r.Context(), g.ID)
		if err != nil {
			writeGroupError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toGroupDTO(fresh, nil))
	}
}

// makeGroupAssignableUsersHandler 返回 GET /api/groups/assignable handler。
//
// 组长要往名册里加人,但 /api/admin/users 是 admin-only——所以这里给一条普通登录用户
// 可读的最小名册(仅 id + username,不含邮箱/描述/登录时间)。用户名在内网实例里不是
// 秘密(项目、运行、审计页到处都在展示),而缺了它组长的成员选择器就是死的。
func makeGroupAssignableUsersHandler(usersSvc *users.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if usersSvc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "用户服务未初始化")
			return
		}
		if _, ok := groupActor(w, r); !ok {
			return
		}
		list, err := usersSvc.List(users.ListFilter{Limit: users.MaxListLimit})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
			return
		}
		out := make([]groupMemberDTO, 0, len(list))
		for _, u := range list {
			out = append(out, groupMemberDTO{ID: u.ID, Username: u.Username})
		}
		writeJSON(w, http.StatusOK, out)
	}
}
