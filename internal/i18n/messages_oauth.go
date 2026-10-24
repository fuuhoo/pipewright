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
	})
}
