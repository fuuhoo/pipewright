<script setup lang="ts">
/**
 * TriggerRunModal —— 手动触发一次流水线运行(项目列表页与流水线配置页共用)。
 * 分支/commit 可留空(后端按项目默认分支与分支 HEAD 解析);运行参数有类型化定义
 * 就渲染类型化控件,没有则回退自由 KV。纯发布项目(repoUrl 为空)不显示分支两项,
 * 列分支的接口对它只会 400。
 */
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { triggerManual, type RunDetail, type TriggerManualInput } from '../../api/runs'
import type { Project } from '../../api/projects'
import { listRefs, listCommits, type GitCommit } from '../../api/refs'
import { getParameters, validateParamValues, type ParamDef } from '../../api/parameters'
import { HttpError } from '../../api/http'
import RunParamsEditor from '../RunParamsEditor.vue'
import TypedRunParams from '../TypedRunParams.vue'
import RefPicker from './RefPicker.vue'

const props = defineProps<{ open: boolean; project: Project | null }>()

const emit = defineEmits<{
  'update:open': [value: boolean]
  triggered: [runId: string]
}>()

const { t } = useI18n()

const form        = ref({ branch: '', commit: '' })
const params      = ref<Record<string, string>>({})
const defs        = ref<ParamDef[]>([])
const branchError = ref('')
const banner      = ref('')
const submitting  = ref(false)

const branches = ref<string[]>([])
const tags     = ref<string[]>([])
const commitOptions = ref<GitCommit[]>([])

// 每次打开都从干净状态开始:调用方只给 project + open,弹窗自己负责装载候选。
watch(
  () => [props.open, props.project?.id] as const,
  ([open, projectId]) => {
    if (!open || !props.project) return
    form.value        = { branch: props.project.defaultBranch || '', commit: '' }
    params.value      = {}
    defs.value        = []
    branchError.value = ''
    banner.value      = ''
    submitting.value  = false
    // 纯发布项目没有仓库,列分支只会 400;参数定义照拉(运行参数与仓库无关)。
    if (props.project.repoUrl) void loadRefOptions(projectId as string)
    void loadDefs(projectId as string)
  },
  { immediate: true },
)

async function loadDefs(projectId: string): Promise<void> {
  try {
    defs.value = await getParameters(projectId)
  } catch {
    defs.value = []
  }
}

// 代码管理区:拉真实分支/tag 供下拉建议;未启用(503)或仓库不可达 → 静默回退手填。
async function loadRefOptions(projectId: string): Promise<void> {
  branches.value = []
  tags.value     = []
  commitOptions.value = []
  try {
    const refs = await listRefs(projectId)
    branches.value = refs.branches.map((r) => r.name)
    tags.value     = refs.tags.map((r) => r.name)
  } catch {
    // 静默:保持纯文本输入(优雅降级)。
  }
  void loadCommitOptions(projectId, form.value.branch)
}

// 据当前分支拉最近 commit 供 commit 下拉(分支变化时刷新);失败静默。
let commitLoadSeq = 0
async function loadCommitOptions(projectId: string, refName: string): Promise<void> {
  const seq = ++commitLoadSeq
  try {
    const commits = await listCommits(projectId, refName, 30)
    if (seq === commitLoadSeq) commitOptions.value = commits
  } catch {
    if (seq === commitLoadSeq) commitOptions.value = []
  }
}

function onBranchInput(value?: string): void {
  branchError.value = ''
  if (props.project) void loadCommitOptions(props.project.id, value ?? form.value.branch)
}

function close(): void {
  if (submitting.value) return
  emit('update:open', false)
}

