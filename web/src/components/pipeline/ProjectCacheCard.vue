<script setup lang="ts">
/**
 * ProjectCacheCard — 项目在本机磁盘上的缓存清单 + 一键清除。
 *
 * 为什么要有这张卡:平台的构建跑在一次性容器里,但有两处东西是**跨构建留在磁盘上**的 ——
 * 代码管理区的 bare 镜像(构建只增量 fetch)和每项目固定依赖缓存(Maven 的 .m2 里存着
 * SNAPSHOT)。前者让人怀疑「拉到的是不是最新代码」,后者让人怀疑「私服重新发过的同一个版本
 * 是不是还是旧包」。两种怀疑的现象一模一样,而原来在界面上没有任何出口能核对或清掉。
 *
 * 只在打开「变量与缓存」页时才挂载(v-if 在父级),因为列体积要遍历缓存目录,没必要在进
 * 流水线页时就扫一遍磁盘。
 */
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  clearProjectCache,
  getProjectCache,
  type ProjectCacheInfo,
} from '../../api/projects'
import { HttpError } from '../../api/http'
import { hasAnythingToClear, summarizeClear } from '../../lib/projectCache'
import { formatBytes } from '../../lib/serverFs'
import { useConfirm } from '../../composables/useConfirm'
import { useToast } from '../../composables/useToast'

const props = defineProps<{ projectId: string }>()

const { t } = useI18n()
const confirm = useConfirm()
const toast = useToast()

const info = ref<ProjectCacheInfo | null>(null)
const loading = ref(false)
const clearing = ref(false)
// 本实例两处缓存都没启用(启动时全关)→ 这张卡没有存在意义,整块不渲染。
const unsupported = ref(false)

const items = computed(() => info.value?.items ?? [])
const canClear = computed(() => hasAnythingToClear(info.value))

function kindLabel(kind: string): string {
  if (kind === 'repo') return t('projectCache.kindRepo')
  if (kind === 'deps') return t('projectCache.kindDeps')
  return kind
}

function serverMessage(e: unknown, fallbackKey: string): string {
  if (e instanceof HttpError) return e.apiError?.message || e.message
  return t(fallbackKey)
}

async function load(): Promise<void> {
  loading.value = true
  try {
    info.value = await getProjectCache(props.projectId)
    unsupported.value = false
  } catch (e) {
    if (e instanceof HttpError && e.status === 503) {
      unsupported.value = true
      info.value = null
      return
    }
    toast.error(t('projectCache.loadFailed'), { detail: serverMessage(e, 'projectCache.loadFailed') })
  } finally {
    loading.value = false
  }
}

async function doClear(): Promise<void> {
  if (!canClear.value || clearing.value) return
  // 逐条列实际存在的类别:未绑仓库/某处缓存被启动开关关掉时那一行根本不在清单里,
  // 确认框不该承诺删一个本实例没有的东西。
  const lines = items.value.map((it) => `· ${kindLabel(it.kind)} — ${formatBytes(it.bytes)}`)
  const hasRepo = items.value.some((it) => it.kind === 'repo')
  const ok = await confirm.open({
    title: t('projectCache.confirmTitle'),
    body: [
      t('projectCache.confirmLead'),
      ...lines,
      t('projectCache.confirmCost'),
      ...(hasRepo ? [t('projectCache.confirmShared')] : []),
    ].join('\n'),
    confirmLabel: t('projectCache.confirmBtn'),
    variant: 'danger',
  })
  if (!ok) return

  clearing.value = true
  try {
    const res = await clearProjectCache(props.projectId)
    const s = summarizeClear(res)
    if (s.removedKinds.length) {
      toast.success(t('projectCache.cleared', { size: formatBytes(s.freedBytes) }), {
        detail: s.removedKinds.map(kindLabel).join(' · '),
      })
    } else {
      // 一个字节都没删:说「本来就是空的」,而不是「释放 0 B」—— 后者容易被读成操作失败。
      toast.info(t('projectCache.wasEmpty'))
    }
    await load()
  } catch (e) {
    toast.error(t('projectCache.clearFailed'), { detail: serverMessage(e, 'projectCache.clearFailed') })
  } finally {
    clearing.value = false
  }
}

onMounted(load)
</script>

