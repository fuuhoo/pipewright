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
 * 这一页负责**把人挂到规则上**,不定义规则本身:角色名单来自 /api/admin/roles(内置管理员 +
 * 库里的角色,含四档预置;点集在「角色」tab 里维护),分组归属直接读写 /api/groups 的名册。
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
  deleteUser,
  BOOTSTRAP_ADMIN_ID,
  MIN_PASSWORD_LEN,
} from '../../api/users'
import type { User } from '../../api/users'
import { listRoles, listPermPoints } from '../../api/roles'
import type { PermPoint, Role, RoleId } from '../../api/roles'
import { listGroups, addGroupMember, removeGroupMember } from '../../api/groups'
import type { Group } from '../../api/groups'
import { HttpError } from '../../api/http'
import { ROLE_TAG_CLASS } from '../../lib/roles'
import { inheritedPerms, labelForRoleId } from '../../lib/roleEditor'
import {
  PROFILE_EMAIL_MAX,
  PROFILE_NAME_MAX,
  emailProblem,
  handleOf,
  nicknameOf,
  primaryNameOf,
  type EmailProblem,
} from '../../lib/userDisplay'
import AppModal from '../../components/ui/AppModal.vue'
import AppButton from '../../components/ui/AppButton.vue'
import FormField from '../../components/ui/FormField.vue'
import TransferList from '../../components/ui/TransferList.vue'
import PermissionTree from '../../components/permissions/PermissionTree.vue'
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

// 角色名单、功能点字典与分组名册都是这一页的输入:角色勾选与继承权限树要角色和点,
// 「关联分组」弹窗要每个组的当前成员。它们各自失败不该拖垮账号列表,所以失败只记在
// 整页 loadError 上,列表照常用(这三份都从同一批 admin 接口来,缺一份页面就用不了)。
const roles = ref<Role[]>([])
const points = ref<PermPoint[]>([])
const groups = ref<Group[]>([])

