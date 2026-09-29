<script setup lang="ts">
/*
  Containers.vue — 容器/镜像管理总览。顶部聚合 KPI + 服务器切换 + 状态筛选,下面每台
  可见服务器一张 ServerCard(卡片内部按 容器/镜像 分 tab,只针对这一台)。日志走抽屉、
  终端跳全屏页、新增容器走弹窗。每 12s 自动刷新聚合(stale-while-revalidate)。
*/
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { streamAllContainers, type ServerContainers, type ContainerInfo } from '../api/containers'
import { listServers, serviceAction, type Server, type ServiceAction } from '../api/servers'
import { HttpError } from '../api/http'
import { stateBucket, type StateBucket } from '../lib/containerState'
import {
  SERVER_ALL,
  SERVER_USABLE,
  resolveServerScope,
  scopedGroups,
  reachableFirst,
  scopeTone,
  preferredFirst,
  type ServerScope,
} from '../lib/containerScope'
import { useToast } from '../composables/useToast'
import { useConfirm } from '../composables/useConfirm'
import AppButton from '../components/ui/AppButton.vue'
import EmptyState from '../components/ui/EmptyState.vue'
import ErrorState from '../components/ui/ErrorState.vue'
import SkeletonBlock from '../components/ui/SkeletonBlock.vue'
import ServerCard from '../components/ops/ServerCard.vue'
import ContainerLogsDrawer from '../components/ops/ContainerLogsDrawer.vue'
import CreateContainerModal from '../components/ops/CreateContainerModal.vue'
import AiDiagnosisModal from '../components/ops/AiDiagnosisModal.vue'
import ContainerAiPanel from '../components/ops/ContainerAiPanel.vue'
import ContainerInspectModal from '../components/ops/ContainerInspectModal.vue'
import SystemPruneModal from '../components/ops/SystemPruneModal.vue'

type LoadState = 'idle' | 'loading' | 'error'
type StateFilter = 'all' | StateBucket

const { t } = useI18n()
const router = useRouter()
const toast = useToast()
const confirm = useConfirm()

const loadState = ref<LoadState>('idle')
const loadError = ref('')
/** 后台刷新失败:旧数据留在屏上,只在标题下补一行说明。 */
const staleError = ref('')
/** 上一轮**全部**采完的本地时刻(「更新于 HH:MM:SS」)。 */
const updatedAt = ref('')
const groups = ref<ServerContainers[]>([])
/** serverId → groups 下标(逐台回调要 O(1) 找到那张卡)。 */
const indexById = new Map<string, number>()
/** 有一轮取数在飞:轮询与手动刷新都跳过,免得叠请求。 */
const inFlight = ref(false)
const serverById = ref<Map<string, Server>>(new Map())

const POLL_MS = 12_000
let timer: ReturnType<typeof setInterval> | null = null

// ─── 服务器切换 ───────────────────────────────────────────────────────────────
// 聚合是一屏摊开所有机器,机器一多就要滚动找,所以给一个「全部 / 只看这台 / 仅看可用的」范围。
// 范围只切展示与统计口径,不改权限:候选永远来自后端已收敛过的聚合结果。
const serverScope = ref<ServerScope>(SERVER_ALL)

/** 在线的在前、离线的沉底(与服务器状态页同一口径);切换范围不改变这个顺序。 */
const orderedGroups = computed(() => reachableFirst(groups.value))
/** 每轮聚合都换一批新对象;选中的机器掉了就自动回落「全部」(按钮高亮与内容始终一致)。 */
const scope = computed(() => resolveServerScope(serverScope.value, orderedGroups.value))
const scopeGroups = computed(() => scopedGroups(orderedGroups.value, scope.value))
/** 一台机时没什么可切的,这一行就不占地方。 */
const showScopeBar = computed(() => orderedGroups.value.length > 1)
/** 有离线机器才值得给「仅看可用的」这一档(否则它跟「全部」是同一屏,纯噪音)。 */
const hasOffline = computed(() => orderedGroups.value.some((g) => !g.reachable))
const reachableCount = computed(() => orderedGroups.value.filter((g) => g.reachable).length)
/** 分母 = 已登记的台数:逐台上屏的中间态不该让「共几台」忽大忽小。 */
const registeredCount = computed(() => Math.max(serverById.value.size, orderedGroups.value.length))

