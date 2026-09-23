import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import JobDrawer from './JobDrawer.vue'
import type { PipelineJob, PipelineStage } from '../../api/pipeline'
import type { PresetBuildEnv } from '../../api/buildEnvs'
import type { ConfigProfile } from '../../api/configProfiles'

// 预置目录接口 mock:构建环境 / 配置资源(隔离 UI 单测与后端)。
const presetEnvs: PresetBuildEnv[] = [
  {
    id: 'env-node20',
    language: 'node',
    version: '20',
    displayName: 'Node.js 20',
    description: '',
    sourceType: 'official',
    image: 'node:20-alpine',
    imageCheckStatus: 'available',
    sortOrder: 10,
  },
  {
    id: 'env-java21',
    language: 'java',
    version: '21',
    displayName: 'Java 21',
    description: '',
    sourceType: 'official',
    image: 'eclipse-temurin:21-jdk-alpine',
    imageCheckStatus: 'available',
    sortOrder: 30,
  },
]
const presetProfiles: ConfigProfile[] = [
  {
    id: 'cp-npm',
    language: 'node',
    configType: 'npmrc',
    name: '.npmrc',
    targetPath: '/root/.npmrc',
    filePath: '',
    isDefault: true,
    isBuiltin: true,
    description: '',
    enabled: true,
    createdBy: '',
    createdAt: '',
    updatedAt: '',
  },
]

vi.mock('../../api/buildEnvs', () => ({
  listEnabledBuildEnvs: vi.fn(async () => presetEnvs),
}))
vi.mock('../../api/configProfiles', () => ({
  listEnabledConfigProfiles: vi.fn(async () => presetProfiles),
}))

const stage: PipelineStage = { id: 's1', name: '构建', kind: 'build', jobs: [] }

