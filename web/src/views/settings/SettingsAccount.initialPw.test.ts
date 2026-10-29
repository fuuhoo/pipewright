import { describe, it, expect, beforeEach, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import SettingsAccount from './SettingsAccount.vue'
import { useSessionStore, type SessionUser } from '../../stores/session'

// ─── module mocks(与 identities 测试同一套,只是把改密相关函数换成可断言的 mock)──

const listSessions = vi.fn()
const listIdentities = vi.fn()
const getOAuthApps = vi.fn()
const changePassword = vi.fn()
const setInitialPassword = vi.fn()

vi.mock('../../api/account', async () => {
  const actual = await vi.importActual<typeof import('../../api/account')>('../../api/account')
  return {
    ...actual,
    listSessions: (...a: unknown[]) => listSessions(...a),
    listIdentities: (...a: unknown[]) => listIdentities(...a),
    changePassword: (...a: unknown[]) => changePassword(...a),
    setInitialPassword: (...a: unknown[]) => setInitialPassword(...a),
    revokeSession: vi.fn(),
  }
})

vi.mock('../../api/oauth', async () => {
  const actual = await vi.importActual<typeof import('../../api/oauth')>('../../api/oauth')
  return { ...actual, getOAuthApps: (...a: unknown[]) => getOAuthApps(...a) }
})

const toastSuccess = vi.fn()
vi.mock('../../composables/useToast', () => ({
  useToast: () => ({ success: toastSuccess, error: vi.fn(), info: vi.fn() }),
}))
vi.mock('../../composables/useConfirm', () => ({ useConfirm: () => ({ open: vi.fn() }) }))
vi.mock('../../composables/useOnboarding', () => ({ resetOnboarding: vi.fn() }))
vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {}, path: '/settings/account' }),
  useRouter: () => ({ replace: vi.fn(), push: vi.fn() }),
}))

function sessionUser(over: Partial<SessionUser> = {}): SessionUser {
  return {
    username: 'oidcuser',
    role: 'user',
    capabilities: { settings: false, kinds: {} },
    displayName: '',
    email: '',
    hasLocalPassword: false,
    ...over,
  }
}

function mountAs(user: SessionUser) {
  // 复用 test/setup.ts 装好的默认 pinia(其 session store 已有一份默认会话),这里只改用户。
  useSessionStore().setUser(user)
  return mount(SettingsAccount, {
    global: { stubs: { Teleport: true, 'router-link': true } },
  })
}

describe('SettingsAccount — 首次设置本地口令', () => {
  beforeEach(() => {
    listSessions.mockReset().mockResolvedValue([])
    listIdentities.mockReset().mockResolvedValue([])
    getOAuthApps.mockReset().mockResolvedValue([])
    changePassword.mockReset().mockResolvedValue(undefined)
    setInitialPassword.mockReset().mockResolvedValue(undefined)
    toastSuccess.mockReset()
  })

  it('OAuth 账号显示「设置本地口令」,不要求当前口令', async () => {
    const wrapper = mountAs(sessionUser({ hasLocalPassword: false }))
    await flushPromises()

    const card = wrapper.get('[aria-labelledby="acct-pw-h"]')
    expect(card.text()).toContain('设置本地口令')
    expect(card.find('#acct-current').exists()).toBe(false)
  })

  it('本地口令账号仍显示「修改密码」并要求当前口令', async () => {
    const wrapper = mountAs(sessionUser({ username: 'admin', role: 'admin', hasLocalPassword: true }))
    await flushPromises()

    const card = wrapper.get('[aria-labelledby="acct-pw-h"]')
    expect(card.text()).toContain('修改密码')
    expect(card.find('#acct-current').exists()).toBe(true)
  })

  it('提交走 /account/password/initial,成功后切回「修改密码」', async () => {
    const wrapper = mountAs(sessionUser({ hasLocalPassword: false }))
    await flushPromises()

    const card = wrapper.get('[aria-labelledby="acct-pw-h"]')
    await card.get('#acct-new').setValue('brandnew1234')
    await card.get('#acct-confirm').setValue('brandnew1234')
    await card.get('form').trigger('submit')
    await flushPromises()

    expect(setInitialPassword).toHaveBeenCalledWith('brandnew1234')
    expect(changePassword).not.toHaveBeenCalled()
    // 本地把会话标成「已有口令」,立即换成常规改密表单。
    expect(wrapper.get('[aria-labelledby="acct-pw-h"]').find('#acct-current').exists()).toBe(true)
    expect(toastSuccess).toHaveBeenCalled()
  })
})
