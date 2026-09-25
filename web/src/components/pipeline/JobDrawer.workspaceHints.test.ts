import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import JobDrawer from './JobDrawer.vue'
import { setLocale, type LocaleCode } from '../../i18n'
import type { PipelineJob, PipelineStage } from '../../api/pipeline'

/** 8 套语言都要能渲染这一段:新增提示最容易漏的就是某个 locale 少一个 key。 */
const LOCALES = ['zh-CN', 'zh-TW', 'en', 'de', 'es', 'fr', 'ja', 'ko'] as const

function stagesFixture(): PipelineStage[] {
  const producer: PipelineJob = {
    id: 'japi',
    name: '后端构建',
    type: 'build_backend',
    summary: '',
    config: { artifactPath: 'reporter-ny/build/linux-amd64/server-ny' },
  }
  const current: PipelineJob = {
    id: 'jpkg',
    name: '打包整站',
    type: 'script',
    summary: '',
    config: { workDir: './reporter-ny', commands: 'cp dist bundle/' },
  }
  return [
    { id: 's1', name: '构建', kind: 'build', jobs: [producer] },
    { id: 's2', name: '打包', kind: 'build', jobs: [current], needs: ['s1'] },
  ]
}

function mountHints(locale: LocaleCode) {
  setLocale(locale)
  const stages = stagesFixture()
  return mount(JobDrawer, {
    props: { job: stages[1].jobs[0], stage: stages[1], allStages: stages },
  })
}

describe('JobDrawer · 工作区路径提示', () => {
  it('列出上游产物的绝对落位与本任务工作目录', () => {
    const wrapper = mountHints('zh-CN')
    const block = wrapper.find('.workspace-hints')
    expect(block.exists()).toBe(true)
    const text = block.text()
    expect(text).toContain('/workspace')
    expect(text).toContain('/workspace/reporter-ny/build/linux-amd64/server-ny')
    expect(text).toContain('/workspace/reporter-ny')
    expect(text).toContain('后端构建')
  })

  it('8 套语言全部渲染成功,且没有漏翻(键名不会当文案露出来)', () => {
    for (const locale of LOCALES) {
      const wrapper = mountHints(locale)
      const text = wrapper.find('.workspace-hints').text()
      expect(text, `locale ${locale} 未渲染提示`).toContain('/workspace/reporter-ny/build/linux-amd64/server-ny')
      expect(text, `locale ${locale} 漏键`).not.toMatch(/workspace[A-Z]\w*/)
    }
    setLocale('zh-CN')
  })

  it('源节点 / 通知节点没有工作区,不显示提示', () => {
    setLocale('zh-CN')
    const stage: PipelineStage = {
      id: 's1',
      name: '源',
      kind: 'source',
      jobs: [{ id: 'jsrc', name: 'Gitee 源', type: 'git_source', summary: '', config: {} }],
    }
    const wrapper = mount(JobDrawer, {
      props: { job: stage.jobs[0], stage, allStages: [stage] },
    })
    expect(wrapper.find('.workspace-hints').exists()).toBe(false)
  })
})
