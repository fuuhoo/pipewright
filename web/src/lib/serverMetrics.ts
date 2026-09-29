/**
 * 服务器指标的展示口径(卡片视图与列表视图共用同一套算术)。
 *
 * 为什么抽出来:两种视图只是排布不同,而「≥90% 算红、≥75% 算黄」「负载相对核数着色」
 * 「内存还有一个含页缓存的口径」这些判定必须一致 —— 同一台机在卡片里黄、在列表里红,
 * 用户只会认为其中一个在骗人。
 *
 * 文案走全局 `t`(与 lib/runStatus、lib/pipelineLabels 同一做法):切语言时由调用方的
 * 渲染重新求值,组件不必把 locale 一路传下来。
 *
 * 一律 best-effort 取值:后端在跨平台采集失败时给 null / 0,这里就回 null 或空串,
 * 让界面跳过那一格 —— 而不是拿 0 假装「用量为零」。
 */

import { t } from '../i18n'
import type { CpuMetric, MemoryMetric, SystemMetric } from '../api/servers'

/** 进度条/数字的着色档(与 ui/ProgressBar 的 variant 同一组词,少一档:这里没有成功态)。 */
export type MetricVariant = 'default' | 'warn' | 'error'

/** 用量百分比(0–100);分母为 0 或缺失 → null(不渲染进度)。 */
export function usagePercent(used: number, total: number): number | null {
  if (!total || total <= 0) return null
  return Math.min(100, Math.max(0, (used / total) * 100))
}

/** 进度条着色:>90% 红、>75% 黄、否则默认。 */
export function usageVariant(percent: number | null): MetricVariant {
  if (percent === null) return 'default'
  if (percent >= 90) return 'error'
  if (percent >= 75) return 'warn'
  return 'default'
}

/** 人读字节(二进制单位,1 位小数)。 */
export function humanBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return '—'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${i === 0 ? v : v.toFixed(1)} ${units[i]}`
}

export interface MemoryView {
  /** 真实占用口径(不含可回收页缓存)。 */
  percent: number | null
  /**
   * 「含缓存」口径的分母:有物理/分配总量时优先用它(对齐宿主面板如 PVE 的总量),
   * 否则回退 free 的可用总量。physicalTotalBytes 为 0 表示采集不到。
   */
  cacheTotal: number | null
  /** 含页缓存(total − free)/ 分母 —— 与 cgroup / 宿主面板「已用」百分比一致。 */
  cachePercent: number | null
  /** 交换分区用量;未配置 swap(总量 0)→ null,界面就不画这一行。 */
  swapPercent: number | null
}

export function memoryView(m: MemoryMetric | null): MemoryView {
  if (!m) return { percent: null, cacheTotal: null, cachePercent: null, swapPercent: null }
  const cacheTotal = m.physicalTotalBytes > 0 ? m.physicalTotalBytes : m.totalBytes
  return {
    percent: usagePercent(m.usedBytes, m.totalBytes),
    cacheTotal: cacheTotal > 0 ? cacheTotal : null,
    cachePercent: usagePercent(m.usedWithCacheBytes, cacheTotal),
    swapPercent: m.swapTotalBytes > 0 ? usagePercent(m.swapUsedBytes, m.swapTotalBytes) : null,
  }
}

/** CPU 负载相对核数的健康着色(无核数则不着色 —— 没有参照系的数字不该判健康)。 */
export function loadVariant(cpu: CpuMetric | null): MetricVariant {
  if (!cpu || cpu.loadavg1 === null) return 'default'
  const cores = cpu.cores ?? 0
  if (cores <= 0) return 'default'
  const ratio = cpu.loadavg1 / cores
  if (ratio >= 1) return 'error'
  if (ratio >= 0.7) return 'warn'
  return 'default'
}

export function loadAvailable(cpu: CpuMetric | null): boolean {
  return !!cpu && cpu.loadavg1 !== null
}

/** 「1.23 / 4 核」;采不到就是「不可用」,不写成 0.00(那是另一个谎)。 */
export function loadText(cpu: CpuMetric | null): string {
  if (!cpu || cpu.loadavg1 === null) return t('opsServer.metrics.unavailable')
  const coresText = cpu.cores !== null ? t('opsServer.metrics.cores', { n: cpu.cores }) : ''
  return `${cpu.loadavg1.toFixed(2)}${coresText}`
}

/** 运行时长:最多给两级(3 天 4 小时 / 5 小时 12 分 / 8 分 / 40 秒);采不到 → 空。 */
export function uptimeText(sec: number): string {
  if (sec <= 0) return ''
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (d > 0) return h > 0 ? t('opsServer.metrics.uptimeDh', { d, h }) : t('opsServer.metrics.uptimeD', { d })
  if (h > 0) return m > 0 ? t('opsServer.metrics.uptimeHm', { h, m }) : t('opsServer.metrics.uptimeH', { h })
  if (m > 0) return t('opsServer.metrics.uptimeM', { m })
  return t('opsServer.metrics.uptimeS', { s: sec })
}

/** 主行:发行版优先(macOS 15.5 / Ubuntu 24.04.2 LTS),没有就退内核名(Linux)。 */
export function sysPrimary(s: SystemMetric | null): string {
  if (!s) return ''
  return s.distro || s.os
}

/** 次行片段:内核(带上内核名以免主行没给)、架构、主机名、运行时长。 */
export function sysParts(s: SystemMetric | null): string[] {
  if (!s) return []
  const primary = sysPrimary(s)
  const kernel = s.kernel ? (s.os && !primary.includes(s.os) ? `${s.os} ${s.kernel}` : s.kernel) : ''
  const up = uptimeText(s.uptimeSeconds)
  return [kernel, s.arch, s.hostname, up ? t('opsServer.metrics.uptime', { text: up }) : ''].filter(Boolean)
}

/** 采集时刻(只显示时分秒;完整时间在悬浮提示里)。 */
export function collectedClock(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleTimeString()
}
