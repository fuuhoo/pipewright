// docker_deploy.go 是「docker 部署」节点(deploy_docker)在目标机上的落地。
//
// 两种方式在目标机上做的事完全不同,共用一个节点是因为它们都以 docker 为交付手段:
//   - 单容器(run):发的必须是上游构建出的镜像 —— 复用 image_release.go 的
//     pull → 停旧起新 → 健康门控 → 失败回滚上一镜像,与镜像产物的既有链路一字不差。
//   - Compose:整份 docker-compose.yml 交目标机的 compose CLI 编排,**不取构建产物**
//     (正文自带镜像与拓扑),与「命令型部署」同属无产物分支。
//
// compose 的落盘位置与 httpapi 的 stacks 部署端点逐字相同
// (/opt/pipewright/stacks/<项目名>/docker-compose.yml),流水线发出去的栈因此直接出现在
// 「容器」页那台机器的 Stacks 里 —— 两处管的是同一份,不是两份互相看不见的部署。
//
// 与包内其余路径同一约束:命令一律 array 化经 target 层执行,compose 正文只以**文件**落地,
// 绝不拼进 shell(AC-SEC-02);message 无明文密钥。
package deploy

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/fuuhoo/pipewright/internal/run"
	"github.com/fuuhoo/pipewright/internal/target"
)

// docker 部署节点的 cfg 键(与 pipeline.ConfigKey* 逐字一致:build 层原样透传,
// 两侧键名不同就会静默失配 —— 参数形同没填)。
const (
	// CfgKeyDockerMode 是部署方式(run | compose;空 = 非 docker 部署节点)。
	CfgKeyDockerMode = "dockerMode"
	// CfgKeyStackName 是 compose 项目名(`-p` 参数 + 受管目录名)。
	CfgKeyStackName = "stackName"
	// CfgKeyComposeYaml 是 compose 正文(原样上传为目标机上的 docker-compose.yml)。
	CfgKeyComposeYaml = "composeYaml"
	// CfgKeyComposeFile 是正文的来源仓库路径(仅日志用:流水线选「引用仓库文件」时,
	// 正文由 DAG 现读并填进 CfgKeyComposeYaml,这里记一下它原本是哪份文件,免得排查时
	// 对着目标机上那份 docker-compose.yml 猜来源)。
	CfgKeyComposeFile = "composeFile"
	// DockerModeRun / DockerModeCompose 取值与 pipeline.DockerMode* 相同。
	DockerModeRun     = "run"
	DockerModeCompose = "compose"
)

const (
	// stackBaseDir / composeFileName 与 httpapi server_stacks.go 的 stacksBaseDir /
	// composeFileName 同值:两处不一致,流水线发的栈在容器页就找不到文件,update / down
	// 只能靠容器标签半瞎工作。
	stackBaseDir    = "/opt/pipewright/stacks"
	composeFileName = "docker-compose.yml"
	// composeUpTimeout 是 compose 一条链路的预算(建目录 + 上传 + up,up 要在目标机拉镜像)。
	// 单命令 60s 的 execTimeout 在这里不够用,同 httpapi 的 stacksUpTimeout 给充裕额度。
	composeUpTimeout = 8 * time.Minute
	// composeMaxBytes / stackNameMaxLen 与 pipeline 保存期校验、前端 composePaste.ts 同一额度。
	composeMaxBytes = 512 << 10
	stackNameMaxLen = 128
)

// reStackName 与 httpapi 的 reDockerTgt 同规则:项目名首字符不许是 `-`(会被 compose 当选项),
// 不许含 `/`(它是目录名,能路径穿越到受管目录之外)。
var reStackName = regexp.MustCompile(`^[\w][\w.-]*$`)

// ErrComposeSpecInvalid 表示 compose 部署的项目名/正文在运行时不成立。保存期已由 pipeline 校验拦过,
// 这里兜住的是绕过保存的路径(.pipewright.yml 旧内容、改库数据)。
var ErrComposeSpecInvalid = errors.New("deploy: compose deployment specification invalid")

// dockerModeOf 取 cfg 里的 docker 部署方式(非 docker 节点为空)。
func dockerModeOf(cfg map[string]string) string {
	return strings.TrimSpace(cfg[CfgKeyDockerMode])
}

// IsDockerRunMode 报告该次部署是不是 docker 单容器方式 —— 它必须发镜像产物,
// 而 pickStageArtifact 在偏好为空时默认优先文件产物,不显式纠正就会发错。
func IsDockerRunMode(cfg map[string]string) bool {
	return dockerModeOf(cfg) == DockerModeRun
}

// composeStackPaths 校验项目名与正文,返回目标机上的受管目录与 compose 文件路径。
// 项目名**不做净化**:净化会静默换掉非法字符,那等于起了另一个名字的栈,旧栈还留在机器上。
func composeStackPaths(project, compose string) (string, string, error) {
	switch {
	case project == "":
		return "", "", fmt.Errorf("%w: 未填 compose 项目名", ErrComposeSpecInvalid)
	case len(project) > stackNameMaxLen:
		return "", "", fmt.Errorf("%w: 项目名超过 %d 字符", ErrComposeSpecInvalid, stackNameMaxLen)
	case !reStackName.MatchString(project):
		return "", "", fmt.Errorf("%w: 项目名 %q 非法(仅字母数字与 . _ -,不以 - 开头,不含 /)", ErrComposeSpecInvalid, project)
	case compose == "":
		return "", "", fmt.Errorf("%w: 项目「%s」没有 compose 正文", ErrComposeSpecInvalid, project)
	case len(compose) > composeMaxBytes:
		return "", "", fmt.Errorf("%w: compose 正文超过 %d KiB 上限", ErrComposeSpecInvalid, composeMaxBytes>>10)
	}
	dir := stackBaseDir + "/" + project
	return dir, dir + "/" + composeFileName, nil
}

