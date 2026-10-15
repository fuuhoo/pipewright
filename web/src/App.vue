<script setup lang="ts">
import { NConfigProvider, NMessageProvider } from 'naive-ui'
import { RouterView } from 'vue-router'
import { useNaiveTheme } from './composables/useNaiveTheme'
import { useNaiveLocale } from './composables/useNaiveLocale'
import ConfirmDialog from './components/ui/ConfirmDialog.vue'
import ToastHost from './components/ui/ToastHost.vue'
import IdentityChip from './components/IdentityChip.vue'
import LocaleSwitcher from './components/LocaleSwitcher.vue'

const { naiveTheme, naiveThemeOverrides } = useNaiveTheme()
const { naiveLocale, naiveDateLocale } = useNaiveLocale()
</script>

<template>
  <n-config-provider
    :theme="naiveTheme"
    :theme-overrides="naiveThemeOverrides"
    :locale="naiveLocale"
    :date-locale="naiveDateLocale"
    :inline-theme-disabled="false"
  >
    <n-message-provider>
      <RouterView />
      <!-- 右上角固定托盘:身份牌(仅登录态出得来)+ 全局语言切换器(登录页也要有)。 -->
      <div class="top-tray">
        <IdentityChip />
        <LocaleSwitcher />
      </div>
      <!-- Global singletons for useToast / useConfirm composables (Story 1.7+). -->
      <ToastHost />
      <ConfirmDialog />
    </n-message-provider>
  </n-config-provider>
</template>

<style scoped>
/* 位置归托盘管,组件自己只管长相 —— 否则两个 fixed 元素只能靠猜宽度错开。 */
.top-tray {
  position: fixed;
  top: 16px;
  right: 20px;
  z-index: 300;
  display: flex;
  align-items: center;
  gap: 8px;
}
</style>
