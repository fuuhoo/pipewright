/**
 * 「平台登录」的邮箱域名白名单:输入框里那串文本 ↔ 要发给服务端的数组。
 *
 * 抽成纯逻辑有两个原因:
 *   1. 分隔符是用户随手打的(逗号、空格、分号、换行都可能),不能靠一个 split(',') 赌运气;
 *   2. 送错一项就会被服务端 422 整条拒掉,而这页面上「保存失败」是很贵的反馈 ——
 *      所以填的时候就把非法项点出来,别等回车才知道。
 *
 * 合法性口径与后端 internal/oauth/login.go 的 validDomain 一致,前端只是提前判一次,
 * 服务端仍是权威(改这里不会让绕过前端写入变得可能)。
 */

/** 用户可用的分隔符:逗号 / 分号 / 空白(含换行)。 */
const SEPARATORS = /[\s,;]+/

/** 把输入框原文拆成域名数组:去空、去重、统一小写(域名大小写不敏感)。 */
export function parseOAuthDomains(raw: string): string[] {
  const seen = new Set<string>()
  for (const part of raw.split(SEPARATORS)) {
    const d = part.trim().toLowerCase()
    if (d) seen.add(d)
  }
  return [...seen]
}

/**
 * 找出非法项。后端要求「只填域名本身」,带协议、路径、@ 或首尾点是常见误填,
 * 提前拦下来能把「哪一项写错了」直接指给用户,而不是一句整段拒绝。
 */
export function invalidOAuthDomains(raw: string): string[] {
  return parseOAuthDomains(raw).filter((d) => !isValidDomain(d))
}

function isValidDomain(d: string): boolean {
  if (!d.includes('.') || d.startsWith('.') || d.endsWith('.')) return false
  for (const label of d.split('.')) {
    if (!label || label.startsWith('-') || label.endsWith('-')) return false
    for (const ch of label) {
      const ok =
        (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch === '-'
      if (!ok) return false
    }
  }
  return true
}

/** 列表回填输入框:逗号 + 空格分隔,读起来最像一列表单字段。 */
export function joinOAuthDomains(domains: string[]): string {
  return domains.join(', ')
}
