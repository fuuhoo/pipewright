<!--
  ServerMetricsCard.vue — Story 6-1: 多机状态总览(FR-15,服务器层资源指标)

  单台已登记服务器的指标卡:
    · 名称 + reachable 徽标(可达 / 不可达)
    · 系统信息行:发行版 / 内核 / 架构 / 主机名 / 运行时长(该机自报,缺哪段跳哪段)
    · CPU:1 分钟负载 + 核数(负载相对核数着色:>1×核数 偏红、>0.7× 偏黄)
    · 内存 used/total 进度条 + 人读字节
    · 磁盘 used/total 进度条 + 人读字节
    · 某指标 null → 该行标「不可用」(跨平台 best-effort:该机没有对应命令/解析不出才缺)
    · 不可达 → 整卡灰显 + 人读错误(绝不含凭据明文)

  复用 1-6 ui:ProgressBar(进度条)。徽标用本组件内联(reachable 语义,非 6-state run 状态)。
-->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import ProgressBar, { type ProgressVariant } from '../ui/ProgressBar.vue'
import type { ServerMetrics } from '../../api/servers'

const { t } = useI18n()

const props = defineProps<{
  /** 展示用服务器名(列表 join 而来)。 */
  name: string
  /** 该台指标(reachable:false 时各指标为 null)。 */
  metrics: ServerMetrics
}>()

/** 只有可达的机器画「远程」按钮:不可达点了只是拿到一条连不上的错误。 */
const emit = defineEmits<{ (e: 'remote', serverId: string): void }>()

// ─── derived display ───────────────────────────────────────────────────────────

/** 用量百分比(0–100);分母为 0 或缺失 → null(不渲染进度)。 */
function pct(used: number, total: number): number | null {
  if (!total || total <= 0) return null
  return Math.min(100, Math.max(0, (used / total) * 100))
}

/** 进度条着色:>90% 红、>75% 黄、否则默认。 */
function usageVariant(percent: number | null): ProgressVariant {
  if (percent === null) return 'default'
  if (percent >= 90) return 'error'
  if (percent >= 75) return 'warn'
  return 'default'
}

