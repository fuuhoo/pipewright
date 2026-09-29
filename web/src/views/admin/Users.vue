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
 * 这一页负责**把人挂到规则上**,不定义规则本身:角色名单来自 /api/admin/roles(内置五档 +
 * 自定义角色,点集在「角色」tab 里维护),分组归属直接读写 /api/groups 的名册。
 * 改角色不会踢掉对方已有的会话 —— sessions.role 是登录快照,下次登录才生效;改分组归属当场生效
 * (每个请求重判)。页面上把这两句写明白,而不是让人以为都是立刻生效 / 都要重登。
 */
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  listUsers,
  createUser,
  updateUser,
  resetUserPassword,
  BOOTSTRAP_ADMIN_ID,
  MIN_PASSWORD_LEN,
} from '../../api/users'
import type { User } from '../../api/users'
import { listRoles } from '../../api/roles'
import type { Role, RoleId } from '../../api/roles'
import { listGroups, addGroupMember, removeGroupMember } from '../../api/groups'
import type { Group } from '../../api/groups'
import { HttpError } from '../../api/http'
import { ROLE_TAG_CLASS, normalizeRole } from '../../lib/roles'
import { labelForRoleId } from '../../lib/roleEditor'
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

// 角色名单与分组名册都是这一页的输入:下拉要角色,「关联分组」弹窗要每个组的当前成员。
// 它们各自失败不该拖垮账号列表,所以失败只记在 rolesError / groupsError 上,列表照常用。
const roles = ref<Role[]>([])
const groups = ref<Group[]>([])

async function load(): Promise<void> {
  loadState.value = 'loading'
  loadError.value = ''
  try {
    const [list, roleList, groupList] = await Promise.all([
      listUsers({ includeDisabled: includeDisabled.value }),
      listRoles(),
      listGroups(),
    ])
    users.value = list
    roles.value = roleList
    groups.value = groupList
    loadState.value = 'idle'
  } catch (err) {
    loadError.value = errText(err, 'adminUsers.errUsersLoad')
    loadState.value = 'error'
  }
}

onMounted(load)

const rolesById = computed<Record<string, Role>>(() => {
  const out: Record<string, Role> = {}
  for (const r of roles.value) out[r.id] = r
  return out
})

function fmtTime(iso: string | null): string {
  if (!iso) return t('adminUsers.never')
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString()
}

function isBootstrapAdmin(u: User): boolean {
  return u.id === BOOTSTRAP_ADMIN_ID
}

/** 角色展示名:内置档走 i18n,自定义档用它的名字;名单查不到时按 normalizeRole 兜底。 */
function roleLabel(role: RoleId): string {
  return labelForRoleId(role, rolesById.value, t)
}

