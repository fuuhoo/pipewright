<script setup lang="ts">
/**
 * StepBuilder — 节点内可视化步骤构建器(低代码 · Tier 3)。
 *
 * 用「加步骤」+ 拖拽/上下移排序拼出脚本块,实时编译成现有可执行 config 键
 * (commands 多行),经 emit('update') 落库。绝不新增执行语义:
 * 编译/反解析全在 stepCompile.ts 的纯函数里(见其文件头契约)。
 *
 * 父级(JobDrawer)负责持有 image/workDir/artifactPath 等节点级字段;本组件只管
 * 「步骤」那部分,把 commands 这一个键回传。产物目录是节点的固定属性(档位=产物时表单里
 * 始终有这一格),不在「加步骤」里。
 */
import { ref, watch, computed, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  type StepBlock,
  type StepKind,
  type CopyMode,
  type CopyStyle,
  STEP_KIND_META,
  copyStyleLabel,
  stepKindLabel,
  nextStepId,
  parseSteps,
  compileSteps,
  lineToStep,
} from './stepCompile'
import type { CommandTemplate } from './commandTemplates'
import CommandTemplateChips from './CommandTemplateChips.vue'
import { rsyncSupportFor } from '../../lib/buildEnvRsync'

const { t } = useI18n()

const props = defineProps<{
  /** 当前节点的完整 config(用于初始反解析) */
  config: Record<string, string>
  /** 所选构建环境的语言:命令模板芯片据此前置(父级知道,本组件不碰预置目录) */
  language?: string
  /**
   * 该节点实际用的镜像串(父级按 buildEnvId → 目录解析好再传进来)。
   * 「高级复制」的排除项只有镜像装了 rsync 才生效,而这一格说了算;「复制」是一行 cp,不看它。
   */
  image?: string
}>()

const emit = defineEmits<{
  /** 步骤变更后回传编译出的 config 片段(只含 commands) */
  (e: 'update', patch: { commands: string }): void
}>()

const steps = ref<StepBlock[]>([])

/**
 * 「高级复制」能不能排除,只看镜像里装没装 rsync。编辑态问不了实时(要拉镜像 + `docker run`,
 * 还只有本机 daemon 准),所以只认那份实测清单:内置目录里的镜像给确定答案,目录外一律
 * 说「运行时决定」—— 绝不替用户猜一个没测过的镜像里有什么。
 */
const rsyncState = computed(() => rsyncSupportFor(props.image))

/** 复制卡片的提示分两套:「复制」是一行 cp、没有排除;「高级复制」讲 rsync 在不在。再按复制方式细分。 */
function copyHintText(step: StepBlock): string {
  // 一行 cp 的「复制」不碰 rsync,排除项没有落点 → 说的是另一套话。
  if ((step.copyStyle ?? 'advanced') === 'plain') {
    return step.copyMode === 'entry'
      ? t('pipelineJob.sbCopyPlainHintEntry')
      : t('pipelineJob.sbCopyPlainHint')
  }
  const image = (props.image ?? '').trim()
  if (step.copyMode === 'entry') {
    return rsyncState.value === 'no'
      ? t('pipelineJob.sbCopyHintEntryNo', { image })
      : t('pipelineJob.sbCopyHintEntry')
  }
  if (rsyncState.value === 'no') return t('pipelineJob.sbCopyHintNo', { image })
  if (rsyncState.value === 'yes') return t('pipelineJob.sbCopyHintYes', { image })
  return t('pipelineJob.sbCopyHint')
}

/** 初次 / 切换节点时从 config 反解析步骤(允许有损,空则起手给一个空命令步骤)。 */
function hydrate(config: Record<string, string>): void {
  const parsed = parseSteps(config)
  steps.value = parsed.length > 0 ? parsed : [blank('command')]
}

/**
 * 只在「外部」变更时重建步骤(切换节点、原始视图改 commands 等)。我们自己 flush 回传后,
 * 父级会把同一份 config 透传回来 —— 这种「自激」更新不能重建,否则正在编辑的空步骤(空 env/空 cd,
 * 编译成空串)会被反解析丢掉。判据:incoming 的 commands 与当前步骤的编译结果一致 → 自激,跳过。
 */
let hydrated = false
watch(
  () => props.config,
  (next) => {
    if (hydrated && (next.commands ?? '') === compileSteps(steps.value).commands) return
    hydrated = true
    hydrate(next)
  },
  { immediate: true },
)

function blank(kind: StepKind, copyStyle?: CopyStyle): StepBlock {
  return { id: nextStepId(), kind, ...(copyStyle ? { copyStyle } : {}) }
}

