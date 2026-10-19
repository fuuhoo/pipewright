// envfile.go 是「构建节点导入 env 文件当环境变量」的契约层。
//
// 节点上选来源三选一:仓库里的文件(envFile,按本次 commit 的工作区读)/ 粘贴正文 / 上传本地文件。
// 上传只发生在浏览器里 —— FileReader 把正文读进 envText,落库形状与粘贴相同,envFileName 仅用于回显
// 「这份正文当初来自哪个文件」。
//
// KEY=VALUE 的解析规则与「步骤输出 → 下游变量」共用同一份 ParseEnvBody(见 internal/build/stage_env.go):
// 两处各写一套就会出现同一份文件在这里能吃、那里不能吃。
//
// 值为什么是明文:节点配置本就随流水线一起落库、随 GET 返回给项目成员,这与粘贴 compose / k8s 清单
// 正文是同一级别的暴露面。日志侧只回显键名(RunToolchain 把每个 -e 都过 maskKV)。要避开明文入库就用
// 仓库来源(正文不进节点配置),或「变量与缓存」里那些只存凭据引用的 secret 变量。
package pipeline

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// 环境变量导入在 job.Config 里的键(与前端 jobConfigSchema 的 script/build 字段逐字一致)。
const (
	// ConfigKeyEnvSource 是来源:none(不导入,空值同义)| repo | paste | upload。
	ConfigKeyEnvSource = "envSource"
	// ConfigKeyEnvFile 是 repo 来源时仓库根下的相对路径。
	ConfigKeyEnvFile = "envFile"
	// ConfigKeyEnvText 是 paste / upload 来源时存进节点的正文。
	ConfigKeyEnvText = "envText"
	// ConfigKeyEnvFileName 是上传文件的原始文件名,只用于表单回显,执行侧不读。
	ConfigKeyEnvFileName = "envFileName"
	// ConfigKeyLegacyEnv 是 YAML 导入(script.env)写进节点的老键:JSON 编码的 map[string]string。
	// 新表单不再写它,但执行侧仍要读 —— 否则导进来的流水线环境变量会静默失效。
	ConfigKeyLegacyEnv = "env"
)

// EnvSource* 是 envSource 的取值(与前端常量逐字一致)。
const (
	EnvSourceNone   = "none"
	EnvSourceRepo   = "repo"
	EnvSourcePaste  = "paste"
	EnvSourceUpload = "upload"
)

// EnvMaxBytes 是 env 正文的字节上限。比 compose(512 KiB)小得多是刻意的:.env 本就是几十行,
// 误贴一份大文件(整包构建日志之类)应该在保存期就失败,而不是让每次构建都多啃几十 MB。
// 导出是因为执行侧读仓库那份时要用同一个额度(两处不同步就会出现「保存能过、读取被截」)。
const EnvMaxBytes = 64 << 10

// ErrJobEnvInvalid 是「节点 env 导入配置不完整 / 来源非法」的哨兵错误(HTTP 映射 422)。
var ErrJobEnvInvalid = errors.New("pipeline: job env import configuration invalid")

// JobEnvImport 是一个节点的环境变量导入声明。
type JobEnvImport struct {
	Source string // envSource 原值(已归一:空 → EnvSourceNone)
	Path   string // repo:仓库内相对路径
	Text   string // paste / upload:正文
}

// JobEnvRef 从 job.Config 读出导入声明(纯函数,nil config 安全)。
func JobEnvRef(cfg map[string]any) JobEnvImport {
	src := ConfigString(cfg, ConfigKeyEnvSource)
	if src == "" {
		src = EnvSourceNone
	}
	return JobEnvImport{Source: src, Path: ConfigString(cfg, ConfigKeyEnvFile), Text: ConfigString(cfg, ConfigKeyEnvText)}
}

// BodyIsText 报告该来源的正文是否就存在节点里(paste / upload 同形)。
func (r JobEnvImport) BodyIsText() bool {
	return r.Source == EnvSourcePaste || r.Source == EnvSourceUpload
}

