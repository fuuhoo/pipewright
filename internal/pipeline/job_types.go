// job_types.go 收拢「job 类型本身是否还成立」的领域判定,与 buildenv 白名单(选了什么环境)正交。
//
// 撤销类型不迁移:老流水线里残留的节点既不在前端 picker 出现,保存与运行都**明确报错**并给出
// 改配指引 —— 静默放行会让用户以为门禁生效(health_check 曾经的真实行为)。
package pipeline

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ErrJobTypeRetired 是「节点类型已撤销,需改配后重存」的哨兵错误(HTTP 映射 422)。
var ErrJobTypeRetired = errors.New("pipeline: job type retired")

// ErrHealthProbeInvalid 是「部署节点开了健康探测但参数不完整」的哨兵错误(HTTP 映射 422)。
var ErrHealthProbeInvalid = errors.New("pipeline: health probe configuration invalid")

// ErrBuildTaskInvalid 是「构建任务缺产物档位 / 档位非法」的哨兵错误(HTTP 映射 422)。
var ErrBuildTaskInvalid = errors.New("pipeline: build task configuration invalid")

// ErrArtifactSourceInvalid 是「部署节点的产物来源任务引用不成立」的哨兵错误(HTTP 映射 422)。
var ErrArtifactSourceInvalid = errors.New("pipeline: deploy artifact source job invalid")

// ErrDockerDeployInvalid 是「docker 部署节点的方式/参数不成立」的哨兵错误(HTTP 映射 422)。
var ErrDockerDeployInvalid = errors.New("pipeline: docker deploy configuration invalid")

// ErrK8sDeployInvalid 是「K8s 发布节点的参数不成立」的哨兵错误(HTTP 映射 422)。
var ErrK8sDeployInvalid = errors.New("pipeline: k8s release configuration invalid")

// ErrDeployTargetInvalid 是「发机器类部署节点的落点或分批档位不成立」的哨兵错误(HTTP 映射 422)。
var ErrDeployTargetInvalid = errors.New("pipeline: deploy target and batching configuration invalid")

// 合并后的「构建」任务与其相关键。
const (
	// JobTypeBuild 是唯一的构建任务类型:按产物档位决定跑脚本构建还是构建镜像。
	JobTypeBuild = "build"
	// JobTypeBuildImage 是旧的独立镜像构建类型(仍执行,不再出现在 picker)。
	JobTypeBuildImage = "build_image"
	// ConfigKeyArtifactType 是产物档位键(image | file;jar / dist 为历史别名)。
	// build_image / build / deploy 三处同名(执行侧按同一键读),故不另起名字。
	ConfigKeyArtifactType = "artifactType"
	// ConfigKeyPushImage 是「构建后推送镜像」开关(仅 image 档位有意义):
	// "false"/"0"/"no" = 只构建不推送,其余(含缺省)= 推送到运行所在环境绑定的镜像仓。
	ConfigKeyPushImage = "pushImage"
	// ConfigKeyArtifactFrom 是部署节点的「产物来源任务」:值 = 上游构建任务的 **job ID**
	// (不是名字 —— ID 唯一、名字可重复,存名字会让两个同名任务互相顶掉)。
	// 并行构建出多件同类产物时,部署节点靠它锁定要发那一条;留空 = 按类型挑首个(历史行为)。
	ConfigKeyArtifactFrom = "artifactFrom"
)

// buildArtifactTiers 是构建任务可选的产物档位。jar / dist 是收敛前的历史档位,继续认:
// 存量流水线与 .pipewright.yml 不迁移,但新配置一律写 file。
var buildArtifactTiers = map[string]bool{
	ArtifactImage: true,
	ArtifactFile:  true,
	ArtifactJAR:   true,
	ArtifactDist:  true,
}

// EffectiveJobType 把「构建」任务映射到真正执行它的类型,让派发/校验只需认识 build_image 与 script:
// 产物档位是镜像 → build_image(有 Dockerfile 走 docker build,否则按工具链容器构建);
// 其余档位(file,含历史的 jar/dist)→ script(在构建环境容器跑 commands,再按 artifactPath 收产物)。
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

