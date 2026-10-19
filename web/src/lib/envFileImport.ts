/**
 * 「构建节点导入 env 文件当环境变量」的前置判据与预览。
 *
 * 规则与后端 internal/pipeline/envfile.go 逐条对齐(键名规则、路径规则、64 KiB 额度、
 * KEY=VALUE 的解析口径)。表单先判一遍,用户就不会点保存才收到一个指向隐形字段的 422;
 * 但这里只是**提前报同样的错**,不能反过来放宽 —— 后端仍是唯一权威。
 */

/** 与后端 pipeline.EnvMaxBytes 一致:64 KiB。 */
export const ENV_MAX_BYTES = 64 << 10

/** 与后端 composeFileMaxLen 一致:路径长度上限 512。 */
export const ENV_PATH_MAX = 512

/** 与后端 EnvKeyOK 一致:字母/下划线开头,其后仅字母数字下划线。 */
export const ENV_KEY_PATTERN = /^[A-Za-z_][A-Za-z0-9_]*$/

/** 与后端 IsEnvFileName 一致:`.env` / `.env.xxx` / `xxx.env`。 */
export function isEnvFileName(name: string): boolean {
  const b = name.trim()
  if (!b) return false
  return b === '.env' || b.startsWith('.env.') || b.endsWith('.env')
}

export type EnvPathIssue = '' | 'empty' | 'tooLong' | 'notEnvFile' | 'notRelative'
/** 仓库来源的相对路径判据(后端 RepoEnvPathOK)。 */
export function repoEnvPathIssue(path: string): EnvPathIssue {
  const v = path.trim()
  if (!v) return 'empty'
  if (v.length > ENV_PATH_MAX) return 'tooLong'
  if (v.startsWith('/') || v.includes('\\')) return 'notRelative'
  const segs = v.split('/')
  if (segs.some((s) => s === '' || s === '.' || s === '..')) return 'notRelative'
  return isEnvFileName(segs[segs.length - 1]) ? '' : 'notEnvFile'
}

export type EnvBodyIssue = '' | 'empty' | 'tooLarge'
/** paste / upload 正文判据(后端 validateJobEnv 的两条)。 */
export function envBodyIssue(text: string): EnvBodyIssue {
  if (!text.trim()) return 'empty'
  return new TextEncoder().encode(text).length > ENV_MAX_BYTES ? 'tooLarge' : ''
}

/**
 * 按后端 ParseEnvBody 同一条口径预览这份正文能提出哪些变量。
 *
 * 只用于表单里那句「识别出 N 个变量」+ 跳过行告警:改名会静默少几个变量,让人在构建日志里查半天,
 * 所以配表单时就得看见。解析本身仍以服务端为准(容器拿到的是后端解析的结果)。
 */
export function previewEnvBody(text: string): { keys: string[]; skipped: number } {
  const keys: string[] = []
  let skipped = 0
  for (const rawLine of text.replace(/^\uFEFF/, '').split(/\r?\n/)) {
    let line = rawLine.trim()
    if (!line || line.startsWith('#')) continue
    if (line.startsWith('export ')) line = line.slice('export '.length).trim()
    const i = line.indexOf('=')
    if (i <= 0) {
      skipped++
      continue
    }
    const key = line.slice(0, i).trim()
    if (!ENV_KEY_PATTERN.test(key)) {
      skipped++
      continue
    }
    if (!keys.includes(key)) keys.push(key)
  }
  return { keys, skipped }
}
