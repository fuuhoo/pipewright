<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import {
  listServers,
  createServer,
  updateServer,
  deleteServer,
  testServer,
} from '../../api/servers'
import type {
  Server,
  CreateServerInput,
  UpdateServerInput,
  ServerTestResult,
} from '../../api/servers'
import { listCredentials, usableCredentials } from '../../api/credentials'
import type { Credential } from '../../api/credentials'
import { listGroups, type Group } from '../../api/groups'
import { groupNames } from '../../lib/groupFilter'
import { useSessionStore } from '../../stores/session'
import { HttpError } from '../../api/http'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import ServiceLogViewer from '../../components/ops/ServiceLogViewer.vue'
import ServiceOpsPanel from '../../components/ops/ServiceOpsPanel.vue'
import RemoteWorkspaceModal from '../../components/ops/RemoteWorkspaceModal.vue'

const router = useRouter()
const { t } = useI18n()
const sessionStore = useSessionStore()

/**
 * 「坐到那台机器前面」这条能力(远程弹窗 = 主机终端 + 远程文件、全屏运维终端)按
 * server.exec 亮灭 —— 后端同一件事也按这个点判(requireServerPoint),没有点时点了只会
 * 拿到一条 role_denied。指标与日志属于 server.view,不受这里影响。
 */
const canExec = computed(() => sessionStore.canPerm('server.exec'))

// ─── state ──────────────────────────────────────────────────────────────────

type LoadState = 'idle' | 'loading' | 'error'

const loadState = ref<LoadState>('idle')
const loadError = ref('')
const servers = ref<Server[]>([])

// SSH credentials available to bind (ssh_key private key, or ssh_password login password).
const sshCredentials = ref<Credential[]>([])

// ─── 分组(v6.2 分组权限)─────────────────────────────────────────────────────
//
// 与项目的差别在「登记」这一步:未归组的主机全员可操作(含终端/服务运维),所以登记一台
// 不归属任何组的目标机本身就是管理员级别的设置。后端在 POST /api/servers 判这条,这里只是
// 把候选与按钮按同一口径收拢 —— 判定结论仍然只来自 GET /api/groups 的 canManage。

const groups = ref<Group[]>([])
const groupById = computed<Record<string, Group>>(() => {
  const out: Record<string, Group> = {}
  for (const g of groups.value) out[g.id] = g
  return out
})
const manageableGroups = computed(() => groups.value.filter((g) => g.canManage))
const isAdminUser = computed(() => sessionStore.user?.role === 'admin')

const groupLabels = computed(() => ({
  all: t('groups.filterAll'),
  ungrouped: t('groups.ungrouped'),
  missing: t('groups.groupMissing'),
  public: t('groups.visibilityPublic'),
  private: t('groups.visibilityPrivate'),
}))

/** 这台主机所在分组的展示名;未归组给单一项「未归组」。 */
function serverGroupNames(s: Server): string[] {
  return groupNames(s.groupIds, groups.value, groupLabels.value)
}

/**
 * 能否改这台主机的归属:改的是**每一个**所在组的归属旁路,所以每一组都要管得住。
 * 未归组(没有组)的主机只有管理员能动;管理员另外总能修(含引用悬挂的组)。
 */
function canChangeGroup(s: Server): boolean {
  if (isAdminUser.value) return true
  if (s.groupIds.length === 0) return false
  return s.groupIds.every((id) => groupById.value[id]?.canManage === true)
}

/** 谁能登记新主机:管理员(可登记为未归组)或至少管着一个组(必须归入自己的组)。 */
const canAddServer = computed(() => isAdminUser.value || manageableGroups.value.length > 0)

async function loadGroups(): Promise<void> {
  try {
    groups.value = await listGroups()
  } catch {
    // 分组读不到不影响主机列表本身:归属列退化成「未归组」,不弹错误横幅。
    groups.value = []
  }
}

// ─── add / edit modal ───────────────────────────────────────────────────────

const modalOpen = ref(false)
const modalMode = ref<'add' | 'edit'>('add')
const editingId = ref<string | null>(null)

const form = ref({
  name: '',
  host: '',
  port: 22,
  user: '',
  credentialId: '',
  groupIds: [] as string[],
})

const formErrors = ref({
  name: '',
  host: '',
  port: '',
  user: '',
  credentialId: '',
  groupIds: '',
})

const formBanner = ref('')
const formSubmitting = ref(false)

// ─── delete confirm ─────────────────────────────────────────────────────────

