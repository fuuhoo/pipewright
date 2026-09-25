<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import {
  listProjects,
  createProject,
  updateProject,
  deleteProject,
  testClone,
  type Project,
  type RunStatus,
  type CreateProjectInput,
  type UpdateProjectInput,
} from '../api/projects'
import { listCredentials, usableCredentials, createCredential, type Credential, type CredentialType } from '../api/credentials'
import { listGroups, type Group } from '../api/groups'
import { triggerManual, type RunDetail, type TriggerManualInput } from '../api/runs'
import { listRefs, listCommits, type GitCommit } from '../api/refs'
import RunParamsEditor from '../components/RunParamsEditor.vue'
import TypedRunParams from '../components/TypedRunParams.vue'
import CredentialSelect from '../components/projects/CredentialSelect.vue'
import ProjectCard from '../components/projects/ProjectCard.vue'
import RefPicker from '../components/projects/RefPicker.vue'
import { getParameters, validateParamValues, type ParamDef } from '../api/parameters'
import { HttpError } from '../api/http'
import { useSessionStore } from '../stores/session'
import { isSupportedRepoUrl } from '../lib/gitUrl'
import { runStatusLabel } from '../lib/runStatus'
import {
  GROUP_ALL,
  GROUP_NONE,
  groupProjects,
  type ProjectGroupSection,
} from '../lib/projectGroups'

// ─── i18n ─────────────────────────────────────────────────────────────────────

const { t } = useI18n()
const sessionStore = useSessionStore()

// ─── router ───────────────────────────────────────────────────────────────────

const router = useRouter()

function goToPipeline(projectId: string): void {
  void router.push({ name: 'project-pipeline', params: { id: projectId } })
}

// Story 7-4: read-only code browsing (FR-4)
function goToCode(projectId: string): void {
  void router.push({ name: 'project-code', params: { id: projectId } })
}

// ─── load state ──────────────────────────────────────────────────────────────

type LoadState = 'idle' | 'loading' | 'error'

const loadState = ref<LoadState>('idle')
const loadError = ref('')
const projects = ref<Project[]>([])

// ─── search + filter ─────────────────────────────────────────────────────────

const searchQuery = ref('')
const statusFilter = ref<RunStatus | 'all'>('all')

// 分组筛选:候选是「可见分组」(后端已按名册收敛),未归组单列一档。
// 用哨兵而不是空串:空串在这里是合法值(它就是「未归组」那一档的 groupId)。
const groupFilter = ref<string>(GROUP_ALL)

const STATUS_OPTIONS = computed<Array<{ value: RunStatus | 'all'; label: string }>>(() => [
  { value: 'all',     label: t('projects.statusAll') },
  { value: '成功',     label: runStatusLabel('成功') },
  { value: '失败',     label: runStatusLabel('失败') },
  { value: '进行中',   label: runStatusLabel('进行中') },
  { value: '部分失败', label: runStatusLabel('部分失败') },
  { value: '已回滚',   label: runStatusLabel('已回滚') },
  { value: '排队中',   label: runStatusLabel('排队中') },
])

const filteredProjects = computed(() => {
  let list = projects.value
  const q = searchQuery.value.trim().toLowerCase()
  if (q) {
    list = list.filter(
      (p) =>
        p.name.toLowerCase().includes(q) ||
        p.repoUrl.toLowerCase().includes(q) ||
        p.defaultBranch.toLowerCase().includes(q),
    )
  }
  if (statusFilter.value !== 'all') {
    list = list.filter((p) => p.lastRunStatus === statusFilter.value)
  }
  if (groupFilter.value !== GROUP_ALL) {
    const want = groupFilter.value === GROUP_NONE ? '' : groupFilter.value
    list = list.filter((p) => p.groupId === want)
  }
  return list
})

// ─── 分组(v6.2 分组权限)─────────────────────────────────────────────────────
//
// 后端已按可见范围过滤列表,所以这里只关心两件事:展示组名,以及「我能不能改它的归属」。
// 归属改动要求对**旧组**有 Manage(未归组的 Manage 只有管理员有),故 canRegroup 与
// 后端判定同构 —— 组名与 canManage 都来自 GET /api/groups,页面不重推权限规则。

const groups = ref<Group[]>([])
/** 可见分组(含 public):归组下拉的候选来自这里的 canManage 子集。 */
const groupById = computed<Record<string, Group>>(() => {
  const out: Record<string, Group> = {}
  for (const g of groups.value) out[g.id] = g
  return out
})
const manageableGroups = computed(() => groups.value.filter((g) => g.canManage))

/** 分组筛选下拉的候选:全部 / 未归组 / 每个可见分组(带可见性后缀,和归组弹窗同一说法)。 */
const GROUP_OPTIONS = computed<Array<{ value: string; label: string }>>(() => [
  { value: GROUP_ALL, label: t('projects.groupAll') },
  { value: GROUP_NONE, label: t('groups.ungrouped') },
  ...groups.value.map((g) => ({
    value: g.id,
    label: `${g.name} · ${g.visibility === 'public' ? t('groups.visibilityPublic') : t('groups.visibilityPrivate')}`,
  })),
])

function groupName(id: string): string {
  return id ? (groupById.value[id]?.name ?? t('groups.groupMissing')) : t('groups.ungrouped')
}

/** 能否改这个项目的归属:未归组只有管理员能挪,组内则看对该组的 canManage。 */
function canRegroup(p: Project): boolean {
  if (!p.groupId) return isAdminUser.value
  return groupById.value[p.groupId]?.canManage === true
}

const isAdminUser = computed(() => sessionStore.user?.role === 'admin')

async function loadGroups(): Promise<void> {
  try {
    groups.value = await listGroups()
  } catch {
    // 分组读不到不影响项目列表本身:归属列退化成「未归组」,不弹错误横幅。
    groups.value = []
  }
}

// ─── 视图切换:卡片 / 分组 ─────────────────────────────────────────────────────
//
// 两种视图读的是同一份筛选结果,只有排布不同:卡片视图一把铺开,分组视图按组折叠、
// 点开才列该组的项目。视图偏好写 localStorage(和主题、侧栏同一做法);折叠状态是
// 一次浏览内的事,不落盘 —— 刷新后回到「收起」比记住半展开的组更符合预期。

type ViewMode = 'cards' | 'groups'

const VIEW_MODE_KEY = 'pipewright.projects.viewMode'

function readViewMode(): ViewMode {
  try {
    return localStorage.getItem(VIEW_MODE_KEY) === 'groups' ? 'groups' : 'cards'
  } catch {
    // 隐私模式 / 安全策略下 localStorage 会抛:安静退回卡片视图。
    return 'cards'
  }
}

const viewMode = ref<ViewMode>(readViewMode())

function setViewMode(mode: ViewMode): void {
  viewMode.value = mode
  try {
    localStorage.setItem(VIEW_MODE_KEY, mode === 'groups' ? 'groups' : 'cards')
  } catch {
    // 存不下不影响这次切换。
  }
}

/** 展开的段(分组 ID;未归组那段是空串)。默认全收起 = 空集。 */
const expandedSections = ref<Set<string>>(new Set())

function isSectionOpen(key: string): boolean {
  return expandedSections.value.has(key)
}

