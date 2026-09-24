import { describe, it, expect, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import JobDrawer from './JobDrawer.vue'
import type { PipelineJob, PipelineStage } from '../../api/pipeline'

// 预置目录接口在挂载时会请求,单测里一律 mock 掉(与本用例无关)。
vi.mock('../../api/buildEnvs', () => ({ listEnabledBuildEnvs: vi.fn(async () => []) }))
vi.mock('../../api/configProfiles', () => ({ listEnabledConfigProfiles: vi.fn(async () => []) }))

function job(id: string, type: string, config: Record<string, string> = {}, needs?: string[]): PipelineJob {
  return { id, name: id, type, summary: '', config, needs }
}

const buildStage: PipelineStage = { id: 'sb', name: '构建', kind: 'build', jobs: [job('japi', 'script'), job('jweb', 'script')] }
const deployStage: PipelineStage = { id: 'sd', name: '部署', kind: 'deploy', jobs: [job('jdep', 'deploy_ssh')] }
const stages: PipelineStage[] = [buildStage, deployStage]

function deployDrawer(config: Record<string, string> = {}) {
  return mount(JobDrawer, {
    props: { job: job('jdep', 'deploy_ssh', config), stage: deployStage, allStages: stages },
  })
}

/** 取「产物来源任务」那个控件(按 aria-label 定位,顺序变了也不受影响)。 */
function sourceSelect(wrapper: ReturnType<typeof deployDrawer>) {
  return wrapper.get('select[aria-label="产物来源任务"]')
}

function sourceValue(wrapper: ReturnType<typeof deployDrawer>) {
  return (sourceSelect(wrapper).element as HTMLSelectElement).value
}

describe('JobDrawer 部署节点的产物来源任务', () => {
  it('列出上游阶段的构建任务,值存任务 ID', () => {
    const wrapper = deployDrawer()
    const opts = sourceSelect(wrapper).findAll('option')
    expect(opts.map((o) => o.attributes('value'))).toEqual(['', 'japi', 'jweb'])
    // 任务名单独看不出是哪条并行分支,分组标题要给出阶段名。
    expect(wrapper.get('optgroup').attributes('label')).toBe('构建')
  })

  it('选中后把任务 ID 写进 config.artifactFrom', async () => {
    const wrapper = deployDrawer()
    await sourceSelect(wrapper).setValue('jweb')
    const patches = wrapper.emitted('update')
    expect(patches).toBeTruthy()
    const last = patches![patches!.length - 1][0] as { config?: Record<string, string> }
    expect(last.config?.artifactFrom).toBe('jweb')
  })

  it('已保存的值回显为选中项', () => {
    const wrapper = deployDrawer({ artifactFrom: 'japi' })
    expect(sourceValue(wrapper)).toBe('japi')
  })

  it('来源任务被删时显式提示重选,而不是静默回到「自动」', () => {
    const wrapper = deployDrawer({ artifactFrom: 'gone' })
    expect(sourceValue(wrapper)).toBe('gone')
    expect(sourceSelect(wrapper).findAll('option').some((o) => o.text().includes('已不存在'))).toBe(true)
  })

  it('命令型部署没有产物可挑,不渲染该字段', () => {
    const wrapper = deployDrawer({ artifactType: 'command', restartCommand: 'nginx -s reload' })
    expect(wrapper.find('select[aria-label="产物来源任务"]').exists()).toBe(false)
  })
})
