<script setup lang="ts">
import { ref, computed, watch, nextTick, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { localizeName } from '../../lib/pipelineLabels'
import type { PipelineJob, PipelineStage } from '../../api/pipeline'
import type { Credential } from '../../api/credentials'
import type { Server } from '../../api/servers'
import type { NotificationChannel } from '../../api/notifications'
import type { Environment } from '../../api/pipelineSettings'
import { listEnabledBuildEnvs, type PresetBuildEnv } from '../../api/buildEnvs'
import { listEnabledConfigProfiles, type ConfigProfile } from '../../api/configProfiles'
import {
  getJobTypeSpec,
  splitConfig,
  droppedKeys,
  managedKeys,
  jobTypeLabel,
  isScriptClassType,
  effectiveJobType,
  normalizeArtifactTier,
  normalizeDeployArtifactPref,
  usesBuildTierField,
  usesDeployPrefField,
  type JobField,
} from './jobConfigSchema'
import { configUsesTemplate } from './stepCompile'
import { artifactSourceGroups } from './artifactSources'
import {
  isStudioNode,
  parsePromotedParams,
  promotedValues,
  applyPromotedValues,
  type PromotedParam,
} from './studioCompile'
import { createCustomNode } from '../../api/customNodes'
import JobTypeIcon from './JobTypeIcon.vue'
import StepBuilder from './StepBuilder.vue'
import StudioInstanceParams from './StudioInstanceParams.vue'

const props = defineProps<{
  job: PipelineJob
  stage: PipelineStage
  credentials?: Credential[]
  servers?: Server[]
  channels?: NotificationChannel[]
  /** 项目的部署环境(含各自绑定的镜像仓)—— push_image 只读回显推送目标用。 */
  environments?: Environment[]
  /** 整条流水线的阶段(含本阶段)—— 部署节点的「产物来源任务」按依赖关系算候选,故需要全图。 */
  allStages?: PipelineStage[]
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'update', patch: Partial<PipelineJob>): void
  (e: 'change-type'): void
}>()

const { t } = useI18n()

// ─── Local editable copy ──────────────────────────────────────────────────────

interface KVRow {
  _key: number
  k: string
  v: string
}

let _kvSeq = 0

const localName    = ref(props.job.name)
const localType    = ref(props.job.type)
const localSummary = ref(props.job.summary)
/** Schema-owned keys for the current type (the typed form binds here) */
const typedConfig  = ref<Record<string, string>>({})
/** Keys NOT covered by the type schema — editable in the advanced section */
const extraRows    = ref<KVRow[]>([])

const showAdvanced = ref(false)

// 仅当「选了不同 job」或「改了 job 类型」时才重置视图模式(steps/raw);纯内容编辑不重置。
// 否则:在「原始参数」视图编辑某字段 → blur 提交 → 父回写 props.job → 本 watch 再跑 →
// 会把视图无端切回「可视化步骤」(用户实测的 bug)。
let lastModeKey = ''

function hydrate(job: PipelineJob): void {
  localName.value    = job.name
  localType.value    = job.type
  localSummary.value = job.summary
  const modeKey = `${job.id} ${job.type}`
  const repickView = modeKey !== lastModeKey
  lastModeKey = modeKey
  splitOnType(job.type, job.config ?? {}, repickView)
}

/** Recompute typed config + raw extras for a given type, preserving all values. */
function splitOnType(type: string, config: Record<string, string>, repickView = true): void {
  const { extras } = splitConfig(type, config)
  const drop = droppedKeys(type)
  const typed: Record<string, string> = {}
  let dropped = 0
  for (const [k, v] of Object.entries(config)) {
    if (drop.has(k)) {
      dropped++
      continue
    }
    if (!extras.some(([ek]) => ek === k)) typed[k] = v
  }
  // 产物档位收敛为「镜像 / 产物」两档:存量的 jar / dist 在读入时归到「产物」
  // (仅内存,下次 flush 才落库),否则下拉框会显示成一个既不选也认不出的值。
  if (typed.artifactType && usesBuildTierField(type)) {
    typed.artifactType = normalizeArtifactTier(typed.artifactType)
  }
  // 部署节点的产物偏好同理:历史 dist/jar/archive 读成「产物」(与构建档位同一套词)。
  if (typed.artifactType && usesDeployPrefField(type)) {
    typed.artifactType = normalizeDeployArtifactPref(typed.artifactType)
  }
  // 旧配置收敛:有镜像/toolchain 但无 buildEnvId 时,按预置目录反查预填(仅内存,
  // 下次 flush 才落库)。查不到就留给 buildenv 控件显示「未匹配」告警项。
  if (!typed.buildEnvId) {
    const spec = getJobTypeSpec(type)
    if (spec?.fields.some((f) => f.kind === 'buildenv')) {
      const hit =
        (config.image && presetByImage(config.image)) ||
        ((config.toolchainLanguage || config.toolchainVersion) &&
          presetByToolchain(config.toolchainLanguage ?? '', config.toolchainVersion ?? ''))
      if (hit) typed.buildEnvId = hit.id
    }
  }
  typedConfig.value = typed
  extraRows.value = extras.map(([k, v]) => ({ _key: ++_kvSeq, k, v }))
  showAdvanced.value = extras.length > 0
  // 假字段只抹内存副本还不够:回写一次,让它从 config 里真正消失(nextTick 避开 setup 期 emit)。
  if (dropped > 0) void nextTick(flush)
  if (repickView) pickViewMode(config)
}

// Resync when the selected job changes (initial hydrate runs after the
// step-builder computeds below are declared, to avoid a temporal dead zone).
watch(() => props.job, (next) => hydrate(next))

