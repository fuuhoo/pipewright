/**
 * 证书管理(/api/certs)客户端。
 *
 * 后端契约(internal/httpapi/certs.go):
 *   GET    /api/certs                  → { items: Cert[] }        (cert.view)
 *   POST   /api/certs                  → Cert(201)                (cert.manage,登记一张待签证书)
 *   GET    /api/certs/cas              → { items: CaOption[] }     (cert.view,内置 CA 名单)
 *   GET    /api/certs/accounts         → { items: AcmeAccount[] }  (cert.view)
 *   POST   /api/certs/accounts         → AcmeAccount              (cert.manage,EAB key 只写不回显)
 *   POST   /api/certs/import           → Cert                     (cert.manage,导入已有证书)
 *   POST   /api/certs/check            → CheckReport              (cert.manage,**会当场续期并铺发**)
 *   GET    /api/certs/{id}             → Cert(含 certPem/chainPem)
 *   DELETE /api/certs/{id}             → 204
 *   POST   /api/certs/{id}/replace     → Cert                     (换正文,落点配置一行不动)
 *   POST   /api/certs/{id}/issue       → Cert                     (ACME DNS-01 签/续,可能要几分钟)
 *   POST   /api/certs/{id}/auto-renew  → Cert
 *   GET    /api/certs/{id}/bundle      → CertBundle               (私钥的唯一出口,cert.manage)
 *   GET    /api/certs/{id}/targets     → { items: CertTarget[] }
 *   POST   /api/certs/{id}/targets     → CertTarget(201)          (还要那台主机的操作权)
 *   PUT    /api/certs/{id}/targets/{t} → CertTarget
 *   DELETE /api/certs/{id}/targets/{t} → 204
 *   POST   /api/certs/{id}/deploy      → DeployReport             (逐落点报告)
 *
 * 私钥只在 CertBundle 里出现(AC-SEC-01),其余响应最多有 hasKey 这个布尔。
 * 时间字段零值回空串(不是 "0001-01-01T00:00:00Z"),页面按「空 = 还没有」处理。
 */

import { http } from './http'

/** 证书来源:acme = 平台自签(能自动续),upload = 用户导入(平台签不了)。 */
export type CertSource = 'acme' | 'upload'
/** 展示口径的状态(由正文与到期时刻推导,不是库里那一列的原样透出)。 */
export type CertStatus = 'pending' | 'issued' | 'failed' | 'expired'

export interface Cert {
  id: string
  domain: string
  /** SAN 列表。库里存空时后端序列化为 null(不是空数组),用之前得兜一下。 */
  sans: string[] | null
  source: CertSource
  status: CertStatus
  ca: string
  dnsProviderId: string
  autoRenew: boolean
  keyAlgo: string
  notBefore: string
  notAfter: string
  issuer: string
  serial: string
  hasKey: boolean
  lastError: string
  /** 后端算好的剩余天数(向上取整;已过期为负)。没有正文时为 0。 */
  daysLeft: number
  issuedAt: string
  checkedAt: string
  createdAt: string
  updatedAt: string
  /** 仅详情响应有值(公钥材料);清单与写响应都不带。 */
  certPem?: string
  chainPem?: string
}

/** 一个发布落点:配置 + 最近一次结果。 */
export interface CertTarget {
  id: string
  certId: string
  serverId: string
  certPath: string
  keyPath: string
  postCommand: string
  autoDeploy: boolean
  lastStatus: string
  lastError: string
  deployedAt: string
  createdAt: string
  updatedAt: string
}

export interface AcmeAccount {
  id: string
  ca: string
  directoryUrl: string
  contact: string
  accountUrl: string
  eabKeyId: string
  /** EAB key 正文永不下发,只有「有没有」。 */
  hasEabKey: boolean
  registeredAt: string
  createdAt: string
  updatedAt: string
}

/** 内置 CA 名单里的一项(GET /api/certs/cas,与代码同源)。 */
export interface CaOption {
  id: string
  name: string
  directoryUrl: string
  requiresEab: boolean
  staging: boolean
}

export interface CertBundle {
  domain: string
  certPem: string
  chainPem: string
  fullchain: string
  privateKey: string
}

export interface DeployResult {
  targetId: string
  serverId: string
  certPath: string
  keyPath: string
  ok: boolean
  error: string
  deployedAt: string
}

export interface DeployReport {
  certId: string
  deployed: number
  failed: number
  results: DeployResult[]
}

