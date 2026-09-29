/**
 * roleEditor.ts —— 角色编辑器的纯逻辑(无组件、无网络),供 admin/Roles.vue 与单测用。
 *
 * 拆出来的理由:这里每一条都对应后端一个 4xx,拦不住就是「点了才报错」——
 *   - 名字空 / 超 40  rune / 与既有角色重名(大小写不敏感)/ 占用内置 admin 的 id → 400 / 409
 *   - 勾了 settings.access → 422(编辑器把那一项渲染成禁用,根本不让人勾)
 *   - 还有账号在用就删 → 409(删除按钮直接 disable,并把人数写在旁边)
 * 后端是权威,这里只是把同一份判据提前搬到浏览器,免得用户靠撞状态码来学规则。
 *
 * 功能点的中文名走一张显式 `Record<PermId, string>`:Go 侧加一个点而这里漏配,
 * TS 编译就会在缺键上报错(单测再钉「每个键在 zh-CN 里真查得到」)。
 */

import type { PermId, ResourceAct, ResourceKind } from '../api/auth'
import type { PermPoint, Role, RoleId } from '../api/roles'
import { normalizeRole, ROLE_LABEL_KEY } from './roles'

export type PointKind = ResourceKind | 'platform'

/** 勾选框的分组顺序:编排 → 运行 → 落点(主机)→ 落点(集群)→ 平台。与 perms.go 的注释分段一致。 */
export const KIND_ORDER: PointKind[] = ['project', 'run', 'server', 'kube_cluster', 'platform']

export const KIND_LABEL_KEY: Record<PointKind, string> = {
  project: 'roleEditor.kindProject',
  run: 'roleEditor.kindRun',
  server: 'roleEditor.kindServer',
  kube_cluster: 'roleEditor.kindCluster',
  platform: 'roleEditor.kindPlatform',
}

/** 每组的补语:说明这一勾管的是哪条轴,免得「运维」这种词在不同组里两义。 */
export const KIND_HINT_KEY: Record<PointKind, string> = {
  project: 'roleEditor.kindProjectHint',
  run: 'roleEditor.kindRunHint',
  server: 'roleEditor.kindServerHint',
  kube_cluster: 'roleEditor.kindClusterHint',
  platform: 'roleEditor.kindPlatformHint',
}

/** 功能点 → 展示键。键名用下划线代替点(vue-i18n 的点路径不能带裸点)。 */
export const PERM_LABEL_KEY: Record<PermId, string> = {
  'dashboard.view': 'roleEditor.perm.dashboard_view',
  'project.view': 'roleEditor.perm.project_view',
  'project.edit': 'roleEditor.perm.project_edit',
  'library.view': 'roleEditor.perm.library_view',
  'library.edit': 'roleEditor.perm.library_edit',
  'run.view': 'roleEditor.perm.run_view',
  'run.operate': 'roleEditor.perm.run_operate',
  'environments.view': 'roleEditor.perm.environments_view',
  'metrics.dora.view': 'roleEditor.perm.metrics_dora_view',
  'server.view': 'roleEditor.perm.server_view',
  'server.exec': 'roleEditor.perm.server_exec',
  'container.view': 'roleEditor.perm.container_view',
  'container.operate': 'roleEditor.perm.container_operate',
  'cert.view': 'roleEditor.perm.cert_view',
  'preview.view': 'roleEditor.perm.preview_view',
  'preview.recycle': 'roleEditor.perm.preview_recycle',
  'anomaly.view': 'roleEditor.perm.anomaly_view',
  'anomaly.edit': 'roleEditor.perm.anomaly_edit',
  'cluster.view': 'roleEditor.perm.cluster_view',
  'cluster.operate': 'roleEditor.perm.cluster_operate',
  'settings.access': 'roleEditor.perm.settings_access',
}

/** 进得了设置的总闸:自定义角色拿不到(role.ErrSettingsPoint),内置 admin 才有。 */
export const SETTINGS_POINT: PermId = 'settings.access'

export function permLabelKey(id: PermId): string {
  return PERM_LABEL_KEY[id]
}

export interface PointGroup {
  kind: PointKind
  labelKey: string
  hintKey: string
  points: PermPoint[]
}

/**
 * 按 kind 分组,顺序固定为 KIND_ORDER。
 * 字典里冒出没见过的 kind 时不丢点:归到末尾的「其他」桶,宁可页面多一行也别让入口静默消失 ——
 * 这正是本套设计里最难查的那类 bug(勾了没反应 / 根本勾不到)。
 */
