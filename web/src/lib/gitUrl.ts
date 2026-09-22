/**
 * 仓库地址前端校验 —— 与后端 internal/giturl 同一套接受规则。
 *
 * 后端是唯一权威(gitauth.ResolveRepoURL 决定放行/拒绝);这里只做提交前的即时反馈,
 * 故规则刻意保持宽松:凡后端可能接受的形态都放行,避免把合法地址挡在表单里。
 *   - http(s)://…
 *   - ssh://…
 *   - scp 语法 user@host:path(如 git@172.17.4.41:org/repo.git)
 */
const HTTP_URL = /^https?:\/\/\S+$/i
const SSH_URL = /^ssh:\/\/\S+$/i
// scp 语法:冒号左侧有 user@ 且不含斜杠,右侧非空且不以斜杠开头(排除本地绝对路径/盘符)。
const SCP_LIKE = /^[^@/\s:]+@[^/\s:]+:[^\s/][^\s]*$/

export function isSupportedRepoUrl(raw: string): boolean {
  const s = raw.trim()
  if (!s) return false
  return HTTP_URL.test(s) || SSH_URL.test(s) || SCP_LIKE.test(s)
}
