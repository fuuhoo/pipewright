export default {
  title: 'OAuth アプリ',
  sectionDesc:
    '各 git プラットフォームに OAuth アプリ(Client ID / Secret)を登録すると、ユーザーは認証情報ボールトからワンクリックでアカウントを「接続」し、PAT を手動で貼り付けることなくトークンを自動取得できます。Secret は保存時にマスクされ、書き込み後は読み出せません。同じプラットフォームはもう一つの用途にも登録できます。訪問者がプラットフォームのアカウントで本インスタンスにログインさせることです。DingTalk、Feishu、企業微信、汎用 OIDC はこの第二の用途のみで、認証情報ボールトには現れません。',
  tabsAria: 'OAuth プロバイダー',
  retry: '再試行',

  providerCustomLabel: 'セルフホスト',
  providerGiteeDesc: 'Gitee',
  providerCustomDesc: 'セルフホストの GitLab / Gitea など',

  statusEnabled: '有効',
  statusConfiguredIdle: '設定済み・無効',
  statusUnconfigured: '未設定',

  clientIdPlaceholder: 'git プラットフォームの OAuth アプリページで取得',
  clientIdPlaceholderOidc: 'ID プロバイダーで OIDC クライアントを登録すると発行されます',
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
  callbackHintOidc: 'ID プロバイダーの OIDC クライアントで、この URL を「callback / Redirect URI」に入力してください',
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

  // ─── git 形態(自己ホスト型はエンドポイントの形 / OIDC は「同時に git プラットフォーム」と宣言)
  // 同じ欄でも意味が二つあります。自己ホスト型では「どの会社の URL を組み立てるか」の選択で、
  // 間違えると認可画面がそのプラットフォーム側に存在しません。OIDC でのこれは明示的な宣言で、
  // その身分源が同時に git プラットフォームでもあると初めて、ログインのトークンをクローンに使えます。
  gitShapeCustomLabel: 'プラットフォームの形態',
  gitShapeOidcLabel: '同時に git プラットフォーム',
  gitShapeOidcOptional: '(空 = 身元認証のみ)',
  gitShapeNone: '未宣言',
  gitShapeCustomHint:
    '自己ホスト型インスタンスの認可 / トークン / ユーザー情報 URL は選んだプラットフォームから導きます。GitLab は /oauth/authorize、Gitea は /login/oauth/authorize。選び方を誤ると存在しないパスを指します。',
  gitShapeOidcHint:
    '自己ホストの GitLab / Gitea は OIDC の身分源も兼ねられます。宣言して初めて、ログインで得たトークンをリポジトリのクローンに使えるようになり、下の「ログイン時に git 認証情報を同期」も有効にできます。迷う場合は空にして身元認証だけ使ってください。',
  errGitShapeInvalid:
    'プラットフォームの形態は空欄、gitlab または gitea のいずれかである必要があります。それ以外を指定すると、存在しないパスを指す認可 URL が作られます',

  // ─── ログイン時に認証情報を同期(git 5 プラットフォーム + 形態を宣言した OIDC のログインカードのみ)
  // ここで「ログイン 1 回でどこまでの権限を持つか」が決まります。既定はオフ。
  // 権限の上げ方と失効後の戻しかたを、同じ文言で必ず伝えます。
  syncTitle: 'ログイン時に git 認証情報を同期',
  syncDesc: 'このプラットフォームでログイン後、今回の認可で得たアクセストークンを本アカウントの個人 git 認証情報として自動的に保存します',
  syncAria: '{provider} のログイン時同期を有効にする',
  syncNote:
    '有効にすると、ログインが要求する権限が「身元のみ」から「リポジトリの読み取り」に上がります。期限が来る前にプラットフォームがトークンを自動更新し、更新を拒否されると、その認証情報は「認可失効」と記されます。もう一度ログインすれば回復します。同期された認証情報は本アカウントにのみ表示され、プロジェクトがどの認証情報を使うかは引き続きあなたが選びます。',
  syncNeedsShapeHint:
    'この OIDC 身分源はまだ背後の git プラットフォームを宣言していないので、スイッチは押さえられています。上の「同時に git プラットフォーム」で選んでから同期できます。',
  errSyncUnsupported:
    'この身分源はリポジトリをクローンできるトークンを発行できません。ログイン時の同期は git プラットフォーム(Gitee / GitHub / GitLab / Gitea / 自己ホスト)と、git 形態を宣言した OIDC だけが対象です',
  errSyncNeedsGitPlatform:
    'この OIDC 身分源はまだ背後の git プラットフォーム(GitLab / Gitea)を宣言していないため、「ログイン時に git 認証情報を同期」を有効にできません。まず「同時に git プラットフォーム」でいずれかを選ぶか、このスイッチをオフにしてください',

  // ─── OIDC 身分源(「プラットフォームログイン」にのみ表示)───────────────
  // 保存時にサーバー側がエンドポイントを実際に解決します。設定を誤ると
  // 「認可画面は通ったのに戻るとエラー」となるため、保存時点で失敗させます。
  providerOidcDesc: '汎用 OIDC / SSO(Keycloak、Authentik など)',
  oidcDiscoveryLabel: 'Discovery URL',
  oidcDiscoveryOptional: '(下の3エンドポイントとどちらか)',
  oidcDiscoveryPlaceholder: 'https://sso.example.com/realms/my/.well-known/openid-configuration',
  oidcDiscoveryHint:
    '入力すると認可 / トークン / ユーザー情報の各エンドポイントをドキュメントから取得します。保存時に実際に取得し、取得できなければその場でエラーになります。',
  oidcOverrideOptional: '(空ならドキュメントの値)',
  oidcAuthorizeLabel: 'Authorization Endpoint',
  oidcTokenLabel: 'Token Endpoint',
  oidcUserinfoLabel: 'Userinfo Endpoint',
  oidcEndpointPlaceholder: 'https://sso.example.com/…',
  oidcEndpointsHint:
    'ドキュメントに届かない場合(隔離ネットワーク、ゲートウェイに遮られる)は3つとも手入力してください。手入力した値が優先されます。',
  oidcIdClaimLabel: '主キーのクレーム',
  oidcIdClaimOptional: '(空 = sub)',
  oidcIdClaimHint:
    'アカウントの唯一の識別に使うクレーム。既定は sub(仕様で不変が求められます)。メールやログイン名にすると、後の名前変更やメール変更で他人のアカウントに衝突します。',
  oidcScopesLabel: 'Scopes',
  oidcScopesOptional: '(空 = openid profile email)',
  oidcScopesHint: 'スペース区切り。openid は必須です。メールを取得できない場合、メールドメイン許可リストがログインを拒否します。',
  oidcUserinfoNote:
    '身元は userinfo エンドポイントから取得します。このバージョンは id_token の署名と audience を検証しないため、社内または HTTPS のプロバイダのみ接続してください。',
  errOidcEndpointsIncomplete:
    'OIDC を有効にするには Discovery URL を入力するか、認可 / トークン / ユーザー情報の3つすべてを入力してください',
  errOidcConfigInvalid:
    'エンドポイントを解決できません: URL の誤り、discovery の取得失敗、または3つの入力が揃っていません(保存済みの設定は変更されていません)',

  // ─── DingTalk / Feishu / 企業微信(「プラットフォームログイン」にのみ出現)───
  // 公式エンドポイントはサーバー側のプリセットに組み込み済み。設定ページは上書き
  // 項目だけ出し、社内プロキシや独自デプロイに使う。
  providerDingTalkDesc: 'DingTalk の QR コードログイン',
  providerFeishuDesc: 'Feishu の QR コードログイン',
  providerWecomDesc: '企業微信の QR コードログイン(自作アプリ)',
  clientIdPlaceholderPlatform:
    'オープンプラットフォームのアプリ詳細にある Client ID(企業微信は企業 ID / corpid)',
  callbackHintPlatform:
    'オープンプラットフォームのアプリでこの URL を「callback / リダイレクト URL」に入力してください。企業微信では本インスタンスのドメインを「信頼済みドメイン」にも登録する必要があります。',
  oidcScopesPlaceholder: 'openid profile email',
  platformOverrideOptional: '(空 = プラットフォーム既定の URL)',
  platformEndpointPlaceholder: 'https://…(社内プロキシや独自デプロイのみ)',
  platformEndpointsHint:
    '認可 / 交換 / 身分の 3 つの URL は公式値を組み込み済みなので、通常は空のままにします。社内プロキシや独自デプロイ経由のときだけ上書きし、入力した値が優先されます。',
  agentIdPlaceholder: '企業微信のアプリ詳細にある AgentId(例: 1000002)',
  agentIdHint:
    'QR コードの認可 URL に必要です(パラメータ名は agentid)。Client ID 欄に入れるのは AgentId ではなく企業 ID(corpid)です。',
  platformScopesOptional: '(空 = 既定値)',
  platformScopesPlaceholder: 'スペース区切り(例: contact:user.base:readonly)',
  platformScopesHint:
    'DingTalk は既定で openid、Feishu は既定で scope を要求しません(アプリが有効化していない権限を要求すると即座に拒否されます)。メールドメイン許可リストで締めたい場合は、プラットフォームにメールアドレスを返させる必要があります。',
  platformIdentityNote:
    'アカウント照合はプラットフォームが発行する安定 ID のみ(DingTalk / Feishu は unionId、取れなければ openId。企業微信は企業 ID + メンバー userid)。ニックネームやメールでは照合しないので、名前の変更が他人のアカウントに繋がることはありません。',
  errAgentIdRequired: '企業微信は AgentId が必要です(アプリ詳細に記載)',
  errPlatformConfigInvalid:
    'エンドポイントの上書きは認証情報を含めない http(s) URL にしてください。空欄ならプラットフォーム既定の URL(保存済みの設定は変更されていません)',
}
