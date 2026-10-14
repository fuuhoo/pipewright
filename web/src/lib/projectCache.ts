/**
 * 「本地缓存」卡的纯逻辑:体积口径与清除结果的对账。
 *
 * 两个容易踩的坑都在这儿兜住:
 *   - **存在 ≠ 有东西可清**。清完之后,下一次构建的 Plan 会把依赖目录重新建成空目录(见
 *     internal/projectcache/layout.go),于是这一类「存在但 0 字节」。按 exists 判定会让
 *     按钮在有缓存时禁用、在没缓存时反而可点,正好反了。
 *   - **类别可能整块缺席**。启动时关了某处缓存(PIPEWRIGHT_NO_REPO_CACHE /
 *     PIPEWRIGHT_NO_PROJECT_CACHE),服务端就不报那一类;项目没绑仓库时也没有代码镜像。
 *     所以这里全部按「拿到什么算什么」,不假设两类都在。
 */
import type { ClearedCacheResult, ProjectCacheInfo } from '../api/projects'

/** 至少有一处缓存真占着磁盘 → 才值得按清除。 */
export function hasAnythingToClear(info: ProjectCacheInfo | null): boolean {
  if (!info) return false
  return info.items.some((it) => it.bytes > 0)
}

/**
 * 清除结果对账:哪些真删掉了、哪些本来就是空的。
 *
 * toast 要说「清了什么」而不是「成功了」——用户点清除是为了拿到最新代码/最新依赖,
 * 如果本来就没缓存,那句「释放 0 B」比「本来就是空的」更容易被读成操作失败。
 */
export function summarizeClear(res: ClearedCacheResult): {
  freedBytes: number
  removedKinds: string[]
  emptyKinds: string[]
} {
  const removedKinds: string[] = []
  const emptyKinds: string[] = []
  for (const it of res.items) {
    if (it.existed && it.bytes > 0) removedKinds.push(it.kind)
    else emptyKinds.push(it.kind)
  }
  return { freedBytes: res.totalBytes, removedKinds, emptyKinds }
}
