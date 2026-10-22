/**
 * apiTokens.ts —— API 令牌页的纯逻辑(名称校验 + 归属人候选 + 端点两把尺 + 客户端配置片段)。
 *
 * 刻意不 import vue-i18n:校验只回「哪一种毛病」,文案由调用方按键取。
 *
 * 名称判据与 internal/apitoken/apitoken.go 的 Create 逐条对齐(折叠空白 → 非空 → ≤128 字)。
 * 不一致的代价是「前端放行、签发回 400」,那比多写两行判据难受得多。
 *
 * 两把尺(工具面 allowedTools / 数据面 groupScope)在这里只做集合运算,判定一律留在后端:
 * 前端算出来的「档」只是快捷勾选,发出去的永远是名字与 ID 的列表。
 *
 * 配置片段对应 MCP 的两条接入线,同一把令牌、同一套工具:
 *   - stdio:客户端在**本机**拉起 `pipewright mcp`,由这个进程出去连平台(平台在内网、
 *     本机不监听任何东西时用这条);
 *   - HTTP:客户端直接连平台上的 `/mcp`(客户端本来就该得到 443 那个口时用这条)。
 * 令牌在这里只出现一次,由页面配「只显示这一次」的提示一起呈现 —— 复制走之后平台再也回显不回来。
 */

import { collapseWhitespace } from './userDisplay'

/** 后端 apitoken.MaxNameLen(按字符计,中文算一个)。 */
export const TOKEN_NAME_MAX = 128

/** 折叠内部连续空白并去首尾 —— 与后端 strings.Fields + Join(" ") 同一算法。 */
export function normalizeTokenName(raw: string): string {
  return collapseWhitespace(raw)
}

export type TokenNameProblem = '' | 'empty' | 'tooLong'

/** 名称判据的失败原因;'' 表示可以签发(入参可以是原始输入,内部先折叠)。 */
export function tokenNameProblem(raw: string): TokenNameProblem {
  const folded = normalizeTokenName(raw)
  if (!folded) return 'empty'
  // Array.from 拆的是码点:与 Go 的 []rune 口径一致(代理对算一个),按 .length 会多算半个。
  if (Array.from(folded).length > TOKEN_NAME_MAX) return 'tooLong'
  return ''
}

/** 候选里最小需要的三列;users 行满足它。 */
export interface OwnerCandidate {
  id: string
  username: string
  displayName?: string
  enabled: boolean
}

export interface OwnerOption {
  id: string
  /** 主标签:有昵称用昵称,没有用登录名。 */
  name: string
  /** 次标签:始终是 @登录名,免得两个同名昵称在列表里分不开。 */
  handle: string
}

/**
 * 归属人下拉:只列启用中的、且**不含当前登录者**。
 *
 * 排除自己不是偷懒:「签给我自己」走的是「请求里省略 userId → 后端默认当前会话」那条路,
 * 如果下拉里又出现自己那一行,就有两条通往同一结果的路,而它们的请求体并不一样 ——
 * 事后看审计会以为是两回事。
 * 禁用账号也排除:后端对禁用账号是当场拒签(ErrValidation),列表里留着只会让操作人吃一个 400。
 */
export function ownerOptions(users: OwnerCandidate[], myUsername: string): OwnerOption[] {
  return users
    .filter((u) => u.enabled && u.username !== myUsername)
    .map((u) => {
      const nickname = collapseWhitespace(u.displayName ?? '')
      return { id: u.id, name: nickname || u.username, handle: `@${u.username}` }
    })
}

/** 去掉结尾斜杠:拼 /mcp 与写进 env 都要一个干净的 base。 */
export function trimBaseURL(raw: string): string {
  return raw.replace(/\/+$/, '')
}

/** 平台上的 MCP 端点地址(后端把 /mcp 与 /mcp/* 都注册了,这里给精确的 /mcp)。 */
export function mcpEndpointURL(baseUrl: string): string {
  return `${trimBaseURL(baseUrl)}/mcp`
}

/**
 * stdio 接入片段:Claude Code 的 .mcp.json / Cursor 的 mcp.json 一类。
 *
 * 令牌放 env 而不是 args:命令行上的明文同机其他用户能从 ps 里看到,env 不会。
 * 这与 `pipewright mcp --help` 里给的那份示例是同一形状,别一处改一处不改。
 */
export function mcpStdioSnippet(baseUrl: string, plaintext: string): string {
  return JSON.stringify(
    {
      mcpServers: {
        pipewright: {
          command: 'pipewright',
          args: ['mcp'],
          env: { PIPEWRIGHT_URL: trimBaseURL(baseUrl), PIPEWRIGHT_TOKEN: plaintext },
        },
      },
    },
    null,
    2,
  )
}

/** HTTP 接入片段:客户端直连平台 /mcp,同一把令牌换成 Authorization 头。 */
export function mcpHttpSnippet(baseUrl: string, plaintext: string): string {
  return JSON.stringify(
    {
      mcpServers: {
        pipewright: {
          type: 'http',
          url: mcpEndpointURL(baseUrl),
          headers: { Authorization: `Bearer ${plaintext}` },
        },
      },
    },
    null,
    2,
  )
}

// ─── 工具范围(端点收窄的第一把尺)────────────────────────────────────────────

/** 一条 MCP 工具的描述,来自 GET /api/admin/mcp-tools(与后端 mcpsrv 名册同一份)。 */
export interface MCPToolDef {
  name: string
  title: string
  readOnly: boolean
}

