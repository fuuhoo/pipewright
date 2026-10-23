import { describe, expect, it } from 'vitest'

import { GROUP_ALL, GROUP_NONE } from './groupFilter'
import {
  matchesProjectQuery,
  narrowProjectOptions,
  projectFilterLabel,
  resolveProjectFilter,
  withProjectFilter,
} from './runProjectFilter'

describe('resolveProjectFilter', () => {
  it('只认非空字符串', () => {
    expect(resolveProjectFilter('p-1')).toBe('p-1')
    expect(resolveProjectFilter('')).toBe('')
    expect(resolveProjectFilter(undefined)).toBe('')
  })

  it('同名重复(?projectId=a&projectId=b)与数字都不算筛选', () => {
    expect(resolveProjectFilter(['a', 'b'])).toBe('')
    expect(resolveProjectFilter(7)).toBe('')
    expect(resolveProjectFilter(null)).toBe('')
  })
})

describe('projectFilterLabel', () => {
  const projects = [{ id: 'p-1', name: '官网前端' }, { id: 'p-2', name: '中台服务' }]

  it('未筛时不给名字', () => {
    expect(projectFilterLabel(projects, '')).toBe('')
  })

  it('清单里有就用展示名', () => {
    expect(projectFilterLabel(projects, 'p-2')).toBe('中台服务')
  })

  it('清单里没有时回退成 id 前 8 位,而不是回退成「全部项目」', () => {
    expect(projectFilterLabel(projects, '0123456789abcdef')).toBe('01234567')
  })

  it('短 id 原样给出(截不出 8 位也不补省略号)', () => {
    expect(projectFilterLabel([], 'p-9')).toBe('p-9')
  })
})

describe('withProjectFilter', () => {
  it('带值时写入 projectId,其余参数原样留着', () => {
    expect(withProjectFilter({ tab: 'list' }, 'p-1')).toEqual({ tab: 'list', projectId: 'p-1' })
  })

  it('空值整个删掉,不留 projectId= 这种空参数', () => {
    expect(withProjectFilter({ projectId: 'p-1', tab: 'list' }, '')).toEqual({ tab: 'list' })
  })

  it('不改动传进来的对象', () => {
    const src = { projectId: 'p-1' }
    withProjectFilter(src, 'p-2')
    expect(src).toEqual({ projectId: 'p-1' })
  })
})

const OPTIONS = [
  { id: 'p-1', name: '官网前端', groupId: '', repoUrl: 'https://example.com/web.git', defaultBranch: 'master' },
  { id: 'p-2', name: '中台服务', groupId: 'g-a', repoUrl: 'git@example.com:mid.git', defaultBranch: 'release/2026' },
  { id: 'p-3', name: 'Report API', groupId: 'g-b', repoUrl: 'https://example.com/report', defaultBranch: 'main' },
]

describe('matchesProjectQuery', () => {
  it('空白查询一律放行', () => {
    expect(matchesProjectQuery(OPTIONS[0], '')).toBe(true)
    expect(matchesProjectQuery(OPTIONS[0], '   ')).toBe(true)
  })

  it('名称大小写不敏感', () => {
    expect(matchesProjectQuery(OPTIONS[2], 'report')).toBe(true)
    expect(matchesProjectQuery(OPTIONS[2], 'REPORT api')).toBe(true)
  })

  it('仓库地址与默认分支也参与(和项目的搜索框同一口径)', () => {
    expect(matchesProjectQuery(OPTIONS[1], 'mid.git')).toBe(true)
    expect(matchesProjectQuery(OPTIONS[1], 'release/2026')).toBe(true)
    expect(matchesProjectQuery(OPTIONS[1], 'nope')).toBe(false)
  })

  it('字段缺省时不炸(undefined 的 repoUrl / defaultBranch)', () => {
    const bare = { id: 'p-9', name: '只有名字' }
    expect(matchesProjectQuery(bare, '只有')).toBe(true)
    expect(matchesProjectQuery(bare, 'http')).toBe(false)
  })
})

describe('narrowProjectOptions', () => {
  const base = { query: '', groupFilter: GROUP_ALL, selectedId: '' }

  it('全部分组 + 无关键词 = 原样原顺序', () => {
    expect(narrowProjectOptions(OPTIONS, base)).toEqual(OPTIONS)
  })

  it('未归组只留空 groupId 的那一项,哨兵不能和空串混', () => {
    expect(narrowProjectOptions(OPTIONS, { ...base, groupFilter: GROUP_NONE }).map((p) => p.id)).toEqual(['p-1'])
  })

  it('按具体分组精确匹配', () => {
    expect(narrowProjectOptions(OPTIONS, { ...base, groupFilter: 'g-b' }).map((p) => p.id)).toEqual(['p-3'])
  })

  it('没带 groupId 的旧数据按未归组算', () => {
    const legacy = [{ id: 'p-8', name: '存量项目' }]
    expect(narrowProjectOptions(legacy, { ...base, groupFilter: GROUP_NONE })).toEqual(legacy)
  })

  it('关键词与分组是「且」:命中名字但不在组里就不出现', () => {
    expect(narrowProjectOptions(OPTIONS, { ...base, query: '服务', groupFilter: GROUP_NONE })).toEqual([])
    expect(narrowProjectOptions(OPTIONS, { ...base, query: '服务', groupFilter: 'g-a' }).map((p) => p.id)).toEqual(['p-2'])
  })

  it('不改动传进来的数组', () => {
    const src = [...OPTIONS]
    narrowProjectOptions(src, { ...base, groupFilter: 'g-a' })
    expect(src).toEqual(OPTIONS)
  })

  describe('已选项补回候选', () => {
    it('选了组、筛中的项目不在该组里时补到首位,控件不会显示着一个自己列表里找不到的值', () => {
      const got = narrowProjectOptions(OPTIONS, { ...base, groupFilter: 'g-a', selectedId: 'p-3' })
      expect(got.map((p) => p.id)).toEqual(['p-3', 'p-2'])
    })

    it('已经在候选里就不重复补', () => {
      const got = narrowProjectOptions(OPTIONS, { ...base, selectedId: 'p-1' })
      expect(got.map((p) => p.id)).toEqual(['p-1', 'p-2', 'p-3'])
    })

    it('用户输入了关键词就照关键词说话,不再兜已选项', () => {
      const got = narrowProjectOptions(OPTIONS, { ...base, query: 'report', selectedId: 'p-1' })
      expect(got.map((p) => p.id)).toEqual(['p-3'])
    })

    it('地址栏带来的野 id(清单里没有)不报错也不凭空冒一项', () => {
      const got = narrowProjectOptions(OPTIONS, { ...base, groupFilter: GROUP_NONE, selectedId: 'deadbeef' })
      expect(got.map((p) => p.id)).toEqual(['p-1'])
    })
  })
})