/** 编译当前步骤并回传给父级落库。 */
function flush(): void {
  emit('update', compileSteps(steps.value))
}

// ─── 增删步骤 ──────────────────────────────────────────────────────────────────

/**
 * 可插入/追加的步骤类型。复制占两项:「复制」(一行 cp,识别用户手写的 cp,也照那样写回)
 * 和「高级复制」(rsync 优先 + cp 兜底,支持排除项,老配置的唯一形态)。
 * `key` 只为 CSS 类名与 v-for key 服务(同一 kind 两条)。
 */
interface StepOption {
  key: string
  kind: StepKind
  copyStyle?: CopyStyle
  label: string
  desc: string
}

const addMenuOpen = ref(false)

const addOptions = computed<ReadonlyArray<StepOption>>(() => [
  { key: 'command', kind: 'command', label: STEP_KIND_META.command.label, desc: t('pipelineJob.sbAddCommandDesc') },
  { key: 'env', kind: 'env', label: STEP_KIND_META.env.label, desc: t('pipelineJob.sbAddEnvDesc') },
  { key: 'workDir', kind: 'workDir', label: STEP_KIND_META.workDir.label, desc: t('pipelineJob.sbAddWorkDirDesc') },
  {
    key: 'copyPlain',
    kind: 'copy',
    copyStyle: 'plain',
    label: copyStyleLabel('plain'),
    desc: t('pipelineJob.sbAddCopyPlainDesc'),
  },
  {
    key: 'copyAdvanced',
    kind: 'copy',
    copyStyle: 'advanced',
    label: copyStyleLabel('advanced'),
    desc: t('pipelineJob.sbAddCopyDesc'),
  },
  { key: 'condition', kind: 'condition', label: STEP_KIND_META.condition.label, desc: t('pipelineJob.sbAddConditionDesc') },
])

function addStep(opt: StepOption): void {
  steps.value = [...steps.value, blank(opt.kind, opt.copyStyle)]
  addMenuOpen.value = false
  flush()
}

/**
 * 步骤之间(含最前面)的「+」:点开是一张和「加步骤」同样的类型菜单,选完就地插入并聚焦
 * 它的第一个输入 —— 省掉「先加到末尾再拖到中间」。
 * 新步骤是空的,编译成空串,所以 flush 回传的 commands 和点开之前一模一样:父级把同一份
 * config 透传回来时会被自激守卫跳过,不会把这个还没填的空步骤反解析丢掉。
 */
const insertMenuIndex = ref<number | null>(null)

function toggleInsertMenu(index: number): void {
  insertMenuIndex.value = insertMenuIndex.value === index ? null : index
}

function insertFromMenu(index: number, opt: StepOption): void {
  const step = blank(opt.kind, opt.copyStyle)
  const arr = [...steps.value]
  arr.splice(index, 0, step)
  steps.value = arr
  insertMenuIndex.value = null
  flush()
  focusFirstField(step.id)
}

/**
 * 每种步骤的第一个输入按 step.id 登记(命令 textarea / env 的键 / 目录 / 复制的源 / 条件)。
 * 插入后要把焦点交给它,得等这一步真的渲染出来。
 */
const firstFieldEls = new Map<string, HTMLElement>()

function registerFirstField(id: string, el: Element | null): void {
  if (el) firstFieldEls.set(id, el as HTMLElement)
  else firstFieldEls.delete(id)
}

function focusFirstField(id: string): void {
  void nextTick(() => {
    const el = firstFieldEls.get(id)
    if (el && el !== document.activeElement) el.focus()
  })
}

/** 分隔条在卡片上方,所以第 0 条说「最前面」,其余按「第 N 步之后」报它插在哪。 */
function insertAria(index: number): string {
  return index === 0
    ? t('pipelineJob.sbInsertTopAria')
    : t('pipelineJob.sbInsertAfterAria', { n: index })
}

function removeStep(id: string): void {
  steps.value = steps.value.filter((s) => s.id !== id)
  flush()
}

/** 点语言模板 = 逐行追加成步骤(已有的步保留,空步骤编译时自然消失)。 */
function appendTemplate(tpl: CommandTemplate): void {
  steps.value = [...steps.value, ...tpl.commands.split('\n').map(lineToStep)]
  flush()
}

function patchStep(id: string, patch: Partial<StepBlock>): void {
  steps.value = steps.value.map((s) => (s.id === id ? { ...s, ...patch } : s))
}

