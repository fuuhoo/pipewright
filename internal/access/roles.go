// roles.go —— 功能轴:角色 → 资源类别的最高动作档位,以及回传给前端的能力位。
//
// 两轴分工(权威说明见 docs/权限架构说明.md):
//   - 功能轴回答「这个角色允不允许做这类动作」,与数据归属无关。它的权威表是 perms.go 的
//     角色 → 功能点集;本文件的 Ceiling 从那张表**派生**,所以菜单与请求上限永远同口径。
//   - 数据轴(access.go 的 Decide)回答「这份数据归谁」(未归组 / public / private + 组长 + 名册)。
//     一次判定取两者更严的一档,即 min(角色上限, 分组授予)。
//
// 只封 View / Operate 两档。ActManage 刻意不受功能轴约束:它判的是「谁拥有这份数据」
// 本身(改归属、删资源),组长靠它管自己的组。若让功能轴把 Manage 封掉,改一次菜单就会
// 静默夺走组长的既有管理权——那属于数据轴的事,不该由功能轴越权。
//
// 档位一律只「收窄」不「放宽」:内置的 user 对四类资源都是 Operate,与引入档位表之前既有
// 行为逐字一致(Manage 仍由 Decide 决定),所以存量账号升级后权限不变。
package access

import "slices"

// 角色枚举。字串取值与 users.role / sessions.role 完全一致(该列没有 CHECK 约束,
// 新增角色不需要重建表的迁移)。
const (
	// RoleUser 是平台默认角色:四类资源都能动(功能轴不封顶),归属仍由分组决定。
	RoleUser = "user"
	// RoleDeveloper 流水线开发者:能改项目与跑运行,但不能碰主机 / 集群这类「部署落点」。
	RoleDeveloper = "developer"
	// RoleOps 运维:能操作主机 / 集群,能对既有运行做审批 / 部署 / 重试 / 回滚,
	// 但不改项目配置。手动触发一条流水线走 POST /projects/{id}/runs,归 project 档,
	// 因此 ops 不含「发起构建」——要放开得把 Kind 拆细,留到后续。
	RoleOps = "ops"
	// RoleViewer 只读:能看列表 / 详情 / 日志 / 报告,任何写请求都 403。
	RoleViewer = "viewer"
)

// Roles 是角色枚举的展示顺序(前端下拉照这个顺序列,不再各自硬编名单)。
func Roles() []string {
	return []string{RoleAdmin, RoleUser, RoleDeveloper, RoleOps, RoleViewer}
}

// NormalizeRole 把任意入参角色归一到枚举内:未知名一律按 RoleUser 处理,
// 空串保留为空(空串是 0053 迁移前旧会话的取值,语义是「按管理员」,不能改写成 user)。
func NormalizeRole(role string) string {
	if role == "" {
		return ""
	}
	if !ValidRole(role) {
		return RoleUser
	}
	return role
}

// Ceiling 返回角色对该类资源允许的最高档位,由 perms.go 的角色点集算出来。
//   - 认不出的类别 → ActView:先只让看(fail closed),管理员也不例外 —— 新加 Kind 却没配点时,
//     它不该顺手继承「什么都允许」。
//   - 空串(旧部署会话)与管理员 → ActManage,即功能轴不拦任何动作。
//   - 未知角色 / 该类资源一个点都没有 → ActView。
func Ceiling(role string, kind Kind) Act {
	if !slices.Contains(ResourceKinds(), kind) {
		return ActView
	}
	if role == "" || role == RoleAdmin {
		return ActManage
	}
	perms, ok := rolePerms[role]
	if !ok {
		return ActView
	}
	// 取该类别下所有点的最高档。Act 从 1 起(View),0 是哨兵「这类一个点都没有」。
	best := Act(0)
	for _, id := range perms {
		p, known := permByID[id]
		if !known || p.Kind != kind || p.Act == ActManage {
			continue
		}
		if p.Act > best {
			best = p.Act
		}
	}
	if best < ActView {
		return ActView
	}
	return best
}

// SettingsAllowed 报告角色能否进入设置类入口(构建环境 / 配置资源 / 全局凭据 / 用户管理 / 审计)。
// 就是「有没有那个点」;空串按管理员口径保留放行,免得升级当天把管理员旧会话锁在门外。
func SettingsAllowed(role string) bool { return HasPerm(role, PermSettingsAccess) }

// Capabilities 是回传给前端的角色能力位。前端据它决定菜单与控件是否出现,
// 但它只是展示层副本——服务端每个请求仍重新查表判定,不以它为准。
type Capabilities struct {
	// Settings 对应设置类入口(构建环境 / 配置资源 / 全局凭据 / 用户 / 审计)。
	Settings bool `json:"settings"`
	// Kinds 是每类资源的功能上限,值为 Act.String()("view" / "operate" / "manage")。
	// 粒度粗(四类资源各一档),适合「按钮能不能点」;入口级的差异看 Perms。
	Kinds map[string]string `json:"kinds"`
	// Perms 是该角色的功能点集(见 perms.go),前端据此决定左栏入口与路由落不落得进来。
	// 与 Kinds 同源:两个都是 perms.go 那张表的投影,不存在「菜单亮着、请求被上限拦掉」的错位。
	Perms []string `json:"perms"`
}

// ResourceKinds 是被判定资源的类别,顺序固定(能力位序列化与测试都按它走)。
func ResourceKinds() []Kind {
	return []Kind{KindProject, KindRun, KindServer, KindKubeCluster}
}

// CapabilitiesFor 从角色点集算出要回传的能力位。
func CapabilitiesFor(role string) Capabilities {
	kinds := ResourceKinds()
	out := make(map[string]string, len(kinds))
	for _, k := range kinds {
		out[string(k)] = Ceiling(role, k).String()
	}
	return Capabilities{Settings: SettingsAllowed(role), Kinds: out, Perms: PermsFor(role)}
}