// ─── Field schema for the current type ────────────────────────────────────────

const spec = computed(() => getJobTypeSpec(localType.value))

/** 推送目标的唯一来源:各环境绑定的镜像仓(push_image 只读回显,节点不再手填 registry)。 */
const pushTargets = computed<Array<{ env: string; registry: string }>>(() =>
  (props.environments ?? []).map((env) => ({
    env: env.name,
    registry: env.imageRegistry?.url?.trim() ?? '',
  })),
)

/** 步骤构建器拥有的 config 键(commands 多行 / artifactPath 多行)。 */
const STEP_OWNED_KEYS = new Set(['commands', 'artifactPath'])

// 「自定义脚本」节点(script/custom)天然就是写命令文本,可视化步骤构建器对它是冗余:
// 这些类型不提供 steps/raw 切换、默认就用原始参数(命令文本框)。可视化步骤仅保留给
// 前端构建/后端构建/模板等带预置语义的脚本类节点。
const RAW_ONLY_SCRIPT_TYPES = new Set(['script', 'custom'])

// 执行/缓存类高级字段:能力保留,但默认收进可折叠「高级」分区,主表单只留镜像/命令/工作目录。
const ADVANCED_FIELD_KEYS = new Set(['timeoutSeconds', 'retries', 'cpu', 'memory', 'cachePaths', 'cacheKey'])

/** 该类型能用可视化步骤构建器吗(脚本类 + 有 commands/artifactPath 字段;但 script/custom 除外)。 */
const canUseStepBuilder = computed<boolean>(() => {
  // 构建任务按产物档位折算:只有「产物」档走脚本路径,才有步骤可编;镜像档是 docker 构建。
  if (!isScriptClassType(localType.value, liveConfig.value)) return false
  if (RAW_ONLY_SCRIPT_TYPES.has(localType.value)) return false
  if (!spec.value) return false
  return spec.value.fields.some((f) => STEP_OWNED_KEYS.has(f.key))
})

/**
 * 视图模式:'steps' 走可视化步骤构建器,'raw' 走原始 typed 表单。
 * 默认对脚本类显示步骤视图;但若 config 用了模板渲染(commandTemplate/params,
 * 步骤构建器不覆盖那套语义)则回退到原始视图,避免误编译丢数据。
 */
const viewMode = ref<'steps' | 'raw'>('raw')

function pickViewMode(config: Record<string, string>): void {
  if (canUseStepBuilder.value && !configUsesTemplate(config)) {
    viewMode.value = 'steps'
  } else {
    viewMode.value = 'raw'
  }
}

/** 步骤模式下,typed 表单只渲染「非步骤拥有」的字段(image/workDir/模板字段等)。 */
const visibleFields = computed<JobField[]>(() => {
  if (!spec.value) return []
  return spec.value.fields.filter((f) => {
    if (f.when && !f.when(typedConfig.value)) return false
    if (viewMode.value === 'steps' && STEP_OWNED_KEYS.has(f.key)) return false
    return true
  })
})

// 高级(执行/缓存)字段折叠:主表单只显示非高级字段;高级字段收进默认折叠分区。
const showExecAdvanced = ref(false)
/** 当前可见字段里第一个高级字段的 key —— 用于在它前面插入「高级」折叠开关。 */
const firstAdvancedKey = computed<string>(
  () => visibleFields.value.find((f) => ADVANCED_FIELD_KEYS.has(f.key))?.key ?? '',
)

/** 步骤构建器回传的编译片段并入 typedConfig 落库。 */
function onStepsUpdate(patch: { commands: string; artifactPath: string }): void {
  typedConfig.value = {
    ...typedConfig.value,
    commands: patch.commands,
    artifactPath: patch.artifactPath,
  }
  flush()
}

// ─── 工作室节点「实例参数短清单」 ─────────────────────────────────────────────
// 把工作室(templated / 带 __studio)节点拖进流水线后,实例编辑默认只面对作者「提升」
// 出来的少数参数(类型化控件),而非整段 commandTemplate/params 原始文本。提升参数的
// 定义(label/type/options)来自 __studio 元信息,只读消费;短清单只编辑各参数的「值」,
// 经 applyPromotedValues 写回 config.params 文本。原始视图收进可折叠「高级」入口兜底。

/** 当前完整 config(typed + extras 合并),供 studio 解析提升参数定义/值。 */
const liveConfig = computed<Record<string, string>>(() => currentConfig())

/** 是否工作室节点(templated 或带 __studio)。 */
const isStudio = computed<boolean>(() => isStudioNode(localType.value, liveConfig.value))

/** 提升参数定义(只读):key/label/type/options + 当前值(default 字段)。 */
const promotedParams = computed<PromotedParam[]>(() =>
  isStudio.value ? parsePromotedParams(liveConfig.value) : [],
)

/** 该节点是否值得显示短清单(工作室节点且至少有一个提升参数)。 */
const showShortlist = computed<boolean>(() => promotedParams.value.length > 0)

/** 短清单当前值表(key → value),由 params 文本解析而来。 */
const shortlistValues = computed<Record<string, string>>(() => promotedValues(liveConfig.value))

/** 工作室实例:原始参数视图(image/params/commandTemplate 等)默认收起。 */
const showStudioRaw = ref(false)

/** 短清单改值 → 写回 params 文本(只动 value,不碰 __studio 定义)→ flush。 */
function onShortlistUpdate(values: Record<string, string>): void {
  const params = applyPromotedValues(promotedParams.value, values)
  typedConfig.value = { ...typedConfig.value, params }
  flush()
}

