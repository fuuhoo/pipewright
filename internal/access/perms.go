// perms.go —— 功能轴的权威表:角色 → 功能点。
//
// 为什么要在「资源类别 × 档位」之外再加一层点(见 roles.go):左栏是十几条具体入口,而多条
// 入口共用同一个类别 —— 服务器状态、容器总览、证书大盘、异常检测都算 KindServer。按类别开关
// 表达不了「运维要看主机指标,但不必看到复用库」这种单条差异;点补的就是这层缺失的粒度。
//
// 一张表两用,不养第二套判据:
//   - 菜单 / 按钮:点集随会话回在 Capabilities.Perms,前端据此决定入口出现与否。
//   - 请求上限:roles.go 的 Ceiling 由本表的点集算出来,所以「入口亮了」与「这个写请求 403」
//     不可能各说一套;RequireAdmin 读的也是本表(设置点)。
//
// 存量账号的档位一格不许变:内置 admin 由 TestCeilingMatrixUnchanged 逐格钉住,四档预置
// 自 0063 起点的是迁移里的种子(见 roles_drift_test.go 的 TestSeededPresetCeilings)。
//
// 点分两类:
//   - 资源点:Kind 取四类资源之一,Act 取 view / operate。它声明「要看得到 / 按得动这个,
//     角色对该类资源至少要哪一档」,派生 Ceiling 只看它。
//   - 平台点(Kind == Platform):平台设置类入口(全局凭据 / 审计 / 通知 / AI 等一组),
//     不吃四类上限,由设置位这条线兜。
//
// Act 刻意不到 manage:manage 判的是「谁拥有这份数据」(改归属、删资源),归分组那条数据轴管。
// 让功能点能封住 manage,就等于让改菜单顺带夺走组长的既有管理权 —— 组长哪怕角色是只读,
// 也照样管得了自己组的名册与归属。
package access

import "slices"

// Perm 是一个功能点的声明。
type Perm struct {
	// ID 是回传给前端的稳定字符串(形如 "container.operate")。它跟能力走,不跟页面走:
	// 入口改名、页面合并都不该改动权限语义,所以 ID 里不出现路由路径。
	ID string
	// Kind 是该点消耗哪条功能轴上限;Platform 表示平台设置类,不参与上限派生。
	Kind Kind
	// Act 是需要的最低档位。只允许 View / Operate(见包注释)。
	Act Act
}

// Platform 是「平台设置类」点的类别标记。它不在 ResourceKinds() 里,因此永远不会成为
// Can 的入参 —— 资源归属判定仍然只对四类真实资源发生。
const Platform = Kind("platform")

// PermSettingsAccess 是进得了设置那一组的总闸(全局凭据 / 保险库 / 审计 / 通知 / AI / DNS /
// 系统信息 / 服务器与集群登记)。这一组本期不再逐页拆点:它们的端点本来就统一收在
// RequireAdmin 后面,拆了只会多出一堆「要么全给要么全不给」的伪粒度。
const PermSettingsAccess = "settings.access"

// permDict 是全部功能点。顺序即序列化顺序与前端列表顺序,固定下来便于逐条评审。
var permDict = []Perm{
	// —— 编排:项目、流水线、触发器、复用库同属「改得了编排」这一条上限 ——
	{ID: "dashboard.view", Kind: KindProject, Act: ActView},
	{ID: "project.view", Kind: KindProject, Act: ActView},
	{ID: "project.edit", Kind: KindProject, Act: ActOperate},
	{ID: "library.view", Kind: KindProject, Act: ActView},
	// 含自定义节点工作室:模板与变量组的增删改是同一种动作,不另开点。
	{ID: "library.edit", Kind: KindProject, Act: ActOperate},

	// —— 运行:环境历史与 DORA 都是从运行算出来的聚合视图 ——
	{ID: "run.view", Kind: KindRun, Act: ActView},
	{ID: "run.operate", Kind: KindRun, Act: ActOperate},
	{ID: "environments.view", Kind: KindRun, Act: ActView},
	{ID: "metrics.dora.view", Kind: KindRun, Act: ActView},

	// —— 落点:主机及其上的容器 / 证书 / 预览 / 告警 ——
	{ID: "server.view", Kind: KindServer, Act: ActView},
	// 主机终端与远程文件是同一件事(坐到那台机器前面),不拆两点。
	{ID: "server.exec", Kind: KindServer, Act: ActOperate},
	{ID: "container.view", Kind: KindServer, Act: ActView},
	// 容器生命周期与镜像清理同档:能重启容器就该能删悬空镜像。
	{ID: "container.operate", Kind: KindServer, Act: ActOperate},
	{ID: "cert.view", Kind: KindServer, Act: ActView},
	{ID: "preview.view", Kind: KindServer, Act: ActView},
	{ID: "preview.recycle", Kind: KindServer, Act: ActOperate},
	{ID: "anomaly.view", Kind: KindServer, Act: ActView},
	{ID: "anomaly.edit", Kind: KindServer, Act: ActOperate},

	// —— 落点:K8s 集群 ——
	{ID: "cluster.view", Kind: KindKubeCluster, Act: ActView},
	{ID: "cluster.operate", Kind: KindKubeCluster, Act: ActOperate},

	// —— 平台 ——
	{ID: PermSettingsAccess, Kind: Platform, Act: ActView},
}

