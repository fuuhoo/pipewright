package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/huangchengsir/pipewright/internal/access"
	"github.com/huangchengsir/pipewright/internal/audit"
	"github.com/huangchengsir/pipewright/internal/auth"
	"github.com/huangchengsir/pipewright/internal/kube"
)

// kubeClusterDTO 是集群对外响应体(camelCase;无凭据明文、无 kubeconfig 正文)。
type kubeClusterDTO struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	CredentialID string `json:"credentialId"`
	// CredentialName 冗余展示名(join credentials);便于列表显示。
	CredentialName string `json:"credentialName"`
	// Endpoint 是集群 API Server 地址(从 kubeconfig 现读现展示;库里不存,故可能为空)。
	Endpoint        string `json:"endpoint"`
	NamespaceDefault string `json:"namespaceDefault"`
	GroupID         string `json:"groupId"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt"`
}

func toKubeClusterDTO(c *kube.Cluster) kubeClusterDTO {
	return kubeClusterDTO{
		ID:               c.ID,
		Name:             c.Name,
		CredentialID:     c.CredentialID,
		CredentialName:   c.CredentialName,
		Endpoint:         c.Endpoint,
		NamespaceDefault: c.NamespaceDefault,
		GroupID:          c.GroupID,
		CreatedAt:        c.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:        c.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// kubeClusterTestDTO 是「测试连接」响应体。error 失败时为人读串、成功时为 null。
type kubeClusterTestDTO struct {
	OK        bool    `json:"ok"`
	LatencyMs int64   `json:"latencyMs"`
	Output    string  `json:"output"`
	Error     *string `json:"error"`
}

// writeKubeClusterError 把领域错误映射为契约错误码/状态码;绝不回显 kubeconfig/ token /栈。
func writeKubeClusterError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, kube.ErrVaultUnconfigured):
		writeError(w, http.StatusServiceUnavailable, "vault_unconfigured", "保险库未配置 master key,无法取集群凭据")
	case errors.Is(err, kube.ErrNotFound):
		writeError(w, http.StatusNotFound, "cluster_not_found", "集群不存在")
	case errors.Is(err, kube.ErrCredentialNotFound):
		writeError(w, http.StatusUnprocessableEntity, "credential_error", "引用的 kubeconfig 凭据不存在")
	case errors.Is(err, kube.ErrEmptyName):
		writeError(w, http.StatusBadRequest, "invalid_cluster", "集群名称不能为空")
	case errors.Is(err, kube.ErrEmptyCredentialID):
		writeError(w, http.StatusBadRequest, "invalid_cluster", "请选择 kubeconfig 凭据")
	case errors.Is(err, kube.ErrBadNamespace):
		writeError(w, http.StatusBadRequest, "invalid_cluster", "默认命名空间需是小写字母/数字与 -(最长 63)")
	case errors.Is(err, kube.ErrGroupNotFound):
		writeError(w, http.StatusUnprocessableEntity, "group_not_found", "引用的分组不存在")
	case errors.Is(err, kube.ErrKubeConfigInvalid):
		// 只回传可行动的判定结论(kube 侧文案已确保不含文档正文)。
		writeError(w, http.StatusUnprocessableEntity, "kubeconfig_invalid", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
	}
}

// makeListKubeClustersHandler 返回 GET /api/kube-clusters handler → { items: [...] }。
// 列表按分组可见性收敛(与服务器同一口径:未归组 + 公开组 + 自己所在私有组)。
func makeListKubeClustersHandler(svc kube.Service, acc *access.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "集群服务未初始化")
			return
		}
		visible, err := visibleGroups(r, acc)
		if err != nil {
			writeAccessError(w, err)
			return
		}
		clusters, err := svc.ListScoped(r.Context(), kube.ListFilter{Visible: visible})
		if err != nil {
			writeKubeClusterError(w, err)
			return
		}
		out := make([]kubeClusterDTO, 0, len(clusters))
		for _, c := range clusters {
			out = append(out, toKubeClusterDTO(c))
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// makeGetKubeClusterHandler 返回 GET /api/kube-clusters/{id} handler。
func makeGetKubeClusterHandler(svc kube.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "集群服务未初始化")
			return
		}
		c, err := svc.Get(r.Context(), chi.URLParam(r, "id"))
		if err != nil {
			writeKubeClusterError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toKubeClusterDTO(c))
	}
}