// ─── 预置目录(构建环境 / 配置资源)───────────────────────────────────────────
// 流水线编辑器的镜像只来自预置目录(R4):buildenv 控件列出已启用环境,选中即把目录里的
// image / language:version 一并写回 config,但**没有手填地址的入口** —— 保存校验(#8)与
// 运行时解析(#9)都只认目录里的条目,目录外的镜像一律报错、要求重新选择(旧配置不迁移)。
// 配置资源(configprofiles 多选)按所选环境的语言过滤,存逗号分隔 ID,运行时只读注入容器(#10)。
// 注:state 声明须在下方 hydrate 之前 —— splitOnType 会按旧镜像反查预置。
const presetEnvs = ref<PresetBuildEnv[]>([])
const presetProfiles = ref<ConfigProfile[]>([])
const presetsReady = ref(false)

// Initial hydrate (after the computeds above are declared, see watch comment).
hydrate(props.job)

function credentialOptions(field: JobField): Credential[] {
  const all = props.credentials ?? []
  if (!field.credentialType) return all
  return all.filter((c) => c.type === field.credentialType)
}

// ─── 部署节点「产物来源任务」候选(按流水线依赖关系算,见 artifactSources.ts) ───────────
// 只认整条流水线:同阶段的并行任务与本阶段之外的上游阶段都可能被本部署节点取产物。

const artifactSources = computed(() =>
  artifactSourceGroups(props.allStages ?? [], props.stage.id, props.job.id),
)

/** 当前值已不在候选里(来源任务被删/改名/改到下游):显式列出而不是静默变成「自动」。 */
const staleArtifactSource = computed(() => {
  const v = typedConfig.value.artifactFrom ?? ''
  if (!v) return ''
  for (const g of artifactSources.value) if (g.jobs.some((j) => j.id === v)) return ''
  return v
})

onMounted(async () => {
  try {
    const [envs, profiles] = await Promise.all([listEnabledBuildEnvs(), listEnabledConfigProfiles('')])
    presetEnvs.value = envs
    presetProfiles.value = profiles
    presetsReady.value = true
    // 预置到达前 hydrate 过的旧配置,这里补一次反查预填。
    splitOnType(localType.value, currentConfig(), false)
  } catch {
    presetsReady.value = false
  }
})

function presetById(id: string): PresetBuildEnv | undefined {
  return presetEnvs.value.find((e) => e.id === id)
}

/** 旧镜像串 → 预置(精确匹配 image 字段)。 */
function presetByImage(image: string): PresetBuildEnv | undefined {
  const needle = image.trim()
  if (!needle) return undefined
  return presetEnvs.value.find((e) => e.image === needle)
}

/** 旧 toolchainLanguage/Version → 预置(语言精确 + 版本前缀匹配,如 "20" 命中 "20-alpine")。 */
function presetByToolchain(language: string, version: string): PresetBuildEnv | undefined {
  const lang = language.trim()
  if (!lang) return undefined
  const ver = version.trim()
  return presetEnvs.value.find(
    (e) => e.language === lang && (ver === '' || e.version === ver || e.image.endsWith(`:${ver}`)),
  )
}

/** 控件当前应显示的环境 ID:显式 buildEnvId 优先,其次旧键反查。
 * 旧镜像键(image / toolchain*)由本控件托管 —— 不进「原始参数」,但值仍留在 config 里,
 * 所以这里读完整 config 而不是只看 typed 字段。 */
function buildEnvFieldValue(): string {
  const cfg = currentConfig()
  const explicit = cfg.buildEnvId
  if (explicit) return explicit
  const byImage = cfg.image ? presetByImage(cfg.image) : undefined
  if (byImage) return byImage.id
  const byTc = presetByToolchain(cfg.toolchainLanguage ?? '', cfg.toolchainVersion ?? '')
  return byTc?.id ?? ''
}

/** 旧值完全不在目录里时需展示告警占位(值为 '' 时下拉显示未选中,无法区分)。 */
function hasUnmatchedLegacyImage(): boolean {
  const cfg = currentConfig()
  if (cfg.buildEnvId) return false
  const legacy =
    (cfg.image ?? '') !== '' ||
    (cfg.toolchainLanguage ?? '') !== '' ||
    (cfg.toolchainVersion ?? '') !== ''
  return legacy && buildEnvFieldValue() === ''
}

/** 选中预置环境:写 buildEnvId + 旧键镜像(image 或 toolchain 拆分),随后 flush。 */
function onBuildEnvSelect(id: string): void {
  const preset = presetById(id)
  const patch: Record<string, string> = { buildEnvId: id }
  if (preset) {
    const isToolchainNode = effectiveJobType(localType.value, currentConfig()) === 'build_image'
    if (isToolchainNode) {
      // 目录里的 image 形如 language:version —— 按最后一个冒号拆,拼回与原镜像一致。
      const idx = preset.image.lastIndexOf(':')
      if (idx > 0) {
        patch.toolchainLanguage = preset.image.slice(0, idx)
        patch.toolchainVersion = preset.image.slice(idx + 1)
      } else {
        patch.toolchainLanguage = preset.image
        patch.toolchainVersion = 'latest'
      }
      delete patch.image
    } else {
      patch.image = preset.image
    }
  }
  typedConfig.value = { ...typedConfig.value, ...patch }
  flush()
}

/** 配置资源多选:逗号分隔 ID 存 configProfileIds。 */
function selectedProfileIds(): string[] {
  return (typedConfig.value.configProfileIds ?? '').split(',').map((s) => s.trim()).filter(Boolean)
}