function toggleSection(key: string): void {
  const next = new Set(expandedSections.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  expandedSections.value = next
}

const groupSections = computed<ProjectGroupSection[]>(() =>
  groupProjects(filteredProjects.value, groups.value, {
    groupFilter: groupFilter.value,
    // 搜索/状态条件生效时空组不占位;只看某一段时按段的真实情况摆。
    hideEmptyGroups: searchQuery.value.trim() !== '' || statusFilter.value !== 'all',
  }),
)

/** 段标题:未归组那段没有名字(它的 key 就是合法 groupId,不能用 key 当展示名)。 */
function sectionLabel(section: ProjectGroupSection): string {
  return section.name || t('groups.ungrouped')
}

/** 折叠区的 idref:指向常存的 <section>,这样收起时 aria-controls 也不会悬空。 */
function sectionGridId(key: string): string {
  return `project-group-${key || 'ungrouped'}`
}

function openCardCode(project: Project): void {
  goToCode(project.id)
}

function openCardPipeline(project: Project): void {
  goToPipeline(project.id)
}

// ─── credentials for dropdown ─────────────────────────────────────────────────
const credentials = ref<Credential[]>([])
const credentialsLoading = ref(false)

const gitCredentials = computed(() =>
  credentials.value.filter(
    (c) => c.type === 'git_token' || c.type === 'git_http' || c.type === 'git_ssh',
  ),
)

// 行内建凭据用的类型标签(与保险库一致;三种 git 类型可选)
const inlineCredTypeLabels = computed<Record<CredentialType, string>>(() => ({
  git_token: t('settingsVault.typeGitToken'),
  git_http: t('settingsVault.typeGitHttp'),
  git_ssh: t('settingsVault.typeGitSSH'),
  ssh_key: t('settingsVault.typeSshKey'),
  ssh_password: t('settingsVault.typeSshPassword'),
  registry: t('settingsVault.typeRegistry'),
  // 行内建不了集群凭据(kubeconfig 要到保险库整份粘贴),此条只为满足 Record 完整性。
  kubeconfig: t('settingsVault.typeKubeconfig'),
}))

async function loadCredentials(): Promise<void> {
  credentialsLoading.value = true
  try {
    credentials.value = usableCredentials(await listCredentials())
  } catch {
    // non-fatal; user will see empty dropdown with helper text
  } finally {
    credentialsLoading.value = false
  }
}

// ─── data loading ──────────────────────────────────────────────────────────────

async function loadProjects(): Promise<void> {
  loadState.value = 'loading'
  loadError.value = ''
  try {
    projects.value = await listProjects()
    loadState.value = 'idle'
  } catch (err) {
    if (err instanceof HttpError) {
      if (err.status === 0) {
        loadError.value = t('projects.errLoadNetwork')
      } else {
        loadError.value = err.apiError?.message ?? t('projects.errLoadStatus', { status: err.status })
      }
    } else {
      loadError.value = t('projects.errLoadRetry')
    }
    loadState.value = 'error'
  }
}

onMounted(async () => {
  await Promise.all([loadProjects(), loadCredentials(), loadGroups()])
})

// ─── new project modal ────────────────────────────────────────────────────────

const createModalOpen = ref(false)

const createForm = ref({
  name: '',
  // 不绑定仓库 = 纯发布项目:没有源码这一环,流水线从别处产出的产物/镜像开始。
  bindRepo: true,
  repoUrl: '',
  credentialId: '',
  defaultBranch: '',
  groupId: '',
})

const createErrors = ref({
  name: '',
  repoUrl: '',
  credentialId: '',
})

const inlineCredentialOpen = ref(false)
const inlineCredentialForm = ref<{ name: string; type: CredentialType; username: string; secret: string }>({ name: '', type: 'git_http', username: '', secret: '' })
const inlineCredentialErrors = ref({ name: '', username: '', secret: '' })
const inlineCredentialBanner = ref('')
const inlineCredentialSubmitting = ref(false)

const createBanner = ref('')
const createSubmitting = ref(false)

// test-clone sub-state
type TestState = 'idle' | 'testing' | 'ok' | 'error'
const testState = ref<TestState>('idle')
const testError = ref('')
const testDetectedBranch = ref('')
// 「测试连接」成功后带回的远端分支/tag,供默认分支输入框的 RefPicker 下拉候选。
const remoteBranches = ref<string[]>([])
const remoteTags = ref<string[]>([])

function openCreateModal(): void {
  createForm.value = { name: '', bindRepo: true, repoUrl: '', credentialId: '', defaultBranch: '', groupId: '' }
  clearCreateErrors()
  createBanner.value = ''
  testState.value = 'idle'
  testError.value = ''
  testDetectedBranch.value = ''
  remoteBranches.value = []
  remoteTags.value = []
  inlineCredentialOpen.value = false
  inlineCredentialForm.value = { name: '', type: 'git_http', username: '', secret: '' }
  inlineCredentialErrors.value = { name: '', username: '', secret: '' }
  inlineCredentialBanner.value = ''
  createModalOpen.value = true
}

function closeCreateModal(): void {
  if (createSubmitting.value) return
  createModalOpen.value = false
}

function clearCreateErrors(): void {
  createErrors.value = { name: '', repoUrl: '', credentialId: '' }
}

function clearInlineCredentialErrors(): void {
  inlineCredentialErrors.value = { name: '', username: '', secret: '' }
  inlineCredentialBanner.value = ''
}

async function handleInlineCredentialSubmit(): Promise<void> {
  clearInlineCredentialErrors()
  let valid = true
  if (!inlineCredentialForm.value.name.trim()) {
    inlineCredentialErrors.value.name = t('projects.inlineCredNameRequired')
    valid = false
  }
  if (inlineCredentialForm.value.type === 'git_http' && !inlineCredentialForm.value.username.trim()) {
    inlineCredentialErrors.value.username = t('projects.inlineCredUsernameRequired')
    valid = false
  }
  if (!inlineCredentialForm.value.secret) {
    inlineCredentialErrors.value.secret = t('projects.inlineCredSecretRequired')
    valid = false
  }
  if (!valid) return

  inlineCredentialSubmitting.value = true
  try {
    const credential = await createCredential({
      name: inlineCredentialForm.value.name.trim(),
      type: inlineCredentialForm.value.type,
      scope: '',
      username: inlineCredentialForm.value.username.trim(),
      secret: inlineCredentialForm.value.secret,
    })
    credentials.value = [credential, ...credentials.value]
    createForm.value.credentialId = credential.id
    inlineCredentialOpen.value = false
    inlineCredentialForm.value.secret = ''
    testState.value = 'idle'
  } catch (err) {
    if (err instanceof HttpError) {
      inlineCredentialBanner.value = err.apiError?.message ?? t('projects.inlineCredCreateStatus', { status: err.status })
    } else {
      inlineCredentialBanner.value = t('projects.inlineCredCreateRetry')
    }
  } finally {
    inlineCredentialSubmitting.value = false
  }
}

function validateCreateForm(): boolean {
  clearCreateErrors()
  let ok = true
  if (!createForm.value.name.trim()) {
    createErrors.value.name = t('projects.errNameRequired')
    ok = false
  }
  // 纯发布项目没有仓库/凭据这两项,自然也不校验它们。
  if (createForm.value.bindRepo) {
    if (!createForm.value.repoUrl.trim()) {
      createErrors.value.repoUrl = t('projects.errRepoRequired')
      ok = false
    } else if (!isSupportedRepoUrl(createForm.value.repoUrl.trim())) {
      createErrors.value.repoUrl = t('projects.errRepoFormat')
      ok = false
    }
    if (!createForm.value.credentialId) {
      createErrors.value.credentialId = t('projects.errCredRequired')
      ok = false
    }
  }
  return ok
}

async function handleTestClone(): Promise<void> {
  // Validate url + credential only
  let ok = true
  if (!createForm.value.repoUrl.trim()) {
    createErrors.value.repoUrl = t('projects.errRepoFirst')
    ok = false
  }
  if (!createForm.value.credentialId) {
    createErrors.value.credentialId = t('projects.errCredFirst')
    ok = false
  }
  if (!ok) return

  testState.value = 'testing'
  testError.value = ''
  testDetectedBranch.value = ''
  remoteBranches.value = []
  remoteTags.value = []

  try {
    const result = await testClone({
      repoUrl: createForm.value.repoUrl.trim(),
      credentialId: createForm.value.credentialId,
    })
    testState.value = 'ok'
    testDetectedBranch.value = result.defaultBranch
    remoteBranches.value = result.branches ?? []
    remoteTags.value = result.tags ?? []
    // Auto-fill default branch if user hasn't typed one
    if (!createForm.value.defaultBranch) {
      createForm.value.defaultBranch = result.defaultBranch
    }
  } catch (err) {
    testState.value = 'error'
    testError.value = testCloneErrorText(err)
  }
}

async function handleCreateSubmit(): Promise<void> {
  if (!validateCreateForm()) return
  createSubmitting.value = true
  createBanner.value = ''

  const bound = createForm.value.bindRepo
  const input: CreateProjectInput = {
    name: createForm.value.name.trim(),
    repoUrl: bound ? createForm.value.repoUrl.trim() : '',
    credentialId: bound ? createForm.value.credentialId : '',
  }
  if (bound && createForm.value.defaultBranch.trim()) {
    input.defaultBranch = createForm.value.defaultBranch.trim()
  }
  // 未归组是零值,不必发送;发出去反而让后端按「放进某个组」做 Manage 校验。
  if (createForm.value.groupId) {
    input.groupId = createForm.value.groupId
  }

  try {
    const created = await createProject(input)
    projects.value = [created, ...projects.value]
    createModalOpen.value = false
  } catch (err) {
    if (err instanceof HttpError) {
      const code = err.apiError?.code
      if (code === 'credential_error') {
        createErrors.value.credentialId = t('projects.createErrCredField')
        createBanner.value = t('projects.createErrCredBanner')
      } else if (code === 'repo_unreachable') {
        createErrors.value.repoUrl = t('projects.createErrRepoField')
        createBanner.value = t('projects.createErrRepoBanner')
      } else if (code === 'vault_unconfigured') {
        createBanner.value = t('projects.createErrVault')
      } else if (err.status === 0) {
        createBanner.value = t('projects.errNetwork')
      } else {
        createBanner.value = err.apiError?.message ?? t('projects.createErrStatus', { status: err.status })
      }
    } else {
      createBanner.value = t('projects.createErrRetry')
    }
  } finally {
    createSubmitting.value = false
  }
}

// testCloneErrorText 把「测试连接」的失败映射成人话:新建弹窗与仓库弹窗共用同一套说法。
function testCloneErrorText(err: unknown): string {
  if (err instanceof HttpError) {
    const code = err.apiError?.code
    if (code === 'credential_error') return t('projects.testErrCredential')
    if (code === 'repo_unreachable') return t('projects.testErrUnreachable')
    if (code === 'vault_unconfigured') return t('projects.testErrVault')
    if (err.status === 0) return t('projects.errNetwork')
    return err.apiError?.message ?? t('projects.testErrStatus', { status: err.status })
  }
  return t('projects.testErrRetry')
}

// ─── repo modal(绑定 / 改绑 / 解绑项目仓库)──────────────────────────────────
// 建项目时选了「不绑定仓库」也能事后补上;绑了也能解绑(退回纯发布用)。
// 解绑是连带清除:凭据引用、默认分支、流水线即代码与 PR 状态回写都依赖读得到仓库。

const repoModalOpen = ref(false)
const repoProject = ref<Project | null>(null)
const repoForm = ref({ bindRepo: true, repoUrl: '', credentialId: '', defaultBranch: '' })
const repoErrors = ref({ repoUrl: '', credentialId: '' })
const repoBanner = ref('')
const repoSubmitting = ref(false)
const repoTestState = ref<TestState>('idle')
const repoTestError = ref('')
const repoRemoteBranches = ref<string[]>([])
const repoRemoteTags = ref<string[]>([])

function openRepoModal(p: Project): void {
  repoProject.value = p
  repoForm.value = {
    bindRepo: Boolean(p.repoUrl),
    repoUrl: p.repoUrl,
    credentialId: p.credentialId,
    defaultBranch: p.defaultBranch,
  }
  repoErrors.value = { repoUrl: '', credentialId: '' }
  repoBanner.value = ''
  repoSubmitting.value = false
  repoTestState.value = 'idle'
  repoTestError.value = ''
  repoRemoteBranches.value = []
  repoRemoteTags.value = []
  repoModalOpen.value = true
}

function closeRepoModal(): void {
  if (repoSubmitting.value) return
  repoModalOpen.value = false
}

function clearRepoErrors(): void {
  repoErrors.value = { repoUrl: '', credentialId: '' }
}

function validateRepoForm(): boolean {
  clearRepoErrors()
  if (!repoForm.value.bindRepo) return true
  let ok = true
  if (!repoForm.value.repoUrl.trim()) {
    repoErrors.value.repoUrl = t('projects.errRepoRequired')
    ok = false
  } else if (!isSupportedRepoUrl(repoForm.value.repoUrl.trim())) {
    repoErrors.value.repoUrl = t('projects.errRepoFormat')
    ok = false
  }
  if (!repoForm.value.credentialId) {
    repoErrors.value.credentialId = t('projects.errCredRequired')
    ok = false
  }
  return ok
}

async function handleRepoTestClone(): Promise<void> {
  let ok = true
  if (!repoForm.value.repoUrl.trim()) {
    repoErrors.value.repoUrl = t('projects.errRepoFirst')
    ok = false
  }
  if (!repoForm.value.credentialId) {
    repoErrors.value.credentialId = t('projects.errCredFirst')
    ok = false
  }
  if (!ok) return

  repoTestState.value = 'testing'
  repoTestError.value = ''
  repoRemoteBranches.value = []
  repoRemoteTags.value = []
  try {
    const result = await testClone({
      repoUrl: repoForm.value.repoUrl.trim(),
      credentialId: repoForm.value.credentialId,
    })
    repoTestState.value = 'ok'
    repoRemoteBranches.value = result.branches ?? []
    repoRemoteTags.value = result.tags ?? []
    if (!repoForm.value.defaultBranch) {
      repoForm.value.defaultBranch = result.defaultBranch
    }
  } catch (err) {
    repoTestState.value = 'error'
    repoTestError.value = testCloneErrorText(err)
  }
}

async function handleRepoSubmit(): Promise<void> {
  const target = repoProject.value
  if (!target || !validateRepoForm()) return
  repoSubmitting.value = true
  repoBanner.value = ''

  const bound = repoForm.value.bindRepo
  const input: UpdateProjectInput = { repoUrl: bound ? repoForm.value.repoUrl.trim() : '' }
  if (bound) {
    input.credentialId = repoForm.value.credentialId
    input.defaultBranch = repoForm.value.defaultBranch.trim()
  }

  try {
    const updated = await updateProject(target.id, input)
    projects.value = projects.value.map((p) => (p.id === updated.id ? updated : p))
    repoModalOpen.value = false
  } catch (err) {
    if (err instanceof HttpError) {
      const code = err.apiError?.code
      if (code === 'credential_error') {
        repoErrors.value.credentialId = t('projects.createErrCredField')
        repoBanner.value = t('projects.createErrCredBanner')
      } else if (code === 'repo_unreachable') {
        repoErrors.value.repoUrl = t('projects.createErrRepoField')
        repoBanner.value = t('projects.repoErrUnreachable')
      } else if (code === 'vault_unconfigured') {
        repoBanner.value = t('projects.createErrVault')
      } else if (err.status === 0) {
        repoBanner.value = t('projects.errNetwork')
      } else {
        repoBanner.value = err.apiError?.message ?? t('projects.repoErrStatus', { status: err.status })
      }
    } else {
      repoBanner.value = t('projects.repoErrRetry')
    }
  } finally {
    repoSubmitting.value = false
  }
}

// ─── rename modal ──────────────────────────────────────────────────────────────

const renameModalOpen = ref(false)
const renamingProject = ref<Project | null>(null)
const renameValue = ref('')
const renameError = ref('')
const renameBanner = ref('')
const renameSubmitting = ref(false)

function openRenameModal(p: Project): void {
  renamingProject.value = p
  renameValue.value = p.name
  renameError.value = ''
  renameBanner.value = ''
  renameModalOpen.value = true
}

function closeRenameModal(): void {
  if (renameSubmitting.value) return
  renameModalOpen.value = false
  renamingProject.value = null
}

async function handleRenameSubmit(): Promise<void> {
  if (!renameValue.value.trim()) {
    renameError.value = t('projects.errNameEmpty')
    return
  }
  if (!renamingProject.value) return
  renameSubmitting.value = true
  renameBanner.value = ''

  const input: UpdateProjectInput = { name: renameValue.value.trim() }

  try {
    const updated = await updateProject(renamingProject.value.id, input)
    projects.value = projects.value.map((p) => (p.id === updated.id ? updated : p))
    renameModalOpen.value = false
    renamingProject.value = null
  } catch (err) {
    if (err instanceof HttpError) {
      if (err.status === 0) {
        renameBanner.value = t('projects.errNetworkRetry')
      } else {
        renameBanner.value = err.apiError?.message ?? t('projects.renameErrStatus', { status: err.status })
      }
    } else {
      renameBanner.value = t('projects.renameErrRetry')
    }
  } finally {
    renameSubmitting.value = false
  }
}

// ─── 归组 modal ───────────────────────────────────────────────────────────────
//
// 与改名的区别:归属改动是权限边界变更(谁能看、谁能跑),所以候选只列「我管得动的组」,
// 且选「移出到未归组」时对旧组的 Manage 由后端判定,这里只负责把 403 的原因说清楚。

const groupModalOpen = ref(false)
const groupingProject = ref<Project | null>(null)
const groupValue = ref('')
const groupBanner = ref('')
const groupSubmitting = ref(false)

/** 候选:未归组 + 我有 Manage 权的组;当前所在组即使不可管也要能显示。 */
const groupOptions = computed<Group[]>(() => {
  const cur = groupingProject.value?.groupId
  if (!cur) return manageableGroups.value
  const g = groupById.value[cur]
  if (!g || g.canManage) return manageableGroups.value
  return [g, ...manageableGroups.value]
})

function openGroupModal(p: Project): void {
  groupingProject.value = p
  groupValue.value = p.groupId
  groupBanner.value = ''
  groupModalOpen.value = true
}

function closeGroupModal(): void {
  if (groupSubmitting.value) return
  groupModalOpen.value = false
  groupingProject.value = null
}

async function handleGroupSubmit(): Promise<void> {
  if (!groupingProject.value) return
  const origin = groupingProject.value
  if (groupValue.value === origin.groupId) {
    closeGroupModal()
    return
  }
  groupSubmitting.value = true
  groupBanner.value = ''
  try {
    const updated = await updateProject(origin.id, { groupId: groupValue.value })
    projects.value = projects.value.map((p) => (p.id === updated.id ? updated : p))
    groupModalOpen.value = false
    groupingProject.value = null
    // 把项目挪出可见范围后,列表要重新按新可见范围取一次(它可能不再属于我可见的组)。
    if (!groupValue.value || !groupById.value[groupValue.value]?.canManage) {
      await loadProjects()
    }
  } catch (err) {
    if (err instanceof HttpError) {
      if (err.status === 0) {
        groupBanner.value = t('projects.errNetworkRetry')
      } else if (err.status === 403) {
        groupBanner.value = err.apiError?.message ?? t('groups.errForbidden')
      } else {
        groupBanner.value = err.apiError?.message ?? t('groups.errStatus', { status: err.status })
      }
    } else {
      groupBanner.value = t('groups.errRetry')
    }
  } finally {
    groupSubmitting.value = false
  }
}

// ─── delete confirm modal ─────────────────────────────────────────────────────

const deleteModalOpen = ref(false)
const deletingProject = ref<Project | null>(null)
const deleteSubmitting = ref(false)
const deleteBanner = ref('')

function openDeleteModal(p: Project): void {
  deletingProject.value = p
  deleteBanner.value = ''
  deleteModalOpen.value = true
}

function closeDeleteModal(): void {
  if (deleteSubmitting.value) return
  deleteModalOpen.value = false
  deletingProject.value = null
}

async function confirmDelete(): Promise<void> {
  if (!deletingProject.value) return
  deleteSubmitting.value = true
  deleteBanner.value = ''
  const id = deletingProject.value.id

  try {
    await deleteProject(id)
    projects.value = projects.value.filter((p) => p.id !== id)
    deleteModalOpen.value = false
    deletingProject.value = null
  } catch (err) {
    if (err instanceof HttpError) {
      if (err.status === 0) {
        deleteBanner.value = t('projects.errNetworkRetry')
      } else {
        deleteBanner.value = err.apiError?.message ?? t('projects.deleteErrStatus', { status: err.status })
      }
    } else {
      deleteBanner.value = t('projects.deleteErrRetry')
    }
  } finally {
    deleteSubmitting.value = false
  }
}

// ─── manual trigger modal ─────────────────────────────────────────────────────

const triggerModalOpen   = ref(false)
const triggerProject     = ref<Project | null>(null)
const triggerForm        = ref({ branch: '', commit: '' })
const triggerParams      = ref<Record<string, string>>({})
const triggerDefs        = ref<ParamDef[]>([])
const triggerBranchError = ref('')
const triggerBanner      = ref('')
const triggerSubmitting  = ref(false)

function openTriggerModal(p: Project): void {
  triggerProject.value     = p
  triggerForm.value        = { branch: p.defaultBranch || '', commit: '' }
  triggerParams.value      = {}
  triggerDefs.value        = []
  triggerBranchError.value = ''
  triggerBanner.value      = ''
  triggerSubmitting.value  = false
  triggerModalOpen.value   = true
  // 纯发布项目没有仓库,列分支只会 400;参数定义照拉(运行参数与仓库无关)。
  if (p.repoUrl) void loadBranchOptions(p.id)
  void loadTriggerDefs(p.id)
}

// 类型化运行参数定义(P0):有定义 → 弹窗渲染类型化控件;无 → 回退自由 KV。失败静默(降级)。
async function loadTriggerDefs(projectId: string): Promise<void> {
  try {
    triggerDefs.value = await getParameters(projectId)
  } catch {
    triggerDefs.value = []
  }
}

// 代码管理区(Story 8-18):拉取真实分支/commit 供下拉建议;未启用/失败 → 静默回退手填。
const triggerBranches = ref<string[]>([])
const triggerTags = ref<string[]>([])
const commitOptions = ref<GitCommit[]>([])
async function loadBranchOptions(projectId: string): Promise<void> {
  triggerBranches.value = []
  triggerTags.value = []
  commitOptions.value = []
  try {
    const refs = await listRefs(projectId)
    triggerBranches.value = refs.branches.map((r) => r.name)
    triggerTags.value = refs.tags.map((r) => r.name)
  } catch {
    // 代码管理区未启用(503)或仓库不可达:静默,保持纯文本输入(优雅降级)。
  }
  void loadCommitOptions(projectId, triggerForm.value.branch)
}

// 据当前分支拉最近 commit 供 commit 下拉(分支变化时刷新);失败静默。
let commitLoadSeq = 0
async function loadCommitOptions(projectId: string, ref: string): Promise<void> {
  const seq = ++commitLoadSeq
  try {
    const commits = await listCommits(projectId, ref, 30)
    if (seq === commitLoadSeq) commitOptions.value = commits
  } catch {
    if (seq === commitLoadSeq) commitOptions.value = []
  }
}

function onTriggerBranchInput(value?: string): void {
  triggerBranchError.value = ''
  if (triggerProject.value) void loadCommitOptions(triggerProject.value.id, value ?? triggerForm.value.branch)
}

function closeTriggerModal(): void {
  if (triggerSubmitting.value) return
  triggerModalOpen.value = false
  triggerProject.value   = null
}

async function handleTriggerSubmit(): Promise<void> {
  triggerBranchError.value = ''
  triggerBanner.value      = ''

  // 分支可选:留空时后端按项目默认分支解析(项目已知默认分支,不必逼用户填)。
  const branch = triggerForm.value.branch.trim()

  if (!triggerProject.value) return

  // 类型化参数:提交前客户端即时校验(后端仍会再校验一次 422)。
  if (triggerDefs.value.length) {
    const verr = validateParamValues(triggerDefs.value, triggerParams.value)
    if (verr) {
      triggerBanner.value = verr
      return
    }
  }

  triggerSubmitting.value = true

  try {
    const input: TriggerManualInput = {}
    if (branch) input.branch = branch
    const commit = triggerForm.value.commit.trim()
    if (commit) input.commit = commit
    if (Object.keys(triggerParams.value).length) input.params = triggerParams.value

    const run: RunDetail = await triggerManual(triggerProject.value.id, input)
    triggerModalOpen.value = false
    triggerProject.value   = null
    void router.push(`/runs/${run.id}`)
  } catch (err) {
    if (err instanceof HttpError) {
      if (err.status === 0) {
        triggerBanner.value = t('projects.errNetwork')
      } else if (err.status === 404) {
        triggerBanner.value = t('projects.triggerErrNotFound')
      } else {
        triggerBanner.value = err.apiError?.message ?? t('projects.triggerErrStatus', { status: err.status })
      }
    } else {
      triggerBanner.value = t('projects.triggerErrRetry')
    }
  } finally {
    triggerSubmitting.value = false
  }
}

</script>

<template>
  <div class="projects-root">
    <!-- ─── Page header ─────────────────────────────────────────────────── -->
    <header class="page-header">
      <div class="page-header-text">
        <h1 class="page-title">{{ t('projects.title') }}</h1>
        <p class="page-sub">{{ t('projects.subtitle') }}</p>
      </div>
      <button
        class="btn-primary"
        :disabled="loadState === 'loading'"
        @click="openCreateModal"
      >
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" aria-hidden="true">
          <path d="M12 5v14M5 12h14"/>
        </svg>
        {{ t('projects.newProject') }}
      </button>
    </header>

    <!-- ─── Load error banner ────────────────────────────────────────────── -->
    <div
      v-if="loadState === 'error'"
      class="banner banner--error"
      role="alert"
    >
      <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
        <circle cx="12" cy="12" r="9"/><path d="M12 8v4M12 16h.01"/>
      </svg>
      <span>{{ loadError }}</span>
      <button class="banner-retry" @click="loadProjects">↻ {{ t('projects.retry') }}</button>
    </div>

    <!-- ─── Search + Filter toolbar ─────────────────────────────────────── -->
    <div
      v-if="loadState === 'idle' && projects.length > 0"
      class="toolbar"
      role="search"
      :aria-label="t('projects.searchFilterAria')"
    >
      <!-- Search box -->
      <div class="search-wrap">
        <svg class="search-icon" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
          <circle cx="11" cy="11" r="7"/><path d="m21 21-4.35-4.35"/>
        </svg>
        <input
          v-model="searchQuery"
          type="search"
          class="search-input"
          :placeholder="t('projects.searchPlaceholder')"
          :aria-label="t('projects.searchAria')"
        />
      </div>

      <!-- Group filter -->
      <div class="select-wrap group-filter">
        <select
          v-model="groupFilter"
          class="field-select"
          :aria-label="t('projects.groupFilterAria')"
        >
          <option v-for="opt in GROUP_OPTIONS" :key="opt.value" :value="opt.value">
            {{ opt.label }}
          </option>
        </select>
        <svg class="select-arrow" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" aria-hidden="true">
          <path d="M6 9l6 6 6-6"/>
        </svg>
      </div>

      <!-- Status filter -->
      <div class="filter-tabs" role="group" :aria-label="t('projects.statusFilterAria')">
        <button
          v-for="opt in STATUS_OPTIONS"
          :key="opt.value"
          type="button"
          class="filter-tab"
          :class="{ 'filter-tab--active': statusFilter === opt.value }"
          @click="statusFilter = opt.value"
        >
          {{ opt.label }}
        </button>
      </div>

      <!-- 视图切换:卡片铺开 / 按组折叠。放在工具栏最右,和「这一页怎么看」的位置一致。 -->
      <div class="filter-tabs view-toggle" role="group" :aria-label="t('projects.viewModeAria')">
        <button
          type="button"
          class="filter-tab view-toggle-btn"
          :class="{ 'filter-tab--active': viewMode === 'cards' }"
          :aria-pressed="viewMode === 'cards'"
          @click="setViewMode('cards')"
        >
          <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
            <rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/>
            <rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/>
          </svg>
          {{ t('projects.viewCards') }}
        </button>
        <button
          type="button"
          class="filter-tab view-toggle-btn"
          :class="{ 'filter-tab--active': viewMode === 'groups' }"
          :aria-pressed="viewMode === 'groups'"
          @click="setViewMode('groups')"
        >
          <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true">
            <path d="M3 5h18M3 12h18M3 19h11"/>
          </svg>
          {{ t('projects.viewGroups') }}
        </button>
      </div>
    </div>

    <!-- ─── Loading skeleton ─────────────────────────────────────────────── -->
    <template v-if="loadState === 'loading'">
      <div class="project-grid" aria-busy="true" :aria-label="t('projects.loading')">
        <div
          v-for="i in 6"
          :key="i"
          class="skel-card"
          aria-hidden="true"
        >
          <div class="skel skel--name" />
          <div class="skel skel--url" />
          <div class="skel skel--tag" />
          <div class="skel skel--meta" />
        </div>
      </div>
    </template>

    <!-- ─── Empty state ──────────────────────────────────────────────────── -->
    <template v-else-if="loadState === 'idle' && projects.length === 0">
      <div class="empty-state" role="status">
        <div class="empty-icon" aria-hidden="true">
          <svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6">
            <path d="M14.5 9.5 21 3M21 3h-5M21 3v5"/>
            <path d="M10 14a5 5 0 1 1-7 4.6"/>
          </svg>
        </div>
        <p class="empty-label">{{ t('projects.emptyTitle') }}</p>
        <p class="empty-hint">{{ t('projects.emptyHint') }}</p>
        <button class="btn-primary" @click="openCreateModal">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" aria-hidden="true">
            <path d="M12 5v14M5 12h14"/>
          </svg>
          {{ t('projects.newProject') }}
        </button>
      </div>
    </template>

    <!-- ─── Empty search result ──────────────────────────────────────────── -->
    <template v-else-if="loadState === 'idle' && filteredProjects.length === 0">
      <div class="empty-state" role="status">
        <div class="empty-icon" aria-hidden="true">
          <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6">
            <circle cx="11" cy="11" r="7"/><path d="m21 21-4.35-4.35"/>
          </svg>
        </div>
        <p class="empty-label">{{ t('projects.noMatchTitle') }}</p>
        <p class="empty-hint">{{ t('projects.noMatchHint') }}</p>
        <button
          class="btn-secondary"
          @click="searchQuery = ''; statusFilter = 'all'; groupFilter = GROUP_ALL"
        >{{ t('projects.clearFilter') }}</button>
      </div>
    </template>

    <!-- ─── Project grid ─────────────────────────────────────────────────── -->
    <template v-else-if="loadState === 'idle'">
      <p class="result-count" aria-live="polite">
        {{ t('projects.resultCount', { n: filteredProjects.length }) }}
        <template v-if="searchQuery || statusFilter !== 'all' || groupFilter !== GROUP_ALL">
          {{ t('projects.resultCountTotal', { total: projects.length }) }}
        </template>
      </p>

      <!-- ─── 卡片视图:命中项目一把铺开 ──────────────────────────────────── -->
      <ul v-if="viewMode === 'cards'" class="project-grid" role="list">
        <ProjectCard
          v-for="project in filteredProjects"
          :key="project.id"
          :project="project"
          :group-label="groupName(project.groupId)"
          :can-assign-group="canRegroup(project)"
          @run="openTriggerModal"
          @rename="openRenameModal"
          @repo="openRepoModal"
          @code="openCardCode"
          @pipeline="openCardPipeline"
          @remove="openDeleteModal"
          @assign="openGroupModal"
        />
      </ul>

      <!-- ─── 分组视图:默认只看各个分组,点开某组才铺该组的卡 ─────────────── -->
      <div v-else class="group-list">
        <section
          v-for="section in groupSections"
          :id="sectionGridId(section.key)"
          :key="section.key"
          class="group-section"
          :aria-label="sectionLabel(section)"
        >
          <button
            type="button"
            class="group-head"
            :aria-expanded="isSectionOpen(section.key)"
            :aria-controls="sectionGridId(section.key)"
            @click="toggleSection(section.key)"
          >
            <svg
              class="group-chevron"
              :class="{ 'group-chevron--open': isSectionOpen(section.key) }"
              width="12"
              height="12"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              stroke-width="2.4"
              stroke-linecap="round"
              stroke-linejoin="round"
              aria-hidden="true"
            >
              <path d="M9 6l6 6-6 6"/>
            </svg>
            <span class="group-name">{{ sectionLabel(section) }}</span>
            <span
              v-if="section.visibility"
              class="group-visibility"
              :class="{ 'group-visibility--private': section.visibility === 'private' }"
            >{{ section.visibility === 'public' ? t('groups.visibilityPublic') : t('groups.visibilityPrivate') }}</span>
            <span class="group-count">{{ t('projects.resultCount', { n: section.projects.length }) }}</span>
            <span v-if="section.ownerName" class="group-owner">{{ t('groups.colOwner') }}:{{ section.ownerName }}</span>
          </button>

          <template v-if="isSectionOpen(section.key)">
            <ul v-if="section.projects.length" class="project-grid group-projects" role="list">
              <ProjectCard
                v-for="project in section.projects"
                :key="project.id"
                :project="project"
                :group-label="groupName(project.groupId)"
                :can-assign-group="canRegroup(project)"
                @run="openTriggerModal"
                @rename="openRenameModal"
                @repo="openRepoModal"
                @code="openCardCode"
                @pipeline="openCardPipeline"
                @remove="openDeleteModal"
                @assign="openGroupModal"
              />
            </ul>
            <p v-else class="group-empty">{{ t('projects.groupEmpty') }}</p>
          </template>
        </section>
      </div>
    </template>
  </div>

  <!-- ═══════════════════════════════════════════════════════════════════════
       Manual trigger modal
  ════════════════════════════════════════════════════════════════════════ -->
  <Teleport to="body">
    <div
      v-if="triggerModalOpen && triggerProject"
      class="modal-scrim"
      role="dialog"
      :aria-label="t('projects.triggerDialogAria', { name: triggerProject.name })"
      aria-modal="true"
      @keydown.esc="closeTriggerModal"
      @click.self="closeTriggerModal"
    >
      <div class="modal modal--sm">
        <!-- Header -->
        <div class="modal-head">
          <div class="modal-icon modal-icon--run" aria-hidden="true">
            <svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7">
              <polygon points="5 3 19 12 5 21 5 3" fill="currentColor" stroke="none"/>
            </svg>
          </div>
          <div>
            <h3 class="modal-title">{{ t('projects.triggerTitle') }}</h3>
            <p class="modal-sub">{{ triggerProject.repoUrl ? t('projects.triggerSub', { name: triggerProject.name }) : t('projects.triggerSubNoRepo', { name: triggerProject.name }) }}</p>
          </div>
          <button
            class="modal-close"
            :aria-label="t('projects.closeDialog')"
            :disabled="triggerSubmitting"
            @click="closeTriggerModal"
          >
            <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <path d="M18 6 6 18M6 6l12 12"/>
            </svg>
          </button>
        </div>

        <!-- Error banner -->
        <div
          v-if="triggerBanner"
          class="banner banner--error modal-banner"
          role="alert"
        >
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
            <circle cx="12" cy="12" r="9"/><path d="M12 8v4M12 16h.01"/>
          </svg>
          {{ triggerBanner }}
        </div>

        <form
          class="modal-form"
          novalidate
          @submit.prevent="handleTriggerSubmit"
        >
          <!-- Branch(纯发布项目没有仓库,不显示分支/commit 两项) -->
          <div v-if="triggerProject?.repoUrl" class="field">
            <label class="field-label" for="trigger-branch">
              {{ t('projects.branch') }}
              <span class="field-hint-inline">{{ t('projects.branchHint') }}</span>
            </label>
            <RefPicker
              input-id="trigger-branch"
              v-model="triggerForm.branch"
              :branches="triggerBranches"
              :tags="triggerTags"
              :disabled="triggerSubmitting"
              :has-error="!!triggerBranchError"
              :described-by="triggerBranchError ? 'trigger-branch-err' : undefined"
              placeholder="main"
              :branches-label="t('projects.refGroupBranches')"
              :tags-label="t('projects.refGroupTags')"
              @update:model-value="onTriggerBranchInput"
            />
            <span
              v-if="triggerBranchError"
              id="trigger-branch-err"
              class="field-error"
              role="alert"
            >{{ triggerBranchError }}</span>
          </div>

          <!-- Commit (optional) -->
          <div v-if="triggerProject?.repoUrl" class="field">
            <label class="field-label" for="trigger-commit">
              {{ t('projects.commit') }}
              <span class="field-hint-inline">{{ t('projects.commitHint') }}</span>
            </label>
            <input
              id="trigger-commit"
              v-model="triggerForm.commit"
              class="field-input field-input--mono"
              type="text"
              :placeholder="t('projects.commitPlaceholder')"
              autocomplete="off"
              list="trigger-commit-options"
              :disabled="triggerSubmitting"
            />
            <!-- 代码管理区:当前分支最近 commit 下拉建议(短 sha — 首行说明) -->
            <datalist id="trigger-commit-options">
              <option v-for="c in commitOptions" :key="c.sha" :value="c.short">{{ c.subject }}</option>
            </datalist>
          </div>

          <!-- Parameters · 有类型化定义(P0)→ 渲染类型化控件;无 → 自由 KV(Story 8-11) -->
          <div class="field">
            <label class="field-label">
              {{ t('projects.params') }}
              <span class="field-hint-inline">{{ triggerDefs.length ? t('projects.paramsHintTyped') : t('projects.paramsHintFree') }}</span>
            </label>
            <TypedRunParams
              v-if="triggerDefs.length"
              :key="`typed-${triggerProject.id}`"
              :defs="triggerDefs"
              v-model="triggerParams"
            />
            <RunParamsEditor
              v-else
              :key="triggerProject.id"
              v-model="triggerParams"
              :disabled="triggerSubmitting"
            />
          </div>

          <!-- Footer -->
          <div class="modal-footer">
            <button
              type="button"
              class="btn-secondary"
              :disabled="triggerSubmitting"
              @click="closeTriggerModal"
            >{{ t('projects.cancel') }}</button>
            <button
              type="submit"
              class="btn-run"
              :disabled="triggerSubmitting"
              :aria-busy="triggerSubmitting"
            >
              <span v-if="triggerSubmitting" class="spinner" aria-hidden="true" />
              <svg v-else width="13" height="13" viewBox="0 0 24 24" fill="none" aria-hidden="true">
                <polygon points="5 3 19 12 5 21 5 3" fill="currentColor"/>
              </svg>
              {{ triggerSubmitting ? t('projects.triggering') : t('projects.runNow') }}
            </button>
          </div>
        </form>
      </div>
    </div>
  </Teleport>

  <!-- ═══════════════════════════════════════════════════════════════════════
       New project modal
  ════════════════════════════════════════════════════════════════════════ -->
  <Teleport to="body">
    <div
      v-if="createModalOpen"
      class="modal-scrim"
      role="dialog"
      :aria-label="t('projects.newProject')"
      aria-modal="true"
      @keydown.esc="closeCreateModal"
      @click.self="closeCreateModal"
    >
      <div class="modal">
        <!-- Header -->
        <div class="modal-head">
          <div class="modal-icon" aria-hidden="true">
            <svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7">
              <path d="M14.5 9.5 21 3M21 3h-5M21 3v5"/>
              <path d="M10 14a5 5 0 1 1-7 4.6"/>
            </svg>
          </div>
          <div>
            <h3 class="modal-title">{{ t('projects.newProject') }}</h3>
            <p class="modal-sub">{{ t('projects.createSub') }}</p>
          </div>
          <button
            class="modal-close"
            :aria-label="t('projects.closeDialog')"
            :disabled="createSubmitting"
            @click="closeCreateModal"
          >
            <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <path d="M18 6 6 18M6 6l12 12"/>
            </svg>
          </button>
        </div>

        <!-- Error banner -->
        <div
          v-if="createBanner"
          class="banner banner--error modal-banner"
          role="alert"
        >
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
            <circle cx="12" cy="12" r="9"/><path d="M12 8v4M12 16h.01"/>
          </svg>
          {{ createBanner }}
        </div>

        <form
          class="modal-form"
          novalidate
          @submit.prevent="handleCreateSubmit"
        >
          <!-- Project name -->
          <div class="field">
            <label class="field-label" for="proj-name">{{ t('projects.fieldName') }}</label>
            <input
              id="proj-name"
              v-model="createForm.name"
              class="field-input"
              :class="{ 'field-input--error': createErrors.name }"
              type="text"
              :placeholder="t('projects.fieldNamePlaceholder')"
              autocomplete="off"
              :disabled="createSubmitting"
              :aria-invalid="createErrors.name ? 'true' : undefined"
              :aria-describedby="createErrors.name ? 'proj-name-err' : undefined"
              @input="createErrors.name = ''"
            />
            <span v-if="createErrors.name" id="proj-name-err" class="field-error" role="alert">{{ createErrors.name }}</span>
          </div>

          <!-- 源码来源:绑定仓库 or 只发布(不拉源码) -->
          <div class="field">
            <label class="field-check">
              <input
                v-model="createForm.bindRepo"
                type="checkbox"
                :disabled="createSubmitting"
                @change="createErrors.repoUrl = ''; createErrors.credentialId = ''; testState = 'idle'; remoteBranches = []; remoteTags = []"
              />
              <span>{{ t('projects.bindRepo') }}</span>
            </label>
            <span class="field-hint">{{ createForm.bindRepo ? t('projects.bindRepoOnHint') : t('projects.bindRepoOffHint') }}</span>
          </div>

          <!-- Repo URL -->
          <div v-if="createForm.bindRepo" class="field">
            <label class="field-label" for="proj-repo">{{ t('projects.fieldRepo') }}</label>
            <input
              id="proj-repo"
              v-model="createForm.repoUrl"
              class="field-input field-input--mono"
              :class="{ 'field-input--error': createErrors.repoUrl }"
              type="url"
              :placeholder="t('projects.repoUrlPlaceholder')"
              autocomplete="off"
              :disabled="createSubmitting"
              :aria-invalid="createErrors.repoUrl ? 'true' : undefined"
              :aria-describedby="createErrors.repoUrl ? 'proj-repo-err' : undefined"
              @input="createErrors.repoUrl = ''; testState = 'idle'; remoteBranches = []; remoteTags = []"
            />
            <span v-if="createErrors.repoUrl" id="proj-repo-err" class="field-error" role="alert">{{ createErrors.repoUrl }}</span>
          </div>

          <!-- Credential dropdown — Git credentials supported by project clone, masked display -->
          <div v-if="createForm.bindRepo" class="field">
            <label class="field-label" for="proj-cred">
              {{ t('projects.credential') }}
              <span class="field-hint-inline">{{ t('projects.fieldCredHint') }}</span>
            </label>
            <CredentialSelect
              input-id="proj-cred"
              v-model="createForm.credentialId"
              :credentials="gitCredentials"
              :loading="credentialsLoading"
              :disabled="createSubmitting"
              :has-error="Boolean(createErrors.credentialId)"
              :placeholder="t('projects.credSelect')"
              :loading-label="t('projects.credLoading')"
              :empty-label="t('projects.credSelect')"
              @change="createErrors.credentialId = ''; testState = 'idle'"
            />
            <button
              type="button"
              class="btn-ghost credential-create-toggle"
              :disabled="createSubmitting || inlineCredentialSubmitting"
              @click="inlineCredentialOpen = !inlineCredentialOpen; clearInlineCredentialErrors()"
            >
              {{ inlineCredentialOpen ? t('projects.inlineCredCancel') : t('projects.inlineCredNew') }}
            </button>
            <span v-if="createErrors.credentialId" id="proj-cred-err" class="field-error" role="alert">{{ createErrors.credentialId }}</span>
            <span
              v-if="!credentialsLoading && gitCredentials.length === 0"
              class="field-hint"
            >
              {{ t('projects.credEmptyPre') }}
              <a href="/settings/vault" class="link">{{ t('projects.credVaultLink') }}</a>
              {{ t('projects.credEmptyPost') }}
            </span>
          </div>

          <div v-if="createForm.bindRepo && inlineCredentialOpen" class="inline-credential-panel">
            <div class="inline-credential-title">{{ t('projects.inlineCredTitle') }}</div>
            <div v-if="inlineCredentialBanner" class="field-error" role="alert">{{ inlineCredentialBanner }}</div>
            <div class="field">
              <span class="field-label">{{ t('settingsVault.fieldType') }}</span>
              <div class="inline-credential-types" role="group" :aria-label="t('settingsVault.credentialTypeAria')">
                <button
                  v-for="type in (['git_token', 'git_http', 'git_ssh'] as CredentialType[])"
                  :key="type"
                  type="button"
                  class="inline-credential-type"
                  :class="{ 'inline-credential-type--active': inlineCredentialForm.type === type }"
                  :disabled="inlineCredentialSubmitting"
                  @click="inlineCredentialForm.type = type; inlineCredentialErrors.secret = ''"
                >{{ inlineCredTypeLabels[type] }}</button>
              </div>
            </div>
            <div class="field">
              <label class="field-label" for="inline-cred-name">{{ t('projects.inlineCredName') }}</label>
              <input
                id="inline-cred-name"
                v-model="inlineCredentialForm.name"
                class="field-input"
                :class="{ 'field-input--error': inlineCredentialErrors.name }"
                type="text"
                :placeholder="t('projects.inlineCredNamePlaceholder')"
                :disabled="inlineCredentialSubmitting"
                @input="inlineCredentialErrors.name = ''"
              />
              <span v-if="inlineCredentialErrors.name" class="field-error" role="alert">{{ inlineCredentialErrors.name }}</span>
            </div>
            <div class="field">
              <label class="field-label" for="inline-cred-username">
                {{ t('projects.inlineCredUsername') }}
                <span class="field-optional">{{ inlineCredentialForm.type === 'git_http' ? '' : t('settingsVault.optional') }}</span>
              </label>
              <input
                id="inline-cred-username"
                v-model="inlineCredentialForm.username"
                class="field-input"
                :class="{ 'field-input--error': inlineCredentialErrors.username }"
                type="text"
                :placeholder="t('projects.inlineCredUsernamePlaceholder')"
                :disabled="inlineCredentialSubmitting"
                autocomplete="username"
                @input="inlineCredentialErrors.username = ''"
              />
              <span v-if="inlineCredentialErrors.username" class="field-error" role="alert">{{ inlineCredentialErrors.username }}</span>
            </div>
            <div class="field">
              <label class="field-label" for="inline-cred-secret">{{ t('projects.inlineCredSecret') }}</label>
              <!-- git_ssh 私钥是多行 PEM:<input> 会按 HTML 规范吃掉换行,私钥直接作废 -->
              <textarea
                v-if="inlineCredentialForm.type === 'git_ssh'"
                id="inline-cred-secret"
                v-model="inlineCredentialForm.secret"
                class="field-input field-input--mono"
                :class="{ 'field-input--error': inlineCredentialErrors.secret }"
                rows="6"
                :placeholder="t('projects.inlineCredSecretPlaceholderSsh')"
                :disabled="inlineCredentialSubmitting"
                autocomplete="off"
                spellcheck="false"
                @input="inlineCredentialErrors.secret = ''"
              ></textarea>
              <input
                v-else
                id="inline-cred-secret"
                v-model="inlineCredentialForm.secret"
                class="field-input"
                :class="{ 'field-input--error': inlineCredentialErrors.secret }"
                type="password"
                :placeholder="t('projects.inlineCredSecretPlaceholder')"
                :disabled="inlineCredentialSubmitting"
                autocomplete="new-password"
                @input="inlineCredentialErrors.secret = ''"
              />
              <span class="field-hint">{{ inlineCredentialForm.type === 'git_ssh' ? t('projects.inlineCredSecretHintSsh') : t('projects.inlineCredSecretHint') }}</span>
              <span v-if="inlineCredentialErrors.secret" class="field-error" role="alert">{{ inlineCredentialErrors.secret }}</span>
            </div>
            <button
              type="button"
              class="btn-secondary inline-credential-submit"
              :disabled="inlineCredentialSubmitting"
              @click="handleInlineCredentialSubmit"
            >{{ inlineCredentialSubmitting ? t('projects.inlineCredCreating') : t('projects.inlineCredCreate') }}</button>
          </div>

          <!-- Default branch (optional) — 测试连接后展示真实分支/tag 面板下拉(仍可手输) -->
          <div v-if="createForm.bindRepo" class="field">
            <label class="field-label" for="proj-branch">
              {{ t('projects.fieldDefaultBranch') }}
              <span class="field-hint-inline">{{ t('projects.fieldDefaultBranchHint') }}</span>
            </label>
            <RefPicker
              input-id="proj-branch"
              v-model="createForm.defaultBranch"
              :branches="remoteBranches"
              :tags="remoteTags"
              :disabled="createSubmitting"
              placeholder="main"
              :branches-label="t('projects.refGroupBranches')"
              :tags-label="t('projects.refGroupTags')"
            />
            <span v-if="remoteBranches.length" class="field-hint">{{ t('projects.fieldDefaultBranchListed', { n: remoteBranches.length + remoteTags.length }) }}</span>
          </div>

          <!-- 分组:决定谁能看/谁能操作;未归组 = 全员可见可操作 -->
          <div class="field">
            <label class="field-label" for="proj-group">
              {{ t('groups.fieldGroup') }}
              <span class="field-hint-inline">{{ t('groups.fieldGroupHint') }}</span>
            </label>
            <select id="proj-group" v-model="createForm.groupId" class="field-input" :disabled="createSubmitting">
              <option value="">{{ t('groups.ungrouped') }}</option>
              <option v-for="g in manageableGroups" :key="g.id" :value="g.id">
                {{ g.name }} · {{ g.visibility === 'public' ? t('groups.visibilityPublic') : t('groups.visibilityPrivate') }}
              </option>
            </select>
            <span v-if="!manageableGroups.length" class="field-hint">{{ t('groups.noGroupsHint') }}</span>
          </div>

          <!-- Test clone — left-bottom, separated from primary actions -->
          <div v-if="createForm.bindRepo" class="test-clone-row">
            <button
              type="button"
              class="btn-ghost"
              :disabled="createSubmitting || testState === 'testing'"
              :aria-busy="testState === 'testing'"
              @click="handleTestClone"
            >
              <span v-if="testState === 'testing'" class="spinner spinner--dim" aria-hidden="true" />
              <svg v-else width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
                <path d="M5 12.55a11 11 0 0 1 14.08 0"/>
                <path d="M1.42 9a16 16 0 0 1 21.16 0"/>
                <path d="M8.53 16.11a6 6 0 0 1 6.95 0"/>
                <circle cx="12" cy="20" r="1" fill="currentColor"/>
              </svg>
              {{ testState === 'testing' ? t('projects.testing') : t('projects.testConnection') }}
            </button>

            <!-- Test result inline -->
            <div
              v-if="testState === 'ok'"
              class="test-result test-result--ok"
              role="status"
              aria-live="polite"
            >
              <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" aria-hidden="true">
                <path d="M20 6 9 17l-5-5"/>
              </svg>
              {{ t('projects.testOk') }}
              <span v-if="testDetectedBranch" class="test-branch mono">{{ t('projects.testDetectedBranch', { branch: testDetectedBranch }) }}</span>
            </div>

            <div
              v-else-if="testState === 'error'"
              class="test-result test-result--error"
              role="alert"
              aria-live="assertive"
            >
              <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
                <circle cx="12" cy="12" r="9"/><path d="M12 8v4M12 16h.01"/>
              </svg>
              {{ testError }}
            </div>
          </div>

          <!-- Modal footer -->
          <div class="modal-footer">
            <button
              type="button"
              class="btn-secondary"
              :disabled="createSubmitting"
              @click="closeCreateModal"
            >{{ t('projects.cancel') }}</button>
            <button
              type="submit"
              class="btn-primary"
              :disabled="createSubmitting"
              :aria-busy="createSubmitting"
            >
              <span v-if="createSubmitting" class="spinner" aria-hidden="true" />
              {{ createSubmitting ? t('projects.creating') : t('projects.createSubmit') }}
            </button>
          </div>
        </form>
      </div>
    </div>
  </Teleport>

  <!-- ═══════════════════════════════════════════════════════════════════════
       Repo modal(绑定 / 改绑 / 解绑仓库)
  ════════════════════════════════════════════════════════════════════════ -->
  <Teleport to="body">
    <div
      v-if="repoModalOpen && repoProject"
      class="modal-scrim"
      role="dialog"
      :aria-label="t('projects.repoTitle')"
      aria-modal="true"
      @keydown.esc="closeRepoModal"
      @click.self="closeRepoModal"
    >
      <div class="modal">
        <div class="modal-head">
          <div class="modal-icon" aria-hidden="true">
            <svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">
              <path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.72"/>
              <path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71"/>
            </svg>
          </div>
          <div>
            <h3 class="modal-title">{{ t('projects.repoTitle') }}</h3>
            <p class="modal-sub">{{ t('projects.repoSub', { name: repoProject.name }) }}</p>
          </div>
          <button
            class="modal-close"
            :aria-label="t('projects.closeDialog')"
            :disabled="repoSubmitting"
            @click="closeRepoModal"
          >
            <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <path d="M18 6 6 18M6 6l12 12"/>
            </svg>
          </button>
        </div>

        <div
          v-if="repoBanner"
          class="banner banner--error modal-banner"
          role="alert"
        >
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
            <circle cx="12" cy="12" r="9"/><path d="M12 8v4M12 16h.01"/>
          </svg>
          {{ repoBanner }}
        </div>

        <form
          class="modal-form"
          novalidate
          @submit.prevent="handleRepoSubmit"
        >
          <div class="field">
            <label class="field-check">
              <input
                v-model="repoForm.bindRepo"
                type="checkbox"
                :disabled="repoSubmitting"
                @change="clearRepoErrors(); repoTestState = 'idle'; repoRemoteBranches = []; repoRemoteTags = []"
              />
              <span>{{ t('projects.bindRepo') }}</span>
            </label>
            <span class="field-hint">{{ repoForm.bindRepo ? t('projects.bindRepoOnHint') : t('projects.bindRepoOffHint') }}</span>
          </div>

          <div v-if="repoForm.bindRepo" class="field">
            <label class="field-label" for="repo-url-input">{{ t('projects.fieldRepo') }}</label>
            <input
              id="repo-url-input"
              v-model="repoForm.repoUrl"
              class="field-input field-input--mono"
              :class="{ 'field-input--error': repoErrors.repoUrl }"
              type="url"
              :placeholder="t('projects.repoUrlPlaceholder')"
              autocomplete="off"
              :disabled="repoSubmitting"
              :aria-invalid="repoErrors.repoUrl ? 'true' : undefined"
              :aria-describedby="repoErrors.repoUrl ? 'repo-url-err' : undefined"
              @input="repoErrors.repoUrl = ''; repoTestState = 'idle'; repoRemoteBranches = []; repoRemoteTags = []"
            />
            <span v-if="repoErrors.repoUrl" id="repo-url-err" class="field-error" role="alert">{{ repoErrors.repoUrl }}</span>
          </div>

          <div v-if="repoForm.bindRepo" class="field">
            <label class="field-label" for="repo-cred-input">
              {{ t('projects.credential') }}
              <span class="field-hint-inline">{{ t('projects.fieldCredHint') }}</span>
            </label>
            <CredentialSelect
              input-id="repo-cred-input"
              v-model="repoForm.credentialId"
              :credentials="gitCredentials"
              :loading="credentialsLoading"
              :disabled="repoSubmitting"
              :has-error="Boolean(repoErrors.credentialId)"
              :placeholder="t('projects.credSelect')"
              :loading-label="t('projects.credLoading')"
              :empty-label="t('projects.credSelect')"
              @change="repoErrors.credentialId = ''; repoTestState = 'idle'"
            />
            <span v-if="repoErrors.credentialId" class="field-error" role="alert">{{ repoErrors.credentialId }}</span>
          </div>

          <div v-if="repoForm.bindRepo" class="field">
            <label class="field-label" for="repo-branch-input">
              {{ t('projects.fieldDefaultBranch') }}
              <span class="field-hint-inline">{{ t('projects.fieldDefaultBranchHint') }}</span>
            </label>
            <RefPicker
              input-id="repo-branch-input"
              v-model="repoForm.defaultBranch"
              :branches="repoRemoteBranches"
              :tags="repoRemoteTags"
              :disabled="repoSubmitting"
              placeholder="main"
              :branches-label="t('projects.refGroupBranches')"
              :tags-label="t('projects.refGroupTags')"
            />
          </div>

          <div v-if="repoForm.bindRepo" class="test-clone-row">
            <button
              type="button"
              class="btn-ghost"
              :disabled="repoSubmitting || repoTestState === 'testing'"
              :aria-busy="repoTestState === 'testing'"
              @click="handleRepoTestClone"
            >
              <span v-if="repoTestState === 'testing'" class="spinner spinner--dim" aria-hidden="true" />
              {{ repoTestState === 'testing' ? t('projects.testing') : t('projects.testConnection') }}
            </button>
            <div
              v-if="repoTestState === 'ok'"
              class="test-result test-result--ok"
              role="status"
              aria-live="polite"
            >
              <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" aria-hidden="true">
                <path d="M20 6 9 17l-5-5"/>
              </svg>
              {{ t('projects.testOk') }}
              <span v-if="repoRemoteBranches.length" class="test-branch mono">{{ t('projects.fieldDefaultBranchListed', { n: repoRemoteBranches.length + repoRemoteTags.length }) }}</span>
            </div>
            <div
              v-else-if="repoTestState === 'error'"
              class="test-result test-result--error"
              role="alert"
              aria-live="assertive"
            >
              <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
                <circle cx="12" cy="12" r="9"/><path d="M12 8v4M12 16h.01"/>
              </svg>
              {{ repoTestError }}
            </div>
          </div>

          <div class="modal-footer">
            <button
              type="button"
              class="btn-secondary"
              :disabled="repoSubmitting"
              @click="closeRepoModal"
            >{{ t('projects.cancel') }}</button>
            <button
              type="submit"
              class="btn-primary"
              :disabled="repoSubmitting"
              :aria-busy="repoSubmitting"
            >
              <span v-if="repoSubmitting" class="spinner" aria-hidden="true" />
              {{ repoSubmitting ? t('projects.saving') : t('projects.save') }}
            </button>
          </div>
        </form>
      </div>
    </div>
  </Teleport>

  <!-- ═══════════════════════════════════════════════════════════════════════
       Rename modal
  ════════════════════════════════════════════════════════════════════════ -->
  <Teleport to="body">
    <div
      v-if="renameModalOpen && renamingProject"
      class="modal-scrim"
      role="dialog"
      :aria-label="t('projects.renameTitle')"
      aria-modal="true"
      @keydown.esc="closeRenameModal"
      @click.self="closeRenameModal"
    >
      <div class="modal modal--sm">
        <div class="modal-head">
          <div class="modal-icon" aria-hidden="true">
            <svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7">
              <path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/>
              <path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/>
            </svg>
          </div>
          <div>
            <h3 class="modal-title">{{ t('projects.renameTitle') }}</h3>
            <p class="modal-sub">{{ t('projects.renameSub') }}</p>
          </div>
          <button
            class="modal-close"
            :aria-label="t('projects.closeDialog')"
            :disabled="renameSubmitting"
            @click="closeRenameModal"
          >
            <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <path d="M18 6 6 18M6 6l12 12"/>
            </svg>
          </button>
        </div>

        <div
          v-if="renameBanner"
          class="banner banner--error modal-banner"
          role="alert"
        >
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
            <circle cx="12" cy="12" r="9"/><path d="M12 8v4M12 16h.01"/>
          </svg>
          {{ renameBanner }}
        </div>

        <form
          class="modal-form"
          novalidate
          @submit.prevent="handleRenameSubmit"
        >
          <div class="field">
            <label class="field-label" for="rename-input">{{ t('projects.fieldName') }}</label>
            <input
              id="rename-input"
              v-model="renameValue"
              class="field-input"
              :class="{ 'field-input--error': renameError }"
              type="text"
              autocomplete="off"
              :disabled="renameSubmitting"
              :aria-invalid="renameError ? 'true' : undefined"
              :aria-describedby="renameError ? 'rename-err' : undefined"
              @input="renameError = ''"
            />
            <span v-if="renameError" id="rename-err" class="field-error" role="alert">{{ renameError }}</span>
          </div>

          <div class="modal-footer">
            <button
              type="button"
              class="btn-secondary"
              :disabled="renameSubmitting"
              @click="closeRenameModal"
            >{{ t('projects.cancel') }}</button>
            <button
              type="submit"
              class="btn-primary"
              :disabled="renameSubmitting"
              :aria-busy="renameSubmitting"
            >
              <span v-if="renameSubmitting" class="spinner" aria-hidden="true" />
              {{ renameSubmitting ? t('projects.saving') : t('projects.save') }}
            </button>
          </div>
        </form>
      </div>
    </div>
  </Teleport>

  <!-- 归组弹窗:改的是「谁能看、谁能操作」,所以候选只列我有管理权的组 -->
  <Teleport to="body">
    <div
      v-if="groupModalOpen && groupingProject"
      class="modal-scrim"
      role="dialog"
      :aria-label="t('groups.reassignTitle')"
      aria-modal="true"
      @keydown.esc="closeGroupModal"
      @click.self="closeGroupModal"
    >
      <div class="modal modal--sm">
        <div class="modal-head">
          <div class="modal-icon" aria-hidden="true">
            <svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7">
              <path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>
            </svg>
          </div>
          <div>
            <h3 class="modal-title">{{ t('groups.reassignTitle') }}</h3>
            <p class="modal-sub">{{ t('groups.reassignSub', { name: groupingProject.name }) }}</p>
          </div>
          <button
            class="modal-close"
            :aria-label="t('projects.closeDialog')"
            :disabled="groupSubmitting"
            @click="closeGroupModal"
          >
            <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <path d="M18 6 6 18M6 6l12 12"/>
            </svg>
          </button>
        </div>

        <div v-if="groupBanner" class="banner banner--error modal-banner" role="alert">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
            <circle cx="12" cy="12" r="9"/><path d="M12 8v4M12 16h.01"/>
          </svg>
          {{ groupBanner }}
        </div>

        <form class="modal-form" novalidate @submit.prevent="handleGroupSubmit">
          <div class="field">
            <label class="field-label" for="group-select">{{ t('groups.fieldGroup') }}</label>
            <select id="group-select" v-model="groupValue" class="field-input" :disabled="groupSubmitting">
              <option value="">{{ t('groups.ungrouped') }}</option>
              <option v-for="g in groupOptions" :key="g.id" :value="g.id">
                {{ g.name }} · {{ g.visibility === 'public' ? t('groups.visibilityPublic') : t('groups.visibilityPrivate') }}
              </option>
            </select>
            <span class="field-hint">{{ t('groups.fieldGroupHint') }}</span>
          </div>

          <div class="modal-footer">
            <button
              type="button"
              class="btn-secondary"
              :disabled="groupSubmitting"
              @click="closeGroupModal"
            >{{ t('projects.cancel') }}</button>
            <button
              type="submit"
              class="btn-primary"
              :disabled="groupSubmitting"
              :aria-busy="groupSubmitting"
            >
              <span v-if="groupSubmitting" class="spinner" aria-hidden="true" />
              {{ groupSubmitting ? t('projects.saving') : t('projects.save') }}
            </button>
          </div>
        </form>
      </div>
    </div>
  </Teleport>

  <!-- ═══════════════════════════════════════════════════════════════════════
       Delete confirm modal
  ════════════════════════════════════════════════════════════════════════ -->
  <Teleport to="body">
    <div
      v-if="deleteModalOpen && deletingProject"
      class="modal-scrim"
      role="dialog"
      :aria-label="t('projects.deleteDialogAria')"
      aria-modal="true"
      @keydown.esc="closeDeleteModal"
      @click.self="closeDeleteModal"
    >
      <div class="modal modal--sm">
        <div class="modal-head">
          <div class="modal-icon modal-icon--danger" aria-hidden="true">
            <svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7">
              <path d="M3 6h18M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"/>
              <path d="M8 6V4a1 1 0 0 1 1-1h6a1 1 0 0 1 1 1v2"/>
            </svg>
          </div>
          <div>
            <h3 class="modal-title">{{ t('projects.deleteTitle') }}</h3>
            <p class="modal-sub">{{ t('projects.deleteSub') }}</p>
          </div>
          <button
            class="modal-close"
            :aria-label="t('projects.closeDialog')"
            :disabled="deleteSubmitting"
            @click="closeDeleteModal"
          >
            <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <path d="M18 6 6 18M6 6l12 12"/>
            </svg>
          </button>
        </div>

        <div class="modal-body">
          <p class="delete-confirm-text">
            {{ t('projects.deleteConfirmPre') }}
            <strong class="delete-name">{{ deletingProject.name }}</strong>
            {{ t('projects.deleteConfirmPost') }}
          </p>

          <div
            v-if="deleteBanner"
            class="banner banner--error modal-banner-body"
            role="alert"
          >
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
              <circle cx="12" cy="12" r="9"/><path d="M12 8v4M12 16h.01"/>
            </svg>
            {{ deleteBanner }}
          </div>
        </div>

        <div class="modal-footer modal-footer--body">
          <button
            type="button"
            class="btn-secondary"
            :disabled="deleteSubmitting"
            @click="closeDeleteModal"
          >{{ t('projects.cancel') }}</button>
          <button
            type="button"
            class="btn-danger"
            :disabled="deleteSubmitting"
            :aria-busy="deleteSubmitting"
            @click="confirmDelete"
          >
            <span v-if="deleteSubmitting" class="spinner spinner--red" aria-hidden="true" />
            {{ deleteSubmitting ? t('projects.deleting') : t('projects.confirmDelete') }}
          </button>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
/* ─── root layout ──────────────────────────────────────────────────────────── */
.projects-root {
  display: flex;
  flex-direction: column;
  gap: var(--card-gap);
}

/* ─── page header ──────────────────────────────────────────────────────────── */
.page-header {
  display: flex;
  align-items: flex-start;
  gap: 16px;
}

.page-header-text {
  flex: 1;
}

.page-title {
  font-size: 1.5rem;
  font-weight: 700;
  letter-spacing: -0.02em;
  color: var(--color-text);
  line-height: 1.2;
}

.page-sub {
  font-size: 0.82rem;
  color: var(--color-faint);
  margin-top: 5px;
  line-height: 1.5;
}

/* ─── toolbar (search + filter) ────────────────────────────────────────────── */
.toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}

