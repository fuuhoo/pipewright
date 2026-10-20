<script setup lang="ts">
/*
  Certs.vue — 证书管理(申请 / 监控 / 发布)的一级页。

  三条线各一段:
  - 申请:登记一张待签证书(选 CA + DNS 提供商 → DNS-01 自签),或粘贴已有证书导入;
  - 监控:「立即检查」当场扫一遍(窗口内的 acme 证书真的会续期并铺发),需要人接手的排在最前;
  - 发布:逐落点(主机 + 两条路径 + 用户自填的 reload 指令)SSH 落盘。

  两条权限轴(与后端 internal/httpapi/certs.go 同源):读要 cert.view,写要 cert.manage;
  新增/改落点与发布还要那台主机的操作权,前端只按功能点亮灭按钮,数据轴交给后端拒(403 说人话)。
  私钥永不进列表与详情(AC-SEC-01),唯一出口是「下载证书包」。
*/
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { NIcon } from 'naive-ui'
import {
  AlertTriangle,
  Certificate,
  CircleCheck,
  CircleX,
  Download,
  Id,
  Pencil,
  Plus,
  Refresh,
  Rocket,
  ShieldCheck,
  Trash,
  Upload,
} from '@vicons/tabler'
import {
  addCertTarget,
  checkCerts,
  createCert,
  deleteCert,
  deleteCertTarget,
  deployCert,
  getCert,
  getCertBundle,
  importCert,
  listAcmeAccounts,
  listCerts,
  listCas,
  listCertTargets,
  registerAcmeAccount,
  replaceCert,
  setAutoRenew,
  updateCertTarget,
  issueCert,
  type AcmeAccount,
  type CaOption,
  type Cert,
  type CertSource,
  type CertStatus,
  type CertTarget,
  type CertTargetInput,
  type CheckReport,
  type DeployReport,
} from '../api/certs'
import { HttpError } from '../api/http'
import { listDnsProviders, type DnsProvider } from '../api/dnsProviders'
import { listServers, type Server } from '../api/servers'
import { useToast } from '../composables/useToast'
import { useConfirm } from '../composables/useConfirm'
import { useSessionStore } from '../stores/session'
import {
  CHECK_BUCKETS,
  RENEW_WINDOW_DAYS,
  canIssue,
  certTone,
  checkBucketCount,
  checkReportClean,
  checkTargetPaths,
  filterAttentions,
  filterCerts,
  invalidDomains,
  needsAttention,
  parseDomains,
  type CheckBucket,
} from '../lib/certRows'
import type { CertTone } from '../lib/certRows'

const { t, locale } = useI18n()
const toast = useToast()
const confirm = useConfirm()
const session = useSessionStore()

const canManage = computed(() => session.canPerm('cert.manage'))

type LoadState = 'idle' | 'loading' | 'error'
const loadState = ref<LoadState>('loading')
const loadError = ref('')
const certs = ref<Cert[]>([])

/** 把后端契约错误说成一句人话;没有 message 时兜底那句。 */
function errText(err: unknown, fallback: string): string {
  return err instanceof HttpError ? (err.apiError?.message ?? fallback) : fallback
}

async function load(): Promise<void> {
  loadState.value = 'loading'
  loadError.value = ''
  try {
    certs.value = await listCerts()
    loadState.value = 'idle'
  } catch (err) {
    loadError.value = errText(err, t('certs.errLoad'))
    loadState.value = 'error'
  }
}

onMounted(load)

/** 就地替换某一行的数据(签发 / 续期开关这些写响应都会带回新行)。 */
function upsertRow(c: Cert): void {
  const i = certs.value.findIndex((x) => x.id === c.id)
  if (i >= 0) certs.value.splice(i, 1, c)
  else certs.value.unshift(c)
}

// ─── 标签与配色 ────────────────────────────────────────────────────────────────
const statusLabels = computed<Record<CertStatus, string>>(() => ({
  pending: t('certs.statusPending'),
  issued: t('certs.statusIssued'),
  failed: t('certs.statusFailed'),
  expired: t('certs.statusExpired'),
}))
const sourceLabels = computed<Record<CertSource, string>>(() => ({
  acme: t('certs.sourceAcme'),
  upload: t('certs.sourceUpload'),
}))

/** 状态徽章:配色由 lib 判,文案在这里拼(与 containerState 同一分工)。 */
function statusBadge(c: Cert): { label: string; tone: CertTone } {
  return { label: statusLabels.value[c.status] ?? c.status, tone: certTone(c.status, c.daysLeft) }
}

/** 为什么这张要人接手:一句话给出能接着行动的理由。 */
function attentionReason(c: Cert): string {
  if (c.status === 'expired') return t('certs.attentionExpired')
  if (c.source === 'upload') return t('certs.attentionUpload')
  return t('certs.attentionNoAutoRenew')
}

const TONE_CLASS: Record<CertTone, string> = {
  green: 'st--green',
  amber: 'st--amber',
  red: 'st--red',
  cyan: 'st--cyan',
  faint: 'st--faint',
}

/** 到期那一列:没有正文说「还没签出来」,过期说成「已过期 N 天」。 */
function expiryText(c: Cert): string {
  if (!c.notAfter) return t('certs.notIssued')
  if (c.daysLeft < 0) return t('certs.daysOver', { n: -c.daysLeft })
  return t('certs.daysLeft', { n: c.daysLeft })
}

function fmtDateTime(ts: string): string {
  if (!ts) return '—'
  const d = new Date(ts)
  return Number.isNaN(d.getTime()) ? ts : d.toLocaleString(locale.value, { hour12: false })
}

function fmtDate(ts: string): string {
  if (!ts) return '—'
  const d = new Date(ts)
  return Number.isNaN(d.getTime()) ? ts : d.toLocaleDateString(locale.value)
}

// ─── 列表筛选 ──────────────────────────────────────────────────────────────────
const query = ref('')
const onlyAttention = ref(false)
const attentionCount = computed(() => filterAttentions(certs.value).length)
const visibleCerts = computed(() => {
  const rows = filterCerts(certs.value, query.value)
  return onlyAttention.value ? filterAttentions(rows) : rows
})

// ─── 逐行忙碌态 ────────────────────────────────────────────────────────────────
type BusyKind = 'issue' | 'deploy' | 'autoRenew' | 'delete'
const busy = ref<{ id: string; kind: BusyKind } | null>(null)
function isBusy(id: string, kind: BusyKind): boolean {
  return busy.value?.id === id && busy.value?.kind === kind
}
const anyBusy = computed(() => busy.value !== null)

async function runIssue(c: Cert): Promise<void> {
  if (anyBusy.value) return
  busy.value = { id: c.id, kind: 'issue' }
  try {
    const next = await issueCert(c.id)
    upsertRow(next)
    if (next.status === 'issued') toast.success(t('certs.issueOk'), { detail: next.domain })
    else toast.warn(t('certs.issuePending'), { detail: next.lastError || next.domain })
  } catch (err) {
    toast.error(t('certs.issueFail'), { detail: errText(err, t('certs.issueFailDetail')) })
  } finally {
    busy.value = null
  }
}

async function runDeploy(c: Cert): Promise<void> {
  if (anyBusy.value) return
  busy.value = { id: c.id, kind: 'deploy' }
  try {
    const rep: DeployReport = await deployCert(c.id)
    if (rep.failed === 0 && rep.deployed > 0) {
      toast.success(t('certs.deployOk', { n: rep.deployed }), { detail: c.domain })
    } else if (rep.deployed > 0) {
      toast.warn(t('certs.deployPartial', { ok: rep.deployed, bad: rep.failed }), {
        detail: rep.results.find((r) => !r.ok)?.error ?? c.domain,
      })
    } else {
      toast.error(t('certs.deployNone'), { detail: rep.results[0]?.error ?? t('certs.deployNoTarget') })
    }
  } catch (err) {
    toast.error(t('certs.deployFail'), { detail: errText(err, t('certs.deployFailDetail')) })
  } finally {
    busy.value = null
  }
}

async function toggleAutoRenew(c: Cert): Promise<void> {
  if (anyBusy.value) return
  busy.value = { id: c.id, kind: 'autoRenew' }
  try {
    upsertRow(await setAutoRenew(c.id, !c.autoRenew))
  } catch (err) {
    toast.error(t('certs.errSave'), { detail: errText(err, t('certs.errSaveRetry')) })
  } finally {
    busy.value = null
  }
}

async function removeCert(c: Cert): Promise<void> {
  const ok = await confirm.open({
    title: t('certs.deleteTitle'),
    body: t('certs.deleteBody', { domain: c.domain }),
    confirmLabel: t('certs.deleteConfirm'),
    variant: 'danger',
  })
  if (!ok) return
  busy.value = { id: c.id, kind: 'delete' }
  try {
    await deleteCert(c.id)
    certs.value = certs.value.filter((x) => x.id !== c.id)
    if (detail.value?.id === c.id) closeDetail()
    toast.success(t('certs.deleted'), { detail: c.domain })
  } catch (err) {
    toast.error(t('certs.errDelete'), { detail: errText(err, t('certs.errDeleteRetry')) })
  } finally {
    busy.value = null
  }
}

