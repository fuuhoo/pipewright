// job_types.go 收拢「job 类型本身是否还成立」的领域判定,与 buildenv 白名单(选了什么环境)正交。
//
// 撤销类型不迁移:老流水线里残留的节点既不在前端 picker 出现,保存与运行都**明确报错**并给出
// 改配指引 —— 静默放行会让用户以为门禁生效(health_check 曾经的真实行为)。
package pipeline

import (
	"errors"
	"fmt"
	"strings"
)

// ErrJobTypeRetired 是「节点类型已撤销,需改配后重存」的哨兵错误(HTTP 映射 422)。
var ErrJobTypeRetired = errors.New("pipeline: job type retired")

// ErrHealthProbeInvalid 是「部署节点开了健康探测但参数不完整」的哨兵错误(HTTP 映射 422)。
var ErrHealthProbeInvalid = errors.New("pipeline: health probe configuration invalid")

// ErrBuildTaskInvalid 是「构建任务缺产物档位 / 档位非法」的哨兵错误(HTTP 映射 422)。
var ErrBuildTaskInvalid = errors.New("pipeline: build task configuration invalid")

// 合并后的「构建」任务与其相关键。
const (
	// JobTypeBuild 是唯一的构建任务类型:按产物档位决定跑脚本构建还是构建镜像。
	JobTypeBuild = "build"
	// JobTypeBuildImage 是旧的独立镜像构建类型(仍执行,不再出现在 picker)。
	JobTypeBuildImage = "build_image"
	// ConfigKeyArtifactType 是产物档位键(image | jar | dist)。
	// build_image / build / deploy 三处同名(执行侧按同一键读),故不另起名字。
	ConfigKeyArtifactType = "artifactType"
	// ConfigKeyPushImage 是「构建后推送镜像」开关(仅 image 档位有意义):
	// "false"/"0"/"no" = 只构建不推送,其余(含缺省)= 推送到运行所在环境绑定的镜像仓。
	ConfigKeyPushImage = "pushImage"
)

// buildArtifactTiers 是构建任务可选的产物档位。
var buildArtifactTiers = map[string]bool{ArtifactImage: true, ArtifactJAR: true, ArtifactDist: true}

// EffectiveJobType 把「构建」任务映射到真正执行它的类型,让派发/校验只需认识 build_image 与 script:
// 产物档位是镜像 → build_image(有 Dockerfile 走 docker build,否则按工具链容器构建);
// 其余档位(jar/dist)→ script(在构建环境容器跑 commands,再按 artifactPath 收产物)。
// 其它类型原样返回。
func EffectiveJobType(jobType string, cfg map[string]any) string {
	if strings.TrimSpace(jobType) != JobTypeBuild {
		return jobType
	}
	if strings.TrimSpace(ConfigString(cfg, ConfigKeyArtifactType)) == ArtifactImage {
		return JobTypeBuildImage
	}
	return StepTypeScript
}

// PushImageEnabled 报告构建出的镜像是否推送到环境绑定的镜像仓。
// 缺省 = 推送(合并前 build_image 节点总是自动推送,开关只为「先只构建」而设,不是新默认)。
func PushImageEnabled(cfg map[string]any) bool {
	switch strings.ToLower(strings.TrimSpace(ConfigString(cfg, ConfigKeyPushImage))) {
	case "false", "0", "no":
		return false
	default:
		return true
	}
}

// 部署节点健康探测的 job.Config 键(值与 deploy.CfgKeyHealth* 逐字一致:build 层原样透传,
// 两侧键名不同就会静默失配 —— 探测形同没配)。
const (
	// ConfigKeyHealthProbe 是探测方式:none | http | command(空 = 不探测)。
	ConfigKeyHealthProbe = "healthProbe"
	// ConfigKeyHealthURL 是 http 探测地址(部署机本机视角,如 http://localhost:8080/healthz)。
	ConfigKeyHealthURL = "healthUrl"
	// ConfigKeyHealthCommand 是命令探测文本(在目标机经 sh -c 执行,与非零退出即不通)。
	ConfigKeyHealthCommand = "healthCommand"
	// ConfigKeyHealthRetries 是最大尝试次数(留空用领域默认)。
	ConfigKeyHealthRetries = "healthRetries"
	// ConfigKeyHealthInterval 是两次尝试的间隔秒数。
	ConfigKeyHealthInterval = "healthInterval"
	// ConfigKeyHealthTimeout 是单次探测超时秒数。
	ConfigKeyHealthTimeout = "healthTimeout"
)

