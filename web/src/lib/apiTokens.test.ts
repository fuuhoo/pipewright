import { describe, expect, it } from 'vitest'

import {
  TOKEN_NAME_MAX,
  mcpEndpointURL,
  mcpHttpSnippet,
  mcpStdioSnippet,
  normalizeTokenName,
  ownerOptions,
  tokenNameProblem,
  trimBaseURL,
} from './apiTokens'

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
