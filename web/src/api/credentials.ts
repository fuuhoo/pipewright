/**
 * Credentials API — aligns to frozen 1.3 contract.
 *
 * GET    /api/credentials            → Credential[]
 * POST   /api/credentials            → Credential  (needs CSRF)
 * PATCH  /api/credentials/:id        → Credential  (needs CSRF)
 * DELETE /api/credentials/:id        → 204          (needs CSRF)
 * POST   /api/credentials/:id/reveal → { secret }   (needs CSRF; admin only; audited)
 * POST   /api/admin/credentials/:id/{disable,enable} → 200 (admin only; audited)
 *
 * List/get never return plaintext — only maskedValue is exposed. Plaintext is
 * returned solely by the explicit, audited reveal endpoint, which is gated to
 * admins: a regular user can *use* their own credential but cannot read it back.
 * Non-admins only ever see their own `personal` credentials.
 */

import { http } from './http'

export type CredentialType = 'git_token' | 'git_http' | 'git_ssh' | 'ssh_key' | 'ssh_password' | 'registry'

export interface Credential {
  id: string
  name: string
  type: CredentialType
  scope: string
  /** Owner of a `personal` credential (users.id); "" for global ones. */
  ownerId: string
  username: string
  /** Server-computed mask, e.g. "ghp_••••a91f" — never plaintext. */
  maskedValue: string
  description: string
  /** Soft-disable switch, flipped by admins; disabled credentials cannot be used. */
  enabled: boolean
  disabledBy: string
  disabledAt: string | null
  createdBy: string
  lastUsedAt: string | null
  createdAt: string
}

export interface CreateCredentialInput {
  name: string
  type: CredentialType
  scope: string
  username?: string
  /** Plaintext secret — sent once on creation, never returned by the server. */
  secret: string
  /** Admin-only: file a personal credential on someone else's behalf. */
  ownerId?: string
}

export interface UpdateCredentialInput {
  name?: string
  scope?: string
  username?: string
  description?: string
  /** Providing secret rotates the key. */
  secret?: string
}

export async function listCredentials(): Promise<Credential[]> {
  return http.get<Credential[]>('/api/credentials')
}

/**
 * 运行期真正能用的凭据。管理员可软禁用 personal 凭据,被禁用的那条在 vault 取用边界
 * 会直接失败,所以「选凭据」的下拉一律该过滤掉——只有管理页需要看到禁用的条目。
 */
export function usableCredentials(list: Credential[]): Credential[] {
  return list.filter((c) => c.enabled !== false)
}

export async function createCredential(input: CreateCredentialInput): Promise<Credential> {
  return http.post<Credential>('/api/credentials', input)
}

export async function updateCredential(
  id: string,
  input: UpdateCredentialInput,
): Promise<Credential> {
  return http.patch<Credential>(`/api/credentials/${id}`, input)
}

export async function deleteCredential(id: string): Promise<void> {
  return http.delete<void>(`/api/credentials/${id}`)
}

/**
 * Reveal the plaintext secret on explicit demand (POST + CSRF; audited server-side
 * as `credential_reveal`). The only endpoint that returns plaintext — and it is
 * admin-only, so a non-admin gets 403 even for a credential they own.
 */
export async function revealCredential(id: string): Promise<string> {
  const res = await http.post<{ secret: string }>(`/api/credentials/${id}/reveal`, {})
  return res.secret
}

/**
 * 禁用一条 personal 凭据(v6.2 §3.4 / §5.2;POST /api/admin/credentials/:id/disable)。
 * 仅管理员可调;global 凭据或 user 角色 → 403。
 *
 * 审计动作为 credential_disable;只改 enabled 元数据,不动密文、不删除。
 */
export async function disableCredential(id: string): Promise<void> {
  await http.post<{ disabled: boolean }>(`/api/admin/credentials/${id}/disable`, {})
}

/**
 * 恢复被禁用的 personal 凭据(POST /api/admin/credentials/:id/enable)。
 * 与禁用对称:仅改元数据,密文与归属都不动,所以禁用是可逆的。
 */
export async function enableCredential(id: string): Promise<void> {
  await http.post<{ disabled: boolean }>(`/api/admin/credentials/${id}/enable`, {})
}
