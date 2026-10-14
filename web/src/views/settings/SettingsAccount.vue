<script setup lang="ts">
/**
 * SettingsAccount (Story 1.7) — account settings.
 *
 *   - identity display (admin username, read-only this version)
 *   - change-password form (current / new / confirm) — 422 weak / 401 wrong-current mapped inline
 *   - active session list (current marked) + revoke (ConfirmDialog second-confirm)
 *   - 外部 git 账号绑定:列出这条账号绑过哪些平台身份、解绑,以及「去绑定」入口
 *   - "重新引导" button — clears localStorage(onboarding_dismissed)
 *
 * 绑定走的是「仓库凭据」那套 OAuth 应用与 /api/oauth/{p}/callback(state 里记的意图不同),
 * 所以可绑定的平台来自 getOAuthApps,不是登录应用那张表。
 * 绑定只写映射:不改角色,解绑也不撤销当前会话 —— 掉线不该是解绑的副作用。
 *
 * Reuses 1-6 components: FormField / AppButton / ConfirmDialog / EmptyState / StatusBadge-free.
 */
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import FormField from '../../components/ui/FormField.vue'
import AppButton from '../../components/ui/AppButton.vue'
import EmptyState from '../../components/ui/EmptyState.vue'
import SkeletonBlock from '../../components/ui/SkeletonBlock.vue'
import { useToast } from '../../composables/useToast'
import { useConfirm } from '../../composables/useConfirm'
import { resetOnboarding } from '../../composables/useOnboarding'
import { useSessionStore } from '../../stores/session'
import { HttpError } from '../../api/http'
import {
  changePassword,
  listIdentities,
  listSessions,
  revokeSession,
  unbindIdentity,
  type IdentityBinding,
  type Session,
} from '../../api/account'
import { bindStartUrl, getOAuthApps, type OAuthApp, type OAuthProvider } from '../../api/oauth'
import { oauthErrorKey, oauthErrorShowsRawCode } from '../../lib/oauthReason'

const { t } = useI18n()
const toast = useToast()
const confirm = useConfirm()
const router = useRouter()
const route = useRoute()
const sessionStore = useSessionStore()

const username = computed(() => sessionStore.user?.username ?? 'admin')

// ——— change-password form ———
const currentPassword = ref('')
const newPassword = ref('')
const confirmPassword = ref('')
const currentError = ref('')
const newError = ref('')
const confirmError = ref('')
const submitting = ref(false)

function clearPwErrors(): void {
  currentError.value = ''
  newError.value = ''
  confirmError.value = ''
}

function validatePassword(): boolean {
  clearPwErrors()
  let ok = true
  if (!currentPassword.value) {
    currentError.value = t('settingsAccount.errCurrentRequired')
    ok = false
  }
  if (newPassword.value.length < 8) {
    newError.value = t('settingsAccount.errNewTooShort')
    ok = false
  }
  if (confirmPassword.value !== newPassword.value) {
    confirmError.value = t('settingsAccount.errConfirmMismatch')
    ok = false
  }
  return ok
}

async function onSubmitPassword(): Promise<void> {
  if (!validatePassword()) return
  submitting.value = true
  try {
    await changePassword({
      currentPassword: currentPassword.value,
      newPassword: newPassword.value,
    })
    currentPassword.value = ''
    newPassword.value = ''
    confirmPassword.value = ''
    clearPwErrors()
    toast.success(t('settingsAccount.toastPwUpdated'), { detail: t('settingsAccount.toastPwUpdatedDetail') })
    // The server revoked other sessions; refresh the visible list.
    await loadSessions()
  } catch (err) {
    if (err instanceof HttpError) {
      if (err.apiError?.code === 'invalid_current_password') {
        currentError.value = t('settingsAccount.errCurrentWrong')
        return
      }
      if (err.apiError?.code === 'weak_password') {
        newError.value = t('settingsAccount.errNewTooShort')
        return
      }
    }
    toast.error(t('settingsAccount.toastPwFailed'), {
      detail: err instanceof Error ? err.message : t('settingsAccount.unknownError'),
    })
  } finally {
    submitting.value = false
  }
}

// ——— active sessions ———
const sessions = ref<Session[]>([])
const sessionsLoading = ref(true)
const sessionsError = ref('')

