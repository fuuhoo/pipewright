<script setup lang="ts">
/**
 * 标签筛选:可输入搜索的多选下拉。
 *
 * 工具栏位置有限,原生 select 既不支持搜索也选不了多个,所以自带一个轻量浮层。
 * 选中集合以项目上的标签文案为准(不区分大小写);菜单内搜索只过滤候选,不改选择。
 */
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

const props = defineProps<{
  modelValue: string[]
  options: string[]
  allLabel: string
  ariaLabel: string
}>()

const emit = defineEmits<{
  'update:modelValue': [value: string[]]
}>()

const { t } = useI18n()

const root = ref<HTMLElement | null>(null)
const searchInput = ref<HTMLInputElement | null>(null)
const open = ref(false)
const query = ref('')
const activeIndex = ref(0)

const filtered = computed(() => {
  const q = query.value.trim().toLowerCase()
  return q ? props.options.filter((tag) => tag.toLowerCase().includes(q)) : props.options
})

const selectedKeys = computed(() => new Set(props.modelValue.map((tag) => tag.toLowerCase())))

function isSelected(tag: string): boolean {
  return selectedKeys.value.has(tag.toLowerCase())
}

const summary = computed(() => {
  if (!props.modelValue.length) return props.allLabel
  if (props.modelValue.length === 1) return props.modelValue[0]
  return `${props.modelValue[0]} +${props.modelValue.length - 1}`
})

async function toggleOpen(): Promise<void> {
  if (open.value) {
    close()
    return
  }
  open.value = true
  query.value = ''
  activeIndex.value = 0
  await nextTick()
  searchInput.value?.focus()
}

function close(): void {
  open.value = false
  query.value = ''
  activeIndex.value = 0
}

function toggleTag(tag: string): void {
  const key = tag.toLowerCase()
  const next = props.modelValue.some((item) => item.toLowerCase() === key)
    ? props.modelValue.filter((item) => item.toLowerCase() !== key)
    : [...props.modelValue, tag]
  emit('update:modelValue', next)
}

function clearAll(): void {
  emit('update:modelValue', [])
  query.value = ''
}

function moveActive(direction: 1 | -1): void {
  if (!filtered.value.length) return
  activeIndex.value = (activeIndex.value + direction + filtered.value.length) % filtered.value.length
}

function handleSearchKeydown(event: KeyboardEvent): void {
  if (event.key === 'ArrowDown') {
    event.preventDefault()
    moveActive(1)
  } else if (event.key === 'ArrowUp') {
    event.preventDefault()
    moveActive(-1)
  } else if (event.key === 'Enter') {
    event.preventDefault()
    const tag = filtered.value[activeIndex.value]
    if (tag) toggleTag(tag)
  } else if (event.key === 'Escape') {
    event.preventDefault()
    close()
  }
}

watch(filtered, () => {
  activeIndex.value = 0
})

function handlePointerdown(event: PointerEvent): void {
  if (open.value && root.value && !root.value.contains(event.target as Node)) close()
}

onMounted(() => document.addEventListener('pointerdown', handlePointerdown))
onBeforeUnmount(() => document.removeEventListener('pointerdown', handlePointerdown))
</script>

