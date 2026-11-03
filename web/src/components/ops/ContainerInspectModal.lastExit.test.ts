import { describe, it, expect, beforeEach, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import type { DOMWrapper } from '@vue/test-utils'
import type { ContainerInspect } from '../../api/containers'

// 反复重启的容器读不到上一次的日志,唯一能拿到的死因证据就是 inspect 里那组字段 —— 而它在容器
// 重新拉起后会被 docker 清零。这些用例锁住界面对这三种情形的说法。
const getContainerInspectMock = vi.fn()

vi.mock('../../api/containers', () => ({
  getContainerInspect: (...a: unknown[]) => getContainerInspectMock(...a),
}))

import ContainerInspectModal from './ContainerInspectModal.vue'

function inspect(over: Partial<ContainerInspect> = {}): ContainerInspect {
  return {
    serverId: 's1',
    containerId: 'nginx',
    reachable: true,
    image: 'registry/app:1',
    command: 'node server.js',
    createdAt: '2026-11-01T10:00:00Z',
    state: 'running',
    restartPolicy: 'always',
    exitCode: 0,
    oomKilled: false,
    restartCount: 0,
    restartMaxRetry: 0,
    env: ['PATH=/usr/bin'],
    mounts: [],
    networks: [],
    ports: [],
    labels: {},
    ...over,
  }
}

async function openModal(over: Partial<ContainerInspect> = {}) {
  getContainerInspectMock.mockResolvedValue(inspect(over))
  const wrapper = mount(ContainerInspectModal, { props: { serverId: 's1', containerName: 'nginx' } })
  await flushPromises()
  return wrapper
}

/** 分区标题按顺序取第 n 个 section(title 文本 + 内容)。 */
function sectionByTitle(wrapper: ReturnType<typeof mount>, text: string): DOMWrapper<Element> | undefined {
  return wrapper
    .findAll('.ci-section')
    .find((s) => s.find('.ci-section-title').text().includes(text))
}

/** 把一个 .ci-kv 读成 标签 → 值 的字典。 */
function kv(section?: DOMWrapper<Element>) {
  const out: Record<string, string> = {}
  if (!section) return out
  const dts = section.findAll('.ci-kv dt')
  const dds = section.findAll('.ci-kv dd')
  dts.forEach((dt, i) => {
    out[dt.text()] = dds[i].text()
  })
  return out
}

describe('ContainerInspectModal 上次退出', () => {
  beforeEach(() => {
    getContainerInspectMock.mockReset()
  })

  it('退避中的崩溃容器:退出码 + OOM 说法 + 错误原文一起端出来', async () => {
    const wrapper = await openModal({
      state: 'restarting',
      exitCode: 137,
      oomKilled: true,
      stateError: 'oom 了',
      startedAt: '2026-11-02T13:07:29Z',
      finishedAt: '2026-11-02T13:07:30Z',
      restartCount: 7,
    })
    const section = sectionByTitle(wrapper, '上次退出')
    expect(section).toBeTruthy()
    const rows = kv(section)
    expect(rows['退出码']).toContain('137')
    expect(rows['退出码']).toContain('内存超限被 OOM 杀掉')
    expect(rows['错误信息']).toBe('oom 了')
    expect(rows['累计重启次数']).toBe('7')
    expect(section!.find('.ci-exit-oom').text()).toBe('OOM')
  })

  it('已经重新拉起:不谎报退出码,说明现场被清零并给出次数', async () => {
    const wrapper = await openModal({
      state: 'running',
      exitCode: 0,
      finishedAt: '2026-11-02T13:07:36Z',
      restartCount: 8,
    })
    const section = sectionByTitle(wrapper, '上次退出')
    expect(section).toBeTruthy()
    // 没有退出码行(那个 0 是 docker 清零后的,不是死因)
    expect(kv(section)['退出码']).toBeUndefined()
    expect(section!.find('.ci-note').text()).toContain('累计自动重启 8 次')
    expect(section!.findAll('.ci-kv dt').map((d) => d.text())).toEqual([
      '上次退出时刻',
      '本次启动时刻',
      '累计重启次数',
    ])
  })

  it('从没重启过的运行中容器不出现这一栏', async () => {
    const wrapper = await openModal({ state: 'running' })
    expect(sectionByTitle(wrapper, '上次退出')).toBeUndefined()
  })

  it('退出码含义:143 说 SIGTERM,1 说应用自己退出', async () => {
    const stopped = await openModal({ state: 'exited', exitCode: 143, finishedAt: '2026-11-02T13:07:36Z' })
    expect(kv(sectionByTitle(stopped, '上次退出'))['退出码']).toContain('收到 SIGTERM(143)')

    getContainerInspectMock.mockReset()
    const crashed = await openModal({ state: 'exited', exitCode: 1, finishedAt: '2026-11-02T13:07:36Z' })
    expect(kv(sectionByTitle(crashed, '上次退出'))['退出码']).toContain('应用自己退出(退出码 1)')
  })

  it('最大重试只在 on-failure 下说话,0 解释成不限次数', async () => {
    const onFailure = await openModal({ restartPolicy: 'on-failure', restartMaxRetry: 3, state: 'exited', exitCode: 1 })
    expect(kv(sectionByTitle(onFailure, '基本信息'))['最多重试']).toBe('3')

    getContainerInspectMock.mockReset()
    const unlimited = await openModal({ restartPolicy: 'on-failure', restartMaxRetry: 0 })
    expect(kv(sectionByTitle(unlimited, '基本信息'))['最多重试']).toBe('不限次数')

    getContainerInspectMock.mockReset()
    const always = await openModal({ restartPolicy: 'always', restartMaxRetry: 5, state: 'exited', exitCode: 1 })
    expect(kv(sectionByTitle(always, '基本信息'))['最多重试']).toBeUndefined()
  })

  it('「重新读取」只再拉一次详情,不让整屏回到骨架', async () => {
    const wrapper = await openModal({ state: 'running', restartCount: 3, finishedAt: '2026-11-02T13:07:36Z' })
    expect(getContainerInspectMock).toHaveBeenCalledTimes(1)

    getContainerInspectMock.mockResolvedValue(
      inspect({ state: 'restarting', exitCode: 137, oomKilled: true, restartCount: 4, finishedAt: '2026-11-02T13:08:02Z' }),
    )
    await wrapper.find('.ci-reload').trigger('click')
    await flushPromises()

    expect(getContainerInspectMock).toHaveBeenCalledTimes(2)
    expect(wrapper.find('.ci-loading').exists()).toBe(false)
    const rows = kv(sectionByTitle(wrapper, '上次退出'))
    expect(rows['退出码']).toContain('内存超限被 OOM 杀掉')
    expect(rows['累计重启次数']).toBe('4')
  })
})
