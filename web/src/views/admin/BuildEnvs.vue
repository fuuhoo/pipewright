<script setup lang="ts">
/**
 * v6.2 构建环境管理(admin-only)。
 *
 * 覆盖 §3.1 / §3.2:
 *   - CRUD(language/version/display_name/description/image/credential/sort_order)
 *   - 语言只给下拉:已添加的语言 + 自定义新增 + 其他(不绑定语言)
 *   - 镜像来源 official(官方短名)/ custom(任意地址,系统不拼接 — R8)
 *   - P0 #4 三态:unchecked 拒启用、unavailable 强制禁用、available 放行
 *   - 手动检查 / 手动拉取 / 一键检查 / 多选或全选批量检查
 *   - 批量新增(共用字段 × 版本清单展开成多行)/ 批量删除所选(逐行结果,坏行不挡好行)
 *   - 列表排序:默认(排序值)/ 名称 / 创建时间三档,后两档可翻方向,选择留本地
 *   - 整表导出(可选格式与是否含禁用)+ 导入(先预览,跳过/覆盖两种冲突策略)
 *
 * 权限:路由 meta.adminOnly + AppShell 菜单 role 隔离双保险。
 */
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  listBuildEnvs,
  createBuildEnv,
  updateBuildEnv,
  deleteBuildEnv,
  toggleBuildEnv,
  checkBuildEnv,
  pullBuildEnv,
  checkAllBuildEnvs,
  checkBuildEnvsBatch,
  batchCreateBuildEnvs,
  deleteBuildEnvsBatch,
  exportBuildEnvText,
  importBuildEnvs,
} from '../../api/buildEnvs'
import type {
  BatchReport,
  BuildEnv,
  BuildEnvInput,
  ImageCheckStatus,
  ImportMode,
  ImportReport,
  TransferFormat,
} from '../../api/buildEnvs'
import {
  batchActionLabel,
  batchRowLabel,
  emptyBatchAddForm,
  expandBatchForm,
  batchAddIssues,
  formatBatchSummary,
  DEFAULT_NAME_TEMPLATE,
  EXAMPLE_IMAGE_TEMPLATE,
  MAX_BATCH_ROWS,
  type BatchAddForm,
} from '../../lib/buildEnvBatch'
import {
  batchCheckLabel,
  buildEnvExportFilename,
  detectTransferFormat,
  exceedsImportLimit,
  formatImportSummary,
  importActionLabel,
  importModeLabel,
} from '../../lib/buildEnvTransfer'
import {
  dirEnabled,
  loadSort,
  nextSort,
  saveSort,
  sortBuildEnvs,
  type BuildEnvSort,
  type BuildEnvSortKey,
} from '../../lib/buildEnvSort'
import { languagesFrom } from '../../lib/languageOptions'
import LanguageSelect from '../../components/ui/LanguageSelect.vue'
import { listCredentials, usableCredentials } from '../../api/credentials'
import type { Credential } from '../../api/credentials'
import { HttpError } from '../../api/http'

const { t } = useI18n()

// ─── state ──────────────────────────────────────────────────────────────────

type LoadState = 'idle' | 'loading' | 'error'

const loadState = ref<LoadState>('idle')
const loadError = ref('')
const envs = ref<BuildEnv[]>([])

// 可绑定的镜像仓库拉取凭据(registry / git_http 类)。
const registryCredentials = ref<Credential[]>([])

// ─── form modal ─────────────────────────────────────────────────────────────

const modalOpen = ref(false)
const editingId = ref<string | null>(null)
const formSubmitting = ref(false)
const formBanner = ref('')

const emptyForm = (): BuildEnvInput => ({
  language: '',
  version: '',
  displayName: '',
  description: '',
  sourceType: 'official',
  image: '',
  credentialId: '',
  sortOrder: 0,
  enabled: false,
})

const form = ref<BuildEnvInput>(emptyForm())

function openAdd(): void {
  editingId.value = null
  form.value = emptyForm()
  formBanner.value = ''
  modalOpen.value = true
}

function openEdit(env: BuildEnv): void {
  editingId.value = env.id
  form.value = {
    language: env.language,
    version: env.version,
    displayName: env.displayName,
    description: env.description,
    sourceType: env.sourceType,
    image: env.image,
    credentialId: env.credentialId,
    sortOrder: env.sortOrder,
    // 编辑时不带 enabled —— 启用状态由 toggle 单独管(三态校验在那里)。
    enabled: env.enabled,
  }
  formBanner.value = ''
  modalOpen.value = true
}

// ─── delete confirm ─────────────────────────────────────────────────────────

const deleteOpen = ref(false)
const deleting = ref<BuildEnv | null>(null)
const deleteSubmitting = ref(false)
const deleteBanner = ref('')

function openDelete(env: BuildEnv): void {
  deleting.value = env
  deleteBanner.value = ''
  deleteOpen.value = true
}

// ─── per-row async actions ──────────────────────────────────────────────────

const busyId = ref<string | null>(null)
const rowBanner = ref('')

// ─── checking 轮询(后端 pull 异步化:状态先置 checking,完成后落终态) ───

const POLL_INTERVAL_MS = 3000
const POLL_SETTLE_TIMEOUT_MS = 300_000
// 静默轮询上限:约 10 分钟;进程崩溃遗留的 checking 状态不会让轮询永动。
const POLL_MAX_TICKS = 200

let pollTimer: number | null = null
let pollTicks = 0
const pullWaiters = new Map<string, (status: ImageCheckStatus, error: string) => void>()
const pullingIds = ref<string[]>([])

function hasChecking(): boolean {
  return envs.value.some((e) => e.imageCheckStatus === 'checking')
}

async function silentRefresh(): Promise<void> {
  try {
    envs.value = await listBuildEnvs({ includeDisabled: true })
  } catch {
    return // 网络瞬断:本轮放弃,下一轮再试
  }
  for (const e of envs.value) {
    if (e.imageCheckStatus === 'checking') continue
    const waiter = pullWaiters.get(e.id)
    if (waiter) waiter(e.imageCheckStatus, e.imageCheckError)
  }
  if (pullWaiters.size === 0 && (!hasChecking() || ++pollTicks >= POLL_MAX_TICKS)) stopPolling()
}

function startPolling(): void {
  if (pollTimer !== null) return
  pollTicks = 0
  pollTimer = window.setInterval(() => void silentRefresh(), POLL_INTERVAL_MS)
}

