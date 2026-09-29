/**
 * Roles API —— 可配置角色(P4)。
 *
 *   GET    /api/admin/roles          → { items: Role[] }(内置模板在前,自定义在后)
 *   GET    /api/admin/roles/points   → { items: PermPoint[] }(功能点字典,编辑器渲染勾选项)
 *   GET    /api/admin/roles/:id      → Role
 *   POST   /api/admin/roles          → Role   建自定义角色
 *   PATCH  /api/admin/roles/:id      → Role   改名 / 改描述 / 改点集
 *   DELETE /api/admin/roles/:id      → { deleted: id }
 *   POST   /api/admin/roles/:id/copy → Role   以某角色为模板复制成新角色
 *
 * 三条边界是后端定的,前端只负责别让人去撞:
 *   - 内置只剩 admin 一档:它是代码表里的**模板**,不可改、不可删,只能复制。其余角色 ——
 *     含 0063 从代码表搬进库的四档预置(user / developer / ops / viewer)—— 都在 roles 表里,
 *     由这条接口读出来,页面据 `builtin` 字段决定编辑/删除入口,别在前端再抄一份 id 名单。
 *   - `settings.access`(进得了设置的总闸)不许分给自定义角色 → 422;勾选框直接禁用。
 *   - 仍有账号在用的角色不许删 → 409,userCount 就是那句「还有 N 个账号」。
 *
 * 改点集会让**在线用户刷新页面**就拿到新能力位(权威判定每请求重算);把人换到另一个角色
 * 才需要对方重新登录 —— sessions.role 是登录快照。
 */

import { http } from './http'
import type { PermId, ResourceAct, ResourceKind } from './auth'

/** 角色引用:内置档 id('admin')或库里角色的 id(0063 播种的预置档是那四个词,自建的 UUID)。 */
export type RoleId = string

export interface Role {
  id: RoleId
  /** 展示名:内置 admin 与没改过名的预置档回的是 id 本身,页面按 i18n 键翻译;其余回用户起的名字。 */
  name: string
  description: string
  /** 复制来源的角色 id('' = 手建)。只用于「基于某模板」这句提示,无判定语义。 */
  baseRole: string
  builtin: boolean
  perms: PermId[]
  /** 当前挂在这个角色上的账号数(删除挡门与列表提示)。 */
  userCount: number
  createdBy: string
  createdAt: string
  updatedAt: string
}

/** 功能点字典的一项。Kind 是资源类别,platform 表示平台设置类(不吃四类上限)。 */
export interface PermPoint {
  id: PermId
  kind: ResourceKind | 'platform'
  /** 需要的档位。字典里只有 view / operate —— manage 归分组那条数据轴,不在这勾。 */
  act: Extract<ResourceAct, 'view' | 'operate'>
  /** settings.access:仅内置管理员可得,渲染成禁用项而不是让人撞 422。 */
  builtinOnly: boolean
}

export interface CreateRoleInput {
  name: string
  description?: string
  /** 模板:服务端名单里的任一角色 id(内置 admin 或库里的角色);不填就是「从零开始,一个入口都不给」。 */
  baseRole?: string
  perms?: PermId[]
}

export interface UpdateRoleInput {
  name?: string
  description?: string
  /** 缺省 = 不动;传空数组 = 明确的「一个入口都不给」,两者不同,所以这里可选而非可空。 */
  perms?: PermId[]
}

export async function listRoles(): Promise<Role[]> {
  const res = await http.get<{ items: Role[] | null }>('/api/admin/roles')
  return res.items ?? []
}

export async function listPermPoints(): Promise<PermPoint[]> {
  const res = await http.get<{ items: PermPoint[] | null }>('/api/admin/roles/points')
  return res.items ?? []
}

export async function getRole(id: RoleId): Promise<Role> {
  return http.get<Role>(`/api/admin/roles/${encodeURIComponent(id)}`)
}

export async function createRole(input: CreateRoleInput): Promise<Role> {
  return http.post<Role>('/api/admin/roles', input)
}

export async function updateRole(id: RoleId, input: UpdateRoleInput): Promise<Role> {
  return http.patch<Role>(`/api/admin/roles/${encodeURIComponent(id)}`, input)
}

export async function deleteRole(id: RoleId): Promise<void> {
  await http.delete<{ deleted: string }>(`/api/admin/roles/${encodeURIComponent(id)}`)
}

/** 复制成自定义角色:源角色的 settings.access 由后端剥掉,所以复制 admin 也造不出管理员。 */
export async function copyRole(id: RoleId, name: string): Promise<Role> {
  return http.post<Role>(`/api/admin/roles/${encodeURIComponent(id)}/copy`, { name })
}
