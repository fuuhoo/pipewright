/**
 * 「本次会话的传输记录」的纯逻辑 + 会话内存储(远程文件面板的下方面板用)。
 *
 * 口径是用户定的两条,代码要如实反映它们:
 *   · **下载只记「已发起」**。下载是 `<a href>` 交给浏览器的(整份文件不进 JS,进度也就
 *     不在 JS 这边),所以平台只知道「点了下载、地址已经交给浏览器」,记不成成功或失败。
 *     目录下载体积在发起时未知(zip 是服务端边压边数),size 如实回 null。
 *   · **只活在当前标签页**。存储是模块内存表,按 serverId 分格:关掉弹窗还在(重开不必
 *     重放),刷新页面就空。不写 localStorage,也不问服务端 —— 那是审计的活,不是这里。
 *
 * 上传行不自己记账:每一帧都从驱动器的 UploadItemState 折算(lib/fsUpload 仍是唯一真源),
 * 所以这里只处理身份与排序,不碰 offset。
 */

import { joinPath } from './serverFs'
import type { UploadItemState } from './fsUpload'

export type TransferDirection = 'upload' | 'download'

/**
 * started 是下载专属的终态(「已交给浏览器」),平台不知道浏览器后来怎么样了。
 * 其余四态与上传队列同名,界面可以复用同一份文案。
 */
export type TransferStatus = 'queued' | 'uploading' | 'done' | 'error' | 'canceled' | 'started'

export interface TransferRow {
  /** 稳定身份:同一行每帧更新只换状态不换 id,界面才不会跳行。 */
  id: string
  direction: TransferDirection
  /** 上传 = 批次内相对落点(带子目录),下载 = 条目名。 */
  name: string
  /** 远端绝对路径(悬浮提示用;上传按发起时的目录算)。 */
  path: string
  /** null = 发起时长度未知(目录 zip)。 */
  size: number | null
  offset: number
  status: TransferStatus
  /** 发起时刻(ms)。 */
  at: number
  /** 上传失败时的错误码(文案由界面按码给)。 */
  error?: string
}

/** 记录条数上限:一次上传几千个文件的目录也只是一条批次,内存要有界。 */
export const TRANSFER_MAX_ROWS = 500

let batchSeq = 0

/**
 * 批次标识:驱动器的 item.key 只是批次内序号(`'0'`、`'1'`…),跨批次会撞,
 * 所以记录行要把它换成「批次 + 序号」。页面内单调递增,刷新即重置 —— 与记录本身同命。
 */
export function newTransferBatchId(): string {
  batchSeq += 1
  return `b${batchSeq}`
}

/**
 * 把一批上传状态折算成记录行。
 *
 * `at` 由调用方在**发起批次时**取一次并原样传进来:每一帧重算行时若现取 Date.now(),
 * 发起时刻就会一路漂到「最新」。
 */
export function uploadRowsOf(
  batchId: string,
  dir: string,
  items: readonly UploadItemState[],
  at: number,
): TransferRow[] {
  return items.map((it) => ({
    id: `${batchId}#${it.key}`,
    direction: 'upload' as const,
    name: it.rel,
    path: joinPath(dir, it.rel),
    size: it.size,
    offset: it.offset,
    status: it.status,
    at,
    error: it.error,
  }))
}

let downloadSeq = 0

/** 一次下载:交出去就记不动了,所以只有一行 started。 */
export function downloadRowOf(e: { name: string; path: string; size: number | null; isDir?: boolean }): TransferRow {
  const at = Date.now()
  // 同一个文件点两次下载是两条记录,身份要跟时刻一样新 —— 页面内单调序号够用。
  downloadSeq += 1
  return {
    id: `d${downloadSeq}`,
    direction: 'download',
    name: e.name,
    path: e.path,
    // 目录给 null:zip 体积在发起时没人知道,写 0 就成了假数据。
    size: e.isDir ? null : e.size,
    offset: 0,
    status: 'started',
    at,
  }
}

/**
 * 把本批次的行并进记录:已有 id 的原位覆盖,新 id 放到最前(列表按最新在上)。
 *
 * 原位覆盖是刻意的 —— 批次跑到后半程时,如果按「整批提到最前」来排,后面新起的下载
 * 会被在传的批次反复压下去,界面就一直跳。
 */
export function applyUploadBatch(rows: readonly TransferRow[], batchRows: readonly TransferRow[]): TransferRow[] {
  const next = [...rows]
  const index = new Map<string, number>()
  next.forEach((r, i) => index.set(r.id, i))
  const fresh: TransferRow[] = []
  for (const r of batchRows) {
    const at = index.get(r.id)
    if (at === undefined) fresh.push(r)
    else next[at] = r
  }
  return [...fresh, ...next]
}

/** 追加一条新记录(下载):最新在上,顺带截掉超出上限的尾巴。 */
export function prependTransfer(rows: readonly TransferRow[], row: TransferRow, max = TRANSFER_MAX_ROWS): TransferRow[] {
  return [row, ...rows].slice(0, max)
}

/**
 * 上限裁剪:先丢落定的旧行,再丢在传的。
 * 正在传的行留到最后才动 —— 半路的进度条凭空消失比丢几条历史糟糕得多。
 */
export function capTransfers(rows: readonly TransferRow[], max = TRANSFER_MAX_ROWS): TransferRow[] {
  if (rows.length <= max) return [...rows]
  const byNewest = (a: TransferRow, b: TransferRow): number => b.at - a.at
  const active = rows.filter((r) => !isSettled(r)).sort(byNewest)
  const settled = rows.filter(isSettled).sort(byNewest)
  return [...active, ...settled].slice(0, max)
}

/** 落定 = 不会再变的行。queued/uploading 还在动,started 是下载的终态。 */
export function isSettled(row: TransferRow): boolean {
  if (row.direction === 'download') return true
  return row.status === 'done' || row.status === 'error' || row.status === 'canceled'
}

export interface TransferSummary {
  uploads: number
  downloads: number
  done: number
  failed: number
  active: number
}

/** 表头计数(一律现算:retry 会把落定的行拉回在传,增量数字一定会说谎)。 */
export function transferSummary(rows: readonly TransferRow[]): TransferSummary {
  let uploads = 0
  let downloads = 0
  let done = 0
  let failed = 0
  let active = 0
  for (const r of rows) {
    if (r.direction === 'download') {
      downloads++
      continue
    }
    uploads++
    if (r.status === 'done') done++
    else if (r.status === 'error' || r.status === 'canceled') failed++
    else active++
  }
  return { uploads, downloads, done, failed, active }
}

/** 时刻 → 时:分:秒(记录密的时候日期没有信息量,悬浮提示再给完整时间)。 */
export function formatClock(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return ''
  const d = new Date(ms)
  if (Number.isNaN(d.getTime())) return ''
  return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

// ─── 会话内存储 ─────────────────────────────────────────────────────────────────
// 面板卸载(关弹窗 / 切服务器)时把当前数组交回这里,重开时取回去:同一标签页内连续,
// 页面刷新自然清空。故意不落地到 localStorage —— 远端路径本身是敏感信息,没必要留在盘上。
const logs = new Map<string, TransferRow[]>()

export function transferLogOf(serverId: string): TransferRow[] {
  return logs.get(serverId) ?? []
}

export function setTransferLog(serverId: string, rows: readonly TransferRow[]): void {
  if (!rows.length) logs.delete(serverId)
  else logs.set(serverId, capTransfers(rows))
}

export function clearTransferLog(serverId: string): void {
  logs.delete(serverId)
}