// docker 部署节点(deploy_docker)的 job.Config 键(值与 deploy.CfgKey* 逐字一致,同上)。
const (
	// JobTypeDeployDocker 是「docker 部署」节点:在目标机用 docker 起,方式为单容器或 compose。
	JobTypeDeployDocker = "deploy_docker"
	// ConfigKeyDockerMode 是 docker 部署的方式:run(单容器)| compose(整份 YAML)。
	// 两种方式在目标机上做的事完全不同(run 是停旧起新换镜像,compose 是交 CLI 编排),
	// 留空等于让执行侧猜这份配置该走哪条路 —— 与构建任务的产物档位同一要求。
	ConfigKeyDockerMode = "dockerMode"
	// ConfigKeyStackName 是 compose 的项目名(-p 值,也是 /opt/pipewright/stacks/<name> 目录名)。
	ConfigKeyStackName = "stackName"
	// ConfigKeyComposeYaml 是 compose 正文(原样上传为目标机上的 docker-compose.yml)。
	ConfigKeyComposeYaml = "composeYaml"
	// DockerModeRun / DockerModeCompose 是 docker 部署的两种方式。
	DockerModeRun     = "run"
	DockerModeCompose = "compose"
	// ConfigKeyComposeSource 是 compose 正文的来源:粘贴(paste)| 引用仓库文件(repo)。
	// 空 = paste,与引入该键之前的存量节点行为逐字一致(存量节点只可能有 composeYaml)。
	ConfigKeyComposeSource = "composeSource"
	// ConfigKeyComposeFile 是 composeSource=repo 时要读的仓库相对路径(如 deploy/docker-compose.yml)。
	ConfigKeyComposeFile = "composeFile"
	// ComposeSourcePaste / ComposeSourceRepo 是 composeSource 的取值。
	ComposeSourcePaste = "paste"
	ComposeSourceRepo  = "repo"
)

// K8s 发布节点(deploy_k8s)的 job.Config 键(值与 deploy.CfgKey* 逐字一致,同上)。
const (
	// JobTypeDeployK8s 是「K8s 发布」节点:平台直连集群 API server 换镜像,不经任何跳板机。
	JobTypeDeployK8s = "deploy_k8s"
	// ConfigKeyClusterID 是目标集群(kube_clusters.id)。与目标主机(ConfigKeyServerIDs)互斥:一个节点发机器或发集群。
	ConfigKeyClusterID = "clusterId"
	// ConfigKeyNamespace 是目标负载所在命名空间。
	ConfigKeyNamespace = "namespace"
	// ConfigKeyWorkloadKind 是负载类型(Deployment | StatefulSet);留空按 Deployment。
	ConfigKeyWorkloadKind = "workloadKind"
	// ConfigKeyWorkloadName 是负载名(如 api)。
	ConfigKeyWorkloadName = "workloadName"
	// ConfigKeyK8sContainer 是 pod 规格里要换镜像的容器名;规格只有一个容器时可留空。
	ConfigKeyK8sContainer = "containerName"
	// ConfigKeyRolloutTimeout 是等滚动完成的秒数(空 = 领域默认 300)。
	ConfigKeyRolloutTimeout = "rolloutTimeout"
	// ConfigKeyAutoRollback 是滚动失败后是否回填上一镜像("false" 关,其余含空 = 开)。
	ConfigKeyAutoRollback = "autoRollback"
	// ConfigKeyManifestSource 是清单来源:none(只换镜像)| repo(读项目仓库里的文件)| paste(粘贴正文)。
	// 留空 = none,与已有节点兼容(它们没这一格)。
	ConfigKeyManifestSource = "manifestSource"
	// ConfigKeyManifestFile 是 repo 来源时仓库根下的相对路径(与 composeFile 同一条路径规则)。
	ConfigKeyManifestFile = "manifestFile"
	// ConfigKeyManifestYaml 是 paste 来源时粘贴的清单正文(多文档用 --- 分隔,可含 {{IMAGE}} 占位符)。
	ConfigKeyManifestYaml = "manifestYaml"
)

