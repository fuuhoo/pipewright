export default {
  title: 'OAuth 应用',
  sectionDesc:
    '为每个 git 平台登记 OAuth 应用(Client ID / Secret),用户即可在凭据保险库一键「连接」账号自动换取令牌,无需手动粘贴 PAT。Secret 落库即掩码,写入后不可读出。同一平台还能登记第二种用途:让访客直接用平台账号登录本实例。',
  tabsAria: 'OAuth 平台切换',
  retry: '重试',

  providerCustomLabel: '自建',
  providerGiteeDesc: '码云',
  providerCustomDesc: '自托管 GitLab / Gitea 等',

  statusEnabled: '已启用',
  statusConfiguredIdle: '已配置·未启用',
  statusUnconfigured: '未配置',

  clientIdPlaceholder: '在 git 平台 OAuth 应用页获取',
  secretOptional: '(写入后仅显示掩码)',
  secretPlaceholderKeep: '留空保留已存 Secret',
  secretPlaceholderNew: '粘贴 Client Secret…',
  secretStored: '已存:{masked}',

  toggleAria: '启用 {provider} OAuth',
  toggleTitle: '启用连接',
  toggleDesc: '启用后此平台的「连接」按钮出现在凭据保险库',

  lastSaved: '上次保存 {time}',
  saveBtn: '保存',

  errNoServer: '无法连接到服务器,请检查后端是否运行后重试',
  errVaultUnconfigured: '保险库未配置 master key,请设置 PIPEWRIGHT_MASTER_KEY 环境变量',
  errLoadStatus: '加载失败({status})',
  errLoadGeneric: '加载 OAuth 应用失败,请稍后重试',

  errClientIdRequiredEnabled: '启用时 Client ID 必填',
  errSecretRequiredEnabled: '启用时需提供 Client Secret',
  errBaseUrlRequiredCustom: '自建实例需填写 Base URL',
  errClientIdRequired: '请填写 Client ID',
  errBaseUrlRequired: '请填写 Base URL',

  toastSaveFailed: '保存失败',
  toastSaved: 'OAuth 应用已保存',
  errNoServerShort: '无法连接到服务器',
  unknownError: '未知错误',

  justNow: '刚刚',
  minutesAgo: '{n} 分钟前',
  hoursAgo: '{n} 小时前',
  daysAgo: '{n} 天前',

  // ─── 用途切换:同一平台的两套配置(仓库凭据 / 平台登录)─────────────────────
  // 两套配置、两个回调地址:一个 OAuth 应用通常只允许登记一个回调,共用会互相牵制。
  purposeSwitchAria: 'OAuth 用途切换',
  purposeVault: '仓库凭据',
  purposeVaultHint: '已登录用户授权后换取 git 令牌,存入凭据保险库',
  purposeLogin: '平台登录',
  purposeLoginHint: '访客用平台账号登进本实例,自动建的账号只给最小角色',
  purposeVaultNote:
    '这套配置只决定「连接 git 账号」按钮。要在平台侧登记的是下面的 Callback URL;改了它,平台那边的应用也得同步改。',
  purposeLoginNote:
    '启用后登录页出现这个平台的按钮。两种用途要各登记一个 OAuth 应用——一个应用只能填一个回调地址。',
  callbackHint: '在 git 平台的 OAuth 应用里,把这个地址填进「回调 / Redirect URI」',
  callbackCopy: '点一下即全选,复制后去平台侧登记',
  domainsLabel: '邮箱域名白名单',
  domainsOptional: '(留空 = 不限域名)',
  domainsPlaceholder: 'corp.example.com, hr.example.com',
  domainsHint:
    '只有邮箱落在这些域名的账号能登录。白名单留空时任何邮箱都会自动建号(角色仍是最小)。',
  errDomainsInvalid:
    '邮箱域名白名单含非法项:只填域名本身(如 corp.example.com),不要带协议、路径或空格',
  toggleTitleLogin: '启用登录',
  toggleDescLogin: '启用后登录页出现「用该平台账号登录」按钮',
}
