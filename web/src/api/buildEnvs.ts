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
 *   POST   /api/admin/build-envs/check-batch → { items, ok, total, skipped }
 *   GET    /api/admin/build-envs/export      → 文本(yaml/json;附件下载)
 *   POST   /api/admin/build-envs/import      → ImportReport(dryRun 可预览)
 *
 * 普通用户端点(RequireUser):
 *   GET    /api/build-envs                   → { items: PresetBuildEnv[] }(仅启用)
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

/**
 * 预置目录条目(流水线编辑器视角):后端只回选项需要的字段,
 * 不含 credentialId / imageCheckError / createdBy(管理员运维信息)。
 */
export interface PresetBuildEnv {
  id: string
  language: string
  version: string
  displayName: string
  description: string
  sourceType: BuildEnvSourceType
  image: string
  imageCheckStatus: ImageCheckStatus
  sortOrder: number
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

/**
 * 已启用构建环境目录(普通用户可访问):流水线编辑器的唯一镜像来源。
 * 禁用的预置环境后端不会返回,因此下架后不会再出现在下拉里。
 */
export async function listEnabledBuildEnvs(params?: {
  language?: string
}): Promise<PresetBuildEnv[]> {
  const q = new URLSearchParams()
  if (params?.language) q.set('language', params.language)
  const qs = q.toString()
  const res = await http.get<{ items: PresetBuildEnv[] }>(`/api/build-envs${qs ? `?${qs}` : ''}`)
  return res.items ?? []
}

/** 导出/导入文件格式。导出走文本下发,所以这里拿到的是文件原文(不是解析后的对象)。 */
export type TransferFormat = 'yaml' | 'json'

/** 冲突处理:skip=已存在行不动;overwrite=已存在行原地更新(沿用同一行 id)。 */
export type ImportMode = 'skip' | 'overwrite'

/** 逐行结果动作;dryRun 时同样这四个值,含义变成「打算这么处理」。 */
export type ImportRowAction = 'created' | 'updated' | 'skipped' | 'failed'

export interface ImportRowResult {
  index: number
  language: string
  version: string
  action: ImportRowAction
  enabled: boolean
  id?: string
  reason?: string
}

export interface ImportSummary {
  total: number
  created: number
  updated: number
  skipped: number
  failed: number
}

export interface ImportReport {
  dryRun: boolean
  mode: ImportMode
  summary: ImportSummary
  results: ImportRowResult[]
}

/** 批量检查的单行结果:检查失败(id 不存在等)也带在 items 里,不会整批报错。 */
export interface BatchCheckItem {
  id: string
  language: string
  version: string
  status: ImageCheckStatus
  error?: string
}

export interface BatchCheckResult {
  items: BatchCheckItem[]
  ok: number
  total: number
  skipped: number
}

/**
 * 导出整表为文件原文(默认含禁用条目)。后端用 text/plain 下发,
 * 附件名由前端按同样规则拼(buildEnvExportFilename),避免依赖响应头。
 */
export async function exportBuildEnvText(params?: {
  format?: TransferFormat
  includeDisabled?: boolean
}): Promise<string> {
  const q = new URLSearchParams()
  q.set('format', params?.format ?? 'yaml')
  if (params?.includeDisabled === false) q.set('includeDisabled', '0')
  const res = await http.get<string>(`/api/admin/build-envs/export?${q.toString()}`)
  return typeof res === 'string' ? res : JSON.stringify(res, null, 2)
}

/** 导入整表;dryRun=true 只取预览,不写库。 */
export async function importBuildEnvs(input: {
  content: string
  format?: TransferFormat
  mode?: ImportMode
  dryRun?: boolean
}): Promise<ImportReport> {
  return http.post<ImportReport>('/api/admin/build-envs/import', {
    content: input.content,
    format: input.format ?? 'yaml',
    mode: input.mode ?? 'skip',
    dryRun: input.dryRun ?? false,
  })
}

/** 批量检查所选(前端多选/全选)。同步等全部检查结束,耗时与「一键检查」同量级。 */
export async function checkBuildEnvsBatch(ids: string[]): Promise<BatchCheckResult> {
  return http.post<BatchCheckResult>('/api/admin/build-envs/check-batch', { ids })
}
