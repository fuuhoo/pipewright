import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import JobDrawer from './JobDrawer.vue'
import type { PipelineJob, PipelineStage } from '../../api/pipeline'

// 「部署前指令」这格盯两件事:它只在**产物真的会落到那台机器**的档位出现(镜像档走停旧起新、
// 命令档整条落位链路都不走 —— 出现在那里就是填了没人读),以及切到不出现的档位时旧值一并抹掉,
// 否则存量值会跟着流水线一路走到下一次部署上。

vi.mock('../../api/buildEnvs', () => ({ listEnabledBuildEnvs: vi.fn(async () => []) }))
vi.mock('../../api/configProfiles', () => ({ listEnabledConfigProfiles: vi.fn(async () => []) }))

const stage: PipelineStage = { id: 's1', name: '部署', kind: 'deploy', jobs: [] }

const sshJob = (config: Record<string, string>): PipelineJob =>
  ({ id: 'jd', name: '部署', type: 'deploy_ssh', summary: '', config })
const dockerJob = (config: Record<string, string>): PipelineJob =>
  ({ id: 'jd', name: 'Docker 部署', type: 'deploy_docker', summary: '', config })

const emittedConfig = (wrapper: ReturnType<typeof mount>) =>
  (wrapper.emitted('update')?.at(-1)?.[0] as { config: Record<string, string> }).config

beforeEach(() => vi.clearAllMocks())

describe('JobDrawer · 部署前指令', () => {
  it('产物档位在重启命令之前露出部署前指令', async () => {
    const wrapper = mount(JobDrawer, { props: { job: sshJob({ artifactType: 'file', deployPath: '/srv/app' }), stage } })
    await flushPromises()
    const labels = wrapper.findAll('.drawer-field-label').map((n) => n.text())
    expect(labels).toEqual(expect.arrayContaining(['部署前指令', '重启 / 切换命令']))
    expect(labels.indexOf('部署前指令')).toBeLessThan(labels.indexOf('重启 / 切换命令'))
  })

  it('镜像档位不出现该格,切过去时旧值一并抹掉', async () => {
    const wrapper = mount(JobDrawer, {
      props: { job: sshJob({ artifactType: 'file', deployPath: '/srv/app', preDeployCommand: 'cp -a app.jar app.jar.bak' }), stage },
    })
    await flushPromises()
    expect(wrapper.find('textarea[aria-label="部署前指令"]').exists()).toBe(true)

    await wrapper.get('select[aria-label="部署产物类型"]').setValue('image')
    await flushPromises()
    expect(wrapper.find('textarea[aria-label="部署前指令"]').exists(), '镜像部署没有文件落位这一步').toBe(false)
    expect(emittedConfig(wrapper), '隐形格的旧值不该留在 config 里').not.toHaveProperty('preDeployCommand')
  })

  it('docker 节点只有 compose 方式给这格', async () => {
    const compose = mount(JobDrawer, { props: { job: dockerJob({ dockerMode: 'compose', stackName: 'web' }), stage } })
    await flushPromises()
    expect(compose.find('textarea[aria-label="部署前指令"]').exists()).toBe(true)

    const run = mount(JobDrawer, { props: { job: dockerJob({ dockerMode: 'run', containerName: 'web' }), stage } })
    await flushPromises()
    expect(run.find('textarea[aria-label="部署前指令"]').exists()).toBe(false)
  })
})
