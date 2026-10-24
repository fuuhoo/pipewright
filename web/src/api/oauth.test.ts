import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { getOAuthApps, saveOAuthApp, authorizeUrl, issuesGitToken, supportsGitCredential } from './oauth'

function jsonResponse(status: number, body: unknown, ok = status < 400): Response {
  return {
    status,
    ok,
    headers: new Headers({ 'content-type': 'application/json' }),
    json: async () => body,
    text: async () => JSON.stringify(body),
  } as unknown as Response
}

describe('oauth api client', () => {
  let fetchMock: ReturnType<typeof vi.fn>

  beforeEach(() => {
    fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    document.cookie.split(';').forEach((c) => {
      const name = c.split('=')[0].trim()
      if (name) document.cookie = `${name}=; expires=Thu, 01 Jan 1970 00:00:00 GMT`
    })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('GET /api/oauth/apps returns the app list', async () => {
    const apps = [
      {
        provider: 'github',
        clientId: 'abc',
        baseUrl: '',
        enabled: true,
        maskedSecret: '••••a91f',
        configured: true,
        updatedAt: '2026-05-30T00:00:00Z',
      },
    ]
    fetchMock.mockResolvedValue(jsonResponse(200, apps))
    const result = await getOAuthApps()
    expect(result).toEqual(apps)
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/oauth/apps')
    expect(init.method).toBe('GET')
  })

  it('PUT /api/oauth/apps/{provider} hits the per-provider URL with the body', async () => {
    document.cookie = 'pipewright_csrf=tok123'
    fetchMock.mockResolvedValue(jsonResponse(200, { provider: 'gitee' }))
    await saveOAuthApp('gitee', {
      clientId: 'cid',
      clientSecret: 'sec',
      enabled: true,
    })
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/oauth/apps/gitee')
    expect(init.method).toBe('PUT')
    expect(init.body).toBe(JSON.stringify({ clientId: 'cid', clientSecret: 'sec', enabled: true }))
    // write methods carry the CSRF token
    expect((init.headers as Headers).get('X-CSRF-Token')).toBe('tok123')
  })

  it('saveOAuthApp omits clientSecret when not provided (keep-existing semantics)', async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, {}))
    await saveOAuthApp('github', { clientId: 'cid', enabled: false })
    const init = fetchMock.mock.calls[0][1]
    const body = JSON.parse(init.body)
    expect(body).not.toHaveProperty('clientSecret')
    expect(body).toEqual({ clientId: 'cid', enabled: false })
  })

  it('authorizeUrl builds the per-provider full-page authorize entrypoint', () => {
    expect(authorizeUrl('gitee')).toBe('/api/oauth/gitee/authorize')
    expect(authorizeUrl('github')).toBe('/api/oauth/github/authorize')
    expect(authorizeUrl('gitlab')).toBe('/api/oauth/gitlab/authorize')
    expect(authorizeUrl('gitea')).toBe('/api/oauth/gitea/authorize')
    expect(authorizeUrl('custom')).toBe('/api/oauth/custom/authorize')
  })

  it('says which identity sources can hand out a git token — the sync switch reads this', () => {
    // 这条判据前后端各写一遍,页面靠它决定「开关能不能点」。它错一次就是
    // 「屏上亮着但保存被 422」或「明明能开却不给开」,所以逐档钉死。
    for (const p of ['gitee', 'github', 'gitlab', 'gitea', 'custom'] as const) {
      expect(issuesGitToken(p), p).toBe(true)
    }
    for (const p of ['oidc', 'dingtalk', 'feishu', 'wecom'] as const) {
      expect(issuesGitToken(p), p).toBe(false)
    }
  })

  it('only opens credential sync for a git platform, or an OIDC that declares one', () => {
    expect(supportsGitCredential('gitlab', '')).toBe(true)
    expect(supportsGitCredential('oidc', '')).toBe(false) // 未声明 = 只当身份源
    expect(supportsGitCredential('oidc', 'gitlab')).toBe(true)
    expect(supportsGitCredential('oidc', 'gitea')).toBe(true)
    // 三家国内身份源就算带个形态也不该被认成 git 平台:形态那栏对它们根本不存在
    expect(supportsGitCredential('dingtalk', 'gitlab')).toBe(false)
    expect(supportsGitCredential('feishu', 'gitea')).toBe(false)
    expect(supportsGitCredential('wecom', 'gitlab')).toBe(false)
  })
})
