package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/fuuhoo/pipewright/internal/access"
	"github.com/fuuhoo/pipewright/internal/group"
	"github.com/fuuhoo/pipewright/internal/project"
	"github.com/fuuhoo/pipewright/internal/run"
	"github.com/fuuhoo/pipewright/internal/target"
)

// access_guard.go —— v6.2 分组权限的 HTTP 收口。
//
// 为什么用中间件而不是逐个 handler 加判定:/api/projects/{id}/* 与 /api/runs/{id}/*
// 合计三十多条路由,分散判定必然漏(漏一条就是一个越权口子)。这里按 URL 形态一次性
// 认出「这条请求在动哪个资源」,新增路由自动被覆盖。
//
// 档位映射只有一条规则:读 → ActView,写 → ActOperate。
// 更高档的 ActManage(改组名/名册/归组)不在这条规则里,由各自的 handler 显式判定,
// 因为它还需要校验「新旧两侧的分组都能管」。

// accessActorFromRequest 把会话换算成分组权限判定用的 Actor。
// 取不到会话返回 false:绝不能退化成「nil = 系统调用 = 放行」——那是 vault 包的约定,
// access 包对 nil actor 一律 fail closed。
func accessActorFromRequest(r *http.Request) (*access.Actor, bool) {
	sess, ok := sessionFromContext(r.Context())
	if !ok || sess == nil || sess.UserID == "" {
		return nil, false
	}
	return &access.Actor{UserID: sess.UserID, Username: sess.Username, Role: sess.Role,
		GroupScope: tokenGroupScopeFrom(r)}, true
}

// tokenGroupScopeFrom 取这把 API 令牌端点的分组范围。
// 浏览器会话(以及不限组的令牌)返回 nil = 不限 —— 范围是令牌的属性,不是人的属性。
func tokenGroupScopeFrom(r *http.Request) []string {
	ids, _ := r.Context().Value(contextKeyTokenGroups).([]string)
	return ids
}

// guardTarget 是从 URL 认出来的被保护资源。
type guardTarget struct {
	kind access.Kind
	id   string
}

// resolveGuardTarget 解析 /api/... 路径的首段 + 资源 ID。
// ok=false 表示这条路径不归分组权限管(列表、创建、test-clone 等)。
func resolveGuardTarget(path string) (guardTarget, bool) {
	segs := strings.Split(strings.Trim(path, "/"), "/") // ["api", "projects", "{id}", ...]
	if len(segs) < 3 || segs[0] != "api" {
		return guardTarget{}, false
	}
	id := segs[2]
	switch segs[1] {
	case "projects":
		// POST /api/projects(创建)与 test-clone 都没有既成资源可判。
		if id == "test-clone" {
			return guardTarget{}, false
		}
		return guardTarget{kind: access.KindProject, id: id}, true
	case "runs":
		return guardTarget{kind: access.KindRun, id: id}, true
	case "servers":
		// 聚合总览是字面段(整表可见范围过滤,见 makeAllServer*Handler),不能被当成服务器 ID。
		if id == "metrics" || id == "containers" {
			return guardTarget{}, false
		}
		return guardTarget{kind: access.KindServer, id: id}, true
	case "kube-clusters":
		return guardTarget{kind: access.KindKubeCluster, id: id}, true
	default:
		return guardTarget{}, false
	}
}

// accessGuardMiddleware 返回挂在 /api 组上的分组权限中间件。
// svc 为 nil(未装配权限仓储)时返回直通中间件:单机演示与既有测试不受影响,
// 生产装配见 cmd/pipewright/main.go——那里恒定注入。
func accessGuardMiddleware(svc *access.Service) func(http.Handler) http.Handler {
	if svc == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			target, ok := resolveGuardTarget(r.URL.Path)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			actor, ok := accessActorFromRequest(r)
			if !ok {
				writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
				return
			}
			act := access.ActView
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
			default:
				act = access.ActOperate
			}
			if err := svc.Can(r.Context(), actor, target.kind, target.id, act); err != nil {
				writeAccessError(w, err)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// writeAccessError 把权限/归属错误映射为契约错误码。顺序敏感:
// 资源不存在(404)要排在 403 之前,否则「项目不存在」会被伪装成无权限。
func writeAccessError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, access.ErrUnauthenticated):
		writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
	case errors.Is(err, group.ErrNotFound), errors.Is(err, project.ErrNotFound), errors.Is(err, run.ErrNotFound), errors.Is(err, target.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "资源不存在")
	case errors.Is(err, access.ErrGroupNotFound):
		// 资源指向已删的分组:按 fail closed 挡下了,但原因要说清楚,便于管理员重新归组。
		writeError(w, http.StatusForbidden, "group_missing", "该资源指向一个已不存在的分组,请管理员重新归组")
	case errors.Is(err, access.ErrRoleCeiling):
		// 功能轴拦下的:与归属无关,提示「找管理员提角色」而不是「去申请加入分组」。
		writeError(w, http.StatusForbidden, "role_denied", "你的角色不允许这类操作,需要管理员调整角色")
	case errors.Is(err, access.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "无权访问:该资源属于私有分组,你不在名册里")
	default:
		writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
	}
}

