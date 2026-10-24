// 构建目录快照的展示口径(纯逻辑,无 DOM)。
//
// 后端给的是「一次采集 = 一串扁平相对路径」(目录带尾斜杠、单目录最多 200 条、整份最多 3000 条)。
// 面板要做的是把这份清单变得可读:能搜、能截断、未命中的声明路径把证据顶到前面。
// 判定全放这里,组件只渲染,便于单测。
//
// 清单以**树**呈现(扁平路径按目录层级还原 + 可折叠)。搜索时命中链自动展开,
// 否则答案会被埋在两次点击之后 —— 见 buildEntryTree 与 isExpanded。

import type {
  DeclaredPathDTO,
  DeclaredStatus,
  SnapshotEntryDTO,
  SnapshotReason,
} from '../api/runs'

/**
 * 一屏最多渲染这么多行。3000 条全铺会把浏览器卡住,而"目录到底生成了什么"几乎都是
 * 靠关键词搜出来的,所以默认截断 + 明确说还剩多少条没显示(不是"只有这些")。
 */
export const RENDER_LIMIT = 400

/** 默认展开到第 2 层(depth 0/1 的目录),即不点任何东西就能看到第三层条目。 */
export const DEFAULT_EXPAND_DEPTH = 2

/** 树节点。path 与后端同形(目录带尾斜杠,文件不带),这样 dropped/截断口径能原样对上。 */
export interface SnapNode {
  path: string
  name: string
  isDir: boolean
  /** 0 = 工作区根下第一层 */
  depth: number
  size: number
  /** 该目录下因单目录上限未列出的子项数(采集期就定了,前端补不出来) */
  dropped: number
  children: SnapNode[]
  /** 自身路径命中当前筛选词 */
  selfMatch: boolean
  /** 自身或任一子孙命中(搜索时决定这条分支留不留) */
  branchMatch: boolean
}

function isDirPath(p: string): boolean {
  return p.endsWith('/')
}

/** 条目所在目录的相对路径(带尾斜杠;根 = 空串)。 */
function parentDir(p: string): string {
  const s = isDirPath(p) ? p.slice(0, -1) : p
  const i = s.lastIndexOf('/')
  return i < 0 ? '' : s.slice(0, i + 1)
}

function baseName(p: string): string {
  const s = isDirPath(p) ? p.slice(0, -1) : p
  return s.slice(s.lastIndexOf('/') + 1)
}

function node(path: string, depth: number, size = 0, dropped = 0): SnapNode {
  return {
    path,
    name: baseName(path),
    isDir: isDirPath(path) || path === '',
    depth,
    size,
    dropped,
    children: [],
    selfMatch: false,
    branchMatch: false,
  }
}

/** 目录先排、同类按名升序(与采集器的排序口径一致)。 */
function sortChildren(n: SnapNode): void {
  n.children.sort((a, b) => {
    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1
    return a.name < b.name ? -1 : a.name > b.name ? 1 : 0
  })
  n.children.forEach(sortChildren)
}

/** 自底向上算命中:整条相对路径做子串匹配(空查询 = 全不命中,由调用方按「无筛选」处理)。 */
function markMatches(n: SnapNode, q: string): boolean {
  const self = q !== '' && n.path.toLowerCase().includes(q)
  let branch = self
  for (const ch of n.children) {
    if (markMatches(ch, q)) branch = true
  }
  n.selfMatch = self
  n.branchMatch = branch
  return branch
}

/** 剪枝:命中项所在分支整条留下(祖先要看得见),其余丢掉。 */
function prune(n: SnapNode): SnapNode | null {
  if (!n.branchMatch) return null
  const kept: SnapNode[] = []
  for (const ch of n.children) {
    const k = prune(ch)
    if (k) kept.push(k)
  }
  return { ...n, children: kept }
}

/**
 * 扁平清单 → 树。query 非空时同时剪枝(只留命中分支)。
 *
 * 采集器保证「父目录一定先于子项入列」,但清单整体受总条数与单目录上限截断,所以这里仍
 * 容忍缺失的中间层(补一个隐式目录节点),不假设数据完整。
 */
export function buildEntryTree(entries: SnapshotEntryDTO[], query = ''): SnapNode[] {
  const root = node('', -1)
  const dirs = new Map<string, SnapNode>([['', root]])
  const seen = new Set<string>()
  for (const e of entries) {
    if (!e.path || seen.has(e.path)) continue // 重复行会让 v-for 的 key 撞车
    seen.add(e.path)
    const parent = ensureDir(dirs, parentDir(e.path))
    const child = node(e.path, parent.depth + 1, e.size, e.dropped)
    parent.children.push(child)
    if (child.isDir) dirs.set(child.path, child)
  }
  sortChildren(root)
  const q = query.trim().toLowerCase()
  if (q === '') return root.children
  markMatches(root, q)
  return root.children
    .map(prune)
    .filter((n): n is SnapNode => n !== null)
}

