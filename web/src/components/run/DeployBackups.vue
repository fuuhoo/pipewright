<!--
  DeployBackups.vue — 部署前备份的列表与「覆盖式恢复」(运行详情 · 部署目标)

  入口在 DeployTargets 的目标卡上(仅当该次部署开了备份)。本组件按 (runId, serverId)
  拉取该目标机上的备份,选中一份 → 二次确认 → 恢复。

  部署目录与重启命令全部由服务端从该次部署记录里取,前端从不回传命令:
  恢复本质是在目标机上跑一段脚本,不把「跑什么」交给浏览器。
  恢复成功后 emit restored,由 RunDetail 刷新 targets(恢复是一次新动作,不改历史部署结果)。
-->
<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { HttpError } from '../../api/http'
import {
  listDeployBackups,
  restoreDeployBackup,
  type DeployTarget,
  type TargetBackup,
} from '../../api/runs'

const { t } = useI18n()

const props = defineProps<{
  runId: string
  target: DeployTarget
  // 无操作权限时只读:能看有哪些备份,但恢复按钮禁用(写操作要 Operate 权限)。
  canOperate?: boolean
}>()

const emit = defineEmits<{ close: []; restored: [] }>()

const loading = ref(false)
const loadError = ref('')
const backups = ref<TargetBackup[]>([])
const deployPath = ref('')
const mode = ref('')

// 二次确认:恢复会覆盖部署目录,点一下不能直接执行。
const confirmId = ref('')
const restoring = ref('')
const actionError = ref('')
const result = ref<{ ok: boolean; message: string } | null>(null)

async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    const res = await listDeployBackups(props.runId, props.target.serverId)
    backups.value = res.backups
    deployPath.value = res.deployPath
    mode.value = res.mode
  } catch (err) {
    loadError.value =
      err instanceof HttpError
        ? (err.apiError?.message ?? t('run.backupsLoadFailed', { status: err.status }))
        : t('run.backupsLoadFailedRetry')
  } finally {
    loading.value = false
  }
}

async function doRestore(b: TargetBackup): Promise<void> {
  restoring.value = b.id
  confirmId.value = ''
  actionError.value = ''
  result.value = null
  try {
    const res = await restoreDeployBackup(props.runId, props.target.serverId, b.id)
    result.value = { ok: res.ok, message: res.message }
    if (res.ok) emit('restored')
  } catch (err) {
    actionError.value =
      err instanceof HttpError
        ? (err.apiError?.message ?? t('run.backupRestoreFailed', { status: err.status }))
        : t('run.backupRestoreFailedRetry')
  } finally {
    restoring.value = ''
  }
}

