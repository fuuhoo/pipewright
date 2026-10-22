<script setup lang="ts">
/*
  StackCard.vue — 一条 compose 项目(跨主机)的容器卡片:容器总览「按 Stack 查看」这一档的
  正文。行 = 一个容器,机器名直接标在行上,所以「这条栈铺到了哪几台、哪台上有服务没起来」
  不用滚动比对几张机器卡片。操作全部复用按主机视图那套(serverId 随行携带),批量勾选也照旧
  走页面的 `${serverId}::${name}` 选择集 —— 于是「把这条栈在 N 台上一起重启」天然能做。
*/
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NIcon } from 'naive-ui'
import { ChevronDown, ChevronRight } from '@vicons/tabler'
import { serviceAction } from '../../api/servers'
import { HttpError } from '../../api/http'
import type { ContainerInfo } from '../../api/containers'
import { useToast } from '../../composables/useToast'
import { useConfirm } from '../../composables/useConfirm'
import { paginate, queryTokens } from '../../lib/containerListFilter'
import { stateBucket, stateMeta, shortId, actionsFor, ACTION_HINTS, DANGER_COPY, type ActionSpec, type StateBucket } from '../../lib/containerState'
import { NO_STACK, rowMatchesStack, type StackGroup, type StackHostView, type StackRow } from '../../lib/containerStacks'

const props = defineProps<{
  stack: StackGroup
  /** serverId → 展示信息。缺项时按 serverId 原样显示,不猜一个名字。 */
  hostViews: Record<string, StackHostView>
  stateFilter: 'all' | StateBucket
  search: string
  pageSize: number
  bulkMode: boolean
  /** 父持有的选择集;key = `${serverId}::${containerName}`。 */
  selectedSet: Set<string>
  /** 父持有的折叠集(按 stack.key)。 */
  foldedSet: Set<string>
}>()

const emit = defineEmits<{
  (e: 'toggle-fold'): void
  (e: 'toggle-select', serverId: string, name: string): void
  (e: 'view-container', serverId: string, name: string): void
  (e: 'logs', serverId: string, c: ContainerInfo): void
  (e: 'terminal', serverId: string, c: ContainerInfo): void
  (e: 'diagnose', serverId: string, c: ContainerInfo): void
  (e: 'inspect', serverId: string, c: ContainerInfo): void
  (e: 'changed'): void
}>()

const { t } = useI18n()
const toast = useToast()
const confirm = useConfirm()

const folded = computed(() => props.foldedSet.has(props.stack.key))
const title = computed(() => (props.stack.key === NO_STACK ? t('containers.stackUnstacked') : props.stack.name))
/** 一条栈铺了几台:比「几个容器」更能说明这条栈的形态(单机栈 vs 全机房栈)。 */
const hostCount = computed(() => props.stack.hosts.length)

function view(id: string): StackHostView {
  return props.hostViews[id] ?? { name: id, host: '', group: '' }
}

// ─── 行筛选(状态档位 + 搜索)+ 分页 ──────────────────────────────────────────
const tokens = computed(() => queryTokens(props.search))

const matchedRows = computed(() =>
  props.stack.rows.filter((row) => {
    if (props.stateFilter !== 'all' && stateBucket(row.container.state) !== props.stateFilter) return false
    return rowMatchesStack(props.stack, row, view(row.serverId), tokens.value)
  }),
)

const rowPage = ref(1)
const page = computed(() => paginate(matchedRows.value, rowPage.value, props.pageSize))
// 列表变短(容器被删、筛得更严)时把越界页码夹回来,免得停在空白末页。
watch(
  () => page.value.page,
  (p) => {
    if (p !== rowPage.value) rowPage.value = p
  },
)
watch([() => props.search, () => props.stateFilter, () => props.pageSize], () => {
  rowPage.value = 1
})

