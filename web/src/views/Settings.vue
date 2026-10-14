<script setup lang="ts">
/**
 * Settings shell with secondary nav — v6.2 §3.6 权限隔离。
 *
 * 分两组:
 *   - 全局设置(仅管理员):AI / OAuth / 通知 / 凭据保险库 / DNS / 服务器 / 诊断反馈 /
 *     系统 / 全局凭据 / 审计日志
 *     (构建环境与配置资源已提升为左栏一级页面,不在此列;用户管理并入了左栏「用户与权限」)
 *   - 个人设置(所有登录用户):账户 / 我的凭据
 *
 * role 来自 session store(后端 /api/auth/session 回显);普通用户看不到全局组。
 * 后端 RequireAdmin 仍是权威校验,这里只是不渲染无权限入口。
 *
 * 布局:宽屏(≥1000px)时导航走左侧竖排 —— 全局 11 项 + 个人 2 项横排会换行成三四排,
 * 光导航就吃掉约 300px 纵向空间,内容被推到首屏之外。窄屏不收成竖排(那时竖排会把内容
 * 挤成一条),而是收成一个原生 select:分组靠 optgroup 保住语义,一行的钱都不多花。
 * 两组用同一条 nav + 组标题,而不是两条 nav —— 竖排时两组之间靠标题分隔就够了。
 */
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useSessionStore } from '../stores/session'
import type { AccessRequires } from '../lib/roles'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const sessionStore = useSessionStore()

// 设置类入口的总闸。今天它的取值与 role==='admin' 完全等价(见 internal/access/roles.go
// 的 settingsRoles),改成读能力位是为了让「谁能进设置」和路由/菜单用同一个来源。
const canSettings = computed(() => sessionStore.canSettings)

interface SettingsNavItem {
  to: string
  key: string
  /** 功能门:与路由 meta.requires 同一判据(§3.6 全局设置)。 */
  requires?: AccessRequires
}

const globalItems: SettingsNavItem[] = [
  { to: '/settings/credentials', key: 'navCredentials', requires: { settings: true } },
  { to: '/settings/vault', key: 'navVault' },
  { to: '/settings/audit', key: 'navAudit', requires: { settings: true } },
  { to: '/settings/servers', key: 'navServers' },
  { to: '/settings/kube-clusters', key: 'navKubeClusters' },
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
  globalItems.filter((i) => sessionStore.meets(i.requires)),
)

/** 窄屏下拉:选中即跳转(浏览器返回仍按路由走,和点链接一致)。 */
function onNavSelect(event: Event): void {
  const to = (event.target as HTMLSelectElement).value
  if (to && to !== route.path) router.push(to)
}
</script>

<template>
  <div class="settings-view">
    <header class="view-header">
      <h1 class="view-title">{{ t('settingsHub.title') }}</h1>
      <p class="view-sub">{{ t('settingsHub.subtitle') }}</p>
    </header>

    <div class="settings-body">
      <!-- 窄屏:一个分组下拉当导航(见文件头注释) -->
      <div class="settings-nav-select">
        <select :value="route.path" :aria-label="t('settingsHub.navAria')" @change="onNavSelect">
          <optgroup v-if="canSettings" :label="t('settingsHub.globalGroup')">
            <option v-for="item in visibleGlobalItems" :key="item.key" :value="item.to">
              {{ t(`settingsHub.${item.key}`) }}
            </option>
          </optgroup>
          <optgroup :label="t('settingsHub.personalGroup')">
            <option v-for="item in personalItems" :key="item.key" :value="item.to">
              {{ t(`settingsHub.${item.key}`) }}
            </option>
          </optgroup>
        </select>
      </div>

      <nav class="settings-nav" :aria-label="t('settingsHub.navAria')">
        <!-- 全局设置:整组仍按设置类能力位收口(与 v6.2 的 admin 口径等价) -->
        <template v-if="canSettings">
          <h2 class="settings-group">
            {{ t('settingsHub.globalGroup') }}
            <span class="settings-group-hint">{{ t('settingsHub.globalGroupHint') }}</span>
          </h2>
          <router-link
            v-for="item in visibleGlobalItems"
            :key="item.key"
            :to="item.to"
            class="settings-nav-item"
          >
            {{ t(`settingsHub.${item.key}`) }}
          </router-link>
        </template>

        <!-- 个人设置(所有登录用户) -->
        <h2 class="settings-group">{{ t('settingsHub.personalGroup') }}</h2>
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
  padding-bottom: 18px;
  border-bottom: 1px solid var(--color-border);
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

.settings-body {
  display: flex;
  flex-direction: column;
  gap: 20px;
  padding-top: 18px;
}

/* ——— 导航 ——— */
/* 窄屏默认只留下拉,竖排列表整个藏起来(两套 DOM 都在,靠媒体查询切,避免 JS 跟视口较劲) */
.settings-nav {
  display: none;
}

.settings-nav-select {
  display: block;
}

.settings-nav-select select {
  width: 100%;
  height: 38px;
  padding: 0 10px;
  background: var(--color-inset);
  border: 1px solid var(--color-border);
  border-radius: var(--rounded);
  color: var(--color-text);
  font-family: inherit;
  font-size: var(--text-label);
}

.settings-nav-select select:focus-visible {
  outline: none;
  border-color: var(--color-primary);
  box-shadow: 0 0 0 3px var(--color-primary-soft);
}

.settings-group {
  flex-basis: 100%;
  display: flex;
  align-items: baseline;
  gap: 8px;
  margin: 14px 0 6px;
  font-size: var(--text-label);
  font-weight: 700;
  color: var(--color-dim);
  text-transform: none;
}

.settings-group:first-child {
  margin-top: 0;
}

.settings-group-hint {
  font-size: var(--text-small, 0.8em);
  font-weight: 500;
  color: var(--color-faint);
}

.settings-nav-item {
  display: block;
  padding: 8px 13px;
  font-size: var(--text-label);
  font-weight: 500;
  color: var(--color-faint);
  text-decoration: none;
  border-radius: var(--rounded-md);
  transition:
    color var(--duration-fast),
    background-color var(--duration-fast);
}

.settings-nav-item:hover {
  color: var(--color-dim);
  background: var(--color-inset);
}

.settings-nav-item.router-link-active {
  color: var(--color-text);
  font-weight: 600;
  background: var(--color-inset);
}

.settings-content {
  flex: 1;
  min-width: 0;
}

/* 宽屏:导航立起来当侧栏,内容留在右边一整块 */
@media (min-width: 1000px) {
  .settings-body {
    flex-direction: row;
    align-items: flex-start;
    gap: 30px;
  }

  .settings-nav-select {
    display: none;
  }

  .settings-nav {
    display: flex;
    flex-wrap: wrap;
    align-content: flex-start;
    gap: 2px;
    width: 208px;
    flex-shrink: 0;
    padding-right: 4px;
    /* 审计日志一屏放不下(整页 4000+px 走文档滚动),导航若不跟着钉住,
       想换一个设置项就得先滚回顶部 —— 侧栏的价值正在随时可切。 */
    position: sticky;
    top: 16px;
    max-height: calc(100vh - 32px);
    overflow-y: auto;
  }

  .settings-group {
    margin: 22px 0 8px;
    padding-left: 13px;
  }

  .settings-nav-item {
    flex-basis: 100%;
    border-left: 2px solid transparent;
    border-radius: 0 var(--rounded-md) var(--rounded-md) 0;
  }

  .settings-nav-item.router-link-active {
    border-left-color: var(--color-primary);
  }
}
</style>
