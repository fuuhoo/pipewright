<script setup lang="ts">
/**
 * NotificationRecords.vue — 左栏「通知记录」:每次路由投递逐渠道留下一行,这里把它读回来。
 *
 * 渠道/路由/模板那三张表回答「怎么发」,这一页回答「发出去了没有」:成功、失败(带人读原因),
 * 或渠道被停用而跳过。正文是当时渲染出来的那份,点开一行就能看全文,不用再去翻进程日志。
 *
 * 刻意不做的事:
 *  · 不改发送行为,也不给「重发」按钮 —— 台账是事后凭证,重发会造出第二条不存在的历史。
 *  · 只覆盖路由投递;流水线里的「通知」节点走直发通道,结果在那条运行的日志里。
 *  · 事件与结论的候选由接口带出,前端不硬编码(事件早已从 6 个涨到 11 个)。
 */
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import {
  listDeliveries,
  type DeliveryStatus,
  type NotificationDelivery,
} from '../api/notifications'
import {
  eventLabelKey,
  hasActiveFilters,
  resolveFilters,
  statusLabelKey,
  toListParams,
  totalPages,
  withFilters,
  type DeliveryFilters,
} from '../lib/notificationRecords'
import { listProjects, type Project } from '../api/projects'
import { HttpError } from '../api/http'
import AppButton from '../components/ui/AppButton.vue'
import AppSelect from '../components/ui/AppSelect.vue'
import ErrorState from '../components/ui/ErrorState.vue'
import EmptyState from '../components/ui/EmptyState.vue'
import SkeletonBlock from '../components/ui/SkeletonBlock.vue'
import ProjectPicker from '../components/projects/ProjectPicker.vue'
import { GROUP_ALL } from '../lib/groupFilter'

type LoadState = 'idle' | 'loading' | 'error'

const route = useRoute()
const router = useRouter()
const { t, te } = useI18n()

/** 每页条数:台账一行比运行一行短,20 条在常见视口里刚好一屏多一点。 */
const PAGE_SIZE = 20

const loadState = ref<LoadState>('idle')
const loadError = ref('')
const deliveries = ref<NotificationDelivery[]>([])
const projects = ref<Project[]>([])
const total = ref(0)
/** 接口带出的两份枚举(第一次响应之前为空,下拉只剩「全部」)。 */
const eventOptions = ref<string[]>([])
const statusOptions = ref<DeliveryStatus[]>([])
/** 展开看正文的那一行;同一时刻只开一行,免得长正文把表格撑散。 */
const expandedId = ref('')

// ─── URL as state ───────────────────────────────────────────────────────────

const filters = computed<DeliveryFilters>(() => resolveFilters(route.query as Record<string, unknown>))

function apply(next: Partial<DeliveryFilters>): void {
  const merged: DeliveryFilters = { ...filters.value, ...next }
  void router.replace({ query: withFilters(route.query as Record<string, string>, merged) })
}

function setEvent(value: string): void {
  apply({ event: value, page: 1 })
}

function setStatus(value: string): void {
  apply({ status: value, page: 1 })
}

function setProject(value: string): void {
  apply({ projectId: value, page: 1 })
}

function setPage(value: number): void {
  apply({ page: value })
}

function clearFilters(): void {
  void router.replace({ query: withFilters(route.query as Record<string, string>, { event: '', status: '', projectId: '', page: 1 }) })
}

// ─── labels ─────────────────────────────────────────────────────────────────

/**
 * 事件名复用设置页那批已翻过 8 语种的键。
 *
 * 后端加事件时这里会生成一个没文案的键:先 te() 探一下,没有就退成原始 ID(build_queued
 * 总比 `settingsNotifications.evBuildQueued` 好读 —— 少一句翻译可以,亮键路径不行)。
 */
function eventLabel(event: string): string {
  const key = eventLabelKey(event)
  if (key && te(key)) return t(key)
  return event
}

function statusLabel(status: string): string {
  const key = statusLabelKey(status)
  return key && te(key) ? t(key) : status
}

function formatWhen(iso: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '—' : d.toLocaleString()
}

// ─── load ───────────────────────────────────────────────────────────────────

async function load(): Promise<void> {
  loadState.value = 'loading'
  loadError.value = ''
  try {
    const res = await listDeliveries(toListParams(filters.value, PAGE_SIZE))
    deliveries.value = res.items
    total.value = res.total
    eventOptions.value = res.events
    statusOptions.value = res.statuses
    loadState.value = 'idle'
  } catch (err) {
    if (err instanceof HttpError) {
      loadError.value =
        err.status === 0
          ? t('notificationRecords.errNetwork')
          : (err.apiError?.message ?? t('notificationRecords.errLoad', { status: err.status }))
    } else {
      loadError.value = t('notificationRecords.errLoadRetry')
    }
    loadState.value = 'error'
  }
}

