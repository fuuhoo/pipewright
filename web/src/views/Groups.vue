<script setup lang="ts">
/**
 * 分组与权限(v6.2 分组权限)—— 资源分组的名册管理页。
 *
 * 交互壳与「设置 > 用户管理」共用:表单弹窗走 ui/AppModal,删除走 useConfirm 的公共确认框,
 * 字段用 ui/FormField、按钮用 ui/AppButton —— 两页的「关闭/回车/报错」是同一套行为。
 *
 * 谁能看到什么由后端决定(GET /api/groups 已按可见范围过滤),页面只读结论开关按钮:
 *   - 建组 / 删组      → 「设置类」能力(分组本身是设置类资源)
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
import { useConfirm } from '../composables/useConfirm'
import { useToast } from '../composables/useToast'

const { t } = useI18n()
const sessionStore = useSessionStore()
const confirm = useConfirm()
const toast = useToast()

/** 「设置类」能力:与路由/侧栏同一份来源(lib/roles),不再各处写 role === 'admin'。 */
const canSettings = computed(() => sessionStore.canSettings)

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

/** 新建时的成员候选:全员名册去掉已选。 */
const createCandidates = computed(() => {
  const picked = new Set(form.value.memberIds)
  return roster.value.filter((u) => !picked.has(u.id))
})

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
    memberIds: [],
  }
  newMemberId.value = ''
  formBanner.value = ''
  modalOpen.value = true
}

/** 提交中/成员操作中不关弹窗:否则用户以为没保存,实际已经落库了。 */
function closeModal(): void {
  if (formSubmitting.value || memberBusy.value) return
  modalOpen.value = false
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

// ─── 成员名册(编辑态弹窗内即时增删,离屏也能看清谁进了组) ──────────────────

const newMemberId = ref('')
const memberBusy = ref(false)

async function addMember(): Promise<void> {
  const g = liveGroup.value
  if (!g || !newMemberId.value) return
  memberBusy.value = true
  formBanner.value = ''
  try {
    await addGroupMember(g.id, newMemberId.value)
    newMemberId.value = ''
    await load()
    const fresh = groups.value.find((x) => x.id === g.id)
    if (fresh) editing.value = fresh
  } catch (err) {
    formBanner.value = errText(err, 'groups.errMemberAdd')
  } finally {
    memberBusy.value = false
  }
}

async function dropMember(userId: string): Promise<void> {
  const g = liveGroup.value
  if (!g) return
  memberBusy.value = true
  formBanner.value = ''
  try {
    await removeGroupMember(g.id, userId)
    await load()
    const fresh = groups.value.find((x) => x.id === g.id)
    if (fresh) editing.value = fresh
  } catch (err) {
    formBanner.value = errText(err, 'groups.errMemberRemove')
  } finally {
    memberBusy.value = false
  }
}

/** 弹窗刷新后把表单字段对齐到最新组数据(成员增删会重拉列表)。 */
const liveGroup = computed<Group | null>(() =>
  editing.value ? (groups.value.find((g) => g.id === editing.value?.id) ?? editing.value) : null,
)

/** 名册里可加入的人:排除已在组内与组长(组长天然是组的人,不必重复列)。 */
const rosterCandidates = computed<GroupMember[]>(() => {
  const g = liveGroup.value
  if (!g) return []
  const inGroup = new Set([g.ownerId, ...g.members.map((m) => m.id)])
  return roster.value.filter((u) => !inGroup.has(u.id))
})

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
        <h1 class="view-title">{{ t('groups.title') }}</h1>
        <p class="view-sub">{{ t('groups.desc') }}</p>
      </div>
      <div v-if="canSettings" class="header-actions">
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
              {{ t('groups.resourceCounts', { projects: g.projectCount, servers: g.serverCount }) }}
            </div>
          </td>
          <td class="cell-actions">
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
      :busy="formSubmitting || memberBusy"
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
          v-if="!editing"
          class="field--wide"
          :label="t('groups.fieldMembers')"
          field-id="group-members"
          :hint="t('groups.membersHint')"
        >
          <template #default="{ fieldId }">
            <select
              :id="fieldId"
              v-model="form.memberIds"
              class="ui-input ui-input--multi"
              multiple
              size="6"
              :disabled="formSubmitting"
            >
              <option v-for="u in createCandidates" :key="u.id" :value="u.id">
                {{ u.username }}
              </option>
            </select>
          </template>
        </FormField>
      </div>

      <!-- 成员名册:编辑态逐人增删(每次操作立即落库,不是保存时才生效) -->
      <div v-if="liveGroup" class="roster">
        <h3 class="roster-title">{{ t('groups.fieldMembers') }}</h3>
        <p v-if="!liveGroup.members.length" class="cell-dim">{{ t('groups.noMembers') }}</p>
        <ul v-else class="roster-list">
          <li v-for="m in liveGroup.members" :key="m.id">
            <span class="chip">{{ m.username }}</span>
            <AppButton variant="ghost" size="sm" :disabled="memberBusy" @click="dropMember(m.id)">
              {{ t('groups.removeMember') }}
            </AppButton>
          </li>
        </ul>
        <div class="roster-add">
          <select v-model="newMemberId" class="ui-input" :disabled="memberBusy">
            <option value="">{{ t('groups.memberAddPlaceholder') }}</option>
            <option v-for="u in rosterCandidates" :key="u.id" :value="u.id">
              {{ u.username }}
            </option>
          </select>
          <AppButton size="sm" :disabled="!newMemberId || memberBusy" @click="addMember">
            {{ t('groups.addMember') }}
          </AppButton>
        </div>
      </div>

      <template #actions>
        <AppButton :disabled="formSubmitting || memberBusy" @click="closeModal">
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
.cell-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  white-space: nowrap;
}
.cell-nowrap {
  white-space: nowrap;
}
/* 弹窗里的成员名册:slot 内容带着本页的 scope,所以样式留在这一侧。 */
.roster {
  padding-top: var(--space-3);
  border-top: 1px solid var(--color-border);
}
.roster-title {
  font-size: var(--text-label);
  font-weight: 600;
  color: var(--color-dim);
  margin-bottom: 8px;
}
.roster-list {
  list-style: none;
  padding: 0;
  margin: 0 0 var(--space-3);
  display: flex;
  flex-wrap: wrap;
  gap: 8px 14px;
}
.roster-list li {
  display: flex;
  align-items: center;
  gap: 6px;
}
.roster-add {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}
.roster-add .ui-input {
  max-width: 320px;
}
</style>