/** 人读字节(二进制单位,1 位小数)。 */
function humanBytes(n: number): string {
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

// ─── system info(该机自报的静态标识;缺段直接跳过,不标「不可用」) ──────────────

const system = computed(() => props.metrics.system)

/** 主行:发行版优先(macOS 15.5 / Ubuntu 24.04.2 LTS),没有就退内核名(Linux)。 */
const sysPrimary = computed(() => {
  const s = system.value
  if (!s) return ''
  return s.distro || s.os
})

/** 运行时长:最多给两级(3 天 4 小时 / 5 小时 12 分 / 8 分 / 40 秒);采不到 → 空。 */
const uptimeText = computed(() => {
  const sec = system.value?.uptimeSeconds ?? 0
  if (sec <= 0) return ''
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (d > 0) return h > 0 ? t('opsServer.metrics.uptimeDh', { d, h }) : t('opsServer.metrics.uptimeD', { d })
  if (h > 0) return m > 0 ? t('opsServer.metrics.uptimeHm', { h, m }) : t('opsServer.metrics.uptimeH', { h })
  if (m > 0) return t('opsServer.metrics.uptimeM', { m })
  return t('opsServer.metrics.uptimeS', { s: sec })
})

/** 次行片段:内核(带上内核名以免主行没给)、架构、主机名、运行时长。 */
const sysParts = computed(() => {
  const s = system.value
  if (!s) return []
  const kernel = s.kernel ? (s.os && !sysPrimary.value.includes(s.os) ? `${s.os} ${s.kernel}` : s.kernel) : ''
  return [kernel, s.arch, s.hostname, uptimeText.value ? t('opsServer.metrics.uptime', { text: uptimeText.value }) : ''].filter(Boolean)
})

const memPercent = computed(() =>
  props.metrics.memory ? pct(props.metrics.memory.usedBytes, props.metrics.memory.totalBytes) : null,
)
/**
 * 「含缓存」口径的分母:有物理/分配总量时优先用它(对齐宿主面板如 PVE 的总量),
 * 否则回退 free 的可用总量。physicalTotalBytes 为 0 表示采集不到。
 */
const memCacheTotal = computed(() => {
  const m = props.metrics.memory
  if (!m) return null
  return m.physicalTotalBytes > 0 ? m.physicalTotalBytes : m.totalBytes
})
/** 含页缓存(total - free)/ 物理总量 —— 与 cgroup / 宿主面板「已用」百分比一致。 */
const memCachePercent = computed(() =>
  props.metrics.memory && memCacheTotal.value
    ? pct(props.metrics.memory.usedWithCacheBytes, memCacheTotal.value)
    : null,
)
const diskPercent = computed(() =>
  props.metrics.disk ? pct(props.metrics.disk.usedBytes, props.metrics.disk.totalBytes) : null,
)

/** 交换分区用量(swapTotalBytes 为 0 → 未配置,不渲染)。 */
const swapPercent = computed(() => {
  const m = props.metrics.memory
  if (!m || m.swapTotalBytes <= 0) return null
  return pct(m.swapUsedBytes, m.swapTotalBytes)
})

/** CPU 负载相对核数的健康着色(无核数则不着色)。 */
const loadVariant = computed<ProgressVariant>(() => {
  const cpu = props.metrics.cpu
  if (!cpu || cpu.loadavg1 === null) return 'default'
  const cores = cpu.cores ?? 0
  if (cores <= 0) return 'default'
  const ratio = cpu.loadavg1 / cores
  if (ratio >= 1) return 'error'
  if (ratio >= 0.7) return 'warn'
  return 'default'
})

const loadAvailable = computed(() => {
  const cpu = props.metrics.cpu
  return !(!cpu || cpu.loadavg1 === null)
})

const loadText = computed(() => {
  const cpu = props.metrics.cpu
  if (!cpu || cpu.loadavg1 === null) return t('opsServer.metrics.unavailable')
  const coresText = cpu.cores !== null ? t('opsServer.metrics.cores', { n: cpu.cores }) : ''
  return `${cpu.loadavg1.toFixed(2)}${coresText}`
})
</script>

<template>
  <article
    class="metrics-card"
    :class="{ 'metrics-card--unreachable': !metrics.reachable }"
    :aria-label="t('opsServer.metrics.cardAria', { name })"
  >
    <header class="metrics-card__head">
      <h3 class="metrics-card__name" :title="name">{{ name }}</h3>
      <span
        class="reach-badge"
        :class="metrics.reachable ? 'reach-badge--ok' : 'reach-badge--down'"
        role="status"
      >
        <span class="reach-badge__dot" aria-hidden="true" />
        {{ metrics.reachable ? t('opsServer.metrics.reachable') : t('opsServer.metrics.unreachable') }}
      </span>
    </header>

    <!-- Unreachable: human error, no metrics rows -->
    <p v-if="!metrics.reachable" class="metrics-card__error" role="alert">
      {{ metrics.error || t('opsServer.metrics.collectFailed') }}
    </p>

    <!-- Reachable: metric rows (each independently degradable) -->
    <dl v-else class="metrics-card__body">
      <!-- 系统信息(静态标识,不是指标):主行发行版,次行内核 / 架构 / 主机名 / 运行时长 -->
      <div v-if="system" class="metric-row metric-row--sys">
        <dt class="metric-row__label">{{ t('opsServer.metrics.system') }}</dt>
        <dd class="metric-row__value">
          <span class="sys-primary" :title="sysPrimary">{{ sysPrimary }}</span>
          <span v-if="sysParts.length" class="sys-meta">{{ sysParts.join(' · ') }}</span>
        </dd>
      </div>

      <!-- CPU -->
      <div class="metric-row">
        <dt class="metric-row__label">{{ t('opsServer.metrics.cpuLoad') }}</dt>
        <dd class="metric-row__value">
          <span
            class="metric-num"
            :class="{
              'metric-num--warn': loadVariant === 'warn',
              'metric-num--error': loadVariant === 'error',
              'metric-num--na': !loadAvailable,
            }"
            >{{ loadText }}</span
          >
        </dd>
      </div>

      <!-- Memory -->
      <div class="metric-row">
        <dt class="metric-row__label">{{ t('opsServer.metrics.memory') }}</dt>
        <dd class="metric-row__value">
          <template v-if="metrics.memory && memPercent !== null">
            <ProgressBar
              :value="memPercent"
              :variant="usageVariant(memPercent)"
              :label="t('opsServer.metrics.memUsageLabel', { n: memPercent.toFixed(0) })"
            />
            <span class="metric-sub">
              {{ humanBytes(metrics.memory.usedBytes) }} / {{ humanBytes(metrics.memory.totalBytes) }}
              <span class="metric-pct">({{ memPercent.toFixed(0) }}%)</span>
            </span>
            <span
              v-if="memCachePercent !== null && memCacheTotal !== null"
              class="metric-sub metric-sub--cache"
              :title="t('opsServer.metrics.cacheTooltip')"
            >
              {{ t('opsServer.metrics.withCache') }} {{ humanBytes(metrics.memory.usedWithCacheBytes) }} /
              {{ humanBytes(memCacheTotal) }}
              <span class="metric-pct">({{ memCachePercent.toFixed(0) }}%)</span>
              <span v-if="metrics.memory.physicalTotalBytes > 0" class="metric-tag">{{ t('opsServer.metrics.physical') }}</span>
            </span>
          </template>
          <span v-else class="metric-num metric-num--na">{{ t('opsServer.metrics.unavailable') }}</span>
        </dd>
      </div>

      <!-- Swap(仅在配置了 swap 时显示) -->
      <div v-if="swapPercent !== null && metrics.memory" class="metric-row">
        <dt class="metric-row__label">{{ t('opsServer.metrics.swap') }}</dt>
        <dd class="metric-row__value">
          <ProgressBar
            :value="swapPercent"
            :variant="usageVariant(swapPercent)"
            :label="t('opsServer.metrics.swapUsageLabel', { n: swapPercent.toFixed(0) })"
          />
          <span class="metric-sub">
            {{ humanBytes(metrics.memory.swapUsedBytes) }} /
            {{ humanBytes(metrics.memory.swapTotalBytes) }}
            <span class="metric-pct">({{ swapPercent.toFixed(0) }}%)</span>
          </span>
        </dd>
      </div>

      <!-- Disk -->
      <div class="metric-row">
        <dt class="metric-row__label">{{ t('opsServer.metrics.disk', { path: metrics.disk?.path ?? '/' }) }}</dt>
        <dd class="metric-row__value">
          <template v-if="metrics.disk && diskPercent !== null">
            <ProgressBar
              :value="diskPercent"
              :variant="usageVariant(diskPercent)"
              :label="t('opsServer.metrics.diskUsageLabel', { n: diskPercent.toFixed(0) })"
            />
            <span class="metric-sub">
              {{ humanBytes(metrics.disk.usedBytes) }} / {{ humanBytes(metrics.disk.totalBytes) }}
              <span class="metric-pct">({{ diskPercent.toFixed(0) }}%)</span>
            </span>
          </template>
          <span v-else class="metric-num metric-num--na">{{ t('opsServer.metrics.unavailable') }}</span>
        </dd>
      </div>
    </dl>

    <footer v-if="metrics.reachable" class="metrics-card__foot">
      <span class="metrics-card__collected">
        {{ t('opsServer.metrics.collectedAt', { time: new Date(metrics.collectedAt).toLocaleTimeString() }) }}
      </span>
      <button class="metrics-card__remote" type="button" @click="emit('remote', metrics.serverId)">
        {{ t('remoteWorkspace.button') }}
      </button>
    </footer>
  </article>
