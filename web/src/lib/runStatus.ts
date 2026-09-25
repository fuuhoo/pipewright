/**
 * 运行状态的展示口径 —— 后端把状态存成固定六个中文词,页面按当前语言出标签。
 *
 * 与 pipelineLabels 同一套做法:用全局 `t`,切语言时由调用方的渲染重新求值。
 */
import { t } from '../i18n'
import type { RunStatus } from '../api/projects'

const STATUS_I18N_KEY: Record<RunStatus, string> = {
  '成功': 'success',
  '失败': 'failed',
  '进行中': 'running',
  '部分失败': 'partial_failed',
  '已回滚': 'rolled_back',
  '排队中': 'queued',
}

export function runStatusLabel(status: RunStatus): string {
  return t(`runStatus.${STATUS_I18N_KEY[status]}`)
}