const deleteModalOpen = ref(false)
const deletingServer = ref<Server | null>(null)
const deleteSubmitting = ref(false)
const deleteBanner = ref('')

// ─── test connection ────────────────────────────────────────────────────────

const testingId = ref<string | null>(null)
const testResults = ref<Record<string, ServerTestResult>>({})

// ─── service logs viewer (Story 6-2, FR-16) ──────────────────────────────────

const logsModalOpen = ref(false)
const logsServer = ref<Server | null>(null)

function openLogsModal(s: Server): void {
  logsServer.value = s
  logsModalOpen.value = true
}

function closeLogsModal(): void {
  logsModalOpen.value = false
  logsServer.value = null
}

// ─── service operations panel (Story 6-3, FR-17) ─────────────────────────────

const opsModalOpen = ref(false)
const opsServer = ref<Server | null>(null)

function openOpsModal(s: Server): void {
  opsServer.value = s
  opsModalOpen.value = true
}

function closeOpsModal(): void {
  opsModalOpen.value = false
  opsServer.value = null
}

// ─── container terminal (Story 6-4, FR-18) ───────────────────────────────────
// 升级为独立全屏「AI 运维终端」页(左终端 / 右 AI 助手),替代旧内联弹窗。

function openTerminal(s: Server): void {
  // 全屏终端在**新标签页**打开,保留当前服务器列表页(终端是长会话,不该顶掉原页面)。
  const route = router.resolve({ name: 'server-terminal', params: { id: s.id } })
  window.open(route.href, '_blank', 'noopener')
}

// ─── remote workspace modal(终端 + 文件面板,同屏联动) ──────────────────────────
// 与全屏终端页并存:这一条是「边看目录边敲命令」的日常入口,全屏页保留 AI 助手与大终端。
const remoteServer = ref<Server | null>(null)

function openRemote(s: Server): void {
  remoteServer.value = s
}

function closeRemote(): void {
  remoteServer.value = null
}

// ─── helpers ────────────────────────────────────────────────────────────────

const hasSSHCredentials = computed(() => sshCredentials.value.length > 0)

function credentialLabel(id: string): string {
  const c = sshCredentials.value.find((c) => c.id === id)
  return c ? c.name : t('settingsServers.credentialDeleted')
}

// ─── data loading ────────────────────────────────────────────────────────────

async function loadServers(): Promise<void> {
  loadState.value = 'loading'
  loadError.value = ''
  try {
    const [srv, creds] = await Promise.all([listServers(), listCredentials(), loadGroups()])
    servers.value = srv
    sshCredentials.value = usableCredentials(creds).filter(
      (c) => c.type === 'ssh_key' || c.type === 'ssh_password',
    )
    loadState.value = 'idle'
  } catch (err) {
    if (err instanceof HttpError) {
      if (err.status === 0) {
        loadError.value = t('settingsServers.errLoadConn')
      } else if (err.apiError?.code === 'vault_unconfigured') {
        loadError.value = t('settingsServers.errVaultUnconfigured')
      } else {
        loadError.value = err.apiError?.message ?? t('settingsServers.errLoadStatus', { status: err.status })
      }
    } else {
      loadError.value = t('settingsServers.errLoadRetry')
    }
    loadState.value = 'error'
  }
}

onMounted(loadServers)

// ─── modal open / close ─────────────────────────────────────────────────────

function openAddModal(): void {
  modalMode.value = 'add'
  editingId.value = null
  form.value = {
    name: '',
    host: '',
    port: 22,
    user: '',
    credentialId: sshCredentials.value[0]?.id ?? '',
    // 非管理员登记的主机必须归入自己管得动的组(未归组 = 管理员专属),所以默认勾上首个候选组。
    groupIds: isAdminUser.value ? [] : (manageableGroups.value[0] ? [manageableGroups.value[0].id] : []),
  }
  clearFormErrors()
  formBanner.value = ''
  modalOpen.value = true
}

function openEditModal(s: Server): void {
  modalMode.value = 'edit'
  editingId.value = s.id
  form.value = {
    name: s.name,
    host: s.host,
    port: s.port,
    user: s.user,
    credentialId: s.credentialId,
    groupIds: [...s.groupIds],
  }
  clearFormErrors()
  formBanner.value = ''
  modalOpen.value = true
}

function closeModal(): void {
  if (formSubmitting.value) return
  modalOpen.value = false
}

// ─── form validation ─────────────────────────────────────────────────────────

