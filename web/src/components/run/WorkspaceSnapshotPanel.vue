<!--
  WorkspaceSnapshotPanel.vue — 构建目录快照(排障现场)

  只在两种情况下后端才会留下快照:脚本节点执行**失败**,或节点**声明的产物路径没命中**
  (节点成功但产物根本没生成)。面板回答的是那句"我是不是把产物目录配错了":
  失败时看命令实际产出在哪,未命中时看声明路径往上最近的存在目录里到底有什么。

  自取数据(container):快照不在冻结的 run-detail DTO 内,经 GET /api/runs/{id}/snapshots 拉取。
  无快照 → 不渲染(不占视觉);仓储未启用(503)→ 同样不渲染。判定与截断口径全在
  lib/workspaceSnapshot.ts(有单测),本组件只管渲染。

  清单按**树**渲染(后端给的是扁平相对路径):默认展开前两层,搜索时命中链自动展开,
  另有「全部展开 / 折叠」兜底 —— 手点过的展开态优先于自动口径。

  保留窗口由后端如实回传(retentionSeconds),面板顶注说明"只保留最近 N 小时" —— 过期后
  这里就是空的,不能让用户以为"从没生成过"。
-->
<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getRunSnapshots } from '../../api/runs'
import type { DeclaredPathDTO, RunSnapshotsResponse, WorkspaceSnapshotDTO } from '../../api/runs'
import { HttpError } from '../../api/http'
import { formatBytes } from '../../lib/serverFs'
import {
  allDirPaths,
  buildEntryTree,
  countNodes,
  declaredStatusKey,
  flattenVisible,
  isExpanded,
  missedDeclared,
  reasonKey,
  retentionWindow,
  snapshotTitle,
  type ExpandOverrides,
  type SnapNode,
} from '../../lib/workspaceSnapshot'

const { t } = useI18n()

const props = defineProps<{
  runId: string
}>()

const snapshots = ref<WorkspaceSnapshotDTO[]>([])
const retentionSeconds = ref(0)
const loaded = ref(false)
const error = ref<string | null>(null)

async function load(id: string): Promise<void> {
  loaded.value = false
  error.value = null
  try {
    const res: RunSnapshotsResponse = await getRunSnapshots(id)
    snapshots.value = res.snapshots ?? []
    retentionSeconds.value = res.retentionSeconds ?? 0
  } catch (err: unknown) {
    // 404(运行不存在)与 503(快照仓储未启用,PIPEWRIGHT_SNAPSHOT=0)等价于"这里没有东西",
    // 静默;其它错误给一行提示 —— 不能把加载失败谎报成"没有快照"。
    if (err instanceof HttpError && (err.status === 404 || err.status === 503)) {
      snapshots.value = []
    } else {
      error.value = t('run.snapLoadFailed')
    }
  } finally {
    loaded.value = true
  }
}

watch(() => props.runId, (id) => { if (id) void load(id) }, { immediate: true })

const hasSnapshots = computed(() => snapshots.value.length > 0)

const retention = computed(() => retentionWindow(retentionSeconds.value))

// 同一节点的多个快照(失败采一次、产物未命中再采一次)成组展示,顺序即后端返回顺序(按时间)。
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

function setAllDirs(id: string, roots: SnapNode[], open: boolean): void {
  const next: ExpandOverrides = {}
  for (const p of allDirPaths(roots)) next[p] = open
  expandMap.value = { ...expandMap.value, [id]: next }
}

const views = computed(() =>
  snapshots.value.map((s) => {
    const missed = missedDeclared(s.declared)
    const hit = s.declared.filter((d) => d.status === 'ok')
    const query = (queries.value[s.id] ?? '').trim()
    const roots = buildEntryTree(s.entries, query)
    return {
      snap: s,
      title: titleOf(s),
      missed,
      hit,
      query,
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
  <section
    v-if="(loaded && hasSnapshots) || error"
    class="snapshots"
    role="region"
    :aria-label="t('run.snapRegion')"
  >
    <header class="sn-head">
      <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true">
        <path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" />
      </svg>
      <h3 class="sn-title">{{ t('run.snapTitle') }}</h3>
      <span v-if="hasSnapshots" class="sn-count mono">{{ snapshots.length }}</span>
      <span v-if="retention.count > 0" class="sn-retention">
        {{ t('run.snapRetention', { n: retention.count, unit: t(`run.${retention.unitKey}`) }) }}
      </span>
    </header>

    <p v-if="error" class="sn-error" role="alert">{{ error }}</p>

    <template v-else>
      <article v-for="v in views" :key="v.snap.id" class="snap">
        <div class="snap-head">
          <span class="snap-reason" :class="v.snap.reason === 'artifact_miss' ? 'snap-reason--miss' : 'snap-reason--failed'">
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
            <span class="entries-count mono">
              {{ t('run.snapEntryCount', { shown: countNodes(v.roots), total: v.snap.entries.length }) }}
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
  </section>
</template>

<style scoped>
.snapshots {
  border: 1px solid var(--color-border);
  border-radius: var(--rounded);
  background: var(--color-card);
  overflow: hidden;
}

.sn-head {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 11px 16px;
  background: var(--color-inset);
  border-bottom: 1px solid var(--color-border);
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

.sn-retention {
  margin-left: auto;
  font-size: 0.72rem;
  color: var(--color-faint);
}

.sn-error {
  margin: 0;
  padding: 12px 16px;
  font-size: 0.8rem;
  color: var(--color-red, #c0392b);
}

.snap {
  border-top: 1px solid var(--color-border);
  padding: 12px 16px 14px;
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

.snap-name {
  /* 右栏只有约 265px 宽,「声明的产物未命中时采集」这类标签本身就近一行宽,
     标题若与它同行会被挤成一字一行 —— 所以标题固定独占一行。 */
  order: 3;
  flex: 1 1 100%;
  min-width: 0;
  font-size: 0.82rem;
  font-weight: 600;
  color: var(--color-text);
  margin: 0;
  overflow-wrap: anywhere;
}

.snap-time {
  order: 2;
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
  /* 右栏窄时宁可让「共 N 条」换行,也不能把搜索框挤到 placeholder 都显示不全。 */
  flex: 1 1 200px;
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
  max-height: 320px;
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
