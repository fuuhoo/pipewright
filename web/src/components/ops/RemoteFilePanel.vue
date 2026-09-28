<!--
  RemoteFilePanel.vue —— 远程文件面板(「远程」弹窗的下半屏)

  一台服务器一个浏览会话:列目录 → 点目录进入(同时让上面的终端 cd 过去)→ 点文件编辑正文。
  能力面与后端 server_fs.go 一一对应:列表 / 取正文 / 存正文 / 下载 / 上传 / mkdir·remove·rename。

  三条刻意的设计:
    · **不提供递归删除**。后端只删空目录(409 directory_not_empty),界面也就不给一个
      能清掉整棵目录树的按钮 —— 远程 rm -rf 不该是弹窗里顺手能点到的东西。
    · **下载交给浏览器**。后端直接回 Content-Disposition,所以是一个 <a href>,
      不经过 fetch:大文件不该整份进内存,也不必重复实现进度条。
    · **正文编辑器是内联的一层**,不是套第二层弹窗:叠层弹窗的焦点/滚动/转义键管理
      在窄分屏里最容易出错,而这里只需要「看正文 + 改 + 存」。
-->
<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  fsDownloadUrl,
  fsOp,
  listFs,
  readFsContent,
  uploadFsFile,
  writeFsContent,
  type FsEntry,
} from '../../api/serverFs'
import { HttpError } from '../../api/http'
import { useToast } from '../../composables/useToast'
import { useConfirm } from '../../composables/useConfirm'
import {
  ROOT,
  formatBytes,
  formatMtime,
  joinPath,
  modeToRwx,
  normalizePath,
  parentDir,
  pathCrumbs,
  sortEntries,
} from '../../lib/serverFs'

const props = defineProps<{
  serverId: string
  /** 终端上报的当前目录;与面板不同时面板跟过去(联动是双向的,这一路是终端 → 面板)。 */
  terminalCwd?: string
}>()

const emit = defineEmits<{
  /** 用户在面板里导航(点目录 / 面包屑 / 手填路径):请让终端也 cd 过去。 */
  (e: 'cd', path: string): void
}>()

const { t } = useI18n()
const toast = useToast()
const confirm = useConfirm()

/** 面板当前所在目录(始终是后端回的真实路径,家目录在首屏就被解析掉)。 */
const currentPath = ref(ROOT)
const pathInput = ref('')
const backend = ref('')
const entries = ref<FsEntry[]>([])
const loading = ref(false)
/** 列表区错误条;fs_unsupported 单独走 unsupported 态(不是错误,是能力缺失)。 */
const errorText = ref('')
const unsupported = ref(false)

const crumbs = computed(() => pathCrumbs(currentPath.value))
const canGoUp = computed(() => currentPath.value !== ROOT)
/** 编辑器/内联输入开着时,导航要先把这层收掉,不然两层的 Enter 会打架。 */
const editing = ref(false)
const editorPath = ref('')
const editorContent = ref('')
const editorOriginal = ref('')
const editorTruncated = ref(false)
const editorBinary = ref(false)
const editorSaving = ref(false)
const editorLoading = ref(false)
const editorDirty = computed(() => editorContent.value !== editorOriginal.value)

type PromptKind = 'mkdir' | 'rename'
const prompt = ref<{ kind: PromptKind; target: FsEntry | null; value: string } | null>(null)

const uploading = ref(false)
const fileInput = ref<HTMLInputElement | null>(null)

/** 后端错误 → 人读一句:后端已按 UI 语言回 message,只在它缺失时兜底。 */
function humanize(err: unknown): string {
  if (err instanceof HttpError) {
    return err.apiError?.message || err.message || t('remoteWorkspace.panel.loadFailed')
  }
  return err instanceof Error ? err.message : t('remoteWorkspace.panel.loadFailed')
}

