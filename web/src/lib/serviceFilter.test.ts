import { describe, it, expect } from 'vitest'
import type { Service } from '../api/services'
import { SERVICE_ALL, SERVICE_NONE, matchesService, serviceOptions } from './serviceFilter'

const svc = (id: string, name: string): Service => ({
  id,
  name,
  description: '',
  projectCount: 0,
  createdAt: '',
  updatedAt: '',
})

describe('serviceOptions', () => {
  it('puts 全部 / 未归属 first, then each service in backend order', () => {
    const opts = serviceOptions([svc('s1', '订单中心'), svc('s2', '支付')], {
      all: '全部服务',
      unassigned: '未归属',
    })
    expect(opts).toEqual([
      { value: SERVICE_ALL, label: '全部服务' },
      { value: SERVICE_NONE, label: '未归属' },
      { value: 's1', label: '订单中心' },
      { value: 's2', label: '支付' },
    ])
  })
})

describe('matchesService', () => {
  it('全部放行', () => {
    expect(matchesService('s1', SERVICE_ALL)).toBe(true)
    expect(matchesService('', SERVICE_ALL)).toBe(true)
  })

  it('未归属只留空串(undefined 视同未归属)', () => {
    expect(matchesService('', SERVICE_NONE)).toBe(true)
    expect(matchesService(undefined, SERVICE_NONE)).toBe(true)
    expect(matchesService('s1', SERVICE_NONE)).toBe(false)
  })

  it('指定服务按 ID 精确命中', () => {
    expect(matchesService('s1', 's1')).toBe(true)
    expect(matchesService('s2', 's1')).toBe(false)
    expect(matchesService('', 's1')).toBe(false)
  })
})
