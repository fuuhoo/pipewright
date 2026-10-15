import { describe, it, expect, beforeEach, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import Login from './Login.vue'

// ─── module mocks ──────────────────────────────────────────────────────────

const login = vi.fn()
vi.mock('../api/auth', () => ({ login: (...a: unknown[]) => login(...a) }))

const listProjects = vi.fn()
vi.mock('../api/projects', () => ({ listProjects: (...a: unknown[]) => listProjects(...a) }))

vi.mock('../composables/useOnboarding', () => ({ isOnboardingDismissed: () => true }))

const getPublicOAuthProviders = vi.fn()
vi.mock('../api/oauth', async () => {
  const actual = await vi.importActual<typeof import('../api/oauth')>('../api/oauth')
  return { ...actual, getPublicOAuthProviders: (...a: unknown[]) => getPublicOAuthProviders(...a) }
})

// vue-router:route.query 由测试喂,replace 记录跳转(抹 oauth_error 参数用的就是它)。
const routeQuery: { value: Record<string, string> } = { value: {} }
const replace = vi.fn()
vi.mock('vue-router', () => ({
  useRoute: () => ({ get query() { return routeQuery.value }, path: '/login' }),
  useRouter: () => ({ replace, push: vi.fn() }),
}))

/** jsdom 的 window.location 不可导航,换成一个只记 href 的假对象。 */
const location: { href: string } = { href: '' }
function fakeLocation() {
  Object.defineProperty(window, 'location', { configurable: true, value: location })
}

function mountLogin() {
  return mount(Login, { global: { plugins: [createPinia()], stubs: { 'router-link': true } } })
}

// jsdom 没有 canvas:装饰用的粒子层调 getContext 只会刷一串「Not implemented」噪声,
// 与本用例无关 → 返回 null 走组件里「拿不到 2D 上下文就不动画」的分支。
HTMLCanvasElement.prototype.getContext = vi.fn(() => null) as unknown as typeof HTMLCanvasElement.prototype.getContext

describe('Login — OAuth 登录入口', () => {
  beforeEach(() => {
    login.mockReset()
    listProjects.mockReset().mockResolvedValue([])
    getPublicOAuthProviders.mockReset().mockResolvedValue([])
    replace.mockReset()
    routeQuery.value = {}
    location.href = ''
    fakeLocation()
  })

  it('只有服务端列出入口时才出现按钮', async () => {
    getPublicOAuthProviders.mockResolvedValue([])
    const wrapper = mountLogin()
    await flushPromises()
    expect(wrapper.findAll('.oauth__btn')).toHaveLength(0)

    getPublicOAuthProviders.mockResolvedValue([
      { provider: 'gitlab', label: 'GitLab' },
      { provider: 'custom', label: 'git.example.com' },
    ])
    const withOAuth = mountLogin()
    await flushPromises()
    const labels = withOAuth.findAll('.oauth__btn').map((b) => b.text())
    expect(labels).toEqual(['使用 GitLab 登录', '使用 git.example.com 登录'])
  })

  it('OIDC 身份源与 git 平台共用同一个入口和同一条导航', async () => {
    // 按钮名来自服务端的 label(OIDC 没有品牌名可借),登录页不该自己按平台拼名字
    getPublicOAuthProviders.mockResolvedValue([{ provider: 'oidc', label: 'OIDC' }])
    const wrapper = mountLogin()
    await flushPromises()

    expect(wrapper.get('.oauth__btn').text()).toBe('使用 OIDC 登录')
    await wrapper.get('.oauth__btn').trigger('click')
    expect(location.href).toBe('/api/auth/oauth/oidc/authorize')
  })

  it('入口拉不到就当没配:登录页照常可用,不能白屏', async () => {
    getPublicOAuthProviders.mockRejectedValue(new Error('boom'))
    const wrapper = mountLogin()
    await flushPromises()
    expect(wrapper.findAll('.oauth__btn')).toHaveLength(0)
    expect(wrapper.find('#password').exists()).toBe(true)
  })

  it('点击走整页导航,并把站内 redirect 带上', async () => {
    getPublicOAuthProviders.mockResolvedValue([{ provider: 'gitee', label: 'Gitee' }])
    routeQuery.value = { redirect: '/projects/7' }
    const wrapper = mountLogin()
    await flushPromises()

    await wrapper.get('.oauth__btn').trigger('click')
    expect(location.href).toBe('/api/auth/oauth/gitee/authorize?redirect=%2Fprojects%2F7')
  })

  it('协议相对的 redirect 不带出去(//evil 会被当成站外地址)', async () => {
    getPublicOAuthProviders.mockResolvedValue([{ provider: 'gitee', label: 'Gitee' }])
    routeQuery.value = { redirect: '//evil.example' }
    const wrapper = mountLogin()
    await flushPromises()

    await wrapper.get('.oauth__btn').trigger('click')
    expect(location.href).toBe('/api/auth/oauth/gitee/authorize')
  })

  it('回调失败的原因码翻成人读文案,并把参数从地址里抹掉', async () => {
    routeQuery.value = { oauth_error: 'name_taken' }
    const wrapper = mountLogin()
    await flushPromises()

    expect(wrapper.get('.banner').text()).toContain('该平台用户名在本实例已被占用')
    expect(replace).toHaveBeenCalledWith({ path: '/login', query: {} })
  })

  it('认不出的原因码走兜底并原样显示,而不是留一行空白', async () => {
    routeQuery.value = { oauth_error: 'brand_new_code' }
    const wrapper = mountLogin()
    await flushPromises()

    expect(wrapper.get('.banner').text()).toContain('登录未完成(brand_new_code)')
  })
})
