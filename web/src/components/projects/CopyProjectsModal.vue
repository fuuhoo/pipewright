<script setup lang="ts">
/**
 * 复制项目弹窗 —— 复制一份与复制一批共用这一个壳。
 *
 * 为什么不做成两个组件:两种情况要问用户的其实是同一件事(副本叫什么、落在哪个组),
 * 差别只有一份时能手写名字。拆成两个壳,迟早一边加了字段另一边忘加,而这两个端点的
 * 请求体形状本来就是一样的。
 *
 * 「只搬配置、不搬历史」这句必须在弹窗里说明:副本的流水线、定时、参数、通知整套照抄,
 * 运行记录一条不带。不说清的话用户会以为新环境里也能翻到上次发布的日志。
 */
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { copyProject, copyProjects, type CopiedProject, type Project } from '../../api/projects'
import type { Group } from '../../api/groups'
import { HttpError } from '../../api/http'
import AppModal from '../ui/AppModal.vue'
import AppButton from '../ui/AppButton.vue'
import FormField from '../ui/FormField.vue'
import {
  buildCopyBody,
  buildCopyManyBody,
  copyErrorMessage,
  copyRows,
  emptyCopyForm,
  selectAllIds,
  targetGroupOptions,
  toggleSelected,
  type CopyForm,
} from '../../lib/projectCopy'

const props = defineProps<{
  open: boolean
  /** 这次要复制的源项目;一份(卡片上的「复制」)或多份(组头的「复制整组」)。 */
  sources: Project[]
  groups: Group[]
}>()

const emit = defineEmits<{
  'update:open': [value: boolean]
  copied: [created: CopiedProject[]]
}>()

const { t } = useI18n()

const form = ref<CopyForm>(emptyCopyForm())
const picked = ref<string[]>([])
const banner = ref('')
const submitting = ref(false)

/** 一份走单个端点(能改名、审计逐条),多份走批量端点(一个事务、审计一行)。 */
const isBatch = computed(() => props.sources.length > 1)

const rows = computed(() => copyRows(props.sources, form.value))
const selectedRows = computed(() => rows.value.filter((r) => picked.value.includes(r.sourceId)))
const selectedIds = computed(() => selectedRows.value.map((r) => r.sourceId))
const allPicked = computed(() => props.sources.length > 0 && selectedRows.value.length === props.sources.length)

const groupCandidates = computed(() =>
  targetGroupOptions(props.groups, {
    keep: t('projects.copyGroupKeep'),
    ungrouped: t('groups.ungrouped'),
    public: t('groups.visibilityPublic'),
    private: t('groups.visibilityPrivate'),
  }),
)

// 每次打开都从干净状态开始:调用方只给 sources + open,命名与勾选归弹窗自己管。
watch(
  () => [props.open, props.sources.length] as const,
  ([open]) => {
    if (!open) return
    form.value = emptyCopyForm()
    picked.value = selectAllIds(props.sources, true)
    banner.value = ''
    submitting.value = false
  },
  { immediate: true },
)

function close(): void {
  if (submitting.value) return
  emit('update:open', false)
}

function toggle(id: string): void {
  picked.value = toggleSelected(picked.value, id)
}

function pickAll(): void {
  picked.value = selectAllIds(props.sources, !allPicked.value)
}

const errorLabels = computed(() => ({
  forbidden: t('projects.copyErrForbidden'),
  sourceGone: t('projects.copyErrSourceGone'),
  tooMany: t('projects.copyErrTooMany'),
  groupGone: t('projects.copyErrGroupGone'),
  network: t('projects.errNetwork'),
}))

