<script setup lang="ts">
/**
 * MCP 端点 —— 机器客户端(MCP、脚本)用的凭据发放台。
 *
 * 一把端点 = 一条地址(`/mcp/<id>`)+ 一把令牌 + 两把尺(能碰哪些工具、能看哪些分组)。
 * 库里存的仍是令牌摘要,所以 REST 路径沿用 /api/admin/api-tokens、页面只把「端点」这层概念
 * 摆到界面上:每行显示自己的地址、可复制,范围还能在不撤销的前提下改。
 *
 * 这一页有「列 / 新增 / 改范围 / 撤销」四件事。明文只在新增那一次响应里出现一次,
 * 之后任何端点都读不回来,所以签发成功后弹窗不能直接关 —— 它得先把明文交付出去
 * (复制,或按客户端给的现成 MCP 配置),交付完了才允许收。忘了存 = 只能撤销重发。
 *
 * 归属人默认「我自己」:请求里省略 userId,由后端落到当前会话账号(会话载荷里没有 userId,
 * 前端拿不到「我自己的 id」,只有用户名)。下拉刻意不再列出自己那一行,免得同一件事有两条
 * 通往不同请求体的路。
 *
 * 两把尺:能碰哪些 MCP 工具(allowedTools)、能看哪些分组(groupScope)。
 * 两把尺都只做交集、只收窄不放大,且**不勾 = 不限**(与后端 apitoken 的口径一致);
 * 所以界面上的「全部工具」发出去的是不带这个键,不是空数组 —— 空数组会被后端拒签。
 * 但**改范围**时线格式相反:PATCH 里带空数组 = 把这把尺解开回不限,不带键 = 不动它。
 * 新增与编辑共用同一段勾选 UI,差别只在这一步怎么发(见 handleSubmit)。
 */
import { computed, onMounted, ref } from 'vue'
import {
  listAPITokens,
  createAPIToken,
  updateTokenScopes,
  revokeAPIToken,
  listMCPTools,
} from '../../api/apiTokens'
import type { APIToken } from '../../api/apiTokens'
import { listGroups } from '../../api/groups'
import type { Group } from '../../api/groups'
import { listUsers } from '../../api/users'
import type { User } from '../../api/users'
import { useSessionStore } from '../../stores/session'
import { HttpError } from '../../api/http'
import { useI18n } from 'vue-i18n'
import {
  TOKEN_NAME_MAX,
  activeToolPreset,
  claudeCodeAddCommand,
  clientSnippets,
  editableGroupOptions,
  editableToolOptions,
  groupScopeNames,
  mcpEndpointURL,
  normalizeTokenName,
  ownerOptions,
  sameScope,
  tokenNameProblem,
  toggleTool,
  toggleValue,
  toolPresetNames,
} from '../../lib/apiTokens'
import type { MCPToolDef, SnippetClient, ToolPresetKey } from '../../lib/apiTokens'

const { t, locale } = useI18n()
const sessionStore = useSessionStore()

type LoadState = 'idle' | 'loading' | 'error'

const loadState = ref<LoadState>('idle')
const loadError = ref('')
const tokens = ref<APIToken[]>([])
const users = ref<User[]>([])
/** MCP 工具名册:勾工具的清单与后端签发校验读同一份,页面不会展出后端不认的名字。 */
const tools = ref<MCPToolDef[]>([])
const groups = ref<Group[]>([])

const myUsername = computed(() => sessionStore.user?.username ?? '')
const ownerOpts = computed(() => ownerOptions(users.value, myUsername.value))
const ownerSelfLabel = computed(() => `${t('settingsAPITokens.ownerSelf')} · @${myUsername.value}`)
const groupOpts = computed(() =>
  groups.value.map((g) => ({ id: g.id, name: g.name, projectCount: g.projectCount })),
)
/** 勾选行上的「N 个项目」附注:候选里查不到(组已删)时按 0 显示,那一行同时会挂「已失效」标签。 */
function groupCountOf(id: string): number {
  return groups.value.find((g) => g.id === id)?.projectCount ?? 0
}

function errText(err: unknown, keys: { conn: string; status: string; retry: string }): string {
  if (err instanceof HttpError) {
    if (err.status === 0) return t(keys.conn)
    // 后端的 message 已是人读文案(「该用户已被禁用,无法为其签发令牌」这类判定结论),
    // 且绝不带令牌明文,所以直接回显比前端再映射一遍更准。
    return err.apiError?.message ?? t(keys.status, { status: err.status })
  }
  return t(keys.retry)
}