function stopPolling(): void {
  if (pollTimer !== null) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

/** 等待某行离开 checking 状态(超时则返回当前状态)。 */
function waitSettled(id: string): Promise<{ status: ImageCheckStatus; error: string }> {
  return new Promise((resolve) => {
    let done = false
    const finish = (status: ImageCheckStatus, error: string): void => {
      if (done) return
      done = true
      pullWaiters.delete(id)
      resolve({ status, error })
    }
    pullWaiters.set(id, finish)
    window.setTimeout(() => {
      const row = envs.value.find((e) => e.id === id)
      finish(row?.imageCheckStatus ?? 'checking', row?.imageCheckError ?? '')
    }, POLL_SETTLE_TIMEOUT_MS)
  })
}

onUnmounted(stopPolling)

// ─── derived ────────────────────────────────────────────────────────────────

const statusLabel = (s: ImageCheckStatus): string => t(`buildEnvs.status${cap(s)}`)

function cap(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1)
}

// ─── 排序 ───────────────────────────────────────────────────────────────────
// 表格里渲染的是这份派生视图;envs 始终保持后端权威顺序(批量检查、导出计数都读它)。

const sort = ref<BuildEnvSort>(loadSort())
const sortedEnvs = computed(() => sortBuildEnvs(envs.value, sort.value))

// 语言下拉的候选:主列表(内置 + 手工添加,含禁用行)里出现过的语言。
const knownLanguages = computed(() => languagesFrom(envs.value))

const SORT_KEYS: BuildEnvSortKey[] = ['order', 'name', 'created']
const sortLabel = (k: BuildEnvSortKey): string => t(`buildEnvs.sort${cap(k)}`)

function pickSort(key: BuildEnvSortKey): void {
  sort.value = nextSort(sort.value, key)
  saveSort(sort.value)
}

function flipSortDir(): void {
  if (!dirEnabled(sort.value)) return
  sort.value = { ...sort.value, dir: sort.value.dir === 'asc' ? 'desc' : 'asc' }
  saveSort(sort.value)
}

// ─── data loading ───────────────────────────────────────────────────────────

async function load(): Promise<void> {
  loadState.value = 'loading'
  loadError.value = ''
  try {
    // 主列表是权威内容;凭据下拉只是可选项(仅 custom 镜像需要),单独加载且允许失败
    // —— vault 未配置(master key 缺失)时不应让整页变成错误态。
    envs.value = await listBuildEnvs({ includeDisabled: true })
    // 行可能被别的会话删掉了:勾选集要跟权威列表对齐,否则「检查所选」会带上不存在的 id。
    selectedIds.value = selectedIds.value.filter((id) => envs.value.some((e) => e.id === id))
    loadState.value = 'idle'
  } catch (err) {
    loadError.value = errMsg(err, 'buildEnvs.errLoad')
    loadState.value = 'error'
    return
  }
  try {
    const creds = await listCredentials()
    registryCredentials.value = usableCredentials(creds).filter(
      (c) => c.type === 'registry' || c.type === 'git_http',
    )
  } catch {
    // 凭据不可用(未配置 master key / 网络故障):下拉为空,用户仍可管理镜像与启停。
    registryCredentials.value = []
  }
}

onMounted(() => {
  void load().then(() => {
    // 页面重载时若仍有拉取/检查进行中,恢复轮询直到落定。
    if (hasChecking()) startPolling()
  })
})

// ─── helpers ────────────────────────────────────────────────────────────────

function errMsg(err: unknown, fallbackKey: string): string {
  if (err instanceof HttpError) {
    if (err.status === 0) return t('buildEnvs.errLoadConn')
    // 领域错误码已人读化(后端 i18n),优先用服务端消息。
    return err.apiError?.message ?? t(fallbackKey)
  }
  return t(fallbackKey)
}

function fmtTime(iso: string | null): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString()
}

// ─── actions ────────────────────────────────────────────────────────────────

async function submitForm(): Promise<void> {
  formSubmitting.value = true
  formBanner.value = ''
  try {
    if (editingId.value) {
      await updateBuildEnv(editingId.value, form.value)
    } else {
      await createBuildEnv(form.value)
    }
    modalOpen.value = false
    await load()
  } catch (err) {
    formBanner.value = errMsg(err, 'buildEnvs.errSave')
  } finally {
    formSubmitting.value = false
  }
}

async function confirmDelete(): Promise<void> {
  if (!deleting.value) return
  deleteSubmitting.value = true
  deleteBanner.value = ''
  try {
    await deleteBuildEnv(deleting.value.id)
    deleteOpen.value = false
    await load()
  } catch (err) {
    deleteBanner.value = errMsg(err, 'buildEnvs.errDelete')
  } finally {
    deleteSubmitting.value = false
  }
}

async function onToggle(env: BuildEnv, enabled: boolean): Promise<void> {
  busyId.value = env.id
  rowBanner.value = ''
  try {
    await toggleBuildEnv(env.id, enabled)
    await load()
  } catch (err) {
    // P0 #4 拒绝(409)时服务端消息已说明原因。
    rowBanner.value = errMsg(err, 'buildEnvs.errSave')
  } finally {
    busyId.value = null
  }
}

async function onCheck(env: BuildEnv): Promise<void> {
  busyId.value = env.id
  rowBanner.value = ''
  try {
    const res = await checkBuildEnv(env.id)
    rowBanner.value =
      res.status === 'available'
        ? t('buildEnvs.checkOk')
        : res.status === 'pullable'
          ? t('buildEnvs.checkPullable')
          : t('buildEnvs.checkFailed', { error: res.error || res.status })
    await load()
  } catch (err) {
    rowBanner.value = errMsg(err, 'buildEnvs.errCheck')
  } finally {
    busyId.value = null
  }
}

async function onPull(env: BuildEnv): Promise<void> {
  busyId.value = env.id
  rowBanner.value = ''
  try {
    // 后端异步受理:状态立即置 checking,docker pull 在后台跑。
    await pullBuildEnv(env.id)
    env.imageCheckStatus = 'checking'
    pullingIds.value = [...pullingIds.value, env.id]
    startPolling()
    const res = await waitSettled(env.id)
    rowBanner.value =
      res.status === 'available'
        ? t('buildEnvs.pullOk')
        : t('buildEnvs.pullFailed', { error: res.error || res.status })
  } catch (err) {
    // 409 = 已有拉取在排队/进行中,服务端消息已说明原因。
    rowBanner.value = errMsg(err, 'buildEnvs.errPull')
  } finally {
    pullingIds.value = pullingIds.value.filter((id) => id !== env.id)
    await silentRefresh()
    busyId.value = null
  }
}

const checkingAll = ref(false)

