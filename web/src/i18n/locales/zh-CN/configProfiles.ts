/**
 * v6.2 配置资源管理(admin/config-profiles)文案。
 * 命名空间:configProfiles。
 */
export default {
  title: '配置资源',
  desc: '管理各语言的构建配置文件(Maven settings / npmrc / pip / go env)。文件落到宿主机固定目录,构建时按配置拷贝进容器;数据库另存一份冗余快照。',

  list: '配置资源',
  count: '{n} 个',
  empty: '还没有任何配置资源。',
  add: '新建配置资源',
  upload: '上传文件',

  colProfile: '配置',
  colLanguage: '语言',
  colType: '类型',
  colTarget: '容器内路径',
  colBuiltin: '内置',
  colEnabled: '启用',
  colActions: '操作',

  builtin: '内置',
  custom: '自定义',
  default: '默认',

  fieldLanguage: '语言',
  fieldConfigType: '配置类型',
  fieldName: '名称',
  fieldTargetPath: '容器内目标路径',
  fieldContent: '文件内容',
  fieldDescription: '说明',
  fieldIsDefault: '设为该语言默认',
  fieldEnabled: '启用',
  fieldFile: '文件',
  create: '新建配置资源',
  edit: '编辑配置资源',

  targetPathPlaceholder: '如 /root/.m2/settings.xml',
  targetPathHint: '构建时会被拷贝到容器内的这个路径。',
  contentHint: '可直接编辑;保存时原子写盘(tmp + fsync + rename),任一步失败整笔回滚。',
  builtinHint: '内置配置仅可修改说明与启用状态,不可改内容或路径。',

  save: '保存',
  saving: '保存中…',
  cancel: '取消',
  editAction: '编辑',
  delete: '删除',
  confirmDelete: '删除配置资源',

  // 上传
  uploadTitle: '上传配置文件',
  uploadHint: '支持 {exts};单文件上限 {max}。',
  uploadPick: '选择文件',
  uploading: '上传中…',
  uploadOk: '已上传',

  // 错误
  errLoad: '加载配置资源失败',
  errLoadConn: '无法连接服务器',
  errSave: '保存失败',
  errDelete: '删除失败',
  errUpload: '上传失败',
  errExt: '不支持的扩展名(允许:{exts})',
  errBuiltinReadonly: '内置配置仅允许修改说明与启用状态',
  errConflict: '同「语言 + 类型 + 名称」已存在',
  errNotFound: '配置资源不存在',
  savedOk: '已保存',
  deletedOk: '已删除',
}
