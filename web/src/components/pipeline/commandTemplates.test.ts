import { describe, it, expect } from 'vitest'
import { COMMAND_TEMPLATES, mergeCommands, templatesForLanguage } from './commandTemplates'

// 与 internal/buildenv/seed.go 的内置目录对齐:模板只有挂在这些语言上才能被环境选中带出。
const CATALOG_LANGUAGES = ['node', 'java', 'python', 'go', 'ruby', 'php', 'rust', 'vfox']

describe('commandTemplates 数据', () => {
  it('id 唯一,语言都在预置目录里', () => {
    const ids = COMMAND_TEMPLATES.map((x) => x.id)
    expect(new Set(ids).size).toBe(ids.length)
    for (const tpl of COMMAND_TEMPLATES) {
      expect(CATALOG_LANGUAGES).toContain(tpl.language)
    }
  })

  it('命令每行都是实活:不留空行、不带 CR(后端逐行拼进 set -e 脚本)', () => {
    for (const tpl of COMMAND_TEMPLATES) {
      expect(tpl.commands.trim()).not.toBe('')
      for (const line of tpl.commands.split('\n')) {
        expect(line.trim()).not.toBe('')
        expect(line).not.toContain('\r')
      }
    }
  })
})

describe('templatesForLanguage', () => {
  it('命中语言的模板前置,其余保持原序跟在后面', () => {
    const nodeFirst = templatesForLanguage('node')
    expect(nodeFirst.slice(0, 3).map((x) => x.language)).toEqual(['node', 'node', 'node'])
    expect(nodeFirst.slice(3).map((x) => x.id)).toEqual(
      COMMAND_TEMPLATES.filter((x) => x.language !== 'node').map((x) => x.id),
    )
  })

  it('大小写与空白不影响判定', () => {
    expect(templatesForLanguage(' Java ')).toEqual(templatesForLanguage('java'))
  })

  it('没选环境 / 语言没有模板(alpine)→ 给全量原序', () => {
    expect(templatesForLanguage('')).toEqual(COMMAND_TEMPLATES)
    expect(templatesForLanguage('alpine')).toEqual(COMMAND_TEMPLATES)
  })

  it('返回新数组,调用方排序不会污染表本身', () => {
    templatesForLanguage('node').reverse()
    expect(COMMAND_TEMPLATES[0].id).toBe('node-npm')
  })
})

describe('mergeCommands', () => {
  it('空框(含纯空白)直接填入模板', () => {
    expect(mergeCommands('', 'npm ci')).toBe('npm ci')
    expect(mergeCommands('\n  \n', 'npm ci')).toBe('npm ci')
  })

  it('已有命令原样在前,模板追加到末尾 —— 绝不覆盖', () => {
    expect(mergeCommands('cd frontend', 'npm ci\nnpm run build')).toBe('cd frontend\nnpm ci\nnpm run build')
  })

  it('压掉尾随换行与 CRLF,拼出来的块之间不留空行', () => {
    expect(mergeCommands('npm ci\r\n\r\n', 'npm run build')).toBe('npm ci\nnpm run build')
  })
})
