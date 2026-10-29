import { describe, it, expect, beforeEach, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import SettingsSystem from './SettingsSystem.vue'

const getProxyImageSetting = vi.fn()
const saveProxyImageSetting = vi.fn()

vi.mock('../../api/reverseProxy', async () => {
  const actual = await vi.importActual<typeof import('../../api/reverseProxy')>('../../api/reverseProxy')
  return {
    ...actual,
    getProxyImageSetting: (...a: unknown[]) => getProxyImageSetting(...a),
    saveProxyImageSetting: (...a: unknown[]) => saveProxyImageSetting(...a),
  }
})

vi.mock('../../api/version', () => ({
  getVersion: vi.fn().mockResolvedValue({ version: 'v0.15.0', commit: 'abc', date: '', platform: '', goVersion: '' }),
  checkUpdate: vi.fn(),
  applyUpdate: vi.fn(),
}))
vi.mock('../../api/retention', () => ({
  getRetentionConfig: vi.fn().mockResolvedValue({
    enabled: true, keepGlobal: 0, keepPerProject: 0, maxAgeDays: 0,
  }),
  setRetentionConfig: vi.fn(),
}))

function setting(over: Partial<{ image: string; effective: string; defaultImage: string }> = {}) {
  return {
    image: '',
    effective: 'ghcr.io/fuuhoo/pipewright-caddy:latest',
    defaultImage: 'ghcr.io/fuuhoo/pipewright-caddy:latest',
    ...over,
  }
}

describe('SettingsSystem — 反代运行环境镜像', () => {
  beforeEach(() => {
    getProxyImageSetting.mockReset().mockResolvedValue(setting())
    saveProxyImageSetting.mockReset()
  })

  it('回显当前生效镜像,而不是把默认值写进输入框', async () => {
    const wrapper = mount(SettingsSystem)
    await flushPromises()

    expect(wrapper.text()).toContain('反代运行环境')
    expect(wrapper.text()).toContain('ghcr.io/fuuhoo/pipewright-caddy:latest')
    // 输入框保持空(显式配置为空),placeholder 才是默认值。
    const input = wrapper.get('input[type="text"]').element as HTMLInputElement
    expect(input.value).toBe('')
    expect(input.placeholder).toContain('ghcr.io/fuuhoo/pipewright-caddy')
  })

  it('保存时把镜像引用提交到 PUT 接口并更新生效值', async () => {
    saveProxyImageSetting.mockResolvedValue(
      setting({ image: 'registry.corp/caddy:1', effective: 'registry.corp/caddy:1' }),
    )
    const wrapper = mount(SettingsSystem)
    await flushPromises()

    await wrapper.get('input[type="text"]').setValue('registry.corp/caddy:1')
    await wrapper.findAll('button').find((b) => b.text().includes('保存镜像'))!.trigger('click')
    await flushPromises()

    expect(saveProxyImageSetting).toHaveBeenCalledWith('registry.corp/caddy:1')
    expect(wrapper.text()).toContain('registry.corp/caddy:1')
  })
})