.search-wrap {
  position: relative;
  flex: 1;
  min-width: 200px;
  max-width: 380px;
}

.search-icon {
  position: absolute;
  left: 12px;
  top: 50%;
  transform: translateY(-50%);
  color: var(--color-faint);
  pointer-events: none;
}

.search-input {
  width: 100%;
  height: 36px;
  background: var(--color-inset);
  border: 1px solid var(--color-border);
  border-radius: var(--rounded);
  padding: 0 12px 0 34px;
  color: var(--color-text);
  font-family: var(--font-sans);
  font-size: 0.83rem;
  transition: border-color var(--duration-fast), box-shadow var(--duration-fast);
}

.search-input::placeholder {
  color: var(--color-faint);
}

.search-input:focus {
  outline: none;
  border-color: var(--color-primary);
  box-shadow: 0 0 0 3px var(--color-primary-soft);
}

/* Clear the native "x" button on search inputs */
.search-input::-webkit-search-cancel-button {
  -webkit-appearance: none;
}

.group-filter {
  width: 186px;
}

/* 与搜索框同高同字号:工具栏里两个控件错位会比多一行还难看 */
.group-filter .field-select {
  height: 36px;
  font-size: 0.83rem;
}

.filter-tabs {
  display: flex;
  gap: 4px;
  background: var(--color-inset);
  border-radius: var(--rounded);
  padding: 3px;
}

