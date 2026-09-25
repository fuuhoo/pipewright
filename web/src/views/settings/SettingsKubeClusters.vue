<script setup lang="ts">
/**
 * 集群(Kubernetes)登记 —— K8s 发布那条腿的「目标」列表。
 *
 * 与「服务器」并列而非合并:一台服务器是「能 SSH 的机器」,一个集群是「一个 API server」,
 * 没有 host/user/shell。合并会让容器总览、主机终端这些机器专属页面开始防着集群行。
 * 这里只登记名称 + 一条 kubeconfig 凭据 + 默认命名空间;地址从凭据现读现展示(库里不存)。
 */
import { ref, computed, onMounted } from 'vue'
import {
  listKubeClusters,
  createKubeCluster,
  updateKubeCluster,
  deleteKubeCluster,
  testKubeCluster,
} from '../../api/kubeClusters'
import type {
  KubeCluster,
  CreateKubeClusterInput,
  UpdateKubeClusterInput,
  KubeTestResult,
} from '../../api/kubeClusters'
import { listCredentials, usableCredentials } from '../../api/credentials'
import type { Credential } from '../../api/credentials'
import { listGroups, type Group } from '../../api/groups'
import { useSessionStore } from '../../stores/session'
import { HttpError } from '../../api/http'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()
const sessionStore = useSessionStore()

type LoadState = 'idle' | 'loading' | 'error'

const loadState = ref<LoadState>('idle')
const loadError = ref('')
const clusters = ref<KubeCluster[]>([])
const kubeCredentials = ref<Credential[]>([])

const groups = ref<Group[]>([])
const groupById = computed<Record<string, Group>>(() => {
  const out: Record<string, Group> = {}
  for (const g of groups.value) out[g.id] = g
  return out
})
const manageableGroups = computed(() => groups.value.filter((g) => g.canManage))
const isAdminUser = computed(() => sessionStore.user?.role === 'admin')

function groupName(id: string): string {
  return id ? (groupById.value[id]?.name ?? t('groups.groupMissing')) : t('groups.ungrouped')
}

function canChangeGroup(c: KubeCluster): boolean {
  if (!c.groupId) return isAdminUser.value
  return groupById.value[c.groupId]?.canManage === true
}

const canAddCluster = computed(() => isAdminUser.value || manageableGroups.value.length > 0)
const hasKubeCredentials = computed(() => kubeCredentials.value.length > 0)

function credentialName(id: string): string {
  const c = kubeCredentials.value.find((x) => x.id === id)
  return c ? c.name : t('settingsKube.credentialDeleted')
}

function errText(err: unknown, keys: { conn: string; status: string; retry: string }): string {
  if (err instanceof HttpError) {
    if (err.status === 0) return t(keys.conn)
    // 后端的 message 已是人读文案(含「kubeconfig 不可用:缺 current-context」这类判定结论),
    // 且绝不带凭据正文,所以直接回显比前端再映射一遍更准。
    return err.apiError?.message ?? t(keys.status, { status: err.status })
  }
  return t(keys.retry)
}

const loadKeys = { conn: 'settingsKube.errLoadConn', status: 'settingsKube.errLoadStatus', retry: 'settingsKube.errLoadRetry' }
const saveKeys = { conn: 'settingsKube.errConnRetry', status: 'settingsKube.errSaveStatus', retry: 'settingsKube.errSaveRetry' }
const delKeys = { conn: 'settingsKube.errConnRetry', status: 'settingsKube.errDeleteStatus', retry: 'settingsKube.errDeleteRetry' }
const testKeys = { conn: 'settingsKube.errConnRetry', status: 'settingsKube.errTestStatus', retry: 'settingsKube.errTestRetry' }

async function loadAll(): Promise<void> {
  loadState.value = 'loading'
  loadError.value = ''
  try {
    const [list, creds, gs] = await Promise.all([listKubeClusters(), listCredentials(), listGroups()])
    clusters.value = list
    kubeCredentials.value = usableCredentials(creds).filter((c) => c.type === 'kubeconfig')
    groups.value = gs
    loadState.value = 'idle'
  } catch (err) {
    loadError.value = errText(err, loadKeys)
    loadState.value = 'error'
  }
}

onMounted(loadAll)

// ─── add / edit ──────────────────────────────────────────────────────────────

