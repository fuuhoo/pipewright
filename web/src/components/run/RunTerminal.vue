<!--
  RunTerminal.vue — Story 3-6: 纯黑实时日志终端

  逐行渲染运行日志的只读终端组件。两种使用方式由 `live` 决定:
    · live=true  (running/queued) → 订阅 SSE:建连先回放历史 log,再实时 tail。
    · live=false (终态)          → 仅历史回放,一次性 getRunLogs 拉全量,只读。

  关键行为:
    · seq 去重 + 升序:SSE 回放历史与实时行可能重叠,统一以 seq 为主键(纯逻辑见
      lib/runLogBuffer.ts:追加 O(1)、乱序才二分插)。
    · **逐行不重算**:SSE 是一行一个事件,直接照单处理会让每行重建一次全表。入队后
      按帧合并(rAF),一帧最多重算/重渲染一次。
    · **只挂尾部窗口**:默认只渲染最近 LOG_TAIL_WINDOW 行,更早的靠「显示更早」逐批
      展开;否则一条 13553 行的日志就是 4 万个 DOM 节点,打开即卡。
    · 自动滚底:新行到达滚到底;但用户手动上滚(离开底部)时暂停自动滚——
      这是终端的常见行为;用户滚回底部自动恢复。滚动用 scrollTop 写入,
      不触发布局抖动。
    · stdout 默认色 / stderr 红色着色;行号(line-num token);命中 [MASKED]
      子串高亮标识(脱敏由后端完成,前端仅渲染标记)。分段在入队时切一次,
      不放在 render 里(那等于每帧把全表文本重扫一遍)。

  脱敏铁律:text 已由后端脱敏,前端绝不二次处理 secret,仅按 [MASKED] 标记渲染。
  动效:仅 opacity(实时 tail 的新行淡入;整屏回放的大批不逐行动画);
  prefers-reduced-motion 降级。
-->
<script setup lang="ts">
import { ref, computed, shallowRef, onMounted, onUnmounted, nextTick, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  getRunLogs,
  subscribeRunEvents,
  type RunLogLine,
} from '../../api/runs'
import {
  createRunLogBuffer,
  logWindowFor,
  LOG_ANIMATE_MAX,
  LOG_WINDOW_STEP,
} from '../../lib/runLogBuffer'

const { t } = useI18n()

const props = defineProps<{
  runId: string
  /** true ⇒ 进行中,订阅 SSE 实时 tail;false ⇒ 终态,仅历史回放只读。 */
  live: boolean
  /** 仅显示该步骤序号(stepOrdinal)的日志;null/undefined = 全部步骤。点步骤详情切换。 */
  filterOrdinal?: number | null
}>()

// ─── log buffer: seq-keyed, ascending ─────────────────────────────────────────
// The buffer grows in place, so Vue can't see the mutation — `revision` is the
// change stamp every recompute reads. Rendering takes a *slice* (a fresh array),
// which keeps the keyed diff bounded by the window instead of the log length.

const buffer = createRunLogBuffer()
const revision = shallowRef(0)
// 用户点「显示更早」累计多展开的行数。
const revealed = ref(0)
// 本帧新增的 seq:只有它们做淡入,回放整屏时不能上千个动画一起跑。
const NO_FRESH: ReadonlySet<number> = new Set()
const freshSeqs = shallowRef<ReadonlySet<number>>(NO_FRESH)

// ─── 逐行合并:SSE 一行一个事件,按帧攒批再入缓冲 ────────────────────────────

let pending: RunLogLine[] = []
let flushHandle = 0

function ingest(incoming: readonly RunLogLine[]): void {
  if (incoming.length === 0) return
  for (const line of incoming) pending.push(line)
  if (!flushHandle) flushHandle = requestAnimationFrame(flushPending)
}

function flushPending(): void {
  flushHandle = 0
  const batch = pending
  pending = []
  const added = buffer.add(batch)
  if (added.length === 0) return
  freshSeqs.value =
    props.live && added.length <= LOG_ANIMATE_MAX ? new Set(added.map((l) => l.seq)) : NO_FRESH
  revision.value++
  scheduleAutoScroll()
}

