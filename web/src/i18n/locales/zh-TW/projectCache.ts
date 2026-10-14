/**
 * 「本機快取」卡(流水線編輯器 → 變數與快取頁)。
 * 文案要點:清除是安全動作,代價只有「下一次建置慢一次」,別寫成嚇人的破壞性操作。
 */
export default {
  title: '本機快取',
  sub: '這個專案留在建置機磁碟上的東西,清掉後下一次建置從遠端重新拿',
  kindRepo: '程式碼鏡像',
  kindDeps: '相依快取',
  reload: '重新統計',
  reloadAria: '重新統計快取容量',
  loading: '正在統計容量…',
  emptyNode: '這臺機器上還沒有屬於該專案的快取。',
  total: '合計 {size}',
  clearBtn: '清除快取',
  clearing: '清除中…',
  clearTitle: '刪掉這兩處快取,讓下一次建置從乾淨狀態重來',
  clearDisabledTitle: '目前沒有已佔用的快取,無需清除',
  confirmTitle: '清除本專案的本機快取?',
  confirmLead: '將刪掉這臺建置機上屬於該專案的:',
  confirmCost: '清除不會弄壞任何東西,只是下一次建置要重新拉全量程式碼、重新下載相依,會慢一次。',
  confirmShared: '程式碼鏡像依倉庫網址存放,共用這個倉庫的其他專案會一起被清掉。',
  confirmBtn: '確認清除',
  cleared: '已釋放 {size}',
  wasEmpty: '兩處快取本來就是空的,沒有東西可清。',
  loadFailed: '快取統計讀取失敗',
  clearFailed: '清除快取失敗',
}
