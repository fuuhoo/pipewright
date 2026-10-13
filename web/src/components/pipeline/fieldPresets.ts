/**
 * fieldPresets — 把输入框 placeholder 里的示例变成可点选的值。
 *
 * 背景:节点表单几乎每格的提示语都是一个「能直接用的值」(dist、8080:80、整份 compose 骨架…),
 * 但它只以灰色 placeholder 出现,用户得照着敲一遍 —— 拼错还得回头对。这里把这些示例抬成芯片,点一下就填进框。
 *
 * 与命令模板同一套取值口径:
 *   - 单行格(text/number)是标量,点选就是「换成这个值」;
 *   - 多行格(textarea)永不覆盖:空框直填,已有内容往末尾并,已经含这段则原样不动(重复点不累积)。
 * 芯片正文都不进 i18n:它们是 shell 片段 / 路径 / YAML,翻译只会把可用值变成不可用值。
 */

/** 芯片文案:多行值只显一个认得出的名字,完整正文走 title 悬浮。 */
export function presetChipLabel(preset: string): string {
  const lines = preset.split('\n').map((l) => l.trim())
  if (lines.length <= 1) return preset
  // 两份 k8s 清单骨架开头几行一模一样(apiVersion: apps/v1),区分得看 kind: 那行
  const kind = lines.find((l) => /^kind:\s+\S/.test(l))
  if (kind) return kind.slice('kind:'.length).trim()
  return `${lines[0]} …`
}

/** 点选后写进多行格的值:空 → 直填;已含该段 → 不动;否则末尾追加。 */
export function mergeIntoTextarea(current: string, preset: string): string {
  const cur = (current ?? '').replace(/\r/g, '')
  if (cur.trim() === '') return preset
  const lines = preset.split('\n').map((l) => l.trim())
  if (lines.length === 1) {
    // 单行值按整行精确比对:'dist' 不能因为框里已有 'frontend/dist' 就被当成重复
    const has = new Set(
      cur
        .split('\n')
        .map((l) => l.trim())
        .filter(Boolean),
    )
    if (has.has(lines[0])) return cur
  } else {
    // 多行段(YAML 骨架)比对前先抹平缩进,否则重新点一次会往框里再摁一份
    const flat = (s: string) => s.replace(/\s+/g, ' ').trim()
    if (flat(cur).includes(flat(preset))) return cur
  }
  return `${cur.replace(/\n+$/, '')}\n${preset}`
}
