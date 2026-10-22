import { describe, expect, it } from 'vitest'

import type { Cert, CheckReport } from '../api/certs'
import {
  MAX_RENEW_BEFORE_DAYS,
  MIN_RENEW_BEFORE_DAYS,
  RENEW_WINDOW_DAYS,
  canIssue,
  certTone,
  checkReportClean,
  checkTargetDraft,
  checkTargetPaths,
  compactTargetDrafts,
  emptyTargetDraft,
  filterAttentions,
  filterCerts,
  invalidDomains,
  isBlankTargetDraft,
  isValidDomain,
  isValidRenewBeforeDays,
  matchesQuery,
  needsAttention,
  parseDomains,
  parseRenewDays,
  renewWindowDays,
} from './certRows'

function cert(over: Partial<Cert> = {}): Cert {
  return {
    id: 'c-1',
    domain: 'example.com',
    sans: ['example.com'],
    source: 'acme',
    status: 'issued',
    ca: 'letsencrypt',
    dnsProviderId: 'dns-1',
    autoRenew: true,
    renewBeforeDays: 0,
    keyAlgo: 'ECDSA-P256',
    notBefore: '',
    notAfter: '',
    issuer: 'LetsEncrypt R11',
    serial: '',
    hasKey: true,
    lastError: '',
    daysLeft: 60,
    issuedAt: '',
    checkedAt: '',
    createdAt: '',
    updatedAt: '',
    ...over,
  } as Cert
}

function report(over: Partial<CheckReport> = {}): CheckReport {
  return { checked: 0, expiring: [], renewed: [], failed: [], ...over }
}

describe('certTone', () => {
  it('待签是青色进行中,签发失败与已过期是红色', () => {
    expect(certTone('pending', 0)).toBe('cyan')
    expect(certTone('failed', 0)).toBe('red')
    expect(certTone('expired', -3)).toBe('red')
  })

  it('窗口内琥珀、窗口外绿色;边界 30 天算窗口内', () => {
    expect(certTone('issued', 31)).toBe('green')
    expect(certTone('issued', RENEW_WINDOW_DAYS)).toBe('amber')
    expect(certTone('issued', 0)).toBe('amber')
  })

  it('窗口天数由调用方给:同一张证书改成 45 天就提前变琥珀', () => {
    expect(certTone('issued', 40, 30)).toBe('green')
    expect(certTone('issued', 40, 45)).toBe('amber')
    expect(certTone('issued', 10, 7)).toBe('green')
  })
})

describe('renewWindowDays / 续期天数输入', () => {
  it('没设置过(0)用默认 30 天,填了用自己的', () => {
    expect(renewWindowDays(cert({ renewBeforeDays: 0 }))).toBe(RENEW_WINDOW_DAYS)
    expect(renewWindowDays(cert({ renewBeforeDays: 45 }))).toBe(45)
    expect(renewWindowDays(cert({ renewBeforeDays: 7 }))).toBe(7)
  })

  it('字符串与数字两种来路算出同一个值:数字输入框给的就是 number', () => {
    expect(parseRenewDays('45')).toBe(45)
    expect(parseRenewDays(45)).toBe(45)
    expect(parseRenewDays(45.0)).toBe(45)
  })

  it('留空是 0(=用默认),半截小数与乱七八糟都是 NaN,由调用方报错而不是提交', () => {
    expect(parseRenewDays('')).toBe(0)
    expect(parseRenewDays('   ')).toBe(0)
    expect(parseRenewDays(7.5)).toBe(NaN)
    expect(parseRenewDays('abc')).toBe(NaN)
  })

  it('可提交的是 0 与 1~365,越界与小数都拦下(和后端同一区间)', () => {
    expect(isValidRenewBeforeDays(0)).toBe(true)
    expect(isValidRenewBeforeDays(MIN_RENEW_BEFORE_DAYS)).toBe(true)
    expect(isValidRenewBeforeDays(MAX_RENEW_BEFORE_DAYS)).toBe(true)
    expect(isValidRenewBeforeDays(-1)).toBe(false)
    expect(isValidRenewBeforeDays(MAX_RENEW_BEFORE_DAYS + 1)).toBe(false)
    expect(isValidRenewBeforeDays(7.5)).toBe(false)
    expect(isValidRenewBeforeDays(NaN)).toBe(false)
  })
})