async function onCheckAll(): Promise<void> {
  checkingAll.value = true
  rowBanner.value = ''
  // 后端同步跑完才返回;请求期间按钮禁用防重复点击,行状态先置「检查中」给出反馈。
  for (const e of envs.value) e.imageCheckStatus = 'checking'
  try {
    const res = await checkAllBuildEnvs()
    rowBanner.value = t('buildEnvs.checkAllDone', { ok: res.ok, total: res.total })
  } catch (err) {
    rowBanner.value = errMsg(err, 'buildEnvs.errCheckAll')
  } finally {
    await load()
    checkingAll.value = false
  }
}

// ─── 多选 + 批量检查 ────────────────────────────────────────────────────────

const selectedIds = ref<string[]>([])

const allSelected = computed(
  () => envs.value.length > 0 && selectedIds.value.length === envs.value.length,
)

function toggleSelected(id: string, checked: boolean): void {
  const next = new Set(selectedIds.value)
  if (checked) next.add(id)
  else next.delete(id)
  selectedIds.value = [...next]
}

function toggleSelectAll(checked: boolean): void {
  selectedIds.value = checked ? envs.value.map((e) => e.id) : []
}

const checkingSelected = ref(false)

async function onCheckSelected(): Promise<void> {
  const ids = [...selectedIds.value]
  if (ids.length === 0) return
  checkingSelected.value = true
  rowBanner.value = ''
  // 与「检查全部」同样的即时反馈:先把勾选行置「检查中」,后端返回后按行落定。
  for (const e of envs.value) {
    if (ids.includes(e.id)) e.imageCheckStatus = 'checking'
  }
  try {
    const res = await checkBuildEnvsBatch(ids)
    rowBanner.value = batchCheckLabel(res.ok, res.total)
  } catch (err) {
    rowBanner.value = errMsg(err, 'buildEnvs.errCheckSelected')
  } finally {
    await load()
    checkingSelected.value = false
  }
}

// ─── 批量删除(复用上面的多选/全选) ────────────────────────────────────────

const batchDeleteOpen = ref(false)
const batchDeleting = ref(false)
const batchDeleteBanner = ref('')
const batchDeleteReport = ref<BatchReport | null>(null)

/** 勾选到的行(按列表权威顺序),确认弹窗要照着名字让人过一遍。 */
const selectedEnvs = computed(() => envs.value.filter((e) => selectedIds.value.includes(e.id)))

function openBatchDelete(): void {
  if (selectedIds.value.length === 0) return
  batchDeleteReport.value = null
  batchDeleteBanner.value = ''
  batchDeleteOpen.value = true
}

async function confirmBatchDelete(): Promise<void> {
  const ids = [...selectedIds.value]
  if (ids.length === 0) return
  batchDeleting.value = true
  batchDeleteBanner.value = ''
  try {
    const report = await deleteBuildEnvsBatch(ids)
    batchDeleteReport.value = report
    await load()
    if (report.summary.failed === 0) {
      // 全批落定:收弹窗、清勾选,汇总回到页头横幅。
      batchDeleteOpen.value = false
      selectedIds.value = []
      rowBanner.value = formatBatchSummary('delete', report.summary)
    } else {
      // 有行没删掉:留着弹窗逐行看原因,勾选取当前列表再收一遍。
      batchDeleteBanner.value = formatBatchSummary('delete', report.summary)
    }
  } catch (err) {
    batchDeleteBanner.value = errMsg(err, 'buildEnvs.errBatchDelete')
  } finally {
    batchDeleting.value = false
  }
}

// ─── 批量新增(一份共享字段 × 一串版本 = 若干行) ──────────────────────────

const batchAddOpen = ref(false)
const batchAddBusy = ref(false)
const batchAddBanner = ref('')
const batchAddReport = ref<BatchReport | null>(null)
const batchForm = ref<BatchAddForm>(emptyBatchAddForm())

/** 用户还没碰过表单时不铺一堆红色提示。 */
const batchAddTouched = computed(
  () => batchForm.value.language.trim() !== '' || batchForm.value.versions.trim() !== '',
)
const batchAddRows = computed(() => expandBatchForm(batchForm.value))
const batchAddBlockers = computed(() =>
  batchAddTouched.value ? batchAddIssues(batchForm.value) : [],
)

const batchIssueText = (k: string): string =>
  t(`buildEnvs.batchIssue${cap(k)}`, { max: MAX_BATCH_ROWS })

function openBatchAdd(): void {
  batchForm.value = emptyBatchAddForm()
  batchAddReport.value = null
  batchAddBanner.value = ''
  batchAddOpen.value = true
}

async function submitBatchAdd(): Promise<void> {
  const issues = batchAddIssues(batchForm.value)
  if (issues.length > 0) {
    batchAddBanner.value = issues.map(batchIssueText).join('；')
    return
  }
  batchAddBusy.value = true
  batchAddBanner.value = ''
  try {
    batchAddReport.value = await batchCreateBuildEnvs(batchAddRows.value)
    await load()
    batchAddBanner.value = formatBatchSummary('create', batchAddReport.value.summary)
  } catch (err) {
    batchAddBanner.value = errMsg(err, 'buildEnvs.errBatchCreate')
    batchAddReport.value = null
  } finally {
    batchAddBusy.value = false
  }
}

// ─── 导出 ───────────────────────────────────────────────────────────────────

const exportFormat = ref<TransferFormat>('yaml')
const exportIncludeDisabled = ref(true)
const exporting = ref(false)

/** 造一个临时链接把文本交给浏览器下载(后端用 text/plain 下发,拿到的就是文件原文)。 */
function downloadTextFile(filename: string, content: string): void {
  const url = URL.createObjectURL(new Blob([content], { type: 'text/plain;charset=utf-8' }))
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}

async function onExport(): Promise<void> {
  exporting.value = true
  rowBanner.value = ''
  try {
    const text = await exportBuildEnvText({
      format: exportFormat.value,
      includeDisabled: exportIncludeDisabled.value,
    })
    const filename = buildEnvExportFilename(exportFormat.value, new Date())
    downloadTextFile(filename, text)
    // 条数按当前列表口径算(页面就是这份数据的权威视图),不去解析刚生成的文本。
    const n = envs.value.filter((e) => exportIncludeDisabled.value || e.enabled).length
    rowBanner.value = t('buildEnvs.exportDone', { n, filename })
  } catch (err) {
    rowBanner.value = errMsg(err, 'buildEnvs.errExport')
  } finally {
    exporting.value = false
  }
}

// ─── 导入 ───────────────────────────────────────────────────────────────────

const importOpen = ref(false)
const importContent = ref('')
const importFormat = ref<TransferFormat>('yaml')
const importMode = ref<ImportMode>('skip')
const importBusy = ref(false)
const importBanner = ref('')
const importReport = ref<ImportReport | null>(null)

