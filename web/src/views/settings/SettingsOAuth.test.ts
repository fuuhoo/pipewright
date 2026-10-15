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
    // OIDC 六项:git 档位服务端回空串,契约里它们是必填字段。
    discoveryUrl: '',
    authorizeUrl: '',
    tokenUrl: '',
    userinfoUrl: '',
    idClaim: '',
    scopes: '',
    // AgentId 同理:只有企业微信有值,其余档位服务端一律回空串。
    agentId: '',
    ...over,
  }
}

/** 登录档的 OIDC 应用:端点走 discovery,主键声明与 scope 由服务端补默认值。 */
function makeOidcApp(over: Partial<import('../../api/oauth').OAuthLoginApp> = {}) {
  return makeLoginApp({
    provider: 'oidc',
    clientId: 'oidc-client-id',
    callbackUrl: 'http://localhost:3000/api/auth/oauth/oidc/callback',
    discoveryUrl: 'https://sso.example.com/realms/my/.well-known/openid-configuration',
    idClaim: 'sub',
    scopes: 'openid profile email',
    ...over,
  })
}

/**
 * 钉钉 / 飞书 / 企业微信:官方端点写死在后端预设里,配置页只给覆盖项,
 * 所以 discovery 与主键声明这两栏压根不出现;企微另外要 AgentId。
 */
