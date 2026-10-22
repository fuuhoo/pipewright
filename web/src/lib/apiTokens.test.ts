import { describe, expect, it } from 'vitest'

import {
  TOKEN_NAME_MAX,
  activeToolPreset,
  claudeCodeAddCommand,
  clientSnippets,
  groupScopeNames,
  mcpEndpointURL,
  mcpHttpSnippet,
  mcpStdioSnippet,
  normalizeTokenName,
  ownerOptions,
  tokenNameProblem,
  toggleTool,
  toolPresetNames,
  trimBaseURL,
} from './apiTokens'
import type { MCPToolDef } from './apiTokens'

describe('normalizeTokenName / tokenNameProblem', () => {
  it('折叠内部连续空白并去首尾(与后端 strings.Fields + Join 同口径)', () => {
    expect(normalizeTokenName('  deploy   runner\n机器人 ')).toBe('deploy runner 机器人')
    expect(tokenNameProblem('  \t\n ')).toBe('empty')
  })

  it('空与超长各自给出原因,正常名称回空串(= 可签发)', () => {
    expect(tokenNameProblem('')).toBe('empty')
    expect(tokenNameProblem('CI 机器人')).toBe('')
    expect(tokenNameProblem('x'.repeat(TOKEN_NAME_MAX))).toBe('')
    expect(tokenNameProblem('x'.repeat(TOKEN_NAME_MAX + 1))).toBe('tooLong')
  })

  it('长度按码点计:中文一个、代理对一个,与 Go 的 []rune 一致', () => {
    expect(tokenNameProblem('名'.repeat(TOKEN_NAME_MAX))).toBe('')
    expect(tokenNameProblem('名'.repeat(TOKEN_NAME_MAX + 1))).toBe('tooLong')
    expect(tokenNameProblem('😀'.repeat(TOKEN_NAME_MAX))).toBe('')
    expect(tokenNameProblem('😀'.repeat(TOKEN_NAME_MAX + 1))).toBe('tooLong')
  })

  it('判据看的是折叠后的值:首尾堆到超限才算超长', () => {
    const padded = `${' '.repeat(40)}${'a'.repeat(TOKEN_NAME_MAX)}${' '.repeat(40)}`
    expect(tokenNameProblem(padded)).toBe('')
  })
})

describe('ownerOptions', () => {
  const users = [
    { id: 'u1', username: 'admin', displayName: '管理员', enabled: true },
    { id: 'u2', username: 'fuubin', displayName: '傅兵', enabled: true },
    { id: 'u3', username: 'bot-old', enabled: true },
    { id: 'u4', username: 'gone', displayName: '禁用的人', enabled: false },
  ]

  it('排掉当前登录者与禁用账号:「我自己」走省略 userId 那条路,不由列表给第二扇门', () => {
    const got = ownerOptions(users, 'admin')
    expect(got.map((o) => o.id)).toEqual(['u2', 'u3'])
  })

  it('主标签有昵称用昵称、没有回登录名;次标签恒为 @登录名', () => {
    const got = ownerOptions(users, 'admin')
    expect(got[0]).toEqual({ id: 'u2', name: '傅兵', handle: '@fuubin' })
    expect(got[1]).toEqual({ id: 'u3', name: 'bot-old', handle: '@bot-old' })
  })

  it('昵称只有空白时也算没昵称(不能渲染出一个空选项)', () => {
    expect(ownerOptions([{ id: 'x', username: 'y', displayName: '  ', enabled: true }], 'z')).toEqual([
      { id: 'x', name: 'y', handle: '@y' },
    ])
  })
})

describe('MCP 客户端配置片段', () => {
  it('base 结尾斜杠会被收掉,两种形态都不多出 //', () => {
    expect(trimBaseURL('https://pw.example.com///')).toBe('https://pw.example.com')
    expect(mcpEndpointURL('https://pw.example.com/')).toBe('https://pw.example.com/mcp')
  })

  it('stdio 形态:命令 + args,令牌放 env 不放命令行(ps 里看不着)', () => {
    const parsed = JSON.parse(mcpStdioSnippet('http://127.0.0.1:8080', 'pwt_abc')) as {
      mcpServers: { pipewright: Record<string, unknown> }
    }
    const server = parsed.mcpServers.pipewright
    expect(server).toEqual({
      command: 'pipewright',
      args: ['mcp'],
      env: { PIPEWRIGHT_URL: 'http://127.0.0.1:8080', PIPEWRIGHT_TOKEN: 'pwt_abc' },
    })
  })

  it('HTTP 形态:地址指到 /mcp,令牌进 Authorization 头', () => {
    const parsed = JSON.parse(mcpHttpSnippet('https://pw.example.com', 'pwt_abc')) as {
      mcpServers: { pipewright: Record<string, unknown> }
    }
    expect(parsed.mcpServers.pipewright).toEqual({
      type: 'http',
      url: 'https://pw.example.com/mcp',
      headers: { Authorization: 'Bearer pwt_abc' },
    })
  })

  it('令牌里的引号与反斜杠由 JSON.stringify 转义,不会拼出一段坏配置', () => {
    const weird = 'pwt_"x\\y'
    expect(() => JSON.parse(mcpStdioSnippet('https://a.b', weird))).not.toThrow()
    expect(mcpHttpSnippet('https://a.b', weird)).toContain('pwt_\\"x\\\\y')
  })
})

