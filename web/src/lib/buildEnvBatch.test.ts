/**
 * buildEnvBatch 纯逻辑回归:版本清单拆分、模板展开、拦路项判定。
 *
 * 这些规则决定了「点确认之后库里到底多出哪几行」,所以逐条断言而不是只跑通主路径。
 */
import { describe, it, expect } from 'vitest'
import {
  MAX_BATCH_ROWS,
  batchAddIssues,
  emptyBatchAddForm,
  expandBatchForm,
  fillTemplate,
  splitVersions,
  type BatchAddForm,
} from './buildEnvBatch'

const base = (over: Partial<BatchAddForm>): BatchAddForm => ({
  ...emptyBatchAddForm(),
  language: 'node',
  imageTemplate: 'node:{version}-alpine',
  ...over,
})

describe('splitVersions', () => {
  it('换行、逗号、中英文分隔符、空格都算一档', () => {
    expect(splitVersions('16\n18, 20；22 24')).toEqual(['16', '18', '20', '22', '24'])
  })

  it('去空项并按首次出现去重', () => {
    expect(splitVersions('20  20\n\n18,,18')).toEqual(['20', '18'])
  })

  it('空白输入得到空数组', () => {
    expect(splitVersions('   \n , ;')).toEqual([])
  })
})

describe('fillTemplate', () => {
  it('两个占位都替换,大小写与花括号内空格都认', () => {
    expect(fillTemplate('{Language}-{ Version }-alpine', 'node', '20')).toBe('node-20-alpine')
  })

  it('没有占位就原样返回(多工具链镜像共用一个地址的用法)', () => {
    expect(fillTemplate('registry.internal/vfox-full:1.0.12', 'node', '20')).toBe(
      'registry.internal/vfox-full:1.0.12',
    )
  })
})

describe('expandBatchForm', () => {
  it('每个版本一行,sortOrder 从起始值逐行 +1', () => {
    const rows = expandBatchForm(base({ versions: '18 20 22', sortOrder: 40 }))
    expect(rows.map((r) => r.version)).toEqual(['18', '20', '22'])
    expect(rows.map((r) => r.sortOrder)).toEqual([40, 41, 42])
    expect(rows.map((r) => r.image)).toEqual([
      'node:18-alpine',
      'node:20-alpine',
      'node:22-alpine',
    ])
  })

  it('显示名模板留空退回「语言 + 版本」', () => {
    const [first] = expandBatchForm(base({ versions: '20', displayNameTemplate: '  ' }))
    expect(first?.displayName).toBe('node 20')
  })

  it('显示名模板照常展开', () => {
    const [first] = expandBatchForm(base({ versions: '20', displayNameTemplate: 'Node.js {version}' }))
    expect(first?.displayName).toBe('Node.js 20')
  })

  it('共享字段(来源、凭据、启用意图)带到每一行', () => {
    const rows = expandBatchForm(
      base({
        versions: '20 22',
        sourceType: 'custom',
        credentialId: 'cred-acr',
        enabled: true,
        description: '阿里云 ACR',
      }),
    )
    expect(rows).toHaveLength(2)
    for (const r of rows) {
      expect(r.sourceType).toBe('custom')
      expect(r.credentialId).toBe('cred-acr')
      expect(r.enabled).toBe(true)
      expect(r.description).toBe('阿里云 ACR')
    }
  })

  it('镜像模板不含版本时每行同址,只有版本不同', () => {
    const rows = expandBatchForm(base({ versions: '16 18', imageTemplate: 'vfox-full:1.0.12' }))
    expect(rows.map((r) => r.image)).toEqual(['vfox-full:1.0.12', 'vfox-full:1.0.12'])
    expect(rows.map((r) => r.version)).toEqual(['16', '18'])
  })

  it('语言与镜像模板里的空格都先 trim,不会把空格写进库', () => {
    const [row] = expandBatchForm(base({ versions: '3.12', language: '  python  ', imageTemplate: ' python:{version} ' }))
    expect(row?.language).toBe('python')
    expect(row?.image).toBe('python:3.12')
  })
})

describe('batchAddIssues', () => {
  it('齐全的配置没有拦路项', () => {
    expect(batchAddIssues(base({ versions: '20' }))).toEqual([])
  })

  it('缺语言/缺版本/缺镜像各自点名', () => {
    expect(batchAddIssues(base({ language: '', versions: '20' }))).toContain('missingLanguage')
    expect(batchAddIssues(base({ versions: '  ' }))).toContain('missingVersions')
    expect(batchAddIssues(base({ versions: '20', imageTemplate: '' }))).toContain('missingImage')
  })

  it('超过后端上限先在这里说清,不拿 400 去试', () => {
    const many = Array.from({ length: MAX_BATCH_ROWS + 1 }, (_, i) => String(i)).join(' ')
    expect(batchAddIssues(base({ versions: many }))).toContain('tooManyVersions')
  })
})