/** 切换复制方式:立刻回传(下拉选择没有 blur 可等)。contents 是缺省值,写回对象也无妨。 */
function setCopyMode(step: StepBlock, mode: CopyMode): void {
  patchStep(step.id, { copyMode: mode })
  flush()
}

/** 切换复制写法:「复制」(一行 cp)↔「高级复制」(rsync 骨架)。切回 cp 要清掉排除项 —— cp 不吃,留着等于骗用户。 */
function setCopyStyle(step: StepBlock, style: CopyStyle): void {
  patchStep(step.id, style === 'plain' ? { copyStyle: style, copyExcludes: '' } : { copyStyle: style })
  flush()
}

// ─── 排序:上下移按钮 + 原生 HTML5 拖拽 ─────────────────────────────────────────

function move(index: number, delta: number): void {
  const next = index + delta
  if (next < 0 || next >= steps.value.length) return
  const arr = [...steps.value]
  const [item] = arr.splice(index, 1)
  arr.splice(next, 0, item)
  steps.value = arr
  flush()
}

const dragIndex = ref<number | null>(null)
const overIndex = ref<number | null>(null)

function onDragStart(index: number): void {
  dragIndex.value = index
}

function onDragOver(index: number, e: DragEvent): void {
  e.preventDefault()
  overIndex.value = index
}

function onDrop(index: number): void {
  const from = dragIndex.value
  dragIndex.value = null
  overIndex.value = null
  if (from === null || from === index) return
  const arr = [...steps.value]
  const [item] = arr.splice(from, 1)
  arr.splice(index, 0, item)
  steps.value = arr
  flush()
}

function onDragEnd(): void {
  dragIndex.value = null
  overIndex.value = null
}

const stepCount = computed(() => steps.value.length)
</script>

