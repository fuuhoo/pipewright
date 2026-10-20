/**
 * Projects API — aligns to frozen 2.1 contract.
 *
 * GET    /api/projects                  → Project[]
 * POST   /api/projects                  → Project   (needs CSRF)
 * POST   /api/projects/test-clone       → TestCloneResult (needs CSRF)
 * PATCH  /api/projects/{id}             → Project   (needs CSRF)
 * DELETE /api/projects/{id}             → 204       (needs CSRF)
 *
 * credentialName is read-only display; credentialId is the reference.
 * lastRunStatus / targetServers may be null/[] until future stories fill them.
 * The API never returns credential plaintext — only credentialName (masked display).
 */

import { http } from './http'

export type RunStatus =
  | '成功'
  | '失败'
  | '进行中'
  | '部分失败'
  | '已回滚'
  | '排队中'

export interface Project {
  id: string
  name: string
  repoUrl: string
  defaultBranch: string
  credentialId: string
  /** Masked display name from the vault — never plaintext. */
  credentialName: string
  /**
   * Pipeline-as-code (GitOps) toggle. When true, each run reads `.pipewright.yml`
   * from the run's branch in the repo and uses it to drive the run (falling back
   * to the stored UI pipeline if the file is missing or invalid). FR-8-12.
   */
  pacEnabled: boolean
  /**
   * PR status checks toggle (Story 8-9 / FR-8-9). When true, on a run reaching a
   * terminal status Pipewright detects the repo platform (GitHub/Gitee) and writes
   * back the commit status (PR check) using the project credential. Best-effort.
   */
  prStatusEnabled: boolean
  /**
   * 所属资源分组(v6.2 分组权限)。'' = 未归组 = 全员可见可操作;
   * 私有分组下的项目只有组长/成员/管理员能列出与操作。
   * 分组名称由前端用 GET /api/groups 映射,后端只给 ID。
   */
  groupId: string
  /** null until pipeline runs exist (Story 2.x). */
  lastRunStatus: RunStatus | null
  /** Empty until servers are bound (Story 2.x). */
  targetServers: string[]
  createdAt: string
  updatedAt: string
}

export interface CreateProjectInput {
  name: string
  repoUrl: string
  credentialId: string
  /** Optional — server auto-detects from ls-remote HEAD if omitted. */
  defaultBranch?: string
  /** Optional group id; omitted or '' = 未归组(仅管理员可登记未归组项目)。 */
  groupId?: string
}

export interface UpdateProjectInput {
  name?: string
  /**
   * 改绑仓库;传 '' 表示解绑 —— 项目退化为「只发布」(不拉源码,凭据/默认分支/需要仓库的开关一并清除)。
   * 省略 = 不动仓库相关字段。
   */
  repoUrl?: string
  defaultBranch?: string
  credentialId?: string
  /** Toggle pipeline-as-code (GitOps) for this project. */
  pacEnabled?: boolean
  /** Toggle PR status checks (commit status writeback) for this project. */
  prStatusEnabled?: boolean
  /** 归组/改组;传 '' 表示移出分组。需要对该组(含原组)有 Manage 权。 */
  groupId?: string
}

export interface TestCloneResult {
  ok: true
  defaultBranch: string
  /** Remote branch short names (populated after a successful test — feeds the create-dialog dropdown). */
  branches: string[]
  /** Remote tag names (peeled duplicates excluded). */
  tags: string[]
}

export interface TestCloneInput {
  repoUrl: string
  credentialId: string
}

export async function listProjects(): Promise<Project[]> {
  return http.get<Project[]>('/api/projects')
}

export async function createProject(input: CreateProjectInput): Promise<Project> {
  return http.post<Project>('/api/projects', input)
}

export async function testClone(input: TestCloneInput): Promise<TestCloneResult> {
  return http.post<TestCloneResult>('/api/projects/test-clone', input)
}

export async function updateProject(id: string, input: UpdateProjectInput): Promise<Project> {
  return http.patch<Project>(`/api/projects/${id}`, input)
}

export async function deleteProject(id: string): Promise<void> {
  return http.delete<void>(`/api/projects/${id}`)
}

/** One stage's summary from a previewed `.pipewright.yml` (no secrets). */
export interface PacStageSummary {
  name: string
  kind: string
  jobCount: number
}

/**
 * Result of previewing/validating the repo's `.pipewright.yml` at a chosen ref.
 * - found=false: the file does not exist at that ref (or repo unreadable) — runs fall back to the UI pipeline.
 * - found=true, valid=false: the file exists but failed to parse/validate; `error` carries the
 *   server's human-readable, secret-free message; `stages` is empty.
 * - found=true, valid=true: `stages`/`stageCount` summarize what the runtime would use.
 */
