import { describe, it, expect } from 'vitest'
import {
  OAUTH_ERR_NS,
  knownOAuthErrorReasons,
  oauthErrorKey,
  oauthErrorShowsRawCode,
} from './oauthReason'
import zhCN from '../i18n/locales/zh-CN'
import zhTW from '../i18n/locales/zh-TW'
import en from '../i18n/locales/en'
import de from '../i18n/locales/de'
import es from '../i18n/locales/es'
import fr from '../i18n/locales/fr'
import ja from '../i18n/locales/ja'
import ko from '../i18n/locales/ko'

/** 8 个根词条文件里的 `oauthErr` 命名空间(login 页与账户页共用的那一套原因文案)。 */
const oauthErrByLocale: Record<string, Record<string, string>> = {
  'zh-CN': (zhCN as { oauthErr: Record<string, string> }).oauthErr,
  'zh-TW': (zhTW as { oauthErr: Record<string, string> }).oauthErr,
  en: (en as { oauthErr: Record<string, string> }).oauthErr,
  de: (de as { oauthErr: Record<string, string> }).oauthErr,
  es: (es as { oauthErr: Record<string, string> }).oauthErr,
  fr: (fr as { oauthErr: Record<string, string> }).oauthErr,
  ja: (ja as { oauthErr: Record<string, string> }).oauthErr,
  ko: (ko as { oauthErr: Record<string, string> }).oauthErr,
}

describe('oauthErrorKey', () => {
  it('已知原因码逐字翻成文案键(不是 snake→camel 的顺手猜测)', () => {
    expect(oauthErrorKey('name_taken')).toBe('oauthErr.nameTaken')
    expect(oauthErrorKey('invalid_state')).toBe('oauthErr.invalidState')
    expect(oauthErrorShowsRawCode('name_taken')).toBe(false)
  })

  it('认不出的码走 generic 兜底,并要求把原文塞进插值', () => {
    // 后端将来加新码而这里漏改时,页面必须显示成「登录未完成(new_code)」而不是空白 ——
    // 否则一次拒绝没有任何可传达的理由。
    expect(oauthErrorKey('brand_new_code')).toBe(`${OAUTH_ERR_NS}.generic`)
    expect(oauthErrorShowsRawCode('brand_new_code')).toBe(true)
  })

  it('空串与大小写漂移都按未知处理', () => {
    expect(oauthErrorKey('')).toBe(`${OAUTH_ERR_NS}.generic`)
    expect(oauthErrorKey('Name_Taken')).toBe(`${OAUTH_ERR_NS}.generic`)
  })
})

describe('oauthReason 与后端原因码的穷举对账', () => {
  it('每个已知码都有对应文案键,一个不漏', () => {
    for (const reason of knownOAuthErrorReasons()) {
      expect(oauthErrorKey(reason)).not.toBe(`${OAUTH_ERR_NS}.generic`)
    }
  })

  it('8 个语种都要备齐全部原因文案 + generic', () => {
    const wanted = [...knownOAuthErrorReasons().map((r) => oauthErrorKey(r).split('.')[1]), 'generic']
    for (const [loc, bundle] of Object.entries(oauthErrByLocale)) {
      expect(bundle, `${loc} 缺整个 oauthErr 命名空间`).toBeTruthy()
      const missing = wanted.filter((k) => !bundle[k])
      expect(missing, `${loc} 缺少的 oauthErr 键`).toEqual([])
    }
  })

  it('语种里不该有枚举外的死键(文案留着没人能触发)', () => {
    const live = new Set(knownOAuthErrorReasons().map((r) => oauthErrorKey(r).split('.')[1]))
    live.add('generic')
    for (const [loc, bundle] of Object.entries(oauthErrByLocale)) {
      const orphans = Object.keys(bundle).filter((k) => !live.has(k))
      expect(orphans, `${loc} 里的 oauthErr 孤儿键`).toEqual([])
    }
  })
})