.filter-tab {
  height: 30px;
  padding: 0 11px;
  border: none;
  background: transparent;
  color: var(--color-dim);
  font-family: var(--font-sans);
  font-size: 0.78rem;
  font-weight: 500;
  border-radius: var(--rounded-md);
  cursor: pointer;
  white-space: nowrap;
  transition: color var(--duration-fast), background-color var(--duration-fast), box-shadow var(--duration-fast);
}

.filter-tab:hover {
  color: var(--color-text);
  background: oklch(100% 0 0 / 0.04);
}

.filter-tab--active {
  background: var(--color-card);
  color: var(--color-text);
  box-shadow: var(--shadow);
}

.filter-tab:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: 1px;
}

/* ─── result count ─────────────────────────────────────────────────────────── */
.result-count {
  font-size: 0.78rem;
  color: var(--color-faint);
  margin-top: -4px;
}

/* ─── project grid ──────────────────────────────────────────────────────────── */
.project-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: var(--card-gap);
  list-style: none;
}

.mono {
  font-family: var(--font-mono);
}

/* ─── 分组视图:折叠的组头 + 展开后的卡片网格 ─────────────────────────────────── */
/* 视图切换按钮靠最右:它管的是「这一页怎么看」,和左边的筛选项不是一类。 */
.view-toggle {
  margin-left: auto;
}

