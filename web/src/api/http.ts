/**
 * Fetch wrapper for Pipewright API.
 *
 * - Reads pipewright_csrf cookie and attaches X-CSRF-Token header on write methods
 *   (POST / PUT / PATCH / DELETE) — "double-submit cookie" CSRF protection.
 * - On 401, redirects to /login (preserving current URL as ?redirect=).
 * - Parses the canonical error envelope { error: { code, message } }.
 */

import { currentLocale } from '../i18n'

export interface ApiError {
  code: string
  message: string
}

export class HttpError extends Error {
  constructor(
    public readonly status: number,
    public readonly apiError: ApiError | null,
    message: string,
  ) {
    super(message)
    this.name = 'HttpError'
  }
}

/** Read a cookie value by name. Returns empty string if not found. */
function getCookie(name: string): string {
  const match = document.cookie.split(';').find((c) => c.trim().startsWith(name + '='))
  return match ? decodeURIComponent(match.trim().slice(name.length + 1)) : ''
}

const WRITE_METHODS = new Set(['POST', 'PUT', 'PATCH', 'DELETE'])

/** Guard against concurrent 401 responses each triggering a redirect. */
let redirectingToLogin = false

/**
 * 401 的唯一处置:整页跳登录(带 redirect 回原路径),并返回一个永不 resolve 的 promise,
 * 让调用方停在「已经不在这一页了」的状态,不去渲染一份不存在的数据。
 */
function bailToLoginOn401(requestUrl: string): Promise<never> {
  if (!redirectingToLogin) {
    redirectingToLogin = true
    const currentPath =
      location.pathname +
      (location.search ? location.search : '') +
      (location.hash ? location.hash : '')
    const redirectTo = encodeURIComponent(currentPath)
    location.replace(`/login?redirect=${redirectTo}`)
  }
  void requestUrl
  return new Promise<never>(() => undefined)
}

/** 后端错误包络 `{ error: { code, message } }` → ApiError;不是这个形状就返回 null。 */
function apiErrorOf(body: unknown): ApiError | null {
  if (typeof body === 'object' && body !== null && 'error' in body) {
    const e = (body as { error: unknown }).error
    if (typeof e === 'object' && e !== null && 'code' in e && 'message' in e) {
      return e as ApiError
    }
  }
  return null
}

async function request<T>(
  url: string,
  options: RequestInit = {},
): Promise<T> {
  const method = (options.method ?? 'GET').toUpperCase()
  const headers = new Headers(options.headers)

  if (!headers.has('Content-Type') && !(options.body instanceof FormData)) {
    headers.set('Content-Type', 'application/json')
  }

  // Tell the backend which UI language to localize error messages into.
  // (The app's chosen locale, not the browser's Accept-Language default.)
  if (!headers.has('X-Pipewright-Locale')) {
    headers.set('X-Pipewright-Locale', currentLocale())
  }

  if (WRITE_METHODS.has(method)) {
    const csrfToken = getCookie('pipewright_csrf')
    if (csrfToken) {
      headers.set('X-CSRF-Token', csrfToken)
    }
  }

  let response: Response
  try {
    response = await fetch(url, { ...options, headers, credentials: 'same-origin' })
  } catch (err) {
    // Network error — backend may not be up yet
    throw new HttpError(0, null, err instanceof Error ? err.message : 'Network error')
  }

  // Auth endpoints (login / session / logout) own their 401 handling:
  // the login form shows an inline error, the route guard treats it as
  // "not logged in". Only redirect for 401s on *other* protected calls.
  const isAuthEndpoint = url.includes('/api/auth/')

  if (response.status === 401 && !isAuthEndpoint) {
    return bailToLoginOn401(url)
  }

  if (response.status === 204) {
    return undefined as T
  }

  let body: unknown
  const ct = response.headers.get('content-type') ?? ''
  if (ct.includes('application/json')) {
    body = await response.json()
  } else {
    body = await response.text()
  }

  if (!response.ok) {
    // Try to extract structured error envelope
    const apiError = apiErrorOf(body)
    throw new HttpError(
      response.status,
      apiError,
      apiError?.message ?? `HTTP ${response.status}`,
    )
  }

  return body as T
}

/**
 * GET 一个「一行一条 JSON」的流式响应(text/ndjson):每读到完整一行就回调一次,
 * 整轮结束才 resolve —— 服务端不必等最慢的那台(见后端 writeMetricsStream)。
 *
 * 鉴权/401/locale 与 request() 同源,免得下一个流式端点又各写一份。
 * 无 ReadableStream 的环境(老浏览器/测试)退回「读完整包再拆行」:内容一致,只是不实时。
 */
async function getLines(
  url: string,
  onLine: (line: string) => void,
  options: RequestInit = {},
): Promise<void> {
  const headers = new Headers(options.headers)
  if (!headers.has('X-Pipewright-Locale')) {
    headers.set('X-Pipewright-Locale', currentLocale())
  }

  let response: Response
  try {
    response = await fetch(url, { ...options, headers, method: 'GET', credentials: 'same-origin' })
  } catch (err) {
    throw new HttpError(0, null, err instanceof Error ? err.message : 'Network error')
  }

  if (response.status === 401) {
    await bailToLoginOn401(url)
  }

  if (!response.ok) {
    const text = await response.text().catch(() => '')
    let apiError: ApiError | null = null
    try {
      apiError = apiErrorOf(JSON.parse(text) as unknown)
    } catch {
      // 非 JSON 错误体(反代 502 之类),按空 apiError 抛状态码
    }
    throw new HttpError(response.status, apiError, apiError?.message ?? `HTTP ${response.status}`)
  }

  if (!response.body) {
    const text = await response.text()
    for (const line of text.split('\n')) {
      if (line.trim()) onLine(line.trim())
    }
    return
  }

  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buf = ''
  const drain = (final: boolean) => {
    let i = buf.indexOf('\n')
    while (i >= 0) {
      const line = buf.slice(0, i).trim()
      buf = buf.slice(i + 1)
      if (line) onLine(line)
      i = buf.indexOf('\n')
    }
    if (final && buf.trim()) onLine(buf.trim())
  }

  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    buf += decoder.decode(value, { stream: true })
    drain(false)
  }
  buf += decoder.decode()
  drain(true)
}

/** 请求体序列化:FormData 原样透传(浏览器自动带 boundary),其余 JSON 化。 */
function serializeBody(data: unknown): BodyInit | undefined {
  if (data === undefined) return undefined
  if (data instanceof FormData) return data
  if (typeof data === 'string') return data
  return JSON.stringify(data)
}

export const http = {
  get<T>(url: string, options?: RequestInit): Promise<T> {
    return request<T>(url, { ...options, method: 'GET' })
  },
  post<T>(url: string, data?: unknown, options?: RequestInit): Promise<T> {
    return request<T>(url, { ...options, method: 'POST', body: serializeBody(data) })
  },
  put<T>(url: string, data?: unknown, options?: RequestInit): Promise<T> {
    return request<T>(url, { ...options, method: 'PUT', body: serializeBody(data) })
  },
  patch<T>(url: string, data?: unknown, options?: RequestInit): Promise<T> {
    return request<T>(url, { ...options, method: 'PATCH', body: serializeBody(data) })
  },
  delete<T>(url: string, options?: RequestInit): Promise<T> {
    return request<T>(url, { ...options, method: 'DELETE' })
  },
  /** 逐行流式 GET(ndjson);见 getLines。 */
  getLines(url: string, onLine: (line: string) => void, options?: RequestInit): Promise<void> {
    return getLines(url, onLine, options ?? {})
  },
}
