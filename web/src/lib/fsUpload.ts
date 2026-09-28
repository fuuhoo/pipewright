/**
 * 分块上传的纯逻辑 + 会话驱动器(视图只负责喂 File 与渲染进度)。
 *
 * 为什么不在组件里直接 fetch 完事:
 *   · 大文件必须切块 —— 一次 POST 要么整份成要么全废,断线/刷新页面就把前面几十 GB
 *     全部作废,而且浏览器也报不出「传到哪了」。
 *   · 断了要能续 —— 服务端记的是**远端实测偏移**,所以 409 offset_mismatch 不是失败,
 *     是「重新定位」:拿它给的数字接着推,已落地的字节一块不丢。
 *   · 网络抖要能扛 —— 可重试错误按指数退避重来,但先问一次 status 拿真实长度,
 *     不然本地簿记比对端多算的那几块会一直撞 409。
 *
 * 这里刻意只依赖结构化错误形状(status / apiError.code / body.offset)与注入的 deps,
 * 于是断线重连、退避、逐块推进这些真机难复现的路径都能在单测里跑。
 */

/** 后端单块请求体上限(internal/httpapi/fs_upload.go: fsChunkMaxBytes)。 */
export const FS_CHUNK_MAX_BYTES = 64 * 1024 * 1024

const MB = 1024 * 1024

/**
 * 块大小分档:块越大往返越少、越压得出带宽,但浏览器一次要交出 chunkSize × 并发数
 * 的内存,所以按文件体积放大而不是一路拉满。
 */
export function chunkSizeFor(size: number): number {
  if (!Number.isFinite(size) || size < 0) return 4 * MB
  if (size <= 32 * MB) return 4 * MB
  if (size <= 1024 * MB) return 8 * MB
  if (size <= 8192 * MB) return 16 * MB
  return 32 * MB
}

/** 下一块的范围;已传完回 null。末块按剩余长度收窄。 */
export function nextChunkRange(
  size: number,
  offset: number,
  chunkSize: number,
): { start: number; end: number } | null {
  if (offset >= size) return null
  const start = Math.max(0, Math.floor(offset))
  const step = Math.max(1, Math.min(Math.floor(chunkSize), FS_CHUNK_MAX_BYTES))
  return { start, end: Math.min(size, start + step) }
}

/**
 * 一个待传文件对驱动器暴露的最小面:相对落点、体积、按范围取字节。
 * 视图侧用 File.slice 实现,单测侧拿 Uint8Array 实现 —— 两条都只需这三样。
 *
 * 字节类型写死 `Uint8Array<ArrayBuffer>`:这些块要直接当 XHR 的请求体,而共享缓冲的
 * 视图(`Uint8Array` 的默认参数)XHR 不收 —— 在源头收紧,免得每层都补一次断言。
 */
export interface UploadFileSource {
  rel: string
  size: number
  slice(start: number, end: number): Promise<Uint8Array<ArrayBuffer>>
}

/** 浏览器选出来的文件(含 webkitdirectory 的整棵子树)。 */
export interface PickedFile {
  name: string
  size: number
  /** 文件夹选择时形如 `proj/src/a.c`(含顶层目录名);单文件选择时为空串。 */
  webkitRelativePath?: string
  slice(start: number, end: number): { arrayBuffer(): Promise<ArrayBuffer> }
}

/**
 * 相对落点:选了文件夹就保留整条相对路径(服务端逐段消毒后 join 到当前目录),
 * 单文件选择就只有文件名。没有 webkitRelativePath 的旧浏览器退回文件名,不猜路径。
 */
export function relOfPickedFile(file: PickedFile): string {
  const rel = (file.webkitRelativePath ?? '').trim()
  if (rel) return rel.replace(/\\/g, '/').replace(/^\.\/+/, '')
  return file.name
}

/** 把浏览器选中的文件折成驱动器要的形状。 */
export function uploadSourcesOf(files: PickedFile[]): UploadFileSource[] {
  return files.map((f) => ({
    rel: relOfPickedFile(f),
    size: f.size,
    slice: async (start: number, end: number) => new Uint8Array(await f.slice(start, end).arrayBuffer()),
  }))
}