// RepoEnvPathOK 报告这是个能拿去工作区里读的 env 文件相对路径。
//
// 规则与 RepoYAMLPathOK 同形(不许绝对路径、反斜杠、空段、`.`/`..` 上跳,长度同上限),
// 差别只有文件名:compose/清单只可能是 .yml/.yaml,而 env 文件的常见形状是 `.env`、
// `.env.production`、`prod.env` 三种,故单独放一行判据。
func RepoEnvPathOK(path string) bool {
	p := strings.TrimSpace(path)
	if p == "" || len(p) > composeFileMaxLen {
		return false
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, `\`) {
		return false
	}
	base := p
	if i := strings.LastIndex(p, "/"); i >= 0 {
		base = p[i+1:]
	}
	if !IsEnvFileName(base) {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// IsEnvFileName 报告文件名像不像一份 env 文件(整份 `.env`、带环境后缀 `.env.prod`、或 `prod.env`)。
func IsEnvFileName(base string) bool {
	b := strings.TrimSpace(base)
	if b == "" {
		return false
	}
	return b == ".env" || strings.HasPrefix(b, ".env.") || strings.HasSuffix(b, ".env")
}

// EnvKeyOK 报告环境变量名合法:字母/下划线开头,其后仅字母数字下划线。
//
// 这条门不只是好看 —— 键名会被拼进容器环境的 `K=V` 实参,`A=1 B=2` 这种带空格/等号/换行的键
// 会把一条参数变成两条,或者把下一行的内容当成变量注入进去。
func EnvKeyOK(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		switch {
		case r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z'):
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// EnvParse 是一次正文解析的结果。Vars 按出现顺序、同名后者覆盖前者(与环境变量语义一致);
// Skipped 是被跳过的非法行数(不阻断构建,但要让人看见)。
type EnvParse struct {
	Vars    []BuildVar
	Skipped int
}

// ParseEnvBody 把 dotenv 形状的正文解析成明文 BuildVar。
//
// 认:`KEY=VALUE`、可选的 `export ` 前缀、空行、`#` 整行注释、CRLF、开头的 UTF-8 BOM、
// 值两端成对的同种引号(脱掉外层一对)。
// 不认(原样保留,避免把合法值改坏):行内 `#` 之后的内容(密码里就有 # 很常见)、
// `${VAR}` 插值、多行值、引号内的转义。
func ParseEnvBody(text string) EnvParse {
	s := strings.TrimPrefix(text, "\ufeff")
	var out []BuildVar
	at := make(map[string]int)
	var res EnvParse
	for _, raw := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "export "); ok {
			line = strings.TrimSpace(rest)
		}
		i := strings.IndexByte(line, '=')
		if i <= 0 {
			res.Skipped++
			continue
		}
		key := strings.TrimSpace(line[:i])
		if !EnvKeyOK(key) {
			res.Skipped++
			continue
		}
		val := stripOuterQuotes(strings.TrimSpace(line[i+1:]))
		if idx, dup := at[key]; dup {
			out[idx].Value = val // 后写覆盖,但不改变首次位置
			continue
		}
		at[key] = len(out)
		out = append(out, BuildVar{Key: key, Value: val})
	}
	res.Vars = out
	return res
}

// stripOuterQuotes 脱掉值外层成对的同种引号;不成对或长度不足原样返回。
func stripOuterQuotes(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}

// LegacyJobEnvVars 读出 YAML 导入写进 cfg["env"] 的节点变量(JSON map[string]string),
// 按键名升序返回 —— map 的迭代顺序是随机的,不排序就会让两次构建拿到同一份配置却顺序不同。
// 缺失 / 非字符串 / 解不出来 → nil(那是「没配」,不是错误)。
func LegacyJobEnvVars(cfg map[string]any) []BuildVar {
	if cfg == nil {
		return nil
	}
	raw, _ := cfg[ConfigKeyLegacyEnv].(string)
	raw = strings.TrimSpace(raw)
	if raw == "" || raw[0] != '{' {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		if EnvKeyOK(k) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := make([]BuildVar, 0, len(keys))
	for _, k := range keys {
		out = append(out, BuildVar{Key: k, Value: m[k]})
	}
	return out
}

// validateJobEnv 校验节点的环境变量导入:来源必须认得,且对应必填项配齐、没超限。
//
// 只放给脚本类节点(构建任务的「产物」档、自定义、前端/后端构建模板):它们的命令真的在隔离容器里跑,
// 变量才有落点。「产物档位=镜像」走 docker build(模型 A)或工具链镜像构建(模型 B),
// 本期不接这份变量,所以选了就当场拒掉,而不是等构建跑完才发现镜像里少了变量。
func validateJobEnv(stageName, jobName, jobType string, cfg map[string]any) error {
	ref := JobEnvRef(cfg)
	if ref.Source == EnvSourceNone {
		return nil
	}
	if !IsScriptJobType(EffectiveJobType(jobType, cfg)) {
		return issuef(ErrJobEnvInvalid, "阶段「%s」节点「%s」不是脚本类节点,导入的 env 没有执行位置(镜像档的构建任务本期不接)", stageName, jobName)
	}
	switch ref.Source {
	case EnvSourceRepo:
		if !RepoEnvPathOK(ref.Path) {
			return issuef(ErrJobEnvInvalid, "阶段「%s」节点「%s」的 env 仓库文件路径非法(%s):要填仓库内相对路径,文件名形如 .env / .env.production / prod.env,不能是绝对路径或含 ..", stageName, jobName, ref.Path)
		}
	case EnvSourcePaste, EnvSourceUpload:
		if strings.TrimSpace(ref.Text) == "" {
			kind := "粘贴的"
			if ref.Source == EnvSourceUpload {
				kind = "上传的"
			}
			return issuef(ErrJobEnvInvalid, "阶段「%s」节点「%s」选了%s env 正文,但正文是空的", stageName, jobName, kind)
		}
		if len(ref.Text) > EnvMaxBytes {
			return issuef(ErrJobEnvInvalid, "阶段「%s」节点「%s」的 env 正文超过 %d KiB 上限(%d 字节)", stageName, jobName, EnvMaxBytes>>10, len(ref.Text))
		}
	default:
		return issuef(ErrJobEnvInvalid, "阶段「%s」节点「%s」的 env 来源无法识别(%s),只能是仓库文件 / 粘贴 / 上传 / 不导入", stageName, jobName, ref.Source)
	}
	return nil
}
