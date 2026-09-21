/**
 * Auth API — aligns to frozen 1.2 contract.
 *
 * GET  /api/auth/session → { username, role } | 401
 * POST /api/auth/login   → { username, role } | 401 | 429
 * POST /api/auth/logout  → 204
 *
 * v6.2:login / session 均回显 role("admin" | "user"),前端据其做菜单与
 * 入口隔离(§3.6)。旧部署会话 role 为空 → 后端统一按 "admin" 回显。
 */

import { http, HttpError } from './http'

/** 平台角色。与后端 auth.RoleAdmin / RoleUser 同集合。 */
export type UserRole = 'admin' | 'user'

export interface SessionUser {
  username: string
  role: UserRole
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
