/**
 * DNS providers API client (R3 / E3.1 — 零 DNS 体验).
 *
 * A `DnsProvider` is a provider account + the API token stored for it. There is **no**
 * base domain on this record: which zones a provider can manage lives in the provider
 * account itself, so the list is read live from the服务商 (`GET /dns/providers/{id}/zones`)
 * and the zone a record belongs to is derived from the record name at write time.
 * The token is **write-only** — supplied once on creation, stored via the backend vault,
 * and **never** returned; the DTO only says whether a credential is configured.
 *
 * Providers unlock two zero-DNS capabilities:
 *  - DNS-01 ACME on a route (wildcard certs `*.example.com`), and
 *  - instant subdomain allocation (`app-xxxx.<zone>` + auto A record + route).
 *
 * GET    /api/dns/providers              → { items: DnsProvider[] }
 * POST   /api/dns/providers              → DnsProvider           (needs CSRF)
 * DELETE /api/dns/providers/{id}         → { ok: true }          (needs CSRF)
 * POST   /api/dns/providers/{id}/verify  → { ok, zones }         (needs CSRF)
 * GET    /api/dns/providers/{id}/zones   → { items: string[] }
 *
 * Writes go through the shared `http` wrapper (auto CSRF + locale). Never returns a token.
 */

import { http } from './http'

/** Supported managed-DNS backends for DNS-01 + instant subdomains. */
export type DnsProviderType =
  | 'cloudflare'
  | 'dnspod'
  | 'alidns'
  | 'tencentcloud'
  | 'baidudns'
  | 'volcengine'

/**
 * 能被挂到托管反代路由上做 DNS-01 的类型 —— 与后端 proxy.validateDNSProviderRef 同一份名单,
 * 也和 deploy/caddy/Dockerfile 里捆的 caddy-dns 插件一一对应。
 * baidudns 只在证书管理那条线可用(平台自己调百度 API 落 TXT);Caddy 没有可信的百度 DNS 插件,
 * 绑进路由会让整份 Caddyfile 因未知模块起不来。
 */
const ROUTE_CAPABLE: readonly DnsProviderType[] = ['cloudflare', 'dnspod', 'alidns', 'tencentcloud', 'volcengine']

export function canAttachToRoute(type: DnsProviderType): boolean {
  return ROUTE_CAPABLE.includes(type)
}

/** A provider account + its write-only token. The token is never returned. */
export interface DnsProvider {
  id: string
  type: DnsProviderType
  /** Human-readable label, e.g. "生产区 Cloudflare". */
  name: string
  /** Server-derived: whether an API token is stored in the vault. Never the token itself. */
  credentialConfigured: boolean
  createdAt: string
}

/** Body for creating a provider. `token` is write-only — sent once, never echoed. */
export interface CreateDnsProviderInput {
  type: DnsProviderType
  name: string
  /** Provider API token (write-only). Stored via the vault; never returned. */
  token: string
}

/** Result of a verify call: the zones this provider account actually hosts. */
export interface VerifyDnsProviderResult {
  ok: boolean
  /** Hosted zones, normalised + sorted. Empty = valid credential but no hosted domain. */
  zones: string[]
}

/** List all configured DNS providers. Tokens are never included. */
export async function listDnsProviders(): Promise<DnsProvider[]> {
  const res = await http.get<{ items: DnsProvider[] }>('/api/dns/providers')
  return res.items ?? []
}

/** Add a DNS provider (token stored write-only via the vault). Returns the created DTO. */
export async function createDnsProvider(input: CreateDnsProviderInput): Promise<DnsProvider> {
  return http.post<DnsProvider>('/api/dns/providers', input)
}

/** Delete a DNS provider. Routes pinned to it lose DNS-01 until re-attached. */
export async function deleteDnsProvider(id: string): Promise<{ ok: boolean }> {
  return http.delete<{ ok: boolean }>(`/api/dns/providers/${encodeURIComponent(id)}`)
}

/** Verify a provider: list the zones its account hosts (read-only probe, audited as a deliberate check). */
export async function verifyDnsProvider(id: string): Promise<VerifyDnsProviderResult> {
  return http.post<VerifyDnsProviderResult>(`/api/dns/providers/${encodeURIComponent(id)}/verify`, {})
}

/**
 * The zones this provider account hosts, straight from the服务商. Feeds the root-domain
 * picker in「一键瞬时子域名」— a random `app-xxxx` prefix can't say which zone owns it,
 * so the caller picks one here and the backend still re-checks it against this same list.
 */
export async function listProviderZones(id: string): Promise<string[]> {
  const res = await http.get<{ items: string[] }>(`/api/dns/providers/${encodeURIComponent(id)}/zones`)
  return res.items ?? []
}
