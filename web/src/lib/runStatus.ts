/**
 * 运行状态的展示口径 —— 后端给的是那套固定 snake_case 词(与运行页同一个联合类型),
 * 这里只负责把它翻成当前语言的标签。
 *
 * 与 pipelineLabels 同一套做法:用全局 `t`,切语言时由调用方的渲染重新求值。
 */
import { t } from '../i18n'
import type { RunStatus } from '../api/projects'

export function runStatusLabel(status: RunStatus): string {
  return t(`runStatus.${status}`)
}
