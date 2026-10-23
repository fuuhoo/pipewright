<script setup lang="ts">
/**
 * SettingsOAuth — OAuth application configuration for git providers.
 *
 * 四个 git 平台(Gitee / GitHub / GitLab / 自建)是**并列的同构配置**,不是要对比着看的东西,
 * 所以走 tab 一次只摊开一个:四张卡片竖排时,页面要滚近两屏才能填完一个平台,
 * 而「哪个平台启用了」这件事在 tab 的状态点上比在四张卡头上更集中。
 *
 * 「平台登录」用途下还多一档 **OIDC**(通用 SSO):discovery 地址或手填三个端点、
 * 可配主键声明与 scope。它只在登录档出现 —— OIDC 服务器给不了 git token,
 * 「仓库凭据」那条路压根不支持它(后端同样拦,见 internal/oauth/provider.go 的两套枚举)。
 *
 *   - clientId text input
 *   - clientSecret password input — WRITE-ONLY, never echoed; placeholder
 *     hints "留空保留已存" when a secret is already configured
 *   - baseUrl (only the self-hosted `custom` provider)
 *   - oidc 独有:Discovery / 三个端点覆盖 / ID claim / Scopes
 *   - enabled toggle
 *   - Save (PUT) → toast "OAuth 应用已保存" + masked secret echo (mirrors SettingsAI)
 *
 * tab 是页内切换而非子路由:四档没有各自的门禁,而且切走再切回要保住没保存的草稿。
 *
 * tab 之上还有一层「用途」切换:同一个 git 平台在本实例里有两种用法 ——
 *   - 仓库凭据:已登录用户授权后换回 access_token,存进保险库给 git 操作用;
 *   - 平台登录:匿名访客用平台账号登进本平台(自动建号只给最小角色)。
 * 两套配置、两个回调地址,因为一个 OAuth 应用通常只允许登记一个回调 —— 所以面板顶部把
 * 该登记的 Callback URL 只读摊出来,不用配的人去猜路径长什么样。
 *
 * Once a provider is enabled, users connect a git account from the vault page
 * (SettingsVault) where the "连接" buttons live and the callback is handled.
 *
 * Reuses: AppButton tokens + vault/AI patterns. No new UI libraries.
 * Animation: transform/opacity + prefers-reduced-motion.
 */
