<script setup lang="ts">
/**
 * 右上角身份牌:头像 + 昵称(没昵称回登录名) + 邮箱(没邮箱回 @登录名),点它进「账户设置」。
 * 与语言切换器同处一个固定托盘(见 App.vue),所以这里只画自己那一格。
 * 会话还没取到时整块不渲染 —— 亮一个空头像会让人以为登录态坏了。
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { SessionUser } from '../api/auth'
import { contactOf, handleOf, initialsOf, nicknameOf, primaryNameOf } from '../lib/userDisplay'
import { useSessionStore } from '../stores/session'

const { t } = useI18n({ useScope: 'global' })
const sessionStore = useSessionStore()

/** 悬停看全三样:没填昵称就只列 @登录名与邮箱,不重复主行。 */
function tipOf(u: SessionUser): string {
  const bits = [nicknameOf(u), handleOf(u), (u.email ?? '').trim()].filter(Boolean)
  return bits.join(' · ')
}

const me = computed(() => {
  const u = sessionStore.user
  if (!u) return null
  return {
    initials: initialsOf(u),
    name: primaryNameOf(u),
    contact: contactOf(u),
    tip: tipOf(u),
  }
})
</script>

<template>
  <router-link
    v-if="me"
    to="/settings/account"
    class="identity-chip"
    :aria-label="t('shell.identityAria')"
    :title="me.tip"
  >
    <span class="identity-chip__avatar mono" aria-hidden="true">{{ me.initials }}</span>
    <span class="identity-chip__text">
      <span class="identity-chip__name">{{ me.name }}</span>
      <span class="identity-chip__contact">{{ me.contact }}</span>
    </span>
  </router-link>
</template>

<style scoped>
.identity-chip {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  max-width: 240px;
  min-width: 0;
  padding: 3px 10px 3px 3px;
  font-family: var(--font-sans);
  color: var(--color-dim);
  text-decoration: none;
  background: var(--color-card);
  border: 1px solid var(--color-border);
  border-radius: var(--rounded);
  box-shadow: var(--shadow);
  transition:
    color var(--duration-fast),
    border-color var(--duration-fast);
}

.identity-chip:hover {
  color: var(--color-text);
  border-color: var(--color-border-strong);
}

.identity-chip:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: 2px;
}

.identity-chip__avatar {
  flex-shrink: 0;
  width: 24px;
  height: 24px;
  border-radius: 50%;
  display: grid;
  place-items: center;
  background: var(--color-primary-soft);
  color: var(--color-primary);
  font-weight: 700;
  font-size: 0.78rem;
}

.identity-chip__text {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
  text-align: left;
}

.identity-chip__name,
.identity-chip__contact {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.identity-chip__name {
  font-size: 0.8rem;
  font-weight: 600;
  line-height: 1.15;
  color: var(--color-text);
}

.identity-chip__contact {
  font-size: 0.68rem;
  line-height: 1.15;
  color: var(--color-faint);
}
</style>