function dropQueued(): void {
  if (flushHandle) {
    cancelAnimationFrame(flushHandle)
    flushHandle = 0
  }
  pending = []
}

function resetBuffer(): void {
  dropQueued()
  buffer.clear()
  revealed.value = 0
  freshSeqs.value = NO_FRESH
  revision.value++
}

// ─── load / connection state ──────────────────────────────────────────────────

type LoadState = 'loading' | 'streaming' | 'done' | 'error'
const loadState = ref<LoadState>('loading')
const loadError = ref('')

let cleanupSse: (() => void) | null = null

function stopSse(): void {
  if (cleanupSse) {
    cleanupSse()
    cleanupSse = null
  }
}

async function bootstrap(): Promise<void> {
  resetBuffer()
  loadState.value = 'loading'
  loadError.value = ''

  if (props.live) {
    // Live mode: SSE handles both history replay (on connect) and live tail.
    // We don't pre-pull via REST to avoid double-fetching history — but if the
    // SSE connection drops, subscribeRunEvents falls back to polling getRun,
    // which does not carry logs; so we also seed once from getRunLogs to be
    // robust to a missed replay, relying on seq de-dup to avoid duplicates.
    try {
      const initial = await getRunLogs(props.runId, 0)
      ingest(initial.lines)
    } catch {
      // Non-fatal: SSE replay will still deliver history. Swallow silently.
    }
    loadState.value = 'streaming'
    cleanupSse = subscribeRunEvents(props.runId, {
      // status/step are owned by the parent view; here we only consume logs.
      onStatus() {},
      onStep() {},
      onLog(line) {
        ingest([line])
      },
    })
  } else {
    // Terminal mode: one-shot full historical replay, read-only.
    try {
      let sinceSeq = 0
      // Drain pages until complete (logs endpoint paginates large logs).
      // Guard against a non-advancing cursor to avoid an infinite loop.
      for (let guard = 0; guard < 10_000; guard++) {
        const page = await getRunLogs(props.runId, sinceSeq)
        ingest(page.lines)
        if (page.complete || page.nextSeq <= sinceSeq) break
        if (page.lines.length === 0) break
        sinceSeq = page.nextSeq
      }
      // 历史回放是一次到位的终态:把攒着的这批立刻落进缓冲,别多等一帧。
      if (flushHandle) flushPending()
      loadState.value = 'done'
      scheduleAutoScroll()
    } catch (err) {
      dropQueued()
      loadError.value = err instanceof Error ? err.message : t('run.logLoadFailed')
      loadState.value = 'error'
    }
  }
}

onMounted(bootstrap)
onUnmounted(() => {
  stopSse()
  dropQueued()
})

// Re-bootstrap if the run flips identity or live⇄terminal (status transition).
watch(
  () => [props.runId, props.live] as const,
  () => {
    stopSse()
    void bootstrap()
  },
)

// ─── auto-scroll: stick to bottom unless the user scrolled up ─────────────────

const scrollEl = ref<HTMLElement | null>(null)
const stickToBottom = ref(true)
// px tolerance: treat "near bottom" as bottom (sub-pixel / rounding slack).
const BOTTOM_EPS = 24

// 终端封顶 56vh、框内纵向滚动:贴底判断与跟随都作用于终端元素内部滚动(实时 tail 在框内滚到底,
// 不动整页)。用户在框内上滚 → 暂停跟随;滚回底部 → 恢复。
function onScroll(): void {
  const el = scrollEl.value
  if (!el) return
  const distance = el.scrollHeight - el.scrollTop - el.clientHeight
  stickToBottom.value = distance <= BOTTOM_EPS
}

let scrollQueued = false
function scheduleAutoScroll(): void {
  if (!props.live || !stickToBottom.value || scrollQueued) return
  scrollQueued = true
  void nextTick(() => {
    scrollQueued = false
    const el = scrollEl.value
    if (el && props.live && stickToBottom.value) {
      el.scrollTop = el.scrollHeight
    }
  })
}