async function load(): Promise<void> {
  loadState.value = 'loading'
  loadError.value = ''
  try {
    const [list, roleList, pointList, groupList] = await Promise.all([
      listUsers({ includeDisabled: includeDisabled.value }),
      listRoles(),
      listPermPoints(),
      listGroups(),
    ])
    users.value = list
    roles.value = roleList
    points.value = pointList
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

/** 角色展示名:走 labelForRoleId(没改过名的预置档回 i18n 标签,其余用它的名字)。 */
function roleLabel(role: RoleId): string {
  return labelForRoleId(role, rolesById.value, t)
}

/** 穿梭框的候选项;角色不多,但名字可能被改过,统一按 id 取值、按展示名显示。 */
const roleOptions = computed(() =>
  roles.value.map((r) => ({ id: r.id, label: roleLabel(r.id), createdAt: r.createdAt })),
)

/**
 * 账号的角色集合。老后端/老数据只有 `role` 一列,这里退回单角色,
 * 免得升级后列表里的角色列忽然空掉。
 */
function userRoles(u: User): RoleId[] {
  return u.roles && u.roles.length ? u.roles : [u.role]
}

/** 集合比较按顺序:第一个角色是主角色,换顺序是有语义的改动。 */
function sameRoles(a: RoleId[], b: RoleId[]): boolean {
  return a.length === b.length && a.every((id, i) => id === b[i])
}

/** 标签配色按 id 走(五个预置 id 各一色),不看 builtin:预置档自 0063 起也是库里的普通行,
 *  但 id 稳定,管理员改了名字也不该让标签换色;自建角色一律用那条通用灰绿。 */
function roleTagClass(role: RoleId): string {
  return (ROLE_TAG_CLASS as Record<string, string>)[role] ?? 'tag--custom'
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
// 一个账号可以挂多个角色,能力取并集;穿梭框按「加入先后」保留顺序,首个即主角色。
// 显示名/邮箱可留空:留空就是「让本人自己在账户设置里填」,昵称缺失时页面回落到登录名。
const createForm = ref({ username: '', password: '', roles: ['user'] as RoleId[], description: '', displayName: '', email: '' })
const createErrors = ref({ username: '', password: '', roles: '', email: '' })

function openCreate(): void {
  createForm.value = { username: '', password: '', roles: ['user'], description: '', displayName: '', email: '' }
  createErrors.value = { username: '', password: '', roles: '', email: '' }
  createBanner.value = ''
  createOpen.value = true
}

function closeCreate(): void {
  if (createBusy.value) return
  createOpen.value = false
}

/** 邮箱的三条判据(与后端 normalizeEmail 同集合),'' 表示这条没问题。 */
function emailErrorKey(problem: EmailProblem): string {
  if (problem === 'space') return 'adminUsers.errEmailSpace'
  if (problem === 'format') return 'adminUsers.errEmailFormat'
  if (problem === 'tooLong') return 'adminUsers.errEmailTooLong'
  return ''
}

function validateCreate(): boolean {
  createErrors.value = { username: '', password: '', roles: '', email: '' }
  let ok = true
  if (!createForm.value.username.trim()) {
    createErrors.value.username = t('adminUsers.errUsernameRequired')
    ok = false
  }
  if (createForm.value.password.length < MIN_PASSWORD_LEN) {
    createErrors.value.password = t('adminUsers.errPasswordShort', { n: MIN_PASSWORD_LEN })
    ok = false
  }
  if (createForm.value.roles.length === 0) {
    createErrors.value.roles = t('adminUsers.rolesRequired')
    ok = false
  }
  const emailErr = emailErrorKey(emailProblem(createForm.value.email))
  if (emailErr) {
    createErrors.value.email = t(emailErr)
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
      roles: createForm.value.roles,
      description: createForm.value.description.trim(),
      displayName: createForm.value.displayName,
      email: createForm.value.email.trim(),
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

// ─── 删除账号 ─────────────────────────────────────────────────────────────────

/**
 * 这一页唯一不可逆的操作,所以确认框要把「和禁用的区别」写在脸上:会话、外部身份绑定、
 * 分组名册一起清掉,用户名随后释放(同名可重建)。
 * 后端还有两类 409 —— 该账号仍是 N 个分组的组长、或名下仍有 N 条个人凭据。这两档不在这里
 * 预先判断(要另拉两份数据才数得准),直接回显服务端带计数的原因,让管理员照着去移交/清理。
 */
async function askDelete(u: User): Promise<void> {
  const ok = await confirm.open({
    title: t('adminUsers.deleteTitle'),
    body: `${t('adminUsers.deleteBody', { name: primaryNameOf(u) })} ${t('adminUsers.deleteEffect')}`,
    confirmLabel: t('adminUsers.delete'),
    variant: 'danger',
  })
  if (!ok) return
  try {
    await deleteUser(u.id)
    // 整页重取而不是本地过滤:分组名册里也有这个人,只删列表那一行会让「关联分组」弹窗
    // 继续显示一个已经不存在于 users 的成员。
    await load()
    toast.success(t('adminUsers.deletedToast', { name: u.username }))
  } catch (err) {
    toast.error(errText(err, 'adminUsers.errDelete'))
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

// ─── 角色与继承权限(功能轴)────────────────────────────────────────────────────

/**
 * 一个账号可以挂多个角色,能力取并集(后端 access.HasPerm / Ceiling 都是并集语义)。
 * 权限设置只有一个入口:左边选角色、右边实时看这组角色并起来能看到的入口树,下方改分组归属。
 * 勾掉一个角色,右侧那棵树立刻少掉它独有的点 —— 比只看一张角色名单更容易理解多角色意味着什么。
 *
 * 树里的每个点都标出来源角色;顺序按 roles 列表(第一个是主角色,列表与旧数据沿用它)。
 * 内置管理员那一行由 admin_user 同步,后端对它的写端点一律 409,所以角色区只读、分组仍可调。
 * 角色是登录快照,下次登录才生效;分组归属每请求重判,刷新页面就生效。
 */
const permsOpen = ref(false)
const permsTarget = ref<User | null>(null)
const roleDraft = ref<RoleId[]>([])
const permsBusy = ref(false)
const permsBanner = ref('')
const permsIssue = ref('')
// 一页塞「角色 + 权限树 + 分组」太长,拆成两档;保存仍一次性提交两边。
const permsTab = ref<'roles' | 'groups'>('roles')

const permsLocked = computed(() => (permsTarget.value ? isBootstrapAdmin(permsTarget.value) : false))
const draftTree = computed(() =>
  inheritedPerms(roleDraft.value, rolesById.value, points.value),
)

/** 穿梭框直接改草稿;顺序即 roles 列表顺序(先选的在前),与后端「首角色为主」一致。 */
const roleDraftModel = computed<RoleId[]>({
  get: () => roleDraft.value,
  set: (next) => {
    roleDraft.value = next
    permsIssue.value = ''
  },
})

function isMember(g: Group, userId: string): boolean {
  return g.members.some((m) => m.id === userId)
}

/** 列表「分组」列:他当前挂的组名,按 groups 的顺序。 */
function groupNames(u: User): string[] {
  return groups.value.filter((g) => isMember(g, u.id)).map((g) => g.name)
}

// 只装「可移入移出」的组 id;不归当前账号管的成员组由 lockedIds 顶在右栏。
const groupDraft = ref<string[]>([])

const groupLockedIds = computed(() => {
  const target = permsTarget.value
  if (!target) return []
  return groups.value.filter((g) => !g.canManage && isMember(g, target.id)).map((g) => g.id)
})
const groupUnmanageableIds = computed(() => groups.value.filter((g) => !g.canManage).map((g) => g.id))
const groupOptions = computed(() =>
  groups.value.map((g) => ({
    id: g.id,
    label: g.name,
    meta: t('adminUsers.groupsMembers', { n: g.members.length }),
    createdAt: g.createdAt,
  })),
)

function openPermissions(u: User): void {
  permsTarget.value = u
  roleDraft.value = [...userRoles(u)]
  groupDraft.value = groups.value.filter((g) => g.canManage && isMember(g, u.id)).map((g) => g.id)
  permsBanner.value = ''
  permsIssue.value = ''
  permsTab.value = 'roles'
  permsOpen.value = true
}

function closePermissions(): void {
  if (permsBusy.value) return
  permsOpen.value = false
  permsTarget.value = null
}

async function submitPermissions(): Promise<void> {
  const target = permsTarget.value
  if (!target) return
  if (!permsLocked.value && roleDraft.value.length === 0) {
    // 校验失败时把用户带回角色档,否则错误提示藏在另一个 tab 里看不见。
    permsTab.value = 'roles'
    permsIssue.value = t('adminUsers.rolesRequired')
    return
  }
  const rolesChanged = !permsLocked.value && !sameRoles(roleDraft.value, userRoles(target))
  const groupOps: { g: Group; want: boolean }[] = []
  for (const g of groups.value) {
    if (!g.canManage) continue
    const want = groupDraft.value.includes(g.id)
    if (want !== isMember(g, target.id)) groupOps.push({ g, want })
  }
  if (!rolesChanged && groupOps.length === 0) {
    closePermissions()
    return
  }

  permsBusy.value = true
  permsBanner.value = ''
  let stage: 'roles' | 'groups' = 'roles'
  try {
    if (rolesChanged) {
      const fresh = await updateUser(target.id, { roles: roleDraft.value })
      users.value = users.value.map((x) => (x.id === fresh.id ? fresh : x))
      // 分组中途失败时弹窗不关,下一次保存不该把已经写成功的角色再写一遍。
      permsTarget.value = fresh
    }
    stage = 'groups'
    // 差集逐条写:每写成一个就把返回的名册换回本地,中途失败时显示的是「真的改成了什么」。
    for (const { g, want } of groupOps) {
      const fresh = want
        ? await addGroupMember(g.id, target.id)
        : await removeGroupMember(g.id, target.id)
      groups.value = groups.value.map((x) => (x.id === fresh.id ? fresh : x))
    }
    permsOpen.value = false
    permsTarget.value = null
    toast.success(t('adminUsers.permsSaved', { name: target.username }))
  } catch (err) {
    permsBanner.value = errText(
      err,
      stage === 'roles' ? 'adminUsers.errRoleUpdate' : 'adminUsers.errGroupsUpdate',
    )
  } finally {
    permsBusy.value = false
  }
}

// ─── 本地档案(昵称 + 邮箱)───────────────────────────────────────────────────

/**
 * 管理员也能改别人的昵称/邮箱 —— 但这是**本地档案**,不是平台那边的人:平台侧改名不会同步进来,
 * 首次建号时抄进来的那份从此归这里管。所以弹窗里把这话写明,免得有人以为改的是「平台名字」。
 * 后端一行 UPDATE 同时写两列,所以两个字段一起提交;当场生效(每次请求重读 users 行),
 * 不用等对方重新登录 —— 与改角色那条快照轴不同。
 */
const profileOpen = ref(false)
const profileTarget = ref<User | null>(null)
const profileBusy = ref(false)
const profileBanner = ref('')
const profileDraft = ref({ displayName: '', email: '' })
const profileEmailError = ref('')

function openProfile(u: User): void {
  profileTarget.value = u
  profileDraft.value = { displayName: nicknameOf(u), email: u.email }
  profileEmailError.value = ''
  profileBanner.value = ''
  profileOpen.value = true
}

function closeProfile(): void {
  if (profileBusy.value) return
  profileOpen.value = false
  profileTarget.value = null
}

async function submitProfile(): Promise<void> {
  const target = profileTarget.value
  if (!target) return
  const problem = emailErrorKey(emailProblem(profileDraft.value.email))
  profileEmailError.value = problem ? t(problem) : ''
  if (problem) return
  profileBusy.value = true
  profileBanner.value = ''
  try {
    const fresh = await updateUser(target.id, {
      displayName: profileDraft.value.displayName,
      email: profileDraft.value.email.trim(),
    })
    users.value = users.value.map((x) => (x.id === fresh.id ? fresh : x))
    profileOpen.value = false
    profileTarget.value = null
  } catch (err) {
    profileBanner.value = errText(err, 'adminUsers.errProfileUpdate')
  } finally {
    profileBusy.value = false
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
            <th>{{ t('adminUsers.colAccount') }}</th>
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
              <!-- 三行同属「这是谁」:昵称(没有则回登录名)、登录名、邮箱。
                   后两行只在有内容时出现,免得昵称就是登录名时重复一版。 -->
              <div class="cell-strong">{{ primaryNameOf(u) }}</div>
              <div v-if="nicknameOf(u)" class="cell-dim mono">{{ handleOf(u) }}</div>
              <div v-if="u.email" class="cell-dim mono">{{ u.email }}</div>
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
              <div class="role-tags">
                <span v-for="rid in userRoles(u)" :key="rid" class="tag" :class="roleTagClass(rid)">
                  {{ roleLabel(rid) }}
                </span>
              </div>
            </td>
            <td>
              <div class="cell-groups">
                <span v-for="name in groupNames(u)" :key="name" class="tag tag--group">{{ name }}</span>
                <span v-if="groupNames(u).length === 0" class="cell-dim cell-dim--inline">
                  {{ t('adminUsers.groupsNone') }}
                </span>
              </div>
            </td>
            <td class="cell-nowrap">
              <span :class="u.enabled ? 'ok' : 'off'">
                {{ u.enabled ? t('adminUsers.enabled') : t('adminUsers.disabled') }}
              </span>
            </td>
            <td class="cell-nowrap">{{ fmtTime(u.lastLoginAt) }}</td>
            <td class="cell-nowrap">{{ fmtTime(u.createdAt) }}</td>
            <td>
              <div class="cell-actions">
                <AppButton variant="ghost" size="sm" @click="openPermissions(u)">
                  {{ t('adminUsers.setPerms') }}
                </AppButton>
                <AppButton
                  variant="ghost"
                  size="sm"
                  :disabled="isBootstrapAdmin(u)"
                  :title="isBootstrapAdmin(u) ? t('adminUsers.bootstrapAdminHint') : ''"
                  @click="openProfile(u)"
                >
                  {{ t('adminUsers.editProfile') }}
                </AppButton>
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
                <AppButton
                  variant="danger"
                  size="sm"
                  :disabled="isBootstrapAdmin(u)"
                  :title="isBootstrapAdmin(u) ? t('adminUsers.bootstrapAdminHint') : t('adminUsers.deleteHint')"
                  @click="askDelete(u)"
                >
                  {{ t('adminUsers.delete') }}
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
          :label="t('adminUsers.fieldDisplayName')"
          field-id="user-create-nick"
          :hint="t('adminUsers.displayNameHint', { n: PROFILE_NAME_MAX })"
        >
          <template #default="{ fieldId, ariaDescribedby }">
            <input
              :id="fieldId"
              v-model="createForm.displayName"
              class="ui-input"
              type="text"
              :maxlength="PROFILE_NAME_MAX"
              autocomplete="off"
              :aria-describedby="ariaDescribedby"
              :disabled="createBusy"
            />
          </template>
        </FormField>
        <FormField
          :label="t('adminUsers.fieldEmail')"
          field-id="user-create-email"
          :error="createErrors.email"
          :hint="t('adminUsers.emailHint')"
        >
          <template #default="{ fieldId, ariaDescribedby }">
            <input
              :id="fieldId"
              v-model="createForm.email"
              class="ui-input"
              type="text"
              inputmode="email"
              :maxlength="PROFILE_EMAIL_MAX"
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
        <div class="field--wide">
          <p class="field-title">
            {{ t('adminUsers.fieldRoles') }}
            <span class="field-required" aria-hidden="true">*</span>
          </p>
          <TransferList
            v-model="createForm.roles"
            :options="roleOptions"
            preserve-order
            :disabled="createBusy"
          />
          <p v-if="createErrors.roles" class="field-error" role="alert">{{ createErrors.roles }}</p>
          <p v-else class="field-hint">{{ t('adminUsers.rolesHint') }}</p>
        </div>
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

    <!-- ─── 设置权限:角色(功能轴)+ 分组(数据轴)─── -->
    <AppModal
      v-if="permsOpen"
      :title="t('adminUsers.permsTitle', { name: permsTarget?.username })"
      width="xl"
      :busy="permsBusy"
      @close="closePermissions"
      @submit="submitPermissions"
    >
      <p v-if="permsBanner" class="banner banner--err" role="alert">{{ permsBanner }}</p>

      <!-- 两条轴分两档:同屏堆叠太长,保存仍一次提交两边。 -->
      <div class="perms-tabs" role="tablist">
        <button
          type="button"
          class="perms-tab"
          :class="{ 'perms-tab--active': permsTab === 'roles' }"
          role="tab"
          :aria-selected="permsTab === 'roles'"
          @click="permsTab = 'roles'"
        >
          {{ t('adminUsers.fieldRoles') }}
          <span class="perms-tab__count">{{ roleDraft.length }}</span>
        </button>
        <button
          type="button"
          class="perms-tab"
          :class="{ 'perms-tab--active': permsTab === 'groups' }"
          role="tab"
          :aria-selected="permsTab === 'groups'"
          @click="permsTab = 'groups'"
        >
          {{ t('adminUsers.colGroups') }}
          <span class="perms-tab__count">{{ groupDraft.length + groupLockedIds.length }}</span>
        </button>
      </div>

      <div v-if="permsTab === 'roles'" class="perms-panel" role="tabpanel">
        <p class="modal-desc">{{ t('adminUsers.accessDesc') }}</p>

        <!-- 左选角色、右看结果:勾掉一个角色,右边那棵树当场少掉它独有的点。 -->
        <div class="access-grid">
          <section class="access-roles">
            <header class="inherited-head">
              <h3 class="inherited-title">{{ t('adminUsers.fieldRoles') }}</h3>
              <span class="cell-dim">{{ roleDraft.length }} / {{ roles.length }}</span>
            </header>
            <TransferList
              v-model="roleDraftModel"
              :options="roleOptions"
              preserve-order
              :disabled="permsLocked || permsBusy"
            />
            <p v-if="permsLocked" class="field-hint">{{ t('adminUsers.rolesLocked') }}</p>
            <p v-else-if="permsIssue" class="field-error" role="alert">{{ permsIssue }}</p>
            <p v-else class="field-hint">{{ t('adminUsers.rolesHint') }}</p>
          </section>

          <section class="inherited">
            <header class="inherited-head">
              <h3 class="inherited-title">{{ t('adminUsers.inheritedTitle') }}</h3>
              <span class="cell-dim">
                {{ t('adminUsers.permsTotal', { n: draftTree.perms.length, total: points.length }) }}
              </span>
            </header>
            <p v-if="draftTree.perms.length === 0" class="cell-dim">{{ t('adminUsers.permsEmpty') }}</p>
            <PermissionTree
              v-else
              :points="points"
              :selected="draftTree.perms"
              readonly
              :sources="draftTree.sources"
              :role-label="roleLabel"
            />
          </section>
        </div>
      </div>

      <div v-else class="perms-panel" role="tabpanel">
        <p class="modal-desc">{{ t('adminUsers.groupsDesc') }}</p>
        <p v-if="groups.length === 0" class="state">{{ t('adminUsers.groupsEmpty') }}</p>
        <template v-else>
          <!-- 已在他名下的组预置在右栏;不归你管的组在左栏置灰、在右栏锁定。 -->
          <TransferList
            v-model="groupDraft"
            :options="groupOptions"
            :locked-ids="groupLockedIds"
            :disabled-ids="groupUnmanageableIds"
            :locked-label="t('adminUsers.groupsLockedTag')"
            :disabled="permsBusy"
          />
          <p v-if="groupUnmanageableIds.length" class="field-hint">
            {{ t('adminUsers.groupsLockedHint') }}
          </p>
        </template>
      </div>

      <template #actions>
        <AppButton :disabled="permsBusy" @click="closePermissions">{{ t('adminUsers.cancel') }}</AppButton>
        <AppButton variant="primary" type="submit" :loading="permsBusy">
          {{ t('adminUsers.save') }}
        </AppButton>
      </template>
    </AppModal>

    <!-- ─── 本地档案(昵称 + 邮箱)─── -->
    <AppModal
      v-if="profileOpen"
      :title="t('adminUsers.profileTitle', { name: profileTarget?.username })"
      width="sm"
      :busy="profileBusy"
      @close="closeProfile"
      @submit="submitProfile"
    >
      <p v-if="profileBanner" class="banner banner--err" role="alert">{{ profileBanner }}</p>
      <p class="modal-desc">{{ t('adminUsers.profileDesc') }}</p>
      <FormField
        :label="t('adminUsers.fieldDisplayName')"
        field-id="user-profile-nick"
        :hint="t('adminUsers.displayNameHint', { n: PROFILE_NAME_MAX })"
      >
        <template #default="{ fieldId, ariaDescribedby }">
          <input
            :id="fieldId"
            v-model="profileDraft.displayName"
            class="ui-input"
            type="text"
            :maxlength="PROFILE_NAME_MAX"
            autocomplete="off"
            :aria-describedby="ariaDescribedby"
            :disabled="profileBusy"
          />
        </template>
      </FormField>
      <FormField
        :label="t('adminUsers.fieldEmail')"
        field-id="user-profile-email"
        :error="profileEmailError"
        :hint="t('adminUsers.emailHint')"
      >
        <template #default="{ fieldId, ariaDescribedby }">
          <input
            :id="fieldId"
            v-model="profileDraft.email"
            class="ui-input"
            type="text"
            inputmode="email"
            :maxlength="PROFILE_EMAIL_MAX"
            autocomplete="off"
            :aria-describedby="ariaDescribedby"
            :disabled="profileBusy"
          />
        </template>
      </FormField>

      <template #actions>
        <AppButton :disabled="profileBusy" @click="closeProfile">{{ t('adminUsers.cancel') }}</AppButton>
        <AppButton variant="primary" type="submit" :loading="profileBusy">
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
/* 角色/分组列给足宽度,标签按两三枚一行铺开,不再被挤成竖条。 */
.grid th:nth-child(2),
.grid td:nth-child(2),
.grid th:nth-child(3),
.grid td:nth-child(3) {
  min-width: 180px;
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
  padding: 2px 8px;
  border-radius: 999px;
  font-size: var(--text-small, 0.8em);
  font-weight: 600;
  white-space: nowrap;
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
/* 角色 / 分组列只陈列标签,操作统一收到一行的「设置权限」里。 */
.role-tags,
.cell-groups {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px;
  min-width: 0;
}
.cell-dim--inline {
  margin-top: 0;
}
.cell-nowrap {
  white-space: nowrap;
}
.field-title {
  font-size: var(--text-label);
  font-weight: 500;
  color: var(--color-dim);
  margin-bottom: 6px;
}
.field-required {
  color: var(--color-red);
  margin-left: 2px;
}
.field-hint {
  margin-top: 6px;
  font-size: var(--text-small, 0.8em);
  color: var(--color-faint);
}
.field-error {
  margin-top: 6px;
  font-size: var(--text-small, 0.8em);
  color: var(--color-red);
}
/* 角色 × 继承权限:左选右看。xl 弹窗下并排,窄屏再退回上下。 */
.access-grid {
  display: grid;
  grid-template-columns: minmax(0, 1.1fr) minmax(0, 0.9fr);
  gap: var(--space-4);
  align-items: start;
}
.access-roles {
  min-width: 0;
}
.inherited {
  min-width: 0;
}
/* 角色 / 分组两档:切换不改草稿,保存时两边一起提交。 */
.perms-tabs {
  align-self: flex-start;
  display: inline-flex;
  gap: 4px;
  padding: 4px;
  background: var(--color-inset);
  border: 1px solid var(--color-border);
  border-radius: var(--rounded);
}
.perms-tab {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 6px 14px;
  border: none;
  border-radius: var(--rounded-sm, 4px);
  background: transparent;
  color: var(--color-dim);
  font-family: inherit;
  font-size: var(--text-label);
  font-weight: 600;
  cursor: pointer;
}
.perms-tab:hover {
  color: var(--color-text);
}
.perms-tab--active {
  background: var(--color-surface);
  color: var(--color-text);
  box-shadow: var(--shadow-sm);
}
.perms-tab__count {
  min-width: 1.5em;
  padding: 0 5px;
  border-radius: 999px;
  font-size: 0.75em;
  font-weight: 600;
  color: var(--color-faint);
  background: var(--color-border);
  text-align: center;
}
.perms-tab--active .perms-tab__count {
  color: var(--color-primary);
  background: var(--color-primary-soft);
}
.perms-panel {
  min-width: 0;
}
@media (max-width: 720px) {
  .access-grid {
    grid-template-columns: 1fr;
  }
}
.inherited-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 8px;
}
.inherited-title {
  font-size: var(--text-label);
  font-weight: 600;
  color: var(--color-dim);
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
