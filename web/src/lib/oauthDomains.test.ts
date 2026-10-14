import { describe, it, expect } from 'vitest'
import { invalidOAuthDomains, joinOAuthDomains, parseOAuthDomains } from './oauthDomains'

describe('parseOAuthDomains', () => {
  it('splits on every separator a person is likely to type', () => {
    expect(parseOAuthDomains('a.com,b.com c.io;d.org,,  e.net\n f.com')).toEqual([
      'a.com',
      'b.com',
      'c.io',
      'd.org',
      'e.net',
      'f.com',
    ])
  })

  it('lowercases and dedupes case-variant entries', () => {
    expect(parseOAuthDomains('Corp.Example.com corp.example.COM')).toEqual(['corp.example.com'])
  })

  it('returns nothing for blank input (blank means "no domain restriction")', () => {
    expect(parseOAuthDomains('')).toEqual([])
    expect(parseOAuthDomains('   ,; ,')).toEqual([])
  })

  it('keeps the first-seen order so the echo back to the box is stable', () => {
    expect(parseOAuthDomains('z.com a.com')).toEqual(['z.com', 'a.com'])
  })
})

describe('invalidOAuthDomains', () => {
  it('accepts plain domains only', () => {
    expect(invalidOAuthDomains('corp.example.com')).toEqual([])
    expect(invalidOAuthDomains('a.b.c.example.com, x-1.dev')).toEqual([])
  })

  // 每一项都是「静默失效」的形态:白名单看着填了,实际谁都匹配不上。
  it('rejects the shapes people paste from a browser bar', () => {
    for (const raw of [
      'https://corp.example.com',
      'corp.example.com/path',
      'corp.example.com:8443',
      '@corp.example.com',
      '.corp.example.com',
      'corp.example.com.',
      'corp..example.com',
      '-corp.example.com',
      'corp-.example.com',
      'corp_example.com',
      'localhost',
      '例え.jp',
    ]) {
      expect(invalidOAuthDomains(raw), raw).toEqual([parseOAuthDomains(raw)[0]])
    }
  })

  it('reports only the offending items so the message can point at them', () => {
    expect(invalidOAuthDomains('a.com, https://b.com ,c.com/x')).toEqual(['https://b.com', 'c.com/x'])
  })
})

describe('joinOAuthDomains', () => {
  it('renders the saved list back into the box', () => {
    expect(joinOAuthDomains(['corp.example.com', 'hr.example.com'])).toBe(
      'corp.example.com, hr.example.com',
    )
    expect(joinOAuthDomains([])).toBe('')
  })

  it('round-trips: join → parse gives the same list back', () => {
    const list = ['corp.example.com', 'hr.example.com']
    expect(parseOAuthDomains(joinOAuthDomains(list))).toEqual(list)
  })
})