/** 候选项按所选环境的语言过滤;未选环境时给全量。 */
function profileOptions(): ConfigProfile[] {
  const env = presetById(buildEnvFieldValue())
  if (!env) return presetProfiles.value
  const same = presetProfiles.value.filter((p) => p.language === env.language)
  return same.length > 0 ? same : presetProfiles.value
}

function toggleProfile(id: string): void {
  const cur = selectedProfileIds()
  const next = cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id]
  typedConfig.value = { ...typedConfig.value, configProfileIds: next.join(',') }
  flush()
}

/** 工作室提升参数里的 image 参数:选项限定预置目录(值仍是镜像串,渲染后后端二次校验)。 */
const studioImageOptions = computed(() =>
  presetEnvs.value.map((e) => ({ value: e.image, label: `${e.displayName} · ${e.image}` })),
)

const CHANNEL_TYPE_LABELS: Record<string, string> = {
  webhook: 'Webhook',
  email: t('pipelineJob.channelTypeEmail'),
  feishu: t('pipelineJob.channelTypeFeishu'),
  wecom: t('pipelineJob.channelTypeWecom'),
  dingtalk: t('pipelineJob.channelTypeDingtalk'),
}

function channelTypeLabel(type: string): string {
  return CHANNEL_TYPE_LABELS[type] ?? type
}

// ─── Value get/set ────────────────────────────────────────────────────────────

function fieldValue(key: string): string {
  return typedConfig.value[key] ?? ''
}

/** Default-display value for selects so the first option shows before any edit. */
function selectValue(field: JobField): string {
  const v = typedConfig.value[field.key]
  if (v) return v
  return field.options?.[0]?.value ?? ''
}

function updateLocal(key: string, value: string): void {
  typedConfig.value = { ...typedConfig.value, [key]: value }
}

function setField(key: string, value: string): void {
  updateLocal(key, value)
  flush()
}

// ─── Raw extras (advanced) ─────────────────────────────────────────────────────

function addExtra(): void {
  extraRows.value.push({ _key: ++_kvSeq, k: '', v: '' })
}

function removeExtra(key: number): void {
  extraRows.value = extraRows.value.filter((r) => r._key !== key)
  flush()
}

function extrasToObject(rows: KVRow[]): Record<string, string> {
  const out: Record<string, string> = {}
  for (const row of rows) {
    const key = row.k.trim()
    if (key) out[key] = row.v
  }
  return out
}

/** 受管键(image / toolchain*)与已删假字段(registry 等)不许在「原始参数」里手填 ——
 *  那正是预置目录(R4/R5)要堵死的后门;输入即撤销该行,与表单里消失的镜像控件同一口径。 */
function onExtraBlur(row: KVRow): void {
  const k = row.k.trim()
  if (k && (managedKeys(localType.value).has(k) || droppedKeys(localType.value).has(k))) {
    removeExtra(row._key)
    return
  }
  flush()
}

// ─── Flush ─────────────────────────────────────────────────────────────────────

/** Build the full config object from typed form + advanced extras (typed wins on conflict). */
function currentConfig(): Record<string, string> {
  return { ...extrasToObject(extraRows.value), ...typedConfig.value }
}

function flush(): void {
  emit('update', {
    name:    localName.value.trim() || props.job.name,
    type:    localType.value.trim() || props.job.type,
    summary: localSummary.value,
    config:  currentConfig(),
  })
}

// ─── Save as custom node (复用库 Tier 2) ──────────────────────────────────────
// Snapshot this node's type + summary + config into the reuse library so it can be
// picked into any pipeline later, pre-filled. Free-form config (no schema enforced).

const savePanelOpen = ref(false)
const saveName      = ref('')
const saveDesc      = ref('')
const saving        = ref(false)
const saveError     = ref('')
const saveOk        = ref(false)

function openSavePanel(): void {
  saveName.value = localName.value.trim()
  saveDesc.value = ''
  saveError.value = ''
  saveOk.value = false
  savePanelOpen.value = true
}

function cancelSave(): void {
  savePanelOpen.value = false
  saveError.value = ''
}

