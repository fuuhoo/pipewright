import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useSessionStore } from './session'
import { http, HttpError } from '../api/http'
import type { Capabilities, SessionUser } from '../api/auth'

// 与后端 access.CapabilitiesFor 同形的夹具:功能档位由后端算,store 只做判断。
const adminCaps: Capabilities = {
  settings: true,
  kinds: { project: 'manage', run: 'manage', server: 'manage', kube_cluster: 'manage' },
}
const userCaps: Capabilities = {
  settings: false,
  kinds: { project: 'operate', run: 'operate', server: 'operate', kube_cluster: 'operate' },
}
const viewerCaps: Capabilities = {
  settings: false,
  kinds: { project: 'view', run: 'view', server: 'view', kube_cluster: 'view' },
}
const admin: SessionUser = { username: 'admin', role: 'admin', capabilities: adminCaps }

describe('session store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('returns { kind: ok } and caches the user on a successful session fetch', async () => {
    const getSpy = vi.spyOn(http, 'get').mockResolvedValue(admin)
    const store = useSessionStore()

    const r1 = await store.ensureSession()
    expect(r1).toEqual({ kind: 'ok', user: admin })
    expect(store.user).toEqual(admin)

    // Cached: second call must NOT hit the network again.
    const r2 = await store.ensureSession()
    expect(r2).toEqual({ kind: 'ok', user: admin })
    expect(getSpy).toHaveBeenCalledTimes(1)
  })

  it('returns { kind: unauthenticated } and nulls user on 401', async () => {
    vi.spyOn(http, 'get').mockRejectedValue(new HttpError(401, null, 'unauthorized'))
    const store = useSessionStore()

    const r = await store.ensureSession()
    expect(r).toEqual({ kind: 'unauthenticated' })
    expect(store.user).toBeNull()
    expect(store.isNetworkError).toBe(false)
  })

  it('returns { kind: error } on 5xx and sets isNetworkError without evicting a cached user', async () => {
    const store = useSessionStore()
    // Prime with a known user first.
    store.setUser(admin)

    vi.spyOn(http, 'get').mockRejectedValue(new HttpError(503, null, 'unavailable'))
    const r = await store.ensureSession(true) // force bypass cache

    expect(r.kind).toBe('error')
    if (r.kind === 'error') expect(r.status).toBe(503)
    expect(store.isNetworkError).toBe(true)
    // Cached user must survive a backend fault.
    expect(store.user).toEqual(admin)
  })

  it('treats raw network errors (non-HttpError) as { kind: error, status: 0 }', async () => {
    vi.spyOn(http, 'get').mockRejectedValue(new Error('boom'))
    const store = useSessionStore()
    const r = await store.ensureSession()
    expect(r.kind).toBe('error')
    if (r.kind === 'error') {
      expect(r.status).toBe(0)
      expect(r.message).toBe('boom')
    }
  })

  it('setUser primes the cache so a later ensureSession does not fetch', async () => {
    const getSpy = vi.spyOn(http, 'get')
    const store = useSessionStore()
    store.setUser({ username: 'root', role: 'user', capabilities: userCaps })

    const r = await store.ensureSession()
    expect(r).toEqual({ kind: 'ok', user: { username: 'root', role: 'user', capabilities: userCaps } })
    expect(getSpy).not.toHaveBeenCalled()
  })

  it('clearSession marks the user as confirmed-logged-out', async () => {
    const getSpy = vi.spyOn(http, 'get')
    const store = useSessionStore()
    store.setUser(admin)
    store.clearSession()

    const r = await store.ensureSession()
    expect(r).toEqual({ kind: 'unauthenticated' })
    expect(getSpy).not.toHaveBeenCalled()
  })

  describe('能力位判断', () => {
    it('管理员过设置门,普通用户不过', async () => {
      vi.spyOn(http, 'get').mockResolvedValue(admin)
      const store = useSessionStore()
      await store.ensureSession()
      expect(store.canSettings).toBe(true)
      expect(store.meets({ settings: true })).toBe(true)

      store.setUser({ username: 'dev', role: 'user', capabilities: userCaps })
      expect(store.canSettings).toBe(false)
      expect(store.meets({ settings: true })).toBe(false)
      // 功能轴不封顶 user:四类资源都能 operate
      expect(store.can('server', 'operate')).toBe(true)
    })

    it('只读角色过不了 server/operate,所以运维终端的路由门会拦下', () => {
      const store = useSessionStore()
      store.setUser({ username: 'watch', role: 'viewer', capabilities: viewerCaps })
      expect(store.can('server', 'operate')).toBe(false)
      expect(store.meets({ kind: 'server', act: 'operate' })).toBe(false)
      expect(store.can('server', 'view')).toBe(true)
      // manage 不看功能档位:组长管自己的组归数据轴判
      expect(store.can('project', 'manage')).toBe(true)
    })

    it('未登录时一律按最严一档,不会误放开入口', () => {
      const store = useSessionStore()
      store.clearSession()
      expect(store.canSettings).toBe(false)
      expect(store.can('project', 'operate')).toBe(false)
      expect(store.meets(undefined)).toBe(true)
    })
  })
})
