<!--
  RemoteFilePanel.vue —— 远程文件面板(「远程」弹窗的下半屏)

  一台服务器一个浏览会话:列目录 → 点目录进入(同时让上面的终端 cd 过去)→ 点文件编辑正文。
  能力面与后端 server_fs.go 一一对应:列表 / 取正文 / 存正文 / 下载 / 上传 / mkdir·remove·rename。

  三条刻意的设计:
    · **不提供递归删除**。后端只删空目录(409 directory_not_empty),界面也就不给一个
      能清掉整棵目录树的按钮 —— 远程 rm -rf 不该是弹窗里顺手能点到的东西。
    · **下载交给浏览器**。后端直接回 Content-Disposition(目录是流式 zip),所以是一个
      <a href>,不经过 fetch:大文件不该整份进内存,也不必重复实现进度条。
    · **上传分块且可续**。文件按体积切块逐块推,界面给逐文件进度 + 取消/重试;断了从
      远端实测偏移接着传,所以 64 GB 的传输不会因为一次网络抖动从头再来(算术在
      lib/fsUpload,这里只做选择器与渲染)。
    · **传输记录只记到平台真知道的那一步**。上传的逐文件结果是从驱动器状态折算的;
      下载只有「已交给浏览器」这一条 —— 字节流不经过 JS,成功与否平台无从得知,
      画成「已完成」就是撒谎。记录活在当前标签页内存里(lib/fsTransfers),按服务器
      分格:关弹窗还在,刷新页面即空,不落盘也不问服务端。
    · **正文编辑器是内联的一层**,不是套第二层弹窗:叠层弹窗的焦点/滚动/转义键管理
      在窄分屏里最容易出错,而这里只需要「看正文 + 改 + 存」。

  版式照 FinalShell 的右半屏:左边目录树(只列目录、点开哪层才加载哪层),
  右边 文件名/大小/类型/修改时间/权限 的表格。树与列表共用同一批 listFs 请求结果之外的
  请求,所以它是懒的 —— 导航到哪一层才展开到哪一层。
