import { describe, expect, it } from 'vitest'

import {
  COMPOSE_MAX_BYTES,
  composeIssue,
  composeProjectName,
  composeServiceNames,
  stackNameIssue,
} from './composePaste'

describe('stackNameIssue', () => {
  it('放行常规项目名', () => {
    for (const n of ['my-stack', 'ai_stack', 'web.v2', 'nginx1']) expect(stackNameIssue(n)).toBe('')
  })

  it('空 / 纯空白都不给过', () => {
    expect(stackNameIssue('')).toBe('empty')
    expect(stackNameIssue('   ')).toBe('empty')
  })

  it('后端白名单拦的东西在弹窗里同样先拦:前导 - 会被当 flag,斜杠能穿出受管目录', () => {
    expect(stackNameIssue('-f')).toBe('illegal')
    expect(stackNameIssue('../../etc')).toBe('illegal')
    expect(stackNameIssue('a/b')).toBe('illegal')
    expect(stackNameIssue('.hidden')).toBe('illegal')
    expect(stackNameIssue('my stack')).toBe('illegal')
    expect(stackNameIssue('栈')).toBe('illegal') // \w 是 ASCII 语义,和 Go 的 [\w] 同口径
  })

  it('超长单独报(后端还有 128 上限)', () => {
    expect(stackNameIssue('a'.repeat(129))).toBe('tooLong')
    expect(stackNameIssue('a'.repeat(128))).toBe('')
  })
})

describe('composeIssue', () => {
  it('空 / 只有注释都不给过', () => {
    expect(composeIssue('')).toBe('empty')
    expect(composeIssue('\n  \t\n')).toBe('empty')
    expect(composeIssue('services:\n  web:\n    image: nginx\n')).toBe('')
  })

  it('超过 512 KiB 拦下,且按字节算(中文一个字三个字节)', () => {
    const justFits = 'a'.repeat(COMPOSE_MAX_BYTES - 16) + '\n'
    expect(composeIssue(justFits)).toBe('')
    expect(composeIssue('啊'.repeat((COMPOSE_MAX_BYTES >> 1) + 8))).toBe('tooLarge')
  })
})

describe('composeServiceNames', () => {
  it('取 services 下一层的键,更深一层的是服务字段', () => {
    const yaml = `services:
  web:
    image: nginx:latest
    ports:
      - "8080:80"
  db:
    environment:
      POSTGRES_PASSWORD: x
`
    expect(composeServiceNames(yaml)).toEqual(['web', 'db'])
  })

  it('注释、--- 、空行、四空格缩进都不影响识别', () => {
    const yaml = `---
# 顶层注释
services:
    api:        # 行内注释
        image: node
    worker:
        image: node
`
    expect(composeServiceNames(yaml)).toEqual(['api', 'worker'])
  })

  it('撞上下一个顶层键就结束,不会把 volumes 的键当服务', () => {
    const yaml = `services:
  web:
    image: nginx
volumes:
  data:
    driver: local
`
    expect(composeServiceNames(yaml)).toEqual(['web'])
  })

  it('没有 services / flow 写法 → 空数组(由界面提示,不拦部署)', () => {
    expect(composeServiceNames('image: nginx\n')).toEqual([])
    expect(composeServiceNames('services: { web: { image: nginx } }\n')).toEqual([])
  })

  it('同名服务只记一次(flow 展开/重复键都不会把摘要撑大)', () => {
    expect(composeServiceNames('services:\n  web:\n    image: a\n  web:\n    image: b\n')).toEqual(['web'])
  })
})

describe('composeProjectName', () => {
  it('读顶层 name,给项目名输入框一个默认值', () => {
    expect(composeProjectName('name: my-stack\nservices:\n  web:\n    image: nginx\n')).toBe('my-stack')
    expect(composeProjectName('services:\n  web:\n    image: nginx\nname: later\n')).toBe('later')
    expect(composeProjectName('name: "quoted-stack"\n')).toBe('quoted-stack')
  })

  it('服务内部的 name 不算项目名;没有则空串', () => {
    expect(composeProjectName('services:\n  web:\n    name: inner\n')).toBe('')
    expect(composeProjectName('services:\n  web:\n    image: nginx\n')).toBe('')
  })
})
