<script setup lang="ts">
/**
 * 功能点树:分组(kind)作父节点、功能点作叶子的两层树。
 *
 * 两个用法共用一份渲染,避免「角色编辑器」与「账号继承权限」两处各写一版分组逻辑:
 *   - 可编辑(:readonly 省略):勾选框 + 组级全选,写回 v-model:selected。
 *   - 只读(:readonly):只显示已授予的叶子,并可在每个点后面标出「由哪个角色给出」。
 *
 * 分组顺序与标题来自 lib/roleEditor 的 KIND_ORDER / KIND_LABEL_KEY,与后端 perms.go 的
 * 分段注释一致;字典里冒出没见过的 kind 时归到末尾,宁可多一行也别让入口静默消失。
 *
 * settings.access(builtinOnly)一律渲染成禁用项:后端 422,这里不让人撞。
 */
import { computed, reactive, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { PermId } from '../../api/auth'
import type { PermPoint, RoleId } from '../../api/roles'
import { groupPoints, permLabelKey } from '../../lib/roleEditor'

const props = withDefaults(
  defineProps<{
    points: PermPoint[]
    selected: PermId[]
    readonly?: boolean
    disabled?: boolean
    /** 只读树的叶子来源(PermId → 角色 id),按角色顺序展示。 */
    sources?: Record<string, RoleId[]>
    roleLabel?: (id: RoleId) => string
    /** 读态在角色列表里默认收起(一行一个角色,全展开会把表撑成墙);弹窗里默认展开。 */
    initiallyExpanded?: boolean
  }>(),
  {
    readonly: false,
    disabled: false,
    sources: () => ({}),
    roleLabel: undefined,
    initiallyExpanded: true,
  },
)

const emit = defineEmits<{
  (e: 'update:selected', ids: PermId[]): void
}>()

const { t } = useI18n()

const groups = computed(() => groupPoints(props.points))
const picked = computed(() => new Set(props.selected))

/** 读态只留下「有授予点」的分组:没授的整块不出现,树才是「他继承到了什么」。 */
const visibleGroups = computed(() =>
  props.readonly ? groups.value.filter((g) => countIn(g.points) > 0) : groups.value,
)

const open = reactive<Record<string, boolean>>({})

watch(
  () => groups.value.map((g) => g.kind).join(','),
  () => {
    for (const g of groups.value) {
      if (open[g.kind] === undefined) open[g.kind] = props.initiallyExpanded
    }
  },
  { immediate: true },
)

function isOpen(kind: string): boolean {
  return open[kind] === true
}

function toggleOpen(kind: string): void {
  open[kind] = !isOpen(kind)
}

/** 读态下叶子只列已授予的;编辑态列全量。 */
function leafPoints(points: PermPoint[]): PermPoint[] {
  return props.readonly ? points.filter((p) => picked.value.has(p.id)) : points
}

function countIn(points: PermPoint[]): number {
  return points.filter((p) => picked.value.has(p.id)).length
}

function allPicked(points: PermPoint[]): boolean {
  const assignable = points.filter((p) => !p.builtinOnly)
  return assignable.length > 0 && assignable.every((p) => picked.value.has(p.id))
}

function sourcesFor(id: PermId): RoleId[] {
  return props.sources[id] ?? []
}

function togglePoint(id: PermId, on: boolean): void {
  if (on) {
    if (picked.value.has(id)) return
    emit('update:selected', [...props.selected, id])
    return
  }
  emit(
    'update:selected',
    props.selected.filter((x) => x !== id),
  )
}

/** 组级勾选:开 = 补齐该组可授予点(不动 builtinOnly),关 = 清掉该组可授予点。 */
function toggleGroup(points: PermPoint[], on: boolean): void {
  const ids = points.filter((p) => !p.builtinOnly).map((p) => p.id)
  if (on) {
    const current = new Set(props.selected)
    emit('update:selected', [...props.selected, ...ids.filter((id) => !current.has(id))])
    return
  }
  const off = new Set(ids)
  emit(
    'update:selected',
    props.selected.filter((id) => !off.has(id)),
  )
}
</script>

<template>
  <div class="perm-tree" :class="{ 'perm-tree--read': readonly }">
    <div v-for="g in visibleGroups" :key="g.kind" class="pt-group">
      <div class="pt-head">
        <button
          type="button"
          class="pt-caret"
          :aria-expanded="isOpen(g.kind)"
          :aria-label="isOpen(g.kind) ? t('roleEditor.treeCollapse') : t('roleEditor.treeExpand')"
          @click="toggleOpen(g.kind)"
        >
          <span class="pt-caret-icon" aria-hidden="true">{{ isOpen(g.kind) ? '▾' : '▸' }}</span>
          <span class="pt-group-title">{{ g.labelKey ? t(g.labelKey) : g.kind }}</span>
        </button>
        <label v-if="!readonly" class="pt-group-check" :title="t('roleEditor.treeGroupAll')">
          <input
            type="checkbox"
            :checked="allPicked(g.points)"
            :disabled="disabled"
            @change="toggleGroup(g.points, ($event.target as HTMLInputElement).checked)"
          />
        </label>
        <span class="pt-count">{{ countIn(g.points) }} / {{ g.points.length }}</span>
      </div>

      <p v-if="isOpen(g.kind) && g.hintKey" class="pt-hint">{{ t(g.hintKey) }}</p>

      <ul v-if="isOpen(g.kind)" class="pt-leaves">
        <li v-for="p in leafPoints(g.points)" :key="p.id">
          <label v-if="!readonly" class="pt-leaf" :class="{ 'pt-leaf--locked': p.builtinOnly }">
            <input
              type="checkbox"
              :checked="picked.has(p.id)"
              :disabled="disabled || p.builtinOnly"
              @change="togglePoint(p.id, ($event.target as HTMLInputElement).checked)"
            />
            <span class="pt-leaf-label">
              {{ t(permLabelKey(p.id)) }}
              <span v-if="p.builtinOnly" class="pt-tag pt-tag--locked">
                {{ t('roleEditor.settingsOnly') }}
              </span>
            </span>
          </label>
          <div v-else class="pt-leaf pt-leaf--read">
            <span class="pt-leaf-label">{{ t(permLabelKey(p.id)) }}</span>
            <span v-if="sourcesFor(p.id).length" class="pt-sources">
              <span v-for="rid in sourcesFor(p.id)" :key="rid" class="pt-tag pt-tag--role">
                {{ roleLabel ? roleLabel(rid) : rid }}
              </span>
            </span>
          </div>
        </li>
      </ul>
    </div>
  </div>
</template>

<style scoped>
.perm-tree {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.pt-group {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm, 6px);
  padding: 6px 10px;
}
.pt-head {
  display: flex;
  align-items: center;
  gap: 8px;
}
.pt-caret {
  display: flex;
  align-items: center;
  gap: 6px;
  flex: 1;
  min-width: 0;
  padding: 2px 0;
  border: none;
  background: transparent;
  color: var(--color-text);
  font: inherit;
  text-align: left;
  cursor: pointer;
}
.pt-caret-icon {
  width: 1em;
  color: var(--color-faint);
}
.pt-group-title {
  font-size: var(--text-label);
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.pt-group-check input,
.pt-leaf input {
  cursor: pointer;
}
.pt-count {
  font-size: var(--text-small, 0.8em);
  color: var(--color-faint);
  white-space: nowrap;
}
.pt-hint {
  margin: 4px 0 2px 1.4em;
  font-size: var(--text-small, 0.8em);
  color: var(--color-faint);
  max-width: 72ch;
}
/* 叶子缩进挂在 ul 上,读态与编辑态同一缩进,树的两层关系才看得出来。 */
.pt-leaves {
  list-style: none;
  margin: 2px 0 2px 0;
  padding: 0 0 0 1.4em;
  display: flex;
  flex-direction: column;
  gap: 1px;
}
.pt-leaf {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 2px 0;
  font-size: var(--text-label);
  color: var(--color-text);
  cursor: pointer;
}
.pt-leaf--read {
  cursor: default;
  flex-wrap: wrap;
}
.pt-leaf--locked {
  cursor: not-allowed;
  opacity: 0.6;
}
.pt-leaf-label {
  display: flex;
  align-items: center;
  gap: 6px;
}
.pt-sources {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  margin-left: auto;
}
.pt-tag {
  display: inline-block;
  padding: 0 7px;
  border-radius: 999px;
  font-size: var(--text-small, 0.75em);
  font-weight: 600;
  line-height: 1.6;
}
.pt-tag--locked {
  background: rgba(100, 116, 139, 0.16);
  color: #475569;
}
.pt-tag--role {
  background: rgba(139, 92, 246, 0.14);
  color: #6d28d9;
}
/* 列表单元格里的一棵只读树:窄一点,次要信息不抢戏。 */
.perm-tree--read {
  font-size: var(--text-small, 0.9em);
}
</style>