async function loadProjects(): Promise<void> {
  try {
    projects.value = await listProjects()
  } catch {
    // 项目清单只为筛选服务,拉不到就把下拉空着 —— 列表照旧是全量的。
    projects.value = []
  }
}

function toggleRow(id: string): void {
  expandedId.value = expandedId.value === id ? '' : id
}

function goToRun(runId: string): void {
  void router.push({ name: 'run-detail', params: { id: runId } })
}

const lastPage = computed(() => totalPages(total.value, PAGE_SIZE))

const eventSelectOptions = computed(() => [
  { value: '', label: t('notificationRecords.allEvents') },
  ...eventOptions.value.map((e) => ({ value: e, label: eventLabel(e) })),
])

const statusSelectOptions = computed(() => [
  { value: '', label: t('notificationRecords.allStatuses') },
  ...statusOptions.value.map((s) => ({ value: s, label: statusLabel(s) })),
])

// 地址栏是唯一状态源:筛选或页码变了就重拉,同时收起展开行(那一行多半已经不在这一页)。
// 只盯这一页自己的四个参数,并且离开本页时收手 —— 不然每次跳走都白发一次注定没人看的请求。
const watchedSig = computed(() => {
  const q = route.query as Record<string, unknown>
  return `${q.event ?? ''}|${q.status ?? ''}|${q.projectId ?? ''}|${q.page ?? ''}`
})
watch(watchedSig, () => {
  if (route.name !== 'notification-records') return
  expandedId.value = ''
  void load()
})

/** 空列表说的是哪句话:没筛是「还没有记录」,筛了是「这个筛选下没有」。 */
const emptyCopy = computed(() =>
  hasActiveFilters(filters.value)
    ? { title: t('notificationRecords.emptyFilteredTitle'), desc: t('notificationRecords.emptyFilteredDesc') }
    : { title: t('notificationRecords.emptyTitle'), desc: t('notificationRecords.emptyDesc') },
)

onMounted(() => {
  void loadProjects()
  void load()
})
</script>

