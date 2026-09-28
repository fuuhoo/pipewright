import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { http, HttpError } from './http'

/** Build a minimal Response-like object the wrapper understands. */
function jsonResponse(status: number, body: unknown, ok = status < 400): Response {
  return {
    status,
    ok,
    headers: new Headers({ 'content-type': 'application/json' }),
    json: async () => body,
    text: async () => JSON.stringify(body),
  } as unknown as Response
}

function textResponse(status: number, text: string): Response {
  return {
    status,
    ok: status < 400,
    headers: new Headers({ 'content-type': 'text/plain' }),
    json: async () => {
      throw new Error('not json')
    },
    text: async () => text,
  } as unknown as Response
}

describe('http client', () => {
  let fetchMock: ReturnType<typeof vi.fn>

  beforeEach(() => {
    fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    // Clear cookies
    document.cookie.split(';').forEach((c) => {
      const name = c.split('=')[0].trim()
      if (name) document.cookie = `${name}=; expires=Thu, 01 Jan 1970 00:00:00 GMT`
    })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('sends GET without a body and parses JSON', async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { username: 'admin' }))
    const data = await http.get<{ username: string }>('/api/auth/session')
    expect(data).toEqual({ username: 'admin' })

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/auth/session')
    expect(init.method).toBe('GET')
    expect(init.credentials).toBe('same-origin')
  })

  it('sets Content-Type application/json for POST with a body', async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { ok: true }))
    await http.post('/api/projects', { name: 'x' })
    const init = fetchMock.mock.calls[0][1]
    expect((init.headers as Headers).get('Content-Type')).toBe('application/json')
    expect(init.body).toBe(JSON.stringify({ name: 'x' }))
  })

  it('attaches X-CSRF-Token from the pipewright_csrf cookie on write methods', async () => {
    document.cookie = 'pipewright_csrf=tok123'
    fetchMock.mockResolvedValue(jsonResponse(200, {}))
    await http.post('/api/x', { a: 1 })
    const headers = fetchMock.mock.calls[0][1].headers as Headers
    expect(headers.get('X-CSRF-Token')).toBe('tok123')
  })

  it('does NOT attach CSRF header on GET (read method)', async () => {
    document.cookie = 'pipewright_csrf=tok123'
    fetchMock.mockResolvedValue(jsonResponse(200, {}))
    await http.get('/api/x')
    const headers = fetchMock.mock.calls[0][1].headers as Headers
    expect(headers.get('X-CSRF-Token')).toBeNull()
  })

  it('returns undefined for 204 No Content', async () => {
    fetchMock.mockResolvedValue({
      status: 204,
      ok: true,
      headers: new Headers(),
    } as unknown as Response)
    const res = await http.delete('/api/x')
    expect(res).toBeUndefined()
  })

  it('parses the canonical { error: { code, message } } envelope on non-2xx', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(400, { error: { code: 'invalid_input', message: '字段缺失' } }, false),
    )
    await expect(http.post('/api/x', {})).rejects.toMatchObject({
      status: 400,
      apiError: { code: 'invalid_input', message: '字段缺失' },
      message: '字段缺失',
    })
  })

  it('falls back to HTTP <status> message when no error envelope is present', async () => {
    fetchMock.mockResolvedValue(jsonResponse(500, { something: 'else' }, false))
    try {
      await http.get('/api/x')
      throw new Error('should have thrown')
    } catch (err) {
      expect(err).toBeInstanceOf(HttpError)
      expect((err as HttpError).status).toBe(500)
      expect((err as HttpError).apiError).toBeNull()
      expect((err as HttpError).message).toBe('HTTP 500')
    }
  })

  it('wraps network failures as HttpError status 0', async () => {
    fetchMock.mockRejectedValue(new TypeError('Failed to fetch'))
    try {
      await http.get('/api/x')
      throw new Error('should have thrown')
    } catch (err) {
      expect(err).toBeInstanceOf(HttpError)
      expect((err as HttpError).status).toBe(0)
      expect((err as HttpError).message).toBe('Failed to fetch')
    }
  })

  it('reads a plain-text body when content-type is not JSON', async () => {
    fetchMock.mockResolvedValue(textResponse(200, 'pong'))
    const res = await http.get<string>('/healthz')
    expect(res).toBe('pong')
  })

  it('does NOT redirect on 401 from an /api/auth/ endpoint (auth owns its 401)', async () => {
    fetchMock.mockResolvedValue(jsonResponse(401, { error: { code: 'unauthorized', message: '未登录' } }, false))
    await expect(http.post('/api/auth/login', {})).rejects.toBeInstanceOf(HttpError)
  })
})