const emptyRowsHint = computed(() => {
  const q = props.search.trim()
  const total = props.stack.total
  if (q && props.stateFilter !== 'all') return t('opsServer.card.emptySearchFilter', { q, total })
  if (q) return t('opsServer.card.emptySearch', { q, total })
  return t('containers.stackNoRows', { total })
})

// ─── 行内生命周期操作 ─────────────────────────────────────────────────────────
const busy = ref<Set<string>>(new Set())
function rowKey(serverId: string, id: string): string {
  return `${serverId}:${id}`
}
function rowBusy(serverId: string, id: string): boolean {
  return busy.value.has(rowKey(serverId, id))
}
function isBusy(serverId: string, id: string, action: string): boolean {
  return busy.value.has(`${rowKey(serverId, id)}:${action}`)
}

function isSelected(row: StackRow): boolean {
  return props.selectedSet.has(`${row.serverId}::${row.container.names}`)
}

async function runAction(row: StackRow, spec: ActionSpec): Promise<void> {
  const c = row.container
  if (spec.danger) {
    const copy = DANGER_COPY[spec.action]
    const ok = await confirm.open({
      title: copy?.title(c.names) ?? t('opsServer.card.actionConfirmTitle', { name: c.names, label: spec.label }),
      body: copy?.body ?? t('opsServer.card.actionConfirmBody'),
      confirmLabel: copy?.confirmLabel ?? spec.label,
      variant: 'danger',
    })
    if (!ok) return
  }
  const key = `${rowKey(row.serverId, c.id)}:${spec.action}`
  busy.value = new Set(busy.value).add(key)
  try {
    const res = await serviceAction(row.serverId, { type: 'docker', target: c.names, action: spec.action })
    if (res.ok)
      toast.success(t('opsServer.card.actionSuccess', { label: spec.label }), {
        detail: `${c.names} · ${view(row.serverId).name}`,
      })
    else toast.error(t('opsServer.card.actionFail', { label: spec.label }), { detail: res.error || t('opsServer.card.errNonZeroExit') })
  } catch (err) {
    toast.error(t('opsServer.card.actionFail', { label: spec.label }), {
      detail: err instanceof HttpError
        ? (err.apiError?.message ?? t('opsServer.card.errReqFail', { status: err.status }))
        : t('opsServer.card.errNetwork'),
    })
  } finally {
    const next = new Set(busy.value)
    next.delete(key)
    busy.value = next
    setTimeout(() => emit('changed'), 600)
  }
}
</script>

