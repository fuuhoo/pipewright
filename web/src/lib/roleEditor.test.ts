/**
 * roleEditor.test.ts —— 角色编辑器纯逻辑的契约。
 *
 * 这里每一条都对应后端一个 4xx 或一条静默 bug,断言的意义是「前端别靠撞状态码学规则」:
 *   - validateRoleName:空 / 超 40 rune / 重名 / 占用内置 admin 的 id → role.Create 的 400 / 409。
 *   - seedFromTemplate、SETTINGS_POINT:自定义角色拿到 settings.access 是 422,
 *     所以复制与建模板起手时就要把它剥干净。
 *   - canDeleteRole:还有人挂着就删是 409 role_in_use。
 *   - groupPoints:没见过的 kind 不许把点丢掉,否则新入口在页面上静默消失。
 *
 * 最后一组是对着 zh-CN 词条树跑的:PERM_LABEL_KEY / KIND_*_KEY 的值都是**动态 key**
 * (t(PERM_LABEL_KEY[id]) 这种写法),i18n/componentKeys.test.ts 只扫字面量 t() 调用,
 * 抓不到它们 —— 漏一个键就是页面上渲染出裸 key,所以在这里逐个真查一遍。
 */

import { describe, expect, it } from 'vitest'
import type { PermId } from '../api/auth'
import type { PermPoint, Role } from '../api/roles'
import { ROLE_LABEL_KEY } from './roles'
import {
  KIND_HINT_KEY,
  KIND_LABEL_KEY,
  KIND_ORDER,
  MAX_ROLE_NAME_LEN,
  PERM_LABEL_KEY,
  SETTINGS_POINT,
  canDeleteRole,
  groupPoints,
  labelForRoleId,
  permsDiff,
  seedFromTemplate,
  validateRoleName,
} from './roleEditor'

const base: Omit<Role, 'id' | 'name' | 'builtin'> = {
  description: '',
  baseRole: '',
  perms: [],
  userCount: 0,
  createdBy: '',
  createdAt: '',
  updatedAt: '',
}

function builtin(id: string): Role {
  return { ...base, id, name: id, builtin: true }
}

function custom(id: string, name: string, extra: Partial<Role> = {}): Role {
  return { ...base, id, name, builtin: false, ...extra }
}

/**
 * 四档预置自迁移 0063 起是 roles 表里的普通行:id 与 name 同为那个小写词,builtin 为假 ——
 * 只有内置 admin 的点集还在 Go 代码表里,因而也只有它在页面上不可改、不可删。
 */
function preset(id: string, extra: Partial<Role> = {}): Role {
  return { ...base, id, name: id, builtin: false, ...extra }
}

/** 服务端名单:内置 admin 在首,四档预置与自建角色同列其后。 */
const roster: Role[] = [
  builtin('admin'),
  preset('user'),
  preset('developer'),
  preset('ops'),
  preset('viewer'),
  custom('7f0c…', '发布值班'),
]

function point(id: PermId, kind: PermPoint['kind'], act: PermPoint['act'] = 'view'): PermPoint {
  return { id, kind, act, builtinOnly: id === SETTINGS_POINT }
}

describe('groupPoints —— 功能点分组', () => {
  const points = [
    point('settings.access', 'platform'),
    point('project.view', 'project'),
    point('server.view', 'server'),
    point('run.view', 'run'),
    point('cluster.view', 'kube_cluster'),
  ]

  it('按 KIND_ORDER 排,不受接口返回顺序影响', () => {
    expect(groupPoints(points).map((g) => g.kind)).toEqual(KIND_ORDER)
  })

  it('每组带展示键与补语键', () => {
    for (const g of groupPoints(points)) {
      expect(g.labelKey).toBe(KIND_LABEL_KEY[g.kind])
      expect(g.hintKey).toBe(KIND_HINT_KEY[g.kind])
    }
  })

  it('空组不渲染(后端少给一类不会留一个空标题)', () => {
    expect(groupPoints([point('project.view', 'project')]).map((g) => g.kind)).toEqual(['project'])
  })

  it('没见过的 kind 归到末尾而不是丢掉(Go 侧加类别时页面仍列得出)', () => {
    const withNew = [...points, point('artifact.view' as PermId, 'artifact' as PermPoint['kind'])]
    const groups = groupPoints(withNew)
    expect(groups.map((g) => g.kind)).toEqual([...KIND_ORDER, 'artifact'])
    expect(groups[groups.length - 1].points.map((p) => p.id)).toEqual(['artifact.view'])
  })

  it('一个点都没有时返回空列表', () => {
    expect(groupPoints([])).toEqual([])
  })
})

