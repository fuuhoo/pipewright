<script setup lang="ts">
/**
 * 分组与权限(v6.2 分组权限)—— 资源分组的名册管理页,挂在「用户与权限」壳(/permissions)的
 * 分组 tab 下;另一 tab 是 admin/Users.vue,两页同处一页。
 *
 * 交互壳与用户管理 tab 共用:表单弹窗走 ui/AppModal,删除走 useConfirm 的公共确认框,
 * 字段用 ui/FormField、按钮用 ui/AppButton —— 两页的「关闭/回车/报错」是同一套行为。
 *
 * 谁能看到什么由后端决定(GET /api/groups 已按可见范围过滤),页面只读结论开关按钮:
 *   - 建组             → 功能点 group.create(默认只有内置管理员,可单独授予角色)
 *   - 删组             → 「设置类」能力(后端路由挂在 RequireAdmin 下)
 *   - 改组 / 管成员    → 有设置能力或该组组长(canManage 由后端给)
 *   - 换组长           → 仅有设置能力
 * 未归组(资源 groupId='')= 全员可见可操作,是存量数据的默认态。
 */
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  listGroups,
  createGroup,
  updateGroup,
  deleteGroup,
  addGroupMember,
  removeGroupMember,
  listAssignableUsers,
} from '../api/groups'
import type { Group, GroupMember, GroupVisibility, UpdateGroupInput } from '../api/groups'
import { HttpError } from '../api/http'
import { useSessionStore } from '../stores/session'
import AppModal from '../components/ui/AppModal.vue'
import AppButton from '../components/ui/AppButton.vue'
import FormField from '../components/ui/FormField.vue'
import TransferList from '../components/ui/TransferList.vue'
import { useConfirm } from '../composables/useConfirm'
import { useToast } from '../composables/useToast'

const { t } = useI18n()
const sessionStore = useSessionStore()
const confirm = useConfirm()
const toast = useToast()

/** 「设置类」能力:与路由/侧栏同一份来源(lib/roles),不再各处写 role === 'admin'。 */
const canSettings = computed(() => sessionStore.canSettings)

/**
 * 建组是一个独立功能点(group.create):后端 POST /api/groups 按点判,不按管理员判。
 * 默认只有内置管理员持有;自定义角色可以被单独授予「能开新组、但改不动各组名册」。
 * 删组仍是管理员专属(后端路由挂在 RequireAdmin 下),所以那两处继续用 canSettings。
 */
const canCreateGroup = computed(() => sessionStore.canPerm('group.create'))

// ─── state ──────────────────────────────────────────────────────────────────

const loadState = ref<'idle' | 'loading' | 'error'>('idle')
const loadError = ref('')
const groups = ref<Group[]>([])
const roster = ref<GroupMember[]>([])

async function load(): Promise<void> {
  loadState.value = 'loading'
  loadError.value = ''
  try {
    const [list, users] = await Promise.all([listGroups(), listAssignableUsers()])
    groups.value = list
    roster.value = users
    loadState.value = 'idle'
  } catch (err) {
    loadError.value = errText(err, 'groups.errLoad')
    loadState.value = 'error'
  }
}

onMounted(load)

/** 校验失败时用后端原文,其余按状态码给一句人话(与用户管理页同口径)。 */
function errText(err: unknown, key: string): string {
  if (err instanceof HttpError) {
    if (err.status === 0) return t('groups.errLoadConn')
    return err.apiError?.message ?? t(key, { status: err.status })
  }
  return t('groups.errRetry')
}

// ─── create / edit modal ────────────────────────────────────────────────────

const modalOpen = ref(false)
const editing = ref<Group | null>(null)
const formSubmitting = ref(false)
const formBanner = ref('')

const form = ref<{
  name: string
  description: string
  visibility: GroupVisibility
  ownerId: string
  memberIds: string[]
}>({ name: '', description: '', visibility: 'private', ownerId: '', memberIds: [] })

