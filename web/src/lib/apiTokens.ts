/**
 * apiTokens.ts —— API 令牌页的纯逻辑(名称校验 + 归属人候选 + 客户端配置片段)。
 *
 * 刻意不 import vue-i18n:校验只回「哪一种毛病」,文案由调用方按键取。
 *
 * 名称判据与 internal/apitoken/apitoken.go 的 Create 逐条对齐(折叠空白 → 非空 → ≤128 字)。
 * 不一致的代价是「前端放行、签发回 400」,那比多写两行判据难受得多。
 *
 * 两个配置片段对应 MCP 的两条接入线,同一把令牌、同一套工具:
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
