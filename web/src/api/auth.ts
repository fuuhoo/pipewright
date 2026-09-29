/**
 * Auth API — aligns to frozen 1.2 contract.
 *
 * GET  /api/auth/session → { username, role, capabilities } | 401
 * POST /api/auth/login   → { username, role, capabilities } | 401 | 429
 * POST /api/auth/logout  → 204
 *
 * v6.2:login / session 均回显 role,前端据其做菜单与入口隔离(§3.6)。旧部署会话 role
 * 为空 → 后端统一按 "admin" 回显。
 *
 * 角色档位(功能轴)落地后,同一响应带 capabilities:后端把该角色的上限算给我们,
 * 菜单/路由据此决定「出现还是不出现」。它只是展示层副本 —— 每个请求后端仍独立判定,
 * 拿它藏按钮是为了不让用户点到一个注定 403 的入口,而不是当作授权本身。
 */

import { http, HttpError } from './http'

/** 平台角色。与 internal/access/roles.go 的枚举逐字同集合。 */
export type UserRole = 'admin' | 'user' | 'developer' | 'ops' | 'viewer'

/** 被判定资源的类别,与 access.Kind 同集合。 */
export type ResourceKind = 'project' | 'run' | 'server' | 'kube_cluster'

/** 动作档位,与 access.Act 同集合(自低到高)。 */
export type ResourceAct = 'view' | 'operate' | 'manage'

/**
 * 功能点:左栏入口与路由落点的可见性键,与 internal/access/perms.go 的字典逐字同集合。
 * 粒度比 ResourceKind 细 —— 同一类资源(如 server)下面挂着主机状态、容器、证书、预览、
 * 异常检测五处入口,按类别只能一起亮或一起灭。
 * roles_drift_test.go 会读这里比对:少一个就是那个入口对所有角色静默消失。
 */
export type PermId =
  | 'dashboard.view'
  | 'project.view'
  | 'project.edit'
  | 'library.view'
  | 'library.edit'
  | 'run.view'
  | 'run.operate'
  | 'environments.view'
  | 'metrics.dora.view'
  | 'server.view'
  | 'server.exec'
  | 'container.view'
  | 'container.operate'
  | 'cert.view'
  | 'preview.view'
  | 'preview.recycle'
  | 'anomaly.view'
  | 'anomaly.edit'
  | 'cluster.view'
  | 'cluster.operate'
  | 'settings.access'

/** 会话能力位;后端 access.Capabilities 的镜像。 */
export interface Capabilities {
  /** 设置类入口(构建环境 / 配置资源 / 全局凭据 / 用户 / 审计)。 */
  settings: boolean
  /** 每类资源的功能上限。缺某个键按「只读」处理(与后端 fail closed 口径一致)。 */
  kinds: Partial<Record<ResourceKind, ResourceAct>>
  /**
   * 该角色的功能点集,决定入口亮不亮。与 kinds 同源(都出自 perms.go 那张表),
   * 所以不存在「菜单看得见、请求被上限拦掉」的错位。缺属性按「一个入口都不给」处理:
   * 宁可让页面空着,也不要把没授权的入口亮出来。
   */
  perms?: PermId[]
}

export interface SessionUser {
  username: string
  role: UserRole
  capabilities: Capabilities
}

/**
 * Fetch the current session from the server.
 *
 * Returns:
 *   - SessionUser   — authenticated
 *   - null          — confirmed 401 (not logged in)
 *
 * Throws HttpError (status ≠ 401) or network Error for 5xx / network faults,
 * so callers can distinguish "not logged in" from "backend down".
 */
export async function fetchSession(): Promise<SessionUser | null> {
  try {
    return await http.get<SessionUser>('/api/auth/session')
  } catch (err) {
    if (err instanceof HttpError && err.status === 401) {
      return null
    }
    // Re-throw so route guards / stores can handle 5xx / network errors
    // without incorrectly treating an authenticated user as unauthenticated.
    throw err
  }
}

export async function login(username: string, password: string): Promise<SessionUser> {
  return http.post<SessionUser>('/api/auth/login', { username, password })
}

export async function logout(): Promise<void> {
  await http.post<void>('/api/auth/logout')
}