async function confirmSave(): Promise<void> {
  const name = saveName.value.trim()
  if (!name) {
    saveError.value = t('pipelineJob.saveErrEmptyName')
    return
  }
  saving.value = true
  saveError.value = ''
  try {
    await createCustomNode({
      name,
      description: saveDesc.value.trim(),
      nodeType: localType.value.trim() || props.job.type,
      summary: localSummary.value,
      config: currentConfig(),
    })
    saveOk.value = true
    savePanelOpen.value = false
  } catch (err) {
    saveError.value =
      err instanceof Error && err.message ? err.message : t('pipelineJob.saveErrFailed')
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <aside class="job-drawer" :aria-label="t('pipelineJob.drawerAria')">
    <!-- Head -->
    <div class="drawer-head">
      <span class="drawer-icon" aria-hidden="true">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9">
          <rect x="3" y="4" width="18" height="6" rx="1.6"/>
          <rect x="3" y="14" width="18" height="6" rx="1.6"/>
        </svg>
      </span>
      <div class="drawer-title">
        {{ localName || t('pipelineJob.unnamedJob') }}
        <small class="drawer-subtitle">{{ localizeName(stage.name) }} · {{ t('pipelineJob.thisPipeline') }}</small>
      </div>
      <button
        class="drawer-close"
        :aria-label="t('pipelineJob.closeDrawer')"
        @click="emit('close')"
      >
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
          <path d="M18 6 6 18M6 6l12 12"/>
        </svg>
      </button>
    </div>

    <!-- Basic info section -->
    <div class="drawer-section">
      <div class="drawer-section-label">{{ t('pipelineJob.basicInfo') }}</div>

      <div class="drawer-field">
        <div class="drawer-field-label">{{ t('pipelineJob.jobNameLabel') }}</div>
        <input
          v-model="localName"
          class="drawer-input"
          type="text"
          :placeholder="t('pipelineJob.jobNamePlaceholder')"
          :aria-label="t('pipelineJob.jobNameLabel')"
          @blur="flush"
        />
      </div>

      <div class="drawer-field">
        <div class="drawer-field-label">{{ t('pipelineJob.jobTypeLabel') }}</div>
        <button
          type="button"
          class="type-trigger"
          :aria-label="t('pipelineJob.changeType')"
          @click="emit('change-type')"
        >
          <JobTypeIcon :type="localType" :size="32" />
          <span class="type-trigger-body">
            <span class="type-trigger-name">{{ jobTypeLabel(localType) }}</span>
            <span class="type-trigger-desc">{{ spec ? spec.description : localType }}</span>
          </span>
          <span class="type-trigger-action">{{ t('pipelineJob.change') }}</span>
        </button>
      </div>

      <div class="drawer-field">
        <div class="drawer-field-label">{{ t('pipelineJob.summaryLabel') }}</div>
        <input
          v-model="localSummary"
          class="drawer-input"
          type="text"
          :placeholder="t('pipelineJob.summaryPlaceholder')"
          :aria-label="t('pipelineJob.summaryLabel')"
          @blur="flush"
        />
      </div>
    </div>

    <!-- 工作室节点「实例参数短清单」:只展示作者提升的参数,类型化控件,default 预填。 -->
    <div v-if="showShortlist" class="drawer-section">
      <div class="drawer-section-label">{{ t('pipelineJob.instanceParams') }}</div>
      <p class="shortlist-hint">
        {{ t('pipelineJob.instanceParamsHint') }}
      </p>
      <StudioInstanceParams
        :params="promotedParams"
        :model-value="shortlistValues"
        :image-options="studioImageOptions"
        @update:model-value="onShortlistUpdate"
      />
    </div>

    <!-- 工作室实例:原始命令模板/参数视图收进可折叠「高级」入口(默认收起)。 -->
    <div v-if="showShortlist && spec" class="drawer-section drawer-studio-raw-toggle">
      <button
        class="advanced-toggle"
        :class="{ 'advanced-toggle--open': showStudioRaw }"
        :aria-expanded="showStudioRaw"
        @click="showStudioRaw = !showStudioRaw"
      >
        <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" aria-hidden="true">
          <path d="M9 18l6-6-6-6"/>
        </svg>
        {{ t('pipelineJob.advancedRawToggle') }}
      </button>
    </div>

    <!-- Typed config section (per-type form) -->
    <div v-if="spec && (!showShortlist || showStudioRaw)" class="drawer-section">
      <div class="drawer-section-head">
        <div class="drawer-section-label">{{ spec.label }}{{ t('pipelineJob.configSuffix') }}</div>
        <div v-if="canUseStepBuilder && !showShortlist" class="view-switch" role="tablist" :aria-label="t('pipelineJob.configView')">
          <button
            class="view-tab"
            :class="{ 'view-tab--active': viewMode === 'steps' }"
            role="tab"
            :aria-selected="viewMode === 'steps'"
            @click="viewMode = 'steps'"
          >
            {{ t('pipelineJob.visualSteps') }}
          </button>
          <button
            class="view-tab"
            :class="{ 'view-tab--active': viewMode === 'raw' }"
            role="tab"
            :aria-selected="viewMode === 'raw'"
            @click="viewMode = 'raw'"
          >
            {{ t('pipelineJob.rawParams') }}
          </button>
        </div>
      </div>

      <template v-for="field in visibleFields" :key="field.key">
        <!-- 高级折叠开关:插在第一个高级(执行/缓存)字段前 -->
        <button
          v-if="field.key === firstAdvancedKey"
          type="button"
          class="advanced-toggle exec-advanced-toggle"
          :class="{ 'advanced-toggle--open': showExecAdvanced }"
          :aria-expanded="showExecAdvanced"
          @click="showExecAdvanced = !showExecAdvanced"
        >
          <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" aria-hidden="true">
            <path d="M9 18l6-6-6-6"/>
          </svg>
          {{ t('pipelineJob.execAdvancedToggle') }}
        </button>
      <div
        v-show="!ADVANCED_FIELD_KEYS.has(field.key) || showExecAdvanced"
        class="drawer-field"
      >
        <div class="drawer-field-label">{{ field.label }}</div>

        <!-- textarea -->
        <textarea
          v-if="field.kind === 'textarea'"
          :value="fieldValue(field.key)"
          class="drawer-input drawer-textarea"
          :class="{ 'is-mono': field.monospace }"
          rows="4"
          :placeholder="field.placeholder"
          :aria-label="field.label"
          @input="updateLocal(field.key, ($event.target as HTMLTextAreaElement).value)"
          @blur="flush"
        ></textarea>

        <!-- select -->
        <select
          v-else-if="field.kind === 'select'"
          :value="selectValue(field)"
          class="drawer-select"
          :aria-label="field.label"
          @change="setField(field.key, ($event.target as HTMLSelectElement).value)"
        >
          <option v-for="opt in field.options" :key="opt.value" :value="opt.value">
            {{ opt.label }}
          </option>
        </select>

        <!-- credential picker -->
        <select
          v-else-if="field.kind === 'credential'"
          :value="fieldValue(field.key)"
          class="drawer-select"
          :aria-label="field.label"
          @change="setField(field.key, ($event.target as HTMLSelectElement).value)"
        >
          <option value="">{{ t('pipelineJob.credUnselected') }}</option>
          <option v-for="c in credentialOptions(field)" :key="c.id" :value="c.id">
            {{ c.name }} ({{ c.maskedValue }})
          </option>
        </select>

        <!-- server picker -->
        <select
          v-else-if="field.kind === 'server'"
          :value="fieldValue(field.key)"
          class="drawer-select"
          :aria-label="field.label"
          @change="setField(field.key, ($event.target as HTMLSelectElement).value)"
        >
          <option value="">{{ t('pipelineJob.credUnselected') }}</option>
          <option v-for="srv in (servers ?? [])" :key="srv.id" :value="srv.id">
            {{ srv.name }} · {{ srv.host }}
          </option>
        </select>

        <!-- 产物来源任务(部署节点):按流水线依赖关系列出「此刻一定已产出产物」的构建任务。
             并行构建出多件同类型产物时,这是唯一能指明「发哪一件」的入口。 -->
        <select
          v-else-if="field.kind === 'artifactsource'"
          :value="fieldValue(field.key)"
          class="drawer-select"
          :aria-label="field.label"
          @change="setField(field.key, ($event.target as HTMLSelectElement).value)"
        >
          <option value="">{{ t('pipelineJob.fieldArtifactFromAuto') }}</option>
          <option v-if="staleArtifactSource" :value="staleArtifactSource">
            {{ t('pipelineJob.fieldArtifactFromMissing') }}
          </option>
          <optgroup
            v-for="g in artifactSources"
            :key="g.stageId"
            :label="localizeName(g.stageName)"
          >
            <option v-for="j in g.jobs" :key="j.id" :value="j.id">
              {{ localizeName(j.name) }}
            </option>
          </optgroup>
        </select>

        <!-- notification channel picker -->
        <select
          v-else-if="field.kind === 'channel'"
          :value="fieldValue(field.key)"
          class="drawer-select"
          :aria-label="field.label"
          @change="setField(field.key, ($event.target as HTMLSelectElement).value)"
        >
          <option value="">{{ t('pipelineJob.channelUnselected') }}</option>
          <option v-for="ch in (channels ?? [])" :key="ch.id" :value="ch.id">
            {{ ch.name }} · {{ channelTypeLabel(ch.type) }}{{ ch.enabled ? '' : t('pipelineJob.channelDisabled') }}
          </option>
        </select>

        <!-- 预置构建环境选择器(镜像唯一来源,R4) -->
        <select
          v-else-if="field.kind === 'buildenv'"
          class="drawer-select"
          :value="buildEnvFieldValue()"
          :aria-label="field.label"
          @change="onBuildEnvSelect(($event.target as HTMLSelectElement).value)"
        >
          <option value="">{{
            hasUnmatchedLegacyImage()
              ? t('pipelineJob.fieldBuildEnvLegacy')
              : t('pipelineJob.fieldBuildEnvUnselected')
          }}</option>
          <option v-for="env in presetEnvs" :key="env.id" :value="env.id">
            {{ env.displayName }} · {{ env.image }}
          </option>
        </select>

        <!-- 配置资源多选(按所选环境语言过滤) -->
        <div v-else-if="field.kind === 'configprofiles'" class="profile-multi">
          <label v-for="p in profileOptions()" :key="p.id" class="profile-option">
            <input
              type="checkbox"
              :checked="selectedProfileIds().includes(p.id)"
              :aria-label="p.name"
              @change="toggleProfile(p.id)"
            />
            <span class="profile-option-name">{{ p.name }}</span>
            <code class="profile-option-path">{{ p.targetPath }}</code>
          </label>
          <p v-if="profileOptions().length === 0" class="field-hint">
            {{ t('pipelineJob.fieldConfigProfilesEmpty') }}
          </p>
        </div>

        <!-- toggle -->
        <label v-else-if="field.kind === 'toggle'" class="drawer-toggle">
          <input
            type="checkbox"
            :checked="fieldValue(field.key) === 'true'"
            :aria-label="field.label"
            @change="setField(field.key, ($event.target as HTMLInputElement).checked ? 'true' : 'false')"
          />
          <span>{{ field.hint || t('pipelineJob.toggleEnable') }}</span>
        </label>

        <!-- 推送目标只读回显:registry 来自「环境」绑定,tag 由服务端按 commit 生成 -->
        <div v-else-if="field.kind === 'pushTarget'" class="push-target">
          <ul v-if="pushTargets.length" class="push-target-list">
            <li v-for="pt in pushTargets" :key="pt.env" class="push-target-row">
              <span class="push-target-env">{{ pt.env }}</span>
              <code class="push-target-url">{{ pt.registry || t('pipelineJob.fieldPushTargetUnbound') }}</code>
            </li>
          </ul>
          <p v-else class="field-hint">{{ t('pipelineJob.fieldPushTargetNone') }}</p>
        </div>

        <!-- number / text -->
        <input
          v-else
          :value="fieldValue(field.key)"
          class="drawer-input"
          :class="{ 'is-mono': field.monospace }"
          :type="field.kind === 'number' ? 'number' : 'text'"
          :placeholder="field.placeholder"
          :aria-label="field.label"
          @input="updateLocal(field.key, ($event.target as HTMLInputElement).value)"
          @blur="flush"
        />

        <p v-if="field.hint && field.kind !== 'toggle'" class="field-hint">{{ field.hint }}</p>
      </div>
      </template>

      <!-- 可视化步骤构建器(脚本类节点;编译进 commands/artifactPath,经 update 落库) -->
      <div v-if="canUseStepBuilder && viewMode === 'steps'" class="drawer-field step-builder-field">
        <StepBuilder :config="typedConfig" @update="onStepsUpdate" />
        <p class="field-hint">
          {{ t('pipelineJob.stepBuilderHint') }}
        </p>
      </div>
    </div>

    <!-- Advanced raw KV (extras not covered by the schema) -->
    <div class="drawer-section">
      <button
        class="advanced-toggle"
        :class="{ 'advanced-toggle--open': showAdvanced }"
        :aria-expanded="showAdvanced"
        @click="showAdvanced = !showAdvanced"
      >
        <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" aria-hidden="true">
          <path d="M9 18l6-6-6-6"/>
        </svg>
        {{ t('pipelineJob.advancedRawParams') }}<span v-if="extraRows.length" class="advanced-count">{{ extraRows.length }}</span>
      </button>

      <div v-if="showAdvanced" class="advanced-body">
        <div v-if="extraRows.length === 0" class="kv-empty">
          {{ spec ? t('pipelineJob.kvEmptyWithSpec') : t('pipelineJob.kvEmptyNoSpec') }}
        </div>

        <div
          v-for="row in extraRows"
          :key="row._key"
          class="kv-row"
        >
          <input
            v-model="row.k"
            class="kv-input"
            type="text"
            :placeholder="t('pipelineJob.kvKeyPlaceholder')"
            :aria-label="t('pipelineJob.kvKeyAria', { n: row._key })"
            @blur="onExtraBlur(row)"
          />
          <input
            v-model="row.v"
            class="kv-input"
            type="text"
            :placeholder="t('pipelineJob.kvValuePlaceholder')"
            :aria-label="t('pipelineJob.kvValueAria', { n: row._key })"
            @blur="flush"
          />
          <button
            class="kv-del"
            :aria-label="t('pipelineJob.kvDelAria', { label: row.k || row._key })"
            @click="removeExtra(row._key)"
          >
            <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
              <path d="M18 6 6 18M6 6l12 12"/>
            </svg>
          </button>
        </div>

        <button class="kv-add-btn" @click="addExtra">
          <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" aria-hidden="true">
            <path d="M12 5v14M5 12h14"/>
          </svg>
          {{ t('pipelineJob.addParam') }}
        </button>
      </div>
    </div>

    <!-- Save as custom node (复用库 Tier 2) -->
    <div class="drawer-section drawer-reuse">
      <div v-if="!savePanelOpen" class="reuse-row">
        <button class="reuse-btn" @click="openSavePanel">
          <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
            <path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z"/>
            <path d="M17 21v-8H7v8M7 3v5h8"/>
          </svg>
          {{ t('pipelineJob.saveAsCustomNode') }}
        </button>
        <span v-if="saveOk" class="reuse-ok" role="status">{{ t('pipelineJob.savedToLibrary') }}</span>
      </div>

      <div v-else class="reuse-panel">
        <div class="reuse-panel-title">{{ t('pipelineJob.saveAsCustomNode') }}</div>
        <p class="reuse-panel-hint">{{ t('pipelineJob.savePanelHint') }}</p>
        <input
          v-model="saveName"
          class="drawer-input"
          type="text"
          :placeholder="t('pipelineJob.saveNamePlaceholder')"
          :aria-label="t('pipelineJob.saveNameAria')"
          @keydown.enter.prevent="confirmSave"
        />
        <input
          v-model="saveDesc"
          class="drawer-input"
          type="text"
          :placeholder="t('pipelineJob.saveDescPlaceholder')"
          :aria-label="t('pipelineJob.saveDescAria')"
        />
        <p v-if="saveError" class="reuse-error" role="alert">{{ saveError }}</p>
        <div class="reuse-actions">
          <button class="reuse-cancel" :disabled="saving" @click="cancelSave">{{ t('pipelineJob.cancel') }}</button>
          <button class="reuse-confirm" :disabled="saving" @click="confirmSave">
            {{ saving ? t('pipelineJob.saving') : t('pipelineJob.save') }}
          </button>
        </div>
      </div>
    </div>
  </aside>
</template>

<style scoped>
.drawer-section-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  margin-bottom: 9px;
}

.drawer-section-head .drawer-section-label {
  margin-bottom: 0;
}

.view-switch {
  display: inline-flex;
  padding: 2px;
  background: var(--color-inset);
  border: 1px solid var(--color-border);
  border-radius: var(--rounded-full);
}

.view-tab {
  padding: 3px 10px;
  background: none;
  border: none;
  border-radius: var(--rounded-full);
  color: var(--color-faint);
  font: inherit;
  font-size: 0.72rem;
  font-weight: 600;
  cursor: pointer;
  transition: color var(--duration-fast), background-color var(--duration-fast);
}

.view-tab:hover {
  color: var(--color-text);
}

.view-tab--active {
  background: var(--color-primary);
  color: #fff;
}

.view-tab:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: 2px;
}

