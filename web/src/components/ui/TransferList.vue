<script setup lang="ts">
/**
 * TransferList — 左右双栏穿梭选择器(自带搜索)。
 *
 * 存在理由:成员勾选列表在用户多起来之后要滚很久才能找到人。穿梭框把「可选 / 已选」分成
 * 两栏、各自带搜索,选中后点箭头或回车即可移动;右侧已选项还能按名字再筛一遍。
 *
 * 约定:
 *   · v-model 只保存「可移出」的已选 id;lockedIds(如组长)永远留在右栏、勾选框锁死。
 *   · 左右勾选是各自独立的临时集合,移动后清空;搜索只筛选显示,不会误移走看不见的项。
 *   · preserveOrder:已选列表按 v-model 的原顺序展示,新加入的追加在末尾。角色场景需要它,
 *     首个角色是主角色;默认按 options 顺序重排,成员这类无序集合不会因加入先后跳位。
 *   · readonly:只留两栏名单与搜索,没有勾选框和箭头。「这个角色的账号」用它 —— 名单是查的,
 *     改派要回账号 tab,不在这里顺手改。
 *   · 全站统一:两栏各自分页(默认 8 条/页)、自带搜索、候选项按 createdAt 倒序(最新的在前)。
 *     搜索会回到第 1 页,箭头只移动当前页勾中的项;没有 createdAt 的项维持传入顺序。
 *   · 所有按钮都显式 type="button":这个组件会出现在 <form> 里,不能触发表单提交。
 */
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

export interface TransferOption {
  id: string
  label: string
  /** 行尾的次要信息(如成员数),不参与搜索。 */
  meta?: string
  /** 创建时间(ISO 字符串);有它时两栏都按「最新的在前」排列。 */
  createdAt?: string
}

const props = withDefaults(
  defineProps<{
    modelValue: string[]
    options: TransferOption[]
    /** 永远在右栏且不可移出的 id(例如组长)。 */
    lockedIds?: string[]
    /** 锁定项右侧的小标签文案;为空则不显示标签。 */
    lockedLabel?: string
    /** 可选栏里置灰、不可移入的 id(例如不归你管、加不进去的分组)。 */
    disabledIds?: string[]
    /** 已选栏按 v-model 顺序展示,新加入的追加在末尾(顺序有语义时用)。 */
    preserveOrder?: boolean
    disabled?: boolean
    /** 只读:隐藏勾选框与箭头,只供查看名单。 */
    readonly?: boolean
    /** 每页条数;0 = 不分页。默认 8,全站统一。 */
    pageSize?: number
    /** 左栏标题(默认「可选」)。只读名单里换成「其他账号」这类说法。 */
    sourceTitle?: string
    /** 右栏标题(默认「已选」)。 */
    targetTitle?: string
  }>(),
  {
    lockedIds: () => [],
    lockedLabel: '',
    disabledIds: () => [],
    preserveOrder: false,
    disabled: false,
    readonly: false,
    pageSize: 8,
    sourceTitle: '',
    targetTitle: '',
  },
)

const emit = defineEmits<{ 'update:modelValue': [string[]] }>()

const { t } = useI18n()

const searchLeft = ref('')
const searchRight = ref('')
const leftChecked = ref<string[]>([])
const rightChecked = ref<string[]>([])
const pageLeft = ref(1)
const pageRight = ref(1)

const lockedSet = computed(() => new Set(props.lockedIds))
const disabledSet = computed(() => new Set(props.disabledIds))
const selectedSet = computed(() => new Set([...props.modelValue, ...props.lockedIds]))

function stampOf(o: TransferOption): number {
  const n = o.createdAt ? Date.parse(o.createdAt) : NaN
  return Number.isFinite(n) ? n : -Infinity
}

/**
 * 最新的在前:按 createdAt 倒序;缺时间的项排在后面并保持传入顺序。
 * 排序是稳定的,所以同一时刻创建的一批人不会每次刷新都换位置。
 */
const sortedOptions = computed(() => {
  if (!props.options.some((o) => o.createdAt)) return props.options
  return [...props.options].sort((a, b) => stampOf(b) - stampOf(a))
})

function filterBy(list: TransferOption[], query: string): TransferOption[] {
  const q = query.trim().toLowerCase()
  return q ? list.filter((o) => o.label.toLowerCase().includes(q)) : list
}