function clearFormErrors(): void {
  formErrors.value = { name: '', host: '', port: '', user: '', credentialId: '', groupIds: '' }
}

/** 正在编辑的主机(归属候选要能显示它当前所在、即使我已管不动的组)。 */
const editingServer = computed(() =>
  editingId.value ? (servers.value.find((s) => s.id === editingId.value) ?? null) : null,
)

/**
 * 弹窗里的候选分组:我管得着的组 + 当前所在但管不着的组。
 * 后者必须显示(否则看不出它现在在哪),且勾选状态不可改。
 */
const groupOptions = computed<Group[]>(() => {
  const cur = editingServer.value?.groupIds ?? []
  const seen = new Set<string>()
  const out: Group[] = []
  for (const id of cur) {
    const g = groupById.value[id]
    if (g && !g.canManage && !seen.has(g.id)) {
      seen.add(g.id)
      out.push(g)
    }
  }
  for (const g of manageableGroups.value) {
    if (!seen.has(g.id)) {
      seen.add(g.id)
      out.push(g)
    }
  }
  return out
})

/** 当前所在、但分组已被删除(列表里取不到快照)的 ID:显示一行「分组已失效」。 */
const missingGroupIDs = computed(() =>
  (editingServer.value?.groupIds ?? []).filter((id) => !groupById.value[id]),
)

/** 清空勾选 = 未归组。新建时只有管理员能这么做(对全员开放操作权)。 */
const ungroupedAllowed = computed(() =>
  modalMode.value === 'add' ? isAdminUser.value : groupFieldEditable.value,
)

/** 归属字段是否可改:编辑时取决于对这个资源的 Manage 权(未归组主机只有管理员能挪)。 */
const groupFieldEditable = computed(() =>
  modalMode.value === 'add' ? canAddServer.value : (editingServer.value ? canChangeGroup(editingServer.value) : false),
)

function validateForm(): boolean {
  clearFormErrors()
  let ok = true
  if (!form.value.name.trim()) {
    formErrors.value.name = t('settingsServers.errNameRequired')
    ok = false
  }
  if (!form.value.host.trim()) {
    formErrors.value.host = t('settingsServers.errHostRequired')
    ok = false
  }
  if (!form.value.user.trim()) {
    formErrors.value.user = t('settingsServers.errUserRequired')
    ok = false
  }
  if (!Number.isInteger(form.value.port) || form.value.port < 1 || form.value.port > 65535) {
    formErrors.value.port = t('settingsServers.errPortRange')
    ok = false
  }
  if (!form.value.credentialId) {
    formErrors.value.credentialId = t('settingsServers.errCredentialRequired')
    ok = false
  }
  if (modalMode.value === 'add' && !isAdminUser.value && form.value.groupIds.length === 0) {
    // 后端同样会拒:非管理员登记的未归组主机等于对全员开放操作权。
    formErrors.value.groupIds = t('settingsServers.errGroupRequired')
    ok = false
  }
  return ok
}

// ─── form submit ─────────────────────────────────────────────────────────────

async function handleFormSubmit(): Promise<void> {
  if (!validateForm()) return
  formSubmitting.value = true
  formBanner.value = ''
  try {
    if (modalMode.value === 'add') {
      const payload: CreateServerInput = {
        name: form.value.name.trim(),
        host: form.value.host.trim(),
        port: form.value.port,
        user: form.value.user.trim(),
        credentialId: form.value.credentialId,
        groupIds: [...form.value.groupIds],
      }
      const created = await createServer(payload)
      servers.value = [created, ...servers.value]
    } else if (editingId.value) {
      const payload: UpdateServerInput = {
        name: form.value.name.trim(),
        host: form.value.host.trim(),
        port: form.value.port,
        user: form.value.user.trim(),
        credentialId: form.value.credentialId,
      }
      // 只在归属真的变了才带上:PUT 用指针区分「不改」,把全空数组原样发回去等于
      // 试图把主机移出所有分组,组长会吃到 403。
      const origin = editingServer.value
      const sortedNext = [...form.value.groupIds].sort()
      const sortedPrev = [...(origin?.groupIds ?? [])].sort()
      if (origin && sortedNext.join(',') !== sortedPrev.join(',')) payload.groupIds = form.value.groupIds
      const updated = await updateServer(editingId.value, payload)
      servers.value = servers.value.map((s) => (s.id === updated.id ? updated : s))
      delete testResults.value[updated.id]
      // 归入自己不起作用的组后,这台机器可能不再对我可见:按新范围重取列表。
      if (payload.groupIds !== undefined && payload.groupIds.some((id) => !groupById.value[id]?.canManage)) {
        await loadServers()
      }
    }
    modalOpen.value = false
  } catch (err) {
    if (err instanceof HttpError) {
      if (err.status === 0) {
        formBanner.value = t('settingsServers.errConnRetry')
      } else {
        formBanner.value = err.apiError?.message ?? t('settingsServers.errSaveStatus', { status: err.status })
      }
    } else {
      formBanner.value = t('settingsServers.errSaveRetry')
    }
  } finally {
    formSubmitting.value = false
  }
}

