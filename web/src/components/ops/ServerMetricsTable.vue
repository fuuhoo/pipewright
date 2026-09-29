<!--
  ServerMetricsTable.vue — 服务器状态的列表视图(与卡片视图同一份数据、同一套口径)

  存在的理由:机器多起来以后卡片网格要滚很久才能横向比较「谁的内存最紧」。列表把每台压成
  一行,列对齐,能一眼扫下来。

  两条不许改的行为:
    · 百分比/着色/人读字节全走 lib/serverMetrics —— 同一台机在卡片里黄、在列表里红是谎报。
    · 不可达那行只给「状态 + 错误 + 一排不可用」,不给「远程」按钮:点了只会拿到同一条连不上。
-->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import ProgressBar from '../ui/ProgressBar.vue'
import type { ServerMetrics } from '../../api/servers'
import {
  collectedClock,
  humanBytes,
  loadAvailable as loadAvailableOf,
  loadText as loadTextOf,
  loadVariant as loadVariantOf,
  memoryView,
  sysParts as sysPartsOf,
  sysPrimary as sysPrimaryOf,
  usagePercent,
  usageVariant,
  type MetricVariant,
} from '../../lib/serverMetrics'

const { t } = useI18n()

/** 一行 = 一台机的指标 + 列表要显示的登记信息(名称/地址由页面 join 好传进来)。 */
const props = defineProps<{
  rows: Array<{ metrics: ServerMetrics; name: string; addr: string }>
}>()

const emit = defineEmits<{ (e: 'remote', serverId: string): void }>()

interface RowView {
  metrics: ServerMetrics
  name: string
  addr: string
  sysPrimary: string
  sysParts: string[]
  loadText: string
  loadVariant: MetricVariant
  loadAvailable: boolean
  memPercent: number | null
  memUsedText: string
  memCacheLine: string
  memCachePct: string
  memPhysical: boolean
  swapLine: string
  diskPercent: number | null
  diskPath: string
  diskLine: string
  clock: string
}

/** 一次算完一行要的所有展示值:模板里反复调 memoryView 只会让同一格算出两种口径的机会变多。 */
const views = computed<RowView[]>(() =>
  props.rows.map((row) => {
    const m = row.metrics
    const mv = memoryView(m.memory)
    const cachePct = mv.cachePercent
    const cacheTotal = mv.cacheTotal
    const disk = m.disk
    const diskPct = disk ? usagePercent(disk.usedBytes, disk.totalBytes) : null
    return {
      metrics: m,
      name: row.name,
      addr: row.addr,
      sysPrimary: sysPrimaryOf(m.system),
      sysParts: sysPartsOf(m.system),
      loadText: loadTextOf(m.cpu),
      loadVariant: loadVariantOf(m.cpu),
      loadAvailable: loadAvailableOf(m.cpu),
      memPercent: mv.percent,
      memUsedText: m.memory ? `${humanBytes(m.memory.usedBytes)} / ${humanBytes(m.memory.totalBytes)}` : '',
      memCacheLine:
        m.memory && cachePct !== null && cacheTotal !== null
          ? `${humanBytes(m.memory.usedWithCacheBytes)} / ${humanBytes(cacheTotal)}`
          : '',
      memCachePct: cachePct !== null ? `(${cachePct.toFixed(0)}%)` : '',
      memPhysical: (m.memory?.physicalTotalBytes ?? 0) > 0,
      swapLine:
        m.memory && mv.swapPercent !== null
          ? `${t('opsServer.metrics.swap')} ${humanBytes(m.memory.swapUsedBytes)} / ${humanBytes(m.memory.swapTotalBytes)} (${mv.swapPercent.toFixed(0)}%)`
          : '',
      diskPercent: diskPct,
      diskPath: disk?.path ?? '/',
      diskLine: disk ? `${humanBytes(disk.usedBytes)} / ${humanBytes(disk.totalBytes)}` : '',
      clock: collectedClock(m.collectedAt),
    }
  }),
)

const na = computed(() => t('opsServer.metrics.unavailable'))
</script>

