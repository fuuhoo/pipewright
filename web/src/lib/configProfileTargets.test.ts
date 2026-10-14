/**
 * 目标路径预置的回归:类型/语言的命中与优先级、归一化口径、以及「预置值必须真能注入」这条硬约束。
 */
import { describe, it, expect } from 'vitest'
import { maxPresetChips, targetPathPresets } from './configProfileTargets'

describe('targetPathPresets:按配置类型命中', () => {
  it('maven 给 settings.xml 打头,单复数与大小写都认', () => {
    expect(targetPathPresets('java', 'maven-settings')[0]).toBe('/root/.m2/settings.xml')
    expect(targetPathPresets('java', 'maven-setting')).toEqual(targetPathPresets('java', 'maven-settings'))
    expect(targetPathPresets('java', 'MAVEN_SETTINGS')).toEqual(targetPathPresets('java', 'maven-settings'))
  })

  it('node/python/go/docker 各自落到工具的默认读取位置', () => {
    expect(targetPathPresets('node', 'npmrc')).toEqual(['/root/.npmrc'])
    expect(targetPathPresets('python', 'pip')).toContain('/root/.pip/pip.conf')
    // 类型里同时含 goproxy 与 go 两个关键词:两条都给(认证走 .netrc、go env 文件也常用)
    expect(targetPathPresets('go', 'goproxy')).toEqual(['/root/.netrc', '/root/.config/go/env'])
    expect(targetPathPresets('other', 'docker-config')).toEqual(['/root/.docker/config.json'])
  })

  it('类型比语言更具体:java 语言 + npmrc 类型只给 npmrc', () => {
    expect(targetPathPresets('java', 'npmrc')).toEqual(['/root/.npmrc'])
  })
})

describe('targetPathPresets:语言兜底与空结果', () => {
  it('类型留空时按内置种子的四条兜底', () => {
    expect(targetPathPresets('java', '')).toEqual(['/root/.m2/settings.xml', '/root/.gradle/init.gradle'])
    expect(targetPathPresets('node', '  ')).toEqual(['/root/.npmrc'])
  })

  it('刚打开弹窗(语言与类型都空)给内置那四条常用路径,不让人对着空芯片栏发懵', () => {
    expect(targetPathPresets('', '')).toEqual([
      '/root/.m2/settings.xml',
      '/root/.npmrc',
      '/root/.pip/pip.conf',
      '/root/.netrc',
    ])
  })

  it('选了但都不认识(自定义类型 / 「其他」)则不给芯片', () => {
    expect(targetPathPresets('other', '')).toEqual([])
    expect(targetPathPresets('other', 'nginx-conf')).toEqual([])
    expect(targetPathPresets('', 'nginx-conf')).toEqual([])
  })
})

describe('targetPathPresets:预置值本身可用', () => {
  const combos: Array<[string, string]> = [
    ['java', 'maven-settings'],
    ['node', 'npmrc'],
    ['python', 'pip'],
    ['go', 'env'],
    ['vfox', 'maven-setting'],
    ['java', 'gradle'],
    ['rust', 'cargo'],
  ]

  it('全是绝对路径、不含 ..、无尾斜杠(后端 safeMountPath 的三条判据)', () => {
    for (const [lang, type] of combos) {
      for (const p of targetPathPresets(lang, type)) {
        expect(p.startsWith('/')).toBe(true)
        expect(p).not.toContain('..')
        expect(p.endsWith('/')).toBe(false)
      }
    }
  })

  it('不重复、不超上限', () => {
    for (const [lang, type] of combos) {
      const got = targetPathPresets(lang, type)
      expect(new Set(got).size).toBe(got.length)
      expect(got.length).toBeLessThanOrEqual(maxPresetChips)
    }
  })
})