const loadKeys = {
  conn: 'settingsAPITokens.errLoadConn',
  status: 'settingsAPITokens.errLoadStatus',
  retry: 'settingsAPITokens.errLoadRetry',
}
const issueKeys = {
  conn: 'settingsAPITokens.errConnRetry',
  status: 'settingsAPITokens.errIssueStatus',
  retry: 'settingsAPITokens.errIssueRetry',
}
const editKeys = {
  conn: 'settingsAPITokens.errConnRetry',
  status: 'settingsAPITokens.errEditStatus',
  retry: 'settingsAPITokens.errEditRetry',
}
const revokeKeys = {
  conn: 'settingsAPITokens.errConnRetry',
  status: 'settingsAPITokens.errRevokeStatus',
  retry: 'settingsAPITokens.errRevokeRetry',
}

async function loadAll(): Promise<void> {
  loadState.value = 'loading'
  loadError.value = ''
  try {
    const [list, us, cat, gs] = await Promise.all([
      listAPITokens(),
      listUsers(),
      listMCPTools(),
      listGroups(),
    ])
    tokens.value = list
    users.value = us
    tools.value = cat
    groups.value = gs
    loadState.value = 'idle'
  } catch (err) {
    loadError.value = errText(err, loadKeys)
    loadState.value = 'error'
  }
}

onMounted(loadAll)

function formatTime(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString(locale.value, { hour12: false })
}

/** 列表行的两把尺摘要:不限时说「不限」,收窄时给数量,完整名单放 title —— 一行放不下两个列表。 */
function toolScopeLabel(tk: APIToken): string {
  return tk.allowedTools.length
    ? t('settingsAPITokens.scopeToolsSome', { n: tk.allowedTools.length })
    : t('settingsAPITokens.scopeToolsAll')
}
function groupScopeLabel(tk: APIToken): string {
  // 自己 join 而不是把数组交给 vue-i18n:插值到占位符里的数组会被序列化成 JSON 多行,列表行会撑成一坨。
  return tk.groupScope.length
    ? t('settingsAPITokens.scopeGroupsSome', { names: groupScopeNames(tk.groupScope, groupOpts.value).join('、') })
    : t('settingsAPITokens.scopeGroupsAll')
}
function scopeTitle(list: string[], names: string[]): string {
  return names.join('、') || list.join('、')
}
/** 工具名换中文标题;名册里没有的(工具已改名)留原名,不静默吞掉。 */
function toolTitles(names: string[]): string[] {
  const index = new Map(tools.value.map((x) => [x.name, x.title]))
  return names.map((n) => index.get(n) ?? n)
}

// ─── 新增 / 改范围(同一个弹窗的两种模式)────────────────────────────────────

/**
 * 弹窗两步(只有新增会走到第二步):填表 → 交付明文。第二步没交付完,第一步关不掉(见文件头)。
 * mode 决定发哪一种请求:create 走 POST(明文只有一次),edit 走 PATCH(只发改过的那把尺)。
 */
const modalOpen = ref(false)
const mode = ref<'create' | 'edit'>('create')
const step = ref<'form' | 'issued'>('form')
const formSubmitting = ref(false)
const formBanner = ref('')
const form = ref({ name: '', ownerId: '', allowedTools: [] as string[], groupScope: [] as string[] })
const formNameError = ref('')
/** 编辑模式的原始快照:PATCH「不带键 = 不动」,判「哪一把真被改过」要靠它,不能凭记忆比。 */
const editing = ref<APIToken | null>(null)

const presetKeys: ToolPresetKey[] = ['all', 'readonly', 'ops']
const activePreset = computed(() => activeToolPreset(form.value.allowedTools, tools.value))

/**
 * 勾选清单 = 名册 + 这把端点已勾但名册里没了的名字。
 * 新增模式下 selected 恒为名册子集,所以这两份清单在新增时就是名册本身,不用分叉写两套 UI。
 */
const toolOptions = computed(() => editableToolOptions(form.value.allowedTools, tools.value))
const groupOptions = computed(() => editableGroupOptions(form.value.groupScope, groupOpts.value))

function applyPreset(preset: ToolPresetKey): void {
  form.value.allowedTools = toolPresetNames(preset, tools.value)
}
function onToggleTool(name: string): void {
  form.value.allowedTools = toggleTool(tools.value, form.value.allowedTools, name)
}
function onToggleGroup(id: string): void {
  form.value.groupScope = toggleValue(groupOpts.value.map((g) => g.id), form.value.groupScope, id)
}

const issuedName = ref('')
const issuedPlain = ref('')
/** 签发响应里的元数据:交付页要把「这把被收窄到哪」再念一遍,列表行已经滚出视野了。 */
const issuedItem = ref<APIToken | null>(null)
const baseUrl = computed(() => window.location.origin)