<template>
  <div class="metrics-table-wrap">
    <table class="metrics-table" :aria-label="t('serverStatus.listAria')">
      <thead>
        <tr>
          <th scope="col" class="col-server">{{ t('serverStatus.list.colServer') }}</th>
          <th scope="col" class="col-addr">{{ t('serverStatus.list.colAddr') }}</th>
          <th scope="col" class="col-status">{{ t('serverStatus.list.colStatus') }}</th>
          <th scope="col">{{ t('serverStatus.list.colSystem') }}</th>
          <th scope="col" class="col-num">{{ t('serverStatus.list.colCpu') }}</th>
          <th scope="col" class="col-usage">{{ t('serverStatus.list.colMem') }}</th>
          <th scope="col" class="col-usage">{{ t('serverStatus.list.colDisk') }}</th>
          <th scope="col" class="col-clock">{{ t('serverStatus.list.colCollected') }}</th>
          <th scope="col" class="col-ops">{{ t('serverStatus.list.colOps') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr
          v-for="r in views"
          :key="r.metrics.serverId"
          :class="{ 'metrics-table__row--down': !r.metrics.reachable }"
        >
          <td class="col-server">
            <span class="st-name" :title="r.name">{{ r.name }}</span>
            <span v-if="!r.metrics.reachable" class="st-name__err" role="alert">
              {{ r.metrics.error || t('opsServer.metrics.collectFailed') }}
            </span>
          </td>
          <td class="col-addr">
            <span class="st-addr" :title="r.addr">{{ r.addr }}</span>
          </td>
          <td class="col-status">
            <span
              class="reach-dot"
              :class="r.metrics.reachable ? 'reach-dot--ok' : 'reach-dot--down'"
              :title="r.metrics.reachable ? t('opsServer.metrics.reachable') : t('opsServer.metrics.unreachable')"
            >
              {{ r.metrics.reachable ? t('opsServer.metrics.reachable') : t('opsServer.metrics.unreachable') }}
            </span>
          </td>

          <td v-if="r.metrics.reachable">
            <template v-if="r.sysPrimary || r.sysParts.length">
              <span class="st-primary" :title="r.sysPrimary">{{ r.sysPrimary }}</span>
              <span v-if="r.sysParts.length" class="st-meta">{{ r.sysParts.join(' · ') }}</span>
            </template>
            <span v-else class="st-na">{{ na }}</span>
          </td>
          <td v-else class="col-num">
            <span class="st-na">{{ na }}</span>
          </td>

          <td v-if="r.metrics.reachable" class="col-num">
            <span
              class="st-num"
              :class="{
                'st-num--warn': r.loadVariant === 'warn',
                'st-num--error': r.loadVariant === 'error',
                'st-num--na': !r.loadAvailable,
              }"
              >{{ r.loadText }}</span
            >
          </td>
          <td v-else class="col-num">
            <span class="st-na">{{ na }}</span>
          </td>

          <td v-if="r.metrics.reachable" class="col-usage">
            <template v-if="r.memPercent !== null">
              <ProgressBar
                :value="r.memPercent"
                :variant="usageVariant(r.memPercent)"
                :label="t('opsServer.metrics.memUsageLabel', { n: r.memPercent.toFixed(0) })"
              />
              <span class="st-sub">
                {{ r.memUsedText }}
                <span class="st-pct">({{ r.memPercent.toFixed(0) }}%)</span>
              </span>
              <span v-if="r.memCacheLine" class="st-sub st-sub--cache" :title="t('opsServer.metrics.cacheTooltip')">
                {{ t('opsServer.metrics.withCache') }} {{ r.memCacheLine }} {{ r.memCachePct }}
                <span v-if="r.memPhysical" class="st-tag">{{ t('opsServer.metrics.physical') }}</span>
              </span>
              <span v-if="r.swapLine" class="st-sub">{{ r.swapLine }}</span>
            </template>
            <span v-else class="st-na">{{ na }}</span>
          </td>
          <td v-else class="col-usage">
            <span class="st-na">{{ na }}</span>
          </td>

          <td v-if="r.metrics.reachable" class="col-usage">
            <template v-if="r.diskPercent !== null">
              <ProgressBar
                :value="r.diskPercent"
                :variant="usageVariant(r.diskPercent)"
                :label="t('opsServer.metrics.diskUsageLabel', { n: r.diskPercent.toFixed(0) })"
              />
              <span class="st-sub" :title="r.diskPath">
                {{ r.diskLine }}
                <span class="st-pct">({{ r.diskPercent.toFixed(0) }}%)</span>
              </span>
            </template>
            <span v-else class="st-na">{{ na }}</span>
          </td>
          <td v-else class="col-usage">
            <span class="st-na">{{ na }}</span>
          </td>

          <td class="col-clock">
            <span v-if="r.metrics.reachable" class="st-clock">{{ r.clock }}</span>
          </td>
          <td class="col-ops">
            <!-- 只有可达的机器给「远程」:不可达点了只是再拿到一条连不上的错误。 -->
            <button
              v-if="r.metrics.reachable"
              class="st-remote"
              type="button"
              @click="emit('remote', r.metrics.serverId)"
            >
              {{ t('remoteWorkspace.button') }}
            </button>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
.metrics-table-wrap {
  overflow-x: auto;
  border: 1px solid var(--color-line);
  border-radius: var(--rounded-lg);
  background: var(--color-surface);
}

.metrics-table {
  width: 100%;
  border-collapse: collapse;
  font-size: var(--text-label);
}

.metrics-table th,
.metrics-table td {
  padding: 10px 12px;
  text-align: left;
  vertical-align: top;
  border-bottom: 1px solid var(--color-line);
}

.metrics-table thead th {
  position: sticky;
  top: 0;
  z-index: 1;
  background: var(--color-inset);
  color: var(--color-dim);
  font-weight: 600;
  white-space: nowrap;
}

.metrics-table tbody tr:last-child td {
  border-bottom: none;
}
.metrics-table tbody tr:hover td {
  background: var(--color-inset);
}
.metrics-table__row--down td {
  opacity: 0.72;
}

.col-server {
  min-width: 140px;
  max-width: 220px;
}
.col-addr {
  min-width: 130px;
}
.col-status {
  min-width: 66px;
}
.col-num {
  min-width: 92px;
}
.col-usage {
  min-width: 168px;
  width: 168px;
}
.col-clock,
.col-ops {
  white-space: nowrap;
}

/* ——— 名称 / 地址 ——— */
.st-name {
  display: block;
  font-weight: 650;
  color: var(--color-text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.st-name__err {
  display: block;
  margin-top: 2px;
  color: var(--color-red);
  line-height: 1.4;
}
.st-addr {
  color: var(--color-dim);
  font-variant-numeric: tabular-nums;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 220px;
  display: block;
}

/* ——— 可达性:列表里一行高度有限,徽标压成「点 + 两个字」 ——— */
.reach-dot {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-weight: 600;
  line-height: 1;
  padding: 3px 8px;
  border-radius: var(--rounded-md);
  white-space: nowrap;
}
.reach-dot::before {
  content: '';
  width: 6px;
  height: 6px;
  border-radius: var(--rounded-full);
  background: currentColor;
}
.reach-dot--ok {
  color: var(--color-green);
  background: var(--color-green-soft);
}
.reach-dot--down {
  color: var(--color-red);
  background: var(--color-red-soft);
}

/* ——— 系统 / 数值 / 用量 ——— */
.st-primary {
  display: block;
  font-weight: 600;
  color: var(--color-text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.st-meta {
  display: block;
  margin-top: 2px;
  color: var(--color-dim);
  line-height: 1.4;
}
.st-num {
  font-variant-numeric: tabular-nums;
  font-weight: 600;
  color: var(--color-text);
}
.st-num--warn {
  color: var(--color-amber);
}
.st-num--error {
  color: var(--color-red);
}
.st-num--na,
.st-na {
  color: var(--color-faint);
  font-style: italic;
  font-weight: 500;
}
.st-sub {
  display: block;
  margin-top: 3px;
  color: var(--color-dim);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
.st-sub--cache {
  color: var(--color-faint);
  cursor: help;
}
.st-pct {
  color: var(--color-faint);
}
.st-tag {
  margin-left: 4px;
  padding: 0 5px;
  border-radius: var(--rounded-sm, 4px);
  font-size: 0.68rem;
  font-weight: 600;
  color: var(--color-dim);
  background: var(--color-inset);
}
.st-clock {
  color: var(--color-faint);
  font-variant-numeric: tabular-nums;
}

.st-remote {
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
.st-remote:hover {
  color: var(--color-text);
  border-color: var(--color-line-strong, var(--color-line));
}
</style>
