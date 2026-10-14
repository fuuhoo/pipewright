/**
 * 缓存卡纯逻辑的回归:重点是「存在 ≠ 有东西可清」这条反直觉的口径,以及清除结果的对账。
 */
import { describe, it, expect } from 'vitest'
import { hasAnythingToClear, summarizeClear } from './projectCache'
import type { ClearedCacheResult, ProjectCacheInfo } from '../api/projects'

const info = (items: ProjectCacheInfo['items'], totalBytes = 0): ProjectCacheInfo => ({
  items,
  totalBytes: totalBytes || items.reduce((acc, it) => acc + it.bytes, 0),
})

describe('hasAnythingToClear:按字节而不是按存在判定', () => {
  it('两类都空(没构建过)→ 没有东西可清', () => {
    expect(hasAnythingToClear(null)).toBe(false)
    expect(hasAnythingToClear(info([]))).toBe(false)
    expect(
      hasAnythingToClear(info([
        { kind: 'repo', path: '/data/repos/a.git', bytes: 0, exists: false },
        { kind: 'deps', path: '/data/project-cache/p', bytes: 0, exists: false },
      ])),
    ).toBe(false)
  })

  it('目录存在但 0 字节 → 仍算没东西可清(清完再构建就是这个状态)', () => {
    expect(
      hasAnythingToClear(info([
        { kind: 'repo', path: '/data/repos/a.git', bytes: 0, exists: true },
        { kind: 'deps', path: '/data/project-cache/p', bytes: 0, exists: true },
      ])),
    ).toBe(false)
  })

  it('任一类真占着磁盘 → 可清', () => {
    expect(
      hasAnythingToClear(info([
        { kind: 'repo', path: '/data/repos/a.git', bytes: 0, exists: true },
        { kind: 'deps', path: '/data/project-cache/p', bytes: 1, exists: true },
      ])),
    ).toBe(true)
  })
})

describe('summarizeClear:说清删了什么、哪些本来就是空的', () => {
  it('两类都真删了 → 汇总释放字节', () => {
    const res: ClearedCacheResult = {
      items: [
        { kind: 'repo', path: '/data/repos/a.git', bytes: 1024, existed: true },
        { kind: 'deps', path: '/p', bytes: 4096, existed: true },
      ],
      totalBytes: 5120,
    }
    expect(summarizeClear(res)).toEqual({
      freedBytes: 5120,
      removedKinds: ['repo', 'deps'],
      emptyKinds: [],
    })
  })

  it('existed=false(原本空着)归到空的那堆,不算删除量', () => {
    const res: ClearedCacheResult = {
      items: [
        { kind: 'repo', path: '/data/repos/a.git', bytes: 0, existed: false },
        { kind: 'deps', path: '/p', bytes: 8, existed: true },
      ],
      totalBytes: 8,
    }
    const s = summarizeClear(res)
    expect(s.removedKinds).toEqual(['deps'])
    expect(s.emptyKinds).toEqual(['repo'])
    expect(s.freedBytes).toBe(8)
  })

  it('一个都没删(全空)→ 两个清单都不该有东西被当成已清除', () => {
    const res: ClearedCacheResult = {
      items: [
        { kind: 'repo', path: '/data/repos/a.git', bytes: 0, existed: true },
        { kind: 'deps', path: '/p', bytes: 0, existed: true },
      ],
      totalBytes: 0,
    }
    expect(summarizeClear(res).removedKinds).toEqual([])
    expect(summarizeClear(res).emptyKinds).toEqual(['repo', 'deps'])
  })
})
