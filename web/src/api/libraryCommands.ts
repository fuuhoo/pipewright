/**
 * 复用库指令 API — 命名、可复用的 shell 命令片段。
 *
 * 部署节点的「部署前指令 / 重启命令 / 探测命令」可以直接引用一条指令,运行时取正文
 * 并按 {{变量}} 渲染(与在字段里直接粘正文完全一致);引用优先,字段里残留的文字不执行。
 *
 *   GET    /api/library-commands       → { commands: LibraryCommand[] }
 *   POST   /api/library-commands       → LibraryCommand   (needs CSRF)
 *   GET    /api/library-commands/{id}  → LibraryCommand
 *   PUT    /api/library-commands/{id}  → LibraryCommand   (needs CSRF)
 *   DELETE /api/library-commands/{id}  → 204              (needs CSRF)
 *
 * 正文是 shell 片段,不含明文 secret(也就不能引用保险库凭据);secret 变量出现在命令里
 * 会在运行时被明确拒绝。
 */

import { http } from './http'

export interface LibraryCommand {
  id: string
  name: string
  description: string
  body: string
  createdAt: string
  updatedAt: string
}

export interface SaveLibraryCommandInput {
  name: string
  description?: string
  body: string
}

interface ListLibraryCommandsResponse {
  commands: LibraryCommand[]
}

export async function listLibraryCommands(): Promise<LibraryCommand[]> {
  const res = await http.get<ListLibraryCommandsResponse>('/api/library-commands')
  return res.commands ?? []
}

export async function getLibraryCommand(id: string): Promise<LibraryCommand> {
  return http.get<LibraryCommand>(`/api/library-commands/${encodeURIComponent(id)}`)
}

export async function createLibraryCommand(
  input: SaveLibraryCommandInput,
): Promise<LibraryCommand> {
  return http.post<LibraryCommand>('/api/library-commands', input)
}

export async function updateLibraryCommand(
  id: string,
  input: SaveLibraryCommandInput,
): Promise<LibraryCommand> {
  return http.put<LibraryCommand>(`/api/library-commands/${encodeURIComponent(id)}`, input)
}

export async function deleteLibraryCommand(id: string): Promise<void> {
  await http.delete<void>(`/api/library-commands/${encodeURIComponent(id)}`)
}
