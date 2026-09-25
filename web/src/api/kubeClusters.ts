/**
 * Kube clusters API — the second "deploy target" leg (K8s release, direct API server).
 *
 * GET    /api/kube-clusters            → { items: KubeCluster[] }
 * POST   /api/kube-clusters            → KubeCluster       (needs CSRF)
 * GET    /api/kube-clusters/:id        → KubeCluster
 * PUT    /api/kube-clusters/:id        → KubeCluster       (needs CSRF)
 * DELETE /api/kube-clusters/:id        → 204               (needs CSRF)
 * POST   /api/kube-clusters/:id/test   → KubeTestResult     (needs CSRF)
 *
 * A cluster binds a `kubeconfig` credential by reference only; the API never
 * returns the document, a token or a client certificate. The `endpoint` shown in
 * the list is parsed from the credential on read (it is not stored in the DB), so
 * it can be empty when the credential was deleted or replaced.
 */

import { http } from './http'

export interface KubeCluster {
  id: string
  name: string
  /** Reference to a kubeconfig credential — never the document itself. */
  credentialId: string
  credentialName: string
  /** API server address read back from the kubeconfig (display only; '' if unavailable). */
  endpoint: string
  /** Fallback namespace for release jobs that leave it empty; '' = none. */
  namespaceDefault: string
  /** 所属资源分组;'' = 未归组(与服务器同一口径:登记未归组集群是管理员动作)。 */
  groupId: string
  createdAt: string
  updatedAt: string
}

export interface CreateKubeClusterInput {
  name: string
  credentialId: string
  namespaceDefault?: string
  groupId?: string
}

export interface UpdateKubeClusterInput {
  name?: string
  credentialId?: string
  namespaceDefault?: string
  /** 归组/改组;传 '' 表示移出分组。需要对该组(含原组)有 Manage 权。 */
  groupId?: string
}

export interface KubeTestResult {
  ok: boolean
  latencyMs: number
  /** Server version on success (`GET /version`); empty on failure. */
  output: string
  /** Human-readable error on failure; never contains the kubeconfig or a token. */
  error: string | null
}

export async function listKubeClusters(): Promise<KubeCluster[]> {
  const res = await http.get<{ items: KubeCluster[] }>('/api/kube-clusters')
  return res.items
}

export async function createKubeCluster(input: CreateKubeClusterInput): Promise<KubeCluster> {
  return http.post<KubeCluster>('/api/kube-clusters', input)
}

export async function updateKubeCluster(
  id: string,
  input: UpdateKubeClusterInput,
): Promise<KubeCluster> {
  return http.put<KubeCluster>(`/api/kube-clusters/${id}`, input)
}

export async function deleteKubeCluster(id: string): Promise<void> {
  return http.delete<void>(`/api/kube-clusters/${id}`)
}

export async function testKubeCluster(id: string): Promise<KubeTestResult> {
  return http.post<KubeTestResult>(`/api/kube-clusters/${id}/test`)
}
