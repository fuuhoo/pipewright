<script setup lang="ts">
/**
 * CommandTemplateChips — 「执行命令」的一排常用语言模板芯片。
 *
 * 两个宿主共用:抽屉的原始参数视图(直接写 commands 文本框)、可视化步骤视图(逐行落成步骤块)。
 * 标签与命令正文都不进 i18n(见 commandTemplates.ts 文件头),这里只翻译行首说明。
 *
 * 抽屉只有 360px 宽,12 个芯片全铺开要吃掉约 6 行(实测 126px),把命令区挤出首屏;
 * 所以默认只展开「与所选环境语言匹配」的那几个,其余收进一颗计数按钮 —— 常用的一侧就是一屏。
 */
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { templatesForLanguage, type CommandTemplate } from './commandTemplates'

const props = defineProps<{
  /** 当前所选构建环境的语言:命中的模板前置;留空给全量原序。 */
  language?: string
}>()

const emit = defineEmits<{ (e: 'pick', tpl: CommandTemplate): void }>()

const { t } = useI18n()

/** 环境没选(或该语言没有模板)时折叠态露出的芯片数。 */
const COLLAPSED_COUNT = 4

const expanded = ref(false)
const all = computed<CommandTemplate[]>(() => templatesForLanguage(props.language ?? ''))

const visible = computed<CommandTemplate[]>(() => {
  if (expanded.value) return all.value
  const lang = (props.language ?? '').trim().toLowerCase()
  const hit = all.value.filter((x) => x.language === lang)
  return hit.length > 0 ? hit : all.value.slice(0, COLLAPSED_COUNT)
})

const hiddenCount = computed(() => all.value.length - visible.value.length)
</script>

<template>
  <div class="tpl-chips">
    <span class="tpl-chips-label">{{ t('pipelineJob.cmdTplLabel') }}</span>
    <button
      v-for="tpl in visible"
      :key="tpl.id"
      type="button"
      class="tpl-chip"
      :title="tpl.commands"
      @click="emit('pick', tpl)"
    >
      {{ tpl.label }}
    </button>
    <button
      v-if="hiddenCount > 0 || expanded"
      type="button"
      class="tpl-chip tpl-chip--more"
      :aria-expanded="expanded"
      @click="expanded = !expanded"
    >
      {{ expanded ? t('pipelineJob.cmdTplLess') : t('pipelineJob.cmdTplMore', { n: hiddenCount }) }}
    </button>
  </div>
</template>

<style scoped>
/* 芯片视觉在 pipeline.css 的 .tpl-chips / .tpl-chip(与抽屉里的字段示例芯片共用一套)。 */
</style>