function sizeText(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '—'
  const units = ['B', 'KB', 'MB', 'GB']
  let v = bytes
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${i === 0 ? v : v.toFixed(1)} ${units[i]}`
}

function timeText(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString('zh-CN', {
    year: 'numeric', month: 'numeric', day: 'numeric',
    hour: '2-digit', minute: '2-digit', second: '2-digit',
  })
}

function modeLabel(m: string): string {
  if (m === 'dir') return t('run.backupsModeDir')
  if (m === 'suffix') return t('run.backupsModeSuffix')
  return t('run.backupsModeUnknown')
}

watch(
  () => [props.runId, props.target.serverId],
  () => {
    confirmId.value = ''
    result.value = null
    actionError.value = ''
    void load()
  },
  { immediate: true },
)
</script>

<template>
  <section class="deploy-backups" role="region" :aria-label="t('run.deployBackupsAria')">
    <header class="db-head">
      <div class="db-head-title">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true">
          <path d="M3 7a9 3 0 1 0 18 0A9 3 0 1 0 3 7" />
          <path d="M3 7v10c0 1.7 4 3 9 3s9-1.3 9-3V7" />
        </svg>
        <span class="db-title">{{ t('run.deployBackups') }}</span>
        <span class="db-server" :title="target.serverName">{{ target.serverName }}</span>
        <span v-if="mode" class="db-mode">{{ modeLabel(mode) }}</span>
      </div>
      <button class="db-close" type="button" :aria-label="t('run.backupsClose')" @click="emit('close')">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
          <path d="M18 6 6 18M6 6l12 12" />
        </svg>
      </button>
    </header>

    <p v-if="deployPath" class="db-path">
      <span class="db-path-key">{{ t('run.backupsDeployPath') }}</span>
      <span class="mono">{{ deployPath }}</span>
    </p>

    <div v-if="loading" class="db-state" aria-busy="true">
      <span class="db-spinner" aria-hidden="true" />
      <span>{{ t('run.backupsLoading') }}</span>
    </div>

    <div v-else-if="loadError" class="db-state db-state--error" role="alert">
      <span>{{ loadError }}</span>
      <button class="db-link" type="button" @click="load">{{ t('runDetail.retry') }}</button>
    </div>

    <p v-else-if="backups.length === 0" class="db-empty">{{ t('run.backupsEmpty') }}</p>

    <ul v-else class="db-list" role="list">
      <li v-for="b in backups" :key="b.id" class="db-row" :aria-label="t('run.backupRowAria', { id: b.id })">
        <div class="db-row-main">
          <span class="db-row-time mono">{{ timeText(b.createdAt) }}</span>
          <span class="db-row-size">{{ t('run.backupSize', { size: sizeText(b.sizeBytes) }) }}</span>
          <span class="db-row-id mono" :title="b.id">{{ b.id }}</span>
          <button
            v-if="confirmId !== b.id"
            class="db-restore"
            type="button"
            :disabled="!canOperate || restoring !== ''"
            @click="confirmId = b.id"
          >
            {{ restoring === b.id ? t('run.backupRestoring') : t('run.backupRestore') }}
          </button>
        </div>

        <div v-if="confirmId === b.id" class="db-confirm">
          <p class="db-confirm-title">{{ t('run.backupConfirmTitle') }}</p>
          <p class="db-confirm-sub">{{ t('run.backupConfirmSub') }}</p>
          <div class="db-confirm-actions">
            <button class="db-cancel" type="button" @click="confirmId = ''">{{ t('run.backupCancel') }}</button>
            <button
              class="db-restore db-restore--danger"
              type="button"
              :disabled="!canOperate || restoring !== ''"
              @click="doRestore(b)"
            >
              <span v-if="restoring === b.id" class="db-spinner" aria-hidden="true" />
              {{ t('run.backupConfirm') }}
            </button>
          </div>
        </div>
      </li>
    </ul>

    <p v-if="actionError" class="db-alert" role="alert">{{ actionError }}</p>
    <p v-if="result" class="db-alert" :class="result.ok ? 'db-alert--ok' : 'db-alert--bad'" role="status">
      <strong>{{ result.ok ? t('run.backupRestoreOk') : t('run.backupRestoreNotOk') }}</strong>
      {{ result.message }}
    </p>
  </section>
</template>

<style scoped>
.deploy-backups {
  border: 1px solid var(--color-border);
  border-radius: var(--rounded);
  background: var(--color-card);
  overflow: hidden;
}

.db-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 11px 16px;
  background: var(--color-inset);
  border-bottom: 1px solid var(--color-border);
}

.db-head-title {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  color: var(--color-text);
}

.db-title {
  font-size: 0.84rem;
  font-weight: 600;
  letter-spacing: -0.01em;
}

.db-server {
  font-size: 0.78rem;
  color: var(--color-dim);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 240px;
}

.db-mode {
  font-size: 0.7rem;
  font-weight: 600;
  color: var(--color-accent);
  background: var(--color-accent-soft, var(--color-card-2));
  padding: 1px 8px;
  border-radius: var(--rounded-sm);
  white-space: nowrap;
}

.db-close {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 26px;
  height: 26px;
  border: 1px solid transparent;
  border-radius: var(--rounded-sm);
  background: transparent;
  color: var(--color-faint);
  cursor: pointer;
}

.db-close:hover {
  color: var(--color-text);
  border-color: var(--color-border);
}

.db-path {
  display: flex;
  gap: 8px;
  align-items: baseline;
  padding: 9px 16px;
  font-size: 0.76rem;
  color: var(--color-dim);
  border-bottom: 1px dashed var(--color-border);
  word-break: break-all;
}

.db-path-key {
  font-size: 0.68rem;
  font-weight: 600;
  letter-spacing: 0.04em;
  text-transform: uppercase;
  color: var(--color-faint);
}

.db-state,
.db-empty {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 14px 16px;
  font-size: 0.8rem;
  color: var(--color-faint);
}

.db-state--error {
  color: var(--color-red);
}

.db-link {
  border: none;
  background: none;
  color: var(--color-accent);
  font-size: 0.78rem;
  font-weight: 600;
  cursor: pointer;
  padding: 0;
  text-decoration: underline;
}

.db-list {
  list-style: none;
  display: flex;
  flex-direction: column;
}

.db-row {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 10px 16px;
  border-bottom: 1px solid var(--color-border);
}

.db-row:last-child {
  border-bottom: none;
}

.db-row-main {
  display: flex;
  align-items: center;
  gap: 14px;
  flex-wrap: wrap;
}

.db-row-time {
  font-size: 0.78rem;
  color: var(--color-text);
}

.db-row-size {
  font-size: 0.74rem;
  color: var(--color-faint);
}

.db-row-id {
  font-size: 0.7rem;
  color: var(--color-faint);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 260px;
}

.db-restore {
  margin-left: auto;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 5px 12px;
  font-family: var(--font-sans);
  font-size: 0.76rem;
  font-weight: 600;
  color: var(--color-accent);
  background: var(--color-card-2);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--rounded-md);
  cursor: pointer;
}

.db-restore:hover:not(:disabled) {
  border-color: var(--color-accent);
}

.db-restore:disabled {
  opacity: 0.55;
  cursor: not-allowed;
}

.db-restore--danger {
  color: var(--color-red);
  border-color: var(--color-red-line);
  background: var(--color-red-soft);
}

.db-confirm {
  background: var(--color-amber-soft);
  border: 1px solid var(--color-amber-line);
  border-radius: var(--rounded-md);
  padding: 10px 12px;
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.db-confirm-title {
  font-size: 0.8rem;
  font-weight: 600;
  color: var(--color-amber);
}

.db-confirm-sub {
  font-size: 0.76rem;
  color: var(--color-dim);
  line-height: 1.5;
}

.db-confirm-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}

.db-cancel {
  padding: 5px 12px;
  font-family: var(--font-sans);
  font-size: 0.76rem;
  color: var(--color-dim);
  background: transparent;
  border: 1px solid var(--color-border-strong);
  border-radius: var(--rounded-md);
  cursor: pointer;
}

.db-alert {
  margin: 0;
  padding: 9px 16px;
  font-size: 0.78rem;
  border-top: 1px solid var(--color-border);
  color: var(--color-red);
  background: var(--color-red-soft);
  word-break: break-word;
}

.db-alert--ok {
  color: var(--color-green);
  background: var(--color-green-soft);
}

.db-spinner {
  width: 12px;
  height: 12px;
  border: 2px solid currentColor;
  border-top-color: transparent;
  border-radius: var(--rounded-full);
  animation: db-spin 0.7s linear infinite;
}

@keyframes db-spin {
  to { transform: rotate(360deg); }
}

@media (prefers-reduced-motion: reduce) {
  .db-spinner { animation-duration: 1.6s; }
}

.mono { font-family: var(--font-mono); }
</style>