async function load(path: string, opts: { silent?: boolean } = {}): Promise<void> {
  if (!opts.silent) loading.value = true
  errorText.value = ''
  try {
    // 空串 = 家目录(与后端 Realpath(".") 同义);首屏就走这条路。
    const res = await listFs(props.serverId, path)
    currentPath.value = res.path || ROOT
    pathInput.value = currentPath.value
    backend.value = res.backend
    unsupported.value = false
    // 后端不保证顺序(尤其 exec 兜底走的是 shell glob),排序固定在界面这一层做。
    entries.value = res.isDir ? sortEntries(res.entries) : []
  } catch (err) {
    if (err instanceof HttpError && err.apiError?.code === 'fs_unsupported') {
      unsupported.value = true
      entries.value = []
    } else {
      errorText.value = humanize(err)
    }
  } finally {
    loading.value = false
  }
}

/** 导航:面板先动,再请终端跟上。 */
function navigate(path: string): void {
  const target = normalizePath(path)
  if (target === currentPath.value) return
  closeEditor()
  prompt.value = null
  void load(target)
  emit('cd', target)
}

function openEntry(e: FsEntry): void {
  if (e.isDir) navigate(e.path)
  else void openEditor(e.path)
}

function goUp(): void {
  navigate(parentDir(currentPath.value))
}

function submitPathInput(): void {
  const p = pathInput.value.trim()
  if (!p) {
    pathInput.value = currentPath.value
    return
  }
  navigate(p)
}

// ─── 终端 → 面板 ────────────────────────────────────────────────────────────────
// 只在与面板不同时才跳转:面板导航会引发终端 cd,终端 cd 又回报同一个路径 ——
// 不比较就是自己触发自己重新加载,列表闪一次、还多打一次 SSH。
watch(
  () => props.terminalCwd,
  (cwd) => {
    if (!cwd) return
    const norm = normalizePath(cwd)
    if (norm === currentPath.value) return
    closeEditor()
    prompt.value = null
    void load(norm)
  },
)

// ─── 正文编辑 ───────────────────────────────────────────────────────────────────
async function openEditor(path: string): Promise<void> {
  editing.value = true
  editorLoading.value = true
  editorPath.value = path
  editorContent.value = ''
  editorOriginal.value = ''
  editorTruncated.value = false
  editorBinary.value = false
  errorText.value = ''
  try {
    const res = await readFsContent(props.serverId, path)
    editorPath.value = res.path
    editorContent.value = res.content
    editorOriginal.value = res.content
    editorTruncated.value = res.truncated
    editorBinary.value = res.binary
  } catch (err) {
    errorText.value = humanize(err)
    editing.value = false
  } finally {
    editorLoading.value = false
  }
}

function closeEditor(): void {
  editing.value = false
  editorPath.value = ''
}

async function saveEditor(): Promise<void> {
  if (editorBinary.value || editorSaving.value) return
  editorSaving.value = true
  try {
    await writeFsContent(props.serverId, editorPath.value, editorContent.value)
    editorOriginal.value = editorContent.value
    toast.success(t('remoteWorkspace.panel.saved'), { detail: editorPath.value })
    await load(currentPath.value, { silent: true })
  } catch (err) {
    toast.error(t('remoteWorkspace.panel.saveFailed'), { detail: humanize(err) })
  } finally {
    editorSaving.value = false
  }
}

// ─── 建目录 / 改名 / 删除 ────────────────────────────────────────────────────────
function startMkdir(): void {
  closeEditor()
  prompt.value = { kind: 'mkdir', target: null, value: '' }
}

function startRename(e: FsEntry): void {
  closeEditor()
  prompt.value = { kind: 'rename', target: e, value: e.name }
}

/** 基名校验与后端 sanitizeRemoteName 同口径:不许路径分隔符、不许 `.`/`..`。 */
function badName(name: string): boolean {
  return !name || name === '.' || name === '..' || name.includes('/')
}