// ─── delete ──────────────────────────────────────────────────────────────────

function openDeleteModal(s: Server): void {
  deletingServer.value = s
  deleteBanner.value = ''
  deleteModalOpen.value = true
}

function closeDeleteModal(): void {
  if (deleteSubmitting.value) return
  deleteModalOpen.value = false
  deletingServer.value = null
}

async function confirmDelete(): Promise<void> {
  if (!deletingServer.value) return
  deleteSubmitting.value = true
  deleteBanner.value = ''
  const id = deletingServer.value.id
  try {
    await deleteServer(id)
    servers.value = servers.value.filter((s) => s.id !== id)
    delete testResults.value[id]
    deleteModalOpen.value = false
    deletingServer.value = null
  } catch (err) {
    if (err instanceof HttpError) {
      if (err.status === 0) {
        deleteBanner.value = t('settingsServers.errConnRetry')
      } else {
        deleteBanner.value = err.apiError?.message ?? t('settingsServers.errDeleteStatus', { status: err.status })
      }
    } else {
      deleteBanner.value = t('settingsServers.errDeleteRetry')
    }
  } finally {
    deleteSubmitting.value = false
  }
}

// ─── test connection ──────────────────────────────────────────────────────────

async function handleTest(s: Server): Promise<void> {
  testingId.value = s.id
  try {
    const result = await testServer(s.id)
    testResults.value = { ...testResults.value, [s.id]: result }
  } catch (err) {
    let message = t('settingsServers.errTestRetry')
    if (err instanceof HttpError) {
      message = err.apiError?.message ?? t('settingsServers.errTestStatus', { status: err.status })
    }
    testResults.value = {
      ...testResults.value,
      [s.id]: { ok: false, latencyMs: 0, output: '', error: message },
    }
  } finally {
    if (testingId.value === s.id) testingId.value = null
  }
}
</script>