.view-toggle-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.group-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.group-section {
  /* 凹下去一档:卡片本身是 --color-card,同色的容器会把卡「吞」进背景里,分组视图就没了层次。 */
  background: var(--color-inset);
  border: 1px solid var(--color-border);
  border-radius: var(--rounded-card);
  padding: 2px 16px;
}

.group-head {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 12px 2px;
  appearance: none;
  background: none;
  border: none;
  color: var(--color-text);
  font-family: var(--font-sans);
  text-align: left;
  cursor: pointer;
}

.group-head:hover .group-name {
  color: var(--color-primary);
}

.group-head:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: -2px;
  border-radius: var(--rounded-md);
}

.group-chevron {
  color: var(--color-faint);
  flex-shrink: 0;
  transition: transform var(--duration-fast);
}

.group-chevron--open {
  transform: rotate(90deg);
}

@media (prefers-reduced-motion: reduce) {
  .group-chevron {
    transition: none;
  }
}

.group-name {
  font-size: 0.9rem;
  font-weight: 600;
  letter-spacing: -0.01em;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  /* 不给 min-width:0,flex 项不肯缩到内容宽度以下,长组名会把整行顶宽而不是省略号。 */
  min-width: 0;
}

.group-visibility {
  padding: 1px 7px;
  border-radius: var(--rounded-md);
  background: var(--color-card);
  border: 1px solid var(--color-border);
  color: var(--color-dim);
  font-size: var(--text-micro);
  font-weight: 600;
  white-space: nowrap;
  flex-shrink: 0;
}

