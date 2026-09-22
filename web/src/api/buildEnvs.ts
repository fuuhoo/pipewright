/**
 * Build environment (构建环境) API — v6.2 §3.1 / §5.2.
 *
 * 管理员端点(RequireAdmin):
 *   GET    /api/admin/build-envs            → { items: BuildEnv[] }
 *   POST   /api/admin/build-envs            → BuildEnv
 *   GET    /api/admin/build-envs/:id        → BuildEnv
 *   PUT    /api/admin/build-envs/:id        → BuildEnv
 *   DELETE /api/admin/build-envs/:id        → 204
 *   POST   /api/admin/build-envs/:id/toggle → { enabled, status }
 *   POST   /api/admin/build-envs/:id/check  → { status, error }
 *   POST   /api/admin/build-envs/:id/pull   → 202 { status: 'checking', error, output }
 *   POST   /api/admin/build-envs/check-all  → { ok, total }
 *
 * 普通用户端点(RequireUser):
 *   GET    /api/build-envs/languages        → { languages: string[] }
 *
 * 注意:check 同步执行(默认 60s 超时);pull 为异步 —— 后端立即返回 checking,
 * docker pull 在后台跑(默认 240s 上限),前端轮询列表直到状态落定。
 */

import { http } from './http'

/** 镜像来源:官方短名 / 任意自定义地址。系统不拼接地址(R8)。 */
export type BuildEnvSourceType = 'official' | 'custom'

/** 镜像检查状态。available=本地已有;pullable=registry 有、可拉取;unavailable=都不可用。 */
export type ImageCheckStatus = 'unchecked' | 'checking' | 'available' | 'pullable' | 'unavailable'

export interface BuildEnv {
  id: string
  language: string
  version: string
  displayName: string
  description: string
  sourceType: BuildEnvSourceType
  image: string
  credentialId: string
  imageCheckStatus: ImageCheckStatus
  imageCheckError: string
  /** RFC3339;未检查过为 null。 */
  imageCheckedAt: string | null
  enabled: boolean
  sortOrder: number
  createdBy: string
  createdAt: string
  updatedAt: string
}

export interface BuildEnvInput {
  language: string
  version: string
  displayName: string
  description?: string
  sourceType: BuildEnvSourceType
  image: string
  credentialId?: string
  sortOrder?: number
  enabled?: boolean
}

export interface ToggleResult {
  enabled: boolean
  status: ImageCheckStatus
}

export interface CheckResult {
  status: ImageCheckStatus
  error: string
  output?: string
}

export interface CheckAllResult {
  ok: number
  total: number
}

interface ListEnvelope {
  items: BuildEnv[]
}

export async function listBuildEnvs(params?: {
  language?: string
  sourceType?: string
  includeDisabled?: boolean
}): Promise<BuildEnv[]> {
  const q = new URLSearchParams()
  if (params?.language) q.set('language', params.language)
  if (params?.sourceType) q.set('sourceType', params.sourceType)
  if (params?.includeDisabled) q.set('includeDisabled', '1')
  const qs = q.toString()
  const res = await http.get<ListEnvelope>(
    `/api/admin/build-envs${qs ? `?${qs}` : ''}`,
  )
  return res.items ?? []
}

export async function getBuildEnv(id: string): Promise<BuildEnv> {
  return http.get<BuildEnv>(`/api/admin/build-envs/${id}`)
}

export async function createBuildEnv(input: BuildEnvInput): Promise<BuildEnv> {
  return http.post<BuildEnv>('/api/admin/build-envs', input)
}

export async function updateBuildEnv(id: string, input: BuildEnvInput): Promise<BuildEnv> {
  return http.put<BuildEnv>(`/api/admin/build-envs/${id}`, input)
}

export async function deleteBuildEnv(id: string): Promise<void> {
  return http.delete<void>(`/api/admin/build-envs/${id}`)
}

/** 启用/禁用。unchecked → 409 IMAGE_NOT_CHECKED;unavailable → 409 IMAGE_UNAVAILABLE(P0 #4)。 */
export async function toggleBuildEnv(id: string, enabled: boolean): Promise<ToggleResult> {
  return http.post<ToggleResult>(`/api/admin/build-envs/${id}/toggle`, { enabled })
}

/** 手动检查镜像(docker manifest inspect,默认 60s 超时)。 */
export async function checkBuildEnv(id: string): Promise<CheckResult> {
  return http.post<CheckResult>(`/api/admin/build-envs/${id}/check`, {})
}

/** 手动拉取镜像(异步):202 立即返回 status=checking;同镜像重复触发返回 409。 */
export async function pullBuildEnv(id: string): Promise<CheckResult> {
  return http.post<CheckResult>(`/api/admin/build-envs/${id}/pull`, {})
}

/** 一键检查全部(并发上限由 PIPEWRIGHT_CHECK_CONCURRENCY 控制,默认 10)。 */
export async function checkAllBuildEnvs(): Promise<CheckAllResult> {
  return http.post<CheckAllResult>('/api/admin/build-envs/check-all', {})
}

/** 已启用环境的去重语言列表(普通用户可访问,供流水线编辑器下拉)。 */
export async function listBuildEnvLanguages(): Promise<string[]> {
  const res = await http.get<{ languages: string[] }>('/api/build-envs/languages')
  return res.languages ?? []
}
