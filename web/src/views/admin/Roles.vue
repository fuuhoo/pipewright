<script setup lang="ts">
/**
 * 角色管理 —— 挂在「用户与权限」壳(/permissions)的第三个 tab。
 *
 * 门与「账号与角色」tab 完全一样:挂在子路由的 beforeEnter 上(见 router/index.ts),
 * 没有「设置类」能力的人根本不会挂载本组件,也就不会发一次注定 403 的 GET /api/admin/roles;
 * 后端 /api/admin/roles 的 RequireAdmin 才是权威。
 *
 * 这一页只改**功能轴**(这个角色能不能看到某个入口)。「这份数据归谁」在分组 tab,
 * 「谁挂哪个角色」在账号 tab —— 三条线各改一件事,是这套权限设计的底子。
 *
 * 内置只剩「管理员」一档:它的点集来自代码表(access/perms.go),页面上不可改、不可删,
 * 只能「复制一份再调」。留着的理由是 admin 改不掉、删不掉,等于给「把自己锁在门外」兜了底。
 * 其余四档(user / developer / ops / viewer)自 0063 迁移起也是 roles 表里的普通行:名字、
 * 点集、删留都归这一页管,和自建角色同一条路径 —— 所以模板下拉列的是整份服务端名单,
 * 而不是某一类「内置档」。
 *
 * 生效时机是这里唯一容易误解的地方,所以写在弹窗里:改**点集**对方刷新页面就生效
 * (能力位每次 /api/auth/session 重算);把**人**换到另一个角色才需要对方重新登录。
 */
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  listRoles,
  listPermPoints,
  createRole,
  updateRole,
  deleteRole,
  copyRole,
} from '../../api/roles'
import type { PermPoint, Role } from '../../api/roles'
import type { PermId } from '../../api/auth'
import { HttpError } from '../../api/http'
import {
  MAX_ROLE_NAME_LEN,
  canDeleteRole,
  groupPoints,
  labelForRoleId,
  permsDiff,
  permLabelKey,
  seedFromTemplate,
  validateRoleName,
  type NameIssue,
} from '../../lib/roleEditor'
import AppModal from '../../components/ui/AppModal.vue'
import AppButton from '../../components/ui/AppButton.vue'
import FormField from '../../components/ui/FormField.vue'
import { useConfirm } from '../../composables/useConfirm'
import { useToast } from '../../composables/useToast'

const { t } = useI18n()
const confirm = useConfirm()
const toast = useToast()

// ─── 加载 ─────────────────────────────────────────────────────────────────────

const loadState = ref<'idle' | 'loading' | 'error'>('idle')
const loadError = ref('')
const roles = ref<Role[]>([])
const points = ref<PermPoint[]>([])

async function load(): Promise<void> {
  loadState.value = 'loading'
  loadError.value = ''
  try {
    // 点集字典与角色名单一起取:缺任何一份,编辑器都渲染不出可用的勾选项。
    const [roleList, pointList] = await Promise.all([listRoles(), listPermPoints()])
    roles.value = roleList
    points.value = pointList
    loadState.value = 'idle'
  } catch (err) {
    loadError.value = errText(err, 'roleEditor.errLoad')
    loadState.value = 'error'
  }
}

onMounted(load)

/** 校验失败时用后端原文,其余按状态码给一句人话(与账号 tab 同口径)。 */
function errText(err: unknown, key: string): string {
  if (err instanceof HttpError) {
    if (err.status === 0) return t('roleEditor.errLoadConn')
    return err.apiError?.message ?? t(key, { status: err.status })
  }
  return t('roleEditor.errRetry')
}

const rolesById = computed<Record<string, Role>>(() => {
  const out: Record<string, Role> = {}
  for (const r of roles.value) out[r.id] = r
  return out
})

const groups = computed(() => groupPoints(points.value))

function roleLabel(id: string): string {
  return labelForRoleId(id, rolesById.value, t)
}

/**
 * 没被人改过名的预置档:id 稳定(user / developer / ops / viewer),名字还等于 id 本身,
 * 所以展示走 i18n(见 labelForRoleId)。自建角色的 id 是 UUID,名字永远不等于它,
 * 内置 admin 另有 builtin,故这里要排掉 —— 三个条件其实只有一条真的在筛。
 */
function isPresetRole(r: Role): boolean {
  return !r.builtin && r.name === r.id
}

function pointLabel(id: PermId): string {
  return t(permLabelKey(id))
}

