/**
 * artifactSources — 部署节点「产物来源任务」的候选集(纯函数,便于单测)。
 *
 * 候选必须是**本部署节点开跑时产物一定已存在**的构建任务:
 *   - 同一阶段内:被本任务 needs(传递)依赖的产出型任务(无 needs 的同阶段任务是并行档,
 *     产物可能还没落盘,不能选);
 *   - 跨阶段:上游阶段的全部产出型任务 —— 阶段图声明了 needs 就走传递闭包,
 *     整个流水线都没声明则按数组顺序取更早阶段(线性回退,与画布同一口径)。
 *
 * 选项值是 **job ID** 而不是任务名:阶段/任务名可重复(后端只对 ID 查重),
 * 存名字会让两个同名任务互相顶掉 —— 部署到错的那件比不部署更糟。
 */

import type { PipelineJob, PipelineStage } from '../../api/pipeline'
import { jobDependsOn } from './jobDeps'
import { hasAnyNeeds } from './stageDeps'

/** 不产物的节点类型:列进候选只会让人以为「这个也能发」。其余类型一律按可产产物对待。 */
const NON_PRODUCING_TYPES = new Set([
  'git_source',
  'push_image',
  'notify',
  'deploy_ssh',
  'deploy_frontend',
])

export function producesArtifact(job: PipelineJob): boolean {
  return !NON_PRODUCING_TYPES.has(job.type)
}

/** 部署节点的一个候选来源分组(一个阶段一组,组内是该阶段可选的任务)。 */
export interface ArtifactSourceGroup {
  stageId: string
  stageName: string
  jobs: PipelineJob[]
}

/** `stageId` 的上游阶段(传递闭包;无 needs 的线性流水线退化为「更早的阶段」)。 */
function upstreamStages(
  stages: ReadonlyArray<PipelineStage>,
  stageId: string,
): PipelineStage[] {
  const idx = stages.findIndex((s) => s.id === stageId)
  if (idx < 0) return []
  if (!hasAnyNeeds(stages)) return stages.slice(0, idx)
  const byId = new Map(stages.map((s) => [s.id, s]))
  const seen = new Set<string>()
  const out: PipelineStage[] = []
  const stack: string[] = [...(byId.get(stageId)?.needs ?? [])]
  while (stack.length) {
    const id = stack.pop() as string
    if (seen.has(id)) continue
    seen.add(id)
    const node = byId.get(id)
    if (!node) continue
    out.push(node)
    if (node.needs) stack.push(...node.needs)
  }
  // 闭包是倒序收集的,按画布顺序回正,下拉框分组才与阶段列从左到右一致。
  return out.sort((a, b) => stages.indexOf(a) - stages.indexOf(b))
}

/**
 * 返回 `stageId.stageJobs[jobId]` 这个部署节点可以选作产物来源的任务分组。
 * 本任务自身与非产出类型一律排除。
 */
export function artifactSourceGroups(
  stages: ReadonlyArray<PipelineStage>,
  stageId: string,
  jobId: string,
): ArtifactSourceGroup[] {
  const groups: ArtifactSourceGroup[] = []
  const current = stages.find((s) => s.id === stageId)
  if (current) {
    const upstreamInStage = current.jobs.filter(
      (j) => j.id !== jobId && producesArtifact(j) && jobDependsOn(current.jobs, jobId, j.id),
    )
    if (upstreamInStage.length > 0) {
      groups.push({ stageId: current.id, stageName: current.name, jobs: upstreamInStage })
    }
  }
  for (const s of upstreamStages(stages, stageId)) {
    const jobs = s.jobs.filter(producesArtifact)
    if (jobs.length > 0) groups.push({ stageId: s.id, stageName: s.name, jobs })
  }
  return groups
}
