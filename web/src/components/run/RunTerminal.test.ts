import { describe, it, expect, beforeEach, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { nextTick } from 'vue'

import type { RunLogLine } from '../../api/runs'
import RunTerminal from './RunTerminal.vue'
import { LOG_TAIL_WINDOW, LOG_WINDOW_STEP } from '../../lib/runLogBuffer'

// 日志接口打桩:REST 拉历史是一次性 JSON,SSE 把 handlers 交出来让我们手动喂行。
const getRunLogsMock = vi.fn()
const subscribeMock = vi.fn()

vi.mock('../../api/runs', () => ({
  getRunLogs: (...a: unknown[]) => getRunLogsMock(...a),
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

/** 缓冲是原地增长的,靠 revision 驱动重算;等一帧 + 一次渲染刷完。 */
async function settle(): Promise<void> {
  await flushPromises()
  await nextTick()
}

/** rAF 回调已注册的时机在 ingest 里,所以这里排一个后面的回调就是「 flush 之后」。 */
function nextFrame(): Promise<void> {
  return new Promise((resolve) => requestAnimationFrame(() => resolve()))
}

async function mountTerminal(lines: RunLogLine[]) {
  getRunLogsMock.mockResolvedValue({ lines, nextSeq: lines.length + 1, complete: true })
  const wrapper = mount(RunTerminal, { props: { runId: 'r1', live: false } })
  await settle()
  return wrapper
}

beforeEach(() => {
  getRunLogsMock.mockReset()
  subscribeMock.mockReset()
  subscribeMock.mockReturnValue(() => {})
})

describe('RunTerminal 尾部窗口渲染', () => {
  it('超长日志只挂最近一窗,行号计数仍是全量', async () => {
    const total = LOG_TAIL_WINDOW + 1200
    const wrapper = await mountTerminal(many(total))

    expect(wrapper.findAll('.term-line')).toHaveLength(LOG_TAIL_WINDOW)
    // 贴尾渲染:窗口末尾那行就是日志最后一行
    const last = wrapper.findAll('.term-line').at(-1)
    expect(last?.find('.term-ln').text()).toBe(String(total))
    expect(wrapper.find('.term-count').text()).toContain(String(total))
    // 压在上面没挂出来的那批,靠按钮展开
    expect(wrapper.find('.term-earlier').text()).toContain(String(total - LOG_TAIL_WINDOW))
  })

  it('点「显示更早」按步长往上扩,扩到底后按钮消失', async () => {
    const total = LOG_TAIL_WINDOW * 2 + 500
    const wrapper = await mountTerminal(many(total))
    expect(wrapper.findAll('.term-line')).toHaveLength(LOG_TAIL_WINDOW)

    await wrapper.find('.term-earlier').trigger('click')
    await settle()
    expect(wrapper.findAll('.term-line')).toHaveLength(LOG_TAIL_WINDOW + LOG_WINDOW_STEP)

    await wrapper.find('.term-earlier').trigger('click')
    await settle()
    expect(wrapper.findAll('.term-line')).toHaveLength(total)
    expect(wrapper.find('.term-earlier').exists()).toBe(false)
  })

  it('短日志全量渲染,不出现「显示更早」', async () => {
    const wrapper = await mountTerminal(many(3))
    expect(wrapper.findAll('.term-line')).toHaveLength(3)
    expect(wrapper.find('.term-earlier').exists()).toBe(false)
  })

  it('实时 tail 逐行入队但按帧合并,重复 seq 不会多挂行', async () => {
    getRunLogsMock.mockResolvedValue({ lines: [], nextSeq: 1, complete: false })
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
