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
} from '../../lib/serverMetrics'

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
// 算术(百分比、着色档、人读字节)全在 lib/serverMetrics:列表视图要给出与卡片
// 完全一致的数字,所以两边只能同一套判定,不能各写一个「≥75% 算黄」。

const system = computed(() => props.metrics.system)
const sysPrimary = computed(() => sysPrimaryOf(system.value))
const sysParts = computed(() => sysPartsOf(system.value))

const memView = computed(() => memoryView(props.metrics.memory))
const memPercent = computed(() => memView.value.percent)
const memCacheTotal = computed(() => memView.value.cacheTotal)
const memCachePercent = computed(() => memView.value.cachePercent)
const swapPercent = computed(() => memView.value.swapPercent)
const diskPercent = computed(() =>
  props.metrics.disk ? usagePercent(props.metrics.disk.usedBytes, props.metrics.disk.totalBytes) : null,
)

const loadVariant = computed(() => loadVariantOf(props.metrics.cpu))
const loadAvailable = computed(() => loadAvailableOf(props.metrics.cpu))
const loadText = computed(() => loadTextOf(props.metrics.cpu))
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
        {{ t('opsServer.metrics.collectedAt', { time: collectedClock(metrics.collectedAt) }) }}
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