async function loadSessions(): Promise<void> {
  sessionsLoading.value = true
  sessionsError.value = ''
  try {
    sessions.value = await listSessions()
  } catch (err) {
    sessionsError.value = err instanceof Error ? err.message : t('settingsAccount.errLoadSessions')
  } finally {
    sessionsLoading.value = false
  }
}

async function onRevoke(s: Session): Promise<void> {
  const ok = await confirm.open({
    title: s.current ? t('settingsAccount.revokeCurrentTitle') : t('settingsAccount.revokeTitle'),
    body: s.current
      ? t('settingsAccount.revokeCurrentBody')
      : t('settingsAccount.revokeBody'),
    confirmLabel: t('settingsAccount.revokeConfirm'),
    variant: 'danger',
  })
  if (!ok) return
  try {
    await revokeSession(s.id)
    if (s.current) {
      // Revoking the current session — backend 204; next call will 401 → login.
      toast.info(t('settingsAccount.toastCurrentRevoked'), { detail: t('settingsAccount.toastCurrentRevokedDetail') })
      sessionStore.clearSession()
      router.push('/login')
      return
    }
    toast.success(t('settingsAccount.toastSessionRevoked'))
    await loadSessions()
  } catch (err) {
    toast.error(t('settingsAccount.toastRevokeFailed'), {
      detail: err instanceof Error ? err.message : t('settingsAccount.unknownError'),
    })
  }
}

function formatTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  })
}

// ——— 外部 git 账号绑定 ———
const identities = ref<IdentityBinding[]>([])
const identitiesLoading = ref(true)
const identitiesError = ref('')
/** 可绑定的平台来自「仓库凭据」那套应用:绑定复用的就是它的 client_id 与回调地址。 */
const bindableApps = ref<OAuthApp[]>([])

const PROVIDER_LABELS = computed<Record<OAuthProvider, string>>(() => ({
  gitee: 'Gitee',
  github: 'GitHub',
  gitlab: 'GitLab',
  custom: t('settingsAccount.providerCustom'),
}))

function providerLabel(provider: string): string {
  return PROVIDER_LABELS.value[provider as OAuthProvider] ?? provider
}

async function loadIdentities(): Promise<void> {
  identitiesLoading.value = true
  identitiesError.value = ''
  try {
    identities.value = await listIdentities()
  } catch (err) {
    identitiesError.value =
      err instanceof Error ? err.message : t('settingsAccount.errLoadIdentities')
  } finally {
    identitiesLoading.value = false
  }
}

async function loadBindableApps(): Promise<void> {
  try {
    bindableApps.value = (await getOAuthApps()).filter((a) => a.enabled && a.configured)
  } catch {
    // 非致命:拉不到就只是没有「去绑定」按钮,已有的绑定照样列得出来。
    bindableApps.value = []
  }
}

/** 发起授权用整页导航:后端 302 到平台授权页,fetch 会把重定向链吞在原地。 */
const diverting = ref('')
function startBind(provider: OAuthProvider): void {
  if (diverting.value) return
  diverting.value = provider
  window.location.href = bindStartUrl(provider)
}

async function onUnbind(b: IdentityBinding): Promise<void> {
  const ok = await confirm.open({
    title: t('settingsAccount.unbindTitle'),
    body: t('settingsAccount.unbindBody', {
      provider: providerLabel(b.provider),
      login: b.username || b.externalId,
    }),
    confirmLabel: t('settingsAccount.unbindConfirm'),
    variant: 'danger',
  })
  if (!ok) return
  try {
    await unbindIdentity(b.id)
    toast.success(t('settingsAccount.toastUnbound'))
    await loadIdentities()
  } catch (err) {
    // 404 多半是另一个标签页已经解过:列表刷新一下就好,不用把「绑定不存在」当失败。
    if (err instanceof HttpError && err.status === 404) {
      toast.info(t('settingsAccount.toastAlreadyUnbound'))
      await loadIdentities()
      return
    }
    toast.error(t('settingsAccount.toastUnbindFailed'), {
      detail: err instanceof Error ? err.message : t('settingsAccount.unknownError'),
    })
  }
}

/**
 * 回跳结果:?bound={provider} 成功、?oauth_error={reason}&provider={p} 失败。
 * 读完就抹掉 query —— 否则刷新一次就把同一条提示再弹一遍。
 */
