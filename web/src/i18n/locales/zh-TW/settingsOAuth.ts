export default {
  title: 'OAuth 應用',
  sectionDesc:
    '為每個 git 平台登記 OAuth 應用(Client ID / Secret),使用者即可在憑證保險庫一鍵「連接」帳號自動換取權杖,無需手動貼上 PAT。Secret 入庫即遮罩,寫入後不可讀出。同一平台還能登記第二種用途:讓訪客直接用平台帳號登入本實例。釘釘、飛書、企業微信和通用 OIDC 只有「平台登入」這一檔,不進憑證保險庫。',
  tabsAria: 'OAuth 平台切換',
  retry: '重試',

  providerCustomLabel: '自建',
  providerGiteeDesc: '碼雲',
  providerCustomDesc: '自架 GitLab / Gitea 等',

  statusEnabled: '已啟用',
  statusConfiguredIdle: '已設定·未啟用',
  statusUnconfigured: '未設定',

  clientIdPlaceholder: '在 git 平台 OAuth 應用頁取得',
  clientIdPlaceholderOidc: '在身分提供方註冊 OIDC 用戶端後取得',
  secretOptional: '(寫入後僅顯示遮罩)',
  secretPlaceholderKeep: '留空保留已存 Secret',
  secretPlaceholderNew: '貼上 Client Secret…',
  secretStored: '已存:{masked}',

  toggleAria: '啟用 {provider} OAuth',
  toggleTitle: '啟用連接',
  toggleDesc: '啟用後此平台的「連接」按鈕出現在憑證保險庫',

  lastSaved: '上次儲存 {time}',
  saveBtn: '儲存',

  errNoServer: '無法連接到伺服器,請檢查後端是否執行後重試',
  errVaultUnconfigured: '保險庫未設定 master key,請設定 PIPEWRIGHT_MASTER_KEY 環境變數',
  errLoadStatus: '載入失敗({status})',
  errLoadGeneric: '載入 OAuth 應用失敗,請稍後重試',

  errClientIdRequiredEnabled: '啟用時 Client ID 必填',
  errSecretRequiredEnabled: '啟用時需提供 Client Secret',
  errBaseUrlRequiredCustom: '自建實例需填寫 Base URL',
  errClientIdRequired: '請填寫 Client ID',
  errBaseUrlRequired: '請填寫 Base URL',

  toastSaveFailed: '儲存失敗',
  toastSaved: 'OAuth 應用已儲存',
  errNoServerShort: '無法連接到伺服器',
  unknownError: '未知錯誤',

  justNow: '剛剛',
  minutesAgo: '{n} 分鐘前',
  hoursAgo: '{n} 小時前',
  daysAgo: '{n} 天前',

  // ─── 用途切換:同一平台的兩組設定(倉庫憑證 / 平台登入)─────────────────────
  // 兩組設定、兩個回呼網址:一個 OAuth 應用通常只准登記一個回呼,共用會互相牽制。
  purposeSwitchAria: 'OAuth 用途切換',
  purposeVault: '倉庫憑證',
  purposeVaultHint: '已登入使用者授權後換取 git 權杖,存入憑證保險庫',
  purposeLogin: '平台登入',
  purposeLoginHint: '訪客用平台帳號登入本實例,自動建立的帳號僅給最小角色',
  purposeVaultNote:
    '這組設定只決定「連接 git 帳號」按鈕。要在平台側登記的是下方的 Callback URL;改了它,平台那邊的應用也得同步改。',
  purposeLoginNote:
    '啟用後登入頁會出現這個平台的按鈕。兩種用途要各登記一個 OAuth 應用——一個應用只能填一個回呼網址。',
  callbackHint: '在 git 平台的 OAuth 應用裡,把這個網址填進「回呼 / Redirect URI」',
  callbackHintOidc: '在身分提供方的 OIDC 用戶端裡,把這個網址填進「回呼 / Redirect URI」',
  callbackCopy: '點一下即全選,複製後到平台側登記',
  domainsLabel: 'Email 網域白名單',
  domainsOptional: '(留空 = 不限網域)',
  domainsPlaceholder: 'corp.example.com, hr.example.com',
  domainsHint:
    '只有 Email 落在這些網域的帳號能登入。白名單留空時任何 Email 都會自動建號(角色仍是最小)。',
  errDomainsInvalid:
    'Email 網域白名單含非法項:只填網域本身(如 corp.example.com),不要附協議、路徑或空白',
  toggleTitleLogin: '啟用登入',
  toggleDescLogin: '啟用後登入頁出現「用該平台帳號登入」按鈕',

  // ─── git 形態(自建實例選端點形態 / OIDC 聲明「同為 git 平台」)──────────────
  // 同一個欄位在兩檔說的是兩件事:自建那檔決定「授權網址按哪家拼」,選錯就在平台側撞 404;
  // OIDC 那檔是一項顯式聲明——這個身分來源背後同時是一家 git 平台,決定登入權杖能不能拿去複製倉庫。
  gitShapeCustomLabel: '平台形態',
  gitShapeOidcLabel: '同為 git 平台',
  gitShapeOidcOptional: '(留空 = 只當身分源)',
  gitShapeNone: '未聲明',
  gitShapeCustomHint:
    '自建實例的授權 / 權杖 / 使用者資訊網址按所選平台推導:GitLab 走 /oauth/authorize,Gitea 走 /login/oauth/authorize。選錯會導向一個不存在的路徑。',
  gitShapeOidcHint:
    '自建 GitLab / Gitea 也能同時當 OIDC 身分源。聲明之後,登入換來的權杖才可能拿去複製倉庫,下面的「登入即同步憑證」才允許開啟;不確定就留空,只當身分源用。',
  errGitShapeInvalid:
    '平台形態只能留空、填 gitlab 或 gitea:填了別家,拼出來的授權網址會指向一個不存在的路徑',

  // ─── 登入即同步憑證(只在 git 五檔 + 已聲明 git 形態的 OIDC 的登入卡出現)──────
  // 這一項決定的是「一次登入順手換走多大權限」,不是又一個填錯的框:預設關閉,
  // 開啟後授權頁會多申請倉庫讀權限,所以文案要同時交代升到哪一級、失效了怎麼辦。
  syncTitle: '登入即同步憑證',
  syncDesc: '用該平台登入後,把這次授權換來的存取權杖自動存成本帳號的一條個人 git 憑證',
  syncAria: '為 {provider} 開啟登入即同步憑證',
  syncNote:
    '開啟後,登入申請的權限會從「只認身分」升到「能讀倉庫」。權杖到期前平台會自動續期;續期被拒時那條憑證會被標註成「授權已失效」,重新用該平台登入一次即可恢復。同步出來的憑證只對本帳號可見,專案要用哪條憑證仍由你自己選。',
  syncNeedsShapeHint:
    '這個 OIDC 身分源還沒聲明它背後是哪家公司 git 平台,所以開關先按住:在上面「同為 git 平台」裡選一家才談得上同步憑證。',
  errSyncUnsupported:
    '這個身分來源給不出能複製倉庫的權杖:「登入即同步憑證」只對 git 平台(Gitee / GitHub / GitLab / Gitea / 自建)或已聲明 git 形態的 OIDC 開放',
  errSyncNeedsGitPlatform:
    '這個 OIDC 身分源還沒聲明它背後是哪家公司 git 平台(GitLab / Gitea),所以不能開「登入即同步憑證」:先在「同為 git 平台」裡選一家,或關掉這個開關',

  // ─── OIDC 身分來源(只在「平台登入」檔出現)──────────────────────────────
  // 端點由服務端在儲存時真的解析一次:配錯的代價是「授權頁一切正常,回來就報錯」,
  // 所以寧可儲存在即失敗,也不要管理員帶著壞設定去登入頁試。
  providerOidcDesc: '通用 OIDC / SSO(Keycloak、Authentik 等)',
  oidcDiscoveryLabel: 'Discovery 網址',
  oidcDiscoveryOptional: '(與下面三個端點二選一)',
  oidcDiscoveryPlaceholder: 'https://sso.example.com/realms/my/.well-known/openid-configuration',
  oidcDiscoveryHint: '填了它,授權 / 權杖 / 使用者資訊三個端點就從文件裡取;儲存時會真的拉一次,拉不到當場報錯。',
  oidcOverrideOptional: '(留空則取文件裡的)',
  oidcAuthorizeLabel: 'Authorization Endpoint',
  oidcTokenLabel: 'Token Endpoint',
  oidcUserinfoLabel: 'Userinfo Endpoint',
  oidcEndpointPlaceholder: 'https://sso.example.com/…',
  oidcEndpointsHint: '文件拉不到(內網隔離、閘道擋住)時把這三個都手填;填了的手填值優先於文件。',
  oidcIdClaimLabel: '主鍵宣告',
  oidcIdClaimOptional: '(留空 = sub)',
  oidcIdClaimHint:
    '用哪個宣告當帳號唯一識別。預設 sub:規範要求它不可變。改成信箱或登入名,對方日後改名或換信箱就會撞上別人的帳號。',
  oidcScopesLabel: 'Scopes',
  oidcScopesOptional: '(留空 = openid profile email)',
  oidcScopesHint: '空格分隔。至少要 openid;拿不到信箱時,信箱域名白名單會直接擋下登入。',
  oidcUserinfoNote:
    '身分取自 userinfo 介面:這一版不驗證 id_token 的簽名與 audience,建議只接內網或 HTTPS 的身分提供者。',
  errOidcEndpointsIncomplete: '啟用 OIDC 要嘛填 Discovery 網址,要嘛把授權 / 權杖 / 使用者資訊三個端點都填上',
  errOidcConfigInvalid: '端點解析失敗:網址寫錯、discovery 拉不到,或三個端點沒湊齊(已儲存的設定沒動)',

  // ─── 釘釘 / 飛書 / 企業微信(只在「平台登入」檔出現)──────────────────────
  // 官方端點寫死在服務端預設裡,設定頁只給覆蓋項:內網代理或自建部署才需要填。
  providerDingTalkDesc: '釘釘掃描登入',
  providerFeishuDesc: '飛書掃描登入',
  providerWecomDesc: '企業微信掃描登入(自建應用)',
  clientIdPlaceholderPlatform: '開放平台應用詳情裡的 Client ID(企業微信填企業 ID / corpid)',
  callbackHintPlatform:
    '在開放平台應用裡把這個網址填進「回呼 / 導向 URL」;企業微信還要把本實例網域加進「可信網域」。',
  oidcScopesPlaceholder: 'openid profile email',
  platformOverrideOptional: '(留空即用平台預設網址)',
  platformEndpointPlaceholder: 'https://…(僅內網代理或自建部署需要填)',
  platformEndpointsHint:
    '授權 / 兌換 / 身分三個網址已內嵌官方值,一般不用動;要走內網代理或自建部署才覆蓋,填了的手填值優先。',
  agentIdPlaceholder: '企業微信應用詳情裡的 AgentId,如 1000002',
  agentIdHint:
    '掃描網址要帶它才換得出成員身分(參數名 agentid);Client ID 那欄填的是企業 ID(corpID),別寫混。',
  platformScopesOptional: '(留空即用預設)',
  platformScopesPlaceholder: '空格分隔,如 contact:user.base:readonly',
  platformScopesHint:
    '釘釘預設 openid;飛書預設不申請任何 scope——申請應用沒開通的權限會被直接打回。要按信箱網域白名單擋人,就得讓平台回信箱。',
  platformIdentityNote:
    '只認平台發的穩定主鍵認人(釘釘 / 飛書取 unionId,拿不到才退 openId;企業微信取企業 ID + 成員 userid),暱稱和信箱都不參與,所以對方改名不會串到別人的帳號。',
  errAgentIdRequired: '企業微信需要填 AgentId(在應用詳情裡)',
  errPlatformConfigInvalid:
    '端點覆蓋必須是 http(s) 網址,且不能附帳號密碼;留空即用平台預設網址(已儲存的設定沒動)',
}