const availableOptions = computed(() => sortedOptions.value.filter((o) => !selectedSet.value.has(o.id)))
const selectedOptions = computed(() => {
  if (!props.preserveOrder) return sortedOptions.value.filter((o) => selectedSet.value.has(o.id))
  const byId = new Map(sortedOptions.value.map((o) => [o.id, o]))
  const ordered: TransferOption[] = []
  for (const id of [...props.modelValue, ...props.lockedIds]) {
    const option = byId.get(id)
    if (option) ordered.push(option)
  }
  return ordered
})

const availableShown = computed(() => filterBy(availableOptions.value, searchLeft.value))
const selectedShown = computed(() => filterBy(selectedOptions.value, searchRight.value))

function pageCount(total: number): number {
  if (props.pageSize <= 0) return 1
  return Math.max(1, Math.ceil(total / props.pageSize))
}
function pageSlice<T>(list: T[], page: number): T[] {
  if (props.pageSize <= 0) return list
  const start = (page - 1) * props.pageSize
  return list.slice(start, start + props.pageSize)
}

const pagesLeft = computed(() => pageCount(availableShown.value.length))
const pagesRight = computed(() => pageCount(selectedShown.value.length))
const pageLeftClamped = computed(() => Math.min(pageLeft.value, pagesLeft.value))
const pageRightClamped = computed(() => Math.min(pageRight.value, pagesRight.value))

const leftShown = computed(() => pageSlice(availableShown.value, pageLeftClamped.value))
const rightShown = computed(() => pageSlice(selectedShown.value, pageRightClamped.value))

// 搜索把结果换掉后停在第 3 页说不通;数据变少(移走/过滤)时把页码拉回最后一页。
watch(searchLeft, () => {
  pageLeft.value = 1
})
watch(searchRight, () => {
  pageRight.value = 1
})
watch(pagesLeft, (n) => {
  if (pageLeft.value > n) pageLeft.value = n
})
watch(pagesRight, (n) => {
  if (pageRight.value > n) pageRight.value = n
})

/** 箭头只作用于当前页看得见的勾选项:翻到别页后不会悄悄带走上一页选的人。 */
const leftPicked = computed(() => leftChecked.value.filter((id) => leftShown.value.some((o) => o.id === id)))
const rightPicked = computed(() => rightChecked.value.filter((id) => rightShown.value.some((o) => o.id === id)))

/** 按 options 顺序重排 v-model:同一批人不会因为加入先后而反复跳位。 */
function moveRight(): void {
  if (props.disabled || props.readonly) return
  const picked = new Set(leftPicked.value)
  const next = props.preserveOrder
    ? [
        ...props.modelValue,
        ...sortedOptions.value
          .filter((o) => picked.has(o.id) && !selectedSet.value.has(o.id))
          .map((o) => o.id),
      ]
    : sortedOptions.value
        .filter((o) => !lockedSet.value.has(o.id))
        .filter((o) => selectedSet.value.has(o.id) || picked.has(o.id))
        .map((o) => o.id)
  emit('update:modelValue', next)
  leftChecked.value = leftChecked.value.filter((id) => !picked.has(id))
}

function moveLeft(): void {
  if (props.disabled || props.readonly) return
  const picked = new Set(rightPicked.value)
  emit(
    'update:modelValue',
    props.modelValue.filter((id) => !picked.has(id)),
  )
  rightChecked.value = rightChecked.value.filter((id) => !picked.has(id))
}
</script>

