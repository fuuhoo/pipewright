import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import type { Project } from '../../api/projects'
import type { Group } from '../../api/groups'
import type { Service } from '../../api/services'
import { HttpError } from '../../api/http'
import ServiceBatchRun from './ServiceBatchRun.vue'

const triggerManual = vi.fn()

vi.mock('../../api/runs', () => ({
  triggerManual: (id: string, input: unknown) => triggerManual(id, input),
}))

function project(over: Partial<Project> = {}): Project {
  return {
    id: 'p-1',
    name: '订单网关',
    repoUrl: 'https://gitlab.example.com/team/gateway.git',
    defaultBranch: 'main',
    credentialId: 'c-1',
    credentialName: '',
    groupId: '',
    serviceId: 's-1',
    serviceName: '订单中心',
    lastRunStatus: null,
    targetServers: [],
    updatedAt: new Date().toISOString(),
    ...over,
  } as Project
}

function group(over: Partial<Group> = {}): Group {
  return {
    id: 'g-pay',
    name: '支付平台',
    description: '',
    visibility: 'private',
    ownerId: 'u-1',
    ownerName: '',
    members: [],
    projectCount: 1,
    serverCount: 0,
    variableGroupCount: 0,
    canManage: true,
    createdAt: '',
    updatedAt: '',
    ...over,
  } as Group
}

const service: Service = {
  id: 's-1',
  name: '订单中心',
  description: '',
  projectCount: 2,
  createdAt: '',
  updatedAt: '',
}

const PROJECTS: Project[] = [
  project(),
  project({ id: 'p-2', name: '订单履约', groupId: 'g-pay', defaultBranch: 'release/1' }),
  // 别的服务 / 没归属的项目都不该进候选
  project({ id: 'p-3', name: '风控', serviceId: 's-2', serviceName: '风控中台' }),
  project({ id: 'p-4', name: '散工', serviceId: '' }),
]

function mountPanel(over: Record<string, unknown> = {}) {
  return mount(ServiceBatchRun, {
    props: { service, projects: PROJECTS, groups: [group()], ...over },
  })
}

describe('ServiceBatchRun', () => {
  beforeEach(() => {
    triggerManual.mockReset()
  })

  it('候选只收本服务的项目,默认全勾并报出条数', () => {
    const wrapper = mountPanel()
    const rows = wrapper.findAll('.sbr-row')

    expect(rows).toHaveLength(2)
    expect(rows.map((r) => r.find('.sbr-name').text())).toEqual(['订单网关', '订单履约'])
    expect(wrapper.find('.sbr-count').text()).toBe('已选 2 / 2 个项目')
    // 一行未归组、一行有组名:归组是筛选轴,回显必须看得见
    expect(rows[0].find('.sbr-group').text()).toBe('未归组')
    expect(rows[1].find('.sbr-group').text()).toBe('支付平台')
    expect(wrapper.find('button.app-btn--primary').text()).toBe('执行 2 个项目')
  })

  it('换分组后候选收窄,勾选跟着重置', async () => {
    const wrapper = mountPanel()
    await wrapper.find('.sbr-select').setValue('g-pay')

    expect(wrapper.findAll('.sbr-row')).toHaveLength(1)
    expect(wrapper.find('.sbr-name').text()).toBe('订单履约')
    expect(wrapper.find('.sbr-count').text()).toBe('已选 1 / 1 个项目')

    await wrapper.find('.sbr-select').setValue('__ungrouped__')
    expect(wrapper.findAll('.sbr-row')).toHaveLength(1)
    expect(wrapper.find('.sbr-name').text()).toBe('订单网关')
  })

  it('全不选就没有可执行的项目,按钮禁用', async () => {
    const wrapper = mountPanel()
    await wrapper.find('.sbr-all input').setValue(false)

    expect(wrapper.find('.sbr-count').text()).toBe('已选 0 / 2 个项目')
    expect(wrapper.find('button.app-btn--primary').attributes('disabled')).toBeDefined()
  })

  it('按勾中的项目逐个触发,分支一律不发(留给服务端取默认分支)', async () => {
    triggerManual.mockImplementation(async (id: string) => ({ id: `r-${id}`, status: 'queued' }))
    const wrapper = mountPanel()
    await wrapper.findAll('.sbr-row input')[1].setValue(false)

    await wrapper.find('button.app-btn--primary').trigger('click')
    await flushPromises()

    expect(triggerManual.mock.calls).toEqual([['p-1', {}]])
    expect(wrapper.find('.sbr-outcome--ok').text()).toContain('已触发')
    expect(wrapper.find('.sbr-progress').text()).toBe('完成:成功 1 个,失败 0 个')
  })

  it('「查看运行」把刚建出的运行 ID 交给调用方', async () => {
    triggerManual.mockImplementation(async (id: string) => ({ id: `r-${id}`, status: 'queued' }))
    const wrapper = mountPanel()
    await wrapper.find('button.app-btn--primary').trigger('click')
    await flushPromises()

    await wrapper.find('.sbr-link').trigger('click')
    expect(wrapper.emitted('open-run')?.[0]).toEqual(['r-p-1'])
  })

  it('一条失败不拦后面的项目,失败原因按状态码各自说清', async () => {
    triggerManual
      .mockRejectedValueOnce(new HttpError(403, { code: 'forbidden', message: 'forbidden' }, 'forbidden'))
      .mockRejectedValueOnce(new HttpError(422, { code: 'invalid_parameter', message: '缺少必填参数 BUILD_TAG' }, 'bad'))
    const wrapper = mountPanel()

    await wrapper.find('button.app-btn--primary').trigger('click')
    await flushPromises()

    expect(triggerManual).toHaveBeenCalledTimes(2)
    const fails = wrapper.findAll('.sbr-outcome--fail').map((n) => n.text())
    expect(fails).toEqual(['没有触发权限', '缺少必填参数 BUILD_TAG'])
    expect(wrapper.find('.sbr-progress').text()).toBe('完成:成功 0 个,失败 2 个')
  })

  it('断网(status 0)说成连不上服务器,而不是一个空错误', async () => {
    triggerManual.mockRejectedValue(new HttpError(0, null, 'network error'))
    const wrapper = mountPanel()

    await wrapper.find('button.app-btn--primary').trigger('click')
    await flushPromises()

    expect(wrapper.find('.sbr-outcome--fail').text()).toBe('无法连接到服务器')
  })

  it('当前分组下没有该项目时给出空态,不提供可点的执行', () => {
    const wrapper = mountPanel({ projects: [project({ id: 'p-3', name: '风控', serviceId: 's-2' })] })

    expect(wrapper.find('.sbr-empty').text()).toBe('该服务在当前分组下没有可见项目。')
    expect(wrapper.find('button.app-btn--primary').attributes('disabled')).toBeDefined()
  })
})
