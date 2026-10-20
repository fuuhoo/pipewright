import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import type { Project } from '../../api/projects'
import TriggerRunModal from './TriggerRunModal.vue'

const triggerManual = vi.fn()
const listRefs      = vi.fn()
const listCommits   = vi.fn()
const getParameters = vi.fn()
const validateParamValues = vi.fn()

vi.mock('../../api/runs', () => ({ triggerManual: (a: string, b: unknown) => triggerManual(a, b) }))
vi.mock('../../api/refs', () => ({
  listRefs: (a: string) => listRefs(a),
  listCommits: (a: string, b: string, c: number) => listCommits(a, b, c),
}))
vi.mock('../../api/parameters', () => ({
  getParameters: (a: string) => getParameters(a),
  validateParamValues: (defs: unknown, values: Record<string, string>) => validateParamValues(defs, values),
}))

function project(over: Partial<Project> = {}): Project {
  return {
    id: 'p-1',
    name: 'go图形报告',
    repoUrl: 'https://gitlab.example.com/team/report.git',
    defaultBranch: 'main',
    credentialId: 'c-1',
    credentialName: '演示凭据名',
    pacEnabled: false,
    prStatusEnabled: false,
    groupId: '',
    lastRunStatus: null,
    targetServers: [],
    updatedAt: new Date().toISOString(),
    ...over,
  } as Project
}

function mountModal(props: Partial<{ open: boolean; project: Project | null }> = {}) {
  return mount(TriggerRunModal, {
    props: { open: true, project: project(), ...props },
    global: { stubs: { teleport: true, TypedRunParams: true, RunParamsEditor: true } },
  })
}

beforeEach(() => {
  triggerManual.mockReset().mockResolvedValue({ id: 'run-9' })
  listRefs.mockReset().mockResolvedValue({ branches: [{ name: 'main' }, { name: 'release' }], tags: [] })
  listCommits.mockReset().mockResolvedValue([])
  getParameters.mockReset().mockResolvedValue([])
  validateParamValues.mockReset().mockReturnValue('')
})

describe('TriggerRunModal', () => {
  it('prefills the project default branch and runs on submit', async () => {
    const wrapper = mountModal()
    await flushPromises()

    expect((wrapper.get('#trigger-branch').element as HTMLInputElement).value).toBe('main')

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(triggerManual).toHaveBeenCalledWith('p-1', { branch: 'main' })
    expect(wrapper.emitted('triggered')?.[0]).toEqual(['run-9'])
    expect(wrapper.emitted('update:open')?.[0]).toEqual([false])
  })

  it('leaves branch out when the field is cleared so the server resolves it', async () => {
    const wrapper = mountModal()
    await flushPromises()

    await wrapper.get('#trigger-branch').setValue('')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(triggerManual).toHaveBeenCalledWith('p-1', {})
  })

  it('hides branch and commit for a repo-less (发布 only) project', async () => {
    const wrapper = mountModal({ project: project({ repoUrl: '' }) })
    await flushPromises()

    expect(wrapper.find('#trigger-branch').exists()).toBe(false)
    expect(wrapper.find('#trigger-commit').exists()).toBe(false)
    // 无仓库时列分支只会 400,不该发起
    expect(listRefs).not.toHaveBeenCalled()

    // 分支框藏了,默认分支仍照旧随请求提交(与抽组件前的行为一致)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(triggerManual).toHaveBeenCalledWith('p-1', { branch: 'main' })
  })

  it('keeps the dialog open and shows the error when the run request fails', async () => {
    triggerManual.mockRejectedValueOnce(new Error('boom'))
    const wrapper = mountModal()
    await flushPromises()

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(wrapper.get('[role="alert"]').text()).toContain('触发失败')
    expect(wrapper.emitted('triggered')).toBeUndefined()
  })

  it('blocks submit on typed parameter validation errors', async () => {
    getParameters.mockResolvedValue([{ key: 'TAG', type: 'string', required: true, defaultValue: '', enumValues: [], description: '' }])
    validateParamValues.mockReturnValue('缺少必填参数 TAG')
    const wrapper = mountModal()
    await flushPromises()

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(validateParamValues).toHaveBeenCalled()
    expect(triggerManual).not.toHaveBeenCalled()
    expect(wrapper.get('[role="alert"]').text()).toContain('缺少必填参数 TAG')
  })
})