import { reactive, ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import AppButton from '../../components/ui/AppButton.vue'
import { useToast } from '../../composables/useToast'
import { HttpError } from '../../api/http'
import {
  getOAuthApps,
  saveOAuthApp,
  getOAuthLoginApps,
  saveOAuthLoginApp,
  issuesGitToken,
  type OAuthApp,
  type OAuthLoginApp,
  type OAuthLoginProvider,
  type OAuthProvider,
  type SaveOAuthAppInput,
  type SaveOAuthLoginAppInput,
} from '../../api/oauth'
import { invalidOAuthDomains, joinOAuthDomains, parseOAuthDomains } from '../../lib/oauthDomains'

// ─── types ───────────────────────────────────────────────────────────────────

type LoadState = 'loading' | 'ready' | 'error'

/**
 * 同一个 git 平台的两种用途:
 *   vault —— 已登录用户授权后换回 access_token,存进保险库给 git 操作用;
 *   login —— 匿名访客用平台账号登进本平台(自动建号只给最小角色)。
 * 两套配置、两个回调地址,因为一个 OAuth 应用通常只能登记一个回调。
 */
type Purpose = 'vault' | 'login'

interface ProviderMeta {
  id: OAuthLoginProvider
  label: string
  desc: string
  logoText: string
  logoStyle: string
  /** Self-hosted provider exposes a base URL field. */
  hasBaseUrl: boolean
  /**
   * 端点组长什么样:
   *   none     —— git 四家,地址写死在代码里,不给配;
   *   oidc     —— 发现文档 + 三项覆盖 + 主键声明 + scope;
   *   platform —— 钉钉/飞书/企业微信:官方地址本就已内置,只给三项覆盖(和 scope)。
   */
  endpoints: 'none' | 'oidc' | 'platform'
  /** 企业微信的扫码地址要 AgentId(自建应用的 AgentId),没有它换不出成员身份。 */
  needsAgentId: boolean
  /** 这一档有没有 scope 可填:企业微信的扫码地址压根不带 scope 参数。 */
  hasScopes: boolean
}

/** Per-provider editable form + saved snapshot(两种用途共用同一张表单壳)。 */
interface CardState {
  meta: ProviderMeta
  saved: OAuthApp | OAuthLoginApp | null
  clientId: string
  clientSecret: string // write-only: blank = keep existing
  baseUrl: string
  /** login 用途:邮箱域名白名单,逗号/空格分隔的原始输入。vault 用途不用。 */
  allowedDomains: string
  /** login 用途:要在平台侧登记的回调地址(服务端按当前 origin 现算,只读回显)。 */
  callbackUrl: string
  /**
   * 仅非 git 身份源:端点组 + 主键声明 + scope + AgentId。
   * 保存是**整份覆盖**语义(后端对 git 档位一律落空),所以登录档每次保存都把这些原样发出,不能省略。
   */
  discoveryUrl: string
  authorizeUrl: string
  tokenUrl: string
  userinfoUrl: string
  idClaim: string
  scopes: string
  /** 企业微信自建应用的 AgentId;其余身份源后端一律清空。 */
  agentId: string
  enabled: boolean
  /**
   * 登录即同步凭据:这次授权顺手落成该账号的一条个人 git 凭据。
   * 只在「平台登录」用途的 git 四档出现 —— 非 git 身份源换不出 git token,后端也会拒(422 sync_unsupported)。
   * 打开它会把登录申请的 scope 从「只认身份」升到「能读仓库」,所以默认关,且要管理员显式勾选。
   */
  syncCredentials: boolean
  errors: { clientId: string; baseUrl: string; allowedDomains: string; endpoints: string; agentId: string; syncCredentials: string }
  saving: boolean
}

/** 清空字段错误要用新对象:四个 tab 共用一份卡片,原地改会把别的档一起改掉。 */
function noErrors(): CardState['errors'] {
  return { clientId: '', baseUrl: '', allowedDomains: '', endpoints: '', agentId: '', syncCredentials: '' }
}

// ─── provider metadata ───────────────────────────────────────────────────────

// `label` / `desc` are resolved via i18n at render time (see labelFor / descFor).
const PROVIDERS: ProviderMeta[] = [
  {
    id: 'gitee',
    label: 'Gitee',
    desc: '',
    logoText: 'G',
    logoStyle: 'background:#c71d23;color:#fff',
    hasBaseUrl: false,
    endpoints: 'none',
    needsAgentId: false,
    hasScopes: false,
  },
  {
    id: 'github',
    label: 'GitHub',
    desc: 'github.com',
    logoText: '',
    logoStyle: 'background:#1f2328;color:#fff',
    hasBaseUrl: false,
    endpoints: 'none',
    needsAgentId: false,
    hasScopes: false,
  },
  {
    id: 'gitlab',
    label: 'GitLab',
    desc: 'gitlab.com',
    logoText: '',
    logoStyle: 'background:#fc6d26;color:#fff',
    hasBaseUrl: false,
    endpoints: 'none',
    needsAgentId: false,
    hasScopes: false,
  },
  {
    id: 'custom',
    label: '',
    desc: '',
    logoText: '⚙',
    logoStyle: 'background:var(--color-inset);color:var(--color-dim)',
    hasBaseUrl: true,
    endpoints: 'none',
    needsAgentId: false,
    hasScopes: false,
  },
]

/**
 * 只出现在「平台登录」档的几档身份源,不进 PROVIDERS 数组(那样「仓库凭据」会渲染出
 * 一张存不出东西的卡),单独追加在登录档的 tab 尾巴上。
 *
 * OIDC 的端点必须问服务器自己(discovery);钉钉/飞书/企业微信的官方地址写死在后端预设里,
 * 配置页只给「端点覆盖」——留空即用默认值,填了才走代理/内网入口。
 * 企业微信额外要 AgentId,且扫码地址压根没有 scope 参数(可见范围由企业微信侧决定)。
 */
const OIDC_META: ProviderMeta = {
  id: 'oidc',
  label: 'OIDC',
  desc: '',
  logoText: 'S',
  logoStyle: 'background:#2d6cdf;color:#fff',
  hasBaseUrl: false,
  endpoints: 'oidc',
  needsAgentId: false,
  hasScopes: true,
}

const PLATFORM_METAS: ProviderMeta[] = [
  {
    id: 'dingtalk',
    label: '钉钉',
    desc: '',
    logoText: '钉',
    logoStyle: 'background:#1e7cf5;color:#fff',
    hasBaseUrl: false,
    endpoints: 'platform',
    needsAgentId: false,
    hasScopes: true,
  },
  {
    id: 'feishu',
    label: '飞书',
    desc: '',
    logoText: '飞',
    logoStyle: 'background:#456fff;color:#fff',
    hasBaseUrl: false,
    endpoints: 'platform',
    needsAgentId: false,
    hasScopes: true,
  },
  {
    id: 'wecom',
    label: '企业微信',
    desc: '',
    logoText: '微',
    logoStyle: 'background:#07c160;color:#fff',
    hasBaseUrl: false,
    endpoints: 'platform',
    needsAgentId: true,
    // 企业微信的扫码授权地址压根不带 scope 这一参数,填了也会被后端忽略。
    hasScopes: false,
  },
]

/** 登录档独有的身份源(OIDC + 三家国内平台):「仓库凭据」那套 tab 里不出现。 */
const LOGIN_ONLY_METAS: ProviderMeta[] = [OIDC_META, ...PLATFORM_METAS]

/** 「仓库凭据」档的 tab 清单(四个 git 平台)与「平台登录」档的(再加五档身份源)。 */
const VAULT_IDS: OAuthLoginProvider[] = PROVIDERS.map((m) => m.id)
const ALL_IDS: OAuthLoginProvider[] = [...VAULT_IDS, ...LOGIN_ONLY_METAS.map((m) => m.id)]

// ─── i18n + toast ──────────────────────────────────────────────────────────────

const { t } = useI18n()
const toast = useToast()

/** 需要在界面里翻译的副说明(品牌名本身不翻:登录页按钮用的是服务端给的 label)。 */
const DESC_KEYS: Partial<Record<OAuthLoginProvider, string>> = {
  gitee: 'settingsOAuth.providerGiteeDesc',
  custom: 'settingsOAuth.providerCustomDesc',
  oidc: 'settingsOAuth.providerOidcDesc',
  dingtalk: 'settingsOAuth.providerDingTalkDesc',
  feishu: 'settingsOAuth.providerFeishuDesc',
  wecom: 'settingsOAuth.providerWecomDesc',
}

/** Provider display label — 品牌名一律照字面,只有自建档翻成界面语。 */
function labelFor(id: OAuthLoginProvider): string {
  if (id === 'custom') return t('settingsOAuth.providerCustomLabel')
  return cards[id].meta.label
}

/** Provider sub-description — 有文案位的走 i18n,其余(github/gitlab)用官方域名。 */
function descFor(id: OAuthLoginProvider): string {
  const key = DESC_KEYS[id]
  return key ? t(key) : cards[id].meta.desc
}

// ─── load state ───────────────────────────────────────────────────────────────

const loadState = ref<LoadState>('loading')
const loadError = ref('')

// ─── per-provider card state ──────────────────────────────────────────────────

function newCard(meta: ProviderMeta): CardState {
  return {
    meta,
    saved: null,
    clientId: '',
    clientSecret: '',
    baseUrl: '',
    allowedDomains: '',
    callbackUrl: '',
    discoveryUrl: '',
    authorizeUrl: '',
    tokenUrl: '',
    userinfoUrl: '',
    idClaim: '',
    scopes: '',
    agentId: '',
    enabled: false,
    syncCredentials: false,
    errors: noErrors(),
    saving: false,
  }
}

/** 两套卡片都含 OIDC 档:保险库那套只是不渲染它,数据位留着免得类型收窄。 */
function newCardSet(): CardSet {
  return Object.fromEntries([...PROVIDERS, ...LOGIN_ONLY_METAS].map((meta) => [meta.id, newCard(meta)])) as CardSet
}

type CardSet = Record<OAuthLoginProvider, CardState>

const cards = reactive<CardSet>(newCardSet())
const loginCards = reactive<CardSet>(newCardSet())

/** 当前用途对应的那套卡片:tab 共用,面板与状态点跟着用途走。 */
const forms = computed<CardSet>(() => (purpose.value === 'login' ? loginCards : cards))
const form = computed<CardState>(() => forms.value[active.value])

/**
 * tab 清单跟着用途走:OIDC 只在「平台登录」出现。
 * 「仓库凭据」给它一张卡毫无意义 —— OIDC 服务器换不出 git token,后端也会拒收。
 */
const cardList = computed<OAuthLoginProvider[]>(() =>
  purpose.value === 'login' ? ALL_IDS : VAULT_IDS,
)

/**
 * 「仓库凭据」的回调地址:后端契约里没有,但管理员要在平台侧登记它 ——
 * 路径是固定的,按当前 origin 现算即可(换域名部署也不用改这里)。
 */
const vaultCallbackUrl = computed<string>(
  () => `${window.location.origin}/api/oauth/${active.value}/callback`,
)

/**
 * 面板回显的回调地址:登录用途优先用服务端按 origin 现算的那条。
 * 库里还没这个应用时服务端给不出值,而管理员恰恰在这时候要把它抄去平台侧登记 ——
 * 路径本就是固定推导,所以按当前 origin 兜底,别留一个空白只读框。
 */
const currentCallbackUrl = computed<string>(() => {
  if (purpose.value === 'vault') return vaultCallbackUrl.value
  return (
    form.value.callbackUrl ||
    `${window.location.origin}/api/auth/oauth/${active.value}/callback`
  )
})

/**
 * 回调地址与 Client ID 的提示语按身份源分三套:git 平台去 OAuth 应用里登记,
 * OIDC 去身份提供方的客户端里登记,钉钉/飞书/企业微信去各自开放平台的应用详情里拿。
 * 说错地方是配置页最容易把人绕进去的一件事,所以宁可各写一句。
 */
const callbackHintKey = computed<string>(() => {
  const kind = form.value.meta.endpoints
  if (kind === 'oidc') return 'settingsOAuth.callbackHintOidc'
  if (kind === 'platform') return 'settingsOAuth.callbackHintPlatform'
  return 'settingsOAuth.callbackHint'
})

const clientIdPlaceholderKey = computed<string>(() => {
  const kind = form.value.meta.endpoints
  if (kind === 'oidc') return 'settingsOAuth.clientIdPlaceholderOidc'
  if (kind === 'platform') return 'settingsOAuth.clientIdPlaceholderPlatform'
  return 'settingsOAuth.clientIdPlaceholder'
})

/** 只读框点一下就全选:管理员要的是复制到平台侧的应用里登记。 */
function selectCallback(event: Event): void {
  ;(event.target as HTMLInputElement).select()
}

/**
 * 「登录即同步凭据」只出现在 git 四档的登录卡:OIDC/钉钉/飞书/企业微信换不出 git 令牌,
 * 后端对它们一律拒收(422 sync_unsupported)—— 给这些档摆一个存不进去的开关等于骗人。
 */
const showSyncSwitch = computed<boolean>(
  () => purpose.value === 'login' && issuesGitToken(active.value),
)

// ─── purpose + tabs ───────────────────────────────────────────────────────────

/** 当前编辑的用途。默认「仓库凭据」——它是这个页面原本唯一的用途。 */
const purpose = ref<Purpose>('vault')

/**
 * 换用途时清掉两边的字段错误:留着会让另一档的面板显示上一条错误的红字。
 * 另外 OIDC 与三家国内平台只在登录档有 tab —— 停在它们上面切回「仓库凭据」会摊开一张没有 tab 的面板,
 * 所以先把档位退回第一个平台。
 */
function setPurpose(p: Purpose): void {
  if (purpose.value === p) return
  purpose.value = p
  for (const id of ALL_IDS) {
    cards[id].errors = noErrors()
    loginCards[id].errors = noErrors()
  }
  if (p === 'vault' && !VAULT_IDS.includes(active.value)) active.value = PROVIDERS[0].id
}

function purposeTitle(p: Purpose): string {
  return p === 'login' ? t('settingsOAuth.purposeLogin') : t('settingsOAuth.purposeVault')
}

function purposeHint(p: Purpose): string {
  return p === 'login' ? t('settingsOAuth.purposeLoginHint') : t('settingsOAuth.purposeVaultHint')
}

/** 当前摊开的平台。默认第一个,不做「跳到已配置的那个」——那会让每次进来位置都不一样。 */
const active = ref<OAuthLoginProvider>(PROVIDERS[0].id)

type ProviderStatus = 'on' | 'idle' | 'off'

function statusOf(pid: OAuthLoginProvider): ProviderStatus {
  const card = forms.value[pid]
  if (!card.saved?.configured) return 'off'
  return card.enabled ? 'on' : 'idle'
}

function statusLabel(pid: OAuthLoginProvider): string {
  const s = statusOf(pid)
  if (s === 'on') return t('settingsOAuth.statusEnabled')
  if (s === 'idle') return t('settingsOAuth.statusConfiguredIdle')
  return t('settingsOAuth.statusUnconfigured')
}

/**
 * tablist 的键盘约定:左右箭头换档、Home/End 跳两端(自动选中 —— 焦点即选中,
 * 与 aria-selected 始终一致,不会出现「焦点在暗的 tab 上但面板还是上一个」)。
 */
function onTabKeydown(event: KeyboardEvent): void {
  const tabs = Array.from(
    (event.currentTarget as HTMLElement).querySelectorAll<HTMLButtonElement>('[role="tab"]'),
  )
  if (!tabs.length) return
  const at = Math.max(0, tabs.findIndex((el) => el.dataset.provider === active.value))
  let next = -1
  if (event.key === 'ArrowRight') next = (at + 1) % tabs.length
  else if (event.key === 'ArrowLeft') next = (at - 1 + tabs.length) % tabs.length
  else if (event.key === 'Home') next = 0
  else if (event.key === 'End') next = tabs.length - 1
  if (next < 0) return
  event.preventDefault()
  const pid = tabs[next].dataset.provider as OAuthLoginProvider
  active.value = pid
  tabs[next].focus()
}

// ─── load ─────────────────────────────────────────────────────────────────────

async function loadApps(): Promise<void> {
  loadState.value = 'loading'
  loadError.value = ''
  try {
    // 两种用途一起拉:拉不到就不能进 ready —— 面板里的白名单框会显示成空的,
    // 管理员照着「空」保存一次,已配的域名白名单就被真清空了。
    const [apps, logins] = await Promise.all([getOAuthApps(), getOAuthLoginApps()])
    for (const app of apps) {
      const card = cards[app.provider]
      if (card) applyApp(card, app)
    }
    for (const app of logins) {
      const card = loginCards[app.provider]
      if (card) applyLoginApp(card, app)
    }
    loadState.value = 'ready'
  } catch (err) {
    if (err instanceof HttpError) {
      if (err.status === 0) {
        loadError.value = t('settingsOAuth.errNoServer')
      } else if (err.apiError?.code === 'vault_unconfigured') {
        loadError.value = t('settingsOAuth.errVaultUnconfigured')
      } else {
        loadError.value = err.apiError?.message ?? t('settingsOAuth.errLoadStatus', { status: err.status })
      }
    } else {
      loadError.value = t('settingsOAuth.errLoadGeneric')
    }
    loadState.value = 'error'
  }
}

function applyApp(card: CardState, app: OAuthApp): void {
  card.saved = app
  card.clientId = app.clientId
  card.clientSecret = '' // never pre-fill the secret
  card.baseUrl = app.baseUrl
  card.enabled = app.enabled
  card.errors = noErrors()
}

function applyLoginApp(card: CardState, app: OAuthLoginApp): void {
  card.saved = app
  card.clientId = app.clientId
  card.clientSecret = ''
  card.baseUrl = app.baseUrl
  card.allowedDomains = joinOAuthDomains(app.allowedDomains ?? [])
  card.callbackUrl = app.callbackUrl
  // 非 git 身份源那几项:git 档位服务端一律返回空串,所以直接覆盖回来即可(不会串味)。
  // idClaim / scopes 为空时后端已按默认值(sub、openid profile email)落库并回显。
  card.discoveryUrl = app.discoveryUrl
  card.authorizeUrl = app.authorizeUrl
  card.tokenUrl = app.tokenUrl
  card.userinfoUrl = app.userinfoUrl
  card.idClaim = app.idClaim
  card.scopes = app.scopes
  card.agentId = app.agentId
  card.enabled = app.enabled
  card.syncCredentials = app.syncCredentials
  card.errors = noErrors()
}

onMounted(loadApps)

// ─── save ─────────────────────────────────────────────────────────────────────

async function handleSave(provider: OAuthLoginProvider): Promise<void> {
  const isLogin = purpose.value === 'login'
  const card = forms.value[provider]
  card.errors = noErrors()

  // Client-side validation: an enabled provider must have a clientId, and a
  // secret must exist (either already configured or freshly entered).
  const clientId = card.clientId.trim()
  const baseUrl = card.baseUrl.trim()
  const domains = parseOAuthDomains(card.allowedDomains)
  const hasExistingSecret = !!card.saved?.maskedSecret

  if (card.enabled && !clientId) {
    card.errors.clientId = t('settingsOAuth.errClientIdRequiredEnabled')
    return
  }
  if (card.enabled && !card.clientSecret && !hasExistingSecret) {
    card.errors.clientId = card.errors.clientId || ''
    toast.error(t('settingsOAuth.toastSaveFailed'), { detail: t('settingsOAuth.errSecretRequiredEnabled') })
    return
  }
  if (card.meta.hasBaseUrl && card.enabled && !baseUrl) {
    card.errors.baseUrl = t('settingsOAuth.errBaseUrlRequiredCustom')
    return
  }
  // 白名单只要写了就得逐项合法 —— 与开关无关:后端对提交的白名单一律校验,静默清洗等于
  // 白名单没生效。这里先拦一次,把「哪一项写错了」当场指出来。
  if (isLogin && invalidOAuthDomains(card.allowedDomains).length) {
    card.errors.allowedDomains = t('settingsOAuth.errDomainsInvalid')
    return
  }
  // OIDC:启用时至少要能凑出三个端点(整份 discovery,或三个都手填)。
  // 后端在保存时真的去解析(拿不到就 422),这里只是把「明显没填全」当场拦下,
  // 省一次往返,也不让管理员以为存上了。
  if (isLogin && card.meta.endpoints === 'oidc' && card.enabled) {
    const filled = [card.authorizeUrl, card.tokenUrl, card.userinfoUrl].filter((v) => v.trim()).length
    if (!card.discoveryUrl.trim() && filled !== 3) {
      card.errors.endpoints = t('settingsOAuth.errOidcEndpointsIncomplete')
      return
    }
  }
  // 企业微信:没有 AgentId 连扫码地址都拼不出来(参数名是 agentid,值就是它)。
  // 后端同样会在保存期拒,这一处先拦是为了把红字钉在 AgentId 那一栏上。
  if (isLogin && card.meta.needsAgentId && card.enabled && !card.agentId.trim()) {
    card.errors.agentId = t('settingsOAuth.errAgentIdRequired')
    return
  }

  card.saving = true
  try {
    if (isLogin) {
      const payload: SaveOAuthLoginAppInput = {
        clientId,
        ...(card.clientSecret ? { clientSecret: card.clientSecret } : {}),
        ...(card.meta.hasBaseUrl ? { baseUrl } : {}),
        allowedDomains: domains,
        enabled: card.enabled,
        discoveryUrl: card.discoveryUrl.trim(),
        authorizeUrl: card.authorizeUrl.trim(),
        tokenUrl: card.tokenUrl.trim(),
        userinfoUrl: card.userinfoUrl.trim(),
        idClaim: card.idClaim.trim(),
        scopes: card.scopes.trim(),
        agentId: card.agentId.trim(),
        // 整份覆盖语义:省略即关闭,所以每次都要把开关发出去。
        // 再按档位收一道 —— 非 git 身份源压根换不出 git 令牌,就算状态不知从哪儿被翻成 true,
        // 也不该发出去让后端回一句 422(后端同样拦,这里是让这条流根本不会出现那个错误)。
        syncCredentials: issuesGitToken(provider) && card.syncCredentials,
      }
      applyLoginApp(card, await saveOAuthLoginApp(provider, payload))
    } else {
      const payload: SaveOAuthAppInput = {
        clientId,
        ...(card.clientSecret ? { clientSecret: card.clientSecret } : {}),
        ...(card.meta.hasBaseUrl ? { baseUrl } : {}),
        enabled: card.enabled,
      }
      // 仓库凭据的 tab 清单里压根没有 OIDC(见 cardList),这里只是给类型收窄一个落点。
      applyApp(card, await saveOAuthApp(provider as OAuthProvider, payload))
    }
    toast.success(t('settingsOAuth.toastSaved'), { detail: labelFor(provider) })
  } catch (err) {
    if (err instanceof HttpError) {
      if (err.status === 422 && err.apiError) {
        const code = err.apiError.code
        if (code === 'client_id_required') {
          card.errors.clientId = t('settingsOAuth.errClientIdRequired')
        } else if (code === 'base_url_required') {
          card.errors.baseUrl = t('settingsOAuth.errBaseUrlRequired')
        } else if (code === 'invalid_domain') {
          card.errors.allowedDomains = t('settingsOAuth.errDomainsInvalid')
        } else if (code === 'oidc_config_invalid') {
          card.errors.endpoints = t('settingsOAuth.errOidcConfigInvalid')
        } else if (code === 'platform_config_invalid') {
          card.errors.endpoints = t('settingsOAuth.errPlatformConfigInvalid')
        } else if (code === 'agent_id_required') {
          card.errors.agentId = t('settingsOAuth.errAgentIdRequired')
        } else if (code === 'sync_unsupported') {
          // 前端已经把非 git 档的开关发成 false,正常走不到这儿;真到了就是两侧判据漂了
          // (这一档前端以为换得出 git 令牌,所以开关确实摆在屏上)—— 红字钉在开关下面,别只弹一条 toast。
          card.errors.syncCredentials = t('settingsOAuth.errSyncUnsupported')
        } else {
          toast.error(t('settingsOAuth.toastSaveFailed'), { detail: err.apiError.message })
        }
        return
      }
      if (err.status === 0) {
        toast.error(t('settingsOAuth.toastSaveFailed'), { detail: t('settingsOAuth.errNoServerShort') })
      } else {
        toast.error(t('settingsOAuth.toastSaveFailed'), { detail: err.apiError?.message ?? `HTTP ${err.status}` })
      }
    } else {
      toast.error(t('settingsOAuth.toastSaveFailed'), { detail: err instanceof Error ? err.message : t('settingsOAuth.unknownError') })
    }
  } finally {
    card.saving = false
  }
}

// ─── helpers ─────────────────────────────────────────────────────────────────

function relativeTime(iso: string | null): string {
  if (!iso) return ''
  const diff = Date.now() - new Date(iso).getTime()
  const s = Math.floor(diff / 1000)
  if (s < 60) return t('settingsOAuth.justNow')
  const m = Math.floor(s / 60)
  if (m < 60) return t('settingsOAuth.minutesAgo', { n: m })
  const h = Math.floor(m / 60)
  if (h < 24) return t('settingsOAuth.hoursAgo', { n: h })
  return t('settingsOAuth.daysAgo', { n: Math.floor(h / 24) })
}
</script>

<template>
  <div class="oauth-root">
    <!-- ─── section header ──────────────────────────────────────────────────── -->
    <div class="section-head">
      <div class="section-head-text">
        <h2 class="section-title">{{ t('settingsOAuth.title') }}</h2>
        <p class="section-desc">
          {{ t('settingsOAuth.sectionDesc') }}
        </p>
      </div>
    </div>

    <!-- ─── load error ──────────────────────────────────────────────────────── -->
    <div v-if="loadState === 'error'" class="banner banner--error" role="alert">
      <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
        <circle cx="12" cy="12" r="9" /><path d="M12 8v4M12 16h.01" />
      </svg>
      <span>{{ loadError }}</span>
      <button class="banner-retry" @click="loadApps">↻ {{ t('settingsOAuth.retry') }}</button>
    </div>

    <!-- ─── loading skeleton ────────────────────────────────────────────────── -->
    <template v-if="loadState === 'loading'">
      <div class="oauth-tabs" aria-hidden="true">
        <span v-for="id in cardList" :key="id" class="skel skel--tab" />
      </div>
      <div class="panel">
        <div class="panel-head">
          <span class="skel skel--text" style="width:120px" />
        </div>
        <div class="panel-body">
          <div class="skel skel--field" style="height:38px" aria-hidden="true" />
          <div class="skel skel--field" style="height:38px;margin-top:14px" aria-hidden="true" />
        </div>
      </div>
    </template>

    <!-- ─── one tab per provider, one panel at a time ───────────────────────── -->
    <template v-else-if="loadState === 'ready'">
      <!-- 用途切换:同一个平台的两套配置(仓库凭据 / 平台登录),各自的回调地址 -->
      <div class="purpose-switch" role="group" :aria-label="t('settingsOAuth.purposeSwitchAria')">
        <button
          v-for="p in (['vault', 'login'] as Purpose[])"
          :key="p"
          type="button"
          class="purpose-btn"
          :class="{ 'purpose-btn--active': purpose === p }"
          :aria-pressed="purpose === p"
          :data-purpose="p"
          @click="setPurpose(p)"
        >
          <span class="purpose-title">{{ purposeTitle(p) }}</span>
          <span class="purpose-hint">{{ purposeHint(p) }}</span>
        </button>
      </div>
      <p class="purpose-note">{{ purpose === 'login' ? t('settingsOAuth.purposeLoginNote') : t('settingsOAuth.purposeVaultNote') }}</p>

      <div
        class="oauth-tabs"
        role="tablist"
        :aria-label="t('settingsOAuth.tabsAria')"
        @keydown="onTabKeydown"
      >
        <button
          v-for="pid in cardList"
          :key="pid"
          :id="`oauth-tab-${pid}`"
          :data-provider="pid"
          type="button"
          role="tab"
          class="oauth-tab"
          :class="{ 'oauth-tab--active': active === pid }"
          :aria-selected="active === pid"
          :aria-controls="`oauth-panel-${pid}`"
          :tabindex="active === pid ? 0 : -1"
          @click="active = pid"
        >
          <span class="tab-logo" :style="cards[pid].meta.logoStyle" aria-hidden="true">
            {{ cards[pid].meta.logoText }}
          </span>
          <span class="tab-label">{{ labelFor(pid) }}</span>
          <span class="tab-dot" :class="`tab-dot--${statusOf(pid)}`">
            <span class="sr-only">{{ statusLabel(pid) }}</span>
          </span>
        </button>
      </div>

      <!-- :key=用途+平台:换档时重建面板,入场动画重放一次(否则 Vue 原地打补丁,看不见切换) -->
      <div
        :id="`oauth-panel-${active}`"
        :key="`${purpose}-${active}`"
        class="panel panel--anim"
        :data-panel="active"
        role="tabpanel"
        :aria-labelledby="`oauth-tab-${active}`"
      >
        <div class="panel-head">
          <span class="ph-label">{{ labelFor(active) }}</span>
          <span class="ph-desc">{{ descFor(active) }}</span>
          <span
            class="status-chip"
            :class="statusOf(active) === 'on' ? 'status-chip--ok' : 'status-chip--idle'"
          >
            <span
              class="status-dot"
              :class="{ 'status-dot--ok': statusOf(active) === 'on' }"
              aria-hidden="true"
            />
            {{ statusLabel(active) }}
          </span>
        </div>

        <div class="panel-body">
          <form class="config-form" novalidate @submit.prevent="handleSave(active)">
            <!-- Callback URL — 固定路径,只读:要在平台侧登记的就是这一串 -->
            <div class="field">
              <label class="field-label" :for="`oauth-callback-${active}`">Callback URL</label>
              <input
                :id="`oauth-callback-${active}`"
                class="field-input field-input--mono"
                :value="currentCallbackUrl"
                type="text"
                readonly
                :title="t('settingsOAuth.callbackCopy')"
                @click="selectCallback"
              />
              <span class="field-hint">{{ t(callbackHintKey) }}</span>
            </div>

            <!-- Client ID -->
            <div class="field">
              <label class="field-label" :for="`oauth-clientid-${active}`">Client ID</label>
              <input
                :id="`oauth-clientid-${active}`"
                v-model="form.clientId"
                type="text"
                class="field-input field-input--mono"
                :class="{ 'field-input--error': form.errors.clientId }"
                :placeholder="t(clientIdPlaceholderKey)"
                autocomplete="off"
                :aria-invalid="!!form.errors.clientId || undefined"
                :disabled="form.saving"
                @input="form.errors.clientId = ''"
              />
              <span v-if="form.errors.clientId" class="field-error" role="alert">
                {{ form.errors.clientId }}
              </span>
            </div>

            <!-- Client Secret — write-only -->
            <div class="field">
              <label class="field-label" :for="`oauth-secret-${active}`">
                Client Secret
                <span class="field-optional">{{ t('settingsOAuth.secretOptional') }}</span>
              </label>
              <input
                :id="`oauth-secret-${active}`"
                v-model="form.clientSecret"
                type="password"
                class="field-input field-input--mono"
                :placeholder="form.saved?.maskedSecret
                  ? t('settingsOAuth.secretPlaceholderKeep')
                  : t('settingsOAuth.secretPlaceholderNew')"
                autocomplete="new-password"
                :disabled="form.saving"
              />
              <span
                v-if="form.saved?.maskedSecret && !form.clientSecret"
                class="field-hint mono"
              >{{ t('settingsOAuth.secretStored', { masked: form.saved?.maskedSecret }) }}</span>
            </div>

            <!-- Base URL — self-hosted only -->
            <div v-if="form.meta.hasBaseUrl" class="field">
              <label class="field-label" :for="`oauth-baseurl-${active}`">Base URL</label>
              <input
                :id="`oauth-baseurl-${active}`"
                v-model="form.baseUrl"
                type="url"
                class="field-input field-input--mono"
                :class="{ 'field-input--error': form.errors.baseUrl }"
                placeholder="https://git.example.com"
                autocomplete="off"
                :aria-invalid="!!form.errors.baseUrl || undefined"
                :disabled="form.saving"
                @input="form.errors.baseUrl = ''"
              />
              <span v-if="form.errors.baseUrl" class="field-error" role="alert">
                {{ form.errors.baseUrl }}
              </span>
            </div>

            <!-- 端点组 — 只出现在「平台登录」档。OIDC 的端点必须问服务器自己(discovery),
                 钉钉/飞书/企业微信的官方地址后端已内置,这里只给「覆盖」(走代理/内网入口时才填)。 -->
            <template v-if="purpose === 'login' && form.meta.endpoints !== 'none'">
              <div v-if="form.meta.endpoints === 'oidc'" class="field">
                <label class="field-label" :for="`oauth-discovery-${active}`">
                  {{ t('settingsOAuth.oidcDiscoveryLabel') }}
                  <span class="field-optional">{{ t('settingsOAuth.oidcDiscoveryOptional') }}</span>
                </label>
                <input
                  :id="`oauth-discovery-${active}`"
                  v-model="form.discoveryUrl"
                  type="url"
                  class="field-input field-input--mono"
                  :class="{ 'field-input--error': form.errors.endpoints }"
                  :placeholder="t('settingsOAuth.oidcDiscoveryPlaceholder')"
                  autocomplete="off"
                  :aria-invalid="!!form.errors.endpoints || undefined"
                  :disabled="form.saving"
                  @input="form.errors.endpoints = ''"
                />
                <span class="field-hint">{{ t('settingsOAuth.oidcDiscoveryHint') }}</span>
                <!-- 端点凑不齐 / discovery 拉不到都由服务端在保存时判,红字统一显示在 discovery 这栏下面 -->
                <span v-if="form.errors.endpoints" class="field-error" role="alert">
                  {{ form.errors.endpoints }}
                </span>
              </div>

              <!-- 端点覆盖凑不齐地址时,红字仍归到上面那组(discovery 栏缺席时由提示语兜住) -->
              <div v-else-if="form.errors.endpoints" class="field">
                <span class="field-error" role="alert">{{ form.errors.endpoints }}</span>
              </div>

              <div class="field">
                <label class="field-label" :for="`oauth-oidc-authorize-${active}`">
                  {{ t('settingsOAuth.oidcAuthorizeLabel') }}
                  <span class="field-optional">{{
                    t(form.meta.endpoints === 'oidc'
                      ? 'settingsOAuth.oidcOverrideOptional'
                      : 'settingsOAuth.platformOverrideOptional')
                  }}</span>
                </label>
                <input
                  :id="`oauth-oidc-authorize-${active}`"
                  v-model="form.authorizeUrl"
                  type="url"
                  class="field-input field-input--mono"
                  :placeholder="t(form.meta.endpoints === 'oidc'
                    ? 'settingsOAuth.oidcEndpointPlaceholder'
                    : 'settingsOAuth.platformEndpointPlaceholder')"
                  autocomplete="off"
                  :disabled="form.saving"
                  @input="form.errors.endpoints = ''"
                />
              </div>

              <div class="field">
                <label class="field-label" :for="`oauth-oidc-token-${active}`">
                  {{ t('settingsOAuth.oidcTokenLabel') }}
                  <span class="field-optional">{{
                    t(form.meta.endpoints === 'oidc'
                      ? 'settingsOAuth.oidcOverrideOptional'
                      : 'settingsOAuth.platformOverrideOptional')
                  }}</span>
                </label>
                <input
                  :id="`oauth-oidc-token-${active}`"
                  v-model="form.tokenUrl"
                  type="url"
                  class="field-input field-input--mono"
                  :placeholder="t(form.meta.endpoints === 'oidc'
                    ? 'settingsOAuth.oidcEndpointPlaceholder'
                    : 'settingsOAuth.platformEndpointPlaceholder')"
                  autocomplete="off"
                  :disabled="form.saving"
                  @input="form.errors.endpoints = ''"
                />
              </div>

              <div class="field">
                <label class="field-label" :for="`oauth-oidc-userinfo-${active}`">
                  {{ t('settingsOAuth.oidcUserinfoLabel') }}
                  <span class="field-optional">{{
                    t(form.meta.endpoints === 'oidc'
                      ? 'settingsOAuth.oidcOverrideOptional'
                      : 'settingsOAuth.platformOverrideOptional')
                  }}</span>
                </label>
                <input
                  :id="`oauth-oidc-userinfo-${active}`"
                  v-model="form.userinfoUrl"
                  type="url"
                  class="field-input field-input--mono"
                  :placeholder="t(form.meta.endpoints === 'oidc'
                    ? 'settingsOAuth.oidcEndpointPlaceholder'
                    : 'settingsOAuth.platformEndpointPlaceholder')"
                  autocomplete="off"
                  :disabled="form.saving"
                  @input="form.errors.endpoints = ''"
                />
                <span class="field-hint">{{
                  t(form.meta.endpoints === 'oidc'
                    ? 'settingsOAuth.oidcEndpointsHint'
                    : 'settingsOAuth.platformEndpointsHint')
                }}</span>
              </div>

              <div v-if="form.meta.endpoints === 'oidc'" class="field">
                <label class="field-label" :for="`oauth-oidc-idclaim-${active}`">
                  {{ t('settingsOAuth.oidcIdClaimLabel') }}
                  <span class="field-optional">{{ t('settingsOAuth.oidcIdClaimOptional') }}</span>
                </label>
                <input
                  :id="`oauth-oidc-idclaim-${active}`"
                  v-model="form.idClaim"
                  type="text"
                  class="field-input field-input--mono"
                  placeholder="sub"
                  autocomplete="off"
                  :disabled="form.saving"
                  @input="form.errors.endpoints = ''"
                />
                <span class="field-hint">{{ t('settingsOAuth.oidcIdClaimHint') }}</span>
              </div>

              <!-- AgentId 只有企业微信有:扫码地址的参数名就是 agentid,值取应用详情里的 AgentId -->
              <div v-if="form.meta.needsAgentId" class="field">
                <label class="field-label" :for="`oauth-agentid-${active}`">
                  AgentId
                  <span class="field-required">*</span>
                </label>
                <input
                  :id="`oauth-agentid-${active}`"
                  v-model="form.agentId"
                  type="text"
                  class="field-input field-input--mono"
                  :class="{ 'field-input--error': form.errors.agentId }"
                  :placeholder="t('settingsOAuth.agentIdPlaceholder')"
                  autocomplete="off"
                  :aria-invalid="!!form.errors.agentId || undefined"
                  :disabled="form.saving"
                  @input="form.errors.agentId = ''"
                />
                <span class="field-hint">{{ t('settingsOAuth.agentIdHint') }}</span>
                <span v-if="form.errors.agentId" class="field-error" role="alert">
                  {{ form.errors.agentId }}
                </span>
              </div>

              <div v-if="form.meta.hasScopes" class="field">
                <label class="field-label" :for="`oauth-oidc-scopes-${active}`">
                  {{ t('settingsOAuth.oidcScopesLabel') }}
                  <span class="field-optional">{{
                    t(form.meta.endpoints === 'oidc'
                      ? 'settingsOAuth.oidcScopesOptional'
                      : 'settingsOAuth.platformScopesOptional')
                  }}</span>
                </label>
                <input
                  :id="`oauth-oidc-scopes-${active}`"
                  v-model="form.scopes"
                  type="text"
                  class="field-input field-input--mono"
                  :placeholder="t(form.meta.endpoints === 'oidc'
                    ? 'settingsOAuth.oidcScopesPlaceholder'
                    : 'settingsOAuth.platformScopesPlaceholder')"
                  autocomplete="off"
                  :disabled="form.saving"
                  @input="form.errors.endpoints = ''"
                />
                <span class="field-hint">{{
                  t(form.meta.endpoints === 'oidc'
                    ? 'settingsOAuth.oidcScopesHint'
                    : 'settingsOAuth.platformScopesHint')
                }}</span>
              </div>

              <p v-if="form.meta.endpoints === 'oidc'" class="purpose-note">
                {{ t('settingsOAuth.oidcUserinfoNote') }}
              </p>
              <p v-else class="purpose-note">{{ t('settingsOAuth.platformIdentityNote') }}</p>
            </template>

            <!-- 邮箱域名白名单 — 仅「平台登录」用途 -->
            <div v-if="purpose === 'login'" class="field">
              <label class="field-label" :for="`oauth-domains-${active}`">
                {{ t('settingsOAuth.domainsLabel') }}
                <span class="field-optional">{{ t('settingsOAuth.domainsOptional') }}</span>
              </label>
              <input
                :id="`oauth-domains-${active}`"
                v-model="form.allowedDomains"
                type="text"
                class="field-input field-input--mono"
                :class="{ 'field-input--error': form.errors.allowedDomains }"
                :placeholder="t('settingsOAuth.domainsPlaceholder')"
                autocomplete="off"
                :aria-invalid="!!form.errors.allowedDomains || undefined"
                :disabled="form.saving"
                @input="form.errors.allowedDomains = ''"
              />
              <span v-if="form.errors.allowedDomains" class="field-error" role="alert">
                {{ form.errors.allowedDomains }}
              </span>
              <span v-else class="field-hint">{{ t('settingsOAuth.domainsHint') }}</span>
            </div>

            <!-- 登录即同步凭据 — 仅 git 四档的登录卡(判据见 showSyncSwitch) -->
            <div v-if="showSyncSwitch" class="sync-box">
              <div class="toggle-row">
                <button
                  type="button"
                  class="toggle-track"
                  :class="{ 'toggle-track--on': form.syncCredentials }"
                  role="switch"
                  :aria-checked="form.syncCredentials"
                  :aria-label="t('settingsOAuth.syncAria', { provider: labelFor(active) })"
                  :disabled="form.saving"
                  @click="form.syncCredentials = !form.syncCredentials; form.errors.syncCredentials = ''"
                >
                  <span class="toggle-thumb" />
                </button>
                <div class="toggle-label">
                  <strong>{{ t('settingsOAuth.syncTitle') }}</strong>
                  <span>{{ t('settingsOAuth.syncDesc') }}</span>
                </div>
              </div>
              <p v-if="form.errors.syncCredentials" class="field-error" role="alert">
                {{ form.errors.syncCredentials }}
              </p>
              <p v-else class="sync-note">{{ t('settingsOAuth.syncNote') }}</p>
            </div>

            <!-- Enabled toggle + save -->
            <div class="card-footer">
              <div class="toggle-row">
                <button
                  type="button"
                  class="toggle-track"
                  :class="{ 'toggle-track--on': form.enabled }"
                  role="switch"
                  :aria-checked="form.enabled"
                  :aria-label="t('settingsOAuth.toggleAria', { provider: labelFor(active) })"
                  :disabled="form.saving"
                  @click="form.enabled = !form.enabled"
                >
                  <span class="toggle-thumb" />
                </button>
                <div class="toggle-label">
                  <strong>{{ purpose === 'login' ? t('settingsOAuth.toggleTitleLogin') : t('settingsOAuth.toggleTitle') }}</strong>
                  <span>{{ purpose === 'login' ? t('settingsOAuth.toggleDescLogin') : t('settingsOAuth.toggleDesc') }}</span>
                </div>
              </div>

              <div class="card-actions">
                <span v-if="form.saved?.updatedAt" class="updated-note">
                  {{ t('settingsOAuth.lastSaved', { time: relativeTime(form.saved?.updatedAt ?? null) }) }}
                </span>
                <AppButton
                  variant="primary"
                  type="submit"
                  :loading="form.saving"
                >
                  {{ t('settingsOAuth.saveBtn') }}
                </AppButton>
              </div>
            </div>
          </form>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
