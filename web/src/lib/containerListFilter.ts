/**
 * 容器总览的「搜索 + 分页」纯逻辑。
 *
 * 搜索是 Portainer 式的单框多字段:一个词命中 容器名 / 镜像 / 容器 ID / 端口映射 / 状态 /
 * 机器名 / 机器 IP 任意一项即算命中;多个词之间取 AND(每词各自命中,词序无关)。
 *
 * 机器内网 IP(172.17.x)不在这里:聚合列表来自 `docker ps --format {{json .}}`,那一列压根
 * 没有 IP,要拿就得每台每 12s 多跑一次 `docker inspect`。端口映射列里的宿主绑定地址
 * (`0.0.0.0:80->80/tcp`)和机器 IP 已经够定位「哪台机器上的哪个口」。
 */
import type { ContainerInfo } from '../api/containers'

/** 一张卡片对应的机器可搜字段。 */
export interface HostFields {
  name: string
  /** `serverHost()` 形态:`user@172.17.4.75:22`(卡头显示的就是它)。 */
  host: string
}

/** 档位:0 = 一次摊完不分页。 */
export const PAGE_ALL = 0

export const PAGE_SIZE_OPTIONS = [10, 25, 50, PAGE_ALL] as const

/** 空白切成小写词;连续空白算一个词。 */
export function queryTokens(raw: string): string[] {
  return raw.trim().toLowerCase().split(/\s+/).filter(Boolean)
}

/**
 * 机器地址去掉 `:22` 尾 —— 每台都是 22,留着当搜索词会命中一切,只有噪音没有筛选力。
 * 无端口尾巴(或尾巴不是数字,即登记的本来就是 host[:port] 之外的形态)原样返回。
 */
export function hostAddress(host: string): string {
  const i = host.lastIndexOf(':')
  if (i <= 0) return host
  return /^\d+$/.test(host.slice(i + 1)) ? host.slice(0, i) : host
}

/** 机器名 / 机器 IP 是否命中(每个词都要命中其一)。 */
export function hostFieldsMatch(host: HostFields, tokens: readonly string[]): boolean {
  if (tokens.length === 0) return true
  const hay = [host.name, hostAddress(host.host)].map((s) => (s ?? '').toLowerCase())
  return tokens.every((token) => hay.some((field) => field.includes(token)))
}

/**
 * 一台机器该不该在搜索结果里出现:有容器命中,或机器名/IP 本身命中。
 * 后者是为了「输入 IP 找机器」这条路径 —— 不可达的机器压根没有容器清单,只按容器命中的话
 * 搜它的 IP 会连这张卡都看不到,像搜索没覆盖 IP。
 */
export function hostHasMatch(
  containers: readonly ContainerInfo[],
  host: HostFields,
  tokens: readonly string[],
): boolean {
  if (tokens.length === 0) return true
  return containers.some((c) => matchesContainer(c, host, tokens)) || hostFieldsMatch(host, tokens)
}

/** 单容器 × 搜索词命中判断(空词表 = 不筛,一律 true)。 */
export function matchesContainer(
  c: ContainerInfo,
  host: HostFields,
  tokens: readonly string[],
): boolean {
  if (tokens.length === 0) return true
  const hay = [c.names, c.image, c.id, c.ports, c.status, c.state, host.name, hostAddress(host.host)]
    .map((s) => (s ?? '').toLowerCase())
  return tokens.every((token) => hay.some((field) => field.includes(token)))
}

export interface PageSlice<T> {
  items: T[]
  /** 夹取到合法区间后的当前页(1 起)。 */
  page: number
  pages: number
  total: number
  /** 这一页覆盖的全局行号区间(1 起,含两端);空列表为 0–0。 */
  from: number
  to: number
}

/**
 * 分页单位是「一张卡片里的容器行」:卡片按机器分组,行才是长尾。
 * size<=0 视为不分页;页码越界(容器被删导致列表变短)自动夹回最后一页,调用方拿 `page` 回写。
 */
export function paginate<T>(list: readonly T[], page: number, size: number): PageSlice<T> {
  const total = list.length
  if (size <= 0) {
    return { items: [...list], page: 1, pages: 1, total, from: total ? 1 : 0, to: total }
  }
  const pages = Math.max(1, Math.ceil(total / size))
  const want = Math.trunc(page)
  const current = Math.min(Math.max(want > 0 ? want : 1, 1), pages)
  const start = (current - 1) * size
  const items = list.slice(start, start + size)
  return { items, page: current, pages, total, from: total ? start + 1 : 0, to: start + items.length }
}
