/**
 * 后端默认名的本地化口径。
 *
 * 盯的是「源任务卡片不许替用户宣称某家 git 平台」:旧版本把源任务种成了「Gitee 源」,
 * 而平台并没有判过仓库 host —— 自建 GitLab / GitHub 上的项目也会看到那句 Gitee。
 * 后端读到/存时会把它收敛成中性名,但历史运行快照里仍是旧字面量,所以两种写法在
 * 每种语言里都必须渲染成同一个不带品牌的标签。
 */
import { afterEach, describe, expect, it } from 'vitest'
import { localizeName } from './pipelineLabels'
import { setLocale, type LocaleCode } from '../i18n'

const LOCALES = ['zh-CN', 'zh-TW', 'en', 'de', 'es', 'fr', 'ja', 'ko'] as const

afterEach(() => setLocale('zh-CN'))

describe('localizeName · 源任务默认名', () => {
  for (const locale of LOCALES) {
    it(`${locale}: 中性名与旧品牌名同一标签,且都不带平台名`, () => {
      setLocale(locale as LocaleCode)
      expect(localizeName('Git 源')).toBe(localizeName('Gitee 源'))
      expect(localizeName('Gitee 源')).not.toMatch(/Gitee|GitHub|GitLab/i)
    })
  }

  it('用户自己取的名字原样显示', () => {
    setLocale('en')
    expect(localizeName('拉取主仓源码')).toBe('拉取主仓源码')
  })

  it('其余后端默认阶段名照旧本地化', () => {
    setLocale('en')
    expect(localizeName('流水线源')).toBe('Source')
    expect(localizeName('构建')).toBe('Build')
  })
})
