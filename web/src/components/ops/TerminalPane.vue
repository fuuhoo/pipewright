<!--
  TerminalPane.vue —— 内嵌终端面板(「远程」弹窗的上半屏)。

  与 /servers/:id/terminal 那台全屏驾驶舱终端是同一底座(xterm + 同源 WebSocket → SSH PTY),
  但这一份只做弹窗里需要的最小闭环:连上、能打字、能复制、尺寸跟着分屏拖动重算。
  没有 AI 补全 / 右键菜单 / 氛围样式 —— 那些在全屏页里各有归属,弹窗里挤进来只会糊。

  多出来的一条能力是**cwd 上报**:连接后往 PTY 注入一段钩子脚本(见 lib/serverFs 的
  cwdReportScript),让远端 shell 每次出提示符时用 OSC 7 打印当前目录,本组件解析后经
  emit('cwd') 交给上层联动文件面板。只有 bash/zsh 有提示符钩子;其余 shell 静默无联动,
  文件面板仍可自行导航(不假装能跟上)。shell 默认留空 = 让服务端在这台机上现挑一个
  (bash/zsh 优先),所以正常情况下联动是开箱即用的。
-->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  openServerTerminal,
  type HostTerminalShell,
  type TerminalConnection,
  type TerminalHandlers,
} from '../../api/servers'
import { cdCommand, cwdReportScript, pathFromOsc7 } from '../../lib/serverFs'

import type { Terminal as XTerm } from '@xterm/xterm'
import type { FitAddon as XFitAddon } from '@xterm/addon-fit'

const props = withDefaults(
  defineProps<{
    serverId: string
    /** 展示用主机标签(名称或 user@host)。 */
    hostLabel?: string
    /** '' = 让服务端在这台机上挑一个最合适的 shell(bash/zsh 优先)。 */
    shell?: HostTerminalShell
  }>(),
  { shell: '', hostLabel: '' },
)

type ConnState = 'idle' | 'connecting' | 'connected' | 'closed' | 'error'
const emit = defineEmits<{
  /** 远端 shell 报告的当前目录(绝对路径,已去百分号转义)。 */
  (e: 'cwd', path: string): void
  (e: 'state', state: ConnState): void
}>()

const { t } = useI18n()
const shellOptions: HostTerminalShell[] = ['', '/bin/sh', '/bin/bash', '/bin/ash', '/bin/zsh', 'sh', 'bash']
const shell = ref<HostTerminalShell>(props.shell)

const connState = ref<ConnState>('idle')
const statusMsg = ref('')
const latencyMs = ref<number | null>(null)
const isBashOrZsh = computed(() => shell.value.includes('bash') || shell.value.includes('zsh'))
/** 自动模式没法预先知道远端挑了哪个 shell:装没装上钩子,只看它有没有真的报过 cwd。 */
const sawCwd = ref(false)
const linkUnconfirmed = ref(false)
const showLinkNote = computed(
  () => (shell.value !== '' && !isBashOrZsh.value) || (shell.value === '' && linkUnconfirmed.value),
)

/** 自动模式只能靠「报没报过 cwd」判有没有钩子:给 4 秒,别一连上就喊不支持。 */
let linkProbe: number | undefined
function clearLinkProbe(): void {
  if (linkProbe !== undefined) window.clearTimeout(linkProbe)
  linkProbe = undefined
}

const termHost = ref<HTMLElement | null>(null)
const term = shallowRef<XTerm | null>(null)
const fitAddon = shallowRef<XFitAddon | null>(null)
const conn = shallowRef<TerminalConnection | null>(null)
let resizeObserver: ResizeObserver | null = null
const decoder = new TextDecoder()

// xterm 在 canvas 上一次性测量单元格;字体晚到会把行数算多、末行溢出到容器外。
// 与全屏终端页同一处理:先等字体就绪再 open,并在 fonts.ready 后重测一次。
const TERM_FONT_SIZE = 13
const TERM_FONT_FAMILY = '"JetBrains Mono", ui-monospace, "SF Mono", Menlo, Consolas, monospace'

function setState(next: ConnState): void {
  connState.value = next
  emit('state', next)
}

/** 适配尺寸后再按真实几何收敛:FitAddon 偶尔多算半行,末行会被容器裁掉。 */
function refit(): void {
  const fit = fitAddon.value
  const t = term.value
  const host = termHost.value
  if (!fit || !t || !host) return
  try {
    fit.fit()
    const screen = host.querySelector('.xterm-screen') as HTMLElement | null
    if (screen) {
      const padBottom = parseFloat(getComputedStyle(host).paddingBottom) || 0
      let guard = 4
      while (guard-- > 0 && t.rows > 1) {
        const limit = host.getBoundingClientRect().bottom - padBottom
        if (screen.getBoundingClientRect().bottom <= limit + 0.5) break
        t.resize(t.cols, t.rows - 1)
      }
    }
    conn.value?.resize(t.cols, t.rows)
  } catch {
    /* 尚未布局完成 */
  }
}

