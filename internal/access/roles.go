// roles.go —— 功能轴:角色 × 资源类别 → 最高动作档位。
//
// 两轴分工(权威说明见 docs/权限架构说明.md):
//   - 本文件是功能轴:这个角色「允不允许做这类动作」,与数据归属无关,纯查表。
//   - access.go 的 Decide 是数据轴:这份数据归谁(未归组 / public / private + 组长 + 名册)。
//     一次判定取两者更严的一档,即 min(角色上限, 分组授予)。
//
// 只封 View / Operate 两档。ActManage 刻意不受角色上限约束:它判的是「谁拥有这份数据」
// 本身(改归属、删资源),组长靠它管自己的组。若让角色把 Manage 封掉,新建角色就会
// 静默夺走组长的既有管理权——那属于数据轴的事,不该由功能轴越权。
//
// 角色档位一律只「收窄」不「放宽」:内置的 user 对四类资源都是 Operate,与引入本表之前
// 的既有行为逐字一致(Manage 仍由 Decide 决定),所以存量账号升级后权限不变。
package access

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

// ceilings 是角色 → 各类资源的最高档位。缺角色的条目按最严一档处理(见 Ceiling)。
var ceilings = map[string]map[Kind]Act{
	RoleAdmin: {
		KindProject: ActManage, KindRun: ActManage, KindServer: ActManage, KindKubeCluster: ActManage,
	},
	RoleUser: {
		KindProject: ActOperate, KindRun: ActOperate, KindServer: ActOperate, KindKubeCluster: ActOperate,
	},
	RoleDeveloper: {
		KindProject: ActOperate, KindRun: ActOperate, KindServer: ActView, KindKubeCluster: ActView,
	},
	RoleOps: {
		KindProject: ActView, KindRun: ActOperate, KindServer: ActOperate, KindKubeCluster: ActOperate,
	},
	RoleViewer: {
		KindProject: ActView, KindRun: ActView, KindServer: ActView, KindKubeCluster: ActView,
	},
}

// 设置类入口(构建环境 / 配置资源 / 全局凭据 / 用户管理 / 审计)的功能位:
// 本期仍只有管理员。单独做成查表,是为了让「谁能进设置页」也落在同一处。
var settingsRoles = map[string]bool{RoleAdmin: true}

// NormalizeRole 把任意入参角色归一到枚举内:未知名一律按 RoleUser 处理,
// 空串保留为空(空串是 0053 迁移前旧会话的取值,语义是「按管理员」,不能改写成 user)。
func NormalizeRole(role string) string {
	if role == "" {
		return ""
	}
	if _, ok := ceilings[role]; !ok {
		return RoleUser
	}
	return role
}

// ValidRole 报告字串是否为枚举内的角色(不含旧会话的空串)。
func ValidRole(role string) bool {
	_, ok := ceilings[role]
	return ok
}

// Ceiling 返回角色对该类资源允许的最高档位。
//   - role 为旧部署会话的空串 → 按管理员上限(与 auth.Session.IsAdmin 的兼容口径一致,
//     否则升级当天旧管理员会话会被静默降成只读)。
//   - 未知角色 / 未知类别 → ActView(fail closed:认不出的东西一律先只让看)。
func Ceiling(role string, kind Kind) Act {
	if role == "" {
		role = RoleAdmin
	}
	row, ok := ceilings[role]
	if !ok {
		return ActView
	}
	if act, ok := row[kind]; ok {
		return act
	}
	return ActView
}

// SettingsAllowed 报告角色能否进入设置类入口。空串按管理员处理,同 Ceiling。
func SettingsAllowed(role string) bool {
	if role == "" {
		role = RoleAdmin
	}
	return settingsRoles[role]
}

// Capabilities 是回传给前端的角色能力位。前端据它决定菜单与控件是否出现,
// 但它只是展示层副本——服务端每个请求仍重新查表判定,不以它为准。
type Capabilities struct {
	// Settings 对应设置类入口(构建环境 / 配置资源 / 全局凭据 / 用户 / 审计)。
	Settings bool `json:"settings"`
	// Kinds 是每类资源的功能上限,值为 Act.String()("view" / "operate" / "manage")。
	Kinds map[string]string `json:"kinds"`
}

// ResourceKinds 是被判定资源的类别,顺序固定(能力位序列化与测试都按它走)。
func ResourceKinds() []Kind {
	return []Kind{KindProject, KindRun, KindServer, KindKubeCluster}
}

// CapabilitiesFor 从 ceilings 算出某角色的能力位。
func CapabilitiesFor(role string) Capabilities {
	kinds := ResourceKinds()
	out := make(map[string]string, len(kinds))
	for _, k := range kinds {
		out[string(k)] = Ceiling(role, k).String()
	}
	return Capabilities{Settings: SettingsAllowed(role), Kinds: out}
}