<template>
  <div class="step-builder">
    <div class="sb-head">
      <span class="sb-head-label">{{ t('pipelineJob.sbVisualSteps') }}</span>
      <span class="sb-count" :aria-label="t('pipelineJob.sbStepCount')">{{ stepCount }}</span>
    </div>

    <!-- 常用语言模板:步骤视图才是构建节点的默认视图,芯片放这里才看得见 -->
    <CommandTemplateChips :language="props.language" @pick="appendTemplate" />

    <ol class="sb-list">
      <template v-for="(step, index) in steps" :key="step.id">
        <!-- 每条卡片上方的分隔条 = 插到这一步之前;最后一条卡片下面另有收尾条。
             平时不占空间也不显形,悬停在相邻卡片上(或键盘走到那里)才浮出来。 -->
        <li class="sb-insert" :class="{ 'sb-insert--open': insertMenuIndex === index }">
          <button
            class="sb-insert-btn"
            :aria-label="insertAria(index)"
            :title="insertAria(index)"
            aria-haspopup="menu"
            :aria-expanded="insertMenuIndex === index"
            @click="toggleInsertMenu(index)"
          >
            <svg width="9" height="9" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.8" aria-hidden="true"><path d="M12 5v14M5 12h14" /></svg>
            <span class="sb-insert-label">{{ t('pipelineJob.sbInsertLabel') }}</span>
          </button>
          <div
            v-if="insertMenuIndex === index"
            class="sb-insert-menu"
            role="menu"
            :aria-label="t('pipelineJob.sbInsertMenuLabel')"
          >
            <button
              v-for="opt in addOptions"
              :key="opt.key"
              class="sb-add-item"
              :class="`sb-add-item--${opt.key}`"
              role="menuitem"
              @click="insertFromMenu(index, opt)"
            >
              <span class="sb-add-dot" aria-hidden="true"></span>
              <span class="sb-add-body">
                <span class="sb-add-name">{{ opt.label }}</span>
                <span class="sb-add-desc">{{ opt.desc }}</span>
              </span>
            </button>
          </div>
        </li>
        <li
          class="sb-step"
          :class="[
            `sb-step--${step.kind}`,
            { 'sb-step--dragging': dragIndex === index, 'sb-step--over': overIndex === index && dragIndex !== index },
          ]"
          draggable="true"
          @dragstart="onDragStart(index)"
          @dragover="onDragOver(index, $event)"
          @drop="onDrop(index)"
          @dragend="onDragEnd"
        >
          <div class="sb-step-bar">
            <span class="sb-grip" aria-hidden="true" :title="t('pipelineJob.sbDragSort')">
              <svg width="10" height="14" viewBox="0 0 10 14" fill="currentColor">
                <circle cx="2.5" cy="2" r="1.3" /><circle cx="7.5" cy="2" r="1.3" />
                <circle cx="2.5" cy="7" r="1.3" /><circle cx="7.5" cy="7" r="1.3" />
                <circle cx="2.5" cy="12" r="1.3" /><circle cx="7.5" cy="12" r="1.3" />
              </svg>
            </span>
            <span class="sb-kind">{{ stepKindLabel(step) }}</span>
            <span class="sb-order">{{ index + 1 }}</span>
            <span class="sb-step-actions">
              <button
                class="sb-move"
                :disabled="index === 0"
                :aria-label="t('pipelineJob.sbMoveUp', { n: index + 1 })"
                @click="move(index, -1)"
              >
                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" aria-hidden="true"><path d="M18 15l-6-6-6 6" /></svg>
              </button>
              <button
                class="sb-move"
                :disabled="index === steps.length - 1"
                :aria-label="t('pipelineJob.sbMoveDown', { n: index + 1 })"
                @click="move(index, 1)"
              >
                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" aria-hidden="true"><path d="M6 9l6 6 6-6" /></svg>
              </button>
              <button
                class="sb-del"
                :aria-label="t('pipelineJob.sbDelStep', { n: index + 1 })"
                @click="removeStep(step.id)"
              >
                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M18 6 6 18M6 6l12 12" /></svg>
              </button>
            </span>
          </div>

          <!-- 运行命令 -->
          <textarea
            v-if="step.kind === 'command'"
            :ref="(el) => registerFirstField(step.id, el as Element | null)"
            :value="step.command ?? ''"
            class="sb-input sb-textarea is-mono"
            rows="2"
            placeholder="npm ci&#10;npm run build"
            :aria-label="t('pipelineJob.sbCommandAria', { n: index + 1 })"
            @input="patchStep(step.id, { command: ($event.target as HTMLTextAreaElement).value })"
            @blur="flush"
          ></textarea>

          <!-- 设环境变量 -->
          <div v-else-if="step.kind === 'env'" class="sb-env">
            <input
              :ref="(el) => registerFirstField(step.id, el as Element | null)"
              :value="step.envKey ?? ''"
              class="sb-input is-mono"
              type="text"
              placeholder="KEY"
              :aria-label="t('pipelineJob.sbEnvKeyAria', { n: index + 1 })"
              @input="patchStep(step.id, { envKey: ($event.target as HTMLInputElement).value })"
              @blur="flush"
            />
            <span class="sb-eq" aria-hidden="true">=</span>
            <input
              :value="step.envValue ?? ''"
              class="sb-input is-mono"
              type="text"
              placeholder="value"
              :aria-label="t('pipelineJob.sbEnvValueAria', { n: index + 1 })"
              @input="patchStep(step.id, { envValue: ($event.target as HTMLInputElement).value })"
              @blur="flush"
            />
          </div>

          <!-- 切目录 -->
          <input
            v-else-if="step.kind === 'workDir'"
            :ref="(el) => registerFirstField(step.id, el as Element | null)"
            :value="step.dir ?? ''"
            class="sb-input is-mono"
            type="text"
            placeholder="frontend"
            :aria-label="t('pipelineJob.sbDirAria', { n: index + 1 })"
            @input="patchStep(step.id, { dir: ($event.target as HTMLInputElement).value })"
            @blur="flush"
          />

          <!-- 复制:写法(一行 cp / rsync 骨架)与复制方式(铺内容 / 连名一起拷)两格并排,再源 → 目标。 -->
          <div v-else-if="step.kind === 'copy'" class="sb-copy">
            <div class="sb-copy-modes">
              <div class="sb-copy-mode">
                <span class="sb-copy-mode-label">{{ t('pipelineJob.sbCopyStyleLabel') }}</span>
                <select
                  class="sb-input sb-select"
                  :value="step.copyStyle ?? 'advanced'"
                  :aria-label="t('pipelineJob.sbCopyStyleAria', { n: index + 1 })"
                  @change="setCopyStyle(step, ($event.target as HTMLSelectElement).value as CopyStyle)"
                >
                  <option value="plain">{{ copyStyleLabel('plain') }}</option>
                  <option value="advanced">{{ copyStyleLabel('advanced') }}</option>
                </select>
              </div>
              <div class="sb-copy-mode">
                <span class="sb-copy-mode-label">{{ t('pipelineJob.sbCopyModeLabel') }}</span>
                <select
                  class="sb-input sb-select"
                  :value="step.copyMode ?? 'contents'"
                  :aria-label="t('pipelineJob.sbCopyModeAria', { n: index + 1 })"
                  @change="setCopyMode(step, ($event.target as HTMLSelectElement).value as CopyMode)"
                >
                  <option value="contents">{{ t('pipelineJob.sbCopyModeContents') }}</option>
                  <option value="entry">{{ t('pipelineJob.sbCopyModeEntry') }}</option>
                </select>
              </div>
            </div>
            <div class="sb-copy-path">
              <input
                :ref="(el) => registerFirstField(step.id, el as Element | null)"
                :value="step.copySrc ?? ''"
                class="sb-input is-mono"
                type="text"
                placeholder="frontend/dist"
                :aria-label="t('pipelineJob.sbCopySrcAria', { n: index + 1 })"
                @input="patchStep(step.id, { copySrc: ($event.target as HTMLInputElement).value })"
                @blur="flush"
              />
              <span class="sb-arrow" aria-hidden="true">→</span>
              <input
                :value="step.copyDest ?? ''"
                class="sb-input is-mono"
                type="text"
                placeholder="build/out"
                :aria-label="t('pipelineJob.sbCopyDestAria', { n: index + 1 })"
                @input="patchStep(step.id, { copyDest: ($event.target as HTMLInputElement).value })"
                @blur="flush"
              />
            </div>
            <!-- 排除项独立成行,但光靠 placeholder 认不出它是干什么的,所以前面钉一个可见标签。
                 只有「高级复制」有这一格:cp 没有排除选项,给「复制」留着这格等于骗人。 -->
            <div v-if="(step.copyStyle ?? 'advanced') !== 'plain'" class="sb-copy-excl">
              <span class="sb-copy-excl-label">{{ t('pipelineJob.sbCopyExclLabel') }}</span>
              <input
                :value="step.copyExcludes ?? ''"
                class="sb-input is-mono"
                type="text"
                placeholder="node_modules .git *.map"
                :aria-label="t('pipelineJob.sbCopyExclAria', { n: index + 1 })"
                @input="patchStep(step.id, { copyExcludes: ($event.target as HTMLInputElement).value })"
                @blur="flush"
              />
            </div>
            <p class="sb-copy-hint" :class="{ 'sb-copy-hint--warn': rsyncState === 'no' && (step.copyStyle ?? 'advanced') !== 'plain' }">{{ copyHintText(step) }}</p>
          </div>

          <!-- 条件守卫 -->
          <div v-else-if="step.kind === 'condition'" class="sb-cond">
            <input
              :ref="(el) => registerFirstField(step.id, el as Element | null)"
              :value="step.condition ?? ''"
              class="sb-input is-mono"
              type="text"
              placeholder='[ "$BRANCH" = "main" ]'
              :aria-label="t('pipelineJob.sbCondAria', { n: index + 1 })"
              @input="patchStep(step.id, { condition: ($event.target as HTMLInputElement).value })"
              @blur="flush"
            />
            <p class="sb-cond-hint">
              {{ t('pipelineJob.sbCondHintPre') }}<strong>{{ t('pipelineJob.sbCondHintStrong') }}</strong>{{ t('pipelineJob.sbCondHintPost') }}
              <code>[ -f package.json ]</code>
            </p>
          </div>
        </li>
      </template>
      <!-- 收尾条:列表末尾的「+」,和每张卡片上方那条同一条语义(只是插到最后);菜单朝上开,不吃出抽屉。 -->
      <li v-if="steps.length > 0" class="sb-insert sb-insert--tail" :class="{ 'sb-insert--open': insertMenuIndex === steps.length }">
        <button
          class="sb-insert-btn"
          :aria-label="insertAria(steps.length)"
          :title="insertAria(steps.length)"
          aria-haspopup="menu"
          :aria-expanded="insertMenuIndex === steps.length"
          @click="toggleInsertMenu(steps.length)"
        >
          <svg width="9" height="9" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.8" aria-hidden="true"><path d="M12 5v14M5 12h14" /></svg>
          <span class="sb-insert-label">{{ t('pipelineJob.sbInsertLabel') }}</span>
        </button>
        <div
          v-if="insertMenuIndex === steps.length"
          class="sb-insert-menu"
          role="menu"
          :aria-label="t('pipelineJob.sbInsertMenuLabel')"
        >
          <button
            v-for="opt in addOptions"
            :key="opt.key"
            class="sb-add-item"
            :class="`sb-add-item--${opt.key}`"
            role="menuitem"
            @click="insertFromMenu(steps.length, opt)"
          >
            <span class="sb-add-dot" aria-hidden="true"></span>
            <span class="sb-add-body">
              <span class="sb-add-name">{{ opt.label }}</span>
              <span class="sb-add-desc">{{ opt.desc }}</span>
            </span>
          </button>
        </div>
      </li>
    </ol>

    <div v-if="steps.length === 0" class="sb-empty">{{ t('pipelineJob.sbEmpty') }}</div>

    <div class="sb-add">
      <button
        class="sb-add-btn"
        :class="{ 'sb-add-btn--open': addMenuOpen }"
        :aria-expanded="addMenuOpen"
        @click="addMenuOpen = !addMenuOpen"
      >
        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" aria-hidden="true"><path d="M12 5v14M5 12h14" /></svg>
        {{ t('pipelineJob.sbAddStep') }}
      </button>
      <div v-if="addMenuOpen" class="sb-add-menu" role="menu">
        <button
          v-for="opt in addOptions"
          :key="opt.key"
          class="sb-add-item"
          :class="`sb-add-item--${opt.key}`"
          role="menuitem"
          @click="addStep(opt)"
        >
          <span class="sb-add-dot" aria-hidden="true"></span>
          <span class="sb-add-body">
            <span class="sb-add-name">{{ opt.label }}</span>
            <span class="sb-add-desc">{{ opt.desc }}</span>
          </span>
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.step-builder {
  display: flex;
  flex-direction: column;
  gap: 9px;
}

