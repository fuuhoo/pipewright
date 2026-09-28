<!--
  ServerStatus.vue — Story 6-1: 多机状态总览(FR-15,服务器层资源指标)

  在一个面板看所有已登记服务器的 CPU 负载 / 内存 / 磁盘使用,免逐台 SSH。
    · 登记服务器列表来自 4-1;指标经 6-1 批量端点并行采集(每台独立)。
    · 某台不可达 → 该卡灰显 + 人读错误,排到可达的后面,不连累其它台。
    · 某指标缺失(跨平台 best-effort)→ 该行「不可用」。
    · 刷新/自动轮询都是**静默**的:不动按钮、不压暗卡片、不清屏,只有首屏才显示骨架。

  这是一个**新增**的总览入口,不动 4-1 SettingsServers CRUD、6-2 日志入口。
-->
<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { getAllServerMetrics, listServers, type ServerMetrics, type Server } from '../api/servers'
import { HttpError } from '../api/http'
import ServerMetricsCard from '../components/ops/ServerMetricsCard.vue'
import AppButton from '../components/ui/AppButton.vue'
import EmptyState from '../components/ui/EmptyState.vue'
import ErrorState from '../components/ui/ErrorState.vue'
import SkeletonBlock from '../components/ui/SkeletonBlock.vue'

const { t } = useI18n()

/** 首屏状态(骨架/整页错误)。后台刷新不改它 —— 那才叫静默。 */
const loadState = ref<'idle' | 'loading' | 'error'>('idle')
const loadError = ref('')
/** 后台刷新失败:旧数据继续留在屏上,只在标题下补一行说明。 */
const staleError = ref('')
/** 上一轮成功取数的本地时刻(「更新于 HH:MM:SS」)。 */
const updatedAt = ref('')
const metrics = ref<ServerMetrics[]>([])
/** serverId → 展示名(来自登记列表,用于卡片标题)。 */
const nameById = ref<Map<string, string>>(new Map())
/** 有一轮取数在飞:轮询与手动刷新都跳过,避免叠请求。 */
const inFlight = ref(false)

const reachableCount = computed(() => metrics.value.filter((m) => m.reachable).length)
const totalCount = computed(() => metrics.value.length)

// 可达的排前面,不可达的沉底(两组内部都保持接口原序 = 登记时间倒序)。
const sortedMetrics = computed(() => {
  const up: ServerMetrics[] = []
  const down: ServerMetrics[] = []
  for (const m of metrics.value) (m.reachable ? up : down).push(m)
  return up.length && down.length ? [...up, ...down] : metrics.value
})

function displayName(m: ServerMetrics): string {
  return nameById.value.get(m.serverId) ?? m.serverId
}

function humanizeLoadError(err: unknown): string {
  if (err instanceof HttpError) {
    return err.status === 0
      ? t('serverStatus.errConnect')
      : (err.apiError?.message ?? t('serverStatus.errLoadStatus', { status: err.status }))
  }
  return t('serverStatus.errLoadRetry')
}

async function load(): Promise<void> {
  if (inFlight.value) return
  inFlight.value = true
  // 首屏(屏上还没有任何卡片)才值得走骨架;之后一律静默替换。
  const firstLoad = metrics.value.length === 0
  if (firstLoad) {
    loadState.value = 'loading'
    loadError.value = ''
  }
  try {
    // 并行:登记列表(取展示名)+ 批量指标。互不阻塞。
    const [servers, items] = await Promise.all([loadServerNames(), getAllServerMetrics()])
    nameById.value = servers
    metrics.value = items
    staleError.value = ''
    updatedAt.value = new Date().toLocaleTimeString()
    loadState.value = 'idle'
  } catch (err) {
    const msg = humanizeLoadError(err)
    if (firstLoad) {
      loadError.value = msg
      loadState.value = 'error'
    } else {
      // 后台轮询失败:保留上一轮结果,不把整页换成错误态(那会「变白闪一下」)。
      staleError.value = msg
    }
  } finally {
    inFlight.value = false
  }
}

async function loadServerNames(): Promise<Map<string, string>> {
  const servers: Server[] = await listServers()
  const m = new Map<string, string>()
  for (const s of servers) m.set(s.id, s.name)
  return m
}