function openAdd(): void {
  editing.value = null
  form.value = { name: '', description: '', visibility: 'private', ownerId: '', memberIds: [] }
  formBanner.value = ''
  modalOpen.value = true
}

function openEdit(g: Group): void {
  editing.value = g
  form.value = {
    name: g.name,
    description: g.description,
    visibility: g.visibility,
    ownerId: g.ownerId,
    memberIds: g.members.map((m) => m.id),
  }
  formBanner.value = ''
  modalOpen.value = true
}

/** 提交中不关弹窗:否则用户以为没保存,实际已经落库了。 */
function closeModal(): void {
  if (formSubmitting.value) return
  modalOpen.value = false
}

/**
 * 编辑保存时把勾选结果折算成增/删:后端只有单人增删接口。
 * 两个方向都幂等,所以上一次保存半途失败后直接重试也不会重复加人或漏删。
 */
async function syncMembers(groupId: string, before: string[]): Promise<void> {
  const was = new Set(before)
  const now = new Set(form.value.memberIds)
  for (const id of now) {
    if (!was.has(id)) await addGroupMember(groupId, id)
  }
  for (const id of was) {
    if (!now.has(id)) await removeGroupMember(groupId, id)
  }
}

async function submitForm(): Promise<void> {
  formSubmitting.value = true
  formBanner.value = ''
  try {
    const g = editing.value
    if (g) {
      const patch: UpdateGroupInput = {
        name: form.value.name,
        description: form.value.description,
        visibility: form.value.visibility,
      }
      // 换组长只有管理员能传,组长自己提交时不带这个键(否则 403)。
      if (canSettings.value && form.value.ownerId && form.value.ownerId !== g.ownerId) {
        patch.ownerId = form.value.ownerId
      }
      await updateGroup(g.id, patch)
      await syncMembers(g.id, g.members.map((m) => m.id))
    } else {
      await createGroup({
        name: form.value.name,
        description: form.value.description,
        visibility: form.value.visibility,
        ownerId: form.value.ownerId || undefined,
        memberIds: form.value.memberIds,
      })
    }
    modalOpen.value = false
    await load()
  } catch (err) {
    formBanner.value = errText(err, 'groups.errSave')
  } finally {
    formSubmitting.value = false
  }
}

/** 当前表单里的组长 id:新建时 ownerId 为空表示「我自己」,按用户名从名册里认回来。 */
const ownerId = computed(() => {
  if (form.value.ownerId) return form.value.ownerId
  const self = sessionStore.user?.username
  return roster.value.find((u) => u.username === self)?.id ?? ''
})

/** 穿梭框的候选项:名册只有 id + username,展示名直接用用户名;createdAt 供「最新的在前」排序。 */
const memberOptions = computed(() =>
  roster.value.map((u) => ({ id: u.id, label: u.username, createdAt: u.createdAt })),
)

// ─── delete ─────────────────────────────────────────────────────────────────

/**
 * 删组走公共确认框(与用户管理页的启停同一壳)。
 * 确认框在请求发出前就关掉了,失败没有地方塞横幅,只能用 toast 说清「没删掉」。
 */
async function askDelete(g: Group): Promise<void> {
  const ok = await confirm.open({
    title: t('groups.confirmDelete'),
    body: `${t('groups.deleteBody', { name: g.name })} ${t('groups.deleteResourcesHint')}`,
    confirmLabel: t('groups.delete'),
    variant: 'danger',
  })
  if (!ok) return
  try {
    await deleteGroup(g.id)
    await load()
  } catch (err) {
    toast.error(errText(err, 'groups.errDelete'))
  }
}

function ownerLabel(g: Group): string {
  return g.ownerName || g.ownerId
}

/**
 * 「我管理的」标:有设置能力的人看什么都算管理,否则按组长本人。
 * 会话里只有用户名没有用户 id(/api/auth/session 不回 id),所以这里按名比对。
 */
function isMine(g: Group): boolean {
  return canSettings.value || sessionStore.user?.username === g.ownerName
}
</script>

