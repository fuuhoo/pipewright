<script setup lang="ts">
/**
 * v6.2 用户管理(设置类入口)。
 *
 * 两类行分开对待:内置管理员那一行是 admin_user 的同步行,口令与启停在「账户设置」里改,
 * 写端点对它一律 409 —— 所以这里直接禁用按钮,而不是等报错。
 * 默认列表不含已禁用账号(后端的 includeDisabled 取舍),要看到被停用的账号得勾上开关。
 *
 * 角色是功能轴(允许做这类动作吗),分组是数据轴(这份数据归谁):这里只改前者,
 * 名单来自 lib/roles(与后端 roles.go 同集合,单测比对防漂移)。改完要对方重新登录
 * 才生效 —— sessions.role 是登录快照,所以页面上把这句话写明白,而不是让人以为立刻生效。
 */
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  listUsers,
  createUser,
  updateUser,
  resetUserPassword,
  MIN_PASSWORD_LEN,
} from '../../api/users'
import type { User, UserRole } from '../../api/users'
import { ROLE_LABEL_KEY, ROLE_ORDER, ROLE_TAG_CLASS, normalizeRole } from '../../lib/roles'
import { HttpError } from '../../api/http'

const { t } = useI18n()

const loadState = ref<'idle' | 'loading' | 'error'>('idle')
const loadError = ref('')
const users = ref<User[]>([])
const includeDisabled = ref(false)
// 行内改描述失败的提示:它不属于「整页加载失败」,所以单独一条横幅而不是复用 loadError。
const rowError = ref('')