<template>
  <section v-if="!unsupported" class="card">
    <header class="card-head">
      <span class="card-ic" aria-hidden="true">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9">
          <path d="M4 7h16M4 12h16M4 17h10" />
          <circle cx="18" cy="17" r="2.4" />
        </svg>
      </span>
      <span class="card-tt">{{ t('projectCache.title') }}</span>
      <span class="card-sub">{{ t('projectCache.sub') }}</span>
      <button
        type="button"
        class="reload"
        :disabled="loading || clearing"
        :aria-label="t('projectCache.reloadAria')"
        @click="load"
      >{{ t('projectCache.reload') }}</button>
    </header>

    <p v-if="loading && !items.length" class="hint">{{ t('projectCache.loading') }}</p>

    <ul v-else class="rows">
      <li v-for="it in items" :key="it.kind" class="row">
        <span class="rh">
          <span class="rk">{{ kindLabel(it.kind) }}</span>
          <span class="rs" :class="{ 'rs--zero': !it.bytes }">{{ formatBytes(it.bytes) }}</span>
        </span>
        <span class="rp mono" :title="it.path">{{ it.path }}</span>
      </li>
      <li v-if="!items.length" class="hint">{{ t('projectCache.emptyNode') }}</li>
    </ul>

    <footer class="foot">
      <span class="foot-tx">{{ t('projectCache.total', { size: formatBytes(info?.totalBytes ?? 0) }) }}</span>
      <button
        type="button"
        class="clear"
        :disabled="!canClear || loading || clearing"
        :title="canClear ? t('projectCache.clearTitle') : t('projectCache.clearDisabledTitle')"
        @click="doClear"
      >
        <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
          <path d="M3 6h18M8 6V4h8v2M6 6l1 14h10l1-14" />
        </svg>
        {{ clearing ? t('projectCache.clearing') : t('projectCache.clearBtn') }}
      </button>
    </footer>
  </section>
</template>

<style scoped>
.card {
  background: var(--color-card);
  border: 1px solid var(--color-border);
  border-radius: var(--rounded-card);
  box-shadow: var(--shadow);
  overflow: hidden;
}

.card-head {
  display: flex;
  align-items: center;
  gap: 9px;
  padding: 13px 18px;
  border-bottom: 1px solid var(--color-border);
  font-size: 0.88rem;
  font-weight: 600;
  color: var(--color-text);
}
.card-ic {
  width: 22px; height: 22px;
  border-radius: 6px;
  background: var(--color-primary-soft);
  color: var(--color-primary);
  display: grid; place-items: center;
  flex: none;
}
.card-ic svg { width: 13px; height: 13px; }
/* 标题/按钮都不许被折行:这一栏只有 ~370px 宽,能折的只有说明文字。 */
.card-tt { white-space: nowrap; }
.card-sub {
  margin-left: 4px;
  min-width: 0;
  font-size: 0.73rem;
  color: var(--color-faint);
  font-weight: 400;
}

.reload {
  margin-left: auto;
  flex: none;
  white-space: nowrap;
  height: 26px;
  padding: 0 10px;
  font-size: 0.73rem;
  font-family: var(--font-sans);
  color: var(--color-dim);
  background: transparent;
  border: 1px solid var(--color-border);
  border-radius: 7px;
  cursor: pointer;
}
.reload:hover:not(:disabled) { color: var(--color-primary); border-color: var(--color-primary); }
.reload:focus-visible { outline: 2px solid var(--color-primary); outline-offset: 1px; }
.reload:disabled { opacity: 0.5; cursor: default; }

.rows { list-style: none; margin: 0; padding: 0; }
.row {
  padding: 9px 18px;
  border-bottom: 1px solid var(--color-border);
  font-size: 0.79rem;
}
.rh { display: flex; align-items: baseline; gap: 10px; }
.rk { color: var(--color-text); font-weight: 500; }
.rs { margin-left: auto; font-family: var(--font-mono); color: var(--color-dim); }
.rs--zero { color: var(--color-faint); }
/* 路径独占一行:这一页在流水线右侧栏只有 ~370px 宽,单行 ellipsis 截掉的正好是结尾
   (镜像哈希 / 项目 ID)—— 而那是「这是哪儿」唯一认得出的部分。 */
.rp {
  display: block;
  margin-top: 3px;
  color: var(--color-faint);
  line-height: 1.4;
  word-break: break-all;
}
.mono { font-family: var(--font-mono); }

.hint { padding: 14px 18px; margin: 0; font-size: 0.78rem; color: var(--color-faint); }

.foot {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 11px 18px;
  background: var(--color-card-2);
}
.foot-tx { font-size: 0.76rem; color: var(--color-dim); }
.clear {
  margin-left: auto;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 30px;
  padding: 0 12px;
  font-size: 0.79rem;
  font-weight: 500;
  font-family: var(--font-sans);
  color: var(--color-red);
  background: transparent;
  border: 1px solid var(--color-red-line);
  border-radius: 8px;
  cursor: pointer;
}
.clear:hover:not(:disabled) { background: var(--color-red-soft); }
.clear:focus-visible { outline: 2px solid var(--color-primary); outline-offset: 2px; }
.clear:disabled { opacity: 0.45; cursor: not-allowed; }
</style>