.sb-head {
  display: flex;
  align-items: center;
  gap: 7px;
}

.sb-head-label {
  font-size: 0.72rem;
  font-weight: 600;
  letter-spacing: 0.02em;
  text-transform: uppercase;
  color: var(--color-faint);
}

.sb-count {
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

.sb-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  /* 8px 是卡片本来的间距:插入条盒子高度 0,一条缝隙 = 上下各一个 8px = 16px,
     刚好等于 16px 的「+」芯片 —— 芯片只在悬停时显形,平时列表一点额外空间都不吃。 */
  gap: 8px;
}

/* Each step is a left-accented card; the accent encodes the kind. */
.sb-step {
  position: relative;
  background: var(--color-inset);
  border: 1px solid var(--color-border);
  border-left: 3px solid var(--accent, var(--color-border-strong));
  border-radius: var(--rounded-md);
  padding: 8px 9px 9px;
  transition: border-color var(--duration-fast), box-shadow var(--duration-fast),
    opacity var(--duration-fast), transform var(--duration-fast);
}

.sb-step--command { --accent: var(--color-primary); }
.sb-step--env { --accent: var(--color-cyan); }
.sb-step--workDir { --accent: var(--color-amber); }
.sb-step--copy { --accent: var(--color-green); }
.sb-step--condition { --accent: var(--color-red); }