async function handleSubmit(): Promise<void> {
  branchError.value = ''
  banner.value      = ''

  const project = props.project
  if (!project) return

  // 分支可选:留空时后端按项目默认分支解析,不必逼用户填。
  const branch = form.value.branch.trim()

  // 类型化参数:提交前客户端即时校验(后端仍会再校验一次 422)。
  if (defs.value.length) {
    const verr = validateParamValues(defs.value, params.value)
    if (verr) {
      banner.value = verr
      return
    }
  }

  submitting.value = true

  try {
    const input: TriggerManualInput = {}
    if (branch) input.branch = branch
    const commit = form.value.commit.trim()
    if (commit) input.commit = commit
    if (Object.keys(params.value).length) input.params = params.value

    const run: RunDetail = await triggerManual(project.id, input)
    emit('update:open', false)
    emit('triggered', run.id)
  } catch (err) {
    if (err instanceof HttpError) {
      if (err.status === 0) {
        banner.value = t('projects.errNetwork')
      } else if (err.status === 404) {
        banner.value = t('projects.triggerErrNotFound')
      } else {
        banner.value = err.apiError?.message ?? t('projects.triggerErrStatus', { status: err.status })
      }
    } else {
      banner.value = t('projects.triggerErrRetry')
    }
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <Teleport to="body">
    <div
      v-if="open && project"
      class="modal-scrim"
      role="dialog"
      :aria-label="t('projects.triggerDialogAria', { name: project.name })"
      aria-modal="true"
      @keydown.esc="close"
      @click.self="close"
    >
      <div class="modal modal--sm">
        <!-- Header -->
        <div class="modal-head">
          <div class="modal-icon modal-icon--run" aria-hidden="true">
            <svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7">
              <polygon points="5 3 19 12 5 21 5 3" fill="currentColor" stroke="none"/>
            </svg>
          </div>
          <div>
            <h3 class="modal-title">{{ t('projects.triggerTitle') }}</h3>
            <p class="modal-sub">{{ project.repoUrl ? t('projects.triggerSub', { name: project.name }) : t('projects.triggerSubNoRepo', { name: project.name }) }}</p>
          </div>
          <button
            class="modal-close"
            :aria-label="t('projects.closeDialog')"
            :disabled="submitting"
            @click="close"
          >
            <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <path d="M18 6 6 18M6 6l12 12"/>
            </svg>
          </button>
        </div>

        <!-- Error banner -->
        <div
          v-if="banner"
          class="banner banner--error modal-banner"
          role="alert"
        >
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
            <circle cx="12" cy="12" r="9"/><path d="M12 8v4M12 16h.01"/>
          </svg>
          {{ banner }}
        </div>

        <form
          class="modal-form"
          novalidate
          @submit.prevent="handleSubmit"
        >
          <!-- Branch(纯发布项目没有仓库,不显示分支/commit 两项) -->
          <div v-if="project.repoUrl" class="field">
            <label class="field-label" for="trigger-branch">
              {{ t('projects.branch') }}
              <span class="field-hint-inline">{{ t('projects.branchHint') }}</span>
            </label>
            <RefPicker
              input-id="trigger-branch"
              v-model="form.branch"
              :branches="branches"
              :tags="tags"
              :disabled="submitting"
              :has-error="!!branchError"
              :described-by="branchError ? 'trigger-branch-err' : undefined"
              placeholder="main"
              :branches-label="t('projects.refGroupBranches')"
              :tags-label="t('projects.refGroupTags')"
              @update:model-value="onBranchInput"
            />
            <span
              v-if="branchError"
              id="trigger-branch-err"
              class="field-error"
              role="alert"
            >{{ branchError }}</span>
          </div>

          <!-- Commit (optional) -->
          <div v-if="project.repoUrl" class="field">
            <label class="field-label" for="trigger-commit">
              {{ t('projects.commit') }}
              <span class="field-hint-inline">{{ t('projects.commitHint') }}</span>
            </label>
            <input
              id="trigger-commit"
              v-model="form.commit"
              class="field-input field-input--mono"
              type="text"
              :placeholder="t('projects.commitPlaceholder')"
              autocomplete="off"
              list="trigger-commit-options"
              :disabled="submitting"
            />
            <!-- 代码管理区:当前分支最近 commit 下拉建议(短 sha — 首行说明) -->
            <datalist id="trigger-commit-options">
              <option v-for="c in commitOptions" :key="c.sha" :value="c.short">{{ c.subject }}</option>
            </datalist>
          </div>

          <!-- Parameters · 有类型化定义 → 类型化控件;无 → 自由 KV -->
          <div class="field">
            <label class="field-label">
              {{ t('projects.params') }}
              <span class="field-hint-inline">{{ defs.length ? t('projects.paramsHintTyped') : t('projects.paramsHintFree') }}</span>
            </label>
            <TypedRunParams
              v-if="defs.length"
              :key="`typed-${project.id}`"
              :defs="defs"
              v-model="params"
            />
            <RunParamsEditor
              v-else
              :key="project.id"
              v-model="params"
              :disabled="submitting"
            />
          </div>

          <!-- Footer -->
          <div class="modal-footer">
            <button
              type="button"
              class="btn-secondary"
              :disabled="submitting"
              @click="close"
            >{{ t('projects.cancel') }}</button>
            <button
              type="submit"
              class="btn-run"
              :disabled="submitting"
              :aria-busy="submitting"
            >
              <span v-if="submitting" class="spinner" aria-hidden="true" />
              <svg v-else width="13" height="13" viewBox="0 0 24 24" fill="none" aria-hidden="true">
                <polygon points="5 3 19 12 5 21 5 3" fill="currentColor"/>
              </svg>
              {{ submitting ? t('projects.triggering') : t('projects.runNow') }}
            </button>
          </div>
        </form>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
/* ─── error banner ──────────────────────────────────────────────────────────── */
.banner {
  display: flex;
  align-items: flex-start;
  gap: 9px;
  padding: 11px 14px;
  border-radius: var(--rounded);
  font-size: 0.83rem;
  line-height: 1.5;
}

.banner--error {
  background: var(--color-red-soft);
  border: 1px solid var(--color-red-line);
  color: var(--color-red);
}

/* ─── buttons ───────────────────────────────────────────────────────────────── */
.btn-secondary {
  display: inline-flex;
  align-items: center;
  height: 34px;
  padding: 0 15px;
  background: var(--color-card-2);
  color: var(--color-text);
  border: 1px solid var(--color-border-strong);
  font-family: var(--font-sans);
  font-size: 0.83rem;
  font-weight: 500;
  border-radius: var(--rounded);
  cursor: pointer;
  transition: border-color var(--duration-fast), background-color var(--duration-fast);
}

.btn-secondary:hover:not(:disabled) {
  border-color: var(--color-faint);
}

.btn-secondary:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: 2px;
}

.btn-secondary:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}

