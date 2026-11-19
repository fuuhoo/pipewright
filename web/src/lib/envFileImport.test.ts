/**
 * envFileImport 纯逻辑回归:与后端 internal/pipeline/envfile.go 同一条判据,漂移了这里先红。
 */
import { describe, it, expect } from 'vitest'
import {
  ENV_MAX_BYTES,
  ENV_PATH_MAX,
  NACOS_BASE_URL_MAX,
  NACOS_DEFAULT_GROUP,
  NACOS_FIELD_MAX,
  envBodyIssue,
  isEnvFileName,
  nacosBaseUrlIssue,
  nacosCoordIssue,
  parseEnvBodyVars,
  planEnvVarMerge,
  previewEnvBody,
  repoEnvPathIssue,
} from './envFileImport'

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

describe('parseEnvBodyVars', () => {
  it('取值:trim、脱成对引号、行内 # 保留', () => {
    const got = parseEnvBodyVars('\uFEFFA= 1 \r\nexport B="two words"\nPASS=abc#123\nQ=\'x\'\n')
    expect(got.vars).toEqual([
      { key: 'A', value: '1' },
      { key: 'B', value: 'two words' },
      { key: 'PASS', value: 'abc#123' },
      { key: 'Q', value: 'x' },
    ])
    expect(got.skipped).toBe(0)
  })

  it('不对成/不配对的引号与空值原样保留', () => {
    const got = parseEnvBodyVars('A="x\nB=\nC= ""\n')
    expect(got.vars).toEqual([
      { key: 'A', value: '"x' },
      { key: 'B', value: '' },
      { key: 'C', value: '' },
    ])
  })

  it('同名后写覆盖值但保持首次位置(与后端一致)', () => {
    expect(parseEnvBodyVars('A=1\nB=2\nA=3\n').vars).toEqual([
      { key: 'A', value: '3' },
      { key: 'B', value: '2' },
    ])
  })

  it('非法行只计数,不影响其他行', () => {
    const got = parseEnvBodyVars('NOEQ\n=novalue\n9bad=x\nA B=1\nGOOD=1\n')
    expect(got.vars).toEqual([{ key: 'GOOD', value: '1' }])
    expect(got.skipped).toBe(4)
  })
})

describe('planEnvVarMerge', () => {
  it('同名明文行就地更新,新键追加,secret 行保持不动', () => {
    const existing = [
      { key: '', secret: false },
      { key: 'PORT', secret: false },
      { key: 'TOKEN', secret: true },
    ]
    const plan = planEnvVarMerge(existing, [
      { key: 'PORT', value: '9090' },
      { key: 'TOKEN', value: 'plain' },
      { key: 'NEW', value: 'v' },
    ])
    expect(plan.updates).toEqual([{ index: 1, value: '9090' }])
    expect(plan.appends).toEqual([{ key: 'NEW', value: 'v' }])
    expect(plan.secretSkipped).toEqual(['TOKEN'])
  })

  it('键名按 trim 后比较;重复的现有行只认第一行', () => {
    const plan = planEnvVarMerge(
      [
        { key: ' A ', secret: false },
        { key: 'A', secret: false },
      ],
      [{ key: 'A', value: '1' }],
    )
    expect(plan.updates).toEqual([{ index: 0, value: '1' }])
    expect(plan.appends).toEqual([])
  })
})

describe('nacosBaseUrlIssue', () => {
  it('本机 / 私网 / 公网 https 一律放行(自建配置中心常与平台同机)', () => {
    for (const u of [
      'http://127.0.0.1:8848/nacos',
      'http://10.0.0.9:8848',
      'https://nacos.example.com/nacos',
      'http://[fd00::9]:8848/nacos',
    ]) {
      expect(nacosBaseUrlIssue(u)).toBe('')
    }
  })

  it('恒拒段:链路本地(云元数据)、未指定、组播', () => {
    for (const u of ['http://169.254.169.254/nacos', 'http://0.0.0.0:8848', 'http://[::]:8848', 'http://224.0.0.1/x', 'http://[ff02::1]/x']) {
      expect(nacosBaseUrlIssue(u)).toBe('blocked')
    }
  })

  it('形状问题分别报出,便于文案对上', () => {
    expect(nacosBaseUrlIssue('')).toBe('empty')
    expect(nacosBaseUrlIssue('   ')).toBe('empty')
    expect(nacosBaseUrlIssue('nacos:8848')).toBe('badScheme')
    expect(nacosBaseUrlIssue('ftp://127.0.0.1')).toBe('badScheme')
    expect(nacosBaseUrlIssue('http:///v1/cs/configs')).toBe('noHost')
    expect(nacosBaseUrlIssue('nacos-8848')).toBe('unparsable')
    expect(nacosBaseUrlIssue('http://nacos:pw@10.0.0.9')).toBe('hasUserInfo')
    expect(nacosBaseUrlIssue('http://10.0.0.9?dataId=a')).toBe('hasQuery')
    expect(nacosBaseUrlIssue('http://10.0.0.9#x')).toBe('hasQuery')
    expect(nacosBaseUrlIssue('http://10.0. 0.9')).toBe('hasSpace')
  })

  it('额度与后端一致:根地址 512 字符', () => {
    expect(NACOS_BASE_URL_MAX).toBe(512)
    expect(nacosBaseUrlIssue('http://10.0.0.9/' + 'p'.repeat(NACOS_BASE_URL_MAX))).toBe('tooLong')
  })
})

describe('nacosCoordIssue', () => {
  it('普通坐标放行,留空也算合法(是否必填由字段各自判)', () => {
    for (const v of ['prod.env', 'DEFAULT_GROUP', '0d8a1f2b-ns', '']) expect(nacosCoordIssue(v)).toBe('')
  })

  it('含空白或控制字符 = 拼进查询串后会「填了却找不到」', () => {
    expect(nacosCoordIssue('prod env')).toBe('dirty')
    expect(nacosCoordIssue('prod\nenv')).toBe('dirty')
    expect(nacosCoordIssue('a\u0000b')).toBe('dirty')
  })

  it('首尾空白不算错(后端 ConfigString 先 trim)', () => {
    expect(nacosCoordIssue('  prod.env  ')).toBe('')
  })

  it('上限与后端 nacosFieldMaxLen 一致:256', () => {
    expect(NACOS_FIELD_MAX).toBe(256)
    expect(nacosCoordIssue('G'.repeat(NACOS_FIELD_MAX))).toBe('')
    expect(nacosCoordIssue('G'.repeat(NACOS_FIELD_MAX + 1))).toBe('tooLong')
  })

  it('默认分组常量与后端逐字一致', () => {
    expect(NACOS_DEFAULT_GROUP).toBe('DEFAULT_GROUP')
  })
})