/* ─── layout ──────────────────────────────────────────────────────────────── */
.oauth-root {
  display: flex;
  flex-direction: column;
  gap: var(--card-gap);
}

/* ─── section header ──────────────────────────────────────────────────────── */
.section-head {
  display: flex;
  align-items: flex-start;
  gap: 16px;
}

.section-head-text {
  flex: 1;
}

.section-title {
  font-size: 1.12rem;
  font-weight: 600;
  letter-spacing: -0.01em;
  color: var(--color-text);
}

.section-desc {
  font-size: 0.82rem;
  color: var(--color-faint);
  margin-top: 4px;
  max-width: 70ch;
  line-height: 1.55;
}

/* ─── purpose switch ──────────────────────────────────────────────────────── */
/* 用途切换在平台 tab 之上:两档各自决定「这一套配置给谁用」和回调地址。
   走成分段控件而不是第二个 tablist —— 它只有两档、且换档会改变整张面板的字段。 */
.purpose-switch {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}

.purpose-btn {
  flex: 1 1 220px;
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 3px;
  padding: 10px 14px;
  text-align: left;
  font-family: inherit;
  background: var(--color-inset);
  border: 1px solid var(--color-border);
  border-radius: var(--rounded-md);
  cursor: pointer;
  transition:
    border-color var(--duration-fast),
    background var(--duration-fast);
}

