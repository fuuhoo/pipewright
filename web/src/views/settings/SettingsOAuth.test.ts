import { describe, it, expect, beforeEach, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import SettingsOAuth from './SettingsOAuth.vue'
import { HttpError } from '../../api/http'

// ─── module mocks ──────────────────────────────────────────────────────────

const getOAuthApps = vi.fn()
const saveOAuthApp = vi.fn()
const getOAuthLoginApps = vi.fn()
const saveOAuthLoginApp = vi.fn()
vi.mock('../../api/oauth', async () => {
  const actual = await vi.importActual<typeof import('../../api/oauth')>('../../api/oauth')
  return {
    ...actual,
    getOAuthApps: (...args: unknown[]) => getOAuthApps(...args),
    saveOAuthApp: (...args: unknown[]) => saveOAuthApp(...args),
    getOAuthLoginApps: (...args: unknown[]) => getOAuthLoginApps(...args),
    saveOAuthLoginApp: (...args: unknown[]) => saveOAuthLoginApp(...args),
  }
})

const toastSuccess = vi.fn()
const toastError = vi.fn()
vi.mock('../../composables/useToast', () => ({
  useToast: () => ({ success: toastSuccess, error: toastError }),
}))

function makeApp(over: Partial<import('../../api/oauth').OAuthApp> = {}) {
  return {
    provider: 'github' as const,
    clientId: 'existing-id',
    baseUrl: '',
    enabled: true,
    maskedSecret: '••••a91f',
    configured: true,
    updatedAt: '2026-05-30T00:00:00Z',
    ...over,
  }
}

/** 四个平台改成 tab 后一次只摊开一档:断言某档的表单前得先把它的 tab 点亮。 */
async function openTab(wrapper: Awaited<ReturnType<typeof mount>>, pid: string) {
  await wrapper.get(`[data-provider="${pid}"]`).trigger('click')
  await flushPromises()
}

/** 用途切换在 tab 之上:切到「平台登录」面板才会出现白名单与登录回调地址。 */
async function openPurpose(wrapper: Awaited<ReturnType<typeof mount>>, purpose: 'vault' | 'login') {
  await wrapper.get(`[data-purpose="${purpose}"]`).trigger('click')
  await flushPromises()
}

function makeLoginApp(over: Partial<import('../../api/oauth').OAuthLoginApp> = {}) {
  return {
    provider: 'github' as const,
    clientId: 'login-client-id',
    baseUrl: '',
    allowedDomains: ['corp.example.com'],
    enabled: false,
    maskedSecret: '••••9d2c',
    configured: true,
    updatedAt: null,
    callbackUrl: 'http://localhost:3000/api/auth/oauth/github/callback',
    ...over,
  }
}

describe('SettingsOAuth', () => {
  beforeEach(() => {
    getOAuthApps.mockReset()
    saveOAuthApp.mockReset()
    getOAuthLoginApps.mockReset()
    saveOAuthLoginApp.mockReset()
    toastSuccess.mockReset()
    toastError.mockReset()
    // 两套用途一起拉(Promise.all),所以两边都要有默认值:缺一个整页会落到错误态。
    getOAuthApps.mockResolvedValue([])
    getOAuthLoginApps.mockResolvedValue([])
  })

  it('renders one tab per provider and mounts a single panel', async () => {
    getOAuthApps.mockResolvedValue([makeApp()])
    const wrapper = mount(SettingsOAuth)
    await flushPromises()

    // four provider tabs (gitee/github/gitlab/custom)
    expect(wrapper.findAll('[data-provider]')).toHaveLength(4)
    const panels = wrapper.findAll('[data-panel]')
    expect(panels).toHaveLength(1)
    expect(panels[0].attributes('data-panel')).toBe('gitee')
    // 未点开的平台不进 DOM:四张卡竖排时页面要滚两屏才填完一档
    expect(wrapper.find('#oauth-clientid-github').exists()).toBe(false)
  })

  it('marks the active tab and hydrates its saved values', async () => {
    getOAuthApps.mockResolvedValue([makeApp()])
    const wrapper = mount(SettingsOAuth)
    await flushPromises()
    await openTab(wrapper, 'github')

    expect(wrapper.get('[data-provider="github"]').attributes('aria-selected')).toBe('true')
    expect(wrapper.get('[data-panel]').attributes('data-panel')).toBe('github')
    const githubInput = wrapper.get('#oauth-clientid-github')
      .element as HTMLInputElement
    expect(githubInput.value).toBe('existing-id')
  })

  it('shows each provider status on its tab so the enabled one is visible at a glance', async () => {
    getOAuthApps.mockResolvedValue([
      makeApp({ provider: 'github' }),
      makeApp({ provider: 'gitlab', enabled: false }),
    ])
    const wrapper = mount(SettingsOAuth)
    await flushPromises()

    expect(wrapper.get('[data-provider="github"]').find('.tab-dot--on').exists()).toBe(true)
    expect(wrapper.get('[data-provider="gitlab"]').find('.tab-dot--idle').exists()).toBe(true)
    expect(wrapper.get('[data-provider="gitee"]').find('.tab-dot--off').exists()).toBe(true)
  })

  it('never echoes the plaintext secret; shows masked hint + keep-existing placeholder', async () => {
    getOAuthApps.mockResolvedValue([makeApp()])
    const wrapper = mount(SettingsOAuth)
    await flushPromises()
    await openTab(wrapper, 'github')

    const secret = wrapper.get('#oauth-secret-github').element as HTMLInputElement
    expect(secret.type).toBe('password')
    expect(secret.value).toBe('') // never pre-filled
    expect(secret.placeholder).toContain('留空保留已存')
    // masked echo is shown, plaintext is not
    expect(wrapper.text()).toContain('••••a91f')
  })

  it('only the self-hosted (custom) tab exposes a Base URL field', async () => {
    getOAuthApps.mockResolvedValue([])
    const wrapper = mount(SettingsOAuth)
    await flushPromises()

    expect(wrapper.find('#oauth-baseurl-github').exists()).toBe(false)
    await openTab(wrapper, 'custom')
    expect(wrapper.find('#oauth-baseurl-custom').exists()).toBe(true)
  })

  it('keeps unsaved edits when switching away and back', async () => {
    getOAuthApps.mockResolvedValue([])
    const wrapper = mount(SettingsOAuth)
    await flushPromises()

    await wrapper.get('#oauth-clientid-gitee').setValue('draft-id')
    await openTab(wrapper, 'gitlab')
    await openTab(wrapper, 'gitee')

    expect((wrapper.get('#oauth-clientid-gitee').element as HTMLInputElement).value).toBe('draft-id')
  })

  it('moves the selection with the arrow keys (tablist keyboard contract)', async () => {
    getOAuthApps.mockResolvedValue([])
    // attachTo:焦点要真的落在按钮上才测得到,而 jsdom 不给脱离 document 的元素设 activeElement。
    const wrapper = mount(SettingsOAuth, { attachTo: document.body })
    await flushPromises()

    await wrapper.get('[role="tablist"]').trigger('keydown', { key: 'ArrowRight' })
    await flushPromises()

    expect(wrapper.get('[data-panel]').attributes('data-panel')).toBe('github')
    expect(document.activeElement?.id).toBe('oauth-tab-github')
    wrapper.unmount()
  })

  it('blocks save when enabling a provider with no Client ID', async () => {
    getOAuthApps.mockResolvedValue([
      makeApp({ provider: 'gitee', clientId: '', enabled: false, configured: false, maskedSecret: '' }),
    ])
    const wrapper = mount(SettingsOAuth)
    await flushPromises()

    const panel = wrapper.get('[data-panel="gitee"]')
    // turn the enable toggle on
    await panel.get('[role="switch"]').trigger('click')
    await panel.get('form').trigger('submit')
    await flushPromises()

    expect(saveOAuthApp).not.toHaveBeenCalled()
    expect(panel.text()).toContain('启用时 Client ID 必填')
  })

  it('omits clientSecret on save when left blank (keep existing) and toasts success', async () => {
    getOAuthApps.mockResolvedValue([makeApp()])
    saveOAuthApp.mockResolvedValue(makeApp())
    const wrapper = mount(SettingsOAuth)
    await flushPromises()
    await openTab(wrapper, 'github')

    await wrapper.get('[data-panel="github"] form').trigger('submit')
    await flushPromises()

    expect(saveOAuthApp).toHaveBeenCalledTimes(1)
    const [provider, payload] = saveOAuthApp.mock.calls[0]
    expect(provider).toBe('github')
    expect(payload).not.toHaveProperty('clientSecret')
    expect(payload).toMatchObject({ clientId: 'existing-id', enabled: true })
    expect(toastSuccess).toHaveBeenCalledWith('OAuth 应用已保存', { detail: 'GitHub' })
  })

  it('sends clientSecret when the user enters a new one (rotation)', async () => {
    getOAuthApps.mockResolvedValue([makeApp()])
    saveOAuthApp.mockResolvedValue(makeApp())
    const wrapper = mount(SettingsOAuth)
    await flushPromises()
    await openTab(wrapper, 'github')

    await wrapper.get('#oauth-secret-github').setValue('new-secret')
    await wrapper.get('[data-panel="github"] form').trigger('submit')
    await flushPromises()

    const payload = saveOAuthApp.mock.calls[0][1]
    expect(payload.clientSecret).toBe('new-secret')
  })

  // ─── 用途切换:仓库凭据 / 平台登录 ─────────────────────────────────────────

  it('defaults to the vault purpose and keeps the login-only fields out of the DOM', async () => {
    const wrapper = mount(SettingsOAuth)
    await flushPromises()

    expect(wrapper.get('[data-purpose="vault"]').attributes('aria-pressed')).toBe('true')
    expect(wrapper.get('[data-purpose="login"]').attributes('aria-pressed')).toBe('false')
    expect(wrapper.find('#oauth-domains-gitee').exists()).toBe(false)
    // vault 的回调地址契约里没有,按固定路径现算给管理员抄
    expect((wrapper.get('#oauth-callback-gitee').element as HTMLInputElement).value).toBe(
      `${window.location.origin}/api/oauth/gitee/callback`,
    )
  })

  it('pulls both purposes on load so an unreachable login list cannot be saved over', async () => {
    getOAuthLoginApps.mockRejectedValue(new Error('boom'))
    const wrapper = mount(SettingsOAuth)
    await flushPromises()

    // 拉不到就不进 ready:否则白名单框显示成空的,照着保存一次就真把已配的清空了
    expect(wrapper.find('[data-panel]').exists()).toBe(false)
    expect(wrapper.get('.banner--error').text()).toContain('加载')
  })

  it('switching to the login purpose re-hydrates the panel from the login app', async () => {
    // 服务端按它看到的请求 origin 现算(反代/换端口部署时前端算出来的会是错的),
    // 所以这里给一条与 window.location 不同的地址,验证的是「原样回显」而不是重算。
    getOAuthLoginApps.mockResolvedValue([
      makeLoginApp({ provider: 'gitee', callbackUrl: 'http://10.0.0.9:8080/api/auth/oauth/gitee/callback' }),
    ])
    const wrapper = mount(SettingsOAuth)
    await flushPromises()

    await openPurpose(wrapper, 'login')

    const clientId = wrapper.get('#oauth-clientid-gitee').element as HTMLInputElement
    expect(clientId.value).toBe('login-client-id')
    const domains = wrapper.get('#oauth-domains-gitee').element as HTMLInputElement
    expect(domains.value).toBe('corp.example.com')
    expect((wrapper.get('#oauth-callback-gitee').element as HTMLInputElement).value).toBe(
      'http://10.0.0.9:8080/api/auth/oauth/gitee/callback',
    )
    expect(wrapper.get('[role="switch"]').attributes('aria-label')).toContain('Gitee')
  })

  it('derives the login callback for an unconfigured app so the admin has something to register', async () => {
    const wrapper = mount(SettingsOAuth)
    await flushPromises()

    await openPurpose(wrapper, 'login')

    // 库里没有这一行时服务端给不出 callbackUrl,而管理员正是这时候要去平台侧登记它
    expect((wrapper.get('#oauth-callback-gitee').element as HTMLInputElement).value).toBe(
      `${window.location.origin}/api/auth/oauth/gitee/callback`,
    )
  })

  it('keeps the two purposes as separate cards: status, values and drafts do not leak', async () => {
    getOAuthApps.mockResolvedValue([makeApp({ provider: 'gitee' })])
    const wrapper = mount(SettingsOAuth)
    await flushPromises()

    expect(wrapper.get('[data-provider="gitee"]').find('.tab-dot--on').exists()).toBe(true)
    await openPurpose(wrapper, 'login')
    // 同一档平台在登录用途下还没配过:状态点必须跟着用途走,不能沿用仓库凭据的绿点
    expect(wrapper.get('[data-provider="gitee"]').find('.tab-dot--off').exists()).toBe(true)

    await wrapper.get('#oauth-clientid-gitee').setValue('login-draft')
    await openPurpose(wrapper, 'vault')
    expect((wrapper.get('#oauth-clientid-gitee').element as HTMLInputElement).value).toBe(
      'existing-id',
    )
    await openPurpose(wrapper, 'login')
    expect((wrapper.get('#oauth-clientid-gitee').element as HTMLInputElement).value).toBe(
      'login-draft',
    )
  })

  it('saves the login app with the whitelist always present (empty array clears it)', async () => {
    getOAuthLoginApps.mockResolvedValue([makeLoginApp({ provider: 'gitee', allowedDomains: ['a.com'] })])
    saveOAuthLoginApp.mockResolvedValue(makeLoginApp({ provider: 'gitee', allowedDomains: [] }))
    const wrapper = mount(SettingsOAuth)
    await flushPromises()
    await openPurpose(wrapper, 'login')

    await wrapper.get('#oauth-domains-gitee').setValue('')
    await wrapper.get('[data-panel="gitee"] form').trigger('submit')
    await flushPromises()

    expect(saveOAuthApp).not.toHaveBeenCalled()
    const [provider, payload] = saveOAuthLoginApp.mock.calls[0]
    expect(provider).toBe('gitee')
    // 省略 = 保留已存,空数组 = 清空:漏传会把管理员的白名单留下,这里是显式清空
    expect(payload).toMatchObject({ clientId: 'login-client-id', allowedDomains: [] })
  })

  it('normalizes the whitelist before sending (separators, case, duplicates)', async () => {
    saveOAuthLoginApp.mockResolvedValue(makeLoginApp({ provider: 'gitee' }))
    const wrapper = mount(SettingsOAuth)
    await flushPromises()
    await openPurpose(wrapper, 'login')

    await wrapper.get('#oauth-domains-gitee').setValue('  Corp.example.com ; a.com,, corp.example.com ')
    await wrapper.get('[data-panel="gitee"] form').trigger('submit')
    await flushPromises()

    expect(saveOAuthLoginApp.mock.calls[0][1].allowedDomains).toEqual(['corp.example.com', 'a.com'])
  })

  it('blocks the save on an invalid whitelist entry instead of a silent wipe', async () => {
    const wrapper = mount(SettingsOAuth)
    await flushPromises()
    await openPurpose(wrapper, 'login')

    await wrapper.get('#oauth-domains-gitee').setValue('https://corp.example.com')
    await wrapper.get('[data-panel="gitee"] form').trigger('submit')
    await flushPromises()

    expect(saveOAuthLoginApp).not.toHaveBeenCalled()
    expect(wrapper.get('[data-panel="gitee"]').text()).toContain('邮箱域名白名单含非法项')
  })

  it('maps the backend invalid_domain onto the whitelist field', async () => {
    saveOAuthLoginApp.mockRejectedValue(
      new HttpError(422, { code: 'invalid_domain', message: 'bad domain' }, 'unprocessable'),
    )
    const wrapper = mount(SettingsOAuth)
    await flushPromises()
    await openPurpose(wrapper, 'login')

    await wrapper.get('[data-panel="gitee"] form').trigger('submit')
    await flushPromises()

    expect(wrapper.get('[data-panel="gitee"]').text()).toContain('邮箱域名白名单含非法项')
    expect(toastError).not.toHaveBeenCalled()
  })

  it('relabels the enable toggle per purpose so it does not promise the other feature', async () => {
    const wrapper = mount(SettingsOAuth)
    await flushPromises()

    expect(wrapper.get('.toggle-label').text()).toContain('启用连接')
    await openPurpose(wrapper, 'login')
    expect(wrapper.get('.toggle-label').text()).toContain('启用登录')
  })
})
