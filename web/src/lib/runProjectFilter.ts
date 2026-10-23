/**
 * 运行记录页的项目筛选 —— 状态存在地址栏(?projectId=),这里只做两处纯换算。
 *
 * 为什么值得单独拎出来:query 的值有三种形态(缺、单个字符串、同名重复),
 * 「筛选中的项目不在可见清单里」也不是空串能表达的 —— 这两条都靠渲染层试不出来。
 */

/** 地址栏里的 projectId;只有非空字符串才算「按某项目筛」,其余一律当没筛。 */
export function resolveProjectFilter(value: unknown): string {
  return typeof value === 'string' && value !== '' ? value : ''
}

export interface ProjectNameSource {
  id: string
  name: string
}

/**
 * 筛选下拉里该显什么名字。
 *
 * 清单里找不到时回退成 id 前 8 位,而不是回退成「全部项目」:项目被删或换了可见范围后,
 * 列表确实是空的,但把筛选说成「全部」就等于把自己带到的那一屏说成别的筛选结果。
 */
export function projectFilterLabel(projects: ProjectNameSource[], id: string): string {
  if (!id) return ''
  const hit = projects.find((p) => p.id === id)
  if (hit) return hit.name
  return id.slice(0, 8)
}

/** 写回地址栏:有值就带上,没值就整个删掉(空串参数会让人以为筛了个空项目)。 */
export function withProjectFilter(
  query: Record<string, string>,
  id: string,
): Record<string, string> {
  const next = { ...query }
  if (id) next.projectId = id
  else delete next.projectId
  return next
}