/** 一条端点一条地址:地址里那一段就是后端主键,列表行与交付页都用同一个它拼。 */
function endpointFor(id: string): string {
  return mcpEndpointURL(baseUrl.value, id)
}
const issuedEndpoint = computed(() => endpointFor(issuedItem.value?.id ?? ''))
const snippets = computed(() =>
  clientSnippets(baseUrl.value, issuedPlain.value, issuedItem.value?.id ?? ''),
)
const activeClient = ref<SnippetClient>('cursor')
const activeSnippet = computed(
  () => snippets.value.find((s) => s.client === activeClient.value) ?? snippets.value[0],
)
/** 片段贴到哪儿那句提示的 i18n 键;labelKey 已经带了「snippetWhere.」前缀,这里只补最外层。 */
const activeWhereKey = computed(() => `settingsAPITokens.${activeSnippet.value?.labelKey ?? ''}`)
const claudeCodeCmd = computed(() =>
  claudeCodeAddCommand(baseUrl.value, issuedPlain.value, issuedItem.value?.id ?? ''),
)

/** 两把尺各自有没有被改过 —— 决定 PATCH 带不带那个键。 */
const toolsDirty = computed(
  () => !!editing.value && !sameScope(form.value.allowedTools, editing.value.allowedTools),
)
const groupsDirty = computed(
  () => !!editing.value && !sameScope(form.value.groupScope, editing.value.groupScope),
)
const scopesDirty = computed(() => toolsDirty.value || groupsDirty.value)

function openModal(): void {
  mode.value = 'create'
  step.value = 'form'
  editing.value = null
  form.value = { name: '', ownerId: '', allowedTools: [], groupScope: [] }
  formNameError.value = ''
  formBanner.value = ''
  issuedName.value = ''
  issuedPlain.value = ''
  issuedItem.value = null
  activeClient.value = 'cursor'
  copyFeedback.value = ''
  modalOpen.value = true
}

function openScopeEdit(tk: APIToken): void {
  mode.value = 'edit'
  step.value = 'form'
  editing.value = tk
  form.value = {
    name: tk.name,
    ownerId: '',
    allowedTools: [...tk.allowedTools],
    groupScope: [...tk.groupScope],
  }
  formNameError.value = ''
  formBanner.value = ''
  copyFeedback.value = ''
  modalOpen.value = true
}

function closeModal(): void {
  if (formSubmitting.value) return
  modalOpen.value = false
  editing.value = null
}

function validateForm(): boolean {
  formNameError.value = ''
  const problem = tokenNameProblem(form.value.name)
  if (problem === 'empty') {
    formNameError.value = t('settingsAPITokens.errNameEmpty')
    return false
  }
  if (problem === 'tooLong') {
    formNameError.value = t('settingsAPITokens.errNameTooLong', { max: TOKEN_NAME_MAX })
    return false
  }
  return true
}

function handleSubmit(): Promise<void> {
  return mode.value === 'create' ? handleIssue() : handleSaveScopes()
}

async function handleIssue(): Promise<void> {
  if (!validateForm()) return
  formSubmitting.value = true
  formBanner.value = ''
  try {
    const name = normalizeTokenName(form.value.name)
    // 两个范围字段都是「不勾就不带这个键」:后端把显式空数组当入参错误拒签(见 apitokens.go)。
    const created = await createAPIToken({
      name,
      ...(form.value.ownerId ? { userId: form.value.ownerId } : {}),
      ...(form.value.allowedTools.length ? { allowedTools: form.value.allowedTools } : {}),
      ...(form.value.groupScope.length ? { groupScope: form.value.groupScope } : {}),
    })
    tokens.value = [created.item, ...tokens.value]
    issuedName.value = created.item.name
    issuedPlain.value = created.plaintext
    issuedItem.value = created.item
    activeClient.value = 'cursor'
    copyFeedback.value = ''
    step.value = 'issued'
  } catch (err) {
    formBanner.value = errText(err, issueKeys)
  } finally {
    formSubmitting.value = false
  }
}

async function handleSaveScopes(): Promise<void> {
  const target = editing.value
  if (!target || !scopesDirty.value) return
  formSubmitting.value = true
  formBanner.value = ''
  try {
    // 只发改过的那一把:另一把保持库里现状。一律回传两把会把「存过一个已删分组」
    // 也原样送回后端,换回一个 400,而用户明明只动了工具。
    const updated = await updateTokenScopes(target.id, {
      ...(toolsDirty.value ? { allowedTools: form.value.allowedTools } : {}),
      ...(groupsDirty.value ? { groupScope: form.value.groupScope } : {}),
    })
    tokens.value = tokens.value.map((x) => (x.id === updated.id ? updated : x))
    modalOpen.value = false
    editing.value = null
  } catch (err) {
    formBanner.value = errText(err, editKeys)
  } finally {
    formSubmitting.value = false
  }
}

