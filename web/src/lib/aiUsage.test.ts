import { describe, expect, it } from 'vitest'
import { aiMonthUsageParts, aiUsageParts } from './aiUsage'

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

describe('aiMonthUsageParts', () => {
  it('本月累计也走千分位,并标出非空', () => {
    expect(aiMonthUsageParts({ monthPrompt: 12345, monthCompletion: 678 }))
      .toEqual({ prompt: '12,345', completion: '678', empty: false })
  })

  it('本月没用过 → empty=true(页面说「还没用过」,而不是摆两个 0)', () => {
    expect(aiMonthUsageParts({ monthPrompt: 0, monthCompletion: 0 }))
      .toEqual({ prompt: '0', completion: '0', empty: true })
    expect(aiMonthUsageParts({ monthPrompt: -5, monthCompletion: 0 }))
      .toEqual({ prompt: '0', completion: '0', empty: true })
  })

  it('只有一项非零也算有用量', () => {
    expect(aiMonthUsageParts({ monthPrompt: 0, monthCompletion: 12 }))
      .toEqual({ prompt: '0', completion: '12', empty: false })
  })

  it('服务端没带 usage 字段 → null(旧 payload 不瞎猜)', () => {
    expect(aiMonthUsageParts(undefined)).toBeNull()
    expect(aiMonthUsageParts(null)).toBeNull()
  })
})
