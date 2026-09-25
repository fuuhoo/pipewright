import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import JobDrawer from './JobDrawer.vue'
import type { PipelineJob, PipelineStage } from '../../api/pipeline'
import type { Server } from '../../api/servers'

// 「docker 部署」节点的表单:两种方式共用一个节点,靠 dockerMode 分派显示哪一组字段。
// 这里盯的是三处回归面:compose 正文必须以多行原文框呈现(它是整份 YAML,单行输入框没法用),
// 项目名非法时表单当场点出来(它直接成为目标机上的受管目录名),以及落点/策略的搭配 ——
// 分批与蓝绿只在多台之间有意义,单机选它等于没选。

vi.mock('../../api/buildEnvs', () => ({ listEnabledBuildEnvs: vi.fn(async () => []) }))
vi.mock('../../api/configProfiles', () => ({ listEnabledConfigProfiles: vi.fn(async () => []) }))

const stage: PipelineStage = { id: 's1', name: '部署', kind: 'deploy', jobs: [] }

function dockerJob(config: Record<string, string>): PipelineJob {
  return { id: 'jd', name: 'Docker 部署', type: 'deploy_docker', summary: '', config }
}

function host(id: string, name: string): Server {
  return {
    id, name, host: `10.0.0.${id.slice(-1)}`, port: 22, user: 'deploy',
    credentialId: 'c-1', credentialName: 'k', groupId: '', createdAt: '', updatedAt: '',
  }
}

