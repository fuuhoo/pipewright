/**
 * Run-data retention policy API.
 *
 * GET /api/retention/config → RetentionConfig
 * PUT /api/retention/config → RetentionConfig   (needs CSRF)
 *
 * Global policy: when enabled, a background sweeper periodically prunes terminal
 * runs (and their logs/steps/artifacts/snapshots). Keeping is a **union** — a run
 * survives if it still fits within keepGlobal OR within maxAgeDays; only runs that
 * exceed every configured limit are deleted. In-progress runs are never pruned.
 * 0 = unlimited for any limit.
 */

import { http } from './http'

export interface RetentionConfig {
  enabled: boolean
  /** Keep the most recent N terminal runs across all projects (0 = unlimited). */
  keepGlobal: number
  /**
   * Legacy per-project limit. No longer exposed in the UI, but it is still a live
   * dimension server-side, so the settings page must echo back whatever GET returned —
   * sending 0 would silently drop an old instance's per-project promise.
   */
  keepPerProject: number
  /** Terminal runs older than N days exceed the age limit (0 = no age limit). */
  maxAgeDays: number
}

export async function getRetentionConfig(): Promise<RetentionConfig> {
  return http.get<RetentionConfig>('/api/retention/config')
}

export async function setRetentionConfig(input: RetentionConfig): Promise<RetentionConfig> {
  return http.put<RetentionConfig>('/api/retention/config', input)
}
