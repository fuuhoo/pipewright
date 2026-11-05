<!--
  WorkspaceSnapshotPanel.vue — 构建目录快照(排障现场)

  每个脚本构建节点执行完,后端都会留一份目录清单(成功也留);镜像节点只在失败时留。
  面板回答的是那句"我是不是把产物目录配错了":失败时看命令实际产出在哪,未命中时看声明路径
  往上最近的存在目录里到底有什么,成功时看那次构建到底产出了什么。

  **默认只占一条触发条**:开页只问 ?meta=1 的轻量清单(哪几个节点留了现场、为什么留、
  产物路径命中没有、各有多少条),点开弹窗才取全量目录清单。原因是数据量:一份清单上限
  3000 条、单份可达 180KB,而运行详情页有多个构建节点 —— 全摊在页面上就是把卡顿留在前端。

  自取数据(container):快照不在冻结的 run-detail DTO 内,经 GET /api/runs/{id}/snapshots 拉取。
  无快照 → 不渲染(不占视觉);仓储未启用(503)→ 同样不渲染。判定与截断口径全在
  lib/workspaceSnapshot.ts(有单测),本组件只管渲染。

  清单按**树**渲染(后端给的是扁平相对路径):默认展开前两层,搜索时命中链自动展开,
  另有「全部展开 / 折叠」兜底 —— 手点过的展开态优先于自动口径。

  保留口径跟着运行记录:设置页「运行数据保留」裁掉那条 run 时快照一并清掉,这里没有独立窗口,
  所以弹窗副标题只说明这一点,不再报"只保留 N 小时"。
-->
<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppModal from '../ui/AppModal.vue'
import AppButton from '../ui/AppButton.vue'
import { getRunSnapshots, getRunSnapshotsMeta } from '../../api/runs'
import type { DeclaredPathDTO, RunSnapshotsResponse, WorkspaceSnapshotDTO } from '../../api/runs'
import { HttpError } from '../../api/http'
import { formatBytes } from '../../lib/serverFs'
import {
  allDirPaths,
  buildEntryTree,
  declaredStatusKey,
  flattenVisible,
  isExpanded,
  missedDeclared,
  reasonClass,
  reasonKey,
  snapshotTitle,
  type ExpandOverrides,
  type SnapNode,
} from '../../lib/workspaceSnapshot'

const { t } = useI18n()

const props = defineProps<{
  runId: string
}>()

// meta = 触发条要的轻量清单;full = 弹窗里的完整快照(含目录条目)。
const meta = ref<WorkspaceSnapshotDTO[]>([])
const loaded = ref(false)
const error = ref<string | null>(null)

const open = ref(false)
const full = ref<WorkspaceSnapshotDTO[]>([])
const fullLoading = ref(false)
const fullError = ref<string | null>(null)

// 404(运行不存在)与 503(快照仓储未启用,PIPEWRIGHT_SNAPSHOT=0)等价于"这里没有东西",
// 静默;其它错误给一行提示 —— 不能把加载失败谎报成"没有快照"。
function treatAsAbsent(err: unknown): boolean {
  return err instanceof HttpError && (err.status === 404 || err.status === 503)
}

async function load(id: string): Promise<void> {
  loaded.value = false
  error.value = null
  meta.value = []
  open.value = false
  full.value = []
  fullError.value = null
  try {
    const res: RunSnapshotsResponse = await getRunSnapshotsMeta(id)
    meta.value = res.snapshots ?? []
  } catch (err: unknown) {
    if (!treatAsAbsent(err)) error.value = t('run.snapLoadFailed')
  } finally {
    loaded.value = true
  }
}

watch(() => props.runId, (id) => { if (id) void load(id) }, { immediate: true })

// 点开才取全量:这一份才是真正的大数据(目录清单)。
async function openModal(): Promise<void> {
  open.value = true
  if (full.value.length > 0 || fullLoading.value) return
  fullLoading.value = true
  fullError.value = null
  try {
    const res: RunSnapshotsResponse = await getRunSnapshots(props.runId)
    full.value = res.snapshots ?? []
  } catch (err: unknown) {
    if (treatAsAbsent(err)) {
      full.value = []
    } else {
      fullError.value = t('run.snapLoadFailed')
    }
  } finally {
    fullLoading.value = false
  }
}

const hasSnapshots = computed(() => meta.value.length > 0)

// 触发条上最值得先说的一句:有几个节点声明的产物没收到东西(那才是要点进来看的理由)。
const missCount = computed(() =>
  meta.value.reduce((n, s) => n + s.declared.filter((d) => d.status !== 'ok').length, 0),
)