<template>
  <div class="transfer" :class="{ 'transfer--readonly': readonly }">
    <section
      class="transfer__panel transfer__panel--source"
      role="group"
      :aria-label="sourceTitle || t('common.transfer.available')"
    >
      <header class="transfer__head">
        <span>{{ sourceTitle || t('common.transfer.available') }}</span>
        <span class="transfer__count">{{ availableShown.length }}</span>
      </header>
      <div class="transfer__search">
        <input
          v-model="searchLeft"
          class="transfer__search-input"
          type="search"
          autocomplete="off"
          :placeholder="t('common.transfer.searchAvailable')"
          :aria-label="t('common.transfer.searchAvailable')"
          :disabled="disabled"
        />
      </div>
      <ul class="transfer__list" :class="{ 'transfer__list--paged': pageSize > 0 }" role="list">
        <li v-for="o in leftShown" :key="o.id">
          <label class="transfer__item" :class="{ 'transfer__item--disabled': disabledSet.has(o.id) }">
            <input
              v-if="!readonly"
              v-model="leftChecked"
              type="checkbox"
              :value="o.id"
              :disabled="disabled || disabledSet.has(o.id)"
            />
            <span class="transfer__name" :title="o.label">{{ o.label }}</span>
            <span v-if="o.meta" class="transfer__meta">{{ o.meta }}</span>
          </label>
        </li>
        <li v-if="!leftShown.length" class="transfer__empty">
          {{ searchLeft.trim() ? t('common.transfer.noMatch') : t('common.transfer.emptyAvailable') }}
        </li>
      </ul>
      <div v-if="pagesLeft > 1" class="transfer__pager">
        <button
          type="button"
          class="transfer__page-btn"
          :disabled="pageLeftClamped <= 1"
          :aria-label="t('common.transfer.prevPage')"
          :title="t('common.transfer.prevPage')"
          @click="pageLeft = pageLeftClamped - 1"
        >
          ‹
        </button>
        <span class="transfer__page-label">
          {{ t('common.transfer.pageOf', { page: pageLeftClamped, pages: pagesLeft }) }}
        </span>
        <button
          type="button"
          class="transfer__page-btn"
          :disabled="pageLeftClamped >= pagesLeft"
          :aria-label="t('common.transfer.nextPage')"
          :title="t('common.transfer.nextPage')"
          @click="pageLeft = pageLeftClamped + 1"
        >
          ›
        </button>
      </div>
    </section>

    <div v-if="!readonly" class="transfer__actions">
      <button
        type="button"
        class="transfer__btn"
        :disabled="disabled || !leftPicked.length"
        :aria-label="t('common.transfer.add')"
        :title="t('common.transfer.add')"
        @click="moveRight"
      >
        →
      </button>
      <button
        type="button"
        class="transfer__btn"
        :disabled="disabled || !rightPicked.length"
        :aria-label="t('common.transfer.remove')"
        :title="t('common.transfer.remove')"
        @click="moveLeft"
      >
        ←
      </button>
    </div>

    <section
      class="transfer__panel transfer__panel--target"
      role="group"
      :aria-label="targetTitle || t('common.transfer.selected')"
    >
      <header class="transfer__head">
        <span>{{ targetTitle || t('common.transfer.selected') }}</span>
        <span class="transfer__count">{{ selectedShown.length }}</span>
      </header>
      <div class="transfer__search">
        <input
          v-model="searchRight"
          class="transfer__search-input"
          type="search"
          autocomplete="off"
          :placeholder="t('common.transfer.searchSelected')"
          :aria-label="t('common.transfer.searchSelected')"
          :disabled="disabled"
        />
      </div>
      <ul class="transfer__list" :class="{ 'transfer__list--paged': pageSize > 0 }" role="list">
        <li v-for="o in rightShown" :key="o.id">
          <label class="transfer__item" :class="{ 'transfer__item--locked': lockedSet.has(o.id) }">
            <template v-if="!readonly">
              <input v-if="lockedSet.has(o.id)" type="checkbox" checked disabled />
              <input v-else v-model="rightChecked" type="checkbox" :value="o.id" :disabled="disabled" />
            </template>
            <span class="transfer__name" :title="o.label">{{ o.label }}</span>
            <span v-if="o.meta" class="transfer__meta">{{ o.meta }}</span>
            <span v-if="lockedSet.has(o.id) && lockedLabel" class="transfer__tag">{{ lockedLabel }}</span>
          </label>
        </li>
        <li v-if="!rightShown.length" class="transfer__empty">
          {{ searchRight.trim() ? t('common.transfer.noMatch') : t('common.transfer.emptySelected') }}
        </li>
      </ul>
      <div v-if="pagesRight > 1" class="transfer__pager">
        <button
          type="button"
          class="transfer__page-btn"
          :disabled="pageRightClamped <= 1"
          :aria-label="t('common.transfer.prevPage')"
          :title="t('common.transfer.prevPage')"
          @click="pageRight = pageRightClamped - 1"
        >
          ‹
        </button>
        <span class="transfer__page-label">
          {{ t('common.transfer.pageOf', { page: pageRightClamped, pages: pagesRight }) }}
        </span>
        <button
          type="button"
          class="transfer__page-btn"
          :disabled="pageRightClamped >= pagesRight"
          :aria-label="t('common.transfer.nextPage')"
          :title="t('common.transfer.nextPage')"
          @click="pageRight = pageRightClamped + 1"
        >
          ›
        </button>
      </div>
    </section>
  </div>
