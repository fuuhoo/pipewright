<script setup lang="ts">
/**
 * v6.2 用户管理 —— 挂在「用户与权限」壳(/permissions)的用户 tab 下。
 *
 * 这一 tab 的门在壳里:没有「设置类」能力时 tab 根本不渲染,而不是靠路由 meta.requires
 * 把人踢回仪表盘(组长还要用同一页的分组 tab)。后端 /api/admin/users 的 RequireAdmin 是权威。
 *
 * 交互壳与分组 tab 共用:表单弹窗走 ui/AppModal(背景/Esc 在提交中不关、回车即提交),
 * 破坏性操作用 useConfirm 的公共确认框(不再各页手搓一个),报错统一 .banner--err 措辞。
 *
 * 两类行分开对待:内置管理员那一行是 admin_user 的同步行,口令与启停在「账户设置」里改,
 * 写端点对它一律 409 —— 所以这里直接禁用按钮,而不是等报错。
 * 默认列表不含已禁用账号(后端的 includeDisabled 取舍),要看到被停用的账号得勾上开关。
 *
 * 角色是功能轴(允许做这类动作吗),分组是数据轴(这份数据归谁):这里只改前者,
 * 名单来自 lib/roles(与后端 roles.go 同集合,单测比对防漂移)。改完要对方重新登录
 * 才生效 —— sessions.role 是登录快照,所以页面上把这句话写明白,而不是让人以为立刻生效。
 */
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  listUsers,
  createUser,
  updateUser,
  resetUserPassword,
  BOOTSTRAP_ADMIN_ID,
  MIN_PASSWORD_LEN,
} from '../../api/users'
import type { User, UserRole } from '../../api/users'
import { ROLE_LABEL_KEY, ROLE_ORDER, ROLE_TAG_CLASS, normalizeRole } from '../../lib/roles'
import { HttpError } from '../../api/http'
import AppModal from '../../components/ui/AppModal.vue'
import AppButton from '../../components/ui/AppButton.vue'
import FormField from '../../components/ui/FormField.vue'
import { useConfirm } from '../../composables/useConfirm'
import { useToast } from '../../composables/useToast'

const { t } = useI18n()
const confirm = useConfirm()
const toast = useToast()

const loadState = ref<'idle' | 'loading' | 'error'>('idle')
const loadError = ref('')
const users = ref<User[]>([])
const includeDisabled = ref(false)
// 行内改描述/改角色失败的提示:它不属于「整页加载失败」,所以单独一条横幅而不是复用 loadError。
const rowError = ref('')

async function load(): Promise<void> {
  loadState.value = 'loading'
  loadError.value = ''
  try {
    users.value = await listUsers({ includeDisabled: includeDisabled.value })
    loadState.value = 'idle'
  } catch (err) {
    loadError.value = errText(err, 'adminUsers.errUsersLoad')
    loadState.value = 'error'
  }
}

onMounted(load)

function fmtTime(iso: string | null): string {
  if (!iso) return t('adminUsers.never')
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString()
}

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

/**
 * 启停走公共确认框(与分组页删除同一壳)。确认框在调用前就关掉了,所以失败只能进 toast,
 * 不能再塞回弹窗横幅。
 */
