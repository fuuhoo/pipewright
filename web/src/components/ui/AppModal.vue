<script setup lang="ts">
/**
 * AppModal — 表单弹窗公共壳。
 *
 * 存在的理由:各设置页原来各自手写遮罩 + 弹层 + 字段样式,同一个「关闭」在三处有三种行为
 * (有的提交中还能点背景、有的按 Esc 无效、有的把焦点丢在页面背后)。这里把规则收一次:
 *   - busy(提交中)时背景点击与 Esc 一律不关 —— 防止「以为没保存成功,其实已经存了」;
 *   - Esc / × / 背景点击都走同一个 close 事件,由父组件决定收尾;
 *   - 打开时焦点进弹层并圈在里面(Tab 循环),关闭后还给触发它的元素;
 *   - 整块 body + actions 包在一个 form 里,所以回车键=提交主操作,不用点按钮。
 *
 * 字段用 ui/FormField、按钮用 ui/AppButton;`.ui-input` 基线样式在这里以 :deep 提供,
 * 这样 slot 里的原生控件在两页里长成一个样。
 */
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId } from 'vue'
import { useI18n } from 'vue-i18n'

const props = withDefaults(defineProps<{
  title: string
  /** 标题下的一句说明(可省略)。 */
  subtitle?: string
  /** 提交中:禁止背景/Esc/× 关闭。 */
  busy?: boolean
  /** 弹层宽度档:确认类用 sm,单列用 md,双列表单用 lg。 */
  width?: 'sm' | 'md' | 'lg'
}>(), { busy: false, width: 'md' })

const emit = defineEmits<{
  close: []
  submit: []
}>()

const { t } = useI18n()

const titleId = useId()
const dialogRef = ref<HTMLElement | null>(null)
let focusedBefore: HTMLElement | null = null

function onRequestClose(): void {
  if (props.busy) return
  emit('close')
}

function onSubmit(): void {
  emit('submit')
}

const widthClass = computed(() => `app-modal--${props.width}`)

function focusables(): HTMLElement[] {
  if (!dialogRef.value) return []
  return Array.from(
    dialogRef.value.querySelectorAll<HTMLElement>(
      'button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), a[href], [tabindex]:not([tabindex="-1"])',
    ),
  )
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') {
    event.preventDefault()
    onRequestClose()
    return
  }
  if (event.key !== 'Tab') return
  const items = focusables()
  if (items.length === 0) return
  const first = items[0]
  const last = items[items.length - 1]
  const active = document.activeElement as HTMLElement | null
  if (event.shiftKey && active === first) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && active === last) {
    event.preventDefault()
    first.focus()
  }
}

onMounted(async () => {
  focusedBefore = document.activeElement as HTMLElement | null
  await nextTick()
  // 落点在第一个可写字段上;没有字段就落在弹层本身(否则 Esc / Tab 的键盘事件没有宿主)。
  const field = dialogRef.value?.querySelector<HTMLElement>(
    '.app-modal__body input:not([type="hidden"]), .app-modal__body select, .app-modal__body textarea',
  )
  ;(field ?? dialogRef.value)?.focus()
})

onBeforeUnmount(() => {
  focusedBefore?.focus?.()
})
</script>

<template>
  <Teleport to="body">
    <div class="app-modal-overlay" @click.self="onRequestClose">
      <div
        ref="dialogRef"
        class="app-modal"
        :class="widthClass"
        role="dialog"
        aria-modal="true"
        :aria-labelledby="titleId"
        tabindex="-1"
        @keydown="onKeydown"
      >
        <header class="app-modal__head">
          <h3 :id="titleId" class="app-modal__title">{{ title }}</h3>
          <button
            type="button"
            class="app-modal__x"
            :aria-label="t('common.close')"
            :disabled="busy"
            @click="onRequestClose"
          >
            ×
          </button>
        </header>
        <p v-if="subtitle" class="app-modal__sub">{{ subtitle }}</p>

        <form class="app-modal__form" @submit.prevent="onSubmit">
          <div class="app-modal__body">
            <slot />
          </div>
          <footer class="app-modal__actions">
            <slot name="actions" />
          </footer>
        </form>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.app-modal-overlay {
  position: fixed;
  inset: 0;
  z-index: 100;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: var(--space-6);
  background: color-mix(in oklch, var(--color-text) 40%, transparent);
}
.app-modal {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  width: 100%;
  max-height: 88vh;
  padding: var(--space-5);
  overflow: auto;
  background: var(--color-surface);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--rounded-lg);
  box-shadow: var(--shadow-modal);
  color: var(--color-text);
  outline: none;
}
.app-modal--sm {
  max-width: 420px;
}
.app-modal--md {
  max-width: 520px;
}
.app-modal--lg {
  max-width: 680px;
}
.app-modal__head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--space-3);
}
.app-modal__title {
  font-size: var(--text-h1);
  font-weight: 700;
  line-height: 1.3;
}
.app-modal__x {
  flex-shrink: 0;
  width: 26px;
  height: 26px;
  border: none;
  border-radius: var(--rounded-sm);
  background: transparent;
  color: var(--color-faint);
  font-size: 1.15rem;
  line-height: 1;
  cursor: pointer;
}
.app-modal__x:hover:not(:disabled) {
  color: var(--color-text);
  background: var(--color-card-2);
}
.app-modal__x:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}
.app-modal__sub {
  margin-top: calc(-1 * var(--space-2));
  font-size: var(--text-label);
  line-height: 1.5;
  color: var(--color-faint);
}
.app-modal__form {
  display: contents;
}
.app-modal__body {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
  min-height: 0;
  overflow: auto;
}
.app-modal__actions {
  display: flex;
  justify-content: flex-end;
  gap: var(--space-3);
}

/* ——— slot 内容的公共词汇:两页的表单字段/横幅/芯片在这里统一 ——— */
.app-modal :deep(.form-grid) {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: var(--space-4);
}
.app-modal :deep(.form-grid--single) {
  grid-template-columns: 1fr;
}
.app-modal :deep(.field--wide) {
  grid-column: 1 / -1;
}
.app-modal :deep(.ui-input) {
  display: block;
  width: 100%;
  height: 38px;
  padding: 0 var(--space-3);
  font-family: var(--font-sans);
  font-size: var(--text-body);
  color: var(--color-text);
  background: var(--color-inset);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--rounded);
  transition:
    border-color var(--duration-fast),
    box-shadow var(--duration-fast);
}
.app-modal :deep(.ui-input::placeholder) {
  color: var(--color-faint);
}
.app-modal :deep(.ui-input:focus) {
  outline: none;
  border-color: var(--color-primary);
  box-shadow: 0 0 0 3px var(--color-primary-soft);
}
.app-modal :deep(.ui-input:disabled) {
  opacity: 0.5;
  cursor: not-allowed;
}
.app-modal :deep(select.ui-input) {
  font-family: var(--font-sans);
}
/* 多行选择器(multiple)不能沿用固定行高,否则只露出一行半。 */
.app-modal :deep(.ui-input.ui-input--multi) {
  height: auto;
  padding: var(--space-2) var(--space-3);
}
.app-modal :deep(.banner--err) {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-red-line);
  border-radius: var(--rounded);
  background: var(--color-red-soft);
  font-size: var(--text-label);
  line-height: 1.5;
  color: var(--color-red);
}
</style>
