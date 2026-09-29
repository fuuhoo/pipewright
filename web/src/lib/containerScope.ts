/**
 * 容器总览「看哪台服务器」的纯逻辑。
 *
 * 总览页把每台可见机器摊成一张卡片,机器一多就得滚动找,所以加一档「全部 / 只看这台」的
 * 范围。范围只决定这一屏展示什么,**不在这重推权限** —— 候选永远来自后端已按分组收敛的
 * 聚合结果(见 httpapi/server_containers.go 的 visibleGroups → ListScoped)。
 */
import type { ServerContainers } from '../api/containers'

/** 哨兵:不切换、看全部。真实机器 ID 是 UUID,不会撞上这个值。 */
export const SERVER_ALL = 'all'
/** 哨兵:只看连得上的那几台(死机在总览里除了占位没有别的作用)。 */
export const SERVER_USABLE = '~usable'

export type ServerScope = typeof SERVER_ALL | typeof SERVER_USABLE | string

/** 每轮聚合都换一批新对象,所以「选中的机器还在不在」得每次现查,不能存副本。 */
export function resolveServerScope(scope: ServerScope, groups: readonly ServerContainers[]): ServerScope {
  if (scope === SERVER_ALL || scope === SERVER_USABLE) return scope
  return groups.some((g) => g.serverId === scope) ? scope : SERVER_ALL
}

export function scopedGroups<T extends { serverId: string; reachable: boolean }>(
  groups: readonly T[],
  scope: ServerScope,
): T[] {
  if (scope === SERVER_ALL) return [...groups]
  if (scope === SERVER_USABLE) return groups.filter((g) => g.reachable)
  return groups.filter((g) => g.serverId === scope)
}

/**
 * 在线的排前面、离线的沉底(与服务器状态页同一口径)。两组内部保持上屏顺序,所以逐台流式
 * 上来的卡不会因为排序而反复换位 —— 只有「可达 / 不可达」这一档决定前后。
 */
export function reachableFirst<T extends { reachable: boolean }>(groups: readonly T[]): T[] {
  const up: T[] = []
  const down: T[] = []
  for (const g of groups) (g.reachable ? up : down).push(g)
  return up.length && down.length ? [...up, ...down] : [...groups]
}

/** 切换按钮上的状态点:连不上(红)/ 连着但没装 docker(琥珀)/ 可用(绿)。 */
export function scopeTone(g: Pick<ServerContainers, 'reachable' | 'runtime'>): 'unreachable' | 'no-runtime' | 'ok' {
  if (!g.reachable) return 'unreachable'
  return g.runtime ? 'ok' : 'no-runtime'
}

/**
 * 新建 / 清理弹窗的机器顺序:当前聚焦那台排最前(弹窗默认取第一台,点开就落在正看着的这台机器上)。
 * 但候选不跟着范围收敛 —— 只看一台不代表别处就不能建容器,那会凭空禁掉本来可用的机器。聚焦那台
 * 没装 docker 时它本就不在候选里,顺序原样保留。
 */
export function preferredFirst<T extends { id: string }>(options: readonly T[], scope: ServerScope): T[] {
  if (scope === SERVER_ALL) return [...options]
  return [...options.filter((o) => o.id === scope), ...options.filter((o) => o.id !== scope)]
}
