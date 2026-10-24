package i18n

// OAuth 提供方与「登录即同步凭据」相关的服务端文案。
//
// 集中在一处而不是散进 part1..part5:这一组词条解释的是「哪一家平台能配、能配成什么形态、
// 什么情况下拿不到 git 令牌」,加一档平台(gitea)或改一句拒绝理由时四处都要动。
// 词条按 zh-CN 原文精确匹配(见 i18n.go),原文改一个字这里就得跟着改,所以就近放、
// 与 httpapi/oauth*.go 里的 writeError 成对维护。

func init() {
	register(map[string]map[string]string{
		"provider 不在支持范围内:仓库凭据只认 gitee、github、gitlab、gitea、custom;登录身份源另加 oidc、dingtalk、feishu、wecom": {
			"zh-TW": "provider 不在支援範圍內:倉庫憑據只認 gitee、github、gitlab、gitea、custom;登入身分源另加 oidc、dingtalk、feishu、wecom",
			"en":    "provider is not supported: repository credentials accept only gitee, github, gitlab, gitea or custom; sign-in identity providers additionally accept oidc, dingtalk, feishu and wecom",
			"ja":    "provider がサポートされていません。リポジトリの認証情報は gitee、github、gitlab、gitea、custom のみ対応し、サインイン用のアイデンティティプロバイダはこれに加え oidc、dingtalk、feishu、wecom が利用できます",
			"ko":    "provider가 지원되지 않습니다. 저장소 자격 증명은 gitee, github, gitlab, gitea, custom만 지원하며, 로그인 신원 공급자는 여기에 oidc, dingtalk, feishu, wecom이 추가됩니다",
			"es":    "provider no admitido: las credenciales de repositorio solo aceptan gitee, github, gitlab, gitea o custom; los proveedores de identidad de inicio de sesión admiten además oidc, dingtalk, feishu y wecom",
			"fr":    "provider non pris en charge : les identifiants de dépôt n'acceptent que gitee, github, gitlab, gitea ou custom ; les fournisseurs d'identité de connexion acceptent en plus oidc, dingtalk, feishu et wecom",
			"de":    "provider wird nicht unterstützt: Repository-Anmeldedaten akzeptieren nur gitee, github, gitlab, gitea oder custom; Identitätsanbieter für die Anmeldung akzeptieren zusätzlich oidc, dingtalk, feishu und wecom",
		},
		"平台形态只能留空、填 gitlab 或 gitea:填了别家,拼出来的授权地址指向一个不存在的路径": {
			"zh-TW": "平台形態只能留空、填 gitlab 或 gitea:填了別家,拼出來的授權網址會指向一個不存在的路徑",
			"en":    "Platform shape must be left empty or set to gitlab or gitea: any other value builds an authorization URL that points to a path that does not exist",
			"ja":    "プラットフォームの形態は空欄、gitlab または gitea のいずれかである必要があります。それ以外を指定すると、存在しないパスを指す認可 URL が作られます",
			"ko":    "플랫폼 형태는 비워 두거나 gitlab 또는 gitea만 가능합니다. 그 외의 값을 넣으면 존재하지 않는 경로를 가리키는 인증 URL이 만들어집니다",
			"es":    "La forma de la plataforma debe quedar vacía o ser gitlab o gitea: cualquier otro valor genera una URL de autorización que apunta a una ruta inexistente",
			"fr":    "La forme de la plateforme doit rester vide ou valoir gitlab ou gitea : toute autre valeur produit une URL d'autorisation pointant vers un chemin inexistant",
			"de":    "Die Plattform-Form muss leer, gitlab oder gitea sein: jeder andere Wert erzeugt eine Autorisierungs-URL, die auf einen nicht existierenden Pfad zeigt",
		},
		"「登录即同步凭据」只对 git 平台(Gitee / GitHub / GitLab / Gitea / 自建)或已声明 git 形态的 OIDC 开放;这个身份源给不出可用于克隆仓库的令牌": {
			"zh-TW": "「登入即同步憑據」只對 git 平台(Gitee / GitHub / GitLab / Gitea / 自建)或已聲明 git 形態的 OIDC 開放;這個身分源給不出可用於複製倉庫的令牌",
			"en":    "\"Sync credentials on sign-in\" is available only for git platforms (Gitee / GitHub / GitLab / Gitea / self-hosted) or an OIDC provider that declares a git shape; this identity provider cannot issue a token usable for cloning repositories",
			"ja":    "「サインイン時に認証情報を同期」は git プラットフォーム(Gitee / GitHub / GitLab / Gitea / 自己ホスト型)、または git 形態を宣言した OIDC のみ利用できます。このアイデンティティプロバイダはリポジトリのクローンに使えるトークンを発行できません",
			"ko":    "\"로그인 시 자격 증명 동기화\"는 git 플랫폼(Gitee / GitHub / GitLab / Gitea / 자체 호스팅) 또는 git 형태를 선언한 OIDC에서만 켤 수 있습니다. 이 신원 공급자는 저장소 복사에 사용할 수 있는 토큰을 발급하지 못합니다",
			"es":    "«Sincronizar credenciales al iniciar sesión» solo está disponible para plataformas git (Gitee / GitHub / GitLab / Gitea / autoalojada) o un proveedor OIDC que declare una forma git; este proveedor de identidad no puede emitir un token que sirva para clonar repositorios",
			"fr":    "« Synchroniser les identifiants à la connexion » n'est réservé qu'aux plateformes git (Gitee / GitHub / GitLab / Gitea / auto-hébergée) ou à un fournisseur OIDC déclarant une forme git ; ce fournisseur d'identité ne peut pas émettre de jeton utilisable pour cloner des dépôts",
			"de":    "„Anmeldedaten bei der Anmeldung synchronisieren“ ist nur für git-Plattformen (Gitee / GitHub / GitLab / Gitea / selbst gehostet) oder einen OIDC-Anbieter mit erklärter git-Form verfügbar; dieser Identitätsanbieter kann kein Token zum Klonen von Repositories ausstellen",
		},
		"这个 OIDC 身份源还没声明它背后是哪家公司 git 平台(GitLab / Gitea),所以不能开「登录即同步凭据」:先在「同为 git 平台」里选一家,或关掉这个开关": {
			"zh-TW": "這個 OIDC 身分源還沒聲明它背後是哪家公司 git 平台(GitLab / Gitea),所以不能開「登入即同步憑據」:先在「同為 git 平台」裡選一家,或關掉這個開關",
			"en":    "This OIDC identity provider has not declared which git platform it fronts (GitLab / Gitea), so \"Sync credentials on sign-in\" cannot be enabled: pick a platform under \"Also a git platform\" first, or turn this switch off",
			"ja":    "この OIDC アイデンティティプロバイダは、その背後にある git プラットフォーム(GitLab / Gitea)が宣言されていないため、「サインイン時に認証情報を同期」を有効にできません。まず「同時に git プラットフォーム」でいずれかを選ぶか、このスイッチをオフにしてください",
			"ko":    "이 OIDC 신원 공급자는 자체 뒤에 있는 git 플랫폼(GitLab / Gitea)이 선언되지 않아 \"로그인 시 자격 증명 동기화\"를 켤 수 없습니다. 먼저 \"동시에 git 플랫폼\"에서 하나를 선택하거나 이 스위치를 끄세요",
			"es":    "Este proveedor de identidad OIDC no ha declarado qué plataforma git hay detrás (GitLab / Gitea), por lo que no se puede activar «Sincronizar credenciales al iniciar sesión»: elija primero una plataforma en «También es una plataforma git» o desactive este interruptor",
			"fr":    "Ce fournisseur d'identité OIDC n'a pas déclaré la plateforme git cachée derrière lui (GitLab / Gitea) ; « Synchroniser les identifiants à la connexion » ne peut donc pas être activé : choisissez d'abord une plateforme dans « Est aussi une plateforme git », ou désactivez ce commutateur",
			"de":    "Dieser OIDC-Identitätsanbieter hat nicht erklärt, welche git-Plattform dahintersteht (GitLab / Gitea); „Anmeldedaten bei der Anmeldung synchronisieren“ kann daher nicht aktiviert werden: Wählen Sie zuerst eine Plattform unter „Ist auch eine git-Plattform“ oder schalten Sie diesen Schalter aus",
		},
	})
}