// ManifestSource* 是 manifestSource 的取值(与 composeSource 同一命名形状)。
const (
	ManifestSourceNone  = "none"
	ManifestSourcePaste = "paste"
	ManifestSourceRepo  = "repo"
)

// manifestMaxBytes 是粘贴清单正文的上限。清单比 compose 小得多(几十 KB 已是很重的部署),
// 卡在这里而不是更高,是因为它最终要落 deploy_manifests.body(MySQL TEXT 的硬上限就是 64 KiB,
// 超了会被**截断**而不报错 —— 半截清单被应用出去比保存期拒掉难查得多)。
const manifestMaxBytes = 64 << 10

// k8sWorkloadKinds 是可发布的负载类型(与 kube.workloadAPI 白名单同一清单)。
var k8sWorkloadKinds = map[string]bool{"Deployment": true, "StatefulSet": true}

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

// 发机器类部署节点的「落点 + 分批」job.Config 键。
const (
	// ConfigKeyServerIDs 是目标主机多选,值为逗号分隔的服务器 ID(与 configProfileIds 同一形状)。
	// **勾选顺序就是发布顺序**:分批时先勾的那批先铺。
	ConfigKeyServerIDs = "serverIds"
	// ConfigKeyServerID 是历史的单主机键。存量节点与旧 .pipewright.yml 只可能有它,
	// 所以读的时候复数键优先、空则回落它;前端重存后会把单数键删掉(droppedKeys)。
	ConfigKeyServerID = "serverId"
	// ConfigKeyStrategy 是发布策略档位。空 = rolling。
	// 之所以在保存期就判取值:引擎 NormalizeStrategy 对认不出的串一律静默走 rolling,
	// 而部署日志照抄所选串 —— 「选了个不存在的档位」这件事过去没人拦得住。
	ConfigKeyStrategy = "strategy"
	// ConfigKeyCanaryCount 是分批发布的首批台数(空 = 1 台)。
	ConfigKeyCanaryCount = "canaryCount"
)

// 发布策略档位取值。blue-green 与 blue_green 都认(前端选项写连字符,引擎归一后是下划线,
// NormalizeStrategy 两种都收),校验侧没必要另立一套拼写。
const (
	DeployStrategyRolling     = "rolling"
	DeployStrategyCanary      = "canary"
	DeployStrategyBlueGreen   = "blue_green"
	DeployStrategyInteractive = "interactive"
)

// deployStrategies 是 NormalizeStrategy 真正认得的档位集合(含它的连字符/驼峰容错写法)。
var deployStrategies = map[string]string{
	"rolling":     DeployStrategyRolling,
	"canary":      DeployStrategyCanary,
	"blue_green":  DeployStrategyBlueGreen,
	"blue-green":  DeployStrategyBlueGreen,
	"bluegreen":   DeployStrategyBlueGreen,
	"blue green":  DeployStrategyBlueGreen,
	"interactive": DeployStrategyInteractive,
	"batch":       DeployStrategyInteractive,
}

