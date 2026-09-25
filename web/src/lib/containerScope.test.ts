import { describe, expect, it } from 'vitest'

import type { ServerContainers } from '../api/containers'
import { SERVER_ALL, resolveServerScope, scopedGroups, scopeTone, preferredFirst } from './containerScope'

function group(serverId: string, over: Partial<ServerContainers> = {}): ServerContainers {
  return {
    serverId,
    reachable: true,
    runtime: 'docker',
    error: '',
    containers: [],
    running: 0,
    total: 0,
    collectedAt: '',
    ...over,
  } as ServerContainers
}

describe('resolveServerScope', () => {
  it('没选机器时就是全部', () => {
    expect(resolveServerScope(SERVER_ALL, [group('a'), group('b')])).toBe(SERVER_ALL)
  })

  it('选中的机器还在聚合里则保持不变', () => {
    expect(resolveServerScope('b', [group('a'), group('b')])).toBe('b')
  })

  it('选中的机器消失(被删 / 失去可见性)时回落全部,不留悬空选择', () => {
    // 首屏聚合还没回来时 groups 是空数组,同样该判成「全部」而不是空白一屏。
    expect(resolveServerScope('b', [group('a')])).toBe(SERVER_ALL)
    expect(resolveServerScope('b', [])).toBe(SERVER_ALL)
  })
})

describe('scopedGroups', () => {
  const groups = [group('a'), group('b'), group('c')]

  it('全部 = 逐台都算,但返回新数组(调用方改它不碰聚合源)', () => {
    const out = scopedGroups(groups, SERVER_ALL)
    expect(out.map((g) => g.serverId)).toEqual(['a', 'b', 'c'])
    expect(out).not.toBe(groups)
  })

  it('只看一台时其余卡片都不进范围', () => {
    expect(scopedGroups(groups, 'b').map((g) => g.serverId)).toEqual(['b'])
  })

  it('范围里没有的机器 → 空范围(由 resolveServerScope 先拦,这里不静默变全部)', () => {
    expect(scopedGroups(groups, 'gone')).toEqual([])
  })
})

describe('scopeTone', () => {
  it('连不上、没运行时、可用三种状态点在切换上分得开', () => {
    expect(scopeTone(group('a', { reachable: false, runtime: '' }))).toBe('unreachable')
    expect(scopeTone(group('a', { runtime: '' }))).toBe('no-runtime')
    expect(scopeTone(group('a'))).toBe('ok')
  })
})

describe('preferredFirst', () => {
  const options = [{ id: 'a' }, { id: 'b' }, { id: 'c' }]

  it('聚焦那台排到最前,弹窗默认就落在正看着的机器上', () => {
    expect(preferredFirst(options, 'b').map((o) => o.id)).toEqual(['b', 'a', 'c'])
  })

  it('没切换时顺序不动', () => {
    const out = preferredFirst(options, SERVER_ALL)
    expect(out.map((o) => o.id)).toEqual(['a', 'b', 'c'])
    expect(out).not.toBe(options)
  })

  it('聚焦那台没装 docker(不在候选里)时不清空别的机器 —— 换个地方照样能建', () => {
    expect(preferredFirst(options, 'gone').map((o) => o.id)).toEqual(['a', 'b', 'c'])
    expect(preferredFirst([], 'a')).toEqual([])
  })
})
