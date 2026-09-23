<script setup lang="ts">
/**
 * 分组与权限(v6.2 分组权限)—— 资源分组的名册管理页。
 *
 * 谁能看到什么由后端决定(GET /api/groups 已按可见范围过滤),页面只读 canManage
 * 结论来开关按钮:
 *   - 建组 / 删组      → 仅管理员(分组是「设置类」)
 *   - 改组 / 管成员    → 管理员或该组组长
 *   - 换组长           → 仅管理员
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

const { t } = useI18n()
const sessionStore = useSessionStore()

const isAdmin = computed(() => sessionStore.user?.role === 'admin')

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
    loadError.value = msg(err, 'groups.errLoad')
    loadState.value = 'error'
  }
}

onMounted(load)

function msg(err: unknown, fallback: string): string {
  if (err instanceof HttpError) {
    if (err.status === 0) return t('groups.errLoadConn')
    return err.apiError?.message ?? t(fallback)
  }
  return t(fallback)
}

// 名册里可加入的人:排除已在组内与组长(组长天然是组的人,不必重复列)。
function candidates(g: Group): GroupMember[] {
  const inGroup = new Set([g.ownerId, ...g.members.map((m) => m.id)])
  return roster.value.filter((u) => !inGroup.has(u.id))
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
      if (isAdmin.value && form.value.ownerId && form.value.ownerId !== g.ownerId) {
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
    formBanner.value = msg(err, 'groups.errSave')
    formSubmitting.value = false
    return
  }
  formSubmitting.value = false
}

// ─── 成员名册(编辑态弹窗内即时增删,离屏也能看清谁进了组) ──────────────────

const newMemberId = ref('')
const memberBusy = ref(false)

async function addMember(g: Group): Promise<void> {
  if (!newMemberId.value) return
  memberBusy.value = true
  formBanner.value = ''
  try {
    await addGroupMember(g.id, newMemberId.value)
    newMemberId.value = ''
    await load()
    const fresh = groups.value.find((x) => x.id === g.id)
    if (fresh) editing.value = fresh
  } catch (err) {
    formBanner.value = msg(err, 'groups.errMemberAdd')
  } finally {
    memberBusy.value = false
  }
}

async function dropMember(g: Group, userId: string): Promise<void> {
  memberBusy.value = true
  formBanner.value = ''
  try {
    await removeGroupMember(g.id, userId)
    await load()
    const fresh = groups.value.find((x) => x.id === g.id)
    if (fresh) editing.value = fresh
  } catch (err) {
    formBanner.value = msg(err, 'groups.errMemberRemove')
  } finally {
    memberBusy.value = false
  }
}

/** 弹窗刷新后把表单字段对齐到最新组数据(成员增删会重拉列表)。 */
const liveGroup = computed<Group | null>(() =>
  editing.value ? (groups.value.find((g) => g.id === editing.value?.id) ?? editing.value) : null,
)

// ─── delete ─────────────────────────────────────────────────────────────────

const deleteOpen = ref(false)
const deleting = ref<Group | null>(null)
const deleteSubmitting = ref(false)
const deleteBanner = ref('')

async function confirmDelete(): Promise<void> {
  if (!deleting.value) return
  deleteSubmitting.value = true
  deleteBanner.value = ''
  try {
    await deleteGroup(deleting.value.id)
    deleteOpen.value = false
    await load()
  } catch (err) {
    deleteBanner.value = msg(err, 'groups.errDelete')
  } finally {
    deleteSubmitting.value = false
  }
}

function ownerLabel(g: Group): string {
  return g.ownerName || g.ownerId
}

function isMine(g: Group): boolean {
  return isAdmin.value || sessionStore.user?.username === g.ownerName
}
</script>

