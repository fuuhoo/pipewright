/**
 * Users API — v6.2 §3.5 / §5.2 用户管理(admin-only)。
 *
 *   GET    /api/admin/users              → { items: User[] }(默认不含已禁用)
 *   GET    /api/admin/users/:id          → User
 *   POST   /api/admin/users              → User   建号(用户名 + 初始口令 + 角色)
 *   POST   /api/admin/users/:id/password → 204    重置口令
 *   PATCH  /api/admin/users/:id          → User   改描述 / 启用禁用 / 改角色
 *   DELETE /api/admin/users/:id          → 204    删号(不可逆)
 *
 * 响应体绝不含 password_hash;口令只在请求里出现一次。
 *
 * 角色列现在存的是**角色引用**:内置只剩 admin 一档,其余(含 0063 起在库里的四档预置
 * user / developer / ops / viewer)都是 roles 表的 id,所以这一列是 RoleId 而不是枚举。
 * 校验在 access.ValidRole(代码表 → 进程内角色表),所以这里放开成 RoleId,不再由前端枚举挡路。
 * 改角色不会踢掉对方已有的会话 —— sessions.role 是登录时的快照,下次登录才生效。
 * 内置管理员那一行(id 尾号 …0001)的口令/启停由「账户设置」管,上述写端点对它 409。
 */

import { http } from './http'
import type { UserRole } from './auth'
import type { RoleId } from './roles'

export type { UserRole }

/**
 * 内置管理员在 users 表里的同步行 id(= Go 侧 users.BootstrapAdminRegularUserID)。
 * 这一行的口令与启停归「账户设置」管,写端点对它一律 409 —— 页面据此提前禁掉按钮,
 * 而不是等报错。
 */
export const BOOTSTRAP_ADMIN_ID = '00000000-0000-0000-0000-000000000001'

export interface User {
  id: string
  username: string
  role: RoleId
  enabled: boolean
  description: string
  /** 本地档案:显示名(昵称)。平台身份首次建号写入,之后由本人/管理员编辑,登录不再覆盖。 */
  displayName: string
  /** 本地档案:邮箱。同上,可与平台快照不同。 */
  email: string
  createdAt: string
  updatedAt: string
  /** RFC3339;从未登录为 null。 */
  lastLoginAt: string | null
}

interface ListEnvelope {
  items: User[]
}

export interface ListUsersParams {
  role?: RoleId
  /** 列出已禁用账号(默认 false)。 */
  includeDisabled?: boolean
}

export interface CreateUserInput {
  username: string
  password: string
  role: RoleId
  description?: string
  displayName?: string
  email?: string
}

export interface UpdateUserInput {
  description?: string
  enabled?: boolean
  /** 改角色(内置档 id 或自定义角色 UUID)。未知名后端回 400(不会静默降级成 user,那是建号路径的取舍)。 */
  role?: RoleId
  /**
   * 本地档案两列。后端是**一行 UPDATE 同时写两列**,所以只改一个时要把另一个的现值一起提交
   * —— 省略即「不改」,传空串才是「清空」。
   */
  displayName?: string
  email?: string
}

export async function listUsers(params: ListUsersParams = {}): Promise<User[]> {
  const qs = new URLSearchParams()
  if (params.role) qs.set('role', params.role)
  if (params.includeDisabled) qs.set('includeDisabled', '1')
  const suffix = qs.toString() ? `?${qs.toString()}` : ''
  const res = await http.get<ListEnvelope>(`/api/admin/users${suffix}`)
  return res.items ?? []
}

export async function getUser(id: string): Promise<User> {
  return http.get<User>(`/api/admin/users/${id}`)
}

export async function createUser(input: CreateUserInput): Promise<User> {
  return http.post<User>('/api/admin/users', input)
}

/** 重置他人口令(204;不改会话,与管理员改自己口令同一取舍)。 */
export async function resetUserPassword(id: string, password: string): Promise<void> {
  return http.post<void>(`/api/admin/users/${id}/password`, { password })
}

export async function updateUser(id: string, input: UpdateUserInput): Promise<User> {
  return http.patch<User>(`/api/admin/users/${id}`, input)
}

/**
 * 永久删除账号(204)。与「禁用」不是一回事:这一条会把该账号的会话、外部身份绑定、
 * 分组名册一起清掉,删完用户名就释放了(同名可重建)。
 *
 * 后端在两件事上会回 409 而不是删:该账号还是 N 个分组的组长、或名下还有 N 条个人凭据
 * —— 两者都会真删数据,所以要先移交/先清。message 里带计数,直接回显给管理员看。
 * 另外「内置管理员行」与「自己的账号」也一律 409。
 */
export async function deleteUser(id: string): Promise<void> {
  return http.delete<void>(`/api/admin/users/${id}`)
}

/** 后端 users.MinPasswordLen;前端据此提前拦下过短口令。 */
export const MIN_PASSWORD_LEN = 8
