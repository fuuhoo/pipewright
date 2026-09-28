/**
 * 分块上传的回归:切块与续传算术 + 会话驱动器的失败路径。
 *
 * 驱动器只认 deps 这个形状,所以这里用一个「真按偏移记账」的假远端:
 * 客户端报的 offset 和远端已收到的长度不一致就回 409 带实测值 —— 这正是真机
 * 断线重连后最容易被写错的那一条,不看住它就会静默把已传的字节重复追加进去。
 */
import { describe, it, expect } from 'vitest'
import {
  FS_CHUNK_MAX_BYTES,
  backoffDelay,
  chunkSizeFor,
  isRetryableUploadError,
  nextChunkRange,
  relOfPickedFile,
  runUploadSession,
  sessionProgress,
  uploadErrorCodeOf,
  uploadResumeHint,
  uploadSourcesOf,
} from './fsUpload'
import type { PickedFile, UploadBeginResult, UploadFileSource, UploadSessionDeps } from './fsUpload'

const MB = 1024 * 1024
const enc = new TextEncoder()

/** 后端那种 `{ status, error:{code}, body }` 形状(lib 只读这三样,不必引 api 层) */
function httpError(status: number, code?: string, body?: Record<string, unknown>): unknown {
  return { status, apiError: code ? { code, message: code } : null, body: body ?? null }
}

function source(rel: string, bytes: Uint8Array<ArrayBuffer>): UploadFileSource {
  return { rel, size: bytes.length, slice: async (s, e) => bytes.slice(s, e) }
}

/** 只用来走 rel 归一的那几个用例:slice 不参与,给个空壳就行 */
function picked(name: string, webkitRelativePath?: string): PickedFile {
  return { name, size: 0, webkitRelativePath, slice: () => ({ arrayBuffer: async () => new ArrayBuffer(0) }) }
}

/** 远端已存在文件的真实字节(驱动送错区间时,拼出来的内容就会对不上) */
interface Served {
  rel: string
  uploadId: string
  expected: Uint8Array<ArrayBuffer>
  got: number[]
  received: number
  attempts: number
  closed: boolean
  done: boolean
}

interface HarnessOptions {
  /** 每次 chunk 落地前调用:回非 null 就这次请求失败(远端不动) */
  onChunk?: (t: Served, offset: number, attempt: number) => unknown
  beginError?: unknown
  /** begin 响应里抹掉这个文件(服务端没收它) */
  dropRel?: string
  /** 远端已有 k 字节但 begin 报 0:模拟客户端簿记落后的续传 */
  seedRemote?: Record<string, number>
  /** 这个文件的 chunk 挂住不返回,只在 abort 时失败 */
  hangRel?: string
}