.purpose-btn:hover {
  border-color: var(--color-border-strong);
}

.purpose-btn:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: 2px;
}

.purpose-btn--active {
  background: var(--color-primary-soft);
  border-color: var(--color-primary);
}

.purpose-title {
  font-size: 0.84rem;
  font-weight: 600;
  color: var(--color-text);
}

.purpose-hint {
  font-size: 0.74rem;
  line-height: 1.5;
  color: var(--color-faint);
}

/* 当前用途的一句话:说明这套配置和回调地址是给谁的 */
.purpose-note {
  margin: 0;
  font-size: 0.76rem;
  line-height: 1.55;
  color: var(--color-faint);
  max-width: 80ch;
}

/* ─── provider tabs ───────────────────────────────────────────────────────── */
.oauth-tabs {
  display: flex;
  flex-wrap: wrap;
  gap: 0;
  border-bottom: 1px solid var(--color-border);
}

.oauth-tab {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 10px 16px;
  background: none;
  border: none;
  border-bottom: 2px solid transparent;
  margin-bottom: -1px;
  font-family: inherit;
  font-size: 0.84rem;
  font-weight: 500;
  color: var(--color-faint);
  cursor: pointer;
  transition:
    color var(--duration-fast),
    border-color var(--duration-fast);
}

.oauth-tab:hover {
  color: var(--color-dim);
}