describe('逐客户端接入片段', () => {
  it('五个客户端各一条,顺序即 tab 顺序', () => {
    const got = clientSnippets('https://pw.example.com', 'pwt_abc')
    expect(got.map((s) => s.client)).toEqual(['cursor', 'vscode', 'claudeCode', 'claudeDesktop', 'generic'])
    expect(got.every((s) => s.labelKey.startsWith('snippetWhere.'))).toBe(true)
  })

  it('Cursor 与 VS Code 的根键不同:前者 mcpServers,后者 servers + inputs', () => {
    const [cursor, vscode] = clientSnippets('https://pw.example.com', 'pwt_abc')
    expect(JSON.parse(cursor.code)).toEqual({
      mcpServers: {
        pipewright: {
          url: 'https://pw.example.com/mcp',
          headers: { Authorization: 'Bearer pwt_abc' },
        },
      },
    })
    expect(JSON.parse(vscode.code)).toEqual({
      servers: {
        pipewright: {
          type: 'http',
          url: 'https://pw.example.com/mcp',
          headers: { Authorization: 'Bearer pwt_abc' },
        },
      },
      inputs: [],
    })
  })

  it('Claude Desktop 只吃本机 stdio,给它 http 片段等于给了段坏配置', () => {
    const desktop = clientSnippets('https://pw.example.com/', 'pwt_abc').find((s) => s.client === 'claudeDesktop')
    expect(JSON.parse(desktop?.code ?? '{}')).toEqual(
      JSON.parse(mcpStdioSnippet('https://pw.example.com', 'pwt_abc')),
    )
  })

  it('Claude Code 的一行命令:地址带 /mcp,带空格的头部值整体加引号', () => {
    expect(claudeCodeAddCommand('https://pw.example.com', 'pwt_abc')).toBe(
      'claude mcp add --transport http pipewright https://pw.example.com/mcp --header "Authorization: Bearer pwt_abc"',
    )
  })
})

describe('工具范围(allowedTools)', () => {
  const catalog: MCPToolDef[] = [
    { name: 'list_projects', title: '项目列表', readOnly: true },
    { name: 'create_project', title: '新建项目', readOnly: false },
    { name: 'trigger_run', title: '触发运行', readOnly: false },
    { name: 'cancel_run', title: '取消运行', readOnly: false },
    { name: 'get_run', title: '运行详情', readOnly: true },
  ]

  it('all 档返回空数组 = 不限(发出去是不带这个键,不是空列表)', () => {
    expect(toolPresetNames('all', catalog)).toEqual([])
  })

  it('readonly 档只点名册里标了只读的工具', () => {
    expect(toolPresetNames('readonly', catalog)).toEqual(['list_projects', 'get_run'])
  })

  it('ops 档 = 只读 + 触发/取消;名册里没有那两个写工具时退化成只读,不凭空造名字', () => {
    expect(toolPresetNames('ops', catalog)).toEqual(['list_projects', 'get_run', 'trigger_run', 'cancel_run'])
    const renamed = catalog.filter((x) => x.name !== 'trigger_run' && x.name !== 'cancel_run')
    expect(toolPresetNames('ops', renamed)).toEqual(['list_projects', 'get_run'])
  })

  it('档位识别判的是集合相等:数量相同但换了工具算手改(null)', () => {
    expect(activeToolPreset([], catalog)).toBe('all')
    expect(activeToolPreset(['get_run', 'list_projects'], catalog)).toBe('readonly')
    expect(activeToolPreset(['list_projects', 'get_run', 'trigger_run', 'cancel_run'], catalog)).toBe('ops')
    expect(activeToolPreset(['list_projects', 'create_project'], catalog)).toBeNull()
    expect(activeToolPreset(['list_projects', 'get_run', 'create_project'], catalog)).toBeNull()
  })

  it('勾选按名册顺序回写,不按点击顺序;名册外的名字不静默吞掉', () => {
    const first = toggleTool(catalog, [], 'get_run')
    const second = toggleTool(catalog, first, 'list_projects')
    expect(second).toEqual(['list_projects', 'get_run'])
    expect(toggleTool(catalog, second, 'list_projects')).toEqual(['get_run'])
    expect(toggleTool(catalog, ['gone_tool'], 'list_projects')).toEqual(['list_projects', 'gone_tool'])
    expect(toggleTool(catalog, ['get_run', 'gone_tool'], 'gone_tool')).toEqual(['get_run'])
  })
})

describe('分组范围(groupScope)', () => {
  const groups = [
    { id: 'g1', name: '前端组', projectCount: 3 },
    { id: 'g2', name: '后端组', projectCount: 0 },
  ]

  it('ID 换成展示名;组已删的留原 ID,不静默吞掉', () => {
    expect(groupScopeNames(['g2', 'g1'], groups)).toEqual(['后端组', '前端组'])
    expect(groupScopeNames(['g1', 'g-deleted'], groups)).toEqual(['前端组', 'g-deleted'])
  })

  it('空选择就是空列表(页面上按「不限」渲染)', () => {
    expect(groupScopeNames([], groups)).toEqual([])
  })
})
