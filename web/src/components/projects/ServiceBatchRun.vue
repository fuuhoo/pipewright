<script setup lang="ts">
/**
 * 归属服务下的「批量执行」面板 —— 选一个分组(或不分组),把这个服务下的项目逐个触发一遍。
 *
 * 三条规矩:
 *   - **候选只有「此刻看得见的项目」**。项目列表后端已按分组可见性收敛过,这里再按服务 +
 *     分组各筛一道;看不见的项目压根不进候选。
 *   - **走的是老端点,没有批量接口**。每个项目各自的默认分支、各自的触发权、审计里各自一行,
 *     复用 POST /api/projects/{id}/runs 就全有了 —— 前端只是顺序发完,并把每条回执单独摆出来。
 *     整批里有的成功有的失败是常态(权限逐条判),所以不做「全成功才算成」的原子性承诺。
 *   - **分支一律不发**。留空由服务端取项目自己的默认分支,前端猜一份分支快照下来,
 *     和点进该项目触发时看到的不一定一致。
 */
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Project } from '../../api/projects'
import type { Group } from '../../api/groups'
import type { Service } from '../../api/services'
import { triggerManual } from '../../api/runs'
import { HttpError } from '../../api/http'
import { GROUP_ALL, filterByGroup, groupLabel, groupOptions as buildGroupOptions } from '../../lib/groupFilter'
import { matchesService } from '../../lib/serviceFilter'
import AppButton from '../ui/AppButton.vue'

const props = defineProps<{
  service: Service
  /** 当前可见的全部项目(面板自己按服务 + 分组收敛候选)。 */
  projects: Project[]
  groups: Group[]
}>()

const emit = defineEmits<{
  'open-run': [runId: string]
}>()

const { t } = useI18n()

interface Outcome {
  state: 'ok' | 'fail'
  runId?: string
  message?: string
}

const groupFilter = ref<string>(GROUP_ALL)
const picked      = ref<string[]>([])
const outcomes    = ref<Record<string, Outcome>>({})
const running     = ref(false)
const doneCount   = ref(0)
const runTotal    = ref(0)

const groupLabels = computed(() => ({
  all: t('groups.filterAll'),
  ungrouped: t('groups.ungrouped'),
  missing: t('groups.groupMissing'),
  public: t('groups.visibilityPublic'),
  private: t('groups.visibilityPrivate'),
}))

const filterOptions = computed(() => buildGroupOptions(props.groups, groupLabels.value))

/** 候选 = 归到这个服务、且落在当前分组档里的项目。 */
const candidates = computed<Project[]>(() =>
  filterByGroup(
    props.projects.filter((p) => matchesService(p.serviceId, props.service.id)),
    groupFilter.value,
  ),
)

const selected = computed(() => candidates.value.filter((p) => picked.value.includes(p.id)))
const allPicked = computed(() => candidates.value.length > 0 && selected.value.length === candidates.value.length)
const counts = computed(() => {
  const list = Object.values(outcomes.value)
  return { ok: list.filter((o) => o.state === 'ok').length, fail: list.filter((o) => o.state === 'fail').length }
})
const hasOutcome = computed(() => Object.keys(outcomes.value).length > 0)

// 换服务 / 换分组后候选就变了:一律从「全勾」开始。留着上一轮的勾选集合会把已经不在
// 这批里的项目带进请求体,而「这批里我不想要哪一个」用户看一眼就知道,重勾不费劲。
watch(candidates, (list) => {
  picked.value = list.map((p) => p.id)
}, { immediate: true })

function toggle(id: string): void {
  picked.value = picked.value.includes(id)
    ? picked.value.filter((x) => x !== id)
    : [...picked.value, id]
}

function pickAll(): void {
  picked.value = allPicked.value ? [] : candidates.value.map((p) => p.id)
}

function errorText(err: unknown): string {
  if (err instanceof HttpError) {
    if (err.status === 0) return t('services.batchErrNetwork')
    if (err.status === 401 || err.status === 403) return t('services.batchErrForbidden')
    return err.apiError?.message ?? t('services.batchErrStatus', { status: err.status })
  }
  return t('services.errRetry')
}

async function execute(): Promise<void> {
  if (running.value) return
  // 快照这一批:跑到一半改勾选不该让后面几个项目凭空消失或多出来。
  const targets = selected.value.slice()
  if (targets.length === 0) return

  running.value = true
  outcomes.value = {}
  doneCount.value = 0
  runTotal.value = targets.length

  for (const p of targets) {
    try {
      const run = await triggerManual(p.id, {})
      outcomes.value = { ...outcomes.value, [p.id]: { state: 'ok', runId: run.id } }
    } catch (err) {
      outcomes.value = { ...outcomes.value, [p.id]: { state: 'fail', message: errorText(err) } }
    } finally {
      doneCount.value += 1
    }
  }
  // 回执只在面板里逐条摆出来,不外发事件:调用方没有要跟着刷新的东西
  // (项目卡片的「上次运行」至今是后端占位字段,见 internal/httpapi/projects.go)。
  running.value = false
}

function viewRun(runId?: string): void {
  if (runId) emit('open-run', runId)
}
</script>

