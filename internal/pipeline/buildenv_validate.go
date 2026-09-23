// buildenv_validate.go 是预置构建环境白名单的领域校验(#8)。
//
// 规则(R4/R6):凡「在隔离容器里跑」的节点,镜像必须落在预置目录内 —— 优先认
// `buildEnvId` 引用;旧配置/内置模板/导入的 YAML 只写了 `image`(或工具链语言+版本)时,
// 要求该镜像**恰好等于**某个已启用预置环境的镜像,否则拒绝。也就是说不存在「任意镜像地址」
// 这条通路:名字必须来自目录,只是允许以镜像名的形式写(便于跨实例分发的模板引用)。
//
// 豁免:阶段旁挂服务(ServiceSpec.Image)按既有决定保持手输 —— 那是被测件依赖(测试旁挂
// DB/redis),不是构建环境;运行时(post)步骤的镜像同样保持手输,待其编辑器接入选择器。
//
// 快照由调用方即时构造(main 注入 buildenv/configprofile 服务),pipeline 包不依赖那两个包,
// 与 validate.go 的 ValidationInput 同一思路。
package pipeline

import (
	"errors"
	"fmt"
	"strings"
)

// 白名单校验错误码(前端 / API 契约按此定位问题字段)。
const (
	// ProblemBuildEnvMissing 表示节点没选构建环境(也没可用的旧镜像引用)。
	ProblemBuildEnvMissing = "build_env_missing"
	// ProblemBuildEnvUnknown 表示 buildEnvId 指向的环境不存在(多半已被管理员删除)。
	ProblemBuildEnvUnknown = "build_env_unknown"
	// ProblemBuildEnvDisabled 表示引用的构建环境已被禁用,不能再作为运行选项。
	ProblemBuildEnvDisabled = "build_env_disabled"
	// ProblemBuildEnvOffCatalog 表示旧配置手填的镜像不在预置目录内,需重选。
	ProblemBuildEnvOffCatalog = "build_env_off_catalog"
	// ProblemConfigProfileUnknown 表示引用的配置资源不存在或已禁用。
	ProblemConfigProfileUnknown = "config_profile_unknown"
	// ProblemConfigProfileLanguage 表示配置资源的语言与所选构建环境不匹配(注入无效,提示重选)。
	ProblemConfigProfileLanguage = "config_profile_language"
	// ProblemConfigProfilePath 表示配置资源不可注入(没有磁盘文件或容器内路径非法)。
	ProblemConfigProfilePath = "config_profile_path"
)

// ErrBuildEnvRequired 是「保存被构建环境白名单挡住」的哨兵错误(HTTP 映射 422)。
var ErrBuildEnvRequired = errors.New("pipeline: build environment must come from the preset catalog")

// ErrConfigProfileUnusable 是「配置资源本身不可注入(缺文件或容器路径非法)」的哨兵错误。
// 与白名单区分开:那是管理员维护的数据问题,不是用户没选环境。
var ErrConfigProfileUnusable = errors.New("pipeline: config profile cannot be injected into the container")

// BuildEnvOption 是预置目录里一个构建环境的中性视图(Enabled=false 表示已禁用,
// 仍要能查到 —— 否则「刚被禁用的环境」报成「不存在」,用户看不出根因)。
type BuildEnvOption struct {
	ID          string
	Language    string
	Version     string
	DisplayName string
	Image       string
	Enabled     bool
	// CheckStatus 是镜像可用性三态(ok / unchecked / unavailable);空按 unchecked 看待。
	CheckStatus string
	// CredentialID 是拉取该镜像所需的镜像仓库凭据引用(保险库),空=公开镜像。运行期拉取用(#9)。
	CredentialID string
}

// ConfigProfileOption 是配置资源的中性视图。
//
// 校验侧只用 ID/语言/启用位;运行期注入(#10)另需 FilePath(宿主文件)与 TargetPath(容器内路径)。
// FilePath 是宿主绝对路径,**绝不出现在任何 HTTP 响应里**(公共端点已置空)。
type ConfigProfileOption struct {
	ID         string
	Language   string
	Enabled    bool
	FilePath   string
	TargetPath string
}

// BuildEnvSnapshot 是预置目录的一次快照(保存期即时构造,不吃缓存)。
type BuildEnvSnapshot struct {
	byID      map[string]BuildEnvOption
	byImage   map[string]BuildEnvOption
	byLangVer map[string]BuildEnvOption
	profiles  map[string]ConfigProfileOption
}