// deployComposeStack 逐机把 compose 正文铺进受管目录并 up -d。逐机**串行**:同一台机有闸,
// 且 up 期间目标机在拉镜像,并发只会互相抢带宽。任一机失败 → 该机 failed,其余继续
// (与文件 / 镜像发布一致:部分失败可见,可只重试失败目标)。
func (s *service) deployComposeStack(ctx context.Context, runID string, serverIDs []string, cfg map[string]string, hc *HealthCheck) ([]TargetResult, error) {
	project := strings.TrimSpace(cfg[CfgKeyStackName])
	compose := strings.TrimSpace(cfg[CfgKeyComposeYaml])
	dir, composePath, err := composeStackPaths(project, compose)
	if err != nil {
		return nil, err
	}
	lg := cmdLogFrom(ctx)
	origin := "正文来自节点粘贴"
	if f := strings.TrimSpace(cfg[CfgKeyComposeFile]); f != "" {
		origin = "正文来自仓库文件 " + f
	}
	lg(cmdStreamStdout, fmt.Sprintf("· Compose 部署:项目「%s」→ 目标机 %s(%s)", project, composePath, origin))

	results := make([]TargetResult, 0, len(serverIDs))
	for _, sid := range serverIDs {
		srv, gerr := s.targets.Get(ctx, sid)
		if gerr != nil {
			if errors.Is(gerr, target.ErrNotFound) {
				return nil, ErrServerNotFound
			}
			return nil, gerr
		}
		started := time.Now().UTC()
		msg, ok := s.composeOne(ctx, sid, project, compose, dir, composePath)
		fin := time.Now().UTC()
		tr := TargetResult{ServerID: sid, ServerName: srv.Name, StartedAt: started, FinishedAt: &fin}
		switch {
		case !ok:
			tr.Status = run.TargetFailed
			tr.Message = msg
		default:
			tr.Status = run.TargetSuccess
			tr.Message = "compose up -d 完成"
			// 起成功 != 服务可用:配了健康探测就沿用与文件 / 镜像发布同一门控语义。
			if hc.enabled() {
				if herr := s.runHealthCheck(ctx, sid, hc); herr != nil {
					tr.Status = run.TargetFailed
					tr.Message = herr.Error()
				} else {
					tr.Message = "compose up -d 完成;健康检查通过"
				}
			}
		}
		results = append(results, tr)
	}
	// 与其余部署路径一致:持久化逐机结果供运行详情展示,不置 run 终态(调度器控制)。
	if err := s.saveStageTargets(ctx, runID, results); err != nil {
		return nil, err
	}
	return results, nil
}

// composeOne 在一台机上完成:建目录 → 上传 compose → 探测 compose CLI → up -d。
// 整条链路共用 composeUpTimeout(逐命令的 60s 预算养不起一次拉镜像的 up)。
// 返回 (人读 message, 是否成功);失败 message 不带栈内容,只带目标机给出的原因。
func (s *service) composeOne(ctx context.Context, serverID, project, compose, dir, composePath string) (string, bool) {
	ctx, release, gerr := s.holdServer(ctx, serverID)
	if gerr != nil {
		return "等待目标机空闲时部署被中断:" + humanExecError(gerr), false
	}
	defer release()

	ctx, cancel := context.WithTimeout(ctx, composeUpTimeout)
	defer cancel()

	if msg, ok := s.runStep(ctx, serverID, [][]string{{"mkdir", "-p", dir}}); !ok {
		return "创建受管目录失败:" + msg, false
	}
	// 上传走 SFTP 字节流,不经回显:compose 正文因此绝无成为 shell 注入面的可能。
	if err := s.upload(ctx, serverID, strings.NewReader(compose), composePath); err != nil {
		return "写入 compose 文件失败:" + humanExecError(err), false
	}
	bin := s.detectComposeBin(ctx, serverID)
	if bin == nil {
		return "该主机未检测到 docker compose / docker-compose,无法部署", false
	}
	up := append(append([]string{}, bin...), "-p", project, "-f", composePath, "up", "-d")
	if msg, ok := s.runStep(ctx, serverID, [][]string{up}); !ok {
		return msg, false
	}
	return "", true
}

// detectComposeBin 探测 compose CLI:v2 插件优先,回退 v1 独立命令,都没有返回 nil。
// 探测经 s.exec(而非直连 targets.Exec),所以「这台机根本没装 compose」是会回流进步骤日志的
// 一条命令,而不是日志里凭空消失的一步。
func (s *service) detectComposeBin(ctx context.Context, serverID string) []string {
	if out, err := s.exec(ctx, serverID, []string{"docker", "compose", "version"}); err == nil && out != nil && out.ExitCode == 0 {
		return []string{"docker", "compose"}
	}
	if out, err := s.exec(ctx, serverID, []string{"docker-compose", "version"}); err == nil && out != nil && out.ExitCode == 0 {
		return []string{"docker-compose"}
	}
	return nil
}
