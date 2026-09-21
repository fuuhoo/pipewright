/**
 * Users API — v6.2 §3.5 / §5.2(阶段 9 最小骨架).
 *
 *   GET /api/admin/users       → { items: User[] }
 *   GET /api/admin/users/:id   → User
 *
 * 完整用户管理(邀请 token / 注册 / 启用禁用 / 改角色)是后续 story;当前后端
 * 只暴露只读列表与单查,页面据此展示。响应体绝不含 password_hash。
 */

import { http } from './http'

export interface User {
  id: string
  username: string
  role: 'admin' | 'user'
  enabled: boolean
  description: string
  createdAt: string
  updatedAt: string
  /** RFC3339;从未登录为 null。 */
  lastLoginAt: string | null
}

interface ListEnvelope {
  items: User[]
}

export async function listUsers(): Promise<User[]> {
  const res = await http.get<ListEnvelope>('/api/admin/users')
  return res.items ?? []
}

export async function getUser(id: string): Promise<User> {
  return http.get<User>(`/api/admin/users/${id}`)
}