-->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  fsDownloadUrl,
  fsOp,
  fsUploadDeps,
  listFs,
  readFsContent,
  writeFsContent,
  type FsEntry,
} from '../../api/serverFs'
import { HttpError } from '../../api/http'
import { useToast } from '../../composables/useToast'
import { useConfirm } from '../../composables/useConfirm'
import ProgressBar from '../ui/ProgressBar.vue'
import {
  runUploadSession,
  sessionProgress,
  uploadSourcesOf,
  type UploadItemState,
  type UploadSessionHandle,
} from '../../lib/fsUpload'
import {
  applyUploadBatch,
  downloadRowOf,
  formatClock,
  newTransferBatchId,
  prependTransfer,
  setTransferLog,
  transferLogOf,
  transferSummary,
  uploadRowsOf,
  type TransferRow,
} from '../../lib/fsTransfers'
import {
  ROOT,
  fileExt,
  formatBytes,
  formatMtime,
  joinPath,
  modeToLs,
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

const fileInput = ref<HTMLInputElement | null>(null)
const dirInput = ref<HTMLInputElement | null>(null)
/**
 * 上传队列的渲染态。驱动器每次推进都整份覆盖它(lib 那边是唯一真源),
 * 所以这里不自己算 offset/计数,免得两份簿记对不上。
 */
const uploadItems = ref<UploadItemState[]>([])
const queueOpen = ref(true)
/** 驱动器只用来调 retry/cancel,不参与渲染:塞进 ref 会让 Vue 深代理整条回调链。 */
let session: UploadSessionHandle | null = null

/** 队列里最多渲染这么多行:选一个 5000 文件的目录也值得界面只画一屏。 */
const QUEUE_ROWS = 200

// ─── 传输记录(本次会话) ────────────────────────────────────────────────────────
// 默认收起:下面已经有一条上传队列,再常驻一块记录会把文件表格挤没。
// 工具栏那个数字徽标就是入口 —— 功能不该藏在没人发现的角落里。
const transfers = ref<TransferRow[]>(transferLogOf(props.serverId))
const txOpen = ref(false)
/** 记录里最多渲染这么多行:计数照常算,DOM 不为几千条记录排队。 */
const TX_ROWS = 120
/**
 * 当前批次的身份与发起态。驱动器的 item.key 只是批次内序号,跨批次会撞,
 * 所以记录行要自己带批次号;落点目录也要在发起时钉住 —— 批次跑一半时终端 cd 走了,
 * 记录里写的还得是文件真正落下的那层。
 */
let batchId = ''
let batchDir = ''
let batchAt = 0

/** 记录的唯一出口:ref 与「同一标签页」的存储同时更新,免得重开弹窗看到旧的一半。 */
function commitTransfers(rows: TransferRow[]): void {
  transfers.value = rows
  setTransferLog(props.serverId, rows)
}

// 一台服务器一份记录:换服务器就换成它那格(上面那台还在内存里,切回来还在)。
watch(
  () => props.serverId,
  (id) => {
    transfers.value = transferLogOf(id)
  },
)

const txTally = computed(() => transferSummary(transfers.value))
const txRows = computed(() => transfers.value.slice(0, TX_ROWS))
const txHidden = computed(() => Math.max(0, transfers.value.length - TX_ROWS))

// ─── 左侧目录树 ─────────────────────────────────────────────────────────────────
// 只装目录、逐层懒加载:一次列目录若顺手把整棵子树拉平,慢的机器上首屏就得等
// 几十次 SSH。展开哪一层才问哪一层,读不到(权限、竞态)当作没有子目录 ——
// 主列表已经如实报过错,树不该把同一个错误再演一遍。
interface TreeNode {
  name: string
  path: string
  /** null = 还没加载过。 */
  children: TreeNode[] | null
  loading: boolean
  expanded: boolean
}

function makeNode(name: string, path: string): TreeNode {
  return { name, path, children: null, loading: false, expanded: false }
}

const treeRoot = ref<TreeNode>(makeNode(ROOT, ROOT))
const treeCollapsed = ref(false)

// ─── 树宽可拖 ───────────────────────────────────────────────────────────────────
// 固定 190px 在目录名长的机器上会把名字截成猜不出的一截,而表格那侧也要留够列宽。
// 宽度存 localStorage:这台机日志目录深、那台机部署目录名长,调过一次就该记住
// (和远程弹窗那条上下分隔条同一个道理)。
const TREE_W_KEY = 'pw-remote-tree-w'
const TREE_W_DEFAULT = 190
const TREE_W_MIN = 140
const TREE_W_MAX = 520
/** 右列至少留这么多:再窄整列文件名都要横向滚,那就别给拖。 */
const LIST_W_MIN = 260

const cols = ref<HTMLElement | null>(null)
const treeWidth = ref(readTreeWidth())
const treeDragging = ref(false)
const treePaneStyle = computed(() => `0 0 ${treeWidth.value}px`)

function readTreeWidth(): number {
  const raw = Number(localStorage.getItem(TREE_W_KEY))
  return Number.isFinite(raw) && raw > 0 ? Math.round(raw) : TREE_W_DEFAULT
}

/** 上界同时受绝对上限和「当前这栏还剩多少给列表」约束。 */
function clampTreeWidth(w: number, avail: number): number {
  const hi = Math.max(TREE_W_MIN, Math.min(TREE_W_MAX, avail - LIST_W_MIN))
  return Math.round(Math.min(hi, Math.max(TREE_W_MIN, w)))
}

function availWidth(): number {
  const rect = cols.value?.getBoundingClientRect()
  return rect && rect.width > 0 ? rect.width : Number.POSITIVE_INFINITY
}

let dragStartX = 0
let dragStartW = 0

function onTreeDragStart(e: PointerEvent): void {
  if (e.button !== 0) return
  treeDragging.value = true
  dragStartX = e.clientX
  dragStartW = treeWidth.value
  e.preventDefault()
  window.addEventListener('pointermove', onTreeDragMove)
  window.addEventListener('pointerup', onTreeDragEnd)
}

function onTreeDragMove(e: PointerEvent): void {
  treeWidth.value = clampTreeWidth(dragStartW + (e.clientX - dragStartX), availWidth())
}

function saveTreeWidth(): void {
  try {
    localStorage.setItem(TREE_W_KEY, String(treeWidth.value))
  } catch {
    /* 隐私模式 / 配额:这次拖出来的宽度只活在当前会话 */
  }
}

function onTreeDragEnd(): void {
  treeDragging.value = false
  window.removeEventListener('pointermove', onTreeDragMove)
  window.removeEventListener('pointerup', onTreeDragEnd)
  saveTreeWidth()
}

// 键盘也该能调:鼠标不便用的场景不该被一条拖不动的分隔条锁死。
function onTreeDragKeydown(e: KeyboardEvent): void {
  if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return
  e.preventDefault()
  treeWidth.value = clampTreeWidth(treeWidth.value + (e.key === 'ArrowLeft' ? -16 : 16), availWidth())
  saveTreeWidth()
}

// 弹窗退出全屏 / 窗口变窄时,可用宽度会小于已存的树宽:把它夹回去,别把列表挤没。
// ResizeObserver 只观察 .fs-cols 自身宽度(它由外层定,不随树宽变),不会自激循环。
function clampTreeToBox(): void {
  const next = clampTreeWidth(treeWidth.value, availWidth())
  if (next !== treeWidth.value) treeWidth.value = next
}

let colsRo: ResizeObserver | null = null

onMounted(() => {
  if (!cols.value || typeof ResizeObserver === 'undefined') return
  colsRo = new ResizeObserver(clampTreeToBox)
  colsRo.observe(cols.value)
})

onBeforeUnmount(() => {
  colsRo?.disconnect()
  colsRo = null
  window.removeEventListener('pointermove', onTreeDragMove)
  window.removeEventListener('pointerup', onTreeDragEnd)
})

/** 深度优先摊平成可渲染行:折叠的节点连子树一起跳过。 */
const treeRows = computed(() => {
  const rows: { node: TreeNode; depth: number }[] = []
  const walk = (node: TreeNode, depth: number): void => {
    rows.push({ node, depth })
    if (node.expanded && node.children) for (const child of node.children) walk(child, depth + 1)
  }
  walk(treeRoot.value, 0)
  return rows
})

async function loadChildren(node: TreeNode): Promise<void> {
  if (node.children || node.loading) return
  node.loading = true
  try {
    const res = await listFs(props.serverId, node.path)
    node.children = sortEntries(res.entries)
      .filter((e) => e.isDir)
      .map((e) => makeNode(e.name, e.path))
  } catch {
    /* 保持 children=null:下次点开还能重试 */
  } finally {
    node.loading = false
  }
}

function toggleNode(node: TreeNode): void {
  node.expanded = !node.expanded
  if (node.expanded) void loadChildren(node)
}

/** 导航后把树展开到当前位置;seq 让后一次导航作废前一次,深路径不会串位。 */
let treeSeq = 0

async function revealInTree(path: string): Promise<void> {
  const seq = ++treeSeq
  let node = treeRoot.value
  await loadChildren(node)
  for (const crumb of pathCrumbs(path).slice(1)) {
    if (seq !== treeSeq) return
    node.expanded = true
    const next = (node.children ?? []).find((c) => c.name === crumb.label)
    if (!next) return
    node = next
    await loadChildren(node)
  }
  if (seq === treeSeq) node.expanded = true
}

function findLoadedNode(path: string): TreeNode | null {
  const target = normalizePath(path)
  const stack: TreeNode[] = [treeRoot.value]
  while (stack.length) {
    const n = stack.pop() as TreeNode
    if (n.path === target) return n
    if (n.children) stack.push(...n.children)
  }
  return null
}

/** 建目录 / 改名之后让对应那一层重新加载,不然树会停在旧快照上骗人。 */
function invalidateTree(path: string): void {
  const node = findLoadedNode(path)
  if (!node || !node.children) return
  node.children = null
  if (node.expanded) void loadChildren(node)
}

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
    void revealInTree(currentPath.value)
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

/** 「类型」列的文案:目录/链接是中文名,普通文件给后缀(无后缀回「文件」)。 */
function typeLabel(e: FsEntry): string {
  if (e.isDir) return t('remoteWorkspace.panel.typeDir')
  if (e.isLink) return t('remoteWorkspace.panel.typeLink')
  const ext = fileExt(e.name)
  return ext ? t('remoteWorkspace.panel.typeFileExt', { ext: ext.toUpperCase() }) : t('remoteWorkspace.panel.typeFile')
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
    // 树是逐层缓存的:新建/改名后让对应那一层重新加载,不然它停在旧快照上。
    invalidateTree(p.kind === 'mkdir' ? currentPath.value : parentDir(p.target?.path ?? currentPath.value))
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
    invalidateTree(parentDir(e.path))
  } catch (err) {
    toast.error(t('remoteWorkspace.panel.opFailed'), { detail: humanize(err) })
  }
}

