import { describe, expect, it } from 'vitest'

import type { Group } from '../api/groups'
import {
  GROUP_ALL,
  GROUP_NONE,
  filterByGroup,
  groupIdsOf,
  groupLabel,
  groupNames,
  groupOptions,
  matchesGroup,
} from './groupFilter'

function group(id: string, name: string, over: Partial<Group> = {}): Group {
  return {
    id,
    name,
    description: '',
    visibility: 'public',
    ownerId: 'u-1',
    ownerName: 'fu',
    members: [],
    projectCount: 0,
    serverCount: 0,
    variableGroupCount: 0,
    canManage: false,
    createdAt: '',
    updatedAt: '',
    ...over,
  } as Group
}

const LABELS = {
  all: '全部分组',
  ungrouped: '未归组',
  missing: '分组已失效',
  public: '公开',
  private: '私有',
}

/** 只带归属的最小行:服务器 / 容器(宿主的组)都长这样。 */
function row(id: string, groupId?: string): { id: string; groupId?: string } {
  return { id, groupId }
}

describe('groupOptions', () => {
  it('把全部 / 未归组放在最前,分组按可见性加后缀', () => {
    const opts = groupOptions([group('g-pay', '支付'), group('g-core', '核心', { visibility: 'private' })], LABELS)
    expect(opts).toEqual([
      { value: GROUP_ALL, label: '全部分组' },
      { value: GROUP_NONE, label: '未归组' },
      { value: 'g-pay', label: '支付 · 公开' },
      { value: 'g-core', label: '核心 · 私有' },
    ])
  })

  it('一个分组都没有时也只剩全部 / 未归组两档', () => {
    expect(groupOptions([], LABELS).map((o) => o.value)).toEqual([GROUP_ALL, GROUP_NONE])
  })
})

describe('matchesGroup / filterByGroup', () => {
  const rows = [row('s-1', 'g-pay'), row('s-2'), row('s-3', 'g-core'), row('s-4', 'g-gone')]

  it('全部放行,未归组只留空串那一档', () => {
    expect(filterByGroup(rows, GROUP_ALL).map((r) => r.id)).toEqual(['s-1', 's-2', 's-3', 's-4'])
    expect(filterByGroup(rows, GROUP_NONE).map((r) => r.id)).toEqual(['s-2'])
  })

  it('按组 ID 精确筛,筛完保持原顺序', () => {
    expect(filterByGroup(rows, 'g-pay').map((r) => r.id)).toEqual(['s-1'])
    expect(filterByGroup([row('b', 'g'), row('a', 'g')], 'g').map((r) => r.id)).toEqual(['b', 'a'])
  })

  it('groupId 缺失(undefined)按未归组处理', () => {
    expect(matchesGroup(undefined, GROUP_NONE)).toBe(true)
    expect(matchesGroup(undefined, 'g-pay')).toBe(false)
  })

  it('组不可见(被删 / 无权)的行不算未归组,只在「全部」里露出来', () => {
    // 后端只回 groupId,组名名单里没有它 —— 静默当成未归组会骗人,所以未归组那档不匹配。
    expect(matchesGroup('g-gone', GROUP_NONE)).toBe(false)
    expect(matchesGroup('g-gone', 'g-pay')).toBe(false)
    // 下拉里也不会出现这个组;手搓 URL 参数按精确匹配处理,不额外兜底。
    expect(filterByGroup(rows, 'g-gone').map((r) => r.id)).toEqual(['s-4'])
  })
})

describe('groupLabel', () => {
  const groups = [group('g-pay', '支付')]

  it('未归组 / 空串都照实说未归组', () => {
    expect(groupLabel('', groups, LABELS)).toBe('未归组')
    expect(groupLabel(undefined, groups, LABELS)).toBe('未归组')
  })

  it('组认得就报组名,认不得说「分组已失效」而不是静默归到未归组', () => {
    expect(groupLabel('g-pay', groups, LABELS)).toBe('支付')
    expect(groupLabel('g-gone', groups, LABELS)).toBe('分组已失效')
  })
})

describe('多组归属(0087:服务器可属于多个分组)', () => {
  it('groupIdsOf 同时认新字段与旧单值字段,空数组/未归组都归到 []', () => {
    expect(groupIdsOf({ groupIds: ['g-a', 'g-b'] })).toEqual(['g-a', 'g-b'])
    expect(groupIdsOf({ groupId: 'g-a' })).toEqual(['g-a'])
    expect(groupIdsOf({ groupIds: [] })).toEqual([])
    expect(groupIdsOf({})).toEqual([])
  })

  it('筛选取并集:机器在任一选中组里就出现;未归组只留一个组都没有的行', () => {
    const multi = [
      { id: 'm-1', groupIds: ['g-pay', 'g-core'] },
      { id: 'm-2', groupIds: ['g-core'] },
      { id: 'm-3', groupIds: [] },
    ]
    expect(filterByGroup(multi, 'g-pay').map((r) => r.id)).toEqual(['m-1'])
    expect(filterByGroup(multi, 'g-core').map((r) => r.id)).toEqual(['m-1', 'm-2'])
    expect(filterByGroup(multi, GROUP_NONE).map((r) => r.id)).toEqual(['m-3'])
    expect(matchesGroup([], GROUP_NONE)).toBe(true)
    expect(matchesGroup(['g-a'], GROUP_NONE)).toBe(false)
    expect(matchesGroup(['g-a', 'g-b'], 'g-b')).toBe(true)
  })

  it('groupLabel 多组按「、」串;groupNames 逐项给标签文案', () => {
    const groups = [group('g-pay', '支付'), group('g-core', '核心')]
    expect(groupLabel(['g-pay', 'g-core'], groups, LABELS)).toBe('支付、核心')
    expect(groupLabel(['g-pay', 'g-gone'], groups, LABELS)).toBe('支付、分组已失效')
    expect(groupNames(['g-core', 'g-pay'], groups, LABELS)).toEqual(['核心', '支付'])
    expect(groupNames([], groups, LABELS)).toEqual(['未归组'])
  })

  it('groupNames 对未归组给单一项,与标签渲染同一口径', () => {
    expect(groupNames(undefined, [], LABELS)).toEqual(['未归组'])
  })
})
