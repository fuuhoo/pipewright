<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount, watch, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import type { PipelineStage, PipelineJob, StageKind } from '../../api/pipeline'
import type { Credential } from '../../api/credentials'
import type { Server } from '../../api/servers'
import type { KubeCluster } from '../../api/kubeClusters'
import type { NotificationChannel } from '../../api/notifications'
import type { Environment } from '../../api/pipelineSettings'
import StageColumn from './StageColumn.vue'
import JobDrawer from './JobDrawer.vue'
import StageDrawer from './StageDrawer.vue'
import JobTypePicker from './JobTypePicker.vue'
import type { CustomNode } from '../../api/customNodes'
import { jobTypeLabel, getJobTypeSpec, jobTemplatePrefill } from './jobConfigSchema'
import { hasAnyNeeds } from './stageDeps'
import { boxOf, countBy, edgePath, fanAnchor, type EdgeBox } from './dagEdges'
import './pipeline.css'

// ─── Props / emits ────────────────────────────────────────────────────────────

const props = defineProps<{
  stages: PipelineStage[]
  /**
   * v6.2:保留入参但不再使用(原 YAML 折叠区已删,见 §3.7)。
   * 保留是为不破坏调用方(ProjectPipeline.vue 仍传 yaml);YAML 只读导出走
   * GET /api/projects/{id}/pipeline 的 yaml 字段,导入走 .../pipeline/import。
   */
  yaml: string
  credentials?: Credential[]
  servers?: Server[]
  /** 透传给 JobDrawer:K8s 发布节点的落点候选(集群列表)。 */
  clusters?: KubeCluster[]
  channels?: NotificationChannel[]
  /** 透传给 JobDrawer:push_image 节点只读回显各环境绑定的镜像仓。 */
  environments?: Environment[]
}>()

const emit = defineEmits<{
  (e: 'update', stages: PipelineStage[]): void
}>()

const { t } = useI18n()

// ─── Unique ID helpers ────────────────────────────────────────────────────────

function uid(): string {
  return Math.random().toString(36).slice(2, 10)
}

// ─── Selected job state ───────────────────────────────────────────────────────

const selectedJobId = ref<string | null>(null)

const selectedJob = computed<PipelineJob | null>(() => {
  if (!selectedJobId.value) return null
  for (const stage of props.stages) {
    const found = stage.jobs.find((j) => j.id === selectedJobId.value)
    if (found) return found
  }
  return null
})

const selectedStage = computed<PipelineStage | null>(() => {
  if (!selectedJobId.value) return null
  return props.stages.find((s) => s.jobs.some((j) => j.id === selectedJobId.value)) ?? null
})

function selectJob(jobId: string): void {
  selectedJobId.value = selectedJobId.value === jobId ? null : jobId
  // Job drawer + stage-settings drawer share the one right slot — mutually exclusive.
  if (selectedJobId.value) selectedStageSettingsId.value = null
}

function closeDrawer(): void {
  selectedJobId.value = null
}

// ─── Selected stage settings (shares the right drawer slot with the job drawer) ─

const selectedStageSettingsId = ref<string | null>(null)

const selectedSettingsStage = computed<PipelineStage | null>(() =>
  selectedStageSettingsId.value
    ? props.stages.find((s) => s.id === selectedStageSettingsId.value) ?? null
    : null,
)

const selectedSettingsIndex = computed<number>(() =>
  selectedStageSettingsId.value
    ? props.stages.findIndex((s) => s.id === selectedStageSettingsId.value)
    : -1,
)

function openStageSettings(stageId: string): void {
  selectedStageSettingsId.value = selectedStageSettingsId.value === stageId ? null : stageId
  if (selectedStageSettingsId.value) selectedJobId.value = null
}

function closeStageSettings(): void {
  selectedStageSettingsId.value = null
}

// ─── Mutation helpers ─────────────────────────────────────────────────────────

function updateJob(stageId: string, jobId: string, patch: Partial<PipelineJob>): void {
  const next = props.stages.map((s) => {
    if (s.id !== stageId) return s
    return {
      ...s,
      jobs: s.jobs.map((j) => (j.id === jobId ? { ...j, ...patch } : j)),
    }
  })
  emit('update', next)
}

