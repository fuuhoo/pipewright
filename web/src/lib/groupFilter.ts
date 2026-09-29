/**
 * 分组筛选的公共口径:下拉候选、命中判定、组名回显,全是纯函数。
 *
 * 原来只有项目页有这套逻辑(哨兵常量在 `projectGroups.ts` 里),服务器状态页与容器页
 * 要加同一个筛选,就把它收成一份。**容器没有自己的分组**:后端没有 KindContainer,
 * 容器经 SSH 从宿主服务器现采,归属就是宿主那台机器的组(见 docs/权限架构说明.md §3),
 * 所以容器页调用时传进来的是「宿主服务器的 groupId」。
 */
import type { Group } from '../api/groups'

/**
 * 分组筛选下拉的哨兵值。用哨兵而不是空串:空串是合法 groupId(它就是「未归组」那一档)。
 */
export const GROUP_ALL = 'all'
export const GROUP_NONE = '__ungrouped__'

/** 带归属的资源(服务器 / 集群 / 被填了宿主组的容器行)。 */
export interface GroupOwned {
  groupId?: string
}

export interface GroupOption {
  value: string
  label: string
}

/** 文案由调用方从 i18n 传进来:lib 不依赖 vue-i18n,才方便脱离页面验规则。 */
export interface GroupLabels {
  all: string
  ungrouped: string
  missing: string
  public: string
  private: string
}

/** 下拉候选:全部 / 未归组 / 每个可见分组(带可见性后缀,与归组弹窗同一说法)。 */
export function groupOptions(groups: Group[], labels: GroupLabels): GroupOption[] {
  return [
    { value: GROUP_ALL, label: labels.all },
    { value: GROUP_NONE, label: labels.ungrouped },
    ...groups.map((g) => ({
      value: g.id,
      label: `${g.name} · ${g.visibility === 'public' ? labels.public : labels.private}`,
    })),
  ]
}

/** 命中判定:全部放行 / 未归组只留空串 / 其余按组 ID 精确。 */
export function matchesGroup(groupId: string | undefined, filter: string): boolean {
  if (filter === GROUP_ALL) return true
  const id = groupId ?? ''
  return filter === GROUP_NONE ? id === '' : id === filter
}

export function filterByGroup<T extends GroupOwned>(items: T[], filter: string): T[] {
  if (filter === GROUP_ALL) return items
  return items.filter((item) => matchesGroup(item.groupId, filter))
}

/**
 * 组名回显:未归组照实说;组不在可见名单里(被删了或自己没权限看到)说「分组已失效」,
 * 而不是静默当成未归组 —— 那种机器/容器还在屏上,骗人说它没主更容易误操作。
 */
export function groupLabel(groupId: string | undefined, groups: Group[], labels: GroupLabels): string {
  const id = groupId ?? ''
  if (!id) return labels.ungrouped
  return groups.find((g) => g.id === id)?.name ?? labels.missing
}
