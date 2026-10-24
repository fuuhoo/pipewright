import { describe, expect, it } from 'vitest'

import type { DeclaredPathDTO, SnapshotEntryDTO, SnapshotReason } from '../api/runs'
import {
  DEFAULT_EXPAND_DEPTH,
  RENDER_LIMIT,
  allDeclaredHit,
  allDirPaths,
  buildEntryTree,
  countNodes,
  declaredStatusKey,
  flattenVisible,
  isExpanded,
  missedDeclared,
  reasonClass,
  reasonKey,
  snapshotTitle,
  type SnapNode,
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

describe('buildEntryTree', () => {
  const flat = [
    entry('package.json', { size: 12 }),
    entry('backend/'),
    entry('frontend/'),
    entry('frontend/dist/'),
    entry('frontend/dist/index.html', { size: 900 }),
  ]

  it('扁平路径还原成层级:目录先排、同类按名升序', () => {
    const roots = buildEntryTree(flat)
    expect(roots.map((n) => n.path)).toEqual(['backend/', 'frontend/', 'package.json'])
    const frontend = roots[1]
    expect(frontend.children.map((n) => n.path)).toEqual(['frontend/dist/'])
    expect(frontend.children[0].children.map((n) => n.path)).toEqual(['frontend/dist/index.html'])
  })

  it('尾斜杠即目录(采集器就是这么给的,靠它决定能不能展开)', () => {
    const roots = buildEntryTree(flat)
    expect(roots.map((n) => n.isDir)).toEqual([true, true, false])
    expect(roots[1].children[0].isDir).toBe(true)
    expect(roots[1].children[0].children[0].isDir).toBe(false)
  })

  it('depth 从 0 起算,name 只留基名(渲染时不再重复整条路径)', () => {
    const roots = buildEntryTree(flat)
    const dist = roots[1].children[0]
    expect([roots[0].depth, roots[1].depth, dist.depth, dist.children[0].depth]).toEqual([0, 0, 1, 2])
    expect([dist.name, dist.children[0].name]).toEqual(['dist', 'index.html'])
  })

  it('大小与「另有 N 项未列出」原样挂在各自节点上', () => {
    const roots = buildEntryTree([entry('node_modules/', { dropped: 3400 }), entry('a.txt', { size: 5 })])
    expect(roots[0].dropped).toBe(3400)
    expect(roots[1].size).toBe(5)
  })

  it('中间层缺失也补出隐式目录(不假设清单完整)', () => {
    const roots = buildEntryTree([entry('a/b/c.txt', { size: 1 })])
    expect(roots.map((n) => n.path)).toEqual(['a/'])
    expect(roots[0].children.map((n) => n.path)).toEqual(['a/b/'])
    expect(roots[0].children[0].children.map((n) => n.path)).toEqual(['a/b/c.txt'])
  })

  it('重复路径只留一份(否则 v-for 的 key 会撞车)', () => {
    expect(buildEntryTree([entry('x/'), entry('x/')])).toHaveLength(1)
  })

  it('空清单 → 空树', () => {
    expect(buildEntryTree([])).toEqual([])
  })

  describe('带筛选词', () => {
    it('命中项的整条祖先链保留,不相关分支剪掉', () => {
      const roots = buildEntryTree(flat, 'index.html')
      expect(roots.map((n) => n.path)).toEqual(['frontend/'])
      expect(roots[0].children.map((n) => n.path)).toEqual(['frontend/dist/'])
      expect(roots[0].children[0].children.map((n) => n.path)).toEqual(['frontend/dist/index.html'])
    })

    it('大小写不敏感', () => {
      expect(buildEntryTree(flat, 'DIST')[0].children.map((n) => n.path)).toEqual(['frontend/dist/'])
    })

    it('目录名命中 → 整支留下(子项的相对路径天然带着这段前缀)', () => {
      const roots = buildEntryTree(flat, 'front')
      expect(roots.map((n) => n.path)).toEqual(['frontend/'])
      expect(roots[0].children.map((n) => n.path)).toEqual(['frontend/dist/'])
      expect(roots[0].selfMatch).toBe(true)
    })

    it('命中判定同时看自身与分支(供渲染层决定强调谁)', () => {
      const roots = buildEntryTree(flat, 'dist')
      const frontend = roots[0]
      expect([frontend.selfMatch, frontend.branchMatch]).toEqual([false, true])
      expect([frontend.children[0].selfMatch, frontend.children[0].branchMatch]).toEqual([true, true])
    })

    it('搜不到 → 空数组(不是 undefined,也不是全量)', () => {
      expect(buildEntryTree(flat, 'nope')).toEqual([])
    })

    it('纯空格查询等于不筛', () => {
      expect(buildEntryTree(flat, '   ')).toHaveLength(3)
    })
  })
})

function pick(roots: SnapNode[], path: string): SnapNode | undefined {
  for (const n of roots) {
    if (n.path === path) return n
    const hit = pick(n.children, path)
    if (hit) return hit
  }
  return undefined
}

function find(roots: SnapNode[], path: string): SnapNode {
  const hit = pick(roots, path)
  if (!hit) throw new Error(`树里没有 ${path}`)
  return hit
}

describe('isExpanded', () => {
  const deep = buildEntryTree([entry('a/'), entry('a/b/'), entry('a/b/c/'), entry('a/b/c/d.txt')])
  const a = find(deep, 'a/')
  const b = find(deep, 'a/b/')
  const c = find(deep, 'a/b/c/')

  it('默认展开到第 2 层:depth 0/1 展开,更深折叠', () => {
    expect(isExpanded(a, {}, false)).toBe(true)
    expect(isExpanded(b, {}, false)).toBe(true)
    expect(isExpanded(c, {}, false)).toBe(false)
    expect(DEFAULT_EXPAND_DEPTH).toBe(2)
  })

  it('手点的覆盖永远优先 —— 包括搜索态下手动折叠', () => {
    expect(isExpanded(a, { 'a/': false }, false)).toBe(false)
    expect(isExpanded(c, { 'a/b/c/': true }, false)).toBe(true)
    expect(isExpanded(c, { 'a/b/c/': false }, true)).toBe(false)
  })

  it('搜索态默认全展开(否则命中要连点几下才看到)', () => {
    expect(isExpanded(c, {}, true)).toBe(true)
  })
})

describe('flattenVisible', () => {
  const tree = buildEntryTree([entry('a/'), entry('a/b/'), entry('a/b/c/'), entry('a/b/c/d.txt'), entry('z.txt')])

  it('只摊开当前可见的行,深度随层级走', () => {
    const v = flattenVisible(tree, {}, false)
    expect(v.rows.map((r) => [r.node.path, r.node.depth])).toEqual([
      ['a/', 0],
      ['a/b/', 1],
      ['a/b/c/', 2],
      ['z.txt', 0],
    ])
    expect(v.hidden).toBe(0)
  })

  it('展开一个目录后它的子项才进列表', () => {
    const v = flattenVisible(tree, { 'a/b/c/': true }, false)
    expect(v.rows.map((r) => r.node.path)).toEqual(['a/', 'a/b/', 'a/b/c/', 'a/b/c/d.txt', 'z.txt'])
  })

  it('没子项也没 dropped 的目录不给假箭头(往下既没内容也没截断线索)', () => {
    const v = flattenVisible(buildEntryTree([entry('e/'), entry('f.txt')]), {}, false)
    expect(v.rows.map((r) => [r.node.path, r.expandable])).toEqual([['e/', false], ['f.txt', false]])
    // 有子项但当前折叠的目录仍然可点
    const t = flattenVisible(tree, {}, false)
    expect(t.rows.find((r) => r.node.path === 'a/b/c/')?.expandable).toBe(true)
  })

  it('只有 dropped 的目录仍可展开(展开后能看到「另有 N 项」这条线索)', () => {
    const v = flattenVisible(buildEntryTree([entry('big/', { dropped: 99 })]), {}, false)
    expect(v.rows[0].expandable).toBe(true)
  })

  it('超上限只取前 N 行,并如实报还有多少行没展示', () => {
    const many = buildEntryTree(entries(10))
    const v = flattenVisible(many, {}, false, 4)
    expect(v.rows).toHaveLength(4)
    expect(v.hidden).toBe(6)
    expect(v.limit).toBe(4)
  })

  it('默认上限就是 RENDER_LIMIT', () => {
    expect(flattenVisible(buildEntryTree(entries(RENDER_LIMIT + 3)), {}, false).hidden).toBe(3)
  })
})

describe('allDirPaths', () => {
  it('收全部目录(含嵌套),不含文件 —— 供「展开全部 / 折叠全部」', () => {
    const roots = buildEntryTree([entry('a/'), entry('a/b/'), entry('a/b/c.txt'), entry('z.txt')])
    expect(allDirPaths(roots)).toEqual(['a/', 'a/b/'])
  })
})

describe('countNodes', () => {
  const list = [entry('a/'), entry('a/b/'), entry('a/b/c.txt'), entry('z.txt')]

  it('节点总数 = 目录 + 文件都算一层', () => {
    expect(countNodes(buildEntryTree(list))).toBe(4)
  })

  it('搜索后即为命中数(与展开/折叠无关,面板计数的口径)', () => {
    expect(countNodes(buildEntryTree(list, 'b/c'))).toBe(3) // a/ → a/b/ → a/b/c.txt
  })

  it('空清单 / 搜不到都是 0', () => {
    expect(countNodes([])).toBe(0)
    expect(countNodes(buildEntryTree(list, 'nope'))).toBe(0)
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
  it('三种采集原因各有文案键', () => {
    expect(reasonKey('failed')).toBe('snapReasonFailed')
    expect(reasonKey('artifact_miss')).toBe('snapReasonMiss')
    expect(reasonKey('success')).toBe('snapReasonSuccess')
  })

  it('后端加了未知原因时说「未知」,不假扮成失败', () => {
    expect(reasonKey('future_reason' as SnapshotReason)).toBe('snapReasonUnknown')
  })
})

describe('reasonClass', () => {
  it('成功采集用淡绿档,不与失败/未命中抢注意力', () => {
    expect(reasonClass('success')).toBe('snap-reason--ok')
  })

  it('失败与未命中仍用各自的告警档,未知按失败档兜底', () => {
    expect(reasonClass('failed')).toBe('snap-reason--failed')
    expect(reasonClass('artifact_miss')).toBe('snap-reason--miss')
    expect(reasonClass('future_reason' as SnapshotReason)).toBe('snap-reason--failed')
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
