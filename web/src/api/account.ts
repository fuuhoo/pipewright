/**
 * Account API — aligns to frozen 1.7 contract.
 *
 * POST   /api/account/password        → 204  (needs CSRF)
 *   { currentPassword, newPassword }
 *   401 invalid_current_password · 422 weak_password
 *   On success the server revokes every OTHER session (current token kept).
 * GET    /api/account/sessions        → { sessions: Session[] }
 *   id = sha256 prefix of the token — the raw token is NEVER returned.
 * DELETE /api/account/sessions/:id     → 204  (needs CSRF)
 *   404 session_not_found
 * GET    /api/account/identities       → IdentityBinding[]      (外部 git 身份绑定)
 * DELETE /api/account/identities/:id   → 204  (needs CSRF;404 = 不是你的绑定)
 */

import { http } from './http'

export interface Session {
  /** sha256 hex prefix of the session token — never the raw token. */
  id: string
  createdAt: string
  lastSeenAt: string
  expiresAt: string
  /** True for the session that issued this request. */
  current: boolean
}

export interface ChangePasswordInput {
  currentPassword: string
  newPassword: string
}

export async function changePassword(input: ChangePasswordInput): Promise<void> {
  return http.post<void>('/api/account/password', input)
}

export async function listSessions(): Promise<Session[]> {
  const res = await http.get<{ sessions: Session[] }>('/api/account/sessions')
  return res.sessions
}

export async function revokeSession(id: string): Promise<void> {
  return http.delete<void>(`/api/account/sessions/${id}`)
}

/**
 * 外部身份绑定(v6.x OAuth 登录)。
 *
 * GET    /api/account/identities          → 当前账号绑过的外部身份
 * DELETE /api/account/identities/{id}     → 204(需 CSRF);别人的绑定回 404
 *
 * 一条绑定 = 一个 (provider, 平台稳定 ID) ↔ 本账号的映射。绑定**不改角色**:
 * 管理员绑上去还是管理员。解绑不撤销当前会话(掉线不该是解绑的副作用)。
 */
export interface IdentityBinding {
  id: string
  provider: string
  /** 平台侧的稳定数字 ID —— 认人只认它,不认可能已被改掉的登录名。 */
  externalId: string
  username: string
  email: string
  createdAt: string
  updatedAt: string
}

export async function listIdentities(): Promise<IdentityBinding[]> {
  return http.get<IdentityBinding[]>('/api/account/identities')
}

export async function unbindIdentity(id: string): Promise<void> {
  return http.delete<void>(`/api/account/identities/${id}`)
}