<template>
  <article class="panel" :class="{ 'panel--folded': folded }">
    <header class="panel__head">
      <div class="panel__id">
        <h2 class="panel__name">{{ title }}</h2>
        <span v-if="hostCount > 1" class="panel__badge panel__badge--multi">{{ t('containers.stackHosts', { n: hostCount }) }}</span>
        <span v-else-if="hostCount === 1" class="panel__badge">{{ t('containers.stackHosts', { n: 1 }) }}</span>
        <span class="panel__hosts">
          <span
            v-for="id in stack.hosts"
            :key="id"
            class="panel__hostchip"
            :title="`${view(id).host}${view(id).group ? ' · ' + view(id).group : ''}`"
          >
            {{ view(id).name }}
            <span v-if="view(id).group" class="panel__hostgroup">{{ view(id).group }}</span>
          </span>
        </span>
      </div>
      <div class="panel__meta">
        <span class="panel__count mono">{{ t('opsServer.card.runningCount', { running: stack.running, total: stack.total }) }}</span>
        <span v-if="stack.running < stack.total" class="panel__badge panel__badge--warn">{{ t('containers.stackNotHealthy') }}</span>
        <button
          class="fold"
          type="button"
          :aria-expanded="!folded"
          :aria-label="folded ? t('opsServer.card.unfoldPanel') : t('opsServer.card.foldPanel')"
          :title="folded ? t('opsServer.card.unfoldPanel') : t('opsServer.card.foldPanel')"
          @click="emit('toggle-fold')"
        >
          <NIcon :size="16">
            <ChevronRight v-if="folded" />
            <ChevronDown v-else />
          </NIcon>
        </button>
      </div>
    </header>

    <template v-if="!folded">
      <p v-if="page.total === 0" class="panel__hint">{{ emptyRowsHint }}</p>
      <ul v-else class="clist" role="list">
        <li
          v-for="row in page.items"
          :key="row.serverId + '/' + row.container.id"
          class="crow"
          :class="{ 'crow--bulk': bulkMode, 'crow--busy': rowBusy(row.serverId, row.container.id) }"
        >
          <label v-if="bulkMode" class="crow__check">
            <input
              type="checkbox"
              :checked="isSelected(row)"
              :aria-label="t('opsServer.card.selectContainerAria', { name: row.container.names })"
              @change="emit('toggle-select', row.serverId, row.container.names)"
            />
          </label>
          <div class="crow__state">
            <span
              class="dot"
              :class="[`dot--${stateMeta(row.container.state).tone}`, { 'dot--pulse': stateMeta(row.container.state).pulse }]"
              aria-hidden="true"
            />
            <span class="crow__statelabel" :class="`txt--${stateMeta(row.container.state).tone}`">{{ stateMeta(row.container.state).label }}</span>
          </div>
          <div class="crow__host">
            <div class="crow__hostname" :title="view(row.serverId).host">{{ view(row.serverId).name }}</div>
            <div v-if="view(row.serverId).group" class="crow__hostgroup">{{ view(row.serverId).group }}</div>
          </div>
          <div class="crow__main">
            <button
              class="crow__name"
              type="button"
              :title="t('containers.viewContainer', { name: row.container.names })"
              @click="emit('view-container', row.serverId, row.container.names)"
            >
              {{ row.container.names }}
            </button>
            <div class="crow__sub mono" :title="`${row.container.image} · ${row.container.id}`">
              {{ row.container.image }} · {{ shortId(row.container.id) }}
            </div>
          </div>
          <div class="crow__status">
            <div class="crow__statustxt" :title="row.container.status">{{ row.container.status }}</div>
            <div v-if="row.container.ports" class="crow__ports mono" :title="row.container.ports">{{ row.container.ports }}</div>
          </div>
          <div class="crow__actions">
            <button
              v-for="a in actionsFor(row.container.state)"
              :key="a.action"
              class="op"
              :class="[`op--${a.variant}`, { 'op--busy': isBusy(row.serverId, row.container.id, a.action) }]"
              :disabled="rowBusy(row.serverId, row.container.id)"
              :title="ACTION_HINTS[a.action]"
              @click="runAction(row, a)"
            >
              {{ a.label }}
            </button>
            <button class="op op--ghost" :title="t('opsServer.card.inspectTitle')" @click="emit('inspect', row.serverId, row.container)">
              {{ t('opsServer.card.detail') }}
            </button>
            <button class="op op--ai" :title="t('opsServer.card.aiDiagnoseTitle')" @click="emit('diagnose', row.serverId, row.container)">
              {{ t('opsServer.card.ai') }}
            </button>
            <button class="op op--ghost" :title="t('opsServer.card.logsTitle')" @click="emit('logs', row.serverId, row.container)">
              {{ t('opsServer.card.logs') }}
            </button>
            <button
              class="op op--ghost"
              :disabled="rowBusy(row.serverId, row.container.id)"
              :title="t('opsServer.card.terminalTitle')"
              @click="emit('terminal', row.serverId, row.container)"
            >
              {{ t('opsServer.card.terminal') }}
            </button>
          </div>
        </li>
      </ul>

      <div v-if="page.pages > 1" class="pager" role="group" :aria-label="t('opsServer.card.pagerAria')">
        <button class="pager__btn" :disabled="page.page <= 1" :aria-label="t('opsServer.card.pagerPrev')" @click="rowPage = page.page - 1">‹</button>
        <span class="pager__pos">{{ t('opsServer.card.pagerPosition', { page: page.page, pages: page.pages }) }}</span>
        <span class="pager__range">{{ t('opsServer.card.pagerRange', { from: page.from, to: page.to, total: page.total }) }}</span>
        <button class="pager__btn" :disabled="page.page >= page.pages" :aria-label="t('opsServer.card.pagerNext')" @click="rowPage = page.page + 1">›</button>
      </div>
    </template>
  </article>
