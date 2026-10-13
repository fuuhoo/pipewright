/**
 * 构建环境列表排序的纯逻辑 —— 排序键、方向与「默认(后端 sort_order)之间的关系。
 *
 * 页面默认的 sort_order 是管理员手工维护的权威顺序,所以它必须是可回到的一档,
 * 而不是「取消排序后剩下的乱序」;名称档走自然序,Node 2 排在 Node 10 前面。
 */
import type { BuildEnv } from '../api/buildEnvs'

/** order=沿用后端 sort_order;name=按显示名;created=按创建时间。 */
export type BuildEnvSortKey = 'order' | 'name' | 'created'
export type BuildEnvSortDir = 'asc' | 'desc'

export interface BuildEnvSort {
  key: BuildEnvSortKey
  dir: BuildEnvSortDir
}

/** 默认排序:后端 sort_order 原序,升序(方向按钮此时不可用)。 */
export const DEFAULT_SORT: BuildEnvSort = { key: 'order', dir: 'asc' }

/** RFC3339 → 毫秒;解析不出来就当最早,让脏时间戳稳定沉到一端而不是抛错。 */
function ts(iso: string): number {
  const ms = Date.parse(iso)
  return Number.isNaN(ms) ? Number.NEGATIVE_INFINITY : ms
}

function byName(a: BuildEnv, b: BuildEnv): number {
  const opts: Intl.CollatorOptions = { numeric: true, sensitivity: 'base' }
  return (
    a.displayName.localeCompare(b.displayName, undefined, opts) ||
    a.language.localeCompare(b.language, undefined, opts) ||
    a.version.localeCompare(b.version, undefined, opts)
  )
}

function byCreated(a: BuildEnv, b: BuildEnv): number {
  return ts(a.createdAt) - ts(b.createdAt)
}

/**
 * 返回排好序的新数组(不改入参)。key=order 时原样返回:那份顺序是管理员设定的,
 * 前端不做任何再解释。
 */
export function sortBuildEnvs(envs: BuildEnv[], sort: BuildEnvSort): BuildEnv[] {
  if (sort.key === 'order') return [...envs]
  const cmp = sort.key === 'name' ? byName : byCreated
  const sign = sort.dir === 'desc' ? -1 : 1
  return [...envs].sort((a, b) => {
    const d = sign * cmp(a, b)
    // 同名/同时间戳仍按 sort_order 收口,免得两次渲染行序抖动。
    return d !== 0 ? d : a.sortOrder - b.sortOrder
  })
}

/** 方向是否可用:order 档没有「反向的权威顺序」可言。 */
export function dirEnabled(sort: BuildEnvSort): boolean {
  return sort.key !== 'order'
}

/** 点已选中的排序键 = 翻方向;点别的键 = 切过去并按该键的自然方向(时间看最新、名称看 A 开头)。 */
export function nextSort(current: BuildEnvSort, key: BuildEnvSortKey): BuildEnvSort {
  // order 档没有方向可言:再点一次就是把视图收回权威顺序。
  if (key === 'order') return { ...DEFAULT_SORT }
  if (current.key === key) {
    return { key, dir: current.dir === 'asc' ? 'desc' : 'asc' }
  }
  return { key, dir: key === 'created' ? 'desc' : 'asc' }
}

const STORAGE_KEY = 'pipewright.buildEnvs.sort'

/** 读偏好;localStorage 抛(隐私模式)或值不认识都退回默认。 */
export function loadSort(): BuildEnvSort {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return DEFAULT_SORT
    const [key, dir] = raw.split('|')
    if (key !== 'order' && key !== 'name' && key !== 'created') return DEFAULT_SORT
    if (dir !== 'asc' && dir !== 'desc') return DEFAULT_SORT
    return { key, dir }
  } catch {
    return DEFAULT_SORT
  }
}

export function saveSort(sort: BuildEnvSort): void {
  try {
    localStorage.setItem(STORAGE_KEY, `${sort.key}|${sort.dir}`)
  } catch {
    // 存不下不影响排序本身,只是下次进来回到默认。
  }
}
