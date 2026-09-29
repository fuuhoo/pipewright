/**
 * roles.ts —— 功能轴在前端的只读视图。
 *
 * 权威判据在 internal/access:内置 admin 的点集是 perms.go 的代码表,其余角色(含四档预置)
 * 的点集在 roles / role_perms 两张表里。后端把它投影成两样东西回在 capabilities 里 ——
 * 每类资源的档位上限(kinds)与该角色的功能点(perms),这里只负责「怎么用它」:
 *   - 这几个稳定 id 的标签、配色和回落;
 *   - 把能力位翻译成布尔判断,供路由守卫、菜单和控件用。
 *
 * 三点刻意与后端逐字对齐:
 *   1. manage 不吃功能轴上限(它判的是「谁拥有这份数据」,归分组那条轴管),所以
 *      roleAllows(_, _, 'manage') 恒真 —— 前端不替后端猜组长的权限。
 *   2. 认不出的类别 / 缺失的能力位一律按 view(fail closed),与 Ceiling 的默认一致。
 *   3. 缺 perms 按「一个点都没有」处理:菜单宁可整块不亮,也不把没授权的入口亮出来。
 */

import type { Capabilities, PermId, ResourceAct, ResourceKind, UserRole } from '../api/auth'

/**
 * 五个**预置 id** 的展示顺序与回落表(内置 admin + 0063 播种进库的四档)。
 * 页面名单来自 /api/admin/roles,这里只负责两件事:给这几个稳定 id 配 i18n 标签/配色,
 * 以及在名单还没拉到时把库里的角色字串归一个能看懂的说法。新增自定义角色不用改这里。
 */
export const ROLE_ORDER: UserRole[] = ['admin', 'user', 'developer', 'ops', 'viewer']

/** 角色 → adminUsers 命名空间下的展示键。Record<> 保证枚举漏一个键就编译不过。 */
export const ROLE_LABEL_KEY: Record<UserRole, string> = {
  admin: 'adminUsers.roleAdmin',
  user: 'adminUsers.roleUser',
  developer: 'adminUsers.roleDeveloper',
  ops: 'adminUsers.roleOps',
  viewer: 'adminUsers.roleViewer',
}

/** 标签配色按档分:管理员/普通偏蓝灰,新档位各给一色,只读最淡。 */
export const ROLE_TAG_CLASS: Record<UserRole, string> = {
  admin: 'tag--admin',
  user: 'tag--user',
  developer: 'tag--developer',
  ops: 'tag--ops',
  viewer: 'tag--viewer',
}

const ACT_RANK: Record<ResourceAct, number> = { view: 0, operate: 1, manage: 2 }

/** 该角色对某类资源的功能上限;缺项按 view。 */
export function ceilingOf(caps: Capabilities | undefined, kind: ResourceKind): ResourceAct {
  return caps?.kinds?.[kind] ?? 'view'
}

/** 角色档位允不允许这类动作(view / operate 查表,manage 恒真)。 */
export function roleAllows(
  caps: Capabilities | undefined,
  kind: ResourceKind,
  act: ResourceAct,
): boolean {
  if (act === 'manage') return true
  return ACT_RANK[act] <= ACT_RANK[ceilingOf(caps, kind)]
}

/** 能否进设置类入口(构建环境 / 配置资源 / 全局凭据 / 用户管理 / 审计)。 */
export function settingsAllowed(caps: Capabilities | undefined): boolean {
  return caps?.settings === true
}

/**
 * 该角色是否被授予某个功能点(入口级可见性)。
 *
 * 比 kind+act 细的地方在于:同一类资源下挂着好几处入口 —— 主机状态、容器、证书、预览、
 * 异常检测都属 server —— 按类别只能一起亮或一起灭,而点能逐个说。
 * 缺 perms 数组(能力位没带这一项)一律按不放行,与后端「未知角色一个点都不给」同口径。
 */
export function permAllowed(caps: Capabilities | undefined, id: PermId): boolean {
  return caps?.perms?.includes(id) === true
}

/**
 * 功能门:路由 meta.requires、侧栏入口、设置页子标签共用同一份判据。
 *
 * 三处各写一次「能不能进」迟早会飘(典型症状:菜单里看得见,点进去被弹回首页)。
 * 判定只看角色功能轴;资源归属(分组)由后端每请求再判,不在这里出现。
 */
export interface AccessRequires {
  /** 设置类总闸。等价于 perm: 'settings.access'(后端同源:设置位就是由那个点算的)。 */
  settings?: true
  kind?: ResourceKind
  act?: ResourceAct
  /** 入口级功能点;控件按 kind+act 判、入口按点判,两条都是同一张表的投影。 */
  perm?: PermId
}

export function meetsRequires(caps: Capabilities | undefined, req?: AccessRequires): boolean {
  if (!req) return true
  if (req.settings === true && !settingsAllowed(caps)) return false
  if (req.perm && !permAllowed(caps, req.perm)) return false
  if (req.kind && req.act && !roleAllows(caps, req.kind, req.act)) return false
  return true
}

/**
 * 落点顺序:登录后 / 被功能门拦下时该把人放到哪一页。
 *
 * 以前守卫写死「不满足就回仪表盘」,而仪表盘自己也要 dashboard.view —— 那五个预置档恰好都带
 * 那个点所以没露馅,角色可配置以后就能建出一个不带概览的角色,那时「回仪表盘」就是
 * 守卫自己 redirect 给自己:导航被中止,页面永远停在登录页。
 * 顺序跟 AppShell 的 navItems 一致(同一张表,别在这里发明新顺序);末项「用户与权限」
 * 不设功能门(组长也得进得来),所以它保证落点永远找得到。
 */
export const LANDING_ORDER: { name: string; requires?: AccessRequires }[] = [
  { name: 'dashboard', requires: { perm: 'dashboard.view' } },
  { name: 'projects', requires: { perm: 'project.view' } },
  { name: 'runs', requires: { perm: 'run.view' } },
  { name: 'library', requires: { perm: 'library.view' } },
  { name: 'environments', requires: { perm: 'environments.view' } },
  { name: 'dora', requires: { perm: 'metrics.dora.view' } },
  { name: 'server-status', requires: { perm: 'server.view' } },
  { name: 'containers', requires: { perm: 'container.view' } },
  { name: 'proxy-overview', requires: { perm: 'cert.view' } },
  { name: 'previews', requires: { perm: 'preview.view' } },
  { name: 'anomaly', requires: { perm: 'anomaly.view' } },
  { name: 'permissions' },
]

/** 该角色第一个开得了的入口;末项无门,故恒有落点。 */
export function landingRouteName(caps: Capabilities | undefined): string {
  return (LANDING_ORDER.find((l) => meetsRequires(caps, l.requires)) ?? LANDING_ORDER[LANDING_ORDER.length - 1]).name
}

/** 归一化角色:库里的值可能是历史遗留的未知名,展示时不能崩。 */
export function normalizeRole(role: string): UserRole {
  return (ROLE_ORDER as string[]).includes(role) ? (role as UserRole) : 'user'
}