/**
 * 预设档:三档里只有 all 与 readonly 是「从名册推出来的」,ops 点的是两个具体写工具。
 *
 * ops 用名字硬编码是有意的:它表达的是一条运维习惯(能看能跑,但不能改配置),
 * 后端只认名字集合、不认识「档」这个概念,所以这里点错名字最坏结果是勾出一把窄了的令牌,
 * 而不是越权。名册改名由 toolPresetNames 的兜底(只保留名册里存在的)和签发接口的 400 双重挡住。
 */
export type ToolPresetKey = 'all' | 'readonly' | 'ops'

export const TOOL_PRESETS: ToolPresetKey[] = ['all', 'readonly', 'ops']

const OPS_WRITE_TOOLS = ['trigger_run', 'cancel_run']

/** 某一档对应的工具名;all 返回空数组 = 不限(与后端的「缺省即不限」同口径)。 */
export function toolPresetNames(preset: ToolPresetKey, catalog: MCPToolDef[]): string[] {
  const known = new Set(catalog.map((tool) => tool.name))
  if (preset === 'all') return []
  const readOnly = catalog.filter((tool) => tool.readOnly).map((tool) => tool.name)
  if (preset === 'readonly') return readOnly
  return [...readOnly, ...OPS_WRITE_TOOLS.filter((name) => known.has(name))]
}

/**
 * 当前勾选恰好等于哪一档;对不上任何档返回 null(说明是手改的)。
 *
 * 判的是「集合相等」而不是「数量相等」:数量相同但换了两个工具,标成某档会误导人。
 */
export function activeToolPreset(selected: string[], catalog: MCPToolDef[]): ToolPresetKey | null {
  const chosen = new Set(selected)
  for (const preset of TOOL_PRESETS) {
    const names = toolPresetNames(preset, catalog)
    if (names.length !== chosen.size) continue
    if (names.every((name) => chosen.has(name))) return preset
  }
  return null
}

/**
 * 勾选/取消一个工具。
 *
 * 返回值按名册顺序排,不按点击顺序:限工具这把尺的效果与顺序无关,但列表要是跟着点击
 * 乱跳(勾掉再勾回就跑到末尾),人会以为顺序有含义。
 */
export function toggleTool(catalog: MCPToolDef[], selected: string[], name: string): string[] {
  const next = new Set(selected)
  if (next.has(name)) next.delete(name)
  else next.add(name)
  const order = catalog.map((tool) => tool.name)
  return order.filter((x) => next.has(x)).concat([...next].filter((x) => !order.includes(x)))
}

// ─── 分组范围(端点收窄的第二把尺)────────────────────────────────────────────

/** 一个可勾选的分组候选。 */
export interface GroupCandidate {
  id: string
  name: string
  /** 组内项目数:0 个的组点了也看不见东西,页面上标出来省得选了「空组」再来问为什么。 */
  projectCount: number
}

/** 把选中的组 ID 换成展示名;名册里没有的(组已删)原样留 ID,不静默吞掉。 */
export function groupScopeNames(selected: string[], groups: GroupCandidate[]): string[] {
  const index = new Map(groups.map((g) => [g.id, g.name]))
  return selected.map((id) => index.get(id) ?? id)
}

// ─── 逐客户端接入片段 ────────────────────────────────────────────────────────

/**
 * 四家的键名/位置各不相同,所以片段必须逐家给,不能一份 JSON 到处贴:
 *   - Cursor、VS Code 读的是两个不同的根键(mcpServers / servers);
 *   - Claude Desktop 的配置项历史上只吃 stdio,给它 http 片段等于给了段坏配置;
 *   - Claude Code 既能读 .mcp.json 也能用命令行加,两种都给。
 * 「其他客户端」用通用 mcpServers + type:http,是名册里没点到的客户端的最可能形状。
 */
export type SnippetClient = 'cursor' | 'vscode' | 'claudeDesktop' | 'claudeCode' | 'generic'

export interface SnippetEntry {
  client: SnippetClient
  /** 贴到哪儿(文件路径或命令),人读文案由 i18n 给,这里只存 key 后缀。 */
  labelKey: string
  lang: 'json' | 'bash'
  code: string
}

export function clientSnippets(baseUrl: string, plaintext: string): SnippetEntry[] {
  const url = mcpEndpointURL(baseUrl)
  const base = trimBaseURL(baseUrl)
  const server = { type: 'http', url, headers: { Authorization: `Bearer ${plaintext}` } }
  const stringify = (v: unknown) => JSON.stringify(v, null, 2)
  return [
    {
      client: 'cursor',
      labelKey: 'snippetWhere.cursor',
      lang: 'json',
      code: stringify({ mcpServers: { pipewright: { url, headers: server.headers } } }),
    },
    {
      client: 'vscode',
      labelKey: 'snippetWhere.vscode',
      lang: 'json',
      code: stringify({ servers: { pipewright: server }, inputs: [] }),
    },
    {
      client: 'claudeCode',
      labelKey: 'snippetWhere.claudeCode',
      lang: 'json',
      code: stringify({ mcpServers: { pipewright: server } }),
    },
    {
      client: 'claudeDesktop',
      labelKey: 'snippetWhere.claudeDesktop',
      lang: 'json',
      code: mcpStdioSnippet(base, plaintext),
    },
    {
      client: 'generic',
      labelKey: 'snippetWhere.generic',
      lang: 'json',
      code: mcpHttpSnippet(base, plaintext),
    },
  ]
}

/** Claude Code 的命令行形态:与 .mcp.json 同一个结果,写给不想建文件的人。 */
export function claudeCodeAddCommand(baseUrl: string, plaintext: string): string {
  // 头部值里有空格,必须整体加双引号;令牌本身不会有双引号(后端字符集是 [A-Za-z0-9_])。
  return `claude mcp add --transport http pipewright ${mcpEndpointURL(baseUrl)} --header "Authorization: Bearer ${plaintext}"`
}