.sb-step--dragging {
  opacity: 0.5;
}

.sb-step--over {
  border-color: var(--color-primary);
  box-shadow: 0 0 0 2px var(--color-primary-soft);
  transform: translateY(1px);
}

/* 步骤之间的「+」分隔条:盒子高度 0(列表因此完全不因它变长),16px 的芯片居中骑在
   16px 的缝隙上,刚好不压到卡片描边。芯片默认**不显形也不接受点击**,只有鼠标走到相邻
   卡片(上/下各一条一起显形)、键盘 Tab 到那里、或它自己的类型菜单开着才浮现 ——
   常显的话每条缝隙都是一枚按钮,列表会被噪音铺满。
   z-index 必需:卡片是 position: relative,不抬一层的话芯片和菜单都会被卡片背景盖掉。 */
.sb-insert {
  position: relative;
  display: flex;
  align-items: center;
  justify-content: center;
  height: 0;
}

/* 缝隙本身是一块透明的悬停区(16px 正好铺满缝隙,不吃进卡片)。没有它的话,鼠标从卡片
   移向芯片的那一瞬间两边都不算 hover → 芯片隐身 → 永远点不到,来回抖动。 */
.sb-insert::before {
  content: '';
  position: absolute;
  left: 0;
  right: 0;
  top: -8px;
  height: 16px;
}

.sb-insert-btn {
  position: relative;
  z-index: 1;
  display: inline-flex;
  align-items: center;
  gap: 3px;
  height: 16px;
  padding: 0 7px;
  border: 1px solid var(--color-border-strong);
  border-radius: 8px;
  background: var(--color-surface);
  color: var(--color-dim);
  font-size: 0.76rem;
  font-weight: 600;
  line-height: 1;
  cursor: pointer;
  opacity: 0;
  pointer-events: none;
  transition: color var(--duration-fast), border-color var(--duration-fast),
    background var(--duration-fast), opacity var(--duration-fast);
}

