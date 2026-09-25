import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import JobDrawer from './JobDrawer.vue'
import type { PipelineJob, PipelineStage } from '../../api/pipeline'

// K8s 发布节点的「清单来源」三档(只换镜像 / 引用仓库文件 / 粘贴清单)。
// 它决定下面哪几格有效:只换镜像靠负载名寻址,按清单 apply 时落点是清单自己的 kind + metadata。
// 后端 validateDeployK8s 在有清单时**要求**命名空间为空(两处各填一个必有一个是假的),
// 所以那几格在该档下根本不该出现 —— 出现就是一个「填了必报错」的陷阱格。

vi.mock('../../api/buildEnvs', () => ({ listEnabledBuildEnvs: vi.fn(async () => []) }))
vi.mock('../../api/configProfiles', () => ({ listEnabledConfigProfiles: vi.fn(async () => []) }))

// 集群清单是 props 传进来的(JobDrawer 自己不取),所以这里给一个 id 就够,无需 mock 接口。
const stage: PipelineStage = {
  id: 's1',
  name: '发布',
  kind: 'deploy',
  jobs: [],
}

function k8sJob(config: Record<string, string>): PipelineJob {
  return { id: 'jk', name: 'K8s 发布', type: 'deploy_k8s', summary: '', config }
}

function errorsOf(wrapper: ReturnType<typeof mount>): string {
  return wrapper.findAll('.field-error').map((n) => n.text()).join(' ')
}

// 只看**格名**,不看整页文本:集群那一格的说明里本就写着「默认命名空间」,
// 用 wrapper.text() 判存在与否会先被自己的提示词绊倒。
function labelsOf(wrapper: ReturnType<typeof mount>): string[] {
  return wrapper.findAll('.drawer-field-label').map((n) => n.text())
}

beforeEach(() => vi.clearAllMocks())

describe('JobDrawer · k8s 清单来源', () => {
  it('只换镜像(含老节点的缺省):寻址格齐全,无清单输入格', async () => {
    // 老节点没存 manifestSource,读出来要等于「只换镜像」那套格。
    const modes: Array<Record<string, string>> = [{}, { manifestSource: 'none' }]
    for (const cfg of modes) {
      const wrapper = mount(JobDrawer, {
        props: { job: k8sJob({ clusterId: 'c1', workloadName: 'api', ...cfg }), stage },
      })
      await flushPromises()
      const labels = labelsOf(wrapper)
      expect(labels).toContain('命名空间')
      expect(labels).toContain('负载名称')
      expect(labels).toContain('容器名')
      expect(labels).not.toContain('仓库内路径')
      expect(wrapper.find('textarea[rows="14"]').exists()).toBe(false)
      expect(errorsOf(wrapper)).toBe('')
      wrapper.unmount()
    }
  })

  it('只换镜像时负载名必填,留空当场点出来', async () => {
    const wrapper = mount(JobDrawer, { props: { job: k8sJob({ clusterId: 'c1' }), stage } })
    await flushPromises()
    expect(errorsOf(wrapper)).toContain('请填写负载名称')
  })

  it('引用仓库文件:路径格出现,寻址格(命名空间/类型/负载名/容器名)全部收起', async () => {
    const wrapper = mount(JobDrawer, { props: { job: k8sJob({ clusterId: 'c1', manifestSource: 'repo' }), stage } })
    await flushPromises()
    const labels = labelsOf(wrapper)
    expect(labels).toContain('仓库内路径')
    // 有清单时落点全由清单给:这四格填了也是被忽略(命名空间更要被后端拒),所以整项收起。
    for (const gone of ['命名空间', '负载类型', '负载名称', '容器名']) {
      expect(labels, `${gone} 在清单档不该出现`).not.toContain(gone)
    }
    // 负载名收起之后,那份「必填」的拦人规则也不能留在看不见的格上。
    expect(errorsOf(wrapper)).not.toContain('请填写负载名称')
    // 但路径本身此刻还不能空着。
    expect(errorsOf(wrapper)).toContain('引用仓库文件时要填仓库里的路径')
  })

  it('切到清单档时把看不见的命名空间抹掉 —— 否则保存会吃到指向隐形格子的 422', async () => {
    // 后端 validateDeployK8s:有清单时 namespace 必须为空(落点以清单的 metadata.namespace 为准)。
    // 而这一格在清单档是收起的,所以旧值必须由抽屉在回写时抹掉 —— 不能留给用户去清一个界面上没有的框。
    const wrapper = mount(JobDrawer, {
      props: {
        job: k8sJob({ clusterId: 'c1', workloadKind: 'Deployment', workloadName: 'api', namespace: 'prod' }),
        stage,
      },
    })
    await flushPromises()
    await wrapper.get('[aria-label="清单来源"]').setValue('repo')
    await flushPromises()

    const patches = wrapper.emitted('update')
    expect(patches, '切档应回写一次 config').toBeTruthy()
    const cfg = (patches!.at(-1) as [{ config: Record<string, string> }])[0].config
    expect(cfg.namespace, '清单档不该带着只换镜像那一档的命名空间').toBeUndefined()
    expect(cfg.manifestSource).toBe('repo')
    wrapper.unmount()
  })

  it('清单路径校验与 compose 同一条规则(绝对路径 / 越界 / 无后缀都拒)', async () => {
    const cases: [string, boolean][] = [
      ['/etc/deployment.yaml', true],
      ['k8s/../secret.yaml', true],
      ['k8s/deploy.txt', true],
      ['k8s/deployment.yaml', false],
    ]
    for (const [path, wantError] of cases) {
      const wrapper = mount(JobDrawer, {
        props: { job: k8sJob({ clusterId: 'c1', manifestSource: 'repo', manifestFile: path }), stage },
      })
      await flushPromises()
      if (wantError) expect(errorsOf(wrapper), `${path} 非法,应报错`).not.toBe('')
      else expect(errorsOf(wrapper), `${path} 合法,不该报错`).toBe('')
      wrapper.unmount()
    }
  })

  it('粘贴清单:正文格必填且有 64 KiB 上限', async () => {
    const wrapper = mount(JobDrawer, { props: { job: k8sJob({ clusterId: 'c1', manifestSource: 'paste' }), stage } })
    await flushPromises()
    expect(wrapper.find('textarea[rows="14"]').exists(), '粘贴档应有 14 行清单正文框').toBe(true)
    expect(errorsOf(wrapper)).toContain('粘贴清单时正文不能为空')

    await wrapper.setProps({ job: k8sJob({ clusterId: 'c1', manifestSource: 'paste', manifestYaml: 'x'.repeat(65 * 1024) }), stage })
    await flushPromises()
    expect(errorsOf(wrapper)).toContain('64 KiB')

    await wrapper.setProps({ job: k8sJob({ clusterId: 'c1', manifestSource: 'paste', manifestYaml: 'kind: Deployment\n' }), stage })
    await flushPromises()
    expect(errorsOf(wrapper)).toBe('')
  })

  it('切档不留残影:仓库 → 只换镜像后,路径报错跟着消失', async () => {
    const wrapper = mount(JobDrawer, {
      props: { job: k8sJob({ clusterId: 'c1', manifestSource: 'repo', manifestFile: '', workloadName: 'api' }), stage },
    })
    await flushPromises()
    expect(errorsOf(wrapper)).toContain('引用仓库文件时要填仓库里的路径')

    await wrapper.setProps({ job: k8sJob({ clusterId: 'c1', manifestSource: 'none', manifestFile: '', workloadName: 'api' }), stage })
    await flushPromises()
    expect(errorsOf(wrapper), '不可见字段的错误不该拦住保存').toBe('')
  })
})
