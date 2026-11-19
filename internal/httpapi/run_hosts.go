package httpapi

import (
	"net/http"

	"github.com/fuuhoo/pipewright/internal/access"
	"github.com/fuuhoo/pipewright/internal/project"
	"github.com/fuuhoo/pipewright/internal/target"
	"github.com/go-chi/chi/v5"
)

// runHostDTO 是手动触发弹窗的候选主机(id 供提交,label 直接渲染)。
type runHostDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type runHostsDTO struct {
	Hosts []runHostDTO `json:"hosts"`
}

// makeRunHostsHandler 返回 GET /api/projects/{id}/run-hosts:手动触发「主机」下拉的候选。
//
// 候选 = 该(库内)流水线部署节点勾选过的主机 ∩ 本次请求可见的主机,按流水线声明顺序。
// 走配置口径而不是运行历史:没跑过也能选;项目卡「目标服务器」摘要用的是同一份解析,
// 两处显示不会互相打架。流水线即代码(仓库 .pipewright.yml)的落点不在这里 —— 开个弹窗
// 不值得克隆仓库;那种项目本地没有配置就没有候选,面板照常可触发(主机本就是可选项)。
func makeRunHostsHandler(projects project.Service, servers target.Service, acc *access.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if projects == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "项目服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		if _, err := projects.Get(r.Context(), id); err != nil {
			writeProjectError(w, err)
			return
		}
		// 主机服务缺席/不可见 → 空候选(下拉退化成只有「不指定」),不阻断触发。
		visible, err := visibleGroups(r, acc)
		if err != nil {
			writeAccessError(w, err)
			return
		}
		names := serverNamesByID(r.Context(), servers, visible)
		ids, err := projects.PipelineServerIDs(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
			return
		}
		hosts := make([]runHostDTO, 0, len(ids))
		for _, sid := range ids {
			if name := names[sid]; name != "" {
				hosts = append(hosts, runHostDTO{ID: sid, Name: name})
			}
		}
		writeJSON(w, http.StatusOK, runHostsDTO{Hosts: hosts})
	}
}

// resolveManualRunHost 校验手动触发选定的主机并返回展示名快照。
//
// 只接受「该流水线部署节点配置过 + 本次请求可见」的主机;其余一律 false(调用方回 422)。
// 名字取触发时刻快照:机器改名/删除后历史运行仍能显示当时的机器名。
func resolveManualRunHost(r *http.Request, projects project.Service, servers target.Service, acc *access.Service, projectID, serverID string) (string, bool) {
	if projects == nil || servers == nil {
		return "", false
	}
	ids, err := projects.PipelineServerIDs(r.Context(), projectID)
	if err != nil {
		return "", false
	}
	configured := false
	for _, sid := range ids {
		if sid == serverID {
			configured = true
			break
		}
	}
	if !configured {
		return "", false
	}
	visible, err := visibleGroups(r, acc)
	if err != nil {
		return "", false
	}
	name := serverNamesByID(r.Context(), servers, visible)[serverID]
	return name, name != ""
}