/* 相邻卡片悬停/聚焦 → 这条缝隙上下的两个芯片一起显形(芯片自身也在 hover 路径里,
   所以显形后可以直接点;`:has` 负责「鼠标在下方那张卡片上」这一半)。 */
.sb-step:hover + .sb-insert .sb-insert-btn,
.sb-step:focus-within + .sb-insert .sb-insert-btn,
.sb-insert:has(+ .sb-step:hover) .sb-insert-btn,
.sb-insert:has(+ .sb-step:focus-within) .sb-insert-btn,
.sb-insert:hover .sb-insert-btn,
/* 菜单开着时必须留住,否则鼠标一移进菜单(它已经不在卡片上了)整张面板就消失。 */
.sb-insert--open .sb-insert-btn,
.sb-insert-btn:focus-visible {
  opacity: 1;
  pointer-events: auto;
}

.sb-insert-btn:hover,
.sb-insert-btn:focus-visible,
.sb-insert--open .sb-insert-btn {
  color: var(--color-primary);
  border-color: var(--color-primary);
  background: var(--color-primary-soft);
}

/* 类型菜单的浮层几何见文件末尾(要排在 .sb-add-menu/.sb-insert-menu 共享外观规则之后)。 */

.sb-step-bar {
  display: flex;
  align-items: center;
  gap: 7px;
  margin-bottom: 7px;
}

.sb-grip {
  color: var(--color-faint);
  cursor: grab;
  display: inline-flex;
}

.sb-grip:active {
  cursor: grabbing;
}

.sb-kind {
  font-size: 0.76rem;
  font-weight: 650;
  color: var(--accent, var(--color-text));
}

.sb-order {
  display: inline-grid;
  place-items: center;
  min-width: 15px;
  height: 15px;
  padding: 0 3px;
  border-radius: 7px;
  background: var(--color-border);
  color: var(--color-faint);
  font-size: 0.62rem;
  font-weight: 700;
  font-family: var(--font-mono);
}

.sb-step-actions {
  margin-left: auto;
  display: inline-flex;
  align-items: center;
  gap: 2px;
}

.sb-move,
.sb-del {
  width: 22px;
  height: 22px;
  display: grid;
  place-items: center;
  background: none;
  border: none;
  border-radius: 4px;
  color: var(--color-faint);
  cursor: pointer;
  padding: 0;
  transition: color var(--duration-fast), background-color var(--duration-fast);
}

.sb-move:hover:not(:disabled) {
  color: var(--color-text);
  background: var(--color-border);
}

.sb-move:disabled {
  opacity: 0.35;
  cursor: not-allowed;
}

.sb-del:hover {
  color: var(--color-red);
  background: var(--color-red-soft);
}

.sb-move:focus-visible,
.sb-del:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: 1px;
}

.sb-input {
  width: 100%;
  height: 30px;
  background: var(--color-bg, var(--color-inset));
  border: 1px solid var(--color-border-strong);
  border-radius: var(--rounded-md);
  padding: 0 9px;
  color: var(--color-text);
  font: inherit;
  font-size: 0.8rem;
  transition: border-color var(--duration-fast), box-shadow var(--duration-fast);
}

.sb-textarea {
  height: auto;
  min-height: 52px;
  padding: 7px 9px;
  line-height: 1.5;
  resize: vertical;
}

.is-mono {
  font-family: var(--font-mono);
  font-size: 0.78rem;
}

.sb-input:focus {
  outline: none;
  border-color: var(--color-primary);
  box-shadow: 0 0 0 2px var(--color-primary-soft);
}

.sb-env {
  display: grid;
  grid-template-columns: 1fr auto 1.4fr;
  align-items: center;
  gap: 6px;
}

.sb-eq {
  color: var(--color-faint);
  font-family: var(--font-mono);
  font-weight: 700;
}

