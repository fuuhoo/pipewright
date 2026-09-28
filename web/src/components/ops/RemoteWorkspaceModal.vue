<!--
  RemoteWorkspaceModal.vue —— 「远程」弹窗:上半屏终端,下半屏文件面板,中间可拖。

  这一层只管三件事:布局、把两边接起来、进出场。终端与文件各自是独立组件
  (TerminalPane / RemoteFilePanel),内部能力与后端一一对应,这里不重复实现。

  cwd 联动是双向的,规则刻意做得很简单:
    · 终端 → 面板:shell 每次出提示符用 OSC 7 报当前目录,面板跟着列那一份。
    · 面板 → 终端:点目录 / 面包屑 / 手填路径,往 PTY 打一条转义过的 `cd '<路径>'`。
  两边都只在**路径真的变了**才动(见 RemoteFilePanel 的 watch 与 navigate),
  否则会自己触发自己:面板 cd → 终端回报同一路径 → 面板重新列目录,白多一次 SSH。

  分屏比例存 localStorage:这台机日志目录长、那台机命令输出多,记住房间怎么分的
  比每次进来重拖一遍省事。
-->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import TerminalPane from './TerminalPane.vue'
import RemoteFilePanel from './RemoteFilePanel.vue'
import { ROOT } from '../../lib/serverFs'
import type { HostTerminalShell } from '../../api/servers'

const props = defineProps<{
  serverId: string
  serverName: string
  /** user@host:port,顶栏与终端条都用它。 */
  hostLabel?: string
  /** '' = 让服务端在这台机上挑一个最合适的 shell(bash/zsh 优先)。 */
  shell?: HostTerminalShell
}>()

const emit = defineEmits<{ (e: 'close'): void }>()

const { t } = useI18n()

const SPLIT_KEY = 'pw-remote-split'
const SPLIT_MIN = 0.18
const SPLIT_MAX = 0.82
const SPLIT_DEFAULT = 0.46

function readSplit(): number {
  const raw = Number(localStorage.getItem(SPLIT_KEY))
  if (!Number.isFinite(raw) || raw <= 0 || raw >= 1) return SPLIT_DEFAULT
  return Math.min(SPLIT_MAX, Math.max(SPLIT_MIN, raw))
}

const body = ref<HTMLElement | null>(null)
const termPane = ref<InstanceType<typeof TerminalPane> | null>(null)
const split = ref(readSplit())
const termHeight = computed(() => `${(split.value * 100).toFixed(2)}%`)

/** 终端上报的当前目录:文件面板跟着走。 */
const terminalCwd = ref(ROOT)

/**
 * 全屏铺满:文件面板要同时容下目录树 + 六列表格,1180px 的卡片宽度不够看。
 * 只换外壳尺寸,内部分屏比例照旧 —— 退出全屏不该把用户调好的布局也一起丢掉。
 */
const fullscreen = ref(false)

function toggleFullscreen(): void {
  fullscreen.value = !fullscreen.value
  // 外壳尺寸突变后让终端重新 fit(ResizeObserver 也会兜这一刀,双保险)。
  termPane.value?.focus()
}

function onTerminalCwd(path: string): void {
  terminalCwd.value = path
}

/** 面板导航 → 终端 cd。 */
function onPanelCd(path: string): void {
  termPane.value?.runCd(path)
}

// ─── 拖动分隔条 ─────────────────────────────────────────────────────────────────
const dragging = ref(false)

function onDragStart(e: PointerEvent): void {
  if (e.button !== 0) return
  dragging.value = true
  e.preventDefault()
  window.addEventListener('pointermove', onDragMove)
  window.addEventListener('pointerup', onDragEnd)
}

function onDragMove(e: PointerEvent): void {
  const el = body.value
  if (!el) return
  const rect = el.getBoundingClientRect()
  if (rect.height <= 0) return
  const ratio = (e.clientY - rect.top) / rect.height
  split.value = Math.min(SPLIT_MAX, Math.max(SPLIT_MIN, ratio))
}