async function applyPrompt(): Promise<void> {
  const p = prompt.value
  if (!p) return
  const name = p.value.trim()
  if (badName(name)) {
    toast.warn(t('remoteWorkspace.panel.badName'))
    return
  }
  try {
    if (p.kind === 'mkdir') {
      await fsOp(props.serverId, 'mkdir', joinPath(currentPath.value, name))
      toast.success(t('remoteWorkspace.panel.mkdirDone'), { detail: joinPath(currentPath.value, name) })
    } else if (p.target) {
      await fsOp(props.serverId, 'rename', p.target.path, joinPath(parentDir(p.target.path), name))
      toast.success(t('remoteWorkspace.panel.renameDone'), { detail: name })
    }
    prompt.value = null
    await load(currentPath.value, { silent: true })
  } catch (err) {
    toast.error(t('remoteWorkspace.panel.opFailed'), { detail: humanize(err) })
  }
}

async function removeEntry(e: FsEntry): Promise<void> {
  closeEditor()
  const ok = await confirm.open({
    title: t('remoteWorkspace.panel.removeTitle'),
    // 目录只可能是空的才删掉(后端不提供递归删除),文案据实说明,不给「会连带删干净」的错觉。
    body: e.isDir
      ? t('remoteWorkspace.panel.removeBodyDir', { name: e.name })
      : t('remoteWorkspace.panel.removeBodyFile', { name: e.name }),
    confirmLabel: t('remoteWorkspace.panel.removeConfirm'),
    variant: 'danger',
  })
  if (!ok) return
  try {
    await fsOp(props.serverId, 'remove', e.path)
    toast.success(t('remoteWorkspace.panel.removeDone'), { detail: e.path })
    await load(currentPath.value, { silent: true })
  } catch (err) {
    toast.error(t('remoteWorkspace.panel.opFailed'), { detail: humanize(err) })
  }
}

// ─── 上传 ───────────────────────────────────────────────────────────────────────
function pickFiles(): void {
  fileInput.value?.click()
}

async function onFilesPicked(ev: Event): Promise<void> {
  const input = ev.target as HTMLInputElement
  const files = Array.from(input.files ?? [])
  if (!files.length) return
  uploading.value = true
  let ok = 0
  let detail = ''
  for (const f of files) {
    try {
      await uploadFsFile(props.serverId, currentPath.value, f)
      ok++
    } catch (err) {
      detail = humanize(err)
    }
  }
  input.value = ''
  uploading.value = false
  if (ok) {
    toast.success(t('remoteWorkspace.panel.uploadDone', { n: ok }), { detail: detail || undefined })
  } else {
    toast.error(t('remoteWorkspace.panel.uploadFailed'), { detail })
  }
  await load(currentPath.value, { silent: true })
}

onMounted(() => {
  // 空路径 = 该会话的家目录:比从界面猜一个 /root 或 /home 都诚实。
  void load('')
})
</script>

