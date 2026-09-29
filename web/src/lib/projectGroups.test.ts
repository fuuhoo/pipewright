import { describe, expect, it } from 'vitest'

import type { Group } from '../api/groups'
import type { Project } from '../api/projects'
import { GROUP_ALL, GROUP_NONE } from './groupFilter'
import { groupProjects } from './projectGroups'

function project(id: string, groupId = '', over: Partial<Project> = {}): Project {
  return {
    id,
    name: id,
    repoUrl: '',
    defaultBranch: '',
    credentialId: '',
    credentialName: '',
    pacEnabled: false,
    prStatusEnabled: false,
    groupId,
    lastRunStatus: null,
    targetServers: [],
    updatedAt: '',
    ...over,
  } as Project
}

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
    canManage: false,
    createdAt: '',
    updatedAt: '',
    ...over,
  } as Group
}

const pay = group('g-pay', '支付平台')
const infra = group('g-infra', '基础设施', { visibility: 'private', canManage: true })

describe('groupProjects', () => {
  it('每个可见分组都成一段,组内保持传入顺序', () => {
    const sections = groupProjects(
      [project('p-2', 'g-pay'), project('p-1'), project('p-3', 'g-pay')],
      [pay, infra],
    )
    expect(sections.map((s) => s.key)).toEqual(['g-pay', 'g-infra', ''])
    expect(sections[0].projects.map((p) => p.id)).toEqual(['p-2', 'p-3'])
    expect(sections[0]).toMatchObject({ name: '支付平台', visibility: 'public', ownerName: 'fu', canManage: false, ungrouped: false })
  })

  it('未归组那段永远在最后,且标出来给调用方补展示名', () => {
    const sections = groupProjects([project('p-1'), project('p-2', 'g-infra')], [pay, infra])
    const last = sections[sections.length - 1]
    expect(last).toMatchObject({ key: '', name: '', visibility: null, ungrouped: true })
    expect(last.projects.map((p) => p.id)).toEqual(['p-1'])
  })

  it('空分组也要露出来:一个项目都没有的组照样成段,这正是它需要被管理的时候', () => {
    const sections = groupProjects([project('p-1', 'g-pay')], [pay, infra])
    expect(sections.find((s) => s.key === 'g-infra')?.projects).toEqual([])
  })

  it('带搜索/状态条件时空段不占位', () => {
    const sections = groupProjects([project('p-1', 'g-pay')], [pay, infra], { hideEmptyGroups: true })
    expect(sections.map((s) => s.key)).toEqual(['g-pay'])
  })

  it('分组筛选收到某个组时只留那一段', () => {
    const sections = groupProjects([project('p-1', 'g-pay'), project('p-2')], [pay, infra], { groupFilter: 'g-pay' })
    expect(sections.map((s) => s.key)).toEqual(['g-pay'])
  })

  it('筛选「未归组」时只看未归组那段,哨兵不会被当成真实 groupId', () => {
    const sections = groupProjects([project('p-1', 'g-pay'), project('p-2')], [pay, infra], { groupFilter: GROUP_NONE })
    expect(sections.map((s) => s.key)).toEqual([''])
  })

  it('组不在可见列表里的项目不静默丢掉,归进未归组那段兜住', () => {
    const sections = groupProjects([project('p-1', 'g-gone')], [pay], { groupFilter: GROUP_ALL })
    const ungrouped = sections.find((s) => s.ungrouped)
    expect(ungrouped?.projects.map((p) => p.id)).toEqual(['p-1'])
  })
})