// ─── 编辑器(新建 / 编辑同一弹窗)──────────────────────────────────────────────

const editOpen = ref(false)
const editing = ref<Role | null>(null)
const busy = ref(false)
const banner = ref('')
const form = ref<{ name: string; description: string; baseRole: string; perms: PermId[] }>({
  name: '',
  description: '',
  baseRole: '',
  perms: [],
})
const nameIssue = ref<NameIssue>('')

const NAME_ISSUE_KEY: Record<Exclude<NameIssue, ''>, string> = {
  required: 'roleEditor.nameErrRequired',
  tooLong: 'roleEditor.nameErrTooLong',
  duplicate: 'roleEditor.nameErrDuplicate',
  reserved: 'roleEditor.nameErrReserved',
}

function openCreate(): void {
  editing.value = null
  // 默认以「普通用户」为模板:它是平台的默认档,也是自定义角色最常见的起点。
  // 它自 0063 起是库里的普通角色,但 id 稳定,所以这里仍按 id 取;管理员删了它就拿不到点集,
  // 弹窗退回「一个入口都不给」的空名单,照样能手动勾。设置类总闸由 seedFromTemplate 剥掉。
  form.value = {
    name: '',
    description: '',
    baseRole: 'user',
    perms: seedFromTemplate(rolesById.value['user']),
  }
  nameIssue.value = ''
  banner.value = ''
  editOpen.value = true
}

function openEdit(r: Role): void {
  if (r.builtin) return
  editing.value = r
  form.value = { name: r.name, description: r.description, baseRole: r.baseRole, perms: [...r.perms] }
  nameIssue.value = ''
  banner.value = ''
  editOpen.value = true
}

function closeEdit(): void {
  if (busy.value) return
  editOpen.value = false
  editing.value = null
}

/** 换模板 = 重新带出那份点集(手改过再换模板会被覆盖,这是「以模板为准」的直觉)。 */
function applyTemplate(id: string): void {
  form.value.baseRole = id
  form.value.perms = seedFromTemplate(id ? rolesById.value[id] : null)
}

function togglePoint(id: PermId, on: boolean): void {
  if (on) {
    if (!form.value.perms.includes(id)) form.value.perms = [...form.value.perms, id]
    return
  }
  form.value.perms = form.value.perms.filter((x) => x !== id)
}

const selectedCount = computed(() => form.value.perms.length)
const assignableCount = computed(() => points.value.filter((p) => !p.builtinOnly).length)

function toggleGroup(members: PermPoint[], on: boolean): void {
  const ids = members.filter((p) => !p.builtinOnly).map((p) => p.id)
  if (on) {
    const set = new Set(form.value.perms)
    form.value.perms = [...form.value.perms, ...ids.filter((id) => !set.has(id))]
    return
  }
  const off = new Set(ids)
  form.value.perms = form.value.perms.filter((id) => !off.has(id))
}

function toggleAll(on: boolean): void {
  form.value.perms = on
    ? points.value.filter((p) => !p.builtinOnly).map((p) => p.id)
    : []
}

function inGroupCount(members: PermPoint[]): number {
  const picked = new Set(form.value.perms)
  return members.filter((p) => picked.has(p.id)).length
}

// ─── 列表里的点集展开 ─────────────────────────────────────────────────────────

/**
 * 一行 21 个芯片会把整页撑成墙,所以默认只露前几个 + 「还有 N 个」。
 * 内置档点不开编辑器(不可改),这一列就是唯一能看到它点集的地方,因此必须能展开全部。
 */
const PEEK_PERMS = 6
const expanded = ref<Record<string, boolean>>({})

function isExpanded(id: string): boolean {
  return expanded.value[id] === true
}

function visiblePerms(r: Role): PermId[] {
  return isExpanded(r.id) ? r.perms : r.perms.slice(0, PEEK_PERMS)
}

function hiddenPermCount(r: Role): number {
  return Math.max(0, r.perms.length - PEEK_PERMS)
}

