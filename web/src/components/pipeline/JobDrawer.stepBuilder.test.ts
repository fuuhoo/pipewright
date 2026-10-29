import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import JobDrawer from './JobDrawer.vue'
import StepBuilder from './StepBuilder.vue'
import { copyStepLine } from './stepCompile'
import type { PipelineJob, PipelineStage } from '../../api/pipeline'

const stage: PipelineStage = { id: 's1', name: '构建', kind: 'build', jobs: [] }

// 步骤构建器适用于带预置语义的脚本类节点(前端/后端构建/模板),不再用于「自定义脚本」。
function builderJob(config: Record<string, string>): PipelineJob {
  return { id: 'j1', name: '前端构建', type: 'build_frontend', summary: '', config }
}

describe('JobDrawer + StepBuilder integration', () => {
  it('shows the step builder by default for a fresh build node', () => {
    const wrapper = mount(JobDrawer, {
      props: { job: builderJob({ image: 'node:20' }), stage },
    })
    expect(wrapper.findComponent(StepBuilder).exists()).toBe(true)
    // the view switch is present with two tabs
    expect(wrapper.findAll('.view-tab').length).toBe(2)
  })

  // 阅读顺序:先「怎么构建」(步骤 = commands),再「构建时能读到什么」(环境变量导入)。
  // 步骤视图里 commands 格被步骤接管并移出表单,所以步骤块必须插在环境变量组之前。
  it('renders the step builder above the env-import group in steps view', () => {
    const wrapper = mount(JobDrawer, {
      props: { job: builderJob({ image: 'node:20', envSource: 'nacos' }), stage },
    })
    const order = wrapper
      .findAll('.drawer-section .drawer-field')
      .map((n) =>
        n.classes().includes('step-builder-field') ? '<steps>' : n.find('.drawer-field-label').text(),
      )
    const stepsIdx = order.findIndex((label) => label.startsWith('<steps>'))
    const envIdx = order.indexOf('环境变量导入')
    expect(stepsIdx).toBeGreaterThanOrEqual(0)
    expect(envIdx).toBeGreaterThanOrEqual(0)
    expect(stepsIdx).toBeLessThan(envIdx)
    // Nacos 坐标组跟着自己的档位走,不能跑到步骤上面
    expect(envIdx).toBeLessThan(order.indexOf('Nacos 地址'))
  })

  // 「自定义脚本」(script/custom)节点天然写命令文本:默认原始参数、无可视化步骤切换。
  it('uses raw view (no step builder, no view switch) for a 自定义脚本 node', () => {
    const job: PipelineJob = { id: 'js', name: '自定义脚本', type: 'script', summary: '', config: { image: 'node:20' } }
    const wrapper = mount(JobDrawer, { props: { job, stage } })
    expect(wrapper.findComponent(StepBuilder).exists()).toBe(false)
    expect(wrapper.findAll('.view-tab').length).toBe(0)
  })

  it('compiles builder steps into commands via emit("update"), leaving artifactPath alone', async () => {
    const job = builderJob({ image: 'node:20', commands: 'npm ci', artifactPath: 'old/dist' })
    const wrapper = mount(JobDrawer, { props: { job, stage } })

    const builder = wrapper.findComponent(StepBuilder)
    // simulate the builder emitting a compiled fragment (env + cd + cmd)
    builder.vm.$emit('update', {
      commands: "export CI='true'\ncd 'frontend'\nnpm run build",
    })
    await wrapper.vm.$nextTick()

    const events = wrapper.emitted('update')
    expect(events).toBeTruthy()
    const last = events!.at(-1)![0] as Partial<PipelineJob>
    expect(last.config!.commands).toBe("export CI='true'\ncd 'frontend'\nnpm run build")
    // 产物目录不再由步骤回写:步骤更新只动 commands,原有 artifactPath 原样保留
    expect(last.config!.artifactPath).toBe('old/dist')
    // node-level field preserved
    expect(last.config!.image).toBe('node:20')
  })

  it('reparses an existing config into ordered step blocks (artifactPath is a field, not a step)', () => {
    const job = builderJob({
      image: 'node:20',
      commands: "export NODE_ENV='production'\ncd 'frontend'\nnpm ci\nnpm run build",
      artifactPath: 'frontend/dist',
    })
    const wrapper = mount(JobDrawer, { props: { job, stage } })
    const builder = wrapper.findComponent(StepBuilder)
    // 只有 4 条命令行 → 4 个步骤(产物路径不进步骤)
    expect(builder.findAll('.sb-step').length).toBe(4)
    // first step is the env block (compiled from export line)
    expect(builder.findAll('.sb-kind')[0].text()).toBe('设环境变量')
    expect(builder.findAll('.sb-kind')[1].text()).toBe('切目录')
    // 步骤视图下「产物路径」仍是节点的固定字段,值直接可编辑
    const artifactField = wrapper
      .findAll('.drawer-section .drawer-field')
      .find((n) => n.find('.drawer-field-label').exists() && n.find('.drawer-field-label').text() === '产物路径')
    expect(artifactField?.find('textarea').element.value).toBe('frontend/dist')
  })

  it('reparses an entry-mode copy line and switches back to contents via the mode select', async () => {
    const job = builderJob({ image: 'node:20', commands: copyStepLine('frontend/dist', 'out', [], 'entry') })
    const wrapper = mount(JobDrawer, { props: { job, stage } })
    const builder = wrapper.findComponent(StepBuilder)
    const select = builder.find('select.sb-select')
    expect((select.element as HTMLSelectElement).value).toBe('entry')

    await select.setValue('contents')

    const events = wrapper.emitted('update')
    expect(events).toBeTruthy()
    const last = events!.at(-1)![0] as Partial<PipelineJob>
    // 切回 contents = 老语义(源尾斜杠),其它字段原样
    expect(last.config!.commands).toBe(copyStepLine('frontend/dist', 'out', []))
    expect(last.config!.image).toBe('node:20')
  })

  it('defaults to raw view for a templated node (commandTemplate present)', () => {
    const job: PipelineJob = {
      id: 'j2',
      name: '自定义',
      type: 'templated',
      summary: '',
      config: { image: 'node:20', commandTemplate: 'cd {{dir}}\nnpm run build', params: 'dir=frontend' },
    }
    const wrapper = mount(JobDrawer, { props: { job, stage } })
    // step builder hidden because config uses the template render path
    expect(wrapper.findComponent(StepBuilder).exists()).toBe(false)
  })

  it('does not show the step builder for a non-script node (e.g. notify)', () => {
    const job: PipelineJob = { id: 'j3', name: '通知', type: 'notify', summary: '', config: {} }
    const wrapper = mount(JobDrawer, { props: { job, stage } })
    expect(wrapper.findComponent(StepBuilder).exists()).toBe(false)
    expect(wrapper.findAll('.view-tab').length).toBe(0)
  })
})
