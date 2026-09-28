import { describe, it, expect, beforeEach, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import type { Server, ServerMetrics } from '../api/servers'
import ServerStatus from './ServerStatus.vue'

// ─── 两个 API 打桩:指标是逐台回调的流,名单是一次性 JSON ────────────────────────
const streamMock = vi.fn()
const listServersMock = vi.fn()

vi.mock('../api/servers', () => ({
  streamAllServerMetrics: (...a: unknown[]) => streamMock(...a),
  listServers: () => listServersMock(),
}))

function server(id: string, name: string): Server {
  return { id, name } as Server
}

function metric(id: string, over: Partial<ServerMetrics> = {}): ServerMetrics {
  return {
    serverId: id,
    reachable: true,
    error: '',
    cpu: { loadavg1: 0.5, cores: 4 },
    memory: null,
    disk: null,
    system: null,
    collectedAt: '2026-09-28T09:00:00Z',
    ...over,
  }
}

/** 一轮流:同步把这几台下发了(上屏节奏由调用方的 flushPromises 决定)。 */
function emitAll(...items: ServerMetrics[]) {
  streamMock.mockImplementation(async (cb: (m: ServerMetrics) => void) => {
    for (const m of items) cb(m)
  })
}

async function clickRefresh(wrapper: ReturnType<typeof mount>) {
  await wrapper.get('header button').trigger('click')
  await flushPromises()
}

describe('ServerStatus — 逐台出卡', () => {
  beforeEach(() => {
    streamMock.mockReset()
    listServersMock.mockReset().mockResolvedValue([server('a', 'local-A'), server('b', 'local-B')])
  })

  it('采完一台上一台,不等整轮结束', async () => {
    let emit: ((m: ServerMetrics) => void) | null = null
    let finishRound: () => void = () => undefined
    const round = new Promise<void>((res) => (finishRound = res))
    streamMock.mockImplementation(async (cb: (m: ServerMetrics) => void) => {
      emit = cb
      await round
    })

    const wrapper = mount(ServerStatus)
    await flushPromises()
    expect(emit).toBeTypeOf('function')
    // 流已开、还没回数据:只有骨架,没有卡片。
    expect(wrapper.findAll('.metrics-card')).toHaveLength(0)

    emit!(metric('a'))
    await flushPromises()
    expect(wrapper.findAll('.metrics-card')).toHaveLength(1)
    expect(wrapper.get('.metrics-card__name').text()).toBe('local-A')
    // 屏上只上了 1 台,分母也已是登记台数(不跟着上屏进度从 1/1 起跳)。
    expect(wrapper.text()).toContain('1/2 可达')
    // 整轮还没结束:「更新于」不该提前出现。
    expect(wrapper.text()).not.toContain('更新于')

    emit!(metric('b', { reachable: false, error: '无法连接服务器' }))
    await flushPromises()
    const cards = wrapper.findAll('.metrics-card')
    expect(cards).toHaveLength(2)
    // 可达在前、不可达沉底:第二张才是那台死机器。
    expect(cards[0].get('.metrics-card__name').text()).toBe('local-A')
    expect(cards[1].get('.reach-badge').classes()).toContain('reach-badge--down')

    finishRound()
    await flushPromises()
    // 分母 = 登记台数,死机上一台也不会把可达数凑成 2/2。
    expect(wrapper.text()).toContain('1/2 可达')
    expect(wrapper.text()).toContain('更新于')
  })

  it('下一轮就地替换同名卡:不增卡', async () => {
    emitAll(metric('a'), metric('b'))
    const wrapper = mount(ServerStatus)
    await flushPromises()
    expect(wrapper.findAll('.metrics-card')).toHaveLength(2)

    emitAll(metric('a', { cpu: { loadavg1: 3.75, cores: 4 } }), metric('b'))
    await clickRefresh(wrapper)
    await flushPromises()

    const cards = wrapper.findAll('.metrics-card')
    expect(cards).toHaveLength(2)
    expect(cards[0].text()).toContain('3.75')
  })

  it('刷新失败但屏上已有卡:卡片留着,只补一行提示(不整页变白)', async () => {
    emitAll(metric('a'), metric('b'))
    const wrapper = mount(ServerStatus)
    await flushPromises()

    streamMock.mockRejectedValue(new Error('boom'))
    await clickRefresh(wrapper)
    await flushPromises()

    expect(wrapper.findAll('.metrics-card')).toHaveLength(2)
    expect(wrapper.find('.view-sub__stale').exists()).toBe(true)
    expect(wrapper.find('.metrics-grid').exists()).toBe(true)
  })

  it('首屏就失败(屏上什么都没有)才走整页错误态', async () => {
    streamMock.mockRejectedValue(new Error('boom'))
    const wrapper = mount(ServerStatus)
    await flushPromises()

    expect(wrapper.findAll('.metrics-card')).toHaveLength(0)
    expect(wrapper.text()).toContain('加载服务器状态失败')
  })

  it('服务器被删后,下一轮不再留它的卡', async () => {
    emitAll(metric('a'), metric('b'))
    const wrapper = mount(ServerStatus)
    await flushPromises()
    expect(wrapper.findAll('.metrics-card')).toHaveLength(2)

    listServersMock.mockResolvedValue([server('a', 'local-A')])
    emitAll(metric('a'), metric('b')) // 后端仍下发已删的那台 → 名单里没有就该撤掉
    await clickRefresh(wrapper)
    await flushPromises()

    const names = wrapper.findAll('.metrics-card__name').map((n) => n.text())
    expect(names).toEqual(['local-A'])
  })
})
