<script setup lang="ts">
/**
 * v6.2 审计日志(admin-only,只读)。
 *
 * 后端 audit_log 是 append-only(存储层硬拦 UPDATE/DELETE);这里只做
 * 游标分页 + action 过滤。v6.2 新增的动作(build_env_* / config_profile_* /
 * user_* / credential_disable)一并列入过滤下拉。
 */
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { listAudit } from '../../api/audit'
import type { AuditEntry } from '../../api/audit'
import { HttpError } from '../../api/http'

const { t } = useI18n()

const loadState = ref<'idle' | 'loading' | 'error'>('idle')
const loadError = ref('')
const entries = ref<AuditEntry[]>([])
const nextBefore = ref<string | null>(null)
const loadingMore = ref(false)
const actionFilter = ref('')

/** 过滤下拉:后端 audit.Action 全量(v6.2 §七 阶段 7 扩展后)。 */
const ACTION_OPTIONS: Array<{ value: string; label: string }> = [
  { value: '', label: '' },
  { value: 'credential_create', label: 'credential_create' },
  { value: 'credential_update', label: 'credential_update' },
  { value: 'credential_delete', label: 'credential_delete' },
  { value: 'credential_reveal', label: 'credential_reveal' },
  { value: 'credential_disable', label: 'credential_disable' },
  { value: 'project_create', label: 'project_create' },
  { value: 'project_update', label: 'project_update' },
  { value: 'project_delete', label: 'project_delete' },
  { value: 'run_trigger_manual', label: 'run_trigger_manual' },
  { value: 'build_env_create', label: 'build_env_create' },
  { value: 'build_env_update', label: 'build_env_update' },
  { value: 'build_env_delete', label: 'build_env_delete' },
  { value: 'build_env_toggle', label: 'build_env_toggle' },
  { value: 'build_env_check', label: 'build_env_check' },
  { value: 'build_env_pull', label: 'build_env_pull' },
  { value: 'config_profile_create', label: 'config_profile_create' },
  { value: 'config_profile_update', label: 'config_profile_update' },
  { value: 'config_profile_delete', label: 'config_profile_delete' },
  { value: 'config_profile_upload', label: 'config_profile_upload' },
  { value: 'user_login', label: 'user_login' },
  { value: 'password_change', label: 'password_change' },
  { value: 'session_revoke', label: 'session_revoke' },
]

async function load(): Promise<void> {
  loadState.value = 'loading'
  loadError.value = ''
  try {
    const res = await listAudit({
      limit: 50,
      action: actionFilter.value || undefined,
    })
    entries.value = res.entries
    nextBefore.value = res.nextBefore
    loadState.value = 'idle'
  } catch (err) {
    loadError.value =
      err instanceof HttpError
        ? err.status === 0
          ? t('adminUsers.errAuditLoadConn')
          : (err.apiError?.message ?? t('adminUsers.errAuditLoad'))
        : t('adminUsers.errAuditLoad')
    loadState.value = 'error'
  }
}

onMounted(load)

async function loadMore(): Promise<void> {
  if (!nextBefore.value) return
  loadingMore.value = true
  try {
    const res = await listAudit({
      limit: 50,
      before: nextBefore.value,
      action: actionFilter.value || undefined,
    })
    entries.value.push(...res.entries)
    nextBefore.value = res.nextBefore
  } catch {
    // 分页失败保留已有数据;用户可再点一次。
  } finally {
    loadingMore.value = false
  }
}

function fmtTime(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString()
}

function detailText(e: AuditEntry): string {
  const keys = Object.keys(e.detail)
  if (keys.length === 0) return '—'
  return keys
    .map((k) => `${k}=${typeof e.detail[k] === 'object' ? JSON.stringify(e.detail[k]) : String(e.detail[k])}`)
    .join(' · ')
}
</script>

<template>
  <div class="audit-view">
    <header class="view-header">
      <div>
        <h1 class="view-title">{{ t('adminUsers.auditTitle') }}</h1>
        <p class="view-sub">{{ t('adminUsers.auditDesc') }}</p>
      </div>
      <div class="header-actions">
        <label class="filter">
          <span>{{ t('adminUsers.filterAction') }}</span>
          <select v-model="actionFilter" @change="load">
            <option value="">{{ t('adminUsers.filterAll') }}</option>
            <option v-for="o in ACTION_OPTIONS.filter((x) => x.value)" :key="o.value" :value="o.value">
              {{ o.value }}
            </option>
          </select>
        </label>
      </div>
    </header>

    <p v-if="loadState === 'loading'" class="state">{{ t('common.refresh') }}…</p>
    <div v-else-if="loadState === 'error'" class="state state--error">
      <p>{{ loadError }}</p>
      <button class="btn" @click="load">{{ t('common.refresh') }}</button>
    </div>
    <p v-else-if="entries.length === 0" class="state">{{ t('adminUsers.auditEmpty') }}</p>

    <table v-else class="grid">
      <thead>
        <tr>
          <th>{{ t('adminUsers.colTime') }}</th>
          <th>{{ t('adminUsers.colActor') }}</th>
          <th>{{ t('adminUsers.colAction') }}</th>
          <th>{{ t('adminUsers.colTarget') }}</th>
          <th>{{ t('adminUsers.colIp') }}</th>
          <th>{{ t('adminUsers.colDetail') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="e in entries" :key="e.id">
          <td class="nowrap">{{ fmtTime(e.timestamp) }}</td>
          <td class="mono">{{ e.actor }}</td>
          <td><code class="mono">{{ e.action }}</code></td>
          <td class="mono mono--sm">{{ e.targetType }}/{{ e.targetId || '—' }}</td>
          <td class="mono mono--sm">{{ e.ip || '—' }}</td>
          <td class="detail">{{ detailText(e) }}</td>
        </tr>
      </tbody>
    </table>

    <div v-if="entries.length > 0" class="more">
      <button v-if="nextBefore" class="btn" :disabled="loadingMore" @click="loadMore">
        {{ loadingMore ? t('common.refresh') + '…' : t('adminUsers.loadMore') }}
      </button>
      <span v-else class="end">{{ t('adminUsers.noMore') }}</span>
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
  max-width: 76ch;
}
.filter {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: var(--text-label);
  color: var(--color-dim);
  flex-shrink: 0;
}
.filter select {
  padding: 7px 10px;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-bg, #fff);
  color: var(--color-text);
  font-size: var(--text-label);
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
  padding: 10px 12px;
  border-bottom: 1px solid var(--color-border);
  vertical-align: top;
}
.nowrap {
  white-space: nowrap;
}
.mono {
  font-family: var(--font-mono, monospace);
  font-size: var(--text-small, 0.85em);
}
.mono--sm {
  opacity: 0.8;
}
.detail {
  font-size: var(--text-small, 0.85em);
  color: var(--color-dim);
  word-break: break-all;
  max-width: 40ch;
}
.more {
  display: flex;
  justify-content: center;
  padding: 16px;
}
.end {
  color: var(--color-faint);
  font-size: var(--text-small, 0.85em);
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
.btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
</style>
