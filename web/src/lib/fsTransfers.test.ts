/**
 * 传输记录的回归:身份、排序、上限裁剪与会话内存储。
 *
 * 要盯住的是三条会静默骗人的行为:
 *   · 下载只能记「已交给浏览器」,不能画成完成 —— 浏览器进度不在 JS 这边。
 *   · 目录下载的体积未知要如实回 null,渲染成 0 B 就是假数据。
 *   · 批次每帧重算时发起时刻不许漂(用调用方传入的 at),正在传的行也不许被新批次挤掉位置。
 */
import { beforeEach, describe, expect, it } from 'vitest'
import {
  TRANSFER_MAX_ROWS,
  applyUploadBatch,
  capTransfers,
  clearTransferLog,
  downloadRowOf,
  formatClock,
  isSettled,
  newTransferBatchId,
  prependTransfer,
  setTransferLog,
  transferLogOf,
  transferSummary,
  uploadRowsOf,
  type TransferRow,
} from './fsTransfers'
import type { UploadItemState } from './fsUpload'

function item(key: string, rel: string, over: Partial<UploadItemState> = {}): UploadItemState {
  return { key, rel, size: 100, offset: 0, status: 'queued', ...over }
}

function row(id: string, over: Partial<TransferRow> = {}): TransferRow {
  return {
    id,
    direction: 'upload',
    name: `${id}.txt`,
    path: `/tmp/${id}.txt`,
    size: 100,
    offset: 0,
    status: 'queued',
    at: 1000,
    ...over,
  }
}

describe('uploadRowsOf', () => {
  it('把批次内序号换成全局身份,并按发起目录拼出绝对路径', () => {
    const rows = uploadRowsOf('b7', '/tmp', [item('0', 'a.txt'), item('1', 'src/b.c', { status: 'uploading', offset: 40 })], 1234)
    expect(rows).toEqual([
      { id: 'b7#0', direction: 'upload', name: 'a.txt', path: '/tmp/a.txt', size: 100, offset: 0, status: 'queued', at: 1234, error: undefined },
      { id: 'b7#1', direction: 'upload', name: 'src/b.c', path: '/tmp/src/b.c', size: 100, offset: 40, status: 'uploading', at: 1234, error: undefined },
    ])
  })

  it('根目录不拼出 //,错误码原样带上', () => {
    const rows = uploadRowsOf('b1', '/', [item('0', 'a.txt', { status: 'error', error: 'network' })], 1)
    expect(rows[0]!.path).toBe('/a.txt')
    expect(rows[0]!.error).toBe('network')
  })
})

describe('applyUploadBatch', () => {
  it('首帧把整批放到最前,后续帧原位覆盖(不跳行)', () => {
    const first = uploadRowsOf('b1', '/tmp', [item('0', 'a.txt'), item('1', 'b.txt')], 10)
    let rows = applyUploadBatch([row('old', { at: 5 })], first)
    expect(rows.map((r) => r.id)).toEqual(['b1#0', 'b1#1', 'old'])

    const second = uploadRowsOf('b1', '/tmp', [item('0', 'a.txt', { status: 'done', offset: 100 }), item('1', 'b.txt', { status: 'uploading', offset: 30 })], 10)
    rows = applyUploadBatch(rows, second)
    expect(rows.map((r) => r.id)).toEqual(['b1#0', 'b1#1', 'old'])
    expect(rows[0]!.status).toBe('done')
    expect(rows[1]!.offset).toBe(30)
  })

  it('后发起的下载不会被在传的批次反复压到下面', () => {
    let rows = applyUploadBatch([], uploadRowsOf('b1', '/tmp', [item('0', 'a.txt')], 10))
    rows = prependTransfer(rows, downloadRowOf({ name: 'a.txt', path: '/tmp/a.txt', size: 100 }))
    rows = applyUploadBatch(rows, uploadRowsOf('b1', '/tmp', [item('0', 'a.txt', { status: 'done', offset: 100 })], 10))
    expect(rows.map((r) => r.id)).toEqual(['d1', 'b1#0'])
  })
})

