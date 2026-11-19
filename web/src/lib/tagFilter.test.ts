import { describe, expect, it } from 'vitest'
import { allTags, matchesTagFilter, mergeTags } from './tagFilter'

describe('allTags', () => {
  it('跨项目收集、去重不分大小写、保留先出现的写法', () => {
    const tags = allTags([
      { tags: ['Prod', '支付'] },
      { tags: ['prod', ' 生产 ', ''] },
      { tags: undefined },
    ])
    expect(tags).toContain('Prod')
    expect(tags).toContain('支付')
    expect(tags).toContain('生产')
    expect(tags.filter((t) => t.toLowerCase() === 'prod')).toHaveLength(1)
  })

  it('按字母序排序,与项目添加顺序无关', () => {
    expect(allTags([{ tags: ['zeta'] }, { tags: ['alpha'] }])).toEqual(['alpha', 'zeta'])
  })
})

describe('matchesTagFilter', () => {
  it('未选标签时全部放行', () => {
    expect(matchesTagFilter(['x'], [])).toBe(true)
    expect(matchesTagFilter(undefined, [])).toBe(true)
  })

  it('多选按「命中任一」放行,且不区分大小写', () => {
    expect(matchesTagFilter(['Prod', '支付'], ['prod'])).toBe(true)
    expect(matchesTagFilter(['支付'], ['生产', '支付'])).toBe(true)
    expect(matchesTagFilter(['生产'], ['生产', '支付'])).toBe(true)
    expect(matchesTagFilter(['其他'], ['生产', '支付'])).toBe(false)
  })

  it('精确匹配标签文案,不做前缀/子串命中', () => {
    expect(matchesTagFilter(['Prod'], ['prod-2'])).toBe(false)
    expect(matchesTagFilter([], ['prod'])).toBe(false)
    expect(matchesTagFilter(undefined, ['prod'])).toBe(false)
  })
})

describe('mergeTags', () => {
  it('按逗号/中文逗号/换行拆开,去空白,保序去重', () => {
    expect(mergeTags(['prod'], ' 支付 ，生产,prod\n服务 ')).toEqual(['prod', '支付', '生产', '服务'])
  })

  it('只有分隔符与空白时原样返回', () => {
    expect(mergeTags(['a'], ' , ， \n ')).toEqual(['a'])
  })
})