function jumpToBottom(): void {
  stickToBottom.value = true
  const el = scrollEl.value
  if (el) el.scrollTop = el.scrollHeight
}

// ─── 尾部窗口渲染 ────────────────────────────────────────────────────────────
// 一次算出「这一帧挂哪些行 + 一共多少行 + 上面还压着多少行」。revision 是缓冲的
// 变更戳(数组原地增长,Vue 看不见),所以这里显式读它;窗口切片每次是新数组,
// keyed diff 的范围就只剩窗口大小。
const shown = computed(() => {
  void revision.value
  const all = buffer.all()
  const src =
    props.filterOrdinal == null ? all : all.filter((l) => l.stepOrdinal === props.filterOrdinal)
  const win = logWindowFor(src.length, revealed.value)
  return {
    lines: src.slice(win.start, win.end),
    total: src.length,
    hiddenEarlier: win.hiddenEarlier,
  }
})

const hasLines = computed(() => shown.value.total > 0)
const showFollowButton = computed(() => !stickToBottom.value && hasLines.value)
const earlierBatch = computed(() => Math.min(LOG_WINDOW_STEP, shown.value.hiddenEarlier))

// 往上展开一批历史。加了行,scrollHeight 会变长,若不锚定视窗就会跳到刚出现的旧行上;
// 所以记下展开前的高度和位置,渲染完把多出来的高度补回 scrollTop,读的那一行原地不动。
function revealEarlier(): void {
  const el = scrollEl.value
  const prevHeight = el ? el.scrollHeight : 0
  const prevTop = el ? el.scrollTop : 0
  revealed.value += LOG_WINDOW_STEP
  void nextTick(() => {
    if (!el) return
    el.scrollTop = prevTop + (el.scrollHeight - prevHeight)
  })
}

// seq 每 run 唯一,直接当 v-for 的 key。
function lineKey(line: { seq: number }): number {
  return line.seq
}
</script>

<template>
  <div class="term" :class="{ 'term--live': props.live }" role="region" :aria-label="t('run.terminalRegion')">
    <!-- Terminal chrome bar -->
    <div class="term-bar">
      <span class="term-dots" aria-hidden="true">
        <span class="term-dot term-dot--r" />
        <span class="term-dot term-dot--y" />
        <span class="term-dot term-dot--g" />
      </span>
      <span class="term-label">{{ t('run.runLog') }}</span>
      <span
        v-if="props.live && loadState === 'streaming'"
        class="term-live"
        :aria-label="t('run.liveAria')"
      >
        <span class="term-live-dot" aria-hidden="true" />
        LIVE
      </span>
      <span class="term-count mono" v-if="hasLines">{{ t('run.lineCount', { n: shown.total }) }}</span>
    </div>

    <!-- Log surface — 封顶 56vh,框内纵向滚动;实时 tail 框内贴底跟随 -->
    <div
      ref="scrollEl"
      class="term-scroll"
      tabindex="0"
      role="log"
      aria-live="polite"
      aria-relevant="additions"
      @scroll.passive="onScroll"
    >
      <!-- Loading -->
      <div v-if="loadState === 'loading'" class="term-state mono">
        {{ t('run.loadingLog') }}
      </div>

      <!-- Error (terminal-mode fetch failure) -->
      <div v-else-if="loadState === 'error'" class="term-state term-state--err mono">
        {{ loadError || t('run.logLoadFailed') }}
      </div>

      <!-- Empty -->
      <div
        v-else-if="!hasLines"
        class="term-state mono"
      >
        <template v-if="props.live">{{ t('run.waitingLog') }}</template>
        <template v-else>{{ t('run.noLogRecords') }}</template>
      </div>

      <!-- Lines:先给「显示更早」的口子,再挂尾部窗口 -->
      <template v-else>
        <button
          v-if="shown.hiddenEarlier > 0"
          class="term-earlier mono"
          type="button"
          @click="revealEarlier"
        >
          {{ t('run.showEarlier', { n: earlierBatch }) }}
        </button>

        <ol class="term-lines" :aria-label="t('run.totalLinesAria', { n: shown.total })">
          <li
            v-for="line in shown.lines"
            :key="lineKey(line)"
            class="term-line"
            :class="{ 'term-line--err': line.stream === 'stderr', 'term-line--new': freshSeqs.has(line.seq) }"
          >
            <span class="term-ln mono" aria-hidden="true">{{ line.seq }}</span>
            <span class="term-text mono">
              <template v-for="(seg, i) in line.segs" :key="i">
                <span v-if="seg.masked" class="term-masked" :title="t('run.maskedTitle')">{{ seg.text }}</span>
                <template v-else>{{ seg.text }}</template>
              </template>
            </span>
          </li>
        </ol>
      </template>
    </div>

    <!-- Jump-to-bottom affordance when auto-scroll is paused -->
    <button
      v-if="showFollowButton"
      class="term-follow"
      type="button"
      @click="jumpToBottom"
      :aria-label="t('run.jumpToLatest')"
    >
      <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" aria-hidden="true">
        <path d="M12 5v14M19 12l-7 7-7-7" />
      </svg>
      {{ t('run.jumpToBottom') }}
    </button>
  </div>
