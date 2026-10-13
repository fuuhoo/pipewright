/**
 * languageOptions 纯逻辑回归:归一化口径、下拉选项的拼装顺序、历史值不被改写、自定义新增的撞车回收。
 */
import { describe, it, expect } from 'vitest'
import {
  CUSTOM_SENTINEL,
  OTHER_LANGUAGE,
  buildLanguageOptions,
  languagesFrom,
  matchExistingLanguage,
  mergeLanguageLists,
  normalizeLanguage,
  sortLanguages,
} from './languageOptions'

const LABELS = { otherLabel: '其他', customLabel: '自定义新增' }

describe('normalizeLanguage', () => {
  it('小写化、去首尾空白、内部空白折成连字符', () => {
    expect(normalizeLanguage('  Node ')).toBe('node')
    expect(normalizeLanguage('Objective C')).toBe('objective-c')
    expect(normalizeLanguage('JAVA\t17 ')).toBe('java-17')
    expect(normalizeLanguage('')).toBe('')
  })
})

describe('sortLanguages', () => {
  it('大小写不同的同义项只留一份', () => {
    expect(sortLanguages(['Go', 'go', ' node '])).toEqual(['go', 'node'])
  })

  it('剔除空值与 other,按自然序排', () => {
    expect(sortLanguages(['', 'ruby', 'node', 'dotnet', OTHER_LANGUAGE])).toEqual([
      'dotnet',
      'node',
      'ruby',
    ])
  })
})

describe('languagesFrom', () => {
  it('从记录里取语言,缺字段与 null 都跳过', () => {
    expect(languagesFrom([{ language: 'java' }, { language: '' }, {}, { language: 'Java' }])).toEqual([
      'java',
    ])
  })
})

describe('mergeLanguageLists', () => {
  it('两个来源并成一份去重排序的表', () => {
    expect(mergeLanguageLists(['node', 'go'], ['go', 'vfox'])).toEqual(['go', 'node', 'vfox'])
  })
})

describe('buildLanguageOptions', () => {
  it('已添加的语言在前,其他与自定义新增固定在末尾', () => {
    const opts = buildLanguageOptions({ current: '', known: ['node', 'java'], ...LABELS })
    expect(opts.map((o) => o.value)).toEqual(['java', 'node', OTHER_LANGUAGE, CUSTOM_SENTINEL])
    expect(opts.map((o) => o.kind)).toEqual(['known', 'known', 'other', 'custom'])
    expect(opts[2].label).toBe('其他')
    expect(opts[3].label).toBe('自定义新增')
  })

  it('库里遗留的下拉里没有的值置顶,编辑不会偷改语言', () => {
    const opts = buildLanguageOptions({ current: 'golang', known: ['go', 'node'], ...LABELS })
    expect(opts[0]).toEqual({ value: 'golang', label: 'golang', kind: 'current' })
    expect(opts.map((o) => o.value)).toEqual(['golang', 'go', 'node', OTHER_LANGUAGE, CUSTOM_SENTINEL])
  })

  it('当前值已在列表里就只用那一项,不出现重复 option', () => {
    const opts = buildLanguageOptions({ current: 'Node', known: ['node', 'go'], ...LABELS })
    expect(opts.filter((o) => o.value === 'node')).toHaveLength(1)
  })

  it('当前值是 other 时不额外插一项', () => {
    const opts = buildLanguageOptions({ current: OTHER_LANGUAGE, known: ['go'], ...LABELS })
    expect(opts.map((o) => o.value)).toEqual(['go', OTHER_LANGUAGE, CUSTOM_SENTINEL])
  })
})

describe('matchExistingLanguage', () => {
  it('大小写/空格差异算撞车,复用已有项', () => {
    expect(matchExistingLanguage(' Java ', ['node', 'java'])).toBe('java')
  })

  it('真正的新语言返回 null,空输入也返回 null', () => {
    expect(matchExistingLanguage('dotnet', ['node', 'java'])).toBeNull()
    expect(matchExistingLanguage('   ', ['node'])).toBeNull()
  })
})
