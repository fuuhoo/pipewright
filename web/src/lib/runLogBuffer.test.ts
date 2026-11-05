import { describe, it, expect } from 'vitest'
import type { RunLogLine } from '../api/runs'
import {
  createRunLogBuffer,
  splitRunLogText,
  logWindowFor,
  LOG_TAIL_WINDOW,
} from './runLogBuffer'

function line(seq: number, over: Partial<RunLogLine> = {}): RunLogLine {
  return { seq, ts: '2026-10-14T01:00:00Z', stream: 'stdout', stepOrdinal: 1, text: `行 ${seq}`, ...over }
}

// ─── 缓冲:去重 + 升序 ─────────────────────────────────────────────────────────
describe('createRunLogBuffer', () => {
  it('单调追加:返回值按 seq 升序,且只报真正新增的行', () => {
    const buf = createRunLogBuffer()
    const fresh = buf.add([line(1), line(2), line(3)])
    expect(fresh.map((l) => l.seq)).toEqual([1, 2, 3])
    expect(buf.all().map((l) => l.seq)).toEqual([1, 2, 3])
    expect(buf.size).toBe(3)
  })

  it('重复 seq 丢掉(SSE 历史回放与 REST 播种必然重叠)', () => {
    const buf = createRunLogBuffer()
    buf.add([line(1), line(2), line(3)])
    expect(buf.add([line(2), line(3), line(4)])).toHaveLength(1)
    expect(buf.all().map((l) => l.seq)).toEqual([1, 2, 3, 4])
    expect(buf.add([])).toEqual([])
    expect(buf.size).toBe(4)
  })

  it('乱序插入补不回单调性:小 seq 插到正确位置而不是末尾', () => {
    const buf = createRunLogBuffer()
    buf.add([line(10), line(20)])
    buf.add([line(15)])
    buf.add([line(5)])
    expect(buf.all().map((l) => l.seq)).toEqual([5, 10, 15, 20])
  })

  it('一批里既追加又乱序时,追加走快路径、乱序攒着一次插', () => {
    const buf = createRunLogBuffer()
    buf.add([line(100)])
    const fresh = buf.add([line(101), line(50), line(102), line(60)])
    // 追加段报给调用方(用于淡入),乱序段是历史行、不参与贴尾动画
    expect(fresh.map((l) => l.seq)).toEqual([101, 102])
    expect(buf.all().map((l) => l.seq)).toEqual([50, 60, 100, 101, 102])
  })

  it('整批倒序下发也保持升序', () => {
    const buf = createRunLogBuffer()
    buf.add([line(9), line(8), line(7), line(6)])
    expect(buf.all().map((l) => l.seq)).toEqual([6, 7, 8, 9])
  })

  it('分段在入队时就切好,渲染期不再扫全文', () => {
    const buf = createRunLogBuffer()
    const [a, b] = buf.add([line(1, { text: 'pwd=[MASKED] ok' }), line(2, { text: 'plain' })])
    expect(a.segs).toEqual([
      { text: 'pwd=', masked: false },
      { text: '[MASKED]', masked: true },
      { text: ' ok', masked: false },
    ])
    expect(b.segs).toEqual([{ text: 'plain', masked: false }])
  })

  it('clear 之后可复用同一个缓冲(切运行 / live⇄终态)', () => {
    const buf = createRunLogBuffer()
    buf.add([line(1), line(2)])
    buf.clear()
    expect(buf.size).toBe(0)
    expect(buf.all()).toEqual([])
    // 旧 seq 不再被当成重复
    expect(buf.add([line(1)]).map((l) => l.seq)).toEqual([1])
  })
})

// ─── [MASKED] 切分 ───────────────────────────────────────────────────────────
describe('splitRunLogText', () => {
  it('不含标记时返回单段', () => {
    expect(splitRunLogText('nothing here')).toEqual([{ text: 'nothing here', masked: false }])
  })

  it('空行也有一段(模板要能渲染出空的 term-text)', () => {
    expect(splitRunLogText('')).toEqual([{ text: '', masked: false }])
  })

  it('标记在行首 / 行尾 / 连着来', () => {
    expect(splitRunLogText('[MASKED]x')).toEqual([
      { text: '[MASKED]', masked: true },
      { text: 'x', masked: false },
    ])
    expect(splitRunLogText('x[MASKED]')).toEqual([
      { text: 'x', masked: false },
      { text: '[MASKED]', masked: true },
    ])
    expect(splitRunLogText('[MASKED][MASKED]')).toEqual([
      { text: '[MASKED]', masked: true },
      { text: '[MASKED]', masked: true },
    ])
  })
})

