<script setup lang="ts">
/**
 * ProjectPicker — 项目多了以后能打字过滤的下拉(运行记录页的「项目」筛选)。
 *
 * 为什么不用原生 <select>:原生控件只能逐条翻,项目上百时找一个等于滚动。
 * 形态照搬同目录的 RefPicker(输入框 + 弹出面板 + 外部点击收起)。
 * 命中哪些字段、已选项要不要补回候选这些规则都在 ../../lib/runProjectFilter 里,
 * 脱离渲染层也能验。
 */
import { computed, nextTick, onBeforeUnmount, ref } from 'vue'
import { narrowProjectOptions, projectFilterLabel, type ProjectOptionSource } from '../../lib/runProjectFilter'

interface PickerRow {
  id: string
  name: string
  /** 行尾的归属说明(组名);空串就不画。 */
  suffix: string
}

const props = withDefaults(defineProps<{
  inputId: string
  /** 选中的项目 ID;'' = 全部项目(不按项目筛)。 */
  modelValue: string
  projects: ProjectOptionSource[]
  /** 分组筛选哨兵(全部分组 / 未归组 / 组 ID),只收窄这里的候选,不改已选的项目。 */
  groupFilter: string
  /** groupId → 展示名(含 '' 那档「未归组」);不给就不画组名。 */
  groupNames?: Record<string, string>
  placeholder: string
  ariaLabel: string
  allLabel: string
  loadingLabel: string
  emptyLabel: string
  disabled?: boolean
  loading?: boolean
}>(), {
  groupNames: undefined,
  disabled: false,
  loading: false,
})

const emit = defineEmits<{ 'update:modelValue': [value: string] }>()

const root = ref<HTMLElement | null>(null)
const input = ref<HTMLInputElement | null>(null)
const list = ref<HTMLElement | null>(null)
const open = ref(false)
const draft = ref('')
const active = ref(-1)

/**
 * 关着的时候输入框显示项目名。地址栏带来的 id 不在清单里(项目被删或换了可见范围)时
 * 显示 id 前 8 位,而不是退成「全部项目」—— 列表确实只筛了那一个项目。
 */
const selectedLabel = computed<string>(() => projectFilterLabel(props.projects, props.modelValue))

const shownLabel = computed(() => (open.value ? draft.value : selectedLabel.value))

const rows = computed<PickerRow[]>(() => {
  const out: PickerRow[] = []
  if (draft.value.trim() === '') out.push({ id: '', name: props.allLabel, suffix: '' })
  for (const p of narrowProjectOptions(props.projects, {
    query: draft.value,
    groupFilter: props.groupFilter,
    selectedId: props.modelValue,
  })) {
    out.push({ id: p.id, name: p.name, suffix: props.groupNames?.[p.groupId ?? ''] ?? '' })
  }
  return out
})

function openPanel(): void {
  if (props.disabled || open.value) return
  draft.value = ''
  open.value = true
  // 打开即把高亮放到已选那一行(没选就放首项「全部项目」):键盘用户按一次 ↓ 再回车
  // 就是当前值,不用先摸面板开没开。口径与同目录 CredentialSelect 一致。
  active.value = Math.max(0, rows.value.findIndex((row) => row.id === props.modelValue))
}

function closePanel(): void {
  open.value = false
  draft.value = ''
  active.value = -1
}

function toggle(): void {
  if (props.disabled) return
  if (open.value) {
    closePanel()
    return
  }
  openPanel()
  void nextTick(() => input.value?.focus())
}

function choose(id: string): void {
  emit('update:modelValue', id)
  closePanel()
}

function moveActive(step: 1 | -1): void {
  const n = rows.value.length
  if (!n) return
  active.value = (active.value + step + n) % n
  void nextTick(() => {
    list.value?.querySelector('[data-active="true"]')?.scrollIntoView({ block: 'nearest' })
  })
}

function handleKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') {
    if (!open.value) return
    event.preventDefault()
    closePanel()
  } else if (event.key === 'ArrowDown') {
    event.preventDefault()
    if (!open.value) openPanel()
    else moveActive(1)
  } else if (event.key === 'ArrowUp') {
    event.preventDefault()
    if (open.value) moveActive(-1)
  } else if (event.key === 'Enter') {
    if (!open.value) return
    event.preventDefault()
    const hit = rows.value[active.value] ?? rows.value[0]
    if (hit) choose(hit.id)
  }
}

function handleDocumentPointerdown(event: PointerEvent): void {
  if (open.value && root.value && !root.value.contains(event.target as Node)) closePanel()
}
document.addEventListener('pointerdown', handleDocumentPointerdown)
onBeforeUnmount(() => document.removeEventListener('pointerdown', handleDocumentPointerdown))
</script>