describe('validateRoleName —— 展示名', () => {
  it('空与纯空白都要填', () => {
    expect(validateRoleName('', roster)).toBe('required')
    expect(validateRoleName('   ', roster)).toBe('required')
  })

  it('按 rune 计长度:中文不算两个', () => {
    const han = '价'.repeat(MAX_ROLE_NAME_LEN)
    expect(validateRoleName(han, roster)).toBe('')
    expect(validateRoleName(han + '值', roster)).toBe('tooLong')
    // 长度上限按去空白后的字串算。
    expect(validateRoleName(`${'a'.repeat(MAX_ROLE_NAME_LEN)}  `, roster)).toBe('')
  })

  it('占用内置 admin 的 id 单独报错(后端只查 roles 表,这一条只在前端拦)', () => {
    expect(validateRoleName('admin', roster)).toBe('reserved')
    expect(validateRoleName('Admin', roster)).toBe('reserved')
  })

  it('预置档已在 roles 表里:撞它们的名字是重名,不是保留字', () => {
    expect(validateRoleName('ops', roster)).toBe('duplicate')
    expect(validateRoleName(' VIEWER ', roster)).toBe('duplicate')
    // 于是给预置档改名也走得通(把自己排除掉就不算撞名)。
    expect(validateRoleName('主机值班', roster, 'ops')).toBe('')
  })

  it('重名大小写不敏感,改名时放过自己', () => {
    expect(validateRoleName('发布值班', roster)).toBe('duplicate')
    expect(validateRoleName('发布值班', roster, '7f0c…')).toBe('')
    expect(validateRoleName('新名字', roster)).toBe('')
  })
})

describe('permsDiff / canDeleteRole / seedFromTemplate', () => {
  it('增删分开列,顺序跟随新点集', () => {
    const d = permsDiff(['project.view', 'run.view'], ['run.view', 'server.view', 'project.edit'])
    expect(d).toEqual({ added: ['server.view', 'project.edit'], removed: ['project.view'] })
  })

  it('点集没变就是两个空数组(保存前的确认框据此不打扰人)', () => {
    expect(permsDiff(['run.view'], ['run.view'])).toEqual({ added: [], removed: [] })
  })

  it('内置 admin 不许删;预置档已入表,和自建角色一样只看有没有人挂着', () => {
    expect(canDeleteRole(custom('x', '甲'))).toBe(true)
    expect(canDeleteRole(custom('x', '甲', { userCount: 2 }))).toBe(false)
    expect(canDeleteRole(preset('viewer'))).toBe(true)
    expect(canDeleteRole(preset('user', { userCount: 7 }))).toBe(false)
    expect(canDeleteRole(builtin('admin'))).toBe(false)
  })

  it('从模板带点集时剥掉 settings.access(复制 admin 造不出管理员)', () => {
    const admin = builtin('admin')
    admin.perms = ['dashboard.view', SETTINGS_POINT, 'server.exec']
    expect(seedFromTemplate(admin)).toEqual(['dashboard.view', 'server.exec'])
    expect(seedFromTemplate(undefined)).toEqual([])
    expect(seedFromTemplate(custom('x', '甲', { perms: [] }))).toEqual([])
  })
})