function makeHarness(
  files: Array<{ rel: string; bytes: Uint8Array<ArrayBuffer> }>,
  opts: HarnessOptions = {},
) {
  const tasks = new Map<string, Served>()
  const byRel = new Map<string, Served>()
  const calls = { begin: 0, chunk: 0, status: 0, complete: 0, abort: 0 }
  const sleeps: number[] = []
  const events: string[] = []
  /** 每次 chunk 请求声称的起点:重发送同一处就是「把已传的字节又传了一遍」 */
  const offsetLog: number[] = []
  const expected = new Map(files.map((f) => [f.rel, f.bytes]))

  const deps: UploadSessionDeps = {
    async begin(dir, refs): Promise<UploadBeginResult> {
      calls.begin++
      if (opts.beginError) throw opts.beginError
      void dir
      const out: UploadBeginResult['files'] = []
      refs.forEach((r, i) => {
        if (opts.dropRel === r.rel) return
        const t: Served = {
          rel: r.rel,
          uploadId: `u${i}`,
          expected: expected.get(r.rel) ?? new Uint8Array(),
          got: [],
          received: 0,
          attempts: 0,
          closed: false,
          done: false,
        }
        const seed = opts.seedRemote?.[r.rel] ?? 0
        if (seed > 0) {
          t.received = seed
          t.got = Array.from(t.expected.slice(0, seed))
        }
        tasks.set(t.uploadId, t)
        byRel.set(r.rel, t)
        out.push({ rel: r.rel, uploadId: t.uploadId, offset: 0 })
      })
      return { batchId: 'b1', files: out }
    },
    chunk(uploadId, offset, data, onLoaded, signal): Promise<number> {
      const t = tasks.get(uploadId)
      if (!t) return Promise.reject(httpError(404, 'upload_not_found'))
      calls.chunk++
      offsetLog.push(offset)
      events.push(`${t.rel}:chunk`)
      if (t.closed) return Promise.reject(httpError(409, 'upload_closed'))
      if (offset !== t.received) {
        return Promise.reject(httpError(409, 'offset_mismatch', { offset: t.received }))
      }
      if (opts.hangRel === t.rel) {
        return new Promise<number>((_resolve, reject) => {
          signal.addEventListener('abort', () => reject(httpError(0, 'aborted')), { once: true })
        })
      }
      const err = opts.onChunk?.(t, offset, ++t.attempts)
      if (err) return Promise.reject(err)
      // 分两段报进度,顺带验证界面读数不会报出超过文件长度的进度
      onLoaded(Math.floor(data.length / 2))
      onLoaded(data.length)
      t.got.push(...data)
      t.received += data.length
      return Promise.resolve(t.received)
    },
    async status(uploadId): Promise<number> {
      calls.status++
      return tasks.get(uploadId)?.received ?? 0
    },
    async complete(uploadId): Promise<void> {
      const t = tasks.get(uploadId)
      if (!t) throw httpError(404, 'upload_not_found')
      calls.complete++
      events.push(`${t.rel}:complete`)
      t.done = true
    },
    async abort(uploadId): Promise<void> {
      const t = tasks.get(uploadId)
      if (!t) return
      calls.abort++
      events.push(`${t.rel}:abort`)
      t.closed = true
      t.got = []
      t.received = 0
    },
  }

  const sources = files.map((f) => source(f.rel, f.bytes))
  return { deps, sources, calls, sleeps, events, offsetLog, byRel }
}

/** 驱动器全程用注入的 sleep,跑完只剩微任务:一个宏任务就能收干。 */
async function drain(ms = 0): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, ms))
}

describe('chunkSizeFor / nextChunkRange', () => {
  it('按体积分档,大文件才用大块', () => {
    expect(chunkSizeFor(0)).toBe(4 * MB)
    expect(chunkSizeFor(32 * MB)).toBe(4 * MB)
    expect(chunkSizeFor(33 * MB)).toBe(8 * MB)
    expect(chunkSizeFor(1024 * MB)).toBe(8 * MB)
    expect(chunkSizeFor(2048 * MB)).toBe(16 * MB)
    expect(chunkSizeFor(8192 * MB)).toBe(16 * MB)
    expect(chunkSizeFor(9000 * MB)).toBe(32 * MB)
  })

  it('垃圾体积回最小档,不去算出负数块', () => {
    expect(chunkSizeFor(-1)).toBe(4 * MB)
    expect(chunkSizeFor(Number.NaN)).toBe(4 * MB)
    expect(chunkSizeFor(Number.POSITIVE_INFINITY)).toBe(4 * MB)
  })

  it('末块按剩余长度收窄,传完回 null', () => {
    expect(nextChunkRange(10, 6, 4)).toEqual({ start: 6, end: 10 })
    expect(nextChunkRange(10, 10, 4)).toBeNull()
    expect(nextChunkRange(10, 12, 4)).toBeNull()
    expect(nextChunkRange(0, 0, 4)).toBeNull()
  })

  it('块大小不会越过后端单块上限,0/负数也不会卡死不动', () => {
    expect(nextChunkRange(200 * MB, 0, 128 * MB)).toEqual({ start: 0, end: FS_CHUNK_MAX_BYTES })
    expect(nextChunkRange(3, 0, 0)).toEqual({ start: 0, end: 1 })
    expect(nextChunkRange(3, 1.7, 4)).toEqual({ start: 1, end: 3 })
  })
})

