/**
 * 通知记录页的纯换算 —— 筛选状态 ↔ 地址栏 ↔ 接口参数,以及事件/结论的展示键。
 *
 * 为什么值得单独拎出来:
 *  · 地址栏的值有三种形态(缺、单个字符串、同名重复),而「第 0 页」这种脏值不该变成请求;
 *  · 「全部」这类默认值如果留在 URL 上,分享出去的链接就带着三个没意义的空参数,
 *    从别人那条链接进来的用户也看不出自己其实没在筛选;
 *  · 事件名与结论的文案键由命名规则推出来,规则错了页面就整列显示 `evBuild...` 这种键路径 ——
 *    这条在渲染层看着像「翻译没配」,实际是命名漂移。
 */
import type { ListDeliveriesParams } from '../api/notifications'

/** 事件 ID → 设置页那 11 个事件名所用的键(那份文案已翻过 8 语种,这里复用不另写一套)。 */
const EVENT_KEY_PREFIX = 'settingsNotifications.ev'

/** 台账结论 → 本页自己的键。 */
const STATUS_KEYS: Record<string, string> = {
  ok: 'notificationRecords.statusOk',
  failed: 'notificationRecords.statusFailed',
  skipped: 'notificationRecords.statusSkipped',
}

export interface DeliveryFilters {
  /** '' = 全部事件。 */
  event: string
  /** '' = 全部结论。 */
  status: string
  /** '' = 全部项目。 */
  projectId: string
  page: number
}

export const DEFAULT_FILTERS: DeliveryFilters = { event: '', status: '', projectId: '', page: 1 }

/**
 * 地址栏取值:只认非空字符串。
 *
 * 同名重复(?event=a&event=b)会被 vue-router 给成数组,这里连同数字一起当作「没筛」——
 * 与运行页的项目筛选同一口径(runProjectFilter.resolveProjectFilter),一个产品里两套规则迟早答不一致。
 */
function queryString(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

/** 页码:非正整数一律回第 1 页(脏 URL 不该发一次注定为空的请求,也不该报错)。 */
function resolvePage(value: unknown): number {
  const raw = queryString(value)
  if (raw === '') return 1
  const n = Number.parseInt(raw, 10)
  return Number.isFinite(n) && n >= 1 ? n : 1
}

/** 从地址栏还原筛选状态。 */
export function resolveFilters(query: Record<string, unknown>): DeliveryFilters {
  return {
    event: queryString(query.event),
    status: queryString(query.status),
    projectId: queryString(query.projectId),
    page: resolvePage(query.page),
  }
}

/**
 * 写回地址栏:默认值整个删掉,而不是留成空串。
 *
 * 空串的 `?event=` 看着像筛了个不存在的事件;`?page=1` 则让「翻过页又退回第 1 页」的链接
 * 与「从没翻页」的链接长得不一样,前进后退的对比也跟着变脏。其余无关参数原样保留。
 */
export function withFilters(
  query: Record<string, string>,
  f: DeliveryFilters,
): Record<string, string> {
  const next = { ...query }
  // 一律按字符串比:页码在状态里是数字、在 URL 里是字符串,拿 '1' !== 1 会比出「非默认」,
  // 于是退回第 1 页也留着一个 page=1。
  const keep = (key: keyof DeliveryFilters, value: string): void => {
    if (value && value !== String(DEFAULT_FILTERS[key])) next[key] = value
    else delete next[key]
  }
  keep('event', f.event)
  keep('status', f.status)
  keep('projectId', f.projectId)
  keep('page', String(f.page))
  return next
}

/** 该发给台账接口的查询参数(页码总是带上,后端按页返回 items/total)。 */
export function toListParams(f: DeliveryFilters, pageSize: number): ListDeliveriesParams {
  return {
    event: f.event || undefined,
    status: f.status || undefined,
    projectId: f.projectId || undefined,
    page: f.page,
    pageSize,
  }
}

/** 是否正在筛选(决定空列表说的是「还没有记录」还是「这个筛选下没有记录」)。 */
export function hasActiveFilters(f: DeliveryFilters): boolean {
  return f.event !== '' || f.status !== '' || f.projectId !== ''
}

/**
 * 事件 ID 的文案键:`build_failed` → `settingsNotifications.evBuildFailed`。
 *
 * 下划线切词后逐段首字母大写,和设置页那批 `evXxx` 键同源;后端将来加事件时这里会跟着生成新键,
 * 由调用方(te)判断该键有没有文案,没有就退成原始事件 ID —— 宁可少一句翻译,不能显示键路径。
 */
export function eventLabelKey(event: string): string {
  const camel = event
    .split('_')
    .filter(Boolean)
    .map((part, i) => (i === 0 ? part.toLowerCase() : part[0].toUpperCase() + part.slice(1).toLowerCase()))
    .join('')
  if (!camel) return ''
  return `${EVENT_KEY_PREFIX}${camel[0].toUpperCase()}${camel.slice(1)}`
}

/** 结论的文案键;认不出来回空串,由调用方退成原始值。 */
export function statusLabelKey(status: string): string {
  return STATUS_KEYS[status] ?? ''
}

/** 页脚文案要的总页数(至少 1 页,0 条也显示「第 1 / 1 页」而不是「第 1 / 0 页」)。 */
export function totalPages(total: number, pageSize: number): number {
  if (total <= 0 || pageSize <= 0) return 1
  return Math.ceil(total / pageSize)
}
