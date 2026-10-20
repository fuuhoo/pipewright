import { describe, expect, it } from 'vitest'

import type { ContainerInfo } from '../api/containers'
import {
  PAGE_ALL,
  hostAddress,
  hostHasMatch,
  matchesContainer,
  paginate,
  queryTokens,
  type HostFields,
} from './containerListFilter'

function ctr(over: Partial<ContainerInfo> = {}): ContainerInfo {
  return {
    id: 'a1b2c3d4e5f60718293a4b5c6d7e8f9011223344',
    names: 'pw-web',
    image: 'nginx:1.27',
    state: 'running',
    status: 'Up 2 hours',
    ports: '0.0.0.0:8080->80/tcp',
    createdAt: '2026-10-01T00:00:00Z',
    ...over,
  } as ContainerInfo
}

const host: HostFields = { name: '75', host: 'root@172.17.4.75:22' }

describe('queryTokens', () => {
  it('切词并小写,空白折叠', () => {
    expect(queryTokens('  Nginx   8080 ')).toEqual(['nginx', '8080'])
  })

  it('空输入 = 不筛', () => {
    expect(queryTokens('')).toEqual([])
    expect(queryTokens('   ')).toEqual([])
  })
})

describe('hostAddress', () => {
  it('去掉端口尾巴,保留 user@ip', () => {
    expect(hostAddress('root@172.17.4.75:22')).toBe('root@172.17.4.75')
  })

  it('没有端口或尾巴不是数字则原样返回', () => {
    expect(hostAddress('root@172.17.4.75')).toBe('root@172.17.4.75')
    expect(hostAddress('')).toBe('')
  })
})

describe('matchesContainer', () => {
  it('空词表一律命中(不筛时全量展示)', () => {
    expect(matchesContainer(ctr(), host, [])).toBe(true)
  })

  it('名称 / 镜像 / 机器名 / 机器 IP 任一命中即算命中', () => {
    expect(matchesContainer(ctr(), host, queryTokens('pw-web'))).toBe(true)
    expect(matchesContainer(ctr(), host, queryTokens('NGINX'))).toBe(true)
    expect(matchesContainer(ctr(), host, queryTokens('75'))).toBe(true)
    expect(matchesContainer(ctr(), host, queryTokens('172.17.4.75'))).toBe(true)
    expect(matchesContainer(ctr(), host, queryTokens('root@172.17.4.75'))).toBe(true)
  })

  it('容器 ID / 端口映射 / 状态也算「等相关信息」', () => {
    expect(matchesContainer(ctr(), host, queryTokens('a1b2c3d4'))).toBe(true)
    expect(matchesContainer(ctr(), host, queryTokens('8080'))).toBe(true)
    expect(matchesContainer(ctr(), host, queryTokens('Up'))).toBe(true)
    expect(matchesContainer(ctr({ state: 'exited', status: 'Exited (0) 3 days ago' }), host, queryTokens('exited'))).toBe(true)
  })

  it('端口号不当搜索词:22 不会因为「每台都监听 22」就命中一切', () => {
    expect(matchesContainer(ctr(), host, queryTokens(':22'))).toBe(false)
  })

  it('多词取 AND(每词各自命中,词序无关)', () => {
    expect(matchesContainer(ctr(), host, queryTokens('nginx 8080'))).toBe(true)
    expect(matchesContainer(ctr(), host, queryTokens('8080 nginx 75'))).toBe(true)
    expect(matchesContainer(ctr(), host, queryTokens('nginx 9090'))).toBe(false)
  })

  it('不命中给 false', () => {
    expect(matchesContainer(ctr(), host, queryTokens('redis'))).toBe(false)
  })

  it('字段缺失(旧聚合结果)不炸', () => {
    const partial = { names: 'x', image: '', state: 'running', status: '', ports: '', id: '' } as ContainerInfo
    expect(matchesContainer(partial, { name: '', host: '' }, queryTokens('x'))).toBe(true)
    expect(matchesContainer(partial, { name: '', host: '' }, queryTokens('y'))).toBe(false)
  })
})

describe('hostHasMatch', () => {
  it('没有容器命中时整张卡片该隐藏', () => {
    const list = [ctr(), ctr({ names: 'db', image: 'postgres:16' })]
    expect(hostHasMatch(list, host, queryTokens('postgres'))).toBe(true)
    expect(hostHasMatch(list, host, queryTokens('redis'))).toBe(false)
    expect(hostHasMatch([], host, queryTokens('nginx'))).toBe(false)
  })

  it('机器名/IP 本身命中则卡片留着(搜 IP 要能找回那台机器)', () => {
    // 不可达的机器没有容器清单,只按容器命中的话它的 IP 就搜不到了。
    expect(hostHasMatch([], host, queryTokens('172.17.4.75'))).toBe(true)
    expect(hostHasMatch([], host, queryTokens('75'))).toBe(true)
    // 但要求每个词都落在机器字段上,不是沾点 IP 边就放行。
    expect(hostHasMatch([], host, queryTokens('172.17 redis'))).toBe(false)
  })

  it('不搜时任何机器都算有(一台空机器也照旧展示)', () => {
    expect(hostHasMatch([], host, [])).toBe(true)
  })
})

describe('paginate', () => {
  const list = Array.from({ length: 13 }, (_, i) => i + 1)

  it('第一页给前 size 条并报出行号区间', () => {
    const p = paginate(list, 1, 10)
    expect(p.items).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9, 10])
    expect(p).toMatchObject({ page: 1, pages: 2, total: 13, from: 1, to: 10 })
  })

  it('末页给剩下的,行号连续', () => {
    const p = paginate(list, 2, 10)
    expect(p.items).toEqual([11, 12, 13])
    expect(p).toMatchObject({ page: 2, pages: 2, from: 11, to: 13 })
  })

  it('列表变短(容器被删/筛选变严)时页码夹回最后一页,不留空白页', () => {
    const p = paginate(list, 5, 10)
    expect(p.page).toBe(2)
    expect(p.items.length).toBe(3)
  })

  it('PAGE_ALL(0)与负数档位一次摊完', () => {
    const p = paginate(list, 1, PAGE_ALL)
    expect(p.items.length).toBe(13)
    expect(p).toMatchObject({ page: 1, pages: 1, from: 1, to: 13 })
    expect(paginate(list, 3, -1).items.length).toBe(13)
  })

  it('非法页码回落第 1 页', () => {
    expect(paginate(list, 0, 10).page).toBe(1)
    expect(paginate(list, -3, 10).page).toBe(1)
    expect(paginate(list, Number.NaN, 10).page).toBe(1)
  })

  it('空列表给 0–0,页码仍是 1(分页脚此时不显示)', () => {
    const p = paginate<number>([], 1, 10)
    expect(p.items).toEqual([])
    expect(p).toMatchObject({ page: 1, pages: 1, total: 0, from: 0, to: 0 })
  })
})