// 一个节点一份快照(顺序即后端返回顺序,按采集时间升序),逐块成组展示,标题用「阶段 · 节点」。
function titleOf(s: WorkspaceSnapshotDTO): string {
  return snapshotTitle(s.stage, s.jobName) || t('run.snapJobFallback')
}

function timeOf(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  return d.toLocaleString(undefined, { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}

// ─── 每个快照各自的搜索词(并行节点有多份快照时,不能共用一个输入框) ──────────────
const queries = ref<Record<string, string>>({})

function setQuery(id: string, v: string): void {
  queries.value = { ...queries.value, [id]: v }
}

// 手点过的展开态,按「快照 id → 目录路径 → 是否展开」记。切搜索词不清掉:用户刚折叠的
// 大目录不该因为多打一个字符又摊开。
const expandMap = ref<Record<string, ExpandOverrides>>({})

function overridesOf(id: string): ExpandOverrides {
  return expandMap.value[id] ?? {}
}

function toggleDir(id: string, node: SnapNode, queryActive: boolean): void {
  const next = { ...overridesOf(id) }
  next[node.path] = !isExpanded(node, next, queryActive)
  expandMap.value = { ...expandMap.value, [id]: next }
}

function setAllDirs(id: string, roots: SnapNode[], expand: boolean): void {
  const next: ExpandOverrides = {}
  for (const p of allDirPaths(roots)) next[p] = expand
  expandMap.value = { ...expandMap.value, [id]: next }
}

// 只有弹窗打开时才有全量数据可渲染;meta 参与不了这里(它不带目录条目)。
const views = computed(() =>
  full.value.map((s) => {
    const missed = missedDeclared(s.declared)
    const hit = s.declared.filter((d) => d.status === 'ok')
    const query = (queries.value[s.id] ?? '').trim()
    const roots = buildEntryTree(s.entries, query)
    // 命中数和下面「共 N 条」同源(都数清单条目)。别用树里的节点数:那含补出来的中间目录,
    // 会出现「命中 2734 / 共 2602 条」这种比总数还大的话。
    const q = query.toLowerCase()
    const hits = q === '' ? 0 : new Set(s.entries.filter((e) => e.path && e.path.toLowerCase().includes(q)).map((e) => e.path)).size
    return {
      snap: s,
      title: titleOf(s),
      missed,
      hit,
      query,
      hits,
      roots,
      tree: flattenVisible(roots, overridesOf(s.id), query !== ''),
    }
  })
)

// 未命中证据里的兄弟条目:后端已按单目录上限截断,前端只如实说"另有几项没列出"。
// 后端用 "." 表示工作区根目录(声明路径往上没有任何一层存在),文案要说成人话。
function parentLabel(parent: string): string {
  return parent === '.' ? t('run.snapParentRoot') : parent
}

function siblingsText(d: DeclaredPathDTO): string {
  if (!d.parent) return ''
  const parent = parentLabel(d.parent)
  const list = d.siblings ?? []
  if (list.length === 0) return t('run.snapSiblingsEmpty', { parent })
  const more = (d.siblingsMore ?? 0) > 0 ? t('run.snapSiblingsMore', { n: d.siblingsMore ?? 0 }) : ''
  return t('run.snapSiblings', { parent, items: list.join('  ·  ') }) + more
}
</script>

<template>
  <!-- 触发条:一页只占这一行。清单不进 DOM,直到点开弹窗。 -->
  <section
    v-if="(loaded && hasSnapshots) || error"
    class="sn-bar"
    role="region"
    :aria-label="t('run.snapRegion')"
  >
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true">
      <path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" />
    </svg>
    <h3 class="sn-title">{{ t('run.snapTitle') }}</h3>
    <span v-if="hasSnapshots" class="sn-count mono">{{ meta.length }}</span>
    <span v-if="error" class="sn-error" role="alert">{{ error }}</span>
    <span v-else-if="missCount > 0" class="sn-miss">{{ t('run.snapMissHint', { n: missCount }) }}</span>
    <!-- 同一个位置两种操作:取数失败时它是「重试」,有清单时是「查看清单」。
         别禁用失败态的按钮 —— 那正是用户唯一能把这块救回来的入口。 -->
    <button
      type="button"
      class="sn-open"
      @click="error ? void load(props.runId) : openModal()"
    >
      {{ error ? t('run.snapRetry') : t('run.snapOpen') }}
    </button>
  </section>

  <AppModal
    v-if="open"
    :title="t('run.snapTitle')"
    :subtitle="t('run.snapRetention')"
    width="xl"
    @close="open = false"
  >
    <p v-if="fullLoading" class="sn-state" role="status">{{ t('run.snapLoading') }}</p>
    <p v-else-if="fullError" class="sn-state sn-state--err" role="alert">
      {{ fullError }}
      <button type="button" class="sn-open" @click="void openModal()">{{ t('run.snapRetry') }}</button>
    </p>
    <template v-else>
      <article v-for="v in views" :key="v.snap.id" class="snap">
        <div class="snap-head">
          <span class="snap-reason" :class="reasonClass(v.snap.reason)">
            {{ t(`run.${reasonKey(v.snap.reason)}`) }}
          </span>
          <h4 class="snap-name">{{ v.title }}</h4>
          <span v-if="timeOf(v.snap.createdAt)" class="snap-time mono">{{ timeOf(v.snap.createdAt) }}</span>
        </div>

        <!-- 声明的产物路径:未命中的顶在最前(配错路径正是这块要回答的问题) -->
        <div v-if="v.snap.declared.length > 0" class="decl">
          <span class="decl-title">{{ t('run.snapDeclaredTitle') }}</span>
          <ul class="decl-list" role="list">
            <li
              v-for="d in [...v.missed, ...v.hit]"
              :key="d.path"
              class="decl-row"
              :class="{ 'decl-row--miss': d.status !== 'ok' }"
            >
              <span class="decl-badge" :class="`decl-badge--${d.status}`">{{ t(`run.${declaredStatusKey(d.status)}`) }}</span>
              <span class="decl-path mono">{{ d.path }}</span>
              <span v-if="d.parent" class="decl-evidence">
                {{ siblingsText(d) }}
              </span>
            </li>
          </ul>
        </div>

        <!-- 目录清单(树形) -->
        <div class="entries">
          <div class="entries-bar">
            <input
              class="entries-search"
              type="search"
              :value="queries[v.snap.id] ?? ''"
              :aria-label="t('run.snapSearchAria')"
              :placeholder="t('run.snapSearchPlaceholder')"
              @input="setQuery(v.snap.id, ($event.target as HTMLInputElement).value)"
            />
            <!-- 两个口径都数清单条目:不筛选时报清单大小,筛选后报命中数。
                 树里的节点含补齐的中间目录,拿它当数会说出「命中 2734 / 共 2602」这种话。 -->
            <span class="entries-count mono">
              <template v-if="v.query">
                {{ t('run.snapEntryCount', { shown: v.hits, total: v.snap.entries.length }) }}
              </template>
              <template v-else>{{ t('run.snapEntryTotal', { total: v.snap.entries.length }) }}</template>
            </span>
            <span class="entries-tools">
              <button type="button" class="entries-tool" :disabled="v.roots.length === 0" @click="setAllDirs(v.snap.id, v.roots, true)">
                {{ t('run.snapExpandAll') }}
              </button>
              <button type="button" class="entries-tool" :disabled="v.roots.length === 0" @click="setAllDirs(v.snap.id, v.roots, false)">
                {{ t('run.snapCollapseAll') }}
              </button>
            </span>
          </div>

          <p v-if="v.snap.truncated" class="entries-note" role="status">
            {{ t('run.snapTruncated') }}
          </p>

          <ul v-if="v.tree.rows.length > 0" class="tree-list mono" role="list">
            <li
              v-for="row in v.tree.rows"
              :key="row.node.path"
              class="tree-row"
              :style="{ paddingInlineStart: 8 + row.node.depth * 14 + 'px' }"
            >
              <button
                v-if="row.expandable"
                type="button"
                class="tree-toggle"
                :aria-expanded="row.expanded"
                :aria-label="t(row.expanded ? 'run.snapCollapseAria' : 'run.snapExpandAria', { path: row.node.path })"
                @click="toggleDir(v.snap.id, row.node, v.query !== '')"
              >
                <svg width="9" height="9" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" aria-hidden="true">
                  <path v-if="row.expanded" d="M5 12h14" />
                  <path v-else d="M12 5v14M5 12h14" />
                </svg>
              </button>
              <span v-else class="tree-toggle is-void" aria-hidden="true" />

              <span class="tree-name" :class="{ 'tree-name--dir': row.node.isDir, 'tree-name--hit': v.query !== '' && row.node.selfMatch }">
                {{ row.node.name }}
              </span>
              <span v-if="row.node.isDir && !row.expanded && row.node.children.length > 0" class="tree-count">
                {{ t('run.snapChildren', { n: row.node.children.length }) }}
              </span>
              <span v-else-if="row.node.isDir && row.node.children.length === 0 && row.node.dropped === 0" class="tree-leaf">
                {{ t('run.snapNoChildren') }}
              </span>
              <span v-if="row.node.size > 0" class="tree-size">{{ formatBytes(row.node.size) }}</span>
              <span v-if="row.node.dropped > 0" class="tree-dropped">{{ t('run.snapDropped', { n: row.node.dropped }) }}</span>
            </li>
          </ul>
          <p v-else-if="v.snap.entries.length > 0" class="entries-empty" role="status">
            {{ t('run.snapNoMatch') }}
          </p>
          <p v-else class="entries-empty" role="status">{{ t('run.snapNoEntries') }}</p>

          <p v-if="v.tree.hidden > 0" class="entries-note" role="status">
            {{ t('run.snapHiddenRows', { hidden: v.tree.hidden, limit: v.tree.limit }) }}
          </p>
        </div>
      </article>
    </template>

    <template #actions>
      <AppButton @click="open = false">{{ t('common.close') }}</AppButton>
    </template>
  </AppModal>
</template>

<style scoped>
/* 触发条:和运行页其它小区块同一套外壳,但只有一行高。 */
.sn-bar {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 4px 8px;
  padding: 8px 12px;
  border: 1px solid var(--color-border);
  border-radius: var(--rounded);
  background: var(--color-inset);
  color: var(--color-dim);
}

.sn-title {
  font-size: 0.82rem;
  font-weight: 600;
  color: var(--color-text);
}

.sn-count {
  font-size: 0.72rem;
  padding: 1px 6px;
  border-radius: var(--rounded-sm, 4px);
  background: var(--color-card-2);
  color: var(--color-dim);
}

/* 未命中是这块要回答的问题,触发条上就给一句「有几处产物没收到」当点开理由。 */
.sn-miss {
  flex: 1 1 auto;
  min-width: 0;
  font-size: 0.72rem;
  color: var(--color-amber);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.sn-open {
  margin-left: auto;
  flex: none;
  font: inherit;
  font-size: 0.74rem;
  padding: 3px 10px;
  border-radius: var(--rounded-sm, 4px);
  border: 1px solid var(--color-border-strong);
  background: var(--color-card-2);
  color: var(--color-dim);
  cursor: pointer;
  white-space: nowrap;
}

.sn-open:hover {
  color: var(--color-text);
  border-color: var(--color-primary);
}

.sn-error {
  min-width: 0;
  font-size: 0.74rem;
  color: var(--color-red, #c0392b);
  overflow-wrap: anywhere;
}

/* 弹窗里的取数中/取数失败提示:全量清单才是大头,这两句只替它占位。 */
.sn-state {
  margin: 0;
  padding: 10px 0;
  font-size: 0.8rem;
  color: var(--color-faint);
  display: flex;
  align-items: center;
  gap: 8px;
}

.sn-state .sn-open {
  margin-left: 0;
}

.sn-state--err {
  color: var(--color-red, #c0392b);
}

.snap {
  border-top: 1px solid var(--color-border);
  padding: 12px 0 2px;
}

.snap:first-of-type {
  border-top: none;
}

.snap-head {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 4px 8px;
  margin-bottom: 10px;
}

.snap-reason {
  font-size: 0.7rem;
  font-weight: 600;
  padding: 2px 7px;
  border-radius: var(--rounded-sm, 4px);
  white-space: nowrap;
}

.snap-reason--failed {
  color: var(--color-red, #c0392b);
  background: var(--color-red-soft, var(--color-amber-soft));
  border: 1px solid var(--color-red-line, var(--color-amber-line));
}

.snap-reason--miss {
  color: var(--color-amber);
  background: var(--color-amber-soft);
  border: 1px solid var(--color-amber-line);
}

/* 成功采集的标签现在每个构建节点都会出现,视觉上要让位:用淡绿,别和失败/未命中抢注意力。 */
.snap-reason--ok {
  color: var(--color-green);
  background: var(--color-green-soft);
  border: 1px solid var(--color-green-line);
}

.snap-name {
  min-width: 0;
  font-size: 0.82rem;
  font-weight: 600;
  color: var(--color-text);
  margin: 0;
  overflow-wrap: anywhere;
}

.snap-time {
  margin-left: auto;
  font-size: 0.72rem;
  color: var(--color-faint);
  white-space: nowrap;
}

/* ─── declared paths ─────────────────────────────────────────────────────── */
.decl {
  display: flex;
  flex-direction: column;
  gap: 5px;
  margin-bottom: 10px;
}

.decl-title {
  font-size: 0.72rem;
  font-weight: 600;
  color: var(--color-dim);
}

.decl-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.decl-row {
  display: flex;
  align-items: baseline;
  gap: 8px;
  flex-wrap: wrap;
  font-size: 0.78rem;
}

.decl-badge {
  font-size: 0.68rem;
  padding: 1px 6px;
  border-radius: var(--rounded-sm, 4px);
  border: 1px solid var(--color-border-strong);
  color: var(--color-dim);
  background: var(--color-card-2);
  white-space: nowrap;
}

.decl-badge--ok {
  color: var(--color-green);
  background: var(--color-green-soft);
  border-color: var(--color-green-line);
}

.decl-badge--missing,
.decl-badge--unmatched,
.decl-badge--rejected {
  color: var(--color-red, #c0392b);
  background: var(--color-red-soft, var(--color-amber-soft));
  border-color: var(--color-red-line, var(--color-amber-line));
}

.decl-evidence {
  color: var(--color-faint);
  font-size: 0.74rem;
  min-width: 0;
  overflow-wrap: anywhere;
}

/* ─── entries ────────────────────────────────────────────────────────────── */
.entries-bar {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px 10px;
  margin-bottom: 8px;
}

.entries-search {
  /* 弹窗有 960px 可用宽,搜索框给足宽度才看得清长路径;但仍允许收缩,窄视口下不溢出。 */
  flex: 1 1 260px;
  min-width: 0;
  font-size: 0.78rem;
  padding: 5px 8px;
  border-radius: var(--rounded-sm, 4px);
  border: 1px solid var(--color-border-strong);
  background: var(--color-card-2);
  color: var(--color-text);
}

.entries-count {
  font-size: 0.72rem;
  color: var(--color-faint);
  white-space: nowrap;
}

.entries-tools {
  display: flex;
  gap: 4px;
}

.entries-tool {
  font: inherit;
  font-size: 0.7rem;
  padding: 3px 7px;
  border-radius: var(--rounded-sm, 4px);
  border: 1px solid var(--color-border-strong);
  background: var(--color-card-2);
  color: var(--color-dim);
  cursor: pointer;
  white-space: nowrap;
}

.entries-tool:disabled {
  cursor: default;
  color: var(--color-faint);
  opacity: 0.6;
}

.tree-list {
  list-style: none;
  margin: 0;
  padding: 0;
  /* 弹窗里清单是主角:给它半屏高,内部滚动,行多了也不会把整份弹窗拉成一条长轴。 */
  max-height: clamp(240px, 45vh, 560px);
  overflow: auto;
  border: 1px solid var(--color-border);
  border-radius: var(--rounded-sm, 4px);
  background: var(--color-inset);
}

.tree-row {
  display: flex;
  align-items: baseline;
  gap: 6px;
  padding-block: 3px;
  padding-inline-end: 10px;
  font-size: 0.74rem;
  border-bottom: 1px solid var(--color-border);
}

.tree-row:last-child {
  border-bottom: none;
}

.tree-toggle {
  flex: none;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 14px;
  height: 14px;
  /* 按钮要贴着基线上的文字,不能用 align-items: center(文字行高比按钮高会错位)。 */
  align-self: center;
  margin-block: -2px;
  padding: 0;
  border: none;
  border-radius: 3px;
  background: transparent;
  color: var(--color-faint);
  cursor: pointer;
}

.tree-toggle:hover {
  background: var(--color-card-2);
  color: var(--color-text);
}

.tree-toggle.is-void {
  cursor: default;
}

.tree-name {
  min-width: 0;
  overflow-wrap: anywhere;
  color: var(--color-dim);
}

.tree-name--dir {
  color: var(--color-text);
  font-weight: 600;
}

/* 搜索命中的那一项单独标出来:祖先链是为它才展开的,不标就分不清"谁才是答案"。 */
.tree-name--hit {
  color: var(--color-amber);
}

.tree-count,
.tree-leaf {
  flex: none;
  font-size: 0.68rem;
  color: var(--color-faint);
  white-space: nowrap;
}

.tree-size,
.tree-dropped {
  margin-inline-start: auto;
  font-size: 0.7rem;
  color: var(--color-faint);
  white-space: nowrap;
}

.tree-dropped {
  color: var(--color-amber);
}

.entries-note,
.entries-empty {
  margin: 6px 0 0;
  font-size: 0.74rem;
  color: var(--color-faint);
}
</style>