async function submitEdit(): Promise<void> {
  const current = editing.value
  nameIssue.value = validateRoleName(form.value.name, roles.value, current?.id ?? '')
  if (nameIssue.value) return
  if (form.value.perms.includes('settings.access')) {
    // 勾不到(UI 禁用),走到这里只可能是名单过期后的历史勾选 —— 后端也会 422。
    banner.value = t('roleEditor.settingsBlocked')
    return
  }

  // 改点集是给一批人同时改权限,值得一次「你知道自己在动几个入口」的确认。
  if (current) {
    const diff = permsDiff(current.perms, form.value.perms)
    if ((diff.added.length || diff.removed.length) && current.userCount > 0) {
      const ok = await confirm.open({
        title: t('roleEditor.permChangeTitle'),
        body: `${t('roleEditor.permChangeBody', {
          name: roleLabel(current.id),
          users: current.userCount,
          added: diff.added.length,
          removed: diff.removed.length,
        })} ${t('roleEditor.permChangeEffect')}`,
        confirmLabel: t('roleEditor.save'),
        variant: 'primary',
      })
      if (!ok) return
    }
  }

  busy.value = true
  banner.value = ''
  try {
    if (current) {
      const fresh = await updateRole(current.id, {
        name: form.value.name.trim(),
        description: form.value.description.trim(),
        perms: form.value.perms,
      })
      roles.value = roles.value.map((r) => (r.id === fresh.id ? fresh : r))
    } else {
      const fresh = await createRole({
        name: form.value.name.trim(),
        description: form.value.description.trim(),
        baseRole: form.value.baseRole || undefined,
        perms: form.value.perms,
      })
      roles.value = [...roles.value, fresh]
    }
    editOpen.value = false
    editing.value = null
  } catch (err) {
    banner.value = errText(err, 'roleEditor.errSave')
  } finally {
    busy.value = false
  }
}

// ─── 复制成自定义角色 ─────────────────────────────────────────────────────────

const copyOpen = ref(false)
const copySource = ref<Role | null>(null)
const copyName = ref('')
const copyBusy = ref(false)
const copyBanner = ref('')
const copyIssue = ref<NameIssue>('')

function openCopy(r: Role): void {
  copySource.value = r
  // 建议名走 i18n(「副本」这个词各语言不同),用户照样能改;重名在提交前被 validateRoleName 拦下。
  copyName.value = t('roleEditor.copyNameSuggest', { name: roleLabel(r.id) })
  copyIssue.value = ''
  copyBanner.value = ''
  copyOpen.value = true
}

function closeCopy(): void {
  if (copyBusy.value) return
  copyOpen.value = false
  copySource.value = null
}

async function submitCopy(): Promise<void> {
  const src = copySource.value
  if (!src) return
  copyIssue.value = validateRoleName(copyName.value, roles.value)
  if (copyIssue.value) return
  copyBusy.value = true
  copyBanner.value = ''
  try {
    const fresh = await copyRole(src.id, copyName.value.trim())
    roles.value = [...roles.value, fresh]
    copyOpen.value = false
    copySource.value = null
    // 复制只是「拿到一份一样的点集」,大多数人下一步是改它 —— 直接把编辑器接上。
    openEdit(fresh)
  } catch (err) {
    copyBanner.value = errText(err, 'roleEditor.errCopy')
  } finally {
    copyBusy.value = false
  }
}

// ─── 删除 ─────────────────────────────────────────────────────────────────────

/**
 * 删除走公共确认框。挡门的是 userCount:后端在还有人挂着这个角色时回 409,
 * 与其让人撞错,不如把按钮 disable 掉并在那儿写明「还有 N 个账号」。
 */
async function askDelete(r: Role): Promise<void> {
  const ok = await confirm.open({
    title: t('roleEditor.deleteTitle'),
    body: `${t('roleEditor.deleteBody', { name: roleLabel(r.id) })} ${t('roleEditor.deleteEffect')}`,
    confirmLabel: t('roleEditor.delete'),
    variant: 'danger',
  })
  if (!ok) return
  try {
    await deleteRole(r.id)
    roles.value = roles.value.filter((x) => x.id !== r.id)
  } catch (err) {
    toast.error(errText(err, 'roleEditor.errDelete'))
  }
}
</script>

