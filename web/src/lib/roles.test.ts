/**
 * roles.test.ts —— 角色档位前端的契约。
 *
 * 「前端角色名单 == 后端枚举」这条防漂移断言住在 Go 侧
 * (internal/access/roles_drift_test.go):那边能直接读文件,这里没有 node 类型可用。
 * 本文件只判前端自己的逻辑:名单齐整、档位比较、缺失能力位按最严处理。
 */

import { describe, expect, it } from 'vitest'
import type { Capabilities, PermId } from '../api/auth'
import {
  ROLE_LABEL_KEY,
  ROLE_ORDER,
  ROLE_TAG_CLASS,
  ceilingOf,
  meetsRequires,
  normalizeRole,
  permAllowed,
  roleAllows,
  settingsAllowed,
} from './roles'

function caps(settings: boolean, kinds: Capabilities['kinds']): Capabilities {
  return { settings, kinds }
}

/** 带功能点的能力位;后端 perms.go 那张表的投影就是这个形状。 */
function capsWith(settings: boolean, kinds: Capabilities['kinds'], perms: PermId[]): Capabilities {
  return { settings, kinds, perms }
}

describe('角色名单', () => {
  it('每个角色都有展示键与标签样式', () => {
    for (const r of ROLE_ORDER) {
      expect(ROLE_LABEL_KEY[r]).toMatch(/^adminUsers\.role/)
      expect(ROLE_TAG_CLASS[r]).toMatch(/^tag--/)
    }
  })

  it('未知名归一到 user,枚举内原样保留', () => {
    expect(normalizeRole('superuser')).toBe('user')
    expect(normalizeRole('')).toBe('user')
    expect(normalizeRole('ops')).toBe('ops')
  })
})

describe('档位判断', () => {
  const viewer = caps(false, { project: 'view', run: 'view', server: 'view', kube_cluster: 'view' })
  const developer = caps(false, { project: 'operate', run: 'operate', server: 'view', kube_cluster: 'view' })
  const admin = caps(true, { project: 'manage', run: 'manage', server: 'manage', kube_cluster: 'manage' })

  it('只读角色任何写动作都不放行', () => {
    expect(roleAllows(viewer, 'server', 'view')).toBe(true)
    expect(roleAllows(viewer, 'server', 'operate')).toBe(false)
    expect(roleAllows(viewer, 'project', 'operate')).toBe(false)
  })

  it('开发者可改项目、不可动主机', () => {
    expect(roleAllows(developer, 'project', 'operate')).toBe(true)
    expect(roleAllows(developer, 'run', 'operate')).toBe(true)
    expect(roleAllows(developer, 'server', 'operate')).toBe(false)
    expect(roleAllows(developer, 'kube_cluster', 'operate')).toBe(false)
  })

  it('manage 不看功能档位(归属由分组决定)', () => {
    expect(roleAllows(viewer, 'project', 'manage')).toBe(true)
    expect(roleAllows(undefined, 'project', 'manage')).toBe(true)
  })

  it('缺能力位按只读处理,设置入口默认关', () => {
    expect(ceilingOf(undefined, 'server')).toBe('view')
    expect(ceilingOf(caps(false, {}), 'server')).toBe('view')
    expect(roleAllows(caps(false, {}), 'run', 'operate')).toBe(false)
    expect(settingsAllowed(undefined)).toBe(false)
    expect(settingsAllowed(viewer)).toBe(false)
    expect(settingsAllowed(admin)).toBe(true)
  })
})

describe('功能门(meta.requires 与菜单共用)', () => {
  const ops = caps(false, { project: 'view', run: 'operate', server: 'operate', kube_cluster: 'operate' })
  const admin = caps(true, { project: 'manage', run: 'manage', server: 'manage', kube_cluster: 'manage' })

  it('无 requires 一律放行', () => {
    expect(meetsRequires(undefined)).toBe(true)
    expect(meetsRequires(admin, {})).toBe(true)
  })

  it('settings 门只看 settings 能力位', () => {
    expect(meetsRequires(ops, { settings: true })).toBe(false)
    expect(meetsRequires(admin, { settings: true })).toBe(true)
    expect(meetsRequires(undefined, { settings: true })).toBe(false)
  })

  it('kind+act 门按档位比较(运维终端要 server/operate)', () => {
    expect(meetsRequires(ops, { kind: 'server', act: 'operate' })).toBe(true)
    expect(meetsRequires(admin, { kind: 'server', act: 'operate' })).toBe(true)
    expect(meetsRequires(caps(false, { server: 'view' }), { kind: 'server', act: 'operate' })).toBe(false)
    expect(meetsRequires(undefined, { kind: 'server', act: 'operate' })).toBe(false)
  })
})

describe('功能点门(入口级可见性)', () => {
  // 开发者真实点集的摘录:编排可改、落点只读。
  const dev = capsWith(
    false,
    { project: 'operate', run: 'operate', server: 'view', kube_cluster: 'view' },
    ['dashboard.view', 'project.view', 'project.edit', 'run.view', 'server.view', 'container.view'],
  )

  it('缺 perms 一律不放行:能力位没带这一项时菜单宁可整块不亮', () => {
    expect(permAllowed(undefined, 'project.view')).toBe(false)
    expect(permAllowed(caps(false, { project: 'operate' }), 'project.view')).toBe(false)
    expect(meetsRequires(caps(false, { project: 'operate' }), { perm: 'project.view' })).toBe(false)
  })

  it('字典里没有的点不放行(手抄错的点不会误亮入口)', () => {
    expect(permAllowed(dev, 'no.such.perm' as PermId)).toBe(false)
  })

  it('同一类资源下的两处入口能分开:看得见容器状态,登不上机器', () => {
    expect(meetsRequires(dev, { perm: 'container.view' })).toBe(true)
    expect(meetsRequires(dev, { perm: 'server.exec' })).toBe(false)
    // 这条差异按旧的 kind+act 表达不出来:两处入口同属 server,上限都是 view。
    expect(meetsRequires(dev, { kind: 'server', act: 'view' })).toBe(true)
  })

  it('settings 门与 settings.access 点同源(两种写法判的是同一条线)', () => {
    const adminish = capsWith(true, { project: 'manage' }, ['settings.access'])
    expect(meetsRequires(adminish, { settings: true })).toBe(true)
    expect(meetsRequires(adminish, { perm: 'settings.access' })).toBe(true)
    expect(meetsRequires(dev, { settings: true })).toBe(false)
    expect(meetsRequires(dev, { perm: 'settings.access' })).toBe(false)
  })

  it('只读角色任何 operate 点都不放行', () => {
    const viewer = capsWith(
      false,
      { project: 'view', run: 'view', server: 'view', kube_cluster: 'view' },
      ['dashboard.view', 'project.view', 'run.view', 'server.view', 'container.view'],
    )
    for (const id of ['project.edit', 'run.operate', 'server.exec', 'container.operate'] as PermId[]) {
      expect(meetsRequires(viewer, { perm: id })).toBe(false)
    }
    expect(meetsRequires(viewer, { perm: 'container.view' })).toBe(true)
  })
})
