import { describe, it, expect, beforeEach, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import type { ServerContainers } from '../api/containers'
import type { Server } from '../api/servers'

// 容器页主机卡片的顺序:默认「在线的在前、离线的沉底」,置顶和拖出来的顺序盖在它上面。
// 这里锁的是页面接线(偏好读进来、写得出去、清得掉,且写的是整份名单);
// 排序规则本身在 lib/serverViewOrder.test.ts,拖放手势在 ServerCard.hostOrder.test.ts。
const streamMock = vi.fn()
const statsMock = vi.fn()
const listServersMock = vi.fn()
const listGroupsMock = vi.fn()
const getPrefsMock = vi.fn()
const putPrefsMock = vi.fn()

vi.mock('../api/containers', () => ({
  streamAllContainers: (...a: unknown[]) => streamMock(...a),
  getServerContainerStats: (...a: unknown[]) => statsMock(...a),
}))

vi.mock('../api/servers', () => ({
  listServers: () => listServersMock(),
  serviceAction: vi.fn(),
}))

vi.mock('../api/groups', () => ({
  listGroups: () => listGroupsMock(),
}))

vi.mock('../api/account', () => ({
  getServerViewPrefs: () => getPrefsMock(),
  putServerViewPrefs: (...a: unknown[]) => putPrefsMock(...a),
}))

// 页面只在「开终端」里用 router,测试不点它 —— 给个桩只为不刷一屏 injection 警告。
vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router')
  return { ...actual, useRouter: () => ({ resolve: () => ({ href: '' }), push: vi.fn(), replace: vi.fn() }) }
})

import Containers from './Containers.vue'
import ServerCard from '../components/ops/ServerCard.vue'

function host(serverId: string, reachable = true): ServerContainers {
  return {
    serverId,
    reachable,
    runtime: reachable ? 'docker' : '',
    error: reachable ? '' : '连不上',
    containers: [],
    running: 0,
    total: 0,
    collectedAt: '',
  } as ServerContainers
}

function server(id: string, name: string): Server {
  return { id, name, host: '10.0.0.1', port: 22, user: 'root', groupIds: [] } as unknown as Server
}

/** 三台机器:s1/s3 在线、s2 离线 —— 默认排序会把 s2 沉底,正好用来验「置顶压过在线优先」。 */
const HOSTS = [host('s1'), host('s2', false), host('s3')]

async function mountPage(prefs: { pinned: string[]; order: string[] } = { pinned: [], order: [] }) {
  streamMock.mockImplementation(async (onItem: (g: ServerContainers) => void) => {
    for (const g of HOSTS) onItem(g)
  })
  statsMock.mockResolvedValue({
    serverId: 's1',
    reachable: true,
    runtime: 'docker',
    error: '',
    stats: [],
    collectedAt: '',
  })
  listServersMock.mockResolvedValue([server('s1', 'alpha'), server('s2', 'beta'), server('s3', 'gamma')])
  listGroupsMock.mockResolvedValue([])
  getPrefsMock.mockResolvedValue(prefs)
  putPrefsMock.mockImplementation(async (p: unknown) => ({ ...(p as object) }))
  const wrapper = mount(Containers)
  await flushPromises()
  return wrapper
}

function cardNames(wrapper: ReturnType<typeof mount>): string[] {
  return wrapper.findAll('.panel__name').map((n) => n.text())
}

describe('容器页主机卡片顺序', () => {
  beforeEach(() => {
    vi.useRealTimers()
    vi.clearAllMocks()
  })

  it('没有偏好时按在线优先(离线那台沉底)', async () => {
    const wrapper = await mountPage()
    expect(cardNames(wrapper)).toEqual(['alpha', 'gamma', 'beta'])
  })

  it('置顶的那台压过在线优先', async () => {
    const wrapper = await mountPage({ pinned: ['s2'], order: [] })
    expect(cardNames(wrapper)).toEqual(['beta', 'alpha', 'gamma'])
  })

  it('拖出来的顺序说了算,离线那台不再被每轮刷新拖回末尾', async () => {
    const wrapper = await mountPage({ pinned: [], order: ['s2', 's3', 's1'] })
    expect(cardNames(wrapper)).toEqual(['beta', 'gamma', 'alpha'])
  })

  it('点图钉:整份偏好写回后端,新置顶的排到最前', async () => {
    vi.useFakeTimers()
    const wrapper = await mountPage()
    // 屏序 alpha / gamma / beta:点第二张(gamma)的图钉。
    await wrapper.findAll('.pin')[1].trigger('click')
    expect(putPrefsMock).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(500)
    expect(putPrefsMock).toHaveBeenCalledTimes(1)
    expect(putPrefsMock.mock.calls[0][0]).toEqual({ pinned: ['s3'], order: [] })
  })

  it('连点两张图钉合并成一次保存', async () => {
    vi.useFakeTimers()
    const wrapper = await mountPage()
    const pins = wrapper.findAll('.pin')
    await pins[0].trigger('click')
    await pins[1].trigger('click')
    await vi.advanceTimersByTimeAsync(500)
    expect(putPrefsMock).toHaveBeenCalledTimes(1)
    expect(putPrefsMock.mock.calls[0][0]).toEqual({ pinned: ['s3', 's1'], order: [] })
  })

  it('落点写的是整份名单:搜索把别的机器筛掉了,它们的位置也一起排好', async () => {
    vi.useFakeTimers()
    const wrapper = await mountPage()
    await wrapper.find('.search-box__input').setValue('alpha')
    expect(cardNames(wrapper)).toEqual(['alpha'])
    // 屏上只剩 alpha,把 beta 拖到它前面 —— 存下去的顺序仍然覆盖三台(gamma 没被挤掉)。
    expect(wrapper.findAllComponents(ServerCard)).toHaveLength(1)
    wrapper.findAllComponents(ServerCard)[0].vm.$emit('reorder-host', 's2', 'before')
    await vi.advanceTimersByTimeAsync(500)
    expect(putPrefsMock.mock.calls[0][0]).toEqual({ pinned: [], order: ['s2', 's1', 's3'] })
  })

  it('排过序才有「恢复默认排序」,点它只清手动顺序、保留置顶', async () => {
    vi.useFakeTimers()
    const wrapper = await mountPage({ pinned: ['s2'], order: ['s3', 's1', 's2'] })
    const reset = wrapper.find('.order-reset')
    expect(reset.exists()).toBe(true)
    await reset.trigger('click')
    await vi.advanceTimersByTimeAsync(500)
    expect(putPrefsMock.mock.calls[0][0]).toEqual({ pinned: ['s2'], order: [] })
    // 顺序清掉后按钮跟着消失(置顶还在,所以卡片仍钉在最上面)。
    await flushPromises()
    expect(wrapper.find('.order-reset').exists()).toBe(false)
    expect(cardNames(wrapper)[0]).toBe('beta')
  })

  it('没排过序时不出现这个按钮(没东西可恢复)', async () => {
    const wrapper = await mountPage({ pinned: ['s2'], order: [] })
    expect(wrapper.find('.order-reset').exists()).toBe(false)
  })

  it('偏好读失败就当没设置过:不弹错误,也不拦着看容器', async () => {
    // once 队列优先于 mountPage 里装的默认值,所以这一发的 GET 一定失败。
    getPrefsMock.mockRejectedValueOnce(new Error('offline'))
    const wrapper = await mountPage()
    expect(cardNames(wrapper)).toEqual(['alpha', 'gamma', 'beta'])
    expect(putPrefsMock).not.toHaveBeenCalled()
  })
})
