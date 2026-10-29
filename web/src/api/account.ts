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
 * POST   /api/account/profile          → { displayName, email }  本地档案(需 CSRF)
 *   两列都可省略,但至少给一个;后端回的是**归一化后**的值(空白折叠 + 按字符截断)。
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

/**
 * 首次设置本地口令:仅对没有本地口令的账号(OAuth/OIDC 首登自动建号)有效。
 * 不要求当前口令 —— 占位口令的明文从未存在。已有口令的账号返回 409 password_already_set。
 */
export async function setInitialPassword(newPassword: string): Promise<void> {
  return http.post<void>('/api/account/password/initial', { newPassword })
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
  /**
   * 非空 = 这条映射同步过一条个人 git 凭据(只有「登录即同步凭据」打开时才会有)。
   * 它是「哪条凭据是我这次登录给的」唯一权威线索:凭据页上那条只写着名字,认不出来源。
   */
  credentialId: string
  /** 同步出来的访问令牌到期时刻(RFC3339);空串 = 长期令牌或未同步。 */
  expiresAt: string
  /** 平台实际授予的权限串(空 = 未同步或平台没回)。 */
  scope: string
}

export async function listIdentities(): Promise<IdentityBinding[]> {
  return http.get<IdentityBinding[]>('/api/account/identities')
}

export async function unbindIdentity(id: string): Promise<void> {
  return http.delete<void>(`/api/account/identities/${id}`)
}

/**
 * 本地档案(显示名 + 邮箱)。
 *
 * 这两个字段存在 users 表上,归本人编辑:平台身份首次建号时写进来一份,之后登录**不再覆盖**
 * —— 想改的去「外部身份绑定」那张表看,那才是平台快照。后端会把连续空白折成一个空格并按长度
 * 截断,所以返回值才是权威,调用方拿它回填本地会话缓存。
 *
 * 出错口径:400 输入不合法(邮箱格式/两个字段都没给)、403 缺 CSRF、409 旧会话未绑定账号、
 * 401 未登录。
 */
export interface ProfileInput {
  displayName?: string
  email?: string
}

export interface Profile {
  displayName: string
  email: string
}

export async function updateProfile(input: ProfileInput): Promise<Profile> {
  return http.post<Profile>('/api/account/profile', input)
}
