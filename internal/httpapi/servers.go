package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/fuuhoo/pipewright/internal/access"
	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/target"
	"github.com/go-chi/chi/v5"
)

// serverDTO 是服务器对外响应体(冻结契约;camelCase;无明文/无私钥/无口令)。
type serverDTO struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Host           string `json:"host"`
	Port           int    `json:"port"`
	User           string `json:"user"`
	CredentialID   string `json:"credentialId"`
	CredentialName string `json:"credentialName"`
	// GroupID 是所属资源分组;"" = 未归组(全员可见可操作)。组名由前端按 /api/groups 映射。
	GroupID   string `json:"groupId"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// toServerDTO 把领域 Server 转为契约 DTO。
func toServerDTO(s *target.Server) serverDTO {
	return serverDTO{
		ID:             s.ID,
		Name:           s.Name,
		Host:           s.Host,
		Port:           s.Port,
		User:           s.User,
		CredentialID:   s.CredentialID,
		CredentialName: s.CredentialName,
		GroupID:        s.GroupID,
		CreatedAt:      s.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:      s.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// serverTestDTO 是测试连接响应体(冻结契约)。error 失败时为人读串、成功时为 null。
type serverTestDTO struct {
	OK        bool    `json:"ok"`
	LatencyMs int64   `json:"latencyMs"`
	Output    string  `json:"output"`
	Error     *string `json:"error"`
}

// writeServerError 把领域错误映射为契约错误码/状态码;绝不回显明文/私钥/口令/栈。
func writeServerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, target.ErrVaultUnconfigured):
		writeError(w, http.StatusServiceUnavailable, "vault_unconfigured", "保险库未配置 master key,无法取 SSH 凭据")
	case errors.Is(err, target.ErrNotFound):
		writeError(w, http.StatusNotFound, "server_not_found", "服务器不存在")
	case errors.Is(err, target.ErrCredentialNotFound):
		writeError(w, http.StatusUnprocessableEntity, "credential_error", "引用的 SSH 凭据不存在")
	case errors.Is(err, target.ErrEmptyName):
		writeError(w, http.StatusBadRequest, "invalid_server", "服务器名称不能为空")
	case errors.Is(err, target.ErrEmptyHost):
		writeError(w, http.StatusBadRequest, "invalid_server", "主机地址不能为空")
	case errors.Is(err, target.ErrEmptyUser):
		writeError(w, http.StatusBadRequest, "invalid_server", "登录用户不能为空")
	case errors.Is(err, target.ErrEmptyCredentialID):
		writeError(w, http.StatusBadRequest, "invalid_server", "请选择 SSH 凭据")
	case errors.Is(err, target.ErrInvalidPort):
		writeError(w, http.StatusBadRequest, "invalid_server", "端口必须在 1..65535 之间")
	case errors.Is(err, target.ErrGroupNotFound):
		writeError(w, http.StatusUnprocessableEntity, "group_not_found", "引用的分组不存在")
	default:
		// 内部错误:不泄漏细节。
		writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
	}
}

// makeListServersHandler 返回 GET /api/servers handler → { items: [...] }。
// 列表按分组可见性收敛:普通用户只看到未归组 + 公开组 + 自己所在私有组的服务器。
func makeListServersHandler(svc target.Service, acc *access.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "服务器服务未初始化")
			return
		}
		visible, err := visibleGroups(r, acc)
		if err != nil {
			writeAccessError(w, err)
			return
		}
		servers, err := svc.ListScoped(r.Context(), target.ListFilter{Visible: visible})
		if err != nil {
			writeServerError(w, err)
			return
		}
		out := make([]serverDTO, 0, len(servers))
		for _, s := range servers {
			out = append(out, toServerDTO(s))
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// makeGetServerHandler 返回 GET /api/servers/{id} handler。
func makeGetServerHandler(svc target.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "服务器服务未初始化")
			return
		}
		s, err := svc.Get(r.Context(), chi.URLParam(r, "id"))
		if err != nil {
			writeServerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toServerDTO(s))
	}
}

// requireServerPlacement 是**登记**服务器时的归属闸门。
//
// 与项目归组的差别:未归组的服务器对全员可操作(含主机终端),所以「登记一台不归属任何组
// 的目标机」本身就是管理员级别的设置操作 —— 新建的资源还没有旧侧归属可依据,只能收在管理员。
// update 到未归组不走这里:那时调用方已判过原组的 ActManage,与项目同一口径。
// 放进某个组则两侧同项目:要求管得住那个组(ActManage)。
func requireServerPlacement(w http.ResponseWriter, r *http.Request, acc *access.Service, groupID string) bool {
	if groupID != access.Ungrouped {
		return requireGroupPlacement(w, r, acc, groupID)
	}
	actor, ok := accessActorFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
		return false
	}
	if !actor.IsAdmin() {
		writeAccessError(w, fmt.Errorf("%w:登记未归组服务器(全员可操作)需要管理员", access.ErrForbidden))
		return false
	}
	return true
}

// makeCreateServerHandler 返回 POST /api/servers handler。
func makeCreateServerHandler(svc target.Service, acc *access.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "服务器服务未初始化")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		var req struct {
			Name         string `json:"name"`
			Host         string `json:"host"`
			Port         int    `json:"port"`
			User         string `json:"user"`
			CredentialID string `json:"credentialId"`
			GroupID      string `json:"groupId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		groupID := strings.TrimSpace(req.GroupID)
		if acc != nil && !requireServerPlacement(w, r, acc, groupID) {
			return
		}
		s, err := svc.Create(r.Context(), target.CreateInput{
			Name:         req.Name,
			Host:         req.Host,
			Port:         req.Port,
			User:         req.User,
			CredentialID: req.CredentialID,
			GroupID:      groupID,
		})
		if err != nil {
			writeServerError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionServerCreate,
			TargetType: audit.TargetServer,
			TargetID:   s.ID,
			// detail 只留机器坐标,绝不落凭据 ID 之外的敏感信息(明文由 vault 保证不外泄)。
			Detail: map[string]any{"name": s.Name, "host": s.Host, "port": s.Port, "groupId": s.GroupID},
			IP:     clientIP(r),
		})
		writeJSON(w, http.StatusCreated, toServerDTO(s))
	}
}

