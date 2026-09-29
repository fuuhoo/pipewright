/**
 * 指标展示口径的回归:卡片视图与列表视图共用这些函数,所以这里盯的是「同一台机在两种
 * 视图里必须同色同数」的那一层判定,而不是排版。
 *
 * 最容易写错的是那些「采不到」的分支:回 0 会被读成「用量为零」,回 null 才让界面跳过那一格。
 * 语言在 setup 里钉死 zh-CN,所以断言直接写中文文案。
 */
import { describe, expect, it } from 'vitest'
import {
  collectedClock,
  humanBytes,
  loadAvailable,
  loadText,
  loadVariant,
  memoryView,
  sysParts,
  sysPrimary,
  uptimeText,
  usagePercent,
  usageVariant,
} from './serverMetrics'
import type { CpuMetric, MemoryMetric, SystemMetric } from '../api/servers'

function mem(over: Partial<MemoryMetric> = {}): MemoryMetric {
  return {
    usedBytes: 4 * 1024 ** 3,
    usedWithCacheBytes: 6 * 1024 ** 3,
    totalBytes: 8 * 1024 ** 3,
    physicalTotalBytes: 0,
    swapUsedBytes: 0,
    swapTotalBytes: 0,
    ...over,
  }
}

function sys(over: Partial<SystemMetric> = {}): SystemMetric {
  return { os: 'Linux', distro: 'Ubuntu 24.04.2 LTS', kernel: '6.6.87', arch: 'x86_64', hostname: 'node-1', uptimeSeconds: 0, ...over }
}

describe('usagePercent / usageVariant', () => {
  it('分母为 0 或缺失回 null,不给出 0%', () => {
    expect(usagePercent(10, 0)).toBeNull()
    expect(usagePercent(0, 0)).toBeNull()
    expect(usagePercent(5, 10)).toBe(50)
  })

  it('用量的着色门是 75% 黄、90% 红', () => {
    expect(usageVariant(null)).toBe('default')
    expect(usageVariant(74.9)).toBe('default')
    expect(usageVariant(75)).toBe('warn')
    expect(usageVariant(89.9)).toBe('warn')
    expect(usageVariant(90)).toBe('error')
  })
})

describe('humanBytes', () => {
  it('字节按二进制单位逐级走,负数与非有限值当没有', () => {
    expect(humanBytes(0)).toBe('0 B')
    expect(humanBytes(512)).toBe('512 B')
    expect(humanBytes(1024)).toBe('1.0 KiB')
    expect(humanBytes(8 * 1024 ** 3)).toBe('8.0 GiB')
    expect(humanBytes(-1)).toBe('—')
    expect(humanBytes(Number.POSITIVE_INFINITY)).toBe('—')
  })
})

describe('memoryView', () => {
  it('没有内存指标时四格全空(而不是 0%)', () => {
    expect(memoryView(null)).toEqual({ percent: null, cacheTotal: null, cachePercent: null, swapPercent: null })
  })

  it('真实占用与含缓存占用是两个口径,分母各算各的', () => {
    const v = memoryView(mem())
    expect(v.percent).toBe(50)
    expect(v.cacheTotal).toBe(8 * 1024 ** 3)
    expect(v.cachePercent).toBe(75)
    expect(v.swapPercent).toBeNull()
  })

  it('采到物理总量时含缓存口径改用它当分母', () => {
    const v = memoryView(mem({ physicalTotalBytes: 16 * 1024 ** 3 }))
    expect(v.cacheTotal).toBe(16 * 1024 ** 3)
    expect(v.cachePercent).toBe(37.5)
  })

  it('配置了 swap 才给出 swap 百分比', () => {
    expect(memoryView(mem({ swapTotalBytes: 2 * 1024 ** 3, swapUsedBytes: 1 * 1024 ** 3 })).swapPercent).toBe(50)
  })
})

describe('负载', () => {
  it('相对核数判健康:1× 核数红、0.7× 黄,没有核数就不判', () => {
    const cpu = (loadavg1: number | null, cores: number | null): CpuMetric => ({ loadavg1, cores })
    expect(loadVariant(cpu(2.0, 4))).toBe('default')
    expect(loadVariant(cpu(2.8, 4))).toBe('warn')
    expect(loadVariant(cpu(4, 4))).toBe('error')
    expect(loadVariant(cpu(12, null))).toBe('default')
    expect(loadVariant(cpu(null, 4))).toBe('default')
    expect(loadVariant(null)).toBe('default')
  })

  it('采不到就明说「不可用」,不写成 0.00', () => {
    expect(loadAvailable(null)).toBe(false)
    expect(loadAvailable({ loadavg1: null, cores: 4 })).toBe(false)
    expect(loadAvailable({ loadavg1: 0, cores: 4 })).toBe(true)
    expect(loadText(null)).toBe('不可用')
    expect(loadText({ loadavg1: 0, cores: null })).toBe('0.00')
    expect(loadText({ loadavg1: 1.234, cores: 4 })).toBe('1.23 / 4 核')
  })
})

describe('uptimeText', () => {
  it('最多两级单位,采不到(0 秒)回空串', () => {
    expect(uptimeText(0)).toBe('')
    expect(uptimeText(-5)).toBe('')
    expect(uptimeText(40)).toBe('40 秒')
    expect(uptimeText(8 * 60)).toBe('8 分')
    expect(uptimeText(5 * 3600)).toBe('5 小时')
    expect(uptimeText(5 * 3600 + 12 * 60)).toBe('5 小时 12 分')
    expect(uptimeText(3 * 86400)).toBe('3 天')
    expect(uptimeText(3 * 86400 + 4 * 3600)).toBe('3 天 4 小时')
  })
})

describe('系统标识', () => {
  it('主行优先发行版,没有就给内核名', () => {
    expect(sysPrimary(null)).toBe('')
    expect(sysPrimary(sys())).toBe('Ubuntu 24.04.2 LTS')
    expect(sysPrimary(sys({ distro: '' }))).toBe('Linux')
  })

  it('主行没带内核名时,次行的内核要把内核名补上', () => {
    // 发行版是 Ubuntu,不含「Linux」字样 → 内核段要自己把内核名带上,否则读者不知道那串数字是什么。
    expect(sysParts(sys())).toEqual(['Linux 6.6.87', 'x86_64', 'node-1'])
    expect(sysParts(sys({ distro: '' }))).toEqual(['6.6.87', 'x86_64', 'node-1'])
  })

  it('运行时长是可选片段,静态字段缺失就整段跳过', () => {
    expect(sysParts(sys({ uptimeSeconds: 3 * 86400 + 4 * 3600, hostname: '' }))).toEqual(['Linux 6.6.87', 'x86_64', '已运行 3 天 4 小时'])
    expect(sysParts(sys({ kernel: '', arch: '', hostname: '', uptimeSeconds: 0 }))).toEqual([])
    expect(sysParts(null)).toEqual([])
  })
})

describe('collectedClock', () => {
  it('给出时分秒,时间戳不可读就回空串', () => {
    expect(collectedClock('not-a-time')).toBe('')
    expect(collectedClock('2026-09-28T09:00:00Z')).toMatch(/\d/)
  })
})