function updateStage(stageId: string, patch: Partial<PipelineStage>): void {
  const next = props.stages.map((s) => (s.id === stageId ? { ...s, ...patch } : s))
  emit('update', next)
}

function deleteJob(stageId: string, jobId: string): void {
  if (selectedJobId.value === jobId) selectedJobId.value = null
  const next = props.stages.map((s) => {
    if (s.id !== stageId) return s
    // Drop the job and strip any sibling job needs referencing it (else save 422s).
    return {
      ...s,
      jobs: s.jobs
        .filter((j) => j.id !== jobId)
        .map((j) =>
          j.needs?.includes(jobId) ? { ...j, needs: j.needs.filter((n) => n !== jobId) } : j,
        ),
    }
  })
  emit('update', next)
}

// ─── Type picker (add new job / change existing job's type) ───────────────────

interface PickerState {
  open: boolean
  mode: 'add' | 'change'
  stageId: string
  jobId: string
  current: string
  /** Intra-stage deps to seed on the new job (serial/parallel add); empty = orphan. */
  needs: string[]
}

const picker = ref<PickerState>({ open: false, mode: 'add', stageId: '', jobId: '', current: '', needs: [] })

/** Open the type picker to add a new job to a stage. `needs` seeds intra-stage deps. */
function requestAddJob(stageId: string, needs: string[] = []): void {
  picker.value = { open: true, mode: 'add', stageId, jobId: '', current: '', needs }
}

/** Open the type picker to change the selected job's type (from the drawer). */
function requestChangeType(): void {
  if (!selectedJob.value || !selectedStage.value) return
  picker.value = {
    open: true,
    mode: 'change',
    stageId: selectedStage.value.id,
    jobId: selectedJob.value.id,
    current: selectedJob.value.type,
    needs: [],
  }
}

function closePicker(): void {
  picker.value = { ...picker.value, open: false }
}

function onPickerSelect(type: string, templateId?: string): void {
  const p = picker.value
  if (p.mode === 'add') {
    const stage = props.stages.find((s) => s.id === p.stageId)
    if (!stage) return closePicker()
    const spec = getJobTypeSpec(type)
    const newJob: PipelineJob = {
      id:      `job_${uid()}`,
      name:    jobTypeLabel(type),
      type,
      summary: '',
      // 模板节点带预填配置(深拷贝避免共享引用);选中模板胶囊时再叠一层模板预填。
      config:  { ...(spec?.defaultConfig ?? {}), ...jobTemplatePrefill(type, templateId ?? '') },
      ...(p.needs.length ? { needs: [...p.needs] } : {}),
    }
    addJobToStage(p.stageId, newJob)
  } else {
    updateJob(p.stageId, p.jobId, { type })
  }
  closePicker()
}

/**
 * Insert a saved custom node (复用库 Tier 2): a new Job pre-filled with the saved
 * type + summary + config snapshot. On "change" mode we overwrite type & config in place.
 */
function onPickerSelectCustom(node: CustomNode): void {
  const p = picker.value
  // config 快照转字符串 KV(Job.config 为 string KV;后端以 any 存,值实为字符串)。
  const config: Record<string, string> = {}
  for (const [k, v] of Object.entries(node.config ?? {})) {
    config[k] = typeof v === 'string' ? v : JSON.stringify(v)
  }
  if (p.mode === 'add') {
    const stage = props.stages.find((s) => s.id === p.stageId)
    if (!stage) return closePicker()
    const newJob: PipelineJob = {
      id:      `job_${uid()}`,
      name:    node.name || jobTypeLabel(node.nodeType),
      type:    node.nodeType,
      summary: node.summary ?? '',
      config,
      ...(p.needs.length ? { needs: [...p.needs] } : {}),
    }
    addJobToStage(p.stageId, newJob)
  } else {
    updateJob(p.stageId, p.jobId, { type: node.nodeType, summary: node.summary ?? '', config })
  }
  closePicker()
}

