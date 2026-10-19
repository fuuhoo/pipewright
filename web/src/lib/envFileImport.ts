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

// ─── Nacos 来源 ──────────────────────────────────────────────────────────────

/** 与后端 nacosBaseURLMaxLen 一致。 */
export const NACOS_BASE_URL_MAX = 512

/** 与后端 nacosFieldMaxLen 一致(Data ID / 分组 / 命名空间 ID 共用)。 */
export const NACOS_FIELD_MAX = 256

/** 与后端 pipeline.NacosDefaultGroup 一致:留空即这一档,表单的占位提示要说清。 */
export const NACOS_DEFAULT_GROUP = 'DEFAULT_GROUP'

export type NacosUrlIssue = '' | 'empty' | 'tooLong' | 'hasSpace' | 'unparsable' | 'badScheme' | 'noHost' | 'hasUserInfo' | 'hasQuery' | 'blocked'

/**
 * 配置中心根地址判据(后端 pipeline.NacosBaseURLIssue)。
 *
 * 两处偏差是刻意的:
 * - 主机名自己去切。WHATWG 的 URL 会把 `http:///v1` 那种空主机「救」成 host=v1,而后端 url.Parse
 *   判它「缺少主机名」;照 URL 的口径走,表单就会放过一个后端必拒的地址。
 * - 不做 DNS:浏览器解不了也不该解。这里只收「字面 IP」那几种恒拒段,让 169.254.169.254 这种
 *   在表单里就红,不用等构建失败。回环不在这里拒 —— 自建 Nacos 常与平台同机,而且本机起服务自测
 *   是这条功能唯一的本地验证路径。
 */
export function nacosBaseUrlIssue(raw: string): NacosUrlIssue {
  const v = (raw ?? '').trim()
  if (!v) return 'empty'
  if (v.length > NACOS_BASE_URL_MAX) return 'tooLong'
  if (/\s/.test(v)) return 'hasSpace'
  const sep = v.indexOf('://')
  if (sep < 0) {
    // 没有 // 的写法:有协议前缀就按协议拒(与后端 scheme 白名单同一句),否则算解析不了。
    return /^[A-Za-z][A-Za-z0-9+.-]*:/.test(v) ? 'badScheme' : 'unparsable'
  }
  const scheme = v.slice(0, sep).toLowerCase()
  if (scheme !== 'http' && scheme !== 'https') return 'badScheme'
  const rest = v.slice(sep + 3)
  const authority = rest.split(/[/?#]/)[0]
  if (!authority) return 'noHost'
  if (authority.includes('@')) return 'hasUserInfo'
  if (v.includes('?') || v.includes('#')) return 'hasQuery'
  if (isBlockedAddress(authorityHost(authority))) return 'blocked'
  return ''
}

/** 从 authority 切出主机名(IPv6 带方括号,尾部 :端口 要去掉)。 */
function authorityHost(authority: string): string {
  if (authority.startsWith('[')) {
    const end = authority.indexOf(']')
    return end > 0 ? authority.slice(1, end) : authority
  }
  return authority.split(':')[0]
}

/** 与后端 internal/netguard 恒拒的那几段对齐(链路本地 / 未指定 / 组播);私网与回环放行。 */
function isBlockedAddress(host: string): boolean {
  const v4 = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(host)
  if (v4) {
    const a = Number(v4[1])
    const b = Number(v4[2])
    if (a === 0) return true
    if (a === 169 && b === 254) return true
    return a >= 224 && a <= 239
  }
  const h = host.toLowerCase()
  if (!h.includes(':')) return false
  if (h.replace(/[:0]/g, '') === '') return true // :: 与 0:0:0:0:0:0:0:0 都是「未指定」
  if (h.startsWith('fe8') || h.startsWith('fe9') || h.startsWith('fea') || h.startsWith('feb')) return true
  return h.startsWith('ff')
}

export type NacosCoordIssue = '' | 'tooLong' | 'dirty'

/**
 * 一段 Nacos 坐标(Data ID / 分组 / 命名空间 ID)的判据(后端 nacosCoordOK)。
 * 这几个值要拼进 HTTP 查询串:带空白或控制字符就会出现「明明填了却报找不到」,而且那是最难查的一种错。
 */
export function nacosCoordIssue(value: string, maxLen: number = NACOS_FIELD_MAX): NacosCoordIssue {
  const v = (value ?? '').trim()
  if (v.length > maxLen) return 'tooLong'
  // 逐码点判,不写 [\x00-\x1f] 那种正则:控制字符写进正则字面量会被 eslint 的 no-control-regex 拦住。
  for (const ch of v) {
    const c = ch.charCodeAt(0)
    if (c < 0x20 || c === 0x7f || /\s/.test(ch)) return 'dirty'
  }
  return ''
}
