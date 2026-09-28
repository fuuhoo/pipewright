/**
 * 配置资源编辑弹窗里与「文件」相关的纯逻辑(视图只留渲染与调用)。
 *
 * 与后端 internal/configprofile 对齐的三条约束:
 *   - 正文只在详情接口出现,且以磁盘权威副本为准(DB.content 是冗余快照);
 *   - 重新上传走 POST /api/admin/config-profiles/{id}/upload(multipart),单文件 1MB;
 *   - 「选了文件」优先于「文本框里的正文」——两条路径不能同时提交。
 */

/** 提交走哪条路径。内置行不进弹窗的文件区(正文只读),但仍走 update。 */
export type SubmitMode = 'upload' | 'create' | 'replace' | 'update'

export function submitMode(editing: boolean, hasFile: boolean): SubmitMode {
  if (editing) return hasFile ? 'replace' : 'update'
  return hasFile ? 'upload' : 'create'
}

/** 后端上传体积上限(PIPEWRIGHT_CONFIG_UPLOAD_MAX_SIZE 默认 1MB)。 */
export const MAX_UPLOAD_BYTES = 1 << 20

export function exceedsUploadLimit(size: number): boolean {
  return size > MAX_UPLOAD_BYTES
}