/** 退避:第 n 次重试等多长(指数封顶,不抖出毫秒级噪声)。 */
export function backoffDelay(attempt: number, baseMs = 400, capMs = 6000): number {
  if (attempt <= 0) return baseMs
  return Math.min(capMs, baseMs * 2 ** Math.min(attempt - 1, 10))
}

/** 错误里的可续偏移:后端在这两种 409 里都给了「远端实测已经收到多少」。 */
export function uploadResumeHint(err: unknown): number | null {
  const e = err as { apiError?: { code?: string }; body?: { offset?: number } } | null
  const code = e?.apiError?.code
  if (code !== 'offset_mismatch' && code !== 'incomplete_upload') return null
  const offset = Number(e?.body?.offset)
  return Number.isFinite(offset) && offset >= 0 ? offset : 0
}

/** 值得重来的错误:网络断(status 0)、对端忙/超时、5xx,以及「前一块还在飞」这种纯竞态。 */
export function isRetryableUploadError(err: unknown): boolean {
  const e = err as { status?: number; apiError?: { code?: string } } | null
  if (!e || typeof e !== 'object') return false
  const code = e.apiError?.code ?? ''
  if (code === 'chunk_in_flight' || code === 'upload_timeout' || code === 'upload_busy') return true
  // 只认「明确的 HTTP 状态」:切片越界之类的本地异常没有 status,退避四次也传不上去,
  // 把它当断线重试只会把一份代码 bug 洗成「网络不好」。
  const status = typeof e.status === 'number' ? e.status : null
  if (status === null) return false
  if (status === 0 || status === 408 || status === 429) return true
  return status >= 500 && status <= 599
}

/** 错误码(界面按码出文案,不把后端英文原文糊上去)。 */
export function uploadErrorCodeOf(err: unknown): string {
  const e = err as { apiError?: { code?: string }; status?: number } | null
  if (e?.apiError?.code) return e.apiError.code
  if (typeof e?.status === 'number' && e.status === 0) return 'network'
  return 'upload_failed'
}

export type UploadItemStatus = 'queued' | 'uploading' | 'done' | 'error' | 'canceled'

/** 界面上一个文件的上传状态(纯数据,可直接渲染)。 */
export interface UploadItemState {
  key: string
  rel: string
  size: number
  offset: number
  status: UploadItemStatus
  error?: string
}

/** 会话对各端点的调用(注入进来才能测断线续传)。 */
export interface UploadSessionDeps {
  begin(dir: string, refs: Array<{ rel: string; size: number }>): Promise<UploadBeginResult>
  chunk(
    uploadId: string,
    offset: number,
    data: Uint8Array<ArrayBuffer>,
    onLoaded: (chunkBytes: number) => void,
    signal: AbortSignal,
  ): Promise<number>
  status(uploadId: string): Promise<number>
  complete(uploadId: string): Promise<void>
  abort(uploadId: string): Promise<void>
}

export interface UploadBeginResult {
  batchId: string
  files: Array<{ rel: string; uploadId: string; offset: number }>
}

export interface UploadSessionConfig {
  dir: string
  files: UploadFileSource[]
  deps: UploadSessionDeps
  onUpdate: (items: UploadItemState[]) => void
  /** 同时飞几个文件:同批次共用一条连接,两路能把往返 latency 藏掉一半。 */
  parallel?: number
  maxRetry?: number
  chunkSizeOf?: (size: number) => number
  sleep?: (ms: number) => Promise<void>
}

export interface UploadSessionTally {
  ok: number
  failed: number
  canceled: number
}

/** 驱动器交给视图的操作面:逐文件取消/重试、整批取消、等结束。 */
export interface UploadSessionHandle {
  items(): UploadItemState[]
  retry(key: string): void
  cancel(key: string): void
  cancelAll(): void
  finished: Promise<UploadSessionTally>
}

