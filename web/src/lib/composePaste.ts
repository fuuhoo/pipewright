/**
 * 「贴 docker-compose 起容器」的前置校验与摘要。
 *
 * 部署走已有的 POST /api/servers/{id}/stacks/deploy(后端 mkdir → 写文件 → compose up -d),
 * 这里只把它对入参的硬要求在前面对齐一遍,免得用户点完才收到 400:
 *   - 项目名走后端同一条白名单 reDockerTgt(internal/httpapi/server_ops.go),它同时挡住了
 *     前导 `-` 的 flag 注入和 `/` 的路径穿越(受管目录是 <base>/<name>/docker-compose.yml);
 *   - compose 正文非空且 ≤ 512 KiB(后端 composeMaxBytes)。
 * 服务名只做「摘要 / 提示」用,不做校验依据:YAML 解析是 compose 的事,前端不替它下结论。
 */

/** 与后端 reDockerTgt 一致:首字符必须是 word 字符,后续仅 [\w.-]。 */
export const STACK_NAME_PATTERN = /^[\w][\w.-]*$/
export const STACK_NAME_MAX = 128
/** 与后端 composeMaxBytes 一致:512 KiB。 */
export const COMPOSE_MAX_BYTES = 512 << 10

const RE_TOP_KEY = /^([A-Za-z_][\w-]*)[ \t]*:(?:[ \t]|$)/
const RE_SUB_KEY = /^[ \t]+([A-Za-z_][\w-]*)[ \t]*:(?:[ \t]|$)/

function isSkippable(line: string): boolean {
  const t = line.trim()
  return t === '' || t.startsWith('#') || t === '---'
}

export type NameIssue = '' | 'empty' | 'tooLong' | 'illegal'
export function stackNameIssue(name: string): NameIssue {
  const v = name.trim()
  if (!v) return 'empty'
  if (v.length > STACK_NAME_MAX) return 'tooLong'
  return STACK_NAME_PATTERN.test(v) ? '' : 'illegal'
}

export type ComposeIssue = '' | 'empty' | 'tooLarge'
export function composeIssue(yaml: string): ComposeIssue {
  if (!yaml.trim()) return 'empty'
  return new TextEncoder().encode(yaml).length > COMPOSE_MAX_BYTES ? 'tooLarge' : ''
}

/**
 * 取 `services:` 下一层的服务名(按缩进判定,不依赖 YAML 库)。
 * 只认「services 之后第一段缩进」上的键,更深一层的是服务内部字段。
 */
export function composeServiceNames(yaml: string): string[] {
  const names: string[] = []
  let baseIndent = -1
  let subIndent = -1
  for (const raw of yaml.split(/\r?\n/)) {
    if (isSkippable(raw)) continue
    const indent = raw.length - raw.trimStart().length
    if (baseIndent === -1) {
      const m = RE_TOP_KEY.exec(raw)
      if (m && m[1] === 'services') baseIndent = indent
      continue
    }
    if (indent <= baseIndent) break // 撞上下一个顶层键,services 块结束
    if (subIndent === -1) subIndent = indent
    if (indent !== subIndent) continue
    const m = RE_SUB_KEY.exec(raw)
    if (m && !names.includes(m[1])) names.push(m[1])
  }
  return names
}

/** 读顶层 `name:`(compose 的项目名),用于给项目名输入框一个默认值。 */
export function composeProjectName(yaml: string): string {
  for (const raw of yaml.split(/\r?\n/)) {
    if (isSkippable(raw)) continue
    if (raw.length - raw.trimStart().length !== 0) continue // 只看顶层键,服务里的 `name:` 不算
    const m = RE_TOP_KEY.exec(raw)
    if (m && m[1] === 'name') {
      return raw
        .slice(raw.indexOf(':') + 1)
        .trim()
        .replace(/^["']|["']$/g, '')
    }
  }
  return ''
}