</template>

<style scoped>
.metrics-card {
  display: flex;
  flex-direction: column;
  gap: 14px;
  padding: 18px 18px 14px;
  border: 1px solid var(--color-line);
  border-radius: var(--rounded-lg);
  background: var(--color-surface);
  transition: border-color var(--duration-fast) var(--ease-out-expo);
}
.metrics-card:hover {
  border-color: var(--color-line-strong, var(--color-line));
}
.metrics-card--unreachable {
  opacity: 0.62;
  background: var(--color-inset);
}

.metrics-card__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
}
.metrics-card__name {
  margin: 0;
  font-size: var(--text-body);
  font-weight: 650;
  color: var(--color-text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* ——— reachability badge (reachable semantics, distinct from run StatusBadge) ——— */
.reach-badge {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
  font-size: var(--text-label);
  font-weight: 600;
  line-height: 1;
  padding: 3px 9px 3px 8px;
  border-radius: var(--rounded-md);
}
.reach-badge__dot {
  width: 6px;
  height: 6px;
  border-radius: var(--rounded-full);
  flex-shrink: 0;
}
.reach-badge--ok {
  color: var(--color-green);
  background: var(--color-green-soft);
}
.reach-badge--ok .reach-badge__dot {
  background: var(--color-green);
}
.reach-badge--down {
  color: var(--color-red);
  background: var(--color-red-soft);
  border: 1px solid var(--color-red-line);
}
.reach-badge--down .reach-badge__dot {
  background: var(--color-red);
}

.metrics-card__error {
  margin: 0;
  font-size: var(--text-label);
  color: var(--color-red);
  line-height: 1.5;
}

.metrics-card__body {
  display: flex;
  flex-direction: column;
  gap: 14px;
  margin: 0;
}
.metric-row {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.metric-row__label {
  font-size: var(--text-label);
  font-weight: 600;
  color: var(--color-dim);
}
.metric-row__value {
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: 5px;
}

/* ——— 系统信息行:静态标识,压成两行小字,别和指标抢视线 ——— */
.metric-row--sys {
  gap: 3px;
  padding-bottom: 12px;
  border-bottom: 1px solid var(--color-line);
}
.sys-primary {
  font-size: var(--text-body);
  font-weight: 600;
  color: var(--color-text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.sys-meta {
  font-size: var(--text-label);
  color: var(--color-dim);
  word-break: break-word;
}
.metric-num {
  font-variant-numeric: tabular-nums;
  font-weight: 600;
  font-size: var(--text-body);
  color: var(--color-text);
}
.metric-num--warn {
  color: var(--color-amber);
}
.metric-num--error {
  color: var(--color-red);
}
.metric-num--na {
  color: var(--color-faint);
  font-weight: 500;
  font-style: italic;
}
.metric-sub {
  font-size: var(--text-label);
  color: var(--color-dim);
  font-variant-numeric: tabular-nums;
}
.metric-pct {
  color: var(--color-faint);
}
.metric-sub--cache {
  color: var(--color-faint);
  cursor: help;
}
.metric-tag {
  margin-left: 4px;
  padding: 0 5px;
  border-radius: var(--rounded-sm, 4px);
  font-size: 0.68rem;
  font-weight: 600;
  color: var(--color-dim);
  background: var(--color-inset);
  vertical-align: 1px;
}

.metrics-card__foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  font-size: var(--text-label);
  color: var(--color-faint);
  font-variant-numeric: tabular-nums;
}

/* 「远程」开弹窗(终端 + 文件面板)。与采集时间同一行:卡片主体留给指标,入口不占高度。 */
.metrics-card__remote {
  font: inherit;
  font-weight: 600;
  color: var(--color-dim);
  background: var(--color-inset);
  border: 1px solid var(--color-line);
  border-radius: var(--rounded-sm);
  padding: 2px 9px;
  cursor: pointer;
  white-space: nowrap;
}
.metrics-card__remote:hover {
  color: var(--color-text);
  border-color: var(--color-line-strong, var(--color-line));
}
</style>
