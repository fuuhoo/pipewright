/**
 * 远程文件面板(「远程」弹窗下半屏)的接口层。
 *
 * 与后端 internal/httpapi/server_fs.go / fs_upload.go 一一对应。每条路由都按
 * ActOperate 把关(读主机文件与开终端等价,不是平台语义的「查看」),所以这里没有
 * 「只读用户可看」的降级路径 —— 没权限就是 403,界面按无远程能力处理。
 *
 * 下载不走 fetch:后端直接回 Content-Disposition 附件,交给浏览器原生下载最省事,
 * 也不会把大文件整份读进内存。因此 fsDownloadUrl 只拼 URL,由 <a download> 触发。
 *
 * 上传是分块的(begin / chunk / status / complete / abort)。逐块要真实进度且请求体是
 * 裸字节,fetch 报不出上传进度,所以那一条走 XHR;其余仍是 http.*。
 */

import { csrfToken, HttpError, http } from './http'
import { currentLocale } from '../i18n'
import type { UploadSessionDeps } from '../lib/fsUpload'

/** 远端一个路径的可见属性(与 target.FileStat 的 json tag 对齐)。 */
export interface FsEntry {
  name: string
  path: string
  isDir: boolean
  isLink: boolean
  linkTarget?: string
  size: number
  mode: number
  /** Unix 秒;对端没给时间为 0。 */
  mtime: number
}

/** GET .../fs:目录带 entries,单个文件带 entry。 */
export interface FsListing {
  path: string
  backend: string
  isDir: boolean
  entries: FsEntry[]
  entry?: FsEntry
  serverId: string
}

/** GET .../fs/content:文本正文。binary=true 时 content 恒空。 */
export interface FsContent {
  path: string
  content: string
  /** 本次返回的正文字节数,不是远端文件大小;是否截断看 truncated。 */
  size: number
  truncated: boolean
  binary: boolean
}

/** 写类操作的统一回执。 */
export interface FsResult {
  ok: boolean
  path?: string
  bytes?: number
  backend?: string
}

export type FsOpKind = 'mkdir' | 'remove' | 'rename'

function base(serverId: string): string {
  return `/api/servers/${encodeURIComponent(serverId)}/fs`
}

/** 列目录;path 传空串 = 该 SSH 会话的家目录。 */
export function listFs(serverId: string, path: string): Promise<FsListing> {
  const q = path ? `?path=${encodeURIComponent(path)}` : ''
  return http.get<FsListing>(`${base(serverId)}${q}`)
}

/** 取文本正文(编辑器用);上限 1 MiB,超出由 truncated 如实标出。 */
export function readFsContent(serverId: string, path: string): Promise<FsContent> {
  return http.get<FsContent>(`${base(serverId)}/content?path=${encodeURIComponent(path)}`)
}

/** 保存文本正文(覆盖写)。 */
export function writeFsContent(
  serverId: string,
  path: string,
  content: string,
): Promise<FsResult> {
  return http.post<FsResult>(`${base(serverId)}/content`, { path, content })
}

/** 上传批次里的一个文件(相对落点由界面给,服务端逐段消毒后 join 到 dir)。 */
export interface FsUploadFileRef {
  rel: string
  size: number
}

/** 一个上传任务:uploadId 是续传的门把,offset 是服务端已确认的字节数。 */
export interface FsUploadTask {
  rel: string
  uploadId: string
  path: string
  offset: number
  size: number
  done?: boolean
}

/** begin / chunk / status 的公共响应形状。 */
export interface FsUploadBatch {
  ok: boolean
  batchId: string
  dir?: string
  backend?: string
  files: FsUploadTask[]
  offset?: number
  error?: { code: string; message: string }
}

/** 开批次:服务端在这一次握手里建好父目录,并回每个文件的 uploadId。 */
export function fsUploadBegin(
  serverId: string,
  dir: string,
  files: FsUploadFileRef[],
): Promise<FsUploadBatch> {
  return http.post<FsUploadBatch>(`${base(serverId)}/upload/begin`, { dir, files })
}

/** 问一个文件在远端**实测**到了哪(断线重连后靠它决定从哪续)。 */
export async function fsUploadStatus(serverId: string, uploadId: string): Promise<number> {
  const dto = await http.get<FsUploadBatch>(
    `${base(serverId)}/upload/status?uploadId=${encodeURIComponent(uploadId)}`,
  )
  return Number(dto.offset ?? dto.files?.[0]?.offset ?? 0)
}