.btn-run {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 34px;
  padding: 0 15px;
  border: none;
  background: var(--color-primary);
  color: #fff;
  font-family: var(--font-sans);
  font-size: 0.83rem;
  font-weight: 600;
  border-radius: var(--rounded);
  cursor: pointer;
  box-shadow: 0 5px 16px var(--color-primary-soft);
  transition: background-color var(--duration-fast), transform var(--duration-fast), box-shadow var(--duration-fast);
  white-space: nowrap;
}

.btn-run:hover:not(:disabled) {
  background: var(--color-primary-press);
  transform: translateY(-1px);
}

.btn-run:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: 3px;
}

.btn-run:disabled {
  opacity: 0.45;
  cursor: not-allowed;
  transform: none;
  box-shadow: none;
}

/* ─── modal ─────────────────────────────────────────────────────────────────── */
.modal-scrim {
  position: fixed;
  inset: 0;
  background: oklch(0% 0 0 / 0.62);
  display: grid;
  place-items: center;
  z-index: 100;
  padding: 24px;
  animation: scrim-in var(--duration-fast) ease both;
}

@keyframes scrim-in {
  from { opacity: 0; }
  to   { opacity: 1; }
}

.modal {
  width: 100%;
  max-width: 520px;
  max-height: calc(100dvh - 48px);
  background: var(--color-card);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--rounded-xl);
  box-shadow: var(--shadow-modal);
  overflow: hidden;
  display: flex;
  flex-direction: column;
  animation: modal-in 0.35s var(--ease-out-expo) both;
}