export function groupPoints(points: PermPoint[]): PointGroup[] {
  const groups: PointGroup[] = KIND_ORDER.map((kind) => ({
    kind,
    labelKey: KIND_LABEL_KEY[kind],
    hintKey: KIND_HINT_KEY[kind] ?? '',
    points: [],
  }))
  const byKind = new Map(groups.map((g) => [g.kind, g]))
  let other: PointGroup | null = null
  for (const p of points) {
    const g = byKind.get(p.kind)
    if (g) {
      g.points.push(p)
      continue
    }
    if (!other) {
      other = { kind: p.kind, labelKey: '', hintKey: '', points: [] }
      groups.push(other)
    }
    other.points.push(p)
  }
  return groups.filter((g) => g.points.length > 0)
}

/** 角色名上限,与 role.maxNameLen 同值(后端按 rune 计,中文不算两个)。 */
export const MAX_ROLE_NAME_LEN = 40

export type NameIssue = '' | 'required' | 'tooLong' | 'duplicate' | 'reserved'

/**
 * 校验展示名。`roles` 是服务端完整名单(内置 + 自定义),`selfId` 在改名时放过自己。
 *
 * reserved 这一条只在前端拦:后端 `nameTaken` 只查 roles 表,而内置 admin 永远不进那张表,
 * 所以自定一个「Admin」是有可能落库的 —— 于是「按名字读」的列表与下拉里就会同时出现内置
 * 「管理员」和一个叫 Admin 的自定义角色。不值得为它开一条后端校验路径,但也不该让人无意识地
 * 造出来,所以给它一句专门的解释。四档预置自 0063 起就在 roles 表里,撞它们的名字走 duplicate。
 */
export function validateRoleName(name: string, roles: Role[], selfId = ''): NameIssue {
  const trimmed = name.trim()
  if (!trimmed) return 'required'
  if ([...trimmed].length > MAX_ROLE_NAME_LEN) return 'tooLong'
  const lower = trimmed.toLowerCase()
  if (roles.some((r) => r.builtin && r.id.toLowerCase() === lower)) return 'reserved'
  if (roles.some((r) => r.id !== selfId && r.name.toLowerCase() === lower)) return 'duplicate'
  return ''
}

export interface PermsDiff {
  added: PermId[]
  removed: PermId[]
}

/** 点集增删(保存前的确认文案用)。顺序跟随 `to`,与后端序列化的字典序一致。 */
export function permsDiff(from: PermId[], to: PermId[]): PermsDiff {
  const fromSet = new Set(from)
  const toSet = new Set(to)
  return {
    added: to.filter((id) => !fromSet.has(id)),
    removed: from.filter((id) => !toSet.has(id)),
  }
}

/** 只有非内置且没账号挂着才允许删(后端同判据:409 role_in_use)。 */
export function canDeleteRole(role: Role): boolean {
  return !role.builtin && role.userCount === 0
}

/**
 * 从模板带出初始点集:剥掉 settings.access。
 * 与 role.Copy 同一取舍 —— 复制 admin 是常见起手式,那个点本来就不许给自定义角色,
 * 报错只会让人以为「复制坏了」。
 */
export function seedFromTemplate(base: Role | null | undefined): PermId[] {
  if (!base) return []
  return base.perms.filter((id) => id !== SETTINGS_POINT)
}

/**
 * 角色展示名,三条分支:
 *   - 名字与 id 不同 → 用那个名字(自建角色,或管理员改过名的预置档)。
 *   - 名字仍是 id 本身(内置 admin,以及 0063 播种后没人改过的四档预置)→ 走 i18n 键,
 *     否则页面会露出裸 `user` / `ops` 这种字串。
 *   - 名单里查不到(历史值 / 名单还没拉到)→ 按 normalizeRole 的口径回「普通用户」的标签,
 *     至少不渲染成裸 i18n key 或 UUID。
 *
 * `t` 由调用方注入(useI18n 的翻译函数),这样本模块不依赖 vue-i18n、单测里能直接断言结果。
 */
export function labelForRoleId(
  id: RoleId,
  rolesById: Record<string, Role>,
  t: (key: string) => string,
): string {
  const role = rolesById[id]
  if (role) {
    const key = role.name === role.id ? (ROLE_LABEL_KEY as Record<string, string>)[role.id] : ''
    return key ? t(key) : role.name
  }
  return t((ROLE_LABEL_KEY as Record<string, string>)[normalizeRole(id)] ?? 'adminUsers.roleUser')
}