export function fsUploadComplete(serverId: string, uploadId: string): Promise<FsResult> {
  return http.post<FsResult>(`${base(serverId)}/upload/complete`, { uploadId })
}

export function fsUploadAbort(serverId: string, uploadId: string): Promise<FsResult> {
  return http.post<FsResult>(`${base(serverId)}/upload/abort`, { uploadId })
}

/**
 * 推一块裸字节,回服务端确认后的末尾偏移。
 *
 * 为什么是 XHR 而不是 fetch:上传进度只有 xhr.upload.onprogress 给得出真实值
 * (fetch 要自己包一层 ReadableStream,块大小一不对就报出假进度)。
 * 409 offset_mismatch 不是异常路径 —— 它带着权威偏移,调用方据此续传。
 */
export function fsUploadChunk(
  serverId: string,
  uploadId: string,
  offset: number,
  data: Uint8Array<ArrayBuffer>,
  onLoaded: (chunkBytes: number) => void,
  signal: AbortSignal,
): Promise<number> {
  const url =
    `${base(serverId)}/upload/chunk?uploadId=${encodeURIComponent(uploadId)}&offset=${offset}`
  return new Promise<number>((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open('PUT', url, true)
    xhr.withCredentials = true
    xhr.setRequestHeader('Content-Type', 'application/octet-stream')
    xhr.setRequestHeader('X-Pipewright-Locale', currentLocale())
    const csrf = csrfToken()
    if (csrf) xhr.setRequestHeader('X-CSRF-Token', csrf)

    const cleanup = (): void => {
      signal.removeEventListener('abort', onAbort)
    }
    const onAbort = (): void => xhr.abort()
    signal.addEventListener('abort', onAbort)

    xhr.upload.onprogress = (event) => {
      if (event.lengthComputable) onLoaded(event.loaded)
    }
    xhr.onload = () => {
      cleanup()
      let body: FsUploadBatch | null = null
      try {
        body = JSON.parse(xhr.responseText) as FsUploadBatch
      } catch {
        body = null
      }
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve(Number(body?.offset ?? 0))
        return
      }
      const apiError = body?.error ?? null
      reject(
        new HttpError(
          xhr.status,
          apiError,
          apiError?.message ?? `HTTP ${xhr.status}`,
          (body ?? null) as unknown,
        ),
      )
    }
    xhr.onerror = () => {
      cleanup()
      reject(new HttpError(0, null, 'network error'))
    }
    xhr.ontimeout = () => {
      cleanup()
      reject(new HttpError(0, null, 'timeout'))
    }
    xhr.onabort = () => {
      cleanup()
      reject(new HttpError(0, null, 'aborted'))
    }
    xhr.send(data)
  })
}

/** 建目录 / 删除 / 改名。rename 必须给 to。 */
export function fsOp(
  serverId: string,
  op: FsOpKind,
  path: string,
  to?: string,
): Promise<FsResult> {
  return http.post<FsResult>(`${base(serverId)}/op`, { op, path, to: to ?? '' })
}

/** 浏览器原生下载的 URL(带会话 Cookie,由后端回 Content-Disposition)。 */
export function fsDownloadUrl(serverId: string, path: string): string {
  return `${base(serverId)}/download?path=${encodeURIComponent(path)}`
}

/**
 * 把五个上传端点折成会话驱动器要的依赖面(lib/fsUpload 只认这个形状,不认 fetch)。
 * 视图因此只需要 `runUploadSession({ deps: fsUploadDeps(serverId), ... })`。
 */
export function fsUploadDeps(serverId: string): UploadSessionDeps {
  return {
    begin: async (dir, refs) => {
      const batch = await fsUploadBegin(serverId, dir, refs)
      return {
        batchId: batch.batchId,
        files: (batch.files ?? []).map((f) => ({ rel: f.rel, uploadId: f.uploadId, offset: Number(f.offset ?? 0) })),
      }
    },
    chunk: (uploadId, offset, data, onLoaded, signal) =>
      fsUploadChunk(serverId, uploadId, offset, data, onLoaded, signal),
    status: (uploadId) => fsUploadStatus(serverId, uploadId),
    complete: async (uploadId) => {
      await fsUploadComplete(serverId, uploadId)
    },
    abort: async (uploadId) => {
      await fsUploadAbort(serverId, uploadId)
    },
  }
}
