import { describe, expect, it } from 'vitest'

import { projectFilterLabel, resolveProjectFilter, withProjectFilter } from './runProjectFilter'

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