// ─── 上传 ───────────────────────────────────────────────────────────────────────
// 一次选择 = 一个批次:批次内共用一条 SSH 连接、按 rel 建出目录结构,断了能从
// 远端实测偏移接着传(全部算术在 lib/fsUpload 里,这里只喂 File 与渲染进度)。
// 批次期间不给再选:两个批次抢同一条连接池会把进度搅乱,而用户本来也只看一条队列。

/** 还有块在飞的批次不算结束:一条都失败过也照样要留在队列里等用户点重试。 */
const uploadActive = computed(
  () =>
    session !== null &&
    uploadItems.value.some((it) => it.status === 'queued' || it.status === 'uploading'),
)

/** 计数一律现算:retry 会把一项从终态拉回在传,增量维护的数字一定会说谎。 */
const uploadTally = computed(() => {
  let ok = 0
  let failed = 0
  let canceled = 0
  for (const it of uploadItems.value) {
    if (it.status === 'done') ok++
    else if (it.status === 'error') failed++
    else if (it.status === 'canceled') canceled++
  }
  return { ok, failed, canceled, total: uploadItems.value.length }
})

const uploadProgress = computed(() => sessionProgress(uploadItems.value))

/** 只渲染前若干行:整批计数照常算,DOM 不为 5000 个文件排队。 */
const uploadRows = computed(() => uploadItems.value.slice(0, QUEUE_ROWS))
const uploadHidden = computed(() => Math.max(0, uploadItems.value.length - QUEUE_ROWS))

function startUpload(files: File[]): void {
  const sources = uploadSourcesOf(files)
  if (!sources.length) return
  queueOpen.value = true
  batchId = newTransferBatchId()
  batchDir = currentPath.value
  batchAt = Date.now()
  const owner = props.serverId
  session = runUploadSession({
    dir: currentPath.value,
    files: sources,
    deps: fsUploadDeps(props.serverId),
    onUpdate: (items) => {
      uploadItems.value = items
      // 换过服务器就别再往新那格的记录里写:旧批次的字节属于旧服务器。
      if (props.serverId !== owner) return
      commitTransfers(applyUploadBatch(transfers.value, uploadRowsOf(batchId, batchDir, items, batchAt)))
    },
  })
  void session.finished.then((tally) => {
    if (tally.ok > 0) {
      toast.success(t('remoteWorkspace.panel.uploadDone', { n: tally.ok }), {
        detail: tally.failed ? t('remoteWorkspace.panel.uploadSomeFailed', { n: tally.failed }) : undefined,
      })
      // 新落地的文件要重列才看得见:整批问一次,别按文件数打 SSH。
      void load(currentPath.value, { silent: true })
      invalidateTree(currentPath.value)
    } else if (tally.failed > 0) {
      toast.error(t('remoteWorkspace.panel.uploadFailed'))
    }
  })
}

function onFilesPicked(ev: Event): void {
  const input = ev.target as HTMLInputElement
  const files = Array.from(input.files ?? [])
  // 先清 value:同一个文件再选一次也要算新批次(ChangeEvent 只认变化)。
  input.value = ''
  if (files.length) startUpload(files)
}

