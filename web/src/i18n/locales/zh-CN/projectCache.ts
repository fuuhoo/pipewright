/**
 * 「本机缓存」卡(流水线编辑器 → 变量与缓存页)。
 * 文案要点:清除是安全动作,代价只有「下一次构建慢一次」,别写成吓人的破坏性操作。
 */
export default {
  title: '本机缓存',
  sub: '这个项目留在构建机磁盘上的东西,清掉后下一次构建从远程重新拿',
  kindRepo: '代码镜像',
  kindDeps: '依赖缓存',
  reload: '重新统计',
  reloadAria: '重新统计缓存体积',
  loading: '正在统计体积…',
  emptyNode: '这台机器上还没有属于该项目的缓存。',
  total: '合计 {size}',
  clearBtn: '清除缓存',
  clearing: '清除中…',
  clearTitle: '删掉这两处缓存,让下一次构建从干净状态重来',
  clearDisabledTitle: '目前没有已占用的缓存,无需清除',
  confirmTitle: '清除本项目的本地缓存?',
  confirmLead: '将删掉这台构建机上属于该项目的:',
  confirmCost: '清除不会弄坏任何东西,只是下一次构建要重新拉全量代码、重新下载依赖,会慢一次。',
  confirmShared: '代码镜像按仓库地址存放,共用这个仓库的其它项目会一起被清掉。',
  confirmBtn: '确认清除',
  cleared: '已释放 {size}',
  wasEmpty: '两处缓存本来就是空的,没有东西可清。',
  loadFailed: '缓存统计读取失败',
  clearFailed: '清除缓存失败',
}
