export default {
  title: 'OAuth 應用',
  sectionDesc:
    '為每個 git 平台登記 OAuth 應用(Client ID / Secret),使用者即可在憑證保險庫一鍵「連接」帳號自動換取權杖,無需手動貼上 PAT。Secret 入庫即遮罩,寫入後不可讀出。同一平台還能登記第二種用途:讓訪客直接用平台帳號登入本實例。',
  tabsAria: 'OAuth 平台切換',
  retry: '重試',

  providerCustomLabel: '自建',
  providerGiteeDesc: '碼雲',
  providerCustomDesc: '自架 GitLab / Gitea 等',

  statusEnabled: '已啟用',
  statusConfiguredIdle: '已設定·未啟用',
  statusUnconfigured: '未設定',

  clientIdPlaceholder: '在 git 平台 OAuth 應用頁取得',
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
}
