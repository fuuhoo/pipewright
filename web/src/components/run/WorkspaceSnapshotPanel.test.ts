import { describe, it, expect, beforeEach, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import type { WorkspaceSnapshotDTO } from '../../api/runs'
import { HttpError } from '../../api/http'
import WorkspaceSnapshotPanel from './WorkspaceSnapshotPanel.vue'

// 快照数据打桩:?meta=1 的轻量清单和全量清单各一个 mock,用来证明「开页只取轻的」。
const getMetaMock = vi.fn()
const getFullMock = vi.fn()

vi.mock('../../api/runs', () => ({
  getRunSnapshotsMeta: (id: string) => getMetaMock(id),
  getRunSnapshots: (id: string) => getFullMock(id),
}))

function snap(over: Partial<WorkspaceSnapshotDTO> = {}): WorkspaceSnapshotDTO {
  return {
    id: 's-1',
    stage: 'build',
    jobId: 'job-1',
    jobName: '打包',
    reason: 'artifact_miss',
    declared: [{ path: 'dist/**/*.js', status: 'missing', parent: 'build', siblings: ['out.js'] }],
    entries: [
      { path: 'build/', size: 0, dropped: 0 },
      { path: 'build/out.js', size: 2048, dropped: 0 },
    ],
    entryCount: 2,
    truncated: false,
    createdAt: '2026-11-04T02:00:00Z',
    ...over,
  }
}

/** meta 口径:同一条快照,但目录条目换成一个条数。 */
function metaSnap(over: Partial<WorkspaceSnapshotDTO> = {}): WorkspaceSnapshotDTO {
  const s = snap(over)
  return { ...s, entries: [], entryCount: s.entries.length }
}

function mountPanel() {
  return mount(WorkspaceSnapshotPanel, {
    props: { runId: 'r-1' },
    global: { stubs: { teleport: true } },
  })
}

async function settle() {
  await flushPromises()
}

beforeEach(() => {
  getMetaMock.mockReset()
  getFullMock.mockReset()
  getMetaMock.mockResolvedValue({ snapshots: [metaSnap()] })
  getFullMock.mockResolvedValue({ snapshots: [snap()] })
})

describe('WorkspaceSnapshotPanel 触发条 + 弹窗', () => {
  it('开页只问 ?meta=1:轻量清单足够画出触发条,全量一次都不取', async () => {
    const wrapper = mountPanel()
    await settle()

    expect(getMetaMock).toHaveBeenCalledWith('r-1')
    expect(getFullMock).not.toHaveBeenCalled()
    expect(wrapper.find('.sn-bar').exists()).toBe(true)
    expect(wrapper.find('.sn-count').text()).toBe('1')
    // 没点开就不该有任何目录树进了 DOM
    expect(wrapper.find('.tree-list').exists()).toBe(false)
  })

  it('点开弹窗才取全量,并把目录清单挂出来', async () => {
    const wrapper = mountPanel()
    await settle()

    await wrapper.find('.sn-open').trigger('click')
    await settle()

    expect(getFullMock).toHaveBeenCalledWith('r-1')
    expect(wrapper.find('.app-modal').exists()).toBe(true)
    expect(wrapper.findAll('.tree-row').length).toBe(2)
    expect(wrapper.find('.tree-name').text()).toBe('build')
  })

  it('再点一次不重复取全量(同一份数据留在内存里)', async () => {
    const wrapper = mountPanel()
    await settle()
    await wrapper.find('.sn-open').trigger('click')
    await settle()
    await wrapper.find('.app-modal__x').trigger('click')
    await wrapper.find('.sn-open').trigger('click')
    await settle()

    expect(getFullMock).toHaveBeenCalledTimes(1)
  })

  it('触发条只说「几处产物没收到」,细节留给弹窗', async () => {
    const wrapper = mountPanel()
    await settle()

    expect(wrapper.find('.sn-miss').text()).toContain('1 处')
    expect(wrapper.find('.decl-list').exists()).toBe(false)
  })

  it('仓储未启用(503)与运行不存在(404)等价于「这里没东西」:整块不渲染', async () => {
    for (const status of [404, 503]) {
      getMetaMock.mockRejectedValue(new HttpError(status, null, 'nope'))
      const wrapper = mountPanel()
      await settle()
      expect(wrapper.find('.sn-bar').exists()).toBe(false)
      getMetaMock.mockResolvedValue({ snapshots: [metaSnap()] })
    }
  })

  it('没有快照也不渲染(不占一条空区块)', async () => {
    getMetaMock.mockResolvedValue({ snapshots: [] })
    const wrapper = mountPanel()
    await settle()
    expect(wrapper.find('.sn-bar').exists()).toBe(false)
  })

  it('取数失败要说清是失败,并给重试;重试成功才收起提示', async () => {
    getMetaMock.mockRejectedValueOnce(new HttpError(500, null, 'boom'))
    const wrapper = mountPanel()
    await settle()

    expect(wrapper.find('.sn-error').exists()).toBe(true)
    expect(wrapper.find('.sn-bar').exists()).toBe(true)

    await wrapper.find('.sn-open').trigger('click')
    await settle()
    expect(getMetaMock).toHaveBeenCalledTimes(2)
    expect(wrapper.find('.sn-error').exists()).toBe(false)
  })

  it('弹窗里取全量失败:就地报错并可重试,不把失败说成「没有快照」', async () => {
    getFullMock.mockRejectedValueOnce(new HttpError(500, null, 'boom'))
    const wrapper = mountPanel()
    await settle()

    await wrapper.find('.sn-open').trigger('click')
    await settle()
    expect(wrapper.find('.sn-state--err').exists()).toBe(true)
    expect(wrapper.findAll('.tree-row').length).toBe(0)

    await wrapper.find('.sn-state .sn-open').trigger('click')
    await settle()
    expect(getFullMock).toHaveBeenCalledTimes(2)
    expect(wrapper.findAll('.tree-row').length).toBe(2)
  })

  it('切换运行重新问 meta,并丢掉上一份全量(不会把上一个运行的清单串进来)', async () => {
    const wrapper = mountPanel()
    await settle()
    await wrapper.find('.sn-open').trigger('click')
    await settle()

    await wrapper.setProps({ runId: 'r-2' })
    await settle()
    expect(getMetaMock).toHaveBeenLastCalledWith('r-2')
    expect(wrapper.find('.app-modal').exists()).toBe(false)
    expect(wrapper.find('.tree-list').exists()).toBe(false)

    await wrapper.find('.sn-open').trigger('click')
    await settle()
    expect(getFullMock).toHaveBeenLastCalledWith('r-2')
  })

  it('搜索框只影响本快照的树:命中的行才挂出来', async () => {
    getFullMock.mockResolvedValue({
      snapshots: [
        snap({
          entries: [
            { path: 'build/', size: 0, dropped: 0 },
            { path: 'build/out.js', size: 2048, dropped: 0 },
            { path: 'src/', size: 0, dropped: 0 },
            { path: 'src/main.ts', size: 12, dropped: 0 },
          ],
        }),
      ],
    })
    const wrapper = mountPanel()
    await settle()
    await wrapper.find('.sn-open').trigger('click')
    await settle()

    // 未筛选时只报清单大小:树节点数含补齐的中间目录,拿来当「显示数」会比总数还大。
    expect(wrapper.find('.entries-count').text()).toBe('共 4 条')

    await wrapper.find('.entries-search').setValue('main')
    await settle()

    const names = wrapper.findAll('.tree-name').map((n) => n.text())
    expect(names).toEqual(['src', 'main.ts'])
    expect(wrapper.find('.tree-name--hit').text()).toBe('main.ts')
    // 命中数只算清单里真正匹配的那条;`src` 是带出来的祖先,不算命中。
    expect(wrapper.find('.entries-count').text()).toBe('命中 1 / 共 4 条')
  })

  it('命中数不会超过条目数:树里补出来的中间目录不算命中', async () => {
    getFullMock.mockResolvedValue({
      snapshots: [
        snap({
          // 清单只有文件行,中间三层是 buildEntryTree 现补的节点。
          entries: [{ path: 'target/classes/com/acme/mod1/ThingIK.class', size: 10, dropped: 0 }],
        }),
      ],
    })
    const wrapper = mountPanel()
    await settle()
    await wrapper.find('.sn-open').trigger('click')
    await settle()

    expect(wrapper.find('.entries-count').text()).toBe('共 1 条')
    await wrapper.find('.entries-search').setValue('classes')
    await settle()
    // 树里还挂着补齐的中间目录,命中数却只算那 1 条清单条目。
    expect(wrapper.findAll('.tree-name').length).toBeGreaterThan(1)
    expect(wrapper.find('.entries-count').text()).toBe('命中 1 / 共 1 条')
  })
})