.group-visibility--private {
  border-color: var(--color-amber-line);
  background: var(--color-amber-soft);
  color: var(--color-amber);
}

.group-count {
  font-size: 0.76rem;
  color: var(--color-faint);
  white-space: nowrap;
  flex-shrink: 0;
}

.group-owner {
  margin-left: auto;
  font-size: 0.74rem;
  color: var(--color-faint);
  white-space: nowrap;
  flex-shrink: 0;
}

.group-projects {
  padding: 4px 0 16px;
}

.group-empty {
  padding: 0 0 14px 22px;
  font-size: 0.78rem;
  color: var(--color-faint);
  font-style: italic;
}

/* ─── skeleton ──────────────────────────────────────────────────────────────── */
.skel-card {
  background: var(--color-card);
  border: 1px solid var(--color-border);
  border-radius: var(--rounded-card);
  padding: 18px 20px;
  display: flex;
  flex-direction: column;
  gap: 12px;
  min-height: 200px;
}

.skel {
  display: block;
  background: linear-gradient(
    90deg,
    var(--color-inset) 0%,
    oklch(100% 0 0 / 0.06) 50%,
    var(--color-inset) 100%
  );
  background-size: 200% 100%;
  border-radius: var(--rounded-md);
  animation: shimmer 1.4s ease-in-out infinite;
}