/** 标签配色只给内置档;自定义角色统一一档灰绿,免得「加一个角色改一次配色表」。 */
function roleTagClass(role: RoleId): string {
  return rolesById.value[role]?.builtin ? ROLE_TAG_CLASS[normalizeRole(role)] : 'tag--custom'
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
// 默认档取内置 user(与后端建号路径的降级同档);自定义角色的 UUID 也在同一份名单里选。
const createForm = ref({ username: '', password: '', role: 'user', description: '' })
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
async function saveRole(u: User, next: RoleId, el: HTMLSelectElement): Promise<void> {
  if (next === u.role) return
  rowError.value = ''
  roleBusyId.value = u.id
  try {
    const fresh = await updateUser(u.id, { role: next })
    users.value = users.value.map((x) => (x.id === fresh.id ? fresh : x))
  } catch (err) {
    el.value = u.role
    rowError.value = errText(err, 'adminUsers.errRoleUpdate')
  } finally {
    roleBusyId.value = ''
  }
}
// ─── 关联分组(数据轴)──────────────────────────────────────────────────────

/**
 * 一人的分组归属就在这一页改,不必绕去分组页找他。弹窗里把**所有**组列出来(含他没进的、
 * 也含他管不着的),管不着的那些勾不上 —— canManage 是后端用同一套判定算出来的结论。
 * 保存按差集逐条写 /api/groups/{id}/members:每写成一个就立刻把返回的名册换回本地,
 * 这样中途失败时页面上显示的仍是「真的改成了什么」,而不是我想当然的终态。
 */
const groupsOpen = ref(false)
const groupsTarget = ref<User | null>(null)
const groupsBusy = ref(false)
const groupsBanner = ref('')
const groupDraft = ref<Record<string, boolean>>({})

function isMember(g: Group, userId: string): boolean {
  return g.members.some((m) => m.id === userId)
}

/** 列表「分组」列:他当前挂的组名,按 groups 的顺序。 */
function groupNames(u: User): string[] {
  return groups.value.filter((g) => isMember(g, u.id)).map((g) => g.name)
}

function openGroups(u: User): void {
  groupsTarget.value = u
  const draft: Record<string, boolean> = {}
  for (const g of groups.value) draft[g.id] = isMember(g, u.id)
  groupDraft.value = draft
  groupsBanner.value = ''
  groupsOpen.value = true
}

function closeGroups(): void {
  if (groupsBusy.value) return
  groupsOpen.value = false
  groupsTarget.value = null
}

async function submitGroups(): Promise<void> {
  const target = groupsTarget.value
  if (!target) return
  groupsBusy.value = true
  groupsBanner.value = ''
  let changed = 0
  try {
    for (const g of groups.value) {
      const want = Boolean(groupDraft.value[g.id])
      if (want === isMember(g, target.id) || !g.canManage) continue
      const fresh = want
        ? await addGroupMember(g.id, target.id)
        : await removeGroupMember(g.id, target.id)
      groups.value = groups.value.map((x) => (x.id === fresh.id ? fresh : x))
      changed++
    }
    if (changed > 0) toast.success(t('adminUsers.groupsSaved', { name: target.username }))
    groupsOpen.value = false
    groupsTarget.value = null
  } catch (err) {
    groupsBanner.value = errText(err, 'adminUsers.errGroupsUpdate')
  } finally {
    groupsBusy.value = false
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

    <div v-else class="table-wrap">
      <table class="grid">
        <thead>
          <tr>
            <th>{{ t('adminUsers.colUsername') }}</th>
            <th>{{ t('adminUsers.colRole') }}</th>
            <th>{{ t('adminUsers.colGroups') }}</th>
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
                <option v-for="r in roles" :key="r.id" :value="r.id">{{ roleLabel(r.id) }}</option>
              </select>
            </td>
            <td>
              <div class="cell-groups">
                <span v-for="name in groupNames(u)" :key="name" class="tag tag--group">{{ name }}</span>
                <span v-if="groupNames(u).length === 0" class="cell-dim">{{ t('adminUsers.groupsNone') }}</span>
                <AppButton variant="ghost" size="sm" @click="openGroups(u)">
                  {{ t('adminUsers.assocGroups') }}
                </AppButton>
              </div>
            </td>
            <td>
              <span :class="u.enabled ? 'ok' : 'off'">
                {{ u.enabled ? t('adminUsers.enabled') : t('adminUsers.disabled') }}
              </span>
            </td>
            <td>{{ fmtTime(u.lastLoginAt) }}</td>
            <td>{{ fmtTime(u.createdAt) }}</td>
            <td>
              <div class="cell-actions">
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
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

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
              <option v-for="r in roles" :key="r.id" :value="r.id">{{ roleLabel(r.id) }}</option>
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

    <!-- ─── 关联分组 ─── -->
    <AppModal
      v-if="groupsOpen"
      :title="t('adminUsers.groupsTitle', { name: groupsTarget?.username })"
      width="md"
      :busy="groupsBusy"
      @close="closeGroups"
      @submit="submitGroups"
    >
      <p v-if="groupsBanner" class="banner banner--err" role="alert">{{ groupsBanner }}</p>
      <p class="modal-desc">{{ t('adminUsers.groupsDesc') }}</p>
      <p v-if="groups.length === 0" class="state">{{ t('adminUsers.groupsEmpty') }}</p>
      <div v-else class="group-list">
        <label v-for="g in groups" :key="g.id" class="group-item" :class="{ 'group-item--locked': !g.canManage }">
          <input
            v-model="groupDraft[g.id]"
            type="checkbox"
            :disabled="!g.canManage || groupsBusy"
            :title="g.canManage ? '' : t('adminUsers.groupsLockedHint')"
          />
          <span class="group-name">{{ g.name }}</span>
          <span class="group-meta">{{ t('adminUsers.groupsMembers', { n: g.members.length }) }}</span>
        </label>
      </div>

      <template #actions>
        <AppButton :disabled="groupsBusy" @click="closeGroups">{{ t('adminUsers.cancel') }}</AppButton>
        <AppButton variant="primary" type="submit" :loading="groupsBusy">
          {{ t('adminUsers.save') }}
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
.table-wrap {
  /* 多了「分组」一列后整表更宽:窄屏让它横向滚,别让七列互相挤成竖排字。 */
  overflow-x: auto;
}
.grid {
  width: 100%;
  min-width: 1000px;
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
/* 挂在内层 div 而不是 td:td 一旦 display:flex 就不再是 table-cell,这格的行底边线会短到按钮下面,列线看着就歪了。 */
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
/* 自定义角色没有预设配色:统一一档紫灰,新增角色不必再改表。 */
.tag--custom {
  background: rgba(139, 92, 246, 0.14);
  color: #6d28d9;
}
.cell-groups {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}
.tag--group {
  background: rgba(14, 165, 233, 0.13);
  color: #0369a1;
}
.modal-desc {
  font-size: var(--text-small, 0.85em);
  color: var(--color-faint);
  margin: 0 0 10px;
  max-width: 76ch;
}
.group-list {
  display: flex;
  flex-direction: column;
  gap: 2px;
  max-height: 320px;
  overflow-y: auto;
}
.group-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 7px 8px;
  border-radius: var(--radius-sm, 6px);
  font-size: var(--text-label);
  cursor: pointer;
}
.group-item:hover {
  background: var(--color-bg, #f8fafc);
}
.group-item--locked {
  cursor: not-allowed;
  color: var(--color-faint);
}
.group-name {
  font-weight: 600;
}
.group-meta {
  margin-left: auto;
  font-size: var(--text-small, 0.85em);
  color: var(--color-faint);
  white-space: nowrap;
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