describe('downloadRowOf', () => {
  it('只记发起,状态是 started,同一路径再点也是新身份', () => {
    const a = downloadRowOf({ name: 'a.txt', path: '/tmp/a.txt', size: 100 })
    const b = downloadRowOf({ name: 'a.txt', path: '/tmp/a.txt', size: 100 })
    expect(a.direction).toBe('download')
    expect(a.status).toBe('started')
    expect(a.size).toBe(100)
    expect(a.id).not.toBe(b.id)
    expect(isSettled(a)).toBe(true)
  })

  it('目录体积发起时未知,如实回 null', () => {
    const d = downloadRowOf({ name: 'src', path: '/tmp/src', size: 0, isDir: true })
    expect(d.size).toBeNull()
  })
})

describe('capTransfers', () => {
  it('未超上限原样返回,超了先丢落定的旧行、保住正在传的', () => {
    const active = row('a', { at: 1, status: 'uploading' })
    const old = row('o', { at: 2, status: 'done' })
    expect(capTransfers([active, old], 5)).toEqual([active, old])

    const many = Array.from({ length: TRANSFER_MAX_ROWS }, (_, i) => row(`x${i}`, { at: i + 2, status: 'done' }))
    const capped = capTransfers([active, ...many], TRANSFER_MAX_ROWS)
    expect(capped).toHaveLength(TRANSFER_MAX_ROWS)
    expect(capped.some((r) => r.id === 'a')).toBe(true)
    // 最旧的那条落定记录被挤掉
    expect(capped.some((r) => r.id === 'x0')).toBe(false)
  })
})

describe('transferSummary', () => {
  it('分开数上传与下载,取消计入失败、在传的算 active', () => {
    const s = transferSummary([
      row('u1', { status: 'done' }),
      row('u2', { status: 'error' }),
      row('u3', { status: 'canceled' }),
      row('u4', { status: 'uploading' }),
      downloadRowOf({ name: 'd', path: '/tmp/d', size: 1 }),
    ])
    expect(s).toEqual({ uploads: 4, downloads: 1, done: 1, failed: 2, active: 1 })
  })

  it('空记录不算完成度', () => {
    expect(transferSummary([])).toEqual({ uploads: 0, downloads: 0, done: 0, failed: 0, active: 0 })
  })
})

describe('会话内存储', () => {
  beforeEach(() => {
    clearTransferLog('s1')
    clearTransferLog('s2')
  })

  it('按 serverId 分格:关弹窗重开还在,换服务器看到的是自己那份', () => {
    expect(transferLogOf('s1')).toEqual([])
    setTransferLog('s1', [row('u1')])
    setTransferLog('s2', [row('u2')])
    expect(transferLogOf('s1').map((r) => r.id)).toEqual(['u1'])
    expect(transferLogOf('s2').map((r) => r.id)).toEqual(['u2'])
  })

  it('清空就是把这一格拿掉', () => {
    setTransferLog('s1', [row('u1')])
    clearTransferLog('s1')
    expect(transferLogOf('s1')).toEqual([])
  })

  it('写空数组等于没有记录,不占格子', () => {
    setTransferLog('s1', [row('u1')])
    setTransferLog('s1', [])
    expect(transferLogOf('s1')).toEqual([])
  })

  it('存储侧也受上限保护', () => {
    const many = Array.from({ length: TRANSFER_MAX_ROWS + 50 }, (_, i) => row(`x${i}`))
    setTransferLog('s1', many)
    expect(transferLogOf('s1')).toHaveLength(TRANSFER_MAX_ROWS)
  })
})

describe('isSettled', () => {
  it('只有上传的在途态算未落定,下载一律落定', () => {
    expect(isSettled(row('a', { status: 'queued' }))).toBe(false)
    expect(isSettled(row('b', { status: 'uploading' }))).toBe(false)
    expect(isSettled(row('c', { status: 'done' }))).toBe(true)
    expect(isSettled(row('d', { direction: 'download', status: 'started' }))).toBe(true)
  })
})

describe('newTransferBatchId', () => {
  it('页面内单调递增,批次身份不会撞', () => {
    const a = newTransferBatchId()
    const b = newTransferBatchId()
    expect(a).not.toBe(b)
    expect(b > a).toBe(true)
  })
})

describe('formatClock', () => {
  it('给出时刻没有值就回空串,不显示 00:00:00 骗人', () => {
    expect(formatClock(0)).toBe('')
    expect(formatClock(Number.NaN)).toBe('')
    expect(formatClock(Date.UTC(2026, 0, 2, 3, 4, 5))).toMatch(/\d/)
  })
})