// ─── copy ────────────────────────────────────────────────────────────────────

const copyFeedback = ref('')
let copyTimer = 0

async function copyText(key: string, text: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(text)
    copyFeedback.value = key
    window.clearTimeout(copyTimer)
    copyTimer = window.setTimeout(() => (copyFeedback.value = ''), 1800)
  } catch {
    // 剪贴板不可用(非 https / 权限):静默。明文就摆在页面上,手选复制得了。
  }
}

// ─── revoke ──────────────────────────────────────────────────────────────────

const revokeOpen = ref(false)
const revoking = ref<APIToken | null>(null)
const revokeSubmitting = ref(false)
const revokeBanner = ref('')

function openRevoke(tk: APIToken): void {
  revoking.value = tk
  revokeBanner.value = ''
  revokeOpen.value = true
}

function closeRevoke(): void {
  if (revokeSubmitting.value) return
  revokeOpen.value = false
  revoking.value = null
}

async function confirmRevoke(): Promise<void> {
  if (!revoking.value) return
  revokeSubmitting.value = true
  revokeBanner.value = ''
  const id = revoking.value.id
  try {
    await revokeAPIToken(id)
    tokens.value = tokens.value.filter((x) => x.id !== id)
    revokeOpen.value = false
    revoking.value = null
  } catch (err) {
    revokeBanner.value = errText(err, revokeKeys)
  } finally {
    revokeSubmitting.value = false
  }
}
</script>