</template>

<style scoped>
/* ─── shell ───────────────────────────────────────────────────────────────── */
.term {
  position: relative;
  display: flex;
  flex-direction: column;
  background: var(--color-term);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--rounded-lg);
  overflow: hidden;
  box-shadow: var(--shadow-inner);
  /* 终端高度贴合内容(不拉伸、不设上限):空/少行时是小盒子、底边紧跟最后一行,随日志增长
     而长高,整页单一滚动条。不再用大 min-height 撑出「拉不到底边」的空黑盒。 */
}

/* ─── chrome bar ──────────────────────────────────────────────────────────── */
.term-bar {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 14px;
  background: oklch(11% 0.004 270);
  border-bottom: 1px solid var(--color-border);
  flex-shrink: 0;
}

.term-dots {
  display: inline-flex;
  gap: 6px;
}

.term-dot {
  width: 10px;
  height: 10px;
  border-radius: var(--rounded-full);
  display: inline-block;
}

.term-dot--r { background: oklch(63% 0.2 25); }
.term-dot--y { background: oklch(80% 0.15 80); }
.term-dot--g { background: oklch(72% 0.16 150); }

.term-label {
  font-family: var(--font-mono);
  font-size: 0.74rem;
  letter-spacing: 0.02em;
  color: var(--color-faint);
}

.term-live {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  margin-left: 4px;
  font-family: var(--font-mono);
  font-size: 0.66rem;
  font-weight: 700;
  letter-spacing: 0.08em;
  color: var(--color-amber);
}

.term-live-dot {
  width: 6px;
  height: 6px;
  border-radius: var(--rounded-full);
  background: var(--color-amber);
  animation: term-pulse 1.1s ease-in-out infinite;
}

@keyframes term-pulse {
  0%, 100% { opacity: 1; }
  50%      { opacity: 0.35; }
}

.term-count {
  margin-left: auto;
  font-size: 0.68rem;
  color: var(--color-line-num);
}

/* ─── scroll surface ──────────────────────────────────────────────────────── */
.term-scroll {
  /* 高度贴合内容,但封顶 56vh:超过则框内纵向滚动(主区 overflow-x:clip 已修好整页滚动,
     不再有「嵌套双滚动滑不到底」的老问题)。空状态给个小下限避免太矮;长行横向滚动。 */
  overflow-x: auto;
  overflow-y: auto;
  min-height: 72px;
  max-height: 56vh;
  /* Comfortable but dense terminal feel */
  padding: 8px 0 12px;
  scrollbar-width: thin;
  scrollbar-color: var(--color-border-strong) transparent;
}

.term-scroll:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: -2px;
}

