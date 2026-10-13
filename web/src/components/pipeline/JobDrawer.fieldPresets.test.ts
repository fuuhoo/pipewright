import { describe, it, expect, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import JobDrawer from './JobDrawer.vue'
import type { PipelineJob, PipelineStage } from '../../api/pipeline'

vi.mock('../../api/buildEnvs', () => ({ listEnabledBuildEnvs: vi.fn(async () => []) }))
vi.mock('../../api/configProfiles', () => ({ listEnabledConfigProfiles: vi.fn(async () => []) }))

const stage: PipelineStage = { id: 's1', name: '构建', kind: 'build', jobs: [] }

function scriptJob(config: Record<string, string>): PipelineJob {
  return { id: 'j1', name: '构建', type: 'script', summary: '', config }
}

/** 按字段标题取那一格里的示例芯片(与它的输入框同属一个 .drawer-field)。 */
function fieldChips(wrapper: ReturnType<typeof mount>, label: string) {
  return wrapper
    .findAll('.drawer-field')
    .find((f) => f.find('.drawer-field-label').text() === label)!
    .findAll('.tpl-chip')
}

function lastConfig(wrapper: ReturnType<typeof mount>) {
  return (wrapper.emitted('update')!.at(-1)![0] as Partial<PipelineJob>).config!
}

describe('JobDrawer 字段示例芯片', () => {
  it('单行格点芯片 → 换成该值,其余字段不动', async () => {
    const wrapper = mount(JobDrawer, {
      props: { job: scriptJob({ workDir: 'web', commands: 'npm run build' }), stage },
    })
    await flushPromises()

    await fieldChips(wrapper, '工作目录')[0].trigger('click')

    expect(lastConfig(wrapper).workDir).toBe('.')
    expect(lastConfig(wrapper).commands).toBe('npm run build')
  })

  it('多行格有内容也敢点:往末尾并一行,原内容不覆盖', async () => {
    const wrapper = mount(JobDrawer, {
      props: { job: scriptJob({ cachePaths: 'node_modules' }), stage },
    })
    await flushPromises()

    const maven = fieldChips(wrapper, '依赖缓存目录').find((c) => c.text() === '.m2/repository')!
    await maven.trigger('click')

    expect(lastConfig(wrapper).cachePaths).toBe('node_modules\n.m2/repository')
  })

  it('重复点同一颗不累积;芯片正文完整放在 title 里', async () => {
    const wrapper = mount(JobDrawer, {
      props: { job: scriptJob({ artifactPath: 'dist' }), stage },
    })
    await flushPromises()

    const dist = fieldChips(wrapper, '产物路径').find((c) => c.text() === 'dist')!
    expect(dist.attributes('title')).toBe('dist')
    await dist.trigger('click')
    await dist.trigger('click')

    expect(lastConfig(wrapper).artifactPath).toBe('dist')
  })

  it('执行命令那格只有语言模板,不再挂一份字段示例芯片', async () => {
    const wrapper = mount(JobDrawer, { props: { job: scriptJob({}), stage } })
    await flushPromises()

    const box = wrapper
      .findAll('.drawer-field')
      .find((f) => f.find('.drawer-field-label').text() === '执行命令')!
    // 那一格只有一排芯片,且带行首说明 —— 即只有语言模板,没有再挂一份字段示例
    expect(box.findAll('.tpl-chips')).toHaveLength(1)
    expect(box.find('.tpl-chips-label').exists()).toBe(true)
  })
})
