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
import { listGroups, type Group } from '../api/groups'
import { GROUP_ALL, groupLabel, groupOptions, matchesGroup } from '../lib/groupFilter'
import {
  PAGE_ALL,
  PAGE_SIZE_OPTIONS,
  hostHasMatch,
  queryTokens,
  type HostFields,
} from '../lib/containerListFilter'
import { buildStackGroups, stackHasMatch, stackHasRows, NO_STACK, type StackGroup } from '../lib/containerStacks'
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
import StackCard from '../components/ops/StackCard.vue'
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

// ─── 宿主分组筛选(数据轴)────────────────────────────────────────────────────
// 容器没有自己的分组:后端没有 KindContainer,容器是经 SSH 从宿主现采的,归属就是那台
// 机器的组(见 docs/权限架构说明.md §3)。所以这里筛的是「宿主在哪一档」,组名来自
// GET /api/groups(后端已按可见范围收敛)。筛选只切展示口径,不改权限。
const permGroups = ref<Group[]>([])
const groupFilter = ref<string>(GROUP_ALL)
/** 组名单是否取到过:没取到时不贴标签 —— 给每台机器冠一句「分组已失效」是谎报。 */
const groupsLoaded = ref(false)

const groupLabels = computed(() => ({
  all: t('groups.filterAll'),
  ungrouped: t('groups.ungrouped'),
  missing: t('groups.groupMissing'),
  public: t('groups.visibilityPublic'),
  private: t('groups.visibilityPrivate'),
}))
const GROUP_OPTIONS = computed(() => groupOptions(permGroups.value, groupLabels.value))

function hostGroupIds(serverId: string): string[] {
  return serverById.value.get(serverId)?.groupIds ?? []
}
/** 卡头那枚组标签;名单没到位时返回空串(卡片据此不渲染),此时「未归组」和「组已失效」都无从判断。 */
function hostGroupTag(serverId: string): string {
  if (!groupsLoaded.value) return ''
  return groupLabel(hostGroupIds(serverId), permGroups.value, groupLabels.value)
}

const POLL_MS = 12_000
let timer: ReturnType<typeof setInterval> | null = null

// ─── 服务器切换 ───────────────────────────────────────────────────────────────
// 聚合是一屏摊开所有机器,机器一多就要滚动找,所以给一个「全部 / 只看这台 / 仅看可用的」范围。
// 范围只切展示与统计口径,不改权限:候选永远来自后端已收敛过的聚合结果。
const serverScope = ref<ServerScope>(SERVER_ALL)

/** 在线的在前、离线的沉底(与服务器状态页同一口径);切换范围不改变这个顺序。 */
const orderedGroups = computed(() => reachableFirst(groups.value))
/** 分组先收窄,后面「只看这台 / 仅看可用的」都只在这一档里解析(两个筛选叠起来才是直觉)。 */
const groupScopedServers = computed(() =>
  orderedGroups.value.filter((g) => matchesGroup(hostGroupIds(g.serverId), groupFilter.value)),
)
/** 每轮聚合都换一批新对象;选中的机器掉了就自动回落「全部」(按钮高亮与内容始终一致)。 */
const scope = computed(() => resolveServerScope(serverScope.value, groupScopedServers.value))
const scopeGroups = computed(() => scopedGroups(groupScopedServers.value, scope.value))
/** 一台机时没什么可切的,这一行就不占地方。 */
const showScopeBar = computed(() => groupScopedServers.value.length > 1)
/** 有离线机器才值得给「仅看可用的」这一档(否则它跟「全部」是同一屏,纯噪音)。 */
const hasOffline = computed(() => groupScopedServers.value.some((g) => !g.reachable))
const reachableCount = computed(() => groupScopedServers.value.filter((g) => g.reachable).length)
/** 机器是有的,只是不在当前这一档:与「压根没登记机器」是两种空态。 */
const filteredToEmpty = computed(() => groups.value.length > 0 && groupScopedServers.value.length === 0)
/** 分母 = 这一档里已登记的台数:逐台上屏的中间态不该让「共几台」忽大忽小。 */
const registeredCount = computed(() => {
  if (groupFilter.value === GROUP_ALL) return Math.max(serverById.value.size, orderedGroups.value.length)
  let n = 0
  for (const s of serverById.value.values()) if (matchesGroup(s.groupIds, groupFilter.value)) n++
  return Math.max(n, groupScopedServers.value.length)
})

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
// 一个框多字段:命中 容器名 / 镜像 / 容器 ID / 端口映射 / 状态 / Stack 名 / 机器名 / 机器 IP
// 任一即算命中(判据在 lib/containerListFilter,卡片行与这里的「整张卡片该不该出现」共用同一函数)。
const searchText = ref('')
const searchTokens = computed(() => queryTokens(searchText.value))

