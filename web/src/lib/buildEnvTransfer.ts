/**
 * 构建环境导入/导出的前端侧纯逻辑 —— 文件名、格式判定、结果标签与汇总文案。
 *
 * 放成纯函数的理由和 projectGroups 一样:这些规则决定了「管理员拿到的是什么文件、
 * 每一行到底发生了什么」,值得脱离页面断言。
 */
import { t } from '../i18n'
import type { ImportMode, ImportRowAction, ImportSummary, TransferFormat } from '../api/buildEnvs'

/** 与后端 MaxBytesReader 同值:超了必被 400 拒,不如在这里先说清。 */
export const MAX_IMPORT_BYTES = 1 << 20

/** 导出文件名:与后端 Content-Disposition 同一套规则(UTC,便于跨机器对齐时间)。 */
export function buildEnvExportFilename(format: TransferFormat, at: Date): string {
  const pad = (n: number): string => String(n).padStart(2, '0')
  const stamp =
    `${at.getUTCFullYear()}${pad(at.getUTCMonth() + 1)}${pad(at.getUTCDate())}` +
    `-${pad(at.getUTCHours())}${pad(at.getUTCMinutes())}${pad(at.getUTCSeconds())}`
  return `build-envs-${stamp}.${format === 'json' ? 'json' : 'yaml'}`
}

/** 按扩展名判格式;.json 走 JSON,其余(含 .yml / 无后缀)都按 YAML 解。 */
export function detectTransferFormat(filename: string): TransferFormat {
  return /\.json$/i.test(filename.trim()) ? 'json' : 'yaml'
}

export function importActionLabel(action: ImportRowAction): string {
  return t(`buildEnvs.action${action.charAt(0).toUpperCase()}${action.slice(1)}`)
}

export function importModeLabel(mode: ImportMode): string {
  return mode === 'overwrite' ? t('buildEnvs.importModeOverwrite') : t('buildEnvs.importModeSkip')
}

/** 汇总行:预览与落库共用同一串计数,只是语气不同(调用方另加 note)。 */
export function formatImportSummary(summary: ImportSummary): string {
  return t('buildEnvs.importSummary', {
    total: summary.total,
    created: summary.created,
    updated: summary.updated,
    skipped: summary.skipped,
    failed: summary.failed,
  })
}

/** 批量检查的落定文案(0 行可用时不写「成功」,免得看着像没报错就等于能用)。 */
export function batchCheckLabel(ok: number, total: number): string {
  return t('buildEnvs.checkSelectedDone', { ok, total })
}

/** 按 UTF-8 字节数判上限(中文说明字段下,字符数会低估体积)。 */
export function exceedsImportLimit(content: string): boolean {
  return new TextEncoder().encode(content).length > MAX_IMPORT_BYTES
}
