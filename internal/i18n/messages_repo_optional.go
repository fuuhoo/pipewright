package i18n

// messages_repo_optional.go:「项目可不绑仓库(纯发布用)」与「流水线源任务自愈」两条规则带来的
// 用户可见文案(源串一律 zh-CN,由 writeError 按请求 locale 命中)。
func init() {
	register(map[string]map[string]string{
		"未填仓库地址,无需选择仓库凭据": {
			"zh-TW": "未填倉庫地址,無需選擇倉庫憑證", "en": "no repository URL is set, so no repository credential is needed", "ja": "リポジトリ URL 未指定のため、リポジトリの認証情報は不要です",
			"ko": "저장소 URL이 비어 있어 저장소 자격 증명이 필요 없습니다", "es": "no hay URL de repositorio, no se necesita credencial del repositorio", "fr": "aucune URL de dépôt renseignée, identifiant de dépôt inutile", "de": "Keine Repository-URL angegeben, Anmeldedaten sind nicht erforderlich",
		},
		"该项目未绑定仓库,此项设置需要仓库": {
			"zh-TW": "該專案未綁定倉庫,此項設定需要倉庫", "en": "this project has no repository bound, but this setting requires one", "ja": "このプロジェクトにはリポジトリがバインドされておらず、この設定にはリポジトリが必要です",
			"ko": "이 프로젝트에는 저장소가 연결되어 있지 않으며 이 설정에는 저장소가 필요합니다", "es": "este proyecto no tiene repositorio vinculado y este ajuste lo requiere", "fr": "ce projet n'a pas de dépôt associé et ce paramètre en exige un", "de": "Dieses Projekt hat kein Repository gebunden, diese Einstellung erfordert eines",
		},
		"该项目未绑定仓库,此项需要仓库;请到项目设置绑定仓库": {
			"zh-TW": "該專案未綁定倉庫,此項需要倉庫;請到專案設定綁定倉庫", "en": "this project has no repository bound; this action needs one — bind a repository in the project settings", "ja": "このプロジェクトにはリポジトリがバインドされていません。この操作にはリポジトリが必要です。プロジェクト設定でバインドしてください",
			"ko": "이 프로젝트에 저장소가 연결되어 없습니다. 이 작업에는 저장소가 필요합니다. 프로젝트 설정에서 연결하세요", "es": "este proyecto no tiene repositorio vinculado; esta acción requiere uno — vincúlalo en los ajustes del proyecto", "fr": "ce projet n'a pas de dépôt associé ; cette action en exige un — associez-en un dans les paramètres du projet", "de": "Dieses Projekt hat kein Repository gebunden; diese Aktion benötigt eines — bitte in den Projekteinstellungen binden",
		},
		"流水线必须有且仅有一个「流水线源」阶段": {
			"zh-TW": "管線必須有且僅有一個「管線源」階段", "en": "a pipeline must have exactly one Pipeline Source stage", "ja": "パイプラインには「パイプラインソース」ステージがちょうど 1 つ必要です",
			"ko": "파이프라인에는 '파이프라인 소스' 스테이지가 정확히 하나 있어야 합니다", "es": "un pipeline debe tener exactamente una etapa «Fuente del pipeline»", "fr": "un pipeline doit comporter exactement une étape « Source du pipeline »", "de": "Eine Pipeline muss genau eine Stage „Pipeline-Quelle“ haben",
		},
		"有任务依赖了一个已被删除的任务,请先在依赖里去掉它": {
			"zh-TW": "有 Job 依賴了一個已被刪除的 Job,請先在依賴裡去掉它", "en": "a job depends on a job that has been deleted — remove it from the dependencies first", "ja": "削除された Job に依存している Job があります。まず依存から削除してください",
			"ko": "삭제된 Job에 의존하는 Job이 있습니다. 먼저 의존 항목에서 제거하세요", "es": "un job depende de un job que fue eliminado — quítalo primero de las dependencias", "fr": "un job dépend d'un job supprimé — retirez-le d'abord de ses dépendances", "de": "Ein Job hängt von einem gelöschten Job ab — entfernen Sie ihn zuerst aus den Abhängigkeiten",
		},
	})
}
