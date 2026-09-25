/**
 * workspaceHints — 「脚本里到底该写哪个路径」的提示数据(纯函数,便于单测)。
 *
 * 编辑器里手写命令时最容易踩空的一点:每个任务跑在**自己新克隆的工作区**里,阶段之间不共享磁盘,
 * 而工作区在容器里挂在 `/workspace`。于是有两处反复出错:
 *   - 把「工作目录」当成「仓库根」:`workDir` 只是仓库根下的相对子目录(脚本从那儿开始跑),
 *     产物路径却是相对仓库根写的,少一层/多一层就找不到;
 *   - 以为上游产物天然在下游任务里:它要等运行前按**原相对路径**放回工作区才存在。
 * 这里把两件事一次说清:本任务的绝对工作目录 + 上游产物在本任务里的绝对落位。
 */

import type { PipelineStage } from '../../api/pipeline'
import { artifactSourceGroups } from './artifactSources'

/** 工作区在构建容器里的挂载点(与后端 scriptWorkspaceMount 同一常量语义)。 */
export const WORKSPACE_ROOT = '/workspace'

/** 通配特征字符:后端按 filepath.Glob 展开,编辑期只能原样转述。 */
const WILDCARD_CHARS = /[*?[\]]/

/** 把若干路径段拼成容器内绝对路径(去掉多余的 `/` 与 `./`,不解释 `..`)。 */
export function joinWorkspace(...segs: Array<string | undefined>): string {
  const parts: string[] = []
  for (const raw of segs) {
    const s = (raw ?? '').trim().replace(/^\.?\//, '').replace(/\/+$/, '')
    if (s) parts.push(s)
  }
  return parts.length ? `${WORKSPACE_ROOT}/${parts.join('/')}` : WORKSPACE_ROOT
}

/** 本任务在容器里的实际工作目录。`workDir` 相对仓库根,空 = 仓库根本身。 */
export function containerWorkDir(workDir?: string): string {
  return joinWorkspace(workDir)
}

/** 一条上游产物提示。 */
export interface ArtifactPathHint {
  /** 来源任务(去重后一条一行)。 */
  sourceJobId: string
  sourceJobName: string
  sourceStageName: string
  /** 来源任务里写的那一行:相对**仓库根**,可能带通配。 */
  declared: string
  /** 本任务运行时它在容器里的绝对路径。 */
  path: string
  /** 通配项:运行时才展开,提示里要标出来,别当字面路径抄。 */
  wildcard: boolean
}

function splitLines(value: string): string[] {
  return value
    .replace(/\r/g, '')
    .split('\n')
    .map((l) => l.trim())
    .filter((l) => l !== '' && !l.startsWith('#'))
}

/**
 * 汇总「本任务开跑前会放回工作区的上游产物」。
 * 上游口径与部署节点挑产物完全一致(见 artifactSources):同阶段只算被本任务传递依赖的任务,
 * 并行分支不算 —— 它的产物此刻未必存在,写进提示就是骗人。
 */
export function artifactPathHints(
  stages: ReadonlyArray<PipelineStage>,
  stageId: string,
  jobId: string,
): ArtifactPathHint[] {
  const out: ArtifactPathHint[] = []
  const seen = new Set<string>()
  for (const group of artifactSourceGroups(stages, stageId, jobId)) {
    for (const job of group.jobs) {
      for (const line of splitLines(job.config?.artifactPath ?? '')) {
        const key = `${job.id}\n${line}`
        if (seen.has(key)) continue
        seen.add(key)
        out.push({
          sourceJobId: job.id,
          sourceJobName: job.name,
          sourceStageName: group.stageName,
          declared: line,
          path: joinWorkspace(line),
          wildcard: WILDCARD_CHARS.test(line),
        })
      }
    }
  }
  return out
}