function readBindResult(): boolean {
  const bound = route.query.bound
  const reason = route.query.oauth_error
  if (typeof bound === 'string' && bound) {
    toast.success(t('settingsAccount.toastBound', { provider: providerLabel(bound) }))
    return true
  }
  if (typeof reason === 'string' && reason) {
    const provider = typeof route.query.provider === 'string' ? route.query.provider : ''
    const detail = oauthErrorShowsRawCode(reason)
      ? t(oauthErrorKey(reason), { code: reason })
      : t(oauthErrorKey(reason))
    toast.error(t('settingsAccount.toastBindFailed', { provider: providerLabel(provider) }), {
      detail,
    })
    return true
  }
  return false
}

function clearBindQuery(): void {
  const query = { ...route.query }
  delete query.bound
  delete query.oauth_error
  delete query.provider
  void router.replace({ query })
}

// ——— re-run onboarding ———
function onReopenOnboarding(): void {
  resetOnboarding()
  toast.info(t('settingsAccount.toastOnboardingReset'), { detail: t('settingsAccount.toastOnboardingResetDetail') })
  router.push('/onboarding')
}

onMounted(async () => {
  void loadSessions()
  void loadIdentities()
  void loadBindableApps()
  if (readBindResult()) clearBindQuery()
})
</script>