function uploadErrorText(code?: string): string {
  switch (code) {
    case 'network':
    case 'upload_timeout':
    case 'chunk_in_flight':
      return t('remoteWorkspace.panel.uploadErrNetwork')
    case 'upload_busy':
      return t('remoteWorkspace.panel.uploadErrBusy')
    case 'upload_not_found':
    case 'upload_closed':
    case 'incomplete_upload':
      return t('remoteWorkspace.panel.uploadErrExpired')
    case 'forbidden':
      return t('remoteWorkspace.panel.uploadErrForbidden')
    case 'fs_unsupported':
      return t('remoteWorkspace.panel.uploadErrUnsupported')
    case 'invalid_path':
    case 'bad_request':
    case 'duplicate_file':
    case 'chunk_too_large':
      return t('remoteWorkspace.panel.uploadErrRejected')
    default:
      return t('remoteWorkspace.panel.uploadErrOther')
  }
}

function uploadStatusText(it: UploadItemState): string {
  if (it.status === 'error') return uploadErrorText(it.error)
  if (it.status === 'done') return t('remoteWorkspace.panel.uploadStateDone')
  if (it.status === 'canceled') return t('remoteWorkspace.panel.uploadStateCanceled')
  if (it.status === 'queued') return t('remoteWorkspace.panel.uploadStateQueued')
  return t('remoteWorkspace.panel.uploadStateUploading')
}

/** 单行读数:done 一定是 100%(末块进度报不满,别让完成态看着像差一块)。 */
function uploadRatio(it: UploadItemState): number {
  if (it.status === 'done') return 100
  if (it.size <= 0) return 0
  return Math.min(100, Math.round((it.offset / it.size) * 100))
}

function uploadVariant(it: UploadItemState): 'default' | 'success' | 'warn' | 'error' {
  if (it.status === 'done') return 'success'
  if (it.status === 'error') return 'error'
  if (it.status === 'canceled') return 'warn'
  return 'default'
}

/** 只有排队/在飞的可取消;done 不可动,失败与已取消的改成「重试」。 */
function uploadCancellable(it: UploadItemState): boolean {
  return it.status === 'queued' || it.status === 'uploading'
}

// ─── 下载:交给浏览器,顺手记一条 ────────────────────────────────────────────────
/**
 * `<a href>` 才是下载本体(整份文件不进 JS),这里只补一条「已发起」。
 * 不 preventDefault:拦了就等于把用户要的文件拦掉了。
 */
function noteDownload(e: FsEntry): void {
  commitTransfers(
    prependTransfer(transfers.value, downloadRowOf({ name: e.name, path: e.path, size: e.size, isDir: e.isDir })),
  )
}

function txStateText(r: TransferRow): string {
  if (r.direction === 'download') return t('remoteWorkspace.panel.txStateStarted')
  if (r.status === 'error') return uploadErrorText(r.error)
  if (r.status === 'done') return t('remoteWorkspace.panel.uploadStateDone')
  if (r.status === 'canceled') return t('remoteWorkspace.panel.uploadStateCanceled')
  if (r.status === 'queued') return t('remoteWorkspace.panel.uploadStateQueued')
  return t('remoteWorkspace.panel.uploadStateUploading')
}

/** 上传才有进度条:下载的字节在浏览器那一侧,平台画不出真的条。 */
function txRatio(r: TransferRow): number {
  if (r.size === null || r.size <= 0) return r.status === 'done' ? 100 : 0
  if (r.status === 'done') return 100
  return Math.min(100, Math.round((r.offset / r.size) * 100))
}

function txVariant(r: TransferRow): 'default' | 'success' | 'warn' | 'error' {
  if (r.status === 'done') return 'success'
  if (r.status === 'error') return 'error'
  if (r.status === 'canceled') return 'warn'
  return 'default'
}

/** 字节读数:下载只给体积(体积未知就空着,别写 0 B);上传给「已传 / 总」。 */
function txBytes(r: TransferRow): string {
  if (r.direction === 'download') return r.size === null ? '' : formatBytes(r.size)
  if (r.size === null) return ''
  return `${formatBytes(r.offset)} / ${formatBytes(r.size)}`
}


onMounted(() => {
  // 弹窗关着的时候批次照跑、记录照写:重开要以存储为准,别显示卸载前那一半。
  transfers.value = transferLogOf(props.serverId)
  // 空路径 = 该会话的家目录:比从界面猜一个 /root 或 /home 都诚实。
  void load('')
})
</script>