describe('needsAttention 用每张证书自己的窗口', () => {
  it('窗口调到 45 天后,剩 40 天的导入证书就进入需要接手', () => {
    expect(needsAttention(cert({ daysLeft: 40, source: 'upload' }))).toBe(false)
    expect(needsAttention(cert({ daysLeft: 40, source: 'upload', renewBeforeDays: 45 }))).toBe(true)
  })

  it('窗口调到 7 天则剩 10 天还不算,阈值不是越大越吵', () => {
    expect(needsAttention(cert({ daysLeft: 10, autoRenew: false, renewBeforeDays: 7 }))).toBe(false)
    expect(needsAttention(cert({ daysLeft: 6, autoRenew: false, renewBeforeDays: 7 }))).toBe(true)
  })
})

describe('needsAttention', () => {
  it('已过期一定要接手', () => {
    expect(needsAttention(cert({ status: 'expired', daysLeft: -1 }))).toBe(true)
  })

  it('窗口内的 acme 且开着自动续期 = 平台自己会办,不算需要接手', () => {
    expect(needsAttention(cert({ daysLeft: 10 }))).toBe(false)
  })

  it('导入的证书与关了自动续期的要人换正文', () => {
    expect(needsAttention(cert({ daysLeft: 10, source: 'upload' }))).toBe(true)
    expect(needsAttention(cert({ daysLeft: 10, autoRenew: false }))).toBe(true)
  })

  it('待签与签发失败不算:首签必须是用户明确点「签发」,后台扫描也跳过它', () => {
    expect(needsAttention(cert({ status: 'pending', daysLeft: 0, hasKey: false }))).toBe(false)
    expect(needsAttention(cert({ status: 'failed', daysLeft: 0, lastError: 'acme 429' }))).toBe(false)
  })

  it('离到期还远就不拦', () => {
    expect(needsAttention(cert({ daysLeft: 31, source: 'upload' }))).toBe(false)
  })

  it('filterAttentions 与逐条判定同一口径,列表和汇总数对得上', () => {
    const rows = [cert({ id: 'a', daysLeft: 5, source: 'upload' }), cert({ id: 'b', daysLeft: 200 }), cert({ id: 'c', status: 'expired' })]
    expect(filterAttentions(rows).map((c) => c.id)).toEqual(['a', 'c'])
  })
})

describe('canIssue', () => {
  it('只有 acme 来源能让平台签,导入的永远要用户自己换', () => {
    expect(canIssue('acme')).toBe(true)
    expect(canIssue('upload')).toBe(false)
  })
})

describe('parseDomains', () => {
  it('逗号 / 分号 / 顿号 / 空白 / 换行都算分隔,统一小写并去重保序', () => {
    expect(parseDomains('A.com, b.com;b.com、 a.com\n c.com  ')).toEqual(['a.com', 'b.com', 'c.com'])
  })

  it('空串与纯分隔符得到空清单', () => {
    expect(parseDomains('')).toEqual([])
    expect(parseDomains(' , , \n')).toEqual([])
  })
})

describe('isValidDomain', () => {
  it('放行普通域名与最左通配,含 punycode 与大写(先归一小写)', () => {
    expect(isValidDomain('example.com')).toBe(true)
    expect(isValidDomain('a.b.example.com')).toBe(true)
    expect(isValidDomain('*.example.com')).toBe(true)
    expect(isValidDomain('EXAMPLE.com')).toBe(true)
    expect(isValidDomain('xn--fiqs8s.example.com')).toBe(true)
  })

  it('拒绝后端规则外的形态:通配不在最左 / 无点 / 下划线 / 空标签 / 首尾连字符 / 协议前缀', () => {
    for (const bad of ['*a.example.com', 'a.*.example.com', 'example', 'ex_ample.com', 'a..b.com', '-a.com', 'a-.com', 'https://example.com', 'ex ample.com', '.com', '']) {
      expect(isValidDomain(bad)).toBe(false)
    }
  })

  it('拒绝过长域名:后端 ValidDomain 有 253 总长上限(标签各自合法,只卡总长)', () => {
    const label = `${'a'.repeat(60)}.`
    expect(isValidDomain(label.repeat(5) + 'com')).toBe(false)
    expect(isValidDomain(label.repeat(4) + 'com')).toBe(true)
  })
})

describe('invalidDomains', () => {
  it('只回非法的那些,顺序不变', () => {
    expect(invalidDomains(['a.com', 'bad_domain', '*.a.com', 'nope'])).toEqual(['bad_domain', 'nope'])
  })
})

