// Package auth — RBAC 中间件签名(v6.2 §3.4 + 阶段 8 接入)。
//
// 本文件**只定义中间件签名与角色判定纯函数**;实际接入 router(挂到
// /api/admin/* 与 /api/* 端点)留到阶段 8 internal/httpapi.RequireAdmin 抽出。
//
// 设计要点(v6.2 §3.4 + 阶段 6 会话扩展):
//   - Session.Role ∈ {"admin","user"} + 旧部署兼容 '' (按 admin 放行)
//   - ActorFromSession 把 *Session 转成 *vault.Actor(供 vault 包 RBAC 调用复用)
//
// 中间件约定:
//   - RequireAuth → 已有 requireAuth(读 cookie → svc.Verify → 注入 Session 到 ctx)
//   - RequireAdmin → 必须 Session.IsAdmin();否则 403。
//     旧会话(role='')按 admin 放行,避免升级锁死旧部署的管理员。
//   - RequireUser  → Session.Role=="user" 或 admin(普通用户端点;admin 可访问作管理调试)。
//     普通用户端点不接受未登录。
package auth

// ActorFromSession 把 Session 转成 vault.Actor 的字段映射(v6.2 §5.7)。
//
// 返回值 nil 表示「会话无 user_id」(理论不应发生:Login 必填;但需兼容旧会话行)。
// 调用方使用方式:
//
//	actor := auth.ActorFromSession(sess)
//	vaultSvc.ListWithActor(actor, filter)
//
// 注意:本函数不 import vault 包以避免循环依赖(auth → vault → ...)。vault.Actor
// 是 {UserID, Role} 双字段结构体(见 internal/vault/rbac.go),调用方按字段组装即可:
//
//	import "github.com/huangchengsir/pipewright/internal/vault"
//	actor := &vault.Actor{UserID: sess.UserID, Role: sess.Role}
//
// 本文件提供 IsAdmin / Role 判定供中间件直接复用。
const (
	// RoleAdmin 管理员角色。
	RoleAdmin = "admin"
	// RoleUser 普通用户角色。
	RoleUser = "user"
)

// IsAdminSession 报告会话是否为管理员(供中间件判定)。
//   - nil → false
//   - Role=="" → true(旧部署兼容;旧会话升级后第一次登出重登会获得明确 Role)
//   - Role=="admin" → true
//   - 其它 → false
func IsAdminSession(s *Session) bool {
	return s != nil && s.IsAdmin()
}

// IsUserSession 报告会话是否为已登录用户(admin 或 user)。中间件 RequireUser 用。
func IsUserSession(s *Session) bool {
	if s == nil {
		return false
	}
	return s.Role == RoleAdmin || s.Role == RoleUser
}