<template>
  <div class="groups-view">
    <header class="view-header">
      <div>
        <h1 class="view-title">{{ t('groups.title') }}</h1>
        <p class="view-sub">{{ t('groups.desc') }}</p>
      </div>
      <div v-if="isAdmin" class="header-actions">
        <button class="btn btn--primary" @click="openAdd">+ {{ t('groups.add') }}</button>
      </div>
    </header>

    <p v-if="loadState === 'loading'" class="state">{{ t('common.refresh') }}…</p>
    <div v-else-if="loadState === 'error'" class="state state--error">
      <p>{{ loadError }}</p>
      <button class="btn" @click="load">{{ t('common.refresh') }}</button>
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
          <th>{{ t('groups.colActions') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="g in groups" :key="g.id">
          <td>
            <div class="cell-strong">
              {{ g.name }}
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
          <td class="actions">
            <button
              class="btn btn--sm"
              :disabled="!g.canManage"
              :title="g.canManage ? '' : t('groups.manageOnlyHint')"
              @click="openEdit(g)"
            >
              {{ t('groups.edit') }}
            </button>
            <button
              v-if="isAdmin"
              class="btn btn--sm btn--danger"
              @click="(deleting = g), (deleteOpen = true), (deleteBanner = '')"
            >
              {{ t('groups.delete') }}
            </button>
          </td>
        </tr>
      </tbody>
    </table>

    <!-- ── create / edit ── -->
    <div v-if="modalOpen" class="modal-mask" @click.self="modalOpen = false">
      <div class="modal" role="dialog">
        <h2 class="modal-title">
          {{ editing ? t('groups.editTitle') : t('groups.createTitle') }}
        </h2>
        <p class="modal-sub">{{ t('groups.formHint') }}</p>

        <div class="form-grid">
          <label class="field">
            <span>{{ t('groups.fieldName') }}</span>
            <input v-model="form.name" type="text" :placeholder="t('groups.namePlaceholder')" />
          </label>
          <label class="field">
            <span>{{ t('groups.fieldVisibility') }}</span>
            <select v-model="form.visibility">
              <option value="private">{{ t('groups.visibilityPrivate') }}</option>
              <option value="public">{{ t('groups.visibilityPublic') }}</option>
            </select>
            <small>{{
              form.visibility === 'public' ? t('groups.publicHint') : t('groups.privateHint')
            }}</small>
          </label>
          <label class="field field--wide">
            <span>{{ t('groups.fieldDesc') }}</span>
            <input v-model="form.description" type="text" />
          </label>
          <label v-if="isAdmin" class="field field--wide">
            <span>{{ t('groups.fieldOwner') }}</span>
            <select v-model="form.ownerId">
              <option value="">{{ t('groups.ownerSelf') }}</option>
              <option v-for="u in roster" :key="u.id" :value="u.id">{{ u.username }}</option>
            </select>
            <small>{{ t('groups.ownerHint') }}</small>
          </label>
          <label v-if="!editing" class="field field--wide">
            <span>{{ t('groups.fieldMembers') }}</span>
            <div class="picker">
              <select v-model="form.memberIds" multiple size="6">
                <option v-for="u in createCandidates" :key="u.id" :value="u.id">
                  {{ u.username }}
                </option>
              </select>
              <small>{{ t('groups.membersHint') }}</small>
            </div>
          </label>
        </div>

        <!-- 成员名册:编辑态逐人增删(每次操作立即落库,不是保存时才生效) -->
        <div v-if="liveGroup" class="roster">
          <h3 class="roster-title">{{ t('groups.fieldMembers') }}</h3>
          <p v-if="!liveGroup.members.length" class="cell-dim">{{ t('groups.noMembers') }}</p>
          <ul v-else class="roster-list">
            <li v-for="m in liveGroup.members" :key="m.id">
              <span class="chip">{{ m.username }}</span>
              <button
                class="link-btn"
                :disabled="memberBusy"
                @click="dropMember(liveGroup, m.id)"
              >
                {{ t('groups.removeMember') }}
              </button>
            </li>
          </ul>
          <div class="roster-add">
            <select v-model="newMemberId" :disabled="memberBusy">
              <option value="">{{ t('groups.memberAddPlaceholder') }}</option>
              <option v-for="u in candidates(liveGroup)" :key="u.id" :value="u.id">
                {{ u.username }}
              </option>
            </select>
            <button
              class="btn btn--sm"
              :disabled="!newMemberId || memberBusy"
              @click="addMember(liveGroup)"
            >
              {{ t('groups.addMember') }}
            </button>
          </div>
        </div>

        <p v-if="formBanner" class="banner banner--err">{{ formBanner }}</p>

        <footer class="modal-actions">
          <button class="btn" @click="modalOpen = false">{{ t('groups.cancel') }}</button>
          <button
            class="btn btn--primary"
            :disabled="formSubmitting || !form.name.trim()"
            @click="submitForm"
          >
            {{ formSubmitting ? t('groups.saving') : t('groups.save') }}
          </button>
        </footer>
      </div>
    </div>

    <!-- ── delete ── -->
    <div v-if="deleteOpen" class="modal-mask" @click.self="deleteOpen = false">
      <div class="modal modal--sm" role="dialog">
        <h2 class="modal-title">{{ t('groups.confirmDelete') }}</h2>
        <p class="modal-body">
          {{ t('groups.deleteBody', { name: deleting?.name ?? '' }) }}
        </p>
        <p class="modal-sub">{{ t('groups.deleteResourcesHint') }}</p>
        <p v-if="deleteBanner" class="banner banner--err">{{ deleteBanner }}</p>
        <footer class="modal-actions">
          <button class="btn" @click="deleteOpen = false">{{ t('groups.cancel') }}</button>
          <button class="btn btn--danger" :disabled="deleteSubmitting" @click="confirmDelete">
            {{ t('groups.delete') }}
          </button>
        </footer>
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
  margin-left: 6px;
  padding: 1px 8px;
  border-radius: 999px;
  font-size: var(--text-small, 0.78em);
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
  margin-left: 6px;
  background: rgba(59, 130, 246, 0.15);
  color: #2563eb;
}
.actions {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.actions .btn {
  flex: none;
  white-space: nowrap;
}
.cell-nowrap {
  white-space: nowrap;
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
.btn--primary {
  background: var(--color-primary);
  border-color: var(--color-primary);
  color: #fff;
}
.btn--danger {
  color: var(--color-danger, #dc2626);
  border-color: var(--color-danger, #dc2626);
}
.btn--sm {
  padding: 4px 10px;
  font-size: var(--text-small, 0.85em);
}
.link-btn {
  border: none;
  background: none;
  color: var(--color-faint);
  font-size: var(--text-small, 0.85em);
  cursor: pointer;
  text-decoration: underline;
}
.link-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.banner {
  margin-top: 14px;
  padding: 10px 14px;
  border-radius: 8px;
  background: rgba(0, 0, 0, 0.04);
  font-size: var(--text-label);
}
.banner--err {
  background: rgba(220, 38, 38, 0.1);
  color: #dc2626;
}
.modal-mask {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.5);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 100;
}
.modal {
  background: var(--color-bg, #fff);
  border-radius: 12px;
  padding: 24px;
  width: min(680px, 92vw);
  max-height: 88vh;
  overflow: auto;
}
.modal--sm {
  width: min(420px, 92vw);
}
.modal-title {
  font-size: var(--text-h3, 1.1rem);
  font-weight: 700;
  margin-bottom: 8px;
}
.modal-sub {
  font-size: var(--text-label);
  color: var(--color-faint);
  margin-bottom: 16px;
}
.modal-body {
  color: var(--color-dim);
}
.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 20px;
}
.form-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 14px;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 5px;
  font-size: var(--text-label);
}
.field--wide {
  grid-column: 1 / -1;
}
.field > span {
  font-weight: 600;
  color: var(--color-dim);
}
.field input,
.field select {
  padding: 8px 10px;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-bg, #fff);
  color: var(--color-text);
  font-size: var(--text-label);
  font-family: inherit;
}
.field small,
.picker small {
  color: var(--color-faint);
  font-size: var(--text-small, 0.85em);
}
.picker {
  display: flex;
  flex-direction: column;
  gap: 5px;
}
.picker select {
  padding: 6px;
}
.roster {
  margin-top: 18px;
  padding-top: 14px;
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
  margin: 0 0 10px;
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
  gap: 8px;
  align-items: center;
}
.roster-add select {
  flex: 1;
  padding: 8px 10px;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-bg, #fff);
  color: var(--color-text);
  font-size: var(--text-label);
}
</style>
