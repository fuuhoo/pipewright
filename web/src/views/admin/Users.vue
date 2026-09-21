<script setup lang="ts">
/**
 * v6.2 用户管理(admin-only)—— 阶段 9 最小骨架的只读视图。
 *
 * 后端当前只暴露 list / get(邀请注册、启用禁用是后续 story),页面据此展示
 * 两类用户:管理员(可与 admin_user 表双向同步)与普通用户。
 */
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { listUsers } from '../../api/users'
import type { User } from '../../api/users'
import { HttpError } from '../../api/http'

const { t } = useI18n()

const loadState = ref<'idle' | 'loading' | 'error'>('idle')
const loadError = ref('')
const users = ref<User[]>([])

async function load(): Promise<void> {
  loadState.value = 'loading'
  loadError.value = ''
  try {
    users.value = await listUsers()
    loadState.value = 'idle'
  } catch (err) {
    loadError.value =
      err instanceof HttpError
        ? err.status === 0
          ? t('adminUsers.errUsersLoadConn')
          : (err.apiError?.message ?? t('adminUsers.errUsersLoad'))
        : t('adminUsers.errUsersLoad')
    loadState.value = 'error'
  }
}

onMounted(load)

function fmtTime(iso: string | null): string {
  if (!iso) return t('adminUsers.never')
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString()
}

const BOOTSTRAP_ADMIN_ID = '00000000-0000-0000-0000-000000000001'

function isBootstrapAdmin(u: User): boolean {
  return u.id === BOOTSTRAP_ADMIN_ID
}
</script>

<template>
  <div class="users-view">
    <header class="view-header">
      <div>
        <h1 class="view-title">{{ t('adminUsers.usersTitle') }}</h1>
        <p class="view-sub">{{ t('adminUsers.usersDesc') }}</p>
      </div>
    </header>

    <p v-if="loadState === 'loading'" class="state">{{ t('common.refresh') }}…</p>
    <div v-else-if="loadState === 'error'" class="state state--error">
      <p>{{ loadError }}</p>
      <button class="btn" @click="load">{{ t('common.refresh') }}</button>
    </div>
    <p v-else-if="users.length === 0" class="state">{{ t('adminUsers.usersEmpty') }}</p>

    <table v-else class="grid">
      <thead>
        <tr>
          <th>{{ t('adminUsers.colUsername') }}</th>
          <th>{{ t('adminUsers.colRole') }}</th>
          <th>{{ t('adminUsers.colEnabled') }}</th>
          <th>{{ t('adminUsers.colLastLogin') }}</th>
          <th>{{ t('adminUsers.colCreated') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="u in users" :key="u.id">
          <td>
            <div class="cell-strong">{{ u.username }}</div>
            <div v-if="isBootstrapAdmin(u)" class="cell-dim">
              {{ t('adminUsers.bootstrapAdmin') }} · {{ t('adminUsers.bootstrapAdminHint') }}
            </div>
            <div v-else-if="u.description" class="cell-dim">{{ u.description }}</div>
          </td>
          <td>
            <span class="tag" :class="u.role === 'admin' ? 'tag--admin' : 'tag--user'">
              {{
                u.role === 'admin' ? t('adminUsers.roleAdmin') : t('adminUsers.roleUser')
              }}
            </span>
          </td>
          <td>
            <span :class="u.enabled ? 'ok' : 'off'">
              {{ u.enabled ? t('adminUsers.enabled') : t('adminUsers.disabled') }}
            </span>
          </td>
          <td>{{ fmtTime(u.lastLoginAt) }}</td>
          <td>{{ fmtTime(u.createdAt) }}</td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
.view-header {
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
.cell-dim {
  font-size: var(--text-small, 0.85em);
  color: var(--color-faint);
  margin-top: 2px;
}
.tag {
  display: inline-block;
  padding: 2px 10px;
  border-radius: 999px;
  font-size: var(--text-small, 0.8em);
  font-weight: 600;
}
.tag--admin {
  background: rgba(59, 130, 246, 0.15);
  color: #2563eb;
}
.tag--user {
  background: rgba(120, 120, 120, 0.15);
  color: var(--color-dim);
}
.ok {
  color: #16a34a;
}
.off {
  color: var(--color-faint);
}
.btn {
  margin-top: 12px;
  padding: 7px 14px;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-bg, #fff);
  color: var(--color-text);
  font-size: var(--text-label);
  cursor: pointer;
}
</style>
