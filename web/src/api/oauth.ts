/**
 * OAuth Apps API — aligns to the frozen OAuth contract.
 *
 * 同一个 git 平台在这里有两种用途,配置与回调地址各自独立(见文件下半段的 login*):
 *   - 「仓库凭据」:已登录用户授权后换回 access_token,存进保险库给 git 操作用;
 *   - 「平台登录」:匿名访客用平台账号登进本平台。
 * 分两套是因为一个 OAuth 应用通常只允许登记一个回调地址,共用会互相牵制。
 *
 * GET /api/oauth/apps               → OAuthApp[]
 * PUT /api/oauth/apps/{provider}    → OAuthApp  (needs CSRF)
 * GET /api/oauth/{provider}/authorize → backend 302 to the git platform.
 *      This is NOT fetched — the browser must do a full-page navigation so
 *      the 302 redirect chain runs. Use `authorizeUrl(provider)` with
 *      `window.location.href = …`, never `http.get`.
 *
 * clientSecret is WRITE-ONLY: the server never returns plaintext.
 * GET/PUT responses only include maskedSecret (e.g. "••••a91f").
 * Leaving clientSecret empty on PUT keeps the existing secret unchanged.
 */

import { http } from './http'

export type OAuthProvider = 'gitee' | 'github' | 'gitlab' | 'custom'

/** GET /api/oauth/apps element — never contains plaintext clientSecret. */
export interface OAuthApp {
  provider: OAuthProvider
  clientId: string
  baseUrl: string
  enabled: boolean
  /** Server-computed mask, e.g. "••••a91f" — never plaintext. */
  maskedSecret: string
  configured: boolean
  updatedAt: string | null
}

/** PUT /api/oauth/apps/{provider} request body. */
export interface SaveOAuthAppInput {
  clientId: string
  /**
   * Write-only: omit or leave empty to keep the existing secret unchanged.
   * Non-empty rotates to the new secret.
   */
  clientSecret?: string
  /** Only meaningful for the self-hosted (`custom`) provider. */
  baseUrl?: string
  enabled: boolean
}

export async function getOAuthApps(): Promise<OAuthApp[]> {
  return http.get<OAuthApp[]>('/api/oauth/apps')
}

export async function saveOAuthApp(
  provider: OAuthProvider,
  input: SaveOAuthAppInput,
): Promise<OAuthApp> {
  return http.put<OAuthApp>(`/api/oauth/apps/${provider}`, input)
}

/**
 * Build the authorize entrypoint URL for a provider.
 *
 * The caller must perform a full-page navigation:
 *   window.location.href = authorizeUrl(provider)
 * The backend responds with a 302 to the git platform's consent screen.
 * Do NOT fetch this — fetch would follow the redirect without leaving the SPA.
 */
export function authorizeUrl(provider: OAuthProvider): string {
  return `/api/oauth/${provider}/authorize`
}

/**
 * 「用 git 平台账号登录本平台」——与上面的「取仓库凭据」是两套配置:
 * 一个 OAuth 应用通常只能登记一个回调地址,所以两张表、两个回调、互不牵制。
 *
 * GET    /api/auth/oauth/providers          → 公开,登录页据此出按钮
 * GET    /api/auth/oauth/{p}/authorize      → 公开;同样必须整页导航,不要 fetch
 * GET    /api/admin/oauth/login-apps        → admin
 * PUT    /api/admin/oauth/login-apps/{p}    → admin(需 CSRF)
 * clientSecret 与上面同义:只写,响应里只有 maskedSecret;留空 = 保留已存。
 */

/** GET /api/auth/oauth/providers 元素(公开;不含任何配置细节)。 */
export interface PublicOAuthProvider {
  provider: OAuthProvider
  label: string
}

/** GET /api/admin/oauth/login-apps 元素。 */
export interface OAuthLoginApp {
  provider: OAuthProvider
  clientId: string
  baseUrl: string
  /** 邮箱域名白名单;空数组 = 不限域名(自动建号仍只给最小角色)。 */
  allowedDomains: string[]
  enabled: boolean
  maskedSecret: string
  configured: boolean
  updatedAt: string | null
  /** 要在 git 平台应用里登记的回调地址(服务端按当前访问 origin 现算)。 */
  callbackUrl: string
}

/** PUT /api/admin/oauth/login-apps/{provider} 请求体。 */
export interface SaveOAuthLoginAppInput {
  clientId: string
  /** 省略或留空 = 保留已存 secret。 */
  clientSecret?: string
  baseUrl?: string
  /**
   * 省略(undefined)= 保留已存白名单;空数组 = 显式清空(不限域名)。
   * 这两个语义必须能区分,否则前端漏传一次就把管理员的白名单清了。
   */
  allowedDomains?: string[]
  enabled: boolean
}

export async function getPublicOAuthProviders(): Promise<PublicOAuthProvider[]> {
  return http.get<PublicOAuthProvider[]>('/api/auth/oauth/providers')
}

export async function getOAuthLoginApps(): Promise<OAuthLoginApp[]> {
  return http.get<OAuthLoginApp[]>('/api/admin/oauth/login-apps')
}

export async function saveOAuthLoginApp(
  provider: OAuthProvider,
  input: SaveOAuthLoginAppInput,
): Promise<OAuthLoginApp> {
  return http.put<OAuthLoginApp>(`/api/admin/oauth/login-apps/${provider}`, input)
}

/**
 * 匿名登录流的发起地址。必须整页导航(`window.location.href = …`):
 * 后端 302 到平台授权页,还要在响应里种一次性浏览器 nonce cookie,fetch 会把这两步都打断。
 * `redirect` 是登录成功后的站内落地路径,服务端会过滤掉协议相对 URL 等开放重定向形态。
 */
export function loginAuthorizeUrl(provider: OAuthProvider, redirect?: string): string {
  const base = `/api/auth/oauth/${provider}/authorize`
  return redirect ? `${base}?redirect=${encodeURIComponent(redirect)}` : base
}

/**
 * 「把 git 身份绑到当前账号」的入口(已登录)。同样是整页导航:
 * 它与「连接凭据」共用 /api/oauth/{p}/callback,分隔只在 state 里记录的意图。
 */
export function bindStartUrl(provider: OAuthProvider): string {
  return `/api/oauth/${provider}/bind`
}
