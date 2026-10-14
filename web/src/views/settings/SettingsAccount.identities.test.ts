import { describe, it, expect, beforeEach, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import SettingsAccount from './SettingsAccount.vue'
import { HttpError } from '../../api/http'

// ─── module mocks ───────────────────────────────────────────────────────────

const listSessions = vi.fn()
const listIdentities = vi.fn()
const unbindIdentity = vi.fn()
vi.mock('../../api/account', async () => {
  const actual = await vi.importActual<typeof import('../../api/account')>('../../api/account')
  return {
    ...actual,
    listSessions: (...a: unknown[]) => listSessions(...a),
    listIdentities: (...a: unknown[]) => listIdentities(...a),
    unbindIdentity: (...a: unknown[]) => unbindIdentity(...a),
    changePassword: vi.fn(),
    revokeSession: vi.fn(),
  }
})

const getOAuthApps = vi.fn()
vi.mock('../../api/oauth', async () => {
  const actual = await vi.importActual<typeof import('../../api/oauth')>('../../api/oauth')
  return { ...actual, getOAuthApps: (...a: unknown[]) => getOAuthApps(...a) }
})

const toastSuccess = vi.fn()
const toastError = vi.fn()
const toastInfo = vi.fn()
vi.mock('../../composables/useToast', () => ({
  useToast: () => ({ success: toastSuccess, error: toastError, info: toastInfo }),
}))

// 解绑走二次确认:测试里直接把确认框「按下」或「取消」。
const confirmOpen = vi.fn()
vi.mock('../../composables/useConfirm', () => ({
  useConfirm: () => ({ open: confirmOpen }),
}))

vi.mock('../../composables/useOnboarding', () => ({ resetOnboarding: vi.fn() }))

const routeQuery: { value: Record<string, string> } = { value: {} }
const replace = vi.fn()
vi.mock('vue-router', () => ({
  useRoute: () => ({ get query() { return routeQuery.value }, path: '/settings/account' }),
  useRouter: () => ({ replace, push: vi.fn() }),
}))

/** jsdom 的 window.location 不可导航:换成只记 href 的假对象(发起授权靠整页跳转)。 */
const location: { href: string } = { href: '' }

function makeBinding(over: Partial<import('../../api/account').IdentityBinding> = {}) {
  return {
    id: 'bnd_1',
    provider: 'github',
    externalId: '88123',
    username: 'octocat',
    email: 'octocat@corp.example.com',
    createdAt: '2026-10-01T00:00:00Z',
    updatedAt: '2026-10-01T00:00:00Z',
    ...over,
  }
}

function mountAccount() {
  return mount(SettingsAccount, {
    global: { plugins: [createPinia()], stubs: { Teleport: true, 'router-link': true } },
  })
}

describe('SettingsAccount — 外部 git 账号绑定', () => {
  beforeEach(() => {
    listSessions.mockReset().mockResolvedValue([])
    listIdentities.mockReset().mockResolvedValue([])
    unbindIdentity.mockReset()
    getOAuthApps.mockReset().mockResolvedValue([])
    toastSuccess.mockReset()
    toastError.mockReset()
    toastInfo.mockReset()
    confirmOpen.mockReset().mockResolvedValue(true)
    replace.mockReset()
    routeQuery.value = {}
    location.href = ''
    Object.defineProperty(window, 'location', { configurable: true, value: location })
  })

  it('lists every bound identity with its provider, login and platform ID', async () => {
    listIdentities.mockResolvedValue([makeBinding()])
    const wrapper = mountAccount()
    await flushPromises()

    const section = wrapper.get('[aria-labelledby="acct-ids-h"]')
    expect(section.text()).toContain('GitHub')
    expect(section.text()).toContain('octocat')
    expect(section.text()).toContain('88123')
    expect(section.text()).toContain('octocat@corp.example.com')
  })

  it('shows the empty state instead of a blank card', async () => {
    listIdentities.mockResolvedValue([])
    const wrapper = mountAccount()
    await flushPromises()

    expect(wrapper.get('[aria-labelledby="acct-ids-h"]').text()).toContain(
      '还没有绑定任何 git 平台账号',
    )
  })

  it('asks before unlinking and skips the call when cancelled', async () => {
    listIdentities.mockResolvedValue([makeBinding()])
    confirmOpen.mockResolvedValueOnce(false)
    const wrapper = mountAccount()
    await flushPromises()

    await wrapper.get('[aria-labelledby="acct-ids-h"]').get('button.app-btn--danger').trigger('click')
    await flushPromises()

    expect(confirmOpen).toHaveBeenCalledTimes(1)
    expect(unbindIdentity).not.toHaveBeenCalled()
  })

  it('unlinks, toasts and reloads the list', async () => {
    // 首帧要有那一行(才点得到解绑),刷新后回空列表(才看得出是跟着服务端走的)。
    listIdentities.mockResolvedValueOnce([makeBinding()]).mockResolvedValue([])
    unbindIdentity.mockResolvedValue(undefined)
    const wrapper = mountAccount()
    await flushPromises()

    await wrapper.get('[aria-labelledby="acct-ids-h"]').get('button.app-btn--danger').trigger('click')
    await flushPromises()

    expect(unbindIdentity).toHaveBeenCalledWith('bnd_1')
    expect(toastSuccess).toHaveBeenCalledWith('已解绑')
    // 挂载时拉一次,解绑后再拉一次:界面里的行必须跟着服务端走
    expect(listIdentities).toHaveBeenCalledTimes(2)
  })

  it('treats an already-gone binding as refreshed, not as a failure', async () => {
    listIdentities.mockResolvedValue([makeBinding()])
    unbindIdentity.mockRejectedValue(
      new HttpError(404, { code: 'binding_not_found', message: 'nope' }, 'not found'),
    )
    const wrapper = mountAccount()
    await flushPromises()

    await wrapper.get('[aria-labelledby="acct-ids-h"]').get('button.app-btn--danger').trigger('click')
    await flushPromises()

    expect(toastError).not.toHaveBeenCalled()
    expect(toastInfo).toHaveBeenCalledWith('这条绑定已不存在,列表已刷新')
    expect(listIdentities).toHaveBeenCalledTimes(2)
  })

  it('only offers to link platforms whose credentials app is enabled and configured', async () => {
    getOAuthApps.mockResolvedValue([
      { provider: 'github', clientId: 'a', baseUrl: '', enabled: true, maskedSecret: '••••1', configured: true, updatedAt: null },
      { provider: 'gitlab', clientId: 'b', baseUrl: '', enabled: false, maskedSecret: '••••2', configured: true, updatedAt: null },
      { provider: 'gitee', clientId: '', baseUrl: '', enabled: false, maskedSecret: '', configured: false, updatedAt: null },
    ])
    const wrapper = mountAccount()
    await flushPromises()

    const row = wrapper.get('.acct-bind__row')
    expect(row.text()).toContain('用 GitHub 绑定')
    expect(row.text()).not.toContain('GitLab')
    expect(row.text()).not.toContain('Gitee')
  })

  it('starts the link with a full-page navigation to the bind entrypoint', async () => {
    getOAuthApps.mockResolvedValue([
      { provider: 'github', clientId: 'a', baseUrl: '', enabled: true, maskedSecret: '••••1', configured: true, updatedAt: null },
    ])
    const wrapper = mountAccount()
    await flushPromises()

    await wrapper.get('.acct-bind__row button').trigger('click')

    expect(location.href).toBe('/api/oauth/github/bind')
  })

  it('a failing OAuth app fetch costs the link buttons, not the identity list', async () => {
    getOAuthApps.mockRejectedValue(new Error('boom'))
    listIdentities.mockResolvedValue([makeBinding()])
    const wrapper = mountAccount()
    await flushPromises()

    expect(wrapper.find('.acct-bind').exists()).toBe(false)
    expect(wrapper.get('[aria-labelledby="acct-ids-h"]').text()).toContain('octocat')
  })

  it('reports a completed link and strips the query so a refresh stays quiet', async () => {
    routeQuery.value = { bound: 'github' }
    mountAccount()
    await flushPromises()

    expect(toastSuccess).toHaveBeenCalledWith('已绑定 GitHub')
    expect(replace).toHaveBeenCalledWith({ query: {} })
  })

  it('translates the callback reason code into readable copy', async () => {
    routeQuery.value = { oauth_error: 'name_taken', provider: 'gitlab' }
    mountAccount()
    await flushPromises()

    // 文案取自 zh-CN 根 locale 的 oauthErr.nameTaken(完整一句,含「找管理员绑定」的指引)。
    expect(toastError).toHaveBeenCalledWith(
      'GitLab 绑定未完成',
      expect.objectContaining({ detail: expect.stringContaining('该平台用户名在本实例已被占用') }),
    )
    expect(replace).toHaveBeenCalledWith({ query: {} })
  })

  it('keeps an unknown reason code visible instead of showing nothing', async () => {
    routeQuery.value = { oauth_error: 'brand_new_code' }
    mountAccount()
    await flushPromises()

    expect(toastError.mock.calls[0][1]?.detail).toContain('brand_new_code')
  })
})
