import { describe, expect, it } from 'vitest'
import { EMPTY_PROJECT_PREFS, hasCustomOrder, moveProjectId, rankProjects } from './projectViewOrder'

const projects = [{ id: 'p1' }, { id: 'p2' }, { id: 'p3' }, { id: 'p4' }]

describe('rankProjects', () => {
  it('没有偏好时原样返回', () => {
    expect(rankProjects(projects, EMPTY_PROJECT_PREFS).map((p) => p.id)).toEqual(['p1', 'p2', 'p3', 'p4'])
  })

  it('按保存的顺序排,没排过的保持入参顺序置顶', () => {
    const out = rankProjects(projects, { order: ['p3', 'p1'] })
    expect(out.map((p) => p.id)).toEqual(['p2', 'p4', 'p3', 'p1'])
  })

  it('新复制出来的项目即使已有自定义顺序也排最前', () => {
    const out = rankProjects([{ id: 'copy' }, ...projects], { order: ['p3', 'p1', 'p2', 'p4'] })
    expect(out.map((p) => p.id)).toEqual(['copy', 'p3', 'p1', 'p2', 'p4'])
  })

  it('认不出的 id 被忽略,不会顶掉真项目', () => {
    const out = rankProjects(projects, { order: ['gone', 'p4'] })
    expect(out.map((p) => p.id)).toEqual(['p1', 'p2', 'p3', 'p4'])
  })
})

describe('moveProjectId', () => {
  const ids = ['p1', 'p2', 'p3', 'p4']

  it('拖到目标上方 → 排在它前面', () => {
    expect(moveProjectId(ids, 'p4', 'p2', 'before')).toEqual(['p1', 'p4', 'p2', 'p3'])
  })

  it('拖到目标下方 → 排在它后面', () => {
    expect(moveProjectId(ids, 'p1', 'p3', 'after')).toEqual(['p2', 'p3', 'p1', 'p4'])
  })

  it('自己拖到自己 / 名单里没有的 id → 原样返回', () => {
    expect(moveProjectId(ids, 'p2', 'p2', 'before')).toEqual(ids)
    expect(moveProjectId(ids, 'gone', 'p2', 'before')).toEqual(ids)
    expect(moveProjectId(ids, 'p1', 'gone', 'before')).toEqual(ids)
  })
})

it('hasCustomOrder 只看 order 是否非空', () => {
  expect(hasCustomOrder(EMPTY_PROJECT_PREFS)).toBe(false)
  expect(hasCustomOrder({ order: ['p1'] })).toBe(true)
})
