/**
 * 运行记录页的项目筛选 —— 状态存在地址栏(?projectId=),这里只做纯换算。
 *
 * 为什么值得单独拎出来:query 的值有三种形态(缺、单个字符串、同名重复),
 * 「筛选中的项目不在可见清单里」也不是空串能表达的 —— 这两条都靠渲染层试不出来。
 * 项目下拉的搜索匹配与分组收窄同理:命中哪几个字段、已选项要不要补回候选,
 * 都是「看不见但会答错」的规则。
 */
import { matchesGroup } from './groupFilter'

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

export interface ProjectOptionSource extends ProjectNameSource {
  /** 所属分组;缺省与空串同义(未归组)。 */
  groupId?: string
  repoUrl?: string
  defaultBranch?: string
}

/**
 * 搜索命中判定:名称、仓库地址、默认分支都参与,大小写不敏感的子串。
 *
 * 口径与项目页那个搜索框对齐(见 views/Projects.vue),同一个词在两页应命中同一批项目。
 */
export function matchesProjectQuery(project: ProjectOptionSource, query: string): boolean {
  const q = query.trim().toLowerCase()
  if (q === '') return true
  if (project.name.toLowerCase().includes(q)) return true
  if ((project.repoUrl ?? '').toLowerCase().includes(q)) return true
  return (project.defaultBranch ?? '').toLowerCase().includes(q)
}

/**
 * 项目下拉的候选:先按分组收窄,再按查询词过滤,保持后端回来的顺序。
 *
 * 已选项在「没在输入关键词」时补回候选,哪怕它不属于当前分组:分组下拉只是收窄选项目的
 * 视野,列表仍在按已选的那个项目筛(URL 上的 projectId 没动)。此时把它从候选里抹掉,
 * 控件就变成一个「显示着值、但列表里找不到自己」的下拉 —— 用户只能重新搜一遍才能换掉它。
 * 输入了关键词就照关键词说话,不再兜这个底。
 */
export function narrowProjectOptions<T extends ProjectOptionSource>(
  projects: T[],
  opts: { query: string; groupFilter: string; selectedId: string },
): T[] {
  let list = projects.filter((p) => matchesGroup(p.groupId, opts.groupFilter))
  if (opts.query.trim() !== '') {
    list = list.filter((p) => matchesProjectQuery(p, opts.query))
    return list
  }
  if (opts.selectedId && !list.some((p) => p.id === opts.selectedId)) {
    const selected = projects.find((p) => p.id === opts.selectedId)
    if (selected) list = [selected, ...list]
  }
  return list
}