const modalOpen = ref(false)
const modalMode = ref<'add' | 'edit'>('add')
const editingId = ref<string | null>(null)
const formSubmitting = ref(false)
const formBanner = ref('')

const form = ref({ name: '', credentialId: '', namespaceDefault: '', groupId: '' })
const formErrors = ref({ name: '', credentialId: '', namespaceDefault: '', groupId: '' })

const editingCluster = computed(() =>
  editingId.value ? (clusters.value.find((c) => c.id === editingId.value) ?? null) : null,
)

const groupOptions = computed<Group[]>(() => {
  const cur = editingCluster.value?.groupId
  if (!cur) return manageableGroups.value
  const g = groupById.value[cur]
  if (!g || g.canManage) return manageableGroups.value
  return [g, ...manageableGroups.value]
})

const ungroupedSelectable = computed(() =>
  modalMode.value === 'add' ? isAdminUser.value : groupFieldEditable.value,
)

const groupFieldEditable = computed(() =>
  modalMode.value === 'add' ? canAddCluster.value : (editingCluster.value ? canChangeGroup(editingCluster.value) : false),
)

function clearFormErrors(): void {
  formErrors.value = { name: '', credentialId: '', namespaceDefault: '', groupId: '' }
}

function openAddModal(): void {
  modalMode.value = 'add'
  editingId.value = null
  form.value = {
    name: '',
    credentialId: kubeCredentials.value[0]?.id ?? '',
    namespaceDefault: '',
    groupId: isAdminUser.value ? '' : (manageableGroups.value[0]?.id ?? ''),
  }
  clearFormErrors()
  formBanner.value = ''
  modalOpen.value = true
}

function openEditModal(c: KubeCluster): void {
  modalMode.value = 'edit'
  editingId.value = c.id
  form.value = {
    name: c.name,
    credentialId: c.credentialId,
    namespaceDefault: c.namespaceDefault,
    groupId: c.groupId,
  }
  clearFormErrors()
  formBanner.value = ''
  modalOpen.value = true
}

function closeModal(): void {
  if (formSubmitting.value) return
  modalOpen.value = false
}

// 与后端同口径:命名空间是 DNS-label(小写字母/数字/-,≤63),留空表示不设默认。
const reNamespace = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/

function validateForm(): boolean {
  clearFormErrors()
  let ok = true
  if (!form.value.name.trim()) {
    formErrors.value.name = t('settingsKube.errNameRequired')
    ok = false
  }
  if (!form.value.credentialId) {
    formErrors.value.credentialId = t('settingsKube.errCredentialRequired')
    ok = false
  }
  const ns = form.value.namespaceDefault.trim()
  if (ns && (ns.length > 63 || !reNamespace.test(ns))) {
    formErrors.value.namespaceDefault = t('settingsKube.errNamespace')
    ok = false
  }
  if (modalMode.value === 'add' && !isAdminUser.value && !form.value.groupId) {
    formErrors.value.groupId = t('settingsKube.errGroupRequired')
    ok = false
  }
  return ok
}

async function handleFormSubmit(): Promise<void> {
  if (!validateForm()) return
  formSubmitting.value = true
  formBanner.value = ''
  try {
    if (modalMode.value === 'add') {
      const payload: CreateKubeClusterInput = {
        name: form.value.name.trim(),
        credentialId: form.value.credentialId,
        namespaceDefault: form.value.namespaceDefault.trim(),
        groupId: form.value.groupId,
      }
      clusters.value = [await createKubeCluster(payload), ...clusters.value]
    } else if (editingId.value) {
      const payload: UpdateKubeClusterInput = {
        name: form.value.name.trim(),
        credentialId: form.value.credentialId,
        namespaceDefault: form.value.namespaceDefault.trim(),
      }
      const origin = editingCluster.value
      if (origin && form.value.groupId !== origin.groupId) payload.groupId = form.value.groupId
      const updated = await updateKubeCluster(editingId.value, payload)
      clusters.value = clusters.value.map((c) => (c.id === updated.id ? updated : c))
      delete testResults.value[updated.id]
      if (payload.groupId !== undefined && !groupById.value[payload.groupId]?.canManage) await loadAll()
    }
    modalOpen.value = false
  } catch (err) {
    formBanner.value = errText(err, saveKeys)
  } finally {
    formSubmitting.value = false
  }
}

// ─── delete ──────────────────────────────────────────────────────────────────