async function load(): Promise<void> {
  loadState.value = 'loading'
  loadError.value = ''
  try {
    users.value = await listUsers({ includeDisabled: includeDisabled.value })
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

/** 角色展示名:库里可能存着枚举外的历史值,先归一(按 user 显示)再取键,免得渲染成裸 key。 */
function roleLabel(role: string): string {
  return t(ROLE_LABEL_KEY[normalizeRole(role)])
}

function roleTagClass(role: string): string {
  return ROLE_TAG_CLASS[normalizeRole(role)]
}

/** 校验失败时用后端原文,其余按状态码给一句人话。 */
function errText(err: unknown, key: string): string {
  if (err instanceof HttpError) {
    if (err.status === 0) return t('adminUsers.errUsersLoadConn')
    return err.apiError?.message ?? t(key, { status: err.status })
  }
  return t('adminUsers.errRetry')
}

// ─── 新建账号 ─────────────────────────────────────────────────────────────────

const createOpen = ref(false)
const createBusy = ref(false)
const createBanner = ref('')
const createForm = ref({ username: '', password: '', role: 'user' as UserRole, description: '' })
const createErrors = ref({ username: '', password: '' })

function openCreate(): void {
  createForm.value = { username: '', password: '', role: 'user', description: '' }
  createErrors.value = { username: '', password: '' }
  createBanner.value = ''
  createOpen.value = true
}

function closeCreate(): void {
  if (createBusy.value) return
  createOpen.value = false
}

function validateCreate(): boolean {
  createErrors.value = { username: '', password: '' }
  let ok = true
  if (!createForm.value.username.trim()) {
    createErrors.value.username = t('adminUsers.errUsernameRequired')
    ok = false
  }
  if (createForm.value.password.length < MIN_PASSWORD_LEN) {
    createErrors.value.password = t('adminUsers.errPasswordShort', { n: MIN_PASSWORD_LEN })
    ok = false
  }
  return ok
}

async function submitCreate(): Promise<void> {
  if (!validateCreate()) return
  createBusy.value = true
  createBanner.value = ''
  try {
    const u = await createUser({
      username: createForm.value.username.trim(),
      password: createForm.value.password,
      role: createForm.value.role,
      description: createForm.value.description.trim(),
    })
    users.value = [...users.value, u].sort((a, b) => a.username.localeCompare(b.username))
    createOpen.value = false
  } catch (err) {
    createBanner.value = errText(err, 'adminUsers.errCreate')
  } finally {
    createBusy.value = false
  }
}

// ─── 重置口令 ─────────────────────────────────────────────────────────────────

const resetOpen = ref(false)
const resetTarget = ref<User | null>(null)
const resetPasswordValue = ref('')
const resetBusy = ref(false)
const resetBanner = ref('')

function openReset(u: User): void {
  resetTarget.value = u
  resetPasswordValue.value = ''
  resetBanner.value = ''
  resetOpen.value = true
}

function closeReset(): void {
  if (resetBusy.value) return
  resetOpen.value = false
  resetTarget.value = null
}

async function submitReset(): Promise<void> {
  const target = resetTarget.value
  if (!target) return
  if (resetPasswordValue.value.length < MIN_PASSWORD_LEN) {
    resetBanner.value = t('adminUsers.errPasswordShort', { n: MIN_PASSWORD_LEN })
    return
  }
  resetBusy.value = true
  resetBanner.value = ''
  try {
    await resetUserPassword(target.id, resetPasswordValue.value)
    // 204:响应没有 body,列表里的这一行本来就不展示口令,无需回填。
    resetOpen.value = false
    resetTarget.value = null
  } catch (err) {
    resetBanner.value = errText(err, 'adminUsers.errReset')
  } finally {
    resetBusy.value = false
  }
}

// ─── 启用 / 禁用 ──────────────────────────────────────────────────────────────

const toggleTarget = ref<User | null>(null)
const toggleBusy = ref(false)
const toggleBanner = ref('')

const toggleIsDisabling = computed(() => toggleTarget.value?.enabled === true)

function openToggle(u: User): void {
  toggleTarget.value = u
  toggleBanner.value = ''
}

function closeToggle(): void {
  if (toggleBusy.value) return
  toggleTarget.value = null
}

async function submitToggle(): Promise<void> {
  const target = toggleTarget.value
  if (!target) return
  toggleBusy.value = true
  toggleBanner.value = ''
  try {
    const fresh = await updateUser(target.id, { enabled: !target.enabled })
    users.value = users.value.map((u) => (u.id === fresh.id ? fresh : u))
    // 取消勾选「显示已禁用」后,刚被停用的账号就不该继续留在列表里。
    if (!includeDisabled.value && !fresh.enabled) {
      users.value = users.value.filter((u) => u.id !== fresh.id)
    }
    toggleTarget.value = null
  } catch (err) {
    toggleBanner.value = errText(err, 'adminUsers.errToggle')
  } finally {
    toggleBusy.value = false
  }
}

async function saveDescription(u: User, value: string): Promise<void> {
  rowError.value = ''
  try {
    const fresh = await updateUser(u.id, { description: value.trim() })
    users.value = users.value.map((x) => (x.id === fresh.id ? fresh : x))
  } catch (err) {
    rowError.value = errText(err, 'adminUsers.errToggle')
  }
}

// ─── 改角色(功能轴)──────────────────────────────────────────────────────────

const roleBusyId = ref('')

/**
 * 改某人的角色档位。失败时把这一行的下拉弹回原值:DOM 已经显示成新选项了,
 * 不回滚就是「看着改成功了、其实没改」——那是最难排查的一类错觉。
 */
async function saveRole(u: User, next: string, el: HTMLSelectElement): Promise<void> {
  if (next === u.role) return
  rowError.value = ''
  roleBusyId.value = u.id
  try {
    const fresh = await updateUser(u.id, { role: next as UserRole })
    users.value = users.value.map((x) => (x.id === fresh.id ? fresh : x))
  } catch (err) {
    el.value = u.role
    rowError.value = errText(err, 'adminUsers.errRoleUpdate')
  } finally {
    roleBusyId.value = ''
  }
}
</script>

<template>
  <div class="users-view">
    <header class="view-header">
      <div>
        <h1 class="view-title">{{ t('adminUsers.usersTitle') }}</h1>
        <p class="view-sub">{{ t('adminUsers.usersDesc') }}</p>
        <p class="view-sub view-sub--hint">{{ t('adminUsers.roleAxesHint') }}</p>
      </div>
      <div class="header-actions">
        <label class="check">
          <input v-model="includeDisabled" type="checkbox" @change="load" />
          <span>{{ t('adminUsers.showDisabled') }}</span>
        </label>
        <button class="btn btn--primary" @click="openCreate">+ {{ t('adminUsers.addUser') }}</button>
      </div>
    </header>

    <div v-if="rowError" class="banner banner--row" role="alert">
      <span>{{ rowError }}</span>
      <button class="banner-close" type="button" aria-label="×" @click="rowError = ''">×</button>
    </div>

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
          <th class="th-actions">{{ t('adminUsers.colActions') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="u in users" :key="u.id">
          <td>
            <div class="cell-strong">{{ u.username }}</div>
            <input
              v-if="!isBootstrapAdmin(u)"
              class="desc-input"
              type="text"
              :value="u.description"
              :placeholder="t('adminUsers.fieldDesc')"
              @change="saveDescription(u, ($event.target as HTMLInputElement).value)"
            />
            <div v-if="isBootstrapAdmin(u)" class="cell-dim">
              {{ t('adminUsers.bootstrapAdmin') }} · {{ t('adminUsers.bootstrapAdminHint') }}
            </div>
          </td>
          <td>
            <span v-if="isBootstrapAdmin(u)" class="tag" :class="roleTagClass(u.role)">
              {{ roleLabel(u.role) }}
            </span>
            <select
              v-else
              class="role-select"
              :value="u.role"
              :disabled="roleBusyId === u.id"
              :title="t('adminUsers.roleChangeHint')"
              @change="saveRole(u, ($event.target as HTMLSelectElement).value, $event.target as HTMLSelectElement)"
            >
              <option v-for="r in ROLE_ORDER" :key="r" :value="r">{{ roleLabel(r) }}</option>
            </select>
          </td>
          <td>
            <span :class="u.enabled ? 'ok' : 'off'">
              {{ u.enabled ? t('adminUsers.enabled') : t('adminUsers.disabled') }}
            </span>
          </td>
          <td>{{ fmtTime(u.lastLoginAt) }}</td>
          <td>{{ fmtTime(u.createdAt) }}</td>
          <td class="cell-actions">
            <button
              class="btn btn--ghost"
              :disabled="isBootstrapAdmin(u)"
              :title="isBootstrapAdmin(u) ? t('adminUsers.bootstrapAdminHint') : ''"
              @click="openReset(u)"
            >
              {{ t('adminUsers.resetPassword') }}
            </button>
            <button
              class="btn btn--ghost"
              :class="{ 'btn--danger': u.enabled }"
              :disabled="isBootstrapAdmin(u)"
              :title="isBootstrapAdmin(u) ? t('adminUsers.bootstrapAdminHint') : ''"
              @click="openToggle(u)"
            >
              {{ u.enabled ? t('adminUsers.disable') : t('adminUsers.enable') }}
            </button>
          </td>
        </tr>
      </tbody>
    </table>

    <!-- ─── 新建账号 ─── -->
    <div v-if="createOpen" class="backdrop" @click.self="closeCreate">
      <div class="modal" role="dialog" aria-modal="true" aria-labelledby="user-create-title">
        <h3 id="user-create-title" class="modal-title">{{ t('adminUsers.createTitle') }}</h3>
        <div v-if="createBanner" class="banner" role="alert">{{ createBanner }}</div>
        <form @submit.prevent="submitCreate">
          <label class="field">
            <span class="field-label">{{ t('adminUsers.fieldUsername') }}</span>
            <input v-model="createForm.username" class="field-input" type="text" autocomplete="off" :disabled="createBusy" />
            <span v-if="createErrors.username" class="field-error">{{ createErrors.username }}</span>
          </label>
          <label class="field">
            <span class="field-label">{{ t('adminUsers.fieldPassword') }}</span>
            <input v-model="createForm.password" class="field-input" type="password" autocomplete="new-password" :disabled="createBusy" />
            <span v-if="createErrors.password" class="field-error">{{ createErrors.password }}</span>
            <span v-else class="field-hint">{{ t('adminUsers.passwordHint', { n: MIN_PASSWORD_LEN }) }}</span>
          </label>
          <label class="field">
            <span class="field-label">{{ t('adminUsers.fieldRole') }}</span>
            <select v-model="createForm.role" class="field-input" :disabled="createBusy">
              <option v-for="r in ROLE_ORDER" :key="r" :value="r">{{ roleLabel(r) }}</option>
            </select>
            <span class="field-hint">{{ t('adminUsers.roleDesc') }}</span>
          </label>
          <label class="field">
            <span class="field-label">{{ t('adminUsers.fieldDesc') }}</span>
            <input v-model="createForm.description" class="field-input" type="text" autocomplete="off" :disabled="createBusy" />
          </label>
          <div class="modal-actions">
            <button type="button" class="btn" :disabled="createBusy" @click="closeCreate">{{ t('adminUsers.cancel') }}</button>
            <button type="submit" class="btn btn--primary" :disabled="createBusy">
              {{ createBusy ? t('adminUsers.saving') : t('adminUsers.create') }}
            </button>
          </div>
        </form>
      </div>
    </div>

    <!-- ─── 重置口令 ─── -->
    <div v-if="resetOpen" class="backdrop" @click.self="closeReset">
      <div class="modal" role="dialog" aria-modal="true" aria-labelledby="user-reset-title">
        <h3 id="user-reset-title" class="modal-title">{{ t('adminUsers.resetTitle', { name: resetTarget?.username }) }}</h3>
        <div v-if="resetBanner" class="banner" role="alert">{{ resetBanner }}</div>
        <form @submit.prevent="submitReset">
          <label class="field">
            <span class="field-label">{{ t('adminUsers.fieldPassword') }}</span>
            <input v-model="resetPasswordValue" class="field-input" type="password" autocomplete="new-password" :disabled="resetBusy" />
            <span class="field-hint">{{ t('adminUsers.passwordHint', { n: MIN_PASSWORD_LEN }) }}</span>
          </label>
          <div class="modal-actions">
            <button type="button" class="btn" :disabled="resetBusy" @click="closeReset">{{ t('adminUsers.cancel') }}</button>
            <button type="submit" class="btn btn--primary" :disabled="resetBusy">
              {{ resetBusy ? t('adminUsers.saving') : t('adminUsers.reset') }}
            </button>
          </div>
        </form>
      </div>
    </div>

    <!-- ─── 启用 / 禁用确认 ─── -->
    <div v-if="toggleTarget" class="backdrop" @click.self="closeToggle">
      <div class="modal" role="dialog" aria-modal="true" aria-labelledby="user-toggle-title">
        <h3 id="user-toggle-title" class="modal-title">
          {{ toggleIsDisabling ? t('adminUsers.disableTitle') : t('adminUsers.enable') }}
        </h3>
        <div v-if="toggleBanner" class="banner" role="alert">{{ toggleBanner }}</div>
        <p class="modal-text">
          {{
            toggleIsDisabling
              ? t('adminUsers.disableBody', { name: toggleTarget.username })
              : t('adminUsers.enableBody', { name: toggleTarget.username })
          }}
        </p>
        <div class="modal-actions">
          <button type="button" class="btn" :disabled="toggleBusy" @click="closeToggle">{{ t('adminUsers.cancel') }}</button>
          <button
            type="button"
            class="btn btn--primary"
            :class="{ 'btn--danger': toggleIsDisabling }"
            :disabled="toggleBusy"
            @click="submitToggle"
          >
            {{ toggleBusy ? t('adminUsers.saving') : t('adminUsers.save') }}
          </button>
        </div>
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
  max-width: 76ch;
}
.view-sub--hint {
  font-size: var(--text-small, 0.85em);
}
.header-actions {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-shrink: 0;
}
.check {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: var(--text-label);
  color: var(--color-dim);
  cursor: pointer;
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
.th-actions {
  text-align: right;
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
.cell-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  white-space: nowrap;
}
.desc-input {
  width: 100%;
  max-width: 280px;
  margin-top: 4px;
  padding: 4px 8px;
  font-size: var(--text-small, 0.85em);
  color: var(--color-dim);
  background: transparent;
  border: 1px solid transparent;
  border-radius: var(--radius-sm, 6px);
}
.desc-input:hover,
.desc-input:focus {
  border-color: var(--color-border);
  background: var(--color-bg, #fff);
  outline: none;
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
.tag--developer {
  background: rgba(16, 185, 129, 0.15);
  color: #047857;
}
.tag--ops {
  background: rgba(245, 158, 11, 0.18);
  color: #b45309;
}
.tag--viewer {
  background: rgba(100, 116, 139, 0.16);
  color: #475569;
}
/* 行内改角色:与描述输入一样做成「看着像文本、点开才像控件」,避免整表变成表单。 */
.role-select {
  padding: 4px 8px;
  font-size: var(--text-small, 0.85em);
  font-weight: 600;
  color: var(--color-text);
  background: transparent;
  border: 1px solid transparent;
  border-radius: var(--radius-sm, 6px);
  cursor: pointer;
}
.role-select:hover,
.role-select:focus {
  border-color: var(--color-border);
  background: var(--color-bg, #fff);
  outline: none;
}
.role-select:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}
.ok {
  color: #16a34a;
}
.off {
  color: var(--color-faint);
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
.btn--primary {
  background: var(--color-primary, #2563eb);
  border-color: var(--color-primary, #2563eb);
  color: #fff;
}
.btn--ghost {
  background: transparent;
}
.btn--danger {
  background: var(--color-danger, #dc2626);
  border-color: var(--color-danger, #dc2626);
  color: #fff;
}
.backdrop {
  position: fixed;
  inset: 0;
  z-index: 100;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
  background: color-mix(in oklch, var(--color-text) 40%, transparent);
}
.modal {
  width: 100%;
  max-width: 420px;
  padding: 20px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md, 12px);
  background: var(--color-surface, #fff);
}
.modal-title {
  margin-bottom: 12px;
  font-size: var(--text-body);
  font-weight: 700;
  color: var(--color-text);
}
.modal-text {
  margin-bottom: 16px;
  font-size: var(--text-label);
  line-height: 1.5;
  color: var(--color-dim);
}
.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-bottom: 14px;
}
.field-label {
  font-size: var(--text-label);
  font-weight: 500;
  color: var(--color-dim);
}
.field-input {
  padding: 8px 12px;
  font-size: var(--text-body);
  color: var(--color-text);
  background: var(--color-bg, #fff);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm, 6px);
}
.field-input:focus {
  outline: none;
  border-color: var(--color-primary);
}
.field-hint {
  font-size: var(--text-small, 0.85em);
  color: var(--color-faint);
}
.field-error {
  font-size: var(--text-small, 0.85em);
  color: var(--color-danger, #dc2626);
}
.banner {
  margin-bottom: 12px;
  padding: 8px 12px;
  border: 1px solid var(--color-danger, #dc2626);
  border-radius: var(--radius-sm, 6px);
  font-size: var(--text-label);
  color: var(--color-danger, #dc2626);
}
.banner--row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.banner-close {
  border: none;
  background: transparent;
  color: inherit;
  font-size: 1rem;
  line-height: 1;
  cursor: pointer;
}
</style>