// makeUpdateServerHandler 返回 PUT /api/servers/{id} handler。
// 改归属要管得住两侧:原组(把它甩出去)与新组(把它接进来),故补 ActManage 这一档;
// 其余字段编辑由中间件判到 ActOperate 即可。
func makeUpdateServerHandler(svc target.Service, acc *access.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "服务器服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		var req struct {
			Name         *string `json:"name"`
			Host         *string `json:"host"`
			Port         *int    `json:"port"`
			User         *string `json:"user"`
			CredentialID *string `json:"credentialId"`
			GroupID      *string `json:"groupId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		var fromGroupID string
		if req.GroupID != nil {
			if acc == nil {
				writeError(w, http.StatusServiceUnavailable, "internal", "权限服务未初始化")
				return
			}
			old, err := svc.Get(r.Context(), id)
			if err != nil {
				writeServerError(w, err)
				return
			}
			if !requireResourceManage(w, r, acc, access.KindServer, id) {
				return
			}
			newGroupID := strings.TrimSpace(*req.GroupID)
			if !requireGroupPlacement(w, r, acc, newGroupID) {
				return
			}
			fromGroupID = old.GroupID
			req.GroupID = &newGroupID
		}
		s, err := svc.Update(r.Context(), id, target.UpdateInput{
			Name:         req.Name,
			Host:         req.Host,
			Port:         req.Port,
			User:         req.User,
			CredentialID: req.CredentialID,
			GroupID:      req.GroupID,
		})
		if err != nil {
			writeServerError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionServerUpdate,
			TargetType: audit.TargetServer,
			TargetID:   s.ID,
			Detail:     map[string]any{"name": s.Name, "host": s.Host, "port": s.Port},
			IP:         clientIP(r),
		})
		if req.GroupID != nil && fromGroupID != s.GroupID {
			recordAuditFromRequest(r, aud, ac, audit.Entry{
				Action:     audit.ActionServerReassign,
				TargetType: audit.TargetServer,
				TargetID:   s.ID,
				Detail:     map[string]any{"from": fromGroupID, "to": s.GroupID},
				IP:         clientIP(r),
			})
		}
		writeJSON(w, http.StatusOK, toServerDTO(s))
	}
}

// makeDeleteServerHandler 返回 DELETE /api/servers/{id} handler。
// 删除是归属级动作(连机器带其上的容器一起消失),要求 ActManage。
func makeDeleteServerHandler(svc target.Service, acc *access.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "服务器服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		if acc != nil && !requireResourceManage(w, r, acc, access.KindServer, id) {
			return
		}
		if err := svc.Delete(r.Context(), id); err != nil {
			writeServerError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionServerDelete,
			TargetType: audit.TargetServer,
			TargetID:   id,
			IP:         clientIP(r),
		})
		w.WriteHeader(http.StatusNoContent)
	}
}

// makeTestServerHandler 返回 POST /api/servers/{id}/test handler。
// 经 SSH 实连跑只读探测命令;失败映射为 200 + ok=false + 人读 error(绝不含凭据明文)。
// 仅服务器/凭据不存在、保险库未配置等定位类错误才走 writeServerError(404/422/503)。
func makeTestServerHandler(svc target.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "服务器服务未初始化")
			return
		}
		res, err := svc.Test(r.Context(), chi.URLParam(r, "id"))
		if err != nil {
			writeServerError(w, err)
			return
		}
		var errStr *string
		if res.Err != "" {
			s := res.Err
			errStr = &s
		}
		writeJSON(w, http.StatusOK, serverTestDTO{
			OK:        res.OK,
			LatencyMs: res.LatencyMs,
			Output:    res.Output,
			Error:     errStr,
		})
	}
}