// permByID 是字典的索引;init 里建,拼错 ID 在测试阶段就会暴露(见 TestPermDictSane)。
var permByID = func() map[string]Perm {
	m := make(map[string]Perm, len(permDict))
	for _, p := range permDict {
		m[p.ID] = p
	}
	return m
}()

// rolePerms 是**内置模板角色** → 功能点集。今天只剩 admin 一档:其余四档(user / developer /
// ops / viewer)已由 0063 迁移搬进 roles / role_perms 两张表,与自定义角色同一条路走
// (catalog.go 缓存),管理员可以改名、改点集、删掉。
//
// admin 刻意留在代码里:它是「管理员不会把自己锁在门外」的兜底 —— 库里的行改坏了、删掉了、
// 甚至整张表读不出来,这一档还得在。这也是本表只留一行的原因:多留一行,就多一处
// 「页面上改不动、只能发版改代码」的角色。
//
// 名单按「这个角色是做什么的」写,不从档位反推 —— 反推出来的表只能复述今天,加不了粒度。
// 两张口径的一致性(档位矩阵不许漂)由测试钉住。
var rolePerms = map[string][]string{
	// 管理员:全部点。它同时短路 Ceiling(直接给到 manage),见 roles.go。
	RoleAdmin: permIDs(),
}

// permIDs 返回字典里所有点的 ID(声明序)。
func permIDs() []string {
	out := make([]string, 0, len(permDict))
	for _, p := range permDict {
		out = append(out, p.ID)
	}
	return out
}

// Perms 返回功能点字典的副本(声明序),供设置页渲染勾选项与角色编辑器分组。
// 返回副本是因为调用方是 HTTP 层:把内部切片交出去,一次就地排序就会改掉全进程的序列化顺序。
func Perms() []Perm {
	out := make([]Perm, len(permDict))
	copy(out, permDict)
	return out
}

// KnownPerm 报告点 ID 是否在字典里(自定义角色写路径用它挡掉前端过期名单)。
func KnownPerm(id string) bool {
	_, ok := permByID[id]
	return ok
}

// ValidRole 报告字串是否为可用角色:内置档(代码表)或已装载的自定义角色(库里)。
// 不含旧会话的空串。users 建号与改角色、RequireUser 的会话判据都走它 —— 自定义角色
// 必须在这里被认下来,否则 NormalizeRole 会把它们统统折成 user(静默改档位)。
func ValidRole(role string) bool {
	_, ok := pointsFor(role)
	return ok
}

// HasPerm 报告角色是否持有某个功能点。空串按管理员口径(旧会话不该因为升级当天少一块菜单),
// 未知角色一律 false(fail closed:认不出的东西先什么都别给)。
func HasPerm(role, id string) bool {
	if role == "" {
		return true
	}
	perms, ok := pointsFor(role)
	if !ok {
		return false
	}
	return slices.Contains(perms, id)
}

// PermsFor 返回角色的功能点集(字典序,便于序列化与测试比对)。
//   - 空串 / 管理员 → 全字典。
//   - 未知角色 → 空集;前端据此把所有挂门的入口收起,一个都不亮。
func PermsFor(role string) []string {
	if role == "" || role == RoleAdmin {
		return permIDs()
	}
	perms, ok := pointsFor(role)
	if !ok {
		return []string{}
	}
	// 回传前按字典过滤一遍:手抄错一个 ID 不该让前端拿到一个谁也不认的字符串。
	out := make([]string, 0, len(perms))
	for _, id := range permIDs() {
		if slices.Contains(perms, id) {
			out = append(out, id)
		}
	}
	return out
}
