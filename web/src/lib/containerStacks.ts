/**
 * 容器总览「按 Stack 查看」的纯逻辑。
 *
 * Stack 就是 compose 项目(容器标签 `com.docker.compose.project`)。后端已经把这一列从
 * `docker ps --format {{json .}}` 的 Labels 串里解析出来、随每个容器一起下发
 * (见 internal/httpapi/server_containers.go 的 composeProjectOf),所以这里**纯前端派生**:
 * 不需要为每台机器多跑一次 SSH,也自动跟着那条逐台流式聚合一起刷新。
 *
 * 与「按主机」视图的实质差别:同名项目**跨主机算同一条**。这正是平台自己发 compose 时的形态
 * (deploy_docker 的 stackName 一次铺 N 台),按机器摊开反而看不出「这一条栈到底跑到哪几台了」。
 * 没有 compose 标签的容器(裸 `docker run`)单独归一档,保证一条都不丢。
 */
import type { ContainerInfo, ServerContainers } from '../api/containers'
import { matchesContainer, type HostFields } from './containerListFilter'
import { stateBucket, type StateBucket } from './containerState'

/**
 * 「独立容器」这一档的 key。真实项目名不会撞上它:compose 只接受 `[a-z0-9][a-z0-9._-]*`,
 * 而这里带了大写和 `:`。展示时由页面换成文案,不参与搜索命中。
 */
export const NO_STACK = 'unstacked:~'

export interface StackRow {
  serverId: string
  container: ContainerInfo
}

export interface StackGroup {
  /** compose 项目名;独立容器那一档为 NO_STACK。 */
  key: string
  /** 展示名:项目名原文(独立容器档为空串,页面自行换文案)。 */
  name: string
  rows: StackRow[]
  /** 涉及的宿主(按聚合入参顺序去重;页面已排成在线优先,这里不重排)。 */
  hosts: string[]
  running: number
  total: number
}

/** 机器名 / 机器地址由页面供给(聚合结果只有 serverId,名字在服务器名单里)。 */
export type HostLookup = (serverId: string) => HostFields

/**
 * 卡片渲染一条 stack 所需的宿主展示信息。`group` 是宿主机所属分组名:容器没有自己的分组,
 * 归属跟宿主走。空串 = 页面当下拿不到组名单,不贴标签(贴一个错的归属比留白更糟)。
 */
export interface StackHostView extends HostFields {
  group: string
}

/** 按 compose 项目跨主机聚合;空项目名的容器归 NO_STACK 档。 */
export function buildStackGroups(groups: readonly ServerContainers[]): StackGroup[] {
  const byKey = new Map<string, StackGroup>()
  for (const g of groups) {
    for (const c of g.containers) {
      const key = c.project || NO_STACK
      let stack = byKey.get(key)
      if (!stack) {
        stack = { key, name: c.project, rows: [], hosts: [], running: 0, total: 0 }
        byKey.set(key, stack)
      }
      stack.rows.push({ serverId: g.serverId, container: c })
      if (!stack.hosts.includes(g.serverId)) stack.hosts.push(g.serverId)
      stack.total++
      if (c.state === 'running') stack.running++
    }
  }
  const list = [...byKey.values()]
  // 名字大小写不敏感排序;独立容器那档没有名字,固定沉底(它是「其余都是 compose」时的噪音)。
  list.sort((a, b) => {
    if (a.key === NO_STACK) return 1
    if (b.key === NO_STACK) return -1
    return a.name.localeCompare(b.name, undefined, { sensitivity: 'base', numeric: true })
  })
  return list
}

/** 每个词都命中 stack 名 = 这条 stack 整体命中(与 hostFieldsMatch 同一套词语义)。 */
function hitsStackName(name: string, tokens: readonly string[]): boolean {
  const hay = name.toLowerCase()
  return tokens.every((token) => hay.includes(token))
}

/**
 * 这张 Stack 卡片该不该出现在搜索结果里:整条名字命中,或任一行命中
 * (容器名/镜像/ID/端口/状态/所在机器名/机器 IP)。名字命中就放行整条 —— 搜「pipewright」
 * 只想看这一条栈铺在哪几台,不该因为某台上的容器名里没有这个词而少一行。
 */
export function stackHasMatch(stack: StackGroup, hostOf: HostLookup, tokens: readonly string[]): boolean {
  if (tokens.length === 0) return true
  if (stack.key !== NO_STACK && hitsStackName(stack.name, tokens)) return true
  return stack.rows.some((row) => matchesContainer(row.container, hostOf(row.serverId), tokens))
}

/**
 * 卡片级命中:这一状态档里还有没有行。
 * 按 Stack 时卡片单位是「一条项目」,实测 49 张卡只装 8 个停止容器 —— 状态筛在这里必须连卡片
 * 一起收,否则等于让用户点 41 次「共 N 个容器,当前筛选下没有要显示的行」。
 * 按主机视图不这么做:那张卡还担着镜像 tab 和机器本身的状态,收掉会丢信息。
 */
export function stackHasRows(stack: StackGroup, bucket: StateBucket | 'all'): boolean {
  if (bucket === 'all') return true
  return stack.rows.some((row) => stateBucket(row.container.state) === bucket)
}

/** 卡片内行级命中:与卡片同判据,再叠「整条名字命中」的放行。 */
export function rowMatchesStack(
  stack: StackGroup,
  row: StackRow,
  host: HostFields,
  tokens: readonly string[],
): boolean {
  if (tokens.length === 0) return true
  if (stack.key !== NO_STACK && hitsStackName(stack.name, tokens)) return true
  return matchesContainer(row.container, host, tokens)
}
