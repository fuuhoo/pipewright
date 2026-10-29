import { describe, expect, it } from 'vitest'
import type { PipelineStage } from '../../api/pipeline'
import { moveStage } from './stageOrder'

function stage(id: string, kind: PipelineStage['kind'] = 'build'): PipelineStage {
  return { id, name: id, kind, jobs: [] }
}

const base = [
  stage('src', 'source'),
  stage('a'),
  stage('b'),
  stage('c'),
]

describe('moveStage', () => {
  it('向后移动:插到目标阶段之后(与阶段内任务重排同一套 from/to 语义)', () => {
    expect(moveStage(base, 1, 3)?.map((s) => s.id)).toEqual(['src', 'b', 'c', 'a'])
  })

  it('向前移动', () => {
    expect(moveStage(base, 3, 1)?.map((s) => s.id)).toEqual(['src', 'c', 'a', 'b'])
  })

  it('拖到源列上:收口到源阶段之后,源阶段不会被换到后面', () => {
    expect(moveStage(base, 3, 0)?.map((s) => s.id)).toEqual(['src', 'c', 'a', 'b'])
  })

  it('源阶段自身不可拖动', () => {
    expect(moveStage(base, 0, 2)).toBeNull()
  })

  it('落在原位 / 越界不产生新数组', () => {
    expect(moveStage(base, 2, 2)).toBeNull()
    expect(moveStage(base, -1, 2)).toBeNull()
    expect(moveStage(base, 2, 9)).toBeNull()
  })

  it('纯发布项目(无源阶段)全部可拖', () => {
    const release = [stage('d1', 'deploy'), stage('n1', 'notify')]
    expect(moveStage(release, 1, 0)?.map((s) => s.id)).toEqual(['n1', 'd1'])
  })
})
