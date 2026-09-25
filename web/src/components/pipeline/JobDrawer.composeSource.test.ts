import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import JobDrawer from './JobDrawer.vue'
import type { PipelineJob, PipelineStage } from '../../api/pipeline'

// compose 部署的「正文来源」:粘贴 YAML ↔ 引用仓库文件。两条路在机器上做的事一样,
// 但节点里存的东西完全不同 —— 粘贴存正文(改节点即改部署),引用仓库只存路径
// (运行时按本次 commit 现读,改完合入即生效)。表单同时只该露出当前来源的那一项,
// 且切回来时另一来源的报错不该留着(否则「明明填对了却保存不了」)。

vi.mock('../../api/buildEnvs', () => ({ listEnabledBuildEnvs: vi.fn(async () => []) }))
vi.mock('../../api/configProfiles', () => ({ listEnabledConfigProfiles: vi.fn(async () => []) }))

const stage: PipelineStage = { id: 's1', name: '部署', kind: 'deploy', jobs: [] }

function composeJob(config: Record<string, string>): PipelineJob {
  // 落点是必填项(后端同一口径会拒),不给一台的话每个用例都会先撞上「还没选目标主机」,
  // 而不是它要测的那一项。
  return { id: 'jd', name: 'Compose 部署', type: 'deploy_docker', summary: '', config: { serverIds: 'srv-1', ...config } }
}

beforeEach(() => vi.clearAllMocks())

// 正文来源是 select,仓库内路径是文本框,粘贴正文是 14 行原文框 —— 三者互斥出现即是判据。
function errorsOf(wrapper: ReturnType<typeof mount>): string[] {
  return wrapper.findAll('.field-error').map((n) => n.text())
}

describe('JobDrawer · compose 正文来源', () => {
  it('缺省来源 = 粘贴:只给正文框,不出现仓库路径', async () => {
    const wrapper = mount(JobDrawer, { props: { job: composeJob({ dockerMode: 'compose' }), stage } })
    await flushPromises()

    expect(wrapper.find('textarea[rows="14"]').exists(), '粘贴来源应有 compose 正文框').toBe(true)
    expect(wrapper.text()).not.toContain('仓库内路径')
    expect(errorsOf(wrapper)).toEqual([])
  })

  it('切到「引用仓库文件」:路径框出现、正文框收起', async () => {
    const wrapper = mount(JobDrawer, {
      props: { job: composeJob({ dockerMode: 'compose', composeSource: 'repo' }), stage },
    })
    await flushPromises()

    expect(wrapper.text()).toContain('仓库内路径')
    expect(wrapper.find('textarea[rows="14"]').exists(), '引用仓库时正文不进节点').toBe(false)
    // 空路径此刻只算「还没写完」,但保存过不去,所以当场就要点出来。
    expect(errorsOf(wrapper).join(' ')).toContain('引用仓库文件时要填仓库里的路径')
  })

  it('仓库路径校验:绝对路径 / 越界 / 无后缀都拒,相对 yml 放行', async () => {
    const cases: [string, boolean][] = [
      ['/srv/shop/docker-compose.yml', true], // 绝对路径:仓库里没有绝对路径这回事
      ['deploy/../secrets.yml', true], // 越出目录
      ['deploy/compose.txt', true], // 非 yaml 后缀
      ['deploy//compose.yml', true], // 空路径段
      ['deploy/docker-compose.yml', false],
      ['docker-compose.yaml', false], // 仓库根下的文件也合法
    ]
    for (const [path, wantError] of cases) {
      const wrapper = mount(JobDrawer, {
        props: { job: composeJob({ dockerMode: 'compose', composeSource: 'repo', composeFile: path }), stage },
      })
      await flushPromises()
      const errs = errorsOf(wrapper)
      if (wantError) expect(errs.length, `${path} 非法,应报错`).toBeGreaterThan(0)
      else expect(errs, `${path} 合法,不该报错`).toEqual([])
      wrapper.unmount()
    }
  })

  it('来源切回粘贴:仓库路径的残留报错跟着消失', async () => {
    const wrapper = mount(JobDrawer, {
      props: { job: composeJob({ dockerMode: 'compose', composeSource: 'repo', composeFile: '' }), stage },
    })
    await flushPromises()
    expect(errorsOf(wrapper).length, '前置条件:repo 来源且路径为空时应报错').toBeGreaterThan(0)

    await wrapper.setProps({
      job: composeJob({ dockerMode: 'compose', composeSource: 'paste', composeFile: '', composeYaml: 'services: {}' }),
      stage,
    })
    await flushPromises()
    expect(errorsOf(wrapper), '不可见字段的错误不该拦住保存').toEqual([])
  })

  it('粘贴来源:正文超限报错,空白正文不算错(交给后端拒)', async () => {
    const wrapper = mount(JobDrawer, {
      props: { job: composeJob({ dockerMode: 'compose', composeYaml: 'x'.repeat(600 * 1024) }), stage },
    })
    await flushPromises()
    expect(errorsOf(wrapper).join(' ')).toContain('512 KiB')

    await wrapper.setProps({ job: composeJob({ dockerMode: 'compose', composeYaml: '   ' }), stage })
    await flushPromises()
    expect(errorsOf(wrapper)).toEqual([])
  })
})

function t_fail(wrapper: ReturnType<typeof mount>, path: string, why: string) {
  expect.fail(`${path}: ${why},实际报错=${JSON.stringify(errorsOf(wrapper))};节点文本=${wrapper.text().slice(0, 0)}`)
}
