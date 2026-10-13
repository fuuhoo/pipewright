import { describe, it, expect } from 'vitest'
import { mergeIntoTextarea, presetChipLabel } from './fieldPresets'

describe('presetChipLabel', () => {
  it('单行值原样做文案', () => {
    expect(presetChipLabel('node_modules')).toBe('node_modules')
  })

  it('多行值(compose 骨架)只显首行,完整正文留给 title', () => {
    expect(presetChipLabel('services:\n  web:\n    image: nginx:latest')).toBe('services: …')
  })

  it('两份 k8s 骨架开头同字,取 kind 才分得开', () => {
    const deployment = 'apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: api'
    const statefulset = 'apiVersion: apps/v1\nkind: StatefulSet\nmetadata:\n  name: api'
    expect(presetChipLabel(deployment)).toBe('Deployment')
    expect(presetChipLabel(statefulset)).toBe('StatefulSet')
  })
})

describe('mergeIntoTextarea', () => {
  it('空框(含只有空白)直接填入', () => {
    expect(mergeIntoTextarea('', 'dist')).toBe('dist')
    expect(mergeIntoTextarea('  \n\n', 'dist')).toBe('dist')
  })

  it('有内容往末尾追加,原有行不动', () => {
    expect(mergeIntoTextarea('node_modules', '.m2/repository')).toBe('node_modules\n.m2/repository')
  })

  it('重复点同一颗不累积', () => {
    expect(mergeIntoTextarea('node_modules', 'node_modules')).toBe('node_modules')
    expect(mergeIntoTextarea('a\nnode_modules\nb', 'node_modules')).toBe('a\nnode_modules\nb')
  })

  it('多行段已整段存在则不动;段首同名的单行值也不重复塞', () => {
    const yaml = 'services:\n  web:\n    image: nginx:latest'
    expect(mergeIntoTextarea('prefix\n' + yaml, yaml)).toBe('prefix\n' + yaml)
    expect(mergeIntoTextarea(yaml, 'services:')).toBe(yaml)
  })

  it('末尾空行不留下双换行', () => {
    expect(mergeIntoTextarea('dist\n\n', '.gradle')).toBe('dist\n.gradle')
  })

  it('CRLF 输入先归一成 LF', () => {
    expect(mergeIntoTextarea('dist\r\n', '.gradle')).toBe('dist\n.gradle')
  })
})