// DeployServerIDs 取部署节点的目标主机列表:复数键优先,空则回落到历史的单值键。
// 逐项 trim、去空、去重且**保持顺序** —— 分批把这份顺序当成先发顺序。
func DeployServerIDs(cfg map[string]any) []string {
	raw := strings.TrimSpace(ConfigString(cfg, ConfigKeyServerIDs))
	if raw == "" {
		raw = strings.TrimSpace(ConfigString(cfg, ConfigKeyServerID))
	}
	var out []string
	seen := make(map[string]bool)
	for _, part := range strings.Split(raw, ",") {
		if id := strings.TrimSpace(part); id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// validateDeployTargets 要求发机器的部署节点说清「发到哪几台」与「这几台怎么发」。
//
// 这三件事过去都是**看不见的错**:落点没选要等到执行期才报( dag_stage_exec 那句「未选目标」);
// 策略串认不出则被引擎静默折成 rolling,日志还照抄所选档位;而单机时分批/蓝绿与一次性
// 行为完全等价 —— 表单给了一个不会改变任何事的选项。保存期判掉,比事后猜便宜得多。
func validateDeployTargets(stageName, jobName, jobType string, cfg map[string]any) error {
	switch strings.TrimSpace(jobType) {
	case "deploy_ssh", "deploy_frontend", JobTypeDeployDocker:
	default:
		return nil
	}
	where := fmt.Sprintf("阶段「%s」任务「%s」", stageName, jobName)
	ids := DeployServerIDs(cfg)
	if len(ids) == 0 {
		return issuef(ErrDeployTargetInvalid, "%s:未选目标主机", where)
	}

	raw := strings.TrimSpace(ConfigString(cfg, ConfigKeyStrategy))
	strategy := DeployStrategyRolling
	if raw != "" {
		norm, ok := deployStrategies[strings.ToLower(raw)]
		if !ok {
			return issuef(ErrDeployTargetInvalid,
				"%s:发布策略 %q 不存在(引擎只实现了一次性 / 分批 / 蓝绿 / 分批暂停,选错的一档会被静默当成一次性发布)", where, raw)
		}
		strategy = norm
	}

	// 分批与整机切换都需要「多台之间」才有的事:一台机器时它们与一次性完全同义。
	if len(ids) < 2 && strategy != DeployStrategyRolling {
		return issuef(ErrDeployTargetInvalid,
			"%s:只选了一台主机,分批/蓝绿/首批暂停与一次性发布没有任何差别(至少选两台,或把发布策略改回一次性)", where)
	}
	// compose 与命令型两条腿各自成一条链路,都不读策略(compose 交 CLI 编排,命令型逐机跑一条命令)。
	if strategy != DeployStrategyRolling {
		if strings.TrimSpace(ConfigString(cfg, ConfigKeyDockerMode)) == DockerModeCompose {
			return issuef(ErrDeployTargetInvalid, "%s:Compose 部署由 docker compose 自己编排,分批/蓝绿不适用(改回一次性发布)", where)
		}
		if strings.TrimSpace(ConfigString(cfg, ConfigKeyArtifactType)) == "command" {
			return issuef(ErrDeployTargetInvalid, "%s:命令型部署只是在每台机器跑一条命令,分批/蓝绿不适用(改回一次性发布)", where)
		}
	}

	if raw := strings.TrimSpace(ConfigString(cfg, ConfigKeyCanaryCount)); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return issuef(ErrDeployTargetInvalid, "%s:首批台数 %q 不是正整数", where, raw)
		}
		// 只有一批的「分批」等于不分批,与其让用户等一个不会发生的门,不如当场说清。
		if strategy != DeployStrategyRolling && n >= len(ids) {
			return issuef(ErrDeployTargetInvalid, "%s:首批台数 %d 已覆盖全部 %d 台主机,后面没有可分批的机器", where, n, len(ids))
		}
	}
	return nil
}

// deployJobTypes 是「把产物/命令发到目标机」的节点类型集合(唯一出处,执行侧与校验侧共用)。
// deploy_frontend 是预填 nginx 重启命令的部署模板,与 deploy_ssh 同一条执行路径。
// deploy_docker 交 docker 落地(单容器 or compose),仍算部署节点:健康探测与产物来源校验都适用于它。
var deployJobTypes = map[string]bool{
	"deploy_ssh":      true,
	"deploy_frontend": true,
	"deploy_docker":   true,
	"deploy_k8s":      true,
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
	return issuef(ErrBuildTaskInvalid, "阶段「%s」任务「%s」要先选产物档位(镜像 / 产物)", stageName, jobName)
}

