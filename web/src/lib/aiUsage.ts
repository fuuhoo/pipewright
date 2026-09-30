/**
 * AI 单次调用的 token 用量展示口径(三处结果卡共用:运行诊断 / 容器诊断 / 风险标注)。
 *
 * 后端只在模型真的回传 usage 时才给非零值(部分兼容端点不回),所以拿不到就整行不显示,
 * 不用 0 冒充「用了 0 个 token」。
 */

export interface AITokenUsage {
  prompt: number
  completion: number
}

/** 拆成 i18n 插值参数;拿不到用量 → null(调用方据此隐藏整行)。 */
export function aiUsageParts(usage?: AITokenUsage | null): { prompt: string; completion: string } | null {
  if (!usage || (usage.prompt <= 0 && usage.completion <= 0)) return null
  return { prompt: groupThousands(usage.prompt), completion: groupThousands(usage.completion) }
}

/** 千分位分组(固定半角逗号,不跟浏览器语言走,免得同一次结果里两种数字格式)。 */
function groupThousands(n: number): string {
  const safe = Number.isFinite(n) && n > 0 ? Math.round(n) : 0
  return String(safe).replace(/\B(?=(\d{3})+$)/g, ',')
}
