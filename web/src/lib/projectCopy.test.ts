import { describe, expect, it } from 'vitest'

import type { CopiedProject, Project } from '../api/projects'
import type { Group } from '../api/groups'
import {
  COPY_DEFAULT_SUFFIX,
  GROUP_KEEP,
  buildCopyBody,
  buildCopyManyBody,
  copyErrorMessage,
  copyRows,
  emptyCopyForm,
  namingFields,
  previewCopyName,
  pruneSelected,
  selectAllIds,
  summarizeCopies,
  targetGroupIdOf,
  targetGroupOptions,
  toggleSelected,
  uniqueProjectIds,
} from './projectCopy'
import { GROUP_NONE } from './groupFilter'

function project(id: string, name: string, groupId = ''): Project {
  return {
    id,
    name,
    groupId,
    repoUrl: '',
    defaultBranch: '',
    credentialId: '',
    credentialName: '',
    pacEnabled: false,
    prStatusEnabled: false,
    lastRunStatus: null,
    targetServers: [],
    createdAt: '',
    updatedAt: '',
  } as unknown as Project
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
    variableGroupCount: 0,
    canManage: false,
    createdAt: '',
    updatedAt: '',
    ...over,
  } as unknown as Group
}

describe('previewCopyName', () => {
  it('后缀原样拼接,不额外补分隔符', () => {
    expect(previewCopyName('网关', emptyCopyForm())).toBe('网关 副本')
    expect(previewCopyName('网关', { ...emptyCopyForm(), suffix: '-staging' })).toBe('网关-staging')
  })

  it('后缀清空退回默认;尾部空白按服务端同一步削掉', () => {
    expect(previewCopyName('网关', { ...emptyCopyForm(), suffix: '' })).toBe('网关 副本')
    expect(previewCopyName('网关', { ...emptyCopyForm(), suffix: ' 备份  ' })).toBe('网关 备份')
  })

  it('显式名只在单份时生效,纯空白不算名字', () => {
    const form = { ...emptyCopyForm(), name: '网关-灰度' }
    expect(previewCopyName('网关', form, true)).toBe('网关-灰度')
    expect(previewCopyName('网关', form, false)).toBe('网关 副本')
    expect(previewCopyName('网关', { ...form, name: '   ' }, true)).toBe('网关 副本')
  })
})

describe('targetGroupIdOf', () => {
  it('沿用=不发字段,未归组=空串,组=原样', () => {
    expect(targetGroupIdOf(GROUP_KEEP)).toBeUndefined()
    expect(targetGroupIdOf(GROUP_NONE)).toBe('')
    expect(targetGroupIdOf('g-1')).toBe('g-1')
  })
})

describe('namingFields', () => {
  it('单份且填了名字只发 name', () => {
    expect(namingFields(true, { ...emptyCopyForm(), name: '甲' })).toEqual({ name: '甲' })
  })

  it('批量只发 suffix:一个名字建出 N 份同名项目是事故,不是命名', () => {
    expect(namingFields(false, { ...emptyCopyForm(), name: '甲' })).toEqual({ suffix: COPY_DEFAULT_SUFFIX })
  })

  it('单份但名字留空走 suffix(空串照发,后端自己补默认)', () => {
    expect(namingFields(true, { ...emptyCopyForm(), name: '', suffix: '' })).toEqual({ suffix: '' })
  })
})

describe('buildCopyBody / buildCopyManyBody', () => {
  it('选「沿用源项目分组」时请求体里根本没有 targetGroupId 这个键', () => {
    const body = buildCopyBody(emptyCopyForm())
    expect(Object.prototype.hasOwnProperty.call(body, 'targetGroupId')).toBe(false)
    expect(JSON.parse(JSON.stringify(body))).toEqual({ suffix: COPY_DEFAULT_SUFFIX })
  })

  it('未归组要显式发空串,和「不发」是两件事', () => {
    const body = buildCopyBody({ ...emptyCopyForm(), target: GROUP_NONE })
    expect(JSON.parse(JSON.stringify(body))).toEqual({ suffix: COPY_DEFAULT_SUFFIX, targetGroupId: '' })
  })

  it('批量:ID 去重去空、保持提交顺序,且不因为填过名字就发 name', () => {
    const sources = [project('p-1', '甲'), project('', '空ID'), project('p-2', '乙'), project('p-1', '甲重复')]
    const body = buildCopyManyBody(sources, { ...emptyCopyForm(), name: '统一名', target: 'g-9' })
    expect(JSON.parse(JSON.stringify(body))).toEqual({
      projectIds: ['p-1', 'p-2'],
      suffix: COPY_DEFAULT_SUFFIX,
      targetGroupId: 'g-9',
    })
  })
})

describe('uniqueProjectIds', () => {
  it('去重、去空、保持顺序', () => {
    const sources = [project('b', 'B'), project('a', 'A'), project('b', 'B重复'), project('', '空')]
    expect(uniqueProjectIds(sources)).toEqual(['b', 'a'])
  })
})