// reStackName 与 httpapi 侧的 reDockerTgt 逐字一致:compose 项目名既是 `compose -p` 的参数,
// 也是目标机上 /opt/pipewright/stacks/<name> 的目录名 —— 首字符不许是 `-`(会被当选项)、
// 不许含 `/`(路径穿越)。两处改了不同步,保存能过但部署会打出个进不去的目录。
var reStackName = regexp.MustCompile(`^[\w][\w.-]*$`)

// reK8sNamespace 是 k8s 的 DNS-label(Namespace 的合法形状),与 kube.validateNamespace、
// 前端表单三处同一规则。保存期就拒掉是为了不放过一个「拼进 URL 路径」的野值(.. 之类)。
var reK8sNamespace = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// composeFileMaxLen 是仓库内 compose 路径的长度上限(路径不是内容,不该有额度)。
const composeFileMaxLen = 512

// RepoYAMLPathOK 报告这是个能拿去仓库里读的相对路径:必须是 .yml/.yaml、不能是绝对路径
// 或含 .. 段。compose 文件与 k8s 清单同用这一条(两者都只是"仓库里的一个 YAML")。
// 执行侧(repocache.ReadFile)自己还有一道同形状的门 —— 两处都判是因为它们各自
// 都会被独立调用:pipeline 判它是「用户填错了」(保存期 422),repocache 判的是「别拿我当
// 任意文件读取器」(边界防护)。
func RepoYAMLPathOK(path string) bool {
	p := strings.TrimSpace(path)
	if p == "" || len(p) > composeFileMaxLen {
		return false
	}
	if !strings.HasSuffix(p, ".yml") && !strings.HasSuffix(p, ".yaml") {
		return false
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, `\`) {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

const (
	// stackNameMaxLen 是 compose 项目名上限(与 httpapi 部署端点同)。
	stackNameMaxLen = 128
	// composeMaxBytes 是 compose 正文上限(与 httpapi 端点、前端 composePaste.ts 同一额度)。
	composeMaxBytes = 512 << 10
)

// validateDeployDocker 要求 docker 部署节点说清用哪种方式,且 compose 方式下项目名与正文配齐:
// 半截配置跑到目标机上就是「上传空文件再 up -d」,报错还不指向真正原因。
func validateDeployDocker(stageName, jobName, jobType string, cfg map[string]any) error {
	if strings.TrimSpace(jobType) != JobTypeDeployDocker {
		return nil
	}
	where := fmt.Sprintf("阶段「%s」任务「%s」", stageName, jobName)
	switch mode := strings.TrimSpace(ConfigString(cfg, ConfigKeyDockerMode)); mode {
	case "":
		return issuef(ErrDockerDeployInvalid, "%s要先选 docker 部署方式(单容器 / Compose)", where)
	case DockerModeRun:
		// 单容器 = 把上游构建出的镜像停旧起新,产物来源由 validateArtifactSources 管。
		return nil
	case DockerModeCompose:
		name := strings.TrimSpace(ConfigString(cfg, ConfigKeyStackName))
		switch {
		case name == "":
			return issuef(ErrDockerDeployInvalid, "%s:Compose 部署未填项目名", where)
		case len(name) > stackNameMaxLen:
			return issuef(ErrDockerDeployInvalid, "%s:Compose 项目名超过 %d 字符", where, stackNameMaxLen)
		case !reStackName.MatchString(name):
			return issuef(ErrDockerDeployInvalid,
				"%s:Compose 项目名 %q 非法(仅字母数字与 . _ -,不以 - 开头,不含 /)", where, name)
		}
		yaml := ConfigString(cfg, ConfigKeyComposeYaml)
		file := ConfigString(cfg, ConfigKeyComposeFile)
		// 来源只认 composeSource 一项,另一路的残留值一律忽略(与 dockerMode 切档的既有语义一致:
		// 编辑器里换来源会藏掉另一路的输入框,但配置里的旧值不该反过来卡住保存)。
		switch src := strings.TrimSpace(ConfigString(cfg, ConfigKeyComposeSource)); src {
		case "", ComposeSourcePaste:
			if strings.TrimSpace(yaml) == "" {
				return issuef(ErrDockerDeployInvalid, "%s:Compose 部署没有正文(粘贴 docker-compose.yml 内容,或改选仓库文件)", where)
			}
			if len(yaml) > composeMaxBytes {
				return issuef(ErrDockerDeployInvalid, "%s:Compose 正文超过 %d KiB 上限", where, composeMaxBytes>>10)
			}
		case ComposeSourceRepo:
			// 引用仓库文件时正文由执行侧现读(见 build 的 repoFileReader),这里只判路径形状 ——
			// 判不出内容是否存在(那要碰网络),但路径写错的概率远高于分支被 force-push。
			if !RepoYAMLPathOK(file) {
				return issuef(ErrDockerDeployInvalid,
					"%s:compose 文件路径 %q 非法(仓库根下的相对路径,以 .yml 或 .yaml 结尾,不含 .. 与绝对路径)", where, file)
			}
		default:
			return issuef(ErrDockerDeployInvalid, "%s:compose 正文来源 %q 非法(仅支持 paste / repo)", where, src)
		}
		return nil
	default:
		return issuef(ErrDockerDeployInvalid, "%s:docker 部署方式 %q 非法(仅支持 run / compose)", where, mode)
	}
}

// validateDeployK8s 要求集群发布节点把「发到哪」说全,并保证同一件事只有一处说了算:
//   - 不带清单(manifestSource 空/none):寻址靠 clusterId + workloadName(+ 可选 namespace / kind),
//     执行期只换镜像;
//   - 带清单(repo / paste):命名空间与负载都从清单读,所以 namespace 这格必须空、workloadName 可选。
//
// 半截配置跑到执行时就是「连上一个不像样的地址」或「把清单发到另一个命名空间」,
// 报错信息指向不到真正漏掉的那个字段 —— 所以这些互斥/必填关系在保存期就判掉。
func validateDeployK8s(stageName, jobName, jobType string, cfg map[string]any) error {
	if strings.TrimSpace(jobType) != JobTypeDeployK8s {
		return nil
	}
	where := fmt.Sprintf("阶段「%s」任务「%s」", stageName, jobName)
	if len(DeployServerIDs(cfg)) > 0 {
		return issuef(ErrK8sDeployInvalid, "%s:同时配了目标主机与集群 —— 一个节点只能发一种落点", where)
	}
	if ConfigString(cfg, ConfigKeyClusterID) == "" {
		return issuef(ErrK8sDeployInvalid, "%s:K8s 发布未选目标集群", where)
	}
	// 清单来源决定「发到哪」由谁说了算:无清单时是这几个配置格,有清单时是清单本身。
	src := strings.TrimSpace(ConfigString(cfg, ConfigKeyManifestSource))
	if src == "" {
		src = ManifestSourceNone // 老节点没这一格
	}
	switch src {
	case ManifestSourceNone:
	case ManifestSourcePaste:
		body := ConfigString(cfg, ConfigKeyManifestYaml)
		switch {
		case strings.TrimSpace(body) == "":
			return issuef(ErrK8sDeployInvalid, "%s:选择粘贴清单却没有正文", where)
		case len(body) > manifestMaxBytes:
			return issuef(ErrK8sDeployInvalid, "%s:清单正文超过 %d KiB 上限", where, manifestMaxBytes>>10)
		}
		// 正文内容(kind 白名单、Secret、占位符)不在这里判:它带 {{IMAGE}} 之类的占位符时
		// 还不是可应用的清单,要到执行期渲染完才判得准(kube.ParseManifests)。
	case ManifestSourceRepo:
		// 引用仓库文件时同 compose:只判路径形状,文件在不在要碰网络,留给执行期。
		if !RepoYAMLPathOK(ConfigString(cfg, ConfigKeyManifestFile)) {
			return issuef(ErrK8sDeployInvalid,
				"%s:清单路径 %q 非法(仓库根下的相对路径,以 .yml 或 .yaml 结尾,不含 .. 与绝对路径)", where,
				ConfigString(cfg, ConfigKeyManifestFile))
		}
	default:
		return issuef(ErrK8sDeployInvalid, "%s:清单来源 %q 非法(仅支持 none / repo / paste)", where, src)
	}
	manifestMode := src != ManifestSourceNone

	// 命名空间:无清单时这格是生效值(留空 = 用集群登记的默认);一旦填了必须是合法 DNS-label,
	// 它会原样拼进 API server 的 URL 路径。有清单时则以清单的 metadata.namespace 为准,
	// 所以这格**必须空着** —— 两处各填一个值就必有一个是假的,不能做成「填了但被忽略」。
	if ns := ConfigString(cfg, ConfigKeyNamespace); manifestMode {
		if ns != "" {
			return issuef(ErrK8sDeployInvalid,
				"%s:用清单发布时命名空间以清单里的 metadata.namespace 为准(清单没写则用集群登记的默认命名空间),请清空「命名空间」这一格", where)
		}
	} else if ns != "" && (len(ns) > 63 || !reK8sNamespace.MatchString(ns)) {
		return issuef(ErrK8sDeployInvalid, "%s:命名空间 %q 非法(只允许小写字母、数字与 -,首尾须为字母或数字,最长 63)", where, ns)
	}
	// 负载名:无清单时必填(它是寻址的唯一依据)。有清单时它整个不参与 —— 清单里每一份负载都会
	// 被按序应用并各自等滚完(releaseByManifest 不读这格),所以不强制为空:残留值既然无害,
	// 拦下来只会让用户在切档后重打一遍(与 compose 对另一路残留正文的态度同一口径)。
	// 前端因此在清单档直接把这格收掉,而不是留一个填了没用的框。
	if !manifestMode && ConfigString(cfg, ConfigKeyWorkloadName) == "" {
		return issuef(ErrK8sDeployInvalid, "%s:K8s 发布未填负载名(如 api)", where)
	}
	if kind := ConfigString(cfg, ConfigKeyWorkloadKind); kind != "" && !k8sWorkloadKinds[kind] {
		return issuef(ErrK8sDeployInvalid, "%s:负载类型 %q 不支持(仅 Deployment / StatefulSet)", where, kind)
	}
	if v := ConfigString(cfg, ConfigKeyRolloutTimeout); v != "" {
		secs, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || secs < 1 || secs > rolloutTimeoutMaxSecs {
			return issuef(ErrK8sDeployInvalid, "%s:等待滚动秒数 %q 非法(1..%d 的整数)", where, v, rolloutTimeoutMaxSecs)
		}
	}
	if v := ConfigString(cfg, ConfigKeyAutoRollback); v != "" && v != "true" && v != "false" {
		return issuef(ErrK8sDeployInvalid, "%s:autoRollback 只接受 true / false,收到 %q", where, v)
	}
	// 滚动由集群控制器做:选 blue_green / canary 不会有任何效果,那就等于向用户假承诺。
	if st := ConfigString(cfg, "strategy"); st != "" && st != "rolling" {
		return issuef(ErrK8sDeployInvalid, "%s:K8s 发布的滚动策略由集群控制器决定,不支持策略 %q(留空或 rolling)", where, st)
	}
	return nil
}

// rolloutTimeoutMaxSecs 与 deploy.maxRolloutTimeout 同一额度(30 分钟)。
const rolloutTimeoutMaxSecs = 1800

// nonProducingJobTypes 是本身不产出可部署产物的节点类型(与前端 artifactSources.ts 同一清单):
// 它们的产物不会带 sourceJobId,选作「产物来源」必然在运行时落空,保存时就拒绝。
var nonProducingJobTypes = map[string]bool{
	"git_source":      true,
	"push_image":      true,
	"notify":          true,
	"deploy_ssh":      true,
	"deploy_frontend": true,
	"deploy_docker":   true,
	"deploy_k8s":      true,
}

// producesArtifact 报告该类型任务是否可能产出可部署产物。
func producesArtifact(jobType string) bool {
	return !nonProducingJobTypes[strings.TrimSpace(jobType)]
}

// validateArtifactSources 校验部署节点的「产物来源任务」(artifactFrom)引用:
//  1. 引用的任务要存在、不是本节点自己,且类型确实产产物;
//  2. 要**一定在本节点开跑前已执行完**:同阶段需被本任务 needs 传递依赖,跨阶段需处于上游阶段
//     (整个阶段图没声明 needs 时按数组顺序 = 线性执行序,与画布/执行侧同一口径)。
//
// 放在 normalizeSpec 末尾而非逐 job 校验:引用成立与否取决于全图拓扑,单看一个节点无从判断。
// 不在这里拦,用户看到的就只是运行时「部署节点没有产物」,猜不到是来源任务选错了。
func validateArtifactSources(stages []Stage) error {
	stageOf := make(map[string]int, len(stages))
	jobs := make(map[string]Job, len(stages))
	linear := true
	for i, st := range stages {
		if len(st.Needs) > 0 {
			linear = false
		}
		for _, jb := range st.Jobs {
			stageOf[jb.ID] = i
			jobs[jb.ID] = jb
		}
	}
	for i, st := range stages {
		// 本阶段的上游阶段下标集合(阶段 ID → 下标);线性回退 = 排在前面的所有阶段。
		upstream := make(map[int]bool, i)
		for j := 0; j < i; j++ {
			if linear {
				upstream[j] = true
				continue
			}
			if stageNeedsTransitively(stages, st.Needs, stages[j].ID) {
				upstream[j] = true
			}
		}
		for _, jb := range st.Jobs {
			ref := strings.TrimSpace(ConfigString(jb.Config, ConfigKeyArtifactFrom))
			if ref == "" || !IsDeployJobType(jb.Type) {
				continue
			}
			where := fmt.Sprintf("阶段「%s」任务「%s」的产物来源任务", st.Name, jb.Name)
			srcStage, ok := stageOf[ref]
			switch {
			case !ok:
				return issuef(ErrArtifactSourceInvalid, "%s「%s」已不存在,请重选", where, ref)
			case ref == jb.ID:
				return issuef(ErrArtifactSourceInvalid, "%s不能是本任务自己", where)
			case !producesArtifact(jobs[ref].Type):
				return issuef(ErrArtifactSourceInvalid, "%s「%s」不产出可部署产物(它不是构建任务)", where, jobs[ref].Name)
			case srcStage == i:
				if !jobNeedsTransitively(st.Jobs, jb.Needs, ref) {
					return issuef(ErrArtifactSourceInvalid, "%s「%s」与本任务并行(未依赖它),产物可能还没产出:先给它加上依赖", where, jobs[ref].Name)
				}
			case !upstream[srcStage]:
				return issuef(ErrArtifactSourceInvalid, "%s「%s」不在本任务的上游阶段,产物可能还没产出", where, jobs[ref].Name)
			}
		}
	}
	return nil
}

// stageNeedsTransitively 报告 `needs` 的传递闭包里是否含 targetID(阶段图已由 dag 校验过无环)。
func stageNeedsTransitively(stages []Stage, needs []string, targetID string) bool {
	byID := make(map[string][]string, len(stages))
	for _, st := range stages {
		byID[st.ID] = st.Needs
	}
	seen := map[string]bool{}
	stack := append([]string(nil), needs...)
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if id == targetID {
			return true
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		stack = append(stack, byID[id]...)
	}
	return false
}

// jobNeedsTransitively 报告同阶段内 `needs` 的传递闭包里是否含 targetID。
func jobNeedsTransitively(jobs []Job, needs []string, targetID string) bool {
	byID := make(map[string][]string, len(jobs))
	for _, jb := range jobs {
		byID[jb.ID] = jb.Needs
	}
	seen := map[string]bool{}
	stack := append([]string(nil), needs...)
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if id == targetID {
			return true
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		stack = append(stack, byID[id]...)
	}
	return false
}