@keyframes shimmer {
  0%   { background-position: 200% center; }
  100% { background-position: -200% center; }
}

@media (prefers-reduced-motion: reduce) {
  .skel { animation: none; background: var(--color-inset); }
}

.skel--name { height: 18px; width: 55%; }
.skel--url  { height: 12px; width: 80%; }
.skel--tag  { height: 12px; width: 40%; }
.skel--meta { height: 12px; width: 65%; }

/* ─── empty state ───────────────────────────────────────────────────────────── */
.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 10px;
  padding: 72px 32px;
  text-align: center;
  background: var(--color-card);
  border: 1.5px dashed var(--color-border-strong);
  border-radius: var(--rounded-card);
}

.empty-icon {
  width: 56px;
  height: 56px;
  border-radius: var(--rounded-xl);
  background: var(--color-inset);
  border: 1.5px dashed var(--color-border-strong);
  display: grid;
  place-items: center;
  color: var(--color-dim);
  margin-bottom: 4px;
}

.empty-label {
  font-size: 1rem;
  font-weight: 600;
  color: var(--color-text);
}

.empty-hint {
  font-size: 0.82rem;
  color: var(--color-faint);
  max-width: 44ch;
  line-height: 1.6;
  margin-bottom: 6px;
}

/* ─── error banner ──────────────────────────────────────────────────────────── */
.banner {
  display: flex;
  align-items: flex-start;
  gap: 9px;
  padding: 11px 14px;
  border-radius: var(--rounded);
  font-size: 0.83rem;
  line-height: 1.5;
}

.banner--error {
  background: var(--color-red-soft);
  border: 1px solid var(--color-red-line);
  color: var(--color-red);
}

.banner-retry {
  margin-left: auto;
  flex-shrink: 0;
  background: none;
  border: none;
  color: var(--color-red);
  font-size: 0.83rem;
  font-weight: 600;
  cursor: pointer;
  padding: 0;
  text-decoration: underline;
  text-underline-offset: 2px;
}

.banner-retry:hover {
  opacity: 0.8;
}

