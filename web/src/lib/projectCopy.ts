/**
 * 「复制项目」弹窗的纯逻辑:命名预览、请求体三态、勾选集合、错误口径。
 *
 * 三条规矩都收在这儿,不散在模板里:
 *   - **副本名最终以服务端回执为准**。重名避让(「网关 副本」→「网关 副本 2」)是后端的活
 *     (internal/projectcopy 的 resolveName),这里只做「源名 + 后缀」的预览。客户端把避让
 *     再算一遍等于同一件事写两处,迟早和库里那份走样。
 *   - **落组是三态,不是两态**:字段缺省 = 每个副本沿用它自己源项目的组,`''` = 真的落到未归组,
 *     组 ID = 全部落进那个组。空串本身是合法去向,不能兼作「没选」,所以下拉用哨兵 GROUP_KEEP
 *     (和筛选下拉同一套做法,见 lib/groupFilter.ts)。
 *   - **批量时显式名一律不发**:N 个源共用一个名字会一次建出 N 份同名项目,而用户想复制的是
 *     一组的成员。与其发出去让服务端照收,不如在这里就不拼进请求体。
 */
import type { CopiedProject, CopyInput, CopyManyInput, Project } from '../api/projects'
import type { Group } from '../api/groups'
import type { GroupOption } from './groupFilter'
import { GROUP_NONE } from './groupFilter'

/** 与后端 projectcopy.DefaultSuffix 同字面量:后缀留空时后端补的也是这个。 */
export const COPY_DEFAULT_SUFFIX = ' 副本'

/** 「沿用源项目的分组」的哨兵:空串是「未归组」这个合法值,不能拿来表示「不动」。 */
export const GROUP_KEEP = '__keep__'

export interface CopyForm {
  /** 显式副本名,只在复制一份时进请求体;批量一律忽略。 */
  name: string
  /** 自动命名的尾巴,原样接在源名后面(自带分隔符,所以前导空格要留着)。空 = 后端默认。 */
  suffix: string
  /** GROUP_KEEP / GROUP_NONE(未归组)/ 分组 ID。 */
  target: string
}

/** 一行「源 → 副本」的预览:弹窗列表与成功后的回执共用。 */
export interface CopyRow {
  sourceId: string
  sourceName: string
  /** 预览名。真撞了名服务端会补序号,所以这是预览不是保证。 */
  copyName: string
  /** 该副本落在哪个组;'' = 未归组。选「沿用」时就是源项目自己的组。 */
  targetGroupId: string
}

export function emptyCopyForm(): CopyForm {
  return { name: '', suffix: COPY_DEFAULT_SUFFIX, target: GROUP_KEEP }
}

/** 后端把后缀的**尾部**空白削掉再拼(全空则退回默认),预览要同一步,否则名字对不上。 */
function effectiveSuffix(form: CopyForm): string {
  const tail = form.suffix.replace(/[ \t]+$/, '')
  return tail === '' ? COPY_DEFAULT_SUFFIX : tail
}

/**
 * 副本预览名。`explicit` 只在「单份且用户手写了名字」时由调用方置 true ——
 * 批量时每个源各自命名,手写的名字没有意义。
 */
export function previewCopyName(sourceName: string, form: CopyForm, explicit = false): string {
  const named = form.name.trim()
  if (explicit && named !== '') return named
  return `${sourceName}${effectiveSuffix(form)}`
}

/** 要发的 targetGroupId:undefined = 干脆不发这个字段(各副本沿用源项目的组)。 */
export function targetGroupIdOf(target: string): string | undefined {
  if (target === GROUP_KEEP) return undefined
  return target === GROUP_NONE ? '' : target
}

/**
 * 命名部分:`explicitAllowed` 为真(只有一份源)且填了名 → 只发 name;
 * 其余只发 suffix,让服务端给每个源各自命名。suffix 原样带上
 * (空串也带:后端对空串就是补默认值,不必在前端猜)。
 */
export function namingFields(explicitAllowed: boolean, form: CopyForm): Pick<CopyInput, 'name' | 'suffix'> {
  const named = form.name.trim()
  if (explicitAllowed && named !== '') return { name: named }
  return { suffix: form.suffix }
}

