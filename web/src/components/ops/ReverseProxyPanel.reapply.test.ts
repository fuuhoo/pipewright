import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ReverseProxyPanel from './ReverseProxyPanel.vue'
import {
  getCaddyStatus,
  listProxyRoutes,
  reapplyCaddyEnv,
  type CaddyStatus,
} from '../../api/reverseProxy'
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
    setProxyRouteEnabled: vi.fn(),
    refreshProxyRoute: vi.fn(),
    deleteProxyRoute: vi.fn(),
    getCaddyStatus: vi.fn(),
    prepareCaddyEnv: vi.fn(),
    reapplyCaddyEnv: vi.fn(),
    removeCaddyEnv: vi.fn(),
  }
})

const runningStatus: CaddyStatus = {
  serverId: 'srv-1',
  installed: true,
  running: true,
  image: 'ghcr.io/fuuhoo/pipewright-caddy:v0.16.0',
  ports: '80,443',
  routeCount: 2,
}

async function mountPanel() {
  const wrapper = mount(ReverseProxyPanel, {
    props: { serverId: 'srv-1', host: '203.0.113.10', containers: [] },
  })
  await flushPromises()
  return wrapper
}

beforeEach(() => {
  vi.mocked(listProxyRoutes).mockResolvedValue([])
  vi.mocked(getCaddyStatus).mockResolvedValue(runningStatus)
  vi.mocked(reapplyCaddyEnv).mockResolvedValue(runningStatus)
  useToast().clear()
})

describe('ReverseProxyPanel 重新下发配置', () => {
  it('点按钮把该主机的配置整体重推一次,并提示成功', async () => {
    const wrapper = await mountPanel()

    await wrapper.get('.caddy__reapply').trigger('click')
    await flushPromises()

    expect(reapplyCaddyEnv).toHaveBeenCalledWith('srv-1')
    expect(useToast().toasts.value.map((t) => t.title)).toContain('已重新下发配置')
    // 下发成功后重拉路由列表(远端配置可能改动了证书状态等)。
    expect(listProxyRoutes).toHaveBeenCalledTimes(2)
  })

  it('下发期间按钮禁用并显示进行中,防重复点击', async () => {
    let resolve!: (s: CaddyStatus) => void
    vi.mocked(reapplyCaddyEnv).mockImplementation(
      () => new Promise<CaddyStatus>((r) => { resolve = r }),
    )
    const wrapper = await mountPanel()

    await wrapper.get('.caddy__reapply').trigger('click')
    await flushPromises()

    const btn = wrapper.get('.caddy__reapply')
    expect(btn.attributes('disabled')).toBeDefined()
    expect(btn.text()).toContain('下发中')

    // 进行中再点一次不应重复下发。
    await btn.trigger('click')
    expect(reapplyCaddyEnv).toHaveBeenCalledTimes(1)

    resolve(runningStatus)
    await flushPromises()
    expect(wrapper.get('.caddy__reapply').attributes('disabled')).toBeUndefined()
  })

  it('下发失败时报错,并回读真实环境状态', async () => {
    vi.mocked(reapplyCaddyEnv).mockRejectedValueOnce(
      new HttpError(502, { code: 'apply_failed', message: '下发反代配置失败' }, 'boom'),
    )
    const wrapper = await mountPanel()

    await wrapper.get('.caddy__reapply').trigger('click')
    await flushPromises()

    const errToast = useToast().toasts.value.find((t) => t.title === '重新下发失败')
    expect(errToast).toBeTruthy()
    expect(errToast?.detail).toContain('下发反代配置失败')
    // 初始加载 1 次 + 失败回读 1 次。
    expect(getCaddyStatus).toHaveBeenCalledTimes(2)
  })

  it('反代环境未部署时不显示重新下发', async () => {
    vi.mocked(getCaddyStatus).mockResolvedValue({ ...runningStatus, installed: false, running: false })
    const wrapper = await mountPanel()

    expect(wrapper.find('.caddy__reapply').exists()).toBe(false)
    expect(wrapper.find('.caddy__deploy').exists()).toBe(true)
  })
})