async function ensureTerm(): Promise<XTerm | null> {
  if (term.value) return term.value
  const [{ Terminal }, { FitAddon }] = await Promise.all([
    import('@xterm/xterm'),
    import('@xterm/addon-fit'),
  ])
  await import('@xterm/xterm/css/xterm.css')
  if (term.value) return term.value // onBeforeUnmount 已抢先 dispose

  const inst = new Terminal({
    cursorBlink: true,
    fontFamily: TERM_FONT_FAMILY,
    fontSize: TERM_FONT_SIZE,
    lineHeight: 1.15,
    scrollback: 4000,
    theme: {
      background: '#0b0d10',
      foreground: '#e6e6ea',
      cursor: '#7fe3f0',
      selectionBackground: 'rgba(120, 225, 240, 0.30)',
      selectionForeground: '#d6f6fb',
    },
  })

  // OSC 7 ← 远端 shell 报 cwd。返回 true 表示已消费,不再落到终端渲染。
  inst.parser.registerOscHandler(7, (data: string): boolean => {
    const p = pathFromOsc7(data)
    if (p) {
      sawCwd.value = true
      linkUnconfirmed.value = false
      clearLinkProbe()
      emit('cwd', p)
    }
    return true
  })

  // 弹窗里终端不是唯一焦点来源(下面就是文件面板):选中即复制,⌘/Ctrl+C 复制选区,
  // ⌘/Ctrl+V 粘贴;无选区的 Ctrl+C 仍透传成 SIGINT。
  inst.attachCustomKeyEventHandler((e: KeyboardEvent): boolean => {
    if (e.type !== 'keydown') return true
    const mod = e.metaKey || e.ctrlKey
    const key = e.key.toLowerCase()
    if (mod && key === 'c' && inst.hasSelection()) {
      void copySelection()
      return false
    }
    if (mod && key === 'v') {
      void pasteFromClipboard()
      return false
    }
    return true
  })

  if (typeof document !== 'undefined' && document.fonts?.load) {
    try {
      await document.fonts.load(`${TERM_FONT_SIZE}px "JetBrains Mono"`)
    } catch {
      /* 字体没到就用 fallback 度量,不阻塞终端 */
    }
  }
  await nextTick()
  term.value = inst
  fitAddon.value = new FitAddon()
  inst.loadAddon(fitAddon.value)
  if (!termHost.value) return inst
  inst.open(termHost.value)
  refit()
  if (typeof document !== 'undefined' && document.fonts?.ready) {
    void document.fonts.ready.then(() => {
      try {
        inst.options.fontSize = TERM_FONT_SIZE + 1
        inst.options.fontSize = TERM_FONT_SIZE
        refit()
      } catch {
        /* host 尚未布局完成 */
      }
    })
  }
  inst.onData((data: string) => conn.value?.send(data))
  inst.onResize(() => refit())
  inst.onSelectionChange(() => {
    // 选中即复制:弹窗里没右键菜单,这条最省事也最符合预期。
    if (inst.hasSelection()) void copySelection()
  })
  if (!resizeObserver && termHost.value) {
    resizeObserver = new ResizeObserver(() => refit())
    resizeObserver.observe(termHost.value)
  }
  return inst
}

const clipboardOK =
  typeof navigator !== 'undefined' && !!navigator.clipboard && typeof window !== 'undefined' && window.isSecureContext

async function copySelection(): Promise<void> {
  const sel = term.value?.getSelection() ?? ''
  if (!sel || !clipboardOK) return
  try {
    await navigator.clipboard.writeText(sel)
  } catch {
    /* 用户拒绝权限:选区还在,不打扰 */
  }
}

async function pasteFromClipboard(): Promise<void> {
  if (!clipboardOK) return
  try {
    const text = await navigator.clipboard.readText()
    if (text) conn.value?.send(text)
  } catch {
    /* 未授权:与全屏终端页一致,不弹阻断式提示 */
  }
}

async function connect(): Promise<void> {
  disconnect()
  const inst = await ensureTerm()
  inst?.reset()
  inst?.focus()
  setState('connecting')
  statusMsg.value = ''
  sawCwd.value = false
  linkUnconfirmed.value = false
  const startedAt = performance.now()
  const handlers: TerminalHandlers = {
    onOpen() {
      setState('connected')
      latencyMs.value = Math.round(performance.now() - startedAt)
      refit()
      // 先让 shell 装上 cwd 钩子,再清一次行,免得注入回显留在提示符前。
      conn.value?.send(cwdReportScript())
      // 自动模式下唯一能证明「这台机的 shell 有提示符钩子」的证据就是它真的报过 cwd。
      if (shell.value === '') {
        linkProbe = window.setTimeout(() => {
          if (!sawCwd.value) linkUnconfirmed.value = true
        }, 4000)
      }
    },
    onData(chunk) {
      inst?.write(decoder.decode(chunk, { stream: true }))
    },
    onClose(reason) {
      if (connState.value === 'connecting') {
        setState('error')
        statusMsg.value = reason || t('remoteWorkspace.connectFailed')
      } else {
        setState('closed')
        statusMsg.value = reason || t('remoteWorkspace.sessionEnded')
      }
      inst?.write('\r\n\x1b[2m── ' + statusMsg.value + ' ──\x1b[0m\r\n')
    },
  }
  conn.value = openServerTerminal(props.serverId, handlers, shell.value)
}