</template>

<style scoped>
.transfer {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto minmax(0, 1fr);
  gap: var(--space-3);
  align-items: stretch;
}
.transfer--readonly {
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
}
.transfer__panel {
  display: flex;
  flex-direction: column;
  min-width: 0;
  border: 1px solid var(--color-border-strong, var(--color-border));
  border-radius: var(--rounded);
  background: var(--color-inset);
  overflow: hidden;
}
.transfer__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: var(--space-2) var(--space-3);
  font-size: var(--text-label);
  font-weight: 600;
  color: var(--color-dim);
  border-bottom: 1px solid var(--color-border);
}
.transfer__count {
  color: var(--color-faint);
  font-weight: 500;
  font-variant-numeric: tabular-nums;
}
.transfer__search {
  padding: var(--space-2);
}
.transfer__search-input {
  display: block;
  width: 100%;
  height: 32px;
  padding: 0 var(--space-2);
  font-family: var(--font-sans);
  font-size: var(--text-label);
  color: var(--color-text);
  background: var(--color-surface);
  border: 1px solid var(--color-border-strong, var(--color-border));
  border-radius: var(--rounded-sm, 4px);
}
.transfer__search-input:focus {
  outline: none;
  border-color: var(--color-primary);
  box-shadow: 0 0 0 3px var(--color-primary-soft);
}
.transfer__search-input:disabled {
  opacity: 0.5;
}
.transfer__list {
  list-style: none;
  margin: 0;
  padding: 0 var(--space-1) var(--space-1);
  height: 200px;
  overflow-y: auto;
}
/* 分页后每页条数固定,不再叠一层内部滚动。 */
.transfer__list--paged {
  height: auto;
  min-height: 200px;
}
.transfer__pager {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: var(--space-2);
  padding: 0 var(--space-2) var(--space-2);
}
.transfer__page-btn {
  width: 26px;
  height: 24px;
  font: inherit;
  line-height: 1;
  color: var(--color-text);
  background: var(--color-surface);
  border: 1px solid var(--color-border-strong, var(--color-border));
  border-radius: var(--rounded-sm, 4px);
  cursor: pointer;
}
.transfer__page-btn:hover:not(:disabled) {
  border-color: var(--color-primary);
  color: var(--color-primary);
}
.transfer__page-btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}
.transfer__page-label {
  font-size: var(--text-label);
  color: var(--color-faint);
  font-variant-numeric: tabular-nums;
}
.transfer__item {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  padding: 6px 8px;
  border-radius: var(--rounded-sm, 4px);
  font-size: var(--text-body);
  color: var(--color-text);
  cursor: pointer;
}
.transfer__item:hover {
  background: var(--color-surface);
}
.transfer__item--locked,
.transfer__item--disabled {
  cursor: default;
}
.transfer__item--disabled {
  opacity: 0.55;
}
.transfer__item input {
  flex: none;
  accent-color: var(--color-primary);
}
.transfer__name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.transfer__meta {
  margin-left: auto;
  font-size: 0.78em;
  color: var(--color-faint);
  white-space: nowrap;
}
.transfer__tag {
  margin-left: auto;
  padding: 0 5px;
  border-radius: var(--rounded-sm, 4px);
  font-size: 0.68rem;
  font-weight: 600;
  color: var(--color-dim);
  background: var(--color-surface);
}
.transfer__meta + .transfer__tag {
  margin-left: 0;
}
.transfer__actions {
  display: flex;
  flex-direction: column;
  justify-content: center;
  gap: var(--space-2);
}
.transfer__btn {
  width: 36px;
  height: 30px;
  font: inherit;
  color: var(--color-text);
  background: var(--color-surface);
  border: 1px solid var(--color-border-strong, var(--color-border));
  border-radius: var(--rounded-sm, 4px);
  cursor: pointer;
}
.transfer__btn:hover:not(:disabled) {
  border-color: var(--color-primary);
  color: var(--color-primary);
}
.transfer__btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}
.transfer__empty {
  padding: var(--space-4) var(--space-2);
  text-align: center;
  font-size: var(--text-label);
  color: var(--color-faint);
}
</style>