// ─── 立即检查 ──────────────────────────────────────────────────────────────────
const checking = ref(false)
const checkError = ref('')
const report = ref<CheckReport | null>(null)

async function runCheck(): Promise<void> {
  if (checking.value) return
  checking.value = true
  checkError.value = ''
  try {
    report.value = await checkCerts()
    // 这一轮真的会改数据(续期 + 铺发),所以扫完必须重拉一遍列表。
    certs.value = await listCerts()
    if (checkReportClean(report.value)) toast.info(t('certs.checkClean', { n: report.value.checked }))
  } catch (err) {
    checkError.value = errText(err, t('certs.checkFail'))
    toast.error(t('certs.checkFail'), { detail: checkError.value })
  } finally {
    checking.value = false
  }
}

function dismissReport(): void {
  report.value = null
  checkError.value = ''
}

/** 三桶标题写成三个静态键(动态拼键 i18n 抽取扫不到,漂移了也没人报错)。 */
function reportBucketLabel(bucket: CheckBucket, n: number): string {
  if (bucket === 'expiring') return t('certs.reportExpiring', { n })
  if (bucket === 'renewed') return t('certs.reportRenewed', { n })
  return t('certs.reportFailed', { n })
}

// ─── 弹窗壳 ────────────────────────────────────────────────────────────────────
type ModalKind = '' | 'create' | 'import' | 'replace' | 'detail' | 'accounts'
const modal = ref<ModalKind>('')

// 登记表单(source=acme:平台自签)
const createBusy = ref(false)
const createBanner = ref('')
const createForm = ref({ domain: '', sans: '', ca: '', dnsProviderId: '', autoRenew: true })
const createErrors = ref({ domain: '', sans: '', ca: '', dnsProviderId: '' })
const cas = ref<CaOption[]>([])
const dnsProviders = ref<DnsProvider[]>([])

/** 下拉里的 CA:测试环境标注出来,免得有人拿它签生产域名。 */
const caOptions = computed(() =>
  cas.value.map((c) => ({
    value: c.id,
    label: c.staging ? `${c.name} · ${t('certs.caStaging')}` : c.name,
    requiresEab: c.requiresEab,
  })),
)
const selectedCa = computed(() => cas.value.find((c) => c.id === createForm.value.ca) ?? null)

async function openCreate(): Promise<void> {
  createForm.value = { domain: '', sans: '', ca: cas.value[0]?.id ?? '', dnsProviderId: '', autoRenew: true }
  createErrors.value = { domain: '', sans: '', ca: '', dnsProviderId: '' }
  createBanner.value = ''
  modal.value = 'create'
  try {
    if (cas.value.length === 0) cas.value = await listCas()
    if (dnsProviders.value.length === 0) dnsProviders.value = await listDnsProviders()
    if (!createForm.value.ca) createForm.value.ca = cas.value[0]?.id ?? ''
  } catch (err) {
    createBanner.value = errText(err, t('certs.errLoadChoices'))
  }
}

function validateCreate(): boolean {
  const e = { domain: '', sans: '', ca: '', dnsProviderId: '' }
  const domain = createForm.value.domain.trim().toLowerCase()
  if (!domain) e.domain = t('certs.valDomainRequired')
  else if (invalidDomains([domain]).length) e.domain = t('certs.valDomainInvalid')
  const bad = invalidDomains(parseDomains(createForm.value.sans))
  if (bad.length) e.sans = t('certs.valSansInvalid', { names: bad.join('、') })
  if (!createForm.value.ca) e.ca = t('certs.valCaRequired')
  if (!createForm.value.dnsProviderId) e.dnsProviderId = t('certs.valDnsRequired')
  createErrors.value = e
  return !e.domain && !e.sans && !e.ca && !e.dnsProviderId
}

async function submitCreate(): Promise<void> {
  if (createBusy.value || !validateCreate()) return
  createBusy.value = true
  createBanner.value = ''
  try {
    const c = await createCert({
      domain: createForm.value.domain.trim().toLowerCase(),
      sans: parseDomains(createForm.value.sans),
      ca: createForm.value.ca,
      dnsProviderId: createForm.value.dnsProviderId,
      autoRenew: createForm.value.autoRenew,
    })
    upsertRow(c)
    modal.value = ''
    toast.success(t('certs.created'), { detail: t('certs.createdDetail', { domain: c.domain }) })
  } catch (err) {
    createBanner.value = errText(err, t('certs.errSave'))
  } finally {
    createBusy.value = false
  }
}

// 导入 / 替换正文:同一份载荷,两种说法
const pemBusy = ref(false)
const pemBanner = ref('')
const pemForm = ref({ domain: '', certPem: '', keyPem: '', chainPem: '', dnsProviderId: '', autoRenew: false })
const pemTarget = ref<Cert | null>(null)

function openImport(): void {
  pemTarget.value = null
  pemForm.value = { domain: '', certPem: '', keyPem: '', chainPem: '', dnsProviderId: '', autoRenew: false }
  pemBanner.value = ''
  modal.value = 'import'
}

async function openReplace(c: Cert): Promise<void> {
  pemTarget.value = c
  pemForm.value = {
    domain: c.domain,
    certPem: '',
    keyPem: '',
    chainPem: '',
    dnsProviderId: c.dnsProviderId,
    autoRenew: false,
  }
  pemBanner.value = ''
  modal.value = 'replace'
  try {
    if (dnsProviders.value.length === 0) dnsProviders.value = await listDnsProviders()
  } catch {
    // 这里只是给「以后想改回平台签发」留个可选项,拉不到不影响本次替换。
  }
}

async function submitPem(): Promise<void> {
  if (pemBusy.value) return
  const f = pemForm.value
  if (!f.certPem.trim() || !f.keyPem.trim()) {
    pemBanner.value = t('certs.valPemRequired')
    return
  }
  pemBusy.value = true
  pemBanner.value = ''
  const input = {
    domain: f.domain.trim().toLowerCase(),
    certPem: f.certPem,
    keyPem: f.keyPem,
    chainPem: f.chainPem,
    dnsProviderId: f.dnsProviderId,
    autoRenew: f.autoRenew,
  }
  try {
    const next = pemTarget.value ? await replaceCert(pemTarget.value.id, input) : await importCert(input)
    upsertRow(next)
    modal.value = ''
    toast.success(pemTarget.value ? t('certs.replaced') : t('certs.imported'), { detail: next.domain })
  } catch (err) {
    pemBanner.value = errText(err, t('certs.errSave'))
  } finally {
    pemBusy.value = false
  }
}

// ─── 详情弹窗(正文 + 落点)────────────────────────────────────────────────────
const detail = ref<Cert | null>(null)
const detailTargets = ref<CertTarget[]>([])
const detailBusy = ref(false)
const detailError = ref('')
const showPem = ref(false)

async function openDetail(c: Cert): Promise<void> {
  detail.value = c
  detailTargets.value = []
  detailError.value = ''
  showPem.value = false
  modal.value = 'detail'
  detailBusy.value = true
  try {
    const [full, targets] = await Promise.all([getCert(c.id), listCertTargets(c.id)])
    detail.value = full
    detailTargets.value = targets
  } catch (err) {
    detailError.value = errText(err, t('certs.errLoadDetail'))
  } finally {
    detailBusy.value = false
  }
}

function closeDetail(): void {
  if (detailBusy.value || targetBusy.value) return
  modal.value = ''
  detail.value = null
  targetFormOpen.value = false
}

const servers = ref<Server[]>([])

const serverName = (id: string): string => servers.value.find((s) => s.id === id)?.name ?? id

/** 落点最近一次结果的说法:空串 = 从没发过。 */
function targetResult(t2: CertTarget): { ok: boolean; label: string } {
  if (t2.lastStatus === '') return { ok: false, label: t('certs.neverDeployed') }
  if (t2.lastStatus === 'success') return { ok: true, label: t('certs.deployedAt', { at: fmtDateTime(t2.deployedAt) }) }
  return { ok: false, label: t('certs.deployFailedAt', { at: fmtDateTime(t2.deployedAt) }) }
}

// 落点子表单
const targetFormOpen = ref(false)
const targetBusy = ref(false)
const targetBanner = ref('')
const targetErrors = ref({ serverId: '', certPath: '', keyPath: '' })
const editingTarget = ref<CertTarget | null>(null)
const targetForm = ref<CertTargetInput>({ serverId: '', certPath: '', keyPath: '', postCommand: '', autoDeploy: false })

async function openTargetForm(t2?: CertTarget): Promise<void> {
  editingTarget.value = t2 ?? null
  targetForm.value = t2
    ? { serverId: t2.serverId, certPath: t2.certPath, keyPath: t2.keyPath, postCommand: t2.postCommand, autoDeploy: t2.autoDeploy }
    : { serverId: servers.value[0]?.id ?? '', certPath: '', keyPath: '', postCommand: '', autoDeploy: false }
  targetErrors.value = { serverId: '', certPath: '', keyPath: '' }
  targetBanner.value = ''
  targetFormOpen.value = true
  if (servers.value.length === 0) {
    try {
      servers.value = await listServers()
      if (!targetForm.value.serverId) targetForm.value.serverId = servers.value[0]?.id ?? ''
    } catch (err) {
      targetBanner.value = errText(err, t('certs.errLoadServers'))
    }
  }
}

