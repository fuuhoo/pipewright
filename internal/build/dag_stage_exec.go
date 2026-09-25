package build

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/huangchengsir/pipewright/internal/dag"
	"github.com/huangchengsir/pipewright/internal/dagrun"
	"github.com/huangchengsir/pipewright/internal/deploy"
	"github.com/huangchengsir/pipewright/internal/notify"
	"github.com/huangchengsir/pipewright/internal/pipeline"
	"github.com/huangchengsir/pipewright/internal/project"
	"github.com/huangchengsir/pipewright/internal/run"
	"github.com/huangchengsir/pipewright/internal/vault"
)

// dag_stage_exec.go 是 DAG 调度器(dagrun)的**真实阶段执行器**(Epic 8 · Story 8-2)。
//
// dagrun.Runner 按阶段 DAG 编排;本文件提供「单个阶段怎么执行」的真实实现:复用 Builder 的
// 容器基建(cloner + driver.RunToolchain)在隔离容器内真实跑 script 类型 job 的命令,而非 stub。
//
// 执行模型(自包含、并行安全):每个含 script job 的阶段**独立克隆一份临时工作区**(即用即销),
// 在其中按 job 声明序逐个执行 script job 的命令(多行 → set -e 单脚本 → 容器内 sh -c)。
// 不同阶段并行执行时各持各的工作区,互不干扰;宿主零落地(RunToolchain = docker run --rm)。
//
// job.Config 取值(对齐前端节点表单的 script 类型字段):image(必填)、commands(多行,必填)、
// workDir(可选,相对工作区根)。非 script/custom 类型的 job 本期不做真实执行(build_image/
// push_image/deploy_ssh 的真实化是后续 increment),仅打一行诚实占位日志后放行——**不冒充**
// 真实构建/部署。
//
// 安全边界(复用 runScriptStep 的铁律):脚本只在隔离容器跑、绝不在宿主执行;命令注入防护
// (宿主侧 array,用户命令仅容器内 sh 解释);ctx 取消即 kill;secret 注入但出网/落库脱敏。
//
// 工作区共享(跨阶段复用同一 clone)是性能优化项,留后续;本期每阶段独立 clone 以求简单与并行安全。

// isScriptJob 判断是否脚本类 job(在隔离容器跑命令 + 收 artifactPath 产物)。
// 类型集合的唯一定义在 pipeline 契约层(#7),这里只转发,不再复述。
func isScriptJob(jobType string) bool { return pipeline.IsScriptJobType(jobType) }