/* ─── buttons ───────────────────────────────────────────────────────────────── */
.btn-primary {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 34px;
  padding: 0 15px;
  border: none;
  background: var(--color-primary);
  color: #fff;
  font-family: var(--font-sans);
  font-size: 0.83rem;
  font-weight: 600;
  border-radius: var(--rounded);
  cursor: pointer;
  box-shadow: 0 5px 16px var(--color-primary-soft);
  transition: background-color var(--duration-fast), transform var(--duration-fast), box-shadow var(--duration-fast);
  white-space: nowrap;
  flex-shrink: 0;
}

.btn-primary:hover:not(:disabled) {
  background: var(--color-primary-press);
  transform: translateY(-1px);
}

.btn-primary:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: 3px;
}

.btn-primary:disabled {
  opacity: 0.45;
  cursor: not-allowed;
  transform: none;
  box-shadow: none;
}

.btn-secondary {
  display: inline-flex;
  align-items: center;
  height: 34px;
  padding: 0 15px;
  background: var(--color-card-2);
  color: var(--color-text);
  border: 1px solid var(--color-border-strong);
  font-family: var(--font-sans);
  font-size: 0.83rem;
  font-weight: 500;
  border-radius: var(--rounded);
  cursor: pointer;
  transition: border-color var(--duration-fast), background-color var(--duration-fast);
}

.btn-secondary:hover:not(:disabled) {
  border-color: var(--color-faint);
}

.btn-secondary:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: 2px;
}

.btn-secondary:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}

.btn-ghost {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 32px;
  padding: 0 12px;
  background: transparent;
  color: var(--color-dim);
  border: 1px solid var(--color-border-strong);
  font-family: var(--font-sans);
  font-size: 0.8rem;
  font-weight: 500;
  border-radius: var(--rounded);
  cursor: pointer;
  transition: color var(--duration-fast), border-color var(--duration-fast), background-color var(--duration-fast);
}

.btn-ghost:hover:not(:disabled) {
  color: var(--color-text);
  border-color: var(--color-faint);
  background: var(--color-inset);
}

.btn-ghost:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: 2px;
}

.btn-ghost:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}

.btn-danger {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 34px;
  padding: 0 15px;
  background: var(--color-red-soft);
  color: var(--color-red);
  border: 1px solid var(--color-red-line);
  font-family: var(--font-sans);
  font-size: 0.83rem;
  font-weight: 600;
  border-radius: var(--rounded);
  cursor: pointer;
  transition: background-color var(--duration-fast), transform var(--duration-fast);
}

.btn-danger:hover:not(:disabled) {
  background: oklch(62% 0.18 22 / 0.25);
  transform: translateY(-1px);
}

.btn-danger:focus-visible {
  outline: 2px solid var(--color-red);
  outline-offset: 2px;
}

.btn-danger:disabled {
  opacity: 0.5;
  cursor: not-allowed;
  transform: none;
}

.btn-run {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 34px;
  padding: 0 15px;
  border: none;
  background: var(--color-primary);
  color: #fff;
  font-family: var(--font-sans);
  font-size: 0.83rem;
  font-weight: 600;
  border-radius: var(--rounded);
  cursor: pointer;
  box-shadow: 0 5px 16px var(--color-primary-soft);
  transition: background-color var(--duration-fast), transform var(--duration-fast), box-shadow var(--duration-fast);
  white-space: nowrap;
}

.btn-run:hover:not(:disabled) {
  background: var(--color-primary-press);
  transform: translateY(-1px);
}

.btn-run:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: 3px;
}

.btn-run:disabled {
  opacity: 0.45;
  cursor: not-allowed;
  transform: none;
  box-shadow: none;
}

/* ─── modal ─────────────────────────────────────────────────────────────────── */
.modal-scrim {
  position: fixed;
  inset: 0;
  background: oklch(0% 0 0 / 0.62);
  display: grid;
  place-items: center;
  z-index: 100;
  padding: 24px;
  animation: scrim-in var(--duration-fast) ease both;
}

@keyframes scrim-in {
  from { opacity: 0; }
  to   { opacity: 1; }
}

.modal {
  width: 100%;
  max-width: 520px;
  max-height: calc(100dvh - 48px);
  background: var(--color-card);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--rounded-xl);
  box-shadow: var(--shadow-modal);
  overflow: hidden;
  display: flex;
  flex-direction: column;
  animation: modal-in 0.35s var(--ease-out-expo) both;
}

.modal--sm {
  max-width: 420px;
}

@keyframes modal-in {
  from { opacity: 0; transform: translateY(14px) scale(0.98); }
  to   { opacity: 1; transform: none; }
}

@media (prefers-reduced-motion: reduce) {
  .modal-scrim { animation: none; }
  .modal       { animation: none; }
}

.modal-head {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  padding: 20px 20px 16px;
  border-bottom: 1px solid var(--color-border);
}

.modal-icon {
  width: 36px;
  height: 36px;
  border-radius: var(--rounded-lg);
  background: var(--color-primary-soft);
  color: var(--color-primary);
  display: grid;
  place-items: center;
  flex-shrink: 0;
}

.modal-icon--danger {
  background: var(--color-red-soft);
  color: var(--color-red);
}

.modal-icon--run {
  background: var(--color-primary-soft);
  color: var(--color-primary);
}

.modal-title {
  font-size: 1rem;
  font-weight: 600;
  color: var(--color-text);
  margin-top: 2px;
  letter-spacing: -0.01em;
}

.modal-sub {
  font-size: 0.78rem;
  color: var(--color-faint);
  margin-top: 3px;
  line-height: 1.4;
}

.modal-close {
  margin-left: auto;
  flex-shrink: 0;
  width: 30px;
  height: 30px;
  border-radius: var(--rounded-md);
  border: none;
  background: transparent;
  color: var(--color-faint);
  cursor: pointer;
  display: grid;
  place-items: center;
  transition: color var(--duration-fast), background-color var(--duration-fast);
}

.modal-close:hover {
  color: var(--color-text);
  background: var(--color-inset);
}

.modal-close:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: 2px;
}

.modal-close:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.modal-banner {
  margin: 16px 20px 0;
  border-radius: var(--rounded);
}

.modal-form {
  padding: 20px;
  display: flex;
  flex-direction: column;
  gap: 16px;
  min-height: 0;
  overflow-y: auto;
}

.modal-body {
  padding: 20px;
}

.modal-banner-body {
  margin-top: 14px;
  border-radius: var(--rounded);
}

.modal-footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding-top: 4px;
}

.modal-footer--body {
  padding: 0 20px 20px;
  padding-top: 0;
}

/* ─── form fields ────────────────────────────────────────────────────────────── */
.field {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.field-label {
  font-size: 0.78rem;
  font-weight: 500;
  color: var(--color-dim);
}

.field-hint-inline {
  font-weight: 400;
  color: var(--color-faint);
}

.field-input {
  width: 100%;
  height: 38px;
  background: var(--color-inset);
  border: 1px solid var(--color-border);
  border-radius: var(--rounded);
  padding: 0 12px;
  color: var(--color-text);
  font-family: var(--font-sans);
  font-size: 0.86rem;
  transition: border-color var(--duration-fast), box-shadow var(--duration-fast);
}

.field-input::placeholder {
  color: var(--color-faint);
}

.field-input:focus {
  outline: none;
  border-color: var(--color-primary);
  box-shadow: 0 0 0 3px var(--color-primary-soft);
}

.field-input--error {
  border-color: var(--color-red);
}

.field-input--error:focus {
  border-color: var(--color-red);
  box-shadow: 0 0 0 3px var(--color-red-soft);
}

.field-input:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.field-input--mono {
  font-family: var(--font-mono);
  font-size: 0.8rem;
}

/* Select / dropdown */
.select-wrap {
  position: relative;
}

.field-select {
  width: 100%;
  height: 38px;
  background: var(--color-inset);
  border: 1px solid var(--color-border);
  border-radius: var(--rounded);
  padding: 0 36px 0 12px;
  color: var(--color-text);
  font-family: var(--font-sans);
  font-size: 0.86rem;
  appearance: none;
  cursor: pointer;
  transition: border-color var(--duration-fast), box-shadow var(--duration-fast);
}

.field-select:focus {
  outline: none;
  border-color: var(--color-primary);
  box-shadow: 0 0 0 3px var(--color-primary-soft);
}

.field-select.field-input--error {
  border-color: var(--color-red);
}

.field-select.field-input--error:focus {
  box-shadow: 0 0 0 3px var(--color-red-soft);
}

.field-select:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

/* Style select options for dark background */
.field-select option {
  background: var(--color-card-2);
  color: var(--color-text);
}

.select-arrow {
  position: absolute;
  right: 11px;
  top: 50%;
  transform: translateY(-50%);
  color: var(--color-faint);
  pointer-events: none;
}

.field-error {
  font-size: 0.76rem;
  color: var(--color-red);
  line-height: 1.4;
}

.field-hint {
  font-size: 0.74rem;
  color: var(--color-faint);
  line-height: 1.4;
}

.field-check {
  display: flex;
  align-items: center;
  gap: 7px;
  font-size: 0.8rem;
  color: var(--color-text);
  cursor: pointer;
}

.link {
  color: var(--color-primary);
  text-decoration: underline;
  text-underline-offset: 2px;
}

.link:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: 2px;
  border-radius: 2px;
}

/* ─── test clone row ─────────────────────────────────────────────────────────── */
.test-clone-row {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  flex-wrap: wrap;
  margin-top: -4px;
}

.credential-create-toggle {
  margin-top: 8px;
}

.inline-credential-panel {
  display: grid;
  gap: 10px;
  margin-top: -2px;
  padding: 14px;
  border: 1px solid var(--color-border-strong);
  border-radius: var(--rounded);
  background: var(--color-inset);
}

.inline-credential-title {
  font-size: 0.82rem;
  font-weight: 650;
  color: var(--color-text);
}

.inline-credential-types {
  display: inline-flex;
  width: fit-content;
  max-width: 100%;
  padding: 3px;
  gap: 2px;
  border: 1px solid var(--color-border);
  border-radius: var(--rounded);
  background: var(--color-card-2);
}

.inline-credential-type {
  min-height: 30px;
  padding: 5px 10px;
  border: 0;
  border-radius: calc(var(--rounded) - 2px);
  background: transparent;
  color: var(--color-dim);
  font: inherit;
  font-size: 0.76rem;
  cursor: pointer;
}

.inline-credential-type:hover:not(:disabled) {
  color: var(--color-text);
}

.inline-credential-type--active {
  background: var(--color-primary-soft);
  color: var(--color-primary);
  font-weight: 650;
}

.inline-credential-type:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.inline-credential-submit {
  justify-self: start;
}

.test-result {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  font-size: 0.8rem;
  line-height: 1.4;
  flex: 1;
  padding-top: 6px;
}

.test-result--ok {
  color: var(--color-green);
}

.test-result--error {
  color: var(--color-red);
}

.test-branch {
  color: var(--color-dim);
  font-size: 0.76rem;
}

/* ─── delete confirm ─────────────────────────────────────────────────────────── */
.delete-confirm-text {
  font-size: 0.86rem;
  color: var(--color-dim);
  line-height: 1.6;
}

.delete-name {
  color: var(--color-text);
  font-weight: 600;
}

/* ─── spinner ────────────────────────────────────────────────────────────────── */
.spinner {
  display: inline-block;
  width: 13px;
  height: 13px;
  border: 2px solid rgba(255, 255, 255, 0.35);
  border-top-color: #fff;
  border-radius: var(--rounded-full);
  animation: spin 0.7s linear infinite;
  flex-shrink: 0;
}

.spinner--dim {
  border-color: oklch(72% 0.008 270 / 0.3);
  border-top-color: var(--color-dim);
}

.spinner--red {
  border-color: oklch(69% 0.17 22 / 0.3);
  border-top-color: var(--color-red);
}

@keyframes spin {
  to { transform: rotate(360deg); }
}

@media (prefers-reduced-motion: reduce) {
  .spinner { animation: none; border-top-color: currentColor; }
}
</style>