// 健康探测方式枚举(与 deploy.HealthCheck* 的字符串取值对齐)。
const (
	HealthProbeNone    = "none"
	HealthProbeHTTP    = "http"
	HealthProbeCommand = "command"
)

// deployJobTypes 是「把产物/命令发到目标机」的节点类型集合(唯一出处,执行侧与校验侧共用)。
// deploy_frontend 是预填 nginx 重启命令的部署模板,与 deploy_ssh 同一条执行路径。
var deployJobTypes = map[string]bool{
	"deploy_ssh":      true,
	"deploy_frontend": true,
}

// IsDeployJobType 报告 job 类型是否按部署路径执行(目标机 SSH 发布 + 可选健康门控)。
func IsDeployJobType(jobType string) bool { return deployJobTypes[strings.TrimSpace(jobType)] }

// retiredJobTypes 是已撤销的节点类型 → 改配指引(唯一出处;前端 picker 与执行侧都据此收敛)。
var retiredJobTypes = map[string]string{
	"health_check": "健康探测已并入部署任务:删除本节点,在部署任务的「健康探测」里配置探测方式",
}

// RetiredJobType 报告该 job 类型是否已撤销,以及改配指引。
func RetiredJobType(jobType string) (string, bool) {
	guidance, ok := retiredJobTypes[strings.TrimSpace(jobType)]
	return guidance, ok
}

// jobTypeIssue 是一条「类型/参数级」的节点问题:哨兵错误只用于 errors.Is 分派,
// 面向用户的文案放自己手里(避免哨兵的英文前缀混进 422 消息)。
type jobTypeIssue struct {
	detail   string
	sentinel error
}

func (e *jobTypeIssue) Error() string  { return e.detail }
func (e *jobTypeIssue) Unwrap() error  { return e.sentinel }

func issuef(sentinel error, format string, args ...any) error {
	return &jobTypeIssue{detail: fmt.Sprintf(format, args...), sentinel: sentinel}
}

// validateHealthProbe 校验部署节点的健康探测:探测方式合法且对应必填项非空。
// 未开探测(空 / none)直接通过 —— 那是「不探测」的显式选择,不是漏配。
func validateHealthProbe(stageName, jobName, jobType string, cfg map[string]any) error {
	if !IsDeployJobType(jobType) {
		return nil
	}
	probe := ConfigString(cfg, ConfigKeyHealthProbe)
	if probe == "" || probe == HealthProbeNone {
		return nil
	}
	where := fmt.Sprintf("阶段「%s」任务「%s」", stageName, jobName)
	switch probe {
	case HealthProbeHTTP:
		if ConfigString(cfg, ConfigKeyHealthURL) == "" {
			return issuef(ErrHealthProbeInvalid, "%s:探测方式为 http 但未填探测 URL", where)
		}
	case HealthProbeCommand:
		if ConfigString(cfg, ConfigKeyHealthCommand) == "" {
			return issuef(ErrHealthProbeInvalid, "%s:探测方式为命令但未填探测命令", where)
		}
	default:
		return issuef(ErrHealthProbeInvalid, "%s:探测方式 %q 非法(仅支持 none / http / command)", where, probe)
	}
	return nil
}

// validateBuildTask 要求「构建」任务显式声明产物档位:档位决定它是「跑脚本收文件产物」还是
// 「构建 Dockerfile/工具链镜像」,留空等于让执行侧猜同一份配置该走哪条路径。
func validateBuildTask(stageName, jobName, jobType string, cfg map[string]any) error {
	if strings.TrimSpace(jobType) != JobTypeBuild {
		return nil
	}
	if buildArtifactTiers[strings.TrimSpace(ConfigString(cfg, ConfigKeyArtifactType))] {
		return nil
	}
	return issuef(ErrBuildTaskInvalid, "阶段「%s」任务「%s」要先选产物档位(image / jar / dist)", stageName, jobName)
}
