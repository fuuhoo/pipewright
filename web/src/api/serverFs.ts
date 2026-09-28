/**
 * 远程文件面板(「远程」弹窗下半屏)的接口层。
 *
 * 与后端 internal/httpapi/server_fs.go 一一对应。五条路由都按 ActOperate 把关
 * (读主机文件与开终端等价,不是平台语义的「查看」),所以这里没有「只读用户可看」
 * 的降级路径 —— 没权限就是 403,界面按无远程能力处理。
 *
 * 下载不走 fetch:后端直接回 Content-Disposition 附件,交给浏览器原生下载最省事,
 * 也不会把大文件整份读进内存。因此 downloadUrl 只拼 URL,由 <a download> 触发。
 */

import { http } from './http'

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

/** 上传到指定目录;落点由服务端 join 消毒后的基名。 */
export async function uploadFsFile(
  serverId: string,
  dir: string,
  file: File,
): Promise<FsResult> {
  const form = new FormData()
  form.append('dir', dir)
  form.append('file', file)
  // Content-Type 交由浏览器按 FormData 边界自动设置(不手动指定)。
  return http.post<FsResult>(`${base(serverId)}/upload`, form)
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
