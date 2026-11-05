<!--
  RunTerminal.vue — Story 3-6: 纯黑实时日志终端

  逐行渲染运行日志的只读终端组件。两种使用方式由 `live` 决定:
    · live=true  (running/queued) → 订阅 SSE:先取尾部一屏播种,再从游标实时 tail。
    · live=false (终态)          → 仅历史回放,只读。

  关键行为:
    · **默认只搬尾部一屏**:开页用 ?tail=<LOG_TAIL_WINDOW> 要最近这么多行,「显示更早」
      再按页往上要(?tail&beforeSeq=)。整条日志一次进内存是把卡顿从后端搬回浏览器 ——
      一条 1.3 万行的日志,光渲染就够把主线程钉住。要全文请走右上角「下载全部日志」
      (后端逐行流式落成 .txt,同样不占浏览器内存)。
    · **选中某个步骤时**改为整条拉一次:服务端尾部窗口不分步骤,若仍只搬全局尾部,
      早期步骤会"看着是空的"。单步行数远小于整条,这条路径的正确性优先于省流量。
    · seq 去重 + 升序:SSE 回放历史与实时行可能重叠,统一以 seq 为主键(纯逻辑见
      lib/runLogBuffer.ts:追加 O(1)、乱序才二分插;往上翻页的前缀行走 prepend)。
    · **逐行不重算**:SSE 是一行一个事件,直接照单处理会让每行重建一次全表。入队后
      按帧合并(rAF),一帧最多重算/重渲染一次。
    · **只挂尾部渲染窗口**:即使已加载的行变多,也只渲染最近 LOG_TAIL_WINDOW +
      已展开的那些;否则一条 13553 行的日志就是 4 万个 DOM 节点,打开即卡。
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
  getRunLogsTail,
  runLogsDownloadUrl,
  subscribeRunEvents,
  type RunLogLine,
} from '../../api/runs'
import {
  createRunLogBuffer,
  logWindowFor,
  LOG_ANIMATE_MAX,
  LOG_TAIL_WINDOW,
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
// 服务端告诉我们的「上面还有」:更早的行数不止窗口没展开的那些,而是根本没搬进浏览器。
const hasEarlier = ref(false)
// 整条日志的总行数(尾部窗口模式才回传);0 = 不知道。
const serverTotal = ref(0)
const loadingEarlier = ref(false)
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
  hasEarlier.value = false
  serverTotal.value = 0
  loadingEarlier.value = false
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

  // 两种取法都只要一屏:没选步骤 = 整条日志的尾部;选了步骤 = 那一步的尾部(?step)。
  // 服务端按步骤筛完才回窗口,所以早期步骤不再被"全局尾部"筛成空的,也不用把整条搬下来。
  const step = props.filterOrdinal ?? null
  let cursor = 0
  let seedFailed = false

  try {
    const page = await getRunLogsTail(props.runId, LOG_TAIL_WINDOW, 0, step)
    ingest(page.lines)
    cursor = page.nextSeq
    hasEarlier.value = page.hasEarlier ?? false
    serverTotal.value = page.total ?? page.lines.length
    // 历史回放是一次到位的:把攒着的这批立刻落进缓冲,别多等一帧。
    if (flushHandle) flushPending()
  } catch (err) {
    dropQueued()
    loadError.value = err instanceof Error ? err.message : t('run.logLoadFailed')
    seedFailed = true
  }

  if (props.live) {
    // 播种失败在实时模式**不致命**:游标留 0,SSE 建连会自己回放历史,seq 去重兜住重叠。
    loadState.value = 'streaming'
    openSse(cursor)
    return
  }
  if (seedFailed) {
    loadState.value = 'error'
    return
  }
  loadState.value = 'done'
  scheduleAutoScroll()
}

function openSse(cursor: number): void {
  cleanupSse = subscribeRunEvents(props.runId, {
    // status/step 由父视图负责;这里只消费日志。
    onStatus() {},
    onStep() {},
    onLog(line) {
      ingest([line])
    },
  }, { sinceSeq: cursor })
}

onMounted(bootstrap)
onUnmounted(() => {
  stopSse()
  dropQueued()
})

