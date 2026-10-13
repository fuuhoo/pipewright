<script setup lang="ts">
/**
 * LanguageSelect — 「语言」字段控件:已添加语言的下拉 + 自定义新增 + 其他。
 *
 * 为什么不是自由输入框:见 ../../lib/languageOptions 顶部注释(语言是按精确字符串配对的,
 * 拼错不报错,只是配不上)。这里把入口收成三档,并把落库值统一归一化。
 *
 * 自定义新增的收尾:失焦时若填的其实就是某个已有语言(大小写/空格差异),把值收回那一项,
 * 免得又攒出一个同义语言。
 */
import { computed, nextTick, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  CUSTOM_SENTINEL,
  buildLanguageOptions,
  matchExistingLanguage,
  normalizeLanguage,
} from '../../lib/languageOptions'

const props = withDefaults(defineProps<{
  modelValue: string
  known?: readonly string[]
  disabled?: boolean
  inputId?: string
}>(), {
  known: () => [],
  disabled: false,
  inputId: undefined,
})

const emit = defineEmits<{
  'update:modelValue': [value: string]
}>()

const { t } = useI18n()

const custom = ref(false)
const draft = ref('')
const draftInput = ref<HTMLInputElement | null>(null)

const options = computed(() =>
  buildLanguageOptions({
    current: props.modelValue,
    known: props.known,
    otherLabel: t('misc.languageSelect.other'),
    customLabel: t('misc.languageSelect.custom'),
  }),
)

// 库里存过但下拉里没有的历史值:选项表会把它顶到第一位,这里照样能正常显示。
const selectValue = computed(() => (custom.value ? CUSTOM_SENTINEL : props.modelValue))

function onPick(event: Event): void {
  const value = (event.target as HTMLSelectElement).value
  if (value === CUSTOM_SENTINEL) {
    custom.value = true
    draft.value = props.modelValue
    emit('update:modelValue', '')
    void nextTick(() => draftInput.value?.focus())
    return
  }
  custom.value = false
  draft.value = ''
  emit('update:modelValue', value)
}

function onDraftInput(): void {
  emit('update:modelValue', normalizeLanguage(draft.value))
}

function onDraftBlur(): void {
  const hit = matchExistingLanguage(draft.value, props.known)
  if (hit) {
    custom.value = false
    draft.value = ''
    emit('update:modelValue', hit)
  }
}
</script>

<template>
  <div class="lang-select">
    <select
      :id="inputId"
      class="lang-select__control"
      :value="selectValue"
      :disabled="disabled"
      @change="onPick"
    >
      <option value="" disabled hidden>{{ t('misc.languageSelect.pick') }}</option>
      <option
        v-for="opt in options"
        :key="opt.value"
        :value="opt.value"
      >{{ opt.label }}</option>
    </select>

    <input
      v-if="custom"
      ref="draftInput"
      v-model="draft"
      class="lang-select__control lang-select__draft"
      type="text"
      spellcheck="false"
      autocomplete="off"
      :disabled="disabled"
      :placeholder="t('misc.languageSelect.customPlaceholder')"
      @input="onDraftInput"
      @blur="onDraftBlur"
    />
    <small v-if="custom" class="lang-select__hint">{{ t('misc.languageSelect.customHint') }}</small>
  </div>
</template>

<style scoped>
.lang-select {
  display: flex;
  flex-direction: column;
  gap: 5px;
}
.lang-select__control {
  padding: 8px 10px;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-bg, #fff);
  color: var(--color-text);
  font-size: var(--text-label);
  font-family: inherit;
}
.lang-select__control:focus-visible {
  outline: none;
  border-color: var(--color-primary);
  box-shadow: 0 0 0 3px var(--color-primary-soft);
}
.lang-select__control:disabled {
  opacity: 0.55;
  cursor: not-allowed;
}
.lang-select__hint {
  color: var(--color-faint);
  font-size: var(--text-small, 0.85em);
}
</style>
