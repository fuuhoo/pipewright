export default {
  title: 'OAuth 应用',
  sectionDesc:
    '为每个 git 平台登记 OAuth 应用(Client ID / Secret),用户即可在凭据保险库一键「连接」账号自动换取令牌,无需手动粘贴 PAT。Secret 落库即掩码,写入后不可读出。同一平台还能登记第二种用途:让访客直接用平台账号登录本实例。钉钉、飞书、企业微信和通用 OIDC 只有「平台登录」这一档,不进凭据保险库。',
  tabsAria: 'OAuth 平台切换',
  retry: '重试',

  providerCustomLabel: '自建',
  providerGiteeDesc: '码云',
  providerCustomDesc: '自托管 GitLab / Gitea 等',

  statusEnabled: '已启用',
  statusConfiguredIdle: '已配置·未启用',
  statusUnconfigured: '未配置',

  clientIdPlaceholder: '在 git 平台 OAuth 应用页获取',
  clientIdPlaceholderOidc: '在身份提供方注册 OIDC 客户端后拿到',
  clientIdPlaceholderPlatform: '开放平台应用详情里的 Client ID(企业微信填企业 ID / corpid)',
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
  callbackHintOidc: '在身份提供方的 OIDC 客户端里,把这个地址填进「回调 / Redirect URI」',
  callbackHintPlatform:
    '在开放平台应用详情里把这个地址填进「回调 / 重定向 URL」;企业微信还要把本实例域名配进「可信域名」。',
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

  // ─── git 形态(自建实例选端点形态 / OIDC 声明「同为 git 平台」)────────────────
  // 同一个字段在两档说的是两件事:自建那档决定「授权地址按哪家拼」,选错就在平台侧撞 404;
  // OIDC 那档是一项显式声明——这个身份源背后同时是一家 git 平台,决定登录令牌能不能拿去克隆仓库。
  gitShapeCustomLabel: '平台形态',
  gitShapeOidcLabel: '同为 git 平台',
  gitShapeOidcOptional: '(留空 = 只当身份源)',
  gitShapeNone: '未声明',
  gitShapeCustomHint:
    '自建实例的授权 / 令牌 / 用户信息地址按所选平台推导:GitLab 走 /oauth/authorize,Gitea 走 /login/oauth/authorize。选错了授权页会指向一个不存在的路径。',
  gitShapeOidcHint:
    '自建 GitLab / Gitea 也能同时当 OIDC 身份源。声明了它,登录换来的令牌才可能拿去克隆仓库,下面的「登录即同步凭据」才允许打开;不确定就留空,只当身份源用。',
  errGitShapeInvalid:
    '平台形态只能留空、填 gitlab 或 gitea:填了别家,拼出来的授权地址指向一个不存在的路径',

  // ─── 登录即同步凭据(只在 git 五档 + 已声明 git 形态的 OIDC 的登录卡出现)──────
  // 这一项决定的是「一次登录顺手换走多大权限」,不是又一个填错的框:默认关,
  // 打开后授权页会多申请仓库读权限,所以文案要同时交代升到哪一级、失效了怎么办。
  syncTitle: '登录即同步凭据',
  syncDesc: '用该平台登录后,把这次授权换来的访问令牌自动存成本账号的一条个人 git 凭据',
  syncAria: '为 {provider} 开启登录即同步凭据',
  syncNote:
    '打开后,登录申请的权限会从「只认身份」升到「能读仓库」。令牌到期前平台会自动续期;续期被拒时那条凭据会被标注成「授权已失效」,重新用该平台登录一次即可恢复。同步出来的凭据只对本账号可见,项目该用哪条凭据仍由你自己选。',
  syncNeedsShapeHint:
    '这个 OIDC 身份源还没声明它背后是哪家公司 git 平台,所以开关先按住:在上面「同为 git 平台」里选一家才谈得上同步凭据。',
  errSyncUnsupported:
    '这个身份源给不出能克隆仓库的令牌:「登录即同步凭据」只对 git 平台(Gitee / GitHub / GitLab / Gitea / 自建)或已声明 git 形态的 OIDC 开放',
  errSyncNeedsGitPlatform:
    '这个 OIDC 身份源还没声明它背后是哪家公司 git 平台(GitLab / Gitea),所以不能开「登录即同步凭据」:先在「同为 git 平台」里选一家,或关掉这个开关',

  // ─── OIDC 身份源(只在「平台登录」档出现)────────────────────────────────
  // 端点由服务端在保存时真解析一次:配错的代价是「授权页一切正常,回来就报错」,
  // 所以宁可保存在即失败,也不让管理员带着坏配置去登录页试。
  providerOidcDesc: '通用 OIDC / SSO(Keycloak、Authentik 等)',
  oidcDiscoveryLabel: 'Discovery 地址',
  oidcDiscoveryOptional: '(与下面三个端点二选一)',
  oidcDiscoveryPlaceholder: 'https://sso.example.com/realms/my/.well-known/openid-configuration',
  oidcDiscoveryHint:
    '填了它,授权 / 令牌 / 用户信息三个端点就从文档里取;保存时会真的拉一次,拉不到当场报错。',
  oidcOverrideOptional: '(留空则取文档里的)',
  oidcAuthorizeLabel: 'Authorization Endpoint',
  oidcTokenLabel: 'Token Endpoint',
  oidcUserinfoLabel: 'Userinfo Endpoint',
  oidcEndpointPlaceholder: 'https://sso.example.com/…',
  oidcEndpointsHint:
    '文档拉不到(内网隔离、网关挡住)时把这三个都手填;填了的手填值优先于文档。',
  oidcIdClaimLabel: '主键声明',
  oidcIdClaimOptional: '(留空 = sub)',
  oidcIdClaimHint:
    '用哪个声明当账号唯一标识。默认 sub:规范要求它不可变。换成邮箱或登录名,以后对方改名或换邮箱就会撞上别人的账号。',
  oidcScopesLabel: 'Scopes',
  oidcScopesOptional: '(留空 = openid profile email)',
  oidcScopesPlaceholder: 'openid profile email',
  oidcScopesHint: '空格分隔。至少要 openid;拿不到邮箱时,邮箱域名白名单会直接拦下登录。',
  oidcUserinfoNote:
    '身份取自 userinfo 接口:这一版不校验 id_token 的签名与 audience,建议只接内网或 HTTPS 的身份提供方。',
  errOidcEndpointsIncomplete:
    '启用 OIDC 要么填 Discovery 地址,要么把授权 / 令牌 / 用户信息三个端点都填上',
  errOidcConfigInvalid: '端点解析失败:地址写错、discovery 拉不到,或三个端点没凑齐(已存的配置没动)',

  // ─── 钉钉 / 飞书 / 企业微信(只在「平台登录」档出现)───────────────────────
  // 官方端点写死在后端预设里,配置页只给覆盖项:内网代理、私有部署才需要填。
  providerDingTalkDesc: '钉钉扫码登录',
  providerFeishuDesc: '飞书扫码登录',
  providerWecomDesc: '企业微信扫码登录(自建应用)',
  platformOverrideOptional: '(留空即用平台默认地址)',
  platformEndpointPlaceholder: 'https://…(仅内网代理或私有部署需要填)',
  platformEndpointsHint:
    '授权 / 兑换 / 身份三个地址已内置官方值,一般不用动;要走内网代理或私有部署才覆盖,填了的手填值优先。',
  agentIdPlaceholder: '企业微信应用详情里的 AgentId,如 1000002',
  agentIdHint:
    '扫码地址要带它才换得出成员身份(参数名 agentid);Client ID 那栏填的是企业 ID(corpID),别写混。',
  platformScopesOptional: '(留空即用默认)',
  platformScopesPlaceholder: '空格分隔,如 contact:user.base:readonly',
  platformScopesHint:
    '钉钉默认 openid;飞书默认不申请任何 scope——申请应用没开通的权限会被直接打回。要按邮箱域名白名单拦人,就得让平台回邮箱。',
  platformIdentityNote:
    '只认平台发的稳定主键认人(钉钉 / 飞书取 unionId,拿不到才退 openId;企业微信取企业 ID + 成员 userid),昵称和邮箱都不参与,所以对方改名不会串到别人的账号。',
  errAgentIdRequired: '企业微信需要填 AgentId(在应用详情里)',
  errPlatformConfigInvalid:
    '端点覆盖必须是 http(s) 地址,且不能带账号密码;留空即用平台默认地址(已存的配置没动)',
}