// NewBuildEnvSnapshot 建快照。同镜像/同语言版本重复时保留首条(目录唯一性由管理端保证)。
func NewBuildEnvSnapshot(envs []BuildEnvOption, profiles []ConfigProfileOption) BuildEnvSnapshot {
	s := BuildEnvSnapshot{
		byID:      make(map[string]BuildEnvOption, len(envs)),
		byImage:   make(map[string]BuildEnvOption, len(envs)),
		byLangVer: make(map[string]BuildEnvOption, len(envs)),
		profiles:  make(map[string]ConfigProfileOption, len(profiles)),
	}
	for _, e := range envs {
		if e.ID == "" {
			continue
		}
		s.byID[e.ID] = e
		if e.Image != "" {
			if _, dup := s.byImage[e.Image]; !dup {
				s.byImage[e.Image] = e
			}
		}
		if e.Language != "" {
			key := e.Language + "\x00" + e.Version
			if _, dup := s.byLangVer[key]; !dup {
				s.byLangVer[key] = e
			}
		}
	}
	for _, p := range profiles {
		if p.ID != "" {
			s.profiles[p.ID] = p
		}
	}
	return s
}

// Empty 报告快照里一个构建环境都没有(全新安装尚未 seed / 目录服务缺席)。
// 此时保存期校验放行,交由运行时的镜像解析报错(#9),避免「还没建目录就无法保存任何流水线」。
func (s BuildEnvSnapshot) Empty() bool { return len(s.byID) == 0 }

// OptionByID 按目录 ID 取一条**已启用**的环境(快照含禁用项,故这里要显式过滤)。
// 用于只有 buildEnvId、没有语言/版本键的配置(如 AI 提案、手改 YAML)在执行期还原工具链。
func (s BuildEnvSnapshot) OptionByID(id string) (BuildEnvOption, bool) {
	e, ok := s.byID[strings.TrimSpace(id)]
	if !ok || !e.Enabled {
		return BuildEnvOption{}, false
	}
	return e, true
}

// BuildEnvProblem 是一条白名单问题(定位到阶段 / 节点 + 字段)。
type BuildEnvProblem struct {
	Code    string
	Stage   string
	Job     string
	Field   string
	Message string
}

func (p BuildEnvProblem) Error() string { return p.Message }

// BuildEnvValidationError 聚合一次保存里的全部问题(wraps ErrBuildEnvRequired 供 HTTP 映射)。
type BuildEnvValidationError struct{ Problems []BuildEnvProblem }