const deleteModalOpen = ref(false)
const deletingCluster = ref<KubeCluster | null>(null)
const deleteSubmitting = ref(false)
const deleteBanner = ref('')

function openDeleteModal(c: KubeCluster): void {
  deletingCluster.value = c
  deleteBanner.value = ''
  deleteModalOpen.value = true
}

function closeDeleteModal(): void {
  if (deleteSubmitting.value) return
  deleteModalOpen.value = false
  deletingCluster.value = null
}

async function confirmDelete(): Promise<void> {
  if (!deletingCluster.value) return
  deleteSubmitting.value = true
  deleteBanner.value = ''
  const id = deletingCluster.value.id
  try {
    await deleteKubeCluster(id)
    clusters.value = clusters.value.filter((c) => c.id !== id)
    delete testResults.value[id]
    deleteModalOpen.value = false
    deletingCluster.value = null
  } catch (err) {
    deleteBanner.value = errText(err, delKeys)
  } finally {
    deleteSubmitting.value = false
  }
}

// ─── test connection ─────────────────────────────────────────────────────────

const testingId = ref<string | null>(null)
const testResults = ref<Record<string, KubeTestResult>>({})

async function handleTest(c: KubeCluster): Promise<void> {
  testingId.value = c.id
  try {
    testResults.value = { ...testResults.value, [c.id]: await testKubeCluster(c.id) }
  } catch (err) {
    testResults.value = {
      ...testResults.value,
      [c.id]: { ok: false, latencyMs: 0, output: '', error: errText(err, testKeys) },
    }
  } finally {
    if (testingId.value === c.id) testingId.value = null
  }
}
</script>

