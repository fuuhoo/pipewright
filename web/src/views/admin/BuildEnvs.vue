<script setup lang="ts">
/**
 * v6.2 构建环境管理(admin-only)。
 *
 * 覆盖 §3.1 / §3.2:
 *   - CRUD(language/version/display_name/description/image/credential/sort_order)
 *   - 镜像来源 official(官方短名)/ custom(任意地址,系统不拼接 — R8)
 *   - P0 #4 三态:unchecked 拒启用、unavailable 强制禁用、available 放行
 *   - 手动检查 / 手动拉取 / 一键检查
 *
 * 权限:路由 meta.adminOnly + AppShell 菜单 role 隔离双保险。
 */
import { ref, onMounted, onUnmounted } from 'vue'
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
} from '../../api/buildEnvs'
import type { BuildEnv, BuildEnvInput, ImageCheckStatus } from '../../api/buildEnvs'
import { listCredentials } from '../../api/credentials'
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

// ─── data loading ───────────────────────────────────────────────────────────

async function load(): Promise<void> {
  loadState.value = 'loading'
  loadError.value = ''
  try {
    // 主列表是权威内容;凭据下拉只是可选项(仅 custom 镜像需要),单独加载且允许失败
    // —— vault 未配置(master key 缺失)时不应让整页变成错误态。
    envs.value = await listBuildEnvs({ includeDisabled: true })
    loadState.value = 'idle'
  } catch (err) {
    loadError.value = errMsg(err, 'buildEnvs.errLoad')
    loadState.value = 'error'
    return
  }
  try {
    const creds = await listCredentials()
    registryCredentials.value = creds.filter(
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
</script>

<template>
  <div class="buildenvs-view">
    <header class="view-header">
      <div>
        <h1 class="view-title">{{ t('buildEnvs.title') }}</h1>
        <p class="view-sub">{{ t('buildEnvs.desc') }}</p>
      </div>
      <div class="header-actions">
        <button class="btn" :disabled="checkingAll" @click="onCheckAll">
          {{ checkingAll ? t('buildEnvs.checking') : t('buildEnvs.checkAll') }}
        </button>
        <button class="btn btn--primary" @click="openAdd">+ {{ t('buildEnvs.add') }}</button>
      </div>
    </header>

    <p v-if="rowBanner" class="banner" role="status">{{ rowBanner }}</p>

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
          <th>{{ t('buildEnvs.colEnv') }}</th>
          <th>{{ t('buildEnvs.colImage') }}</th>
          <th>{{ t('buildEnvs.colStatus') }}</th>
          <th>{{ t('buildEnvs.colEnabled') }}</th>
          <th>{{ t('buildEnvs.colActions') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="e in envs" :key="e.id">
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
            <input v-model="form.language" type="text" placeholder="node" />
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
  </div>
</template>

<style scoped>
.view-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
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
  gap: 8px;
  flex-shrink: 0;
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
</style>