// Re-bootstrap if the run flips identity, live⇄terminal (status transition), or the
// 换运行 / live⇄终态 / 换选中步骤都要重取:缓冲里装的就是「这一步的最近一屏」,
// 步骤变了那一屏也变了 —— 在客户端从同一份缓冲里筛是筛不出没下载的那些行的。
watch(
  () => [props.runId, props.live, props.filterOrdinal ?? -1] as const,
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

// 「显示更早」能给的行数 = 已加载但没挂出来的 + 服务端还没搬下来的。两段合起来才算,
// 否则按钮会说"再显示 1500 行"而实际只压着 37 行。
const notFetched = computed(() =>
  hasEarlier.value ? Math.max(0, serverTotal.value - shown.value.total) : 0,
)
const earlierBatch = computed(() =>
  Math.min(LOG_WINDOW_STEP, shown.value.hiddenEarlier + notFetched.value),
)
const canShowEarlier = computed(() => earlierBatch.value > 0)

// 行数是「已加载 / 共」两个数:窗口模式下 shown.total 只是搬进浏览器的那部分,
// 直接报它等于把"共 13553 行"谎报成"1500 行"。
const countLabel = computed(() => {
  const loaded = shown.value.total
  const total = hasEarlier.value ? Math.max(serverTotal.value, loaded) : loaded
  return total > loaded ? t('run.lineCountOf', { loaded, total }) : t('run.lineCount', { n: loaded })
})

// 往上翻一页历史:先展开本地窗口里已经压着的行;本地见底了才向服务端要更早的一页。
// 加了行 scrollHeight 会变长,若不锚定视窗就会跳到刚出现的旧行上;所以记下展开前的高度
// 和位置,渲染完把多出来的高度补回 scrollTop,读的那一行原地不动。
async function revealEarlier(): Promise<void> {
  const el = scrollEl.value
  const prevHeight = el ? el.scrollHeight : 0
  const prevTop = el ? el.scrollTop : 0
  const anchor = () => {
    void nextTick(() => {
      if (!el) return
      el.scrollTop = prevTop + (el.scrollHeight - prevHeight)
    })
  }

  if (shown.value.hiddenEarlier > 0) {
    revealed.value += LOG_WINDOW_STEP
    anchor()
    return
  }
  if (!hasEarlier.value || loadingEarlier.value) return

  loadingEarlier.value = true
  try {
    // 游标用缓冲里最小的 seq(已加载的最早一行),而不是本地窗口 —— 窗口只管渲染。
    const step = props.filterOrdinal ?? null
    const page = await getRunLogsTail(props.runId, LOG_WINDOW_STEP, buffer.firstSeq, step)
    // 按步骤翻页时 seq 本来就不连续(别的步骤的行夹在中间),不能拿连续性当断口判。
    const added = buffer.prepend(page.lines, { contiguous: step == null })
    if (added.length > 0) {
      // 新接上的这批必须同时多展开这么多行,否则窗口仍贴尾渲染,翻上来的页看不见。
      revealed.value += added.length
      revision.value++
    }
    hasEarlier.value = page.hasEarlier ?? false
    if (typeof page.total === 'number') serverTotal.value = page.total
  } catch {
    // 翻不动就停在原地:上面确实可能有,但这次没拿到 —— 按钮留着,用户可再点一次。
    hasEarlier.value = false
  } finally {
    loadingEarlier.value = false
    anchor()
  }
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
      <span class="term-count mono" v-if="hasLines">{{ countLabel }}</span>
      <!-- 全文下载交给后端逐行流式:窗口只搬了一屏,但"下载全部"不该受窗口大小限制。 -->
      <a
        v-if="hasLines"
        class="term-dl mono"
        :href="runLogsDownloadUrl(props.runId)"
        download
        :title="t('run.downloadAllLogTitle')"
      >
        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
          <path d="M12 3v12M7 11l5 5 5-5M4 20h16" />
        </svg>
        {{ t('run.downloadAllLog') }}
      </a>
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
          v-if="canShowEarlier"
          class="term-earlier mono"
          type="button"
          :disabled="loadingEarlier"
          @click="revealEarlier"
        >
          {{ loadingEarlier ? t('run.loadingEarlier') : t('run.showEarlier', { n: earlierBatch }) }}
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