<template>
  <div ref="root" class="pp" :class="{ 'pp--open': open }">
    <!-- Escape 关掉面板后焦点还留在输入框上,再点它只发 click、不再发 focus ——
         少了 @click 这个入口就点不开,只能去戳右侧箭头。 -->
    <input
      ref="input"
      :id="inputId"
      class="pp__input"
      type="text"
      :value="shownLabel"
      :placeholder="placeholder"
      autocomplete="off"
      spellcheck="false"
      :disabled="disabled"
      role="combobox"
      :aria-label="ariaLabel"
      :aria-expanded="open"
      :aria-controls="`${inputId}-menu`"
      aria-autocomplete="list"
      :aria-activedescendant="active >= 0 ? `${inputId}-opt-${active}` : undefined"
      @focus="openPanel"
      @click="openPanel"
      @input="draft = ($event.target as HTMLInputElement).value; active = -1; open = true"
      @keydown="handleKeydown"
    />
    <button
      type="button"
      class="pp__caret"
      :disabled="disabled"
      tabindex="-1"
      :aria-label="ariaLabel"
      @click="toggle"
    >
      <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" aria-hidden="true">
        <path d="M6 9l6 6 6-6" />
      </svg>
    </button>

    <div
      v-if="open"
      :id="`${inputId}-menu`"
      ref="list"
      class="pp__menu"
      role="listbox"
      :aria-label="ariaLabel"
    >
      <p v-if="loading" class="pp__note">{{ loadingLabel }}</p>
      <p v-else-if="!rows.length" class="pp__note">{{ emptyLabel }}</p>
      <template v-else>
        <button
          v-for="(row, i) in rows"
          :id="`${inputId}-opt-${i}`"
          :key="row.id || '__all__'"
          type="button"
          class="pp__option"
          :class="{ 'pp__option--active': i === active, 'pp__option--selected': row.id === modelValue }"
          :data-active="i === active ? 'true' : undefined"
          role="option"
          :aria-selected="row.id === modelValue"
          @mouseenter="active = i"
          @click="choose(row.id)"
        >
          <span class="pp__name">{{ row.name }}</span>
          <span v-if="row.suffix" class="pp__suffix">{{ row.suffix }}</span>
          <svg v-if="row.id === modelValue" class="pp__check" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" aria-hidden="true">
            <path d="m5 12 4 4L19 6" />
          </svg>
        </button>
      </template>
    </div>
  </div>
</template>

<style scoped>
.pp {
  position: relative;
  min-width: 0;
}

.pp__input {
  width: 100%;
  height: 30px;
  padding: 0 28px 0 10px;
  background: var(--color-inset);
  border: 1px solid var(--color-border);
  border-radius: var(--rounded);
  color: var(--color-text);
  font-family: var(--font-sans);
  font-size: 0.78rem;
  text-overflow: ellipsis;
  transition: border-color var(--duration-fast), box-shadow var(--duration-fast);
}

.pp__input::placeholder {
  color: var(--color-faint);
}

.pp__input:focus {
  outline: none;
  border-color: var(--color-primary);
  box-shadow: 0 0 0 3px var(--color-primary-soft);
}

.pp__input:disabled {
  color: var(--color-faint);
  cursor: not-allowed;
}

.pp__caret {
  position: absolute;
  top: 0;
  right: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 26px;
  height: 30px;
  border: none;
  background: transparent;
  color: var(--color-dim);
  cursor: pointer;
}

.pp__caret:hover {
  color: var(--color-text);
}

.pp__caret:disabled {
  color: var(--color-faint);
  cursor: not-allowed;
}

.pp__menu {
  position: absolute;
  z-index: 30;
  top: calc(100% + 6px);
  left: 0;
  display: flex;
  flex-direction: column;
  width: max(100%, 220px);
  max-height: 244px;
  overflow-y: auto;
  padding: 5px;
  background: var(--color-card-2);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--rounded);
  box-shadow: 0 12px 28px rgb(0 0 0 / 28%);
}

.pp__note {
  padding: 8px 10px;
  color: var(--color-faint);
  font-size: 0.76rem;
}

.pp__option {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 6px 9px;
  border: 0;
  border-radius: calc(var(--rounded) - 2px);
  background: transparent;
  color: var(--color-text);
  font-family: var(--font-sans);
  font-size: 0.78rem;
  text-align: left;
  cursor: pointer;
}

.pp__option:hover {
  background: var(--color-inset);
}

.pp__option--active {
  background: var(--color-primary-soft);
}

.pp__option--selected,
.pp__option--selected .pp__check {
  color: var(--color-primary);
}

.pp__name {
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pp__suffix {
  flex: 0 0 auto;
  color: var(--color-faint);
  font-size: 0.7rem;
}

.pp__check {
  flex: 0 0 auto;
}
</style>
