<script setup lang="ts">
/**
 * Settings shell with secondary nav — v6.2 §3.6 权限隔离。
 *
 * 分两组:
 *   - 全局设置(仅管理员):AI / OAuth / 通知 / 凭据保险库 / DNS / 服务器 / 诊断反馈 /
 *     系统 / 全局凭据 / 用户管理 / 审计日志
 *     (构建环境与配置资源已提升为左栏一级页面,不在此列)
 *   - 个人设置(所有登录用户):账户 / 我的凭据
 *
 * role 来自 session store(后端 /api/auth/session 回显);普通用户看不到全局组。
 * 后端 RequireAdmin 仍是权威校验,这里只是不渲染无权限入口。
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useSessionStore } from '../stores/session'

const { t } = useI18n()
const sessionStore = useSessionStore()

const isAdmin = computed(() => sessionStore.user?.role === 'admin')

interface SettingsNavItem {
  to: string
  key: string
  /** 仅管理员可见(§3.6 全局设置)。 */
  adminOnly?: boolean
}

const globalItems: SettingsNavItem[] = [
  { to: '/settings/credentials', key: 'navCredentials', adminOnly: true },
  { to: '/settings/vault', key: 'navVault' },
  { to: '/settings/users', key: 'navUsers', adminOnly: true },
  { to: '/settings/audit', key: 'navAudit', adminOnly: true },
  { to: '/settings/servers', key: 'navServers' },
  { to: '/settings/ai', key: 'navAi' },
  { to: '/settings/oauth', key: 'navOauth' },
  { to: '/settings/dns-providers', key: 'navDnsProviders' },
  { to: '/settings/notifications', key: 'navNotifications' },
  { to: '/settings/diagnosis-stats', key: 'navDiagnosisStats' },
  { to: '/settings/system', key: 'navSystem' },
]

const personalItems: SettingsNavItem[] = [
  { to: '/settings/account', key: 'navAccount' },
  { to: '/settings/my-credentials', key: 'navMyCredentials' },
]

const visibleGlobalItems = computed(() =>
  isAdmin.value ? globalItems : globalItems.filter((i) => !i.adminOnly),
)
</script>

<template>
  <div class="settings-view">
    <header class="view-header">
      <h1 class="view-title">{{ t('settingsHub.title') }}</h1>
      <p class="view-sub">{{ t('settingsHub.subtitle') }}</p>
    </header>

    <!-- 全局设置(仅管理员可见整组) -->
    <template v-if="isAdmin">
      <h2 class="settings-group">
        {{ t('settingsHub.globalGroup') }}
        <span class="settings-group-hint">{{ t('settingsHub.globalGroupHint') }}</span>
      </h2>
      <nav class="settings-nav" :aria-label="t('settingsHub.navAria')">
        <router-link
          v-for="item in visibleGlobalItems"
          :key="item.key"
          :to="item.to"
          class="settings-nav-item"
        >
          {{ t(`settingsHub.${item.key}`) }}
        </router-link>
      </nav>
    </template>

    <!-- 个人设置(所有登录用户) -->
    <h2 class="settings-group">{{ t('settingsHub.personalGroup') }}</h2>
    <nav class="settings-nav settings-nav--personal" :aria-label="t('settingsHub.navAria')">
      <router-link
        v-for="item in personalItems"
        :key="item.key"
        :to="item.to"
        class="settings-nav-item"
      >
        {{ t(`settingsHub.${item.key}`) }}
      </router-link>
    </nav>

    <div class="settings-content">
      <router-view />
    </div>
  </div>
</template>

<style scoped>
.settings-view {
  display: flex;
  flex-direction: column;
  gap: 0;
}

.view-header {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding-bottom: 20px;
  border-bottom: 1px solid var(--color-border);
  margin-bottom: 0;
}

.view-title {
  font-size: var(--text-display);
  font-weight: 700;
  letter-spacing: -0.02em;
  color: var(--color-text);
}

.view-sub {
  font-size: var(--text-body);
  color: var(--color-faint);
  margin-top: 2px;
}

.settings-group {
  display: flex;
  align-items: baseline;
  gap: 8px;
  margin-top: 20px;
  margin-bottom: 8px;
  font-size: var(--text-label);
  font-weight: 700;
  color: var(--color-dim);
  text-transform: none;
}

.settings-group-hint {
  font-size: var(--text-small, 0.8em);
  font-weight: 500;
  color: var(--color-faint);
}

/* Settings secondary nav — tab-style with primary underline */
.settings-nav {
  display: flex;
  flex-wrap: wrap;
  gap: 0;
  border-bottom: 1px solid var(--color-border);
  margin-bottom: 24px;
}

.settings-nav-item {
  padding: 12px 18px;
  font-size: var(--text-label);
  font-weight: 500;
  color: var(--color-faint);
  text-decoration: none;
  border-bottom: 2px solid transparent;
  margin-bottom: -1px;
  transition:
    color var(--duration-fast),
    border-color var(--duration-fast);
}

.settings-nav-item:hover {
  color: var(--color-dim);
}

.settings-nav-item.router-link-active {
  color: var(--color-text);
  font-weight: 600;
  border-bottom-color: var(--color-primary);
}

.settings-content {
  flex: 1;
}
</style>