describe('copyRows', () => {
  it('「沿用」时每行落在自己源项目的组;强制目标时整批同一个组', () => {
    const sources = [project('p-1', '甲', 'g-a'), project('p-2', '乙', 'g-b')]

    const inherited = copyRows(sources, emptyCopyForm())
    expect(inherited.map((r) => r.targetGroupId)).toEqual(['g-a', 'g-b'])
    expect(inherited.map((r) => r.copyName)).toEqual(['甲 副本', '乙 副本'])
    expect(inherited[0].sourceName).toBe('甲')

    const forced = copyRows(sources, { ...emptyCopyForm(), target: GROUP_NONE })
    expect(forced.map((r) => r.targetGroupId)).toEqual(['', ''])
  })

  it('单份 + 显式名时预览就是那个名字', () => {
    const rows = copyRows([project('p-1', '网关')], { ...emptyCopyForm(), name: '网关-灰度' })
    expect(rows[0].copyName).toBe('网关-灰度')
  })
})

describe('勾选集合', () => {
  it('toggleSelected 增和删都按 ID', () => {
    expect(toggleSelected(['a', 'b'], 'a')).toEqual(['b'])
    expect(toggleSelected(['a'], 'b')).toEqual(['a', 'b'])
  })

  it('selectAllIds:给候选全集或空数组', () => {
    const sources = [project('p-1', '甲'), project('p-2', '乙')]
    expect(selectAllIds(sources, true)).toEqual(['p-1', 'p-2'])
    expect(selectAllIds(sources, false)).toEqual([])
  })

  it('pruneSelected 丢掉已不在候选里的勾(弹窗开着时列表被刷新过)', () => {
    expect(pruneSelected(['p-1', 'p-2'], [project('p-1', '甲')])).toEqual(['p-1'])
  })
})

describe('targetGroupOptions', () => {
  const LABELS = { keep: '沿用源项目分组', ungrouped: '未归组', public: '公开', private: '私有' }

  it('沿用、未归组永远在前,后面只跟管得动的组', () => {
    const opts = targetGroupOptions([group('g-1', '交易组', { canManage: true }), group('g-2', '搜索组')], LABELS)
    expect(opts).toEqual([
      { value: GROUP_KEEP, label: '沿用源项目分组' },
      { value: GROUP_NONE, label: '未归组' },
      { value: 'g-1', label: '交易组 · 公开' },
    ])
  })

  it('私有组带可见性后缀:别把副本挪进一个别人看不见的组还以为是公开组', () => {
    const opts = targetGroupOptions([group('g-3', '财务组', { canManage: true, visibility: 'private' })], LABELS)
    expect(opts[2].label).toBe('财务组 · 私有')
  })
})

describe('summarizeCopies', () => {
  it('回执报份数 + 名字:一次点击出 N 份,只说「已复制」会让人以为只有一份', () => {
    const created = [
      { id: 'c-1', name: '网关 副本', groupId: '', sourceId: 'p-1' },
      { id: 'c-2', name: '搜索 副本', groupId: '', sourceId: 'p-2' },
    ] as CopiedProject[]
    expect(summarizeCopies(created)).toEqual({ count: 2, names: ['网关 副本', '搜索 副本'] })
  })
})

describe('copyErrorMessage', () => {
  const LABELS = {
    forbidden: '没有这个项目的操作权限',
    sourceGone: '源项目已经不存在了',
    tooMany: '同名副本太多,请手动指定名称',
    groupGone: '要落进的分组不存在',
    network: '连不上服务器,请重试',
  }

  it('断网(status=0)优先于契约码', () => {
    expect(copyErrorMessage('internal', 0, '', LABELS)).toBe(LABELS.network)
  })

  it('三种 403 码归成一句用户能懂的话', () => {
    for (const code of ['forbidden', 'role_denied', 'group_missing']) {
      expect(copyErrorMessage(code, 403, '原始消息', LABELS)).toBe(LABELS.forbidden)
    }
  })

  it('源没了 / 目标组没了 / 副本太多各自说清', () => {
    expect(copyErrorMessage('project_not_found', 404, 'x', LABELS)).toBe(LABELS.sourceGone)
    expect(copyErrorMessage('not_found', 404, 'x', LABELS)).toBe(LABELS.sourceGone)
    expect(copyErrorMessage('too_many_copies', 409, 'x', LABELS)).toBe(LABELS.tooMany)
    expect(copyErrorMessage('group_not_found', 422, 'x', LABELS)).toBe(LABELS.groupGone)
  })

  it('认不出的码回落到服务端消息,不编造原因', () => {
    expect(copyErrorMessage('internal', 503, '复制服务未初始化', LABELS)).toBe('复制服务未初始化')
    expect(copyErrorMessage(null, 500, '服务器内部错误', LABELS)).toBe('服务器内部错误')
  })
})
