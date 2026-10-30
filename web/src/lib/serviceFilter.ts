/**
 * 归属服务筛选的公共口径(项目页 / 运行页共用一个说法)。
 *
 * 与分组筛选同一套哨兵写法:空串是合法 serviceId(它就是「未归属」那一档),
 * 所以「全部」与「未归属」都必须用哨兵值区分。
 */
import type { Service } from '../api/services'

export const SERVICE_ALL = 'all'
export const SERVICE_NONE = '__unassigned__'

export interface ServiceOption {
  value: string
  label: string
}

/** 文案由调用方从 i18n 传进来:lib 不依赖 vue-i18n。 */
export interface ServiceLabels {
  all: string
  unassigned: string
}

/** 下拉候选:全部 / 未归属 / 每个服务(按后端给的名字升序)。 */
export function serviceOptions(services: Service[], labels: ServiceLabels): ServiceOption[] {
  return [
    { value: SERVICE_ALL, label: labels.all },
    { value: SERVICE_NONE, label: labels.unassigned },
    ...services.map((s) => ({ value: s.id, label: s.name })),
  ]
}

/** 命中判定:全部放行 / 未归属只留空串 / 其余按服务 ID 精确。 */
export function matchesService(serviceId: string | undefined, filter: string): boolean {
  if (filter === SERVICE_ALL) return true
  if (filter === SERVICE_NONE) return !serviceId
  return serviceId === filter
}