</template>

<style scoped>
.panel {
  background: var(--color-card);
  border: 1px solid var(--color-border);
  border-radius: var(--rounded-lg, 12px);
  box-shadow: var(--shadow);
  overflow: hidden;
}
.panel__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 14px 18px;
  border-bottom: 1px solid var(--color-border);
  background: var(--color-card-2);
}
.panel__id {
  display: flex;
  align-items: baseline;
  gap: 10px;
  min-width: 0;
}
.panel__name {
  margin: 0;
  font-size: var(--text-section);
  font-weight: 700;
  color: var(--color-text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.panel__meta {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-shrink: 0;
}
.panel__count {
  font-size: var(--text-micro);
  color: var(--color-dim);
  font-variant-numeric: tabular-nums;
}
.panel__badge {
  flex-shrink: 0;
  padding: 1px 7px;
  border: 1px solid var(--color-border);
  border-radius: 999px;
  background: var(--color-inset);
  font-size: var(--text-micro);
  color: var(--color-dim);
  white-space: nowrap;
}
.panel__badge--multi {
  color: var(--color-primary);
  border-color: var(--color-primary-soft);
  background: var(--color-primary-soft);
}
.panel__badge--warn {
  color: var(--color-amber);
  border-color: var(--color-amber-line);
  background: var(--color-amber-soft);
}
/* 涉及的宿主:一条栈可以铺几台,这里逐台列出(点不开,只是标注) */
.panel__hosts {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  overflow: hidden;
}
.panel__hostchip {
  display: inline-flex;
  align-items: baseline;
  gap: 5px;
  max-width: 180px;
  padding: 2px 8px;
  border: 1px solid var(--color-border);
  border-radius: var(--rounded-sm);
  background: var(--color-card);
  font-size: var(--text-micro);
  color: var(--color-dim);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.panel__hostgroup {
  color: var(--color-faint);
}
.fold {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 26px;
  height: 26px;
  padding: 0;
  border-radius: var(--rounded-sm);
  border: 1px solid var(--color-border-strong);
  background: transparent;
  color: var(--color-dim);
  cursor: pointer;
}
.fold:hover {
  color: var(--color-text);
  border-color: var(--color-text);
}
.panel--folded .panel__head {
  border-bottom: none;
}
.panel__hint {
  margin: 0;
  padding: 16px 18px;
  font-size: var(--text-label);
  color: var(--color-faint);
}

.clist {
  list-style: none;
  margin: 0;
  padding: 0;
}
.crow {
  display: grid;
  /* 宿主列是这一视图存在的理由:行属于哪台机器,不用回到机器卡片去比对 */
  grid-template-columns: 92px minmax(0, 0.8fr) minmax(0, 1.2fr) minmax(0, 1.1fr) auto;
  align-items: center;
  gap: 14px;
  padding: 12px 18px;
  border-bottom: 1px solid var(--color-border);
}
.crow:last-child {
  border-bottom: none;
}
.crow:hover {
  background: var(--color-card-2);
}
.crow--bulk {
  grid-template-columns: 26px 92px minmax(0, 0.8fr) minmax(0, 1.2fr) minmax(0, 1.1fr) auto;
}
.crow--busy {
  opacity: 0.55;
}
.crow__check {
  display: inline-flex;
  align-items: center;
  cursor: pointer;
}
.crow__check input {
  width: 16px;
  height: 16px;
  cursor: pointer;
  accent-color: var(--color-primary);
}
.crow__state {
  display: flex;
  align-items: center;
  gap: 7px;
}
.crow__statelabel {
  font-size: var(--text-micro);
  font-weight: 600;
}
.dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
  background: var(--color-faint);
}
.dot--green { background: var(--color-green); }
.dot--amber { background: var(--color-amber); }
.dot--cyan { background: var(--color-cyan); }
.dot--red { background: var(--color-red); }
.dot--faint { background: var(--color-faint); }
.dot--pulse { animation: dotPulse 1.8s var(--ease-out-expo) infinite; }
@keyframes dotPulse {
  0%, 100% { opacity: 1; transform: scale(1); }
  50% { opacity: 0.45; transform: scale(0.8); }
}
.txt--green { color: var(--color-green); }
.txt--amber { color: var(--color-amber); }
.txt--cyan { color: var(--color-cyan); }
.txt--red { color: var(--color-red); }
.txt--faint { color: var(--color-faint); }
.crow__host {
  min-width: 0;
}
.crow__hostname {
  font-size: var(--text-label);
  font-weight: 600;
  color: var(--color-dim);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.crow__hostgroup {
  font-size: var(--text-micro);
  color: var(--color-faint);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.crow__main {
  min-width: 0;
}
.crow__name {
  /* 容器名是这一行的入口:点了回到那台机器的「容器」tab 看完整一行(端口、占用、操作)。 */
  display: block;
  max-width: 100%;
  margin: 0;
  padding: 0;
  border: none;
  background: none;
  text-align: left;
  font-family: inherit;
  font-size: var(--text-body);
  font-weight: 600;
  color: var(--color-text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  cursor: pointer;
}
.crow__name:hover {
  color: var(--color-primary);
  text-decoration: underline;
}
.crow__sub {
  font-size: var(--text-micro);
  color: var(--color-faint);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.crow__status {
  min-width: 0;
}
.crow__statustxt {
  font-size: var(--text-label);
  color: var(--color-dim);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.crow__ports {
  font-size: var(--text-micro);
  color: var(--color-faint);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.crow__actions {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
}

.pager {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 18px;
  border-top: 1px solid var(--color-border);
  font-size: var(--text-micro);
  color: var(--color-dim);
}
.pager__btn {
  width: 24px;
  height: 24px;
  padding: 0;
  border-radius: var(--rounded-sm);
  border: 1px solid var(--color-border-strong);
  background: transparent;
  color: var(--color-dim);
  cursor: pointer;
}
.pager__btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.op {
  font-size: var(--text-micro);
  font-weight: 600;
  padding: 4px 10px;
  border-radius: var(--rounded-sm);
  border: 1px solid var(--color-border-strong);
  background: transparent;
  color: var(--color-dim);
  cursor: pointer;
  transition: all var(--duration-fast) var(--ease-out-expo);
}
.op:hover:not(:disabled) { color: var(--color-text); border-color: var(--color-text); }
.op--default { color: var(--color-primary); border-color: var(--color-primary-soft); }
.op--default:hover:not(:disabled) { color: #fff; background: var(--color-primary); border-color: var(--color-primary); }
.op--ai { color: var(--color-primary); border-color: var(--color-primary-soft); }
.op--ai:hover:not(:disabled) { color: #fff; background: var(--color-primary); border-color: var(--color-primary); }
.op:disabled { opacity: 0.45; cursor: not-allowed; }
.op--busy { opacity: 0.6; pointer-events: none; }

@media (max-width: 860px) {
  .crow,
  .crow--bulk {
    grid-template-columns: 1fr;
    gap: 8px;
  }
  .crow__actions {
    justify-content: flex-start;
    flex-wrap: wrap;
  }
  .panel__hosts {
    display: none;
  }
}
</style>