<template>
  <div class="sbr" :aria-label="t('services.batchAria', { name: service.name })">
    <div class="sbr-bar">
      <label class="sbr-filter">
        <span class="sbr-filter-label">{{ t('services.batchGroupLabel') }}</span>
        <select v-model="groupFilter" class="sbr-select" :disabled="running" :aria-label="t('services.batchGroupLabel')">
          <option v-for="opt in filterOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
        </select>
      </label>
      <span class="sbr-count" aria-live="polite">
        {{ t('services.batchPickedCount', { picked: selected.length, total: candidates.length }) }}
      </span>
    </div>

    <ul v-if="candidates.length" class="sbr-list" role="list">
      <li class="sbr-head">
        <label class="sbr-all">
          <input type="checkbox" :checked="allPicked" :disabled="running" @change="pickAll" />
          {{ t('services.batchSelectAll') }}
        </label>
      </li>
      <li
        v-for="p in candidates"
        :key="p.id"
        class="sbr-row"
        :class="{ 'sbr-row--off': !picked.includes(p.id) }"
      >
        <label class="sbr-check">
          <input
            type="checkbox"
            :checked="picked.includes(p.id)"
            :disabled="running"
            :aria-label="t('services.batchPickAria', { name: p.name })"
            @change="toggle(p.id)"
          />
        </label>
        <span class="sbr-name" :title="p.name">{{ p.name }}</span>
        <span class="sbr-group">{{ groupLabel(p.groupId, groups, groupLabels) }}</span>
        <code v-if="p.defaultBranch" class="sbr-branch">{{ p.defaultBranch }}</code>

        <span v-if="outcomes[p.id]?.state === 'ok'" class="sbr-outcome sbr-outcome--ok">
          {{ t('services.batchOk') }}
          <button type="button" class="sbr-link" @click="viewRun(outcomes[p.id]?.runId)">
            {{ t('services.batchViewRun') }}
          </button>
        </span>
        <span v-else-if="outcomes[p.id]?.state === 'fail'" class="sbr-outcome sbr-outcome--fail">
          {{ outcomes[p.id]?.message }}
        </span>
      </li>
    </ul>
    <p v-else class="sbr-empty">{{ t('services.batchEmpty') }}</p>

    <p class="sbr-hint">{{ t('services.batchHint') }}</p>

    <div class="sbr-foot">
      <span v-if="running" class="sbr-progress" aria-live="polite">
        {{ t('services.batchProgress', { done: doneCount, total: runTotal }) }}
      </span>
      <span v-else-if="hasOutcome" class="sbr-progress" aria-live="polite">
        {{ t('services.batchDone', { ok: counts.ok, fail: counts.fail }) }}
      </span>
      <AppButton
        variant="primary"
        :loading="running"
        :disabled="running || selected.length === 0"
        @click="execute"
      >
        {{ t('services.batchRun', { n: selected.length }) }}
      </AppButton>
    </div>
  </div>
</template>

<style scoped>
.sbr {
  flex-basis: 100%;
  border: 1px solid var(--color-border);
  border-radius: var(--rounded);
  background: var(--color-inset);
  padding: 10px 12px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.sbr-bar {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}

.sbr-filter {
  display: inline-flex;
  align-items: center;
  gap: 7px;
}

.sbr-filter-label {
  font-size: 0.75rem;
  color: var(--color-dim);
}

.sbr-select {
  height: 28px;
  max-width: 220px;
  padding: 0 8px;
  border: 1px solid var(--color-border);
  border-radius: var(--rounded);
  background: var(--color-surface);
  color: var(--color-text);
  font-family: var(--font-sans);
  font-size: 0.78rem;
}

.sbr-select:disabled {
  opacity: 0.5;
}

.sbr-count {
  font-size: 0.75rem;
  color: var(--color-faint);
  margin-left: auto;
  white-space: nowrap;
}

.sbr-list {
  list-style: none;
  margin: 0;
  padding: 0;
  border: 1px solid var(--color-border);
  border-radius: var(--rounded);
  background: var(--color-surface);
  max-height: 220px;
  overflow-y: auto;
}

.sbr-head {
  padding: 6px 10px;
  border-bottom: 1px solid var(--color-border);
}

.sbr-all {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  font-size: 0.76rem;
  color: var(--color-dim);
  cursor: pointer;
}

.sbr-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 5px 10px;
  min-width: 0;
}

.sbr-row--off {
  opacity: 0.45;
}

.sbr-check {
  display: inline-flex;
  align-items: center;
  flex-shrink: 0;
}

.sbr-name {
  flex: 1;
  min-width: 0;
  font-size: 0.8rem;
  color: var(--color-text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.sbr-group,
.sbr-branch {
  flex-shrink: 0;
  font-size: 0.72rem;
  color: var(--color-faint);
}

.sbr-branch {
  font-family: var(--font-mono);
}

.sbr-outcome {
  flex-shrink: 0;
  font-size: 0.74rem;
}

.sbr-outcome--ok {
  color: var(--color-success);
}

.sbr-outcome--fail {
  color: var(--color-danger);
}

.sbr-link {
  border: none;
  background: none;
  padding: 0 0 0 6px;
  font-family: var(--font-sans);
  font-size: 0.74rem;
  color: var(--color-primary);
  cursor: pointer;
  text-decoration: underline;
}

.sbr-empty,
.sbr-hint {
  margin: 0;
  font-size: 0.75rem;
  color: var(--color-faint);
  line-height: 1.5;
}

.sbr-foot {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 10px;
}

.sbr-progress {
  font-size: 0.75rem;
  color: var(--color-dim);
}
</style>
