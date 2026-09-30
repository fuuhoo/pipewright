/**
 * AI token 用量的展示口径,两处各有一条规则:
 *   - 单次调用(三处结果卡:运行诊断 / 容器诊断 / 风险标注):后端只在模型真的回传 usage
 *     时才给非零值(部分兼容端点不回),拿不到就整行不显示,不用 0 冒充「用了 0 个 token」。
 *   - 本月累计(AI 设置页的「已使用」):0 是有意义的事实 —— 这个月确实还没用过,
 *     所以要用「本月还没用过」说清楚,而不是把数字藏掉。
 */

export interface AITokenUsage {
  prompt: number
  completion: number
}

/** 某一档协议本自然月(UTC)的累计用量,与月 Token 上限同口径。 */
export interface AIMonthUsage {
  monthPrompt: number
  monthCompletion: number
}

/** 拆成 i18n 插值参数;拿不到用量 → null(调用方据此隐藏整行)。 */
export function aiUsageParts(usage?: AITokenUsage | null): { prompt: string; completion: string } | null {
  if (!usage || (usage.prompt <= 0 && usage.completion <= 0)) return null
  return { prompt: groupThousands(usage.prompt), completion: groupThousands(usage.completion) }
}

/** 本月用量的展示结果(type 而非 interface:vue-i18n 的具名插值要隐式索引签名)。 */
export type AIMonthUsageParts = {
  prompt: string
  completion: string
  /** true = 本月一笔用量都没记上(调用方换「本月还没用过」措辞,别摆两个 0)。 */
  empty: boolean
}

/** 本月累计的展示参数;服务端没给这个字段(older payload)→ null。 */
export function aiMonthUsageParts(usage?: AIMonthUsage | null): AIMonthUsageParts | null {
  if (!usage) return null
  const prompt = Math.max(0, Number.isFinite(usage.monthPrompt) ? usage.monthPrompt : 0)
  const completion = Math.max(0, Number.isFinite(usage.monthCompletion) ? usage.monthCompletion : 0)
  return {
    prompt: groupThousands(prompt),
    completion: groupThousands(completion),
    empty: prompt <= 0 && completion <= 0,
  }
}

/** 千分位分组(固定半角逗号,不跟浏览器语言走,免得同一次结果里两种数字格式)。 */
function groupThousands(n: number): string {
  const safe = Number.isFinite(n) && n > 0 ? Math.round(n) : 0
  return String(safe).replace(/\B(?=(\d{3})+$)/g, ',')
}