function ensureDir(dirs: Map<string, SnapNode>, path: string): SnapNode {
  const hit = dirs.get(path)
  if (hit) return hit
  const parent = ensureDir(dirs, parentDir(path))
  const made = node(path, parent.depth + 1)
  parent.children.push(made)
  dirs.set(path, made)
  return made
}

/** 用户手点过的展开态(按节点 path 记);没记过的走自动口径。 */
export type ExpandOverrides = Record<string, boolean>

/**
 * 该目录的子项当前是否可见。
 *
 * 手点的永远优先(否则「展开全部」之后一搜索就被打回默认)。搜索态下默认全展开 ——
 * 命中藏在第 5 层却要用户点四下才看到,这一栏就白做了。
 */
export function isExpanded(n: SnapNode, overrides: ExpandOverrides, queryActive: boolean): boolean {
  const own = overrides[n.path]
  if (own !== undefined) return own
  if (queryActive) return true
  return n.depth < DEFAULT_EXPAND_DEPTH
}

export interface SnapRow {
  node: SnapNode
  /** 有子项可展开(空目录/未采集到的目录不给假箭头) */
  expandable: boolean
  expanded: boolean
}

export interface TreeView {
  rows: SnapRow[]
  /** 因渲染上限未展示的行数(0 = 全展示了) */
  hidden: number
  limit: number
}

/** 深度优先摊开「当前可见」的行,并按 RENDER_LIMIT 截断。 */
export function flattenVisible(
  roots: SnapNode[],
  overrides: ExpandOverrides,
  queryActive: boolean,
  limit: number = RENDER_LIMIT,
): TreeView {
  const rows: SnapRow[] = []
  const walk = (list: SnapNode[]): void => {
    for (const n of list) {
      const expandable = n.isDir && (n.children.length > 0 || n.dropped > 0)
      const expanded = expandable && isExpanded(n, overrides, queryActive)
      rows.push({ node: n, expandable, expanded })
      if (expanded) walk(n.children)
    }
  }
  walk(roots)
  return { rows: rows.slice(0, limit), hidden: Math.max(0, rows.length - limit), limit }
}

/** 所有目录路径(供「展开全部 / 折叠全部」一次性写覆盖表)。 */
export function allDirPaths(roots: SnapNode[]): string[] {
  const out: string[] = []
  const walk = (list: SnapNode[]): void => {
    for (const n of list) {
      if (!n.isDir) continue
      out.push(n.path)
      walk(n.children)
    }
  }
  walk(roots)
  return out
}

/**
 * 树里的节点总数。搜索后即为命中数,面板用它说「显示 N 条」——
 * 不能用可见行数:那个数会随用户展开/折叠跳动,和「清单到底有多少条」不是一回事。
 */
export function countNodes(roots: SnapNode[]): number {
  let n = 0
  const walk = (list: SnapNode[]): void => {
    for (const k of list) {
      n += 1
      walk(k.children)
    }
  }
  walk(roots)
  return n
}


/** 声明路径里"没命中"的那些(路径配错正是这块要解决的问题,必须顶在最前)。 */
export function missedDeclared(declared: DeclaredPathDTO[]): DeclaredPathDTO[] {
  return declared.filter((d) => d.status !== 'ok')
}

/** 声明路径全部命中(→ 这次采集只因节点失败发生)。 */
export function allDeclaredHit(declared: DeclaredPathDTO[]): boolean {
  return declared.every((d) => d.status === 'ok')
}

/** declared.status → `run` 命名空间下的文案键。 */
export function declaredStatusKey(status: DeclaredStatus): string {
  switch (status) {
    case 'ok':
      return 'snapStatusOk'
    case 'missing':
      return 'snapStatusMissing'
    case 'unmatched':
      return 'snapStatusUnmatched'
    case 'rejected':
      return 'snapStatusRejected'
    default:
      // 后端加了新状态而前端没跟:说"未知"而不是假装命中。
      return 'snapStatusUnknown'
  }
}

/** reason → 文案键(采集触发原因)。 */
export function reasonKey(reason: SnapshotReason): string {
  switch (reason) {
    case 'failed':
      return 'snapReasonFailed'
    case 'artifact_miss':
      return 'snapReasonMiss'
    case 'success':
      return 'snapReasonSuccess'
    default:
      // 后端加了新原因而前端没跟:说"未知"而不是假扮成失败。
      return 'snapReasonUnknown'
  }
}

/** reason → 标签的样式类后缀(失败红、未命中黄、成功绿)。 */
export function reasonClass(reason: SnapshotReason): string {
  switch (reason) {
    case 'artifact_miss':
      return 'snap-reason--miss'
    case 'success':
      return 'snap-reason--ok'
    default:
      return 'snap-reason--failed'
  }
}

/** 面板按快照逐块展示(一次运行里每个构建节点各一份),标题用「阶段 · 节点」。 */
export function snapshotTitle(stage: string, jobName: string): string {
  const s = stage.trim()
  const j = jobName.trim()
  if (s && j) return `${s} · ${j}`
  return s || j
}
