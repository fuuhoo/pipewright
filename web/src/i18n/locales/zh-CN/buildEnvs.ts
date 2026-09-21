/**
 * v6.2 构建环境管理(admin/build-envs)文案。
 * 命名空间:buildEnvs(文件名即命名空间,由 i18n/index.ts 自动并入)。
 */
export default {
  title: '构建环境',
  desc: '预置「语言 + 版本 + 镜像」组合。普通用户只能从预置目录选择,不可自由输入镜像地址;镜像拉取完全由系统执行,不拼接地址。',

  // 列表
  list: '预置环境',
  count: '{n} 个',
  empty: '还没有预置任何构建环境。',
  add: '新建构建环境',

  // 表头
  colEnv: '环境',
  colImage: '镜像',
  colStatus: '镜像状态',
  colEnabled: '启用',
  colSort: '排序',
  colActions: '操作',
  colCheckedAt: '检查时间',

  // 状态(P0 #4 三态)
  statusUnchecked: '未检查',
  statusChecking: '检查中',
  statusAvailable: '可用',
  statusUnavailable: '不可用',
  statusHintUnchecked: '新创建或地址刚改过,需检查后才能启用',
  statusHintUnavailable: '镜像不存在或拉取失败,不可启用',

  // 表单
  fieldLanguage: '语言',
  fieldVersion: '版本',
  fieldDisplayName: '显示名',
  fieldDescription: '说明',
  fieldSourceType: '镜像来源',
  fieldImage: '镜像地址',
  fieldCredential: '拉取凭据(可选)',
  fieldSortOrder: '排序',
  fieldEnabled: '启用',
  sourceOfficial: '官方镜像',
  sourceCustom: '自定义地址',
  imagePlaceholder: '如 node:20-alpine 或 registry.example.com:5000/team/node:20',
  imageHint: '系统不拼接地址,填什么拉什么。',
  noCredential: '不使用凭据(公开镜像)',
  edit: '编辑构建环境',
  create: '新建构建环境',

  // 操作
  check: '检查',
  checking: '检查中…',
  pull: '拉取',
  pulling: '拉取中…',
  checkAll: '检查全部',
  enable: '启用',
  disable: '禁用',
  editAction: '编辑',
  delete: '删除',
  save: '保存',
  saving: '保存中…',
  cancel: '取消',
  confirmDelete: '删除构建环境',

  // 提示 / 错误
  errLoad: '加载构建环境失败',
  errLoadConn: '无法连接服务器',
  errSave: '保存失败',
  errDelete: '删除失败',
  errCheck: '检查失败',
  errPull: '拉取失败',
  errCheckAll: '一键检查失败',
  errConflict: '该「语言 + 版本」组合已存在',
  errUnavailable: '镜像不可用,无法启用。请先检查镜像或更换地址',
  errNotChecked: '镜像未检查过,请先点击【检查】或【拉取】验证可用性后再启用',
  errNotFound: '构建环境不存在',
  checkOk: '镜像可用',
  checkFailed: '镜像不可用: {error}',
  checkAllDone: '检查完成:{ok}/{total} 可用',
  deleteOk: '已删除',
  savedOk: '已保存',
  toggleOk: '已更新启用状态',
}
