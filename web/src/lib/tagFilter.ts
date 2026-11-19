/**
 * 项目标签的公共口径:筛选下拉、命中判定、录入时的合并去重,全是纯函数。
 *
 * 标签只用来「分类看项目」:不参与权限,也不参与流水线,所以筛选在项目页本地做
 * (项目列表本来就全量取回),和分组 / 归属服务筛选同一套做法。
 */

/** 与后端 project.MaxTags / MaxTagLen 同口径;前端先挡一道,后端仍是最终裁判。 */
export const MAX_TAGS = 20
export const MAX_TAG_LEN = 24

export interface TagOwner {
  tags?: string[]
}

/**
 * 全部出现过的标签(去重不分大小写,保留先出现的写法)。
 * 按字母序排,让不同项目里添加顺序不同也能得到同一份下拉候选。
 */
export function allTags(projects: TagOwner[]): string[] {
  const seen = new Map<string, string>()
  for (const p of projects) {
    for (const raw of p.tags ?? []) {
      const tag = raw.trim()
      if (!tag) continue
      const key = tag.toLowerCase()
      if (!seen.has(key)) seen.set(key, tag)
    }
  }
  return [...seen.values()].sort((a, b) => a.localeCompare(b))
}

/** 命中判定:未选标签时全部放行;选了多个时命中任一即可(不区分大小写)。 */
export function matchesTagFilter(tags: string[] | undefined, filters: string[]): boolean {
  if (!filters.length) return true
  const wanted = new Set(filters.map((tag) => tag.toLowerCase()))
  return (tags ?? []).some((tag) => wanted.has(tag.toLowerCase()))
}

/**
 * 录入时把一段文本里的标签并进已有列表:
 * 按逗号 / 中文逗号 / 换行拆开,去空白,去重(不分大小写),保序。
 * 数量与长度上限由调用方按 MAX_TAGS / MAX_TAG_LEN 判断并提示。
 */
export function mergeTags(existing: string[], chunk: string): string[] {
  const out = [...existing]
  const seen = new Set(out.map((tag) => tag.toLowerCase()))
  for (const piece of chunk.split(/[,，\r\n]+/)) {
    const tag = piece.trim()
    if (!tag) continue
    const key = tag.toLowerCase()
    if (seen.has(key)) continue
    seen.add(key)
    out.push(tag)
  }
  return out
}