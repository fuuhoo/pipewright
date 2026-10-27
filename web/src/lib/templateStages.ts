/**
 * 画布 stages → 模板 stages 快照 —— 新建模板与覆盖模板共用这一份映射。
 *
 * 存在的理由只有一个:id 必须原样带上。后端 NormalizeSpec 只给空 id 补新 uuid,而阶段 needs、
 * 任务 needs 和部署节点的「产物来源任务」都按 id 引用;把 id 抹掉就等于让引用指向一对陌生的
 * uuid,保存期直接被 DAG 校验判成非法。曾经这里写成 id: undefined,于是任何带阶段依赖的流水线
 * 都存不成模板 —— 下面的回归用例钉的就是这条。
 *
 * matrix / post / services / when / gate 同理:PUT 是整份覆盖,漏一个键等于把用户配的功能清空。
 */
import type { PipelineStage, SavePipelineInput } from '../api/pipeline'

export type TemplateStagesInput = SavePipelineInput['stages']

export function toTemplateStages(stages: PipelineStage[]): TemplateStagesInput {
  return stages.map((s) => ({
    id: s.id,
    name: s.name,
    kind: s.kind,
    needs: s.needs,
    allowFailure: s.allowFailure,
    when: s.when,
    gate: s.gate,
    matrix: s.matrix,
    post: s.post,
    services: s.services,
    jobs: s.jobs.map((j) => ({
      id: j.id,
      name: j.name,
      type: j.type,
      summary: j.summary,
      config: j.config,
      needs: j.needs,
    })),
  }))
}