// ─── 聚合统计(口径 = 当前切换范围)────────────────────────────────────────────
const totalContainers = computed(() => scopeGroups.value.reduce((n, g) => n + g.total, 0))
const runningContainers = computed(() => scopeGroups.value.reduce((n, g) => n + g.running, 0))
const stoppedContainers = computed(() => totalContainers.value - runningContainers.value)
const coveredServers = computed(() => scopeGroups.value.filter((g) => g.reachable && g.runtime).length)
/** 范围覆盖的台数:整体范围用登记数当分母(逐台上屏时才有稳定基准)。 */
const scopeTotal = computed(() =>
  scope.value === SERVER_ALL || scope.value === SERVER_USABLE ? registeredCount.value : scopeGroups.value.length,
)

function serverName(id: string): string {
  return serverById.value.get(id)?.name ?? id
}
function serverHost(id: string): string {
  const s = serverById.value.get(id)
  return s ? `${s.user}@${s.host}:${s.port}` : ''
}

// ─── 状态筛选 ─────────────────────────────────────────────────────────────────
const stateFilter = ref<StateFilter>('all')

const bucketCounts = computed(() => {
  let running = 0
  let paused = 0
  let stopped = 0
  for (const g of scopeGroups.value) {
    for (const c of g.containers) {
      const b = stateBucket(c.state)
      if (b === 'running') running++
      else if (b === 'paused') paused++
      else stopped++
    }
  }
  return { all: running + paused + stopped, running, paused, stopped }
})

interface FilterOption {
  key: StateFilter
  labelKey: string
}
const FILTER_OPTIONS: FilterOption[] = [
  { key: 'all', labelKey: 'containers.filterAll' },
  { key: 'running', labelKey: 'containers.filterRunning' },
  { key: 'stopped', labelKey: 'containers.filterStopped' },
  { key: 'paused', labelKey: 'containers.filterPaused' },
]
function filterCount(key: StateFilter): number {
  return bucketCounts.value[key]
}

// ─── 文本搜索 ─────────────────────────────────────────────────────────────────
// 透传给各 ServerCard,与状态筛选叠加(按名字 / 镜像,忽略大小写)。
const searchText = ref('')

// ─── 卡片折叠 ─────────────────────────────────────────────────────────────────
// 折叠集由页面持有,卡片只读自己那一项 —— 这样页头才能一键折叠/展开当前范围的全部卡片。
// 范围外的 id 保留:切回那台时还是折叠着,这正是逐台看时的连续性。
const folded = ref<Set<string>>(new Set())
// 只有连得上且有运行时的卡片有正文可折(卡片内那个按钮也是这个条件)。
const foldableIds = computed(() =>
  scopeGroups.value.filter((g) => g.reachable && g.runtime).map((g) => g.serverId),
)
const allFolded = computed(
  () => foldableIds.value.length > 0 && foldableIds.value.every((id) => folded.value.has(id)),
)
function toggleFold(serverId: string): void {
  const next = new Set(folded.value)
  if (next.has(serverId)) next.delete(serverId)
  else next.add(serverId)
  folded.value = next
}
function foldAll(): void {
  folded.value = allFolded.value ? new Set() : new Set(foldableIds.value)
}

// ─── 批量操作 ─────────────────────────────────────────────────────────────────
// 父统一持有选择集;key = `${serverId}::${containerName}`。ServerCard 上报勾选,父加 serverId。
const bulkMode = ref(false)
const selected = ref<Set<string>>(new Set())
const bulkBusy = ref(false)
const selectedCount = computed(() => selected.value.size)

