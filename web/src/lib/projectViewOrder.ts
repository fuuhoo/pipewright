/**
 * 项目页「项目卡片」的我的顺序:拖动排序,每用户一份(存 /api/account/project-view)。
 *
 * 和 serverViewOrder(容器页主机卡片)同一口径:这里只做排序,不判权限也不取数。
 * 列表存的是项目 ID —— 项目改名、改状态都不影响先后;order 里查不到的(新建的、
 * 别人建的)保持默认顺序沉到末尾。
 */

/** 落点:拖到目标卡片上半 = 排在它前面,下半 = 排在它后面。 */
export type DropEdge = 'before' | 'after'

export interface ProjectPrefs {
  order: readonly string[]
}

export const EMPTY_PROJECT_PREFS: ProjectPrefs = { order: [] }

/** 没动过手就别插手:后端给的默认顺序该什么样还什么样。 */
export function hasCustomOrder(prefs: ProjectPrefs): boolean {
  return prefs.order.length > 0
}

/** 卡片顺序 = 按 saved order 排,没排过的保持入参顺序垫底。 */
export function rankProjects<T extends { id: string }>(projects: readonly T[], prefs: ProjectPrefs): T[] {
  if (!hasCustomOrder(prefs)) return [...projects]
  const orderAt = new Map(prefs.order.map((id, i) => [id, i] as const))
  const tail = Number.MAX_SAFE_INTEGER
  return projects
    .map((p, i) => ({ p, i, at: orderAt.get(p.id) ?? tail }))
    .sort((a, b) => a.at - b.at || a.i - b.i)
    .map((x) => x.p)
}

/**
 * 拖动 → 整份新的 id 顺序。
 *
 * 入参是**全部**项目而不是当前筛出来那几张卡:搜索/分组正收窄时拖动,排的是底下的全集,
 * 筛子一撤顺序还在。源或落点不在这份名单里(数据正好在换)就原样返回,不猜位置。
 */
export function moveProjectId(
  ids: readonly string[],
  movingId: string,
  targetId: string,
  edge: DropEdge,
): string[] {
  if (movingId === targetId) return [...ids]
  const from = ids.indexOf(movingId)
  const target = ids.indexOf(targetId)
  if (from < 0 || target < 0) return [...ids]
  const out = ids.filter((id) => id !== movingId)
  // 摘走一张后,它后面那些整体上移一位 —— 落点按原始下标算,再校正。
  const at = target > from ? target - 1 : target
  out.splice(edge === 'before' ? at : at + 1, 0, movingId)
  return out
}