// 落点这条线的功能点 ID(internal/access/perms.go 的字典项)。写成常量是因为守卫散在三处
// handler 里,手抄错一个字母不会报错,只会让那个入口对所有人静默消失(或静默放开)。
const (
	permServerExec       = "server.exec"
	permContainerOperate = "container.operate"
)

// requireServerOperate 判定 actor 的角色档位能否在那台服务器上「动得起来」(KindServer 上限)。
//
// 中间件对 GET 一律只判到 ActView,而这里的语义是操作而非查看;WS 升级后也无法再回写 HTTP
// 状态码,必须在握手之前挡下。证书发布复用这一档:它往目标机写私钥并跑用户填的指令,
// 权限方向不该比终端更低,也不该更高(见 certs.go)。
func requireServerOperate(w http.ResponseWriter, r *http.Request, svc *access.Service, serverID string) bool {
	if svc == nil {
		return true
	}
	actor, ok := accessActorFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
		return false
	}
	if err := svc.Can(r.Context(), actor, access.KindServer, serverID, access.ActOperate); err != nil {
		writeAccessError(w, err)
		return false
	}
	return true
}

// requireServerPoint 判定 actor 能否在那台服务器上行使**某个功能点**(按点判,而非按类别上限)。
//
// 与 requireServerOperate 的分工:上限那条线只要角色在 KindServer 下持有任一 operate 点就放行,
// 表达不了「给了容器操作、明确关掉主机终端」这种单条差异。终端与远程文件都属于「坐到那台机器
// 前面」,所以各自钉死自己的点(server.exec / container.operate),归属那把尺仍由 CanPoint 内部的
// Can 叠加(数据轴照旧生效)。
//
// svc == nil(未装配权限仓储,单机演示)时直通 —— 与中间件、requireServerOperate 同一条策略。
func requireServerPoint(w http.ResponseWriter, r *http.Request, svc *access.Service, serverID, pointID string) bool {
	if svc == nil {
		return true
	}
	actor, ok := accessActorFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
		return false
	}
	if err := svc.CanPoint(r.Context(), actor, pointID, access.KindServer, serverID, access.ActOperate); err != nil {
		writeAccessError(w, err)
		return false
	}
	return true
}

// visibleGroups 算出 actor 的资源列表可见分组范围(项目 / 服务器共用:运行与服务器都挂在
// 同一套分组语义上)。未装配权限服务时返回「不受限」(与中间件同策略)。
func visibleGroups(r *http.Request, svc *access.Service) (access.ListFilter, error) {
	if svc == nil {
		return access.ListFilter{Unrestricted: true}, nil
	}
	actor, ok := accessActorFromRequest(r)
	if !ok {
		return access.ListFilter{}, access.ErrUnauthenticated
	}
	return svc.VisibleGroups(r.Context(), actor)
}

// requireResourceManage 校验 actor 能否「管理」某个既成资源(改归属 / 删除)。
// 管理档 = 管理员或该资源所属分组的组长;未归组资源没有组长这一说,只有管理员能管。
func requireResourceManage(w http.ResponseWriter, r *http.Request, svc *access.Service, kind access.Kind, id string) bool {
	if svc == nil {
		return true
	}
	actor, ok := accessActorFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
		return false
	}
	if err := svc.Can(r.Context(), actor, kind, id, access.ActManage); err != nil {
		writeAccessError(w, err)
		return false
	}
	return true
}
