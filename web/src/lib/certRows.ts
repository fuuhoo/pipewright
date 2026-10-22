/**
 * 证书管理页(/certs)的公共口径:状态配色、要不要人接手、域名与落点路径的提交前校验、
 * 列表筛选与「立即检查」汇总,全是纯函数。
 *
 * 规则尽量与后端 `internal/cert` 同源,页面只做「提交前先说清楚」,判定权仍在后端:
 *   - 续期窗口默认 30 天 = `cert.IssuanceWindow`,每张证书可用 renewBeforeDays 覆盖(`cert.RenewWindow`);
 *     窗口内平台能自己续的不算需要接手(见 `cert.NeedsRenew`);
 *   - 可填天数 1~365 = `cert.Min/MaxRenewBeforeDays`(0 = 改回默认);
 *   - 域名正则 = `cert.domainRe`(通配只允许最左标签);
 *   - 落点路径 = `cert.ValidateDeployTarget`(两条都绝对、且不能同路径)。
 * 文案一律由调用方从 i18n 传进来:lib 不依赖 vue-i18n,才方便脱离页面验规则。
 */
import type { Cert, CertSource, CertStatus, CheckReport } from '../api/certs'

/** 与后端 `cert.IssuanceWindow` 同长:证书没设置过自己的阈值时按这个天数算。 */
export const RENEW_WINDOW_DAYS = 30

/** 用户能填的「到期前 N 天」区间,与后端 Min/MaxRenewBeforeDays 同值;0 是「用默认」的专用值。 */
export const MIN_RENEW_BEFORE_DAYS = 1
export const MAX_RENEW_BEFORE_DAYS = 365

/** 状态徽章配色;与 StatusBadge / containerState 用同一组色名。 */
export type CertTone = 'green' | 'amber' | 'red' | 'cyan' | 'faint'

/** 这张证书实际生效的续期窗口天数:填过用自己的,没填(0)用默认。 */
export function renewWindowDays(cert: Cert): number {
  return cert.renewBeforeDays > 0 ? cert.renewBeforeDays : RENEW_WINDOW_DAYS
}

/** 输入框的原始文本是否落在可提交区间(1~365 或 0);提交前拦一下,省一次 400。 */
export function isValidRenewBeforeDays(n: number): boolean {
  return n === 0 || (Number.isInteger(n) && n >= MIN_RENEW_BEFORE_DAYS && n <= MAX_RENEW_BEFORE_DAYS)
}

/**
 * 天数输入框的当前值 → 要提交的天数:留空 = 0 = 用后端默认窗口,非整数 = NaN(调用方据此报错)。
 *
 * 必须同时收字符串和数字:`<input type="number">` 上的 v-model 在 Vue 3.4 起会自动转成 number,
 * 清格或填半截小数时还可能给到 `''` 之外的东西,所以这里先统一转字符串再判。
 */
export function parseRenewDays(value: string | number): number {
  const trimmed = String(value ?? '').trim()
  if (trimmed === '') return 0
  const n = Number(trimmed)
  return Number.isInteger(n) ? n : NaN
}

/**
 * 状态配色 + 剩余天数 → 徽章颜色。
 * 待签用青色(进行中,不是坏了),签发失败与已过期用红色,窗口内用琥珀色提醒「该留意了」。
 * 窗口天数由调用方传(每张证书可以各自设),不传按默认 30 天。
 */
export function certTone(status: CertStatus, daysLeft: number, windowDays = RENEW_WINDOW_DAYS): CertTone {
  if (status === 'pending') return 'cyan'
  if (status === 'failed') return 'red'
  if (status === 'expired') return 'red'
  if (daysLeft >= 0 && daysLeft <= windowDays) return 'amber'
  return 'green'
}

/**
 * 这张证书要不要人接手:已过期,或落在它自己的续期窗口内而平台续不动(导入的、或关了自动续期的)。
 *
 * 待签(还没有正文)不算:首签必须是用户明确点「签发」的结果,后台扫描器也刻意跳过它,
 * 页面把它列成「需要接手」会和后端的行为对不上。
 */
export function needsAttention(cert: Cert): boolean {
  if (cert.status === 'expired') return true
  if (cert.status !== 'issued') return false
  return cert.daysLeft <= renewWindowDays(cert) && (cert.source === 'upload' || !cert.autoRenew)
}

/** 平台此刻能不能自己把它签出来:只有 ACME 来源能,导入的永远要用户自己换正文。 */
export function canIssue(source: CertSource): boolean {
  return source === 'acme'
}

/** 域名输入框的原始文本 → 去重后的清单(逗号 / 空白 / 换行都算分隔符,统一小写)。 */
export function parseDomains(text: string): string[] {
  const out: string[] = []
  const seen = new Set<string>()
  for (const raw of text.split(/[,;、\s]+/)) {
    const d = raw.trim().toLowerCase()
    if (d === '' || seen.has(d)) continue
    seen.add(d)
    out.push(d)
  }
  return out
}