function closeTargetForm(): void {
  targetFormOpen.value = false
  editingTarget.value = null
}

function validateTarget(): boolean {
  const e = { serverId: '', certPath: '', keyPath: '' }
  if (!targetForm.value.serverId) e.serverId = t('certs.valServerRequired')
  const issue = checkTargetPaths(targetForm.value.certPath, targetForm.value.keyPath)
  if (issue === 'notAbsolute') {
    e.certPath = t('certs.valPathNotAbsolute')
    e.keyPath = t('certs.valPathNotAbsolute')
  } else if (issue === 'same') {
    e.keyPath = t('certs.valPathSame')
  }
  targetErrors.value = e
  return !e.serverId && !e.certPath && !e.keyPath
}

async function submitTarget(): Promise<void> {
  if (!detail.value || targetBusy.value || !validateTarget()) return
  targetBusy.value = true
  targetBanner.value = ''
  try {
    if (editingTarget.value) {
      const next = await updateCertTarget(detail.value.id, editingTarget.value.id, targetForm.value)
      detailTargets.value = detailTargets.value.map((x) => (x.id === next.id ? next : x))
    } else {
      detailTargets.value = [...detailTargets.value, await addCertTarget(detail.value.id, targetForm.value)]
    }
    closeTargetForm()
  } catch (err) {
    targetBanner.value = errText(err, t('certs.errTargetSave'))
  } finally {
    targetBusy.value = false
  }
}

async function removeTarget(t2: CertTarget): Promise<void> {
  if (!detail.value || targetBusy.value) return
  const ok = await confirm.open({
    title: t('certs.deleteTargetTitle'),
    body: t('certs.deleteTargetBody', { path: t2.certPath }),
    confirmLabel: t('certs.deleteConfirm'),
    variant: 'danger',
  })
  if (!ok) return
  targetBusy.value = true
  try {
    await deleteCertTarget(detail.value.id, t2.id)
    detailTargets.value = detailTargets.value.filter((x) => x.id !== t2.id)
  } catch (err) {
    targetBanner.value = errText(err, t('certs.errTargetDelete'))
  } finally {
    targetBusy.value = false
  }
}

/** 私钥的唯一出口:拉证书包后落两个文件。内容只在内存里,不进任何日志。 */
const bundleBusy = ref(false)
async function downloadBundle(): Promise<void> {
  if (!detail.value || bundleBusy.value) return
  bundleBusy.value = true
  try {
    const b = await getCertBundle(detail.value.id)
    saveText(`${b.domain}-fullchain.pem`, b.fullchain || `${b.certPem}${b.chainPem}`)
    saveText(`${b.domain}.key`, b.privateKey)
    toast.success(t('certs.bundleSaved'), { detail: t('certs.bundleSavedDetail') })
  } catch (err) {
    toast.error(t('certs.errBundle'), { detail: errText(err, t('certs.errBundleRetry')) })
  } finally {
    bundleBusy.value = false
  }
}

