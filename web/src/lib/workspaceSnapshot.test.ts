import { describe, expect, it } from 'vitest'

import type { DeclaredPathDTO, SnapshotEntryDTO } from '../api/runs'
import {
  RENDER_LIMIT,
  allDeclaredHit,
  capEntries,
  declaredStatusKey,
  filterEntries,
  isDirEntry,
  missedDeclared,
  reasonKey,
  retentionWindow,
  snapshotTitle,
} from './workspaceSnapshot'

function entry(path: string, over: Partial<SnapshotEntryDTO> = {}): SnapshotEntryDTO {
  return { path, size: 0, dropped: 0, ...over }
}

function declared(path: string, status: DeclaredPathDTO['status'], over: Partial<DeclaredPathDTO> = {}): DeclaredPathDTO {
  return { path, status, ...over }
}

function entries(n: number): SnapshotEntryDTO[] {
  return Array.from({ length: n }, (_, i) => entry(`f${i}.txt`))
}

describe('isDirEntry', () => {
  it('尾斜杠即目录(采集器就是这么给的)', () => {
    expect(isDirEntry(entry('target/'))).toBe(true)
    expect(isDirEntry(entry('target/app.jar', { size: 1024 }))).toBe(false)
  })
})

describe('filterEntries', () => {
  const list = [entry('dist/index.html'), entry('Target/app.jar'), entry('src/main.go')]

  it('空查询(含纯空格)原样返回', () => {
    expect(filterEntries(list, '')).toBe(list)
    expect(filterEntries(list, '   ')).toBe(list)
  })

  it('大小写不敏感的子串匹配整条相对路径', () => {
    expect(filterEntries(list, 'TARGET').map((e) => e.path)).toEqual(['Target/app.jar'])
    expect(filterEntries(list, '.html').map((e) => e.path)).toEqual(['dist/index.html'])
    expect(filterEntries(list, 'app')).toHaveLength(1)
  })

  it('搜不到就返回空数组(不是 undefined)', () => {
    expect(filterEntries(list, 'nope')).toEqual([])
  })
})

describe('capEntries', () => {
  it('未超上限:全部展示、hidden=0', () => {
    const list = entries(3)
    expect(capEntries(list)).toEqual({ rows: list, hidden: 0, limit: RENDER_LIMIT })
  })

  it('正好等于上限:不算截断', () => {
    const list = entries(RENDER_LIMIT)
    expect(capEntries(list).hidden).toBe(0)
  })

  it('超上限:只取前 N 条,并如实报未展示的行数', () => {
    const list = entries(RENDER_LIMIT + 7)
    const v = capEntries(list)
    expect(v.rows).toHaveLength(RENDER_LIMIT)
    expect(v.hidden).toBe(7)
  })

  it('可传入更小上限(测试/窄面板用)', () => {
    const v = capEntries(entries(10), 4)
    expect(v.rows.map((e) => e.path)).toEqual(['f0.txt', 'f1.txt', 'f2.txt', 'f3.txt'])
    expect(v.hidden).toBe(6)
    expect(v.limit).toBe(4)
  })
})

describe('missedDeclared / allDeclaredHit', () => {
  const list = [declared('dist', 'ok'), declared('build', 'missing'), declared('*.jar', 'unmatched')]

  it('未命中的三条都算(含 rejected),按原顺序', () => {
    expect(missedDeclared(list).map((d) => d.path)).toEqual(['build', '*.jar'])
  })

  it('全命中 → allDeclaredHit 为真;有未命中 → 假', () => {
    expect(allDeclaredHit([declared('dist', 'ok')])).toBe(true)
    expect(allDeclaredHit(list)).toBe(false)
  })

  it('空声明列表视为「没有未命中」(此时采集只因节点失败)', () => {
    expect(missedDeclared([])).toEqual([])
    expect(allDeclaredHit([])).toBe(true)
  })
})

describe('declaredStatusKey', () => {
  it('四种状态各有文案键', () => {
    expect(declaredStatusKey('ok')).toBe('snapStatusOk')
    expect(declaredStatusKey('missing')).toBe('snapStatusMissing')
    expect(declaredStatusKey('unmatched')).toBe('snapStatusUnmatched')
    expect(declaredStatusKey('rejected')).toBe('snapStatusRejected')
  })

  it('后端加了未知状态时说「未知」,不假装命中', () => {
    expect(declaredStatusKey('future_state' as DeclaredPathDTO['status'])).toBe('snapStatusUnknown')
  })
})

describe('reasonKey', () => {
  it('产物未命中与失败分别对应各自徽标文案', () => {
    expect(reasonKey('artifact_miss')).toBe('snapReasonMiss')
    expect(reasonKey('failed')).toBe('snapReasonFailed')
  })
})

describe('retentionWindow', () => {
  it('整小时给小时', () => {
    expect(retentionWindow(7200)).toEqual({ count: 2, unitKey: 'snapUnitHours' })
  })

  it('非整小时但整分钟给分钟(PIPEWRIGHT_SNAPSHOT_TTL 可配成 30m)', () => {
    expect(retentionWindow(1800)).toEqual({ count: 30, unitKey: 'snapUnitMinutes' })
    expect(retentionWindow(5400)).toEqual({ count: 90, unitKey: 'snapUnitMinutes' })
  })

  it('整秒以外的兜底按秒说,不虚报成分钟', () => {
    expect(retentionWindow(90)).toEqual({ count: 90, unitKey: 'snapUnitSeconds' })
  })

  it('窗口缺失(0 / 负数 / 非有限值)→ count 0,调用方据此省略这句', () => {
    expect(retentionWindow(0).count).toBe(0)
    expect(retentionWindow(-5).count).toBe(0)
    expect(retentionWindow(Number.NaN).count).toBe(0)
  })
})

describe('snapshotTitle', () => {
  it('阶段与节点齐全时用「阶段 · 节点」', () => {
    expect(snapshotTitle('构建', '后端构建')).toBe('构建 · 后端构建')
  })

  it('缺一个就只说另一个,不留孤立分隔符', () => {
    expect(snapshotTitle('构建', '')).toBe('构建')
    expect(snapshotTitle('', '后端构建')).toBe('后端构建')
    expect(snapshotTitle('', '')).toBe('')
  })

  it('两侧空白不参与判断', () => {
    expect(snapshotTitle(' 构建 ', '  ')).toBe('构建')
  })
})
