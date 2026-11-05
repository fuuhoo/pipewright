import { describe, it, expect, beforeEach, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { nextTick } from 'vue'

import type { RunLogLine } from '../../api/runs'
import RunTerminal from './RunTerminal.vue'
import { LOG_TAIL_WINDOW, LOG_WINDOW_STEP } from '../../lib/runLogBuffer'

// 日志接口打桩:REST 拉历史是一次性 JSON,SSE 把 handlers 交出来让我们手动喂行。
const getRunLogsTailMock = vi.fn()
const subscribeMock = vi.fn()

vi.mock('../../api/runs', () => ({
  getRunLogsTail: (...a: unknown[]) => getRunLogsTailMock(...a),
  runLogsDownloadUrl: (id: string) => `/api/runs/${id}/logs/download`,
  subscribeRunEvents: (...a: unknown[]) => subscribeMock(...a),
}))

type LogHandler = (line: RunLogLine) => void

function line(seq: number, over: Partial<RunLogLine> = {}): RunLogLine {
  return {
    seq,
    ts: '2026-10-14T09:00:00Z',
    stream: 'stdout',
    stepOrdinal: 1,
    text: `line ${seq}`,
    ...over,
  }
}

function many(count: number): RunLogLine[] {
  return Array.from({ length: count }, (_, i) => line(i + 1))
}

/**
 * 按服务端 ?tail 的口径切片:先按 ?step 过滤(序号 0 起,null = 不筛),再取 seq<beforeSeq
 * 里最近的 tail 行(升序),并如实报首行 seq、上面还有没有更早、**该口径下**的总行数 ——
 * 测试里的「往上翻页」要和真接口同形。
 */
function tailServerFor(all: RunLogLine[]) {
  return (_id: string, tail: number, beforeSeq = 0, step?: number | null) => {
    const scoped = step == null ? all : all.filter((l) => l.stepOrdinal === step)
    const pool = beforeSeq > 0 ? scoped.filter((l) => l.seq < beforeSeq) : scoped
    const page = pool.slice(Math.max(0, pool.length - Math.max(1, tail)))
    return Promise.resolve({
      lines: page,
      nextSeq: all.length + 1,
      complete: true,
      firstSeq: page.length > 0 ? page[0].seq : undefined,
      hasEarlier: page.length > 0 && page[0].seq > (scoped[0]?.seq ?? 1),
      total: scoped.length,
    })
  }
}

/** 缓冲是原地增长的,靠 revision 驱动重算;等一帧 + 一次渲染刷完。 */
async function settle(): Promise<void> {
  await flushPromises()
  await nextTick()
}

/** rAF 回调已注册的时机在 ingest 里,所以这里排一个后面的回调就是「 flush 之后」。 */
function nextFrame(): Promise<void> {
  return new Promise((resolve) => requestAnimationFrame(() => resolve()))
}

async function mountTerminal(lines: RunLogLine[], filterOrdinal?: number | null) {
  getRunLogsTailMock.mockImplementation(tailServerFor(lines))
  const wrapper = mount(RunTerminal, { props: { runId: 'r1', live: false, filterOrdinal } })
  await settle()
  return wrapper
}

beforeEach(() => {
  getRunLogsTailMock.mockReset()
  subscribeMock.mockReset()
  subscribeMock.mockReturnValue(() => {})
})

describe('RunTerminal 尾部窗口渲染', () => {
  it('开页只搬一窗:走 ?tail,不逐页抽干整条日志', async () => {
    const total = LOG_TAIL_WINDOW + 1200
    await mountTerminal(many(total))

    expect(getRunLogsTailMock).toHaveBeenCalledWith('r1', LOG_TAIL_WINDOW, 0, null)
  })

  it('超长日志只挂最近一窗,计数说清「已加载 / 共」', async () => {
    const total = LOG_TAIL_WINDOW + 1200
    const wrapper = await mountTerminal(many(total))

    expect(wrapper.findAll('.term-line')).toHaveLength(LOG_TAIL_WINDOW)
    // 贴尾渲染:窗口末尾那行就是日志最后一行
    const last = wrapper.findAll('.term-line').at(-1)
    expect(last?.find('.term-ln').text()).toBe(String(total))
    const count = wrapper.find('.term-count').text()
    expect(count).toContain(String(LOG_TAIL_WINDOW))
    expect(count).toContain(String(total))
    // 服务端还没搬下来的那批也算进按钮,否则它会少报能翻的量
    expect(wrapper.find('.term-earlier').text()).toContain(String(total - LOG_TAIL_WINDOW))
  })

  it('「显示更早」向服务端要更早的一页,翻到 seq 1 后按钮消失', async () => {
    // 比窗口多 7 行:播种只拿到尾部一窗,上面还压着 7 行在服务端。
    const total = LOG_TAIL_WINDOW + 7
    const wrapper = await mountTerminal(many(total))
    expect(wrapper.findAll('.term-line')).toHaveLength(LOG_TAIL_WINDOW)

    await wrapper.find('.term-earlier').trigger('click')
    await settle()

    expect(getRunLogsTailMock).toHaveBeenCalledTimes(2)
    // 游标 = 已加载的最小 seq,所以这一页交出的正是 1..7
    expect(getRunLogsTailMock).toHaveBeenLastCalledWith('r1', LOG_WINDOW_STEP, 8, null)
    expect(wrapper.findAll('.term-line')).toHaveLength(total)
    expect(wrapper.find('.term-earlier').exists()).toBe(false)
  })

  it('短日志全量渲染,不出现「显示更早」', async () => {
    const wrapper = await mountTerminal(many(3))
    expect(wrapper.findAll('.term-line')).toHaveLength(3)
    expect(wrapper.find('.term-earlier').exists()).toBe(false)
  })

  it('选中步骤时按 ?step 只要那一步的一屏:早期步骤不再是空的', async () => {
    // 早期步骤(seq 1..3)在整个窗口里被后来的步骤挤掉 —— 服务端替我们筛,才不必把整条搬下来。
    const lines = [
      ...many(3).map((l) => ({ ...l, stepOrdinal: 1 })),
      ...Array.from({ length: 8 }, (_, i) => line(i + 4, { stepOrdinal: 2 })),
    ]

    const wrapper = await mountTerminal(lines, 1)

    expect(getRunLogsTailMock).toHaveBeenCalledWith('r1', LOG_TAIL_WINDOW, 0, 1)
    const rows = wrapper.findAll('.term-line')
    expect(rows).toHaveLength(3)
    expect(rows[0]?.find('.term-ln').text()).toBe('1')
    expect(wrapper.find('.term-count').text()).toContain('3')
  })

  it('选中步骤时「显示更早」要的是该步骤的更早一页:seq 被别的步骤隔断也接得上', async () => {
    // 偶数行归步骤 7、奇数行归步骤 8:步骤 7 有 1700 行,但 seq 全是断开的(2,4,6…)。
    const total = LOG_TAIL_WINDOW * 2 + 400
    const lines = Array.from({ length: total }, (_, i) =>
      line(i + 1, { stepOrdinal: (i + 1) % 2 === 0 ? 7 : 8 }),
    )

    const wrapper = await mountTerminal(lines, 7)
    // 一屏只有这一步的最近 1500 行,不是整条日志的 1500 行
    expect(wrapper.findAll('.term-line')).toHaveLength(LOG_TAIL_WINDOW)
    const firstLoaded = Number(wrapper.find('.term-line').find('.term-ln').text())
    expect(firstLoaded).toBe(402)

    await wrapper.find('.term-earlier').trigger('click')
    await settle()

    expect(getRunLogsTailMock).toHaveBeenLastCalledWith('r1', LOG_WINDOW_STEP, 402, 7)
    // 断开的 seq 不影响往前接:这一步一共 1700 行,现在全在里面了。
    expect(wrapper.findAll('.term-line')).toHaveLength(1700)
    expect(wrapper.find('.term-line').find('.term-ln').text()).toBe('2')
    expect(wrapper.find('.term-earlier').exists()).toBe(false)
  })

  it('下载全部日志指向后端逐行导出的地址', async () => {
    const wrapper = await mountTerminal(many(2))
    const dl = wrapper.find('a.term-dl')
    expect(dl.exists()).toBe(true)
    expect(dl.attributes('href')).toBe('/api/runs/r1/logs/download')
    expect(dl.attributes()).toHaveProperty('download')
  })

  it('实时 tail 逐行入队但按帧合并,重复 seq 不会多挂行', async () => {
    getRunLogsTailMock.mockResolvedValue({
      lines: [],
      nextSeq: 1,
      complete: false,
      firstSeq: undefined,
      hasEarlier: false,
      total: 0,
    })
    let onLog!: LogHandler
    subscribeMock.mockImplementation((_id: string, handlers: { onLog: LogHandler }) => {
      onLog = handlers.onLog
      return () => {}
    })

    const wrapper = mount(RunTerminal, { props: { runId: 'r1', live: true } })
    await settle()

    for (const s of [1, 2, 3, 3, 2]) onLog(line(s))
    await nextFrame()
    await settle()
    expect(wrapper.findAll('.term-line')).toHaveLength(3)
    // 小批实时新增才淡入
    expect(wrapper.findAll('.term-line--new')).toHaveLength(3)

    for (const s of [4, 5, 5]) onLog(line(s))
    await nextFrame()
    await settle()
    expect(wrapper.findAll('.term-line')).toHaveLength(5)
    expect(wrapper.find('.term-count').text()).toContain('5')
  })

  it('播种失败在实时模式不致命:照样建 SSE,让回放补历史', async () => {
    getRunLogsTailMock.mockRejectedValue(new Error('boom'))
    let sinceSeq = -1
    subscribeMock.mockImplementation((_id: string, _h: unknown, opts: { sinceSeq: number }) => {
      sinceSeq = opts.sinceSeq
      return () => {}
    })

    const wrapper = mount(RunTerminal, { props: { runId: 'r1', live: true } })
    await settle()

    expect(subscribeMock).toHaveBeenCalledTimes(1)
    expect(sinceSeq).toBe(0)
    expect(wrapper.find('.term-live').exists()).toBe(true)
  })

  it('脱敏标记仍然高亮(分段入队时切好,不在 render 里切)', async () => {
    const wrapper = await mountTerminal([line(1, { text: 'token = [MASKED] pushed' })])
    const masked = wrapper.find('.term-masked')
    expect(masked.exists()).toBe(true)
    expect(masked.text()).toBe('[MASKED]')
    expect(wrapper.find('.term-text').text()).toBe('token = [MASKED] pushed')
  })

  it('整屏回放的上千行不做淡入动画', async () => {
    const wrapper = await mountTerminal(many(LOG_TAIL_WINDOW))
    expect(wrapper.findAll('.term-line')).toHaveLength(LOG_TAIL_WINDOW)
    expect(wrapper.findAll('.term-line--new')).toHaveLength(0)
  })
})
