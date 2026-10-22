/**
 * API 令牌 —— 机器客户端(MCP、脚本)用的 Bearer 凭据,页面上按「MCP 端点」呈现。
 *
 *   GET    /api/admin/api-tokens       → { items: APIToken[] }
 *   POST   /api/admin/api-tokens       → { item, plaintext }   (needs CSRF)
 *   PATCH  /api/admin/api-tokens/:id   → { item }              (needs CSRF,只改两把尺)
 *   DELETE /api/admin/api-tokens/:id   → 204                   (needs CSRF)
 *   GET    /api/admin/mcp-tools        → { items: MCPTool[] }  (工具名册,勾选用)
 *
 * 明文只在这次 POST 的响应体里出现一次,之后再没有第二个端点读得回来(库里只有摘要),
 * 所以这里既没有「查看」也不该有 —— 列表拿到的 APIToken 结构里根本没有明文字段。
 * 忘了存 = 只能撤销重发,因此创建弹窗要把明文单独成卡并强提示「只显示这一次」。
 *
 * 能改的只有范围(工具面 + 数据面),名称与归属改不了:令牌是「一把凭据 = 一个机器身份」,
 * 改归属在事后追查里等于换了一个人跑构建,撤销重发才是那条正路。
 *
 * PATCH 的空数组语义与 POST **相反**,这是刻意的(口径见 internal/apitoken/scopes.go 与
 * httpapi/apitokens.go):签发时显式 [] 会被当入参错误拒签,编辑时 [] 表示「把这把尺解开回不限」。
 * 省略某个键 = 不动它,所以调用方只发真的改过的那一把。
 *
 * 五个端点都收在 RequireAdmin 后面(与审计、用户管理同一道门),这页也因此只给管理员看。
 */

import { http } from './http'
import type { MCPToolDef } from '../lib/apiTokens'

export interface APIToken {
  /** = 端点地址里那一段(`/mcp/<id>`),后端直接拿主键当端点号,没有第二张端点表。 */
  id: string
  name: string
  /** 归属人:机器凭据真正要能回答「代表谁跑的」,所以这把令牌始终挂在一个具体账号上。 */
  userId: string
  ownerName: string
  /** 发放者(会话用户名)。管理员替他人签发时,这一列与 ownerName 不同。 */
  createdBy: string
  createdAt: string
  /** RFC3339;'' = 从没被用过 —— 发出去没动过的通常就是该删的那种。 */
  lastUsedAt: string
  /** 工具面收窄:后端保证是数组,空数组 = 不限(不是「什么都不能干」)。 */
  allowedTools: string[]
  /** 数据面收窄:分组 ID 列表,空数组 = 不限。非空时未归组的项目必然看不见(范围存的是真组 ID)。 */
  groupScope: string[]
}

export interface CreateAPITokenInput {
  name: string
  /** 留空 = 后端默认签给当前登录者自己。会话载荷里没有 userId,所以「给我自己」只能靠省略实现。 */
  userId?: string
  /** 省略或空数组都发不出「限」:后端把显式 [] 当入参错误拒签(400),所以这里用 undefined 表不限。 */
  allowedTools?: string[]
  groupScope?: string[]
}

/** 创建响应:元数据 + 一次性明文。 */
export interface CreatedAPIToken {
  item: APIToken
  plaintext: string
}

/** 编辑范围的入参:省略 = 不动;显式空数组 = 解开回不限;非空 = 收窄到这些。 */
export interface UpdateTokenScopesInput {
  allowedTools?: string[]
  groupScope?: string[]
}

export async function listAPITokens(): Promise<APIToken[]> {
  const res = await http.get<{ items: APIToken[] }>('/api/admin/api-tokens')
  return res.items ?? []
}

export async function createAPIToken(input: CreateAPITokenInput): Promise<CreatedAPIToken> {
  return http.post<CreatedAPIToken>('/api/admin/api-tokens', input)
}

/** 改两把尺,返回改完的元数据(不含明文 —— 明文本来也读不回来)。 */
export async function updateTokenScopes(id: string, input: UpdateTokenScopesInput): Promise<APIToken> {
  const res = await http.patch<{ item: APIToken }>(`/api/admin/api-tokens/${id}`, input)
  return res.item
}

export async function revokeAPIToken(id: string): Promise<void> {
  return http.delete<void>(`/api/admin/api-tokens/${id}`)
}

/** MCP 工具名册(与后端 mcpsrv 那份是同一份,页面与签发校验共用)。 */
export async function listMCPTools(): Promise<MCPToolDef[]> {
  const res = await http.get<{ items: MCPToolDef[] }>('/api/admin/mcp-tools')
  return res.items ?? []
}
