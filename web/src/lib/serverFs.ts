/**
 * 远程文件面板的纯逻辑(视图只留渲染与调用)。
 *
 * 路径语义跟的是**远端**的 POSIX,不是浏览器所在的 OS:后端把路径原样交给 SFTP/exec,
 * 分隔符只有 `/`。所以这里一律自己做规范化,不用 path 库、也不碰 window。
 *
 * 单引号转义是给「面板 → 终端」那条联动用的:面板里点目录要往 PTY 打 `cd '<路径>'`,
 * 路径是人/远端给的字符串,里面什么都可能有一一不转义就是一个命令注入点。
 */

import type { FsEntry } from '../api/serverFs'

/** 根目录。 */
export const ROOT = '/'

/** 规范化:折叠重复斜杠与 `.`、去掉尾斜杠(根除外)。绝对路径保持绝对。 */
export function normalizePath(p: string): string {
  if (!p) return ROOT
  const absolute = p.startsWith('/')
  const out: string[] = []
  for (const seg of p.split('/')) {
    if (!seg || seg === '.') continue
    if (seg === '..') {
      out.pop()
      continue
    }
    out.push(seg)
  }
  return (absolute ? '/' : '') + out.join('/')
}

/** 上一级目录(根停在根:列目录不该跑出文件系统)。 */
export function parentDir(p: string): string {
  const n = normalizePath(p)
  if (n === ROOT) return ROOT
  const cut = n.lastIndexOf('/')
  if (cut <= 0) return ROOT
  return n.slice(0, cut)
}

/** 把基名接在目录后(基名里的路径分隔符由调用方先清掉)。 */
export function joinPath(dir: string, name: string): string {
  const d = normalizePath(dir)
  return d === ROOT ? `/${name}` : `${d}/${name}`
}

/** 面包屑:每一段都能点,故一并给出该段的完整路径。 */
export interface Crumb {
  label: string
  path: string
}

export function pathCrumbs(p: string): Crumb[] {
  const n = normalizePath(p)
  const crumbs: Crumb[] = [{ label: ROOT, path: ROOT }]
  if (n === ROOT) return crumbs
  let acc = ''
  for (const seg of n.split('/')) {
    if (!seg) continue
    acc += `/${seg}`
    crumbs.push({ label: seg, path: acc })
  }
  return crumbs
}

/** 目录在前、同级按名升序;远端返回的顺序不保证,界面排序不能跟着后端抖。 */
export function sortEntries(entries: FsEntry[]): FsEntry[] {
  return [...entries].sort((a, b) => {
    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1
    return a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: 'base' })
  })
}

/** 人读字节(二进制单位;目录不显示体积,调用方自己挡)。 */
export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return '—'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${i === 0 ? v : v.toFixed(1)} ${units[i]}`
}

/**
 * mtime → 本地时刻字符串。0 表示对端没给时间(exec 兜底里 stat 不支持就是这种),
 * 这时回空串让界面直接省略这一列,而不是显示 1970 年骗人。
 */
export function formatMtime(unix: number): string {
  if (!unix || unix <= 0) return ''
  const d = new Date(unix * 1000)
  if (Number.isNaN(d.getTime())) return ''
  return d.toLocaleString(undefined, {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  })
}

/** mode 低 9 位 → `rwxr-xr-x`(setuid/setgid/sticky 不在面板里逞能,忽略)。 */
export function modeToRwx(mode: number): string {
  let out = ''
  const names = ['r', 'w', 'x']
  for (let g = 0; g < 3; g++) {
    const bits = (mode >> (6 - g * 3)) & 0o7
    for (let b = 0; b < 3; b++) out += bits & (4 >> b) ? names[b] : '-'
  }
  return out
}

/**
 * `ls -l` 式 10 位权限串:首字符是类型(d 目录 / l 链接 / - 普通文件)。
 * 后 9 位取自 mode,而 mode 只有权限位(两路后端都只回 Perm),所以类型首字符
 * 必须问 isDir/isLink,不能从 mode 里猜。
 */
export function modeToLs(entry: { isDir: boolean; isLink: boolean; mode: number }): string {
  return (entry.isDir ? 'd' : entry.isLink ? 'l' : '-') + modeToRwx(entry.mode)
}

/**
 * 后缀名(小写、不含点),给「类型」列用;无后缀与点开头隐藏文件回空串。
 * `.bashrc` 的点是隐藏标记不是分隔符,把它当后缀会显示成「BASHRC 文件」。
 */
export function fileExt(name: string): string {
  const dot = name.lastIndexOf('.')
  if (dot <= 0 || dot === name.length - 1) return ''
  return name.slice(dot + 1).toLowerCase()
}

/** POSIX 单引号安全引用:整串包进单引号,内部单引号换成 `'\''`。 */
export function shellQuote(s: string): string {
  return `'${s.replace(/'/g, `'\\''`)}'`
}

/** 面板 → 终端:让登录 shell 切到该目录(CR 而非 LF:PTY 行编辑认 CR 回车)。 */
export function cdCommand(path: string): string {
  return `cd ${shellQuote(normalizePath(path))}\r`
}

/** 终端 OSC 7 载荷(`file://host/path`)→ 纯路径;形状不对回空串。 */
export function pathFromOsc7(payload: string): string {
  const colon = payload.indexOf(';')
  const rest = (colon >= 0 ? payload.slice(colon + 1) : payload).trim()
  if (!rest.startsWith('file://')) return ''
  const slash = rest.indexOf('/', 'file://'.length)
  if (slash < 0) return ''
  try {
    return decodeURIComponent(rest.slice(slash))
  } catch {
    return rest.slice(slash)
  }
}
