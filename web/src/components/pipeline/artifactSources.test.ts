import { describe, it, expect } from 'vitest'
import type { PipelineJob, PipelineStage } from '../../api/pipeline'
import { artifactSourceGroups, producesArtifact } from './artifactSources'

function job(id: string, type = 'script', needs: string[] = []): PipelineJob {
  return { id, name: id.toUpperCase(), type, summary: '', config: {}, needs }
}

function stage(id: string, jobs: PipelineJob[], needs: string[] = [], kind: PipelineStage['kind'] = 'build'): PipelineStage {
  return { id, name: `阶段-${id}`, kind, jobs, needs }
}

/** 每个分组里的任务 ID(忽略阶段名,断言更聚焦)。 */
function groupIdList(stages: PipelineStage[], stageId: string, jobId: string): string[][] {
  return artifactSourceGroups(stages, stageId, jobId).map((g) => g.jobs.map((j) => j.id))
}

describe('artifactSourceGroups · 部署节点的产物来源候选', () => {
  it('线性流水线:更早阶段里的构建任务全部可选', () => {
    const stages = [
      stage('src', [job('jsrc', 'git_source')], [], 'source'),
      stage('build', [job('japi'), job('jweb')]),
      stage('deploy', [job('jdep', 'deploy_ssh')], [], 'deploy'),
    ]
    // 源阶段不产产物 → 整组消失,只剩构建阶段那一组。
    expect(groupIdList(stages, 'deploy', 'jdep')).toEqual([['japi', 'jweb']])
  })

  it('源/推送/通知/部署这类不产产物的节点不进候选', () => {
    const stages = [
      stage('build', [job('jsrc', 'git_source'), job('jpush', 'push_image'), job('japi')]),
      stage('deploy', [job('jdep', 'deploy_ssh'), job('jnotify', 'notify')], [], 'deploy'),
    ]
    expect(groupIdList(stages, 'deploy', 'jdep')).toEqual([['japi']])
    expect(producesArtifact(job('n', 'notify'))).toBe(false)
    expect(producesArtifact(job('b', 'build'))).toBe(true)
  })

  it('阶段图声明 needs 后只认传递闭包,并行分支不算上游', () => {
    const stages = [
      stage('api', [job('japi')]),
      stage('web', [job('jweb')]),
      stage('pkg', [job('jpkg')], ['api']),
      stage('deploy', [job('jdep', 'deploy_ssh')], ['pkg'], 'deploy'),
    ]
    // japi 经 pkg 传递可达(按画布顺序排在前);jweb 在并行的另一条分支上,产物未必已就绪。
    expect(groupIdList(stages, 'deploy', 'jdep')).toEqual([['japi'], ['jpkg']])
  })

  it('同阶段:只有被本任务 needs 传递依赖的任务才算上游', () => {
    const stages = [
      stage('src', [job('jsrc', 'git_source')], [], 'source'),
      stage('mix', [job('japi'), job('jweb'), job('jdep', 'deploy_ssh', ['japi'])]),
    ]
    // japi 被依赖 → 可选;jweb 与本任务同阶段并行 → 不可选(产物可能还没落盘)。
    expect(groupIdList(stages, 'mix', 'jdep')).toEqual([['japi']])
  })

  it('本任务自己不进候选(哪怕 config 里残留了自己的 ID)', () => {
    const stages = [stage('build', [job('japi')]), stage('deploy', [job('jdep', 'deploy_ssh')], [], 'deploy')]
    expect(groupIdList(stages, 'deploy', 'jdep')).toEqual([['japi']])
    // 部署任务排在构建任务之前时,后面的任务不该成为候选。
    const reversed = [stage('deploy', [job('jdep', 'deploy_ssh')], [], 'deploy'), stage('build', [job('japi')])]
    expect(groupIdList(reversed, 'deploy', 'jdep')).toEqual([])
  })

  it('分组保留阶段边界与阶段顺序,供 optgroup 直接渲染', () => {
    const stages = [
      stage('first', [job('j1')]),
      stage('second', [job('j2')]),
      stage('deploy', [job('jdep', 'deploy_ssh')], [], 'deploy'),
    ]
    const groups = artifactSourceGroups(stages, 'deploy', 'jdep')
    expect(groups.map((g) => g.stageName)).toEqual(['阶段-first', '阶段-second'])
  })
})
