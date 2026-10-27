/**
 * toTemplateStages 回归:快照必须把 id 与阶段扩展字段原样带走。
 *
 * 判据来自后端 NormalizeSpec —— 空 id 会被补成新 uuid,而 needs 按 id 引用,
 * 所以「丢 id」不是无害的简化,是直接让带依赖的流水线存不成模板。
 */
import { describe, it, expect } from 'vitest'
import { toTemplateStages } from './templateStages'
import type { PipelineStage } from '../api/pipeline'

const src: PipelineStage = {
  id: 'st-src',
  name: '源',
  kind: 'source',
  jobs: [{ id: 'jb-src', name: 'Git 源', type: 'git_source', summary: '拉代码', config: { branch: 'main' } }],
}
const build: PipelineStage = {
  id: 'st-build',
  name: '构建',
  kind: 'build',
  needs: ['st-src'],
  allowFailure: true,
  when: { branches: ['release/*'], events: ['manual'] },
  gate: true,
  matrix: { go: ['1.21', '1.22'] },
  post: [{ condition: 'always', image: 'alpine', commands: ['rm -rf .cache'], workDir: '/w' }],
  services: [{ name: 'pg', image: 'postgres:16', env: ['POSTGRES_PASSWORD=x'], ports: ['5432:5432'] }],
  jobs: [
    { id: 'jb-a', name: '编译', type: 'build', summary: '', config: { artifactType: 'jar' } },
    { id: 'jb-b', name: '打包镜像', type: 'build_image', summary: '', config: {}, needs: ['jb-a'] },
  ],
}

describe('toTemplateStages', () => {
  it('阶段与任务 id 原样保留(needs 指得到的正是这些 id)', () => {
    const out = toTemplateStages([src, build])
    expect(out.map((s) => s.id)).toEqual(['st-src', 'st-build'])
    expect(out[1].jobs.map((j) => j.id)).toEqual(['jb-a', 'jb-b'])
    expect(out[1].needs).toEqual(['st-src'])
    expect(out[1].jobs[1].needs).toEqual(['jb-a'])
  })

  it('阶段扩展字段一个都不落(PUT 整份覆盖,漏键等于清空)', () => {
    expect(toTemplateStages([build])[0]).toMatchObject({
      allowFailure: true,
      when: { branches: ['release/*'], events: ['manual'] },
      gate: true,
      matrix: { go: ['1.21', '1.22'] },
      post: [{ condition: 'always', image: 'alpine', commands: ['rm -rf .cache'], workDir: '/w' }],
      services: [{ name: 'pg', image: 'postgres:16', env: ['POSTGRES_PASSWORD=x'], ports: ['5432:5432'] }],
    })
  })

  it('未设置的可选字段保持未设置,不编造空值', () => {
    const out = toTemplateStages([src])
    expect(Object.prototype.hasOwnProperty.call(out[0], 'gate')).toBe(true)
    expect(out[0].gate).toBeUndefined()
    expect(out[0].jobs[0].needs).toBeUndefined()
    expect(out[0].jobs[0].summary).toBe('拉代码')
  })

  it('空画布映射为空数组(后端仍会因缺源阶段判 422,前端不该先崩)', () => {
    expect(toTemplateStages([])).toEqual([])
  })
})