function scriptJob(config: Record<string, string>): PipelineJob {
  return { id: 'j1', name: '构建', type: 'script', summary: '', config }
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('JobDrawer 预置构建环境选择器', () => {
  it('渲染 buildenv 下拉,数据源为已启用预置目录', async () => {
    const wrapper = mount(JobDrawer, { props: { job: scriptJob({}), stage } })
    await flushPromises()

    const selects = wrapper.findAll('select.drawer-select')
    const envSelect = selects.find((s) => s.text().includes('Node.js 20'))
    expect(envSelect).toBeTruthy()
    expect(envSelect!.text()).toContain('node:20-alpine')
  })

  it('选中预置 → 写 buildEnvId 并镜像 image 旧键(桥接现有运行时)', async () => {
    const wrapper = mount(JobDrawer, { props: { job: scriptJob({}), stage } })
    await flushPromises()

    const selects = wrapper.findAll('select.drawer-select')
    const envSelect = selects.find((s) => s.text().includes('Node.js 20'))!
    await envSelect.setValue('env-node20')

    const events = wrapper.emitted('update')
    expect(events).toBeTruthy()
    const last = events!.at(-1)![0] as Partial<PipelineJob>
    expect(last.config!.buildEnvId).toBe('env-node20')
    expect(last.config!.image).toBe('node:20-alpine')
  })

  it('旧配置反查:只有 image 且命中目录 → 下拉回显对应预置', async () => {
    const wrapper = mount(JobDrawer, {
      props: { job: scriptJob({ image: 'node:20-alpine', commands: 'npm ci' }), stage },
    })
    await flushPromises()

    const selects = wrapper.findAll('select.drawer-select')
    const envSelect = selects.find((s) => s.text().includes('Node.js 20'))!
    expect((envSelect.element as HTMLSelectElement).value).toBe('env-node20')
  })

  it('旧配置不在目录 → 出现告警占位且不强写 buildEnvId', async () => {
    const wrapper = mount(JobDrawer, {
      props: { job: scriptJob({ image: 'evil.registry.io/x:1' }), stage },
    })
    await flushPromises()

    expect(wrapper.text()).toContain('不在预置目录')
    const events = wrapper.emitted('update')
    // 仅 hydrate 不产生 update;无 buildEnvId 被强写
    expect(events ?? []).toHaveLength(0)
  })

  it('build_image toolchain 选中预置 → 镜像写 toolchainLanguage/Version', async () => {
    const job: PipelineJob = {
      id: 'j2',
      name: '构建镜像',
      type: 'build_image',
      summary: '',
      config: { buildModel: 'toolchain' },
    }
    const wrapper = mount(JobDrawer, { props: { job, stage } })
    await flushPromises()

    const selects = wrapper.findAll('select.drawer-select')
    const envSelect = selects.find((s) => s.text().includes('Node.js 20'))!
    await envSelect.setValue('env-node20')

    const last = wrapper.emitted('update')!.at(-1)![0] as Partial<PipelineJob>
    expect(last.config!.buildEnvId).toBe('env-node20')
    expect(last.config!.toolchainLanguage).toBe('node')
    expect(last.config!.toolchainVersion).toBe('20-alpine')
    // toolchain 节点不写 image 键(运行时按 language + version 查预置目录)
    expect(last.config!.image).toBeUndefined()
  })

  it('托管镜像键不出现在「原始参数」,但改选环境时值原样保留', async () => {
    const wrapper = mount(JobDrawer, {
      props: { job: scriptJob({ image: 'node:20-alpine', commands: 'npm ci', workDir: 'app' }), stage },
    })
    await flushPromises()

    // 高级区只装真正未知的键:workDir 归 schema 管,image 归构建环境控件管 → 都不该出现
    expect(wrapper.find('.kv-row').exists()).toBe(false)
    expect(wrapper.find('.advanced-count').exists()).toBe(false)

    const envSelect = wrapper
      .findAll('select.drawer-select')
      .find((s) => s.text().includes('Node.js 20'))!
    await envSelect.setValue('env-java21')

    const last = wrapper.emitted('update')!.at(-1)![0] as Partial<PipelineJob>
    expect(last.config!.image).toBe('eclipse-temurin:21-jdk-alpine')
    expect(last.config!.workDir).toBe('app')
    expect(last.config!.commands).toBe('npm ci')
  })

  it('在「原始参数」手填受管键 → 该行被撤销,镜像写不进去', async () => {
    const wrapper = mount(JobDrawer, {
      props: { job: scriptJob({ commands: 'npm ci', myFlag: 'x' }), stage },
    })
    await flushPromises()

    // 新增一行(不动既有的 myFlag 行),把它的键名改成受管的 image
    await wrapper.find('.kv-add-btn').trigger('click')
    const rows = wrapper.findAll('.kv-row')
    const keyInput = rows[rows.length - 1].findAll('input')[0]
    await keyInput.setValue('image')
    await keyInput.trigger('blur')
    await flushPromises()

    expect(wrapper.findAll('.kv-row')).toHaveLength(1)
    const last = wrapper.emitted('update')!.at(-1)![0] as Partial<PipelineJob>
    expect(last.config!.image).toBeUndefined()
    expect(last.config!.myFlag).toBe('x') // 真正的自定义键不受影响
  })
})

describe('JobDrawer 配置资源多选', () => {
  it('按所选环境语言过滤候选项,勾选写逗号分隔 ID', async () => {
    const wrapper = mount(JobDrawer, { props: { job: scriptJob({}), stage } })
    await flushPromises()

    // 选 node 环境 → 出现 node 语言的 .npmrc 配置
    const selects = wrapper.findAll('select.drawer-select')
    const envSelect = selects.find((s) => s.text().includes('Node.js 20'))!
    await envSelect.setValue('env-node20')
    await flushPromises()

    const checkbox = wrapper.find('input[type="checkbox"][aria-label=".npmrc"]')
    expect(checkbox.exists()).toBe(true)
    await checkbox.setValue(true)

    const last = wrapper.emitted('update')!.at(-1)![0] as Partial<PipelineJob>
    expect(last.config!.configProfileIds).toBe('cp-npm')
  })
})

describe('JobDrawer 推送镜像节点(R5:registry 来自环境,节点只读回显)', () => {
  const envs = [
    {
      id: 'e1',
      name: '生产',
      targetServerIds: [],
      envVars: [],
      imageRegistry: { type: 'harbor' as const, url: 'harbor.local/team' },
    },
    {
      id: 'e2',
      name: '测试',
      targetServerIds: [],
      envVars: [],
      imageRegistry: { type: '' as const, url: '' },
    },
  ]

  it('旧的四个假字段既不进表单也不进「原始参数」,并回写抹掉', async () => {
    const job: PipelineJob = {
      id: 'j3',
      name: '推送',
      type: 'push_image',
      summary: '',
      config: { registry: 'harbor.local/team', imageName: 'app', tag: 'v1', credentialId: 'c-1' },
    }
    const wrapper = mount(JobDrawer, { props: { job, stage, environments: envs } })
    await flushPromises()

    const last = wrapper.emitted('update')!.at(-1)![0] as Partial<PipelineJob>
    expect(last.config).toEqual({})
    // 值只可能来自「环境」回显,不再有输入框承载它
    const typed = wrapper.findAll('input, textarea, select')
      .some((el) => (el.element as HTMLInputElement).value === 'harbor.local/team')
    expect(typed).toBe(false)
  })

  it('只读回显各环境绑定的镜像仓,未绑定的标出来', async () => {
    const job: PipelineJob = { id: 'j4', name: '推送', type: 'push_image', summary: '', config: {} }
    const wrapper = mount(JobDrawer, { props: { job, stage, environments: envs } })
    await flushPromises()

    const rows = wrapper.findAll('.push-target-row')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('生产')
    expect(rows[0].text()).toContain('harbor.local/team')
    expect(rows[1].text()).toContain('未绑定镜像仓')
  })
})
