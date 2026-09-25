import { describe, it, expect } from 'vitest'
import type { PipelineJob, PipelineStage } from '../../api/pipeline'
import { artifactPathHints, containerWorkDir, joinWorkspace } from './workspaceHints'

function job(id: string, type = 'script', config: Record<string, string> = {}, needs: string[] = []): PipelineJob {
  return { id, name: id.toUpperCase(), type, summary: '', config, needs }
}

function stage(id: string, jobs: PipelineJob[], needs: string[] = [], kind: PipelineStage['kind'] = 'build'): PipelineStage {
  return { id, name: `阶段-${id}`, kind, jobs, needs }
}

describe('joinWorkspace · 容器内路径拼接', () => {
  it('仓库根相对路径挂到 /workspace 下,顺带去掉 ./ 与结尾斜杠', () => {
    expect(joinWorkspace()).toBe('/workspace')
    expect(joinWorkspace('reporter-ny/web/app/dist')).toBe('/workspace/reporter-ny/web/app/dist')
    expect(joinWorkspace('./reporter-ny/', '/web/app')).toBe('/workspace/reporter-ny/web/app')
  })

  it('workDir 相对仓库根,不是相对产物路径', () => {
    expect(containerWorkDir('./reporter-ny/')).toBe('/workspace/reporter-ny')
    expect(containerWorkDir('')).toBe('/workspace')
    expect(containerWorkDir()).toBe('/workspace')
  })
})

describe('artifactPathHints · 上游产物落哪个路径', () => {
  const stages = [
    stage('src', [job('jsrc', 'git_source')], [], 'source'),
    stage('build', [
      job('japi', 'script', { artifactPath: 'reporter-ny/build/linux-amd64/server-ny\n# 注释行不算\n' }),
      job('jweb', 'script', { artifactPath: 'reporter-ny/web/app/dist' }, ['japi']),
    ]),
    stage('pkg', [job('jpkg', 'script', { artifactPath: 'bundle' })], ['build']),
  ]

  it('同阶段被依赖的任务 + 上游阶段的产物都列出来,带来源任务名', () => {
    const hints = artifactPathHints(stages, 'pkg', 'jpkg')
    expect(hints.map((h) => [h.sourceJobName, h.declared, h.path])).toEqual([
      ['JAPI', 'reporter-ny/build/linux-amd64/server-ny', '/workspace/reporter-ny/build/linux-amd64/server-ny'],
      ['JWEB', 'reporter-ny/web/app/dist', '/workspace/reporter-ny/web/app/dist'],
    ])
  })

  it('本任务自己的产物不算上游(它就是被产的那个)', () => {
    expect(artifactPathHints(stages, 'build', 'jweb').map((h) => h.declared)).toEqual([
      'reporter-ny/build/linux-amd64/server-ny',
    ])
  })

  it('通配原样转述并标注,免得被当字面路径抄进脚本', () => {
    const withGlob = [
      stage('a', [job('ja', 'script', { artifactPath: 'backend/target/*.jar' })]),
      stage('b', [job('jb', 'build_image', {}, ['a'])], ['a']),
    ]
    const [hint] = artifactPathHints(withGlob, 'b', 'jb')
    expect(hint.declared).toBe('backend/target/*.jar')
    expect(hint.wildcard).toBe(true)
    expect(hint.path).toBe('/workspace/backend/target/*.jar')
  })

  it('并行分支不算上游:它的产物此刻未必存在', () => {
    const produced = { artifactPath: 'out' }
    const stages2 = [
      stage('build', [job('japi', 'script', produced), job('jweb', 'script', produced)]),
      stage('deploy', [job('jdep', 'deploy_ssh', {}, ['japi'])], ['build'], 'deploy'),
    ]
    // 上游阶段整组算:阶段级依赖已声明,进到这组时它的产物一定已归档。
    expect(artifactPathHints(stages2, 'deploy', 'jdep').map((h) => h.sourceJobName)).toEqual(['JAPI', 'JWEB'])

    const stages3 = [
      stage('mix', [job('japi', 'script', produced), job('jweb', 'script', produced), job('jdep', 'deploy_ssh', {}, ['japi'])]),
    ]
    // 同阶段的并行任务 jweb 没被本任务依赖 → 不列,否则提示指向一个还不存在的产物。
    expect(artifactPathHints(stages3, 'mix', 'jdep').map((h) => h.sourceJobName)).toEqual(['JAPI'])
  })

  it('来源任务没声明产物路径 → 无提示(不显示空壳)', () => {
    const bare = [stage('a', [job('ja')]), stage('b', [job('jb', 'script', {}, ['a'])], ['a'])]
    expect(artifactPathHints(bare, 'b', 'jb')).toEqual([])
  })
})