<template>
  <div class="kube-root">
    <div class="section-head">
      <div class="section-head-text">
        <h2 class="section-title">{{ t('settingsKube.title') }}</h2>
        <p class="section-desc">{{ t('settingsKube.desc') }}</p>
      </div>
      <button
        class="btn-primary"
        :disabled="loadState === 'loading' || !hasKubeCredentials || !canAddCluster"
        :title="!canAddCluster ? t('settingsKube.noManageGroupHint') : (!hasKubeCredentials ? t('settingsKube.addDisabledHint') : '')"
        @click="openAddModal"
      >
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" aria-hidden="true">
          <path d="M12 5v14M5 12h14" />
        </svg>
        {{ t('settingsKube.addCluster') }}
      </button>
    </div>

    <div v-if="loadState === 'idle' && !hasKubeCredentials" class="banner banner--warn" role="status">
      <span>{{ t('settingsKube.noCredentialHint') }}</span>
    </div>

    <div v-if="loadState === 'error'" class="banner banner--error" role="alert">
      <span>{{ loadError }}</span>
      <button class="banner-retry" @click="loadAll">↻ {{ t('settingsKube.retry') }}</button>
    </div>

    <div class="panel" :class="{ 'panel--loading': loadState === 'loading' }">
      <div class="panel-head">
        <span>{{ t('settingsKube.registeredClusters') }}</span>
        <span v-if="loadState === 'idle'" class="panel-meta">{{ t('settingsKube.clusterCount', { n: clusters.length }) }}</span>
      </div>

      <template v-if="loadState === 'loading'">
        <div v-for="i in 3" :key="i" class="skel-row"><div class="skel-bar" /></div>
      </template>

      <template v-else-if="loadState === 'idle' && clusters.length === 0">
        <div class="empty-row" role="status">{{ t('settingsKube.emptyList') }}</div>
      </template>

      <ul v-else class="cluster-list">
        <li v-for="c in clusters" :key="c.id" class="cluster-row">
          <div class="cluster-main">
            <div class="cluster-name">{{ c.name }}</div>
            <div class="cluster-meta">
              <span class="mono">{{ c.endpoint || t('settingsKube.endpointUnknown') }}</span>
              <span v-if="c.namespaceDefault" class="ns-tag">{{ c.namespaceDefault }}</span>
              <span class="cred-tag">🔑 {{ credentialName(c.credentialId) }}</span>
              <span class="group-tag" :title="t('groups.fieldGroupHint')">{{ groupName(c.groupId) }}</span>
            </div>
            <div
              v-if="testResults[c.id]"
              class="test-result"
              :class="testResults[c.id].ok ? 'test-result--ok' : 'test-result--fail'"
              role="status"
            >
              <template v-if="testResults[c.id].ok">
                ✓ {{ t('settingsKube.testOk', { ms: testResults[c.id].latencyMs }) }}
                <span class="mono">{{ testResults[c.id].output }}</span>
              </template>
              <template v-else>✕ {{ testResults[c.id].error }}</template>
            </div>
          </div>
          <div class="cluster-actions">
            <button class="btn-ghost" :disabled="testingId === c.id" @click="handleTest(c)">
              {{ testingId === c.id ? t('settingsKube.testing') : t('settingsKube.testConnection') }}
            </button>
            <button class="btn-ghost" @click="openEditModal(c)">{{ t('settingsKube.edit') }}</button>
            <button class="btn-ghost btn-danger" @click="openDeleteModal(c)">{{ t('settingsKube.delete') }}</button>
          </div>
        </li>
      </ul>
    </div>

    <div v-if="modalOpen" class="modal-backdrop" @click.self="closeModal">
      <div class="modal" role="dialog" aria-modal="true" aria-labelledby="kube-modal-title">
        <h3 id="kube-modal-title" class="modal-title">
          {{ modalMode === 'add' ? t('settingsKube.addCluster') : t('settingsKube.editCluster') }}
        </h3>

        <div v-if="formBanner" class="banner banner--error" role="alert">{{ formBanner }}</div>

        <form @submit.prevent="handleFormSubmit">
          <label class="field">
            <span class="field-label">{{ t('settingsKube.fieldName') }}</span>
            <input v-model="form.name" class="field-input" type="text" placeholder="prod-cn-1" autocomplete="off" />
            <span v-if="formErrors.name" class="field-error">{{ formErrors.name }}</span>
          </label>

          <label class="field">
            <span class="field-label">{{ t('settingsKube.fieldCredential') }}</span>
            <select v-model="form.credentialId" class="field-input">
              <option value="" disabled>{{ t('settingsKube.selectCredential') }}</option>
              <option v-for="c in kubeCredentials" :key="c.id" :value="c.id">{{ c.name }}</option>
            </select>
            <span v-if="formErrors.credentialId" class="field-error">{{ formErrors.credentialId }}</span>
            <span v-else class="field-hint">{{ t('settingsKube.credentialHint') }}</span>
          </label>

          <label class="field">
            <span class="field-label">{{ t('settingsKube.fieldNamespace') }}</span>
            <input v-model="form.namespaceDefault" class="field-input" type="text" placeholder="default" autocomplete="off" />
            <span v-if="formErrors.namespaceDefault" class="field-error">{{ formErrors.namespaceDefault }}</span>
            <span v-else class="field-hint">{{ t('settingsKube.namespaceHint') }}</span>
          </label>

          <label class="field">
            <span class="field-label">
              {{ t('groups.fieldGroup') }}
              <span class="field-hint-inline">{{ t('groups.fieldGroupHint') }}</span>
            </span>
            <select v-model="form.groupId" class="field-input" :disabled="formSubmitting || !groupFieldEditable">
              <option v-if="ungroupedSelectable" value="">{{ t('groups.ungrouped') }}</option>
              <option v-for="g in groupOptions" :key="g.id" :value="g.id">
                {{ g.name }} · {{ g.visibility === 'public' ? t('groups.visibilityPublic') : t('groups.visibilityPrivate') }}
              </option>
            </select>
            <span v-if="formErrors.groupId" class="field-error">{{ formErrors.groupId }}</span>
            <span v-else-if="!groupFieldEditable" class="field-hint">{{ t('groups.lockedHint') }}</span>
          </label>

          <div class="modal-actions">
            <button type="button" class="btn-ghost" :disabled="formSubmitting" @click="closeModal">{{ t('settingsKube.cancel') }}</button>
            <button type="submit" class="btn-primary" :disabled="formSubmitting">
              {{ formSubmitting ? t('settingsKube.saving') : t('settingsKube.save') }}
            </button>
          </div>
        </form>
      </div>
    </div>

    <div v-if="deleteModalOpen" class="modal-backdrop" @click.self="closeDeleteModal">
      <div class="modal" role="dialog" aria-modal="true" aria-labelledby="kube-del-title">
        <h3 id="kube-del-title" class="modal-title">{{ t('settingsKube.deleteCluster') }}</h3>
        <div v-if="deleteBanner" class="banner banner--error" role="alert">{{ deleteBanner }}</div>
        <p class="modal-text">
          <i18n-t keypath="settingsKube.deleteConfirm" tag="span">
            <template #name><strong>{{ deletingCluster?.name }}</strong></template>
          </i18n-t>
        </p>
        <div class="modal-actions">
          <button type="button" class="btn-ghost" :disabled="deleteSubmitting" @click="closeDeleteModal">{{ t('settingsKube.cancel') }}</button>
          <button type="button" class="btn-primary btn-danger" :disabled="deleteSubmitting" @click="confirmDelete">
            {{ deleteSubmitting ? t('settingsKube.deleting') : t('settingsKube.confirmDelete') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.kube-root {
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
  color: var(--color-dim);
  background: transparent;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  cursor: pointer;
}
.btn-ghost:hover:not(:disabled) {
  color: var(--color-text);
  border-color: var(--color-primary);
}
.btn-ghost:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.btn-danger {
  color: var(--color-danger, #d43c3c);
}
.banner {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 14px;
  border-radius: var(--radius-sm);
  font-size: var(--text-label);
  line-height: 1.5;
}
.banner--warn {
  background: var(--color-warn-soft, rgba(212, 160, 23, 0.12));
  color: var(--color-text);
}
.banner--error {
  background: var(--color-danger-soft, rgba(212, 60, 60, 0.12));
  color: var(--color-text);
}
.banner-retry {
  margin-left: auto;
  padding: 4px 10px;
  font-size: var(--text-label);
  background: transparent;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  cursor: pointer;
  color: var(--color-dim);
}
.panel {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface, transparent);
}
.panel-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 12px 16px;
  border-bottom: 1px solid var(--color-border);
  font-size: var(--text-label);
  font-weight: 600;
  color: var(--color-text);
}
.panel-meta {
  font-weight: 400;
  color: var(--color-faint);
}
.empty-row {
  padding: 28px 16px;
  text-align: center;
  font-size: var(--text-label);
  color: var(--color-faint);
}
.skel-row {
  padding: 14px 16px;
}
.skel-bar {
  height: 14px;
  border-radius: var(--radius-sm);
  background: var(--color-border);
  opacity: 0.6;
}
.cluster-list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.cluster-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 16px;
  padding: 14px 16px;
  border-top: 1px solid var(--color-border);
}
.cluster-row:first-child {
  border-top: none;
}
.cluster-name {
  font-size: var(--text-label);
  font-weight: 600;
  color: var(--color-text);
}
.cluster-meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin-top: 4px;
  font-size: var(--text-label);
  color: var(--color-faint);
}
.mono {
  font-family: var(--font-mono, ui-monospace, monospace);
}
.ns-tag,
.cred-tag,
.group-tag {
  padding: 1px 7px;
  border: 1px solid var(--color-border);
  border-radius: 999px;
  font-size: 12px;
}
.test-result {
  margin-top: 6px;
  font-size: var(--text-label);
}
.test-result--ok {
  color: var(--color-ok, #1f9254);
}
.test-result--fail {
  color: var(--color-danger, #d43c3c);
}
.cluster-actions {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 6px;
}
.modal-backdrop {
  position: fixed;
  inset: 0;
  z-index: 60;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
  background: rgba(0, 0, 0, 0.45);
}
.modal {
  width: min(520px, 100%);
  max-height: 85vh;
  overflow-y: auto;
  padding: 20px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-bg, #fff);
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.modal-title {
  margin: 0;
  font-size: var(--text-heading);
  font-weight: 600;
  color: var(--color-text);
}
.modal-text {
  margin: 0;
  font-size: var(--text-label);
  color: var(--color-dim);
  line-height: 1.6;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 5px;
  margin-bottom: 12px;
}
.field-label {
  font-size: var(--text-label);
  font-weight: 600;
  color: var(--color-text);
}
.field-hint-inline {
  margin-left: 6px;
  font-weight: 400;
  color: var(--color-faint);
}
.field-input {
  padding: 7px 10px;
  font-size: var(--text-label);
  color: var(--color-text);
  background: var(--color-bg, #fff);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
}
.field-error {
  font-size: 12px;
  color: var(--color-danger, #d43c3c);
}
.field-hint {
  font-size: 12px;
  color: var(--color-faint);
  line-height: 1.5;
}
.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 4px;
}
</style>
