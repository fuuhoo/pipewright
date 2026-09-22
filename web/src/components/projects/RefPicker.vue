<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref } from 'vue'

interface Props {
  inputId: string
  modelValue: string
  branches: string[]
  tags: string[]
  disabled?: boolean
  placeholder?: string
  hasError?: boolean
  describedBy?: string
  branchesLabel: string
  tagsLabel: string
}

const props = defineProps<Props>()
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()

const root = ref<HTMLElement | null>(null)
const input = ref<HTMLInputElement | null>(null)
const open = ref(false)

const hasOptions = computed(() => props.branches.length + props.tags.length > 0)

// 输入值已等于某个已知引用时不再过滤 —— 否则「测试连接」回填 master 会让面板只剩 master*。
const query = computed(() => {
  const v = props.modelValue.trim().toLowerCase()
  if (!v) return ''
  if (props.branches.includes(props.modelValue) || props.tags.includes(props.modelValue)) return ''
  return v
})

function pick(names: string[]): string[] {
  if (!query.value) return names
  return names.filter((n) => n.toLowerCase().includes(query.value))
}

const shownBranches = computed(() => pick(props.branches))
const shownTags = computed(() => pick(props.tags))

function show(): void {
  if (!props.disabled && !open.value) open.value = true
}

function toggle(): void {
  if (props.disabled) return
  if (open.value) {
    open.value = false
  } else {
    open.value = true
    void nextTick(() => input.value?.focus())
  }
}

function choose(name: string): void {
  emit('update:modelValue', name)
  open.value = false
}

function handleKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape' && open.value) {
    event.preventDefault()
    open.value = false
  } else if (event.key === 'ArrowDown' && !open.value) {
    event.preventDefault()
    open.value = true
  }
}

function handleDocumentPointerdown(event: PointerEvent): void {
  if (open.value && root.value && !root.value.contains(event.target as Node)) open.value = false
}
document.addEventListener('pointerdown', handleDocumentPointerdown)
onBeforeUnmount(() => document.removeEventListener('pointerdown', handleDocumentPointerdown))
</script>

<template>
  <div ref="root" class="ref-picker" :class="{ 'ref-picker--open': open }">
    <input
      ref="input"
      :id="inputId"
      class="ref-picker__input ref-picker__input--mono"
      :class="{ 'ref-picker__input--error': hasError }"
      type="text"
      :value="modelValue"
      :placeholder="placeholder"
      autocomplete="off"
      spellcheck="false"
      :disabled="disabled"
      role="combobox"
      :aria-expanded="open"
      :aria-controls="`${inputId}-menu`"
      aria-autocomplete="list"
      :aria-invalid="hasError ? 'true' : undefined"
      :aria-describedby="describedBy || undefined"
      @input="emit('update:modelValue', ($event.target as HTMLInputElement).value); show()"
      @focus="show"
      @keydown="handleKeydown"
    />
    <button
      type="button"
      class="ref-picker__trigger"
      :disabled="disabled || !hasOptions"
      :aria-label="branchesLabel"
      tabindex="-1"
      @click="toggle"
    >
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
        <path d="m6 9 6 6 6-6" />
      </svg>
    </button>

    <div
      v-if="open && hasOptions"
      :id="`${inputId}-menu`"
      class="ref-picker__menu"
      role="listbox"
      :aria-labelledby="inputId"
    >
      <template v-if="shownBranches.length">
        <div class="ref-picker__group">{{ branchesLabel }}</div>
        <button
          v-for="b in shownBranches"
          :key="'b-' + b"
          type="button"
          class="ref-picker__option"
          :class="{ 'ref-picker__option--selected': b === modelValue }"
          role="option"
          :aria-selected="b === modelValue"
          @click="choose(b)"
        >
          <span class="ref-picker__name mono">{{ b }}</span>
          <svg v-if="b === modelValue" class="ref-picker__check" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" aria-hidden="true">
            <path d="m5 12 4 4L19 6" />
          </svg>
        </button>
      </template>
      <template v-if="shownTags.length">
        <div class="ref-picker__group">{{ tagsLabel }}</div>
        <button
          v-for="g in shownTags"
          :key="'t-' + g"
          type="button"
          class="ref-picker__option"
          :class="{ 'ref-picker__option--selected': g === modelValue }"
          role="option"
          :aria-selected="g === modelValue"
          @click="choose(g)"
        >
          <span class="ref-picker__name mono">{{ g }}</span>
          <svg v-if="g === modelValue" class="ref-picker__check" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" aria-hidden="true">
            <path d="m5 12 4 4L19 6" />
          </svg>
        </button>
      </template>
    </div>
  </div>
</template>

<style scoped>
.ref-picker {
  position: relative;
}

.ref-picker__input {
  width: 100%;
  height: 38px;
  padding: 0 34px 0 12px;
  background: var(--color-inset);
  border: 1px solid var(--color-border);
  border-radius: var(--rounded);
  color: var(--color-text);
  font-size: 0.86rem;
  transition: border-color var(--duration-fast), box-shadow var(--duration-fast);
}

.ref-picker__input--mono {
  font-family: var(--font-mono);
}

.ref-picker__input::placeholder {
  color: var(--color-faint);
}

.ref-picker--open .ref-picker__input,
.ref-picker__input:focus {
  outline: none;
  border-color: var(--color-primary);
  box-shadow: 0 0 0 3px var(--color-primary-soft);
}

.ref-picker__input--error {
  border-color: var(--color-red);
}

.ref-picker__input--error:focus {
  border-color: var(--color-red);
  box-shadow: 0 0 0 3px rgb(220 38 38 / 22%);
}

.ref-picker__input:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.ref-picker__trigger {
  position: absolute;
  top: 50%;
  right: 4px;
  display: grid;
  place-items: center;
  width: 26px;
  height: 26px;
  transform: translateY(-50%);
  border: 0;
  border-radius: calc(var(--rounded) - 4px);
  background: transparent;
  color: var(--color-faint);
  cursor: pointer;
}

.ref-picker__trigger:hover:not(:disabled) {
  color: var(--color-text);
  background: var(--color-card-2);
}

.ref-picker__trigger:disabled {
  opacity: 0.35;
  cursor: not-allowed;
}

.ref-picker--open .ref-picker__trigger {
  color: var(--color-primary);
}

.ref-picker--open .ref-picker__trigger svg {
  transform: rotate(180deg);
}

.ref-picker__menu {
  position: absolute;
  z-index: 30;
  top: calc(100% + 6px);
  left: 0;
  width: 100%;
  max-height: 244px;
  overflow-y: auto;
  padding: 5px;
  border: 1px solid var(--color-border-strong);
  border-radius: var(--rounded);
  background: var(--color-card-2);
  box-shadow: 0 12px 28px rgb(0 0 0 / 28%);
}

.ref-picker__group {
  padding: 7px 9px 3px;
  color: var(--color-faint);
  font-size: 0.7rem;
  font-weight: 600;
  letter-spacing: 0.04em;
  text-transform: uppercase;
}

.ref-picker__option {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 6px 9px;
  border: 0;
  border-radius: calc(var(--rounded) - 2px);
  background: transparent;
  color: var(--color-text);
  font: inherit;
  text-align: left;
  cursor: pointer;
}

.ref-picker__option:hover {
  background: var(--color-inset);
}

.ref-picker__option--selected,
.ref-picker__option--selected .ref-picker__check {
  color: var(--color-primary);
}

.ref-picker__name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ref-picker__check {
  flex: 0 0 auto;
  margin-left: auto;
}
</style>