<template>
  <div class="servers-root">
    <!-- ─── section header ──────────────────────────────────────────────────── -->
    <div class="section-head">
      <div class="section-head-text">
        <h2 class="section-title">{{ t('settingsServers.title') }}</h2>
        <p class="section-desc">
          {{ t('settingsServers.desc') }}
        </p>
      </div>
      <button
        class="btn-primary"
        :disabled="loadState === 'loading' || !hasSSHCredentials || !canAddServer"
        :title="!canAddServer ? t('settingsServers.noManageGroupHint') : (!hasSSHCredentials ? t('settingsServers.addDisabledHint') : '')"
        @click="openAddModal"
      >
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" aria-hidden="true">
          <path d="M12 5v14M5 12h14" />
        </svg>
        {{ t('settingsServers.addServer') }}
      </button>
    </div>

    <!-- ─── no-credential hint ────────────────────────────────────────────────── -->
    <div v-if="loadState === 'idle' && !hasSSHCredentials" class="banner banner--warn" role="status">
      <span>{{ t('settingsServers.noCredentialHint') }}</span>
    </div>

    <!-- ─── load error banner ─────────────────────────────────────────────────── -->
    <div v-if="loadState === 'error'" class="banner banner--error" role="alert">
      <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
        <circle cx="12" cy="12" r="9" /><path d="M12 8v4M12 16h.01" />
      </svg>
      <span>{{ loadError }}</span>
      <button class="banner-retry" @click="loadServers">↻ {{ t('settingsServers.retry') }}</button>
    </div>

    <!-- ─── servers panel ─────────────────────────────────────────────────────── -->
    <div class="panel" :class="{ 'panel--loading': loadState === 'loading' }">
      <div class="panel-head">
        <span>{{ t('settingsServers.registeredServers') }}</span>
        <span class="panel-meta" v-if="loadState === 'idle'">{{ t('settingsServers.serverCount', { n: servers.length }) }}</span>
      </div>

      <template v-if="loadState === 'loading'">
        <div class="skel-row" v-for="i in 3" :key="i" aria-hidden="true">
          <div class="skel-bar" />
        </div>
      </template>

      <template v-else-if="loadState === 'idle' && servers.length === 0">
        <div class="empty-row" role="status">{{ t('settingsServers.emptyList') }}</div>
      </template>

      <ul v-else class="server-list">
        <li v-for="s in servers" :key="s.id" class="server-row">
          <div class="server-main">
            <div class="server-name">{{ s.name }}</div>
            <div class="server-addr">
              <span class="mono">{{ s.user }}@{{ s.host }}:{{ s.port }}</span>
              <span class="cred-tag">🔑 {{ credentialLabel(s.credentialId) }}</span>
              <span
                v-for="(gm, gi) in serverGroupNames(s)"
                :key="gi"
                class="group-tag"
                :title="t('groups.fieldGroupHint')"
              >{{ gm }}</span>
            </div>
            <!-- test result -->
            <div
              v-if="testResults[s.id]"
              class="test-result"
              :class="testResults[s.id].ok ? 'test-result--ok' : 'test-result--fail'"
              role="status"
            >
              <template v-if="testResults[s.id].ok">
                ✓ {{ t('settingsServers.testOk', { ms: testResults[s.id].latencyMs }) }}
                <span class="mono uname">{{ testResults[s.id].output }}</span>
              </template>
              <template v-else>
                ✕ {{ testResults[s.id].error }}
              </template>
            </div>
          </div>
          <div class="server-actions">
            <button class="btn-ghost" :disabled="testingId === s.id" @click="handleTest(s)">
              {{ testingId === s.id ? t('settingsServers.testing') : t('settingsServers.testConnection') }}
            </button>
            <button class="btn-ghost" @click="openLogsModal(s)">{{ t('settingsServers.logs') }}</button>
            <button v-if="canExec" class="btn-ghost" @click="openRemote(s)">
              {{ t('settingsServers.remote') }}
            </button>
            <button v-if="canExec" class="btn-ghost" @click="openTerminal(s)">
              {{ t('settingsServers.terminal') }}
            </button>
            <button class="btn-ghost" @click="openOpsModal(s)">{{ t('settingsServers.serviceOps') }}</button>
            <button class="btn-ghost" @click="openEditModal(s)">{{ t('settingsServers.edit') }}</button>
            <button class="btn-ghost btn-danger" @click="openDeleteModal(s)">{{ t('settingsServers.delete') }}</button>
          </div>
        </li>
      </ul>
    </div>

    <!-- ─── add / edit modal ──────────────────────────────────────────────────── -->
    <div v-if="modalOpen" class="modal-backdrop" @click.self="closeModal">
      <div class="modal" role="dialog" aria-modal="true" aria-labelledby="server-modal-title">
        <h3 id="server-modal-title" class="modal-title">
          {{ modalMode === 'add' ? t('settingsServers.addServer') : t('settingsServers.editServer') }}
        </h3>

        <div v-if="formBanner" class="banner banner--error" role="alert">{{ formBanner }}</div>

        <form @submit.prevent="handleFormSubmit">
          <label class="field">
            <span class="field-label">{{ t('settingsServers.fieldName') }}</span>
            <input v-model="form.name" class="field-input" type="text" placeholder="web-prod-1" autocomplete="off" />
            <span v-if="formErrors.name" class="field-error">{{ formErrors.name }}</span>
          </label>

          <div class="field-row">
            <label class="field field-grow">
              <span class="field-label">{{ t('settingsServers.fieldHost') }}</span>
              <input v-model="form.host" class="field-input" type="text" placeholder="10.0.0.5" autocomplete="off" />
              <span v-if="formErrors.host" class="field-error">{{ formErrors.host }}</span>
            </label>
            <label class="field field-port">
              <span class="field-label">{{ t('settingsServers.fieldPort') }}</span>
              <input v-model.number="form.port" class="field-input" type="number" min="1" max="65535" />
              <span v-if="formErrors.port" class="field-error">{{ formErrors.port }}</span>
            </label>
          </div>

          <label class="field">
            <span class="field-label">{{ t('settingsServers.fieldUser') }}</span>
            <input v-model="form.user" class="field-input" type="text" placeholder="deploy" autocomplete="off" />
            <span v-if="formErrors.user" class="field-error">{{ formErrors.user }}</span>
          </label>

          <label class="field">
            <span class="field-label">{{ t('settingsServers.fieldCredential') }}</span>
            <select v-model="form.credentialId" class="field-input">
              <option value="" disabled>{{ t('settingsServers.selectCredential') }}</option>
              <option v-for="c in sshCredentials" :key="c.id" :value="c.id">{{ c.name }}</option>
            </select>
            <span v-if="formErrors.credentialId" class="field-error">{{ formErrors.credentialId }}</span>
          </label>

          <!-- 分组:决定谁能看/谁能操作这台主机(含终端与服务运维)。可多选,权限取并集。 -->
          <div class="field">
            <span class="field-label">
              {{ t('groups.fieldGroup') }}
              <span class="field-hint-inline">{{ t('groups.fieldGroupsHint') }}</span>
            </span>
            <div class="group-picker" :class="{ 'group-picker--locked': formSubmitting || !groupFieldEditable }">
              <label v-for="g in groupOptions" :key="g.id" class="group-picker__item">
                <input
                  v-model="form.groupIds"
                  type="checkbox"
                  :value="g.id"
                  :disabled="formSubmitting || !groupFieldEditable || !g.canManage"
                />
                <span class="group-picker__name">{{ g.name }}</span>
                <span class="group-picker__vis">{{
                  g.visibility === 'public' ? t('groups.visibilityPublic') : t('groups.visibilityPrivate')
                }}</span>
                <span v-if="!g.canManage" class="group-picker__note">{{ t('groups.multiGroupLocked') }}</span>
              </label>
              <label v-for="id in missingGroupIDs" :key="id" class="group-picker__item">
                <input type="checkbox" checked disabled />
                <span class="group-picker__name">{{ t('groups.groupMissing') }}</span>
                <span class="group-picker__note">{{ t('groups.multiGroupLocked') }}</span>
              </label>
              <p v-if="ungroupedAllowed" class="group-picker__hint">{{ t('groups.multiGroupUngroupedHint') }}</p>
            </div>
            <span v-if="formErrors.groupIds" class="field-error">{{ formErrors.groupIds }}</span>
            <span v-else-if="groupFieldEditable && !groupOptions.length" class="field-hint">
              {{ t('groups.noGroupsHint') }}
            </span>
            <span v-else-if="!groupFieldEditable" class="field-hint">{{ t('groups.lockedHint') }}</span>
          </div>

          <div class="modal-actions">
            <button type="button" class="btn-ghost" :disabled="formSubmitting" @click="closeModal">{{ t('settingsServers.cancel') }}</button>
            <button type="submit" class="btn-primary" :disabled="formSubmitting">
              {{ formSubmitting ? t('settingsServers.saving') : t('settingsServers.save') }}
            </button>
          </div>
        </form>
      </div>
    </div>

    <!-- ─── delete confirm modal ──────────────────────────────────────────────── -->
    <div v-if="deleteModalOpen" class="modal-backdrop" @click.self="closeDeleteModal">
      <div class="modal" role="dialog" aria-modal="true" aria-labelledby="server-del-title">
        <h3 id="server-del-title" class="modal-title">{{ t('settingsServers.deleteServer') }}</h3>
        <div v-if="deleteBanner" class="banner banner--error" role="alert">{{ deleteBanner }}</div>
        <p class="modal-text">
          <i18n-t keypath="settingsServers.deleteConfirm" tag="span">
            <template #name><strong>{{ deletingServer?.name }}</strong></template>
          </i18n-t>
        </p>
        <div class="modal-actions">
          <button type="button" class="btn-ghost" :disabled="deleteSubmitting" @click="closeDeleteModal">{{ t('settingsServers.cancel') }}</button>
          <button type="button" class="btn-primary btn-danger" :disabled="deleteSubmitting" @click="confirmDelete">
            {{ deleteSubmitting ? t('settingsServers.deleting') : t('settingsServers.confirmDelete') }}
          </button>
        </div>
      </div>
    </div>

    <!-- ─── service logs modal (Story 6-2, FR-16) ─────────────────────────────── -->
    <div v-if="logsModalOpen" class="modal-backdrop" @click.self="closeLogsModal">
      <div class="modal modal--wide" role="dialog" aria-modal="true" aria-labelledby="server-logs-title">
        <div class="logs-modal-head">
          <h3 id="server-logs-title" class="modal-title">
            {{ t('settingsServers.serviceLogsTitle', { name: logsServer?.name }) }}
          </h3>
          <button type="button" class="btn-ghost" :aria-label="t('settingsServers.close')" @click="closeLogsModal">{{ t('settingsServers.close') }}</button>
        </div>
        <p class="logs-modal-sub mono">
          {{ logsServer?.user }}@{{ logsServer?.host }}:{{ logsServer?.port }}
        </p>
        <ServiceLogViewer
          v-if="logsServer"
          :server-id="logsServer.id"
          :server-name="logsServer.name"
        />
      </div>
    </div>

    <!-- ─── service operations modal (Story 6-3, FR-17) ───────────────────────── -->
    <div v-if="opsModalOpen" class="modal-backdrop" @click.self="closeOpsModal">
      <div class="modal" role="dialog" aria-modal="true" aria-labelledby="server-ops-title">
        <div class="logs-modal-head">
          <h3 id="server-ops-title" class="modal-title">
            {{ t('settingsServers.serviceOpsTitle', { name: opsServer?.name }) }}
          </h3>
          <button type="button" class="btn-ghost" :aria-label="t('settingsServers.close')" @click="closeOpsModal">{{ t('settingsServers.close') }}</button>
        </div>
        <p class="logs-modal-sub mono">
          {{ opsServer?.user }}@{{ opsServer?.host }}:{{ opsServer?.port }}
        </p>
        <ServiceOpsPanel
          v-if="opsServer"
          :server-id="opsServer.id"
          :server-name="opsServer.name"
        />
      </div>
    </div>

    <!-- ─── 远程工作区(上半屏终端 / 下半屏文件面板) ───────────────────────────── -->
    <RemoteWorkspaceModal
      v-if="remoteServer"
      :server-id="remoteServer.id"
      :server-name="remoteServer.name"
      :host-label="`${remoteServer.user}@${remoteServer.host}:${remoteServer.port}`"
      @close="closeRemote"
    />
  </div>