<template>
  <div class="nr-view">
    <header class="view-header">
      <div class="view-header__text">
        <h1 class="view-title">{{ t('notificationRecords.title') }}</h1>
        <p class="view-sub">{{ t('notificationRecords.subtitle') }}</p>
      </div>
      <AppButton variant="default" :loading="loadState === 'loading'" @click="load">
        {{ t('common.refresh') }}
      </AppButton>
    </header>

    <!-- 筛选:事件与结论用接口带出的枚举,项目沿用运行页那个可搜索下拉(同口径,同一个词在两页命中同一批项目)。 -->
    <div class="nr-filters" role="group" :aria-label="t('notificationRecords.filterAria')">
      <AppSelect
        :model-value="filters.event"
        :options="eventSelectOptions"
        :aria-label="t('notificationRecords.eventAria')"
        min-width="180px"
        @update:model-value="setEvent"
      />
      <AppSelect
        :model-value="filters.status"
        :options="statusSelectOptions"
        :aria-label="t('notificationRecords.statusAria')"
        min-width="150px"
        @update:model-value="setStatus"
      />
      <ProjectPicker
        input-id="nr-project"
        :model-value="filters.projectId"
        :projects="projects"
        :group-filter="GROUP_ALL"
        :placeholder="t('notificationRecords.projectSearchPlaceholder')"
        :ariaLabel="t('notificationRecords.projectAria')"
        :all-label="t('notificationRecords.allProjects')"
        :loading-label="t('notificationRecords.loadingProjects')"
        :empty-label="t('notificationRecords.projectSearchEmpty')"
        @update:model-value="setProject"
      />
      <AppButton
        v-if="hasActiveFilters(filters)"
        variant="ghost"
        @click="clearFilters"
      >
        {{ t('notificationRecords.clearFilters') }}
      </AppButton>
    </div>

    <!-- Error -->
    <ErrorState
      v-if="loadState === 'error'"
      :title="t('notificationRecords.errLoadTitle')"
      :description="loadError"
      @retry="load"
    />

    <!-- Loading skeleton -->
    <div v-else-if="loadState === 'loading' && deliveries.length === 0" class="table-wrap" aria-busy="true">
      <div v-for="i in 6" :key="i" class="skel-row">
        <SkeletonBlock :height="14" width="72px" />
        <SkeletonBlock :height="14" width="96px" />
        <SkeletonBlock :height="14" width="150px" />
        <SkeletonBlock :height="14" width="110px" />
        <SkeletonBlock :height="14" width="30%" />
        <SkeletonBlock :height="14" width="120px" />
      </div>
    </div>

    <!-- Empty -->
    <EmptyState
      v-else-if="deliveries.length === 0"
      :title="emptyCopy.title"
      :description="emptyCopy.desc"
    />

    <!-- Ledger -->
    <template v-else>
      <p class="result-count" aria-live="polite">
        {{ t('notificationRecords.resultCount', { n: total }) }}
      </p>

      <div class="table-wrap">
        <div class="table-head" role="row">
          <span class="th">{{ t('notificationRecords.colStatus') }}</span>
          <span class="th">{{ t('notificationRecords.colEvent') }}</span>
          <span class="th">{{ t('notificationRecords.colProject') }}</span>
          <span class="th">{{ t('notificationRecords.colChannel') }}</span>
          <span class="th">{{ t('notificationRecords.colTitle') }}</span>
          <span class="th">{{ t('notificationRecords.colTime') }}</span>
          <span class="th th--tail" />
        </div>

        <template v-for="d in deliveries" :key="d.id">
          <div
            class="delivery-row"
            :class="{ 'delivery-row--failed': d.status === 'failed' }"
            role="row"
            tabindex="0"
            :aria-expanded="expandedId === d.id"
            :aria-label="t('notificationRecords.rowAria', { event: eventLabel(d.event), channel: d.channelName, status: statusLabel(d.status) })"
            @click="toggleRow(d.id)"
            @keydown.enter.prevent="toggleRow(d.id)"
            @keydown.space.prevent="toggleRow(d.id)"
          >
            <span class="col-status">
              <span class="pill" :class="`pill--${d.status}`">{{ statusLabel(d.status) }}</span>
            </span>
            <span class="col-event">{{ eventLabel(d.event) }}</span>
            <span class="col-project" :title="d.projectName || ''">
              {{ d.projectName || t('notificationRecords.globalEvent') }}
            </span>
            <span class="col-channel" :title="d.channelName">{{ d.channelName }}</span>
            <span class="col-title" :title="d.title">{{ d.title }}</span>
            <span class="col-time">{{ formatWhen(d.createdAt) }}</span>
            <span class="col-tail" aria-hidden="true">{{ expandedId === d.id ? '▾' : '▸' }}</span>
          </div>

          <!-- 展开:当时发出去的那份正文,原样呈现(库里存的就是渲染结果,已按字符截过顶)。 -->
          <div v-if="expandedId === d.id" class="delivery-detail">
            <div v-if="d.errorText" class="detail-error">
              <span class="detail-error__label">{{ t('notificationRecords.reasonLabel') }}</span>
              <span>{{ d.errorText }}</span>
            </div>
            <p class="detail-label">{{ t('notificationRecords.bodyLabel') }}</p>
            <pre class="detail-body">{{ d.body || t('notificationRecords.bodyEmpty') }}</pre>
            <div class="detail-foot">
              <span class="detail-meta">
                {{ t('notificationRecords.channelLabel', { name: d.channelName }) }}
                <template v-if="d.projectName"> · {{ d.projectName }}</template>
              </span>
              <AppButton v-if="d.runId" variant="ghost" @click="goToRun(d.runId)">
                {{ t('notificationRecords.viewRun') }} →
              </AppButton>
            </div>
          </div>
        </template>
      </div>

      <!-- 分页:台账按时间倒序累积,不像运行页那样一次取满就完事。 -->
      <div class="pager">
        <AppButton
          variant="default"
          :disabled="filters.page <= 1 || loadState === 'loading'"
          @click="setPage(filters.page - 1)"
        >
          ← {{ t('notificationRecords.prevPage') }}
        </AppButton>
        <span class="pager__count">{{ t('notificationRecords.pagePosition', { page: filters.page, pages: lastPage }) }}</span>
        <AppButton
          variant="default"
          :disabled="filters.page >= lastPage || loadState === 'loading'"
          @click="setPage(filters.page + 1)"
        >
          {{ t('notificationRecords.nextPage') }} →
        </AppButton>
      </div>
    </template>
  </div>
</template>

<style scoped>
.nr-view {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}

.view-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--space-4);
}
.view-title {
  font-size: var(--text-display);
  font-weight: 700;
  letter-spacing: -0.02em;
  margin: 0;
}
.view-sub {
  margin: var(--space-1) 0 0;
  color: var(--color-dim);
  font-size: var(--text-body);
}