function addJobToStage(stageId: string, newJob: PipelineJob): void {
  const next = props.stages.map((s) =>
    s.id === stageId ? { ...s, jobs: [...s.jobs, newJob] } : s,
  )
  emit('update', next)
  selectedJobId.value = newJob.id
}

function reorderJob(stageId: string, from: number, to: number): void {
  const next = props.stages.map((s) => {
    if (s.id !== stageId) return s
    const jobs = [...s.jobs]
    if (from < 0 || from >= jobs.length || to < 0 || to >= jobs.length) return s
    const [moved] = jobs.splice(from, 1)
    jobs.splice(to, 0, moved)
    return { ...s, jobs }
  })
  emit('update', next)
}

function deleteStage(stageId: string): void {
  const idx = props.stages.findIndex((s) => s.id === stageId)
  if (idx < 0) return
  // Deselect if selected job was in this stage
  if (selectedStage.value?.id === stageId) selectedJobId.value = null
  if (selectedStageSettingsId.value === stageId) selectedStageSettingsId.value = null
  // Drop the deleted stage and strip any dangling needs referencing it (else save 422s).
  const next = props.stages
    .filter((s) => s.id !== stageId)
    .map((s) =>
      s.needs?.includes(stageId) ? { ...s, needs: s.needs.filter((n) => n !== stageId) } : s,
    )
  emit('update', next)
}

function addStage(): void {
  const KIND_SEQ: StageKind[] = ['build', 'deploy', 'notify', 'custom']
  const existing = props.stages.map((s) => s.kind)
  const nextKind: StageKind = KIND_SEQ.find((k) => !existing.includes(k)) ?? 'custom'
  const nextNum = props.stages.filter((s) => s.kind !== 'source').length + 1
  const KIND_LABELS: Partial<Record<StageKind, string>> = {
    build: t('pipelineCanvas.stageBuild'),
    deploy: t('pipelineCanvas.stageDeploy'),
    notify: t('pipelineCanvas.stageNotify'),
  }
  const newStage: PipelineStage = {
    id:   `stg_${uid()}`,
    name: nextKind === 'custom'
      ? t('pipelineCanvas.stageCustomN', { n: nextNum })
      : (KIND_LABELS[nextKind] ?? t('pipelineCanvas.stageDefaultN', { n: nextNum })),
    kind: nextKind,
    jobs: [],
  }
  emit('update', [...props.stages, newStage])
}

// ─── DAG edge overlay (Story 8-7) ─────────────────────────────────────────────
// Draw connectors from each stage's declared needs (upstream → downstream). When no
// stage declares needs, fall back to a linear chain (mirrors backend BuildGraph).

const flowRef = ref<HTMLElement | null>(null)
const overlay = ref({ w: 0, h: 0 })
const edgePaths = ref<string[]>([])

interface Edge { from: string; to: string }

const edges = computed<Edge[]>(() => {
  if (hasAnyNeeds(props.stages)) {
    const out: Edge[] = []
    for (const s of props.stages) for (const n of s.needs ?? []) out.push({ from: n, to: s.id })
    return out
  }
  const out: Edge[] = []
  for (let i = 1; i < props.stages.length; i++) {
    out.push({ from: props.stages[i - 1].id, to: props.stages[i].id })
  }
  return out
})

/**
 * 连线的两端都量「阶段表头那块可见的底板」,而不是阶段列的外框。
 *
 * 之前纵向量表头、横向量列边界,混用两套坐标:带内部 DAG 的阶段列会被任务网格撑得比表头宽
 * (量到过 236px 的空档),线就从列右边界伸出来,那一段什么也没有 —— 看着正是「线头停在半路」。
 * 表头有实边、有底色,是唯一两端都能对齐的可见元素;列边界留给 CSS 自己管。
 */
function headerBox(col: HTMLElement): EdgeBox {
  const head = col.querySelector<HTMLElement>('.stage-header')
  if (!head) return boxOf(col)
  return {
    x: col.offsetLeft + head.offsetLeft,
    y: col.offsetTop + head.offsetTop,
    w: head.offsetWidth,
    h: head.offsetHeight,
  }
}