.oauth-tab:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: -2px;
  border-radius: var(--rounded-sm);
}

.oauth-tab--active {
  color: var(--color-text);
  font-weight: 600;
  border-bottom-color: var(--color-primary);
}

.tab-logo {
  width: 20px;
  height: 20px;
  border-radius: var(--rounded-sm);
  display: grid;
  place-items: center;
  font-family: var(--font-mono);
  font-weight: 700;
  font-size: 0.68rem;
  flex-shrink: 0;
}

.tab-label {
  white-space: nowrap;
}

/* 状态点:一眼看出哪档已启用/哪档还是空的,不用逐个点开 */
.tab-dot {
  width: 7px;
  height: 7px;
  border-radius: var(--rounded-full);
  flex-shrink: 0;
  background: transparent;
  box-shadow: inset 0 0 0 1px var(--color-border-strong);
}

.tab-dot--idle {
  background: var(--color-faint);
  box-shadow: none;
}

.tab-dot--on {
  background: var(--color-green);
  box-shadow: none;
}

/* ─── status chip ─────────────────────────────────────────────────────────── */
.status-chip {
  margin-left: auto;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 10px;
  border-radius: var(--rounded-full);
  font-size: 0.72rem;
  font-weight: 600;
  border: 1px solid;
  white-space: nowrap;
  flex-shrink: 0;
}