.nr-filters {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  flex-wrap: wrap;
}

.result-count {
  margin: 0;
  font-size: var(--text-label);
  color: var(--color-dim);
}

.table-wrap {
  background: var(--color-card);
  border: 1px solid var(--color-border);
  border-radius: var(--rounded-card);
  /* 窄屏(内置浏览器面板)时整表横向滚,别让 minmax(0,1fr) 那两列被挤成 0 宽 ——
     那样「项目」和「标题」会直接看不见,而不是缩短成省略号。 */
  overflow-x: auto;
  overflow-y: hidden;
}

.table-head,
.delivery-row {
  display: grid;
  grid-template-columns: 96px 120px minmax(0, 1fr) 130px minmax(0, 1.6fr) 160px 24px;
  /* 摊开七列的最短可读宽度:窄屏交给 .table-wrap 横向滚,表头和行共用同一个值才对齐。 */
  min-width: 980px;
  padding: 0 18px;
  gap: 12px;
  align-items: center;
}

.table-head {
  height: 38px;
  background: var(--color-card-2);
  border-bottom: 1px solid var(--color-border);
}
.th {
  font-size: 0.71rem;
  font-weight: 600;
  letter-spacing: 0.04em;
  text-transform: uppercase;
  color: var(--color-faint);
}
.th--tail {
  text-align: center;
}

.delivery-row {
  min-height: 46px;
  border-bottom: 1px solid var(--color-border);
  cursor: pointer;
  font-size: var(--text-body);
  outline: none;
}
.delivery-row:last-child {
  border-bottom: none;
}
.delivery-row:hover {
  background: var(--color-inset);
}
.delivery-row:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: -2px;
}
/* 失败那行抹一层淡红:一排记录里要找失败,不该靠读文字。 */
.delivery-row--failed {
  background: linear-gradient(90deg, var(--color-red-soft) 0%, transparent 46%);
}

.col-event,
.col-project,
.col-channel,
.col-title {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.col-project,
.col-channel {
  color: var(--color-dim);
}
.col-time {
  font-size: var(--text-label);
  color: var(--color-dim);
}
.col-tail {
  text-align: center;
  color: var(--color-faint);
  font-size: 0.8rem;
}

.pill {
  display: inline-block;
  font-size: var(--text-micro);
  font-weight: 700;
  padding: 2px 8px;
  border-radius: var(--rounded-md);
  border: 1px solid var(--color-border);
  white-space: nowrap;
}
.pill--ok {
  color: var(--color-green);
  border-color: color-mix(in oklch, var(--color-green) 45%, transparent);
  background: color-mix(in oklch, var(--color-green) 12%, transparent);
}
.pill--failed {
  color: var(--color-red);
  border-color: color-mix(in oklch, var(--color-red) 45%, transparent);
  background: color-mix(in oklch, var(--color-red) 12%, transparent);
}
.pill--skipped {
  color: var(--color-faint);
}

/* 展开区 */
.delivery-detail {
  /* 与表头/行同一个下限:横向滚动时展开面板跟着铺满,不会只停在左侧那一小截。 */
  min-width: 980px;
  padding: var(--space-3) 18px var(--space-4);
  border-bottom: 1px solid var(--color-border);
  background: var(--color-inset);
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}
.delivery-detail:last-child {
  border-bottom: none;
}
.detail-error {
  display: flex;
  gap: 6px;
  font-size: var(--text-label);
  color: var(--color-red);
}
.detail-error__label {
  font-weight: 700;
  flex-shrink: 0;
}
.detail-label {
  margin: 0;
  font-size: var(--text-micro);
  font-weight: 700;
  letter-spacing: 0.04em;
  text-transform: uppercase;
  color: var(--color-faint);
}
/* 正文原样呈现:通知是多行纯文本,折叠空白就等于没给全文。 */
.detail-body {
  margin: 0;
  padding: var(--space-3);
  background: var(--color-card);
  border: 1px solid var(--color-border);
  border-radius: var(--rounded-md);
  font-family: var(--font-mono, monospace);
  font-size: var(--text-label);
  line-height: 1.55;
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 320px;
  overflow: auto;
}
.detail-foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-3);
}
.detail-meta {
  font-size: var(--text-label);
  color: var(--color-dim);
}

.pager {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-3);
}
.pager__count {
  font-size: var(--text-label);
  color: var(--color-dim);
}

.skel-row {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 14px 18px;
  border-bottom: 1px solid var(--color-border);
}
.skel-row:last-child {
  border-bottom: none;
}
</style>