interface Intern {
  key: string
  rel: string
  size: number
  slice: (start: number, end: number) => Promise<Uint8Array<ArrayBuffer>>
  uploadId: string
  offset: number
  state: UploadItemState
  controller: AbortController | null
  canceled: boolean
}

const wait = (ms: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, ms))

/**
 * 开一个上传批次并驱动到结束。
 *
 * 三条不许改的行为:
 *   · 相对落点重复时服务端整批拒绝 —— 所以 begin 失败是「全批 error」,不是半批落地。
 *   · 一个文件报错后它的会话仍然活着(批次没全完成就不会被回收),因此 retry 拿得到
 *     原 uploadId,先问 status 再从那儿接着推。
 *   · 取消要清掉远端半成品:几 GB 的半截文件留在盘上没人认领,比传失败更糟。
 */
export function runUploadSession(cfg: UploadSessionConfig): UploadSessionHandle {
  const parallel = Math.max(1, Math.min(cfg.parallel ?? 2, 4))
  const maxRetry = Math.max(0, cfg.maxRetry ?? 4)
  const chunkSizeOf = cfg.chunkSizeOf ?? chunkSizeFor
  const sleep = cfg.sleep ?? wait

  const list: Intern[] = cfg.files.map((f, i) => {
    const key = String(i)
    return {
      key,
      rel: f.rel,
      size: f.size,
      slice: f.slice.bind(f),
      uploadId: '',
      offset: 0,
      controller: null,
      canceled: false,
      state: { key, rel: f.rel, size: f.size, offset: 0, status: 'queued' },
    }
  })

  let running = 0
  let resolved = false
  const queue = [...list]

  let resolveFinished: (t: UploadSessionTally) => void = () => undefined
  const finished = new Promise<UploadSessionTally>((resolve) => {
    resolveFinished = resolve
  })

  const emit = (): void => {
    cfg.onUpdate(list.map((it) => ({ ...it.state })))
  }

  /** 计数一律现算:retry 会把一项从终态拉回在传,增量维护的数字一定会说谎。 */
  const tallyNow = (): UploadSessionTally => {
    let ok = 0
    let failed = 0
    let canceled = 0
    for (const it of list) {
      if (it.state.status === 'done') ok++
      else if (it.state.status === 'error') failed++
      else if (it.state.status === 'canceled') canceled++
    }
    return { ok, failed, canceled }
  }

  /** 整批首次落定时 resolve 一次(之后的 retry 靠 onUpdate 渲染,不再重复 resolve)。 */
  const finish = (): void => {
    if (resolved || running > 0 || queue.length > 0) return
    resolved = true
    resolveFinished(tallyNow())
  }

  const markFailed = (it: Intern, code: string): void => {
    it.state.status = 'error'
    it.state.error = code
  }

  const markCanceled = (it: Intern): void => {
    it.state.status = 'canceled'
  }

  /** 把一个文件推到完:逐块 + 退避 + 按服务端给的偏移重新定位。 */
  const pump = async (it: Intern): Promise<void> => {
    if (!it.uploadId) {
      markFailed(it, 'upload_not_found')
      return
    }
    it.canceled = false
    it.state.status = 'uploading'
    it.state.error = undefined
    emit()

    let retries = 0
    for (;;) {
      if (it.canceled) break
      const range = nextChunkRange(it.size, it.offset, chunkSizeOf(it.size))
      if (!range) break
      try {
        const data = await it.slice(range.start, range.end)
        const controller = new AbortController()
        it.controller = controller
        const end = await cfg.deps.chunk(
          it.uploadId,
          range.start,
          data,
          (loaded) => {
            it.state.offset = Math.min(it.size, range.start + loaded)
            emit()
          },
          controller.signal,
        )
        it.controller = null
        it.offset = end
        it.state.offset = end
        retries = 0
        emit()
      } catch (err) {
        it.controller = null
        if (it.canceled) break
        // 409 带的是远端实测长度:这不是失败,是重新定位,不消耗重试次数。
        const hint = uploadResumeHint(err)
        if (hint !== null) {
          it.offset = hint
          it.state.offset = hint
          emit()
          continue
        }
        if (!isRetryableUploadError(err) || retries >= maxRetry) {
          markFailed(it, uploadErrorCodeOf(err))
          emit()
          return
        }
        retries++
        emit()
        await sleep(backoffDelay(retries))
        if (it.canceled) break
        // 断过线就不信本地簿记:问一次远端实测,从那儿接。
        const real = await cfg.deps.status(it.uploadId).catch(() => null)
        if (real !== null) {
          it.offset = real
          it.state.offset = real
        }
        emit()
      }
    }

    if (it.canceled) {
      await cfg.deps.abort(it.uploadId).catch(() => undefined)
      markCanceled(it)
      emit()
      return
    }
    // 零字节文件一块都不会发,complete 自己把它建出来。
    try {
      await cfg.deps.complete(it.uploadId)
    } catch (err) {
      markFailed(it, uploadErrorCodeOf(err))
      emit()
      return
    }
    it.state.status = 'done'
    it.state.offset = it.size
    emit()
  }

  const schedule = (): void => {
    while (running < parallel && queue.length > 0) {
      const it = queue.shift()
      if (!it) break
      if (it.state.status === 'done' || it.state.status === 'error') {
        finish()
        continue
      }
      running++
      void pump(it)
        .catch(() => {
          if (it.state.status !== 'done') markFailed(it, 'upload_failed')
        })
        .finally(() => {
          running--
          emit()
          schedule()
          finish()
        })
    }
    finish()
  }

  const retry = (key: string): void => {
    const it = list.find((x) => x.key === key)
    if (!it || (it.state.status !== 'error' && it.state.status !== 'canceled')) return
    it.state.status = 'queued'
    it.state.error = undefined
    it.offset = it.state.offset
    queue.unshift(it)
    emit()
    schedule()
  }

  const cancel = (key: string): void => {
    const it = list.find((x) => x.key === key)
    if (!it || it.state.status === 'done') return
    it.canceled = true
    if (it.state.status === 'queued') {
      const at = queue.findIndex((x) => x.key === key)
      if (at >= 0) queue.splice(at, 1)
      markCanceled(it)
      emit()
      finish()
      return
    }
    // 在飞的那一块掐掉:pump 会在 catch 里走取消分支,并清掉远端半成品。
    it.controller?.abort()
  }

  const items = (): UploadItemState[] => list.map((it) => ({ ...it.state }))

  /** begin 之后才知道每个文件的 uploadId:批次开不成就是整批失败(服务端也不会半落地)。 */
  const start = async (): Promise<void> => {
    try {
      const batch = await cfg.deps.begin(
        cfg.dir,
        list.map((it) => ({ rel: it.rel, size: it.size })),
      )
      for (const it of list) {
        const task = batch.files.find((f) => f.rel === it.rel)
        if (!task) {
          markFailed(it, 'upload_not_found')
          continue
        }
        it.uploadId = task.uploadId
        it.offset = task.offset
        it.state.offset = task.offset
      }
    } catch (err) {
      for (const it of list) {
        if (it.state.status === 'queued') markFailed(it, uploadErrorCodeOf(err))
      }
    }
    emit()
    schedule()
    finish()
  }

  void start()

  return { items, retry, cancel, cancelAll: () => list.forEach((it) => cancel(it.key)), finished }
}

/** 队列总进度(界面顶部那条):按字节加权,不是按文件数 —— 传 10 GB 和传 10 个空文件不等价。 */
export function sessionProgress(items: readonly UploadItemState[]): { bytes: number; total: number; ratio: number } {
  let bytes = 0
  let total = 0
  for (const it of items) {
    if (it.status === 'done') {
      bytes += it.size
      total += it.size
      continue
    }
    if (it.status === 'canceled') continue
    bytes += Math.min(it.offset, it.size)
    total += it.size
  }
  return { bytes, total, ratio: total > 0 ? bytes / total : 1 }
}
