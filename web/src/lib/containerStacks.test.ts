import { describe, expect, it } from 'vitest'

import type { ContainerInfo, ServerContainers } from '../api/containers'
import type { HostFields } from './containerListFilter'
import { NO_STACK, buildStackGroups, rowMatchesStack, stackHasMatch, stackHasRows, type StackGroup } from './containerStacks'

function ctr(over: Partial<ContainerInfo> = {}): ContainerInfo {
  return {
    id: 'a1b2c3d4e5f60718293a4b5c6d7e8f9011223344',
    names: 'pw-web',
    image: 'nginx:1.27',
    state: 'running',
    status: 'Up 2 hours',
    ports: '0.0.0.0:8080->80/tcp',
    createdAt: '2026-10-01T00:00:00Z',
    project: '',
    ...over,
  }
}

function host(serverId: string, containers: ContainerInfo[]): ServerContainers {
  return {
    serverId,
    reachable: true,
    runtime: 'docker',
    error: '',
    containers,
    running: containers.filter((c) => c.state === 'running').length,
    total: containers.length,
    collectedAt: '2026-10-22T00:00:00Z',
  }
}

const HOSTS: Record<string, HostFields> = {
  s1: { name: '75', host: 'root@172.17.4.75:22' },
  s2: { name: 'oracle', host: 'root@172.17.4.41:22' },
}
const hostOf = (id: string): HostFields => HOSTS[id]

describe('buildStackGroups', () => {
  it('同名项目跨主机合成一条,宿主清单去重', () => {
    const stacks = buildStackGroups([
      host('s1', [ctr({ project: 'pipewright', names: 'pw-1' }), ctr({ project: 'weknora', names: 'wk-1' })]),
      host('s2', [ctr({ project: 'pipewright', names: 'pw-2', state: 'exited', status: 'Exited (0) 1 hour ago' })]),
    ])
    const pw = stacks.find((s) => s.key === 'pipewright')
    expect(pw?.rows.map((r) => r.container.names)).toEqual(['pw-1', 'pw-2'])
    expect(pw?.hosts).toEqual(['s1', 's2'])
    expect(pw?.total).toBe(2)
    expect(pw?.running).toBe(1)
    expect(stacks.map((s) => s.key)).toEqual(['pipewright', 'weknora'])
  })

  it('一条栈里同主机的多个服务只算一次宿主', () => {
    const stacks = buildStackGroups([
      host('s1', [ctr({ project: 'app', names: 'app-db' }), ctr({ project: 'app', names: 'app-api' })]),
    ])
    expect(stacks[0].hosts).toEqual(['s1'])
    expect(stacks[0].total).toBe(2)
  })

  it('没有 compose 项目名的容器归入独立档,一条不丢', () => {
    const stacks = buildStackGroups([
      host('s1', [ctr({ project: 'app' }), ctr({ project: '', names: 'solo' })]),
    ])
    expect(stacks).toHaveLength(2)
    const loose = stacks.find((s) => s.key === NO_STACK)
    expect(loose?.name).toBe('')
    expect(loose?.rows.map((r) => r.container.names)).toEqual(['solo'])
  })

  it('按项目名排序(大小写不敏感、数字按数值),独立档固定沉底', () => {
    const stacks = buildStackGroups([
      host('s1', [
        ctr({ project: '', names: 'loose' }),
        ctr({ project: 'Zeta', names: 'z' }),
        ctr({ project: 'app10', names: 'a10' }),
        ctr({ project: 'alpha', names: 'al' }),
        ctr({ project: 'app2', names: 'a2' }),
      ]),
    ])
    expect(stacks.map((s) => s.key)).toEqual(['alpha', 'app2', 'app10', 'Zeta', NO_STACK])
  })

  it('没有任何容器(含不可达机器)→ 空清单', () => {
    expect(buildStackGroups([])).toEqual([])
    expect(buildStackGroups([host('s1', [])])).toEqual([])
  })
})

function sampleStack(): StackGroup {
  return buildStackGroups([
    host('s1', [ctr({ project: 'pipewright', names: 'pw-web', image: 'nginx:1.27' })]),
    host('s2', [ctr({ project: 'pipewright', names: 'pw-db', image: 'mysql:8', ports: '' })]),
  ])[0]
}

