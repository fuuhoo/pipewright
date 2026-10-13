/**
 * buildEnvSort 纯逻辑回归:排序键/方向的切换、默认排序不被改写、localStorage 偏好回读。
 */
import { describe, it, expect, beforeEach } from 'vitest'
import {
  DEFAULT_SORT,
  dirEnabled,
  loadSort,
  nextSort,
  saveSort,
  sortBuildEnvs,
  type BuildEnvSort,
} from './buildEnvSort'
import type { BuildEnv } from '../api/buildEnvs'

function env(over: Partial<BuildEnv>): BuildEnv {
  return {
    id: over.id ?? 'x',
    language: over.language ?? 'node',
    version: over.version ?? '20',
    displayName: over.displayName ?? 'Node 20',
    description: '',
    sourceType: 'official',
    image: 'node:20-alpine',
    credentialId: '',
    imageCheckStatus: 'available',
    imageCheckError: '',
    imageCheckedAt: null,
    enabled: true,
    sortOrder: 0,
    createdBy: 'admin',
    createdAt: '2026-01-01T00:00:00Z',
    updatedAt: '2026-01-01T00:00:00Z',
    ...over,
  }
}

const rows = [
  env({ id: 'a', displayName: 'Node 10', version: '10', sortOrder: 3, createdAt: '2026-03-01T00:00:00Z' }),
  env({ id: 'b', displayName: 'Alpine 最新', sortOrder: 1, createdAt: '2026-05-01T00:00:00Z' }),
  env({ id: 'c', displayName: 'Node 2', version: '2', sortOrder: 2, createdAt: 'bad-timestamp' }),
]

const ids = (list: BuildEnv[]): string[] => list.map((e) => e.id)
const at = (sort: Partial<BuildEnvSort>): BuildEnvSort => ({ ...DEFAULT_SORT, ...sort })

describe('sortBuildEnvs', () => {
  it('order 档原样保留后端顺序(方向也不翻转),且不改动入参', () => {
    const input = [rows[1], rows[2], rows[0]]
    expect(ids(sortBuildEnvs(input, at({ key: 'order' })))).toEqual(['b', 'c', 'a'])
    expect(ids(sortBuildEnvs(input, at({ key: 'order', dir: 'desc' })))).toEqual(['b', 'c', 'a'])
    expect(ids(input)).toEqual(['b', 'c', 'a'])
  })

  it('name 档走自然序:Node 2 在 Node 10 之前', () => {
    expect(ids(sortBuildEnvs(rows, at({ key: 'name' })))).toEqual(['b', 'c', 'a'])
    expect(ids(sortBuildEnvs(rows, at({ key: 'name', dir: 'desc' })))).toEqual(['a', 'c', 'b'])
  })

  it('created 档按时间,解析不出的时间戳当最早(升序沉首、降序沉尾)', () => {
    expect(ids(sortBuildEnvs(rows, at({ key: 'created' })))).toEqual(['c', 'a', 'b'])
    expect(ids(sortBuildEnvs(rows, at({ key: 'created', dir: 'desc' })))).toEqual(['b', 'a', 'c'])
  })

  it('时间戳相同的行按 sort_order 收口,行序不随渲染抖动', () => {
    const same = [
      env({ id: 'x', sortOrder: 9, createdAt: '2026-04-01T00:00:00Z' }),
      env({ id: 'y', sortOrder: 2, createdAt: '2026-04-01T00:00:00Z' }),
    ]
    expect(ids(sortBuildEnvs(same, at({ key: 'created' })))).toEqual(['y', 'x'])
  })
})

describe('dirEnabled / nextSort', () => {
  it('order 档不给方向按钮', () => {
    expect(dirEnabled(at({ key: 'order' }))).toBe(false)
    expect(dirEnabled(at({ key: 'name' }))).toBe(true)
  })

  it('点别的键切到该键的自然方向:名称升序、创建时间看最新', () => {
    expect(nextSort(at({ key: 'order' }), 'name')).toEqual({ key: 'name', dir: 'asc' })
    expect(nextSort(at({ key: 'order' }), 'created')).toEqual({ key: 'created', dir: 'desc' })
  })

  it('再点同一个键翻方向;点 order 一律收回权威顺序', () => {
    expect(nextSort(at({ key: 'name', dir: 'asc' }), 'name')).toEqual({ key: 'name', dir: 'desc' })
    expect(nextSort(at({ key: 'created', dir: 'desc' }), 'order')).toEqual({ key: 'order', dir: 'asc' })
  })
})

describe('loadSort / saveSort', () => {
  beforeEach(() => localStorage.clear())

  it('存进去能原样读回来', () => {
    saveSort({ key: 'created', dir: 'asc' })
    expect(loadSort()).toEqual({ key: 'created', dir: 'asc' })
  })

  it('没存过或存了不认识的值都退回默认', () => {
    expect(loadSort()).toEqual(DEFAULT_SORT)
    localStorage.setItem('pipewright.buildEnvs.sort', 'size|asc')
    expect(loadSort()).toEqual(DEFAULT_SORT)
    localStorage.setItem('pipewright.buildEnvs.sort', 'name|sideways')
    expect(loadSort()).toEqual(DEFAULT_SORT)
  })
})