<template>
  <div class="groups-view">
    <header class="view-header">
      <div>
        <h2 class="view-title">{{ t('groups.title') }}</h2>
        <p class="view-sub">{{ t('groups.desc') }}</p>
      </div>
      <div v-if="canCreateGroup" class="header-actions">
        <AppButton variant="primary" @click="openAdd">+ {{ t('groups.add') }}</AppButton>
      </div>
    </header>

    <p v-if="loadState === 'loading'" class="state">{{ t('common.refresh') }}…</p>
    <div v-else-if="loadState === 'error'" class="state state--error">
      <p>{{ loadError }}</p>
      <AppButton @click="load">{{ t('common.refresh') }}</AppButton>
    </div>
    <p v-else-if="groups.length === 0" class="state">{{ t('groups.empty') }}</p>

    <table v-else class="grid">
      <thead>
        <tr>
          <th>{{ t('groups.colGroup') }}</th>
          <th>{{ t('groups.colVisibility') }}</th>
          <th>{{ t('groups.colOwner') }}</th>
          <th>{{ t('groups.colMembers') }}</th>
          <th>{{ t('groups.colResources') }}</th>
          <th class="th-actions">{{ t('groups.colActions') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="g in groups" :key="g.id">
          <td>
            <div class="cell-strong name-row">
              <span>{{ g.name }}</span>
              <span v-if="isMine(g)" class="tag tag--mine">{{ t('groups.mine') }}</span>
            </div>
            <div v-if="g.description" class="cell-dim">{{ g.description }}</div>
          </td>
          <td>
            <span class="tag" :class="g.visibility === 'public' ? 'tag--public' : 'tag--private'">
              {{
                g.visibility === 'public' ? t('groups.visibilityPublic') : t('groups.visibilityPrivate')
              }}
            </span>
            <div class="cell-dim">
              {{
                g.visibility === 'public' ? t('groups.publicHint') : t('groups.privateHint')
              }}
            </div>
          </td>
          <td>{{ ownerLabel(g) }}</td>
          <td>
            <div v-if="g.members.length" class="chips">
              <span v-for="m in g.members" :key="m.id" class="chip">{{ m.username }}</span>
            </div>
            <span v-else class="cell-dim">{{ t('groups.noMembers') }}</span>
            <div class="cell-dim">{{ t('groups.memberCount', { n: g.members.length }) }}</div>
          </td>
          <td>
            <div class="cell-dim cell-nowrap">
              {{
                t('groups.resourceCounts', {
                  projects: g.projectCount,
                  servers: g.serverCount,
                  varGroups: g.variableGroupCount,
                })
              }}
            </div>
          </td>
          <td>
            <div class="cell-actions">
              <AppButton
                variant="ghost"
                size="sm"
                :disabled="!g.canManage"
                :title="g.canManage ? '' : t('groups.manageOnlyHint')"
                @click="openEdit(g)"
              >
                {{ t('groups.edit') }}
              </AppButton>
              <AppButton v-if="canSettings" variant="danger" size="sm" @click="askDelete(g)">
                {{ t('groups.delete') }}
              </AppButton>
            </div>
          </td>
        </tr>
      </tbody>
    </table>

    <!-- ── create / edit ── -->
    <AppModal
      v-if="modalOpen"
      :title="editing ? t('groups.editTitle') : t('groups.createTitle')"
      :subtitle="t('groups.formHint')"
      width="lg"
      :busy="formSubmitting"
      @close="closeModal"
      @submit="submitForm"
    >
      <p v-if="formBanner" class="banner banner--err" role="alert">{{ formBanner }}</p>

      <div class="form-grid">
        <FormField :label="t('groups.fieldName')" field-id="group-name" required>
          <template #default="{ fieldId }">
            <input
              :id="fieldId"
              v-model="form.name"
              class="ui-input"
              type="text"
              autocomplete="off"
              :placeholder="t('groups.namePlaceholder')"
              :disabled="formSubmitting"
            />
          </template>
        </FormField>

        <FormField
          :label="t('groups.fieldVisibility')"
          field-id="group-visibility"
          :hint="form.visibility === 'public' ? t('groups.publicHint') : t('groups.privateHint')"
        >
          <template #default="{ fieldId, ariaDescribedby }">
            <select
              :id="fieldId"
              v-model="form.visibility"
              class="ui-input"
              :aria-describedby="ariaDescribedby"
              :disabled="formSubmitting"
            >
              <option value="private">{{ t('groups.visibilityPrivate') }}</option>
              <option value="public">{{ t('groups.visibilityPublic') }}</option>
            </select>
          </template>
        </FormField>

        <FormField class="field--wide" :label="t('groups.fieldDesc')" field-id="group-desc">
          <template #default="{ fieldId }">
            <input
              :id="fieldId"
              v-model="form.description"
              class="ui-input"
              type="text"
              autocomplete="off"
              :disabled="formSubmitting"
            />
          </template>
        </FormField>

        <FormField
          v-if="canSettings"
          class="field--wide"
          :label="t('groups.fieldOwner')"
          field-id="group-owner"
          :hint="t('groups.ownerHint')"
        >
          <template #default="{ fieldId }">
            <select :id="fieldId" v-model="form.ownerId" class="ui-input" :disabled="formSubmitting">
              <option value="">{{ t('groups.ownerSelf') }}</option>
              <option v-for="u in roster" :key="u.id" :value="u.id">{{ u.username }}</option>
            </select>
          </template>
        </FormField>

        <FormField
          class="field--wide"
          :label="t('groups.fieldMembers')"
          field-id="group-members"
          :hint="t('groups.membersHint')"
        >
          <template #default="{ fieldId, ariaDescribedby }">
            <!-- 新建与编辑共用同一个穿梭框:右栏就是当前名册,左栏是可加入的人。
                 组长天然拥有组内权限,作为锁定项显示在右栏,不占名册名额。 -->
            <TransferList
              :id="fieldId"
              v-model="form.memberIds"
              :options="memberOptions"
              :locked-ids="ownerId ? [ownerId] : []"
              :locked-label="t('groups.colOwner')"
              :disabled="formSubmitting"
              :aria-describedby="ariaDescribedby"
            />
          </template>
        </FormField>
      </div>

      <template #actions>
        <AppButton :disabled="formSubmitting" @click="closeModal">
          {{ t('groups.cancel') }}
        </AppButton>
        <AppButton
          variant="primary"
          type="submit"
          :loading="formSubmitting"
          :disabled="!form.name.trim()"
        >
          {{ t('groups.save') }}
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
.header-actions {
  display: flex;
  gap: 8px;
  flex-shrink: 0;
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
.chips {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  max-width: 34ch;
}
.chip {
  display: inline-block;
  padding: 1px 8px;
  border-radius: 999px;
  background: rgba(120, 120, 120, 0.13);
  color: var(--color-dim);
  font-size: var(--text-small, 0.8em);
}
.tag {
  display: inline-block;
  padding: 2px 10px;
  border-radius: 999px;
  font-size: var(--text-small, 0.8em);
  font-weight: 600;
}
.tag--public {
  background: rgba(22, 163, 74, 0.15);
  color: #15803d;
}
.tag--private {
  background: rgba(234, 179, 8, 0.18);
  color: #a16207;
}
.tag--mine {
  background: rgba(59, 130, 246, 0.15);
  color: #2563eb;
}
.name-row {
  display: flex;
  align-items: center;
  gap: 6px;
}
.th-actions {
  text-align: right;
}
/* 挂在内层 div 而不是 td:td 一旦 display:flex 就不再是 table-cell,这格的行底边线会短到按钮下面,列线看着就歪了。 */
.cell-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  white-space: nowrap;
}
.cell-nowrap {
  white-space: nowrap;
}
</style>
