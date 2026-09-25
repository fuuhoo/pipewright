/**
 * dagEdges — 画布连线的纯几何件(锚点分配 + 贝塞尔路径),供 PipelineCanvas(阶段之间)与
 * StageColumn(阶段内任务之间)共用。
 *
 * 抽出来的原因:连线「看着错位」从来不是算法算错,而是锚点取自看不见的容器边界、以及多条边全挤
 * 在同一中心点。锚点坐标必须由调用方从 DOM 量出来传进来(本模块不碰 DOM),这样才测得动。
 */

/** 一个量到的盒子(相对同一 offsetParent 的像素)。 */
export interface EdgeBox {
  x: number
  y: number
  w: number
  h: number
}

/** 从 DOM 元素取相对 offsetParent 的盒(与连线 overlay 同一坐标原点)。 */
export function boxOf(el: HTMLElement): EdgeBox {
  return { x: el.offsetLeft, y: el.offsetTop, w: el.offsetWidth, h: el.offsetHeight }
}

/**
 * fanAnchor 给出「同一侧第 i 条边(共 n 条)」应该锚在盒子的哪个纵坐标。
 * 单边取中心;多边按 (i+1)/(n+1) 沿边均分 —— 全挤在中心点会让扇入读成一条折线,分不清谁接谁。
 */
export function fanAnchor(box: EdgeBox, i: number, n: number): number {
  if (n <= 1) return box.y + box.h / 2
  return box.y + (box.h * (i + 1)) / (n + 1)
}

/**
 * edgePath 画「右缘出 → 左缘入」的三次贝塞尔。进出点用调用方给的 fromY/toY(锚点由 fanAnchor 定),
 * 控制点纯横向,保证线在节点边缘是水平进出的;minCtrl 让很短的边也不出现尖角。
 */
export function edgePath(from: EdgeBox, to: EdgeBox, fromY: number, toY: number, minCtrl = 18): string {
  const x1 = from.x + from.w
  const x2 = to.x
  const dx = Math.max(minCtrl, Math.abs(x2 - x1) * 0.5)
  return `M${n(x1)},${n(fromY)} C${n(x1 + dx)},${n(fromY)} ${n(x2 - dx)},${n(toY)} ${n(x2)},${n(toY)}`
}

/** 按 key 计数(扇入/扇出各自有几条边)。 */
export function countBy(keys: Iterable<string>): Map<string, number> {
  const out = new Map<string, number>()
  for (const k of keys) out.set(k, (out.get(k) ?? 0) + 1)
  return out
}

/** 坐标保留两位:路径串进 DOM attribute,整数化会让细线看着抖。 */
function n(v: number): number {
  return Math.round(v * 100) / 100
}