function stageCols(flow: HTMLElement): Map<string, HTMLElement> {
  const out = new Map<string, HTMLElement>()
  for (const s of props.stages) {
    const el = flow.querySelector<HTMLElement>(`:scope > .stage-col[data-stage-id="${CSS.escape(s.id)}"]`)
    if (el) out.set(s.id, el)
  }
  return out
}

function measureEdges(): void {
  const flow = flowRef.value
  if (!flow) return
  const byId = stageCols(flow)
  overlay.value = { w: flow.scrollWidth, h: flow.scrollHeight }
  const boxes = new Map<string, EdgeBox>()
  for (const [id, col] of byId) boxes.set(id, headerBox(col))
  // 扇入/扇出各自按边数沿表头高度均分锚点:多条边全挤在中心点会读成一条折线,分不清谁接谁。
  const outs = countBy(edges.value.map((e) => e.from))
  const ins = countBy(edges.value.map((e) => e.to))
  const outIdx = new Map<string, number>()
  const inIdx = new Map<string, number>()
  const paths: string[] = []
  for (const e of edges.value) {
    const a = boxes.get(e.from)
    const b = boxes.get(e.to)
    if (!a || !b) continue
    const oi = outIdx.get(e.from) ?? 0
    outIdx.set(e.from, oi + 1)
    const ii = inIdx.get(e.to) ?? 0
    inIdx.set(e.to, ii + 1)
    paths.push(edgePath(a, b, fanAnchor(a, oi, outs.get(e.from) ?? 1), fanAnchor(b, ii, ins.get(e.to) ?? 1), 26))
  }
  edgePaths.value = paths
}

// 阶段列/表头任一变化都要重测:只观察外层容器时,列宽随内部 DAG 网格变宽变高而容器尺寸不变,
// 就会拿旧坐标画线 —— 那正是「线头停在半路」的成因。
let ro: ResizeObserver | null = null
function observeTargets(): void {
  if (!ro) return
  ro.disconnect()
  const flow = flowRef.value
  if (!flow) return
  ro.observe(flow)
  flow.querySelectorAll<HTMLElement>('.stage-col, .stage-header').forEach((el) => ro?.observe(el))
}

onMounted(() => {
  measureEdges()
  ro = new ResizeObserver(() => measureEdges())
  observeTargets()
})
onBeforeUnmount(() => ro?.disconnect())
watch(
  () => props.stages,
  () => nextTick(() => {
    measureEdges()
    observeTargets()
  }),
  { deep: true },
)

// ─── v6.2 §3.7:YAML 折叠区已删除 ───
// 原「查看 YAML」按钮 + 只读预览块下架(前端不再提供直接查看/编辑入口)。
// YAML 仍可经 GET /api/projects/{id}/pipeline 的只读 `yaml` 字段导出,以及
// POST .../pipeline/import 导入;PUT .../pipeline 已拒绝 body.yaml。
// 相关 i18n 键(pipelineCanvas.viewYaml)与 CSS(.yaml-toggle/.yaml-block)一并清理。

function handleDrawerUpdate(patch: Partial<PipelineJob>): void {
  if (!selectedJob.value || !selectedStage.value) return
  updateJob(selectedStage.value.id, selectedJob.value.id, patch)
}
</script>

