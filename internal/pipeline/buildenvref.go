// buildenvref.go 是「job 用哪个预置构建环境 / 哪些配置资源」的契约层(R4/R6)。
//
// 流水线里凡是「在隔离容器内跑」的节点,镜像一律来自预置构建环境目录,由 job 存
// `buildEnvId` 引用;配置资源(如 .npmrc / settings.xml)以 `configProfileIds`
// 逗号分隔引用。二者都是**引用**,不是镜像地址本身 —— 手填镜像的入口在前端已删除,
// 服务端校验(#8)与运行时解析(#9)都只读这里,避免各处再各写一套键名。
//
// 旧配置(只有 `image` 或 `toolchainLanguage`+`toolchainVersion`)不做迁移:此处原样
// 读出供报错文案用(#8 会拒绝保存、#9 会拒绝运行并要求重选),前端编辑器据此反查回显。
package pipeline

import "strings"

// job.Config 里与构建环境 / 配置资源相关的键名(JSON 为 camelCase,与前端一致)。
const (
	// ConfigKeyBuildEnvID 是「选哪个预置构建环境」的引用键(值为 build_envs.id)。
	ConfigKeyBuildEnvID = "buildEnvId"
	// ConfigKeyConfigProfileIDs 是「注入哪些配置资源」的引用键(逗号分隔 config_profiles.id)。
	ConfigKeyConfigProfileIDs = "configProfileIds"
	// ConfigKeyImage 是旧版手填镜像键(仍被运行时读作桥接;新配置不再写它)。
	ConfigKeyImage = "image"
	// ConfigKeyBuildModel 是 build_image 的构建模型键(dockerfile | toolchain)。
	ConfigKeyBuildModel = "buildModel"
	// ConfigKeyToolchainLanguage / ConfigKeyToolchainVersion 是旧版工具链键。
	ConfigKeyToolchainLanguage = "toolchainLanguage"
	ConfigKeyToolchainVersion  = "toolchainVersion"
)

// scriptJobTypes 是「在隔离构建容器内跑脚本」的 job 类型集合(唯一出处)。
// build_frontend / build_backend 是预填好命令的 script 模板;templated 是自定义节点
// (渲染 {{参数}} 后同样进容器),三者与 script/custom 走同一执行路径。
var scriptJobTypes = map[string]bool{
	StepTypeScript:   true,
	"custom":         true,
	"build_frontend": true,
	"build_backend":  true,
	"templated":      true,
}

// IsScriptJobType 报告 job 类型是否按脚本路径执行(容器跑命令 + 收 artifactPath 产物)。
func IsScriptJobType(jobType string) bool { return scriptJobTypes[strings.TrimSpace(jobType)] }

// NeedsBuildEnv 报告该 job 是否必须绑定一个预置构建环境。
//
// 脚本类节点全部需要;镜像构建只在「工具链构建(模型 B)」下需要 —— 模型 A 走
// docker build,镜像由 Dockerfile 的 FROM 决定,与平台构建环境无关。
// 合并后的「构建」任务先按产物档位折算成实际执行类型(见 EffectiveJobType)再判。
// 其余类型(git_source / push_image / deploy_* / notify)不进构建容器。
func NeedsBuildEnv(jobType string, cfg map[string]any) bool {
	switch EffectiveJobType(jobType, cfg) {
	case JobTypeBuildImage:
		return ConfigString(cfg, ConfigKeyBuildModel) == BuildModelToolchain
	default:
		return IsScriptJobType(EffectiveJobType(jobType, cfg))
	}
}

// BuildEnvRef 是一个 job 的构建环境选择(引用而非镜像地址)。
//
// EnvID / ProfileIDs 为新契约;LegacyImage / LegacyToolchain 只承载旧配置里的手填值,
// 供保存期校验(#8)与运行期解析(#9)给出「当前是 X,请重选」的可读报错。
type BuildEnvRef struct {
	EnvID           string
	ProfileIDs      []string
	LegacyImage     string
	LegacyToolchain Toolchain
}

// JobBuildEnvRef 从 job.Config 读出构建环境引用(纯函数,nil config 安全)。
func JobBuildEnvRef(cfg map[string]any) BuildEnvRef {
	return BuildEnvRef{
		EnvID:           ConfigString(cfg, ConfigKeyBuildEnvID),
		ProfileIDs:      ConfigStringList(cfg, ConfigKeyConfigProfileIDs),
		LegacyImage:     ConfigString(cfg, ConfigKeyImage),
		LegacyToolchain: Toolchain{Language: ConfigString(cfg, ConfigKeyToolchainLanguage), Version: ConfigString(cfg, ConfigKeyToolchainVersion)},
	}
}

// ConfigString 取自由 KV 里的字符串值(trim;缺失 / 非字符串 → "")。
// JSON 反序列化后数值也是 float64,这里只认字符串——job 配置表单写的全是字符串。
func ConfigString(cfg map[string]any, key string) string {
	if cfg == nil {
		return ""
	}
	s, _ := cfg[key].(string)
	return strings.TrimSpace(s)
}

// ConfigStringList 取逗号 / 换行分隔的引用列表:去空、去重(保留首次序)。
// 前端多选控件把 configProfileIds 存成逗号串,手改 YAML 也可能写成多行。
func ConfigStringList(cfg map[string]any, key string) []string {
	raw := ConfigString(cfg, key)
	if raw == "" {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' }) {
		v := strings.TrimSpace(part)
		if v == "" {
			continue
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// UsesTemplateParam 报告文本里是否还留着 {{参数}} 占位(自定义节点的镜像可在参数域内
// 取值,#8 只在渲染后校验最终镜像;渲染前占位本身不该被判成非法镜像)。
func UsesTemplateParam(s string) bool { return strings.Contains(s, "{{") }