describe('checkTargetPaths', () => {
  it('两条绝对且不同路径 = 通过(两侧的空格会被忽略)', () => {
    expect(checkTargetPaths('/etc/ssl/fullchain.pem', '/etc/ssl/privkey.pem')).toBe('')
    expect(checkTargetPaths(' /etc/a ', ' /etc/b ')).toBe('')
  })

  it('空路径与相对路径都归为「必须绝对路径」,与后端 ErrEmptyPath 同一档', () => {
    expect(checkTargetPaths('', '/etc/b')).toBe('notAbsolute')
    expect(checkTargetPaths('/etc/a', '')).toBe('notAbsolute')
    expect(checkTargetPaths('etc/a', '/etc/b')).toBe('notAbsolute')
    expect(checkTargetPaths('/etc/a', './b.pem')).toBe('notAbsolute')
  })

  it('两条同路径单独报错:那会把公开证书覆盖成私钥', () => {
    expect(checkTargetPaths('/etc/a.pem', '/etc/a.pem')).toBe('same')
    expect(checkTargetPaths(' /etc/a.pem ', '/etc/a.pem')).toBe('same')
  })
})

describe('落点草稿行(申请弹窗里一次填多台)', () => {
  it('整行没填 = 空白行,只填了空格也算空白', () => {
    expect(isBlankTargetDraft(emptyTargetDraft())).toBe(true)
    expect(isBlankTargetDraft({ ...emptyTargetDraft(), certPath: '  ' })).toBe(true)
    expect(isBlankTargetDraft({ ...emptyTargetDraft(), autoDeploy: true })).toBe(false)
  })

  it('逐行判:先要主机,再看两条路径(半填的行不能悄悄丢掉)', () => {
    expect(checkTargetDraft(emptyTargetDraft())).toBe('noServer')
    expect(checkTargetDraft({ ...emptyTargetDraft(), serverId: 's-1' })).toBe('notAbsolute')
    expect(
      checkTargetDraft({ ...emptyTargetDraft(), serverId: 's-1', certPath: '/etc/a.pem', keyPath: '/etc/a.pem' }),
    ).toBe('same')
    expect(
      checkTargetDraft({ ...emptyTargetDraft(), serverId: 's-1', certPath: '/etc/a.pem', keyPath: '/etc/a.key' }),
    ).toBe('')
  })

  it('汇总时去掉空行并修剪两侧空白,留下的就是要逐条提交的', () => {
    const rows = [
      emptyTargetDraft(),
      {
        ...emptyTargetDraft(),
        serverId: 's-1',
        certPath: ' /etc/a.pem ',
        keyPath: ' /etc/a.key ',
        postCommand: '  systemctl reload nginx  ',
      },
      emptyTargetDraft(),
    ]
    expect(compactTargetDrafts(rows)).toEqual([
      { serverId: 's-1', certPath: '/etc/a.pem', keyPath: '/etc/a.key', postCommand: 'systemctl reload nginx', autoDeploy: false },
    ])
  })
})

describe('matchesQuery / filterCerts', () => {
  const rows = [
    cert({ id: 'a', domain: 'shop.example.com', sans: ['shop.example.com', 'api.example.com'], issuer: 'LetsEncrypt R11' }),
    cert({ id: 'b', domain: 'old.example.org', sans: null, ca: 'zerossl', issuer: '' }),
  ]

  it('域名、SAN、签发者、CA 都能命中,大小写不敏感', () => {
    expect(filterCerts(rows, 'API.EXAMPLE').map((c) => c.id)).toEqual(['a'])
    expect(filterCerts(rows, 'encrypt').map((c) => c.id)).toEqual(['a'])
    expect(filterCerts(rows, 'zerossl').map((c) => c.id)).toEqual(['b'])
  })

  it('sans 为 null 的老行不参与 SAN 匹配,但域名照样能命中且不报错', () => {
    expect(matchesQuery(rows[1], 'api')).toBe(false)
    expect(matchesQuery(rows[1], 'ORG')).toBe(true)
    expect(filterCerts(rows, 'api').map((c) => c.id)).toEqual(['a'])
  })

  it('空查询与纯空格不过滤,顺序保持后端返回的样子', () => {
    expect(filterCerts(rows, '')).toBe(rows)
    expect(filterCerts(rows, '   ')).toBe(rows)
    expect(filterCerts(rows, 'example').map((c) => c.id)).toEqual(['a', 'b'])
  })
})

describe('checkReportClean', () => {
  it('三桶全空 = 这一轮没点到任何证书', () => {
    expect(checkReportClean(report({ checked: 4 }))).toBe(true)
  })

  it('任一桶有条目就不算干净', () => {
    const item = { certId: 'c-1', domain: 'example.com', status: 'issued' as const, daysLeft: 12, detail: '', deploy: '' }
    expect(checkReportClean(report({ renewed: [item] }))).toBe(false)
    expect(checkReportClean(report({ expiring: [item], failed: [item] }))).toBe(false)
  })
})
