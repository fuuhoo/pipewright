// 构建目录快照的展示口径(纯逻辑,无 DOM)。
//
// 后端给的是「一次采集 = 一串扁平相对路径」(目录带尾斜杠、单目录最多 200 条、整份最多 3000 条)。
// 面板要做的是把这份清单变得可读:能搜、能截断、未命中的声明路径把证据顶到前面。
// 判定全放这里,组件只渲染,便于单测。

import type {
  DeclaredPathDTO,
  DeclaredStatus,
  SnapshotEntryDTO,
  SnapshotReason,
} from '../api/runs'
/** 目录条目(采集器始终给目录加尾斜杠,文件不加)。 */
export function isDirEntry(e: SnapshotEntryDTO): boolean {
  return e.path.endsWith('/')
}

/** 大小写不敏感的子串匹配(按整条相对路径)。空查询 = 原样返回。 */
export function filterEntries(entries: SnapshotEntryDTO[], query: string): SnapshotEntryDTO[] {
  const q = query.trim().toLowerCase()
  if (!q) return entries
  return entries.filter((e) => e.path.toLowerCase().includes(q))
}

/**
 * 一屏最多渲染这么多行。3000 条全铺会把浏览器卡住,而"目录到底生成了什么"几乎都是
 * 靠关键词搜出来的,所以默认截断 + 明确说还剩多少条没显示(不是"只有这些")。
 */
export const RENDER_LIMIT = 400

export interface EntryView {
  rows: SnapshotEntryDTO[]
  /** 未渲染的行数(0 = 全展示了) */
  hidden: number
  /** 渲染上限本身(供提示语说清"前 N 条"的 N 是上限,不是总数) */
  limit: number
}

export function capEntries(entries: SnapshotEntryDTO[], limit: number = RENDER_LIMIT): EntryView {
  if (entries.length <= limit) {
    return { rows: entries, hidden: 0, limit }
  }
  return { rows: entries.slice(0, limit), hidden: entries.length - limit, limit }
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
  return reason === 'artifact_miss' ? 'snapReasonMiss' : 'snapReasonFailed'
}

export interface RetentionWindow {
  count: number
  /** 单位文案键 */
  unitKey: string
}

/** 保留窗口秒数 → 「N 小时 / N 分钟」口径;拿不到窗口时回 0,由调用方省略这句。 */
export function retentionWindow(seconds: number): RetentionWindow {
  if (!Number.isFinite(seconds) || seconds <= 0) return { count: 0, unitKey: 'snapUnitHours' }
  if (seconds % 3600 === 0) return { count: seconds / 3600, unitKey: 'snapUnitHours' }
  if (seconds % 60 === 0) return { count: seconds / 60, unitKey: 'snapUnitMinutes' }
  return { count: seconds, unitKey: 'snapUnitSeconds' }
}

/** 同一节点可能有多份快照(失败采一次、产物未命中再采一次);面板按快照逐块展示,标题用「阶段 · 节点」。 */
export function snapshotTitle(stage: string, jobName: string): string {
  const s = stage.trim()
  const j = jobName.trim()
  if (s && j) return `${s} · ${j}`
  return s || j
}
