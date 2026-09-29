// roles.go —— 功能轴:角色 → 资源类别的最高动作档位,以及回传给前端的能力位。
//
// 两轴分工(权威说明见 docs/权限架构说明.md):
//   - 功能轴回答「这个角色允不允许做这类动作」,与数据归属无关。它的权威点集有两处来源:
//     内置档(只剩 admin)在 perms.go 的代码表里,是只读模板;其余角色 —— 包括 0063 从代码表
//     搬进库的四档预置(user / developer / ops / viewer)—— 都在 roles / role_perms 两张表里,
//     由 catalog.go 缓存。本文件的 Ceiling 从「先代码表、再缓存」查到的点集**派生**,
//     所以菜单与请求上限永远同口径。
//   - 数据轴(access.go 的 Decide)回答「这份数据归谁」(未归组 / public / private + 组长 + 名册)。
//     一次判定取两者更严的一档,即 min(角色上限, 分组授予)。
//
// 只封 View / Operate 两档。ActManage 刻意不受功能轴约束:它判的是「谁拥有这份数据」
// 本身(改归属、删资源),组长靠它管自己的组。若让功能轴把 Manage 封掉,改一次菜单就会
// 静默夺走组长的既有管理权——那属于数据轴的事,不该由功能轴越权。
//
// 档位一律只「收窄」不「放宽」:点集为空或角色认不出 → 只给 View,绝不顺手放行。
// 存量账号的权限在 0063 之后不变,靠的是那四档的点集被逐字搬进了库(见 0063 的守卫测试),
// 而不是靠代码表兜底 —— 库里读不到角色时它和任意陌生 id 一样落 View。
package access

import "slices"

// 角色枚举。字串取值与 users.role / sessions.role 完全一致(该列没有 CHECK 约束,
// 新增角色不需要重建表的迁移)。
const (
	// RoleUser 是**平台默认角色的 id**,不是内置模板:0063 迁移把它连同 developer / ops / viewer
	// 一起播种进 roles 表,点集从此由管理员在页面上改。常量留着是因为建号时角色留空要落它,
	// 而它的字串 'user' 与库里那行的 id 逐字相同 —— 存量账号的 users.role 一次都不用动。
	RoleUser = "user"
)

// Roles 是**内置模板档**的展示顺序(自定义角色由服务端名单另发,页面把它们排在内置档之后)。
// 内置只剩管理员一档:它不可改删,兜住「管理员不会把自己锁在门外」。
func Roles() []string {
	return []string{RoleAdmin}
}

// IsBuiltinRole 报告该 id 是否为内置档。内置档是「模板」:设置页不许改删,库里也不许出现
// 同名 id 的自定义行顶掉它(见 catalog.go 的 ReloadRoles)。
func IsBuiltinRole(id string) bool { return isBuiltinRole(id) }

// NormalizeRole 把任意入参角色归一到枚举内:未知名一律按 RoleUser 处理,
// 空串保留为空(空串是 0053 迁移前旧会话的取值,语义是「按管理员」,不能改写成 user)。
//
// 只给展示与报错文案用,别把它接进判定:它认不出自定义角色 id 时会退成 user,
// 那是「读起来顺」的口径而不是「权限够」的口径。真正的判定读 Ceiling / HasPerm,
// 认不出的角色一律 fail closed。
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
	perms, ok := pointsFor(role)
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