<template>
  <div class="fs-panel">
    <div class="fs-bar">
      <button
        class="fs-ibtn"
        :class="{ 'fs-ibtn--on': !treeCollapsed }"
        type="button"
        :aria-pressed="!treeCollapsed"
        :title="treeCollapsed ? t('remoteWorkspace.panel.expandTree') : t('remoteWorkspace.panel.collapseTree')"
        @click="treeCollapsed = !treeCollapsed"
      >▤</button>
      <button class="fs-ibtn" type="button" :disabled="!canGoUp" :title="t('remoteWorkspace.panel.up')" @click="goUp">↑</button>
      <button class="fs-ibtn" type="button" :title="t('remoteWorkspace.panel.refresh')" @click="load(currentPath)">⟳</button>
      <span class="fs-bar__sep" aria-hidden="true" />
      <button class="fs-ibtn" type="button" :title="t('remoteWorkspace.panel.mkdir')" @click="startMkdir">＋</button>
      <button
        class="fs-ibtn"
        type="button"
        :disabled="uploadActive"
        :title="uploadActive ? t('remoteWorkspace.panel.uploading') : t('remoteWorkspace.panel.uploadFiles')"
        @click="fileInput?.click()"
      >
        <svg class="fs-ibtn-ico" viewBox="0 0 16 16" aria-hidden="true">
          <path d="M4.4 2.4h4l3.2 3.2v8h-7.2z" />
          <path d="M8 12.8V8.2M6.2 10 8 8.2l1.8 1.8" />
        </svg>
      </button>
      <button
        class="fs-ibtn"
        type="button"
        :disabled="uploadActive"
        :title="uploadActive ? t('remoteWorkspace.panel.uploading') : t('remoteWorkspace.panel.uploadFolder')"
        @click="dirInput?.click()"
      >
        <svg class="fs-ibtn-ico" viewBox="0 0 16 16" aria-hidden="true">
          <path d="M2 4.8a1.6 1.6 0 0 1 1.6-1.6h2.2l1.3 1.7h5.3A1.6 1.6 0 0 1 14 6.5v4.9a1.6 1.6 0 0 1-1.6 1.6H3.6A1.6 1.6 0 0 1 2 11.4z" />
          <path d="M8 11.8V7.6M6.2 9.4 8 7.6l1.8 1.8" />
        </svg>
      </button>
      <span class="fs-bar__sep" aria-hidden="true" />
      <button
        class="fs-ibtn"
        type="button"
        :disabled="!uploadItems.length"
        :title="queueOpen ? t('remoteWorkspace.panel.queueCollapse') : t('remoteWorkspace.panel.queueExpand')"
        :aria-pressed="queueOpen"
        @click="queueOpen = !queueOpen"
      >≡</button>
      <button
        class="fs-ibtn"
        type="button"
        :class="{ 'fs-ibtn--on': txOpen }"
        :title="txOpen ? t('remoteWorkspace.panel.txCollapse') : t('remoteWorkspace.panel.txExpand')"
        :aria-pressed="txOpen"
        @click="txOpen = !txOpen"
      >⇅</button>
      <span v-if="transfers.length" class="fs-badge" :title="t('remoteWorkspace.panel.txAria')">{{ transfers.length }}</span>
      <input
        v-model="pathInput"
        class="fs-path"
        type="text"
        spellcheck="false"
        :aria-label="t('remoteWorkspace.panel.pathAria')"
        @keydown.enter.prevent="submitPathInput"
      />
      <span v-if="backend" class="fs-badge" :title="t('remoteWorkspace.panel.backendTitle')">{{ backend }}</span>
      <input ref="fileInput" class="fs-file" type="file" multiple hidden @change="onFilesPicked" />
      <!-- 整个目录树交给后端按 rel 重建:浏览器只交文件,平台不碰落点以外的东西 -->
      <input
        ref="dirInput"
        class="fs-file"
        type="file"
        multiple
        webkitdirectory
        hidden
        @change="onFilesPicked"
      />
    </div>

    <nav class="fs-crumbs" :aria-label="t('remoteWorkspace.panel.crumbsAria')">
      <template v-for="(c, i) in crumbs" :key="c.path">
        <span v-if="i" class="fs-crumbs__sep">/</span>
        <button class="fs-crumb" :class="{ 'fs-crumb--here': i === crumbs.length - 1 }" type="button" @click="navigate(c.path)">
          {{ c.label }}
        </button>
      </template>
    </nav>

    <p v-if="unsupported" class="fs-note">{{ t('remoteWorkspace.panel.unsupported') }}</p>
    <p v-else-if="errorText && !editing" class="fs-note fs-note--err">{{ errorText }}</p>

    <div ref="cols" class="fs-cols" :class="{ 'fs-cols--dragging': treeDragging }">
      <!-- 目录树:只列目录、点开哪层加载哪层 -->
      <aside
        v-if="!unsupported && !treeCollapsed"
        class="fs-tree"
        :style="{ flex: treePaneStyle }"
        :aria-label="t('remoteWorkspace.panel.treeAria')"
      >
        <div v-for="row in treeRows" :key="row.node.path" class="fs-tree__row" :style="{ '--depth': String(row.depth) }">
          <button
            class="fs-tree__twisty"
            type="button"
            :disabled="row.node.loading"
            :aria-label="row.node.expanded ? t('remoteWorkspace.panel.treeCollapse') : t('remoteWorkspace.panel.treeExpand')"
            @click.stop="toggleNode(row.node)"
          >{{ row.node.loading ? '·' : row.node.expanded ? '▾' : '▸' }}</button>
          <button
            class="fs-tree__name"
            :class="{ 'fs-tree__name--here': row.node.path === currentPath }"
            type="button"
            :title="row.node.path"
            @click="navigate(row.node.path)"
          >
            <svg class="fs-ico fs-ico--dir" viewBox="0 0 16 16" aria-hidden="true"><path d="M1.75 3.5c0-.69.56-1.25 1.25-1.25h2.7c.4 0 .77.19 1 .51l.7 1.04h4.6c.69 0 1.25.56 1.25 1.25v6.9c0 .69-.56 1.25-1.25 1.25H3A1.25 1.25 0 0 1 1.75 13.25z" /></svg>
            <span class="fs-tree__label">{{ row.node.name }}</span>
          </button>
        </div>
      </aside>

      <!-- 树宽分隔条:跟弹窗那条上下分隔条同一套交互(鼠标拖 + 方向键微调)。 -->
      <div
        v-if="!unsupported && !treeCollapsed"
        class="fs-resizer"
        role="separator"
        aria-orientation="vertical"
        tabindex="0"
        :aria-label="t('remoteWorkspace.panel.treeResize')"
        :title="t('remoteWorkspace.panel.treeResize')"
        @pointerdown="onTreeDragStart"
        @keydown="onTreeDragKeydown"
      >
        <span class="fs-resizer__grip" aria-hidden="true" />
      </div>

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
              <th class="col-size">{{ t('remoteWorkspace.panel.colSize') }}</th>
              <th class="col-type">{{ t('remoteWorkspace.panel.colType') }}</th>
              <th class="col-mtime">{{ t('remoteWorkspace.panel.colMtime') }}</th>
              <th class="col-mode">{{ t('remoteWorkspace.panel.colMode') }}</th>
              <th class="col-ops">{{ t('remoteWorkspace.panel.colOps') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="e in entries" :key="e.path">
              <td class="col-name">
                <button class="fs-name" type="button" @click="openEntry(e)">
                  <svg v-if="e.isDir" class="fs-ico fs-ico--dir" viewBox="0 0 16 16" aria-hidden="true">
                    <path d="M1.75 3.5c0-.69.56-1.25 1.25-1.25h2.7c.4 0 .77.19 1 .51l.7 1.04h4.6c.69 0 1.25.56 1.25 1.25v6.9c0 .69-.56 1.25-1.25 1.25H3A1.25 1.25 0 0 1 1.75 13.25z" />
                  </svg>
                  <svg v-else class="fs-ico" :class="{ 'fs-ico--link': e.isLink }" viewBox="0 0 16 16" aria-hidden="true">
                    <path d="M4.75 1.75c0-.414.336-.75.75-.75h3.6l3.15 3.15v9.35c0 .414-.336.75-.75.75h-6a.75.75 0 0 1-.75-.75z" />
                  </svg>
                  <span :class="{ 'fs-name__dir': e.isDir }">{{ e.name }}</span>
                  <span v-if="e.isLink && e.linkTarget" class="fs-name__link">→ {{ e.linkTarget }}</span>
                </button>
              </td>
              <!-- 目录不给体积:两路后端回的都是 0/块数,画成 0 B 是假数据(FinalShell 同样留空)。 -->
              <td class="col-size">{{ e.isDir ? '' : formatBytes(e.size) }}</td>
              <td class="col-type">{{ typeLabel(e) }}</td>
              <td class="col-mtime">{{ formatMtime(e.mtime) }}</td>
              <td class="col-mode">{{ modeToLs(e) }}</td>
              <td class="col-ops">
                <!-- 目录也给出下载:后端按路径自己判,目录走流式 zip(不落中间文件)。 -->
                <a
                  class="fs-op"
                  :href="fsDownloadUrl(serverId, e.path)"
                  :title="e.isDir ? t('remoteWorkspace.panel.downloadDirTitle') : t('remoteWorkspace.panel.downloadTitle')"
                  @click="noteDownload(e)"
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

    <!-- 上传队列:整批一条按字节加权的总进度 + 逐文件一行(取消/重试都在行内) -->
    <section v-if="uploadItems.length" class="fs-queue" :aria-label="t('remoteWorkspace.panel.queueAria')">
      <header class="fs-queue__head">
        <span class="fs-queue__title">
          {{ t('remoteWorkspace.panel.queueTitle', { ok: uploadTally.ok, total: uploadTally.total }) }}
        </span>
        <span v-if="uploadTally.failed" class="fs-queue__warn">
          {{ t('remoteWorkspace.panel.uploadSomeFailed', { n: uploadTally.failed }) }}
        </span>
        <ProgressBar
          class="fs-queue__bar"
          :value="Math.round(uploadProgress.ratio * 100)"
          :variant="uploadTally.failed ? 'warn' : 'default'"
          :label="t('remoteWorkspace.panel.queueAria')"
        />
        <span class="fs-queue__bytes">
          {{ formatBytes(uploadProgress.bytes) }} / {{ formatBytes(uploadProgress.total) }}
        </span>
        <span class="grow" />
        <button v-if="uploadActive" class="fs-btn" type="button" @click="session?.cancelAll()">
          {{ t('remoteWorkspace.panel.queueCancelAll') }}
        </button>
        <button v-else class="fs-btn" type="button" @click="uploadItems = []">
          {{ t('remoteWorkspace.panel.queueClear') }}
        </button>
      </header>

      <ul v-if="queueOpen" class="fs-queue__list">
        <li v-for="it in uploadRows" :key="it.key" class="fs-queue__row">
          <span class="fs-queue__name" :title="it.rel">{{ it.rel }}</span>
          <ProgressBar
            class="fs-queue__prog"
            :value="uploadRatio(it)"
            :variant="uploadVariant(it)"
            :label="it.rel"
          />
          <span class="fs-queue__state" :class="`fs-queue__state--${it.status}`">
            {{ uploadStatusText(it) }}
          </span>
          <span class="fs-queue__num">{{ formatBytes(it.offset) }} / {{ formatBytes(it.size) }}</span>
          <button
            v-if="uploadCancellable(it)"
            class="fs-op"
            type="button"
            @click="session?.cancel(it.key)"
          >{{ t('remoteWorkspace.panel.cancel') }}</button>
          <button
            v-else-if="it.status === 'error' || it.status === 'canceled'"
            class="fs-op"
            type="button"
            @click="session?.retry(it.key)"
          >{{ t('remoteWorkspace.panel.uploadRetry') }}</button>
        </li>
        <li v-if="uploadHidden" class="fs-queue__more">
          {{ t('remoteWorkspace.panel.queueMore', { n: uploadHidden }) }}
        </li>
      </ul>
    </section>

    <!-- 传输记录:本次会话(同一标签页)的逐条传输,只读 —— 取消/重试在上面的队列里做。 -->
    <section v-if="txOpen" class="fs-queue fs-tx" :aria-label="t('remoteWorkspace.panel.txAria')">
      <header class="fs-queue__head">
        <span class="fs-queue__title">{{ t('remoteWorkspace.panel.txTitle', { n: transfers.length }) }}</span>
        <span v-if="txTally.failed" class="fs-queue__warn">
          {{ t('remoteWorkspace.panel.txFailed', { n: txTally.failed }) }}
        </span>
        <span class="fs-queue__bytes">
          {{ t('remoteWorkspace.panel.txCounts', { up: txTally.uploads, down: txTally.downloads }) }}
        </span>
        <span class="grow" />
        <button v-if="transfers.length" class="fs-btn" type="button" @click="commitTransfers([])">
          {{ t('remoteWorkspace.panel.txClear') }}
        </button>
        <button class="fs-btn" type="button" @click="txOpen = false">
          {{ t('remoteWorkspace.panel.txCollapse') }}
        </button>
      </header>

      <p v-if="!transfers.length" class="fs-tx__empty">{{ t('remoteWorkspace.panel.txEmpty') }}</p>
      <ul v-else class="fs-queue__list">
        <li v-for="r in txRows" :key="r.id" class="fs-queue__row">
          <span class="fs-tx__dir" :class="`fs-tx__dir--${r.direction}`" :title="r.direction === 'upload' ? t('remoteWorkspace.panel.txUpload') : t('remoteWorkspace.panel.txDownload')">
            {{ r.direction === 'upload' ? '↑' : '↓' }}
          </span>
          <span class="fs-queue__name" :title="r.path">{{ r.name }}</span>
          <ProgressBar
            v-if="r.direction === 'upload'"
            class="fs-queue__prog"
            :value="txRatio(r)"
            :variant="txVariant(r)"
            :label="r.name"
          />
          <span class="fs-queue__state" :class="`fs-queue__state--${r.status}`">{{ txStateText(r) }}</span>
          <span class="fs-queue__num">{{ txBytes(r) }}</span>
          <span class="fs-tx__at" :title="new Date(r.at).toLocaleString()">{{ formatClock(r.at) }}</span>
        </li>
        <li v-if="txHidden" class="fs-queue__more">
          {{ t('remoteWorkspace.panel.txMore', { n: txHidden }) }}
        </li>
        <li v-if="txTally.downloads" class="fs-queue__more">{{ t('remoteWorkspace.panel.txDownloadNote') }}</li>
      </ul>
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

.fs-bar {
  display: flex;
  align-items: center;
  gap: 4px;
  flex-shrink: 0;
}
/* 图标按钮:工具条要像 FinalShell 那样一行排开,文字标签留给 title/aria。 */
.fs-ibtn {
  flex-shrink: 0;
  width: 24px;
  height: 24px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  font: inherit;
  font-size: 13px;
  line-height: 1;
  color: var(--color-dim);
  background: transparent;
  border: 1px solid transparent;
  border-radius: var(--rounded-sm);
  cursor: pointer;
}
.fs-ibtn:hover:not(:disabled) {
  color: var(--color-text);
  background: var(--color-inset);
  border-color: var(--color-line);
}
.fs-ibtn:disabled {
  opacity: 0.45;
  cursor: default;
}
.fs-ibtn--on {
  color: var(--color-text);
  background: var(--color-inset);
  border-color: var(--color-line);
}
.fs-bar__sep {
  width: 1px;
  height: 16px;
  margin: 0 3px;
  flex-shrink: 0;
  background: var(--color-line);
}
.fs-badge {
  flex-shrink: 0;
  padding: 1px 6px;
  font-size: var(--text-label);
  color: var(--color-faint);
  border: 1px solid var(--color-line);
  border-radius: var(--rounded-sm);
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
/* 两栏:左目录树(宽度可拖,拖完记住),右列表/编辑器吃掉余下空间。 */
.fs-cols {
  display: flex;
  align-items: stretch;
  gap: 8px;
  flex: 1 1 auto;
  min-height: 0;
}
/* 拖动时整块不许选中文本:指针快速扫过目录名会糊出一片高亮。 */
.fs-cols--dragging {
  user-select: none;
}
/* 分隔条:视觉上只有一根发丝线,命中区靠负边距吃掉两侧栏间距撑到 17px ——
   1px 的线手指拖不中。 */
.fs-resizer {
  position: relative;
  z-index: 1;
  flex: 0 0 17px;
  margin: 0 -8px;
  display: flex;
  align-items: center;
  justify-content: center;
  cursor: col-resize;
}
.fs-resizer__grip {
  width: 1px;
  height: 26px;
  border-radius: 1px;
  background: var(--color-line-strong, #cbd5e1);
  opacity: 0.6;
  transition: height 0.12s ease, opacity 0.12s ease;
}
.fs-resizer:hover .fs-resizer__grip,
.fs-resizer:focus-visible .fs-resizer__grip {
  height: 100%;
  opacity: 1;
  background: var(--color-primary, var(--color-accent));
}
.fs-resizer:focus-visible {
  outline: 2px solid var(--color-accent, #7fe3f0);
  outline-offset: -2px;
}
.fs-tree {
  flex: 0 0 190px;
  min-width: 0;
  overflow: auto;
  padding: 2px 0;
  background: var(--color-inset);
  border: 1px solid var(--color-line);
  border-radius: var(--rounded-sm);
}
.fs-tree__row {
  display: flex;
  align-items: center;
  /* 每深一层缩进一档;CSS 变量由 :style 给成字符串,故用 calc 兜住单位。 */
  padding-left: calc(2px + var(--depth, 0) * 12px);
  padding-right: 4px;
}
.fs-tree__twisty {
  flex-shrink: 0;
  width: 16px;
  height: 20px;
  font: inherit;
  font-size: 10px;
  color: var(--color-faint);
  background: none;
  border: 0;
  cursor: pointer;
}
.fs-tree__twisty:disabled {
  opacity: 0.5;
  cursor: default;
}
.fs-tree__name {
  display: flex;
  align-items: center;
  gap: 5px;
  flex: 1 1 auto;
  min-width: 0;
  height: 22px;
  font: inherit;
  font-size: var(--text-label);
  color: var(--color-text);
  background: none;
  border: 0;
  border-radius: var(--rounded-sm);
  padding: 0 4px;
  cursor: pointer;
  text-align: left;
}
.fs-tree__name:hover {
  background: var(--color-card-2, var(--color-inset));
}
.fs-tree__name--here {
  background: var(--color-primary-soft, var(--color-inset));
  box-shadow: inset 2px 0 0 var(--color-primary, var(--color-accent));
}
.fs-tree__label {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 图标:目录用品牌蓝的文件夹,文件用中性文档,链接描一下边以示区别。 */
.fs-ico {
  flex-shrink: 0;
  width: 14px;
  height: 14px;
  fill: var(--color-faint);
}
.fs-ico--dir {
  fill: var(--color-primary, var(--color-accent));
}
.fs-ico--link {
  fill: none;
  stroke: var(--color-faint);
  stroke-width: 1.2;
}

/* 工具条里的描线图标:跟 .fs-ibtn 一起变亮/变灰,免得每个图标各配一套颜色。 */
.fs-ibtn-ico {
  width: 14px;
  height: 14px;
  fill: none;
  stroke: currentColor;
  stroke-width: 1.3;
  stroke-linecap: round;
  stroke-linejoin: round;
}

/* ─── 上传队列 ─── 面板底部的固定条:总进度一行,明细逐行可滚 */
.fs-queue {
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 6px 8px;
  border: 1px solid var(--color-line);
  border-radius: var(--rounded-sm);
  background: var(--color-inset);
}
.fs-queue__head {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: var(--text-label);
  color: var(--color-dim);
}
.fs-queue__title {
  flex-shrink: 0;
  color: var(--color-text);
}
.fs-queue__warn {
  flex-shrink: 0;
  color: var(--color-amber, var(--color-dim));
}
.fs-queue__bar {
  flex: 0 1 180px;
  min-width: 60px;
}
.fs-queue__bytes {
  flex-shrink: 0;
  font-family: var(--font-mono, ui-monospace, monospace);
  color: var(--color-faint);
  white-space: nowrap;
}
.fs-queue__list {
  margin: 0;
  padding: 0;
  list-style: none;
  /* 明细要有界:选一个上千文件的目录时,列表本身不能把面板顶到看不见终端。 */
  max-height: 140px;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.fs-queue__row {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: var(--text-label);
  color: var(--color-dim);
}
.fs-queue__name {
  flex: 0 1 40%;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--color-text);
}
.fs-queue__prog {
  flex: 1 1 80px;
  min-width: 48px;
}
.fs-queue__state {
  flex-shrink: 0;
  width: 120px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.fs-queue__state--error {
  color: var(--color-red, var(--color-dim));
}
.fs-queue__state--canceled {
  color: var(--color-amber, var(--color-dim));
}
.fs-queue__state--done {
  color: var(--color-green, var(--color-dim));
}
.fs-queue__num {
  flex-shrink: 0;
  font-family: var(--font-mono, ui-monospace, monospace);
  color: var(--color-faint);
  white-space: nowrap;
}
.fs-queue__more {
  font-size: var(--text-label);
  color: var(--color-faint);
  padding-left: 2px;
}

/* 传输记录:沿用队列的容器与行版式,只加方向、时刻、空态这三样自己的东西。 */
.fs-tx__dir {
  flex-shrink: 0;
  width: 14px;
  text-align: center;
  font-family: var(--font-mono, ui-monospace, monospace);
  color: var(--color-faint);
}
.fs-tx__dir--upload {
  color: var(--color-green, var(--color-dim));
}
.fs-tx__dir--download {
  color: var(--color-primary, var(--color-dim));
}
.fs-tx__at {
  flex-shrink: 0;
  font-family: var(--font-mono, ui-monospace, monospace);
  font-size: var(--text-label);
  color: var(--color-faint);
}
.fs-tx__empty {
  margin: 0;
  padding: 2px;
  font-size: var(--text-label);
  color: var(--color-faint);
}

.fs-editor,
.fs-list {
  flex: 1 1 auto;
  min-width: 0;
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
/* 隔行条纹:一屏几十条目,没有条纹时眼睛会串行(和 FinalShell 同一手法)。 */
.fs-table tbody tr:nth-child(even) {
  background: var(--color-inset);
}
.col-name {
  min-width: 0;
}
.col-mode,
.col-size,
.col-type,
.col-mtime {
  color: var(--color-dim);
  white-space: nowrap;
}
.col-mode,
.col-mtime,
.col-size {
  font-family: var(--font-mono, ui-monospace, monospace);
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
.fs-name:hover span {
  text-decoration: underline;
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
