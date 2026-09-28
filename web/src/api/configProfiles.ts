/**
 * Config profile (配置资源) API — v6.2 §3.3 / §5.2.
 *
 * 管理员端点(RequireAdmin):
 *   GET    /api/admin/config-profiles        → { items: ConfigProfile[] }
 *   POST   /api/admin/config-profiles        → ConfigProfile
 *   GET    /api/admin/config-profiles/:id    → ConfigProfileDetail(多带文件正文)
 *   PUT    /api/admin/config-profiles/:id    → ConfigProfile
 *   DELETE /api/admin/config-profiles/:id    → 204
 *   POST   /api/admin/config-profiles/upload → ConfigProfile(multipart,新建)
 *   POST   /api/admin/config-profiles/:id/upload → ConfigProfile(multipart,覆盖已有行的文件)
 *
 * 普通用户端点(RequireUser):
 *   GET    /api/config-profiles?language=xx  → { items: ConfigProfile[] }(仅启用)
 *
 * 语义要点:
 *   - 文件落宿主 `${PIPEWRIGHT_DATA_DIR}/config_profiles/<id>/<filename>`(P0 #3 原子写)。
 *   - is_builtin=1 内置行:不可经 API 创建;Update 仅允许 description/enabled,
 *     改 target_path/content 等 → 403 builtin_readonly。
 *   - content 是 DB 冗余快照;权威副本是 file_path 指向的磁盘文件。
 *     只有详情接口回正文,且读的是磁盘那份(磁盘读不到才回退快照,见 contentSource)。
 *   - PUT 的 content 传空串 = 「这次不动文件」,后端沿用原正文。
 */

import { http } from './http'

export interface ConfigProfile {
  id: string
  language: string
  configType: string
  name: string
  /** 容器内目标路径,如 /root/.m2/settings.xml。 */
  targetPath: string
  /** 宿主机路径,权威副本。 */
  filePath: string
  isDefault: boolean
  isBuiltin: boolean
  description: string
  enabled: boolean
  createdBy: string
  createdAt: string
  updatedAt: string
}

/** 详情视图:列表字段 + 文件正文。 */
export interface ConfigProfileDetail extends ConfigProfile {
  /** 文件正文;优先磁盘权威副本。 */
  content: string
  /** disk = 读的是磁盘文件(构建时真正拷进容器的那份);db = 磁盘读不到,回退冗余快照。 */
  contentSource: 'disk' | 'db'
}

/** 上传允许的扩展名白名单(后端 IsExtAllowed 同款,提前挡掉无效请求)。 */
export const CONFIG_UPLOAD_EXTS = [
  '.xml',
  '.conf',
  '.npmrc',
  '.ini',
  '.env',
  '.toml',
  '.yaml',
  '.yml',
] as const

export interface ConfigProfileInput {
  language: string
  configType: string
  name: string
  targetPath: string
  content: string
  isDefault?: boolean
  description?: string
  enabled?: boolean
}

export interface UploadConfigProfileInput {
  file: File
  language: string
  configType: string
  name: string
  targetPath: string
  description?: string
  isDefault?: boolean
}

/** 重新上传的入参:文件必带,targetPath 留空表示不改容器内路径。 */
export interface ReplaceConfigProfileFileInput {
  file: File
  targetPath?: string
}

interface ListEnvelope {
  items: ConfigProfile[]
}

export async function listConfigProfiles(params?: {
  language?: string
  configType?: string
}): Promise<ConfigProfile[]> {
  const q = new URLSearchParams()
  if (params?.language) q.set('language', params.language)
  if (params?.configType) q.set('configType', params.configType)
  const qs = q.toString()
  const res = await http.get<ListEnvelope>(
    `/api/admin/config-profiles${qs ? `?${qs}` : ''}`,
  )
  return res.items ?? []
}

export async function getConfigProfile(id: string): Promise<ConfigProfileDetail> {
  return http.get<ConfigProfileDetail>(`/api/admin/config-profiles/${id}`)
}

export async function createConfigProfile(input: ConfigProfileInput): Promise<ConfigProfile> {
  return http.post<ConfigProfile>('/api/admin/config-profiles', input)
}

export async function updateConfigProfile(
  id: string,
  input: ConfigProfileInput,
): Promise<ConfigProfile> {
  return http.put<ConfigProfile>(`/api/admin/config-profiles/${id}`, input)
}

export async function deleteConfigProfile(id: string): Promise<void> {
  return http.delete<void>(`/api/admin/config-profiles/${id}`)
}

/**
 * multipart 上传。file 字段名为 `file`,其余为普通 form 字段。
 * 后端限制默认 1MB(PIPEWRIGHT_CONFIG_UPLOAD_MAX_SIZE)。
 */
export async function uploadConfigProfile(input: UploadConfigProfileInput): Promise<ConfigProfile> {
  const form = new FormData()
  form.append('file', input.file)
  form.append('language', input.language)
  form.append('configType', input.configType)
  form.append('name', input.name)
  form.append('targetPath', input.targetPath)
  if (input.description) form.append('description', input.description)
  form.append('isDefault', input.isDefault ? 'true' : 'false')
  // Content-Type 交由浏览器按 FormData 边界自动设置(不手动指定)。
  return http.post<ConfigProfile>('/api/admin/config-profiles/upload', form)
}

/**
 * 给已有配置资源重新上传文件:覆盖磁盘权威副本,DB 快照同步。
 * 语言/类型/名称等身份字段保持行内原值;内置行由后端拒(403 builtin_readonly)。
 */
export async function replaceConfigProfileFile(
  id: string,
  input: ReplaceConfigProfileFileInput,
): Promise<ConfigProfile> {
  const form = new FormData()
  form.append('file', input.file)
  if (input.targetPath) form.append('targetPath', input.targetPath)
  return http.post<ConfigProfile>(`/api/admin/config-profiles/${id}/upload`, form)
}

/** 普通用户视角:某语言下所有已启用配置。 */
export async function listEnabledConfigProfiles(language: string): Promise<ConfigProfile[]> {
  const q = language ? `?language=${encodeURIComponent(language)}` : ''
  const res = await http.get<ListEnvelope>(`/api/config-profiles${q}`)
  return res.items ?? []
}

/** 前端预检:扩展名是否在白名单内(避免一次注定失败的往返)。 */
export function isExtAllowed(filename: string): boolean {
  const lower = filename.toLowerCase()
  return CONFIG_UPLOAD_EXTS.some((ext) => lower.endsWith(ext))
}