describe('relOfPickedFile / uploadSourcesOf', () => {
  it('文件夹选择保留整条相对路径(含顶层目录名)', () => {
    expect(relOfPickedFile(picked('a.c', 'proj/src/a.c'))).toBe('proj/src/a.c')
  })

  it('Windows 分隔符与 ./ 前缀归一,免得落点分裂成两棵树', () => {
    expect(relOfPickedFile(picked('a.c', 'proj\\src\\a.c'))).toBe('proj/src/a.c')
    expect(relOfPickedFile(picked('a.c', './proj/a.c'))).toBe('proj/a.c')
  })

  it('单文件选择没有 webkitRelativePath:只按文件名落,不猜路径', () => {
    expect(relOfPickedFile(picked('notes.txt', ''))).toBe('notes.txt')
    expect(relOfPickedFile(picked('notes.txt'))).toBe('notes.txt')
  })

  it('uploadSourcesOf 把 slice 转成 Uint8Array,长度如实', async () => {
    const bytes = enc.encode('hello world')
    const [src] = uploadSourcesOf([
      {
        name: 'a.txt',
        size: bytes.length,
        slice: (s, e) => ({ arrayBuffer: async () => bytes.slice(s, e).buffer }),
      },
    ])
    expect(src.rel).toBe('a.txt')
    expect(src.size).toBe(bytes.length)
    expect(Array.from(await src.slice(0, 5))).toEqual(Array.from(enc.encode('hello')))
  })
})

describe('退避与错误分类', () => {
  it('指数退避封顶,不去抖出分钟级等待', () => {
    expect(backoffDelay(0)).toBe(400)
    expect(backoffDelay(1)).toBe(400)
    expect(backoffDelay(2)).toBe(800)
    expect(backoffDelay(3)).toBe(1600)
    expect(backoffDelay(30)).toBe(6000)
  })

  it('409 的两种码都读出权威偏移,缺字段当 0(重新定位而不是判死)', () => {
    expect(uploadResumeHint(httpError(409, 'offset_mismatch', { offset: 5 }))).toBe(5)
    expect(uploadResumeHint(httpError(409, 'incomplete_upload', { offset: 1024 }))).toBe(1024)
    expect(uploadResumeHint(httpError(409, 'incomplete_upload'))).toBe(0)
    expect(uploadResumeHint(httpError(409, 'upload_closed', { offset: 5 }))).toBeNull()
    expect(uploadResumeHint(new Error('boom'))).toBeNull()
    expect(uploadResumeHint(null)).toBeNull()
  })

  it('可重试:断线、忙、对端超时、5xx;4xx 语义错误不可重试', () => {
    expect(isRetryableUploadError(httpError(0))).toBe(true)
    expect(isRetryableUploadError(httpError(408))).toBe(true)
    expect(isRetryableUploadError(httpError(429))).toBe(true)
    expect(isRetryableUploadError(httpError(502))).toBe(true)
    expect(isRetryableUploadError(httpError(409, 'chunk_in_flight'))).toBe(true)
    expect(isRetryableUploadError(httpError(403, 'permission_denied'))).toBe(false)
    expect(isRetryableUploadError(httpError(400, 'invalid_path'))).toBe(false)
    expect(isRetryableUploadError(null)).toBe(false)
    // 没有 HTTP 状态的本地异常(切片越界、代码 bug)不去退避重试,免得洗成「网络不好」
    expect(isRetryableUploadError(new Error('boom'))).toBe(false)
  })

  it('错误码优先取后端的,断线归 network,其余归 upload_failed', () => {
    expect(uploadErrorCodeOf(httpError(500, 'upload_busy'))).toBe('upload_busy')
    expect(uploadErrorCodeOf(httpError(0))).toBe('network')
    expect(uploadErrorCodeOf(httpError(403))).toBe('upload_failed')
    expect(uploadErrorCodeOf(new Error('x'))).toBe('upload_failed')
  })
})

