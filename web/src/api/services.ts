/**
 * 归属服务 API — 项目上的纯展示 / 筛选属性,与资源分组(权限轴)正交。
 *
 * 普通登录用户:
 *   GET    /api/services            → Service[](含项目数,后端 LEFT JOIN 统计)
 * 管理员:
 *   POST   /api/services            → Service
 *   PATCH  /api/services/{id}       → Service     (字段省略 = 不动)
 *   DELETE /api/services/{id}       → 204         (仍有项目归属 → 409 service_in_use)
 *
 * 服务不参与可见性 / 操作权判定:看得见项目就看得见它的服务归属。
 * 项目上的归属走 PATCH /api/projects/{id} 的 serviceId('' = 未归属)。
 */

import { http } from './http'

export interface Service {
  id: string
  name: string
  description: string
  /** 归属该服务的项目数(管理弹窗展示,删前提示用)。 */
  projectCount: number
  createdAt: string
  updatedAt: string
}

export interface CreateServiceInput {
  name: string
  description?: string
}

export interface UpdateServiceInput {
  name?: string
  description?: string
}

export async function listServices(): Promise<Service[]> {
  return http.get<Service[]>('/api/services')
}

export async function createService(input: CreateServiceInput): Promise<Service> {
  return http.post<Service>('/api/services', input)
}

export async function updateService(id: string, input: UpdateServiceInput): Promise<Service> {
  return http.patch<Service>(`/api/services/${encodeURIComponent(id)}`, input)
}

export async function deleteService(id: string): Promise<void> {
  return http.delete<void>(`/api/services/${encodeURIComponent(id)}`)
}