export interface PacPreviewResult {
  found: boolean
  valid: boolean
  ref: string
  file: string
  error: string
  stageCount: number
  stages: PacStageSummary[]
}

/**
 * Fetch & validate the repo's `.pipewright.yml` at `ref` (defaults to the project's default
 * branch when omitted). Read-only; never returns secrets or the repo URL credentials.
 */
export async function previewPacConfig(id: string, ref?: string): Promise<PacPreviewResult> {
  const qs = ref && ref.trim() ? `?ref=${encodeURIComponent(ref.trim())}` : ''
  return http.get<PacPreviewResult>(`/api/projects/${id}/pac/preview${qs}`)
}

/**
 * 项目本地磁盘缓存(GET /api/projects/{id}/cache)。
 *
 * 两处按项目分格的缓存:
 *   - repo:代码管理区的 bare 镜像(构建只增量 fetch,怀疑和远程不一致时要能重来)
 *   - deps:每项目固定依赖缓存(Maven 的 .m2 里存着 SNAPSHOT,私服重发过也可能拿到旧的那份)
 *
 * 未启用那一处(启动时 PIPEWRIGHT_NO_REPO_CACHE / PIPEWRIGHT_NO_PROJECT_CACHE)不会出现在 items 里。
 */
export type CacheKind = 'repo' | 'deps'

export interface ProjectCacheItem {
  kind: CacheKind | string
  path: string
  bytes: number
  exists: boolean
}

export interface ProjectCacheInfo {
  items: ProjectCacheItem[]
  totalBytes: number
}

/** 清除结果:bytes 是删除前的体积 = 本次释放量;existed=false 表示原本就是空的。 */
export interface ClearedCacheItem {
  kind: CacheKind | string
  path: string
  bytes: number
  existed: boolean
}

export interface ClearedCacheResult {
  items: ClearedCacheItem[]
  totalBytes: number
}

export async function getProjectCache(id: string): Promise<ProjectCacheInfo> {
  return http.get<ProjectCacheInfo>(`/api/projects/${encodeURIComponent(id)}/cache`)
}

/**
 * 清除项目本地缓存。kinds 省略 = 清本实例启用的全部。
 * 该项目正在构建时服务端回 409(cache_busy);未绑仓库偏要清 repo 回 400(repo_not_bound)。
 */
export async function clearProjectCache(id: string, kinds?: string[]): Promise<ClearedCacheResult> {
  return http.post<ClearedCacheResult>(
    `/api/projects/${encodeURIComponent(id)}/cache/clear`,
    kinds && kinds.length ? { kinds } : {},
  )
}

// ─── 复制项目(单个 / 一次 N 个)─────────────────────────────────────────────
//
// 搬的是配置,不是历史:副本的流水线、定时、参数、通知一套整套照抄,运行记录一条不带。
// 两个端点回执形状相同(都是 CopiedProject[]),所以刷新列表的逻辑只写一遍。
//
// POST /api/projects/{id}/copy    → 单个。权限由服务端按 URL 认出源项目后判定。
// POST /api/project-copies        → 一批。源 ID 全在请求体里,服务端逐条判 ——
//   走集合式 POST 而不是 /groups/{id}/copy-projects,是因为「未归组」的 groupId 是空串,
//   塞不进 URL 路径;复制未归组那一档会 404。

/** 一次复制的命名意图:显式名优先;留空则「源名 + 后缀」,重名由服务端自动避让。 */
export interface CopyNaming {
  /** 仅单个复制有用(批量时每个源各自命名)。空 = 走 suffix。 */
  name?: string
  /** 原样接在源名后面的尾巴,自带分隔符。空 = 服务端默认「 副本」。 */
  suffix?: string
}

/**
 * 复制后的落组三态,与后端 targetGroupId 字段一一对应:
 *   - 字段缺省(undefined)= 各副本沿用自己源项目的分组
 *   - ''(未归组)/ 组 ID = 全部落到这一个组
 */
export interface CopyInput extends CopyNaming {
  targetGroupId?: string
}

export interface CopyManyInput extends CopyInput {
  projectIds: string[]
}

/** 一份副本的回执 —— 拿 id 刷新列表,名字以服务端说的为准(它才知道避让结果)。 */
export interface CopiedProject {
  id: string
  name: string
  groupId: string
  sourceId: string
}

export async function copyProject(id: string, input: CopyInput = {}): Promise<CopiedProject[]> {
  return http.post<CopiedProject[]>(`/api/projects/${encodeURIComponent(id)}/copy`, input)
}

export async function copyProjects(input: CopyManyInput): Promise<CopiedProject[]> {
  return http.post<CopiedProject[]>('/api/project-copies', input)
}