function makePlatformApp(
  provider: 'dingtalk' | 'feishu' | 'wecom',
  over: Partial<import('../../api/oauth').OAuthLoginApp> = {},
) {
  return makeLoginApp({
    provider,
    clientId: provider === 'wecom' ? 'ww1234567890abcdef' : 'platform-client-id',
    callbackUrl: `http://localhost:3000/api/auth/oauth/${provider}/callback`,
    agentId: provider === 'wecom' ? '1000002' : '',
    ...over,
  })
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

  // ─── OIDC 身份源(只挂在「平台登录」档)──────────────────────────────────

  it('offers the OIDC tab only under the login purpose', async () => {
    const wrapper = mount(SettingsOAuth)
    await flushPromises()

    // 仓库凭据复用的是 git 平台的 token 流程,OIDC 在那边没有意义,tab 都不该出现
    expect(wrapper.find('[data-provider="oidc"]').exists()).toBe(false)
    expect(wrapper.findAll('[data-provider]')).toHaveLength(4)

    await openPurpose(wrapper, 'login')
    expect(wrapper.findAll('[data-provider]')).toHaveLength(8)
    expect(wrapper.get('[data-provider="oidc"]').text()).toContain('OIDC')
  })

  it('renders the endpoint group only for OIDC, and says what this version does not verify', async () => {
    const wrapper = mount(SettingsOAuth)
    await flushPromises()
    await openPurpose(wrapper, 'login')

    expect(wrapper.find('#oauth-discovery-gitee').exists()).toBe(false)
    await openTab(wrapper, 'oidc')

    const panel = wrapper.get('[data-panel="oidc"]')
    for (const id of [
      '#oauth-discovery-oidc',
      '#oauth-oidc-authorize-oidc',
      '#oauth-oidc-token-oidc',
      '#oauth-oidc-userinfo-oidc',
      '#oauth-oidc-idclaim-oidc',
      '#oauth-oidc-scopes-oidc',
    ]) {
      expect(panel.find(id).exists(), `OIDC 面板缺少字段 ${id}`).toBe(true)
    }
    // 只读 userinfo、不验 id_token 是这一期的真实边界,得让管理员在保存前就看到
    expect(panel.text()).toContain('这一版不校验 id_token 的签名与 audience')
    expect(wrapper.get('[data-panel]').attributes('data-panel')).toBe('oidc')
  })

  it('falls back to the first git tab when leaving the login purpose with OIDC open', async () => {
    const wrapper = mount(SettingsOAuth)
    await flushPromises()
    await openPurpose(wrapper, 'login')
    await openTab(wrapper, 'oidc')

    await openPurpose(wrapper, 'vault')
    // active 停在 oidc 会渲染一张 vault 档根本不存在的卡:面板得回到 git 平台
    expect(wrapper.get('[data-panel]').attributes('data-panel')).toBe('gitee')
  })

  it('echoes back the endpoints and defaults the server resolved', async () => {
    getOAuthLoginApps.mockResolvedValue([makeOidcApp()])
    const wrapper = mount(SettingsOAuth)
    await flushPromises()
    await openPurpose(wrapper, 'login')
    await openTab(wrapper, 'oidc')

    const value = (id: string) => (wrapper.get(id).element as HTMLInputElement).value
    expect(value('#oauth-discovery-oidc')).toBe(
      'https://sso.example.com/realms/my/.well-known/openid-configuration',
    )
    expect(value('#oauth-oidc-idclaim-oidc')).toBe('sub')
    expect(value('#oauth-oidc-scopes-oidc')).toBe('openid profile email')
    expect(value('#oauth-callback-oidc')).toContain('/api/auth/oauth/oidc/callback')
  })

  it('tells the admin to register at the identity provider, not at a git platform, on the OIDC card', async () => {
    getOAuthLoginApps.mockResolvedValue([makeOidcApp()])
    const wrapper = mount(SettingsOAuth)
    await flushPromises()
    await openPurpose(wrapper, 'login')
    await openTab(wrapper, 'oidc')

    // 两处提示都指向「去哪登记」,写着 git 平台会把人支到错的那个应用页
    expect(wrapper.get('[data-panel="oidc"]').text()).toContain('身份提供方')
    expect((wrapper.get('#oauth-clientid-oidc').element as HTMLInputElement).placeholder).toContain('OIDC 客户端')

    await openTab(wrapper, 'gitee')
    expect(wrapper.get('[data-panel="gitee"]').text()).toContain('git 平台')
  })

  it('sends all six OIDC fields on every login save so an override can be cleared', async () => {
    getOAuthLoginApps.mockResolvedValue([makeOidcApp()])
    saveOAuthLoginApp.mockResolvedValue(makeOidcApp({ discoveryUrl: '' }))
    const wrapper = mount(SettingsOAuth)
    await flushPromises()
    await openPurpose(wrapper, 'login')
    await openTab(wrapper, 'oidc')

    await wrapper.get('#oauth-discovery-oidc').setValue('')
    await wrapper.get('#oauth-oidc-authorize-oidc').setValue('  https://sso.local/authorize  ')
    await wrapper.get('[data-panel="oidc"] form').trigger('submit')
    await flushPromises()

    expect(saveOAuthApp).not.toHaveBeenCalled()
    const [provider, payload] = saveOAuthLoginApp.mock.calls[0]
    expect(provider).toBe('oidc')
    // 整份覆盖:留空即清空,所以清空 discovery 必须显式传空串,漏传会把旧值留下
    expect(payload).toMatchObject({
      discoveryUrl: '',
      authorizeUrl: 'https://sso.local/authorize',
      idClaim: 'sub',
      scopes: 'openid profile email',
    })
  })

  it('sends the OIDC fields empty for a git app so a stray value cannot survive', async () => {
    saveOAuthLoginApp.mockResolvedValue(makeLoginApp({ provider: 'gitee' }))
    const wrapper = mount(SettingsOAuth)
    await flushPromises()
    await openPurpose(wrapper, 'login')

    await wrapper.get('[data-panel="gitee"] form').trigger('submit')
    await flushPromises()

    expect(saveOAuthLoginApp.mock.calls[0][1]).toMatchObject({
      discoveryUrl: '',
      idClaim: '',
      scopes: '',
    })
  })

  it('blocks enabling OIDC without a discovery URL or all three hand-filled endpoints', async () => {
    getOAuthLoginApps.mockResolvedValue([makeOidcApp()])
    const wrapper = mount(SettingsOAuth)
    await flushPromises()
    await openPurpose(wrapper, 'login')
    await openTab(wrapper, 'oidc')

    await wrapper.get('#oauth-discovery-oidc').setValue('')
    await wrapper.get('#oauth-oidc-authorize-oidc').setValue('https://sso.local/authorize')
    await wrapper.get('[data-panel="oidc"]').get('[role="switch"]').trigger('click')
    await wrapper.get('[data-panel="oidc"] form').trigger('submit')
    await flushPromises()

    // 后端保存时真去解析,但明显没凑齐的不必浪费一次往返,也别让管理员以为存上了
    expect(saveOAuthLoginApp).not.toHaveBeenCalled()
    expect(wrapper.get('[data-panel="oidc"]').text()).toContain(
      '要么把授权 / 令牌 / 用户信息三个端点都填上',
    )
  })

  it('accepts three hand-filled endpoints with no discovery URL (isolated IdP)', async () => {
    saveOAuthLoginApp.mockResolvedValue(makeOidcApp({ discoveryUrl: '' }))
    const wrapper = mount(SettingsOAuth)
    await flushPromises()
    await openPurpose(wrapper, 'login')
    await openTab(wrapper, 'oidc')

    await wrapper.get('#oauth-clientid-oidc').setValue('oidc-client-id')
    await wrapper.get('#oauth-secret-oidc').setValue('oidc-client-secret')
    await wrapper.get('#oauth-oidc-authorize-oidc').setValue('https://sso.local/authorize')
    await wrapper.get('#oauth-oidc-token-oidc').setValue('https://sso.local/token')
    await wrapper.get('#oauth-oidc-userinfo-oidc').setValue('https://sso.local/userinfo')
    await wrapper.get('[data-panel="oidc"]').get('[role="switch"]').trigger('click')
    await wrapper.get('[data-panel="oidc"] form').trigger('submit')
    await flushPromises()

    expect(saveOAuthLoginApp).toHaveBeenCalledTimes(1)
    expect(saveOAuthLoginApp.mock.calls[0][1]).toMatchObject({
      enabled: true,
      userinfoUrl: 'https://sso.local/userinfo',
    })
    expect(toastSuccess).toHaveBeenCalled()
  })

  it('maps the backend oidc_config_invalid onto the endpoint group instead of a toast', async () => {
    saveOAuthLoginApp.mockRejectedValue(
      new HttpError(422, { code: 'oidc_config_invalid', message: 'discovery unreachable' }, 'unprocessable'),
    )
    const wrapper = mount(SettingsOAuth)
    await flushPromises()
    await openPurpose(wrapper, 'login')
    await openTab(wrapper, 'oidc')

    await wrapper.get('#oauth-discovery-oidc').setValue('https://sso.local/nope')
    await wrapper.get('[data-panel="oidc"] form').trigger('submit')
    await flushPromises()

    expect(wrapper.get('[data-panel="oidc"] .field-error').text()).toContain('已存的配置没动')
    expect(toastError).not.toHaveBeenCalled()

    await wrapper.get('#oauth-discovery-oidc').setValue('https://sso.local/real')
    expect(wrapper.find('.field-error').exists()).toBe(false)
  })

  // ─── 钉钉 / 飞书 / 企业微信(登录档另加三张卡)───────────────────────────

  it('lists the three platform sources under the login purpose only', async () => {
    const wrapper = mount(SettingsOAuth)
    await flushPromises()

    for (const pid of ['dingtalk', 'feishu', 'wecom']) {
      expect(wrapper.find(`[data-provider="${pid}"]`).exists(), `${pid} 不该出现在仓库凭据档`).toBe(false)
    }

    await openPurpose(wrapper, 'login')
    expect(wrapper.get('[data-provider="dingtalk"]').text()).toContain('钉钉')
    expect(wrapper.get('[data-provider="feishu"]').text()).toContain('飞书')
    // tab 状态点也得跟着新档位走:三家全都没配过
    expect(wrapper.get('[data-provider="wecom"]').find('.tab-dot--off').exists()).toBe(true)
    expect(wrapper.get('[data-provider="wecom"]').text()).toContain('企业微信')
  })

  it('gives the platform cards endpoint overrides without the OIDC-only discovery and ID claim', async () => {
    getOAuthLoginApps.mockResolvedValue([makePlatformApp('dingtalk')])
    const wrapper = mount(SettingsOAuth)
    await flushPromises()
    await openPurpose(wrapper, 'login')
    await openTab(wrapper, 'dingtalk')

    const panel = wrapper.get('[data-panel="dingtalk"]')
    for (const id of [
      '#oauth-oidc-authorize-dingtalk',
      '#oauth-oidc-token-dingtalk',
      '#oauth-oidc-userinfo-dingtalk',
      '#oauth-oidc-scopes-dingtalk',
    ]) {
      expect(panel.find(id).exists(), `钉钉面板缺少字段 ${id}`).toBe(true)
    }
    // discovery 与主键声明是 OIDC 独有的:钉钉的地址由后端预设,主键由平台决定
    expect(panel.find('#oauth-discovery-dingtalk').exists()).toBe(false)
    expect(panel.find('#oauth-oidc-idclaim-dingtalk').exists()).toBe(false)
    expect(panel.find('#oauth-agentid-dingtalk').exists()).toBe(false)
    // 认人只认平台发的稳定主键,这句得让管理员在保存前就看到
    expect(panel.text()).toContain('只认平台发的稳定主键')
    expect((panel.get('#oauth-clientid-dingtalk').element as HTMLInputElement).placeholder).toContain(
      '开放平台',
    )
  })

  it('asks the enterprise-WeChat card for an AgentId and hides the scope field', async () => {
    getOAuthLoginApps.mockResolvedValue([makePlatformApp('wecom')])
    const wrapper = mount(SettingsOAuth)
    await flushPromises()
    await openPurpose(wrapper, 'login')
    await openTab(wrapper, 'wecom')

    const panel = wrapper.get('[data-panel="wecom"]')
    // 扫码地址的参数名是 agentid,值取应用详情里的 AgentId
    expect((panel.get('#oauth-agentid-wecom').element as HTMLInputElement).value).toBe('1000002')
    // 企业微信的扫码授权地址压根不带 scope 这一参数,给了栏位只会误导
    expect(panel.find('#oauth-oidc-scopes-wecom').exists()).toBe(false)
    // Client ID 那栏填的是企业 ID,不是应用 ID
    expect((panel.get('#oauth-clientid-wecom').element as HTMLInputElement).placeholder).toContain('corpid')
  })

  it('blocks enabling WeCom without an AgentId before the round trip', async () => {
    getOAuthLoginApps.mockResolvedValue([makePlatformApp('wecom', { agentId: '', enabled: false })])
    const wrapper = mount(SettingsOAuth)
    await flushPromises()
    await openPurpose(wrapper, 'login')
    await openTab(wrapper, 'wecom')

    await wrapper.get('[data-panel="wecom"]').get('[role="switch"]').trigger('click')
    await wrapper.get('[data-panel="wecom"] form').trigger('submit')
    await flushPromises()

    expect(saveOAuthLoginApp).not.toHaveBeenCalled()
    expect(wrapper.get('[data-panel="wecom"] .field-error').text()).toContain('AgentId')
  })

  it('sends the AgentId and clears it explicitly when the field is emptied', async () => {
    getOAuthLoginApps.mockResolvedValue([makePlatformApp('wecom')])
    saveOAuthLoginApp.mockResolvedValue(makePlatformApp('wecom', { agentId: '' }))
    const wrapper = mount(SettingsOAuth)
    await flushPromises()
    await openPurpose(wrapper, 'login')
    await openTab(wrapper, 'wecom')

    await wrapper.get('#oauth-agentid-wecom').setValue(' 1000002 ')
    await wrapper.get('[data-panel="wecom"] form').trigger('submit')
    await flushPromises()

    // 整份覆盖:七项每次都传,漏传等于清空
    expect(saveOAuthLoginApp.mock.calls[0][1]).toMatchObject({
      agentId: '1000002',
      discoveryUrl: '',
      idClaim: '',
    })
    expect(saveOAuthLoginApp.mock.calls[0][0]).toBe('wecom')

    await wrapper.get('#oauth-agentid-wecom').setValue('')
    await wrapper.get('[data-panel="wecom"] form').trigger('submit')
    await flushPromises()
    expect(saveOAuthLoginApp.mock.calls[1][1].agentId).toBe('')
  })

  it('keeps a git card free of the platform fields and sends them empty', async () => {
    saveOAuthLoginApp.mockResolvedValue(makeLoginApp({ provider: 'gitee' }))
    const wrapper = mount(SettingsOAuth)
    await flushPromises()
    await openPurpose(wrapper, 'login')

    const panel = wrapper.get('[data-panel="gitee"]')
    for (const id of ['discovery', 'oidc-authorize', 'oidc-scopes', 'agentid']) {
      expect(panel.find(`#oauth-${id}-gitee`).exists()).toBe(false)
    }
    await wrapper.get('[data-panel="gitee"] form').trigger('submit')
    await flushPromises()

    expect(saveOAuthLoginApp.mock.calls[0][1]).toMatchObject({ agentId: '', scopes: '' })
    expect(panel.text()).toContain('git 平台')
  })

  it('maps the backend platform_config_invalid onto the endpoint group', async () => {
    saveOAuthLoginApp.mockRejectedValue(
      new HttpError(422, { code: 'platform_config_invalid', message: 'bad override' }, 'unprocessable'),
    )
    const wrapper = mount(SettingsOAuth)
    await flushPromises()
    await openPurpose(wrapper, 'login')
    await openTab(wrapper, 'feishu')

    await wrapper.get('#oauth-clientid-feishu').setValue('platform-client-id')
    await wrapper.get('#oauth-secret-feishu').setValue('a-secret')
    await wrapper.get('#oauth-oidc-authorize-feishu').setValue('ftp://accounts.feishu.cn/authorize')
    await wrapper.get('[data-panel="feishu"] form').trigger('submit')
    await flushPromises()

    // 没有 discovery 栏时红字单独成一行,但仍然归在端点这组里
    expect(wrapper.get('[data-panel="feishu"] .field-error').text()).toContain('已存的配置没动')
    expect(toastError).not.toHaveBeenCalled()
  })

  it('maps the backend agent_id_required onto the AgentId field, not the toast', async () => {
    saveOAuthLoginApp.mockRejectedValue(
      new HttpError(422, { code: 'agent_id_required', message: 'missing agent id' }, 'unprocessable'),
    )
    const wrapper = mount(SettingsOAuth)
    await flushPromises()
    await openPurpose(wrapper, 'login')
    await openTab(wrapper, 'wecom')

    await wrapper.get('#oauth-agentid-wecom').setValue('')
    await wrapper.get('[data-panel="wecom"] form').trigger('submit')
    await flushPromises()

    expect(wrapper.get('[data-panel="wecom"] .field-error').text()).toContain('企业微信需要填 AgentId')
    expect(toastError).not.toHaveBeenCalled()
  })

  it('falls back to a git tab when leaving the login purpose with WeCom open', async () => {
    const wrapper = mount(SettingsOAuth)
    await flushPromises()
    await openPurpose(wrapper, 'login')
    await openTab(wrapper, 'wecom')

    await openPurpose(wrapper, 'vault')
    // 企微在仓库凭据档没有对应的卡:active 停在那里会渲染一张存不了的表单
    expect(wrapper.get('[data-panel]').attributes('data-panel')).toBe('gitee')
  })
})
