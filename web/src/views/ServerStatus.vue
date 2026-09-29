<!--
  ServerStatus.vue — Story 6-1: 多机状态总览(FR-15,服务器层资源指标)

  在一个面板看所有已登记服务器的 CPU 负载 / 内存 / 磁盘使用,免逐台 SSH。
    · 登记服务器列表来自 4-1;指标经 6-1 批量端点**逐台流式**采集(每台独立)。
    · 一张卡采完就上一张,不等最慢那台(死机要拖满探针超时,不该让健康机陪着空白)。
    · 某台不可达 → 该卡灰显 + 人读错误,排到可达的后面,不连累其它台。
    · 某指标缺失(跨平台 best-effort)→ 该行「不可用」。
    · 刷新/自动轮询的数据区都是**静默**的:就地替换已有卡片,不压暗、不清屏,只有首屏才显示骨架。
      手点「刷新」时按钮自己转圈并挡重复点(反馈给点击,不打扰屏上的卡)。
    · 两种视图:卡片(默认)/ 一行一台的列表。只有排布不同,取数与展示口径完全共用。

  这是一个**新增**的总览入口,不动 4-1 SettingsServers CRUD、6-2 日志入口。
-->
<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { streamAllServerMetrics, listServers, type ServerMetrics, type Server } from '../api/servers'
import { listGroups, type Group } from '../api/groups'
import { GROUP_ALL, groupLabel, groupOptions, matchesGroup } from '../lib/groupFilter'
import { HttpError } from '../api/http'
import ServerMetricsCard from '../components/ops/ServerMetricsCard.vue'
import ServerMetricsTable from '../components/ops/ServerMetricsTable.vue'
import RemoteWorkspaceModal from '../components/ops/RemoteWorkspaceModal.vue'
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
/** 上一轮**全部**采完的本地时刻(「更新于 HH:MM:SS」)。 */
const updatedAt = ref('')
/** 逐台上屏:一台一张卡,按 serverId 就地替换(所以刷新既不增卡也不闪)。 */
const metrics = ref<ServerMetrics[]>([])
/** serverId → metrics 下标(逐台回调要 O(1) 找到那张卡)。 */
const indexById = new Map<string, number>()
/** serverId → 登记信息(标题用 name,「远程」弹窗还要 host:port 与 user)。 */
const serverById = ref<Map<string, Server>>(new Map())
/** 有一轮取数在飞:轮询与手动刷新都跳过,避免叠请求。 */
const inFlight = ref(false)
/** 在飞这一轮是不是手点的:只有手点才让按钮转圈,自动轮询照旧静默。 */
const manualRound = ref(false)
/** 刷新按钮的转圈态:首屏(骨架)或用户手点的那一轮在飞。 */
const refreshing = computed(() => loadState.value === 'loading' || (inFlight.value && manualRound.value))

// ─── 分组筛选(数据轴)─────────────────────────────────────────────────────────
// 服务器自己带 groupId(登记时定的),组名来自 GET /api/groups(后端已按可见范围收敛)。
// 筛选只切展示口径,不改权限:能列出来的本来就是有权看的。
const groups = ref<Group[]>([])
const groupFilter = ref<string>(GROUP_ALL)
/** 组名单是否取到过:没取到时整屏都不贴组标签 —— 给每机器冠一句「分组已失效」是谎报。 */
const groupsLoaded = ref(false)

const groupLabels = computed(() => ({
  all: t('groups.filterAll'),
  ungrouped: t('groups.ungrouped'),
  missing: t('groups.groupMissing'),
  public: t('groups.visibilityPublic'),
  private: t('groups.visibilityPrivate'),
}))
const GROUP_OPTIONS = computed(() => groupOptions(groups.value, groupLabels.value))

function groupIdOf(serverId: string): string {
  return serverById.value.get(serverId)?.groupId ?? ''
}
function groupNameOf(serverId: string): string {
  return groupLabel(groupIdOf(serverId), groups.value, groupLabels.value)
}
/**
 * 卡/行上那枚组标签。名单没到位时返回空串(组件据此不渲染),因为此时"未归组"和
 * "分组已失效"都无从判断 —— 印一个错的归属比留白更糟。
 */
function groupTagOf(serverId: string): string {
  return groupsLoaded.value ? groupNameOf(serverId) : ''
}

