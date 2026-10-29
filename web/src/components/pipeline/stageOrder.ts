import type { PipelineStage } from '../../api/pipeline'

/**
 * 把第 from 个阶段移到第 to 个位置,返回新数组;无需移动时返回 null。
 * 源阶段固定在首位(它由项目仓库绑定决定,不是可拖的普通步骤):
 * 源列自身不可拖动,目标落在源列之前时收口到源列之后。
 */
export function moveStage(stages: PipelineStage[], from: number, to: number): PipelineStage[] | null {
  if (from < 0 || from >= stages.length || to < 0 || to >= stages.length) return null
  const sourceIdx = stages.findIndex((s) => s.kind === 'source')
  const minIdx = sourceIdx + 1
  if (from < minIdx) return null
  const target = Math.max(to, minIdx)
  if (target === from) return null
  const next = [...stages]
  const [moved] = next.splice(from, 1)
  next.splice(target, 0, moved)
  return next
}
