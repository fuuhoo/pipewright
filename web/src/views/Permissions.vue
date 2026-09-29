<script setup lang="ts">
/**
 * 「用户与权限」—— 角色与功能点(功能轴的表)、账号(把人挂上去)、资源分组(数据轴)的统一管理页。
 *
 * 三个 tab 的门不一样,这是合并成一个入口时唯一要小心的地方:
 *   - 分组 tab 对所有登录用户开放:组长(可能不是管理员)也得进得来改名册,
 *     页内能做什么由后端给的 canManage 决定。
 *   - 用户 tab 与角色 tab 只在有「设置类」能力时出现。门挂在路由的 beforeEnter 上
 *     (见 router/index.ts 里 permissions-users / permissions-roles 两条):既不会让无权限的人挂载这些组件
 *     去发注定 403 的请求,也不会像 meta.requires 那样把人踢回仪表盘 —— 只把他挪回分组 tab。
 *     后端 /api/admin/users、/api/admin/roles 的 RequireAdmin 才是权威校验。
 *
 * tab 用子路由(而非页内 v-if):可深链、可书签、浏览器返回落在原 tab。
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useSessionStore } from '../stores/session'

const { t } = useI18n()
const sessionStore = useSessionStore()

const canSettings = computed(() => sessionStore.canSettings)

const tabs = computed(() => {
  const list: Array<{ name: string; key: string }> = [{ name: 'permissions-groups', key: 'tabGroups' }]
  if (canSettings.value) {
    // 顺序按「先定规则再套人」:角色表(哪些入口给谁)→ 账号(谁挂哪个角色)→ 分组(数据归谁)。
    list.push({ name: 'permissions-roles', key: 'tabRoles' })
    list.push({ name: 'permissions-users', key: 'tabUsers' })
  }
  return list
})
</script>

<template>
  <div class="permissions-view">
    <header class="view-header">
      <h1 class="view-title">{{ t('permissions.title') }}</h1>
      <p class="view-sub">{{ t('permissions.desc') }}</p>
    </header>

    <nav class="permissions-nav" :aria-label="t('permissions.navAria')">
      <router-link
        v-for="tab in tabs"
        :key="tab.name"
        :to="{ name: tab.name }"
        class="permissions-nav-item"
      >
        {{ t(`permissions.${tab.key}`) }}
      </router-link>
    </nav>

    <div class="permissions-content">
      <router-view />
    </div>
  </div>
</template>

<style scoped>
.permissions-view {
  display: flex;
  flex-direction: column;
}

.view-header {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding-bottom: 16px;
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
  max-width: 92ch;
}

/* 二级 tab:与设置总览同一套观感(下划线式) */
.permissions-nav {
  display: flex;
  flex-wrap: wrap;
  gap: 0;
  border-bottom: 1px solid var(--color-border);
  margin-bottom: 24px;
  margin-top: 16px;
}

.permissions-nav-item {
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

.permissions-nav-item:hover {
  color: var(--color-dim);
}

.permissions-nav-item.router-link-active {
  color: var(--color-text);
  font-weight: 600;
  border-bottom-color: var(--color-primary);
}

.permissions-content {
  flex: 1;
}
</style>