.modal--sm {
  max-width: 420px;
}

@keyframes modal-in {
  from { opacity: 0; transform: translateY(14px) scale(0.98); }
  to   { opacity: 1; transform: none; }
}

@media (prefers-reduced-motion: reduce) {
  .modal-scrim { animation: none; }
  .modal       { animation: none; }
}

.modal-head {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  padding: 20px 20px 16px;
  border-bottom: 1px solid var(--color-border);
}

.modal-icon {
  width: 36px;
  height: 36px;
  border-radius: var(--rounded-lg);
  background: var(--color-primary-soft);
  color: var(--color-primary);
  display: grid;
  place-items: center;
  flex-shrink: 0;
}

.modal-icon--run {
  background: var(--color-primary-soft);
  color: var(--color-primary);
}

.modal-title {
  font-size: 1rem;
  font-weight: 600;
  color: var(--color-text);
  margin-top: 2px;
  letter-spacing: -0.01em;
}

.modal-sub {
  font-size: 0.78rem;
  color: var(--color-faint);
  margin-top: 3px;
  line-height: 1.4;
}

.modal-close {
  margin-left: auto;
  flex-shrink: 0;
  width: 30px;
  height: 30px;
  border-radius: var(--rounded-md);
  border: none;
  background: transparent;
  color: var(--color-faint);
  cursor: pointer;
  display: grid;
  place-items: center;
  transition: color var(--duration-fast), background-color var(--duration-fast);
}

.modal-close:hover {
  color: var(--color-text);
  background: var(--color-inset);
}

.modal-close:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: 2px;
}

.modal-close:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.modal-banner {
  margin: 16px 20px 0;
  border-radius: var(--rounded);
}

.modal-form {
  padding: 20px;
  display: flex;
  flex-direction: column;
  gap: 16px;
  min-height: 0;
  overflow-y: auto;
}

.modal-footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding-top: 4px;
}

/* ─── form fields ────────────────────────────────────────────────────────────── */
.field {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.field-label {
  font-size: 0.78rem;
  font-weight: 500;
  color: var(--color-dim);
}

.field-hint-inline {
  font-weight: 400;
  color: var(--color-faint);
}

.field-input {
  width: 100%;
  height: 38px;
  background: var(--color-inset);
  border: 1px solid var(--color-border);
  border-radius: var(--rounded);
  padding: 0 12px;
  color: var(--color-text);
  font-family: var(--font-sans);
  font-size: 0.86rem;
  transition: border-color var(--duration-fast), box-shadow var(--duration-fast);
}

.field-input::placeholder {
  color: var(--color-faint);
}

.field-input:focus {
  outline: none;
  border-color: var(--color-primary);
  box-shadow: 0 0 0 3px var(--color-primary-soft);
}

.field-input:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.field-input--mono {
  font-family: var(--font-mono);
  font-size: 0.8rem;
}

.field-error {
  font-size: 0.76rem;
  color: var(--color-red);
  line-height: 1.4;
}

/* ─── spinner ────────────────────────────────────────────────────────────────── */
.spinner {
  display: inline-block;
  width: 13px;
  height: 13px;
  border: 2px solid rgba(255, 255, 255, 0.35);
  border-top-color: #fff;
  border-radius: var(--rounded-full);
  animation: spin 0.7s linear infinite;
  flex-shrink: 0;
}

@keyframes spin {
  to { transform: rotate(360deg); }
}

@media (prefers-reduced-motion: reduce) {
  .spinner { animation: none; border-top-color: currentColor; }
}
</style>