function onDragEnd(): void {
  dragging.value = false
  window.removeEventListener('pointermove', onDragMove)
  window.removeEventListener('pointerup', onDragEnd)
  try {
    localStorage.setItem(SPLIT_KEY, String(split.value))
  } catch {
    /* 隐私模式 / 配额:本次比例只活在内存里 */
  }
  // 松手后终端尺寸变了,让 pane 重新 fit(ResizeObserver 也会兜这一刀,双保险)。
  termPane.value?.focus()
}

// 键盘也能调比例:分隔条可聚焦,↑/↓ 各让一档(鼠标不便用的场景不该被锁死)。
function onDividerKeydown(e: KeyboardEvent): void {
  if (e.key !== 'ArrowUp' && e.key !== 'ArrowDown') return
  e.preventDefault()
  const delta = e.key === 'ArrowUp' ? -0.04 : 0.04
  split.value = Math.min(SPLIT_MAX, Math.max(SPLIT_MIN, split.value + delta))
  try {
    localStorage.setItem(SPLIT_KEY, String(split.value))
  } catch {
    /* ignore */
  }
}

function onKeydown(e: KeyboardEvent): void {
  if (e.key !== 'Escape') return
  // 终端里按 Esc 是 shell/vi 的按键,不该顺手关掉整个弹窗。
  const el = e.target as HTMLElement | null
  if (el?.closest?.('.term-pane')) return
  // 全屏时 Esc 先退回卡片大小:铺满整屏后「关掉」和「缩回去」都该有反悔的路。
  if (fullscreen.value) {
    fullscreen.value = false
    return
  }
  emit('close')
}

onMounted(() => {
  window.addEventListener('keydown', onKeydown)
})

onBeforeUnmount(() => {
  window.removeEventListener('keydown', onKeydown)
  window.removeEventListener('pointermove', onDragMove)
  window.removeEventListener('pointerup', onDragEnd)
})
</script>

<template>
  <div class="rw-backdrop" :class="{ 'rw-backdrop--dragging': dragging, 'rw-backdrop--full': fullscreen }" @click.self="emit('close')">
    <div
      class="rw-modal"
      :class="{ 'rw-modal--full': fullscreen }"
      role="dialog"
      aria-modal="true"
      :aria-label="t('remoteWorkspace.title')"
    >
      <header class="rw-head">
        <div class="rw-head-text">
          <h3 class="rw-title">{{ t('remoteWorkspace.title') }}</h3>
          <span class="rw-sub">{{ serverName }}<template v-if="hostLabel"> · {{ hostLabel }}</template></span>
        </div>
        <a class="rw-fullscreen" :href="`/servers/${serverId}/terminal`" target="_blank" rel="noopener">
          {{ t('remoteWorkspace.openFullscreen') }}
        </a>
        <button
          class="rw-icon-btn"
          type="button"
          :aria-label="fullscreen ? t('remoteWorkspace.exitFullscreen') : t('remoteWorkspace.enterFullscreen')"
          :title="fullscreen ? t('remoteWorkspace.exitFullscreen') : t('remoteWorkspace.enterFullscreen')"
          :aria-pressed="fullscreen"
          @click="toggleFullscreen"
        >
          <span class="rw-icon-btn__glyph" aria-hidden="true">{{ fullscreen ? '⤡' : '⤢' }}</span>
        </button>
        <button class="rw-close" type="button" :aria-label="t('remoteWorkspace.close')" @click="emit('close')">×</button>
      </header>

      <div ref="body" class="rw-body">
        <section class="rw-term" :style="{ height: termHeight }">
          <TerminalPane
            ref="termPane"
            :server-id="serverId"
            :host-label="hostLabel"
            :shell="shell"
            @cwd="onTerminalCwd"
          />
        </section>

        <div
          class="rw-divider"
          role="separator"
          aria-orientation="horizontal"
          tabindex="0"
          :aria-label="t('remoteWorkspace.resize')"
          @pointerdown="onDragStart"
          @keydown="onDividerKeydown"
        >
          <span class="rw-divider__grip" aria-hidden="true" />
        </div>

        <section class="rw-files">
          <RemoteFilePanel
            :server-id="serverId"
            :terminal-cwd="terminalCwd"
            @cd="onPanelCd"
          />
        </section>
      </div>
    </div>
  </div>
