/**
 * v6.2 全局凭据管理(admin)文案。命名空间:adminCredentials。
 */
export default {
  title: '全局凭据',
  desc: '查看全部凭据元数据,并可禁用违规的个人凭据。密文级操作(创建 / 轮换 / 删除 / 查看明文)请在「凭据保险库」进行。',

  openVault: '打开凭据保险库',
  empty: '还没有任何凭据。',
  never: '从未使用',

  colName: '名称',
  colType: '类型',
  colScope: '作用域',
  colMasked: '掩码',
  colLastUsed: '最近使用',
  colCreated: '创建时间',
  colActions: '操作',

  scopeGlobal: '全局',
  scopePersonal: '个人',
  globalNotDisableable: '全局凭据不可禁用',

  disable: '禁用',
  disableOk: '已禁用「{name}」',

  vaultUnconfigured: '未配置 master key,凭据保险库不可用。请设置 PIPEWRIGHT_MASTER_KEY 后重启。',
  errLoad: '加载凭据失败',
  errConn: '无法连接服务器',
  errDisable: '禁用失败',
}