<template>
  <div class="account">
    <!-- Identity -->
    <section class="acct-card" aria-labelledby="acct-identity-h">
      <header class="acct-card__h">
        <h2 id="acct-identity-h" class="acct-card__title">{{ t('settingsAccount.identityTitle') }}</h2>
        <p class="acct-card__sub">{{ t('settingsAccount.identitySub') }}</p>
      </header>
      <div class="acct-identity">
        <div class="acct-avatar mono" aria-hidden="true">{{ username.slice(0, 1).toUpperCase() }}</div>
        <div>
          <div class="acct-identity__name">{{ username }}</div>
          <div class="acct-identity__role">{{ t('settingsAccount.roleAdmin') }}</div>
        </div>
      </div>
    </section>

    <!-- Change password -->
    <section class="acct-card" aria-labelledby="acct-pw-h">
      <header class="acct-card__h">
        <h2 id="acct-pw-h" class="acct-card__title">{{ t('settingsAccount.changePwTitle') }}</h2>
        <p class="acct-card__sub">{{ t('settingsAccount.changePwSub') }}</p>
      </header>
      <form class="acct-form" @submit.prevent="onSubmitPassword">
        <FormField :label="t('settingsAccount.currentPwLabel')" field-id="acct-current" :error="currentError" required>
          <template #default="{ fieldId, ariaDescribedby }">
            <input
              :id="fieldId"
              v-model="currentPassword"
              class="field-input"
              :class="{ 'field-input--error': currentError }"
              type="password"
              autocomplete="current-password"
              :aria-invalid="!!currentError || undefined"
              :aria-describedby="ariaDescribedby"
            />
          </template>
        </FormField>

        <FormField
          :label="t('settingsAccount.newPwLabel')"
          field-id="acct-new"
          :error="newError"
          :hint="t('settingsAccount.newPwHint')"
          required
        >
          <template #default="{ fieldId, ariaDescribedby }">
            <input
              :id="fieldId"
              v-model="newPassword"
              class="field-input"
              :class="{ 'field-input--error': newError }"
              type="password"
              autocomplete="new-password"
              :aria-invalid="!!newError || undefined"
              :aria-describedby="ariaDescribedby"
            />
          </template>
        </FormField>

        <FormField :label="t('settingsAccount.confirmPwLabel')" field-id="acct-confirm" :error="confirmError" required>
          <template #default="{ fieldId, ariaDescribedby }">
            <input
              :id="fieldId"
              v-model="confirmPassword"
              class="field-input"
              :class="{ 'field-input--error': confirmError }"
              type="password"
              autocomplete="new-password"
              :aria-invalid="!!confirmError || undefined"
              :aria-describedby="ariaDescribedby"
            />
          </template>
        </FormField>

        <div class="acct-form__actions">
          <AppButton variant="primary" type="submit" :loading="submitting">
            {{ t('settingsAccount.changePwBtn') }}
          </AppButton>
        </div>
      </form>
    </section>

    <!-- Active sessions -->
    <section class="acct-card" aria-labelledby="acct-sessions-h">
      <header class="acct-card__h">
        <h2 id="acct-sessions-h" class="acct-card__title">{{ t('settingsAccount.sessionsTitle') }}</h2>
        <p class="acct-card__sub">{{ t('settingsAccount.sessionsSub') }}</p>
      </header>

      <div v-if="sessionsLoading" class="acct-sessions__loading" aria-busy="true">
        <SkeletonBlock :height="52" />
        <SkeletonBlock :height="52" />
      </div>

      <p v-else-if="sessionsError" class="acct-sessions__error" role="alert">
        {{ sessionsError }}
        <button type="button" class="acct-link" @click="loadSessions">{{ t('settingsAccount.retry') }}</button>
      </p>

      <EmptyState
        v-else-if="sessions.length === 0"
        :title="t('settingsAccount.emptyTitle')"
        :description="t('settingsAccount.emptyDesc')"
      />

      <ul v-else class="acct-sessions">
        <li v-for="s in sessions" :key="s.id" class="acct-session">
          <div class="acct-session__icon" aria-hidden="true">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">
              <rect x="2" y="4" width="20" height="13" rx="2" />
              <path d="M8 21h8M12 17v4" />
            </svg>
          </div>
          <div class="acct-session__bd">
            <div class="acct-session__top">
              <span class="acct-session__id mono">{{ s.id }}</span>
              <span v-if="s.current" class="acct-session__current">{{ t('settingsAccount.currentSession') }}</span>
            </div>
            <div class="acct-session__meta">
              {{ t('settingsAccount.sessionMeta', { created: formatTime(s.createdAt), lastSeen: formatTime(s.lastSeenAt) }) }}
            </div>
          </div>
          <div class="acct-session__act">
            <AppButton variant="danger" @click="onRevoke(s)">{{ t('settingsAccount.revokeBtn') }}</AppButton>
          </div>
        </li>
      </ul>
    </section>

    <!-- Bound git identities -->
    <section class="acct-card" aria-labelledby="acct-ids-h">
      <header class="acct-card__h">
        <h2 id="acct-ids-h" class="acct-card__title">{{ t('settingsAccount.identitiesTitle') }}</h2>
        <p class="acct-card__sub">{{ t('settingsAccount.identitiesSub') }}</p>
      </header>

      <div v-if="identitiesLoading" class="acct-sessions__loading" aria-busy="true">
        <SkeletonBlock :height="52" />
      </div>

      <p v-else-if="identitiesError" class="acct-sessions__error" role="alert">
        {{ identitiesError }}
        <button type="button" class="acct-link" @click="loadIdentities">{{ t('settingsAccount.retry') }}</button>
      </p>

      <EmptyState
        v-else-if="identities.length === 0"
        :title="t('settingsAccount.identitiesEmpty')"
        :description="t('settingsAccount.identitiesEmptyDesc')"
      />

      <ul v-else class="acct-sessions">
        <li v-for="b in identities" :key="b.id" class="acct-session">
          <div class="acct-session__icon" aria-hidden="true">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">
              <path d="M9 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8Z" />
              <path d="M3 21v-1a5 5 0 0 1 5-5h2M14 15l3 3 4-5" />
            </svg>
          </div>
          <div class="acct-session__bd">
            <div class="acct-session__top">
              <span class="acct-session__id">{{ providerLabel(b.provider) }}</span>
              <span class="acct-id__login mono">{{ b.username || '—' }}</span>
            </div>
            <div class="acct-session__meta">
              {{
                t('settingsAccount.identityMeta', {
                  email: b.email || t('settingsAccount.identityNoEmail'),
                  externalId: b.externalId,
                })
              }}
            </div>
          </div>
          <div class="acct-session__act">
            <AppButton variant="danger" @click="onUnbind(b)">{{ t('settingsAccount.unbindBtn') }}</AppButton>
          </div>
        </li>
      </ul>

      <!-- 去绑定:只有「仓库凭据」里已启用且配好的平台才发得起授权 -->
      <div v-if="bindableApps.length" class="acct-bind">
        <span class="acct-bind__label">{{ t('settingsAccount.bindHint') }}</span>
        <div class="acct-bind__row">
          <AppButton
            v-for="app in bindableApps"
            :key="app.provider"
            variant="default"
            :loading="diverting === app.provider"
            :disabled="!!diverting && diverting !== app.provider"
            @click="startBind(app.provider)"
          >
            {{ t('settingsAccount.bindBtn', { provider: providerLabel(app.provider) }) }}
          </AppButton>
        </div>
      </div>
    </section>

    <!-- Re-run onboarding -->
    <section class="acct-card" aria-labelledby="acct-onboarding-h">
      <header class="acct-card__h">
        <h2 id="acct-onboarding-h" class="acct-card__title">{{ t('settingsAccount.onboardingTitle') }}</h2>
        <p class="acct-card__sub">{{ t('settingsAccount.onboardingSub') }}</p>
      </header>
      <div class="acct-form__actions">
        <AppButton variant="default" @click="onReopenOnboarding">{{ t('settingsAccount.onboardingBtn') }}</AppButton>
      </div>
    </section>
  </div>
