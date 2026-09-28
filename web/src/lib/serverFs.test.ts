/**
 * serverFs 纯逻辑回归:路径规范化/导航、排序、字节与权限显示,以及最要紧的
 * 「面板 → 终端」那条 cd 的转义 —— 它是把远端字符串打进 PTY 的唯一入口。
 */
import { describe, it, expect } from 'vitest'
import type { FsEntry } from '../api/serverFs'
import {
  ROOT,
  cdCommand,
  cwdReportScript,
  formatBytes,
  formatMtime,
  joinPath,
  modeToRwx,
  normalizePath,
  parentDir,
  pathCrumbs,
  pathFromOsc7,
  shellQuote,
  sortEntries,
} from './serverFs'

function entry(name: string, isDir: boolean, size = 0): FsEntry {
  return { name, path: (isDir ? '/' : '/x/') + name, isDir, isLink: false, size, mode: 0, mtime: 0 }
}

describe('normalizePath', () => {
  it('折叠重复斜杠、. 与尾斜杠', () => {
    expect(normalizePath('/app//sub/')).toBe('/app/sub')
    expect(normalizePath('/./app')).toBe('/app')
    expect(normalizePath('/')).toBe(ROOT)
    expect(normalizePath('')).toBe(ROOT)
  })

  it('.. 往上退,退过头停在根(绝不逃出文件系统)', () => {
    expect(normalizePath('/app/sub/../log')).toBe('/app/log')
    expect(normalizePath('/../../etc/passwd')).toBe('/etc/passwd')
  })
})

describe('parentDir / joinPath', () => {
  it('根停在根;一级目录的上级是根', () => {
    expect(parentDir(ROOT)).toBe(ROOT)
    expect(parentDir('/app')).toBe(ROOT)
    expect(parentDir('/app/sub/')).toBe('/app')
  })

  it('根下面不出现双斜杠', () => {
    expect(joinPath('/', 'a.txt')).toBe('/a.txt')
    expect(joinPath('/app', 'a.txt')).toBe('/app/a.txt')
  })
})

describe('pathCrumbs', () => {
  it('每段都带完整路径,首段是根', () => {
    expect(pathCrumbs('/var/log')).toEqual([
      { label: '/', path: '/' },
      { label: 'var', path: '/var' },
      { label: 'log', path: '/var/log' },
    ])
    expect(pathCrumbs('/')).toEqual([{ label: '/', path: '/' }])
  })
})

describe('sortEntries', () => {
  it('目录在前,同级按名升序;数字段按数值比(b2 在 b10 前)', () => {
    const sorted = sortEntries([entry('b10', false), entry('z.log', false), entry('b2', true), entry('a', true)])
    expect(sorted.map((e) => e.name)).toEqual(['a', 'b2', 'b10', 'z.log'])
  })

  it('不改动入参(排序后原数组仍是接口回的那份)', () => {
    const src = [entry('b', false), entry('a', true)]
    expect(sortEntries(src).map((e) => e.name)).toEqual(['a', 'b'])
    expect(src.map((e) => e.name)).toEqual(['b', 'a'])
  })
})

describe('formatBytes / formatMtime', () => {
  it('字节按二进制单位,1 位小数', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(5)).toBe('5 B')
    expect(formatBytes(1024)).toBe('1.0 KiB')
    expect(formatBytes(1536)).toBe('1.5 KiB')
    expect(formatBytes(-1)).toBe('—')
  })

  it('对端没给 mtime(0)时回空串,不假装是 1970', () => {
    expect(formatMtime(0)).toBe('')
    expect(formatMtime(1_700_000_000)).not.toBe('')
  })
})

describe('modeToRwx', () => {
  it('取低 9 位', () => {
    expect(modeToRwx(0o755)).toBe('rwxr-xr-x')
    expect(modeToRwx(0o644)).toBe('rw-r--r--')
    expect(modeToRwx(0o600)).toBe('rw-------')
    expect(modeToRwx(0)).toBe('---------')
  })

  it('高位(setuid 等)不参与显示', () => {
    expect(modeToRwx(0o4755)).toBe('rwxr-xr-x')
  })
})

describe('shellQuote / cdCommand', () => {
  it('单引号内的单引号换成标准 \'"\'" 断串', () => {
    expect(shellQuote("it's")).toBe(`'it'\\''s'`)
  })

  it('带分号/空格的怪名字被整串包住,不会拆成两条命令', () => {
    expect(cdCommand('/a b;rm -rf /x')).toBe("cd '/a b;rm -rf /x'\r")
  })

  it('注入样例:闭合引号 + 命令分隔也只当字面量', () => {
    const path = "/tmp'; touch /tmp/pwned; '"
    const cmd = cdCommand(path)
    expect(cmd.startsWith("cd '")).toBe(true)
    expect(cmd.endsWith('\r')).toBe(true)
    // 反向走一遍:剥掉外层单引号、把 '\'' 还原成 ',必须拿回原路径 ——
    // 说明转义是可逆的,远端 shell 也只会把它当一个字面量参数。
    const arg = cmd.slice('cd '.length, -1)
    expect(arg.startsWith("'") && arg.endsWith("'")).toBe(true)
    expect(arg.slice(1, -1).replace(/'\\''/g, "'")).toBe(path)
  })

  it('回车用 CR(PTY 行编辑认 CR,LF 在部分 shell 不提交整行)', () => {
    expect(cdCommand('/app').endsWith('\r')).toBe(true)
  })
})

describe('pathFromOsc7', () => {
  it('剥掉 scheme 与 host,只留路径,并还原百分号转义', () => {
    expect(pathFromOsc7('file://host/app/data')).toBe('/app/data')
    expect(pathFromOsc7('file://host/a%20b/c')).toBe('/a b/c')
    expect(pathFromOsc7('file:///tmp')).toBe('/tmp')
  })

  it('不是 file:// 形状就回空串(调用方据此忽略这条 OSC)', () => {
    expect(pathFromOsc7('file://host')).toBe('')
    expect(pathFromOsc7('8;something')).toBe('')
    expect(pathFromOsc7('')).toBe('')
  })
})

describe('cwdReportScript', () => {
  it('bash 走 PROMPT_COMMAND,zsh 走 precmd,其余 shell 静默跳过', () => {
    const s = cwdReportScript()
    expect(s).toContain('BASH_VERSION')
    expect(s).toContain('PROMPT_COMMAND=')
    expect(s).toContain('ZSH_VERSION')
    expect(s).toContain('precmd()')
  })

  it('打的是 OSC 7(file:// + $PWD),不是给人看的一行文本', () => {
    const s = cwdReportScript()
    expect(s).toContain('printf "\\033]7;file://%s%s\\007"')
    expect(s).toContain('$PWD')
  })

  it('PROMPT_COMMAND 用单引号赋值:双引号会在赋值那刻展开 $PWD,之后永远上报登录目录', () => {
    const s = cwdReportScript()
    expect(s).toContain(`PROMPT_COMMAND='printf "\\033]7;file://%s%s\\007" "$HOSTNAME" "$PWD"'`)
    expect(s).not.toContain('PROMPT_COMMAND="')
  })
})