function openImport(): void {
  importContent.value = ''
  importFormat.value = 'yaml'
  importMode.value = 'skip'
  importReport.value = null
  importBanner.value = ''
  importOpen.value = true
}

async function onPickImportFile(ev: Event): Promise<void> {
  const input = ev.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file) return
  importFormat.value = detectTransferFormat(file.name)
  importBanner.value = ''
  importContent.value = await file.text()
  // 上限与后端 MaxBytesReader 一致:超了先在本地说清,别等 400 的含糊报错。
  if (exceedsImportLimit(importContent.value)) {
    importBanner.value = t('buildEnvs.importTooLarge')
  }
  input.value = ''
}

/** 跑一次导入;dryRun=true 只取预览(与后端约定一致,一行都不写)。 */
async function runImport(dryRun: boolean): Promise<void> {
  if (!importContent.value.trim()) {
    importBanner.value = t('buildEnvs.importEmpty')
    return
  }
  if (exceedsImportLimit(importContent.value)) {
    importBanner.value = t('buildEnvs.importTooLarge')
    return
  }
  importBusy.value = true
  importBanner.value = ''
  try {
    importReport.value = await importBuildEnvs({
      content: importContent.value,
      format: importFormat.value,
      mode: importMode.value,
      dryRun,
    })
    if (!dryRun) {
      await load()
      // 行可能已被覆盖或删除,选中的 id 要跟着实际列表收一遍。
      selectedIds.value = selectedIds.value.filter((id) => envs.value.some((e) => e.id === id))
    }
  } catch (err) {
    importBanner.value = errMsg(err, 'buildEnvs.errImport')
    importReport.value = null
  } finally {
    importBusy.value = false
  }
}
</script>