<template>
  <div
    ref="root"
    class="tag-filter"
    :class="{ 'tag-filter--open': open, 'tag-filter--active': modelValue.length > 0 }"
  >
    <button
      type="button"
      class="tag-filter__trigger"
      role="combobox"
      aria-haspopup="listbox"
      :aria-expanded="open"
      :aria-label="ariaLabel"
      :title="modelValue.length ? modelValue.join('、') : allLabel"
      @click="toggleOpen"
    >
      <span :class="modelValue.length ? 'tag-filter__value' : 'tag-filter__placeholder'">{{ summary }}</span>
      <svg class="tag-filter__chevron" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="m6 9 6 6 6-6" /></svg>
    </button>

    <div v-if="open" class="tag-filter__menu">
      <div class="tag-filter__search">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><circle cx="11" cy="11" r="7" /><path d="m20 20-3.5-3.5" /></svg>
        <input
          ref="searchInput"
          v-model="query"
          type="search"
          class="tag-filter__search-input"
          :placeholder="t('projects.tagFilterSearch')"
          :aria-label="t('projects.tagFilterSearch')"
          autocomplete="off"
          @keydown="handleSearchKeydown"
        >
      </div>

      <div class="tag-filter__list" role="listbox" aria-multiselectable="true" :aria-label="ariaLabel">
        <button
          v-for="(tag, index) in filtered"
          :key="tag"
          type="button"
          class="tag-filter__option"
          :class="{ 'tag-filter__option--active': index === activeIndex, 'tag-filter__option--selected': isSelected(tag) }"
          role="option"
          :aria-selected="isSelected(tag)"
          @mouseenter="activeIndex = index"
          @click="toggleTag(tag)"
        >
          <span class="tag-filter__check" aria-hidden="true">
            <svg v-if="isSelected(tag)" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3"><path d="m5 12 4 4L19 6" /></svg>
          </span>
          <span class="tag-filter__label">{{ tag }}</span>
        </button>
        <div v-if="!filtered.length" class="tag-filter__empty">{{ t('projects.tagFilterEmpty') }}</div>
      </div>

      <div class="tag-filter__footer">
        <span class="tag-filter__hint">{{ t('projects.tagFilterMultiHint') }}</span>
        <button v-if="modelValue.length" type="button" class="tag-filter__clear" @click="clearAll">
          {{ t('projects.clearFilter') }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.tag-filter { position: relative; width: 186px; }
.tag-filter__trigger {
  position: relative; display: flex; align-items: center; width: 100%; height: 36px; min-height: 0;
  padding: 8px 34px 8px 12px; border: 1px solid var(--color-border); border-radius: var(--rounded);
  background: var(--color-card-2); color: var(--color-text); font: inherit; font-size: 0.83rem; text-align: left;
  cursor: pointer; transition: border-color var(--duration-fast), box-shadow var(--duration-fast), background var(--duration-fast);
}
.tag-filter__trigger:hover { border-color: var(--color-border-strong); background: var(--color-inset); }
.tag-filter--open .tag-filter__trigger,
.tag-filter__trigger:focus-visible { outline: none; border-color: var(--color-primary); box-shadow: 0 0 0 3px var(--color-primary-soft); }
.tag-filter__value, .tag-filter__placeholder { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tag-filter__placeholder { color: var(--color-faint); }
.tag-filter--active .tag-filter__trigger { border-color: var(--color-primary); color: var(--color-primary); }
.tag-filter__chevron { position: absolute; right: 11px; color: var(--color-faint); transition: transform var(--duration-fast), color var(--duration-fast); }
.tag-filter--open .tag-filter__chevron { transform: rotate(180deg); color: var(--color-primary); }

.tag-filter__menu {
  position: absolute; z-index: 30; top: calc(100% + 6px); left: 0; width: 248px;
  border: 1px solid var(--color-border-strong); border-radius: var(--rounded);
  background: var(--color-card-2); box-shadow: 0 12px 28px rgb(0 0 0 / 24%);
  overflow: hidden;
}
.tag-filter__search {
  display: flex; align-items: center; gap: 8px; padding: 8px 10px;
  border-bottom: 1px solid var(--color-border); color: var(--color-faint);
}
.tag-filter__search-input {
  flex: 1; min-width: 0; height: 28px; padding: 0; border: 0; background: transparent;
  color: var(--color-text); font: inherit; font-size: 0.8rem; outline: none;
}
.tag-filter__search-input::placeholder { color: var(--color-faint); }
.tag-filter__search-input::-webkit-search-cancel-button { -webkit-appearance: none; }

.tag-filter__list { max-height: 240px; overflow-y: auto; padding: 5px; }
.tag-filter__option {
  display: flex; align-items: center; gap: 8px; width: 100%; min-height: 34px;
  padding: 6px 9px; border: 0; border-radius: calc(var(--rounded) - 2px);
  background: transparent; color: var(--color-text); font: inherit; font-size: 0.8rem;
  text-align: left; cursor: pointer;
}
.tag-filter__option:hover, .tag-filter__option--active { background: var(--color-inset); }
.tag-filter__option--selected { color: var(--color-primary); }
.tag-filter__check {
  display: inline-flex; align-items: center; justify-content: center; flex: 0 0 auto;
  width: 16px; height: 16px; border: 1.5px solid var(--color-border-strong); border-radius: 4px;
  color: var(--color-on-primary, #fff);
}
.tag-filter__option--selected .tag-filter__check { background: var(--color-primary); border-color: var(--color-primary); }
.tag-filter__label { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tag-filter__empty { padding: 10px 9px; color: var(--color-faint); font-size: 0.78rem; }

.tag-filter__footer {
  display: flex; align-items: center; justify-content: space-between; gap: 8px;
  padding: 7px 10px; border-top: 1px solid var(--color-border);
}
.tag-filter__hint { color: var(--color-faint); font-size: 0.7rem; line-height: 1.4; }
.tag-filter__clear {
  flex: 0 0 auto; padding: 0; border: 0; background: none;
  color: var(--color-primary); font: inherit; font-size: 0.74rem; cursor: pointer;
}
.tag-filter__clear:hover { text-decoration: underline; }
</style>