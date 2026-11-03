/**
 * 容器页主机卡片的「我的顺序」:置顶 + 拖动排序,每用户一份(存 /api/account/server-view)。
 *
 * 这里只做排序,不判权限也不取数 —— 候选永远是后端按分组收敛过的聚合结果。
 * 两个列表存的都是服务器 ID:卡片每 60s 换一批新对象,按 id 排序才谈得拢「这一轮上屏的
 * 还是那几台」;order 里查不到的(新登记的、这轮才采出来的)沉到各自档位末尾。
 */

/** 落点:拖到目标卡片上半 = 排在它前面,下半 = 排在它后面。 */
export type DropEdge = 'before' | 'after'

export interface HostPrefs {
  pinned: readonly string[]
  order: readonly string[]
}

export const EMPTY_PREFS: HostPrefs = { pinned: [], order: [] }

/** 没动过手就别插手:默认排序(在线的在前、离线的沉底)该什么样还什么样。 */
export function hasCustomOrder(prefs: HostPrefs): boolean {
  return prefs.pinned.length > 0 || prefs.order.length > 0
}

/**
 * 卡片顺序 = 先分置顶/未置顶两档,档内先按 saved order、没排过的那台再按置顶数组、都查不到的保持入参顺序垫底。
 *
 * 入参通常是 reachableFirst 的结果,所以「只置顶、没拖过」时在线的在前这条仍然成立;
 * 一旦拖出过 order,档内就完全听人的 —— 离线机器不会因为下一轮刷新又爬回上面去。
 * order 一旦存在就覆盖全集(拖动时按整份名单写),所以「置顶数组决定档内前后」只在还没拖过的时候起作用。
 */
export function rankHosts<T extends { serverId: string }>(hosts: readonly T[], prefs: HostPrefs): T[] {
  if (!hasCustomOrder(prefs)) return [...hosts]
  const pinnedAt = new Map(prefs.pinned.map((id, i) => [id, i] as const))
  const orderAt = new Map(prefs.order.map((id, i) => [id, i] as const))
  const tail = Number.MAX_SAFE_INTEGER
  return hosts
    .map((h, i) => {
      const pin = pinnedAt.get(h.serverId)
      return { h, i, tier: pin === undefined ? 1 : 0, at: orderAt.get(h.serverId) ?? pin ?? tail }
    })
    .sort((a, b) => a.tier - b.tier || a.at - b.at || a.i - b.i)
    .map((x) => x.h)
}

/**
 * 拖动 → 整份新的 id 顺序。
 *
 * 入参是**全部**主机而不是当前筛出来那几张卡:搜索/分组正收窄时拖动,排的是底下的全集,
 * 筛子一撤顺序还在(这正是「按 id 排序」换来的,别只传可视区)。
 * 源或落点不在这份名单里(数据正好在换)就原样返回,不猜位置。
 */
export function moveHostId(
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

/** 置顶开关:新置顶的排到最前 —— 刚点这台,就是要看它冒尖。 */
export function togglePinned(pinned: readonly string[], id: string): string[] {
  return pinned.includes(id) ? pinned.filter((p) => p !== id) : [id, ...pinned]
}