<template>
  <div class="buildenvs-view">
    <header class="view-header">
      <div>
        <h1 class="view-title">{{ t('buildEnvs.title') }}</h1>
        <p class="view-sub">{{ t('buildEnvs.desc') }}</p>
      </div>
      <div class="header-actions">
        <div class="opt-group">
          <label class="field field--inline">
            <span class="sr-only">{{ t('buildEnvs.exportFormat') }}</span>
            <select v-model="exportFormat">
              <option value="yaml">YAML</option>
              <option value="json">JSON</option>
            </select>
          </label>
          <label class="opt">
            <input v-model="exportIncludeDisabled" type="checkbox" />
            <span>{{ t('buildEnvs.exportIncludeDisabled') }}</span>
          </label>
          <button class="btn" :disabled="exporting" @click="onExport">
            {{ exporting ? t('buildEnvs.exporting') : t('buildEnvs.export') }}
          </button>
        </div>
        <button class="btn" @click="openImport">{{ t('buildEnvs.import') }}</button>
        <!-- 勾选后才会做的事成组放:与导出组同款边框,表示这两颗按钮共用同一批行 -->
        <div class="opt-group">
          <button
            class="btn"
            :disabled="checkingSelected || selectedIds.length === 0"
            @click="onCheckSelected"
          >
            {{
              checkingSelected
                ? t('buildEnvs.checking')
                : t('buildEnvs.checkSelected', { n: selectedIds.length })
            }}
          </button>
          <button
            class="btn btn--danger"
            :disabled="batchDeleting || selectedIds.length === 0"
            @click="openBatchDelete"
          >
            {{ t('buildEnvs.deleteSelected', { n: selectedIds.length }) }}
          </button>
        </div>
        <button class="btn" :disabled="checkingAll" @click="onCheckAll">
          {{ checkingAll ? t('buildEnvs.checking') : t('buildEnvs.checkAll') }}
        </button>
        <button class="btn" @click="openBatchAdd">{{ t('buildEnvs.batchAdd') }}</button>
        <button class="btn btn--primary" @click="openAdd">+ {{ t('buildEnvs.add') }}</button>
      </div>
    </header>

    <p v-if="rowBanner" class="banner" role="status">{{ rowBanner }}</p>

    <!-- 排序档单独占一行:放进页头会把标题挤成竖排字(629px 窄窗口实测) -->
    <div v-if="envs.length > 0" class="sort-bar">
      <div class="seg" role="group" :aria-label="t('buildEnvs.sortAria')">
        <button
          v-for="k in SORT_KEYS"
          :key="k"
          class="seg__btn"
          :class="{ 'seg__btn--on': sort.key === k }"
          :aria-pressed="sort.key === k"
          @click="pickSort(k)"
        >
          {{ sortLabel(k) }}
        </button>
      </div>
      <button
        class="btn btn--sm"
        :disabled="!dirEnabled(sort)"
        :title="t('buildEnvs.sortDirHint')"
        @click="flipSortDir"
      >
        {{ sort.dir === 'asc' ? t('buildEnvs.sortDirAsc') : t('buildEnvs.sortDirDesc') }}
      </button>
    </div>

    <!-- loading / error / empty -->
    <p v-if="loadState === 'loading'" class="state">{{ t('common.refresh') }}…</p>
    <div v-else-if="loadState === 'error'" class="state state--error">
      <p>{{ loadError }}</p>
      <button class="btn" @click="load">{{ t('buildEnvs.save') }}</button>
    </div>
    <p v-else-if="envs.length === 0" class="state">{{ t('buildEnvs.empty') }}</p>

    <table v-else class="grid">
      <thead>
        <tr>
          <th class="col-check">
            <input
              type="checkbox"
              :checked="allSelected"
              :aria-label="t('buildEnvs.selectAll')"
              @change="toggleSelectAll(($event.target as HTMLInputElement).checked)"
            />
          </th>
          <th>{{ t('buildEnvs.colEnv') }}</th>
          <th>{{ t('buildEnvs.colImage') }}</th>
          <th>{{ t('buildEnvs.colStatus') }}</th>
          <th>{{ t('buildEnvs.colEnabled') }}</th>
          <th>{{ t('buildEnvs.colCreated') }}</th>
          <th>{{ t('buildEnvs.colActions') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="e in sortedEnvs" :key="e.id">
          <td class="col-check">
            <input
              type="checkbox"
              :checked="selectedIds.includes(e.id)"
              :aria-label="e.displayName"
              @change="toggleSelected(e.id, ($event.target as HTMLInputElement).checked)"
            />
          </td>
          <td>
            <div class="cell-strong">{{ e.displayName }}</div>
            <div class="cell-dim">
              {{ e.language }} / {{ e.version }}
            </div>
          </td>
          <td>
            <code class="mono">{{ e.image }}</code>
            <div v-if="e.credentialId" class="cell-dim">
              {{ t('buildEnvs.fieldCredential') }}
            </div>
          </td>
          <td>
            <span class="pill" :class="`pill--${e.imageCheckStatus}`">
              {{ statusLabel(e.imageCheckStatus) }}
            </span>
            <div v-if="e.imageCheckError" class="cell-dim cell--err" :title="e.imageCheckError">
              {{ e.imageCheckError }}
            </div>
            <div v-else-if="e.imageCheckedAt" class="cell-dim">
              {{ fmtTime(e.imageCheckedAt) }}
            </div>
            <div v-else class="cell-dim">{{ t('buildEnvs.statusHintUnchecked') }}</div>
          </td>
          <td>
            <label class="switch">
              <input
                type="checkbox"
                :checked="e.enabled"
                :disabled="busyId === e.id"
                @change="onToggle(e, ($event.target as HTMLInputElement).checked)"
              />
              <span>{{ e.enabled ? t('buildEnvs.enable') : t('buildEnvs.disable') }}</span>
            </label>
          </td>
          <td class="cell-dim nowrap">{{ fmtTime(e.createdAt) }}</td>
          <td>
            <div class="actions">
              <button
                class="btn btn--sm btn--state"
                :disabled="busyId === e.id || e.imageCheckStatus === 'checking'"
                @click="onCheck(e)"
              >
                {{ busyId === e.id ? t('buildEnvs.checking') : t('buildEnvs.check') }}
              </button>
              <button
                class="btn btn--sm btn--state"
                :disabled="busyId === e.id || e.imageCheckStatus === 'checking'"
                @click="onPull(e)"
              >
                {{ pullingIds.includes(e.id) ? t('buildEnvs.pulling') : t('buildEnvs.pull') }}
              </button>
              <button class="btn btn--sm" @click="openEdit(e)">{{ t('buildEnvs.editAction') }}</button>
              <button class="btn btn--sm btn--danger" @click="openDelete(e)">
                {{ t('buildEnvs.delete') }}
              </button>
            </div>
          </td>
        </tr>
      </tbody>
    </table>

    <!-- ── add / edit modal ── -->
    <div v-if="modalOpen" class="modal-mask" @click.self="modalOpen = false">
      <div class="modal" role="dialog" :aria-label="t('buildEnvs.create')">
        <h2 class="modal-title">
          {{ editingId ? t('buildEnvs.edit') : t('buildEnvs.create') }}
        </h2>

        <div class="form-grid">
          <label class="field">
            <span>{{ t('buildEnvs.fieldLanguage') }}</span>
            <LanguageSelect v-model="form.language" :known="knownLanguages" />
          </label>
          <label class="field">
            <span>{{ t('buildEnvs.fieldVersion') }}</span>
            <input v-model="form.version" type="text" placeholder="20" />
          </label>
          <label class="field field--wide">
            <span>{{ t('buildEnvs.fieldDisplayName') }}</span>
            <input v-model="form.displayName" type="text" placeholder="Node.js 20" />
          </label>
          <label class="field field--wide">
            <span>{{ t('buildEnvs.fieldDescription') }}</span>
            <input v-model="form.description" type="text" />
          </label>

          <label class="field">
            <span>{{ t('buildEnvs.fieldSourceType') }}</span>
            <select v-model="form.sourceType">
              <option value="official">{{ t('buildEnvs.sourceOfficial') }}</option>
              <option value="custom">{{ t('buildEnvs.sourceCustom') }}</option>
            </select>
          </label>
          <label class="field">
            <span>{{ t('buildEnvs.fieldSortOrder') }}</span>
            <input v-model.number="form.sortOrder" type="number" />
          </label>

          <label class="field field--wide">
            <span>{{ t('buildEnvs.fieldImage') }}</span>
            <input
              v-model="form.image"
              type="text"
              :placeholder="t('buildEnvs.imagePlaceholder')"
            />
            <small>{{ t('buildEnvs.imageHint') }}</small>
          </label>

          <label class="field field--wide">
            <span>{{ t('buildEnvs.fieldCredential') }}</span>
            <select v-model="form.credentialId">
              <option value="">{{ t('buildEnvs.noCredential') }}</option>
              <option v-for="c in registryCredentials" :key="c.id" :value="c.id">
                {{ c.name }}
              </option>
            </select>
          </label>
        </div>

        <p v-if="formBanner" class="banner banner--err">{{ formBanner }}</p>

        <footer class="modal-actions">
          <button class="btn" @click="modalOpen = false">{{ t('buildEnvs.cancel') }}</button>
          <button class="btn btn--primary" :disabled="formSubmitting" @click="submitForm">
            {{ formSubmitting ? t('buildEnvs.saving') : t('buildEnvs.save') }}
          </button>
        </footer>
      </div>
    </div>

    <!-- ── delete confirm ── -->
    <div v-if="deleteOpen" class="modal-mask" @click.self="deleteOpen = false">
      <div class="modal modal--sm" role="dialog">
        <h2 class="modal-title">{{ t('buildEnvs.confirmDelete') }}</h2>
        <p class="modal-body">
          <strong>{{ deleting?.displayName }}</strong>
          ({{ deleting?.language }}/{{ deleting?.version }})
        </p>
        <p v-if="deleteBanner" class="banner banner--err">{{ deleteBanner }}</p>
        <footer class="modal-actions">
          <button class="btn" @click="deleteOpen = false">{{ t('buildEnvs.cancel') }}</button>
          <button
            class="btn btn--danger"
            :disabled="deleteSubmitting"
            @click="confirmDelete"
          >
            {{ t('buildEnvs.delete') }}
          </button>
        </footer>
      </div>
    </div>

    <!-- ── batch add ── -->
    <div v-if="batchAddOpen" class="modal-mask" @click.self="batchAddOpen = false">
      <div class="modal" role="dialog" :aria-label="t('buildEnvs.batchAddTitle')">
        <h2 class="modal-title">{{ t('buildEnvs.batchAddTitle') }}</h2>
        <p class="modal-body">{{ t('buildEnvs.batchAddDesc') }}</p>

        <div class="form-grid">
          <label class="field">
            <span>{{ t('buildEnvs.fieldLanguage') }}</span>
            <LanguageSelect v-model="batchForm.language" :known="knownLanguages" />
          </label>
          <label class="field">
            <span>{{ t('buildEnvs.fieldSourceType') }}</span>
            <select v-model="batchForm.sourceType">
              <option value="official">{{ t('buildEnvs.sourceOfficial') }}</option>
              <option value="custom">{{ t('buildEnvs.sourceCustom') }}</option>
            </select>
          </label>

          <label class="field field--wide">
            <span>{{ t('buildEnvs.versions') }}</span>
            <textarea
              v-model="batchForm.versions"
              class="mono-area"
              rows="5"
              spellcheck="false"
              :placeholder="t('buildEnvs.versionsPlaceholder')"
            ></textarea>
            <small>{{ t('buildEnvs.versionsHint', { max: MAX_BATCH_ROWS }) }}</small>
          </label>

          <label class="field field--wide">
            <span>{{ t('buildEnvs.nameTemplate') }}</span>
            <input
              v-model="batchForm.displayNameTemplate"
              type="text"
              :placeholder="DEFAULT_NAME_TEMPLATE"
            />
            <!-- 占位符原样展示走代码常量:文案里的花括号会被 vue-i18n 当插值吃掉 -->
            <small>{{ t('buildEnvs.nameTemplateHint') }} <code class="mono">{{ DEFAULT_NAME_TEMPLATE }}</code></small>
          </label>

          <label class="field field--wide">
            <span>{{ t('buildEnvs.imageTemplate') }}</span>
            <input
              v-model="batchForm.imageTemplate"
              type="text"
              :placeholder="EXAMPLE_IMAGE_TEMPLATE"
            />
            <small>{{ t('buildEnvs.imageTemplateHint') }} <code class="mono">{{ DEFAULT_NAME_TEMPLATE }}</code></small>
          </label>

          <label class="field field--wide">
            <span>{{ t('buildEnvs.fieldDescription') }}</span>
            <input v-model="batchForm.description" type="text" />
          </label>

          <label class="field">
            <span>{{ t('buildEnvs.fieldCredential') }}</span>
            <select v-model="batchForm.credentialId">
              <option value="">{{ t('buildEnvs.noCredential') }}</option>
              <option v-for="c in registryCredentials" :key="c.id" :value="c.id">
                {{ c.name }}
              </option>
            </select>
          </label>
          <label class="field">
            <span>{{ t('buildEnvs.sortOrderStart') }}</span>
            <input v-model.number="batchForm.sortOrder" type="number" />
            <small>{{ t('buildEnvs.sortOrderStartHint') }}</small>
          </label>

          <div class="field field--wide">
            <label class="opt">
              <input v-model="batchForm.enabled" type="checkbox" />
              <span>{{ t('buildEnvs.batchAddEnable') }}</span>
            </label>
            <small>{{ t('buildEnvs.batchAddEnableHint') }}</small>
          </div>
        </div>

        <p v-if="batchAddBlockers.length > 0" class="note note--warn">
          {{ batchAddBlockers.map(batchIssueText).join('；') }}
        </p>
        <p v-if="batchAddBanner" class="banner banner--err">{{ batchAddBanner }}</p>

        <div v-if="batchAddRows.length > 0" class="import-result">
          <p class="import-summary">{{ t('buildEnvs.batchPreview', { n: batchAddRows.length }) }}</p>
          <table class="grid grid--sm">
            <thead>
              <tr>
                <th>#</th>
                <th>{{ t('buildEnvs.colEnv') }}</th>
                <th>{{ t('buildEnvs.fieldDisplayName') }}</th>
                <th>{{ t('buildEnvs.colImage') }}</th>
                <th>{{ t('buildEnvs.colSort') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(r, i) in batchAddRows" :key="`${r.language}/${r.version}`">
                <td>{{ i + 1 }}</td>
                <td class="mono">{{ r.language }} / {{ r.version }}</td>
                <td>{{ r.displayName }}</td>
                <td class="mono cell--wrap">{{ r.image || '—' }}</td>
                <td>{{ r.sortOrder }}</td>
              </tr>
            </tbody>
          </table>
        </div>

        <div v-if="batchAddReport" class="import-result">
          <p class="import-summary">
            {{ formatBatchSummary('create', batchAddReport.summary) }}
          </p>
          <table class="grid grid--sm">
            <thead>
              <tr>
                <th>#</th>
                <th>{{ t('buildEnvs.colEnv') }}</th>
                <th>{{ t('buildEnvs.colAction') }}</th>
                <th>{{ t('buildEnvs.colEnabled') }}</th>
                <th>{{ t('buildEnvs.colReason') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="r in batchAddReport.results" :key="r.index">
                <td>{{ r.index + 1 }}</td>
                <td class="mono">{{ r.language }} / {{ r.version }}</td>
                <td>
                  <span class="pill" :class="`pill--${r.action}`">
                    {{ batchActionLabel(r.action) }}
                  </span>
                </td>
                <td>{{ r.enabled ? t('buildEnvs.enabledShort') : t('buildEnvs.disabledShort') }}</td>
                <td class="cell-dim cell--wrap">{{ r.reason || '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>

        <footer class="modal-actions">
          <button class="btn" @click="batchAddOpen = false">{{ t('buildEnvs.cancel') }}</button>
          <button
            class="btn btn--primary"
            :disabled="batchAddBusy || batchAddRows.length === 0"
            @click="submitBatchAdd"
          >
            {{
              batchAddBusy
                ? t('buildEnvs.batchWorking')
                : t('buildEnvs.batchAddConfirm', { n: batchAddRows.length })
            }}
          </button>
        </footer>
      </div>
    </div>

    <!-- ── batch delete confirm ── -->
    <div v-if="batchDeleteOpen" class="modal-mask" @click.self="batchDeleteOpen = false">
      <div class="modal modal--sm" role="dialog" :aria-label="t('buildEnvs.batchDeleteTitle')">
        <h2 class="modal-title">{{ t('buildEnvs.batchDeleteTitle') }}</h2>
        <p class="modal-body">{{ t('buildEnvs.batchDeleteCount', { n: selectedEnvs.length }) }}</p>

        <ul class="del-list">
          <li v-for="e in selectedEnvs" :key="e.id">
            <span class="cell-strong">{{ e.displayName }}</span>
            <span class="cell-dim mono">{{ batchRowLabel(e.language, e.version) }}</span>
            <span v-if="e.enabled" class="cell-dim">{{ t('buildEnvs.enabledShort') }}</span>
          </li>
        </ul>

        <p class="note note--warn">{{ t('buildEnvs.batchDeleteWarn') }}</p>

        <p v-if="batchDeleteBanner" class="banner banner--err">{{ batchDeleteBanner }}</p>

        <div v-if="batchDeleteReport" class="import-result">
          <p class="import-summary">
            {{ formatBatchSummary('delete', batchDeleteReport.summary) }}
          </p>
          <table class="grid grid--sm">
            <thead>
              <tr>
                <th>{{ t('buildEnvs.colEnv') }}</th>
                <th>{{ t('buildEnvs.colAction') }}</th>
                <th>{{ t('buildEnvs.colReason') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="r in batchDeleteReport.results" :key="r.index">
                <td class="mono">{{ r.language }} / {{ r.version }}</td>
                <td>
                  <span class="pill" :class="`pill--${r.action}`">
                    {{ batchActionLabel(r.action) }}
                  </span>
                </td>
                <td class="cell-dim cell--wrap">{{ r.reason || '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>

        <footer class="modal-actions">
          <button class="btn" @click="batchDeleteOpen = false">{{ t('buildEnvs.cancel') }}</button>
          <button
            class="btn btn--danger"
            :disabled="batchDeleting || selectedEnvs.length === 0"
            @click="confirmBatchDelete"
          >
            {{
              batchDeleting
                ? t('buildEnvs.batchWorking')
                : t('buildEnvs.batchDeleteConfirm', { n: selectedEnvs.length })
            }}
          </button>
        </footer>
      </div>
    </div>

    <!-- ── import ── -->
    <div v-if="importOpen" class="modal-mask" @click.self="importOpen = false">
      <div class="modal" role="dialog" :aria-label="t('buildEnvs.importTitle')">
        <h2 class="modal-title">{{ t('buildEnvs.importTitle') }}</h2>
        <p class="modal-body">{{ t('buildEnvs.importDesc') }}</p>
        <p class="note note--warn">{{ t('buildEnvs.importNoCredential') }}</p>

        <div class="form-grid">
          <label class="field">
            <span>{{ t('buildEnvs.importFormat') }}</span>
            <select v-model="importFormat">
              <option value="yaml">YAML</option>
              <option value="json">JSON</option>
            </select>
          </label>
          <label class="field">
            <span>{{ t('buildEnvs.importFile') }}</span>
            <input
              type="file"
              accept=".yaml,.yml,.json"
              @change="onPickImportFile($event)"
            />
          </label>

          <div class="field field--wide">
            <span>{{ t('buildEnvs.importMode') }}</span>
            <div class="radio-row">
              <label class="opt">
                <input v-model="importMode" type="radio" value="skip" name="import-mode" />
                <span>{{ importModeLabel('skip') }}</span>
              </label>
              <label class="opt">
                <input v-model="importMode" type="radio" value="overwrite" name="import-mode" />
                <span>{{ importModeLabel('overwrite') }}</span>
              </label>
            </div>
            <small>{{ t('buildEnvs.importModeHint') }}</small>
          </div>

          <label class="field field--wide">
            <span>{{ t('buildEnvs.importContent') }}</span>
            <textarea
              v-model="importContent"
              class="mono-area"
              rows="10"
              spellcheck="false"
              :placeholder="t('buildEnvs.importPlaceholder')"
            ></textarea>
          </label>
        </div>

        <p v-if="importBanner" class="banner banner--err">{{ importBanner }}</p>

        <div v-if="importReport" class="import-result">
          <p class="import-summary">
            {{ formatImportSummary(importReport.summary) }}
            <span class="cell-dim">· {{ importModeLabel(importReport.mode) }}</span>
          </p>
          <p class="note">
            {{
              importReport.dryRun
                ? t('buildEnvs.importPreviewNote')
                : t('buildEnvs.importAppliedNote', { n: importReport.summary.total })
            }}
          </p>
          <table class="grid grid--sm">
            <thead>
              <tr>
                <th>#</th>
                <th>{{ t('buildEnvs.colEnv') }}</th>
                <th>{{ t('buildEnvs.colAction') }}</th>
                <th>{{ t('buildEnvs.colEnabled') }}</th>
                <th>{{ t('buildEnvs.colReason') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="r in importReport.results" :key="r.index">
                <td>{{ r.index + 1 }}</td>
                <td class="mono">{{ r.language }} / {{ r.version }}</td>
                <td>
                  <span class="pill" :class="`pill--${r.action}`">
                    {{ importActionLabel(r.action) }}
                  </span>
                </td>
                <td>{{ r.enabled ? t('buildEnvs.enabledShort') : t('buildEnvs.disabledShort') }}</td>
                <td class="cell-dim cell--wrap">{{ r.reason || '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>

        <footer class="modal-actions">
          <button class="btn" @click="importOpen = false">{{ t('buildEnvs.cancel') }}</button>
          <button class="btn" :disabled="importBusy" @click="runImport(true)">
            {{ importBusy ? t('buildEnvs.importWorking') : t('buildEnvs.importPreview') }}
          </button>
          <button class="btn btn--primary" :disabled="importBusy" @click="runImport(false)">
            {{ importBusy ? t('buildEnvs.importWorking') : t('buildEnvs.importConfirm') }}
          </button>
        </footer>
      </div>
    </div>
  </div>
</template>

<style scoped>
.view-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  /* 窄窗口(629px 实测)下让按钮整组换到标题下面:不换行时按钮组按 max-content 撑开,
     会把标题挤成 1 字 1 行的竖排,并把最右边的按钮裁出视口。 */
  flex-wrap: wrap;
  margin-bottom: 16px;
}
.view-title {
  font-size: var(--text-display);
  font-weight: 700;
  color: var(--color-text);
}
.view-sub {
  font-size: var(--text-body);
  color: var(--color-faint);
  margin-top: 4px;
  max-width: 70ch;
}
.header-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  justify-content: flex-end;
  /* 必须允许收缩:flex-shrink:0 时整组按 max-content(6 个控件 ≈ 819px)撑开,
     629px 窄窗口下右侧三颗按钮会被裁出视口且页面不横向滚动,等于点不到。 */
  flex: 1 1 auto;
  min-width: 0;
}
/* 导出的一组选项:格式 + 是否含禁用 + 按钮,靠得近一点表示"这三样是一件事" */
.opt-group {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 8px;
  border: 1px solid var(--color-border);
  border-radius: 10px;
}
/* 排序档:与代理总览同款分段控件,order 档是后端权威顺序,另两档才是前端排 */
.sort-bar {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 12px;
}
.seg {
  display: inline-flex;
  gap: 2px;
  padding: 3px;
  border-radius: var(--rounded-md);
  border: 1px solid var(--color-border);
  background: var(--color-inset);
}
.seg__btn {
  font-size: var(--text-micro);
  font-weight: 600;
  padding: 6px 14px;
  border-radius: var(--rounded-sm);
  border: none;
  background: transparent;
  color: var(--color-faint);
  cursor: pointer;
  transition:
    color var(--duration-fast, 150ms) ease,
    background var(--duration-fast, 150ms) ease;
}
.seg__btn:hover {
  color: var(--color-dim);
}
.seg__btn--on {
  color: var(--color-text);
  background: var(--color-card);
  box-shadow: var(--shadow);
}
.nowrap {
  white-space: nowrap;
}
.opt {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: var(--text-label);
  color: var(--color-dim);
  cursor: pointer;
}
.field--inline {
  gap: 0;
}
.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip: rect(0 0 0 0);
  white-space: nowrap;
  border: 0;
}

.banner {
  padding: 10px 14px;
  border-radius: 8px;
  background: var(--color-bg-soft, rgba(0, 0, 0, 0.04));
  font-size: var(--text-label);
  margin-bottom: 12px;
}
.banner--err {
  background: var(--color-danger-soft, rgba(220, 38, 38, 0.1));
  color: var(--color-danger, #dc2626);
}

.state {
  padding: 32px;
  text-align: center;
  color: var(--color-faint);
}
.state--error {
  color: var(--color-danger, #dc2626);
}

.grid {
  width: 100%;
  border-collapse: collapse;
  font-size: var(--text-label);
}
.grid th {
  text-align: left;
  padding: 10px 12px;
  border-bottom: 1px solid var(--color-border);
  color: var(--color-faint);
  font-weight: 600;
  white-space: nowrap;
}
.grid td {
  padding: 12px;
  border-bottom: 1px solid var(--color-border);
  vertical-align: top;
}
.cell-strong {
  font-weight: 600;
  color: var(--color-text);
}
.cell-dim {
  font-size: var(--text-small, 0.85em);
  color: var(--color-faint);
}
.cell--err {
  color: var(--color-danger, #dc2626);
  /* 错误信息可长达 1KB(含 registry URL):限制列宽,最多两行,完整内容悬浮查看。 */
  max-width: 300px;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  word-break: break-all;
}
.mono {
  font-family: var(--font-mono, monospace);
  font-size: var(--text-small, 0.85em);
  word-break: break-all;
}

.pill {
  display: inline-block;
  padding: 2px 10px;
  border-radius: 999px;
  font-size: var(--text-small, 0.8em);
  font-weight: 600;
}
.pill--available {
  background: rgba(34, 197, 94, 0.15);
  color: #16a34a;
}
.pill--pullable {
  background: rgba(20, 184, 166, 0.16);
  color: #0d9488;
}
.pill--unavailable {
  background: rgba(220, 38, 38, 0.15);
  color: #dc2626;
}
.pill--unchecked {
  background: rgba(234, 179, 8, 0.15);
  color: #a16207;
}
.pill--checking {
  background: rgba(59, 130, 246, 0.15);
  color: #2563eb;
}

.switch {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  cursor: pointer;
}

.actions {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}

.btn {
  padding: 7px 14px;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-bg, #fff);
  color: var(--color-text);
  font-size: var(--text-label);
  cursor: pointer;
}
.btn:hover:not(:disabled) {
  border-color: var(--color-primary);
}
.btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.btn--primary {
  background: var(--color-primary);
  border-color: var(--color-primary);
  color: #fff;
}
.btn--danger {
  color: var(--color-danger, #dc2626);
  border-color: var(--color-danger, #dc2626);
}
.btn--sm {
  padding: 4px 10px;
  font-size: var(--text-small, 0.85em);
}

/* 检查/拉取按钮文案会在"拉取 ↔ 拉取中…"间切换,预留最长文案宽度避免行内按钮抖动错位 */
.btn--state {
  min-width: 6.5em;
  text-align: center;
}

.modal-mask {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.5);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 100;
}
.modal {
  background: var(--color-bg, #fff);
  border-radius: 12px;
  padding: 24px;
  width: min(680px, 92vw);
  max-height: 88vh;
  overflow: auto;
}
.modal--sm {
  width: min(420px, 92vw);
}
.modal-title {
  font-size: var(--text-h3, 1.1rem);
  font-weight: 700;
  margin-bottom: 16px;
}
.modal-body {
  color: var(--color-dim);
  margin-bottom: 12px;
}
.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 20px;
}

.form-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 14px;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 5px;
  font-size: var(--text-label);
}
.field--wide {
  grid-column: 1 / -1;
}
.field > span {
  font-weight: 600;
  color: var(--color-dim);
}
.field input,
.field select {
  padding: 8px 10px;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-bg, #fff);
  color: var(--color-text);
  font-size: var(--text-label);
}
.field small {
  color: var(--color-faint);
  font-size: var(--text-small, 0.85em);
}
/* 选项类控件(radio/checkbox)不吃 .field 的输入框样式,否则会被画出框线 */
.opt input[type='radio'],
.opt input[type='checkbox'] {
  width: auto;
  padding: 0;
  border: none;
  background: none;
  accent-color: var(--color-primary);
}
.radio-row {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

/* 勾选列:只放一个框,宽度收紧,别挤掉环境/镜像两列。用 .grid 前缀压过 `.grid th` 的左对齐。 */
.grid th.col-check,
.grid td.col-check {
  width: 34px;
  text-align: center;
}

.mono-area {
  padding: 10px;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-bg, #fff);
  color: var(--color-text);
  font-family: var(--font-mono, monospace);
  font-size: var(--text-small, 0.85em);
  line-height: 1.5;
  resize: vertical;
}

.note {
  font-size: var(--text-small, 0.85em);
  color: var(--color-faint);
  margin-bottom: 12px;
}
.note--warn {
  padding: 8px 12px;
  border-radius: 8px;
  background: rgba(234, 179, 8, 0.12);
  color: #a16207;
}

.import-result {
  margin-top: 16px;
  padding-top: 14px;
  border-top: 1px solid var(--color-border);
}
.import-summary {
  font-size: var(--text-label);
  font-weight: 600;
  color: var(--color-text);
  margin-bottom: 8px;
}
.grid--sm th,
.grid--sm td {
  padding: 6px 8px;
  font-size: var(--text-small, 0.85em);
}
.cell--wrap {
  max-width: 340px;
  word-break: break-word;
}

.pill--created {
  background: rgba(34, 197, 94, 0.15);
  color: #16a34a;
}
.pill--updated {
  background: rgba(59, 130, 246, 0.15);
  color: #2563eb;
}
.pill--skipped {
  background: rgba(120, 120, 120, 0.16);
  color: var(--color-dim);
}
.pill--failed {
  background: rgba(220, 38, 38, 0.15);
  color: #dc2626;
}
.pill--deleted {
  background: rgba(220, 38, 38, 0.15);
  color: #dc2626;
}

/* 批量删除确认:勾选的行要逐条看得见,但列表本身不是主角,限高滚动即可。 */
.del-list {
  list-style: none;
  margin: 0 0 12px;
  padding: 8px 10px;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  max-height: 200px;
  overflow: auto;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.del-list li {
  display: flex;
  align-items: baseline;
  gap: 8px;
  flex-wrap: wrap;
  font-size: var(--text-label);
}
</style>
