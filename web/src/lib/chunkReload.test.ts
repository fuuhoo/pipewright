import { describe, expect, it } from 'vitest'
import { CHUNK_RELOAD_FLAG, isChunkLoadFailure, planChunkReload, type ChunkReloadStorage } from './chunkReload'

interface FakeStorage extends ChunkReloadStorage {
  peek(key: string): string | null
}

/** 内存版 storage,替掉 jsdom 那份,免得用例之间共用一个全局。 */
function memStorage(initial?: Record<string, string>): FakeStorage {
  const map = new Map<string, string>(Object.entries(initial ?? {}))
  return {
    getItem: (k: string) => map.get(k) ?? null,
    setItem: (k: string, v: string) => { map.set(k, v) },
    removeItem: (k: string) => { map.delete(k) },
    peek: (k: string) => map.get(k) ?? null,
  }
}

describe('isChunkLoadFailure', () => {
  it('认出各浏览器的动态 import 失败', () => {
    expect(isChunkLoadFailure(new Error('Failed to fetch dynamically imported module: http://x/assets/Login-abc.js'))).toBe(true)
    expect(isChunkLoadFailure(new Error('error loading dynamically imported module'))).toBe(true)
    expect(isChunkLoadFailure(new Error('Importing a module script failed.'))).toBe(true)
    expect(isChunkLoadFailure(new Error('Unable to preload CSS for /assets/Dashboard-abc.css'))).toBe(true)
  })

  it('别的错误不算分块失效', () => {
    expect(isChunkLoadFailure(new Error('Network request failed'))).toBe(false)
    expect(isChunkLoadFailure(new TypeError('x is not a function'))).toBe(false)
    expect(isChunkLoadFailure(undefined)).toBe(false)
    expect(isChunkLoadFailure('boom')).toBe(false)
    expect(isChunkLoadFailure({ message: 42 })).toBe(false)
  })
})

describe('planChunkReload', () => {
  it('分块失效时放行硬跳转,并把目标记进 storage', () => {
    const storage = memStorage()
    expect(planChunkReload(new Error('Failed to fetch dynamically imported module'), '/login', storage)).toBe('/login')
    expect(storage.peek(CHUNK_RELOAD_FLAG)).toBe('/login')
  })

  it('同一个目标只跳一次,第二次让路并清掉标记', () => {
    const storage = memStorage({ [CHUNK_RELOAD_FLAG]: '/login' })
    expect(planChunkReload(new Error('Failed to fetch dynamically imported module'), '/login', storage)).toBeNull()
    expect(storage.peek(CHUNK_RELOAD_FLAG)).toBeNull()
  })

  it('换了一个目标仍可跳(服务端刚更新时不会卡死)', () => {
    const storage = memStorage({ [CHUNK_RELOAD_FLAG]: '/login' })
    expect(planChunkReload(new Error('Failed to fetch dynamically imported module'), '/runs/12', storage)).toBe('/runs/12')
  })

  it('不是分块的错就不刷新,也不动标记', () => {
    const storage = memStorage()
    expect(planChunkReload(new Error('unauthenticated'), '/dashboard', storage)).toBeNull()
    expect(storage.peek(CHUNK_RELOAD_FLAG)).toBeNull()
  })

  it('非站内路径不跳(不给 location 喂外链)', () => {
    const storage = memStorage()
    expect(planChunkReload(new Error('Failed to fetch dynamically imported module'), 'http://evil.test/x', storage)).toBeNull()
  })
})