// makeCreateKubeClusterHandler 返回 POST /api/kube-clusters handler。
// 未归组的集群对全员可发布到,登记它本身就是管理员级设置操作(与服务器同一口径)。
func makeCreateKubeClusterHandler(svc kube.Service, acc *access.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "集群服务未初始化")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		var req struct {
			Name             string `json:"name"`
			CredentialID     string `json:"credentialId"`
			NamespaceDefault string `json:"namespaceDefault"`
			GroupID          string `json:"groupId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		groupID := strings.TrimSpace(req.GroupID)
		if acc != nil && !requireServerPlacement(w, r, acc, groupID) {
			return
		}
		c, err := svc.Create(r.Context(), kube.CreateInput{
			Name:             req.Name,
			CredentialID:     req.CredentialID,
			NamespaceDefault: req.NamespaceDefault,
			GroupID:          groupID,
		})
		if err != nil {
			writeKubeClusterError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionKubeClusterCreate,
			TargetType: audit.TargetKubeCluster,
			TargetID:   c.ID,
			// detail 只留坐标:endpoint 是内网地址而非密钥,凭证正文由 vault 保证不外泄。
			Detail: map[string]any{"name": c.Name, "endpoint": c.Endpoint, "groupId": c.GroupID},
			IP:     clientIP(r),
		})
		writeJSON(w, http.StatusCreated, toKubeClusterDTO(c))
	}
}

// makeUpdateKubeClusterHandler 返回 PUT /api/kube-clusters/{id} handler。
// 改归属要管得住两侧(同服务器口径);其余字段编辑由中间件判到 ActOperate。
func makeUpdateKubeClusterHandler(svc kube.Service, acc *access.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "集群服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		var req struct {
			Name             *string `json:"name"`
			CredentialID     *string `json:"credentialId"`
			NamespaceDefault *string `json:"namespaceDefault"`
			GroupID          *string `json:"groupId"`
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
				writeKubeClusterError(w, err)
				return
			}
			if !requireResourceManage(w, r, acc, access.KindKubeCluster, id) {
				return
			}
			newGroupID := strings.TrimSpace(*req.GroupID)
			if !requireGroupPlacement(w, r, acc, newGroupID) {
				return
			}
			fromGroupID = old.GroupID
			req.GroupID = &newGroupID
		}
		c, err := svc.Update(r.Context(), id, kube.UpdateInput{
			Name:             req.Name,
			CredentialID:     req.CredentialID,
			NamespaceDefault: req.NamespaceDefault,
			GroupID:          req.GroupID,
		})
		if err != nil {
			writeKubeClusterError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionKubeClusterUpdate,
			TargetType: audit.TargetKubeCluster,
			TargetID:   c.ID,
			Detail:     map[string]any{"name": c.Name, "endpoint": c.Endpoint},
			IP:         clientIP(r),
		})
		if req.GroupID != nil && fromGroupID != c.GroupID {
			recordAuditFromRequest(r, aud, ac, audit.Entry{
				Action:     audit.ActionKubeClusterReassign,
				TargetType: audit.TargetKubeCluster,
				TargetID:   c.ID,
				Detail:     map[string]any{"from": fromGroupID, "to": c.GroupID},
				IP:         clientIP(r),
			})
		}
		writeJSON(w, http.StatusOK, toKubeClusterDTO(c))
	}
}

// makeDeleteKubeClusterHandler 返回 DELETE /api/kube-clusters/{id} handler。
// 删除是归属级动作(流水线里的集群节点随之悬空),要求 ActManage。
func makeDeleteKubeClusterHandler(svc kube.Service, acc *access.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "集群服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		if acc != nil && !requireResourceManage(w, r, acc, access.KindKubeCluster, id) {
			return
		}
		if err := svc.Delete(r.Context(), id); err != nil {
			writeKubeClusterError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionKubeClusterDelete,
			TargetType: audit.TargetKubeCluster,
			TargetID:   id,
			IP:         clientIP(r),
		})
		w.WriteHeader(http.StatusNoContent)
	}
}

// makeTestKubeClusterHandler 返回 POST /api/kube-clusters/{id}/test handler。
// 实连集群取版本;失败映射为 200 + ok=false + 人读 error(绝不含 token/kubeconfig)。
func makeTestKubeClusterHandler(svc kube.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "集群服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		res, err := svc.Test(r.Context(), id)
		if err != nil {
			writeKubeClusterError(w, err)
			return
		}
		var errStr *string
		if res.Err != "" {
			s := res.Err
			errStr = &s
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionKubeClusterTest,
			TargetType: audit.TargetKubeCluster,
			TargetID:   id,
			Detail:     map[string]any{"ok": res.OK},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusOK, kubeClusterTestDTO{
			OK:        res.OK,
			LatencyMs: res.LatencyMs,
			Output:    res.Output,
			Error:     errStr,
		})
	}
}