function hostFields(serverId: string): HostFields {
  return { name: serverName(serverId), host: serverHost(serverId) }
}

/** 搜索时只留命中机器的卡片:一台机器一屏,摊 50 张空卡片比滚动找更累人。 */
const displayGroups = computed(() => {
  const tokens = searchTokens.value
  if (tokens.length === 0) return scopeGroups.value
  return scopeGroups.value.filter((g) => hostHasMatch(g.containers, hostFields(g.serverId), tokens))
})

// ─── 视图切换:按主机 / 按 Stack ───────────────────────────────────────────────
// 同一份逐台聚合结果,两种切法。按 Stack 时把 compose 项目相同的那些行合成一条 ——
// 平台的 deploy_docker 一次把同一条栈铺 N 台,按机器摊开反而看不出它铺到了哪几台。
// 项目名由后端从 `docker ps` 的 Labels 列里带出来(不额外跑 inspect),所以这一档
// 跟按主机视图共用同一轮刷新,不多花一次 SSH。
const HOST_VIEW = 'host'
const STACK_VIEW = 'stack'
const viewMode = ref<string>(HOST_VIEW)

/** 涉及的宿主展示信息(名字/地址/所属分组);StackCard 只认这张表,不猜名字。 */
const hostViews = computed(() => {
  const m: Record<string, { name: string; host: string; group: string }> = {}
  for (const g of scopeGroups.value) {
    m[g.serverId] = { name: serverName(g.serverId), host: serverHost(g.serverId), group: hostGroupTag(g.serverId) }
  }
  return m
})

const stacks = computed<StackGroup[]>(() => buildStackGroups(scopeGroups.value))
/** 只按搜索收敛的栈:空态文案要说准是「搜没了」还是「这一档没行」,所以先不掺状态筛。 */
const stacksBySearch = computed(() => {
  const tokens = searchTokens.value
  if (tokens.length === 0) return stacks.value
  return stacks.value.filter((s) => stackHasMatch(s, hostFields, tokens))
})
/** 与按主机视图同一口径:搜索只收敛「这条栈该不该出现在屏上」,行级筛选在卡片里做。 */
const displayStacks = computed(() => stacksBySearch.value.filter((s) => stackHasRows(s, stateFilter.value)))
/** 有机器、却一台都不命中:与「机器被分组筛光」是两种空态,文案要分开(两种视图各按各自的单位判)。 */
const searchToEmpty = computed(() => {
  if (searchTokens.value.length === 0 || scopeGroups.value.length === 0) return false
  return viewMode.value === STACK_VIEW ? stacksBySearch.value.length === 0 : displayGroups.value.length === 0
})
/** 状态档筛到一张栈不剩:容器都还在,只是都不在这一档 —— 与「搜没了」也是两种空态。 */
const bucketToEmpty = computed(() => stacksBySearch.value.length > 0 && displayStacks.value.length === 0)
/** 范围内一条栈都没有(机器都在,只是没有容器):与「搜没了」是两种空态。 */
const stacksEmpty = computed(() => stacks.value.length === 0)
/** 栈头那枚「几条栈」计数:独立容器那一档不算一条栈,它是「其余都是 compose」的兜底。 */
const stackCount = computed(() => stacks.value.filter((s) => s.key !== NO_STACK).length)