// ─── 往前翻页:prepend ───────────────────────────────────────────────────────
describe('createRunLogBuffer.prepend', () => {
  function seeded(): ReturnType<typeof createRunLogBuffer> {
    const buf = createRunLogBuffer()
    buf.add([line(6), line(7), line(8), line(9), line(10)])
    return buf
  }
  const seqsOf = (buf: ReturnType<typeof createRunLogBuffer>) => buf.all().map((l) => l.seq)

  it('更早的一页接到头部,顺序仍是升序', () => {
    const buf = seeded()
    const page = buf.prepend([line(3), line(4), line(5)])
    expect(page.map((l) => l.seq)).toEqual([3, 4, 5])
    expect(seqsOf(buf)).toEqual([3, 4, 5, 6, 7, 8, 9, 10])
    expect(buf.firstSeq).toBe(3)
  })

  it('已经收过的行不再挂第二遍', () => {
    const buf = seeded()
    expect(buf.prepend([line(1), line(2), line(3), line(4), line(5)]).map((l) => l.seq)).toEqual([1, 2, 3, 4, 5])
    expect(seqsOf(buf)).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9, 10])
    expect(buf.prepend([line(2), line(3)]).length).toBe(0)
  })

  it('页里跳了一行:停在洞之上,不把两段拼成假连续', () => {
    const buf = seeded()
    // 头部从 6 开始,这页缺 4 → 只接得上 5
    expect(buf.prepend([line(3), line(5)]).map((l) => l.seq)).toEqual([5])
    expect(seqsOf(buf)).toEqual([5, 6, 7, 8, 9, 10])
  })

  it('和头部不挨着的一页整段拒收(游标错了也不能硬拼)', () => {
    const buf = seeded()
    // 5 才是该接的那一行,给的是 1..4 → 中间断在 5
    expect(buf.prepend([line(1), line(2), line(3), line(4)]).length).toBe(0)
    expect(buf.size).toBe(5)
  })

  it('晚于头部的行不归 prepend 管(那是实时 tail 的活)', () => {
    const buf = seeded()
    expect(buf.prepend([line(5), line(11)]).map((l) => l.seq)).toEqual([5])
    expect(seqsOf(buf)).toEqual([5, 6, 7, 8, 9, 10])
  })

  it('空缓冲时等价于追加,不能让「显示更早」假装失败', () => {
    const buf = createRunLogBuffer()
    buf.prepend([line(2), line(1)])
    expect(seqsOf(buf)).toEqual([1, 2])
  })

  it('contiguous:false(按步骤翻页):seq 隔断也整段接上', () => {
    const buf = seeded()
    // 这一步只有偶数行:头部是 6,往上是 4、2 —— 缺的 5/3 属于别的步骤,不是断口。
    const page = buf.prepend([line(4), line(2)], { contiguous: false })
    expect(page.map((l) => l.seq)).toEqual([2, 4])
    expect(seqsOf(buf)).toEqual([2, 4, 6, 7, 8, 9, 10])
    expect(buf.firstSeq).toBe(2)
  })

  it('contiguous:false 仍去重,且不吃比头部更晚的行', () => {
    const buf = seeded()
    buf.prepend([line(4), line(2)], { contiguous: false })
    expect(buf.prepend([line(2), line(4)], { contiguous: false }).length).toBe(0)
    expect(buf.prepend([line(11)], { contiguous: false }).length).toBe(0)
    expect(seqsOf(buf)).toEqual([2, 4, 6, 7, 8, 9, 10])
  })

  it('firstSeq:空缓冲是 0,有行时是最小 seq', () => {
    const buf = createRunLogBuffer()
    expect(buf.firstSeq).toBe(0)
    buf.add([line(4), line(3)])
    expect(buf.firstSeq).toBe(3)
  })
})

// ─── 尾部窗口 ────────────────────────────────────────────────────────────────
describe('logWindowFor', () => {
  it('日志不足窗口时全量渲染(小日志行为与以前一致)', () => {
    expect(logWindowFor(37, 0)).toEqual({ start: 0, end: 37, hiddenEarlier: 0 })
    expect(logWindowFor(0, 0)).toEqual({ start: 0, end: 0, hiddenEarlier: 0 })
  })

  it('超过窗口只贴尾渲染,压住的行数就是 start', () => {
    expect(logWindowFor(13553, 0)).toEqual({
      start: 13553 - LOG_TAIL_WINDOW,
      end: 13553,
      hiddenEarlier: 13553 - LOG_TAIL_WINDOW,
    })
  })

  it('「显示更早」按累计展开量前移左边界,展开到底就停在 0', () => {
    const total = 3000
    expect(logWindowFor(total, 1500).start).toBe(0)
    expect(logWindowFor(total, 1200).start).toBe(300)
    expect(logWindowFor(total, 999_999).start).toBe(0)
    expect(logWindowFor(total, -5).start).toBe(1500)
  })

  it('实时 tail 继续追加时窗口右端始终跟到末尾', () => {
    expect(logWindowFor(20000, 0).end).toBe(20000)
  })
})