describe('labelForRoleId —— 角色展示名', () => {
  const byId: Record<string, Role> = {}
  for (const r of roster) byId[r.id] = r

  /** 假 t():把已知键换成「zh:」前缀,好把「走了哪条分支」写在断言里。 */
  const t = (key: string) => `zh:${key}`

  it('自定义档直接用用户起的名字', () => {
    expect(labelForRoleId('7f0c…', byId, t)).toBe('发布值班')
  })

  it('内置 admin 走 i18n 键(库里 name 存的就是 id,不能显示裸 admin)', () => {
    expect(labelForRoleId('admin', byId, t)).toBe('zh:adminUsers.roleAdmin')
  })

  it('预置档没改过名时也走 i18n 键(判据是 name 还等于 id,不是 builtin)', () => {
    expect(labelForRoleId('ops', byId, t)).toBe('zh:adminUsers.roleOps')
  })

  it('预置档被管理员改名后显示新名字(它是库里的普通行,名字以它为准)', () => {
    const renamed: Record<string, Role> = { ...byId, ops: { ...byId.ops, name: '主机值班' } }
    expect(labelForRoleId('ops', renamed, t)).toBe('主机值班')
  })

  it('名单查不到时按 normalizeRole 兜底,不渲染裸 key 或 UUID', () => {
    expect(labelForRoleId('0e4b-uuid', byId, t)).toBe('zh:adminUsers.roleUser')
    expect(labelForRoleId('', {}, t)).toBe('zh:adminUsers.roleUser')
  })
})

describe('功能点标签在 zh-CN 真查得到(动态 t() 键的补充守卫)', () => {
  // 与 i18n/componentKeys.test.ts 同一套装配方式:zh-CN.ts + locales/zh-CN/<ns>.ts。
  const baseModules = import.meta.glob<{ default: Record<string, unknown> }>(
    '../i18n/locales/zh-CN.ts',
    { eager: true },
  )
  const pageModules = import.meta.glob<{ default: Record<string, unknown> }>(
    '../i18n/locales/zh-CN/*.ts',
    { eager: true },
  )

  function build(): Record<string, unknown> {
    const out: Record<string, unknown> = {}
    for (const mod of Object.values(baseModules)) Object.assign(out, mod.default)
    for (const [path, mod] of Object.entries(pageModules)) {
      const m = path.match(/\/zh-CN\/([^/]+)\.ts$/)
      if (m) out[m[1]] = mod.default
    }
    return out
  }

  const zhCN = build()

  function resolves(dotted: string): boolean {
    let node: unknown = zhCN
    for (const seg of dotted.split('.')) {
      if (node == null || typeof node !== 'object') return false
      node = (node as Record<string, unknown>)[seg]
    }
    return typeof node === 'string' && node.length > 0
  }

  it('PERM_LABEL_KEY 覆盖 21 个功能点,每个都能在 roleEditor.perm 下命中', () => {
    // 数量与 internal/access/perms.go 的字典同源于 PermId(Record 缺键编译不过),
    // 这里再钉一次数字:Go 侧加点时必须同时改 PermId + 这张表 + 8 份词条。
    const keys = Object.keys(PERM_LABEL_KEY)
    expect(keys.length).toBe(21)
    for (const id of keys) expect(resolves(PERM_LABEL_KEY[id as PermId])).toBe(true)
  })

  it('分组标题、名称报错与内置档标签这些动态键也已就位', () => {
    // 这些都是 t(变量) 的写法:componentKeys.test.ts 扫不到,漏词条就是页面渲染裸 key。
    const dynamic = [
      ...Object.values(KIND_LABEL_KEY),
      ...Object.values(KIND_HINT_KEY),
      'roleEditor.nameErrRequired',
      'roleEditor.nameErrTooLong',
      'roleEditor.nameErrDuplicate',
      'roleEditor.nameErrReserved',
      ...Object.values(ROLE_LABEL_KEY),
    ]
    for (const k of dynamic) expect(resolves(k)).toBe(true)
  })
})