<template>
  <div class="canvas-body">
    <!-- ─── Scrollable canvas ────────────────────────────────────────────── -->
    <div class="pipeline-canvas">
      <div ref="flowRef" class="pipeline-flow pipeline-flow--dag" role="list" :aria-label="t('pipelineCanvas.flowAriaLabel')">

        <!-- DAG edge overlay (drawn from declared needs; decorative) -->
        <svg
          v-if="edgePaths.length"
          class="dag-overlay"
          :width="overlay.w"
          :height="overlay.h"
          :viewBox="`0 0 ${overlay.w} ${overlay.h}`"
          aria-hidden="true"
        >
          <path v-for="(d, i) in edgePaths" :key="i" class="dag-edge" :d="d" />
          <path v-for="(d, i) in edgePaths" :key="`f${i}`" class="dag-edge-flow" :d="d" />
        </svg>

        <template v-for="(stage, idx) in stages" :key="stage.id">
          <!-- Stage column -->
          <StageColumn
            :stage="stage"
            :stage-index="idx"
            :data-stage-id="stage.id"
            :selected-job-id="selectedJobId"
            :all-stages="stages"
            role="listitem"
            @select-job="selectJob"
            @delete-job="(jobId) => deleteJob(stage.id, jobId)"
            @add-job="(needs) => requestAddJob(stage.id, needs)"
            @delete-stage="deleteStage(stage.id)"
            @reorder-job="(p) => reorderJob(stage.id, p.from, p.to)"
            :settings-active="selectedStageSettingsId === stage.id"
            @update-needs="(needs) => updateStage(stage.id, { needs })"
            @update-allow-failure="(v) => updateStage(stage.id, { allowFailure: v })"
            @update-job-needs="(p) => updateJob(stage.id, p.jobId, { needs: p.needs })"
            @open-settings="openStageSettings(stage.id)"
          />
        </template>

        <!-- Add stage button (dashed) -->
        <button
          class="add-stage-btn"
          :aria-label="t('pipelineCanvas.addStageAria')"
          @click="addStage"
        >{{ t('pipelineCanvas.addStage') }}</button>
      </div>
    </div>

    <!-- ─── Right-side drawer (selected job OR stage settings — one shared slot) ─ -->
    <JobDrawer
      v-if="selectedJob && selectedStage"
      :job="selectedJob"
      :stage="selectedStage"
      :credentials="props.credentials"
      :servers="props.servers"
      :clusters="props.clusters"
      :channels="props.channels"
      :environments="props.environments"
      :all-stages="props.stages"
      @close="closeDrawer"
      @update="handleDrawerUpdate"
      @change-type="requestChangeType"
    />

    <StageDrawer
      v-else-if="selectedSettingsStage"
      :stage="selectedSettingsStage"
      :stage-index="selectedSettingsIndex"
      @close="closeStageSettings"
      @update-when="(when) => updateStage(selectedSettingsStage!.id, { when })"
      @update-gate="(v) => updateStage(selectedSettingsStage!.id, { gate: v })"
      @update-matrix="(matrix) => updateStage(selectedSettingsStage!.id, { matrix })"
      @update-post="(post) => updateStage(selectedSettingsStage!.id, { post })"
      @update-services="(services) => updateStage(selectedSettingsStage!.id, { services })"
    />

    <!-- Type picker modal (add new job / change type) -->
    <JobTypePicker
      :open="picker.open"
      :current="picker.mode === 'change' ? picker.current : ''"
      :title="picker.mode === 'change' ? t('pipelineCanvas.pickerTitleChange') : t('pipelineCanvas.pickerTitleAdd')"
      @select="onPickerSelect"
      @select-custom="onPickerSelectCustom"
      @close="closePicker"
    />
  </div>
</template>

<style scoped>
.canvas-body {
  flex: 1;
  display: flex;
  min-height: 0;
  overflow: hidden;
}

/* DAG layout: position the flow so the overlay anchors to it; give columns a gap for edges. */
.pipeline-flow--dag {
  position: relative;
  gap: 60px;
}

.dag-overlay {
  position: absolute;
  inset: 0;
  z-index: 0;
  pointer-events: none;
  overflow: visible;
}

.dag-edge {
  fill: none;
  stroke: var(--color-border-strong);
  stroke-width: 2;
}

/* animated flow pulse along the edge */
.dag-edge-flow {
  fill: none;
  stroke: var(--color-primary);
  stroke-width: 2;
  stroke-dasharray: 5 12;
  opacity: 0.75;
  animation: dag-flow 1.4s linear infinite;
}

@keyframes dag-flow {
  to {
    stroke-dashoffset: -34;
  }
}

@media (prefers-reduced-motion: reduce) {
  .dag-edge-flow {
    animation: none;
  }
}
/* The overlay (z-index 0, first child) paints below the stage columns (later siblings),
   so edges show only in the gaps between columns. */
</style>