// ─── 自动刷新 ────────────────────────────────────────────────────────────────
// 指标实时、不落库,这里定时轮询:旧数据留屏、后台静默重取,成功就地替换,失败也不动画面。
// 标签页隐藏时暂停(省 SSH;终端常在新标签开,这页可能被晾在后台),回到前台立即补一次。
const REFRESH_INTERVAL_MS = 12_000
let pollTimer: ReturnType<typeof setInterval> | null = null

function startPolling(): void {
  if (pollTimer !== null) return
  pollTimer = setInterval(() => {
    if (!inFlight.value) void load()
  }, REFRESH_INTERVAL_MS)
}

function stopPolling(): void {
  if (pollTimer !== null) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

function onVisibilityChange(): void {
  if (document.hidden) {
    stopPolling()
  } else {
    void load() // 回前台立即补一次,再恢复轮询
    startPolling()
  }
}

onMounted(() => {
  void load()
  startPolling()
  document.addEventListener('visibilitychange', onVisibilityChange)
})

onUnmounted(() => {
  stopPolling()
  document.removeEventListener('visibilitychange', onVisibilityChange)
})
</script>

<template>
  <div class="server-status">
    <header class="view-header">
      <div class="view-header__text">
        <h1 class="view-title">{{ t('serverStatus.title') }}</h1>
        <p class="view-sub">
          {{ t('serverStatus.subtitle') }}
          <span v-if="totalCount > 0" class="view-sub__count">
            · {{ t('serverStatus.reachableSummary', { reachable: reachableCount, total: totalCount }) }}
          </span>
          <span class="view-sub__count">· {{ t('serverStatus.autoRefresh', { n: 12 }) }}</span>
          <span v-if="updatedAt" class="view-sub__count">· {{ t('serverStatus.updatedAt', { time: updatedAt }) }}</span>
        </p>
        <p v-if="staleError" class="view-sub__stale">{{ t('serverStatus.staleError', { msg: staleError }) }}</p>
      </div>
      <!-- 只有首屏才转圈;后台刷新保持静默(按钮不变、卡片不压暗)。 -->
      <AppButton variant="default" :loading="loadState === 'loading'" @click="load">
        {{ t('common.refresh') }}
      </AppButton>
    </header>

    <!-- Initial loading skeletons -->
    <div
      v-if="loadState === 'loading' && metrics.length === 0"
      class="metrics-grid"
      aria-busy="true"
      :aria-label="t('serverStatus.loadingAria')"
    >
      <div v-for="n in 3" :key="n" class="skeleton-card">
        <SkeletonBlock :height="20" width="50%" />
        <SkeletonBlock :height="14" width="80%" />
        <SkeletonBlock :height="14" width="80%" />
      </div>
    </div>

    <!-- Load error (whole-page; 仅在从未取到数据时出现;后台刷新失败走上面的 staleError) -->
    <ErrorState
      v-else-if="loadState === 'error'"
      :title="t('serverStatus.errTitle')"
      :description="loadError"
      @retry="load"
    />

    <!-- Empty: no registered servers -->
    <EmptyState
      v-else-if="metrics.length === 0"
      :title="t('serverStatus.emptyTitle')"
      :description="t('serverStatus.emptyDesc')"
    />

    <!-- Metrics grid (per-host cards;可达在前、不可达沉底) -->
    <div v-else class="metrics-grid">
      <ServerMetricsCard
        v-for="m in sortedMetrics"
        :key="m.serverId"
        :name="displayName(m)"
        :metrics="m"
      />
    </div>
  </div>
</template>

<style scoped>
.server-status {
  display: flex;
  flex-direction: column;
  gap: 24px;
}

.view-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
}
.view-header__text {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.view-title {
  margin: 0;
  font-size: var(--text-h1, 1.5rem);
  font-weight: 700;
  color: var(--color-text);
}
.view-sub {
  margin: 0;
  font-size: var(--text-label);
  color: var(--color-dim);
}
.view-sub__count {
  color: var(--color-faint);
  font-variant-numeric: tabular-nums;
}
.view-sub__stale {
  margin: 0;
  font-size: var(--text-label);
  color: var(--color-warn, var(--color-dim));
}

/* 刷新时不压暗、不做过渡:静默替换内容即可。 */
.metrics-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 16px;
}

.skeleton-card {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 18px;
  border: 1px solid var(--color-line);
  border-radius: var(--rounded-lg);
  background: var(--color-surface);
}
</style>
