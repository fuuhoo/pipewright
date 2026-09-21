<script setup lang="ts">
/**
 * v6.2 全局凭据管理(admin-only)。
 *
 * 与「设置 → 凭据保险库」的分工:那边管**创建/轮换/删除/查看明文**(密文操作),
 * 本页聚焦 v6.2 §3.4 的管理面:
 *   - 列出全部凭据的元数据(含 personal 的 owner 视角,后端当前未返回 owner_id,
 *     故只显示 scope 与创建/使用时间)
 *   - 禁用违规的 personal 凭据(POST /api/admin/credentials/:id/disable)
 *   - 快速跳转到保险库做密文级操作
 *
 * 约束:禁用只改元数据(enabled=0),不动密文、不删除;global 凭据不可被 disable。
 */
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { listCredentials, disableCredential } from '../../api/credentials'
import type { Credential } from '../../api/credentials'
import { HttpError } from '../../api/http'

const { t } = useI18n()
const router = useRouter()

const loadState = ref<'idle' | 'loading' | 'error'>('idle')
const loadError = ref('')
const credentials = ref<Credential[]>([])
const banner = ref('')
const busyId = ref<string | null>(null)

async function load(): Promise<void> {
  loadState.value = 'loading'
  loadError.value = ''
  try {
    credentials.value = await listCredentials()
    loadState.value = 'idle'
  } catch (err) {
    // vault 未配置(无 master key)是合法状态:页面给出引导而非报错。
    if (err instanceof HttpError && err.apiError?.code === 'vault_unconfigured') {
      loadError.value = t('adminCredentials.vaultUnconfigured')
    } else if (err instanceof HttpError && err.status === 0) {
      loadError.value = t('adminCredentials.errConn')
    } else if (err instanceof HttpError) {
      loadError.value = err.apiError?.message ?? t('adminCredentials.errLoad')
    } else {
      loadError.value = t('adminCredentials.errLoad')
    }
    loadState.value = 'error'
  }
}

onMounted(load)

function fmtTime(iso: string | null): string {
  if (!iso) return t('adminCredentials.never')
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString()
}

async function onDisable(c: Credential): Promise<void> {
  busyId.value = c.id
  banner.value = ''
  try {
    await disableCredential(c.id)
    banner.value = t('adminCredentials.disableOk', { name: c.name })
    await load()
  } catch (err) {
    banner.value =
      err instanceof HttpError
        ? (err.apiError?.message ?? t('adminCredentials.errDisable'))
        : t('adminCredentials.errDisable')
  } finally {
    busyId.value = null
  }
}

function goVault(): void {
  void router.push({ name: 'settings-vault' })
}
</script>

<template>
  <div class="creds-view">
    <header class="view-header">
      <div>
        <h1 class="view-title">{{ t('adminCredentials.title') }}</h1>
        <p class="view-sub">{{ t('adminCredentials.desc') }}</p>
      </div>
      <div class="header-actions">
        <button class="btn" @click="goVault">{{ t('adminCredentials.openVault') }}</button>
      </div>
    </header>

    <p v-if="banner" class="banner" role="status">{{ banner }}</p>

    <p v-if="loadState === 'loading'" class="state">{{ t('common.refresh') }}…</p>
    <div v-else-if="loadState === 'error'" class="state state--error">
      <p>{{ loadError }}</p>
      <div class="state-actions">
        <button class="btn" @click="load">{{ t('common.refresh') }}</button>
        <button class="btn" @click="goVault">{{ t('adminCredentials.openVault') }}</button>
      </div>
    </div>
    <p v-else-if="credentials.length === 0" class="state">{{ t('adminCredentials.empty') }}</p>

    <table v-else class="grid">
      <thead>
        <tr>
          <th>{{ t('adminCredentials.colName') }}</th>
          <th>{{ t('adminCredentials.colType') }}</th>
          <th>{{ t('adminCredentials.colScope') }}</th>
          <th>{{ t('adminCredentials.colMasked') }}</th>
          <th>{{ t('adminCredentials.colLastUsed') }}</th>
          <th>{{ t('adminCredentials.colCreated') }}</th>
          <th>{{ t('adminCredentials.colActions') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="c in credentials" :key="c.id">
          <td class="cell-strong">{{ c.name }}</td>
          <td><code class="mono">{{ c.type }}</code></td>
          <td>
            <span class="tag" :class="c.scope === 'global' ? 'tag--global' : 'tag--personal'">
              {{ c.scope === 'global' ? t('adminCredentials.scopeGlobal') : t('adminCredentials.scopePersonal') }}
            </span>
          </td>
          <td class="mono mono--sm">{{ c.maskedValue }}</td>
          <td class="nowrap">{{ fmtTime(c.lastUsedAt) }}</td>
          <td class="nowrap">{{ fmtTime(c.createdAt) }}</td>
          <td>
            <button
              class="btn btn--sm btn--danger"
              :disabled="busyId === c.id || c.scope !== 'personal'"
              :title="
                c.scope !== 'personal' ? t('adminCredentials.globalNotDisableable') : ''
              "
              @click="onDisable(c)"
            >
              {{ t('adminCredentials.disable') }}
            </button>
          </td>
        </tr>
      </tbody>
    </table>
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
.header-actions {
  flex-shrink: 0;
}
.banner {
  padding: 10px 14px;
  border-radius: 8px;
  background: rgba(34, 197, 94, 0.12);
  font-size: var(--text-label);
  margin-bottom: 12px;
}
.state {
  padding: 32px;
  text-align: center;
  color: var(--color-faint);
}
.state--error {
  color: var(--color-danger, #dc2626);
}
.state-actions {
  display: flex;
  gap: 8px;
  justify-content: center;
  margin-top: 12px;
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
.mono {
  font-family: var(--font-mono, monospace);
  font-size: var(--text-small, 0.85em);
}
.mono--sm {
  opacity: 0.8;
}
.nowrap {
  white-space: nowrap;
}
.tag {
  display: inline-block;
  padding: 2px 10px;
  border-radius: 999px;
  font-size: var(--text-small, 0.8em);
  font-weight: 600;
}
.tag--global {
  background: rgba(59, 130, 246, 0.15);
  color: #2563eb;
}
.tag--personal {
  background: rgba(168, 85, 247, 0.15);
  color: #9333ea;
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
.btn--danger {
  color: var(--color-danger, #dc2626);
  border-color: var(--color-danger, #dc2626);
}
.btn--sm {
  padding: 4px 10px;
  font-size: var(--text-small, 0.85em);
}
</style>