function tokens(...words: string[]): string[] {
  return words.map((w) => w.toLowerCase())
}

describe('stackHasMatch', () => {
  it('空词表 = 不筛', () => {
    expect(stackHasMatch(sampleStack(), hostOf, [])).toBe(true)
  })

  it('只命中栈名也放行整条(容器名里没有这个词)', () => {
    expect(stackHasMatch(sampleStack(), hostOf, tokens('pipewright'))).toBe(true)
  })

  it('命中某行的镜像 / 容器名 / 机器名 / 机器 IP', () => {
    const s = sampleStack()
    expect(stackHasMatch(s, hostOf, tokens('mysql'))).toBe(true)
    expect(stackHasMatch(s, hostOf, tokens('pw-web'))).toBe(true)
    expect(stackHasMatch(s, hostOf, tokens('oracle'))).toBe(true)
    expect(stackHasMatch(s, hostOf, tokens('172.17.4.75'))).toBe(true)
  })

  it('多词取 AND:每词都得命中,否则整条不出', () => {
    const s = sampleStack()
    expect(stackHasMatch(s, hostOf, tokens('mysql', 'pw-web'))).toBe(false)
    expect(stackHasMatch(s, hostOf, tokens('pipewright', 'mysql'))).toBe(true)
  })

  it('独立档不靠名字命中,只按行命中', () => {
    const loose = buildStackGroups([host('s1', [ctr({ project: '', names: 'solo' })])]).find(
      (s) => s.key === NO_STACK,
    )!
    expect(stackHasMatch(loose, hostOf, tokens('unstacked'))).toBe(false)
    expect(stackHasMatch(loose, hostOf, tokens('solo'))).toBe(true)
  })
})

describe('rowMatchesStack', () => {
  it('栈名命中时整条的每一行都留下', () => {
    const s = sampleStack()
    const rows = s.rows
    expect(rowMatchesStack(s, rows[0], hostOf('s1'), tokens('pipewright'))).toBe(true)
    expect(rowMatchesStack(s, rows[1], hostOf('s2'), tokens('pipewright'))).toBe(true)
  })

  it('否则按行自身字段判:这行不含词就不留(卡片仍可因别行出现)', () => {
    const s = sampleStack()
    expect(rowMatchesStack(s, s.rows[0], hostOf('s1'), tokens('nginx'))).toBe(true)
    expect(rowMatchesStack(s, s.rows[1], hostOf('s2'), tokens('nginx'))).toBe(false)
  })

  it('所在机器的名字 / IP 也算这一行的字段', () => {
    const s = sampleStack()
    expect(rowMatchesStack(s, s.rows[1], hostOf('s2'), tokens('41'))).toBe(true)
    expect(rowMatchesStack(s, s.rows[0], hostOf('s1'), tokens('41'))).toBe(false)
  })
})

describe('stackHasRows', () => {
  it('「全部」不收敛:任何一条栈都留在屏上', () => {
    const stacks = buildStackGroups([host('s1', [ctr({ project: 'a' })])])
    expect(stackHasRows(stacks[0], 'all')).toBe(true)
  })

  it('只要有一行落在这一档,整条栈就留着(其余行由卡片自己筛)', () => {
    const stacks = buildStackGroups([
      host('s1', [ctr({ project: 'mix' }), ctr({ project: 'mix', names: 'pw-2', state: 'exited', status: 'Exited (1) 1 min ago' })]),
    ])
    expect(stackHasRows(stacks[0], 'running')).toBe(true)
    expect(stackHasRows(stacks[0], 'stopped')).toBe(true)
    expect(stackHasRows(stacks[0], 'paused')).toBe(false)
  })

  it('整条栈都不在这一档 → 卡片一起收掉,不占一张空卡', () => {
    const stacks = buildStackGroups([host('s1', [ctr({ project: 'up', state: 'running' })])])
    expect(stackHasRows(stacks[0], 'stopped')).toBe(false)
  })

  it('restarting 算运行档,与卡片行的状态桶口径一致', () => {
    const stacks = buildStackGroups([host('s1', [ctr({ project: 'flap', state: 'restarting', status: 'Restarting (1)' })])])
    expect(stackHasRows(stacks[0], 'running')).toBe(true)
    expect(stackHasRows(stacks[0], 'stopped')).toBe(false)
  })
})
