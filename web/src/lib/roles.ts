/**
 * roles.ts —— 功能轴在前端的只读视图。
 *
 * 权威表在 internal/access/perms.go(角色 → 功能点);后端把它同时投影成两样东西回在
 * capabilities 里 —— 每类资源的档位上限(kinds)与该角色的功能点(perms),这里只负责「怎么用它」:
 *   - 下拉与标签要的角色名单和顺序;
 *   - 把能力位翻译成布尔判断,供路由守卫、菜单和控件用。
 *
 * 三点刻意与后端逐字对齐:
 *   1. manage 不吃功能轴上限(它判的是「谁拥有这份数据」,归分组那条轴管),所以
 *      roleAllows(_, _, 'manage') 恒真 —— 前端不替后端猜组长的权限。
 *   2. 认不出的类别 / 缺失的能力位一律按 view(fail closed),与 Ceiling 的默认一致。
 *   3. 缺 perms 按「一个点都没有」处理:菜单宁可整块不亮,也不把没授权的入口亮出来。
 */

import type { Capabilities, PermId, ResourceAct, ResourceKind, UserRole } from '../api/auth'

/** 下拉展示顺序,与 access.Roles() 同序;新增角色改这里 + i18n 八份键 + 后端 perms.go 的点集。 */
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

/** 归一化角色:库里的值可能是历史遗留的未知名,展示时不能崩。 */
export function normalizeRole(role: string): UserRole {
  return (ROLE_ORDER as string[]).includes(role) ? (role as UserRole) : 'user'
}
