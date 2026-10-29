import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ReverseProxyPanel from './ReverseProxyPanel.vue'
import {
  allocateSubdomain,
  getCaddyStatus,
  listProxyRoutes,
  type CaddyStatus,
  type ProxyRoute,
  type ProxyRouteConfig,
} from '../../api/reverseProxy'
import { listDnsProviders, listProviderZones, type DnsProvider } from '../../api/dnsProviders'
import type { ContainerInfo } from '../../api/containers'
import { HttpError } from '../../api/http'
import { useToast } from '../../composables/useToast'

vi.mock('../../api/reverseProxy', async () => {
  const actual = await vi.importActual<typeof import('../../api/reverseProxy')>(
    '../../api/reverseProxy',
  )
  return {
    ...actual,
    listProxyRoutes: vi.fn(),
    createProxyRoute: vi.fn(),
    allocateSubdomain: vi.fn(),
    setProxyRouteEnabled: vi.fn(),
    refreshProxyRoute: vi.fn(),
    deleteProxyRoute: vi.fn(),
    getCaddyStatus: vi.fn(),
    prepareCaddyEnv: vi.fn(),
    reapplyCaddyEnv: vi.fn(),
    removeCaddyEnv: vi.fn(),
  }
})

vi.mock('../../api/dnsProviders', async () => {
  const actual = await vi.importActual<typeof import('../../api/dnsProviders')>(
    '../../api/dnsProviders',
  )
  return { ...actual, listDnsProviders: vi.fn(), listProviderZones: vi.fn() }
})

const runningStatus: CaddyStatus = {
  serverId: 'srv-1',
  installed: true,
  running: true,
  image: 'ghcr.io/fuuhoo/pipewright-caddy:v0.16.0',
  ports: '80,443',
  routeCount: 0,
}

const providers: DnsProvider[] = [
  {
    id: 'p-cloud',
    type: 'cloudflare',
    name: '生产区 Cloudflare',
    credentialConfigured: true,
    createdAt: '2026-10-01T00:00:00Z',
  },
]

const containers: ContainerInfo[] = [
  {
    id: 'c1',
    names: 'web',
    image: 'nginx:latest',
    state: 'running',
    status: 'Up 2 hours',
    ports: '0.0.0.0:8080->80/tcp',
    project: '',
    createdAt: '2026-10-01T00:00:00Z',
  },
]

const routeConfig: ProxyRouteConfig = {
  upstreamKind: 'container',
  aliases: [],
  forceHttps: true,
  hsts: false,
  securityHeaders: false,
  compression: false,
  basicAuthUser: '',
  basicAuthEnabled: false,
  ipAllow: [],
  ipDeny: [],
  redirects: [],
}

function route(over: Partial<ProxyRoute>): ProxyRoute {
  return {
    id: 'r1',
    serverId: 'srv-1',
    domain: 'app-ab12.example.com',
    upstreamContainer: 'web',
    upstreamPort: 8080,
    tlsMode: 'auto',
    enabled: true,
    certStatus: 'pending',
    certDetail: '',
    config: routeConfig,
    createdAt: '2026-10-01T00:00:00Z',
    updatedAt: '2026-10-01T00:00:00Z',
    ...over,
  }
}

type Wrapper = Awaited<ReturnType<typeof mountPanel>>

async function mountPanel() {
  const wrapper = mount(ReverseProxyPanel, {
    props: { serverId: 'srv-1', host: '203.0.113.10', containers },
    global: {
      // 「无提供商」分支里的 RouterLink 需要路由;测试里只当链接占位。
      stubs: { RouterLink: { props: ['to'], template: '<a><slot /></a>' } },
    },
  })
  await flushPromises()
  return wrapper
}

/** 域名来源分段控件(第一个 label-row 的第二个按钮 = 自动子域名)。 */
async function switchDomainMode(wrapper: Wrapper, mode: 'manual' | 'auto') {
  const btns = wrapper.findAll('.rp__label-row')[0].findAll('.rp__seg-btn')
  await btns[mode === 'manual' ? 0 : 1].trigger('click')
  await flushPromises()
}

function providerSelect(wrapper: Wrapper) {
  return wrapper.get('select[aria-label="DNS 提供商"]')
}

function zoneSelect(wrapper: Wrapper) {
  return wrapper.get('select[aria-label="托管域名"]')
}

