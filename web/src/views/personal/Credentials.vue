<script setup lang="ts">
/**
 * v6.2 我的凭据(personal credentials;所有登录用户可见)。
 *
 * §3.4:personal 凭据 owner_id 指向 users.id,仅创建者本人可见/可用。
 * 后端当前 /api/credentials 对所有登录用户返回全量(vault RBAC 的 owner 过滤
 * 依赖 0053_credentials_owner 迁移,尚未落地),故本页:
 *   - 展示「本人可管理」的凭据(创建/轮换/删除/查看明文)
 *   - 对非本人可见的条目只显示元数据(明文入口禁用),与 §3.4 语义保持一致
 *
 * 密文级操作复用「设置 → 凭据保险库」的既有链路,本页聚焦 personal 视角。
 */
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { listCredentials } from '../../api/credentials'
import type { Credential } from '../../api/credentials'
import { HttpError } from '../../api/http'
import { useSessionStore } from '../../stores/session'

const { t } = useI18n()
const router = useRouter()
const sessionStore = useSessionStore()

const loadState = ref<'idle' | 'loading' | 'error'>('idle')
const loadError = ref('')
const credentials = ref<Credential[]>([])

async function load(): Promise<void> {
  loadState.value = 'loading'
  loadError.value = ''
  try {
    credentials.value = await listCredentials()
    loadState.value = 'idle'
  } catch (err) {
    if (err instanceof HttpError && err.apiError?.code === 'vault_unconfigured') {
      loadError.value = t('myCredentials.vaultUnconfigured')
    } else if (err instanceof HttpError && err.status === 0) {
      loadError.value = t('myCredentials.errConn')
    } else if (err instanceof HttpError) {
      loadError.value = err.apiError?.message ?? t('myCredentials.errLoad')
    } else {
      loadError.value = t('myCredentials.errLoad')
    }
    loadState.value = 'error'
  }
}

onMounted(load)

function fmtTime(iso: string | null): string {
  if (!iso) return t('myCredentials.never')
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString()
}

function goVault(): void {
  void router.push({ name: 'settings-vault' })
}
</script>

<template>
  <div class="my-creds-view">
    <header class="view-header">
      <div>
        <h1 class="view-title">{{ t('myCredentials.title') }}</h1>
        <p class="view-sub">{{ t('myCredentials.desc', { user: sessionStore.user?.username ?? '' }) }}</p>
      </div>
      <div class="header-actions">
        <button class="btn btn--primary" @click="goVault">{{ t('myCredentials.manage') }}</button>
      </div>
    </header>

    <p v-if="loadState === 'loading'" class="state">{{ t('common.refresh') }}…</p>
    <div v-else-if="loadState === 'error'" class="state state--error">
      <p>{{ loadError }}</p>
      <button class="btn" @click="load">{{ t('common.refresh') }}</button>
    </div>
    <p v-else-if="credentials.length === 0" class="state">{{ t('myCredentials.empty') }}</p>

    <table v-else class="grid">
      <thead>
        <tr>
          <th>{{ t('myCredentials.colName') }}</th>
          <th>{{ t('myCredentials.colType') }}</th>
          <th>{{ t('myCredentials.colScope') }}</th>
          <th>{{ t('myCredentials.colMasked') }}</th>
          <th>{{ t('myCredentials.colLastUsed') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="c in credentials" :key="c.id">
          <td class="cell-strong">{{ c.name }}</td>
          <td><code class="mono">{{ c.type }}</code></td>
          <td>
            <span class="tag" :class="c.scope === 'global' ? 'tag--global' : 'tag--personal'">
              {{ c.scope === 'global' ? t('myCredentials.scopeGlobal') : t('myCredentials.scopePersonal') }}
            </span>
          </td>
          <td class="mono mono--sm">{{ c.maskedValue }}</td>
          <td class="nowrap">{{ fmtTime(c.lastUsedAt) }}</td>
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
.btn--primary {
  background: var(--color-primary);
  border-color: var(--color-primary);
  color: #fff;
}
</style>