async function submit(): Promise<void> {
  banner.value = ''
  // 一份时 sources 就是那一个;批量只发勾中的 —— 没勾就等于用户不想复制它,不能替他想。
  const sources = isBatch.value
    ? props.sources.filter((p) => picked.value.includes(p.id))
    : props.sources.slice(0, 1)
  if (sources.length === 0) {
    banner.value = t('projects.copyErrNothing')
    return
  }

  submitting.value = true
  try {
    const created =
      sources.length === 1
        ? await copyProject(sources[0].id, buildCopyBody(form.value))
        : await copyProjects(buildCopyManyBody(sources, form.value))
    emit('update:open', false)
    emit('copied', created)
  } catch (err) {
    if (err instanceof HttpError) {
      banner.value = copyErrorMessage(
        err.apiError?.code ?? null,
        err.status,
        err.apiError?.message ?? t('projects.copyErrStatus', { status: err.status }),
        errorLabels.value,
      )
    } else {
      banner.value = t('projects.copyErrRetry')
    }
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <AppModal
    v-if="open && sources.length"
    :title="isBatch ? t('projects.copyBatchTitle') : t('projects.copyTitle')"
    :subtitle="isBatch ? t('projects.copyBatchSub', { n: sources.length }) : t('projects.copySub', { name: sources[0].name })"
    width="lg"
    :busy="submitting"
    @close="close"
    @submit="submit"
  >
    <p v-if="banner" class="banner banner--err" role="alert">{{ banner }}</p>

    <p class="copy-note">{{ t('projects.copyNote') }}</p>

    <!-- 一份:名字可以手写;批量时这个字段不出现,因为一个名字建 N 份同名项目是事故 -->
    <FormField
      v-if="!isBatch"
      :label="t('projects.copyNameField')"
      field-id="copy-name"
      :hint="t('projects.copyNameHint', { name: rows[0].copyName })"
    >
      <template #default="{ fieldId }">
        <input
          :id="fieldId"
          v-model="form.name"
          class="ui-input"
          type="text"
          autocomplete="off"
          :placeholder="rows[0].copyName"
          :disabled="submitting"
        />
      </template>
    </FormField>

    <FormField
      :label="t('projects.copySuffixField')"
      field-id="copy-suffix"
      :hint="t('projects.copySuffixHint')"
    >
      <template #default="{ fieldId }">
        <input
          :id="fieldId"
          v-model="form.suffix"
          class="ui-input"
          type="text"
          autocomplete="off"
          :disabled="submitting"
        />
      </template>
    </FormField>

    <FormField
      :label="t('projects.copyGroupField')"
      field-id="copy-group"
      :hint="t('projects.copyGroupHint')"
    >
      <template #default="{ fieldId }">
        <select
          :id="fieldId"
          v-model="form.target"
          class="ui-input"
          :disabled="submitting"
        >
          <option v-for="opt in groupCandidates" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
        </select>
      </template>
    </FormField>

    <!-- 一批:勾谁复制谁,右边直接看得到每个副本会叫什么 -->
    <div v-if="isBatch" class="copy-list">
      <div class="copy-list-head">
        <label class="copy-all">
          <input type="checkbox" :checked="allPicked" :disabled="submitting" @change="pickAll" />
          {{ t('projects.copySelectAll') }}
        </label>
        <span class="copy-count" aria-live="polite">
          {{ t('projects.copyPickedCount', { picked: selectedIds.length, total: sources.length }) }}
        </span>
      </div>
      <ul class="copy-rows" role="list">
        <li
          v-for="row in rows"
          :key="row.sourceId"
          class="copy-row"
          :class="{ 'copy-row--off': !picked.includes(row.sourceId) }"
        >
          <label class="copy-check">
            <input
              type="checkbox"
              :checked="picked.includes(row.sourceId)"
              :disabled="submitting"
              :aria-label="t('projects.copyPickAria', { name: row.sourceName })"
              @change="toggle(row.sourceId)"
            />
          </label>
          <span class="copy-src" :title="row.sourceName">{{ row.sourceName }}</span>
          <span class="copy-arrow" aria-hidden="true">→</span>
          <span class="copy-dst" :title="row.copyName">{{ row.copyName }}</span>
        </li>
      </ul>
    </div>

    <template #actions>
      <AppButton variant="ghost" :disabled="submitting" @click="close">{{ t('projects.cancel') }}</AppButton>
      <AppButton
        variant="primary"
        type="submit"
        :loading="submitting"
        :disabled="isBatch && selectedIds.length === 0"
      >
        {{ isBatch ? t('projects.copyGoBatch', { n: selectedIds.length }) : t('projects.copyGo') }}
      </AppButton>
    </template>
  </AppModal>
</template>

<style scoped>
.copy-note {
  font-size: 0.78rem;
  color: var(--color-faint);
  line-height: 1.5;
  margin: 0 0 4px;
}

.copy-list {
  border: 1px solid var(--color-border);
  border-radius: var(--rounded);
  background: var(--color-inset);
  overflow: hidden;
}

.copy-list-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  padding: 8px 12px;
  border-bottom: 1px solid var(--color-border);
}

.copy-all {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  font-size: 0.78rem;
  color: var(--color-dim);
  cursor: pointer;
}

.copy-count {
  font-size: 0.75rem;
  color: var(--color-faint);
  white-space: nowrap;
}

.copy-rows {
  list-style: none;
  margin: 0;
  padding: 4px 0;
  max-height: 260px;
  overflow-y: auto;
}

.copy-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 5px 12px;
  min-width: 0;
}

.copy-row--off {
  opacity: 0.45;
}

.copy-check {
  display: inline-flex;
  align-items: center;
  flex-shrink: 0;
}

.copy-src,
.copy-dst {
  font-size: 0.8rem;
  color: var(--color-text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  min-width: 0;
  flex: 1;
}

.copy-dst {
  color: var(--color-primary);
}

.copy-arrow {
  flex-shrink: 0;
  color: var(--color-faint);
  font-size: 0.8rem;
}
</style>
