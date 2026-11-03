import { describe, it, expect, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import type { ServerContainers } from '../../api/containers'

// 主机卡片的排序控件:图钉置顶 + 握把拖动 + 方向键挪格。这三样全靠原生 HTML5 拖放,
// jsdom 没有 DataTransfer,所以用例自己造一个 —— 也正好把「认哪个 MIME」这条契约钉住。
const getServerContainerStatsMock = vi.fn()

vi.mock('../../api/containers', async () => {
  const actual = await vi.importActual<typeof import('../../api/containers')>('../../api/containers')
  return {
    ...actual,
    getServerContainerStats: (...a: unknown[]) => getServerContainerStatsMock(...a),
  }
})

getServerContainerStatsMock.mockResolvedValue({
  serverId: 's1',
  reachable: false,
  runtime: '',
  error: '',
  stats: [],
  collectedAt: '',
})

import ServerCard from './ServerCard.vue'

const MIME = 'application/x-pipewright-host'

function group(over: Partial<ServerContainers> = {}): ServerContainers {
  return {
    serverId: 's1',
    reachable: false,
    runtime: '',
    error: '连不上',
    containers: [],
    running: 0,
    total: 0,
    collectedAt: '',
    ...over,
  } as ServerContainers
}

/**
 * 造一个够用的 DataTransfer:组件只用到 types / setData / getData / effectAllowed / setDragImage。
 * 一次拖动里写的东西要在 drop 时读得回来,所以 store 由调用方拿着;setData 顺带把类型登记进
 * types —— 真实浏览器就是这么做的,而落点判断(是不是主机拖动)认的正是它。
 */
function transfer(types: string[] = [], store: Record<string, string> = {}) {
  return {
    types,
    effectAllowed: '',
    setData: (k: string, v: string) => {
      store[k] = v
      if (!types.includes(k)) types.push(k)
    },
    getData: (k: string) => store[k] ?? '',
    setDragImage: () => undefined,
  }
}

function mountCard(props: Record<string, unknown> = {}) {
  return mount(ServerCard, {
    props: {
      group: group(),
      name: 'prod-a',
      host: 'root@10.0.0.1:22',
      stateFilter: 'all',
      search: '',
      pageSize: 0,
      bulkMode: false,
      selectedSet: new Set<string>(),
      foldedSet: new Set<string>(),
      ...props,
    },
  })
}

/** 卡片高 200:上半 = before,下半 = after(真实 jsdom 的 getBoundingClientRect 全是 0,必须自己钉)。 */
function stubHeight(wrapper: ReturnType<typeof mountCard>, height: number): void {
  const el = wrapper.find('article').element as Element
  el.getBoundingClientRect = () =>
    ({ top: 0, left: 0, bottom: height, right: 0, height, width: 0, x: 0, y: 0, toJSON: () => ({}) }) as DOMRect
}

function grip(wrapper: ReturnType<typeof mountCard>) {
  return wrapper.find('.panel__grip')
}

describe('ServerCard 置顶按钮', () => {
  it('没置顶时 aria-pressed=false,点一下上报 toggle-pin', async () => {
    const wrapper = mountCard()
    const pin = wrapper.find('.pin')
    expect(pin.attributes('aria-pressed')).toBe('false')
    await pin.trigger('click')
    expect(wrapper.emitted('toggle-pin')).toHaveLength(1)
  })

  it('已置顶时按钮带 pin--on 且 aria-pressed=true(读屏能听出这一张钉住了)', () => {
    const wrapper = mountCard({ pinned: true })
    const pin = wrapper.find('.pin')
    expect(pin.classes()).toContain('pin--on')
    expect(pin.attributes('aria-pressed')).toBe('true')
  })
})

describe('ServerCard 拖动排序', () => {
  it('握把带 serverId 落到 dataTransfer,并用自己的 MIME 标记', async () => {
    const wrapper = mountCard()
    const store: Record<string, string> = {}
    const dt = transfer([], store)
    await grip(wrapper).trigger('dragstart', { dataTransfer: dt, clientX: 4, clientY: 4 })
    expect(dt.types).toContain(MIME)
    expect(store[MIME]).toBe('s1')
    expect(wrapper.find('article').classes()).toContain('panel--dragging')
  })

  it('落在目标卡片上半 → reorder-host(被拖那台, before)', async () => {
    const wrapper = mountCard()
    stubHeight(wrapper, 200)
    await wrapper
      .find('article')
      .trigger('drop', { dataTransfer: transfer([MIME], { [MIME]: 'other' }), clientY: 30 })
    expect(wrapper.emitted('reorder-host')).toEqual([['other', 'before']])
  })

  it('落在下半 → after', async () => {
    const wrapper = mountCard()
    stubHeight(wrapper, 200)
    await wrapper
      .find('article')
      .trigger('drop', { dataTransfer: transfer([MIME], { [MIME]: 'other' }), clientY: 170 })
    expect(wrapper.emitted('reorder-host')).toEqual([['other', 'after']])
  })

  it('拖回自己那张不上报(否则白白存一次没变化的顺序)', async () => {
    const wrapper = mountCard()
    stubHeight(wrapper, 200)
    await wrapper
      .find('article')
      .trigger('drop', { dataTransfer: transfer([MIME], { [MIME]: 's1' }), clientY: 30 })
    expect(wrapper.emitted('reorder-host')).toBeUndefined()
  })

  it('不带这个 MIME 的拖动(选中文字/文件)不当自己是落点', async () => {
    const wrapper = mountCard()
    stubHeight(wrapper, 200)
    await wrapper.find('article').trigger('dragover', { dataTransfer: transfer(['text/plain']), clientY: 30 })
    expect(wrapper.find('article').classes()).not.toContain('panel--drop-before')
    await wrapper.find('article').trigger('drop', { dataTransfer: transfer(['text/plain']), clientY: 30 })
    expect(wrapper.emitted('reorder-host')).toBeUndefined()
  })

  it('dragover 时按上下半显示插入线', async () => {
    const wrapper = mountCard()
    stubHeight(wrapper, 200)
    const article = wrapper.find('article')
    await article.trigger('dragover', { dataTransfer: transfer([MIME]), clientY: 30 })
    expect(article.classes()).toContain('panel--drop-before')
    await article.trigger('dragover', { dataTransfer: transfer([MIME]), clientY: 180 })
    expect(article.classes()).toContain('panel--drop-after')
  })
})

describe('ServerCard 键盘排序', () => {
  it('握把可聚焦,上下方向键各上报一格', async () => {
    const wrapper = mountCard()
    expect(grip(wrapper).attributes('tabindex')).toBe('0')
    await grip(wrapper).trigger('keydown', { key: 'ArrowUp' })
    await grip(wrapper).trigger('keydown', { key: 'ArrowDown' })
    expect(wrapper.emitted('nudge-host')).toEqual([[-1], [1]])
  })

  it('别的按键不动它(不抢页面的滚动/输入)', async () => {
    const wrapper = mountCard()
    await grip(wrapper).trigger('keydown', { key: 'Enter' })
    expect(wrapper.emitted('nudge-host')).toBeUndefined()
  })
})