func (e *BuildEnvValidationError) Error() string {
	if len(e.Problems) == 0 {
		return "构建环境校验失败"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s", e.Problems[0].Message)
	if len(e.Problems) > 1 {
		fmt.Fprintf(&b, "(另有 %d 个节点同样未选择预置构建环境)", len(e.Problems)-1)
	}
	return b.String()
}

func (e *BuildEnvValidationError) Unwrap() error { return ErrBuildEnvRequired }

// resolveJobEnv 把 job 的构建环境引用解析成目录条目;不合法时返回定位到节点的问题。
//
// renderedImage 只有执行期会传(自定义节点的 {{参数}} 渲染完得到的镜像);保存期传空串,
// 此时若引用里仍是未渲染的占位,则放行(不报问题、也不给环境),由执行期二次校验。
func (s BuildEnvSnapshot) resolveJobEnv(ref BuildEnvRef, renderedImage, stage, job string) (BuildEnvOption, *BuildEnvProblem) {
	problem := func(code, msg string) (BuildEnvOption, *BuildEnvProblem) {
		return BuildEnvOption{}, &BuildEnvProblem{Code: code, Stage: stage, Job: job, Field: ConfigKeyBuildEnvID, Message: msg}
	}
	enabled := func(e BuildEnvOption) (BuildEnvOption, *BuildEnvProblem) {
		if !e.Enabled {
			return problem(ProblemBuildEnvDisabled,
				fmt.Sprintf("阶段「%s」的节点「%s」所选构建环境「%s」已被禁用,请重新选择", stage, job, e.DisplayName))
		}
		return e, nil
	}

	if ref.EnvID != "" {
		e, ok := s.byID[ref.EnvID]
		if !ok {
			return problem(ProblemBuildEnvUnknown,
				fmt.Sprintf("阶段「%s」的节点「%s」所选构建环境已不存在(可能已被删除),请重新选择", stage, job))
		}
		return enabled(e)
	}

	image := ref.LegacyImage
	if renderedImage != "" {
		image = renderedImage
	}
	if image != "" {
		if e, ok := s.byImage[image]; ok {
			return enabled(e)
		}
		if UsesTemplateParam(image) {
			return BuildEnvOption{}, nil // 占位未渲染:留给执行期
		}
		return problem(ProblemBuildEnvOffCatalog,
			fmt.Sprintf("阶段「%s」的节点「%s」的镜像不在预置目录内(image=%s),请重新选择构建环境", stage, job, image))
	}

	if lang := ref.LegacyToolchain.Language; lang != "" {
		ver := ref.LegacyToolchain.Version
		if ver == "" {
			ver = "latest"
		}
		if e, ok := s.byImage[lang+":"+ver]; ok {
			return enabled(e)
		}
		if e, ok := s.byLangVer[lang+"\x00"+ver]; ok {
			return enabled(e)
		}
		return problem(ProblemBuildEnvOffCatalog,
			fmt.Sprintf("阶段「%s」的节点「%s」的镜像不在预置目录内(toolchain=%s:%s),请重新选择构建环境", stage, job, lang, ver))
	}

	return problem(ProblemBuildEnvMissing,
		fmt.Sprintf("阶段「%s」的节点「%s」未选择构建环境", stage, job))
}

// ValidateBuildEnvRefs 校验 spec 里所有需要构建环境的节点(快照为空视为未启用校验)。
//
// 自定义节点(templated)的 image 允许写成 {{参数}}:保存期无法求值,放行并由运行时
// (ResolveJobRuntime)在渲染后按同一条白名单二次校验。
func ValidateBuildEnvRefs(spec Spec, snapshot BuildEnvSnapshot) []BuildEnvProblem {
	var problems []BuildEnvProblem
	for _, st := range spec.Stages {
		for _, jb := range st.Jobs {
			if !NeedsBuildEnv(jb.Type, jb.Config) {
				continue
			}
			ref := JobBuildEnvRef(jb.Config)
			env, p := snapshot.resolveJobEnv(ref, "", st.Name, jb.Name)
			if p != nil {
				problems = append(problems, *p)
				continue
			}
			problems = append(problems, snapshot.profileProblems(ref, env, st.Name, jb.Name)...)
		}
	}
	return problems
}

// profileProblems 校验配置资源引用:必须存在且启用,语言须与所选构建环境一致。
// 环境未解析出来(占位待渲染 / 零值)时跳过语言匹配 —— 那条已另行报错,不再叠加噪音。
func (s BuildEnvSnapshot) profileProblems(ref BuildEnvRef, env BuildEnvOption, stage, job string) []BuildEnvProblem {
	var out []BuildEnvProblem
	for _, id := range ref.ProfileIDs {
		p, ok := s.profiles[id]
		if !ok || !p.Enabled {
			out = append(out, BuildEnvProblem{
				Code: ProblemConfigProfileUnknown, Stage: stage, Job: job, Field: ConfigKeyConfigProfileIDs,
				Message: fmt.Sprintf("阶段「%s」的节点「%s」引用的配置资源已不存在或被禁用(%s),请重新勾选", stage, job, id),
			})
			continue
		}
		if env.ID != "" && env.Language != "" && p.Language != env.Language {
			out = append(out, BuildEnvProblem{
				Code: ProblemConfigProfileLanguage, Stage: stage, Job: job, Field: ConfigKeyConfigProfileIDs,
				Message: fmt.Sprintf("阶段「%s」的节点「%s」的配置资源「%s」语言(%s)与构建环境(%s)不匹配,注入不会生效,请重新勾选", stage, job, p.ID, p.Language, env.Language),
			})
		}
		// 资源自身不可注入(无磁盘文件 / 容器路径非法)在保存期就报出来,而不是等构建跑到一半失败。
		if msg, bad := profileDefect(p); bad {
			out = append(out, BuildEnvProblem{
				Code: ProblemConfigProfilePath, Stage: stage, Job: job, Field: ConfigKeyConfigProfileIDs,
				Message: fmt.Sprintf("阶段「%s」的节点「%s」勾选的配置资源「%s」%s,请联系管理员修正", stage, job, p.ID, msg),
			})
		}
	}
	return out
}