// tplPlaceholder 匹配 {{key}} 占位(key 为标识符)。自定义节点参数渲染用。
var tplPlaceholder = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}`)

// templateContext 收集自定义节点的渲染上下文:config 里的字符串值 + 自由「参数表」(params 字段,
// 每行 `key=value` 或 `key: value`)。参数完全自由(用户随便定义键),不锁死 schema。
func templateContext(cfg map[string]any) map[string]string {
	ctx := make(map[string]string, len(cfg)+4)
	for k, v := range cfg {
		if s, ok := v.(string); ok {
			ctx[k] = s
		}
	}
	for _, line := range splitCommands(cfgString(cfg, "params")) {
		i := strings.IndexAny(line, "=:")
		if i <= 0 {
			continue
		}
		if k := strings.TrimSpace(line[:i]); k != "" {
			ctx[k] = strings.TrimSpace(line[i+1:])
		}
	}
	return ctx
}

// renderTemplate 把 {{key}} 替换为 ctx[key]。未知占位原样保留(不报错;$ENV 交给容器内 shell;
// 无 {{ 则零开销直返)。ctx 由 templateContext 收集(config 字段 + params 自由参数)。
func renderTemplate(tpl string, ctx map[string]string) string {
	if tpl == "" || !strings.Contains(tpl, "{{") {
		return tpl
	}
	return tplPlaceholder.ReplaceAllStringFunc(tpl, func(m string) string {
		key := tplPlaceholder.FindStringSubmatch(m)[1]
		if v, ok := ctx[key]; ok {
			return v
		}
		return m
	})
}

// isDeployJob 判断是否「SSH 部署」类节点(deploy_ssh 通用 / deploy_frontend 前端部署模板)。
// 类型集合的唯一定义在 pipeline 契约层,这里只转发。
func isDeployJob(jobType string) bool { return pipeline.IsDeployJobType(jobType) }

// isBuildImageJob 判断是否「构建产物(镜像/JAR/dist)」节点(画布 build_image 类型)。
func isBuildImageJob(jobType string) bool {
	return strings.TrimSpace(jobType) == "build_image"
}

// effectiveJobType 把合并后的「构建」任务按产物档位折算成真正执行它的类型
// (镜像 → build_image 路径,产物 → script 路径)。派发处只认折算结果。
func effectiveJobType(jb pipeline.Job) string {
	return pipeline.EffectiveJobType(jb.Type, jb.Config)
}

// NewStageExecutor 返回一个复用 Builder 容器基建的真实 dagrun.StageExecutor。
// 仅对 script/custom 类型 job 做真实容器执行;其余类型打占位日志放行。
// reportSink 可选(nil = 不持久化测试报告,但质量门禁阻断仍生效;Story 8-6 / FR-8-6)。
func NewStageExecutor(b *Builder, reportSink TestReportSink) dagrun.StageExecutor {
	return func(ctx context.Context, r *run.Run, stage pipeline.Stage, rep dagrun.StageReporter) error {
		scriptJobs := make([]pipeline.Job, 0, len(stage.Jobs))
		buildImageJobs := make([]pipeline.Job, 0, len(stage.Jobs))
		deployJobs := make([]pipeline.Job, 0, len(stage.Jobs))
		notifyJobs := make([]pipeline.Job, 0, len(stage.Jobs))
		hasPushJob := false
		for _, jb := range stage.Jobs {
			et := effectiveJobType(jb)
			switch {
			case isScriptJob(et):
				scriptJobs = append(scriptJobs, jb)
			case isBuildImageJob(et):
				buildImageJobs = append(buildImageJobs, jb)
			case strings.TrimSpace(jb.Type) == "push_image":
				hasPushJob = true
			case isDeployJob(jb.Type):
				deployJobs = append(deployJobs, jb)
			case strings.TrimSpace(jb.Type) == "notify":
				notifyJobs = append(notifyJobs, jb)
			}
		}

		needsBuild := len(scriptJobs) > 0 || len(buildImageJobs) > 0
		// 撤销的类型绝不因「整阶段没有可执行节点」而被兜成占位放行:静默绿等于冒充门禁生效。
		for _, jb := range stage.Jobs {
			if guidance, retired := pipeline.RetiredJobType(jb.Type); retired {
				_ = rep.JobRunning(ctx, jb.ID)
				jr := rep.JobReporter(jb.ID)
				_ = jr.Log(ctx, streamStderr, fmt.Sprintf("节点「%s」类型 %s 已撤销。%s", jb.Name, jb.Type, guidance))
				_ = rep.JobDone(ctx, jb.ID, run.StepFailed)
				return ErrBuildFailed
			}
		}
		// 没有任何可执行节点(script/build_image/deploy_ssh/notify)且无 post → 诚实占位放行。
		if !needsBuild && len(deployJobs) == 0 && len(notifyJobs) == 0 && len(stage.Post) == 0 {
			// git_source 卡片要说实话:绑了仓库才报分支/提交,没绑就直说「无源可拉」。
			// 真值取自项目(节点 config 里的 repoUrl 只是保存期填的镜像,可能滞后)。
			var srcProj *project.Project
			for _, jb := range stage.Jobs {
				if strings.TrimSpace(jb.Type) == "git_source" {
					// 真值取自项目(节点 config 里的 repoUrl 只是保存期填的镜像,可能滞后)。
					if p, _, perr := b.resolve(ctx, r); perr == nil {
						srcProj = p
					}
					break
				}
			}
			for _, jb := range stage.Jobs {
				_ = rep.JobRunning(ctx, jb.ID)
				jr := rep.JobReporter(jb.ID)
				if strings.TrimSpace(jb.Type) == "git_source" {
					for _, line := range gitSourceLogLines(jb, r, srcProj) {
						_ = jr.Log(ctx, streamStdout, line)
					}
				} else {
					_ = jr.Log(ctx, streamStdout, fmt.Sprintf("· %s(%s)— 真实执行未接入;本阶段放行", jb.Name, jb.Type))
				}
				_ = rep.JobDone(ctx, jb.ID, run.StepSuccess)
			}
			if len(stage.Jobs) == 0 {
				_ = rep.Log(ctx, streamStdout, fmt.Sprintf("阶段「%s」无 job", stage.Name))
			}
			return nil
		}

		sink := &reporterSink{rep: rep}
		hasPost := len(stage.Post) > 0
		// 阶段内 job 级 DAG:多 job(且都带 ID)或任一 job 声明 needs → 按 job 级 DAG 并发调度
		// (无依赖 job 并行,对齐画布「并行节点(无 needs)」语义;有 needs 的 job 等其全部上游成功后
		// 再跑;各 job 独立克隆工作区以保并发安全)。单 job 阶段沿用既有路径(零额外克隆)。
		// 要求都带 ID:DAG 需唯一节点 ID 建图(生产侧保存流水线时 job 必有 ID);无 ID(测试/异常数据)
		// 退回既有类型分组路径以防建图失败。
		// 注:无 needs 的同阶段 job 现按「并行」跑(各自独立工作区);若需「串行 + 共享 env/产物」,应给
		// 下游 job 显式声明 needs(画布「串行节点」),与 UI 的串/并语义一致。
		hasJobDAG := stageHasJobNeeds(stage.Jobs) || (len(stage.Jobs) > 1 && allJobsHaveIDs(stage.Jobs))

		// 工作区:
		//  - 非 DAG 路径:有 script/build_image **或** 有 post 步骤时克隆一份阶段工作区(沿用既有语义)。
		//  - DAG 路径:job 各自克隆独立工作区,阶段级工作区仅 post 步骤(在其中跑)才需要。
		// 纯部署/通知阶段无工作区。proj/settings 在 needsBuild||hasPost||部署节点要读仓库文件 时解析
		// (两路径都可能用到)。最后一项是必须的:部署阶段常常只有 deploy 节点(needsBuild=false),
		// 而 compose/清单 的「引用仓库文件」那条路要拿 proj.RepoURL 去读 —— 不解析就是必然失败。
		var (
			proj      *project.Project
			settings  *pipeline.Settings
			workspace string
			commitTag = "latest"
		)
		if needsBuild || hasPost || deployJobsReadRepoFile(deployJobs) {
			p, s, perr := b.resolve(ctx, r)
			if perr != nil {
				_ = rep.Log(ctx, streamStderr, "无法加载项目构建配置:"+perr.Error())
				return ErrBuildFailed
			}
			proj, settings = p, s
		}
		needStageWS := hasPost || (needsBuild && !hasJobDAG)
		if needStageWS {
			ws, mkErr := mkTempWorkspace()
			if mkErr != nil {
				_ = rep.Log(ctx, streamStderr, "创建临时工作区失败:"+mkErr.Error())
				return ErrBuildFailed
			}
			workspace = ws
			defer func() { _ = os.RemoveAll(workspace) }() // 宿主零污染

			if err := workspaceNeedsRepo(proj, len(buildImageJobs) > 0); err != nil {
				_ = rep.Log(ctx, streamStderr, err.Error())
				return ErrBuildFailed
			}
			tag, werr := b.fillWorkspace(ctx, r, proj, workspace, rep)
			if werr != nil {
				return werr
			}
			commitTag = tag
			// 跨阶段产物传递:把上游阶段已归档的 jar/dist 真字节恢复回本阶段新工作区的原相对路径,
			// 使「构建/打包/部署」拆成独立串行阶段时,下游(如 build_image)仍能拿到上游产物。
			// best-effort(首阶段无上游产物即 no-op;失败仅记日志,不阻断)。
			b.restorePriorArtifacts(ctx, r, workspace, rep)
		}

		// 阶段主体(job 执行):捕获错误而非提前返回,以便其后无论成败都按条件跑 post 步骤。
		jobErr := func() error {
			// 旁挂服务(P1):阶段声明 services 时,起服务容器到临时网络,脚本容器加入同网按服务名互访;
			// 阶段结束(成败/取消)拆除。驱动不支持容器网络能力 → 直接失败(不在缺依赖下假跑)。
			// 两条执行路径(DAG / 类型分组)共用,故提到分支之前。
			var svcNetwork string
			if needsBuild && len(stage.Services) > 0 {
				net, ok := b.startStageServices(ctx, r, stage, rep)
				if !ok {
					return ErrBuildFailed
				}
				svcNetwork = net
				defer b.stopStageServices(context.WithoutCancel(ctx), r, stage, net, rep)
			}

			// ── 阶段内 job 级 DAG 路径:按 job 依赖并发调度,无依赖 job 并行、各自独立工作区 ──
			if hasJobDAG {
				return b.runStageJobsDAG(ctx, r, stage, rep, sink, proj, settings, svcNetwork, hasPushJob, reportSink)
			}

			// ── 既有路径(零行为变化):类型分组串行,共用单一阶段工作区 ──
			if needsBuild {
				// 步骤输出→下游变量(P1):同阶段 job 间经 $PIPEWRIGHT_ENV 文件传值,carriedEnv 累积注入后续 job。
				var carriedEnv []pipeline.BuildVar
				for _, jb := range scriptJobs {
					if canceled(ctx) {
						return run.ErrCanceled
					}
					_ = rep.JobRunning(ctx, jb.ID)
					jrep := rep.JobReporter(jb.ID)
					jsink := &reporterSink{rep: jrep}
					step, verr := b.scriptStepFromJob(jb, stage.Name)
					if verr != nil {
						_ = jrep.Log(ctx, streamStderr, fmt.Sprintf("script job「%s」配置无效:%v", jb.Name, verr))
						_ = rep.JobDone(ctx, jb.ID, run.StepFailed)
						return ErrBuildFailed
					}
					// 注入顺序:运行参数 → 上游 job 输出(carriedEnv)→ 流水线级变量(「变量与缓存」,
					// 含 secret,vault 即取即用)→ job 自身 env(后者覆盖同名)→ PIPEWRIGHT_ENV(系统,末位防覆盖)。
					base := append(runParamsAsEnv(r.Trigger.Params), carriedEnv...)
					if settings != nil && len(settings.Build.Vars) > 0 {
						base = append(base, settings.Build.Vars...)
					}
					step.Env = append(base, step.Env...)
					step.Env = append(step.Env, pipewrightEnvVar())
					// 旁挂服务网络:脚本容器加入,按服务名互访。
					if svcNetwork != "" {
						step.Resource.Network = svcNetwork
					}
					// 构建依赖缓存(#61):执行前恢复(暖构建)、成功后保存(best-effort,缓存问题绝不让构建失败)。
					// 任务级 timeout/retry(#63):零值时 runScriptStepWithOpts 退化为单次无超时执行(旧行为)。
					b.restoreJobCache(ctx, jrep, jb, r.Trigger.Branch, workspace)
					if err := b.runScriptStepWithOpts(ctx, jsink, 0, step, workspace); err != nil {
						_ = rep.JobDone(ctx, jb.ID, run.StepFailed)
						return err // ErrBuildFailed / run.ErrCanceled
					}
					b.saveJobCache(ctx, jrep, jb, r.Trigger.Branch, workspace)
					// 捕获本 job 写入 $PIPEWRIGHT_ENV 的变量,供后续同阶段 job 引用。
					carriedEnv = append(carriedEnv, captureStageEnv(ctx, jrep, workspace)...)
					_ = rep.JobDone(ctx, jb.ID, run.StepSuccess)
				}

				for _, jb := range buildImageJobs {
					if canceled(ctx) {
						return run.ErrCanceled
					}
					_ = rep.JobRunning(ctx, jb.ID)
					jrep := rep.JobReporter(jb.ID)
					jsink := &reporterSink{rep: jrep}
					if err := b.runBuildImageJob(ctx, jsink, jrep, jb, stage.Name, proj, settings, r.Trigger.ResolvedEnvironment, workspace, commitTag, hasPushJob); err != nil {
						_ = rep.JobDone(ctx, jb.ID, run.StepFailed)
						return err
					}
					_ = rep.JobDone(ctx, jb.ID, run.StepSuccess)
				}

				b.collectScriptArtifacts(ctx, scriptJobs, workspace, slugify(proj.Name), stage.Name, rep)
				if err := collectStageReport(ctx, reportSink, r, stage, workspace, rep); err != nil {
					return err
				}
			}

			// ── 部署节点(deploy_ssh):把本 run 已产出的产物经 SSH 部署到目标机(中途部署,不动 run 终态)──
			for _, jb := range deployJobs {
				if canceled(ctx) {
					return run.ErrCanceled
				}
				_ = rep.JobRunning(ctx, jb.ID)
				jrep := rep.JobReporter(jb.ID)
				if err := b.runDeployJob(ctx, jrep, jb, r, proj, settings); err != nil {
					_ = rep.JobDone(ctx, jb.ID, run.StepFailed)
					return err
				}
				_ = rep.JobDone(ctx, jb.ID, run.StepSuccess)
			}

			// ── 通知节点(notify):按节点配的渠道发通知(best-effort,不因通知失败而失败本阶段)──
			for _, jb := range notifyJobs {
				if canceled(ctx) {
					return run.ErrCanceled
				}
				_ = rep.JobRunning(ctx, jb.ID)
				b.runNotifyJob(ctx, rep.JobReporter(jb.ID), jb, r)
				_ = rep.JobDone(ctx, jb.ID, run.StepSuccess)
			}

			return nil
		}()

		// 阶段后置步骤(P1 · 对标 Jenkins post):无论 job 成败,按 condition 在同工作区跑清理/通知/归档
		// (best-effort,post 失败只记日志、不改阶段结果)。用 WithoutCancel,使取消/失败后清理仍能跑。
		if hasPost && workspace != "" {
			b.runStagePost(context.WithoutCancel(ctx), sink, r, stage, workspace, jobErr != nil, rep)
		}
		return jobErr
	}
}

// defaultJobDAGConcurrency 是「阶段内 job 级 DAG」的并发上限。阶段之间已可能并行,故 job 级再
// 限一个较小上限,避免并发容器数无界放大拉爆宿主(0 = 由 dag 调度取节点数;这里给确定上限)。
const defaultJobDAGConcurrency = 4

// stageHasJobNeeds 报告阶段内是否有任一 job 声明了 job 级依赖(needs)。
// 有 → 走 job 级 DAG 并发调度;无 → 走既有类型分组串行(向后兼容)。
func stageHasJobNeeds(jobs []pipeline.Job) bool {
	for _, jb := range jobs {
		if len(jb.Needs) > 0 {
			return true
		}
	}
	return false
}

// allJobsHaveIDs 报告阶段内每个 job 都有非空 ID。job 级 DAG 调度需唯一节点 ID 建图;生产侧保存
// 流水线时 job 必有 ID,无 ID(仅测试/异常数据)时调用方退回既有类型分组路径以防 dag 建图失败。
func allJobsHaveIDs(jobs []pipeline.Job) bool {
	for _, jb := range jobs {
		if strings.TrimSpace(jb.ID) == "" {
			return false
		}
	}
	return true
}

// runStageJobsDAG 按「阶段内 job 级 DAG」并发执行该阶段的所有 job:无依赖的 job 并行跑、有 needs
// 的 job 等其全部上游成功后再跑(复用 dag.Graph.Schedule,与阶段级调度同一内核)。每个需工作区的
// job(script/build_image)各自克隆一份独立临时工作区以保并发安全;env 沿 needs 边传递(上游 job
// 写 $PIPEWRIGHT_ENV → 下游合并注入)。任一 job 有效失败/取消 → 阶段失败。
//
// 并发安全:工作区各 job 独立(Clone 本就被阶段级并行调用,已并发安全);日志经 sink 的运行级
// mutex 串行化;jobEnvOut 用 mutex 保护;buildcache/产物按 job 隔离键写入。
func (b *Builder) runStageJobsDAG(
	ctx context.Context,
	r *run.Run,
	stage pipeline.Stage,
	rep dagrun.StageReporter,
	sink *reporterSink,
	proj *project.Project,
	settings *pipeline.Settings,
	svcNetwork string,
	hasPushJob bool,
	reportSink TestReportSink,
) error {
	nodes := make([]dag.Node, 0, len(stage.Jobs))
	jobByID := make(map[string]pipeline.Job, len(stage.Jobs))
	for _, jb := range stage.Jobs {
		nodes = append(nodes, dag.Node{ID: jb.ID, Needs: jb.Needs})
		jobByID[jb.ID] = jb
	}
	g, gerr := dag.New(nodes)
	if gerr != nil {
		// 保存期已校验过;这里再失败属防御(数据异常)。
		_ = rep.Log(ctx, streamStderr, "阶段内 job 依赖图非法:"+gerr.Error())
		return ErrBuildFailed
	}

	var envMu sync.Mutex
	jobEnvOut := make(map[string][]pipeline.BuildVar, len(stage.Jobs)) // jobID → 该 job 产出的 env(供下游合并)

	runJob := func(ctx context.Context, id string) error {
		if canceled(ctx) {
			return run.ErrCanceled
		}
		jb := jobByID[id]
		// 收集上游(needs)产出的 env,按 needs 声明序合并(后者覆盖同名,与既有 carriedEnv 语义一致)。
		var upstreamEnv []pipeline.BuildVar
		if len(jb.Needs) > 0 {
			envMu.Lock()
			for _, dep := range jb.Needs {
				upstreamEnv = append(upstreamEnv, jobEnvOut[dep]...)
			}
			envMu.Unlock()
		}

		// 节点级 step:本 job 独占一个 step,日志/产物经 job 级 rep/sink 归到该节点 ordinal。
		_ = rep.JobRunning(ctx, jb.ID)
		jrep := rep.JobReporter(jb.ID)
		jsink := &reporterSink{rep: jrep}

		jobErr := func() error {
			et := effectiveJobType(jb)
			switch {
			case isScriptJob(et):
				out, err := b.runScriptJobIsolated(ctx, jsink, jrep, r, jb, stage, proj, settings, svcNetwork, upstreamEnv, reportSink)
				if err != nil {
					return err
				}
				if len(out) > 0 {
					envMu.Lock()
					jobEnvOut[id] = out
					envMu.Unlock()
				}
				return nil
			case isBuildImageJob(et):
				return b.runBuildImageJobIsolated(ctx, jsink, jrep, r, jb, stage, proj, settings, hasPushJob)
			case isDeployJob(jb.Type):
				return b.runDeployJob(ctx, jrep, jb, r, proj, settings)
			case strings.TrimSpace(jb.Type) == "push_image":
				// 推送由构建任务自己完成(见「构建后推送」开关 + 环境是否绑定镜像仓);本节点只做编排顺序。
				_ = jrep.Log(ctx, streamStdout, fmt.Sprintf("· 推送镜像「%s」:推送在构建任务里完成,本节点仅用于编排顺序", jb.Name))
				return nil
			case strings.TrimSpace(jb.Type) == "notify":
				b.runNotifyJob(ctx, jrep, jb, r)
				return nil
			default:
				if guidance, retired := pipeline.RetiredJobType(jb.Type); retired {
					// 撤销的类型绝不放行:静默绿会让用户以为门禁在生效(health_check 曾有的行为)。
					_ = jrep.Log(ctx, streamStderr, fmt.Sprintf("节点「%s」类型 %s 已撤销。%s", jb.Name, jb.Type, guidance))
					return ErrBuildFailed
				}
				_ = jrep.Log(ctx, streamStdout, fmt.Sprintf("· %s(%s)— 真实执行未接入;本节点放行", jb.Name, jb.Type))
				return nil
			}
		}()

		status := run.StepSuccess
		if jobErr != nil {
			status = run.StepFailed
		}
		_ = rep.JobDone(ctx, jb.ID, status)
		return jobErr
	}

	res := g.Schedule(ctx, runJob, dag.Options{MaxConcurrency: defaultJobDAGConcurrency})

	// 因上游失败/取消而从未执行的 job:显式标 skipped(其 runJob 未被调到,JobDone 未触发),
	// 使其在运行详情里如实显示「跳过」而非被阶段失败兜底成 failed。
	for _, jb := range stage.Jobs {
		switch res[jb.ID].Status {
		case dag.StatusSkipped, dag.StatusCanceled:
			_ = rep.JobDone(ctx, jb.ID, run.StepSkipped)
		}
	}

	if canceled(ctx) {
		return run.ErrCanceled
	}
	// 汇总:任一 job 失败(取消优先识别)→ 阶段失败。skipped 仅因上游失败,已由该上游失败反映。
	for _, jb := range stage.Jobs {
		nr := res[jb.ID]
		switch nr.Status {
		case dag.StatusFailed:
			if errors.Is(nr.Err, run.ErrCanceled) {
				return run.ErrCanceled
			}
			return ErrBuildFailed
		case dag.StatusCanceled:
			return run.ErrCanceled
		}
	}
	return nil
}

// cloneJobWorkspace 为单个 job 备一份独立的临时工作区(并发安全),并恢复本 run 已归档的上游产物。
// needsSource=false 时(纯 script 任务)未绑仓库的项目不拉源码,工作区从空目录开始。
// 返回 (workspace, commitTag, cleanup, err);调用方务必在用完后调用 cleanup()。失败时已自行清理。
func (b *Builder) cloneJobWorkspace(ctx context.Context, r *run.Run, proj *project.Project, rep dagrun.StageReporter, needsSource bool) (string, string, func(), error) {
	if err := workspaceNeedsRepo(proj, needsSource); err != nil {
		_ = rep.Log(ctx, streamStderr, err.Error())
		return "", "", func() {}, ErrBuildFailed
	}
	ws, mkErr := mkTempWorkspace()
	if mkErr != nil {
		_ = rep.Log(ctx, streamStderr, "创建临时工作区失败:"+mkErr.Error())
		return "", "", func() {}, ErrBuildFailed
	}
	cleanup := func() { _ = os.RemoveAll(ws) }

	commitTag, werr := b.fillWorkspace(ctx, r, proj, ws, rep)
	if werr != nil {
		cleanup()
		return "", "", func() {}, werr
	}
	// 跨阶段 + 阶段内上游 job 产物:把本 run 已归档的 jar/dist 真字节恢复进本 job 工作区原相对路径。
	b.restorePriorArtifacts(ctx, r, ws, rep)
	return ws, commitTag, cleanup, nil
}

// runScriptJobIsolated 在独立工作区内执行单个 script 类 job(DAG 路径用)。返回该 job 写入
// $PIPEWRIGHT_ENV 的变量(供下游 needs 合并)。env 注入序与既有串行路径一致:
// 运行参数 → 上游 job 输出 → job 自身 env → PIPEWRIGHT_ENV。产物 + 测试报告逐 job 收集/门禁。
func (b *Builder) runScriptJobIsolated(
	ctx context.Context,
	sink *reporterSink,
	rep dagrun.StageReporter,
	r *run.Run,
	jb pipeline.Job,
	stage pipeline.Stage,
	proj *project.Project,
	settings *pipeline.Settings,
	svcNetwork string,
	upstreamEnv []pipeline.BuildVar,
	reportSink TestReportSink,
) ([]pipeline.BuildVar, error) {
	ws, _, cleanup, err := b.cloneJobWorkspace(ctx, r, proj, rep, false)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	step, verr := b.scriptStepFromJob(jb, stage.Name)
	if verr != nil {
		_ = rep.Log(ctx, streamStderr, fmt.Sprintf("script job「%s」配置无效:%v", jb.Name, verr))
		return nil, ErrBuildFailed
	}
	// 注入顺序:运行参数 → 上游 job 输出 → 流水线级变量(「变量与缓存」,含 secret,vault 即取即用)
	// → job 自身 env(后者覆盖同名)。此前流水线级变量只注入镜像构建路径、不到 script 步骤(漏),
	// 导致脚本节点拿不到「变量与缓存」里配的 secret(如发版的 GITHUB_TOKEN)。本修复补齐。
	base := append(runParamsAsEnv(r.Trigger.Params), upstreamEnv...)
	if settings != nil && len(settings.Build.Vars) > 0 {
		base = append(base, settings.Build.Vars...)
	}
	step.Env = append(base, step.Env...)
	step.Env = append(step.Env, pipewrightEnvVar())
	if svcNetwork != "" {
		step.Resource.Network = svcNetwork
	}
	b.restoreJobCache(ctx, rep, jb, r.Trigger.Branch, ws)
	if err := b.runScriptStepWithOpts(ctx, sink, 0, step, ws); err != nil {
		return nil, err // ErrBuildFailed / run.ErrCanceled
	}
	b.saveJobCache(ctx, rep, jb, r.Trigger.Branch, ws)
	out := captureStageEnv(ctx, rep, ws)
	b.collectScriptArtifacts(ctx, []pipeline.Job{jb}, ws, slugify(proj.Name), stage.Name, rep)
	if rerr := collectStageReport(ctx, reportSink, r, stage, ws, rep); rerr != nil {
		return out, rerr // 质量门禁阻断
	}
	return out, nil
}

// runBuildImageJobIsolated 在独立工作区内执行单个 build_image job(DAG 路径用):先克隆 + 恢复上游
// 产物(jar/dist),再复用既有 runBuildImageJob 真实构建/推送。
func (b *Builder) runBuildImageJobIsolated(
	ctx context.Context,
	sink *reporterSink,
	rep dagrun.StageReporter,
	r *run.Run,
	jb pipeline.Job,
	stage pipeline.Stage,
	proj *project.Project,
	settings *pipeline.Settings,
	hasPushJob bool,
) error {
	ws, commitTag, cleanup, err := b.cloneJobWorkspace(ctx, r, proj, rep, true)
	if err != nil {
		return err
	}
	defer cleanup()
	return b.runBuildImageJob(ctx, sink, rep, jb, stage.Name, proj, settings, r.Trigger.ResolvedEnvironment, ws, commitTag, hasPushJob)
}

// runDeployJob 执行一个部署节点(deploy_ssh / deploy_docker / deploy_k8s):把本 run 已产出的产物
// (或节点自带的 compose / k8s 清单正文)发到节点配置的目标(经 SSH 到机器,或直连集群 API server;
// 复用 deploy.Service.DeployForStage 中途部署,不动 run 终态)。任一目标失败 → 阶段失败、阻断下游。
func (b *Builder) runDeployJob(ctx context.Context, rep dagrun.StageReporter, jb pipeline.Job, r *run.Run, proj *project.Project, settings *pipeline.Settings) error {
	runID := r.ID
	params := deployTemplateVars(r.Trigger.Params, settings)
	if b.deployer == nil {
		_ = rep.Log(ctx, streamStdout, "· 部署节点:部署服务未注入,跳过")
		return nil
	}
	// 落点二选一:集群 ID(k8s 发布)或服务器 ID(SSH / docker)。两条腿的落点表不同,
	// 混着填在保存期就被 validateDeployK8s 拒掉了,这里只可能有一个非空。
	clusterID := cfgString(jb.Config, pipeline.ConfigKeyClusterID)
	serverID := cfgString(jb.Config, "serverId")
	targetID, targetKind := serverID, "服务器"
	if clusterID != "" {
		targetID, targetKind = clusterID, "集群"
	}
	if targetID == "" {
		_ = rep.Log(ctx, streamStderr, fmt.Sprintf("部署节点「%s」未选目标%s", jb.Name, targetKind))
		return ErrBuildFailed
	}
	cfg := map[string]string{}
	// deployPath / restartCommand 支持 {{param}} 占位:用本次运行参数渲染(命令型部署据此让
	// 「本地端口 / 穿透端口」等随运行参数变化;非配置类部署不写占位 → renderTemplate 零开销直返)。
	if dp := renderTemplate(cfgString(jb.Config, "deployPath"), params); dp != "" {
		cfg["deployPath"] = dp
	}
	if rc := renderTemplate(cfgString(jb.Config, "restartCommand"), params); rc != "" {
		cfg["restartCommand"] = rc
	}
	// 镜像产物部署参数(#51)透传:deploy.DeployForStage 经这些键挑镜像产物并组装
	// `docker run`(artifactType=image 选镜像;containerName/ports/runArgs 驱动容器名与端口/运行参数)。
	// artifactFrom = 产物来源任务 ID:并行构建出多件同类产物时,靠它锁定部署哪一件。
	// dockerMode/stackName/composeYaml 是「docker 部署」节点的三件套(单容器 / compose 两种方式)。
	// 各值原样搬运(deploy 层 array 化、绝不拼 shell,守 AC-SEC-02);空值不入 cfg 保持默认。
	for _, k := range []string{
		"artifactType", pipeline.ConfigKeyArtifactFrom, "containerName", "ports", "runArgs",
		pipeline.ConfigKeyDockerMode, pipeline.ConfigKeyStackName,
		pipeline.ConfigKeyClusterID, pipeline.ConfigKeyWorkloadKind,
		pipeline.ConfigKeyRolloutTimeout, pipeline.ConfigKeyAutoRollback,
		pipeline.ConfigKeyManifestSource,
	} {
		if v := cfgString(jb.Config, k); v != "" {
			cfg[k] = v
		}
	}
	// 命名空间与负载名支持 {{param}}:同一套流水线按运行参数发到不同命名空间(如按分支隔离)
	// 是常规用法;集群 ID 是 uuid,渲染它没有意义。
	for _, k := range []string{pipeline.ConfigKeyNamespace, pipeline.ConfigKeyWorkloadName} {
		if v := renderTemplate(cfgString(jb.Config, k), params); v != "" {
			cfg[k] = v
		}
	}
	// compose 正文在这一步收敛成「一份正文」交下去(deploy 层只认 composeYaml):
	// 粘贴的按运行参数渲染 —— 同一份栈按参数换端口是一条流水线的常见用法;
	// 引用仓库文件的现读现用,且**不渲染** —— 那份文件是仓库的资产,不是模板,
	// 拿它当模板替换会把作者写的 ${...}/{{...}} 悄悄吃掉。
	// 项目名**两条路都不渲染** —— 它是栈的身份,渲染出第二个名字等于机器上留下两个栈。
	if composeSourceIsRepo(jb) {
		path := composeRepoPath(jb)
		body, rerr := b.readRepoYAML(ctx, jb, path, r, proj, "compose")
		if rerr != nil {
			_ = rep.Log(ctx, streamStderr, rerr.Error())
			return ErrBuildFailed
		}
		cfg[pipeline.ConfigKeyComposeYaml] = body
		// 仓库路径也透下去只为一个目的:部署日志写明这份正文从哪来,排查「改了没生效」时
		// 不用猜目标机上那份是谁铺的。粘贴档不写这个键 —— 写了就是假线索。
		cfg[deploy.CfgKeyComposeFile] = path
		_ = rep.Log(ctx, streamStdout, fmt.Sprintf("· compose 正文取自仓库文件 %s(%d 字节)", path, len(body)))
	} else if cy := renderTemplate(cfgString(jb.Config, pipeline.ConfigKeyComposeYaml), params); cy != "" {
		cfg[pipeline.ConfigKeyComposeYaml] = cy
	}
	// K8s 清单同样在这里收敛成「一份正文」交下去(deploy 层只认 manifestYaml)。
	// 与 compose 的差别是**两条路都渲染**:仓库里那份 k8s.yaml 本就是流水线模板 —— 它必须靠
	// {{IMAGE}} 拿到本次构建的镜像,而 deploy 层之前没人知道镜像是什么。真留着没替换的变量
	// 也不会静默发出去:kube 层解析时拒绝任何残留占位符(见 internal/kube/apply.go)。
	if manifestIsApplied(jb) {
		body := cfgString(jb.Config, pipeline.ConfigKeyManifestYaml)
		if cfgString(jb.Config, pipeline.ConfigKeyManifestSource) == pipeline.ManifestSourceRepo {
			path := strings.TrimSpace(cfgString(jb.Config, pipeline.ConfigKeyManifestFile))
			text, rerr := b.readRepoYAML(ctx, jb, path, r, proj, "清单")
			if rerr != nil {
				_ = rep.Log(ctx, streamStderr, rerr.Error())
				return ErrBuildFailed
			}
			body = text
			// 仓库路径透下去只为一句日志:排查「改了没生效」时不必猜集群里那份是谁铺的。
			cfg[deploy.CfgKeyManifestFile] = path
			_ = rep.Log(ctx, streamStdout, fmt.Sprintf("· 清单正文取自仓库文件 %s(%d 字节)", path, len(text)))
		}
		rendered := renderTemplate(body, params)
		if names := secretVarNames(rendered, settings); names != "" {
			// 变量表里刻意剔除了 secret(正文会入库,见 deployTemplateVars)。这类写法必须
			// 停在执行之前说清楚 —— 否则用户看到的是「变量没给全」,而他明明配了。
			_ = rep.Log(ctx, streamStderr, fmt.Sprintf("部署节点「%s」的清单引用了加密变量 %s:清单正文会随发布结果入库,"+
				"不能把机密抄进去。请改用非加密变量,或让镜像 / 集群自己取凭据(Secret 名、服务账号)", jb.Name, names))
			return ErrBuildFailed
		}
		cfg[pipeline.ConfigKeyManifestYaml] = rendered
	}
	// 部署后健康探测(原「健康检查」节点的能力,现并入部署任务):deploy 层在同一条 exec
	// 链路上逐机探测,不通 → 该机 failed(阻断下游)。缺参数当场判失败,不留「静默不探测」的假绿。
	// job.Config 键与 deploy cfg 键逐字相同,读 pipeline.*、写 deploy.* 让边界清楚。
	probe := cfgString(jb.Config, pipeline.ConfigKeyHealthProbe)
	if probe != "" && probe != pipeline.HealthProbeNone {
		cfg[deploy.CfgKeyHealthProbe] = probe
		switch probe {
		case pipeline.HealthProbeHTTP:
			url := renderTemplate(cfgString(jb.Config, pipeline.ConfigKeyHealthURL), params)
			if strings.TrimSpace(url) == "" {
				_ = rep.Log(ctx, streamStderr, fmt.Sprintf("部署节点「%s」探测方式 http 但未填探测 URL", jb.Name))
				return ErrBuildFailed
			}
			cfg[deploy.CfgKeyHealthURL] = url
		case pipeline.HealthProbeCommand:
			command := renderTemplate(cfgString(jb.Config, pipeline.ConfigKeyHealthCommand), params)
			if strings.TrimSpace(command) == "" {
				_ = rep.Log(ctx, streamStderr, fmt.Sprintf("部署节点「%s」探测方式为命令但未填探测命令", jb.Name))
				return ErrBuildFailed
			}
			cfg[deploy.CfgKeyHealthCommand] = command
		default:
			_ = rep.Log(ctx, streamStderr, fmt.Sprintf("部署节点「%s」探测方式 %q 非法(仅支持 http / command)", jb.Name, probe))
			return ErrBuildFailed
		}
		for _, pair := range []struct{ from, to string }{
			{pipeline.ConfigKeyHealthRetries, deploy.CfgKeyHealthRetries},
			{pipeline.ConfigKeyHealthInterval, deploy.CfgKeyHealthInterval},
			{pipeline.ConfigKeyHealthTimeout, deploy.CfgKeyHealthTimeout},
		} {
			if v := cfgString(jb.Config, pair.from); v != "" {
				cfg[pair.to] = v
			}
		}
		_ = rep.Log(ctx, streamStdout, fmt.Sprintf("· 部署后将探测健康(%s),探测不通则该节点失败", probe))
	}
	strategy := cfgString(jb.Config, "strategy")
	stratLabel := strategy
	if stratLabel == "" {
		stratLabel = "rolling(默认)"
	}
	// 日志首行说清走的是哪条链路:docker 两种方式在目标机上做的事完全不同,
	// 都写成「SSH 部署本次产物」会让人以为 compose 节点也在发构建产物。
	switch what := cfgString(jb.Config, pipeline.ConfigKeyDockerMode); what {
	case pipeline.DockerModeCompose:
		_ = rep.Log(ctx, streamStdout, fmt.Sprintf("→ Compose 部署到服务器 %s(整份 YAML 交目标机 docker compose)…", serverID))
	case pipeline.DockerModeRun:
		_ = rep.Log(ctx, streamStdout, fmt.Sprintf("→ Docker 部署本次镜像到服务器 %s(停旧起新,策略 %s)…", serverID, stratLabel))
	default:
		if clusterID != "" {
			if manifestIsApplied(jb) {
				// 清单那条腿发出去的是「这一份声明」,不是某一个负载 —— 别按负载名报一个集群里
				// 可能并不存在的目标。
				_ = rep.Log(ctx, streamStdout, fmt.Sprintf("→ K8s 应用清单到集群 %s(按清单建/收敛对象,滚完才算成功)…", clusterID))
				break
			}
			_ = rep.Log(ctx, streamStdout, fmt.Sprintf("→ K8s 发布本次镜像到集群 %s 的 %s/%s(换镜像后等集群滚完)…",
				clusterID, cfgString(jb.Config, pipeline.ConfigKeyNamespace), cfgString(jb.Config, pipeline.ConfigKeyWorkloadName)))
			break
		}
		_ = rep.Log(ctx, streamStdout, fmt.Sprintf("→ SSH 部署本次产物到服务器 %s(策略 %s)…", serverID, stratLabel))
	}
	// 把目标机真实执行的命令 + stdout/stderr 实时回流到本部署步骤日志(脱敏由 sink 侧 Masker 兜底)。
	dctx := deploy.WithCmdLog(ctx, func(stream, text string) { _ = rep.Log(ctx, stream, text) })
	results, err := b.deployer.DeployForStage(dctx, runID, []string{targetID}, cfg, strategy)
	if err != nil {
		_ = rep.Log(ctx, streamStderr, "部署失败:"+err.Error())
		return ErrBuildFailed
	}
	failed := false
	for _, dr := range results {
		line := fmt.Sprintf("· %s:%s — %s", dr.ServerName, dr.Status, dr.Message)
		if dr.Status == "success" {
			_ = rep.Log(ctx, streamStdout, line)
		} else {
			_ = rep.Log(ctx, streamStderr, line)
			failed = true
		}
	}
	if failed {
		return ErrBuildFailed
	}
	return nil
}

// composeSourceIsRepo 报告这个 compose 节点的正文取自项目仓库,而不是节点里粘的那份。
func composeSourceIsRepo(jb pipeline.Job) bool {
	return cfgString(jb.Config, pipeline.ConfigKeyComposeSource) == pipeline.ComposeSourceRepo
}

// composeRepoPath 是节点配的仓库相对路径(已 trim;合法性由调用处判)。
func composeRepoPath(jb pipeline.Job) string {
	return strings.TrimSpace(cfgString(jb.Config, pipeline.ConfigKeyComposeFile))
}

// deployJobsReadRepoFile 报告这批部署节点里有没有要从项目仓库读正文的(compose 或 k8s 清单)。
// 有,就必须解析出 proj —— 这是纯部署阶段(没有构建节点)唯一需要项目信息的理由。
func deployJobsReadRepoFile(jobs []pipeline.Job) bool {
	for _, jb := range jobs {
		if composeSourceIsRepo(jb) || cfgString(jb.Config, pipeline.ConfigKeyManifestSource) == pipeline.ManifestSourceRepo {
			return true
		}
	}
	return false
}

// manifestIsApplied 报告该部署节点走「应用清单」这条腿(repo / paste)。空与 none 都是
// 「只换镜像」—— 老节点没这一格,行为必须与加它之前一字不差。
func manifestIsApplied(jb pipeline.Job) bool {
	switch cfgString(jb.Config, pipeline.ConfigKeyManifestSource) {
	case pipeline.ManifestSourceRepo, pipeline.ManifestSourcePaste:
		return true
	}
	return false
}

// deployTemplateVars 是部署节点正文(命令 / compose / 清单)渲染用的变量表:本次运行参数
// + 流水线级「变量」。**加密变量刻意不进**:这些正文会落到目标机上、清单还会随发布结果入库
// (deploy_manifests,回滚要读它),收加密变量等于把机密抄一份存在我们自己的库里。
// IMAGE 也从这里剔掉 —— 它是内置占位符,由部署层换成本次挑中的那件镜像,运行参数不许顶掉它。
func deployTemplateVars(params map[string]string, settings *pipeline.Settings) map[string]string {
	out := make(map[string]string, len(params)+4)
	for k, v := range params {
		out[k] = v
	}
	if settings != nil {
		for _, v := range settings.Build.Vars {
			if v.Secret || strings.TrimSpace(v.Key) == "" {
				continue
			}
			out[v.Key] = v.Value
		}
	}
	delete(out, deploy.ImagePlaceholder)
	return out
}

// secretVarNames 返回正文里残留、且名字正好是某个加密变量的占位符(形如 `{{TOKEN}}`)。
// 这些是被 deployTemplateVars 有意跳过的:不说破就会变成一句查不出原因的「变量没给全」。
func secretVarNames(text string, settings *pipeline.Settings) string {
	if settings == nil || !strings.Contains(text, "{{") {
		return ""
	}
	secret := map[string]bool{}
	for _, v := range settings.Build.Vars {
		if v.Secret {
			secret[v.Key] = true
		}
	}
	var names []string
	for _, m := range tplPlaceholder.FindAllStringSubmatch(text, -1) {
		if !secret[m[1]] {
			continue
		}
		name := "{{" + m[1] + "}}"
		if !strings.Contains(strings.Join(names, " "), name) {
			names = append(names, name)
		}
	}
	return strings.Join(names, " / ")
}

// projectHasRepo 报告项目是否绑了 git 仓库(纯发布项目为空)。
func projectHasRepo(proj *project.Project) bool {
	return proj != nil && strings.TrimSpace(proj.RepoURL) != ""
}

// sourcelessEmptyWorkspaceNote 在未绑仓库的项目日志里说明工作区为什么是空的,免得人以为检出坏了。
const sourcelessEmptyWorkspaceNote = "· 项目未绑定仓库:本阶段工作区为空(不拉源码),任务在没有源码的目录里跑"

// workspaceNeedsRepo 在工作区要克隆前确认项目绑了仓库。needsSource 由调用方按任务类型给:
// 镜像档构建吃的是仓库里的 Dockerfile,没源码就没得构建 → 拦;纯脚本任务能在空目录里自己造文件
// → 放行。被拦时给一句照着能改的话,而不是让 go-git 对着空地址报「鉴权/网络」。
func workspaceNeedsRepo(proj *project.Project, needsSource bool) error {
	if needsSource && !projectHasRepo(proj) {
		return errors.New("本阶段的构建任务要拉取源码,但项目未绑定仓库:请到项目里绑定仓库,或把这一阶段改成不依赖源码(只发已有产物/镜像)")
	}
	return nil
}

// fillWorkspace 填入工作区内容:绑了仓库就克隆源码,没绑(纯发布项目)就留空目录并说明一句。
// 返回本次用于镜像 tag 的 commitTag(无仓库/未解析出 commit → "latest")。克隆失败与取消
// 都记日志后返回错误;成功且解析到 commit 时顺手回写运行记录。
func (b *Builder) fillWorkspace(ctx context.Context, r *run.Run, proj *project.Project, workspace string, rep dagrun.StageReporter) (string, error) {
	if !projectHasRepo(proj) {
		_ = rep.Log(ctx, streamStdout, sourcelessEmptyWorkspaceNote)
		return "latest", nil
	}
	auth := b.revealGitAuth(proj.CredentialID)
	resolved, cerr := b.cloner.Clone(ctx, proj.RepoURL, auth.Username, auth.Token, r.Trigger.Branch, r.Trigger.Commit, workspace)
	auth = vault.GitAuth{}
	if cerr != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return "", run.ErrCanceled
		}
		_ = rep.Log(ctx, streamStderr, "源码克隆失败(鉴权/网络/ref 不存在或被 SSRF 拒绝)")
		return "", ErrBuildFailed
	}
	commitTag := "latest"
	if resolved != nil && resolved.CommitShort != "" {
		commitTag = resolved.CommitShort
		if b.recordCommit != nil {
			b.recordCommit(ctx, r.ID, resolved.CommitShort)
		}
	}
	return commitTag, nil
}

// readRepoYAML 读出「部署节点引用仓库文件」时那份正文(compose 与 k8s 清单共用一条路径规则;
// what 只影响报错里那句主语)。读的是本次运行 commit(无 commit 则 branch)上的文件,不是工作区
// —— 部署节点可能跑在与构建不同的机器上,工作区未必存在。错误信息一律面向用户、可操作,
// 不透传 go-git 内部报错。
func (b *Builder) readRepoYAML(ctx context.Context, jb pipeline.Job, path string, r *run.Run, proj *project.Project, what string) (string, error) {
	if b.repoFiles == nil {
		return "", fmt.Errorf("%s 节点「%s」要引用仓库文件 %s,但平台未启用代码管理区(读不到仓库内容);改回粘贴正文或开启代码管理区", what, jb.Name, path)
	}
	if proj == nil || strings.TrimSpace(proj.RepoURL) == "" {
		return "", fmt.Errorf("%s 节点「%s」要引用仓库文件,但项目未绑定仓库", what, jb.Name)
	}
	if path == "" || !pipeline.RepoYAMLPathOK(path) {
		return "", fmt.Errorf("%s 节点「%s」的仓库文件路径非法:%s(需为仓库内相对路径,以 .yml/.yaml 结尾)", what, jb.Name, path)
	}
	auth := b.revealGitAuth(proj.CredentialID)
	body, err := b.repoFiles.ReadFile(ctx, proj.RepoURL, auth.Username, auth.Token, r.Trigger.Branch, r.Trigger.Commit, path)
	auth = vault.GitAuth{}
	if err != nil {
		switch {
		case errors.Is(err, ErrRepoFileNotFound):
			return "", fmt.Errorf("仓库里没有 %s(分支 %s / commit %s 上不存在该文件)", path, r.Trigger.Branch, shortCommit(r))
		case errors.Is(err, ErrRepoBadFilePath):
			return "", fmt.Errorf("%s 节点「%s」的仓库文件路径非法:%s", what, jb.Name, path)
		case errors.Is(err, ErrRepoFileTooLarge):
			return "", fmt.Errorf("%s 超过仓库单文件正文上限", path)
		default:
			return "", fmt.Errorf("读取仓库文件 %s 失败:%s", path, err)
		}
	}
	s := string(body)
	if strings.TrimSpace(s) == "" {
		return "", fmt.Errorf("仓库文件 %s 是空的", path)
	}
	return s, nil
}

// shortCommit 取 commit 短号用于日志;没 commit 时直说「最新提交」。
func shortCommit(r *run.Run) string {
	c := strings.TrimSpace(r.Trigger.Commit)
	if c == "" {
		return "最新提交"
	}
	if len(c) > 7 {
		return c[:7]
	}
	return c
}

// runNotifyJob 执行一个 notify 节点:按节点配的渠道(id 或名称)发一条通知。
// best-effort:通知失败只记日志、不令阶段失败(与终态通知钩子一致)。
func (b *Builder) runNotifyJob(ctx context.Context, rep dagrun.StageReporter, jb pipeline.Job, r *run.Run) {
	if b.notifier == nil {
		_ = rep.Log(ctx, streamStdout, "· 通知节点:通知服务未注入,跳过")
		return
	}
	chRef := cfgString(jb.Config, "channel")
	if chRef == "" {
		_ = rep.Log(ctx, streamStdout, "· 通知节点:未配渠道,跳过")
		return
	}
	chID, chName := b.resolveChannel(ctx, chRef)
	if chID == "" {
		_ = rep.Log(ctx, streamStderr, "通知节点:找不到渠道「"+chRef+"」,跳过")
		return
	}
	commit := strings.TrimSpace(r.Trigger.Commit)
	if len(commit) > 7 {
		commit = commit[:7]
	}
	payload := notify.Payload{
		Title:  "流水线通知",
		Body:   fmt.Sprintf("流水线执行到通知节点:分支 %s,commit %s。", r.Trigger.Branch, commit),
		Fields: map[string]string{"branch": r.Trigger.Branch, "commit": commit, "runId": r.ID, "stage": "notify"},
	}
	// 节点级内联模板(可选):配了标题/正文模板就按 {{占位}} 渲染覆盖默认文案,
	// 占位语义与「通知」设置里的模板一致(project/branch/commit/status/runId 等)。
	titleTpl := strings.TrimSpace(cfgString(jb.Config, "titleTemplate"))
	bodyTpl := strings.TrimSpace(cfgString(jb.Config, "bodyTemplate"))
	if titleTpl != "" || bodyTpl != "" {
		vars := notify.TemplateVars{
			Project: r.ProjectName,
			Branch:  r.Trigger.Branch,
			Commit:  commit,
			Status:  r.Status,
			Event:   "notify",
			RunID:   r.ID,
		}
		if titleTpl != "" {
			payload.Title = notify.RenderText(titleTpl, vars)
		}
		if bodyTpl != "" {
			payload.Body = notify.RenderText(bodyTpl, vars)
		}
	}
	if err := b.notifier.SendVia(ctx, chID, payload); err != nil {
		_ = rep.Log(ctx, streamStderr, "通知发送失败(best-effort):"+err.Error())
		return
	}
	_ = rep.Log(ctx, streamStdout, "· 已发通知 → 渠道「"+chName+"」")
}

// resolveChannel 把节点配的「渠道 id 或名称」解析为 (id, name);找不到返回空。
func (b *Builder) resolveChannel(ctx context.Context, ref string) (id, name string) {
	if ch, err := b.notifier.Get(ctx, ref); err == nil && ch != nil {
		return ch.ID, ch.Name
	}
	chs, err := b.notifier.List(ctx)
	if err != nil {
		return "", ""
	}
	for _, ch := range chs {
		if ch.Name == ref || ch.ID == ref {
			return ch.ID, ch.Name
		}
	}
	return "", ""
}

// runBuildImageJob 真实执行一个 build_image 节点:据节点 config 构造 BuildConfig,复用 Builder.build
// (模型 A=docker build / 模型 B=工具链构建)在宿主驱动上真实构建,产出镜像/jar/dist 产物。
// 镜像产物:环境绑定了 registry(或本阶段含 push_image 节点示意要推)→ b.push 推送并改写为远端引用 + digest;
// 无 registry → 镜像留本地,emit 本地 tag。其它产物(jar/dist)直接 emit。失败映射 ErrBuildFailed / 取消。
// 边界:docker build 上下文沿用工作区根(子目录上下文 = 后续);buildCommand 覆盖工具链默认命令 = 后续。
func (b *Builder) runBuildImageJob(ctx context.Context, sink run.StepSink, rep dagrun.StageReporter, jb pipeline.Job, stageName string, proj *project.Project, settings *pipeline.Settings, envName, workspace, commitTag string, hasPushJob bool) error {
	cfg := pipeline.BuildConfig{
		Model:          cfgString(jb.Config, "buildModel"),
		DockerfilePath: cfgString(jb.Config, "dockerfilePath"),
		Context:        cfgString(jb.Config, "context"),
		Toolchain:      pipeline.Toolchain{Language: cfgString(jb.Config, "toolchainLanguage"), Version: cfgString(jb.Config, "toolchainVersion")},
		ArtifactType:   cfgString(jb.Config, "artifactType"),
	}
	if cfg.ArtifactType == "" {
		cfg.ArtifactType = pipeline.ArtifactImage
	}
	if cfg.Model == "" {
		cfg.Model = pipeline.BuildModelDockerfile
	}
	// 只有 buildEnvId、没有 toolchainLanguage/Version 的配置(AI 提案、手改 YAML):按目录
	// 条目把语言/版本补上,执行期仍以目录为唯一镜像来源(#9),不会退回去拼「语言:版本」。
	if cfg.Model == pipeline.BuildModelToolchain && cfg.Toolchain.Language == "" {
		if id := cfgString(jb.Config, pipeline.ConfigKeyBuildEnvID); id != "" {
			if snap, serr := b.buildEnvSnapshot(stageName, jb.Name); serr == nil {
				if opt, ok := snap.OptionByID(id); ok {
					cfg.Toolchain = pipeline.Toolchain{Language: opt.Language, Version: opt.Version}
				}
			}
		}
	}

	localTag, art, berr := b.build(ctx, sink, 0, proj, cfg, workspace, commitTag)
	if berr != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return run.ErrCanceled
		}
		_ = rep.Log(ctx, streamStderr, fmt.Sprintf("构建节点「%s」失败:%v", jb.Name, berr))
		return ErrBuildFailed
	}
	if art == nil {
		return nil
	}

	// 镜像产物 + 绑定了 registry → 推送并登记远端引用(与非-dag Builder 同语义)。
	// 「构建后推送」开关(pushImage=false)可以只构建不推送;缺省仍是推送(旧行为)。
	if art.Type == run.ArtifactImage && localTag != "" {
		registry := b.resolveRegistry(settings, envName)
		pushWanted := pipeline.PushImageEnabled(jb.Config)
		switch {
		case registry != nil && pushWanted:
			remoteTag, digest, perr := b.push(ctx, sink, 0, localTag, registry, proj, commitTag)
			if perr != nil {
				if errors.Is(ctx.Err(), context.Canceled) {
					return run.ErrCanceled
				}
				_ = rep.Log(ctx, streamStderr, fmt.Sprintf("镜像推送失败:%v", perr))
				return ErrBuildFailed
			}
			art.Reference = remoteTag
			if digest != "" {
				if art.Metadata == nil {
					art.Metadata = map[string]any{}
				}
				art.Metadata["digest"] = digest
			}
		case !pushWanted:
			_ = rep.Log(ctx, streamStdout, "· 按「构建后推送」开关只构建、不推送:镜像留在本机,需要上仓时打开开关重跑")
		case hasPushJob:
			_ = rep.Log(ctx, streamStdout, "配了 push_image 但环境未绑定镜像仓库(registry),镜像留本地;到「触发设置 → 环境」绑定仓库后即自动推送")
		}
	}

	// 记来源节点(阶段 + job 名 + job ID),供运行详情标注「哪个节点产的」;
	// ID 是给部署节点按来源挑产物用的(名字可重复,不能当标识)。
	if art.Metadata == nil {
		art.Metadata = map[string]any{}
	}
	art.Metadata["sourceStage"] = stageName
	art.Metadata["sourceJob"] = jb.Name
	art.Metadata["sourceJobId"] = jb.ID

	if err := rep.EmitArtifact(ctx, *art); err != nil {
		_ = rep.Log(ctx, streamStderr, "登记产物失败:"+err.Error())
	}
	return nil
}

// collectScriptArtifacts 收集 script job 声明的文件产物(job.Config["artifactPath"],**多行、每行一条**):
// 逐条定位工作区内路径 → 按类型(目录=dist、*.jar=jar、其它文件=archive)归档进制品库真字节 →
// EmitArtifact 登记。一个 job 可声明多条(既出 jar 又出 dist 等);未声明则跳过。
// 镜像类产物不走这里(走 build_image 节点);文件类才在此收集。
func (b *Builder) collectScriptArtifacts(ctx context.Context, jobs []pipeline.Job, workspace, slug, stageName string, rep dagrun.StageReporter) {
	onLine := func(stream, line string) { _ = rep.Log(ctx, stream, line) }
	for _, jb := range jobs {
		for _, rel := range splitCommands(renderTemplate(cfgString(jb.Config, "artifactPath"), templateContext(jb.Config))) { // 渲染 {{参数}} + 按行拆分
			// 通配(如 backend/target/*.jar)→ 展开为实际文件逐个收集;否则按原路径收集。
			if strings.ContainsAny(rel, "*?[") {
				clean := filepath.Clean(rel)
				if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
					onLine(streamStderr, "产物路径越界,已拒绝:"+rel)
					continue
				}
				matches, _ := filepath.Glob(filepath.Join(workspace, clean))
				if len(matches) == 0 {
					onLine(streamStderr, "产物通配未匹配,跳过:"+rel)
					continue
				}
				for _, m := range matches {
					if relMatch, rerr := filepath.Rel(workspace, m); rerr == nil {
						b.collectOneFileArtifact(ctx, workspace, relMatch, slug, jb.ID, jb.Name, stageName, rep, onLine)
					}
				}
				continue
			}
			b.collectOneFileArtifact(ctx, workspace, rel, slug, jb.ID, jb.Name, stageName, rep, onLine)
		}
	}
}

// collectOneFileArtifact 收集单条文件产物路径:越界(.. / 绝对)拒绝;定位不到打日志跳过(不致命);
// 类型按路径自动判(目录=dist、*.jar=jar、其它文件=archive)。产物 metadata 记来源节点(阶段 + job 名),
// 供运行详情标注「哪个节点产的」。制品库未注入时 emit 占位引用,向后兼容。
func (b *Builder) collectOneFileArtifact(ctx context.Context, workspace, rel, slug, jobID, jobName, stageName string, rep dagrun.StageReporter, onLine func(stream, line string)) {
	clean := filepath.Clean(rel)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		onLine(streamStderr, "产物路径越界,已拒绝:"+rel)
		return
	}
	full := filepath.Join(workspace, clean)
	fi, err := os.Stat(full)
	if err != nil {
		onLine(streamStderr, "产物路径未找到,跳过:"+rel)
		return
	}
	// 产物名带上路径基名,避免一个 job 多产物同名(如多个 dist 目录)难以区分。
	base := filepath.Base(full)
	// metadata 记来源节点:sourceStage/sourceJob 供 UI 标注「哪个节点产的」(storeXxx 只补 stored/format,不清这些)。
	// sourceJobId 供部署节点按来源任务精确挑产物(job 名可重复,不能当标识)。
	// workspacePath 记原始工作区相对路径,供跨阶段产物传递:下游阶段据此把本产物真字节恢复回原位
	// (如 backend/target/x.jar),使被拆到下游阶段的 build_image「COPY target/x.jar」仍能命中。
	art := &run.Artifact{Name: slug + "-" + base, Reference: base, Metadata: map[string]any{"sourceStage": stageName, "sourceJob": jobName, "sourceJobId": jobID, "workspacePath": clean}}
	switch {
	case fi.IsDir():
		art.Type = run.ArtifactDist
		art.SizeBytes = dirSize(full)
		b.storeDistDir(art, full, onLine)
	case strings.HasSuffix(strings.ToLower(full), ".jar"):
		art.Type = run.ArtifactJar
		art.SizeBytes = fileSize(full)
		b.storeJarBytes(art, full, onLine)
	default:
		art.Type = run.ArtifactArchive
		art.SizeBytes = fileSize(full)
		b.storeJarBytes(art, full, onLine) // 单文件原样字节归档(format=file)
	}
	if err := rep.EmitArtifact(ctx, *art); err != nil {
		onLine(streamStderr, "登记产物失败:"+err.Error())
		return
	}
	onLine(streamStdout, "已产出产物:"+art.Name+"("+art.Type+","+rel+")")
}

// scriptStepFromJob 从画布 job.Config 构造一条 script 步骤(commands + 可选 workDir + 资源规格)。
//
// 镜像**不来自 job.Config**:按 buildEnvId(旧配置:目录内的 image / 工具链语言+版本)在预置目录
// 里解析(#9)—— 目录外或没选环境一律失败并要求重选,不存在「任意镜像地址」这条通路。
// 配置资源引用同批解析成只读挂载(#10),挂在 step.Resource.Mounts 上交给 driver。
// commands 全空 → 错误(诚实失败,不静默跳过)。
func (b *Builder) scriptStepFromJob(jb pipeline.Job, stageName string) (pipeline.PipelineStep, error) {
	ctx := templateContext(jb.Config)
	// 自定义节点的 image 允许是 {{参数}};渲染后仍要在目录内命中才算数。
	rendered := renderTemplate(cfgString(jb.Config, pipeline.ConfigKeyImage), ctx)
	resolved, err := b.jobRuntime(jb.Config, rendered, stageName, jb.Name)
	if err != nil {
		return pipeline.PipelineStep{}, err
	}
	// 自定义节点(templated):有 commandTemplate 则渲染 {{参数}} 作命令;否则用原始 commands。
	rawCmds := cfgString(jb.Config, "commands")
	if tpl := cfgString(jb.Config, "commandTemplate"); tpl != "" {
		rawCmds = renderTemplate(tpl, ctx)
	}
	cmds := splitCommands(rawCmds)
	if len(cmds) == 0 {
		return pipeline.PipelineStep{}, errors.New("缺少执行命令(commands / commandTemplate)")
	}
	mounts, merr := b.checkMountFiles(resolved.Mounts)
	if merr != nil {
		return pipeline.PipelineStep{}, merr
	}
	return pipeline.PipelineStep{
		ID:                jb.ID,
		Name:              jb.Name,
		Type:              pipeline.StepTypeScript,
		Image:             resolved.Image,
		ImageCredentialID: resolved.CredentialID,
		Commands:          cmds,
		Env:               matrixEnvVars(jb.Config), // 矩阵 cell 注入的 axis 环境变量(MATRIX_<AXIS>),空时 nil
		WorkDir:           renderTemplate(cfgString(jb.Config, "workDir"), ctx),
		// 任务级 timeout/retry/资源规格(P0 引擎能力):从 job.Config 自由 KV 读取(非负;非法/缺失→零值=旧行为)。
		TimeoutSeconds: cfgNonNegInt(jb.Config, "timeoutSeconds"),
		Retries:        cfgNonNegInt(jb.Config, "retries"),
		Resource: pipeline.Resource{
			CPU:    cfgString(jb.Config, "cpu"),
			Memory: cfgString(jb.Config, "memory"),
			Mounts: mounts,
		},
	}, nil
}

// checkMountFiles 确认配置资源的宿主文件确实在磁盘上:docker 对不存在的 bind 来源会**建一个空目录**
// 挂进去,构建会以「配置文件是空的」这种看不出根因的方式失败,所以这里提前拒绝。
func (b *Builder) checkMountFiles(mounts []pipeline.ContainerMount) ([]pipeline.ContainerMount, error) {
	for _, m := range mounts {
		if _, err := os.Stat(m.HostPath); err != nil {
			return nil, fmt.Errorf("配置资源文件在中控机上找不到(%s),请在「配置资源」里重新保存该文件", m.HostPath)
		}
	}
	return mounts, nil
}

// cfgNonNegInt 从自由 KV config 取非负整数(支持 JSON number 与字符串两种存法;
// 缺失/非法/负数 → 0,即「不限/不重试」的旧行为,向后兼容)。
func cfgNonNegInt(cfg map[string]any, key string) int {
	if cfg == nil {
		return 0
	}
	switch v := cfg[key].(type) {
	case float64: // encoding/json 把数字反序列化为 float64
		if v > 0 {
			return int(v)
		}
	case int:
		if v > 0 {
			return v
		}
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// matrixEnvVars 读取矩阵展开(dagrun.ExpandMatrix)注入 job.Config 的 cell 环境变量,转为非 secret
// BuildVar(`MATRIX_<AXIS>=<值>`,容器内命令可 `$MATRIX_GO` 引用)。键 "__matrixEnv" 由调度层合成、
// 非用户配置;值通常是 map[string]string(内存构造),JSON 回环时为 map[string]any,两者皆容错。
// 普通(非矩阵)job 无此键 → 返回 nil(零行为变化)。
func matrixEnvVars(cfg map[string]any) []pipeline.BuildVar {
	if cfg == nil {
		return nil
	}
	raw, ok := cfg["__matrixEnv"]
	if !ok {
		return nil
	}
	collect := func(k, v string) pipeline.BuildVar {
		return pipeline.BuildVar{Key: k, Value: v, Secret: false}
	}
	switch m := raw.(type) {
	case map[string]string:
		out := make([]pipeline.BuildVar, 0, len(m))
		for k, v := range m {
			out = append(out, collect(k, v))
		}
		return out
	case map[string]any:
		out := make([]pipeline.BuildVar, 0, len(m))
		for k, v := range m {
			if s, ok := v.(string); ok {
				out = append(out, collect(k, s))
			}
		}
		return out
	default:
		return nil
	}
}

// runParamsAsEnv 把运行参数(明文 K=V)转为非 secret BuildVar(注入容器环境)。键序确定性不保证(map),
// 但同名键不会重复(map 天然去重)。
func runParamsAsEnv(params map[string]string) []pipeline.BuildVar {
	if len(params) == 0 {
		return nil
	}
	out := make([]pipeline.BuildVar, 0, len(params))
	for k, v := range params {
		out = append(out, pipeline.BuildVar{Key: k, Value: v, Secret: false})
	}
	return out
}

// cfgString 从自由 KV config 取字符串值(非字符串/缺失 → "")。
func cfgString(cfg map[string]any, key string) string { return pipeline.ConfigString(cfg, key) }

// gitSourceLogLines 为 git_source 节点拼出可读的源码信息(仓库 / 分支 / 提交 / 凭据)。
// 注:git_source 是 go-git 库克隆(非 shell 命令),真实检出发生在构建阶段各 job 工作区,
// 此处展示「本阶段引用的源」让步骤日志不再是空占位。绝不回显凭据值,只标注是否已绑定。
// proj 为项目真值,可为 nil(项目读不到时):只有确实读到「未绑仓库」才说「无源可拉」,
// 读不到时退回按节点 config 里的镜像信息展示,不把读取失败说成没绑仓库。
func gitSourceLogLines(jb pipeline.Job, r *run.Run, proj *project.Project) []string {
	lines := []string{}
	if proj != nil && strings.TrimSpace(proj.RepoURL) == "" {
		return append(lines,
			"· 项目未绑定仓库:本流水线不拉源码,只用于发布已有产物/镜像",
			"· 需要源码请到项目里绑定仓库(项目卡片 →「仓库设置」)")
	}
	repo := cfgString(jb.Config, "repoUrl")
	cred := cfgString(jb.Config, "credentialId")
	if proj != nil {
		if repo == "" {
			repo = strings.TrimSpace(proj.RepoURL)
		}
		if cred == "" {
			cred = strings.TrimSpace(proj.CredentialID)
		}
	}
	branch := cfgString(jb.Config, "branch")
	if branch == "" {
		branch = strings.TrimSpace(r.Trigger.Branch)
	}
	if branch == "" {
		branch = "(默认分支)"
	}
	commit := strings.TrimSpace(r.Trigger.Commit)
	if repo != "" {
		lines = append(lines, "· 源码仓库:"+repo)
	}
	branchLine := "· 分支:" + branch
	if commit != "" {
		if len(commit) > 12 {
			commit = commit[:12]
		}
		branchLine += "   · 提交:" + commit
	} else {
		branchLine += "   · 提交:构建阶段克隆时解析 HEAD"
	}
	lines = append(lines, branchLine)
	if cred != "" {
		lines = append(lines, "· 凭据:已绑定(经保险库,绝不回显)")
	}
	lines = append(lines, "· 实际克隆在构建阶段各 job 工作区执行(go-git 浅克隆)")
	if len(lines) == 0 {
		lines = append(lines, fmt.Sprintf("· %s(git_source)", jb.Name))
	}
	return lines
}

// splitCommands 把多行命令字符串拆成逐行命令(trim 尾随 CR、剔空行)。
func splitCommands(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	out := make([]string, 0, 4)
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

// reporterSink 把 runScriptStep 期望的 run.StepSink 适配到 dagrun.StageReporter:
// Log/EmitArtifact 转发到已绑定本阶段序号的 reporter,其余步骤生命周期方法为 no-op
// (阶段的 StepRunning/StepDone 由 dagrun.Runner 负责)。
type reporterSink struct {
	rep dagrun.StageReporter
}

func (s *reporterSink) Plan(context.Context, []run.StepDecl) error          { return nil }
func (s *reporterSink) StepRunning(context.Context, int) error              { return nil }
func (s *reporterSink) StepDone(context.Context, int, string) error         { return nil }
func (s *reporterSink) SetFailureLog(context.Context, string) error         { return nil }
func (s *reporterSink) SetSpecSource(context.Context, run.SpecSource) error { return nil }
func (s *reporterSink) Log(ctx context.Context, stream string, _ int, line string) error {
	return s.rep.Log(ctx, stream, line)
}
func (s *reporterSink) EmitArtifact(ctx context.Context, a run.Artifact) error {
	return s.rep.EmitArtifact(ctx, a)
}
