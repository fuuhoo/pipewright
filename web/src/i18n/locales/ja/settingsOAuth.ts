export default {
  title: 'OAuth アプリ',
  sectionDesc:
    '各 git プラットフォームに OAuth アプリ(Client ID / Secret)を登録すると、ユーザーは認証情報ボールトからワンクリックでアカウントを「接続」し、PAT を手動で貼り付けることなくトークンを自動取得できます。Secret は保存時にマスクされ、書き込み後は読み出せません。同じプラットフォームはもう一つの用途にも登録できます。訪問者がプラットフォームのアカウントで本インスタンスにログインさせることです。',
  tabsAria: 'OAuth プロバイダー',
  retry: '再試行',

  providerCustomLabel: 'セルフホスト',
  providerGiteeDesc: 'Gitee',
  providerCustomDesc: 'セルフホストの GitLab / Gitea など',

  statusEnabled: '有効',
  statusConfiguredIdle: '設定済み・無効',
  statusUnconfigured: '未設定',

  clientIdPlaceholder: 'git プラットフォームの OAuth アプリページで取得',
  secretOptional: '(書き込み後はマスクのみ表示)',
  secretPlaceholderKeep: '空欄のままにすると既存の Secret を保持します',
  secretPlaceholderNew: 'Client Secret を貼り付け…',
  secretStored: '保存済み:{masked}',

  toggleAria: '{provider} OAuth を有効化',
  toggleTitle: '接続を有効化',
  toggleDesc: '有効化すると、このプラットフォームの「接続」ボタンが認証情報ボールトに表示されます',

  lastSaved: '前回の保存 {time}',
  saveBtn: '保存',

  errNoServer: 'サーバーに接続できません。バックエンドが稼働しているか確認してから再試行してください',
  errVaultUnconfigured: 'ボールトに master key が設定されていません。PIPEWRIGHT_MASTER_KEY 環境変数を設定してください',
  errLoadStatus: '読み込みに失敗しました({status})',
  errLoadGeneric: 'OAuth アプリの読み込みに失敗しました。しばらくしてから再試行してください',

  errClientIdRequiredEnabled: '有効化時は Client ID が必須です',
  errSecretRequiredEnabled: '有効化時は Client Secret が必要です',
  errBaseUrlRequiredCustom: 'セルフホストインスタンスには Base URL が必要です',
  errClientIdRequired: 'Client ID を入力してください',
  errBaseUrlRequired: 'Base URL を入力してください',

  toastSaveFailed: '保存に失敗しました',
  toastSaved: 'OAuth アプリを保存しました',
  errNoServerShort: 'サーバーに接続できません',
  unknownError: '不明なエラー',

  justNow: 'たった今',
  minutesAgo: '{n} 分前',
  hoursAgo: '{n} 時間前',
  daysAgo: '{n} 日前',

  // ─── 用途の切り替え:1 つのプラットフォームに 2 組の設定(リポジトリ認証情報 / プラットフォームログイン)───
  // 設定も callback URL も別々:OAuth アプリは通常 1 つの callback しか登録できず、
  // 両用途で共有すると互いに潰し合います。
  purposeSwitchAria: 'OAuth の用途',
  purposeVault: 'リポジトリの認証情報',
  purposeVaultHint: 'ログイン中のユーザーが認可すると、git トークンがボールトに保存されます',
  purposeLogin: 'プラットフォームログイン',
  purposeLoginHint: '未ログインの訪問者をプラットフォームのアカウントで本インスタンスにログインさせます。自動作成アカウントは最小権限のみ',
  purposeVaultNote:
    'この設定は「git アカウントを接続」ボタンだけを制御します。プラットフォーム側に登録するのは下記の Callback URL です。変更したらアプリ側も更新してください。',
  purposeLoginNote:
    '有効にするとこのプロバイダーがログイン画面に出ます。用途ごとに OAuth アプリを分けてください。1 アプリに登録できる callback URL は 1 本です。',
  callbackHint: 'git プラットフォームの OAuth アプリで、この URL を「callback / Redirect URI」に入力してください',
  callbackCopy: 'クリックで全選択。コピーしてプラットフォーム側に登録します',
  domainsLabel: 'メールドメイン許可リスト',
  domainsOptional: '(空欄 = ドメイン制限なし)',
  domainsPlaceholder: 'corp.example.com, hr.example.com',
  domainsHint:
    'メールがこれらのドメインに属するアカウントのみログインできます。許可リストが空欄ならあらゆるメールで自動アカウントが作られます(権限は最小のまま)。',
  errDomainsInvalid:
    'メールドメイン許可リストに不正な項目があります。ドメイン単体(例 corp.example.com)を、スキーム・パス・空白なしで入力してください',
  toggleTitleLogin: 'ログインを有効化',
  toggleDescLogin: '有効にすると、ログイン画面に「このプラットフォームでログイン」ボタンが表示されます',
}