/** 「立即检查」里逐张证书的一条。 */
export interface CheckItem {
  certId: string
  domain: string
  status: CertStatus
  daysLeft: number
  /** 该做什么:为什么平台没替这张自动续 / 续失败的原因。 */
  detail: string
  /** 自动铺发的结果;没有落点或未续时为 ''。 */
  deploy: string
}

export interface CheckReport {
  checked: number
  expiring: CheckItem[]
  renewed: CheckItem[]
  failed: CheckItem[]
}

export interface CreateCertInput {
  domain: string
  sans: string[]
  ca: string
  dnsProviderId: string
  autoRenew: boolean
}

/** 导入与「替换正文」共用同一份载荷。 */
export interface ImportCertInput {
  domain: string
  certPem: string
  keyPem: string
  chainPem: string
  dnsProviderId: string
  autoRenew: boolean
}

export interface CertTargetInput {
  serverId: string
  certPath: string
  keyPath: string
  postCommand: string
  autoDeploy: boolean
}

export interface RegisterAccountInput {
  ca: string
  contact: string
  eabKeyId: string
  /** 只写:提交后不再回显,重提同一账户会就地更新。 */
  eabKey: string
}

export async function listCerts(): Promise<Cert[]> {
  const res = await http.get<{ items: Cert[] }>('/api/certs')
  return res.items ?? []
}

export async function getCert(id: string): Promise<Cert> {
  return http.get<Cert>(`/api/certs/${encodeURIComponent(id)}`)
}

export async function createCert(input: CreateCertInput): Promise<Cert> {
  return http.post<Cert>('/api/certs', input)
}

export async function importCert(input: ImportCertInput): Promise<Cert> {
  return http.post<Cert>('/api/certs/import', input)
}

export async function replaceCert(id: string, input: ImportCertInput): Promise<Cert> {
  return http.post<Cert>(`/api/certs/${encodeURIComponent(id)}/replace`, input)
}

export async function issueCert(id: string): Promise<Cert> {
  return http.post<Cert>(`/api/certs/${encodeURIComponent(id)}/issue`, {})
}

export async function setAutoRenew(id: string, autoRenew: boolean): Promise<Cert> {
  return http.post<Cert>(`/api/certs/${encodeURIComponent(id)}/auto-renew`, { autoRenew })
}

export async function deleteCert(id: string): Promise<void> {
  await http.delete(`/api/certs/${encodeURIComponent(id)}`)
}

export async function checkCerts(): Promise<CheckReport> {
  return http.post<CheckReport>('/api/certs/check', {})
}

export async function getCertBundle(id: string): Promise<CertBundle> {
  return http.get<CertBundle>(`/api/certs/${encodeURIComponent(id)}/bundle`)
}

export async function listCertTargets(certId: string): Promise<CertTarget[]> {
  const res = await http.get<{ items: CertTarget[] }>(`/api/certs/${encodeURIComponent(certId)}/targets`)
  return res.items ?? []
}

export async function addCertTarget(certId: string, input: CertTargetInput): Promise<CertTarget> {
  return http.post<CertTarget>(`/api/certs/${encodeURIComponent(certId)}/targets`, input)
}

export async function updateCertTarget(certId: string, targetId: string, input: CertTargetInput): Promise<CertTarget> {
  return http.put<CertTarget>(
    `/api/certs/${encodeURIComponent(certId)}/targets/${encodeURIComponent(targetId)}`,
    input,
  )
}

export async function deleteCertTarget(certId: string, targetId: string): Promise<void> {
  await http.delete(`/api/certs/${encodeURIComponent(certId)}/targets/${encodeURIComponent(targetId)}`)
}

export async function deployCert(certId: string): Promise<DeployReport> {
  return http.post<DeployReport>(`/api/certs/${encodeURIComponent(certId)}/deploy`, {})
}

export async function listCas(): Promise<CaOption[]> {
  const res = await http.get<{ items: CaOption[] }>('/api/certs/cas')
  return res.items ?? []
}

export async function listAcmeAccounts(): Promise<AcmeAccount[]> {
  const res = await http.get<{ items: AcmeAccount[] }>('/api/certs/accounts')
  return res.items ?? []
}

export async function registerAcmeAccount(input: RegisterAccountInput): Promise<AcmeAccount> {
  return http.post<AcmeAccount>('/api/certs/accounts', input)
}