/** 去重且保序:同一项目在一批里出现两次会让命名避让白转一圈,第二份没有任何用户意图支撑。 */
export function uniqueProjectIds(sources: Project[]): string[] {
  const out: string[] = []
  for (const p of sources) {
    if (p.id !== '' && !out.includes(p.id)) out.push(p.id)
  }
  return out
}

/** 「源 → 副本」预览行;sources 就是这次要复制的那批(单个时长度为 1)。 */
export function copyRows(sources: Project[], form: CopyForm): CopyRow[] {
  const explicit = sources.length === 1
  const forced = targetGroupIdOf(form.target)
  return sources.map((p) => ({
    sourceId: p.id,
    sourceName: p.name,
    copyName: previewCopyName(p.name, form, explicit),
    targetGroupId: forced ?? p.groupId,
  }))
}

/** 单个复制的请求体(POST /api/projects/{id}/copy;源 ID 在 URL 里)。 */
export function buildCopyBody(form: CopyForm): CopyInput {
  const body: CopyInput = { ...namingFields(true, form) }
  const group = targetGroupIdOf(form.target)
  // 缺字段和发 "" 是两件事:前者「沿用源项目的组」,后者「落到未归组」,所以这里不加键。
  if (group !== undefined) body.targetGroupId = group
  return body
}

/**
 * 一批复制的请求体(POST /api/project-copies)。
 *
 * 这个端点没有 name 字段,所以显式名一律不发 —— 发出去服务端会安静丢掉,
 * 用户以为副本叫「统一名」,实际拿到 N 份「源名 副本」。一份都这么绕不过去:
 * 只有一份源时调用方应当走单个端点。
 */
export function buildCopyManyBody(sources: Project[], form: CopyForm): CopyManyInput {
  const body: CopyManyInput = { projectIds: uniqueProjectIds(sources), ...namingFields(false, form) }
  const group = targetGroupIdOf(form.target)
  if (group !== undefined) body.targetGroupId = group
  return body
}

/**
 * 目标分组候选:沿用 / 未归组 / 我有管理权的组。
 * 「沿用」不下发字段,所以列不出组的可管性也无所谓;真要挪进别人的组才需要 Manage 权,
 * 那一条判定服务端还会再判一次(见 internal/httpapi/project_copy.go)。
 */
export function targetGroupOptions(
  groups: Group[],
  labels: { keep: string; ungrouped: string; public: string; private: string },
): GroupOption[] {
  return [
    { value: GROUP_KEEP, label: labels.keep },
    { value: GROUP_NONE, label: labels.ungrouped },
    ...groups
      .filter((g) => g.canManage)
      .map((g) => ({
        value: g.id,
        label: `${g.name} · ${g.visibility === 'public' ? labels.public : labels.private}`,
      })),
  ]
}

export function toggleSelected(ids: string[], id: string): string[] {
  return ids.includes(id) ? ids.filter((x) => x !== id) : [...ids, id]
}

/** 全选(true)/ 清空(false),返回集合本身供模板直接赋值。 */
export function selectAllIds(sources: Project[], checked: boolean): string[] {
  return checked ? sources.map((p) => p.id) : []
}

/** 勾选集合里已经不在候选里的 ID(列表刷新后弹窗还开着):留着会让请求体带上线上不存在的项目。 */
export function pruneSelected(ids: string[], sources: Project[]): string[] {
  const alive = new Set(sources.map((p) => p.id))
  return ids.filter((x) => alive.has(x))
}

/** 一次点击复制了 N 份,回执要报总数:只说「已复制」会让人以为只出一个,回头找不着第二个副本。 */
export function summarizeCopies(created: CopiedProject[]): { count: number; names: string[] } {
  return { count: created.length, names: created.map((c) => c.name) }
}

/** 复制失败的文案口径:按后端契约码分派,认不出的回落到服务端消息。 */
export interface CopyErrorLabels {
  forbidden: string
  sourceGone: string
  tooMany: string
  groupGone: string
  network: string
}

export function copyErrorMessage(
  code: string | null | undefined,
  status: number,
  serverMessage: string,
  labels: CopyErrorLabels,
): string {
  if (status === 0) return labels.network
  switch (code) {
    case 'forbidden':
    case 'role_denied':
    case 'group_missing':
      return labels.forbidden
    case 'project_not_found':
    case 'not_found':
      return labels.sourceGone
    case 'too_many_copies':
      return labels.tooMany
    case 'group_not_found':
      return labels.groupGone
    default:
      return serverMessage
  }
}