async function askToggle(u: User): Promise<void> {
  const disabling = u.enabled
  const ok = await confirm.open({
    title: t(disabling ? 'adminUsers.disableTitle' : 'adminUsers.enableTitle'),
    body: t(disabling ? 'adminUsers.disableBody' : 'adminUsers.enableBody', { name: u.username }),
    confirmLabel: t(disabling ? 'adminUsers.disable' : 'adminUsers.enable'),
    variant: disabling ? 'danger' : 'primary',
  })
  if (!ok) return
  try {
    const fresh = await updateUser(u.id, { enabled: !u.enabled })
    users.value = users.value.map((x) => (x.id === fresh.id ? fresh : x))
    // 取消勾选「显示已禁用」后,刚被停用的账号就不该继续留在列表里。
    if (!includeDisabled.value && !fresh.enabled) {
      users.value = users.value.filter((x) => x.id !== fresh.id)
    }
  } catch (err) {
    toast.error(errText(err, 'adminUsers.errToggle'))
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
        <h2 class="view-title">{{ t('adminUsers.usersTitle') }}</h2>
        <p class="view-sub">{{ t('adminUsers.usersDesc') }}</p>
        <p class="view-sub view-sub--hint">{{ t('adminUsers.roleAxesHint') }}</p>
      </div>
      <div class="header-actions">
        <label class="check">
          <input v-model="includeDisabled" type="checkbox" @change="load" />
          <span>{{ t('adminUsers.showDisabled') }}</span>
        </label>
        <AppButton variant="primary" @click="openCreate">+ {{ t('adminUsers.addUser') }}</AppButton>
      </div>
    </header>

    <div v-if="rowError" class="banner banner--err banner--row" role="alert">
      <span>{{ rowError }}</span>
      <button class="banner-close" type="button" aria-label="×" @click="rowError = ''">×</button>
    </div>

    <p v-if="loadState === 'loading'" class="state">{{ t('common.refresh') }}…</p>
    <div v-else-if="loadState === 'error'" class="state state--error">
      <p>{{ loadError }}</p>
      <AppButton @click="load">{{ t('common.refresh') }}</AppButton>
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
            <AppButton
              variant="ghost"
              size="sm"
              :disabled="isBootstrapAdmin(u)"
              :title="isBootstrapAdmin(u) ? t('adminUsers.bootstrapAdminHint') : ''"
              @click="openReset(u)"
            >
              {{ t('adminUsers.resetPassword') }}
            </AppButton>
            <AppButton
              :variant="u.enabled ? 'danger' : 'ghost'"
              size="sm"
              :disabled="isBootstrapAdmin(u)"
              :title="isBootstrapAdmin(u) ? t('adminUsers.bootstrapAdminHint') : ''"
              @click="askToggle(u)"
            >
              {{ u.enabled ? t('adminUsers.disable') : t('adminUsers.enable') }}
            </AppButton>
          </td>
        </tr>
      </tbody>
    </table>

    <!-- ─── 新建账号 ─── -->
    <AppModal
      v-if="createOpen"
      :title="t('adminUsers.createTitle')"
      width="lg"
      :busy="createBusy"
      @close="closeCreate"
      @submit="submitCreate"
    >
      <p v-if="createBanner" class="banner banner--err" role="alert">{{ createBanner }}</p>
      <div class="form-grid">
        <FormField
          :label="t('adminUsers.fieldUsername')"
          field-id="user-create-name"
          :error="createErrors.username"
          required
        >
          <template #default="{ fieldId, ariaDescribedby }">
            <input
              :id="fieldId"
              v-model="createForm.username"
              class="ui-input"
              type="text"
              autocomplete="off"
              :aria-describedby="ariaDescribedby"
              :disabled="createBusy"
            />
          </template>
        </FormField>
        <FormField
          :label="t('adminUsers.fieldPassword')"
          field-id="user-create-pw"
          :error="createErrors.password"
          :hint="t('adminUsers.passwordHint', { n: MIN_PASSWORD_LEN })"
          required
        >
          <template #default="{ fieldId, ariaDescribedby }">
            <input
              :id="fieldId"
              v-model="createForm.password"
              class="ui-input"
              type="password"
              autocomplete="new-password"
              :aria-describedby="ariaDescribedby"
              :disabled="createBusy"
            />
          </template>
        </FormField>
        <FormField
          :label="t('adminUsers.fieldRole')"
          field-id="user-create-role"
          :hint="t('adminUsers.roleDesc')"
        >
          <template #default="{ fieldId }">
            <select :id="fieldId" v-model="createForm.role" class="ui-input" :disabled="createBusy">
              <option v-for="r in ROLE_ORDER" :key="r" :value="r">{{ roleLabel(r) }}</option>
            </select>
          </template>
        </FormField>
        <FormField :label="t('adminUsers.fieldDesc')" field-id="user-create-desc">
          <template #default="{ fieldId }">
            <input
              :id="fieldId"
              v-model="createForm.description"
              class="ui-input"
              type="text"
              autocomplete="off"
              :disabled="createBusy"
            />
          </template>
        </FormField>
      </div>

      <template #actions>
        <AppButton :disabled="createBusy" @click="closeCreate">{{ t('adminUsers.cancel') }}</AppButton>
        <AppButton variant="primary" type="submit" :loading="createBusy">
          {{ t('adminUsers.create') }}
        </AppButton>
      </template>
    </AppModal>

    <!-- ─── 重置口令 ─── -->
    <AppModal
      v-if="resetOpen"
      :title="t('adminUsers.resetTitle', { name: resetTarget?.username })"
      width="sm"
      :busy="resetBusy"
      @close="closeReset"
      @submit="submitReset"
    >
      <p v-if="resetBanner" class="banner banner--err" role="alert">{{ resetBanner }}</p>
      <FormField
        :label="t('adminUsers.fieldPassword')"
        field-id="user-reset-pw"
        :hint="t('adminUsers.passwordHint', { n: MIN_PASSWORD_LEN })"
        required
      >
        <template #default="{ fieldId, ariaDescribedby }">
          <input
            :id="fieldId"
            v-model="resetPasswordValue"
            class="ui-input"
            type="password"
            autocomplete="new-password"
            :aria-describedby="ariaDescribedby"
            :disabled="resetBusy"
          />
        </template>
      </FormField>

      <template #actions>
        <AppButton :disabled="resetBusy" @click="closeReset">{{ t('adminUsers.cancel') }}</AppButton>
        <AppButton variant="primary" type="submit" :loading="resetBusy">
          {{ t('adminUsers.reset') }}
        </AppButton>
      </template>
    </AppModal>
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
  /* 这一页是「用户与权限」壳下的一个 tab:大标题由壳给,这里降到小节级。 */
  font-size: var(--text-heading);
  font-weight: 600;
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
.banner {
  padding: 8px 12px;
  border-radius: var(--radius-sm, 6px);
  font-size: var(--text-label);
}
.banner--err {
  border: 1px solid var(--color-red-line);
  background: var(--color-red-soft);
  color: var(--color-red);
}
.banner--row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
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
