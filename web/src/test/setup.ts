/**
 * Vitest global setup (jsdom).
 *
 * jsdom 不实现 matchMedia / ResizeObserver,部分组件(主题、动效降级)
 * 间接触达它们 → 这里提供最小 stub,避免无关报错污染单测信号。
 * 真实断言仍由各 spec 自行 mock 所需 API(如 fetch / localStorage)。
 */
import { vi } from 'vitest'
import { config } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { i18n, setLocale } from '../i18n'
import { PERM_LABEL_KEY } from '../lib/roleEditor'
import type { PermId, ResourceKind, SessionUser } from '../api/auth'
import { useSessionStore } from '../stores/session'

// 组件普遍使用 useI18n() → 全局为所有 mount 安装 i18n 插件,避免每个 spec 重复配置。
// locale 钉到 zh-CN(产品默认),让既有「断言中文文案」的组件测试保持确定性。
setLocale('zh-CN')

// 带权限判断的控件(复用库、节点工作室、JobDrawer 的「存为自定义节点」)在 setup 里读
// session store → 没有活动 pinia 就直接抛。这里给一份默认「全权限」会话,让既有断言照旧
// 看得见受权限收起的区块;要验「没权限」的那种 spec 自己 createPinia() 覆盖即可
// (mount 的 global.plugins 排在 config.global 之后,后装的那个 pinia 生效)。
const pinia = createPinia()
setActivePinia(pinia)
const allPerms = Object.keys(PERM_LABEL_KEY) as PermId[]
const fullSession: SessionUser = {
  username: 'tester',
  role: 'admin',
  displayName: '',
  email: '',
  capabilities: {
    settings: true,
    kinds: Object.fromEntries(
      (['project', 'run', 'server', 'kube_cluster'] as ResourceKind[]).map((k) => [k, 'manage']),
    ),
    perms: allPerms,
  },
}
useSessionStore().setUser(fullSession)

config.global.plugins = [...(config.global.plugins ?? []), i18n, pinia]

// Node 25 ships a native global `localStorage` stub (the `--localstorage-file`
// experiment) that shadows jsdom's implementation but lacks getItem/clear/etc.
// Install a real in-memory Web Storage so theme/onboarding code under test works.
class MemoryStorage implements Storage {
  private store = new Map<string, string>()
  get length(): number {
    return this.store.size
  }
  clear(): void {
    this.store.clear()
  }
  getItem(key: string): string | null {
    return this.store.has(key) ? this.store.get(key)! : null
  }
  key(index: number): string | null {
    return Array.from(this.store.keys())[index] ?? null
  }
  removeItem(key: string): void {
    this.store.delete(key)
  }
  setItem(key: string, value: string): void {
    this.store.set(key, String(value))
  }
}

if (typeof localStorage === 'undefined' || typeof localStorage.clear !== 'function') {
  const ls = new MemoryStorage()
  const ss = new MemoryStorage()
  Object.defineProperty(globalThis, 'localStorage', { value: ls, configurable: true })
  Object.defineProperty(globalThis, 'sessionStorage', { value: ss, configurable: true })
  Object.defineProperty(window, 'localStorage', { value: ls, configurable: true })
  Object.defineProperty(window, 'sessionStorage', { value: ss, configurable: true })
}

// matchMedia stub — prefers-reduced-motion / 主题查询会用到
if (!window.matchMedia) {
  window.matchMedia = vi.fn().mockImplementation((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    addListener: vi.fn(),
    removeListener: vi.fn(),
    dispatchEvent: vi.fn(),
  }))
}

// ResizeObserver stub — naive-ui 等组件挂载时可能引用
if (!('ResizeObserver' in globalThis)) {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}

// scrollIntoView stub — jsdom 没有布局,可滚动下拉的方向键导航会调用它
if (!Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = () => {}
}
