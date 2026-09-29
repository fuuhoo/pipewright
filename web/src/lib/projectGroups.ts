/**
 * 项目分组视图的分桶逻辑 —— 纯函数,方便脱离页面验规则。
 *
 * 分组视图与卡片视图看的是同一份已筛选项目:这里只负责「按组切开、组内保持原顺序」,
 * 以及两件事不能同时成立时的取舍(见 hideEmptyGroups)。
 */
import type { Project } from '../api/projects'
import type { Group } from '../api/groups'
import { GROUP_ALL, GROUP_NONE } from './groupFilter'

/** 未归组那一段的 key:它同时也是合法的空 groupId。 */
const GROUP_NONE_KEY = ''

/** 分组视图里的一段:一个分组(或未归组),连同这一档当前要展示的项目。 */
export interface ProjectGroupSection {
  /** 折叠状态的键:分组 ID;未归组那段用空串。 */
  key: string
  /** 空串 = 未归组那段,展示名由调用方补(未归组是页面级说法)。 */
  name: string
  /** 未归组那段没有可见性可言。 */
  visibility: Group['visibility'] | null
  ownerName: string
  /** 当前用户能否管理这一档(未归组只有管理员能挪)。 */
  canManage: boolean
  projects: Project[]
  ungrouped: boolean
}

export interface GroupSectionsOptions {
  /**
   * 分组筛选下拉的当前值(GROUP_ALL / GROUP_NONE / 分组 ID)。
   * 分组视图同样要听它的话:下拉收到一个组时,不该把别的组也铺出来。
   */
  groupFilter?: string
  /** 有搜索词或状态筛选时置 true:没有命中的组不占一段,免得满屏「暂无项目」。 */
  hideEmptyGroups?: boolean
}

export function groupProjects(
  projects: Project[],
  groups: Group[],
  opts: GroupSectionsOptions = {},
): ProjectGroupSection[] {
  const { groupFilter = GROUP_ALL, hideEmptyGroups = false } = opts

  const buckets = new Map<string, Project[]>()
  for (const p of projects) {
    // 组不在可见列表里(理论上不该发生:项目可见就意味着组可见)也不丢,
    // 归到未归组那段兜住,卡片上的分组行仍会照实说「分组不存在」。
    const known = !p.groupId || groups.some((g) => g.id === p.groupId)
    const key = known ? p.groupId : GROUP_NONE_KEY
    const list = buckets.get(key)
    if (list) list.push(p)
    else buckets.set(key, [p])
  }

  const sections: ProjectGroupSection[] = groups.map((g) => ({
    key: g.id,
    name: g.name,
    visibility: g.visibility,
    ownerName: g.ownerName,
    canManage: g.canManage,
    projects: buckets.get(g.id) ?? [],
    ungrouped: false,
  }))

  sections.push({
    key: GROUP_NONE_KEY,
    name: '',
    visibility: null,
    ownerName: '',
    canManage: false,
    projects: buckets.get(GROUP_NONE_KEY) ?? [],
    ungrouped: true,
  })

  const wanted = groupFilter === GROUP_NONE ? GROUP_NONE_KEY : groupFilter
  const visible = groupFilter === GROUP_ALL ? sections : sections.filter((s) => s.key === wanted)
  if (!hideEmptyGroups) return visible
  // 有搜索/状态条件时空段不占位:满屏「暂无项目」的分组列表比一条「没有匹配」更骗人。
  return visible.filter((s) => s.projects.length > 0)
}