/** 屏上这一档的机器(卡片与列表都吃它,所以两种视图口径一致)。 */
const shownMetrics = computed(() =>
  metrics.value.filter((m) => matchesGroup(groupIdOf(m.serverId), groupFilter.value)),
)
/** 筛完一台不剩:与「压根没登记服务器」是两回事,空态要说清是被筛掉了。 */
const filteredToEmpty = computed(() => metrics.value.length > 0 && shownMetrics.value.length === 0)

const reachableCount = computed(() => shownMetrics.value.filter((m) => m.reachable).length)
/** 分母 = 这一档里已登记的台数:卡片逐台上屏的中间态不该把「共几台」忽大忽小。 */
const totalCount = computed(() => {
  if (groupFilter.value === GROUP_ALL) return Math.max(serverById.value.size, metrics.value.length)
  let n = 0
  for (const s of serverById.value.values()) if (matchesGroup(s.groupId, groupFilter.value)) n++
  return Math.max(n, shownMetrics.value.length)
})

// 「远程」弹窗的目标机:null = 关闭。每次点卡上都重新取一份登记信息(改过 host 也生效)。
const remoteId = ref<string | null>(null)
const remoteServer = computed(() => (remoteId.value ? serverById.value.get(remoteId.value) ?? null : null))
const remoteHostLabel = computed(() => {
  const s = remoteServer.value
  return s ? `${s.user}@${s.host}:${s.port}` : ''
})
const remoteName = computed(() => remoteServer.value?.name ?? remoteId.value ?? '')

function openRemote(id: string): void {
  remoteId.value = id
}

function closeRemote(): void {
  remoteId.value = null
}

// 可达的排前面,不可达的沉底(两组内部都保持上屏顺序 = 登记时间倒序)。
// 只排当前筛选档的那些台:排序与筛选都收在这一份上,卡片和列表看到的是同一个序列。
const sortedMetrics = computed(() => {
  const up: ServerMetrics[] = []
  const down: ServerMetrics[] = []
  for (const m of shownMetrics.value) (m.reachable ? up : down).push(m)
  return up.length && down.length ? [...up, ...down] : shownMetrics.value
})

function displayName(m: ServerMetrics): string {
  return serverById.value.get(m.serverId)?.name ?? m.serverId
}

/** 列表行:同一份 sortedMetrics,只是把登记信息 join 成地址 + 所属分组(排序口径两边共用)。 */
const tableRows = computed(() =>
  sortedMetrics.value.map((m) => {
    const s = serverById.value.get(m.serverId)
    return {
      metrics: m,
      name: displayName(m),
      addr: s ? `${s.user}@${s.host}:${s.port}` : '',
      group: groupTagOf(m.serverId),
    }
  }),
)

// ─── 视图切换:卡片 / 列表 ─────────────────────────────────────────────────────
// 默认卡片:机器少时卡片的信息密度更合适,列表是给「几十台里找那台内存最紧的」用的。
// 偏好写 localStorage(与主题、项目页视图同一做法);取数逻辑完全不受视图影响。

type ViewMode = 'cards' | 'list'
const VIEW_MODE_KEY = 'pipewright.serverStatus.viewMode'

function readViewMode(): ViewMode {
  try {
    return localStorage.getItem(VIEW_MODE_KEY) === 'list' ? 'list' : 'cards'
  } catch {
    // 隐私模式 / 安全策略下 localStorage 会抛:安静退回卡片。
    return 'cards'
  }
}

const viewMode = ref<ViewMode>(readViewMode())

function setViewMode(mode: ViewMode): void {
  viewMode.value = mode
  try {
    localStorage.setItem(VIEW_MODE_KEY, mode)
  } catch {
    // 存不下不影响这次切换。
  }
}

function upsertMetrics(item: ServerMetrics): void {
  const at = indexById.get(item.serverId)
  if (at === undefined) {
    indexById.set(item.serverId, metrics.value.length)
    metrics.value.push(item)
    return
  }
  metrics.value[at] = item
}

/** 本轮之后已不存在的服务器(被删 / 收回可见性)从屏上撤掉。 */
function retainOnly(keep: Set<string>): void {
  const next = metrics.value.filter((m) => keep.has(m.serverId))
  indexById.clear()
  next.forEach((m, i) => indexById.set(m.serverId, i))
  metrics.value = next
}

function humanizeLoadError(err: unknown): string {
  if (err instanceof HttpError) {
    return err.status === 0
      ? t('serverStatus.errConnect')
      : (err.apiError?.message ?? t('serverStatus.errLoadStatus', { status: err.status }))
  }
  return t('serverStatus.errLoadRetry')
}

