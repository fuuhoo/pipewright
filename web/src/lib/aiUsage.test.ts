import { describe, expect, it } from 'vitest'
import { aiUsageParts } from './aiUsage'

describe('aiUsageParts', () => {
  it('把用量拆成千分位的进/出两段', () => {
    expect(aiUsageParts({ prompt: 1200, completion: 340 })).toEqual({ prompt: '1,200', completion: '340' })
    expect(aiUsageParts({ prompt: 1234567, completion: 89 })).toEqual({ prompt: '1,234,567', completion: '89' })
  })

  it('端点没回传用量时返回 null(调用方隐藏整行,不显示 0)', () => {
    expect(aiUsageParts(undefined)).toBeNull()
    expect(aiUsageParts(null)).toBeNull()
    expect(aiUsageParts({ prompt: 0, completion: 0 })).toBeNull()
    expect(aiUsageParts({ prompt: -1, completion: -1 })).toBeNull()
  })

  it('只回传一项也照实显示', () => {
    expect(aiUsageParts({ prompt: 0, completion: 12 })).toEqual({ prompt: '0', completion: '12' })
    expect(aiUsageParts({ prompt: 12, completion: 0 })).toEqual({ prompt: '12', completion: '0' })
  })
})
