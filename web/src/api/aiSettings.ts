/**
 * AI Settings API — 三档协议各存一份配置。
 *
 * GET  /api/settings/ai          → AISettings   (整份总览:active + 三档各自配置)
 * PUT  /api/settings/ai          → AISettings   (needs CSRF;只写 body.provider 那一档)
 * POST /api/settings/ai/test     → AITestResult (needs CSRF)
 *
 * apiKey is WRITE-ONLY: the server never returns plaintext.
 * GET/PUT responses only include apiKeyMasked (e.g. "sk-ant-••••a91f").
 * Ollama does not require an apiKey.
 * `active` 是当前生效的那一档;enabled=true 保存后其他档自动停用(同一时刻只有一份生效)。
 */

import { http } from './http'
import type { AIMonthUsage } from '../lib/aiUsage'

/** 三档协议;'' 只出现在「还没选过任何一档」的前端初始态。 */
export type AIProvider = 'claude' | 'openai' | 'ollama' | ''

/** 可保存的三档(服务端总览里恒有这三项)。 */
export type SavedProvider = 'claude' | 'openai' | 'ollama'

export interface AIBudget {
  monthlyTokenLimit: number | null
}

/** 单档协议的配置;绝不包含明文 apiKey。 */
export interface AIProviderConfig {
  provider: SavedProvider
  configured: boolean
  enabled: boolean
  baseUrl: string
  model: string
  /** Server-computed mask, e.g. "sk-ant-••••a91f" — never plaintext. */
  apiKeyMasked: string
  budget: AIBudget
  /** 本自然月(UTC)累计用量,与 budget.monthlyTokenLimit 配对;没用过为 0/0。 */
  usage: AIMonthUsage
  updatedAt: string | null
}

/** GET/PUT 响应:当前生效档 + 三档各自的配置(未配过的档位为空默认)。 */
export interface AISettings {
  active: AIProvider
  configs: AIProviderConfig[]
}

/** PUT /api/settings/ai request body — 只写 provider 那一行,其余两档不动。 */
export interface SaveAISettingsInput {
  provider: SavedProvider
  baseUrl: string
  model: string
  /**
   * Write-only: omit or leave empty to keep that provider's existing key unchanged.
   * Non-empty rotates to the new key.
   */
  apiKey?: string
  budget: AIBudget
  /** true = 把这一档设为当前生效(其他档随之停用)。 */
  enabled: boolean
}

/** POST /api/settings/ai/test response */
export interface AITestResult {
  ok: boolean
  latencyMs: number
  detail: string
  error: string | null
}

/** POST /api/settings/ai/test request body (all optional — falls back to saved config) */
export interface TestAIConnectionInput {
  provider?: AIProvider
  baseUrl?: string
  model?: string
  /** Write-only draft key for testing before saving. */
  apiKey?: string
}

export async function getAISettings(): Promise<AISettings> {
  return http.get<AISettings>('/api/settings/ai')
}

export async function saveAISettings(input: SaveAISettingsInput): Promise<AISettings> {
  return http.put<AISettings>('/api/settings/ai', input)
}

export async function testAIConnection(draft?: TestAIConnectionInput): Promise<AITestResult> {
  return http.post<AITestResult>('/api/settings/ai/test', draft ?? {})
}