/** 手点刷新:让按钮立刻有反馈(转圈 + 挡重复点),不叠请求。 */
function refreshNow(): void {
  void load(true)
}

async function load(manual = false): Promise<void> {
  if (inFlight.value) {
    // 轮询那一轮正在飞时手点:不发第二个请求,但把转圈接过来给用户一个交代。
    if (manual) manualRound.value = true
    return
  }
  inFlight.value = true
  manualRound.value = manual
  // 首屏(屏上还没有任何卡片)才值得走骨架;之后一律就地替换。
  const firstLoad = metrics.value.length === 0
  if (firstLoad) {
    loadState.value = 'loading'
    loadError.value = ''
  }
  // 名单与指标流并行:名单一到位就上屏,免得第一张卡顶着 serverId 当标题。
  // 剪枝(撤掉已删的服务器)留到整轮结束再做,免得轮中间闪没一张卡。
  const namesPromise = loadServerNames()
    .then((m) => {
      serverById.value = m
      return m
    })
    .catch(() => null)
  void loadGroups()
  try {
    await streamAllServerMetrics(upsertMetrics)
    const names = await namesPromise
    if (names) retainOnly(new Set(names.keys()))
    staleError.value = ''
    updatedAt.value = new Date().toLocaleTimeString()
    loadState.value = 'idle'
  } catch (err) {
    const msg = humanizeLoadError(err)
    const names = await namesPromise
    if (metrics.value.length === 0) {
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
    manualRound.value = false
  }
}

async function loadServerNames(): Promise<Map<string, Server>> {
  const servers: Server[] = await listServers()
  const m = new Map<string, Server>()
  for (const s of servers) m.set(s.id, s)
  return m
}

/**
 * 组名单只在没取到过时取:它是静态目录,不像指标需要实时,每轮都拉白搭一次请求。
 * 失败就安静等着 —— 下一轮(或下次进页)自然重试。宁可用旧组名,也不给整屏机器贴「分组已失效」。
 */
async function loadGroups(): Promise<void> {
  if (groupsLoaded.value) return
  try {
    groups.value = await listGroups()
    groupsLoaded.value = true
  } catch {
    // 拿不到名单就把筛选档藏起来,不拿空数组冒充「只有未归组」。
  }
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
      <div class="view-actions">
        <!-- 分组筛选:机器一多就分不清哪台归哪档;名单没取到时整控件不出现(见 loadGroups)。 -->
        <div v-if="groupsLoaded && metrics.length > 0" class="group-filter">
          <select v-model="groupFilter" class="group-filter__select" :aria-label="t('groups.filterAria')">
            <option v-for="opt in GROUP_OPTIONS" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
          </select>
          <svg class="group-filter__arrow" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" aria-hidden="true">
            <path d="M6 9l6 6 6-6"/>
          </svg>
        </div>
        <!-- 视图切换:卡片铺开 / 一行一台。机器多了要横向比较「谁的内存最紧」,列表才扫得动。 -->
        <div v-if="metrics.length > 0" class="view-toggle" role="group" :aria-label="t('serverStatus.viewModeAria')">
          <button
            type="button"
            class="view-toggle__btn"
            :class="{ 'view-toggle__btn--active': viewMode === 'cards' }"
            :aria-pressed="viewMode === 'cards'"
            @click="setViewMode('cards')"
          >
            <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
              <rect x="3" y="3" width="7" height="7" rx="1.5" /><rect x="14" y="3" width="7" height="7" rx="1.5" />
              <rect x="3" y="14" width="7" height="7" rx="1.5" /><rect x="14" y="14" width="7" height="7" rx="1.5" />
            </svg>
            {{ t('serverStatus.viewCards') }}
          </button>
          <button
            type="button"
            class="view-toggle__btn"
            :class="{ 'view-toggle__btn--active': viewMode === 'list' }"
            :aria-pressed="viewMode === 'list'"
            @click="setViewMode('list')"
          >
            <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true">
              <path d="M3 5h18M3 12h18M3 19h11" />
            </svg>
            {{ t('serverStatus.viewList') }}
          </button>
        </div>
        <!-- 手点才转圈(顺带挡重复点);自动轮询保持静默(按钮不变、卡片不压暗)。 -->
        <AppButton class="view-refresh" variant="default" :loading="refreshing" @click="refreshNow">
          {{ t('common.refresh') }}
        </AppButton>
      </div>
    </header>

    <!-- Initial loading skeletons -->
    <div
      v-if="loadState === 'loading' && metrics.length === 0"
      :class="viewMode === 'list' ? 'skeleton-rows' : 'metrics-grid'"
      aria-busy="true"
      :aria-label="t('serverStatus.loadingAria')"
    >
      <template v-if="viewMode === 'list'">
        <div v-for="n in 4" :key="n" class="skeleton-row">
          <SkeletonBlock :height="16" width="24%" />
          <SkeletonBlock :height="16" width="18%" />
          <SkeletonBlock :height="16" width="30%" />
        </div>
      </template>
      <template v-else>
        <div v-for="n in 3" :key="n" class="skeleton-card">
          <SkeletonBlock :height="20" width="50%" />
          <SkeletonBlock :height="14" width="80%" />
          <SkeletonBlock :height="14" width="80%" />
        </div>
      </template>
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

    <!-- 筛到一台不剩:机器是有的,只是不在这一档 —— 说「没有服务器」会让人白跑去登记 -->
    <EmptyState
      v-else-if="filteredToEmpty"
      :title="t('serverStatus.groupEmptyTitle')"
      :description="t('serverStatus.groupEmpty')"
    />

    <!-- Metrics(可达在前、不可达沉底):两种视图吃同一份排序结果 -->
    <ServerMetricsTable v-else-if="viewMode === 'list'" :rows="tableRows" @remote="openRemote" />
    <div v-else class="metrics-grid">
      <ServerMetricsCard
        v-for="m in sortedMetrics"
        :key="m.serverId"
        :name="displayName(m)"
        :group-label="groupTagOf(m.serverId)"
        :metrics="m"
        @remote="openRemote"
      />
    </div>

    <!-- 远程:上半屏终端、下半屏文件面板(挂载即连,卸载即断 —— 不给后台留一条 PTY) -->
    <RemoteWorkspaceModal
      v-if="remoteId"
      :server-id="remoteId"
      :server-name="remoteName"
      :host-label="remoteHostLabel"
      @close="closeRemote"
    />
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

.view-actions {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  /* 窄屏(或分组名单长)时三个控件放不下:让它们换行,而不是把「刷新」挤出可视区 */
  flex-wrap: wrap;
  gap: 10px;
  flex-shrink: 0;
}

/* 分组下拉:与视图切换同高(26 + 3*2 padding ≈ 32),免得两个控件一高一低。 */
.group-filter {
  position: relative;
  width: 170px;
}
.group-filter__select {
  width: 100%;
  height: 34px;
  padding: 0 30px 0 10px;
  background: var(--color-inset);
  border: 1px solid var(--color-line);
  border-radius: var(--rounded-md);
  color: var(--color-text);
  font-family: inherit;
  font-size: var(--text-label);
  appearance: none;
  cursor: pointer;
}
.group-filter__select:focus-visible {
  outline: none;
  border-color: var(--color-primary);
}
.group-filter__arrow {
  position: absolute;
  right: 10px;
  top: 50%;
  transform: translateY(-50%);
  color: var(--color-faint);
  pointer-events: none;
}

/* 视图切换:与项目页同一「凹底 + 抬起当前档」的形状(token 名这页用 --color-line/--color-surface)。 */
.view-toggle {
  display: flex;
  gap: 4px;
  padding: 3px;
  background: var(--color-inset);
  border: 1px solid var(--color-line);
  border-radius: var(--rounded-md);
}
.view-toggle__btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 26px;
  padding: 0 10px;
  border: none;
  background: transparent;
  border-radius: var(--rounded-sm, 4px);
  color: var(--color-dim);
  font-family: inherit;
  font-size: var(--text-label);
  font-weight: 550;
  cursor: pointer;
  white-space: nowrap;
  transition: color var(--duration-fast) var(--ease-out-expo), background-color var(--duration-fast) var(--ease-out-expo);
}
.view-toggle__btn:hover {
  color: var(--color-text);
}
.view-toggle__btn--active {
  background: var(--color-surface);
  color: var(--color-text);
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

/* 列表视图的首屏骨架:一行一台,免得切了视图还先看见三张卡。 */
.skeleton-rows {
  display: flex;
  flex-direction: column;
  gap: 1px;
  padding: 10px 14px;
  border: 1px solid var(--color-line);
  border-radius: var(--rounded-lg);
  background: var(--color-surface);
}
.skeleton-row {
  display: flex;
  align-items: center;
  gap: 24px;
  padding: 12px 0;
}
</style>
