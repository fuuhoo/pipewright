/**
 * 「语言」字段的纯逻辑 —— 下拉选项怎么拼、手输的值怎么归一。
 *
 * 为什么这个字段不让自由输入:它下游全是**精确字符串匹配** —— 流水线里「配置资源」按
 * `profile.language === env.language` 自动配对,执行命令模板也按 language 归类。拼错不报错,
 * 只是配不上。库里已经留了证据:构建环境表里 `go` 和 `golang` 同时存在,就是两份同义语言。
 *
 * 所以入口只承认三件事:从已有语言里选、走「自定义新增」填一个新标识、或选「其他」
 * (不绑定语言的资源,如一份通用 shell 脚本)。
 */

/** 「其他」的落库值。后端要求 language 非空(buildenv/model.go:78、configprofile/model.go:74),所以用真实 token 而不是空串。 */
export const OTHER_LANGUAGE = 'other'

/** 「自定义新增」在 select 里的哨兵值,只在 UI 层出现,不会落库。 */
export const CUSTOM_SENTINEL = '__custom__'

export type LanguageOptionKind = 'known' | 'current' | 'other' | 'custom'

export interface LanguageOption {
  value: string
  label: string
  kind: LanguageOptionKind
}

/** 归一用户输入的语言标识:小写、去首尾空白、内部空白折成 '-',避免 `Node JS` 这类变体。 */
export function normalizeLanguage(raw: string): string {
  return raw.trim().toLowerCase().replace(/\s+/g, '-')
}

/** 自然序比较(Node 10 排在 Node 2 后面那一类的反面:数字档按数值走)。 */
function byNaturalOrder(a: string, b: string): number {
  return a.localeCompare(b, undefined, { numeric: true, sensitivity: 'base' })
}

/** 去重 + 自然序;'other' 由选项表统一收口,这里先剔除。 */
export function sortLanguages(values: readonly string[]): string[] {
  const set = new Set<string>()
  for (const v of values) {
    const n = normalizeLanguage(v)
    if (n && n !== OTHER_LANGUAGE) set.add(n)
  }
  return [...set].sort(byNaturalOrder)
}

interface LanguageRow {
  language?: string | null
}

/** 从一批记录(构建环境 / 配置资源)提取已用语言。 */
export function languagesFrom(rows: readonly LanguageRow[]): string[] {
  return sortLanguages(rows.map((r) => r?.language ?? ''))
}

/** 把若干来源(如「本页记录」+「构建环境目录」)并成一份选项语言表。 */
export function mergeLanguageLists(...lists: readonly (readonly string[])[]): string[] {
  return sortLanguages(lists.flat())
}

/**
 * 拼出 select 的选项:已添加的语言(自然序)→ 「其他」→ 「自定义新增」。
 *
 * `current` 是记录当前值:库里可能有下拉里没有的历史值,那种情况把它顶到第一位,
 * 否则编辑一条记录会把它静默改成别的语言。
 */
export function buildLanguageOptions(input: {
  current: string
  known: readonly string[]
  otherLabel: string
  customLabel: string
}): LanguageOption[] {
  const known = sortLanguages(input.known)
  const options: LanguageOption[] = known.map((value) => ({ value, label: value, kind: 'known' }))
  const current = normalizeLanguage(input.current)
  if (current && current !== OTHER_LANGUAGE && !known.includes(current)) {
    options.unshift({ value: current, label: current, kind: 'current' })
  }
  options.push({ value: OTHER_LANGUAGE, label: input.otherLabel, kind: 'other' })
  options.push({ value: CUSTOM_SENTINEL, label: input.customLabel, kind: 'custom' })
  return options
}

/**
 * 切到「自定义新增」后,填进来的值是否已经和某个已有语言撞车(大小写/空格差异也算)。
 * 撞车时不再新增一份同义语言,而是直接复用已有的那个。
 */
export function matchExistingLanguage(draft: string, known: readonly string[]): string | null {
  const n = normalizeLanguage(draft)
  if (!n) return null
  for (const raw of known) {
    if (normalizeLanguage(raw) === n) return n
  }
  return null
}
