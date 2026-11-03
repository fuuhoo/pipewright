import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import JobDrawer from './JobDrawer.vue'
import type { PipelineJob, PipelineStage } from '../../api/pipeline'
import type { Credential } from '../../api/credentials'

// 源节点的「仓库 / 分支 / 凭据」是项目配置的只读镜像,改不动;能做的只有让人查证
// 「这次拉的到底是哪份代码」。这个出口只指向站内代码页,且只在仓库地址与项目 ID 都在时才出现 ——
// 点了落到 404 的入口比没有入口更糟,所以宁可不渲染。

vi.mock('../../api/buildEnvs', () => ({ listEnabledBuildEnvs: vi.fn(async () => []) }))
vi.mock('../../api/configProfiles', () => ({ listEnabledConfigProfiles: vi.fn(async () => []) }))

const stage: PipelineStage = { id: 's1', name: '源码', kind: 'source', jobs: [] }

const sourceJob = (config: Record<string, string>): PipelineJob =>
  ({ id: 'jd', name: 'Git 源', type: 'git_source', summary: '', config })

const credentials: Credential[] = [{ id: 'c1', name: '公司读钥', type: 'ssh', username: '', secret: '' } as unknown as Credential]

async function open(config: Record<string, string>, projectId?: string) {
  const wrapper = mount(JobDrawer, {
    props: projectId === undefined
      ? { job: sourceJob(config), stage, credentials }
      : { job: sourceJob(config), stage, credentials, projectId },
  })
  await flushPromises()
  return wrapper
}

const fullConfig: Record<string, string> = {
  repoUrl: 'git@example.com:org/repo.git',
  branch: 'main',
  credentialId: 'c1',
}

describe('JobDrawer · 源节点的看源码出口', () => {
  beforeEach(() => vi.clearAllMocks())

  it('有仓库又有项目 ID 时给出出口,地址由路由表解析', async () => {
    const link = (await open(fullConfig, 'p-7')).get('.readonly-action')
    expect(link.text()).toBe('查看源码')
    expect(link.attributes('href')).toBe('/projects/p-7/code')
    expect(link.attributes('target')).toBe('_blank')
    expect(link.attributes('rel')).toContain('noopener')
  })

  it('只有仓库地址那一格挂出口,分支与凭据两格不跟着挂', async () => {
    const wrapper = await open(fullConfig, 'p-7')
    expect(wrapper.findAll('.readonly-action')).toHaveLength(1)
    expect(wrapper.findAll('.readonly-value')).toHaveLength(3)
  })

  it('项目 ID 缺失时不给出口(没有目标页可指)', async () => {
    const wrapper = await open(fullConfig)
    expect(wrapper.find('.readonly-action').exists()).toBe(false)
    expect(wrapper.find('.readonly-value code').text()).toBe('git@example.com:org/repo.git')
  })

  it('项目未绑仓库时不给出口,空值文案照旧', async () => {
    const wrapper = await open({ branch: 'main' }, 'p-7')
    expect(wrapper.find('.readonly-action').exists()).toBe(false)
    expect(wrapper.text()).toContain('项目未绑定仓库')
  })
})