</template>

<style scoped>
.account {
  display: flex;
  flex-direction: column;
  gap: 18px;
  max-width: 720px;
}

.acct-card {
  background: var(--color-card);
  border: 1px solid var(--color-border);
  border-radius: var(--rounded-card);
  box-shadow: var(--shadow);
  padding: 20px 22px;
}
.acct-card__h {
  margin-bottom: 16px;
}
.acct-card__title {
  font-size: 1rem;
  font-weight: 600;
  color: var(--color-text);
}
.acct-card__sub {
  font-size: 0.79rem;
  color: var(--color-faint);
  margin-top: 4px;
  line-height: 1.5;
}

/* identity */
.acct-identity {
  display: flex;
  align-items: center;
  gap: 14px;
}
.acct-avatar {
  width: 44px;
  height: 44px;
  border-radius: var(--rounded-lg);
  background: var(--color-primary-soft);
  color: var(--color-primary);
  display: grid;
  place-items: center;
  font-weight: 700;
  font-size: 1.1rem;
  flex: none;
}
.acct-identity__name {
  font-size: 0.95rem;
  font-weight: 600;
  color: var(--color-text);
}
.acct-identity__role {
  font-size: 0.78rem;
  color: var(--color-faint);
  margin-top: 2px;
}

/* form */
.acct-form {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.acct-form__actions {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
}

.field-input {
  width: 100%;
  height: 40px;
  background: var(--color-inset);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--rounded);
  padding: 0 13px;
  color: var(--color-text);
  font-family: var(--font-sans);
  font-size: 0.9rem;
  transition:
    border-color var(--duration-fast, 150ms),
    box-shadow var(--duration-fast, 150ms);
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

/* sessions */
.acct-sessions {
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.acct-sessions__loading {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.acct-sessions__error {
  font-size: 0.83rem;
  color: var(--color-red);
}
.acct-session {
  display: flex;
  align-items: center;
  gap: 13px;
  padding: 12px 14px;
  border: 1px solid var(--color-border);
  border-radius: var(--rounded-lg);
  background: var(--color-inset);
}
.acct-session__icon {
  width: 34px;
  height: 34px;
  border-radius: var(--rounded);
  background: var(--color-card);
  border: 1px solid var(--color-border);
  display: grid;
  place-items: center;
  color: var(--color-dim);
  flex: none;
}
.acct-session__icon svg {
  width: 17px;
  height: 17px;
}
.acct-session__bd {
  flex: 1;
  min-width: 0;
}
.acct-session__top {
  display: flex;
  align-items: center;
  gap: 9px;
}
.acct-session__id {
  font-size: 0.82rem;
  color: var(--color-text);
  font-weight: 500;
}
.acct-session__current {
  font-size: 0.68rem;
  color: var(--color-green);
  background: var(--color-green-soft);
  border: 1px solid var(--color-green-line);
  border-radius: var(--rounded-sm);
  padding: 1px 7px;
  font-weight: 600;
}
.acct-session__meta {
  font-size: 0.75rem;
  color: var(--color-faint);
  margin-top: 3px;
}
.acct-session__act {
  flex: none;
}

.acct-link {
  background: none;
  border: none;
  color: var(--color-primary);
  cursor: pointer;
  font: inherit;
  text-decoration: underline;
  text-underline-offset: 2px;
  padding: 0;
  margin-left: 6px;
}

/* bound identities */
.acct-id__login {
  font-size: 0.78rem;
  color: var(--color-dim);
}
.acct-bind {
  margin-top: 14px;
  padding-top: 14px;
  border-top: 1px dashed var(--color-border);
}
.acct-bind__label {
  display: block;
  font-size: 0.78rem;
  color: var(--color-faint);
  margin-bottom: 9px;
}
.acct-bind__row {
  display: flex;
  flex-wrap: wrap;
  gap: 9px;
}
</style>
