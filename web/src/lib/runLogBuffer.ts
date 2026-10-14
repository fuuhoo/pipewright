/**
 * 运行日志终端(黑框)的纯逻辑:seq 缓冲、脱敏分段、尾部渲染窗口。
 *
 * 抽出来的原因是一条实测曲线:13553 行的日志,旧实现每收到一行就
 * `Array.from(map.values()).sort()` 并把整个数组换个新引用 —— 光这一步
 * 在浏览器里就要 1804ms(而且随行数平方增长),外面还套一层整表 vnode diff。
 * 于是「打开正在构建的运行页」= 主线程被钉死。这里把三件事各自变成有界:
 *   · **入队**:seq 单调追加是绝对多数路径,O(1);真出现乱序才二分插入。
 *   · **分段**:`[MASKED]` 的切分每行只做一次(旧代码写在 render 里,每次重渲染
 *     把全表文本重扫一遍)。
 *   · **渲染**:只挂最近 LOG_TAIL_WINDOW 行,更早的靠「显示更早」逐批展开。
 *     DOM 规模从「日志有多少行」变成「窗口有多大」。
 */
import type { RunLogLine } from '../api/runs'

/** 一行里的一段:要么是被脱敏的标记,要么是普通正文。 */
export interface RunLogSeg {
  text: string
  masked: boolean
}

/** 渲染用的一行 = DTO + 已经切好的分段。 */
export interface RenderedRunLogLine extends RunLogLine {
  segs: RunLogSeg[]
}

const MASK_TOKEN = '[MASKED]'

/**
 * 按 `[MASKED]` 切分正文(后端已脱敏,前端只是把标记高亮,绝不二次处理 secret)。
 * 不含标记时返回单段,免得每行都构造数组。
 */
export function splitRunLogText(text: string): RunLogSeg[] {
  if (!text.includes(MASK_TOKEN)) return [{ text, masked: false }]
  const out: RunLogSeg[] = []
  let rest = text
  for (;;) {
    const idx = rest.indexOf(MASK_TOKEN)
    if (idx < 0) break
    if (idx > 0) out.push({ text: rest.slice(0, idx), masked: false })
    out.push({ text: MASK_TOKEN, masked: true })
    rest = rest.slice(idx + MASK_TOKEN.length)
  }
  if (rest.length > 0) out.push({ text: rest, masked: false })
  return out
}

export interface RunLogBuffer {
  /** 收一批行:按 seq 去重、升序维护,返回其中**真正新增**的行(已带分段)。 */
  add(incoming: readonly RunLogLine[]): RenderedRunLogLine[]
  /** 升序快照。返回内部数组本身(只读;原地增长,靠调用方的变更戳驱动重算)。 */
  all(): RenderedRunLogLine[]
  readonly size: number
  clear(): void
}

export function createRunLogBuffer(): RunLogBuffer {
  const seen = new Set<number>()
  const items: RenderedRunLogLine[] = []

  /** 乱序行的落点:二分找第一个 seq 更大的位置插进去。 */
  function insertAscending(entry: RenderedRunLogLine): void {
    let lo = 0
    let hi = items.length
    while (lo < hi) {
      const mid = (lo + hi) >>> 1
      if (items[mid].seq < entry.seq) lo = mid + 1
      else hi = mid
    }
    items.splice(lo, 0, entry)
  }

  function add(incoming: readonly RunLogLine[]): RenderedRunLogLine[] {
    if (incoming.length === 0) return []
    const appended: RenderedRunLogLine[] = []
    // 乱序行极少(SSE 历史回放与实时 tail 重叠时会被去重掉),攒起来最后一次性插。
    const strays: RenderedRunLogLine[] = []
    for (const line of incoming) {
      if (seen.has(line.seq)) continue
      seen.add(line.seq)
      const entry: RenderedRunLogLine = { ...line, segs: splitRunLogText(line.text) }
      const tail = items[items.length - 1]
      if (!tail || line.seq > tail.seq) {
        items.push(entry)
        appended.push(entry)
      } else {
        strays.push(entry)
      }
    }
    if (strays.length > 1) strays.sort((a, b) => a.seq - b.seq)
    for (const s of strays) insertAscending(s)
    return appended
  }

  return {
    add,
    all: () => items,
    get size() {
      return items.length
    },
    clear() {
      seen.clear()
      items.length = 0
    },
  }
}

/** 默认只挂最近这么多行:再多的行走「显示更早」分批展开。 */
export const LOG_TAIL_WINDOW = 1500

/** 每点一次「显示更早」多展开这么多行。 */
export const LOG_WINDOW_STEP = 1500

/** 一帧内新增这么多行以上就不再做淡入动画(几百个动画同时跑比不 anim 更贵)。 */
export const LOG_ANIMATE_MAX = 200

export interface LogWindow {
  /** 窗口左边界(含);也是「上面还压着多少行没挂出来」。 */
  start: number
  /** 窗口右边界(不含):永远是末尾,贴尾渲染。 */
  end: number
  hiddenEarlier: number
}

/**
 * 尾部窗口的位置:`revealed` = 用户点「显示更早」累计展开的行数。
 * 行数不足窗口时自然退化为「全量渲染」,小日志的行为跟以前一样。
 */
export function logWindowFor(total: number, revealed: number, tail = LOG_TAIL_WINDOW): LogWindow {
  const shown = Math.min(Math.max(total, 0), Math.max(1, tail) + Math.max(0, revealed))
  const start = Math.max(0, total - shown)
  return { start, end: Math.max(total, 0), hiddenEarlier: start }
}
