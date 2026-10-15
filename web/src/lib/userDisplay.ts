/**
 * userDisplay.ts —— 「用户名 / 昵称 / 邮箱」三件套的展示口径。
 *
 * 三样东西来自两个地方,别混:
 *   - username 是本机账号的登录名,唯一且不可改语义;
 *   - displayName / email 存在 users 表上,是**本地档案**:平台身份首次建号时写进来一份,
 *     之后归本人(或管理员)编辑,再登录不再覆盖 —— 想认平台那份随时可变的,去看
 *     「外部身份绑定」里的 user_identities 快照。
 * 所以昵称可能为空(老账号还没回填),展示层必须有回落,而不是假设它总有。
 *
 * 长度与邮箱判据镜像 internal/users/users.go 的 normalize*:后端对显示名是「折叠空白 + 按
 * 字符截断」(不报错),对邮箱是「拒绝」,所以这里只有 emailProblem 会给出错误,昵称只做截断。
 * 不一致的代价是「前端放行、保存回 400」,那比多写两行判据难受得多。
 */

/** 后端 users.MaxDisplayNameLen / MaxEmailLen(按字符计,中文算一个)。 */
export const PROFILE_NAME_MAX = 64
export const PROFILE_EMAIL_MAX = 254

/** 三件套的最小形状:会话载荷与 users 行都满足它。 */
export interface NamedAccount {
  username: string
  displayName?: string
  email?: string
}

/** 折叠内部连续空白(含换行)并去首尾 —— 与后端 normalizeDisplayName 同一算法。 */
export function collapseWhitespace(value: string): string {
  return value.split(/\s+/).filter(Boolean).join(' ')
}

/** 存进本地档案的显示名:折叠后按字符截断。Array.from 拆的是码点,与 Go 的 rune 口径一致(代理对算一个)。 */
export function normalizeDisplayName(value: string): string {
  const folded = collapseWhitespace(value)
  const chars = Array.from(folded)
  return chars.length > PROFILE_NAME_MAX ? chars.slice(0, PROFILE_NAME_MAX).join('') : folded
}

/** 昵称(已归一化)。空串表示「这个账号还没有本地昵称」,调用方据此回落。 */
export function nicknameOf(account: NamedAccount): string {
  return collapseWhitespace(account.displayName ?? '')
}

/** 主标签:有昵称用昵称,没有用登录名。 */
export function primaryNameOf(account: NamedAccount): string {
  return nicknameOf(account) || account.username
}

/** 登录名写成 @handle,和邮箱区分开 —— 一个是本机账号,一个是联系方式。 */
export function handleOf(account: NamedAccount): string {
  return `@${account.username}`
}

/** 次行:有邮箱显示邮箱,没有退回 @登录名(而不是留一个空行)。 */
export function contactOf(account: NamedAccount): string {
  const email = (account.email ?? '').trim()
  return email || handleOf(account)
}

/** 头像字头:取主标签的第一个非空字符(码点,不是 UTF-16 单元)。中文取字、拉丁取首字母并大写。 */
export function initialsOf(account: NamedAccount): string {
  const first = Array.from(collapseWhitespace(primaryNameOf(account)))[0]
  return first ? first.toUpperCase() : '?'
}

/** 邮箱判据的失败原因(与后端逐条对应)。'' 表示通过(含「留空」这一合法档)。 */
export type EmailProblem = '' | 'space' | 'format' | 'tooLong'

export function emailProblem(value: string): EmailProblem {
  const v = value.trim()
  if (!v) return ''
  if (/\s/.test(v)) return 'space'
  const at = v.lastIndexOf('@')
  if (at <= 0 || at === v.length - 1 || v.split('@').length - 1 !== 1) return 'format'
  if (Array.from(v).length > PROFILE_EMAIL_MAX) return 'tooLong'
  return ''
}
