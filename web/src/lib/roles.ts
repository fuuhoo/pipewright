/**
 * roles.ts —— 角色档位在前端的只读视图。
 *
 * 权威表在 internal/access/roles.go(功能轴:角色 × 资源类别 → 最高动作),后端把算好的
 * 上限随会话回在 capabilities 里;这里只负责「怎么用它」:
 *   - 下拉与标签要的角色名单和顺序;
 *   - 把能力位翻译成布尔判断,供路由守卫、菜单和控件用。
 *
 * 两点刻意与后端逐字对齐:
 *   1. manage 不吃功能轴上限(它判的是「谁拥有这份数据」,归分组那条轴管),所以
 *      roleAllows(_, _, 'manage') 恒真 —— 前端不替后端猜组长的权限。
 *   2. 认不出的类别 / 缺失的能力位一律按 view(fail closed),与 Ceiling 的默认一致。
 */

import type { Capabilities, ResourceAct, ResourceKind, UserRole } from '../api/auth'

/** 下拉展示顺序,与 access.Roles() 同序;新增角色改这里 + i18n 五份键。 */
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
 * 功能门:路由 meta.requires、侧栏入口、设置页子标签共用同一份判据。
 *
 * 三处各写一次「能不能进」迟早会飘(典型症状:菜单里看得见,点进去被弹回首页)。
 * 判定只看角色功能档位;资源归属(分组)由后端每请求再判,不在这里出现。
 */
export interface AccessRequires {
  settings?: true
  kind?: ResourceKind
  act?: ResourceAct
}

export function meetsRequires(caps: Capabilities | undefined, req?: AccessRequires): boolean {
  if (!req) return true
  if (req.settings === true && !settingsAllowed(caps)) return false
  if (req.kind && req.act && !roleAllows(caps, req.kind, req.act)) return false
  return true
}

/** 归一化角色:库里的值可能是历史遗留的未知名,展示时不能崩。 */
export function normalizeRole(role: string): UserRole {
  return (ROLE_ORDER as string[]).includes(role) ? (role as UserRole) : 'user'
}
