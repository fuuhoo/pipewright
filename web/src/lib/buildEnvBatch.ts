/**
 * 构建环境批量新增/批量删除的纯逻辑 —— 版本清单展开、模板填充、结果文案。
 *
 * 「批量」的价值全在展开规则上:管理员输入的是一行一个的版本清单,落库的是
 * 「语言 + 版本 + 镜像」若干行。这套换算决定了他点确认之后到底多出哪些行,
 * 值得脱离页面断言(与 buildEnvTransfer 同一理由)。
 */
import { t } from '../i18n'
import type { BatchRowAction, BatchSummary, BuildEnvInput, BuildEnvSourceType } from '../api/buildEnvs'

/** 与后端 buildenv.MaxBatchRows 同值:超了必被 400 拒,不如在这里先说清。 */
export const MAX_BATCH_ROWS = 200

/** 显示名留空时用的模板:node 20 / python 3.12 这种「语言 + 版本」写法。 */
export const DEFAULT_NAME_TEMPLATE = '{language} {version}'

/**
 * 镜像输入框的示例地址:与语言无关,且不进 i18n —— vue-i18n 会把文案里的
 * `{...}` 当插值吃掉,而这里正是要原样展示花括号。
 */
export const EXAMPLE_IMAGE_TEMPLATE = 'node:{version}-alpine'

/** 批量新增表单:一份共享字段 + 一串版本,展开成 N 行。 */
export interface BatchAddForm {
  language: string
  /** 多行文本原样留着(用户可能一行一个,也可能逗号分隔)。 */
  versions: string
  displayNameTemplate: string
  imageTemplate: string
  description: string
  sourceType: BuildEnvSourceType
  credentialId: string
  sortOrder: number
  enabled: boolean
}

export const emptyBatchAddForm = (): BatchAddForm => ({
  language: '',
  versions: '',
  displayNameTemplate: DEFAULT_NAME_TEMPLATE,
  imageTemplate: '',
  description: '',
  sourceType: 'official',
  credentialId: '',
  sortOrder: 0,
  enabled: false,
})

/**
 * 拆版本清单:换行、逗号(中英文)、分号、空格、制表符都算分隔符;
 * 去空白项并按首次出现去重(同一版本写两遍只会多一行重复键的 skipped)。
 */
export function splitVersions(raw: string): string[] {
  const out: string[] = []
  const seen = new Set<string>()
  for (const part of raw.split(/[\s,，;；]+/)) {
    const v = part.trim()
    if (v === '' || seen.has(v)) continue
    seen.add(v)
    out.push(v)
  }
  return out
}

/** 填模板:{language} / {version} 两个占位(花括号里留空格、大写都认)。 */
export function fillTemplate(tpl: string, language: string, version: string): string {
  return tpl.replace(/\{\s*(language|version)\s*\}/gi, (_m, key: string) =>
    key.toLowerCase() === 'language' ? language : version,
  )
}

/**
 * 展开成待提交的行:sortOrder 从起始值逐行 +1(保持管理员写的顺序),
 * 显示名模板留空则退回「{language} {version}」。
 *
 * 镜像模板不含 {version} 是合法用法:vfox-full 这类一个镜像供多套工具链的地址,
 * 每行镜像相同、只有版本不同。
 */
export function expandBatchForm(form: BatchAddForm): BuildEnvInput[] {
  const language = form.language.trim()
  const image = form.imageTemplate.trim()
  const nameTpl = form.displayNameTemplate.trim() === '' ? DEFAULT_NAME_TEMPLATE : form.displayNameTemplate
  return splitVersions(form.versions).map((version, i) => ({
    language,
    version,
    displayName: fillTemplate(nameTpl, language, version).trim(),
    description: form.description,
    sourceType: form.sourceType,
    image: fillTemplate(image, language, version).trim(),
    credentialId: form.credentialId,
    sortOrder: form.sortOrder + i,
    enabled: form.enabled,
  }))
}

/** 批量新增的拦路项(返回 i18n key + 参数,由页面翻译)。 */
export function batchAddIssues(form: BatchAddForm): string[] {
  const rows = expandBatchForm(form)
  const issues: string[] = []
  if (form.language.trim() === '') issues.push('missingLanguage')
  if (rows.length === 0) issues.push('missingVersions')
  if (rows.some((r) => r.image === '')) issues.push('missingImage')
  if (rows.length > MAX_BATCH_ROWS) issues.push('tooManyVersions')
  return issues
}

export function batchActionLabel(action: BatchRowAction): string {
  return t(`buildEnvs.action${action.charAt(0).toUpperCase()}${action.slice(1)}`)
}

/** 汇总行:新增与删除共用同一串计数,只是 success 的口吻由调用方给。 */
export function formatBatchSummary(kind: 'create' | 'delete', summary: BatchSummary): string {
  return t(kind === 'create' ? 'buildEnvs.batchCreateSummary' : 'buildEnvs.batchDeleteSummary', {
    total: summary.total,
    success: summary.success,
    skipped: summary.skipped,
    failed: summary.failed,
  })
}

/** 删除确认里的行标签:「Node.js 20(node/20)」,让管理员看见自己勾的是什么。 */
export function batchRowLabel(language: string, version: string): string {
  return `${language}/${version}`
}