.status-chip--ok {
  background: var(--color-green-soft);
  color: var(--color-green);
  border-color: var(--color-green-line);
}

.status-chip--idle {
  background: var(--color-inset);
  color: var(--color-faint);
  border-color: var(--color-border);
}

.status-dot {
  display: inline-block;
  width: 6px;
  height: 6px;
  border-radius: var(--rounded-full);
  background: var(--color-faint);
  flex-shrink: 0;
}

.status-dot--ok {
  background: var(--color-green);
}

/* ─── banner ──────────────────────────────────────────────────────────────── */
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

.banner-retry:hover { opacity: 0.8; }

/* ─── panel ───────────────────────────────────────────────────────────────── */
.panel {
  background: var(--color-card);
  border: 1px solid var(--color-border);
  border-radius: var(--rounded-card);
  box-shadow: var(--shadow);
  overflow: hidden;
}

.panel--anim {
  animation: panel-in 0.45s var(--ease-out-expo) both;
}

@keyframes panel-in {
  from { opacity: 0; transform: translateY(13px); }
  to   { opacity: 1; transform: none; }
}

.panel-head {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 13px 18px;
  border-bottom: 1px solid var(--color-border);
}

.ph-label {
  font-size: 0.9rem;
  font-weight: 600;
  color: var(--color-text);
}

