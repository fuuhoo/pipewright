import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import JobDrawer from './JobDrawer.vue'
import type { PipelineJob, PipelineStage } from '../../api/pipeline'
import type { PresetBuildEnv } from '../../api/buildEnvs'
import { COMMAND_TEMPLATES } from './commandTemplates'

// 预置目录 mock:两种语言各一条,用来验「芯片顺序跟着所选环境走」。
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

vi.mock('../../api/buildEnvs', () => ({
  listEnabledBuildEnvs: vi.fn(async () => presetEnvs),
}))
vi.mock('../../api/configProfiles', () => ({
  listEnabledConfigProfiles: vi.fn(async () => []),
}))

const stage: PipelineStage = { id: 's1', name: '构建', kind: 'build', jobs: [] }

function scriptJob(config: Record<string, string>): PipelineJob {
  return { id: 'j1', name: '构建', type: 'script', summary: '', config }
}

function commandsBox(wrapper: ReturnType<typeof mount>) {
  return wrapper.find('textarea[aria-label="执行命令"]')
}

/**
 * 语言模板那一排芯片。芯片的视觉类与「字段示例芯片」共用(pipeline.css 的 .tpl-chip),
 * 区分靠行首说明:只有模板那一排带 .tpl-chips-label。
 */
function templateRow(wrapper: ReturnType<typeof mount>) {
  return wrapper.findAll('.tpl-chips').find((row) => row.find('.tpl-chips-label').exists())!
}

function chipTexts(wrapper: ReturnType<typeof mount>): string[] {
  return templateRow(wrapper)
    .findAll('.tpl-chip:not(.tpl-chip--more)')
    .map((c) => c.text())
}

function moreButton(wrapper: ReturnType<typeof mount>) {
  return templateRow(wrapper).find('.tpl-chip--more')
}

function chipByText(wrapper: ReturnType<typeof mount>, label: string) {
  return templateRow(wrapper).findAll('.tpl-chip').find((c) => c.text() === label)!
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('JobDrawer 执行命令的语言模板', () => {
  it('未选环境时折叠态只给前 4 个,其余收进计数按钮', async () => {
    const wrapper = mount(JobDrawer, { props: { job: scriptJob({}), stage } })
    await flushPromises()

    expect(chipTexts(wrapper)).toEqual(['Node.js · npm', 'Node.js · pnpm', 'Node.js · yarn', 'Java · Maven'])
    // 折叠态固定给 4 个,其余按目录条数收进计数按钮(加模板不用回来改这里)
    expect(moreButton(wrapper).text()).toContain(`+${COMMAND_TEMPLATES.length - 4}`)
    expect(chipByText(wrapper, 'Node.js · npm').attributes('title')).toBe(
      'npm ci --no-audit --no-fund\nnpm run build',
    )
    // 抽屉里其余 .tpl-chips 是各字段的示例芯片,语言模板那排只有一个行首说明
    expect(wrapper.findAll('.tpl-chips-label')).toHaveLength(1)
  })

  it('展开后给出全部语言,再点收起', async () => {
    const wrapper = mount(JobDrawer, { props: { job: scriptJob({}), stage } })
    await flushPromises()

    await moreButton(wrapper).trigger('click')
    expect(chipTexts(wrapper)).toEqual(COMMAND_TEMPLATES.map((x) => x.label))
    await moreButton(wrapper).trigger('click')
    expect(chipTexts(wrapper)).toHaveLength(4)
  })

  it('空框点芯片 → 直接填入该语言的命令', async () => {
    const wrapper = mount(JobDrawer, { props: { job: scriptJob({ image: 'node:20-alpine' }), stage } })
    await flushPromises()

    const npm = chipByText(wrapper, 'Node.js · npm')
    await npm.trigger('click')

    const last = wrapper.emitted('update')!.at(-1)![0] as Partial<PipelineJob>
    expect(last.config!.commands).toBe('npm ci --no-audit --no-fund\nnpm run build')
    expect(commandsBox(wrapper).element).toHaveProperty('value', last.config!.commands)
  })

  it('已有命令只追加,原内容与其余字段都不动', async () => {
    const wrapper = mount(JobDrawer, {
      props: { job: scriptJob({ commands: 'cd frontend', workDir: 'web' }), stage },
    })
    await flushPromises()

    await moreButton(wrapper).trigger('click')
    const pnpm = chipByText(wrapper, 'Node.js · pnpm')
    await pnpm.trigger('click')

    const last = wrapper.emitted('update')!.at(-1)![0] as Partial<PipelineJob>
    expect(last.config!.commands).toBe(
      'cd frontend\ncorepack enable\npnpm install --frozen-lockfile\npnpm run build',
    )
    expect(last.config!.workDir).toBe('web')
  })

  it('选中环境语言的芯片前置', async () => {
    const wrapper = mount(JobDrawer, { props: { job: scriptJob({}), stage } })
    await flushPromises()

    const envSelect = wrapper
      .findAll('select.drawer-select')
      .find((s) => s.text().includes('Java 21'))!
    await envSelect.setValue('env-java21')
    await flushPromises()

    expect(chipTexts(wrapper)).toEqual(['Java · Maven', 'Java · Gradle'])
  })

  it('vfox 模板直接写 vfox use:不再带 activate/heredoc 样板(1.0.13 镜像自带包装层)', async () => {
    const vfox = COMMAND_TEMPLATES.filter((x) => x.language === 'vfox')
    expect(vfox.map((x) => x.id)).toEqual(['vfox', 'vfox-java'])
    for (const tpl of vfox) {
      expect(tpl.commands).not.toContain('eval "$(vfox activate bash)"')
      expect(tpl.commands).not.toContain('bash <<')
      expect(tpl.commands).toContain('vfox use ')
    }
    expect(vfox[0].commands).toContain('vfox use nodejs@22')
    expect(vfox[1].commands).toContain('vfox use java@17')
  })
})

// 「构建」任务的产物档默认开可视化步骤视图(命令框被步骤列表顶掉),所以芯片也必须在那一侧出现,
// 否则最常见的场景反而看不到模板。
describe('JobDrawer 可视化步骤视图的模板芯片', () => {
  const buildJob: PipelineJob = {
    id: 'j2',
    name: '前端构建',
    type: 'build',
    summary: '',
    config: { artifactType: 'file', buildEnvId: 'env-node20', commands: 'cd web\npnpm install' },
  }

  it('步骤视图同样给芯片,点击把每行落成新的命令步骤', async () => {
    const wrapper = mount(JobDrawer, { props: { job: buildJob, stage } })
    await flushPromises()

    expect(wrapper.find('.step-builder').exists()).toBe(true)
    const before = wrapper.findAll('.sb-step').length
    const npm = chipByText(wrapper, 'Node.js · npm')
    await npm.trigger('click')
    await flushPromises()

    expect(wrapper.findAll('.sb-step')).toHaveLength(before + 2)
    // 原有命令按步骤视图的编译口径回写(cd 参数会被引号包住),模板行原样追加在后面
    const last = wrapper.emitted('update')!.at(-1)![0] as Partial<PipelineJob>
    expect(last.config!.commands).toBe(
      "cd 'web'\npnpm install\nnpm ci --no-audit --no-fund\nnpm run build",
    )
  })
})
