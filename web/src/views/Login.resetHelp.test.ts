import { describe, it, expect, beforeEach, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import Login from './Login.vue'
import { HttpError } from '../api/http'

// ─── module mocks ──────────────────────────────────────────────────────────

const login = vi.fn()
vi.mock('../api/auth', () => ({ login: (...a: unknown[]) => login(...a) }))
vi.mock('../api/projects', () => ({ listProjects: vi.fn(async () => []) }))
vi.mock('../composables/useOnboarding', () => ({ isOnboardingDismissed: () => true }))
vi.mock('../api/oauth', async () => {
  const actual = await vi.importActual<typeof import('../api/oauth')>('../api/oauth')
  return { ...actual, getPublicOAuthProviders: vi.fn(async () => []) }
})
vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {}, path: '/login' }),
  useRouter: () => ({ replace: vi.fn(), push: vi.fn() }),
}))

function mountLogin() {
  return mount(Login, { global: { plugins: [createPinia()], stubs: { 'router-link': true } } })
}

/** 走一次真实提交,让组件自己把 429 变成锁定态。 */
async function lockOut(wrapper: ReturnType<typeof mountLogin>) {
  login.mockRejectedValue(new HttpError(429, { code: 'locked_out', message: '登录失败次数过多' }, 'locked'))
  await wrapper.get('#username').setValue('admin')
  await wrapper.get('#password').setValue('nope')
  await wrapper.get('form').trigger('submit')
  await flushPromises()
}

HTMLCanvasElement.prototype.getContext = vi.fn(() => null) as unknown as typeof HTMLCanvasElement.prototype.getContext

describe('Login — 忘记管理员密码', () => {
  beforeEach(() => {
    login.mockReset()
  })

  it('只有被锁定时才出现入口', async () => {
    const wrapper = mountLogin()
    await flushPromises()
    expect(wrapper.find('.forgot').exists()).toBe(false)

    await lockOut(wrapper)
    expect(wrapper.get('.forgot').text()).toContain('忘记管理员密码')
  })

  it('点开弹层给出两条可执行命令与锁定自解说明', async () => {
    const wrapper = mountLogin()
    await flushPromises()
    await lockOut(wrapper)

    expect(wrapper.find('.modal').exists()).toBe(false)
    await wrapper.get('.forgot').trigger('click')

    const modal = wrapper.get('.modal')
    expect(modal.attributes('role')).toBe('dialog')
    expect(modal.text()).toContain('pipewright reset-admin-password --generate')
    expect(modal.text()).toContain('docker exec -it <容器名> /pipewright reset-admin-password --generate')
    // 只是被锁 ≠ 忘了口令,这句必须出现,否则会有人白白去改口令
    expect(modal.text()).toContain('15 分钟')
  })

  it('Esc 与关闭按钮都能收起弹层,且不误触发表单提交', async () => {
    const wrapper = mountLogin()
    await flushPromises()
    await lockOut(wrapper)

    await wrapper.get('.forgot').trigger('click')
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await flushPromises()
    expect(wrapper.find('.modal').exists()).toBe(false)

    await wrapper.get('.forgot').trigger('click')
    await wrapper.get('.modal__ok').trigger('click')
    expect(wrapper.find('.modal').exists()).toBe(false)
  })
})