function disconnect(): void {
  clearLinkProbe()
  conn.value?.close()
  conn.value = null
  if (connState.value === 'connected' || connState.value === 'connecting') setState('closed')
}

/** 文件面板 → 终端:切目录。路径已由 lib/serverFs 转义,回车用 CR。 */
function runCd(path: string): void {
  if (connState.value !== 'connected') return
  conn.value?.send(cdCommand(path))
  term.value?.focus()
}

function focus(): void {
  term.value?.focus()
}

watch(shell, () => {
  if (connState.value === 'connected') void connect()
})

onMounted(() => {
  void connect()
})

onBeforeUnmount(() => {
  resizeObserver?.disconnect()
  resizeObserver = null
  disconnect()
  term.value?.dispose()
  term.value = null
})

defineExpose({ runCd, connect, disconnect, focus })
</script>

<template>
  <div class="term-pane">
    <div class="term-pane__bar">
      <span class="term-pane__host">{{ hostLabel || serverId }}</span>
      <label class="term-pane__shell">
        <span>Shell</span>
        <select v-model="shell" :aria-label="'Shell'" :disabled="connState === 'connecting'">
          <option v-for="s in shellOptions" :key="s" :value="s">{{ s || t('remoteWorkspace.shellAuto') }}</option>
        </select>
      </label>
      <span v-if="connState === 'connected'" class="term-pane__live">
        <span class="dot" /> LIVE<template v-if="latencyMs !== null"> · {{ latencyMs }}ms</template>
      </span>
      <span v-else-if="connState === 'connecting'" class="term-pane__state">{{ t('remoteWorkspace.connecting') }}</span>
      <span v-else class="term-pane__state err">{{ statusMsg || t('remoteWorkspace.disconnected') }}</span>
      <button
        v-if="connState === 'connected'"
        class="term-pane__btn"
        type="button"
        @click="disconnect"
      >{{ t('remoteWorkspace.disconnect') }}</button>
      <button
        v-else
        class="term-pane__btn"
        type="button"
        :disabled="connState === 'connecting'"
        @click="connect"
      >{{ connState === 'closed' || connState === 'error' ? t('remoteWorkspace.reconnect') : t('remoteWorkspace.connect') }}</button>
    </div>

    <div ref="termHost" class="term-pane__screen" tabindex="0" :aria-label="t('remoteWorkspace.terminalAria')" />

    <p v-if="showLinkNote" class="term-pane__note">
      {{ t('remoteWorkspace.cdLinkUnsupported') }}
    </p>
  </div>
</template>

<style scoped>
.term-pane {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-height: 0;
  height: 100%;
  padding: 6px 8px;
  background: #0b0d10;
  border-radius: var(--rounded-md);
}

.term-pane__bar {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-shrink: 0;
  font-size: var(--text-label);
  color: #9aa3ad;
}
.term-pane__host {
  font-weight: 600;
  color: #e6e6ea;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.term-pane__shell {
  display: inline-flex;
  align-items: center;
  gap: 5px;
}
.term-pane__shell select {
  font: inherit;
  color: #e6e6ea;
  background: #14171c;
  border: 1px solid #262b32;
  border-radius: var(--rounded-sm);
  padding: 1px 4px;
}
.term-pane__live {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  color: #7fe3f0;
  font-variant-numeric: tabular-nums;
}
.term-pane__live .dot {
  width: 6px;
  height: 6px;
  border-radius: var(--rounded-full);
  background: #7fe3f0;
}
.term-pane__state {
  color: #9aa3ad;
}
.term-pane__state.err {
  color: #f2b8a5;
}
.term-pane__btn {
  margin-left: auto;
  font: inherit;
  color: #cfd6dd;
  background: #14171c;
  border: 1px solid #2b313a;
  border-radius: var(--rounded-sm);
  padding: 2px 9px;
  cursor: pointer;
}
.term-pane__btn:hover:not(:disabled) {
  border-color: #7fe3f0;
  color: #fff;
}
.term-pane__btn:disabled {
  opacity: 0.55;
  cursor: default;
}

/* 终端画布必须能缩:分屏拖窄时 xterm 靠这块的 ResizeObserver 重算行列。 */
.term-pane__screen {
  flex: 1 1 auto;
  min-height: 0;
  overflow: hidden;
}
.term-pane__screen :deep(.xterm) {
  height: 100%;
}

.term-pane__note {
  margin: 0;
  flex-shrink: 0;
  font-size: var(--text-label);
  color: #8a919b;
}
</style>
