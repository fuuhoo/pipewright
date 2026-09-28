/**
 * configProfileFile 纯逻辑回归:提交路径的三分支与上传体积预检。
 */
import { describe, it, expect } from 'vitest'
import { MAX_UPLOAD_BYTES, exceedsUploadLimit, submitMode } from './configProfileFile'

describe('submitMode', () => {
  it('新建:没选文件走 JSON,选了文件走 multipart 上传', () => {
    expect(submitMode(false, false)).toBe('create')
    expect(submitMode(false, true)).toBe('upload')
  })

  it('编辑:没选文件走 PUT(正文来自详情),选了文件走替换', () => {
    expect(submitMode(true, false)).toBe('update')
    expect(submitMode(true, true)).toBe('replace')
  })
})

describe('exceedsUploadLimit', () => {
  it('1MB 以内放行,超出拦住', () => {
    expect(exceedsUploadLimit(MAX_UPLOAD_BYTES)).toBe(false)
    expect(exceedsUploadLimit(MAX_UPLOAD_BYTES + 1)).toBe(true)
  })

  it('上限与后端 PIPEWRIGHT_CONFIG_UPLOAD_MAX_SIZE 默认值一致', () => {
    expect(MAX_UPLOAD_BYTES).toBe(1048576)
  })
})