.term-scroll::-webkit-scrollbar {
  width: 10px;
  height: 10px;
}

.term-scroll::-webkit-scrollbar-thumb {
  background: var(--color-border-strong);
  border-radius: var(--rounded-full);
  border: 2px solid transparent;
  background-clip: padding-box;
}

/* ─── states ──────────────────────────────────────────────────────────────── */
.term-state {
  padding: 22px 16px;
  font-size: 0.78rem;
  color: var(--color-faint);
}

.term-state--err {
  color: var(--color-red);
}

/* ─── 显示更早 ────────────────────────────────────────────────────────────── */
.term-earlier {
  display: block;
  width: max-content;
  margin: 0 16px 8px;
  padding: 4px 10px;
  font-size: 0.7rem;
  color: var(--color-line-num);
  background: oklch(100% 0 0 / 0.05);
  border: 1px solid var(--color-border);
  border-radius: var(--rounded-md);
  cursor: pointer;
  transition: color var(--duration-fast), border-color var(--duration-fast);
}

.term-earlier:hover {
  color: oklch(88% 0.006 270);
  border-color: var(--color-border-strong);
}

.term-earlier:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: 2px;
}

/* ─── lines ───────────────────────────────────────────────────────────────── */
.term-lines {
  list-style: none;
  margin: 0;
  padding: 0;
  counter-reset: none;
  /* keep long lines on one row; horizontal scroll handles overflow */
  min-width: max-content;
}

.term-line {
  display: flex;
  align-items: flex-start;
  gap: 14px;
  padding: 0 16px;
  /* dense terminal line-height */
  line-height: 1.55;
  white-space: pre;
}

/* 淡入只给本帧真正新增的那几行。整条规则挂在 .term-line--new 上而不是 .term-line:
   回放上万行历史时,挂全表等于同时启动上万个动画。 */
.term-line--new {
  animation: line-in 140ms ease-out both;
}

@keyframes line-in {
  from { opacity: 0; }
  to   { opacity: 1; }
}

@media (prefers-reduced-motion: reduce) {
  .term-line--new { animation: none; }
  .term-live-dot { animation: none; }
}

.term-line:hover {
  background: oklch(100% 0 0 / 0.035);
}

/* line number / seq gutter */
.term-ln {
  flex-shrink: 0;
  width: 3.5ch;
  text-align: right;
  font-size: 0.72rem;
  color: var(--color-line-num);
  user-select: none;
  -webkit-user-select: none;
}

.term-text {
  flex: 1;
  font-size: 0.78rem;
  color: oklch(88% 0.006 270);  /* terminal default foreground on pure black */
  white-space: pre;
  word-break: normal;
}

/* stderr coloring */
.term-line--err .term-text {
  color: var(--color-red);
}

.term-line--err .term-ln {
  color: var(--color-red);
  opacity: 0.6;
}

/* [MASKED] highlight — never reveals secrets, just marks redaction */
.term-masked {
  display: inline-block;
  padding: 0 4px;
  border-radius: var(--rounded-sm);
  background: var(--color-amber-soft);
  color: var(--color-amber);
  font-weight: 700;
  letter-spacing: 0.02em;
}

/* ─── follow button ───────────────────────────────────────────────────────── */
.term-follow {
  position: absolute;
  right: 16px;
  bottom: 14px;
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 5px 11px;
  font-family: var(--font-sans);
  font-size: 0.74rem;
  font-weight: 600;
  color: var(--color-text);
  background: var(--color-card-2);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--rounded-full);
  box-shadow: var(--shadow);
  cursor: pointer;
  transition: transform var(--duration-fast) var(--ease-out-expo),
              border-color var(--duration-fast);
}

.term-follow:hover {
  border-color: var(--color-primary);
  transform: translateY(-1px);
}

.term-follow:active {
  transform: translateY(0);
}

.term-follow:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: 2px;
}

@media (prefers-reduced-motion: reduce) {
  .term-follow { transition: border-color var(--duration-fast); }
  .term-follow:hover { transform: none; }
}
</style>
