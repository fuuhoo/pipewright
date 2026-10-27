/**
 * 懒加载视图的分块失效判定。
 *
 * 前端产物按内容哈希命名(/assets/index-<hash>.js 与各家视图分块),服务端一更新,
 * 那些更新前就打开着的标签页引用的旧分块路径就不存在了 —— 静态资源命中不了缓存就是 404。
 * 此时 vue-router 加载路由组件失败只会**静默中止**这次导航:人点了「退出登录」,
 * 会话其实已经吊销,页面却原地不动,看起来就像按钮坏了。
 *
 * 修法是一次硬跳转重新取 index.html(那一份不走 immutable 缓存),拿到的就是新入口。
 * 只认分块加载失败这一类错误,别的错误(网络抖动、守卫里抛的异常)不该触发整页刷新。
 */

/** 各浏览器对「动态 import 失败」的措辞不一样,只能按特征串认。 */
const CHUNK_FAILURE_PATTERNS = [
  'Failed to fetch dynamically imported module', // Chromium
  'error loading dynamically imported module', // Firefox
  'Importing a module script failed', // Safari
  'Unable to preload CSS', // 视图的 css 分块单独预载失败
  'ChunkLoadError', // 打包器兜底名
]

export function isChunkLoadFailure(error: unknown): boolean {
  const message = errorMessageOf(error)
  if (message === '') return false
  return CHUNK_FAILURE_PATTERNS.some((p) => message.toLowerCase().includes(p.toLowerCase()))
}

function errorMessageOf(error: unknown): string {
  if (error instanceof Error) return error.message
  if (typeof error === 'string') return error
  if (error && typeof error === 'object' && 'message' in error) {
    const m = (error as { message: unknown }).message
    if (typeof m === 'string') return m
  }
  return ''
}

/** 同一次会话里,一个目标路径只许因分块失效硬跳一次,免得服务端真坏了就无限刷。 */
export const CHUNK_RELOAD_FLAG = 'pipewright.chunkReloadTarget'

export interface ChunkReloadStorage {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
  removeItem(key: string): void
}

/**
 * 该不该为这次错误硬跳转,该的话跳哪儿。
 *
 * 返回目标路径表示放行(并记下这个路径);返回 null 表示不刷新 ——
 * 要么不是分块的错,要么这个路径刚刚已经为它刷过一次了。
 */
export function planChunkReload(
  error: unknown,
  target: string,
  storage: ChunkReloadStorage,
): string | null {
  if (!isChunkLoadFailure(error)) return null
  if (!target.startsWith('/')) return null

  const lastTried = storage.getItem(CHUNK_RELOAD_FLAG)
  if (lastTried === target) {
    // 刷过一次还是加载不上:别再刷了,把标记让给下一个目标。
    storage.removeItem(CHUNK_RELOAD_FLAG)
    return null
  }
  storage.setItem(CHUNK_RELOAD_FLAG, target)
  return target
}
