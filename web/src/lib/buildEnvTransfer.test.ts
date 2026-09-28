/**
 * buildEnvTransfer 纯逻辑回归:导出文件名、格式判定、结果标签与汇总。
 * locale 由 test/setup.ts 钉在 zh-CN。
 */
import { describe, it, expect } from 'vitest'
import {
  MAX_IMPORT_BYTES,
  batchCheckLabel,
  buildEnvExportFilename,
  detectTransferFormat,
  formatImportSummary,
  importActionLabel,
  importModeLabel,
} from './buildEnvTransfer'

describe('buildEnvExportFilename', () => {
  it('用 UTC 时间戳,与后端附件名同一套规则', () => {
    const at = new Date(Date.UTC(2026, 8, 28, 2, 30, 41))
    expect(buildEnvExportFilename('yaml', at)).toBe('build-envs-20260928-023041.yaml')
    expect(buildEnvExportFilename('json', at)).toBe('build-envs-20260928-023041.json')
  })
})

describe('detectTransferFormat', () => {
  it('只有 .json 走 JSON,其余按 YAML', () => {
    expect(detectTransferFormat('build-envs.JSON')).toBe('json')
    expect(detectTransferFormat('envs.yaml')).toBe('yaml')
    expect(detectTransferFormat('envs.yml')).toBe('yaml')
    expect(detectTransferFormat('  ')).toBe('yaml')
  })
})

describe('importActionLabel / importModeLabel', () => {
  it('四种结果动作都有译文(预览与落库共用)', () => {
    expect(importActionLabel('created')).toBe('新建')
    expect(importActionLabel('updated')).toBe('更新')
    expect(importActionLabel('skipped')).toBe('跳过')
    expect(importActionLabel('failed')).toBe('失败')
  })

  it('两种冲突策略说清差别', () => {
    expect(importModeLabel('skip')).toContain('跳过')
    expect(importModeLabel('overwrite')).toContain('覆盖')
  })
})

describe('formatImportSummary', () => {
  it('四类计数都出现在汇总里', () => {
    const s = formatImportSummary({ total: 9, created: 3, updated: 2, skipped: 3, failed: 1 })
    for (const n of ['3', '2', '1']) expect(s).toContain(n)
    expect(s).toContain('9')
  })

  it('全零也不留空串(预览空文件时要能看到「0 条」)', () => {
    const s = formatImportSummary({ total: 0, created: 0, updated: 0, skipped: 0, failed: 0 })
    expect(s.trim().length).toBeGreaterThan(0)
  })
})

describe('batchCheckLabel', () => {
  it('动态拼出的 key 也要有译文(页面靠它报「几行可用」)', () => {
    expect(batchCheckLabel(3, 5)).toBe('所选检查完成:3/5 可用')
  })
})

describe('MAX_IMPORT_BYTES', () => {
  it('与后端请求体上限一致(1MB)', () => {
    expect(MAX_IMPORT_BYTES).toBe(1048576)
  })
})