<template>
  <div class="roles-view">
    <header class="view-header">
      <div>
        <h2 class="view-title">{{ t('roleEditor.title') }}</h2>
        <p class="view-sub">{{ t('roleEditor.desc') }}</p>
        <p class="view-sub view-sub--hint">{{ t('roleEditor.axesHint') }}</p>
      </div>
      <div class="header-actions">
        <AppButton variant="primary" @click="openCreate">+ {{ t('roleEditor.add') }}</AppButton>
      </div>
    </header>

    <p v-if="loadState === 'loading'" class="state">{{ t('common.refresh') }}…</p>
    <div v-else-if="loadState === 'error'" class="state state--error">
      <p>{{ loadError }}</p>
      <AppButton @click="load">{{ t('common.refresh') }}</AppButton>
    </div>
    <p v-else-if="roles.length === 0" class="state">{{ t('roleEditor.empty') }}</p>

    <div v-else class="table-wrap">
      <table class="grid">
        <thead>
          <tr>
            <th class="th-role">{{ t('roleEditor.colRole') }}</th>
            <th class="th-template">{{ t('roleEditor.colTemplate') }}</th>
            <th class="th-perms">{{ t('roleEditor.colPerms') }}</th>
            <th class="th-users">{{ t('roleEditor.colUsers') }}</th>
            <th class="th-actions">{{ t('roleEditor.colActions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="r in roles" :key="r.id">
            <td>
              <div class="cell-strong name-row">
                <span>{{ roleLabel(r.id) }}</span>
                <span v-if="r.builtin" class="tag tag--builtin">{{ t('roleEditor.builtinTag') }}</span>
              </div>
              <div v-if="r.description" class="cell-dim">{{ r.description }}</div>
            </td>
            <td>
              <span v-if="r.baseRole" class="cell-dim cell-nowrap">{{ roleLabel(r.baseRole) }}</span>
              <span v-else class="cell-dim">{{ t('roleEditor.noTemplate') }}</span>
            </td>
            <td class="cell-perms">
              <div class="cell-dim">
                {{ t('roleEditor.permCount', { n: r.perms.length, total: points.length }) }}
              </div>
              <div v-if="r.perms.length" class="chips">
                <span v-for="id in visiblePerms(r)" :key="id" class="chip">{{ pointLabel(id) }}</span>
                <button
                  v-if="hiddenPermCount(r)"
                  class="chip chip--more"
                  type="button"
                  @click="expanded[r.id] = !isExpanded(r.id)"
                >
                  {{
                    isExpanded(r.id)
                      ? t('roleEditor.permsCollapse')
                      : t('roleEditor.permsMore', { n: hiddenPermCount(r) })
                  }}
                </button>
              </div>
              <div v-else class="cell-dim">{{ t('roleEditor.permsNone') }}</div>
            </td>
            <td class="cell-nowrap">
              <span :class="r.userCount ? 'cell-strong' : 'cell-dim'">
                {{ t('roleEditor.userCount', { n: r.userCount }) }}
              </span>
            </td>
            <td>
              <div class="cell-actions">
                <AppButton
                  variant="ghost"
                  size="sm"
                  :disabled="r.builtin"
                  :title="r.builtin ? t('roleEditor.builtinEditHint') : ''"
                  @click="openEdit(r)"
                >
                  {{ t('roleEditor.edit') }}
                </AppButton>
                <AppButton variant="ghost" size="sm" @click="openCopy(r)">
                  {{ t('roleEditor.copy') }}
                </AppButton>
                <AppButton
                  variant="danger"
                  size="sm"
                  :disabled="!canDeleteRole(r)"
                  :title="
                    r.builtin
                      ? t('roleEditor.builtinEditHint')
                      : r.userCount
                        ? t('roleEditor.deleteBlockedHint', { n: r.userCount })
                        : ''
                  "
                  @click="askDelete(r)"
                >
                  {{ t('roleEditor.delete') }}
                </AppButton>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- ─── 新建 / 编辑:点集编辑器 ─── -->
    <AppModal
      v-if="editOpen"
      :title="editing ? t('roleEditor.editTitle', { name: roleLabel(editing.id) }) : t('roleEditor.createTitle')"
      :subtitle="t('roleEditor.formHint')"
      width="lg"
      :busy="busy"
      @close="closeEdit"
      @submit="submitEdit"
    >
      <p v-if="banner" class="banner banner--err" role="alert">{{ banner }}</p>

      <div class="form-grid">
        <FormField
          :label="t('roleEditor.fieldName')"
          field-id="role-name"
          :error="nameIssue ? t(NAME_ISSUE_KEY[nameIssue]) : ''"
          :hint="editing && isPresetRole(editing) ? t('roleEditor.nameHintPreset') : t('roleEditor.nameHint', { n: MAX_ROLE_NAME_LEN })"
          required
        >
          <template #default="{ fieldId, ariaDescribedby }">
            <input
              :id="fieldId"
              v-model="form.name"
              class="ui-input"
              type="text"
              autocomplete="off"
              :aria-describedby="ariaDescribedby"
              :disabled="busy"
            />
          </template>
        </FormField>

        <FormField :label="t('roleEditor.fieldDesc')" field-id="role-desc">
          <template #default="{ fieldId }">
            <input
              :id="fieldId"
              v-model="form.description"
              class="ui-input"
              type="text"
              autocomplete="off"
              :disabled="busy"
            />
          </template>
        </FormField>

        <FormField
          v-if="!editing"
          class="field--wide"
          :label="t('roleEditor.fieldTemplate')"
          field-id="role-template"
          :hint="t('roleEditor.templateHint')"
        >
          <template #default="{ fieldId, ariaDescribedby }">
            <select
              :id="fieldId"
              class="ui-input"
              :value="form.baseRole"
              :aria-describedby="ariaDescribedby"
              :disabled="busy"
              @change="applyTemplate(($event.target as HTMLSelectElement).value)"
            >
              <option value="">{{ t('roleEditor.templateNone') }}</option>
              <!-- 模板 = 整份服务端名单(内置 admin + 库里的角色)。名字可能被人改过,
                   按 id 取值、按 roleLabel 显示,才不会「改了名就找不到这个模板」。 -->
              <option v-for="r in roles" :key="r.id" :value="r.id">{{ roleLabel(r.id) }}</option>
            </select>
          </template>
        </FormField>
      </div>

      <!-- 点集:按资源类别分组勾选,组头能整组选/清,settings.access 永远灰着 -->
      <div class="points">
        <div class="points-bar">
          <h3 class="points-title">
            {{ t('roleEditor.fieldPerms') }}
            <span class="cell-dim">
              {{ t('roleEditor.permTotal', { n: selectedCount, total: assignableCount }) }}
            </span>
          </h3>
          <div class="points-bar-actions">
            <AppButton size="sm" :disabled="busy" @click="toggleAll(true)">
              {{ t('roleEditor.selectAll') }}
            </AppButton>
            <AppButton size="sm" :disabled="busy" @click="toggleAll(false)">
              {{ t('roleEditor.clearAll') }}
            </AppButton>
          </div>
        </div>
        <p class="points-hint">{{ t('roleEditor.permsHint') }}</p>

        <section v-for="g in groups" :key="g.kind" class="point-group">
          <header class="point-group-head">
            <div>
              <span class="point-group-title">{{ g.labelKey ? t(g.labelKey) : g.kind }}</span>
              <span class="cell-dim">{{ inGroupCount(g.points) }} / {{ g.points.length }}</span>
              <p v-if="g.hintKey" class="point-group-hint">{{ t(g.hintKey) }}</p>
            </div>
            <div class="point-group-actions">
              <AppButton
                size="sm"
                :disabled="busy"
                @click="toggleGroup(g.points, true)"
              >
                {{ t('roleEditor.selectAll') }}
              </AppButton>
              <AppButton size="sm" :disabled="busy" @click="toggleGroup(g.points, false)">
                {{ t('roleEditor.clearAll') }}
              </AppButton>
            </div>
          </header>
          <label
            v-for="p in g.points"
            :key="p.id"
            class="point"
            :class="{ 'point--locked': p.builtinOnly }"
          >
            <input
              type="checkbox"
              :checked="form.perms.includes(p.id)"
              :disabled="busy || p.builtinOnly"
              @change="
                togglePoint(p.id, ($event.target as HTMLInputElement).checked)
              "
            />
            <span class="point-label">
              {{ t(permLabelKey(p.id)) }}
              <span v-if="p.builtinOnly" class="tag tag--locked">
                {{ t('roleEditor.settingsOnly') }}
              </span>
            </span>
          </label>
        </section>

        <p class="effect-hint">{{ t('roleEditor.effectHint') }}</p>
      </div>

      <template #actions>
        <AppButton :disabled="busy" @click="closeEdit">{{ t('roleEditor.cancel') }}</AppButton>
        <AppButton variant="primary" type="submit" :loading="busy" :disabled="!form.name.trim()">
          {{ t('roleEditor.save') }}
        </AppButton>
      </template>
    </AppModal>

    <!-- ─── 复制成自定义角色 ─── -->
    <AppModal
      v-if="copyOpen"
      :title="t('roleEditor.copyTitle', { name: copySource ? roleLabel(copySource.id) : '' })"
      :subtitle="t('roleEditor.copyHint')"
      width="sm"
      :busy="copyBusy"
      @close="closeCopy"
      @submit="submitCopy"
    >
      <p v-if="copyBanner" class="banner banner--err" role="alert">{{ copyBanner }}</p>
      <FormField
        :label="t('roleEditor.fieldName')"
        field-id="role-copy-name"
        :error="copyIssue ? t(NAME_ISSUE_KEY[copyIssue]) : ''"
        :hint="t('roleEditor.nameHint', { n: MAX_ROLE_NAME_LEN })"
        required
      >
        <template #default="{ fieldId, ariaDescribedby }">
          <input
            :id="fieldId"
            v-model="copyName"
            class="ui-input"
            type="text"
            autocomplete="off"
            :aria-describedby="ariaDescribedby"
            :disabled="copyBusy"
          />
        </template>
      </FormField>

      <template #actions>
        <AppButton :disabled="copyBusy" @click="closeCopy">{{ t('roleEditor.cancel') }}</AppButton>
        <AppButton variant="primary" type="submit" :loading="copyBusy" :disabled="!copyName.trim()">
          {{ t('roleEditor.copy') }}
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
.table-wrap {
  /* 窄屏下整表横向滚,而不是把五列压成竖排字:功能点芯片一行排不下时最难读。 */
  overflow-x: auto;
}
.grid {
  width: 100%;
  min-width: 860px;
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
  width: 200px;
}
/* 几列给固定宽度,剩余都留给功能点:窄屏下不先挤掉名字与账号数,免得「管理员」竖着排。 */
.th-role {
  width: 170px;
}
.th-template {
  width: 110px;
}
.th-users {
  width: 100px;
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
.cell-nowrap {
  white-space: nowrap;
}
/* 挂在内层 div 而不是 td:td 一旦 display:flex 就不再是 table-cell,这格的行底边线会短到按钮下面,列线看着就歪了。 */
.cell-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  white-space: nowrap;
}
.name-row {
  display: flex;
  align-items: center;
  gap: 6px;
}
.chips {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  margin-top: 6px;
}
.chip {
  display: inline-block;
  padding: 1px 8px;
  border-radius: 999px;
  background: rgba(120, 120, 120, 0.13);
  color: var(--color-dim);
  font-size: var(--text-small, 0.8em);
}
/* 「还有 N 个」是个芯片形状的按钮:与只读芯片同一形状,但看得出来点得动。 */
.chip--more {
  border: 1px dashed var(--color-border);
  background: transparent;
  color: var(--color-text);
  font: inherit;
  font-size: var(--text-small, 0.8em);
  cursor: pointer;
}
.chip--more:hover {
  border-style: solid;
  background: rgba(120, 120, 120, 0.13);
}
.tag {
  display: inline-block;
  padding: 1px 8px;
  border-radius: 999px;
  font-size: var(--text-small, 0.8em);
  font-weight: 600;
}
.tag--builtin {
  background: rgba(59, 130, 246, 0.15);
  color: #2563eb;
}
.tag--locked {
  background: rgba(100, 116, 139, 0.16);
  color: #475569;
}
.banner {
  padding: 8px 12px;
  border-radius: var(--radius-sm, 6px);
  font-size: var(--text-label);
  margin-bottom: 12px;
}
.banner--err {
  border: 1px solid var(--color-red-line);
  background: var(--color-red-soft);
  color: var(--color-red);
}
/* 点集编辑器:分组小标题 + 一行一个勾选项(名称点的是入口,不是权限术语)。 */
.points {
  padding-top: var(--space-3);
  border-top: 1px solid var(--color-border);
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.points-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.points-title {
  font-size: var(--text-label);
  font-weight: 600;
  color: var(--color-dim);
}
.points-bar-actions,
.point-group-actions {
  display: flex;
  gap: 6px;
  flex-shrink: 0;
}
.points-hint {
  font-size: var(--text-small, 0.85em);
  color: var(--color-faint);
  max-width: 88ch;
}
.point-group {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm, 6px);
  padding: 10px 12px;
}
.point-group-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 6px;
}
.point-group-title {
  font-size: var(--text-label);
  font-weight: 600;
  color: var(--color-text);
  margin-right: 6px;
}
.point-group-hint {
  font-size: var(--text-small, 0.85em);
  color: var(--color-faint);
  margin-top: 2px;
  max-width: 70ch;
}
.point {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 3px 0;
  font-size: var(--text-label);
  color: var(--color-text);
  cursor: pointer;
}
.point--locked {
  cursor: not-allowed;
  opacity: 0.6;
}
.point-label {
  display: flex;
  align-items: center;
  gap: 6px;
}
.effect-hint {
  font-size: var(--text-small, 0.85em);
  color: var(--color-faint);
  border-left: 2px solid var(--color-border);
  padding-left: 10px;
}
</style>