// 回归:v6.2 联调发现 http.post 曾无条件 JSON.stringify,把 FormData 序列化成
// "[object FormData]" 且 Content-Type 不是 multipart → 后端 ParseMultipartForm
// 直接 400。config_profiles 上传端点因此完全不可用。
describe('FormData body passthrough', () => {
  it('post() sends FormData as-is (no JSON.stringify)', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      }),
    )
    vi.stubGlobal('fetch', fetchMock)

    const form = new FormData()
    form.append('file', new Blob(['x']), 'a.npmrc')
    form.append('language', 'node')

    await http.post('/api/admin/config-profiles/upload', form)

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [, init] = fetchMock.mock.calls[0]
    // body 必须是原 FormData,不能被字符串化
    expect(init.body).toBe(form)
    // Content-Type 不手动指定(浏览器负责带 boundary)
    const headers = init.headers as Headers
    expect(headers.get('Content-Type')).toBeNull()
    vi.unstubAllGlobals()
  })

  it('post() still JSON-stringifies plain objects', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      }),
    )
    vi.stubGlobal('fetch', fetchMock)

    await http.post('/api/x', { a: 1 })

    const [, init] = fetchMock.mock.calls[0]
    expect(init.body).toBe('{"a":1}')
    expect((init.headers as Headers).get('Content-Type')).toBe('application/json')
    vi.unstubAllGlobals()
  })
})

// getLines:服务器状态页的逐台流式指标靠它。坑都在分块边界上 —— 一行 JSON 可能被 TCP
// 拆成两块、多字节字符可能正好被劈开、最后一行可能不带换行。
describe('http.getLines (ndjson streaming)', () => {
  function streamResponse(chunks: Uint8Array[]): Response {
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        for (const c of chunks) controller.enqueue(c)
        controller.close()
      },
    })
    return new Response(stream, {
      status: 200,
      headers: { 'content-type': 'application/x-ndjson' },
    })
  }

  function bytes(...lines: string[]): Uint8Array[] {
    const enc = new TextEncoder()
    return lines.map((l) => enc.encode(l))
  }

  async function collect(url = '/api/x'): Promise<string[]> {
    const got: string[] = []
    await http.getLines(url, (line) => got.push(line))
    return got
  }

  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn())
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('reassembles a line split across chunks and drops the trailing newline', async () => {
    ;(globalThis.fetch as ReturnType<typeof vi.fn>).mockResolvedValue(
      streamResponse(bytes('{"a":1}\n{"b"', ':2}\n')),
    )
    expect(await collect()).toEqual(['{"a":1}', '{"b":2}'])
  })

  it('flushes the last line even when the stream ends without a newline', async () => {
    ;(globalThis.fetch as ReturnType<typeof vi.fn>).mockResolvedValue(
      streamResponse(bytes('one\n', 'two')),
    )
    expect(await collect()).toEqual(['one', 'two'])
  })

  it('keeps utf-8 text intact when a multi-byte char is split between chunks', async () => {
    const enc = new TextEncoder()
    const line = enc.encode('{"host":"北京-1"}\n')
    // 劈进「北」(E5 8C 97)三字节中间:第一块只含首字节,单独 decode 会出半个字符。
    const cut = line.findIndex((b) => b >= 0x80) + 1
    expect(cut).toBeGreaterThan(0)
    ;(globalThis.fetch as ReturnType<typeof vi.fn>).mockResolvedValue(
      streamResponse([line.slice(0, cut), line.slice(cut)]),
    )
    expect(await collect()).toEqual(['{"host":"北京-1"}'])
  })

  it('skips blank lines (keep-alive newlines must not reach the caller)', async () => {
    ;(globalThis.fetch as ReturnType<typeof vi.fn>).mockResolvedValue(
      streamResponse(bytes('\n', 'a\n\n', '\nb\r\n')),
    )
    expect(await collect()).toEqual(['a', 'b'])
  })

  it('requests with the locale header and same-origin credentials', async () => {
    const fetchMock = vi.fn().mockResolvedValue(streamResponse(bytes('a\n')))
    vi.stubGlobal('fetch', fetchMock)
    await http.getLines('/api/servers/metrics?stream=1', () => undefined)
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/servers/metrics?stream=1')
    expect(init.method).toBe('GET')
    expect(init.credentials).toBe('same-origin')
    expect((init.headers as Headers).get('X-Pipewright-Locale')).toBeTruthy()
  })

  it('surfaces a non-ok stream as HttpError with the error envelope message', async () => {
    ;(globalThis.fetch as ReturnType<typeof vi.fn>).mockResolvedValue(
      new Response(JSON.stringify({ error: { code: 'unauthorized', message: '请先登录' } }), {
        status: 403,
        headers: { 'content-type': 'application/json' },
      }),
    )
    await expect(collect()).rejects.toMatchObject({
      status: 403,
      message: '请先登录',
    })
  })

  it('falls back to buffering the whole body when ReadableStream is unavailable', async () => {
    // jsdom 之外的老环境里 response.body 为 null:内容必须一致,只是不实时。
    ;(globalThis.fetch as ReturnType<typeof vi.fn>).mockResolvedValue({
      status: 200,
      ok: true,
      headers: new Headers({ 'content-type': 'application/x-ndjson' }),
      body: null,
      text: async () => 'a\nb\n',
    } as unknown as Response)
    expect(await collect()).toEqual(['a', 'b'])
  })
})
