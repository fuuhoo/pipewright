/**
 * envFileImport 纯逻辑回归:与后端 internal/pipeline/envfile.go 同一条判据,漂移了这里先红。
 */
import { describe, it, expect } from 'vitest'
import { ENV_MAX_BYTES, ENV_PATH_MAX, isEnvFileName, previewEnvBody, repoEnvPathIssue, envBodyIssue } from './envFileImport'

describe('isEnvFileName', () => {
  it('认 .env / .env.后缀 / 前缀.env 三种形状', () => {
    for (const n of ['.env', '.env.production', 'prod.env', '.env.local']) {
      expect(isEnvFileName(n)).toBe(true)
    }
  })

  it('不认不像 env 文件的名字', () => {
    for (const n of ['', 'env', 'docker-compose.yml', '.environmentrc', 'application.yaml']) {
      expect(isEnvFileName(n)).toBe(false)
    }
  })
})

describe('repoEnvPathIssue', () => {
  it('仓库内相对路径放行', () => {
    for (const p of ['.env', '.env.production', 'deploy/prod.env', 'backend/.env.local']) {
      expect(repoEnvPathIssue(p)).toBe('')
    }
  })

  it('绝对路径 / 上跳 / 空段 / 反斜杠都归「不是仓库内相对路径」', () => {
    for (const p of ['/etc/env', '../secret.env', 'a/../b.env', 'a//b.env', 'deploy\\prod.env']) {
      expect(repoEnvPathIssue(p)).toBe('notRelative')
    }
  })

  it('空值与不像 env 文件分别报出,便于文案对上', () => {
    expect(repoEnvPathIssue('   ')).toBe('empty')
    expect(repoEnvPathIssue('docker-compose.yml')).toBe('notEnvFile')
  })

  it('超长路径先报超长(与后端 512 同一额度)', () => {
    expect(repoEnvPathIssue('a/'.repeat(ENV_PATH_MAX) + '.env')).toBe('tooLong')
  })
})

describe('envBodyIssue', () => {
  it('空白正文报空,超限报过大', () => {
    expect(envBodyIssue('')).toBe('empty')
    expect(envBodyIssue('\n   \n')).toBe('empty')
    expect(envBodyIssue('A=1')).toBe('')
    expect(envBodyIssue('A=' + 'x'.repeat(ENV_MAX_BYTES))).toBe('tooLarge')
  })

  it('额度按 UTF-8 字节数算,不是字符数', () => {
    // 一个中文字 3 字节:用字符数判会少算三分之一。
    const cjk = 'A=' + '字'.repeat(Math.floor(ENV_MAX_BYTES / 3) + 1)
    expect(envBodyIssue(cjk)).toBe('tooLarge')
  })

  it('上限与后端 pipeline.EnvMaxBytes 一致(64 KiB)', () => {
    expect(ENV_MAX_BYTES).toBe(65536)
  })
})

describe('previewEnvBody', () => {
  it('认 export 前缀、BOM、CRLF,行内 # 不剥', () => {
    const got = previewEnvBody('\uFEFFA=1\r\nexport B=2\nPASS=abc#123\n')
    expect(got.keys).toEqual(['A', 'B', 'PASS'])
    expect(got.skipped).toBe(0)
  })

  it('非法键名与没有 = 的行计入 skipped', () => {
    const got = previewEnvBody('NOEQ\n=novalue\n9bad=x\nA B=1\nGOOD=1\n')
    expect(got.keys).toEqual(['GOOD'])
    expect(got.skipped).toBe(4)
  })

  it('重名只列一次(位置取首次),与后端「后写覆盖但保持首次位置」一致', () => {
    expect(previewEnvBody('A=1\nB=2\nA=3\n').keys).toEqual(['A', 'B'])
  })

  it('注释与空行不计入 skipped(那是正常写法)', () => {
    expect(previewEnvBody('# comment\n\nA=1\n').skipped).toBe(0)
  })
})
