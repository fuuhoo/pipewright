/**
 * v6.2 用户管理(admin/users)+ 审计日志(admin/audit)文案。
 * 命名空间:adminUsers。
 */
export default {
  // ─── 用户管理 ───
  usersTitle: '用户管理',
  usersDesc: '平台用户分两类:管理员(可管理构建环境 / 配置资源 / 全局凭据)与普通用户(只能从预置目录选择构建环境,管理自己的个人凭据)。',
  usersList: '用户',
  usersCount: '{n} 个',
  usersEmpty: '还没有普通用户。邀请注册功能将在后续版本提供。',
  colUsername: '用户名',
  colRole: '角色',
  colEnabled: '状态',
  colLastLogin: '最近登录',
  colCreated: '创建时间',
  colActions: '操作',
  roleAdmin: '管理员',
  roleUser: '普通用户',
  enabled: '已启用',
  disabled: '已禁用',
  never: '从未登录',
  errUsersLoad: '加载用户列表失败',
  errUsersLoadConn: '无法连接服务器',
  bootstrapAdmin: '内置管理员',
  bootstrapAdminHint: '由 PIPEWRIGHT_ADMIN_PASSWORD 引导创建,与 admin_user 表双向同步。',

  // ─── 审计日志 ───
  auditTitle: '审计日志',
  auditDesc: '敏感操作(凭据、项目、构建环境、配置资源、用户)的追加式记录。本地表由存储层硬拦 UPDATE / DELETE。',
  auditList: '审计记录',
  auditCount: '{n} 条',
  auditEmpty: '还没有审计记录。',
  colTime: '时间',
  colActor: '操作者',
  colAction: '动作',
  colTarget: '目标',
  colIp: '来源 IP',
  colDetail: '详情',
  filterAction: '按动作过滤',
  filterAll: '全部动作',
  loadMore: '加载更多',
  errAuditLoad: '加载审计日志失败',
  errAuditLoadConn: '无法连接服务器',
  noMore: '已到底部',
}
