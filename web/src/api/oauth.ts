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

/**
 * 「登录用」身份源的枚举 = git 四档 + OIDC + 钉钉 / 飞书 / 企业微信。
 * 与后端 `normalizeProvider` / `normalizeLoginProvider` 两套枚举一一对应:
 * 后四档都给不了 git token,所以它们只出现在登录这一侧,永远不进保险库/仓库凭据。
 */
export type OAuthLoginProvider = OAuthProvider | 'oidc' | 'dingtalk' | 'feishu' | 'wecom'

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
  provider: OAuthLoginProvider
  /** 服务端给的显示名(Gitee/GitHub/GitLab/实例主机名/OIDC),前端不自己拼。 */
  label: string
}

/** GET /api/admin/oauth/login-apps 元素。 */
export interface OAuthLoginApp {
  provider: OAuthLoginProvider
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
  /**
   * 以下七项只对非 git 身份源有值,git 平台永远返回空串:
   * 发现文档地址、三个可覆盖的端点、主键声明名、scope 串、企业微信的 AgentId。
   * 前端据此决定这组字段显不显示,不必自己再判一遍枚举。
   */
  discoveryUrl: string
  authorizeUrl: string
  tokenUrl: string
  userinfoUrl: string
  idClaim: string
  scopes: string
  agentId: string
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
  /**
   * 端点 / scope / 主键声明 / AgentId 是**整份覆盖**语义(与 secret/白名单的「省略即保留」相反):
   * 每次保存都以这几个值为准,省略即清空。
   * 只有非 git 身份源用得上 —— git 档位服务端一律落空串,多发几个空字段不会串味。
   */
  discoveryUrl?: string
  authorizeUrl?: string
  tokenUrl?: string
  userinfoUrl?: string
  idClaim?: string
  scopes?: string
  /** 企业微信自建应用的 AgentId;其余身份源服务端一律清空。 */
  agentId?: string
}

export async function getPublicOAuthProviders(): Promise<PublicOAuthProvider[]> {
  return http.get<PublicOAuthProvider[]>('/api/auth/oauth/providers')
}

export async function getOAuthLoginApps(): Promise<OAuthLoginApp[]> {
  return http.get<OAuthLoginApp[]>('/api/admin/oauth/login-apps')
}

export async function saveOAuthLoginApp(
  provider: OAuthLoginProvider,
  input: SaveOAuthLoginAppInput,
): Promise<OAuthLoginApp> {
  return http.put<OAuthLoginApp>(`/api/admin/oauth/login-apps/${provider}`, input)
}

/**
 * 匿名登录流的发起地址。必须整页导航(`window.location.href = …`):
 * 后端 302 到平台授权页,还要在响应里种一次性浏览器 nonce cookie,fetch 会把这两步都打断。
 * `redirect` 是登录成功后的站内落地路径,服务端会过滤掉协议相对 URL 等开放重定向形态。
 */
export function loginAuthorizeUrl(provider: OAuthLoginProvider, redirect?: string): string {
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