.sb-copy {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

/* 「写法」和「复制方式」两个下拉并排:各占一半,窄了才换行,卡片不因此长高。 */
.sb-copy-modes {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.sb-copy-modes .sb-copy-mode {
  flex: 1 1 200px;
}

.sb-copy-path {
  display: grid;
  grid-template-columns: 1fr auto 1fr;
  align-items: center;
  gap: 6px;
}

.sb-arrow {
  color: var(--color-faint);
  font-family: var(--font-mono);
  font-weight: 700;
}

.sb-copy-mode {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  align-items: center;
  gap: 6px;
}

.sb-copy-mode-label {
  font-size: 0.7rem;
  color: var(--color-dim);
  white-space: nowrap;
}

/* 原生 select 跟 .sb-input 对齐(高度/边框/字号),再自己画个箭头。 */
.sb-select {
  appearance: none;
  padding-right: 26px;
  background-image: linear-gradient(45deg, transparent 50%, var(--color-faint) 50%),
    linear-gradient(135deg, var(--color-faint) 50%, transparent 50%);
  background-position: calc(100% - 14px) 13px, calc(100% - 9px) 13px;
  background-size: 5px 5px;
  background-repeat: no-repeat;
  cursor: pointer;
}

.sb-copy-excl {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  align-items: center;
  gap: 6px;
}

.sb-copy-excl-label {
  font-size: 0.7rem;
  color: var(--color-dim);
  white-space: nowrap;
}

.sb-copy-hint {
  margin: 0;
  font-size: 0.68rem;
  line-height: 1.45;
  color: var(--color-faint);
}

/* 这个镜像明确没有 rsync → 排除项是空的,得说得住眼睛。 */
.sb-copy-hint--warn {
  color: var(--color-amber);
}

.sb-cond {
  display: flex;
  flex-direction: column;
  gap: 5px;
}

.sb-cond-hint {
  margin: 0;
  font-size: 0.68rem;
  line-height: 1.45;
  color: var(--color-faint);
}

.sb-cond-hint strong {
  color: var(--color-red);
  font-weight: 600;
}

.sb-cond-hint code {
  font-family: var(--font-mono);
  font-size: 0.66rem;
  padding: 0 3px;
  border-radius: 3px;
  background: var(--color-border);
  color: var(--color-dim);
}

.sb-empty {
  font-size: 0.78rem;
  color: var(--color-faint);
  font-style: italic;
  padding: 4px 0;
}

.sb-add {
  position: relative;
}

.sb-add-btn {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  height: 30px;
  padding: 0 11px;
  background: none;
  border: 1px dashed var(--color-border-strong);
  border-radius: var(--rounded-md);
  color: var(--color-primary);
  font: inherit;
  font-size: 0.78rem;
  font-weight: 600;
  cursor: pointer;
  transition: border-color var(--duration-fast), background-color var(--duration-fast);
}

.sb-add-btn:hover,
.sb-add-btn--open {
  border-color: var(--color-primary);
  background: var(--color-primary-soft);
}

.sb-add-btn:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: 2px;
}

.sb-add-menu,
.sb-insert-menu {
  margin-top: 6px;
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 6px;
  padding: 7px;
  background: var(--color-inset);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--rounded);
}

/* 缝隙里的类型菜单:绝对浮层 —— 不吃列表高度,盖住相邻卡片(不透明 + 投影才看得出是浮上来的);
   宽度贴着列表,不会被抽屉裁掉。收尾条那一条朝上开,免得盖住下面的「加步骤」。 */
.sb-insert-menu {
  position: absolute;
  z-index: 3;
  top: 4px;
  left: 0;
  right: 0;
  box-shadow: var(--shadow);
}

.sb-insert--tail .sb-insert-menu {
  top: auto;
  bottom: 10px;
}

.sb-add-item {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 8px 9px;
  background: none;
  border: 1px solid var(--color-border);
  border-radius: var(--rounded-md);
  text-align: left;
  cursor: pointer;
  transition: border-color var(--duration-fast), background-color var(--duration-fast);
}

.sb-add-item:hover {
  border-color: var(--item-accent, var(--color-primary));
  background: var(--color-border);
}

.sb-add-item:focus-visible {
  outline: 2px solid var(--item-accent, var(--color-primary));
  outline-offset: 1px;
}

.sb-add-item--command { --item-accent: var(--color-primary); }
.sb-add-item--env { --item-accent: var(--color-cyan); }
.sb-add-item--workDir { --item-accent: var(--color-amber); }
.sb-add-item--copyPlain { --item-accent: var(--color-green); }
.sb-add-item--copyAdvanced { --item-accent: var(--color-green); }
.sb-add-item--condition { --item-accent: var(--color-red); }

.sb-add-dot {
  flex-shrink: 0;
  width: 8px;
  height: 8px;
  margin-top: 4px;
  border-radius: 50%;
  background: var(--item-accent, var(--color-primary));
}

.sb-add-body {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
}

.sb-add-name {
  font-size: 0.78rem;
  font-weight: 600;
  color: var(--color-text);
}

.sb-add-desc {
  font-size: 0.68rem;
  color: var(--color-faint);
  line-height: 1.3;
}
</style>