</template>

<style scoped>
.rw-backdrop {
  position: fixed;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  background: rgba(15, 23, 42, 0.55);
  z-index: 500;
  padding: 20px;
}
/* 拖动时整页不许选中文本:选中高亮会在快速拖拽里糊成一片。 */
.rw-backdrop--dragging {
  user-select: none;
  cursor: row-resize;
}
/* 全屏:去掉四周留白,弹窗外壳就是整个视口。 */
.rw-backdrop--full {
  padding: 0;
}

.rw-modal {
  width: min(1180px, 100%);
  height: min(92vh, 1000px);
  display: flex;
  flex-direction: column;
  background: var(--color-surface, #fff);
  color: var(--color-text, #111827);
  border: 1px solid var(--color-border, #d1d5db);
  border-radius: 14px;
  box-shadow: 0 24px 60px rgba(15, 23, 42, 0.35);
  overflow: hidden;
}
/* 写在 .rw-modal 之后:同特异度,靠顺序覆盖掉卡片尺寸与圆角。 */
.rw-modal--full {
  width: 100%;
  height: 100%;
  border: 0;
  border-radius: 0;
  box-shadow: none;
}

.rw-head {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 16px;
  border-bottom: 1px solid var(--color-border, #e5e7eb);
  flex-shrink: 0;
}
.rw-head-text {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.rw-title {
  margin: 0;
  font-size: 15px;
  font-weight: 650;
}
.rw-sub {
  font-size: var(--text-label);
  color: var(--color-text-muted, #6b7280);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.rw-fullscreen {
  margin-left: auto;
  font-size: var(--text-label);
  color: var(--color-dim, #6b7280);
  text-decoration: none;
  border: 1px solid var(--color-line, #e5e7eb);
  border-radius: var(--rounded-sm);
  padding: 3px 9px;
  white-space: nowrap;
}
.rw-fullscreen:hover {
  color: var(--color-text);
  border-color: var(--color-line-strong, var(--color-line, #e5e7eb));
}
.rw-close {
  flex-shrink: 0;
  width: 30px;
  height: 30px;
  border: 1px solid transparent;
  border-radius: 8px;
  background: transparent;
  color: var(--color-text-muted, #6b7280);
  font-size: 22px;
  line-height: 1;
  cursor: pointer;
}
.rw-close:hover {
  background: var(--color-inset, #f3f4f6);
  color: var(--color-text);
}
/* 全屏开关:和 × 同尺寸,免得顶栏两个按钮一高一低。 */
.rw-icon-btn {
  flex-shrink: 0;
  width: 30px;
  height: 30px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border: 1px solid transparent;
  border-radius: 8px;
  background: transparent;
  color: var(--color-text-muted, #6b7280);
  cursor: pointer;
}
.rw-icon-btn:hover {
  background: var(--color-inset, #f3f4f6);
  color: var(--color-text);
}
.rw-icon-btn__glyph {
  font-size: 15px;
  line-height: 1;
}

.rw-body {
  display: flex;
  flex-direction: column;
  flex: 1 1 auto;
  min-height: 0;
}
.rw-term {
  flex: 0 0 auto;
  min-height: 0;
}
.rw-files {
  flex: 1 1 auto;
  min-height: 0;
  background: var(--color-surface, #fff);
  border-top: 1px solid var(--color-border, #e5e7eb);
}

.rw-divider {
  flex: 0 0 auto;
  height: 8px;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--color-inset, #f6f7f9);
  cursor: row-resize;
  border-top: 1px solid var(--color-border, #e5e7eb);
  border-bottom: 1px solid var(--color-border, #e5e7eb);
}
.rw-divider__grip {
  width: 38px;
  height: 3px;
  border-radius: 2px;
  background: var(--color-line-strong, #cbd5e1);
}
.rw-divider:hover .rw-divider__grip,
.rw-divider:focus-visible .rw-divider__grip {
  background: var(--color-accent, #7fe3f0);
}
.rw-divider:focus-visible {
  outline: 2px solid var(--color-accent, #7fe3f0);
  outline-offset: -2px;
}
</style>