/**
 * 单个域名是否合法。与 `cert.domainRe` 同形:通配只允许出现在最左标签。
 * 总长 ≤253 是后端 `ValidDomain` 的硬约束,超了提交必被拒,不如就地标出来。
 */
export function isValidDomain(s: string): boolean {
  const d = s.trim().toLowerCase()
  if (d === '' || d.length > 253) return false
  return /^(\*\.|)([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$/.test(d)
}

/** 清单里非法的那些(保持原顺序),供表单逐条提示。 */
export function invalidDomains(list: string[]): string[] {
  return list.filter((d) => !isValidDomain(d))
}

/** 落点路径校验的失败种类,对应后端两个错误。'' = 通过。 */
export type PathIssue = '' | 'notAbsolute' | 'same'

/**
 * 证书与私钥的两条目标路径:都必须是非空绝对路径,且不能是同一个文件
 * (同路径会把公开证书覆盖成私钥,或反过来 —— 后端 `ErrSamePath` 拦的就是这个)。
 */
export function checkTargetPaths(certPath: string, keyPath: string): PathIssue {
  const c = certPath.trim()
  const k = keyPath.trim()
  if (c === '' || k === '' || !c.startsWith('/') || !k.startsWith('/')) return 'notAbsolute'
  if (c === k) return 'same'
  return ''
}

/** 申请弹窗里的一行落点草稿:字段与 `CertTargetInput` 同形,但允许整行留空(= 不想加这个落点)。 */
export interface TargetDraft {
  serverId: string
  certPath: string
  keyPath: string
  postCommand: string
  autoDeploy: boolean
}

export function emptyTargetDraft(): TargetDraft {
  return { serverId: '', certPath: '', keyPath: '', postCommand: '', autoDeploy: false }
}

/** 整行没填 = 用户只是点了「再加一台」又放弃,提交时跳过而不是报错。 */
export function isBlankTargetDraft(d: TargetDraft): boolean {
  return (
    d.serverId === '' &&
    d.certPath.trim() === '' &&
    d.keyPath.trim() === '' &&
    d.postCommand.trim() === '' &&
    !d.autoDeploy
  )
}

/** 落点草稿逐行判:先要主机,再要两条路径。'' = 这行可以提交。 */
export type DraftIssue = '' | 'noServer' | PathIssue

export function checkTargetDraft(d: TargetDraft): DraftIssue {
  if (d.serverId === '') return 'noServer'
  return checkTargetPaths(d.certPath, d.keyPath)
}

/** 丢掉空行、把两条路径与指令两端空白清掉,得到能逐条 POST /targets 的清单。 */
export function compactTargetDrafts(list: TargetDraft[]): TargetDraft[] {
  return list
    .filter((d) => !isBlankTargetDraft(d))
    .map((d) => ({
      serverId: d.serverId,
      certPath: d.certPath.trim(),
      keyPath: d.keyPath.trim(),
      postCommand: d.postCommand.trim(),
      autoDeploy: d.autoDeploy,
    }))
}

/** 搜索命中判定:域名、SAN、签发者与 CA 都参与,大小写不敏感。 */
export function matchesQuery(cert: Cert, query: string): boolean {
  const q = query.trim().toLowerCase()
  if (q === '') return true
  if (cert.domain.toLowerCase().includes(q)) return true
  if ((cert.sans ?? []).some((d) => d.toLowerCase().includes(q))) return true
  if (cert.issuer.toLowerCase().includes(q)) return true
  return cert.ca.toLowerCase().includes(q)
}

/** 按查询词筛证书,保持后端返回的顺序(后端已按到期时间排)。 */
export function filterCerts(certs: Cert[], query: string): Cert[] {
  if (query.trim() === '') return certs
  return certs.filter((c) => matchesQuery(c, query))
}

/** 「只看需要接手」这一档:与 `needsAttention` 同一判定,列表和汇总数才不会对不上。 */
export function filterAttentions(certs: Cert[]): Cert[] {
  return certs.filter(needsAttention)
}

/** 检查汇总的三桶;页面按它渲染顺序,这里按它计数。 */
export type CheckBucket = 'expiring' | 'renewed' | 'failed'
export const CHECK_BUCKETS: CheckBucket[] = ['expiring', 'renewed', 'failed']

/** 某一桶的条数(后端没回该桶算 0)。 */
export function checkBucketCount(report: CheckReport, bucket: CheckBucket): number {
  return report[bucket]?.length ?? 0
}

/** 这一轮有没有点到任何证书:三桶全空 = 一切还早,页面只显示「检查过 N 张,都还早」。 */
export function checkReportClean(report: CheckReport): boolean {
  return CHECK_BUCKETS.every((b) => checkBucketCount(report, b) === 0)
}