</template>

<style scoped>
.servers-root {
  display: flex;
  flex-direction: column;
  gap: 20px;
}

.section-head {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 24px;
}

.section-title {
  font-size: var(--text-heading);
  font-weight: 600;
  color: var(--color-text);
}

.section-desc {
  font-size: var(--text-label);
  color: var(--color-faint);
  margin-top: 6px;
  max-width: 60ch;
  line-height: 1.55;
}

.btn-primary {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 8px 14px;
  font-size: var(--text-label);
  font-weight: 600;
  color: #fff;
  background: var(--color-primary);
  border: none;
  border-radius: var(--radius-md);
  cursor: pointer;
  white-space: nowrap;
  transition: filter var(--duration-fast);
}
.btn-primary:hover:not(:disabled) {
  filter: brightness(1.08);
}
.btn-primary:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.btn-ghost {
  padding: 6px 12px;
  font-size: var(--text-label);
  font-weight: 500;
  color: var(--color-dim);
  background: transparent;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  cursor: pointer;
  transition:
    color var(--duration-fast),
    border-color var(--duration-fast),
    background var(--duration-fast);
}
.btn-ghost:hover:not(:disabled) {
  color: var(--color-text);
  border-color: var(--color-dim);
}
.btn-ghost:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.btn-danger {
  color: var(--color-danger, #d4503e);
}
.btn-danger:hover:not(:disabled) {
  border-color: var(--color-danger, #d4503e);
  color: var(--color-danger, #d4503e);
}
.btn-primary.btn-danger {
  color: #fff;
  background: var(--color-danger, #d4503e);
}

.banner {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 14px;
  font-size: var(--text-label);
  border-radius: var(--radius-md);
}
.banner--error {
  color: var(--color-danger, #d4503e);
  background: color-mix(in oklch, var(--color-danger, #d4503e) 10%, transparent);
}
.banner--warn {
  color: var(--color-warn, #b88600);
  background: color-mix(in oklch, var(--color-warn, #b88600) 12%, transparent);
}
.banner-retry {
  margin-left: auto;
  background: none;
  border: none;
  color: inherit;
  font-weight: 600;
  cursor: pointer;
}

.panel {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-surface);
  overflow: hidden;
}
.panel--loading {
  opacity: 0.7;
}
.panel-head {
  display: flex;
  justify-content: space-between;
  padding: 12px 18px;
  font-size: var(--text-label);
  font-weight: 600;
  color: var(--color-dim);
  border-bottom: 1px solid var(--color-border);
}
.panel-meta {
  color: var(--color-faint);
  font-weight: 500;
}

.skel-row {
  padding: 16px 18px;
}
.skel-bar {
  height: 16px;
  border-radius: 4px;
  background: linear-gradient(90deg, var(--color-border) 25%, transparent 50%, var(--color-border) 75%);
  background-size: 200% 100%;
  animation: skel 1.4s ease-in-out infinite;
}
@keyframes skel {
  0% {
    background-position: 200% 0;
  }
  100% {
    background-position: -200% 0;
  }
}

.empty-row {
  padding: 28px 18px;
  text-align: center;
  font-size: var(--text-label);
  color: var(--color-faint);
}

.server-list {
  list-style: none;
}
.server-row {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 16px;
  padding: 16px 18px;
  border-bottom: 1px solid var(--color-border);
}
.server-row:last-child {
  border-bottom: none;
}
.server-main {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}
.server-name {
  font-size: var(--text-body);
  font-weight: 600;
  color: var(--color-text);
}
.server-addr {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
  font-size: var(--text-label);
  color: var(--color-faint);
}
.mono {
  font-family: var(--font-mono, ui-monospace, monospace);
}
.cred-tag {
  color: var(--color-dim);
}
.group-tag {
  padding: 1px 8px;
  border: 1px solid var(--color-border);
  border-radius: 999px;
  font-size: 0.9em;
  line-height: 1.6;
  color: var(--color-dim);
  background: color-mix(in oklch, var(--color-text) 4%, transparent);
  white-space: nowrap;
}
.test-result {
  margin-top: 6px;
  font-size: var(--text-label);
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: baseline;
}
.test-result--ok {
  color: var(--color-success, #2e8b57);
}
.test-result--fail {
  color: var(--color-danger, #d4503e);
}
.uname {
  color: var(--color-faint);
  font-size: 0.85em;
  word-break: break-all;
}
.server-actions {
  display: flex;
  gap: 8px;
  flex-shrink: 0;
}

/* modal */
.modal-backdrop {
  position: fixed;
  inset: 0;
  background: color-mix(in oklch, var(--color-text) 40%, transparent);
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
  z-index: 100;
}
.modal {
  width: 100%;
  max-width: 440px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  padding: 24px;
  display: flex;
  flex-direction: column;
  gap: 16px;
  box-shadow: var(--shadow-lg, 0 24px 60px rgba(0, 0, 0, 0.24));
}
.modal--wide {
  max-width: min(960px, 92vw);
}
.logs-modal-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.logs-modal-sub {
  font-size: 0.78rem;
  color: var(--color-dim);
  margin-top: -8px;
}
.modal-title {
  font-size: var(--text-heading);
  font-weight: 600;
  color: var(--color-text);
}
.modal-text {
  font-size: var(--text-label);
  color: var(--color-dim);
  line-height: 1.5;
}
.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 4px;
}

.field {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-bottom: 14px;
}
.field-row {
  display: flex;
  gap: 12px;
}
.field-grow {
  flex: 1;
}
.field-port {
  width: 96px;
}
.field-label {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 6px;
  font-size: var(--text-label);
  font-weight: 500;
  color: var(--color-dim);
}
.field-input {
  padding: 8px 12px;
  font-size: var(--text-body);
  color: var(--color-text);
  background: var(--color-bg, var(--color-surface));
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  transition: border-color var(--duration-fast);
}
.field-input:focus {
  outline: none;
  border-color: var(--color-primary);
}
.field-input:disabled {
  opacity: 0.55;
  cursor: not-allowed;
}
.field-hint-inline {
  font-weight: 400;
  color: var(--color-faint);
}
.field-hint {
  font-size: var(--text-label);
  color: var(--color-faint);
  line-height: 1.45;
}
.field-error {
  font-size: var(--text-label);
  color: var(--color-danger, #d4503e);
}
.group-picker {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 6px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-bg, var(--color-surface));
  max-height: 220px;
  overflow-y: auto;
}
.group-picker--locked {
  opacity: 0.75;
}
.group-picker__item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 5px 6px;
  border-radius: var(--radius-sm);
  font-size: var(--text-body);
  cursor: pointer;
}
.group-picker__item:hover {
  background: color-mix(in oklch, var(--color-text) 5%, transparent);
}
.group-picker__item input:disabled {
  cursor: not-allowed;
}
.group-picker__name {
  color: var(--color-text);
}
.group-picker__vis {
  font-size: var(--text-label);
  color: var(--color-faint);
}
.group-picker__note {
  margin-left: auto;
  font-size: var(--text-label);
  color: var(--color-faint);
}
.group-picker__hint {
  margin: 4px 6px 2px;
  font-size: var(--text-label);
  color: var(--color-faint);
  line-height: 1.45;
}
</style>