.step-builder-field {
  margin-top: 4px;
}

.shortlist-hint {
  margin: 0 0 12px;
  font-size: 0.73rem;
  line-height: 1.45;
  color: var(--color-faint);
}

.drawer-studio-raw-toggle {
  padding-top: 0;
  margin-top: -4px;
}

.kv-empty {
  font-size: 0.78rem;
  color: var(--color-faint);
  font-style: italic;
  margin-bottom: 8px;
}

.field-desc {
  margin: 6px 0 0;
  font-size: 0.74rem;
  color: var(--color-faint);
}

.type-trigger {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 8px 10px;
  background: var(--color-inset);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--rounded);
  cursor: pointer;
  text-align: left;
  transition: border-color var(--duration-fast), box-shadow var(--duration-fast);
}

.type-trigger:hover {
  border-color: var(--color-primary);
}

.type-trigger:focus-visible {
  outline: none;
  border-color: var(--color-primary);
  box-shadow: 0 0 0 3px var(--color-primary-soft);
}

.type-trigger-body {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
  flex: 1;
}

.type-trigger-name {
  font-size: 0.86rem;
  font-weight: 600;
  color: var(--color-text);
}

.type-trigger-desc {
  font-size: 0.73rem;
  color: var(--color-faint);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.type-trigger-action {
  flex-shrink: 0;
  font-size: 0.74rem;
  font-weight: 600;
  color: var(--color-primary);
}

.field-hint {
  margin: 5px 0 0;
  font-size: 0.72rem;
  color: var(--color-faint);
  line-height: 1.4;
}

/* 推送目标只读回显:环境 → 该环境绑定的镜像仓 */
.push-target {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.push-target-list {
  margin: 0;
  padding: 8px 10px;
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 6px;
  border: 1px solid var(--color-border);
  border-radius: var(--rounded-md, 6px);
  background: var(--color-inset);
}
.push-target-row {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 10px;
  font-size: 0.82rem;
}
.push-target-env { font-weight: 500; color: var(--color-text); }
.push-target-url {
  font-family: var(--font-mono, ui-monospace, monospace);
  font-size: 0.72rem;
  color: var(--color-faint);
  overflow-wrap: anywhere;
}

/* 配置资源多选:语言过滤后的复选清单 */
.profile-multi {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 8px 10px;
  border: 1px solid var(--color-border);
  border-radius: var(--rounded-md, 6px);
  background: var(--color-inset);
  max-height: 180px;
  overflow-y: auto;
}
.profile-option {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 0.82rem;
  color: var(--color-text);
  cursor: pointer;
}
.profile-option input { flex: none; }
.profile-option-name { font-weight: 500; }
.profile-option-path {
  font-family: var(--font-mono, ui-monospace, monospace);
  font-size: 0.72rem;
  color: var(--color-faint);
}

.drawer-textarea {
  height: auto;
  min-height: 84px;
  padding: 9px 11px;
  line-height: 1.5;
  resize: vertical;
}

.is-mono {
  font-family: var(--font-mono);
  font-size: 0.8rem;
}

.drawer-toggle {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font-size: 0.8rem;
  color: var(--color-dim);
  cursor: pointer;
}

.drawer-toggle input {
  width: 15px;
  height: 15px;
  accent-color: var(--color-primary);
  cursor: pointer;
}

.advanced-toggle {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  background: none;
  border: none;
  padding: 0;
  color: var(--color-dim);
  font: inherit;
  font-size: 0.76rem;
  font-weight: 600;
  cursor: pointer;
  text-transform: uppercase;
  letter-spacing: 0.02em;
}

.advanced-toggle svg {
  transition: transform var(--duration-fast);
}

.advanced-toggle--open svg {
  transform: rotate(90deg);
}

.advanced-toggle:hover {
  color: var(--color-text);
}

.advanced-count {
  display: inline-grid;
  place-items: center;
  min-width: 16px;
  height: 16px;
  padding: 0 4px;
  border-radius: 8px;
  background: var(--color-primary-soft);
  color: var(--color-primary);
  font-size: 0.66rem;
  font-weight: 700;
}

.advanced-body {
  margin-top: 11px;
}

/* ——— Save as custom node ——— */
.drawer-reuse {
  border-top: 1px dashed var(--color-border);
  padding-top: 14px;
}

.reuse-row {
  display: flex;
  align-items: center;
  gap: 10px;
}

.reuse-btn {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  padding: 7px 12px;
  background: var(--color-inset);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--rounded);
  color: var(--color-text);
  font: inherit;
  font-size: 0.8rem;
  font-weight: 600;
  cursor: pointer;
  transition: border-color var(--duration-fast), color var(--duration-fast);
}

.reuse-btn:hover {
  border-color: var(--color-primary);
  color: var(--color-primary);
}

.reuse-ok {
  font-size: 0.76rem;
  font-weight: 600;
  color: var(--color-green, var(--color-primary));
}

.reuse-panel {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 12px;
  background: var(--color-inset);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--rounded);
}

.reuse-panel-title {
  font-size: 0.84rem;
  font-weight: 650;
  color: var(--color-text);
}

.reuse-panel-hint {
  margin: 0;
  font-size: 0.73rem;
  line-height: 1.45;
  color: var(--color-faint);
}

.reuse-error {
  margin: 0;
  font-size: 0.74rem;
  color: var(--color-red, #d33);
}

.reuse-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 2px;
}

.reuse-cancel,
.reuse-confirm {
  padding: 6px 14px;
  border-radius: var(--rounded);
  font: inherit;
  font-size: 0.8rem;
  font-weight: 600;
  cursor: pointer;
  transition: opacity var(--duration-fast), background-color var(--duration-fast);
}

.reuse-cancel {
  background: none;
  border: 1px solid var(--color-border-strong);
  color: var(--color-dim);
}

.reuse-cancel:hover:not(:disabled) {
  color: var(--color-text);
}

.reuse-confirm {
  background: var(--color-primary);
  border: 1px solid var(--color-primary);
  color: #fff;
}

.reuse-confirm:hover:not(:disabled) {
  opacity: 0.9;
}

.reuse-cancel:disabled,
.reuse-confirm:disabled {
  opacity: 0.55;
  cursor: not-allowed;
}
</style>
