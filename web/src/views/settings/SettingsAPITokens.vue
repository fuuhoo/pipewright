<script setup lang="ts">
/**
 * API 令牌 —— 机器客户端(MCP、脚本)用的 Bearer 凭据发放台。
 *
 * 这一页只有「列 / 签发 / 撤销」三件事,不是偷懒:库里存的是摘要,明文只在签发那一次响应
 * 里出现,之后任何端点都读不回来。所以签发成功后弹窗不能直接关 —— 它得先把明文交付出去
 * (复制,或两段现成的 MCP 客户端配置),交付完了才允许收。忘了存 = 只能撤销重发。
 *
 * 归属人默认「我自己」:请求里省略 userId,由后端落到当前会话账号(会话载荷里没有 userId,
 * 前端拿不到「我自己的 id」,只有用户名)。下拉刻意不再列出自己那一行,免得同一件事有两条
 * 通往不同请求体的路。
 */
import { computed, onMounted, ref } from 'vue'
import { listAPITokens, createAPIToken, revokeAPIToken } from '../../api/apiTokens'
import type { APIToken } from '../../api/apiTokens'
import { listUsers } from '../../api/users'
import type { User } from '../../api/users'
import { useSessionStore } from '../../stores/session'
import { HttpError } from '../../api/http'
import { useI18n } from 'vue-i18n'
import {
  TOKEN_NAME_MAX,
  mcpHttpSnippet,
  mcpStdioSnippet,
  normalizeTokenName,
  ownerOptions,
  tokenNameProblem,
} from '../../lib/apiTokens'

const { t, locale } = useI18n()
const sessionStore = useSessionStore()

type LoadState = 'idle' | 'loading' | 'error'

const loadState = ref<LoadState>('idle')
const loadError = ref('')
const tokens = ref<APIToken[]>([])
const users = ref<User[]>([])

const myUsername = computed(() => sessionStore.user?.username ?? '')
const ownerOpts = computed(() => ownerOptions(users.value, myUsername.value))
const ownerSelfLabel = computed(() => `${t('settingsAPITokens.ownerSelf')} · @${myUsername.value}`)

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
const revokeKeys = {
  conn: 'settingsAPITokens.errConnRetry',
  status: 'settingsAPITokens.errRevokeStatus',
  retry: 'settingsAPITokens.errRevokeRetry',
}

async function loadAll(): Promise<void> {
  loadState.value = 'loading'
  loadError.value = ''
  try {
    const [list, us] = await Promise.all([listAPITokens(), listUsers()])
    tokens.value = list
    users.value = us
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

// ─── issue ───────────────────────────────────────────────────────────────────

/** 弹窗两步:填表 → 交付明文。第二步没交付完,第一步关不掉(见文件头)。 */
const modalOpen = ref(false)
const step = ref<'form' | 'issued'>('form')
const formSubmitting = ref(false)
const formBanner = ref('')
const form = ref({ name: '', ownerId: '' })
const formNameError = ref('')

const issuedName = ref('')
const issuedPlain = ref('')
const baseUrl = computed(() => window.location.origin)
const stdioSnippet = computed(() => mcpStdioSnippet(baseUrl.value, issuedPlain.value))
const httpSnippet = computed(() => mcpHttpSnippet(baseUrl.value, issuedPlain.value))

function openModal(): void {
  step.value = 'form'
  form.value = { name: '', ownerId: '' }
  formNameError.value = ''
  formBanner.value = ''
  issuedName.value = ''
  issuedPlain.value = ''
  copyFeedback.value = ''
  modalOpen.value = true
}

function closeModal(): void {
  if (formSubmitting.value) return
  modalOpen.value = false
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

async function handleIssue(): Promise<void> {
  if (!validateForm()) return
  formSubmitting.value = true
  formBanner.value = ''
  try {
    const name = normalizeTokenName(form.value.name)
    // 留空 = 请求里不带 userId 这个键:后端据此把令牌签给当前会话用户。
    const created = await createAPIToken(
      form.value.ownerId ? { name, userId: form.value.ownerId } : { name },
    )
    tokens.value = [created.item, ...tokens.value]
    issuedName.value = created.item.name
    issuedPlain.value = created.plaintext
    copyFeedback.value = ''
    step.value = 'issued'
  } catch (err) {
    formBanner.value = errText(err, issueKeys)
  } finally {
    formSubmitting.value = false
  }
}

// ─── copy ────────────────────────────────────────────────────────────────────

const copyFeedback = ref('')
let copyTimer = 0

async function copyText(key: 'plain' | 'stdio' | 'http'): Promise<void> {
  const text =
    key === 'plain' ? issuedPlain.value : key === 'stdio' ? stdioSnippet.value : httpSnippet.value
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
              <span :class="{ 'is-stale': !tk.lastUsedAt }">
                {{
                  tk.lastUsedAt
                    ? t('settingsAPITokens.metaLastUsed', { at: formatTime(tk.lastUsedAt) })
                    : t('settingsAPITokens.neverUsed')
                }}
              </span>
            </div>
          </div>
          <div class="token-actions">
            <button class="btn-ghost btn-danger" @click="openRevoke(tk)">{{ t('settingsAPITokens.revoke') }}</button>
          </div>
        </li>
      </ul>
    </div>

    <div v-if="modalOpen" class="modal-backdrop" @click.self="closeModal">
      <div class="modal" role="dialog" aria-modal="true" aria-labelledby="tok-modal-title">
        <h3 id="tok-modal-title" class="modal-title">
          {{ step === 'form' ? t('settingsAPITokens.modalTitle') : t('settingsAPITokens.issuedTitle') }}
        </h3>

        <template v-if="step === 'form'">
          <div v-if="formBanner" class="banner banner--error" role="alert">{{ formBanner }}</div>

          <form @submit.prevent="handleIssue">
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

            <div class="modal-actions">
              <button type="button" class="btn-ghost" :disabled="formSubmitting" @click="closeModal">
                {{ t('settingsAPITokens.cancel') }}
              </button>
              <button type="submit" class="btn-primary" :disabled="formSubmitting">
                {{ formSubmitting ? t('settingsAPITokens.issuing') : t('settingsAPITokens.issue') }}
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
              <button class="btn-ghost" @click="copyText('plain')">
                {{ copyFeedback === 'plain' ? t('settingsAPITokens.copied') : t('settingsAPITokens.copy') }}
              </button>
            </div>
          </div>

          <p class="modal-text">{{ t('settingsAPITokens.howToTitle') }}</p>

          <div class="field">
            <span class="field-label">{{ t('settingsAPITokens.snippetStdioTitle') }}</span>
            <pre class="snippet mono">{{ stdioSnippet }}</pre>
            <button class="btn-ghost snippet-copy" @click="copyText('stdio')">
              {{ copyFeedback === 'stdio' ? t('settingsAPITokens.copied') : t('settingsAPITokens.copy') }}
            </button>
          </div>

          <div class="field">
            <span class="field-label">{{ t('settingsAPITokens.snippetHttpTitle') }}</span>
            <pre class="snippet mono">{{ httpSnippet }}</pre>
            <button class="btn-ghost snippet-copy" @click="copyText('http')">
              {{ copyFeedback === 'http' ? t('settingsAPITokens.copied') : t('settingsAPITokens.copy') }}
            </button>
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