const errorsOf = (wrapper: ReturnType<typeof mount>) => wrapper.findAll('.field-error').map((n) => n.text())

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

  // 策略档位必须与引擎真的实现的编排一致:recreate 后端从来没实现(选它实际就是滚动),
  // 选项撤掉后存量节点仍带着它 —— 读入时归成滚动,否则那一格是个认不出的空值。
  it('部署策略下拉只有引擎真实现的四档,存量 recreate 读成滚动', async () => {
    const wrapper = mount(JobDrawer, { props: { job: dockerJob({ dockerMode: 'run', strategy: 'recreate' }), stage } })
    await flushPromises()
    const sel = wrapper.get<HTMLSelectElement>('select[aria-label="部署策略"]')
    expect(sel.findAll('option').map((o) => o.text())).toEqual([
      '一次性发布(所有主机同时发)',
      '分批发布(首批通过后再发其余)',
      '首批后暂停(确认后才发其余)',
      '蓝绿部署',
    ])
    expect(sel.element.value).toBe('rolling')
  })

  // 首批后暂停与分批共用同一套闸门(要两台以上、要问首批几台),差别只在放行由人点。
  it('选「首批后暂停」单机报错、两台后才问首批台数', async () => {
    const twoHosts = [host('srv-1', 'web-01'), host('srv-2', 'web-02')]
    const wrapper = mount(JobDrawer, {
      props: { job: dockerJob({ dockerMode: 'run', strategy: 'interactive', serverIds: 'srv-1' }), stage, servers: twoHosts },
    })
    await flushPromises()
    expect(errorsOf(wrapper).join(' ')).toContain('至少要选两台主机')
    expect(wrapper.find('[aria-label="首批台数"]').exists(), '分批还不成立时不该问首批几台').toBe(false)

    await wrapper.setProps({
      job: dockerJob({ dockerMode: 'run', strategy: 'interactive', serverIds: 'srv-1,srv-2', canaryCount: '2' }),
      stage,
      servers: twoHosts,
    })
    await flushPromises()
    expect(wrapper.find('[aria-label="首批台数"]').exists(), '两台 + 首批暂停该问首批几台').toBe(true)
    expect(errorsOf(wrapper).join(' ')).toContain('要小于主机总数')

    await wrapper.setProps({
      job: dockerJob({ dockerMode: 'run', strategy: 'interactive', serverIds: 'srv-1,srv-2' }),
      stage,
      servers: twoHosts,
    })
    await flushPromises()
    expect(errorsOf(wrapper), '首批留空 = 1 台,合法').toEqual([])
    expect(wrapper.get<HTMLSelectElement>('select[aria-label="部署策略"]').element.value).toBe('interactive')
  })

  // 下拉要落后端 NormalizeStrategy 认得的串,前端不能自造名字。
  it('从一次性发布切到「首批后暂停」存 interactive', async () => {
    const wrapper = mount(JobDrawer, {
      props: {
        job: dockerJob({ dockerMode: 'run', strategy: 'rolling', serverIds: 'srv-1,srv-2' }),
        stage,
        servers: [host('srv-1', 'web-01'), host('srv-2', 'web-02')],
      },
    })
    await flushPromises()
    await wrapper.get('select[aria-label="部署策略"]').setValue('interactive')
    await flushPromises()
    const last = wrapper.emitted('update')?.at(-1)?.[0] as { config: Record<string, string> }
    expect(last.config.strategy).toBe('interactive')
  })

  // 落点是一批机器:勾选即写入 serverIds(顺序 = 分批的先发顺序),存量单主机键读进来即并入。
  it('目标主机是多选清单,存量 serverId 读进 serverIds', async () => {
    const wrapper = mount(JobDrawer, {
      props: { job: dockerJob({ dockerMode: 'run', serverId: 'srv-1' }), stage, servers: [host('srv-1', 'web-01'), host('srv-2', 'web-02')] },
    })
    await flushPromises()
    const boxes = wrapper.get<HTMLDivElement>('[aria-label="目标主机"]').findAll<HTMLInputElement>('input[type="checkbox"]')
    expect(boxes).toHaveLength(2)
    expect(boxes[0].element.checked, '存量的那台机器该已经勾上').toBe(true)

    await boxes[1].setValue(true)
    await flushPromises()
    const last = wrapper.emitted('update')?.at(-1)?.[0] as { config: Record<string, string> }
    expect(last.config.serverIds).toBe('srv-1,srv-2')
    expect(last.config, '旧键不留在 config 里(否则两份落点会各说各话)').not.toHaveProperty('serverId')
  })

  // 分批/蓝绿是「多台之间怎么排」的编排:只有一台时它与一次性发布做的是同一件事。
  // 后端同一口径会拒保存,所以表单要当场说清,而不是让人选完以为已经生效。
  it('单机选分批当场报错,补到两台才放行并出现首批台数', async () => {
    const wrapper = mount(JobDrawer, {
      props: {
        job: dockerJob({ dockerMode: 'run', strategy: 'canary', serverIds: 'srv-1' }),
        stage,
        servers: [host('srv-1', 'web-01'), host('srv-2', 'web-02')],
      },
    })
    await flushPromises()
    expect(errorsOf(wrapper).join(' ')).toContain('至少要选两台主机')
    expect(wrapper.find('[aria-label="首批台数"]').exists(), '还没成立就不该问首批几台').toBe(false)

    await wrapper.setProps({
      job: dockerJob({ dockerMode: 'run', strategy: 'canary', serverIds: 'srv-1,srv-2' }),
      stage,
      servers: [host('srv-1', 'web-01'), host('srv-2', 'web-02')],
    })
    await flushPromises()
    expect(errorsOf(wrapper), '两台 + 分批:策略本身成立').toEqual([])
    expect(wrapper.text()).toContain('首批台数')

    // 首批把全部机器都发了 = 没有第二批,那一门永远不会到。
    await wrapper.setProps({
      job: dockerJob({ dockerMode: 'run', strategy: 'canary', serverIds: 'srv-1,srv-2', canaryCount: '2' }),
      stage,
      servers: [host('srv-1', 'web-01'), host('srv-2', 'web-02')],
    })
    await flushPromises()
    expect(errorsOf(wrapper).join(' ')).toContain('要小于主机总数')
  })

  // compose 档整份 YAML 交目标机的 compose CLI 编排:那里没有「批次」这个概念。
  it('compose 档不出现策略与首批台数,切档时把旧值一并抹掉', async () => {
    const wrapper = mount(JobDrawer, {
      props: { job: dockerJob({ dockerMode: 'run', strategy: 'canary', canaryCount: '1', serverIds: 'srv-1,srv-2' }), stage },
    })
    await flushPromises()
    expect(wrapper.find('select[aria-label="部署策略"]').exists()).toBe(true)

    await wrapper.setProps({ job: dockerJob({ dockerMode: 'compose', strategy: 'canary', canaryCount: '1', serverIds: 'srv-1,srv-2' }), stage })
    await flushPromises()
    expect(wrapper.find('select[aria-label="部署策略"]').exists(), 'compose 档不该给策略').toBe(false)
    expect(wrapper.find('[aria-label="首批台数"]').exists()).toBe(false)
  })

  it('项目名非法 → 字段下方出错误提示,合法则无', async () => {
    const wrapper = mount(JobDrawer, { props: { job: dockerJob({ dockerMode: 'compose', stackName: 'shop/../x', serverIds: 'srv-1' }), stage } })
    await flushPromises()
    const err = wrapper.find('.field-error')
    expect(err.exists()).toBe(true)
    expect(err.text()).toBeTruthy()

    await wrapper.setProps({ job: dockerJob({ dockerMode: 'compose', stackName: 'shop-web', serverIds: 'srv-1' }), stage })
    await flushPromises()
    expect(wrapper.find('.field-error').exists()).toBe(false)
  })
})
