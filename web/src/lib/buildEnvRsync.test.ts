/**
 * buildEnvRsync 回归:三档判定的口径。
 *
 * 「内置目录每条镜像都被实测过」这条防漂移住在 Go 侧(internal/buildenv/rsync_drift_test.go):
 * 前端 tsconfig 没引 node 类型,单测里读不了 seed.go。
 */
import { describe, it, expect } from 'vitest'
import { rsyncSupportFor } from './buildEnvRsync'

describe('rsyncSupportFor', () => {
  it('说没有的是实测过的内置目录镜像', () => {
    expect(rsyncSupportFor('node:22-alpine')).toBe('no')
    expect(rsyncSupportFor('registry.cn-qingdao.aliyuncs.com/fubin/vfox-full:1.0.14')).toBe('no')
  })

  it('没实测过的镜像串说「运行时决定」,绝不替用户猜', () => {
    expect(rsyncSupportFor('harbor.local/team/my-jdk:17-with-rsync')).toBe('unknown')
    expect(rsyncSupportFor('')).toBe('unknown')
    expect(rsyncSupportFor(undefined)).toBe('unknown')
  })

  it('镜像串要精确匹配:尾巴差一点就退回 unknown', () => {
    expect(rsyncSupportFor('node:22')).toBe('unknown')
    expect(rsyncSupportFor(' node:22-alpine ')).toBe('no')
  })
})