<template>
  <div class="fs-panel">
    <div class="fs-panel__bar">
      <button class="fs-btn" type="button" :disabled="!canGoUp" :title="t('remoteWorkspace.panel.up')" @click="goUp">↑</button>
      <button class="fs-btn" type="button" :title="t('remoteWorkspace.panel.refresh')" @click="load(currentPath)">⟳</button>
      <input
        v-model="pathInput"
        class="fs-path"
        type="text"
        spellcheck="false"
        :aria-label="t('remoteWorkspace.panel.pathAria')"
        @keydown.enter.prevent="submitPathInput"
      />
      <button class="fs-btn fs-btn--text" type="button" @click="startMkdir">{{ t('remoteWorkspace.panel.mkdir') }}</button>
      <button class="fs-btn fs-btn--text" type="button" :disabled="uploading" @click="pickFiles">
        {{ uploading ? t('remoteWorkspace.panel.uploading') : t('remoteWorkspace.panel.upload') }}
      </button>
      <input ref="fileInput" class="fs-file" type="file" multiple hidden @change="onFilesPicked" />
    </div>

    <nav class="fs-crumbs" :aria-label="t('remoteWorkspace.panel.crumbsAria')">
      <template v-for="(c, i) in crumbs" :key="c.path">
        <span v-if="i" class="fs-crumbs__sep">/</span>
        <button class="fs-crumb" :class="{ 'fs-crumb--here': i === crumbs.length - 1 }" type="button" @click="navigate(c.path)">
          {{ c.label }}
        </button>
      </template>
      <span v-if="backend" class="fs-crumbs__backend" :title="t('remoteWorkspace.panel.backendTitle')">{{ backend }}</span>
    </nav>

    <p v-if="unsupported" class="fs-note">{{ t('remoteWorkspace.panel.unsupported') }}</p>
    <p v-else-if="errorText && !editing" class="fs-note fs-note--err">{{ errorText }}</p>

    <!-- 内联编辑层 -->
    <section v-if="editing" class="fs-editor">
      <header class="fs-editor__head">
        <span class="fs-editor__path">{{ editorPath }}</span>
        <span v-if="editorTruncated" class="fs-warn">{{ t('remoteWorkspace.panel.truncated') }}</span>
        <span class="grow" />
        <button class="fs-btn fs-btn--text" type="button" @click="closeEditor">{{ t('remoteWorkspace.panel.closeEditor') }}</button>
      </header>
      <p v-if="errorText" class="fs-note fs-note--err">{{ errorText }}</p>
      <p v-else-if="editorBinary" class="fs-note">{{ t('remoteWorkspace.panel.binaryHint') }}</p>
      <textarea
        v-else
        v-model="editorContent"
        class="fs-editor__text"
        spellcheck="false"
        :readonly="editorLoading"
        :aria-label="t('remoteWorkspace.panel.editorAria')"
        :placeholder="editorLoading ? t('remoteWorkspace.panel.loading') : ''"
      />
      <footer class="fs-editor__foot">
        <span v-if="editorDirty" class="fs-dirty">{{ t('remoteWorkspace.panel.unsaved') }}</span>
        <span class="grow" />
        <button
          class="fs-btn fs-btn--primary"
          type="button"
          :disabled="editorBinary || editorLoading || !editorDirty || editorSaving"
          @click="saveEditor"
        >{{ editorSaving ? t('remoteWorkspace.panel.saving') : t('remoteWorkspace.panel.save') }}</button>
      </footer>
    </section>

    <!-- 列表层 -->
    <section v-else class="fs-list">
      <form v-if="prompt" class="fs-prompt" @submit.prevent="applyPrompt">
        <span class="fs-prompt__label">
          {{ prompt.kind === 'mkdir' ? t('remoteWorkspace.panel.mkdirIn', { path: currentPath }) : t('remoteWorkspace.panel.renameTo', { name: prompt.target?.name ?? '' }) }}
        </span>
        <input v-model="prompt.value" class="fs-prompt__input" type="text" :autofocus="true" @keydown.esc.prevent="prompt = null" />
        <button class="fs-btn fs-btn--primary" type="submit">{{ t('remoteWorkspace.panel.ok') }}</button>
        <button class="fs-btn" type="button" @click="prompt = null">{{ t('remoteWorkspace.panel.cancel') }}</button>
      </form>

      <div v-if="loading && !entries.length" class="fs-empty">{{ t('remoteWorkspace.panel.loading') }}</div>
      <div v-else-if="!entries.length && !errorText && !unsupported" class="fs-empty">{{ t('remoteWorkspace.panel.empty') }}</div>

      <table v-else class="fs-table">
        <thead>
          <tr>
            <th class="col-name">{{ t('remoteWorkspace.panel.colName') }}</th>
            <th class="col-mode">{{ t('remoteWorkspace.panel.colMode') }}</th>
            <th class="col-size">{{ t('remoteWorkspace.panel.colSize') }}</th>
            <th class="col-mtime">{{ t('remoteWorkspace.panel.colMtime') }}</th>
            <th class="col-ops">{{ t('remoteWorkspace.panel.colOps') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="e in entries" :key="e.path">
            <td class="col-name">
              <button class="fs-name" type="button" @click="openEntry(e)">
                <span class="fs-name__icon" aria-hidden="true">{{ e.isDir ? '▸' : e.isLink ? '↗' : '·' }}</span>
                <span :class="{ 'fs-name__dir': e.isDir }">{{ e.name }}</span>
                <span v-if="e.isLink && e.linkTarget" class="fs-name__link">→ {{ e.linkTarget }}</span>
              </button>
            </td>
            <td class="col-mode">{{ modeToRwx(e.mode) }}</td>
            <td class="col-size">{{ e.isDir ? '—' : formatBytes(e.size) }}</td>
            <td class="col-mtime">{{ formatMtime(e.mtime) || '—' }}</td>
            <td class="col-ops">
              <a
                v-if="!e.isDir"
                class="fs-op"
                :href="fsDownloadUrl(serverId, e.path)"
                :title="t('remoteWorkspace.panel.downloadTitle')"
              >{{ t('remoteWorkspace.panel.download') }}</a>
              <button v-if="!e.isDir" class="fs-op" type="button" @click="openEditor(e.path)">{{ t('remoteWorkspace.panel.edit') }}</button>
              <button class="fs-op" type="button" @click="startRename(e)">{{ t('remoteWorkspace.panel.rename') }}</button>
              <button class="fs-op fs-op--danger" type="button" @click="removeEntry(e)">{{ t('remoteWorkspace.panel.remove') }}</button>
            </td>
          </tr>
        </tbody>
      </table>
    </section>
  </div>
</template>

<style scoped>
.fs-panel {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-height: 0;
  height: 100%;
  padding: 8px;
  overflow: hidden;
}

.fs-panel__bar {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
}
.fs-path {
  flex: 1 1 auto;
  min-width: 0;
  font-family: var(--font-mono, ui-monospace, monospace);
  font-size: var(--text-label);
  color: var(--color-text);
  background: var(--color-inset);
  border: 1px solid var(--color-line);
  border-radius: var(--rounded-sm);
  padding: 4px 8px;
}
.fs-path:focus-visible {
  outline: 2px solid var(--color-accent, var(--color-line-strong));
  outline-offset: 1px;
}

.fs-btn {
  font: inherit;
  font-size: var(--text-label);
  color: var(--color-text);
  background: var(--color-surface);
  border: 1px solid var(--color-line);
  border-radius: var(--rounded-sm);
  padding: 3px 8px;
  cursor: pointer;
  white-space: nowrap;
}
.fs-btn:hover:not(:disabled) {
  border-color: var(--color-line-strong, var(--color-line));
}
.fs-btn:disabled {
  opacity: 0.5;
  cursor: default;
}
.fs-btn--text {
  padding: 3px 9px;
}
.fs-btn--primary {
  background: var(--color-primary, var(--color-surface));
  color: var(--color-on-primary, var(--color-text));
  border-color: transparent;
}

.fs-crumbs {
  display: flex;
  align-items: center;
  gap: 2px;
  flex-shrink: 0;
  font-size: var(--text-label);
  color: var(--color-dim);
  overflow-x: auto;
  white-space: nowrap;
}
.fs-crumb {
  font: inherit;
  color: var(--color-dim);
  background: none;
  border: 0;
  padding: 1px 3px;
  border-radius: var(--rounded-sm);
  cursor: pointer;
}
.fs-crumb:hover {
  color: var(--color-text);
  background: var(--color-inset);
}
.fs-crumb--here {
  color: var(--color-text);
  font-weight: 600;
  cursor: default;
}
.fs-crumbs__sep {
  color: var(--color-faint);
}
.fs-crumbs__backend {
  margin-left: auto;
  padding: 0 6px;
  font-size: var(--text-label);
  color: var(--color-faint);
  border: 1px solid var(--color-line);
  border-radius: var(--rounded-sm);
}

.fs-note {
  margin: 0;
  flex-shrink: 0;
  font-size: var(--text-label);
  color: var(--color-dim);
}
.fs-note--err {
  color: var(--color-red);
}
.fs-warn {
  font-size: var(--text-label);
  color: var(--color-warn, var(--color-dim));
}
.fs-empty {
  padding: 18px 8px;
  text-align: center;
  font-size: var(--text-label);
  color: var(--color-faint);
}

/* 编辑层:整块占满列表层的位置,textarea 自己滚。 */
.fs-editor {
  display: flex;
  flex-direction: column;
  gap: 6px;
  flex: 1 1 auto;
  min-height: 0;
}
.fs-editor__head {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-shrink: 0;
}
.fs-editor__path {
  font-family: var(--font-mono, ui-monospace, monospace);
  font-size: var(--text-label);
  color: var(--color-text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.grow {
  flex: 1 1 auto;
}
.fs-editor__text {
  flex: 1 1 auto;
  min-height: 0;
  resize: none;
  font-family: var(--font-mono, ui-monospace, monospace);
  font-size: var(--text-label);
  line-height: 1.5;
  color: var(--color-text);
  background: var(--color-inset);
  border: 1px solid var(--color-line);
  border-radius: var(--rounded-sm);
  padding: 8px;
  tab-size: 4;
}
.fs-editor__foot {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-shrink: 0;
}
.fs-dirty {
  font-size: var(--text-label);
  color: var(--color-warn, var(--color-dim));
}

.fs-list {
  display: flex;
  flex-direction: column;
  gap: 6px;
  flex: 1 1 auto;
  min-height: 0;
  overflow: auto;
}
.fs-prompt {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
  padding: 6px;
  background: var(--color-inset);
  border: 1px solid var(--color-line);
  border-radius: var(--rounded-sm);
}
.fs-prompt__label {
  font-size: var(--text-label);
  color: var(--color-dim);
  white-space: nowrap;
}
.fs-prompt__input {
  flex: 1 1 auto;
  min-width: 0;
  font: inherit;
  font-size: var(--text-label);
  color: var(--color-text);
  background: var(--color-surface);
  border: 1px solid var(--color-line);
  border-radius: var(--rounded-sm);
  padding: 3px 6px;
}

.fs-table {
  width: 100%;
  border-collapse: collapse;
  font-size: var(--text-label);
}
.fs-table th {
  position: sticky;
  top: 0;
  z-index: 1;
  text-align: left;
  font-weight: 600;
  color: var(--color-faint);
  background: var(--color-surface);
  border-bottom: 1px solid var(--color-line);
  padding: 4px 6px;
}
.fs-table td {
  border-bottom: 1px solid var(--color-line);
  padding: 2px 6px;
  vertical-align: middle;
}
.col-name {
  min-width: 0;
}
.col-mode,
.col-size,
.col-mtime {
  color: var(--color-dim);
  font-family: var(--font-mono, ui-monospace, monospace);
  white-space: nowrap;
}
.col-size {
  text-align: right;
}
.col-ops {
  white-space: nowrap;
  text-align: right;
}

.fs-name {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  max-width: 100%;
  font: inherit;
  color: var(--color-text);
  background: none;
  border: 0;
  padding: 2px 0;
  cursor: pointer;
  text-align: left;
}
.fs-name:hover .fs-name__dir,
.fs-name:hover span:last-child {
  text-decoration: underline;
}
.fs-name__icon {
  color: var(--color-faint);
  flex-shrink: 0;
}
.fs-name__dir {
  font-weight: 600;
}
.fs-name__link {
  color: var(--color-faint);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.fs-op {
  font: inherit;
  font-size: var(--text-label);
  color: var(--color-dim);
  background: none;
  border: 0;
  border-bottom: 1px solid transparent;
  padding: 1px 2px;
  margin-left: 6px;
  cursor: pointer;
  text-decoration: none;
  white-space: nowrap;
}
.fs-op:hover {
  color: var(--color-text);
  border-bottom-color: var(--color-line-strong, var(--color-line));
}
.fs-op--danger:hover {
  color: var(--color-red);
  border-bottom-color: var(--color-red-line, var(--color-red));
}
</style>