function saveText(filename: string, content: string): void {
  const blob = new Blob([content], { type: 'application/x-pem-file' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  // 立刻 revoke 会把下载掐断,留一个短窗口让浏览器把字节取走。
  setTimeout(() => URL.revokeObjectURL(url), 30_000)
}

// ─── ACME 账户弹窗 ─────────────────────────────────────────────────────────────
const accounts = ref<AcmeAccount[]>([])
const accountsBusy = ref(false)
const accountsError = ref('')
const regOpen = ref(false)
const regBusy = ref(false)
const regBanner = ref('')
const CONTACT_PLACEHOLDER = 'cert-admin@example.com'
const regForm = ref({ ca: '', contact: '', eabKeyId: '', eabKey: '' })

async function openAccounts(): Promise<void> {
  modal.value = 'accounts'
  regOpen.value = false
  await loadAccounts()
}

async function loadAccounts(): Promise<void> {
  accountsBusy.value = true
  accountsError.value = ''
  try {
    accounts.value = await listAcmeAccounts()
    if (cas.value.length === 0) cas.value = await listCas()
  } catch (err) {
    accountsError.value = errText(err, t('certs.errLoadAccounts'))
  } finally {
    accountsBusy.value = false
  }
}

const regCa = computed(() => cas.value.find((c) => c.id === regForm.value.ca) ?? null)

function openRegister(): void {
  regForm.value = { ca: cas.value[0]?.id ?? '', contact: '', eabKeyId: '', eabKey: '' }
  regBanner.value = ''
  regOpen.value = true
}

async function submitRegister(): Promise<void> {
  if (regBusy.value) return
  if (!regForm.value.ca) {
    regBanner.value = t('certs.valCaRequired')
    return
  }
  if (regCa.value?.requiresEab && !regForm.value.eabKeyId.trim()) {
    regBanner.value = t('certs.valEabRequired')
    return
  }
  regBusy.value = true
  regBanner.value = ''
  try {
    const a = await registerAcmeAccount({
      ca: regForm.value.ca,
      contact: regForm.value.contact.trim(),
      eabKeyId: regForm.value.eabKeyId.trim(),
      eabKey: regForm.value.eabKey,
    })
    const i = accounts.value.findIndex((x) => x.id === a.id)
    if (i >= 0) accounts.value.splice(i, 1, a)
    else accounts.value.push(a)
    regOpen.value = false
    toast.success(t('certs.accountRegistered'), { detail: a.ca })
  } catch (err) {
    regBanner.value = errText(err, t('certs.errRegister'))
  } finally {
    regBusy.value = false
  }
}

/** 某家 CA 还没注册账户时,登记处给一句提示(不拦提交:后端会回 422 说清)。 */
const hasAccountForCa = computed(() => {
  const set = new Set(accounts.value.map((a) => a.ca))
  return (caId: string): boolean => set.has(caId)
})
</script>

<template>
  <div class="certs-root">
    <div class="section-head">
      <div class="section-head-text">
        <h2 class="section-title">{{ t('certs.title') }}</h2>
        <p class="section-desc">{{ t('certs.desc') }}</p>
      </div>
      <div class="head-actions">
        <button class="btn-secondary" :disabled="!canManage || checking" :title="canManage ? t('certs.checkHint') : t('certs.needManageTip')" @click="runCheck">
          <span v-if="checking" class="spinner spinner--dim" aria-hidden="true" />
          <NIcon v-else :size="14"><ShieldCheck /></NIcon>
          {{ checking ? t('certs.checking') : t('certs.btnCheck') }}
        </button>
        <button class="btn-secondary" @click="openAccounts">{{ t('certs.btnAccounts') }}</button>
        <button class="btn-secondary" :disabled="!canManage" :title="canManage ? '' : t('certs.needManageTip')" @click="openImport">
          <NIcon :size="14"><Upload /></NIcon>
          {{ t('certs.btnImport') }}
        </button>
        <button class="btn-primary" :disabled="!canManage" :title="canManage ? '' : t('certs.needManageTip')" @click="openCreate">
          <NIcon :size="14"><Plus /></NIcon>
          {{ t('certs.btnCreate') }}
        </button>
      </div>
    </div>

    <!-- 检查汇总:哪些是平台替你续的、哪些还得你自己动手 -->
    <div v-if="report" class="panel report">
      <div class="panel-head">
        <span>{{ t('certs.reportTitle', { n: report.checked }) }}</span>
        <button class="report-close" @click="dismissReport">{{ t('common.close') }}</button>
      </div>
      <div v-if="checkReportClean(report)" class="state-msg">{{ t('certs.reportClean') }}</div>
      <div v-else class="report-body">
        <section v-for="bucket in CHECK_BUCKETS" :key="bucket" class="report-group">
          <h3 class="report-group-title" :class="`report-group-title--${bucket}`">
            {{ reportBucketLabel(bucket, checkBucketCount(report, bucket)) }}
          </h3>
          <ul v-if="report[bucket].length" class="report-list">
            <li v-for="item in report[bucket]" :key="item.certId + item.domain" class="report-item">
              <strong class="report-domain">{{ item.domain }}</strong>
              <span class="report-detail">{{ item.detail }}</span>
              <span v-if="item.deploy" class="report-deploy">{{ item.deploy }}</span>
            </li>
          </ul>
        </section>
      </div>
    </div>

    <div v-if="loadState === 'error'" class="banner banner--error" role="alert">
      <span>⚠ {{ loadError }}</span>
      <button class="banner-retry" @click="load">↻ {{ t('common.refresh') }}</button>
    </div>

    <div class="toolbar">
      <input
        v-model="query"
        class="field-input search"
        type="search"
        :placeholder="t('certs.searchPlaceholder')"
        :aria-label="t('certs.searchPlaceholder')"
      />
      <label class="switch">
        <input v-model="onlyAttention" type="checkbox" />
        <span>{{ t('certs.onlyAttention', { n: attentionCount }) }}</span>
      </label>
      <button class="btn-secondary btn-refresh" :disabled="loadState === 'loading'" @click="load">
        <NIcon :size="13"><Refresh /></NIcon>
        {{ t('common.refresh') }}
      </button>
    </div>

    <div class="panel">
      <div class="panel-head">
        <span>{{ t('certs.panelTitle') }}</span>
        <span v-if="loadState === 'idle'" class="panel-meta">
          {{ t('certs.countLabel', { n: visibleCerts.length, total: certs.length }) }}
        </span>
      </div>

      <div v-if="loadState === 'loading'" class="state-msg">{{ t('common.refresh') }}…</div>

      <div v-else-if="loadState === 'idle' && certs.length === 0" class="empty-state">
        <div class="empty-icon" aria-hidden="true"><NIcon :size="22"><Certificate /></NIcon></div>
        <p class="empty-label">{{ t('certs.emptyLabel') }}</p>
        <p class="empty-hint">{{ t('certs.emptyHint', { days: RENEW_WINDOW_DAYS }) }}</p>
        <button v-if="canManage" class="btn-primary" @click="openCreate">+ {{ t('certs.btnCreate') }}</button>
      </div>

      <div v-else-if="visibleCerts.length === 0" class="state-msg">{{ t('certs.noMatch') }}</div>

      <template v-else-if="loadState === 'idle'">
        <div class="cert-row cert-row--head" aria-hidden="true">
          <span>{{ t('certs.colDomain') }}</span>
          <span>{{ t('certs.colSource') }}</span>
          <span>{{ t('certs.colStatus') }}</span>
          <span>{{ t('certs.colExpiry') }}</span>
          <span>{{ t('certs.colAutoRenew') }}</span>
          <span>{{ t('certs.colChecked') }}</span>
          <span />
        </div>
        <div v-for="c in visibleCerts" :key="c.id" class="cert-row">
          <span class="cert-domain">
            <strong>{{ c.domain }}</strong>
            <span v-if="c.sans && c.sans.length > 1" class="cert-sans" :title="c.sans.join('、')">
              +{{ c.sans.length - 1 }}
            </span>
            <NIcon
              v-if="needsAttention(c)"
              :size="13"
              class="cert-attention"
              :title="attentionReason(c)"
              :aria-label="attentionReason(c)"
            ><AlertTriangle /></NIcon>
          </span>
          <span class="cert-src">
            {{ sourceLabels[c.source] ?? c.source }}
            <span v-if="c.source === 'acme' && c.ca" class="cert-ca">{{ c.ca }}</span>
          </span>
          <span class="status-badge" :class="TONE_CLASS[statusBadge(c).tone]">
            {{ statusBadge(c).label }}
          </span>
          <span class="cert-expire">
            <span class="cert-expire-days">{{ expiryText(c) }}</span>
            <span v-if="c.notAfter" class="cert-expire-date mono">{{ fmtDate(c.notAfter) }}</span>
            <span v-else class="cert-expire-date">{{ t('certs.notIssued') }}</span>
          </span>
          <span class="cert-auto">
            <button
              class="toggle"
              :class="{ 'toggle--on': c.autoRenew }"
              :disabled="!canManage || anyBusy || !canIssue(c.source)"
              :title="!canManage ? t('certs.needManageTip') : canIssue(c.source) ? t('certs.autoRenewHint') : t('certs.autoRenewUploadHint')"
              role="switch"
              :aria-checked="c.autoRenew"
              @click="toggleAutoRenew(c)"
            >
              <span class="toggle-knob" aria-hidden="true" />
            </button>
          </span>
          <span class="cert-checked mono" :title="t('certs.checkedHint')">{{ c.checkedAt ? fmtDateTime(c.checkedAt) : '—' }}</span>
          <span class="cert-ops">
            <button
              v-if="canManage && canIssue(c.source)"
              class="op-btn"
              :disabled="anyBusy"
              :title="c.notAfter ? t('certs.renewHint') : t('certs.issueHint')"
              @click="runIssue(c)"
            >
              <span v-if="isBusy(c.id, 'issue')" class="spinner spinner--dim" aria-hidden="true" />
              {{ isBusy(c.id, 'issue') ? t('certs.working') : c.notAfter ? t('certs.opRenew') : t('certs.opIssue') }}
            </button>
            <button
              v-if="canManage && c.hasKey"
              class="op-btn"
              :disabled="anyBusy"
              :title="t('certs.deployHint')"
              @click="runDeploy(c)"
            >
              <span v-if="isBusy(c.id, 'deploy')" class="spinner spinner--dim" aria-hidden="true" />
              <NIcon v-else :size="12"><Rocket /></NIcon>
              {{ isBusy(c.id, 'deploy') ? t('certs.working') : t('certs.opDeploy') }}
            </button>
            <button class="op-btn" :title="t('certs.detailHint')" @click="openDetail(c)">
              <NIcon :size="12"><Pencil /></NIcon>
              {{ t('certs.opDetail') }}
            </button>
            <button
              v-if="canManage"
              class="op-btn op-btn--danger"
              :disabled="anyBusy"
              :title="t('certs.deleteHint')"
              :aria-label="t('certs.deleteHint')"
              @click="removeCert(c)"
            >
              <NIcon :size="12"><Trash /></NIcon>
            </button>
          </span>
        </div>
      </template>
    </div>
  </div>

  <!-- 登记一张待签证书 -->
  <Teleport to="body">
    <div
      v-if="modal === 'create'"
      class="modal-scrim"
      role="dialog"
      :aria-label="t('certs.createTitle')"
      aria-modal="true"
      @keydown.esc="createBusy || (modal = '')"
      @click.self="!createBusy && (modal = '')"
    >
      <div class="modal">
        <div class="modal-head">
          <div class="modal-icon" aria-hidden="true"><NIcon :size="18"><Certificate /></NIcon></div>
          <div>
            <h3 class="modal-title">{{ t('certs.createTitle') }}</h3>
            <p class="modal-sub">{{ t('certs.createSub') }}</p>
          </div>
          <button class="modal-close" :aria-label="t('certs.closeDialog')" :disabled="createBusy" @click="modal = ''">
            <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M18 6 6 18M6 6l12 12"/></svg>
          </button>
        </div>
        <div v-if="createBanner" class="banner banner--error modal-banner" role="alert">{{ createBanner }}</div>
        <form class="modal-form" novalidate @submit.prevent="submitCreate">
          <div class="field">
            <label class="field-label" for="cert-domain">{{ t('certs.fieldDomain') }}</label>
            <input
              id="cert-domain"
              v-model="createForm.domain"
              class="field-input field-input--mono"
              :class="{ 'field-input--error': createErrors.domain }"
              type="text"
              :placeholder="t('certs.domainPlaceholder')"
              :disabled="createBusy"
              autocomplete="off"
              spellcheck="false"
              @input="createErrors.domain = ''"
            />
            <span v-if="createErrors.domain" class="field-error" role="alert">{{ createErrors.domain }}</span>
            <span class="field-hint">{{ t('certs.domainHint') }}</span>
          </div>

          <div class="field">
            <label class="field-label" for="cert-sans">{{ t('certs.fieldSans') }}</label>
            <textarea
              id="cert-sans"
              v-model="createForm.sans"
              class="field-input field-input--mono area"
              :class="{ 'field-input--error': createErrors.sans }"
              rows="2"
              :placeholder="t('certs.sansPlaceholder')"
              :disabled="createBusy"
              spellcheck="false"
              @input="createErrors.sans = ''"
            />
            <span v-if="createErrors.sans" class="field-error" role="alert">{{ createErrors.sans }}</span>
            <span v-else-if="parseDomains(createForm.sans).length" class="field-hint">
              {{ t('certs.sansPreview', { names: parseDomains(createForm.sans).join('、') }) }}
            </span>
          </div>

          <div class="field">
            <label class="field-label" for="cert-ca">{{ t('certs.fieldCa') }}</label>
            <select
              id="cert-ca"
              v-model="createForm.ca"
              class="field-input"
              :class="{ 'field-input--error': createErrors.ca }"
              :disabled="createBusy"
            >
              <option value="" disabled>{{ t('certs.caPlaceholder') }}</option>
              <option v-for="opt in caOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
            </select>
            <span v-if="createErrors.ca" class="field-error" role="alert">{{ createErrors.ca }}</span>
            <span v-else-if="selectedCa && !hasAccountForCa(selectedCa.id)" class="field-hint">
              {{ t('certs.caNoAccount', { name: selectedCa.name }) }}
              <button type="button" class="link-btn" @click="modal = 'accounts'; openAccounts()">{{ t('certs.btnAccounts') }}</button>
            </span>
          </div>

          <div class="field">
            <label class="field-label" for="cert-dns">{{ t('certs.fieldDns') }}</label>
            <select
              id="cert-dns"
              v-model="createForm.dnsProviderId"
              class="field-input"
              :class="{ 'field-input--error': createErrors.dnsProviderId }"
              :disabled="createBusy"
            >
              <option value="" disabled>{{ dnsProviders.length ? t('certs.dnsPlaceholder') : t('certs.dnsNone') }}</option>
              <option v-for="p in dnsProviders" :key="p.id" :value="p.id">{{ p.name }} · {{ p.baseDomain }}</option>
            </select>
            <span v-if="createErrors.dnsProviderId" class="field-error" role="alert">{{ createErrors.dnsProviderId }}</span>
            <span class="field-hint">{{ t('certs.dnsHint') }}</span>
          </div>

          <label class="check-line">
            <input v-model="createForm.autoRenew" type="checkbox" :disabled="createBusy" />
            <span>{{ t('certs.autoRenewLabel') }}</span>
          </label>

          <div class="modal-footer">
            <button type="button" class="btn-secondary" :disabled="createBusy" @click="modal = ''">{{ t('certs.cancel') }}</button>
            <button type="submit" class="btn-primary" :disabled="createBusy" :aria-busy="createBusy">
              <span v-if="createBusy" class="spinner" aria-hidden="true" />
              {{ createBusy ? t('certs.saving') : t('certs.createSubmit') }}
            </button>
          </div>
        </form>
      </div>
    </div>
  </Teleport>

  <!-- 导入 / 替换正文(同一份载荷) -->
  <Teleport to="body">
    <div
      v-if="modal === 'import' || modal === 'replace'"
      class="modal-scrim"
      role="dialog"
      :aria-label="modal === 'replace' ? t('certs.replaceTitle') : t('certs.importTitle')"
      aria-modal="true"
      @keydown.esc="pemBusy || (modal = '')"
      @click.self="!pemBusy && (modal = '')"
    >
      <div class="modal modal--wide">
        <div class="modal-head">
          <div class="modal-icon" aria-hidden="true"><NIcon :size="18"><Upload /></NIcon></div>
          <div>
            <h3 class="modal-title">{{ modal === 'replace' ? t('certs.replaceTitle') : t('certs.importTitle') }}</h3>
            <p class="modal-sub">{{ modal === 'replace' ? t('certs.replaceSub') : t('certs.importSub') }}</p>
          </div>
          <button class="modal-close" :aria-label="t('certs.closeDialog')" :disabled="pemBusy" @click="modal = ''">
            <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M18 6 6 18M6 6l12 12"/></svg>
          </button>
        </div>
        <div v-if="pemBanner" class="banner banner--error modal-banner" role="alert">{{ pemBanner }}</div>
        <div v-if="modal === 'replace'" class="banner banner--warn modal-banner">{{ t('certs.replaceWarn') }}</div>
        <form class="modal-form" novalidate @submit.prevent="submitPem">
          <div class="field">
            <label class="field-label" for="pem-domain">{{ t('certs.fieldDomain') }}</label>
            <input
              id="pem-domain"
              v-model="pemForm.domain"
              class="field-input field-input--mono"
              type="text"
              :placeholder="t('certs.importDomainPlaceholder')"
              :disabled="pemBusy"
              autocomplete="off"
              spellcheck="false"
            />
            <span v-if="modal === 'import'" class="field-hint">{{ t('certs.importDomainHint') }}</span>
          </div>

          <div class="field">
            <label class="field-label" for="pem-cert">{{ t('certs.fieldCertPem') }}</label>
            <textarea
              id="pem-cert"
              v-model="pemForm.certPem"
              class="field-input field-input--mono area area--pem"
              rows="6"
              :placeholder="t('certs.certPemPlaceholder')"
              :disabled="pemBusy"
              spellcheck="false"
              @input="pemBanner = ''"
            />
          </div>

          <div class="field">
            <label class="field-label" for="pem-key">{{ t('certs.fieldKeyPem') }}</label>
            <textarea
              id="pem-key"
              v-model="pemForm.keyPem"
              class="field-input field-input--mono area area--pem"
              rows="6"
              :placeholder="t('certs.keyPemPlaceholder')"
              :disabled="pemBusy"
              autocomplete="off"
              spellcheck="false"
              @input="pemBanner = ''"
            />
            <span class="field-hint">{{ t('certs.keyPemHint') }}</span>
          </div>

          <div class="field">
            <label class="field-label" for="pem-chain">{{ t('certs.fieldChainPem') }}</label>
            <textarea
              id="pem-chain"
              v-model="pemForm.chainPem"
              class="field-input field-input--mono area area--pem"
              rows="4"
              :placeholder="t('certs.chainPemPlaceholder')"
              :disabled="pemBusy"
              spellcheck="false"
            />
            <span class="field-hint">{{ t('certs.chainPemHint') }}</span>
          </div>

          <div v-if="modal === 'import'" class="field">
            <label class="field-label" for="pem-dns">{{ t('certs.fieldDnsOptional') }}</label>
            <select id="pem-dns" v-model="pemForm.dnsProviderId" class="field-input" :disabled="pemBusy">
              <option value="">{{ t('certs.dnsSkip') }}</option>
              <option v-for="p in dnsProviders" :key="p.id" :value="p.id">{{ p.name }} · {{ p.baseDomain }}</option>
            </select>
            <span class="field-hint">{{ t('certs.dnsOptionalHint') }}</span>
          </div>

          <div class="modal-footer">
            <button type="button" class="btn-secondary" :disabled="pemBusy" @click="modal = ''">{{ t('certs.cancel') }}</button>
            <button type="submit" class="btn-primary" :disabled="pemBusy" :aria-busy="pemBusy">
              <span v-if="pemBusy" class="spinner" aria-hidden="true" />
              {{ pemBusy ? t('certs.saving') : modal === 'replace' ? t('certs.replaceSubmit') : t('certs.importSubmit') }}
            </button>
          </div>
        </form>
      </div>
    </div>
  </Teleport>

  <!-- 详情:公钥材料 + 落点 -->
  <Teleport to="body">
    <div
      v-if="modal === 'detail' && detail"
      class="modal-scrim"
      role="dialog"
      :aria-label="t('certs.detailTitle', { domain: detail.domain })"
      aria-modal="true"
      @keydown.esc="closeDetail"
      @click.self="closeDetail"
    >
      <div class="modal modal--wide">
        <div class="modal-head">
          <div class="modal-icon" aria-hidden="true"><NIcon :size="18"><Certificate /></NIcon></div>
          <div>
            <h3 class="modal-title">{{ t('certs.detailTitle', { domain: detail.domain }) }}</h3>
            <p class="modal-sub">{{ sourceLabels[detail.source] }} · {{ statusLabels[detail.status] }} · {{ detail.keyAlgo || '—' }}</p>
          </div>
          <button class="modal-close" :aria-label="t('certs.closeDialog')" :disabled="detailBusy" @click="closeDetail">
            <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M18 6 6 18M6 6l12 12"/></svg>
          </button>
        </div>

        <div v-if="detailError" class="banner banner--error modal-banner" role="alert">{{ detailError }}</div>
        <div v-if="detail.lastError" class="banner banner--warn modal-banner" role="alert">
          {{ t('certs.lastError') }}:{{ detail.lastError }}
        </div>

        <div class="detail-body">
          <dl class="info">
            <div><dt>{{ t('certs.infoNotBefore') }}</dt><dd class="mono">{{ fmtDateTime(detail.notBefore) }}</dd></div>
            <div><dt>{{ t('certs.infoNotAfter') }}</dt><dd class="mono">{{ fmtDateTime(detail.notAfter) }} {{ detail.notAfter ? expiryText(detail) : '' }}</dd></div>
            <div><dt>{{ t('certs.infoIssuer') }}</dt><dd>{{ detail.issuer || '—' }}</dd></div>
            <div><dt>{{ t('certs.infoSerial') }}</dt><dd class="mono">{{ detail.serial || '—' }}</dd></div>
            <div><dt>{{ t('certs.infoSans') }}</dt><dd>{{ (detail.sans ?? []).join('、') || '—' }}</dd></div>
            <div><dt>{{ t('certs.infoChecked') }}</dt><dd class="mono">{{ fmtDateTime(detail.checkedAt) }}</dd></div>
          </dl>

          <div class="pem-bar">
            <button class="op-btn" type="button" @click="showPem = !showPem">
              {{ showPem ? t('certs.hidePem') : t('certs.showPem') }}
            </button>
          </div>
          <div v-if="showPem" class="pem-box">
            <pre class="pem mono">{{ detail.certPem || t('certs.pemNone') }}</pre>
            <pre v-if="detail.chainPem" class="pem mono">{{ detail.chainPem }}</pre>
          </div>

          <div class="targets-head">
            <h4 class="sub-title">{{ t('certs.targetsTitle') }}</h4>
            <p class="sub-desc">{{ t('certs.targetsDesc') }}</p>
            <button
              v-if="canManage"
              class="btn-secondary btn-sm"
              type="button"
              :disabled="targetBusy || detailBusy"
              @click="openTargetForm()"
            >+ {{ t('certs.addTarget') }}</button>
          </div>

          <div v-if="detailBusy" class="state-msg">{{ t('certs.loadingDetail') }}</div>
          <div v-else-if="detailTargets.length === 0 && !targetFormOpen" class="targets-empty">
            {{ t('certs.targetsEmpty') }}
          </div>
          <table v-else class="targets">
            <thead>
              <tr>
                <th>{{ t('certs.colHost') }}</th>
                <th>{{ t('certs.colCertPath') }}</th>
                <th>{{ t('certs.colKeyPath') }}</th>
                <th>{{ t('certs.colAutoDeploy') }}</th>
                <th>{{ t('certs.colLast') }}</th>
                <th v-if="canManage" />
              </tr>
            </thead>
            <tbody>
              <tr v-for="t2 in detailTargets" :key="t2.id">
                <td>{{ serverName(t2.serverId) }}</td>
                <td class="mono">{{ t2.certPath }}</td>
                <td class="mono">{{ t2.keyPath }}</td>
                <td>{{ t2.autoDeploy ? t('certs.on') : t('certs.off') }}</td>
                <td>
                  <span :class="targetResult(t2).ok ? 'res--ok' : 'res--bad'">
                    <NIcon :size="12"><CircleCheck v-if="targetResult(t2).ok" /><CircleX v-else /></NIcon>
                    {{ targetResult(t2).label }}
                  </span>
                  <span v-if="t2.lastError" class="res-err" :title="t2.lastError">{{ t2.lastError }}</span>
                  <div v-if="t2.postCommand" class="mono cmd" :title="t('certs.postCommandTitle')">{{ t2.postCommand }}</div>
                </td>
                <td v-if="canManage" class="row-ops">
                  <button class="op-btn op-btn--icon" type="button" :disabled="targetBusy" :title="t('certs.editTarget')" @click="openTargetForm(t2)">
                    <NIcon :size="12"><Pencil /></NIcon>
                  </button>
                  <button class="op-btn op-btn--danger op-btn--icon" type="button" :disabled="targetBusy" :title="t('certs.deleteTarget')" @click="removeTarget(t2)">
                    <NIcon :size="12"><Trash /></NIcon>
                  </button>
                </td>
              </tr>
            </tbody>
          </table>

          <form v-if="targetFormOpen" class="target-form" novalidate @submit.prevent="submitTarget">
            <div v-if="targetBanner" class="banner banner--error" role="alert">{{ targetBanner }}</div>
            <div class="field">
              <label class="field-label">{{ t('certs.fieldServer') }}</label>
              <select v-model="targetForm.serverId" class="field-input" :class="{ 'field-input--error': targetErrors.serverId }" :disabled="targetBusy">
                <option value="" disabled>{{ servers.length ? t('certs.serverPlaceholder') : t('certs.serverNone') }}</option>
                <option v-for="s in servers" :key="s.id" :value="s.id">{{ s.name }} · {{ s.host }}</option>
              </select>
              <span v-if="targetErrors.serverId" class="field-error" role="alert">{{ targetErrors.serverId }}</span>
            </div>
            <div class="field">
              <label class="field-label">{{ t('certs.fieldCertPath') }}</label>
              <input
                v-model="targetForm.certPath"
                class="field-input field-input--mono"
                :class="{ 'field-input--error': targetErrors.certPath }"
                type="text"
                :placeholder="t('certs.certPathPlaceholder')"
                :disabled="targetBusy"
                spellcheck="false"
                autocomplete="off"
                @input="targetErrors.certPath = ''"
              />
              <span v-if="targetErrors.certPath" class="field-error" role="alert">{{ targetErrors.certPath }}</span>
            </div>
            <div class="field">
              <label class="field-label">{{ t('certs.fieldKeyPath') }}</label>
              <input
                v-model="targetForm.keyPath"
                class="field-input field-input--mono"
                :class="{ 'field-input--error': targetErrors.keyPath }"
                type="text"
                :placeholder="t('certs.keyPathPlaceholder')"
                :disabled="targetBusy"
                spellcheck="false"
                autocomplete="off"
                @input="targetErrors.keyPath = ''"
              />
              <span v-if="targetErrors.keyPath" class="field-error" role="alert">{{ targetErrors.keyPath }}</span>
              <span v-else class="field-hint">{{ t('certs.pathHint') }}</span>
            </div>
            <div class="field">
              <label class="field-label">{{ t('certs.fieldPostCommand') }}</label>
              <input
                v-model="targetForm.postCommand"
                class="field-input field-input--mono"
                type="text"
                :placeholder="t('certs.postCommandPlaceholder')"
                :disabled="targetBusy"
                spellcheck="false"
                autocomplete="off"
              />
              <span class="field-hint">{{ t('certs.postCommandHint') }}</span>
            </div>
            <label class="check-line">
              <input v-model="targetForm.autoDeploy" type="checkbox" :disabled="targetBusy" />
              <span>{{ t('certs.autoDeployLabel') }}</span>
            </label>
            <div class="modal-footer">
              <button type="button" class="btn-secondary" :disabled="targetBusy" @click="closeTargetForm">{{ t('certs.cancel') }}</button>
              <button type="submit" class="btn-primary" :disabled="targetBusy" :aria-busy="targetBusy">
                <span v-if="targetBusy" class="spinner" aria-hidden="true" />
                {{ editingTarget ? t('certs.saveTarget') : t('certs.addTarget') }}
              </button>
            </div>
          </form>
        </div>

        <div class="detail-footer">
          <button v-if="canManage && detail.hasKey" class="btn-secondary btn-sm" type="button" :disabled="bundleBusy" @click="downloadBundle">
            <NIcon :size="13"><Download /></NIcon>
            {{ bundleBusy ? t('certs.working') : t('certs.btnBundle') }}
          </button>
          <span class="bundle-hint">{{ t('certs.bundleHint') }}</span>
          <button v-if="canManage" class="btn-primary btn-sm" type="button" @click="openReplace(detail)">
            <NIcon :size="13"><Upload /></NIcon>
            {{ t('certs.btnReplace') }}
          </button>
        </div>
      </div>
    </div>
  </Teleport>

  <!-- ACME 账户 -->
  <Teleport to="body">
    <div
      v-if="modal === 'accounts'"
      class="modal-scrim"
      role="dialog"
      :aria-label="t('certs.accountsTitle')"
      aria-modal="true"
      @keydown.esc="modal = ''"
      @click.self="modal = ''"
    >
      <div class="modal">
        <div class="modal-head">
          <div class="modal-icon" aria-hidden="true"><NIcon :size="18"><Id /></NIcon></div>
          <div>
            <h3 class="modal-title">{{ t('certs.accountsTitle') }}</h3>
            <p class="modal-sub">{{ t('certs.accountsSub') }}</p>
          </div>
          <button class="modal-close" :aria-label="t('certs.closeDialog')" @click="modal = ''">
            <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M18 6 6 18M6 6l12 12"/></svg>
          </button>
        </div>

        <div v-if="accountsError" class="banner banner--error modal-banner" role="alert">{{ accountsError }}</div>

        <div class="detail-body">
          <div v-if="accountsBusy" class="state-msg">{{ t('certs.loadingDetail') }}</div>
          <div v-else-if="accounts.length === 0 && !regOpen" class="targets-empty">{{ t('certs.accountsEmpty') }}</div>
          <table v-else-if="accounts.length" class="targets">
            <thead>
              <tr>
                <th>{{ t('certs.colCa') }}</th>
                <th>{{ t('certs.colContact') }}</th>
                <th>{{ t('certs.colEab') }}</th>
                <th>{{ t('certs.colRegistered') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="a in accounts" :key="a.id">
                <td>{{ a.ca }}</td>
                <td class="mono">{{ a.contact || '—' }}</td>
                <td>
                  <span :class="a.hasEabKey ? 'res--ok' : 'res--bad'">
                    {{ a.eabKeyId || (a.hasEabKey ? t('certs.eabSet') : t('certs.eabNone')) }}
                  </span>
                </td>
                <td class="mono">{{ fmtDateTime(a.registeredAt) }}</td>
              </tr>
            </tbody>
          </table>

          <form v-if="regOpen" class="target-form" novalidate @submit.prevent="submitRegister">
            <div v-if="regBanner" class="banner banner--error" role="alert">{{ regBanner }}</div>
            <div class="field">
              <label class="field-label">{{ t('certs.fieldCa') }}</label>
              <select v-model="regForm.ca" class="field-input" :disabled="regBusy">
                <option value="" disabled>{{ t('certs.caPlaceholder') }}</option>
                <option v-for="opt in caOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
              </select>
              <span v-if="regCa?.requiresEab" class="field-hint">{{ t('certs.caNeedsEab') }}</span>
            </div>
            <div class="field">
              <label class="field-label">{{ t('certs.fieldContact') }}</label>
              <input
                v-model="regForm.contact"
                class="field-input field-input--mono"
                type="text"
                :placeholder="CONTACT_PLACEHOLDER"
                :disabled="regBusy"
                autocomplete="off"
                spellcheck="false"
              />
              <span class="field-hint">{{ t('certs.contactHint') }}</span>
            </div>
            <template v-if="regCa?.requiresEab">
              <div class="field">
                <label class="field-label">{{ t('certs.fieldEabKeyId') }}</label>
                <input
                  v-model="regForm.eabKeyId"
                  class="field-input field-input--mono"
                  type="text"
                  :placeholder="t('certs.eabKeyIdPlaceholder')"
                  :disabled="regBusy"
                  autocomplete="off"
                  spellcheck="false"
                />
              </div>
              <div class="field">
                <label class="field-label">{{ t('certs.fieldEabKey') }}</label>
                <input
                  v-model="regForm.eabKey"
                  class="field-input field-input--mono"
                  type="password"
                  :placeholder="t('certs.eabKeyPlaceholder')"
                  :disabled="regBusy"
                  autocomplete="new-password"
                  @input="regBanner = ''"
                />
                <span class="field-hint">{{ t('certs.eabKeyHint') }}</span>
              </div>
            </template>
            <div class="modal-footer">
              <button type="button" class="btn-secondary" :disabled="regBusy" @click="regOpen = false">{{ t('certs.cancel') }}</button>
              <button type="submit" class="btn-primary" :disabled="regBusy" :aria-busy="regBusy">
                <span v-if="regBusy" class="spinner" aria-hidden="true" />
                {{ regBusy ? t('certs.working') : t('certs.registerSubmit') }}
              </button>
            </div>
          </form>
        </div>

        <div class="detail-footer">
          <button v-if="canManage && !regOpen" class="btn-primary btn-sm" type="button" @click="openRegister">
            + {{ t('certs.registerTitle') }}
          </button>
          <span class="bundle-hint">{{ t('certs.accountsFooterHint') }}</span>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.certs-root {
  display: flex;
  flex-direction: column;
  gap: var(--card-gap);
}

.section-head {
  display: flex;
  align-items: flex-start;
  gap: 16px;
  flex-wrap: wrap;
}
.section-head-text {
  flex: 1;
  min-width: 260px;
}
.section-title {
  font-size: 1.12rem;
  font-weight: 600;
  letter-spacing: -0.01em;
  color: var(--color-text);
}
.section-desc {
  font-size: 0.82rem;
  color: var(--color-faint);
  margin-top: 4px;
  max-width: 72ch;
  line-height: 1.55;
}
.head-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.panel {
  background: var(--color-card);
  border: 1px solid var(--color-border);
  border-radius: var(--rounded-card);
  box-shadow: var(--shadow);
  overflow: hidden;
  animation: panel-in 0.45s var(--ease-out-expo) both;
}
@keyframes panel-in {
  from { opacity: 0; transform: translateY(13px); }
  to { opacity: 1; transform: none; }
}
.panel-head {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 13px 18px;
  border-bottom: 1px solid var(--color-border);
  font-size: 0.86rem;
  font-weight: 600;
  color: var(--color-text);
}
.panel-meta {
  margin-left: auto;
  font-size: 0.74rem;
  color: var(--color-faint);
  font-weight: 400;
}
.state-msg {
  padding: 28px 18px;
  font-size: 0.84rem;
  color: var(--color-faint);
  text-align: center;
}

.toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}
.search {
  flex: 1;
  min-width: 200px;
  max-width: 360px;
}
.btn-refresh {
  height: 30px;
  padding: 0 12px;
  font-size: 0.78rem;
}
.switch {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 0.78rem;
  color: var(--color-dim);
  cursor: pointer;
  white-space: nowrap;
}

/* 检查汇总 */
.report-close {
  margin-left: auto;
  background: none;
  border: none;
  color: var(--color-faint);
  font-size: 0.76rem;
  cursor: pointer;
  padding: 0;
}
.report-close:hover {
  color: var(--color-text);
}
.report-body {
  display: grid;
  gap: 0;
}
.report-group {
  padding: 12px 18px;
  border-bottom: 1px solid var(--color-border);
}
.report-group:last-child {
  border-bottom: none;
}
.report-group-title {
  font-size: 0.76rem;
  font-weight: 600;
  letter-spacing: 0.02em;
  margin-bottom: 8px;
}
.report-group-title--expiring {
  color: var(--color-amber);
}
.report-group-title--renewed {
  color: var(--color-green);
}
.report-group-title--failed {
  color: var(--color-red);
}
.report-list {
  display: flex;
  flex-direction: column;
  gap: 6px;
  list-style: none;
}
.report-item {
  display: flex;
  align-items: baseline;
  gap: 10px;
  flex-wrap: wrap;
  font-size: 0.8rem;
}
.report-domain {
  color: var(--color-text);
  font-family: var(--font-mono);
  font-size: 0.78rem;
}
.report-detail,
.report-deploy {
  color: var(--color-dim);
  line-height: 1.5;
}
.report-deploy {
  color: var(--color-faint);
}

/* 列表 */
.cert-row {
  display: grid;
  grid-template-columns: minmax(150px, 1.4fr) 110px 92px minmax(120px, 1fr) 62px 128px auto;
  align-items: center;
  gap: 12px;
  padding: 12px 18px;
  border-bottom: 1px solid var(--color-border);
  transition: background-color var(--duration-fast);
}
.cert-row:last-child {
  border-bottom: none;
}
.cert-row:not(.cert-row--head):hover {
  background: var(--color-inset);
}
.cert-row--head {
  height: 34px;
  padding-top: 0;
  padding-bottom: 0;
  font-size: 0.71rem;
  color: var(--color-faint);
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  background: var(--color-card-2);
  pointer-events: none;
}

.cert-domain {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}
.cert-domain strong {
  font-size: 0.85rem;
  font-weight: 500;
  color: var(--color-text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.cert-sans {
  font-size: 0.7rem;
  color: var(--color-faint);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--rounded-full);
  padding: 1px 6px;
  flex-shrink: 0;
}
.cert-attention {
  color: var(--color-amber);
  flex-shrink: 0;
}

.cert-src {
  display: flex;
  flex-direction: column;
  font-size: 0.76rem;
  color: var(--color-dim);
}
.cert-ca {
  font-size: 0.68rem;
  color: var(--color-faint);
}

.status-badge {
  justify-self: start;
  font-size: 0.72rem;
  font-weight: 600;
  padding: 3px 9px;
  border-radius: var(--rounded-full);
  white-space: nowrap;
}
.st--green {
  color: var(--color-green);
  background: var(--color-green-soft);
}
.st--amber {
  color: var(--color-amber);
  background: var(--color-amber-soft);
}
.st--red {
  color: var(--color-red);
  background: var(--color-red-soft);
}
.st--cyan {
  color: var(--color-primary);
  background: var(--color-primary-soft);
}
.st--faint {
  color: var(--color-faint);
  background: var(--color-inset);
}

.cert-expire {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.cert-expire-days {
  font-size: 0.78rem;
  color: var(--color-text);
}
.cert-expire-date {
  font-size: 0.7rem;
  color: var(--color-faint);
}
.cert-checked {
  font-size: 0.72rem;
  color: var(--color-faint);
}
.mono {
  font-family: var(--font-mono);
}

.cert-auto {
  display: flex;
  align-items: center;
}
.toggle {
  width: 34px;
  height: 18px;
  border-radius: var(--rounded-full);
  border: 1px solid var(--color-border-strong);
  background: var(--color-inset);
  position: relative;
  cursor: pointer;
  padding: 0;
  transition: background-color var(--duration-fast), border-color var(--duration-fast);
}
.toggle--on {
  background: var(--color-primary);
  border-color: var(--color-primary);
}
.toggle:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.toggle-knob {
  position: absolute;
  top: 1px;
  left: 1px;
  width: 14px;
  height: 14px;
  border-radius: var(--rounded-full);
  background: var(--color-card);
  transition: transform var(--duration-fast);
}
.toggle--on .toggle-knob {
  transform: translateX(16px);
}

.cert-ops {
  display: flex;
  justify-content: flex-end;
  align-items: center;
  gap: 6px;
}

/* 空态 */
.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 10px;
  padding: 44px 32px;
  text-align: center;
}
.empty-icon {
  width: 52px;
  height: 52px;
  border-radius: var(--rounded-xl);
  background: var(--color-inset);
  border: 1.5px dashed var(--color-border-strong);
  display: grid;
  place-items: center;
  color: var(--color-dim);
  margin-bottom: 4px;
}
.empty-label {
  font-size: 0.92rem;
  font-weight: 600;
  color: var(--color-text);
}
.empty-hint {
  font-size: 0.8rem;
  color: var(--color-faint);
  max-width: 48ch;
  line-height: 1.55;
  margin-bottom: 6px;
}

/* banner */
.banner {
  display: flex;
  align-items: center;
  gap: 9px;
  padding: 11px 14px;
  border-radius: var(--rounded);
  font-size: 0.83rem;
  line-height: 1.5;
}
.banner--error {
  background: var(--color-red-soft);
  border: 1px solid var(--color-red-line);
  color: var(--color-red);
}
.banner--warn {
  background: var(--color-amber-soft);
  border: 1px solid var(--color-amber-line);
  color: var(--color-amber);
}
.banner-retry {
  margin-left: auto;
  flex-shrink: 0;
  background: none;
  border: none;
  color: var(--color-red);
  font-size: 0.83rem;
  font-weight: 600;
  cursor: pointer;
  padding: 0;
  text-decoration: underline;
  text-underline-offset: 2px;
}

/* buttons */
.btn-primary {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 34px;
  padding: 0 15px;
  border: none;
  background: var(--color-primary);
  color: #fff;
  font-family: var(--font-sans);
  font-size: 0.83rem;
  font-weight: 600;
  border-radius: var(--rounded);
  cursor: pointer;
  box-shadow: 0 5px 16px var(--color-primary-soft);
  transition: background-color var(--duration-fast), transform var(--duration-fast);
  white-space: nowrap;
  flex-shrink: 0;
}
.btn-primary:hover:not(:disabled) {
  background: var(--color-primary-press);
  transform: translateY(-1px);
}
.btn-primary:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: 3px;
}
.btn-primary:disabled {
  opacity: 0.45;
  cursor: not-allowed;
  transform: none;
  box-shadow: none;
}
.btn-secondary {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 34px;
  padding: 0 15px;
  background: var(--color-card-2);
  color: var(--color-text);
  border: 1px solid var(--color-border-strong);
  font-family: var(--font-sans);
  font-size: 0.83rem;
  font-weight: 500;
  border-radius: var(--rounded);
  cursor: pointer;
  transition: border-color var(--duration-fast);
  white-space: nowrap;
}
.btn-secondary:hover:not(:disabled) {
  border-color: var(--color-faint);
}
.btn-secondary:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.btn-sm {
  height: 28px;
  padding: 0 11px;
  font-size: 0.78rem;
}

.op-btn {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  height: 28px;
  padding: 0 10px;
  border: 1px solid var(--color-border);
  background: transparent;
  color: var(--color-dim);
  border-radius: var(--rounded-md);
  cursor: pointer;
  font-size: 0.74rem;
  font-weight: 500;
  white-space: nowrap;
  transition: color var(--duration-fast), border-color var(--duration-fast), background-color var(--duration-fast);
}
.op-btn:hover:not(:disabled) {
  color: var(--color-primary);
  border-color: var(--color-primary);
}
.op-btn:disabled {
  opacity: 0.5;
  cursor: progress;
}
.op-btn--icon {
  padding: 0 7px;
}
.op-btn--danger {
  padding: 0 8px;
}
.op-btn--danger:hover:not(:disabled) {
  color: var(--color-red);
  border-color: var(--color-red-line);
  background: var(--color-red-soft);
}
.link-btn {
  background: none;
  border: none;
  color: var(--color-primary);
  font-size: 0.74rem;
  cursor: pointer;
  padding: 0;
  text-decoration: underline;
  text-underline-offset: 2px;
}

/* modal */
.modal-scrim {
  position: fixed;
  inset: 0;
  background: oklch(0% 0 0 / 0.62);
  display: grid;
  place-items: center;
  z-index: 100;
  padding: 24px;
  animation: scrim-in var(--duration-fast) ease both;
  overflow: auto;
}
@keyframes scrim-in {
  from { opacity: 0; }
  to { opacity: 1; }
}
.modal {
  width: 100%;
  max-width: 540px;
  background: var(--color-card);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--rounded-xl);
  box-shadow: var(--shadow-modal);
  overflow: hidden;
  animation: modal-in 0.35s var(--ease-out-expo) both;
}
.modal--wide {
  max-width: 760px;
}
@keyframes modal-in {
  from { opacity: 0; transform: translateY(14px) scale(0.98); }
  to { opacity: 1; transform: none; }
}
.modal-head {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  padding: 20px 20px 16px;
  border-bottom: 1px solid var(--color-border);
}
.modal-icon {
  width: 36px;
  height: 36px;
  border-radius: var(--rounded-lg);
  background: var(--color-primary-soft);
  color: var(--color-primary);
  display: grid;
  place-items: center;
  flex-shrink: 0;
}
.modal-title {
  font-size: 1rem;
  font-weight: 600;
  color: var(--color-text);
  margin-top: 2px;
  letter-spacing: -0.01em;
}
.modal-sub {
  font-size: 0.78rem;
  color: var(--color-faint);
  margin-top: 3px;
  line-height: 1.4;
}
.modal-close {
  margin-left: auto;
  flex-shrink: 0;
  width: 30px;
  height: 30px;
  border-radius: var(--rounded-md);
  border: none;
  background: transparent;
  color: var(--color-faint);
  cursor: pointer;
  display: grid;
  place-items: center;
  transition: color var(--duration-fast), background-color var(--duration-fast);
}
.modal-close:hover {
  color: var(--color-text);
  background: var(--color-inset);
}
.modal-close:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}
.modal-banner {
  margin: 16px 20px 0;
  border-radius: var(--rounded);
}
.modal-form {
  padding: 20px;
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.modal-footer {
  display: flex;
  justify-content: flex-end;
  align-items: center;
  gap: 8px;
  padding-top: 4px;
}

/* fields */
.field {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.field-label {
  font-size: 0.78rem;
  font-weight: 500;
  color: var(--color-dim);
}
.field-input {
  width: 100%;
  height: 38px;
  background: var(--color-inset);
  border: 1px solid var(--color-border);
  border-radius: var(--rounded);
  padding: 0 12px;
  color: var(--color-text);
  font-family: var(--font-sans);
  font-size: 0.86rem;
  transition: border-color var(--duration-fast), box-shadow var(--duration-fast);
}
.field-input::placeholder {
  color: var(--color-faint);
}
.field-input:focus {
  outline: none;
  border-color: var(--color-primary);
  box-shadow: 0 0 0 3px var(--color-primary-soft);
}
.field-input--error {
  border-color: var(--color-red);
}
.field-input--error:focus {
  border-color: var(--color-red);
  box-shadow: 0 0 0 3px var(--color-red-soft);
}
.field-input:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.field-input--mono {
  font-family: var(--font-mono);
  font-size: 0.8rem;
}
.area {
  height: auto;
  padding: 10px 12px;
  line-height: 1.5;
  resize: vertical;
}
.area--pem {
  min-height: 92px;
}
.field-error {
  font-size: 0.76rem;
  color: var(--color-red);
  line-height: 1.4;
}
.field-hint {
  font-size: 0.74rem;
  color: var(--color-faint);
  line-height: 1.45;
}
.check-line {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 0.8rem;
  color: var(--color-dim);
  cursor: pointer;
}

/* 详情体 */
.detail-body {
  padding: 18px 20px;
  display: flex;
  flex-direction: column;
  gap: 14px;
  max-height: 60vh;
  overflow: auto;
}
.info {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 8px 18px;
}
.info > div {
  display: flex;
  gap: 8px;
  align-items: baseline;
  min-width: 0;
}
.info dt {
  font-size: 0.74rem;
  color: var(--color-faint);
  white-space: nowrap;
}
.info dd {
  font-size: 0.78rem;
  color: var(--color-text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.pem-bar {
  display: flex;
  justify-content: flex-start;
}
.pem-box {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.pem {
  background: var(--color-inset);
  border: 1px solid var(--color-border);
  border-radius: var(--rounded);
  padding: 10px 12px;
  font-size: 0.7rem;
  line-height: 1.45;
  max-height: 150px;
  overflow: auto;
  white-space: pre-wrap;
  word-break: break-all;
  color: var(--color-dim);
}
.sub-title {
  font-size: 0.84rem;
  font-weight: 600;
  color: var(--color-text);
}
.sub-desc {
  font-size: 0.75rem;
  color: var(--color-faint);
  line-height: 1.5;
}
.targets-head {
  display: flex;
  flex-direction: column;
  gap: 6px;
  align-items: flex-start;
}
.targets-head .btn-sm {
  margin-top: 2px;
}
.targets-empty {
  font-size: 0.78rem;
  color: var(--color-faint);
  padding: 14px;
  border: 1px dashed var(--color-border-strong);
  border-radius: var(--rounded);
  text-align: center;
}
.targets {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.78rem;
}
.targets th {
  text-align: left;
  font-size: 0.7rem;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: var(--color-faint);
  font-weight: 600;
  padding: 6px 8px;
  border-bottom: 1px solid var(--color-border);
}
.targets td {
  padding: 8px;
  border-bottom: 1px solid var(--color-border);
  color: var(--color-dim);
  vertical-align: top;
}
.targets tr:last-child td {
  border-bottom: none;
}
.row-ops {
  display: flex;
  gap: 5px;
  justify-content: flex-end;
}
.res--ok {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  color: var(--color-green);
}
.res--bad {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  color: var(--color-faint);
}
.res-err {
  display: block;
  font-size: 0.7rem;
  color: var(--color-red);
  margin-top: 2px;
  max-width: 40ch;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.cmd {
  font-size: 0.68rem;
  color: var(--color-faint);
  margin-top: 3px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 40ch;
}
.target-form {
  display: flex;
  flex-direction: column;
  gap: 14px;
  padding: 14px;
  border: 1px solid var(--color-border-strong);
  border-radius: var(--rounded);
  background: var(--color-card-2);
}
.detail-footer {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 14px 20px;
  border-top: 1px solid var(--color-border);
  background: var(--color-card-2);
}
.bundle-hint {
  font-size: 0.72rem;
  color: var(--color-faint);
  flex: 1;
  line-height: 1.45;
}

/* spinner */
.spinner {
  display: inline-block;
  width: 13px;
  height: 13px;
  border: 2px solid rgba(255, 255, 255, 0.35);
  border-top-color: #fff;
  border-radius: var(--rounded-full);
  animation: spin 0.7s linear infinite;
  flex-shrink: 0;
}
.spinner--dim {
  border-color: var(--color-border-strong);
  border-top-color: var(--color-dim);
}
@keyframes spin {
  to { transform: rotate(360deg); }
}

@media (max-width: 640px) {
  .cert-row {
    grid-template-columns: 1fr 1fr;
    gap: 8px 12px;
  }
  .cert-row--head {
    display: none;
  }
  .cert-ops {
    grid-column: 1 / -1;
    justify-content: flex-start;
  }
}

@media (prefers-reduced-motion: reduce) {
  *, *::before, *::after {
    animation-duration: 0.01ms !important;
    animation-iteration-count: 1 !important;
    transition-duration: 0.01ms !important;
  }
}
</style>
