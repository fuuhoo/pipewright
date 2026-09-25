import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import JobDrawer from './JobDrawer.vue'
import type { PipelineJob, PipelineStage } from '../../api/pipeline'

// 「docker 部署」节点的表单:两种方式共用一个节点,靠 dockerMode 分派显示哪一组字段。
// 这里盯的是两处回归面:compose 正文必须以多行原文框呈现(它是整份 YAML,单行输入框没法用),
// 以及项目名非法时表单当场点出来 —— 项目名会直接成为目标机上的受管目录名。

vi.mock('../../api/buildEnvs', () => ({ listEnabledBuildEnvs: vi.fn(async () => []) }))
vi.mock('../../api/configProfiles', () => ({ listEnabledConfigProfiles: vi.fn(async () => []) }))

const stage: PipelineStage = { id: 's1', name: '部署', kind: 'deploy', jobs: [] }

function dockerJob(config: Record<string, string>): PipelineJob {
  return { id: 'jd', name: 'Docker 部署', type: 'deploy_docker', summary: '', config }
}

beforeEach(() => vi.clearAllMocks())

describe('JobDrawer · docker 部署节点', () => {
  it('compose 方式露出多行正文框,单容器方式的参数组收起', async () => {
    const wrapper = mount(JobDrawer, { props: { job: dockerJob({ dockerMode: 'compose' }), stage } })
    await flushPromises()

    const area = wrapper.find('textarea[rows="14"]')
    expect(area.exists(), 'compose 正文应是 14 行的原文框').toBe(true)
    expect(wrapper.text()).not.toContain('端口映射')
    expect(wrapper.text()).toContain('项目名')
  })

  it('单容器方式反过来:容器名/端口可见,正文框消失', async () => {
    const wrapper = mount(JobDrawer, { props: { job: dockerJob({ dockerMode: 'run' }), stage } })
    await flushPromises()

    expect(wrapper.find('textarea[rows="14"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('端口映射')
  })

  it('项目名非法 → 字段下方出错误提示,合法则无', async () => {
    const wrapper = mount(JobDrawer, { props: { job: dockerJob({ dockerMode: 'compose', stackName: 'shop/../x' }), stage } })
    await flushPromises()
    const err = wrapper.find('.field-error')
    expect(err.exists()).toBe(true)
    expect(err.text()).toBeTruthy()

    await wrapper.setProps({ job: dockerJob({ dockerMode: 'compose', stackName: 'shop-web' }), stage })
    await flushPromises()
    expect(wrapper.find('.field-error').exists()).toBe(false)
  })
})
