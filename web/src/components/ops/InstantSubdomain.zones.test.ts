import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import InstantSubdomain from './InstantSubdomain.vue'
import { listDnsProviders, listProviderZones, type DnsProvider } from '../../api/dnsProviders'
import { allocateSubdomain, type ProxyRoute } from '../../api/reverseProxy'
import type { ContainerInfo } from '../../api/containers'
import { HttpError } from '../../api/http'

vi.mock('../../api/dnsProviders', async () => {
  const actual = await vi.importActual<typeof import('../../api/dnsProviders')>('../../api/dnsProviders')
  return { ...actual, listDnsProviders: vi.fn(), listProviderZones: vi.fn() }
})
vi.mock('../../api/reverseProxy', () => ({ allocateSubdomain: vi.fn() }))

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
    createdAt: '2026-10-01T00:00:00Z',
  },
]

async function openModal() {
  const wrapper = mount(InstantSubdomain, {
    props: { serverId: 'srv-1', containers },
    global: {
      // Teleport 打桩后弹窗留在 wrapper 内可查;RouterLink 只在「无提供商」分支出现。
      stubs: { teleport: true, RouterLink: { template: '<a><slot /></a>' } },
    },
  })
  await flushPromises()
  await wrapper.get('.isub__cta').trigger('click')
  return wrapper
}

/** 弹窗里的两个下拉:0 = 提供商,1 = 托管域名。 */
function zoneSelect(wrapper: Awaited<ReturnType<typeof openModal>>) {
  return wrapper.findAll('.field select')[1]
}

/** 读取 select 当前选中值(jsdom 里 value 只在 HTMLSelectElement 上)。 */
function selectedValue(wrapper: Awaited<ReturnType<typeof openModal>>): string {
  return (zoneSelect(wrapper).element as HTMLSelectElement).value
}

async function pickProvider(wrapper: Awaited<ReturnType<typeof openModal>>, id = 'p-cloud') {
  await wrapper.findAll('.field select')[0].setValue(id)
  await flushPromises()
}

beforeEach(() => {
  vi.mocked(listDnsProviders).mockResolvedValue(providers)
  vi.mocked(listProviderZones).mockResolvedValue([])
  vi.mocked(allocateSubdomain).mockResolvedValue({} as ProxyRoute)
})

describe('InstantSubdomain 托管域名选择', () => {
  it('选提供商后实时拉域名清单并渲染成选项', async () => {
    vi.mocked(listProviderZones).mockResolvedValue(['example.com', 'lab.example.cn'])
    const wrapper = await openModal()

    await pickProvider(wrapper)

    expect(listProviderZones).toHaveBeenCalledWith('p-cloud')
    const options = zoneSelect(wrapper).findAll('option').map((o) => o.text())
    expect(options).toEqual(['选择账号里托管的域名…', 'example.com', 'lab.example.cn'])
    // 多域名时不替用户猜,必须自己选一个。
    expect(selectedValue(wrapper)).toBe('')
  })

  it('单域名账号自动选中,分配请求带上所选域名', async () => {
    vi.mocked(listProviderZones).mockResolvedValue(['example.com'])
    vi.mocked(allocateSubdomain).mockResolvedValue({
      id: 'r1',
      serverId: 'srv-1',
      domain: 'app-ab12.example.com',
      upstreamContainer: 'web',
      upstreamPort: 8080,
    } as ProxyRoute)
    const wrapper = await openModal()

    await pickProvider(wrapper)
    expect(selectedValue(wrapper)).toBe('example.com')

    await wrapper.findAll('.field select')[2].setValue('web')
    await wrapper.findAll('.field input')[0].setValue('8080')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(allocateSubdomain).toHaveBeenCalledWith({
      providerId: 'p-cloud',
      zone: 'example.com',
      serverId: 'srv-1',
      upstreamContainer: 'web',
      upstreamPort: 8080,
    })
    expect(wrapper.get('.celebrate__domain').text()).toContain('app-ab12.example.com')
  })

  it('没选域名时不给分配', async () => {
    vi.mocked(listProviderZones).mockResolvedValue(['example.com', 'lab.example.cn'])
    const wrapper = await openModal()

    await pickProvider(wrapper)
    await wrapper.findAll('.field select')[2].setValue('web')
    await wrapper.findAll('.field input')[0].setValue('8080')

    expect(wrapper.get('.btn-primary').attributes('disabled')).toBeDefined()
    expect(allocateSubdomain).not.toHaveBeenCalled()
  })

  it('账号里一条托管域名都没有:说清凭据可用但清单为空', async () => {
    vi.mocked(listProviderZones).mockResolvedValue([])
    const wrapper = await openModal()

    await pickProvider(wrapper)

    expect(wrapper.get('.field-hint').text()).toContain('这个账号里还没有托管的域名')
    expect(zoneSelect(wrapper).attributes('disabled')).toBeDefined()
  })

  it('读域名失败:显示服务端原因并可重试', async () => {
    vi.mocked(listProviderZones)
      .mockRejectedValueOnce(new HttpError(401, { code: 'unauthorized', message: '凭据被服务商拒绝' }, 'boom'))
      .mockResolvedValueOnce(['example.com'])
    const wrapper = await openModal()

    await pickProvider(wrapper)

    expect(wrapper.get('.field-error').text()).toContain('凭据被服务商拒绝')

    await wrapper.get('.field-retry').trigger('click')
    await flushPromises()

    expect(listProviderZones).toHaveBeenCalledTimes(2)
    expect(zoneSelect(wrapper).findAll('option').map((o) => o.text())).toContain('example.com')
  })

  it('连着换提供商只认最后一次结果', async () => {
    vi.mocked(listDnsProviders).mockResolvedValue([
      ...providers,
      { ...providers[0], id: 'p-slow', name: '慢账号' },
    ])
    const slow = new Promise<string[]>((resolve) => setTimeout(() => resolve(['stale.example.com']), 40))
    vi.mocked(listProviderZones)
      .mockImplementationOnce(() => slow)
      .mockImplementationOnce(() => Promise.resolve(['fresh.example.net']))
    const wrapper = await openModal()

    // 先选慢的那个,紧接着换成快的:快的先返回,慢的那份属于过期请求,不能覆盖。
    await wrapper.findAll('.field select')[0].setValue('p-slow')
    await wrapper.findAll('.field select')[0].setValue('p-cloud')
    await new Promise((r) => setTimeout(r, 80))
    await flushPromises()

    const options = zoneSelect(wrapper).findAll('option').map((o) => o.text())
    expect(options).toContain('fresh.example.net')
    expect(options).not.toContain('stale.example.com')
  })
})
