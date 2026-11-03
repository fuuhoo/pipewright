import { describe, expect, it } from 'vitest'

import { EMPTY_PREFS, hasCustomOrder, rankHosts, togglePinned, moveHostId } from './serverViewOrder'

function host(serverId: string) {
  return { serverId }
}
function ids(list: { serverId: string }[]): string[] {
  return list.map((h) => h.serverId)
}

describe('hasCustomOrder', () => {
  it('没设置过偏好时是 false(排序完全让开)', () => {
    expect(hasCustomOrder(EMPTY_PREFS)).toBe(false)
  })

  it('只置顶、只拖过都算动过手', () => {
    expect(hasCustomOrder({ pinned: ['a'], order: [] })).toBe(true)
    expect(hasCustomOrder({ pinned: [], order: ['a'] })).toBe(true)
  })
})

describe('rankHosts', () => {
  const hosts = [host('a'), host('b'), host('c'), host('d')]

  it('默认偏好原样返回入参顺序(在线在前的口径不被打断)', () => {
    expect(ids(rankHosts(hosts, EMPTY_PREFS))).toEqual(['a', 'b', 'c', 'd'])
  })

  it('置顶档在前,档内按置顶数组顺序,其余保持入参顺序', () => {
    expect(ids(rankHosts(hosts, { pinned: ['c', 'a'], order: [] }))).toEqual(['c', 'a', 'b', 'd'])
  })

  it('手动顺序决定档内前后,且离线机器不会因为刷新爬回上面', () => {
    expect(ids(rankHosts(hosts, { pinned: [], order: ['d', 'c', 'b', 'a'] }))).toEqual(['d', 'c', 'b', 'a'])
  })

  it('新登记的机器(order 里查不到)沉到所在档位末尾', () => {
    // 只有 a/b 被排过序,c 是这轮才采出来的新机器。
    expect(ids(rankHosts(hosts, { pinned: [], order: ['b', 'a'] }))).toEqual(['b', 'a', 'c', 'd'])
  })

  it('置顶优先于手动顺序:没排过序的置顶机器也排在排过序的普通机器前面', () => {
    expect(ids(rankHosts(hosts, { pinned: ['d'], order: ['a', 'b', 'c'] }))).toEqual(['d', 'a', 'b', 'c'])
  })

  it('档内 order 说了算(置顶数组只排它自己的前后)', () => {
    expect(ids(rankHosts(hosts, { pinned: ['c', 'a'], order: ['a', 'c', 'd', 'b'] }))).toEqual(['a', 'c', 'd', 'b'])
  })

  it('order 里的残留 id(机器已删)不影响排序', () => {
    expect(ids(rankHosts(hosts, { pinned: [], order: ['zz', 'c', 'a'] }))).toEqual(['c', 'a', 'b', 'd'])
  })
})

describe('moveHostId', () => {
  const all = ['a', 'b', 'c', 'd']

  it('拖到目标上半 = 排在目标前面(往下拖不会因为摘走一张而错位)', () => {
    expect(moveHostId(all, 'a', 'c', 'before')).toEqual(['b', 'a', 'c', 'd'])
  })

  it('拖到目标下半 = 排在目标后面', () => {
    expect(moveHostId(all, 'a', 'c', 'after')).toEqual(['b', 'c', 'a', 'd'])
  })

  it('从下往上拖同样自洽', () => {
    expect(moveHostId(all, 'd', 'a', 'before')).toEqual(['d', 'a', 'b', 'c'])
    expect(moveHostId(all, 'd', 'b', 'after')).toEqual(['a', 'b', 'd', 'c'])
  })

  it('相邻互换得到预期结果', () => {
    expect(moveHostId(all, 'a', 'b', 'after')).toEqual(['b', 'a', 'c', 'd'])
    expect(moveHostId(all, 'b', 'a', 'before')).toEqual(['b', 'a', 'c', 'd'])
  })

  it('拖到自己身上不改顺序', () => {
    expect(moveHostId(all, 'b', 'b', 'after')).toEqual(all)
  })

  it('源或落点不在名单里(数据正好在换)原样返回', () => {
    expect(moveHostId(all, 'x', 'b', 'after')).toEqual(all)
    expect(moveHostId(all, 'a', 'y', 'before')).toEqual(all)
  })

  it('结果覆盖全集:搜索筛着时拖动也只动被拖那一张的落点', () => {
    // 屏上只有 a/d 两张卡(中间的被筛掉),把 d 拖到 a 前面 → b/c 仍留在原位。
    expect(moveHostId(['a', 'b', 'c', 'd'], 'd', 'a', 'before')).toEqual(['d', 'a', 'b', 'c'])
  })
})

describe('togglePinned', () => {
  it('新置顶的排到最前', () => {
    expect(togglePinned(['a'], 'c')).toEqual(['c', 'a'])
  })

  it('再点一次取消置顶,别的置顶顺序不动', () => {
    expect(togglePinned(['c', 'a'], 'c')).toEqual(['a'])
    expect(togglePinned(['c', 'a'], 'a')).toEqual(['c'])
  })
})