// ─── 每页条数 ─────────────────────────────────────────────────────────────────
// 分页单位是一张卡片里的容器行(机器维度已经有「只看这台」+ 分组筛选 + 搜索收敛)。
const pageSize = ref<number>(PAGE_SIZE_OPTIONS[0])

// ─── 卡片折叠 ─────────────────────────────────────────────────────────────────
// 折叠集由页面持有,卡片只读自己那一项 —— 这样页头才能一键折叠/展开当前范围的全部卡片。
// 范围外的 id 保留:切回那台时还是折叠着,这正是逐台看时的连续性。
// 两种视图各留一份:serverId 与 compose 项目名是两套 key,混在一格里会互相误伤。
const folded = ref<Set<string>>(new Set())
const foldedStacks = ref<Set<string>>(new Set())
// 只有连得上且有运行时的卡片有正文可折(卡片内那个按钮也是这个条件)。
const foldableKeys = computed(() =>
  viewMode.value === STACK_VIEW
    ? displayStacks.value.map((s) => s.key)
    : displayGroups.value.filter((g) => g.reachable && g.runtime).map((g) => g.serverId),
)
const foldedInMode = computed(() => (viewMode.value === STACK_VIEW ? foldedStacks.value : folded.value))
const allFolded = computed(
  () => foldableKeys.value.length > 0 && foldableKeys.value.every((k) => foldedInMode.value.has(k)),
)
function toggleFold(serverId: string): void {
  const next = new Set(folded.value)
  if (next.has(serverId)) next.delete(serverId)
  else next.add(serverId)
  folded.value = next
}
function toggleFoldStack(key: string): void {
  const next = new Set(foldedStacks.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  foldedStacks.value = next
}
function foldAll(): void {
  const next = allFolded.value ? new Set<string>() : new Set(foldableKeys.value)
  if (viewMode.value === STACK_VIEW) foldedStacks.value = next
  else folded.value = next
}

// ─── 从 Stack 行跳回那台机器的「容器」tab ──────────────────────────────────────
// 栈视图(页面级 StackCard 的行、机器卡片 Stacks tab 展开出来的服务行)只回答「这条栈铺到了
// 哪几台、有哪几个容器」;要看某个容器的完整一行(端口映射、实时占用、生命周期按钮)得回到
// 那台机器的「容器」tab。一次跳转要把这一屏摆到那个容器面前:视图切回按主机、范围收到那台、
// 卡片从折叠集里放出来、卡片停在容器 tab,并把状态档松开到「全部」—— 栈里那一台展开服务行时不看
// 状态档,点进来的容器未必在当前档位里,点了没反应是最糟的结果,宁可多显示。
// (分组不用动:屏幕上的栈行本来就来自分组收敛后的集合,那把尺挡不住已显示的行。)
// 搜索框填容器名:落点就是这一行,用户想看清别的把词一删就回来了。
const containerFocus = ref<{ serverId: string; n: number } | null>(null)
/** 每次跳转换一个编号:卡片只认编号变没变,同一台连续跳两次也得切 tab。 */
let focusSeq = 0

function focusContainerTab(serverId: string, containerName: string): void {
  const name = containerName.trim()
  if (!name) return
  viewMode.value = HOST_VIEW
  serverScope.value = serverId
  stateFilter.value = 'all'
  searchText.value = name
  if (folded.value.has(serverId)) {
    const next = new Set(folded.value)
    next.delete(serverId)
    folded.value = next
  }
  focusSeq += 1
  containerFocus.value = { serverId, n: focusSeq }
}

/** 某张卡片该不该切到容器 tab:只认自己那一项,编号每次跳转都变。 */
function tabFocusFor(serverId: string): number {
  const f = containerFocus.value
  return f && f.serverId === serverId ? f.n : 0
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

// 收窄范围后,不在范围内的勾选既看不见又仍会被批量执行 —— 那是最坏的一种「偷偷操作」,直接清掉。
// 切分组同理:它同样把机器整批移出屏外。
watch([scope, groupFilter], () => clearSelection())

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
 * 组名单只在没取到过时取:它是静态目录,不像容器要实时,每轮都拉白搭一次请求。
 * 失败就安静等下一轮 —— 宁可用旧组名,也不给整屏机器贴「分组已失效」。
 */
async function loadGroups(): Promise<void> {
  if (groupsLoaded.value) return
  try {
    permGroups.value = await listGroups()
    groupsLoaded.value = true
  } catch {
    // 拿不到名单就把筛选档藏起来,不拿空数组冒充「只有未归组」。
  }
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
  void loadGroups()
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

    <!-- 只要还连着机器,筛选段/搜索框/视图切换就必须留在屏上:空态收的是卡片正文,不是控件。
         把控件一起藏掉,用户点进「已暂停」这一档就再也回不来(没有「全部」可切,只能刷新页面)。 -->
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
          v-for="g in groupScopedServers"
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
          <!-- 视图切换:同一份逐台聚合结果,按机器摊 or 按 compose 项目摊(后者看得见一条栈铺了几台) -->
          <div class="view-switch" role="group" :aria-label="t('containers.viewAria')">
            <button
              class="filter-chip"
              :class="{ 'filter-chip--active': viewMode === HOST_VIEW }"
              :aria-pressed="viewMode === HOST_VIEW"
              @click="viewMode = HOST_VIEW"
            >
              {{ t('containers.viewHost') }}
              <span class="filter-chip__count">{{ scopeGroups.length }}</span>
            </button>
            <button
              class="filter-chip"
              :class="{ 'filter-chip--active': viewMode === STACK_VIEW }"
              :aria-pressed="viewMode === STACK_VIEW"
              @click="viewMode = STACK_VIEW"
            >
              {{ t('containers.viewStack') }}
              <span class="filter-chip__count">{{ stackCount }}</span>
            </button>
          </div>
          <!-- 宿主分组筛选:容器跟宿主走,所以这一档切的是「哪几台机器的容器」 -->
          <div v-if="groupsLoaded" class="group-filter">
            <select v-model="groupFilter" class="group-filter__select" :aria-label="t('groups.filterAria')">
              <option v-for="opt in GROUP_OPTIONS" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
            </select>
            <svg class="group-filter__arrow" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" aria-hidden="true">
              <path d="M6 9l6 6 6-6"/>
            </svg>
          </div>
          <div class="page-size">
            <span class="page-size__label">{{ t('containers.pageSizeLabel') }}</span>
            <select v-model.number="pageSize" class="page-size__select" :aria-label="t('containers.pageSizeAria')">
              <option v-for="n in PAGE_SIZE_OPTIONS" :key="n" :value="n">
                {{ n === PAGE_ALL ? t('containers.pageSizeAll') : n }}
              </option>
            </select>
          </div>
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
          <!-- 机器 / 栈 一多,逐个点折叠太累:一档开关按当前范围整体收起/放开 -->
          <button
            v-if="foldableKeys.length > 1"
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

      <!-- 卡片正文区。四种「筛没了」各自说清是哪一把尺:分组、搜索、范围内没有栈、状态档 ——
           空态只收这一片,上面的筛选段/搜索框/视图切换全留着,用户随时能换一把尺。 -->
      <EmptyState
        v-if="filteredToEmpty"
        :title="t('containers.groupEmptyTitle')"
        :description="t('containers.groupEmpty')"
      />
      <EmptyState
        v-else-if="searchToEmpty"
        :title="t('containers.searchEmptyTitle')"
        :description="t('containers.searchEmpty', { q: searchText.trim() })"
      />
      <EmptyState
        v-else-if="viewMode === STACK_VIEW && stacksEmpty"
        :title="t('containers.stackEmptyTitle')"
        :description="t('containers.stackEmpty')"
      />
      <EmptyState
        v-else-if="viewMode === STACK_VIEW && bucketToEmpty"
        :title="t('containers.stackBucketEmptyTitle')"
        :description="t('containers.stackBucketEmpty')"
      />
      <!-- 按 Stack:范围内压根没有容器(机器都摊着但每张是空的),或状态档把栈收光了 —— 后两档与「搜没了」是不同空态 -->
      <section v-else-if="viewMode === HOST_VIEW" class="cards" :aria-label="t('containers.cardsAria')">
        <ServerCard
          v-for="g in displayGroups"
          :key="g.serverId"
          :group="g"
          :name="serverName(g.serverId)"
          :host="serverHost(g.serverId)"
          :owner-group="hostGroupTag(g.serverId)"
          :state-filter="stateFilter"
          :search="searchText"
          :page-size="pageSize"
          :bulk-mode="bulkMode"
          :selected-set="selected"
          :folded-set="folded"
          :tab-focus="tabFocusFor(g.serverId)"
          @toggle-fold="toggleFold(g.serverId)"
          @toggle-select="(name) => toggleSelect(g.serverId, name)"
          @view-container="(name) => focusContainerTab(g.serverId, name)"
          @changed="load"
          @logs="(c) => openLogs(g.serverId, c)"
          @terminal="(c) => openTerminal(g.serverId, c)"
          @diagnose="(c) => openDiagnose(g.serverId, c)"
          @inspect="(c) => openInspect(g.serverId, c)"
        />
      </section>

      <!-- 按 Stack:一条 compose 项目一张卡,同名跨主机合成一条;行上直接标所在机器,批量勾选照旧跨机器累加 -->
      <section v-else class="cards" :aria-label="t('containers.stackCardsAria')">
        <StackCard
          v-for="s in displayStacks"
          :key="s.key"
          :stack="s"
          :host-views="hostViews"
          :state-filter="stateFilter"
          :search="searchText"
          :page-size="pageSize"
          :bulk-mode="bulkMode"
          :selected-set="selected"
          :folded-set="foldedStacks"
          @toggle-fold="toggleFoldStack(s.key)"
          @toggle-select="toggleSelect"
          @view-container="focusContainerTab"
          @changed="load"
          @logs="(serverId, c) => openLogs(serverId, c)"
          @terminal="(serverId, c) => openTerminal(serverId, c)"
          @diagnose="(serverId, c) => openDiagnose(serverId, c)"
          @inspect="(serverId, c) => openInspect(serverId, c)"
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
  flex-wrap: wrap;
  gap: 8px;
}
/* 视图切换(按主机 / 按 Stack):同一份数据的两种切法,放在同一行展示控件的最前 */
.view-switch {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
/* 分组下拉与同一行的搜索框同高同圆角:一行里两种形状会比多一行还乱 */
.group-filter {
  position: relative;
  flex-shrink: 0;
  width: 170px;
}
.group-filter__select {
  width: 100%;
  padding: 7px 30px 7px 13px;
  font-family: inherit;
  font-size: var(--text-label);
  color: var(--color-text);
  background: var(--color-card);
  border: 1px solid var(--color-border);
  border-radius: 999px;
  appearance: none;
  cursor: pointer;
  transition: border-color var(--duration-fast) var(--ease-out-expo);
}
.group-filter__select:focus-visible {
  outline: none;
  border-color: var(--color-primary);
}
.group-filter__arrow {
  position: absolute;
  right: 12px;
  top: 50%;
  transform: translateY(-50%);
  color: var(--color-faint);
  pointer-events: none;
}
/* 每页条数:留原生下拉箭头,但收进一颗胶囊,免得右侧一长串控件里再长出第二个框 */
.page-size {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  flex-shrink: 0;
  padding: 5px 11px;
  border: 1px solid var(--color-border);
  border-radius: 999px;
  background: var(--color-card);
  font-size: var(--text-label);
  color: var(--color-faint);
  white-space: nowrap;
}
.page-size__select {
  font-family: inherit;
  font-size: var(--text-label);
  color: var(--color-text);
  background: transparent;
  border: none;
  padding-right: 2px;
  cursor: pointer;
}
.page-size__select:focus-visible {
  outline: none;
  color: var(--color-primary);
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
