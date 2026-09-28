// Package vault 凭据保险库 — RBAC 层(v6.2 §3.4, §5.7)。
//
// Actor 是当前操作者身份。vault.Service.List/Get/Update/Delete/Create 接受 Actor
// 参数;Actor=nil 视为系统调用(等同于 admin,内部维护/health check 等场景)。
//
// 注意:本文件只定义身份与纯过滤逻辑;SQL 侧的 actor-aware 方法在 vault.go。
// 0058_credential_owner 已落地 owner_id/description/enabled/disabled_by/disabled_at/
// created_by 列,因此 ListWithActor/DisableWithActor 走完整 SQL(不再有兼容分支)。
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
	IncludeGlobal   bool   // true=返回 scope='global' 凭据
	IncludePersonal bool   // true=返回 scope='personal' 凭据
	OwnerID         string // 当 IncludePersonal=true 时:若非空则限定 owner_id
	IncludeDisabled bool   // false=仅 enabled=1
}

// effectiveFilter 据 Actor 推导安全的 ListFilter:
//   - Actor=nil(系统调用) → 全开(global+所有 personal)
//   - Actor.Role="admin"  → 全开(请求什么给什么)
//   - Actor.Role="user"   → global(只读元数据)+ 自己的 personal;别人的 personal 永远不出现
//
// 为什么 global 不给普通用户藏起来:存量部署里的凭据**全是** global(0058 之前压根
// 没有 owner_id 列),项目/服务器配置都引用它们。藏起来会让非管理员的项目页显示成
// 「未选择凭据」并无法再选回来——这不是收紧权限,是把老数据打死。列表只出掩码,
// 引用不到明文;写/删/读明文在 authorize* 与 RequireAdmin 那几道仍然拒。
func effectiveFilter(actor *Actor, requested ListFilter) ListFilter {
	if actor == nil || actor.IsAdmin() {
		return requested
	}
	return ListFilter{
		IncludeGlobal:   requested.IncludeGlobal,
		IncludePersonal: requested.IncludePersonal,
		OwnerID:         actor.UserID, // 强制覆写:忽略调用方传的别人的 id
		IncludeDisabled: requested.IncludeDisabled,
	}
}

// ErrAccessDenied 是普通用户越权访问他人 personal 凭据时的错误。
// 由 Get/GetGitAuth/Reveal/Update/Delete 在 actor 非 admin 且凭据 owner 不匹配 actor.UserID 时返回。
var ErrAccessDenied = errors.New("vault: access denied")

// ErrOwnerRequired 表示创建 scope='personal' 的凭据却没给 owner_id。
// 个人凭据没有归属就等于没人能看见它,因此在入参校验阶段直接拒,而不是静默落成 global。
var ErrOwnerRequired = errors.New("vault: personal credential requires owner_id")

// ErrDisabledCredential 表示凭据已被管理员禁用,不再可用。
var ErrDisabledCredential = errors.New("vault: credential disabled")

// ErrForbidden 是普通用户试图访问/写入 global 凭据、或非管理员尝试 disable 凭据时的错误。
var ErrForbidden = errors.New("vault: forbidden")
