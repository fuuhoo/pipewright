// Package vault 凭据保险库 — RBAC 层(v6.2 §3.4, §5.7)。
//
// Actor 是当前操作者身份。vault.Service.List/Get/Update/Delete/Create 接受 Actor
// 参数;Actor=nil 视为系统调用(等同于 admin,内部维护/health check 等场景)。
//
// 注意:本文件 Stage 5 仅定义接口与纯过滤逻辑。SQL 接口签名改造留到
// Stage 8/9 httpapi handler 重构时一次性推进。当前阶段 0053_credentials_owner
// 迁移尚未重做,credentials 表不含 owner_id/enabled/disabled_by/description/
// created_by 列,ListWithActor/DisableWithActor 的 SQL 走「兼容版本」(仅按 scope
// 过滤,owner_id/enabled 等字段在 Credential 视图里填空)。待 0053 重做后再切回
// 完整 SQL(逻辑分支已用单元测试覆盖)。
package vault

import "errors"

// Actor 表示当前操作者身份。
//   - Role="admin" → 可看 global 凭据 + 自己的 personal;可禁用他人 personal
//   - Role="user"  → 只能看自己的 personal
//   - nil Actor     → 视为 admin(系统调用)
type Actor struct {
	UserID string // users.id(UUID v4)
	Role   string // "admin" | "user"
}

// IsAdmin 判断 Actor 是否管理员。
//   - nil Actor → true(系统调用视为 admin)
//   - Role=="admin" → true
//   - 其余 → false
func (a *Actor) IsAdmin() bool {
	if a == nil {
		return true
	}
	return a.Role == "admin"
}

// ListFilter 按 scope+owner 过滤凭据列表。
type ListFilter struct {
	IncludeGlobal   bool // true=返回 scope='global' 凭据
	IncludePersonal bool // true=返回 scope='personal' 凭据
	OwnerID         string // 当 IncludePersonal=true 时:若非空则限定 owner_id
	IncludeDisabled bool // false=仅 enabled=1
}

// effectiveFilter 据 Actor 推导安全的 ListFilter:
//   - Actor=nil(系统调用) → 全开(global+所有 personal)
//   - Actor.Role="admin"  → 全开
//   - Actor.Role="user"   → 仅自己的 personal(无视 IncludeGlobal=true 的请求)
func effectiveFilter(actor *Actor, requested ListFilter) ListFilter {
	if actor == nil || actor.IsAdmin() {
		return requested
	}
	// 普通用户:只能看自己的 personal,即便调用方指定 IncludeGlobal=true 也被强制覆盖
	return ListFilter{
		IncludePersonal: true,
		OwnerID:         actor.UserID,
		IncludeDisabled: requested.IncludeDisabled,
	}
}

// ErrAccessDenied 是普通用户越权访问他人 personal 凭据时的错误。
// 由 Get/GetGitAuth/Reveal/Update/Delete 在 actor 非 admin 且凭据 owner 不匹配 actor.UserID 时返回。
var ErrAccessDenied = errors.New("vault: access denied")

// ErrForbidden 是普通用户试图访问/写入 global 凭据、或非管理员尝试 disable 凭据时的错误。
var ErrForbidden = errors.New("vault: forbidden")
