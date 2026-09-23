/**
 * 资源分组 API — v6.2 分组权限(§3.5 / §3.6)。
 *
 * 普通登录用户(RequireUser):
 *   GET    /api/groups                          → Group[](admin 看全部,其余看可见子集)
 *   GET    /api/groups/assignable               → GroupMember[](可加入名册的用户:仅 id + username)
 *   GET    /api/groups/{id}                     → Group
 *   PATCH  /api/groups/{id}                     → Group        (管理员或组长)
 *   POST   /api/groups/{id}/members             → Group        (管理员或组长)
 *   DELETE /api/groups/{id}/members/{userId}    → Group        (管理员或组长)
 *
 * 管理员:
 *   POST   /api/groups                          → Group
 *   DELETE /api/groups/{id}                     → 204
 *
 * canManage 由后端用 access.Decide 算出,前端不重推判定 —— 只读结论决定按钮显隐。
 * 私有分组对无权限用户是 403(明确拒绝,不伪装 404)。
 */

import { http } from './http'

export type GroupVisibility = 'public' | 'private'

/** 名册成员(id + 展示名)。 */
export interface GroupMember {
  id: string
  username: string
}

export interface Group {
  id: string
  name: string
  description: string
  visibility: GroupVisibility
  ownerId: string
  ownerName: string
  members: GroupMember[]
  projectCount: number
  serverCount: number
  /** 当前用户能否管理该组(改名册 / 改可见性 / 把资源归进来)。 */
  canManage: boolean
  createdAt: string
  updatedAt: string
}

export interface CreateGroupInput {
  name: string
  description?: string
  visibility: GroupVisibility
  /** 缺省为创建者本人。 */
  ownerId?: string
  memberIds?: string[]
}

export interface UpdateGroupInput {
  name?: string
  description?: string
  visibility?: GroupVisibility
  /** 仅管理员可传(更换组长)。 */
  ownerId?: string
}

export async function listGroups(): Promise<Group[]> {
  return http.get<Group[]>('/api/groups')
}

export async function getGroup(id: string): Promise<Group> {
  return http.get<Group>(`/api/groups/${id}`)
}

/** 可加入名册的用户名册(组长也需要,故不挂在 admin 路径下;只含 id + username)。 */
export async function listAssignableUsers(): Promise<GroupMember[]> {
  return http.get<GroupMember[]>('/api/groups/assignable')
}

export async function createGroup(input: CreateGroupInput): Promise<Group> {
  return http.post<Group>('/api/groups', input)
}

export async function updateGroup(id: string, input: UpdateGroupInput): Promise<Group> {
  return http.patch<Group>(`/api/groups/${id}`, input)
}

export async function deleteGroup(id: string): Promise<void> {
  return http.delete<void>(`/api/groups/${id}`)
}

export async function addGroupMember(id: string, userId: string): Promise<Group> {
  return http.post<Group>(`/api/groups/${id}/members`, { userId })
}

export async function removeGroupMember(id: string, userId: string): Promise<Group> {
  return http.delete<Group>(`/api/groups/${id}/members/${encodeURIComponent(userId)}`)
}

/** 未归组的展示值:后端用空串表示,列表里统一显示为「未归组」。 */
export const UNGROUPED = ''

/** 把 groupId 映射成展示名用的索引('' 与不存在的组都归到未归组)。 */
export function buildGroupNameIndex(groups: Group[]): Record<string, string> {
  const out: Record<string, string> = {}
  for (const g of groups) out[g.id] = g.name
  return out
}