function selectKey(serverId: string, name: string): string {
  return `${serverId}::${name}`
}
function toggleSelect(serverId: string, name: string): void {
  const key = selectKey(serverId, name)
  const next = new Set(selected.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  selected.value = next
}
function clearSelection(): void {
  selected.value = new Set()
}
function toggleBulkMode(): void {
  bulkMode.value = !bulkMode.value
  if (!bulkMode.value) clearSelection()
}

// 切换机器后,不在范围内的勾选既看不见又仍会被批量执行 —— 那是最坏的一种「偷偷操作」,直接清掉。
watch(scope, () => clearSelection())

interface BulkAction {
  action: ServiceAction
  labelKey: string
  danger: boolean
}
const BULK_ACTIONS: BulkAction[] = [
  { action: 'start', labelKey: 'containers.actionStart', danger: false },
  { action: 'stop', labelKey: 'containers.actionStop', danger: true },
  { action: 'restart', labelKey: 'containers.actionRestart', danger: true },
  { action: 'rm', labelKey: 'containers.actionDelete', danger: true },
]

async function runBulk({ action, labelKey, danger }: BulkAction): Promise<void> {
  if (selected.value.size === 0 || bulkBusy.value) return
  const label = t(labelKey)
  const targets = [...selected.value].map((key) => {
    const i = key.indexOf('::')
    return { serverId: key.slice(0, i), name: key.slice(i + 2) }
  })
  const n = targets.length
  if (danger) {
    const ok = await confirm.open({
      title: t('containers.confirmTitle', { label, n }),
      body:
        action === 'rm'
          ? t('containers.confirmBodyRm')
          : t('containers.confirmBodyAction', { label, n }),
      confirmLabel: t('containers.confirmLabel', { label, n }),
      variant: 'danger',
    })
    if (!ok) return
  }
  bulkBusy.value = true
  try {
    const results = await Promise.allSettled(
      targets.map((tg) => serviceAction(tg.serverId, { type: 'docker', target: tg.name, action })),
    )
    let okCount = 0
    let failCount = 0
    for (const r of results) {
      if (r.status === 'fulfilled' && r.value.ok) okCount++
      else failCount++
    }
    if (failCount === 0)
      toast.success(t('containers.toastDone', { label }), {
        detail: t('containers.toastDoneDetail', { n: okCount }),
      })
    else if (okCount === 0)
      toast.error(t('containers.toastFail', { label }), {
        detail: t('containers.toastFailDetail', { n: failCount }),
      })
    else
      toast.warn(t('containers.toastPartial', { label }), {
        detail: t('containers.toastPartialDetail', { ok: okCount, fail: failCount }),
      })
    clearSelection()
    await load()
  } finally {
    bulkBusy.value = false
  }
}

// ─── 新增容器 ─────────────────────────────────────────────────────────────────
const showCreate = ref(false)
// 候选永远是全部可用机器(切换只切这一屏的展示),但把聚焦那台排到最前 —— 弹窗默认取第一台。
const creatableServers = computed(() =>
  preferredFirst(
    groups.value
      .filter((g) => g.reachable && g.runtime)
      .map((g) => ({ id: g.serverId, name: serverName(g.serverId) })),
    scope.value,
  ),
)
function onContainerCreated(): void {
  void load()
}

// ─── 日志抽屉 / 终端 ──────────────────────────────────────────────────────────
const logsTarget = ref<{ serverId: string; name: string; id: string } | null>(null)
function openLogs(serverId: string, c: ContainerInfo): void {
  logsTarget.value = { serverId, name: c.names, id: c.id.length > 12 ? c.id.slice(0, 12) : c.id }
}
function openTerminal(serverId: string, c: ContainerInfo): void {
  const url = router.resolve({
    name: 'server-terminal',
    params: { id: serverId },
    query: { container: c.names, cid: c.id.length > 12 ? c.id.slice(0, 12) : c.id },
  }).href
  window.open(url, '_blank', 'noopener')
}

// AI 助手抽屉。
const showAi = ref(false)
const aiContext = { os: 'linux', shell: '/bin/sh', container: t('containers.aiContextContainer') }

// AI 诊断弹窗。
const diagnoseTarget = ref<{ serverId: string; name: string } | null>(null)
function openDiagnose(serverId: string, c: ContainerInfo): void {
  diagnoseTarget.value = { serverId, name: c.names }
}

// 容器详情 inspect 弹窗。
const inspectTarget = ref<{ serverId: string; name: string } | null>(null)
function openInspect(serverId: string, c: ContainerInfo): void {
  inspectTarget.value = { serverId, name: c.names }
}

// 一键清理弹窗(作用于有 docker 的服务器,复用 creatableServers)。
const showPrune = ref(false)

// ─── 加载 ─────────────────────────────────────────────────────────────────────
/** 逐台上屏:一台一张卡,按 serverId 就地替换(所以刷新既不增卡也不闪)。 */
function upsertGroup(item: ServerContainers): void {
  const at = indexById.get(item.serverId)
  if (at === undefined) {
    indexById.set(item.serverId, groups.value.length)
    groups.value.push(item)
    return
  }
  groups.value[at] = item
}

/** 本轮之后已不存在的服务器(被删 / 收回可见性)从屏上撤掉。 */
function retainOnly(keep: Set<string>): void {
  const next = groups.value.filter((g) => keep.has(g.serverId))
  indexById.clear()
  next.forEach((g, i) => indexById.set(g.serverId, i))
  groups.value = next
}

function humanizeLoadError(err: unknown): string {
  if (err instanceof HttpError) {
    return err.status === 0
      ? t('containers.errConnect')
      : (err.apiError?.message ?? t('containers.errLoadStatus', { status: err.status }))
  }
  return t('containers.errLoadRetry')
}

/**
 * 拉一轮聚合:容器走逐台流式(采完一台就上一张卡),名单并行取。
 * 刷新是**静默**的 —— 不压暗、不清屏,只有首屏(屏上还没有任何卡)才显示骨架。
 */
async function load(): Promise<void> {
  if (inFlight.value) return
  const firstLoad = groups.value.length === 0
  if (firstLoad) {
    loadState.value = 'loading'
    loadError.value = ''
  }
  inFlight.value = true
  const namesPromise = listServers()
    .then((servers) => {
      const m = new Map<string, Server>()
      for (const s of servers) m.set(s.id, s)
      serverById.value = m
      return m
    })
    .catch(() => null)
  try {
    await streamAllContainers(upsertGroup)
    const names = await namesPromise
    if (names) retainOnly(new Set(names.keys()))
    staleError.value = ''
    updatedAt.value = new Date().toLocaleTimeString()
    loadState.value = 'idle'
  } catch (err) {
    const msg = humanizeLoadError(err)
    const names = await namesPromise
    if (groups.value.length === 0) {
      // 屏上什么都没有(含首屏就失败)才走整页错误态。
      loadError.value = msg
      loadState.value = 'error'
    } else {
      // 已上屏的卡留着(这一轮可能已换过几张),只补一行「本轮刷新失败」。
      if (names) retainOnly(new Set(names.keys()))
      staleError.value = msg
      loadState.value = 'idle'
    }
  } finally {
    inFlight.value = false
  }
}

onMounted(() => {
  void load()
  timer = setInterval(() => void load(), POLL_MS)
})
onUnmounted(() => {
  if (timer) clearInterval(timer)
})
</script>

<template>
  <div class="containers-view">
    <header class="view-header">
      <div class="view-header__text">
        <h1 class="view-title">{{ t('containers.title') }}</h1>
        <p class="view-sub">
          {{ t('containers.subtitle') }}
          <span v-if="totalContainers > 0" class="view-sub__count">
            {{ t('containers.countSummary', { total: totalContainers, running: runningContainers }) }}
          </span>
          <span class="view-sub__count">{{ t('containers.autoRefresh', { n: POLL_MS / 1000 }) }}</span>
          <span v-if="updatedAt" class="view-sub__count">{{ t('containers.updatedAt', { time: updatedAt }) }}</span>
        </p>
        <p v-if="staleError" class="view-sub__stale">{{ t('containers.staleError', { msg: staleError }) }}</p>
      </div>
      <div class="header-actions">
        <AppButton variant="ai" @click="showAi = true">{{ t('containers.aiAssistant') }}</AppButton>
        <AppButton variant="default" :disabled="creatableServers.length === 0" @click="showPrune = true">
          {{ t('containers.prune') }}
        </AppButton>
        <AppButton :variant="bulkMode ? 'primary' : 'default'" @click="toggleBulkMode">
          {{ bulkMode ? t('containers.bulkExit') : t('containers.bulkEnter') }}
        </AppButton>
        <AppButton variant="primary" :disabled="creatableServers.length === 0" @click="showCreate = true">
          {{ t('containers.create') }}
        </AppButton>
        <AppButton variant="default" :loading="loadState === 'loading' && groups.length === 0" @click="load">
          {{ t('common.refresh') }}
        </AppButton>
      </div>
    </header>

    <!-- 首屏骨架 -->
    <div v-if="loadState === 'loading' && groups.length === 0" class="skeletons" aria-busy="true" :aria-label="t('containers.loadingAria')">
      <div v-for="n in 2" :key="n" class="skeleton-panel">
        <SkeletonBlock :height="20" width="40%" />
        <SkeletonBlock :height="48" width="100%" />
        <SkeletonBlock :height="48" width="100%" />
      </div>
    </div>

    <ErrorState
      v-else-if="loadState === 'error' && groups.length === 0"
      :title="t('containers.errTitle')"
      :description="loadError"
      @retry="load"
    />

    <EmptyState
      v-else-if="groups.length === 0"
      :title="t('containers.emptyTitle')"
      :description="t('containers.emptyDesc')"
    />

    <template v-else>
      <!-- 服务器切换:一台一张卡,机器多了不用滚动找;在线的排前面、离线的沉底 -->
      <div v-if="showScopeBar" class="scope-bar" role="group" :aria-label="t('containers.serverFilterAria')">
        <span class="scope-bar__label">{{ t('containers.serverFilterLabel') }}</span>
        <button
          class="scope-chip"
          :class="{ 'scope-chip--active': scope === SERVER_ALL }"
          :aria-pressed="scope === SERVER_ALL"
          @click="serverScope = SERVER_ALL"
        >
          {{ t('containers.serverAll') }}
          <span class="scope-chip__count">{{ registeredCount }}</span>
        </button>
        <!-- 死机摊在总览里除了占位没有别的作用:给一档只看连得上的 -->
        <button
          v-if="hasOffline"
          class="scope-chip"
          :class="{ 'scope-chip--active': scope === SERVER_USABLE }"
          :aria-pressed="scope === SERVER_USABLE"
          @click="serverScope = SERVER_USABLE"
        >
          {{ t('containers.serverUsable') }}
          <span class="scope-chip__count">{{ reachableCount }}</span>
        </button>
        <button
          v-for="g in orderedGroups"
          :key="g.serverId"
          class="scope-chip"
          :class="{ 'scope-chip--active': scope === g.serverId }"
          :aria-pressed="scope === g.serverId"
          :title="g.error || `${serverName(g.serverId)} · ${serverHost(g.serverId)}`"
          @click="serverScope = g.serverId"
        >
          <span class="scope-dot" :class="'scope-dot--' + scopeTone(g)" />
          <span class="scope-chip__name">{{ serverName(g.serverId) }}</span>
          <span class="scope-chip__count">{{ g.running }}/{{ g.total }}</span>
        </button>
      </div>

      <!-- 聚合 KPI 条(统计口径 = 上面选中的范围) -->
      <section class="kpi-strip" :aria-label="t('containers.kpiStripAria')">
        <div class="kpi kpi--total">
          <span class="kpi__num">{{ totalContainers }}</span>
          <span class="kpi__label">{{ t('containers.kpiTotal') }}</span>
        </div>
        <div class="kpi kpi--running">
          <span class="kpi__num">{{ runningContainers }}</span>
          <span class="kpi__label">{{ t('containers.kpiRunning') }}</span>
        </div>
        <div class="kpi kpi--stopped">
          <span class="kpi__num">{{ stoppedContainers }}</span>
          <span class="kpi__label">{{ t('containers.kpiStopped') }}</span>
        </div>
        <div class="kpi kpi--hosts">
          <span class="kpi__num">{{ coveredServers }}<span class="kpi__den">/{{ scopeTotal }}</span></span>
          <span class="kpi__label">{{ t('containers.kpiHosts') }}</span>
        </div>
      </section>

      <!-- 状态筛选段 + 文本搜索(均作用于各卡片的容器 tab) -->
      <div class="controls-row">
        <div class="filter-bar" role="group" :aria-label="t('containers.filterAria')">
          <button
            v-for="f in FILTER_OPTIONS"
            :key="f.key"
            class="filter-chip"
            :class="{ 'filter-chip--active': stateFilter === f.key }"
            :aria-pressed="stateFilter === f.key"
            @click="stateFilter = f.key"
          >
            {{ t(f.labelKey) }}
            <span class="filter-chip__count">{{ filterCount(f.key) }}</span>
          </button>
        </div>
        <div class="controls-right">
          <div class="search-box">
            <input
              v-model="searchText"
              type="search"
              class="search-box__input"
              :placeholder="t('containers.searchPlaceholder')"
              :aria-label="t('containers.searchAria')"
            />
            <button v-if="searchText" class="search-box__clear" :title="t('containers.searchClear')" @click="searchText = ''">✕</button>
          </div>
          <!-- 机器一多,逐台点折叠太累:一档开关按当前范围整体收起/放开 -->
          <button
            v-if="foldableIds.length > 1"
            class="fold-all"
            type="button"
            :aria-pressed="allFolded"
            :title="t(allFolded ? 'containers.unfoldAll' : 'containers.foldAll')"
            @click="foldAll"
          >
            {{ t(allFolded ? 'containers.unfoldAll' : 'containers.foldAll') }}
          </button>
        </div>
      </div>

      <!-- 批量操作条 -->
      <div v-if="bulkMode" class="bulk-bar" role="group" :aria-label="t('containers.bulkAria')">
        <span class="bulk-bar__count">{{ t('containers.bulkSelected') }} <strong>{{ selectedCount }}</strong> {{ t('containers.bulkSelectedUnit') }}</span>
        <div class="bulk-bar__actions">
          <button
            v-for="a in BULK_ACTIONS"
            :key="a.action"
            class="bulk-btn"
            :class="{ 'bulk-btn--danger': a.danger }"
            :disabled="selectedCount === 0 || bulkBusy"
            @click="runBulk(a)"
          >
            {{ t(a.labelKey) }}
          </button>
          <button class="bulk-btn bulk-btn--ghost" :disabled="selectedCount === 0 || bulkBusy" @click="clearSelection">
            {{ t('containers.bulkClear') }}
          </button>
        </div>
      </div>

      <!-- 逐台卡片(卡片内自带 容器/镜像 tab;只渲染切换范围内的机器)。后台刷新就地换卡,不压暗。 -->
      <section class="cards" :aria-label="t('containers.cardsAria')">
        <ServerCard
          v-for="g in scopeGroups"
          :key="g.serverId"
          :group="g"
          :name="serverName(g.serverId)"
          :host="serverHost(g.serverId)"
          :state-filter="stateFilter"
          :search="searchText"
          :bulk-mode="bulkMode"
          :selected-set="selected"
          :folded-set="folded"
          @toggle-fold="toggleFold(g.serverId)"
          @toggle-select="(name) => toggleSelect(g.serverId, name)"
          @changed="load"
          @logs="(c) => openLogs(g.serverId, c)"
          @terminal="(c) => openTerminal(g.serverId, c)"
          @diagnose="(c) => openDiagnose(g.serverId, c)"
          @inspect="(c) => openInspect(g.serverId, c)"
        />
      </section>
    </template>

    <!-- 新增容器弹窗 -->
    <CreateContainerModal
      v-if="showCreate"
      :servers="creatableServers"
      @close="showCreate = false"
      @created="onContainerCreated"
    />

    <!-- 容器日志抽屉 -->
    <ContainerLogsDrawer
      v-if="logsTarget"
      :server-id="logsTarget.serverId"
      :container-name="logsTarget.name"
      :container-id="logsTarget.id"
      @close="logsTarget = null"
    />

    <!-- AI 诊断弹窗 -->
    <AiDiagnosisModal
      v-if="diagnoseTarget"
      :server-id="diagnoseTarget.serverId"
      :container-name="diagnoseTarget.name"
      @close="diagnoseTarget = null"
    />

    <!-- AI 助手抽屉 -->
    <ContainerAiPanel v-if="showAi" :context="aiContext" @close="showAi = false" />

    <!-- 容器详情 inspect 弹窗 -->
    <ContainerInspectModal
      v-if="inspectTarget"
      :server-id="inspectTarget.serverId"
      :container-name="inspectTarget.name"
      @close="inspectTarget = null"
    />

    <!-- 一键清理弹窗 -->
    <SystemPruneModal v-if="showPrune" :servers="creatableServers" @close="showPrune = false" />
  </div>
</template>

<style scoped>
.containers-view {
  display: flex;
  flex-direction: column;
  gap: 22px;
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
.header-actions {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-shrink: 0;
}
.view-title {
  margin: 0;
  font-size: var(--text-h1);
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
/* 后台刷新失败:旧数据还在屏上,这里只补一行警示,不清屏、不压暗。 */
.view-sub__stale {
  margin: 4px 0 0;
  font-size: var(--text-label);
  color: var(--color-warn);
}

/* 筛选段 + 搜索同一行 */
.controls-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 12px;
}

/* 状态筛选段 */
.filter-bar {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

/* 右侧一组:文本搜索 + 一键折叠 */
.controls-right {
  display: flex;
  align-items: center;
  gap: 8px;
}
.fold-all {
  flex-shrink: 0;
  font-size: var(--text-label);
  font-weight: 600;
  padding: 7px 13px;
  border-radius: 999px;
  border: 1px solid var(--color-border-strong);
  background: transparent;
  color: var(--color-dim);
  cursor: pointer;
  white-space: nowrap;
  transition: all var(--duration-fast) var(--ease-out-expo);
}
.fold-all:hover {
  color: var(--color-text);
  border-color: var(--color-text);
}

/* 文本搜索 */
.search-box {
  position: relative;
  display: inline-flex;
  align-items: center;
  flex: 0 1 320px;
  min-width: 200px;
}
.search-box__input {
  width: 100%;
  padding: 7px 30px 7px 13px;
  font-size: var(--text-label);
  color: var(--color-text);
  background: var(--color-card);
  border: 1px solid var(--color-border);
  border-radius: 999px;
  transition: border-color var(--duration-fast) var(--ease-out-expo);
}
.search-box__input::placeholder {
  color: var(--color-faint);
}
.search-box__input:focus {
  outline: none;
  border-color: var(--color-primary);
}
.search-box__clear {
  position: absolute;
  right: 10px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  font-size: 11px;
  line-height: 1;
  color: var(--color-faint);
  background: transparent;
  border: none;
  border-radius: 50%;
  cursor: pointer;
}
.search-box__clear:hover {
  color: var(--color-text);
}

/* 批量操作条 */
.bulk-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 12px;
  padding: 10px 16px;
  background: var(--color-card-2);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--rounded);
}
.bulk-bar__count {
  font-size: var(--text-label);
  color: var(--color-dim);
}
.bulk-bar__count strong {
  color: var(--color-text);
  font-variant-numeric: tabular-nums;
}
.bulk-bar__actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.bulk-btn {
  font-size: var(--text-label);
  font-weight: 600;
  padding: 6px 14px;
  border-radius: var(--rounded-sm);
  border: 1px solid var(--color-border-strong);
  background: var(--color-card);
  color: var(--color-text);
  cursor: pointer;
  transition: all var(--duration-fast) var(--ease-out-expo);
}
.bulk-btn:hover:not(:disabled) {
  border-color: var(--color-primary);
  color: var(--color-primary);
}
.bulk-btn--danger:hover:not(:disabled) {
  border-color: var(--color-red);
  color: var(--color-red);
}
.bulk-btn--ghost {
  color: var(--color-dim);
}
.bulk-btn--ghost:hover:not(:disabled) {
  color: var(--color-text);
  border-color: var(--color-text);
}
.bulk-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.filter-chip {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  padding: 6px 13px;
  font-size: var(--text-label);
  font-weight: 600;
  color: var(--color-dim);
  background: var(--color-card);
  border: 1px solid var(--color-border);
  border-radius: 999px;
  cursor: pointer;
  transition: color var(--duration-fast) var(--ease-out-expo), border-color var(--duration-fast) var(--ease-out-expo), background var(--duration-fast) var(--ease-out-expo);
}
.filter-chip:hover {
  color: var(--color-text);
  border-color: var(--color-border-strong);
}
.filter-chip--active {
  color: #fff;
  background: var(--color-primary);
  border-color: var(--color-primary);
}
.filter-chip__count {
  font-size: var(--text-micro);
  font-variant-numeric: tabular-nums;
  padding: 1px 7px;
  border-radius: 999px;
  background: var(--color-inset);
  color: var(--color-faint);
}
.filter-chip--active .filter-chip__count {
  background: oklch(100% 0 0 / 0.22);
  color: #fff;
}

/* 服务器切换条 */
.scope-bar {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}
.scope-bar__label {
  font-size: var(--text-caps);
  letter-spacing: 0.04em;
  text-transform: uppercase;
  color: var(--color-faint);
  margin-right: 2px;
}
.scope-chip {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  padding: 6px 13px;
  font-size: var(--text-label);
  font-weight: 600;
  white-space: nowrap;
  color: var(--color-dim);
  background: var(--color-card);
  border: 1px solid var(--color-border);
  border-radius: 999px;
  cursor: pointer;
  transition: color var(--duration-fast) var(--ease-out-expo), border-color var(--duration-fast) var(--ease-out-expo), background var(--duration-fast) var(--ease-out-expo);
}
/* 机器名可能很长(自动登记的叫 p3-in-group180594 这种):截断而不是把 chip 撑成两行,
   整名 + 地址留在 title 里。窄屏时优先牺牲名字,状态点和计数始终要看得见。 */
.scope-chip__name {
  max-width: 22ch;
  overflow: hidden;
  text-overflow: ellipsis;
}
.scope-chip:hover {
  color: var(--color-text);
  border-color: var(--color-border-strong);
}
.scope-chip--active {
  color: #fff;
  background: var(--color-primary);
  border-color: var(--color-primary);
}
.scope-chip__count {
  font-size: var(--text-micro);
  font-variant-numeric: tabular-nums;
  padding: 1px 7px;
  border-radius: 999px;
  background: var(--color-inset);
  color: var(--color-faint);
}
.scope-chip--active .scope-chip__count {
  background: oklch(100% 0 0 / 0.22);
  color: #fff;
}
/* 状态点:连不上 / 没装 docker / 可用。给切换前先看哪台能用的时候省一次点击。 */
.scope-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  flex-shrink: 0;
  background: var(--color-faint);
}
.scope-dot--ok { background: var(--color-green); }
.scope-dot--no-runtime { background: var(--color-amber); }
.scope-dot--unreachable { background: var(--color-red); }