function selectedZone(wrapper: Wrapper): string {
  return (zoneSelect(wrapper).element as HTMLSelectElement).value
}

async function pickProvider(wrapper: Wrapper, id = 'p-cloud') {
  await providerSelect(wrapper).setValue(id)
  await flushPromises()
}

async function pickContainerUpstream(wrapper: Wrapper, name = 'web', port = '8080') {
  await wrapper.findAll('.rp__field')[1].get('select').setValue(name)
  await wrapper.get('input[inputmode="numeric"]').setValue(port)
}

function submitButton(wrapper: Wrapper) {
  return wrapper.get('.rp__submit')
}

beforeEach(() => {
  vi.mocked(listProxyRoutes).mockResolvedValue([])
  vi.mocked(getCaddyStatus).mockResolvedValue(runningStatus)
  vi.mocked(listDnsProviders).mockResolvedValue(providers)
  vi.mocked(listProviderZones).mockResolvedValue([])
  useToast().clear()
})

describe('ReverseProxyPanel 自动子域名', () => {
  it('默认自填域名,不预拉 DNS 提供商;切到自动才拉,且只拉一次', async () => {
    const wrapper = await mountPanel()

    expect(wrapper.find('select[aria-label="DNS 提供商"]').exists()).toBe(false)
    expect(listDnsProviders).not.toHaveBeenCalled()

    await switchDomainMode(wrapper, 'auto')
    expect(listDnsProviders).toHaveBeenCalledTimes(1)

    await switchDomainMode(wrapper, 'manual')
    await switchDomainMode(wrapper, 'auto')
    expect(listDnsProviders).toHaveBeenCalledTimes(1)
  })

  it('选提供商后实时拉域名清单并渲染成选项;多域名时不替用户猜', async () => {
    vi.mocked(listProviderZones).mockResolvedValue(['example.com', 'lab.example.cn'])
    const wrapper = await mountPanel()

    await switchDomainMode(wrapper, 'auto')
    await pickProvider(wrapper)

    expect(listProviderZones).toHaveBeenCalledWith('p-cloud')
    const options = zoneSelect(wrapper).findAll('option').map((o) => o.text())
    expect(options).toEqual(['选择账号里托管的域名…', 'example.com', 'lab.example.cn'])
    expect(selectedZone(wrapper)).toBe('')
    // 域名未选 → 不能提交。
    expect(submitButton(wrapper).attributes('disabled')).toBeDefined()
  })

  it('单域名账号自动选中,分配请求带上所选域名与容器上游', async () => {
    vi.mocked(listProviderZones).mockResolvedValue(['example.com'])
    vi.mocked(allocateSubdomain).mockResolvedValue(route({}))
    const wrapper = await mountPanel()

    await switchDomainMode(wrapper, 'auto')
    await pickProvider(wrapper)
    expect(selectedZone(wrapper)).toBe('example.com')

    await pickContainerUpstream(wrapper)
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(allocateSubdomain).toHaveBeenCalledWith({
      providerId: 'p-cloud',
      zone: 'example.com',
      serverId: 'srv-1',
      upstreamKind: 'container',
      upstreamContainer: 'web',
      upstreamPort: 8080,
    })
    expect(wrapper.get('.rp__alloc-domain').text()).toContain('app-ab12.example.com')
    expect(wrapper.get('.rp__alloc-title').text()).toContain('子域名已就绪')
    expect(useToast().toasts.value.map((t) => t.title)).toContain('子域名已分配')
  })

  it('自定义地址上游:填 host + 端口即可分配,请求带 upstreamKind=address', async () => {
    vi.mocked(listProviderZones).mockResolvedValue(['example.com'])
    vi.mocked(allocateSubdomain).mockResolvedValue(
      route({
        id: 'r2',
        domain: 'app-cd34.example.com',
        upstreamContainer: '10.0.0.9',
        upstreamPort: 3000,
        config: { ...routeConfig, upstreamKind: 'address' },
      }),
    )
    const wrapper = await mountPanel()

    await switchDomainMode(wrapper, 'auto')
    await pickProvider(wrapper)
    // 上游切到「自定义地址」:容器选择器消失,换成地址输入。
    const kindBtns = wrapper.findAll('.rp__label-row')[1].findAll('.rp__seg-btn')
    await kindBtns[1].trigger('click')

    await wrapper.findAll('.rp__field')[1].get('input').setValue('10.0.0.9')
    await wrapper.get('input[inputmode="numeric"]').setValue('3000')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(allocateSubdomain).toHaveBeenCalledWith({
      providerId: 'p-cloud',
      zone: 'example.com',
      serverId: 'srv-1',
      upstreamKind: 'address',
      upstreamContainer: '10.0.0.9',
      upstreamPort: 3000,
    })
    expect(wrapper.get('.rp__alloc-domain').text()).toContain('app-cd34.example.com')
  })

  it('地址不合法时给出提示且不给分配', async () => {
    vi.mocked(listProviderZones).mockResolvedValue(['example.com'])
    const wrapper = await mountPanel()

    await switchDomainMode(wrapper, 'auto')
    await pickProvider(wrapper)
    const kindBtns = wrapper.findAll('.rp__label-row')[1].findAll('.rp__seg-btn')
    await kindBtns[1].trigger('click')
    await wrapper.findAll('.rp__field')[1].get('input').setValue('not a host')
    await wrapper.get('input[inputmode="numeric"]').setValue('3000')

    expect(wrapper.get('.rp__formhint').text()).toContain('请填写合法地址')
    expect(submitButton(wrapper).attributes('disabled')).toBeDefined()

    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(allocateSubdomain).not.toHaveBeenCalled()
  })

  it('账号里一条托管域名都没有:说清凭据可用但清单为空', async () => {
    vi.mocked(listProviderZones).mockResolvedValue([])
    const wrapper = await mountPanel()

    await switchDomainMode(wrapper, 'auto')
    await pickProvider(wrapper)

    expect(wrapper.get('.rp__hint').text()).toContain('这个账号里还没有托管的域名')
    expect(zoneSelect(wrapper).attributes('disabled')).toBeDefined()
    expect(submitButton(wrapper).attributes('disabled')).toBeDefined()
  })

  it('读域名失败:显示服务端原因并可重试', async () => {
    vi.mocked(listProviderZones)
      .mockRejectedValueOnce(
        new HttpError(401, { code: 'unauthorized', message: '凭据被服务商拒绝' }, 'boom'),
      )
      .mockResolvedValueOnce(['example.com'])
    const wrapper = await mountPanel()

    await switchDomainMode(wrapper, 'auto')
    await pickProvider(wrapper)

    expect(wrapper.get('.rp__hint--err').text()).toContain('凭据被服务商拒绝')

    await wrapper.get('.rp__retry').trigger('click')
    await flushPromises()

    expect(listProviderZones).toHaveBeenCalledTimes(2)
    expect(zoneSelect(wrapper).findAll('option').map((o) => o.text())).toContain('example.com')
  })

  it('连着换提供商只认最后一次结果', async () => {
    vi.mocked(listDnsProviders).mockResolvedValue([
      ...providers,
      { ...providers[0], id: 'p-slow', name: '慢账号' },
    ])
    const slow = new Promise<string[]>((resolve) =>
      setTimeout(() => resolve(['stale.example.com']), 40),
    )
    vi.mocked(listProviderZones)
      .mockImplementationOnce(() => slow)
      .mockImplementationOnce(() => Promise.resolve(['fresh.example.net']))
    const wrapper = await mountPanel()

    await switchDomainMode(wrapper, 'auto')
    // 先选慢的那个,紧接着换成快的:快的先返回,慢的那份属于过期请求,不能覆盖。
    await providerSelect(wrapper).setValue('p-slow')
    await providerSelect(wrapper).setValue('p-cloud')
    await new Promise((r) => setTimeout(r, 80))
    await flushPromises()

    const options = zoneSelect(wrapper).findAll('option').map((o) => o.text())
    expect(options).toContain('fresh.example.net')
    expect(options).not.toContain('stale.example.com')
  })

  it('一个可用提供商都没有:给出配置入口,不显示下拉', async () => {
    vi.mocked(listDnsProviders).mockResolvedValue([])
    const wrapper = await mountPanel()

    await switchDomainMode(wrapper, 'auto')

    expect(wrapper.get('.rp__noprov').text()).toContain('尚未配置 DNS 提供商')
    expect(wrapper.find('select[aria-label="DNS 提供商"]').exists()).toBe(false)
    expect(submitButton(wrapper).attributes('disabled')).toBeDefined()
  })
})