<template>
  <div class="tok-root">
    <div class="section-head">
      <div class="section-head-text">
        <h2 class="section-title">{{ t('settingsAPITokens.title') }}</h2>
        <p class="section-desc">{{ t('settingsAPITokens.desc') }}</p>
      </div>
      <button class="btn-primary" :disabled="loadState === 'loading'" @click="openModal">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" aria-hidden="true">
          <path d="M12 5v14M5 12h14" />
        </svg>
        {{ t('settingsAPITokens.issue') }}
      </button>
    </div>

    <div v-if="loadState === 'error'" class="banner banner--error" role="alert">
      <span>{{ loadError }}</span>
      <button class="banner-retry" @click="loadAll">↻ {{ t('settingsAPITokens.retry') }}</button>
    </div>

    <div class="panel">
      <div class="panel-head">
        <span>{{ t('settingsAPITokens.listTitle') }}</span>
        <span v-if="loadState === 'idle'" class="panel-meta">
          {{ t('settingsAPITokens.tokenCount', { n: tokens.length }) }}
        </span>
      </div>

      <template v-if="loadState === 'loading'">
        <div v-for="i in 3" :key="i" class="skel-row"><div class="skel-bar" /></div>
      </template>

      <div v-else-if="loadState === 'idle' && tokens.length === 0" class="empty-row" role="status">
        {{ t('settingsAPITokens.emptyList') }}
      </div>

      <ul v-else class="token-list">
        <li v-for="tk in tokens" :key="tk.id" class="token-row">
          <div class="token-main">
            <div class="token-name">{{ tk.name }}</div>
            <div class="token-meta">
              <span>{{ t('settingsAPITokens.metaOwner', { owner: tk.ownerName }) }}</span>
              <span>{{ t('settingsAPITokens.metaCreatedBy', { who: tk.createdBy }) }}</span>
              <span>{{ t('settingsAPITokens.metaCreated', { at: formatTime(tk.createdAt) }) }}</span>
              <span :class="{ 'is-narrow': tk.allowedTools.length }" :title="scopeTitle(tk.allowedTools, toolTitles(tk.allowedTools))">
                {{ t('settingsAPITokens.metaTools', { scope: toolScopeLabel(tk) }) }}
              </span>
              <span :class="{ 'is-narrow': tk.groupScope.length }" :title="scopeTitle(tk.groupScope, groupScopeNames(tk.groupScope, groupOpts))">
                {{ t('settingsAPITokens.metaGroups', { scope: groupScopeLabel(tk) }) }}
              </span>
              <span :class="{ 'is-stale': !tk.lastUsedAt }">
                {{
                  tk.lastUsedAt
                    ? t('settingsAPITokens.metaLastUsed', { at: formatTime(tk.lastUsedAt) })
                    : t('settingsAPITokens.neverUsed')
                }}
              </span>
            </div>
            <div class="token-addr">
              <code class="addr mono">{{ endpointFor(tk.id) }}</code>
              <button
                class="btn-ghost btn-mini"
                :aria-label="t('settingsAPITokens.endpointLabel')"
                @click="copyText('ep-' + tk.id, endpointFor(tk.id))"
              >
                {{ copyFeedback === 'ep-' + tk.id ? t('settingsAPITokens.copied') : t('settingsAPITokens.copy') }}
              </button>
            </div>
          </div>
          <div class="token-actions">
            <button class="btn-ghost" @click="openScopeEdit(tk)">{{ t('settingsAPITokens.editScope') }}</button>
            <button class="btn-ghost btn-danger" @click="openRevoke(tk)">{{ t('settingsAPITokens.revoke') }}</button>
          </div>
        </li>
      </ul>
    </div>

    <div v-if="modalOpen" class="modal-backdrop" @click.self="closeModal">
      <div class="modal" role="dialog" aria-modal="true" aria-labelledby="tok-modal-title">
        <h3 id="tok-modal-title" class="modal-title">
          {{
            step === 'issued'
              ? t('settingsAPITokens.issuedTitle')
              : mode === 'create'
                ? t('settingsAPITokens.modalTitle')
                : t('settingsAPITokens.editTitle')
          }}
        </h3>

        <template v-if="step === 'form'">
          <div v-if="formBanner" class="banner banner--error" role="alert">{{ formBanner }}</div>

          <form @submit.prevent="handleSubmit">
            <!-- 编辑模式改不了名称与归属:明文读不回来、归属换了就等于换了一个机器身份,
                 那两件事只有「撤销 + 新增」这一条正路,所以这里干脆不给字段。 -->
            <template v-if="mode === 'create'">
              <label class="field">
                <span class="field-label">{{ t('settingsAPITokens.fieldName') }}</span>
                <input
                  v-model="form.name"
                  class="field-input"
                  type="text"
                  placeholder="deploy runner"
                  autocomplete="off"
                />
                <span v-if="formNameError" class="field-error">{{ formNameError }}</span>
                <span v-else class="field-hint">{{ t('settingsAPITokens.nameHint', { max: TOKEN_NAME_MAX }) }}</span>
              </label>

              <label class="field">
                <span class="field-label">{{ t('settingsAPITokens.fieldOwner') }}</span>
                <select v-model="form.ownerId" class="field-input">
                  <option value="">{{ ownerSelfLabel }}</option>
                  <option v-for="o in ownerOpts" :key="o.id" :value="o.id">{{ o.name }} · {{ o.handle }}</option>
                </select>
                <span class="field-hint">{{ t('settingsAPITokens.ownerHint') }}</span>
              </label>
            </template>

            <div v-else class="field">
              <span class="field-label">{{ editing?.name }}</span>
              <div class="secret-row">
                <code class="secret mono">{{ endpointFor(editing?.id ?? '') }}</code>
                <button
                  type="button"
                  class="btn-ghost"
                  @click="copyText('edit-ep', endpointFor(editing?.id ?? ''))"
                >
                  {{ copyFeedback === 'edit-ep' ? t('settingsAPITokens.copied') : t('settingsAPITokens.copy') }}
                </button>
              </div>
              <span class="field-hint">{{ t('settingsAPITokens.editWho') }}</span>
            </div>

            <div class="field">
              <span class="field-label">{{ t('settingsAPITokens.fieldTools') }}</span>
              <div class="preset-row" role="group">
                <button
                  v-for="p in presetKeys"
                  :key="p"
                  type="button"
                  class="tpl-chip"
                  :class="{ 'is-active': activePreset === p }"
                  @click="applyPreset(p)"
                >
                  {{ t(`settingsAPITokens.preset.${p}`) }}
                </button>
                <span v-if="activePreset === null" class="preset-custom">
                  {{ t('settingsAPITokens.presetCustom') }}
                </span>
              </div>
              <ul class="check-list">
                <li v-for="opt in toolOptions" :key="opt.value">
                  <label class="check-row">
                    <input
                      type="checkbox"
                      :checked="form.allowedTools.includes(opt.value)"
                      @change="onToggleTool(opt.value)"
                    />
                    <span class="check-name">{{ opt.label }}</span>
                    <code class="check-id mono">{{ opt.value }}</code>
                    <span v-if="opt.missing" class="check-tag check-tag--gone">
                      {{ t('settingsAPITokens.optionGone') }}
                    </span>
                    <span v-else-if="opt.noteKey === 'readOnly'" class="check-tag">
                      {{ t('settingsAPITokens.tagReadOnly') }}
                    </span>
                  </label>
                </li>
              </ul>
              <span v-if="!form.allowedTools.length" class="field-hint">
                {{ t(mode === 'create' ? 'settingsAPITokens.toolsHintAll' : 'settingsAPITokens.editToolsHintAll') }}
              </span>
              <span v-else class="field-hint">
                {{ t('settingsAPITokens.toolsHintSome', { n: form.allowedTools.length, total: tools.length }) }}
              </span>
            </div>

            <div class="field">
              <span class="field-label">{{ t('settingsAPITokens.fieldGroups') }}</span>
              <ul v-if="groupOptions.length" class="check-list">
                <li v-for="opt in groupOptions" :key="opt.value">
                  <label class="check-row">
                    <input
                      type="checkbox"
                      :checked="form.groupScope.includes(opt.value)"
                      @change="onToggleGroup(opt.value)"
                    />
                    <span class="check-name">{{ opt.label }}</span>
                    <span v-if="opt.missing" class="check-tag check-tag--gone">
                      {{ t('settingsAPITokens.optionGone') }}
                    </span>
                    <span v-else class="check-tag">
                      {{ t('settingsAPITokens.groupProjectCount', { n: groupCountOf(opt.value) }) }}
                    </span>
                  </label>
                </li>
              </ul>
              <p v-else class="modal-text">{{ t('settingsAPITokens.noGroups') }}</p>
              <span v-if="!form.groupScope.length" class="field-hint">
                {{ t(mode === 'create' ? 'settingsAPITokens.groupsHintAll' : 'settingsAPITokens.editGroupsHintAll') }}
              </span>
              <span v-else class="field-hint">{{ t('settingsAPITokens.groupsHintSome') }}</span>
            </div>

            <p v-if="mode === 'edit'" class="field-hint">{{ t('settingsAPITokens.editEffectHint') }}</p>

            <div class="modal-actions">
              <button type="button" class="btn-ghost" :disabled="formSubmitting" @click="closeModal">
                {{ t('settingsAPITokens.cancel') }}
              </button>
              <button
                type="submit"
                class="btn-primary"
                :disabled="mode === 'edit' ? formSubmitting || !scopesDirty : formSubmitting"
              >
                {{
                  mode === 'create'
                    ? formSubmitting
                      ? t('settingsAPITokens.issuing')
                      : t('settingsAPITokens.issue')
                    : formSubmitting
                      ? t('settingsAPITokens.saving')
                      : t('settingsAPITokens.save')
                }}
              </button>
            </div>
          </form>
        </template>

        <template v-else>
          <div class="banner banner--warn" role="status">
            <span>{{ t('settingsAPITokens.issuedOnce') }}</span>
          </div>

          <div class="field">
            <span class="field-label">{{ t('settingsAPITokens.plaintextLabel') }} · {{ issuedName }}</span>
            <div class="secret-row">
              <code class="secret mono">{{ issuedPlain }}</code>
              <button class="btn-ghost" @click="copyText('plain', issuedPlain)">
                {{ copyFeedback === 'plain' ? t('settingsAPITokens.copied') : t('settingsAPITokens.copy') }}
              </button>
            </div>
            <span v-if="issuedItem" class="field-hint">
              {{
                t('settingsAPITokens.scopeEcho', {
                  tools: toolScopeLabel(issuedItem),
                  groups: groupScopeLabel(issuedItem),
                })
              }}
            </span>
          </div>

          <div class="field">
            <span class="field-label">{{ t('settingsAPITokens.endpointLabel') }}</span>
            <div class="secret-row">
              <code class="secret mono">{{ issuedEndpoint }}</code>
              <button class="btn-ghost" @click="copyText('endpoint', issuedEndpoint)">
                {{ copyFeedback === 'endpoint' ? t('settingsAPITokens.copied') : t('settingsAPITokens.copy') }}
              </button>
            </div>
            <span class="field-hint">{{ t('settingsAPITokens.endpointOwnHint') }}</span>
          </div>

          <p class="modal-text">{{ t('settingsAPITokens.howToTitle') }}</p>

          <div class="field">
            <div class="tab-row" role="tablist">
              <button
                v-for="s in snippets"
                :key="s.client"
                type="button"
                role="tab"
                class="tab"
                :class="{ 'is-active': s.client === activeClient }"
                :aria-selected="s.client === activeClient"
                @click="activeClient = s.client"
              >
                {{ t(`settingsAPITokens.client.${s.client}`) }}
              </button>
            </div>
            <span class="field-hint">{{ t(activeWhereKey) }}</span>
            <pre class="snippet mono">{{ activeSnippet?.code }}</pre>
            <button class="btn-ghost snippet-copy" @click="copyText(activeClient, activeSnippet?.code ?? '')">
              {{ copyFeedback === activeClient ? t('settingsAPITokens.copied') : t('settingsAPITokens.copy') }}
            </button>
            <template v-if="activeClient === 'claudeCode'">
              <span class="field-hint">{{ t('settingsAPITokens.snippetWhere.claudeCodeCli') }}</span>
              <pre class="snippet mono">{{ claudeCodeCmd }}</pre>
              <button class="btn-ghost snippet-copy" @click="copyText('claudeCodeCmd', claudeCodeCmd)">
                {{ copyFeedback === 'claudeCodeCmd' ? t('settingsAPITokens.copied') : t('settingsAPITokens.copy') }}
              </button>
            </template>
          </div>

          <p class="field-hint">{{ t('settingsAPITokens.snippetHint') }}</p>

          <div class="modal-actions">
            <button type="button" class="btn-primary" @click="closeModal">{{ t('settingsAPITokens.done') }}</button>
          </div>
        </template>
      </div>
    </div>

    <div v-if="revokeOpen" class="modal-backdrop" @click.self="closeRevoke">
      <div class="modal" role="dialog" aria-modal="true" aria-labelledby="tok-revoke-title">
        <h3 id="tok-revoke-title" class="modal-title">{{ t('settingsAPITokens.revokeTitle') }}</h3>
        <div v-if="revokeBanner" class="banner banner--error" role="alert">{{ revokeBanner }}</div>
        <p class="modal-text">
          <i18n-t keypath="settingsAPITokens.revokeConfirm" tag="span">
            <template #name><strong>{{ revoking?.name }}</strong></template>
          </i18n-t>
        </p>
        <div class="modal-actions">
          <button type="button" class="btn-ghost" :disabled="revokeSubmitting" @click="closeRevoke">
            {{ t('settingsAPITokens.cancel') }}
          </button>
          <button type="button" class="btn-primary btn-danger" :disabled="revokeSubmitting" @click="confirmRevoke">
            {{ revokeSubmitting ? t('settingsAPITokens.revoking') : t('settingsAPITokens.confirmRevoke') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.tok-root {
  display: flex;
  flex-direction: column;
  gap: 20px;
}
.section-head {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 24px;
}
.section-title {
  font-size: var(--text-heading);
  font-weight: 600;
  color: var(--color-text);
}
.section-desc {
  font-size: var(--text-label);
  color: var(--color-faint);
  margin-top: 6px;
  max-width: 62ch;
  line-height: 1.55;
}
.btn-primary {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 8px 14px;
  font-size: var(--text-label);
  font-weight: 600;
  color: #fff;
  background: var(--color-primary);
  border: none;
  border-radius: var(--radius-md);
  cursor: pointer;
  white-space: nowrap;
}
.btn-primary:hover:not(:disabled) {
  filter: brightness(1.08);
}
.btn-primary:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.btn-ghost {
  padding: 6px 12px;
  font-size: var(--text-label);
  color: var(--color-dim);
  background: transparent;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  cursor: pointer;
}
.btn-ghost:hover:not(:disabled) {
  color: var(--color-text);
  border-color: var(--color-primary);
}
.btn-ghost:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.btn-danger {
  color: var(--color-danger, #d43c3c);
}
.banner {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 14px;
  border-radius: var(--radius-sm);
  font-size: var(--text-label);
  line-height: 1.5;
}
.banner--warn {
  background: var(--color-warn-soft, rgba(212, 160, 23, 0.12));
  color: var(--color-text);
}
.banner--error {
  background: var(--color-danger-soft, rgba(212, 60, 60, 0.12));
  color: var(--color-text);
}
.banner-retry {
  margin-left: auto;
  padding: 4px 10px;
  font-size: var(--text-label);
  background: transparent;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  cursor: pointer;
  color: var(--color-dim);
}
.panel {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-surface, transparent);
}
.panel-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 12px 16px;
  border-bottom: 1px solid var(--color-border);
  font-size: var(--text-label);
  font-weight: 600;
  color: var(--color-text);
}
.panel-meta {
  font-weight: 400;
  color: var(--color-faint);
}
.empty-row {
  padding: 28px 16px;
  text-align: center;
  font-size: var(--text-label);
  color: var(--color-faint);
}
.skel-row {
  padding: 14px 16px;
}
.skel-bar {
  height: 14px;
  border-radius: var(--radius-sm);
  background: var(--color-border);
  opacity: 0.6;
}
.token-list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.token-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 16px;
  padding: 14px 16px;
  border-top: 1px solid var(--color-border);
}
.token-row:first-child {
  border-top: none;
}
.token-name {
  font-size: var(--text-label);
  font-weight: 600;
  color: var(--color-text);
}
.token-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 14px;
  margin-top: 4px;
  font-size: 12px;
  color: var(--color-faint);
}
.token-meta .is-stale {
  color: var(--color-warn, #b8860b);
}
/* 收窄过的范围值得比「不限」更显眼:一眼扫列表时要能挑出哪些令牌是残缺权限的。 */
.token-meta .is-narrow {
  color: var(--color-primary);
}
/* 端点地址单独一行:它是这页的主角(贴进客户端的就是这一串),不能挤在 meta 的灰色小字里。 */
.token-addr {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 8px;
}
.addr {
  flex: 1;
  min-width: 0;
  padding: 5px 8px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-inset, rgba(0, 0, 0, 0.03));
  font-size: 12px;
  user-select: all;
  overflow-wrap: anywhere;
}
.btn-mini {
  flex: none;
  padding: 4px 10px;
  font-size: 12px;
}
.preset-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 5px;
}
.tpl-chip.is-active {
  border-style: solid;
  border-color: var(--color-primary);
  color: var(--color-primary);
  background: var(--color-primary-soft, rgba(0, 0, 0, 0.05));
}
.preset-custom {
  font-size: 12px;
  color: var(--color-faint);
}
.check-list {
  list-style: none;
  margin: 4px 0 0;
  padding: 0;
  max-height: 220px;
  overflow-y: auto;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
}
.check-list li + li {
  border-top: 1px solid var(--color-border);
}
.check-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 7px 10px;
  font-size: var(--text-label);
  color: var(--color-text);
  cursor: pointer;
}
.check-row:hover {
  background: var(--color-inset, rgba(0, 0, 0, 0.03));
}
.check-name {
  flex: 0 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.check-id {
  font-size: 11px;
  color: var(--color-faint);
}
.check-tag {
  margin-left: auto;
  flex: none;
  padding: 1px 6px;
  font-size: 11px;
  color: var(--color-dim);
  border: 1px solid var(--color-border);
  border-radius: 999px;
}
/* 失效项要用告警色:它留着会让后端拒签,必须在列表里跳出来。 */
.check-tag--gone {
  color: var(--color-warn, #b8860b);
  border-color: var(--color-warn, #b8860b);
}
.tab-row {
  display: flex;
  flex-wrap: wrap;
  gap: 2px;
  border-bottom: 1px solid var(--color-border);
}
.tab {
  padding: 6px 10px;
  font-size: var(--text-label);
  color: var(--color-dim);
  background: none;
  border: none;
  border-bottom: 2px solid transparent;
  cursor: pointer;
  white-space: nowrap;
}
.tab:hover {
  color: var(--color-text);
}
.tab.is-active {
  color: var(--color-primary);
  border-bottom-color: var(--color-primary);
  font-weight: 600;
}
.token-actions {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 6px;
}
.mono {
  font-family: var(--font-mono, ui-monospace, monospace);
}
.modal-backdrop {
  position: fixed;
  inset: 0;
  z-index: 60;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
  background: rgba(0, 0, 0, 0.45);
}
.modal {
  width: min(620px, 100%);
  max-height: 85vh;
  overflow-y: auto;
  padding: 20px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-bg, #fff);
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.modal-title {
  margin: 0;
  font-size: var(--text-heading);
  font-weight: 600;
  color: var(--color-text);
}
.modal-text {
  margin: 0;
  font-size: var(--text-label);
  color: var(--color-dim);
  line-height: 1.6;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 5px;
}
.field-label {
  font-size: var(--text-label);
  font-weight: 600;
  color: var(--color-text);
}
.field-input {
  padding: 7px 10px;
  font-size: var(--text-label);
  color: var(--color-text);
  background: var(--color-bg, #fff);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
}
.field-error {
  font-size: 12px;
  color: var(--color-danger, #d43c3c);
}
.field-hint {
  font-size: 12px;
  color: var(--color-faint);
  line-height: 1.5;
}
.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 4px;
}
.secret-row {
  display: flex;
  align-items: stretch;
  gap: 8px;
}
.secret {
  flex: 1;
  min-width: 0;
  padding: 8px 10px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-inset, rgba(0, 0, 0, 0.03));
  font-size: 12px;
  line-height: 1.5;
  /* 手选也要整段选中:令牌是长串,选掉半个字符那一份就废了。 */
  user-select: all;
  overflow-wrap: anywhere;
}
.snippet {
  margin: 0;
  padding: 10px 12px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-inset, rgba(0, 0, 0, 0.03));
  font-size: 12px;
  line-height: 1.55;
  overflow-x: auto;
  white-space: pre;
}
.snippet-copy {
  align-self: flex-start;
}
</style>