describe('会话驱动器', () => {
  it('两个文件分块传完:内容按块拼回原样,计数落定', async () => {
    const a = enc.encode('hello world!')
    const b = enc.encode('second file')
    const h = makeHarness(
      [
        { rel: 'a.txt', bytes: a },
        { rel: 'b.txt', bytes: b },
      ],
    )
    const seen: Array<{ rel: string; offset: number }> = []
    const handle = runUploadSession({
      dir: '/tmp/notes',
      files: h.sources,
      deps: h.deps,
      sleep: async () => undefined,
      chunkSizeOf: () => 5,
      onUpdate: (items) => items.forEach((it) => seen.push({ rel: it.rel, offset: it.offset })),
    })
    const tally = await handle.finished
    expect(tally).toEqual({ ok: 2, failed: 0, canceled: 0 })
    expect(Array.from(h.byRel.get('a.txt')!.got)).toEqual(Array.from(a))
    expect(Array.from(h.byRel.get('b.txt')!.got)).toEqual(Array.from(b))
    expect(h.calls.complete).toBe(2)
    expect(handle.items().every((it) => it.status === 'done')).toBe(true)
    // 4 字节步长的进度点确实出现过(界面那条细进度不是只在块边界才动)
    expect(seen.some((s) => s.rel === 'a.txt' && s.offset > 0 && s.offset < a.length)).toBe(true)
  })

  it('远端比本地多收了字节:409 给出权威偏移就接着传,不算失败也不退避', async () => {
    const bytes = enc.encode('hello world!')
    const h = makeHarness([{ rel: 'a.txt', bytes }], { seedRemote: { 'a.txt': 6 } })
    const handle = runUploadSession({
      dir: '/d',
      files: h.sources,
      deps: h.deps,
      sleep: async () => void h.sleeps.push(0),
      chunkSizeOf: () => 4,
      onUpdate: () => undefined,
    })
    expect(await handle.finished).toEqual({ ok: 1, failed: 0, canceled: 0 })
    expect(Array.from(h.byRel.get('a.txt')!.got)).toEqual(Array.from(bytes))
    // 重新定位不消耗重试次数:一次退避都不该发生
    expect(h.sleeps).toEqual([])
  })

  it('断线错误:退避后先问远端实测长度,再从那儿接', async () => {
    const bytes = enc.encode('hello world!')
    const h = makeHarness([{ rel: 'a.txt', bytes }], {
      onChunk: (t, _offset, attempt) => (attempt === 1 && t.rel === 'a.txt' ? httpError(0) : null),
    })
    const handle = runUploadSession({
      dir: '/d',
      files: h.sources,
      deps: h.deps,
      sleep: async (ms) => void h.sleeps.push(ms),
      chunkSizeOf: () => 4,
      onUpdate: () => undefined,
    })
    expect(await handle.finished).toEqual({ ok: 1, failed: 0, canceled: 0 })
    expect(Array.from(h.byRel.get('a.txt')!.got)).toEqual(Array.from(bytes))
    expect(h.calls.status).toBe(1)
    expect(h.sleeps).toEqual([400])
  })

  it('一直 5xx:重试到上限就判死,错误码留给界面', async () => {
    const h = makeHarness([{ rel: 'a.txt', bytes: enc.encode('abcdef') }], {
      onChunk: () => httpError(500),
    })
    const handle = runUploadSession({
      dir: '/d',
      files: h.sources,
      deps: h.deps,
      sleep: async () => undefined,
      maxRetry: 2,
      chunkSizeOf: () => 3,
      onUpdate: () => undefined,
    })
    expect(await handle.finished).toEqual({ ok: 0, failed: 1, canceled: 0 })
    expect(handle.items()[0]).toMatchObject({ status: 'error', error: 'upload_failed' })
    expect(h.calls.chunk).toBe(3) // 首块 + 2 次重试
    expect(h.calls.complete).toBe(0)
  })

  it('403 这类语义错误不重试,直接判死', async () => {
    const h = makeHarness([{ rel: 'a.txt', bytes: enc.encode('abcdef') }], {
      onChunk: () => httpError(403, 'permission_denied'),
    })
    const handle = runUploadSession({
      dir: '/d',
      files: h.sources,
      deps: h.deps,
      sleep: async () => undefined,
      onUpdate: () => undefined,
    })
    expect(await handle.finished).toEqual({ ok: 0, failed: 1, canceled: 0 })
    expect(handle.items()[0].error).toBe('permission_denied')
    expect(h.calls.chunk).toBe(1)
    expect(h.calls.status).toBe(0)
  })

  it('retry 拿原 uploadId 从断点接着传,已落地的字节不再重 send', async () => {
    const bytes = enc.encode('hello world!')
    const h = makeHarness([{ rel: 'a.txt', bytes }], {
      // 第二块撞 403:第一块(4 字节)已经在远端,界面 retry 必须从 4 续而不是从 0 重传
      onChunk: (t, _offset, attempt) =>
        t.rel === 'a.txt' && attempt === 2 ? httpError(403, 'permission_denied') : null,
    })
    const handle = runUploadSession({
      dir: '/d',
      files: h.sources,
      deps: h.deps,
      sleep: async () => undefined,
      chunkSizeOf: () => 4,
      onUpdate: () => undefined,
    })
    expect(await handle.finished).toEqual({ ok: 0, failed: 1, canceled: 0 })
    expect(h.byRel.get('a.txt')!.received).toBe(4)
    expect(handle.items()[0]).toMatchObject({ status: 'error', offset: 4 })

    handle.retry('0')
    await drain()
    expect(handle.items()[0].status).toBe('done')
    expect(Array.from(h.byRel.get('a.txt')!.got)).toEqual(Array.from(bytes))
    expect(h.offsetLog.filter((o) => o === 0)).toHaveLength(1)
    expect(h.offsetLog).toEqual([0, 4, 4, 8])
  })

  it('取消在飞的那一块:远端半成品要清掉', async () => {
    const h = makeHarness([{ rel: 'a.txt', bytes: enc.encode('hello world!') }], { hangRel: 'a.txt' })
    const handle = runUploadSession({
      dir: '/d',
      files: h.sources,
      deps: h.deps,
      sleep: async () => undefined,
      chunkSizeOf: () => 4,
      onUpdate: () => undefined,
    })
    await drain()
    handle.cancel('0')
    expect(await handle.finished).toEqual({ ok: 0, failed: 0, canceled: 1 })
    expect(handle.items()[0].status).toBe('canceled')
    expect(h.calls.abort).toBe(1)
    expect(h.byRel.get('a.txt')!.received).toBe(0)
    expect(h.calls.complete).toBe(0)
  })

  it('排队中的文件取消:摘出队列即了结,不去动远端', async () => {
    const h = makeHarness(
      [
        { rel: 'a.txt', bytes: enc.encode('aaaa') },
        { rel: 'b.txt', bytes: enc.encode('bbbb') },
      ],
      { hangRel: 'a.txt' },
    )
    const handle = runUploadSession({
      dir: '/d',
      parallel: 1,
      files: h.sources,
      deps: h.deps,
      sleep: async () => undefined,
      onUpdate: () => undefined,
    })
    await drain()
    handle.cancel('1') // b 还在排队
    expect(handle.items()[1].status).toBe('canceled')
    expect(h.calls.abort).toBe(0)
    handle.cancel('0') // 掐掉在飞的 a,整批才落定
    expect(await handle.finished).toEqual({ ok: 0, failed: 0, canceled: 2 })
  })

  it('零字节文件一块都不发,由 complete 把它建出来', async () => {
    const h = makeHarness([{ rel: 'empty', bytes: new Uint8Array() }])
    const handle = runUploadSession({
      dir: '/d',
      files: h.sources,
      deps: h.deps,
      sleep: async () => undefined,
      onUpdate: () => undefined,
    })
    expect(await handle.finished).toEqual({ ok: 1, failed: 0, canceled: 0 })
    expect(h.calls.chunk).toBe(0)
    expect(h.calls.complete).toBe(1)
  })

  it('开批失败 = 整批 error(服务端也不会半落地)', async () => {
    const h = makeHarness(
      [
        { rel: 'a.txt', bytes: enc.encode('a') },
        { rel: 'b.txt', bytes: enc.encode('b') },
      ],
      { beginError: httpError(429, 'too_many_files') },
    )
    const handle = runUploadSession({
      dir: '/d',
      files: h.sources,
      deps: h.deps,
      sleep: async () => undefined,
      onUpdate: () => undefined,
    })
    expect(await handle.finished).toEqual({ ok: 0, failed: 2, canceled: 0 })
    expect(handle.items().every((it) => it.error === 'too_many_files')).toBe(true)
    expect(h.calls.chunk).toBe(0)
  })

  it('begin 少回一个文件:那项判死,其余照传', async () => {
    const h = makeHarness(
      [
        { rel: 'a.txt', bytes: enc.encode('aaa') },
        { rel: 'b.txt', bytes: enc.encode('bbb') },
      ],
      { dropRel: 'b.txt' },
    )
    const handle = runUploadSession({
      dir: '/d',
      files: h.sources,
      deps: h.deps,
      sleep: async () => undefined,
      onUpdate: () => undefined,
    })
    expect(await handle.finished).toEqual({ ok: 1, failed: 1, canceled: 0 })
    const items = handle.items()
    expect(items[1]).toMatchObject({ rel: 'b.txt', error: 'upload_not_found' })
    expect(items[0].status).toBe('done')
  })

  it('parallel=1 时严格串行:前一个文件完事才开下一个', async () => {
    const h = makeHarness(
      [
        { rel: 'a.txt', bytes: enc.encode('aaaa') },
        { rel: 'b.txt', bytes: enc.encode('bbbb') },
      ],
    )
    const handle = runUploadSession({
      dir: '/d',
      parallel: 1,
      files: h.sources,
      deps: h.deps,
      sleep: async () => undefined,
      onUpdate: () => undefined,
    })
    await handle.finished
    expect(h.events.indexOf('b.txt:chunk')).toBeGreaterThan(h.events.indexOf('a.txt:complete'))
  })

  it('finished 只落定一次:retry 之后的进展走 onUpdate,不再重复 resolve', async () => {
    const h = makeHarness([{ rel: 'a.txt', bytes: enc.encode('ab') }], {
      onChunk: (t, _o, attempt) => (attempt === 1 ? httpError(403, 'permission_denied') : null),
    })
    let resolutions = 0
    const handle = runUploadSession({
      dir: '/d',
      files: h.sources,
      deps: h.deps,
      sleep: async () => undefined,
      onUpdate: () => undefined,
    })
    void handle.finished.then(() => {
      resolutions++
    })
    await drain()
    expect(resolutions).toBe(1)
    handle.retry('0')
    await drain()
    expect(resolutions).toBe(1)
    expect(handle.items()[0].status).toBe('done')
  })
})

describe('sessionProgress', () => {
  it('按字节加权:传 10 GB 和传 10 个空文件不等价', () => {
    const p = sessionProgress([
      { key: '0', rel: 'big', size: 90, offset: 90, status: 'done' },
      { key: '1', rel: 'mid', size: 10, offset: 5, status: 'uploading' },
    ])
    expect(p).toEqual({ bytes: 95, total: 100, ratio: 0.95 })
  })

  it('取消项不计入分母,读数为 0 长度批次时回 1 而不是 NaN', () => {
    expect(sessionProgress([{ key: '0', rel: 'x', size: 10, offset: 4, status: 'canceled' }])).toEqual({
      bytes: 0,
      total: 0,
      ratio: 1,
    })
    expect(sessionProgress([]).ratio).toBe(1)
  })

  it('offset 越界不会报出超过 100% 的进度', () => {
    const p = sessionProgress([{ key: '0', rel: 'x', size: 10, offset: 40, status: 'uploading' }])
    expect(p.ratio).toBe(1)
    expect(p.bytes).toBe(10)
  })
})
