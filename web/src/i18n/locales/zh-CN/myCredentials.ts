/**
 * v6.2 我的凭据(personal)文案。命名空间:myCredentials。
 */
export default {
  title: '我的凭据',
  desc: '{user} 的个人凭据。个人凭据仅本人可见、可用;全局凭据由管理员统一管理。密文级操作请在「凭据保险库」进行。',

  manage: '管理我的凭据',
  empty: '你还没有任何凭据。',
  never: '从未使用',

  colName: '名称',
  colType: '类型',
  colScope: '作用域',
  colMasked: '掩码',
  colLastUsed: '最近使用',

  scopeGlobal: '全局',
  scopePersonal: '个人',

  vaultUnconfigured: '未配置 master key,凭据保险库不可用。请设置 PIPEWRIGHT_MASTER_KEY 后重启。',
  errLoad: '加载凭据失败',
  errConn: '无法连接服务器',
}