.ph-desc {
  font-size: 0.74rem;
  color: var(--color-faint);
}

.panel-body {
  padding: 18px;
}

/* ─── skeleton ────────────────────────────────────────────────────────────── */
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

.skel--text { height: 14px; }
.skel--field { width: 100%; }
.skel--tab { width: 108px; height: 20px; margin: 10px 16px; }

@keyframes shimmer {
  0%   { background-position: 200% center; }
  100% { background-position: -200% center; }
}

/* ─── config form ─────────────────────────────────────────────────────────── */
.config-form {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

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

.field-required,
.field-optional {
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
  transition:
    border-color var(--duration-fast),
    box-shadow var(--duration-fast);
}

.field-input--mono {
  font-family: var(--font-mono);
  font-size: 0.8rem;
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
  border-color: var(--color-red) !important;
}

.field-input--error:focus {
  box-shadow: 0 0 0 3px var(--color-red-soft) !important;
}

.field-input:disabled {
  opacity: 0.5;
  cursor: not-allowed;
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

.mono {
  font-family: var(--font-mono);
  letter-spacing: 0.04em;
  user-select: none;
}

/* ─── card footer ─────────────────────────────────────────────────────────── */
.card-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  flex-wrap: wrap;
  border-top: 1px solid var(--color-border);
  padding-top: 16px;
}

.card-actions {
  display: flex;
  align-items: center;
  gap: 12px;
}

.updated-note {
  font-size: 0.72rem;
  color: var(--color-faint);
}

/* ─── 登录即同步凭据 ───────────────────────────────────────────────────────── */
/* 用 inset 底把它和上面的输入框分开:这一项是「这次登录顺手多换走什么」的授权决定,
   和填 clientId 不是同一类操作,视觉上混在一起容易被当成一个普通勾选框顺手打开。 */
.sync-box {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 13px 14px;
  border: 1px solid var(--color-border);
  border-radius: var(--rounded-lg);
  background: var(--color-inset);
}

.sync-note {
  margin: 0;
  font-size: 0.74rem;
  line-height: 1.5;
  color: var(--color-faint);
  max-width: 92ch;
}

/* ─── toggle ──────────────────────────────────────────────────────────────── */
.toggle-row {
  display: flex;
  align-items: flex-start;
  gap: 11px;
}

.toggle-track {
  position: relative;
  width: 40px;
  height: 22px;
  border-radius: var(--rounded-full);
  background: var(--color-border-strong);
  border: none;
  cursor: pointer;
  flex-shrink: 0;
  margin-top: 1px;
  transition: background-color var(--duration-normal);
}

.toggle-track:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: 2px;
}

.toggle-track:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.toggle-track--on {
  background: var(--color-primary);
}

.toggle-thumb {
  position: absolute;
  top: 3px;
  left: 3px;
  width: 16px;
  height: 16px;
  border-radius: var(--rounded-full);
  background: #fff;
  transition: transform var(--duration-normal) var(--ease-out-expo);
  pointer-events: none;
}

.toggle-track--on .toggle-thumb {
  transform: translateX(18px);
}

.toggle-label {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.toggle-label strong {
  font-size: 0.82rem;
  font-weight: 500;
  color: var(--color-text);
}

.toggle-label span {
  font-size: 0.74rem;
  color: var(--color-faint);
  line-height: 1.45;
}

/* ─── reduced motion ──────────────────────────────────────────────────────── */
@media (prefers-reduced-motion: reduce) {
  *,
  *::before,
  *::after {
    animation-duration: 0.01ms !important;
    animation-iteration-count: 1 !important;
    transition-duration: 0.01ms !important;
  }
}
</style>
