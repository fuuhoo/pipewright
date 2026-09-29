/**
 * Session store — caches the auth session so route guards don't
 * hit /api/auth/session on every navigation.
 *
 * Error semantics:
 *   - 401  → user is not logged in  (session = null, isNetworkError = false)
 *   - 5xx / network → backend unreachable (session stays as-is if already
 *     loaded; isNetworkError = true so callers can show a fault state
 *     instead of kicking a logged-in user to /login)
 *
 * v6.2:SessionUser 带 role,供菜单与路由级隔离。
 * 角色档位落地后同一响应另带 capabilities(功能轴上限的展示副本);本 store 只负责把它
 * 转成 can() / canSettings() / meets() 三个判断 —— 权威判定仍在后端,这里只决定入口出不出得来。
 */

import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { http, HttpError } from '../api/http'
import type { PermId, ResourceAct, ResourceKind, SessionUser } from '../api/auth'
import {
  meetsRequires,
  permAllowed,
  roleAllows,
  settingsAllowed,
  type AccessRequires,
} from '../lib/roles'

export type { SessionUser }

/** Discriminated result returned by fetchSession */
export type SessionResult =
  | { kind: 'ok'; user: SessionUser }
  | { kind: 'unauthenticated' }
  | { kind: 'error'; status: number; message: string }

export const useSessionStore = defineStore('session', () => {
  /** null = not logged in (confirmed 401); undefined = not yet fetched */
  const user = ref<SessionUser | null | undefined>(undefined)
  const isNetworkError = ref(false)

  /** Whether we have already fetched once (cache primed). */
  let fetched = false

  /**
   * Fetch (or return cached) session.
   *
   * - Returns { kind:'ok' }            — valid session
   * - Returns { kind:'unauthenticated' } — confirmed 401
   * - Returns { kind:'error' }           — 5xx / network; does NOT clear
   *   an existing cached session so a logged-in user is not evicted.
   *
   * Pass force=true to bypass cache (e.g. after explicit logout).
   */
  async function ensureSession(force = false): Promise<SessionResult> {
    if (fetched && !force) {
      if (user.value === null) return { kind: 'unauthenticated' }
      if (user.value !== undefined) return { kind: 'ok', user: user.value }
    }

    try {
      const data = await http.get<SessionUser>('/api/auth/session')
      user.value = data
      isNetworkError.value = false
      fetched = true
      return { kind: 'ok', user: data }
    } catch (err) {
      if (err instanceof HttpError && err.status === 401) {
        // Confirmed not logged in
        user.value = null
        isNetworkError.value = false
        fetched = true
        return { kind: 'unauthenticated' }
      }

      // 5xx or network error — do NOT overwrite a valid cached session
      isNetworkError.value = true
      fetched = true // mark fetched so we don't hammer on every nav
      const status = err instanceof HttpError ? err.status : 0
      const message =
        err instanceof HttpError
          ? (err.apiError?.message ?? err.message)
          : err instanceof Error
            ? err.message
            : 'Network error'
      return { kind: 'error', status, message }
    }
  }

  /** Prime the cache with a known user (call after a successful login). */
  function setUser(u: SessionUser): void {
    user.value = u
    isNetworkError.value = false
    fetched = true
  }

  /** Clear the cached session (call after logout). */
  function clearSession(): void {
    user.value = null
    isNetworkError.value = false
    fetched = true
  }

  /** 会话能力位;未登录或尚未取到 → undefined,下面的判断一律按最严一档。 */
  const capabilities = computed(() => user.value?.capabilities)

  /** 角色档位允不允许对某类资源做这类动作(数据归属不在这一步,由后端按分组判)。 */
  function can(kind: ResourceKind, act: ResourceAct): boolean {
    return roleAllows(capabilities.value, kind, act)
  }

  /** 能否进设置类入口(构建环境 / 配置资源 / 全局凭据 / 用户管理 / 审计)。 */
  const canSettings = computed(() => settingsAllowed(capabilities.value))

  /** 该角色是否持有某功能点(左栏入口、路由落点按它亮灭)。 */
  function canPerm(id: PermId): boolean {
    return permAllowed(capabilities.value, id)
  }

  /** 路由 meta.requires / 菜单项 requires 的统一判据。 */
  function meets(req?: AccessRequires): boolean {
    return meetsRequires(capabilities.value, req)
  }

  return {
    user,
    isNetworkError,
    capabilities,
    canSettings,
    can,
    canPerm,
    meets,
    ensureSession,
    setUser,
    clearSession,
  }
})