/* KPI 条 */
.kpi-strip {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
  gap: 12px;
}
.kpi {
  position: relative;
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 16px 18px;
  background: var(--color-card);
  border: 1px solid var(--color-border);
  border-radius: var(--rounded);
  overflow: hidden;
}
.kpi::before {
  content: '';
  position: absolute;
  inset: 0 auto 0 0;
  width: 3px;
  background: var(--color-faint);
}
.kpi--running::before { background: var(--color-green); }
.kpi--stopped::before { background: var(--color-faint); }
.kpi--total::before { background: var(--color-primary); }
.kpi--hosts::before { background: var(--color-cyan); }
.kpi__num {
  font-size: var(--text-kpi);
  font-weight: 700;
  line-height: 1;
  color: var(--color-text);
  font-variant-numeric: tabular-nums;
}
.kpi__den {
  font-size: var(--text-body);
  font-weight: 600;
  color: var(--color-faint);
}
.kpi__label {
  font-size: var(--text-caps);
  letter-spacing: 0.04em;
  text-transform: uppercase;
  color: var(--color-faint);
}

/* 卡片列表(刷新不压暗:静默就地替换) */
.cards {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.skeletons {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.skeleton-panel {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 18px;
  border: 1px solid var(--color-border);
  border-radius: var(--rounded-lg, 12px);
  background: var(--color-card);
}
</style>
