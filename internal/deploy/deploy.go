// Package deploy 是「SSH 部署执行」领域层(FR-10 / Story 4.2)。
//
// 它把一次成功运行的某个产物经 SSH 部署到一台或多台目标服务器,记录每机结果,并据结果
// 更新 run 终态。部署命令一律 **array 化([]string)**:经注入的 target.Service.Exec
// 执行(各参数由 target 层 shell 转义后再交 SSH session),绝不拼接原始 shell 字符串
// (AC-SEC-02,杜绝命令注入)。SSH 密钥经 vault 由 target 层即用即弃,本层绝不接触明文;
// 输出 / message 绝无明文密钥。
//
// 编排边界:文件类产物(dist/jar/archive)**直铺**到部署路径(无 releases/current 软链,故不做
// 零停机切换与一键回滚);image 走容器停旧起新 + 失败回滚上一镜像。多机策略见 strategy.go。
// 本层 import run(取产物 + 写部署结果 + 更新终态)与 target(SSH 执行);
// run 包**不** import deploy(避免环)。
package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/fuuhoo/pipewright/internal/artifactstore"
	"github.com/fuuhoo/pipewright/internal/kube"
	"github.com/fuuhoo/pipewright/internal/run"
	"github.com/fuuhoo/pipewright/internal/target"
)

// 领域错误。错误体永不含明文 / 私钥 / 口令 / 内部栈。
var (
	// ErrRunNotFound 表示运行不存在。
	ErrRunNotFound = errors.New("deploy: run not found")
	// ErrRunNotSuccessful 表示运行非成功态,不可部署。
	ErrRunNotSuccessful = errors.New("deploy: run is not in a successful state")
	// ErrArtifactNotFound 表示该 run 下无指定产物。
	ErrArtifactNotFound = errors.New("deploy: artifact not found for run")
	// ErrArtifactSourceNotFound 表示部署节点指定的「产物来源任务」本次没产出可部署产物
	// (任务改名/删除,或那个任务本身不产产物)。与「没有任何产物」分开:前者是配置错了节点。
	ErrArtifactSourceNotFound = errors.New("deploy: no deployable artifact from the specified source job")
	// ErrServerNotFound 表示指定的目标服务器不存在。
	ErrServerNotFound = errors.New("deploy: target server not found")
	// ErrNoServers 表示未指定任何目标服务器。
	ErrNoServers = errors.New("deploy: no target servers specified")
	// ErrNoFailedTargets 表示该 run 当前无 failed/rolled_back 目标可重试(retry 专用)。
	ErrNoFailedTargets = errors.New("deploy: run has no failed targets to retry")
	// ErrRunNotDeployed 表示该 run 尚未部署过(无 deploy_targets),不可重试。
	ErrRunNotDeployed = errors.New("deploy: run has not been deployed yet")
	// ErrNoPendingTargets 表示该 run 当前无 pending 目标(分批部署续发/中止专用:无暂停中的批次)。
	ErrNoPendingTargets = errors.New("deploy: run has no pending targets")
)

// truncateLen 是写入 message 的命令输出最大长度(防超大输出撑爆响应 / 内存)。
const truncateLen = 800

// maxParallelDeploys 是多机扇出的有界并发上限(Story 4.5;信号量防同时打爆 N 台 SSH)。
// 每机独立 goroutine,信号量 cap 4:目标机再多也不会一次性建超过 4 条 SSH 连接。
const maxParallelDeploys = 4

// maxConcurrentDiagnoses 是部署失败 best-effort 自动诊断的并发上限(有界;NFR-4),对齐 run 侧
// (internal/run.maxConcurrentDiagnoses)。诊断钩子脱离请求在独立 goroutine 跑,若无界,批量部署失败
// (多机/多 run)会无界并发打 LLM + 多份日志/解密 key 同时驻留。超限则跳过本次自动诊断(用户仍可手动触发)。
const maxConcurrentDiagnoses = 2

// DeployInput 是一次部署请求(对齐 POST /api/runs/{id}/deploy 请求体)。
type DeployInput struct {
	RunID      string
	ArtifactID string
	ServerIDs  []string
	// Config 是可选部署参数(如目标路径);本期最小消费(dist 用 Path 决定部署目录)。
	Config map[string]string
	// HealthCheck 是可选的部署后健康门控(Story 4.3 / FR-12)。
	// nil 或 type=none → 跳过(向后兼容 4-2:部署命令成功即 success)。
	HealthCheck *HealthCheck
	// Strategy 是部署策略(Story 8-8 / FR-8-8):rolling(默认)| canary | blue_green。
	// 空 / 未知 → rolling(行为与未加策略前一致)。金丝雀批量经 Config["canaryCount"|"canaryPercent"]。
	Strategy string
}

// RetryInput 是一次「仅重试失败目标」请求(对齐 POST /api/runs/{id}/deploy/retry 请求体)。
//
// 无迁移、复用 deploy_targets:retry 不持久化原始部署产物/配置,故 ArtifactID + Config +
// HealthCheck 由调用方(前端已持有上次部署表单)随请求带回,复用既有 deployOne 链路。
// ServerIDs 可选:省略 → 重试该 run 当前所有 failed/rolled_back 目标;给定 → 只重试其中指定的
// (须是已有失败目标;不在失败集合内的忽略)。
type RetryInput struct {
	RunID       string
	ArtifactID  string
	ServerIDs   []string
	Config      map[string]string
	HealthCheck *HealthCheck
}

// TargetResult 是一台目标机的部署结果(与 run.DeployTarget 同形,供 HTTP 层映射回 DTO)。
type TargetResult struct {
	ServerID   string
	ServerName string
	Status     string // run.Target* 枚举:success | failed | …
	Message    string // 人读摘要(绝无明文密钥)
	StartedAt  time.Time
	FinishedAt *time.Time
	// Manifests 是本次实际交到集群手里的清单正文(只有 k8s「应用清单」那条腿带;SSH 腿为空)。
	// 它与结果同事务落 deploy_manifests,回滚读的就是这张表里的上一版。
	Manifests []run.ManifestDoc
}

// Service 定义部署执行对外接口(冻结点由 httpapi 层 DTO 承载;本接口可演进)。
type Service interface {
	// Deploy 取 run 的指定产物 → 校验成功态 / 产物 / 服务器 → 逐机经 SSH 执行该产物类型的
	// 部署命令 → 持久化每机结果 → 据结果更新 run 终态 → 返回每机结果。
	//
	// 定位类错误(run 非成功 / 无产物 / 服务器不存在 / 未指定服务器)上抛,供 HTTP 层 422/404。
	// **执行失败不上抛**:该机 status=failed + 人读 message,整体仍返回结果(整体 200)。
	Deploy(ctx context.Context, in DeployInput) ([]TargetResult, error)

	// RetryFailed 仅重试该 run 当前 failed/rolled_back 的目标(Story 4.5;FR-13「仅重试失败」)。
	//
	// 查 run 既有 deploy_targets → 取失败/回滚目标(可经 ServerIDs 进一步限定)→ 复用产物 + 配置
	// 对这些机并行重跑 deployOne → **逐目标 upsert**(成功目标不动,只覆盖被重试目标的行)→
	// 据全量目标重算 run 终态 → 返回该 run 全量最新 targets。
	//
	// 定位类错误(run 不存在 / 非失败态 / 无失败目标 / 无产物 / 服务器不存在)上抛,供 HTTP 层 422/404。
	// 执行失败不上抛:重试目标置 failed + 人读 message,整体仍 200。
	//
	// 只覆盖 SSH / docker 落点:k8s 节点失败请在流水线里重跑该任务 —— 这里复用逐机 SSH 链路,
	// 拿集群 ID 找不到服务器(ErrServerNotFound),不会静默发错东西。
	RetryFailed(ctx context.Context, in RetryInput) ([]TargetResult, error)

	// ContinueDeploy 续发交互式分批部署中**暂停**(pending)的其余目标(对齐 POST /runs/{id}/deploy/continue)。
	// 复用上次产物 + 配置(由请求带回,同 RetryFailed),对 pending 机按 strategy(默认 rolling)续发 →
	// 逐目标 upsert → 据全量重算 run 终态 → 返回全量最新 targets。无 pending → ErrNoPendingTargets。
	ContinueDeploy(ctx context.Context, in ContinueInput) ([]TargetResult, error)

	// AbortDeploy 中止交互式分批部署:把 pending(未部署)目标标记为「已中止,保留旧版本」(failed),
	// **不触碰已部署批次** → 据全量重算 run 终态 → 返回全量最新 targets。无 pending → ErrNoPendingTargets。
	AbortDeploy(ctx context.Context, in AbortInput) ([]TargetResult, error)

	// DeployForStage 是「流水线部署节点」用的中途部署:取该 run 已产出的可发布产物 →
	// 按落点分腿发布(SSH / docker 走逐机策略与切后探测;cfg 带 clusterId 的 k8s 节点走 API server
	// 直连 + rollout 门控)→ 持久化每目标结果(填 run-detail targets)。
	// **不校验 run 状态**(流水线执行中 run 仍 running)、**不置 run 终态**(终态由 dag 调度器控制)。
	// 无可发布产物 → ErrArtifactNotFound;服务器 / 集群不存在 → ErrServerNotFound / ErrClusterNotFound。
	// targetIDs 是落点 ID:SSH / docker 节点填服务器 ID,k8s 节点填集群 ID(同记进 deploy_targets)。
	// 策略 interactive 会在这里落地:首批铺完即把其余落点登记成 pending 并返回(调用方负责等人确认)。
	DeployForStage(ctx context.Context, runID string, targetIDs []string, cfg map[string]string, strategy string) ([]TargetResult, error)

	// ResumeStageTargets 续发流水线部署节点里「首批后暂停」剩下的落点:重新挑同一件产物、
	// 按 rolling 铺完 → 按目标 upsert。**不置 run 终态、也不重算**(终态归 DAG 调度器,
	// 与独立部署路径的 ContinueDeploy 的区别就在这里)。serverIDs 是调用方上一轮拿到的 pending 落点。
	ResumeStageTargets(ctx context.Context, runID string, serverIDs []string, cfg map[string]string) ([]TargetResult, error)

	// AbortStageTargets 中止流水线部署节点的剩余落点:把 serverIDs 标为「已中止,保留旧版本」(failed),
	// 不触碰已发批次、不置 run 终态。
	AbortStageTargets(ctx context.Context, runID string, serverIDs []string) ([]TargetResult, error)
}

// service 是 run + target 支撑的 Service 实现。
type service struct {
	targets target.Service
	runs    run.Service
	// diagnoseHook 是部署失败后的 best-effort 自动诊断钩子(Story 4.6;FR-22 种子)。
	// 由 main 注入(复用 7-2 NewDiagnoseHook);nil 则跳过。deploy 不 import ai(钩子解耦)。
	diagnoseHook func(ctx context.Context, runID string)
	// diagnoseSem 限流自动诊断 goroutine(有界并发,NFR-4;cap=maxConcurrentDiagnoses)。
	// try-acquire 满则跳过本次自动诊断(best-effort 语义不变)。在 New 中初始化。
	diagnoseSem chan struct{}
	// artStore 是制品库(Story 8-16):非 nil 时部署 release 类「已归档」产物会取真字节经 SSH 上传到
	// 目标机;nil 或产物非归档 → 旧占位路径(向后兼容)。由 main 注入(WithArtifactStore)。
	artStore *artifactstore.Store
	// kube 是 Kubernetes 集群目标层(deploy_k8s 节点用它直连 API server);nil = 平台未启用
	// k8s 能力,此时集群发布节点明确报错而不是退回 SSH(见 k8sRouteOf)。
	kube kube.Service
	// gates 是「同机串行」的执行权:同一台目标机一次只放一个远程操作出去(见 gate.go)。
	gatesMu sync.Mutex
	gates   map[string]chan struct{}
}

// Option 配置 deploy.Service(如注入诊断钩子)。
type Option func(*service)

// WithDiagnoseHook 注入部署失败后的 best-effort 自动诊断钩子(Story 4.6;FR-22 种子):
// 部署 failed/partial_failed → 合成失败日志(SetFailureLog)+ 触发钩子,让 7-2 诊断飞轮覆盖部署失败。
func WithDiagnoseHook(fn func(ctx context.Context, runID string)) Option {
	return func(s *service) { s.diagnoseHook = fn }
}

// WithKube 注入 Kubernetes 集群目标层(deploy_k8s 节点;不注入则该节点明确失败,不偷偷走 SSH)。
func WithKube(svc kube.Service) Option {
	return func(s *service) { s.kube = svc }
}

// WithArtifactStore 注入制品库(Story 8-16):部署 release 类已归档产物时取真字节上传目标机。
func WithArtifactStore(st *artifactstore.Store) Option {
	return func(s *service) {
		if st != nil {
			s.artStore = st
		}
	}
}

// New 构造部署 Service。
//   - targetSvc:通用 SSH 执行层(Story 4.1),部署命令经其 Exec 执行(array 不拼 shell)。
//   - runSvc   :运行领域层,取产物 / 写部署结果 / 更新终态。
//   - opts     :可选(如 WithDiagnoseHook 注入部署失败诊断,Story 4.6)。
//
// 不做任何重活(无 init() 副作用,避免抬高空载内存)。
func New(targetSvc target.Service, runSvc run.Service, opts ...Option) Service {
	s := &service{
		targets:     targetSvc,
		runs:        runSvc,
		diagnoseSem: make(chan struct{}, maxConcurrentDiagnoses),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// seedDiagnosisOnFailure 在部署终态为 failed/partial_failed 时合成失败日志并触发 best-effort 诊断
// (Story 4.6;FR-22 种子):让 7-2 的 AI 失败分析 + 7-5 反馈闭环覆盖部署失败,而非只覆盖构建失败。
// 失败日志取自各失败/回滚目标的 message(平台构造,无明文密钥)。绝不阻断部署结果返回。
func (s *service) seedDiagnosisOnFailure(ctx context.Context, runID, status string, targets []TargetResult) {
	if status != run.StatusFailed && status != run.StatusPartialFailed {
		return
	}
	var b strings.Builder
	b.WriteString("[部署失败 deploy] 以下目标机部署未成功:\n")
	for _, t := range targets {
		if t.Status == run.TargetFailed || t.Status == run.TargetRolledBack {
			fmt.Fprintf(&b, "- %s(%s):%s\n", t.ServerName, t.Status, t.Message)
		}
	}
	if err := s.runs.SetFailureLog(ctx, runID, b.String()); err != nil {
		return // best-effort:写失败日志失败则不诊断,不阻断
	}
	if s.diagnoseHook != nil {
		hook := s.diagnoseHook
		// 并发有界(NFR-4):try-acquire,满则跳过本次自动诊断(用户仍可手动触发)。
		select {
		case s.diagnoseSem <- struct{}{}:
		default:
			return // 已达上限:跳过自动诊断,不阻断部署结果返回。
		}
		go func() {
			defer func() { <-s.diagnoseSem }()
			defer func() { _ = recover() }()
			hook(context.WithoutCancel(ctx), runID)
		}()
	}
}

func (s *service) Deploy(ctx context.Context, in DeployInput) ([]TargetResult, error) {
	if len(in.ServerIDs) == 0 {
		return nil, ErrNoServers
	}

	// 1) 校验 run 存在 + 成功态。
	rn, err := s.runs.Get(ctx, in.RunID)
	if err != nil {
		if errors.Is(err, run.ErrNotFound) {
			return nil, ErrRunNotFound
		}
		return nil, err
	}
	if rn.Status != run.StatusSuccess && rn.Status != run.StatusPartialFailed {
		// 仅成功 / 部分成功的运行有可部署产物;进行中 / 失败 / 排队 → 不可部署。
		return nil, ErrRunNotSuccessful
	}

	// 2) 定位产物(必须属于该 run)。
	arts, err := s.runs.ListArtifacts(ctx, in.RunID)
	if err != nil {
		return nil, err
	}
	var artifact *run.Artifact
	for i := range arts {
		if arts[i].ID == in.ArtifactID {
			a := arts[i]
			artifact = &a
			break
		}
	}
	if artifact == nil {
		return nil, ErrArtifactNotFound
	}

	// 3) 预解析所有目标服务器(任一不存在 → 422,整次拒绝,不留半截)。
	servers := make([]*target.Server, 0, len(in.ServerIDs))
	for _, sid := range in.ServerIDs {
		srv, gerr := s.targets.Get(ctx, sid)
		if gerr != nil {
			if errors.Is(gerr, target.ErrNotFound) {
				return nil, ErrServerNotFound
			}
			return nil, gerr
		}
		servers = append(servers, srv)
	}

	// 4) 按**部署策略**执行(Story 8-8 / FR-8-8):rolling(默认,= 4-5 并行扇出)| canary | blue_green。
	// 每机独立 goroutine + recover,有界信号量(cap 4)防同时打爆 N 台;单机 panic/失败不连累其它机;
	// 结果按输入顺序独立收集。策略仅改机群编排(分批门控 / 统一切换),不改单机执行语义。
	strategy := NormalizeStrategy(in.Strategy)
	var results []TargetResult
	paused := false
	if strategy == StrategyInteractive {
		// 交互式分批:发首批 → 首批全过则其余登记 pending 并**暂停**(不置终态),否则中止其余。
		results, paused = s.deployInteractiveFirstBatch(ctx, servers, *artifact, in.Config, in.HealthCheck)
	} else {
		results = s.deployWithStrategy(ctx, servers, *artifact, in.Config, in.HealthCheck, strategy)
	}

	// 5) 持久化每机结果(填 run-detail targets slot)。
	dts := make([]run.DeployTarget, 0, len(results))
	for _, r := range results {
		dts = append(dts, deployTargetOf(in.RunID, r))
	}
	if err := s.runs.SaveDeployTargets(ctx, in.RunID, dts); err != nil {
		return nil, err
	}
	// 6) 暂停态(交互式首批已过、其余 pending)→ 不置终态(run 保持成功,等续发/中止);
	//    否则据结果置 run 终态:全成功 → success;有失败 → partial_failed;全失败 → failed。
	if paused {
		return results, nil
	}
	final := overallStatus(results)
	if err := s.runs.SetDeployTerminal(ctx, in.RunID, final); err != nil {
		return nil, err
	}

	// 7) 部署失败 → best-effort 触发 AI 诊断(Story 4.6;FR-22 种子;不阻断返回)。
	s.seedDiagnosisOnFailure(ctx, in.RunID, final, results)

	return results, nil
}

// DeployForStage 见接口注释:流水线 deploy_ssh 节点的中途部署(不校验 run 状态、不置终态)。
//
// targetIDs 是这条流水线的落点:SSH / docker 节点是目标服务器 ID,k8s 节点(cfg 带 clusterId)
// 是集群 ID —— 两者都是「一台/一个目标」,共用同一份 deploy_targets 记录与结果形状。
func (s *service) DeployForStage(ctx context.Context, runID string, targetIDs []string, cfg map[string]string, strategy string) ([]TargetResult, error) {
	if len(targetIDs) == 0 {
		return nil, ErrNoServers
	}
	// 流水线部署节点的健康探测(节点表单配置;未配 → nil,行为与此前完全一致)。
	hc := HealthCheckFromConfig(cfg)
	// 「命令型」部署(artifactType=command):不取构建产物,直接在目标机执行 cfg["restartCommand"]。
	// 供「配置类」流水线用(如 frp 隧道:就地 upsert frpc.ini + reload)。与产物发布完全隔离——
	// **真实产物部署(artifactType != command)绝不进此分支**,既有部署/策略/健康检查路径零影响。
	if strings.TrimSpace(cfg["artifactType"]) == "command" {
		return s.runCommandOnly(ctx, runID, targetIDs, cfg, hc)
	}
	// docker 部署的 compose 方式:正文自带镜像与拓扑,**不取构建产物**(与命令型同为无产物分支)。
	// 放在产物查找之前,否则一次纯 compose 部署会因为本次没构建出东西而报 ErrArtifactNotFound。
	// 单容器方式继续走下面的产物路径(那里强制偏好镜像)。
	if dockerModeOf(cfg) == DockerModeCompose {
		return s.deployComposeStack(ctx, runID, targetIDs, cfg, hc)
	}
	// 取该 run 已产出的可部署产物。dist/jar/archive 走文件发布;image 走容器 pull→停旧起新→
	// 健康→回滚(复用 image_release.go)。二者都在时按节点 cfg["artifactType"] 选(空 → 默认优先
	// 文件发布,保持既有行为;显式 image → 选镜像)。选定后由 deployWithStrategy 据产物类型自动路由。
	artifact, err := s.pickStageArtifactFor(ctx, runID, cfg)
	if err != nil {
		return nil, err
	}

	// 集群发布(deploy_k8s):落点不是机器而是一个 API server,整条链路没有 shell。
	// 因此它不进 deployWithStrategy —— 逐机批次、切后探测、软链切换在这里既跑不了也不该跑,
	// 滚动与就绪判定由集群控制器 + rollout 门控负责(见 k8s_deploy.go)。
	if K8sRouteOf(cfg) {
		results, err := s.deployToK8s(ctx, runID, targetIDs, cfg, *artifact)
		if err != nil {
			return nil, err
		}
		return results, s.saveStageTargets(ctx, runID, results)
	}

	servers := make([]*target.Server, 0, len(targetIDs))
	for _, sid := range targetIDs {
		srv, gerr := s.targets.Get(ctx, sid)
		if gerr != nil {
			if errors.Is(gerr, target.ErrNotFound) {
				return nil, ErrServerNotFound
			}
			return nil, gerr
		}
		servers = append(servers, srv)
	}

	results := s.deployWithStrategy(ctx, servers, *artifact, cfg, hc, NormalizeStrategy(strategy))
	if err := s.saveStageTargets(ctx, runID, results); err != nil {
		return nil, err
	}
	return results, nil
}

// pickStageArtifactFor 从该 run 的产物里挑出这个部署节点该发的那一件(与 DeployForStage 同一口径:
// 先按 artifactFrom 把范围收到来源任务,再按类型偏好挑)。
// 单独成一个函数是为了「首批后暂停」:人工确认之后续发的必须还是确认之前那一件,
// 不能重新在同类产物里撞一次运气。
func (s *service) pickStageArtifactFor(ctx context.Context, runID string, cfg map[string]string) (*run.Artifact, error) {
	arts, err := s.runs.ListArtifacts(ctx, runID)
	if err != nil {
		return nil, err
	}
	// 「产物来源任务」(artifactFrom):并行构建产出多件同类产物时,部署节点按来源任务先把范围
	// 收窄到本节点该发的那一件,再在范围内按类型偏好挑(否则同类型产物只能靠建单顺序撞运气)。
	// 指定了来源却一件都没有 → 明确报错,绝不退回"随便发一件":发错东西比不发更糟。
	if ref := strings.TrimSpace(cfg[CfgKeyArtifactFrom]); ref != "" {
		scoped := artifactsFromSource(arts, ref)
		if len(scoped) == 0 {
			return nil, fmt.Errorf("%w: 指定的来源任务 %q 本次没有产出可部署产物(任务被改名/删除,或该任务没有产物)", ErrArtifactSourceNotFound, ref)
		}
		arts = scoped
	}
	// docker 单容器方式发的必然是镜像:偏好留空时 pickStageArtifact 默认偏向文件产物,
	// 不显式纠正就会把 dist 铺进机器却报告容器已起。
	// k8s 节点同理(且更严格:产物不是镜像直接判错,见 deployToK8s)。
	prefer := strings.TrimSpace(cfg["artifactType"])
	if prefer == "" && (IsDockerRunMode(cfg) || K8sRouteOf(cfg)) {
		prefer = run.ArtifactImage
	}
	artifact := pickStageArtifact(arts, prefer)
	if artifact == nil {
		return nil, ErrArtifactNotFound
	}
	// 回流一次"到底发了哪一件":并行构建下产物同名同类型,不写明来源就无从核对。
	if src, ok := artifact.Metadata["sourceJob"].(string); ok && src != "" {
		cmdLogFrom(ctx)(cmdStreamStdout, fmt.Sprintf("· 产物 %s(%s)← 来源任务「%s」", artifact.Name, artifact.Type, src))
	} else {
		cmdLogFrom(ctx)(cmdStreamStdout, fmt.Sprintf("· 产物 %s(%s)", artifact.Name, artifact.Type))
	}
	return artifact, nil
}

// saveStageTargets 持久化每个落点的部署结果(填 run-detail targets slot);**不置 run 终态**
// —— 流水线部署节点由 dag 调度器控制终态,这里只留痕。
func (s *service) saveStageTargets(ctx context.Context, runID string, results []TargetResult) error {
	dts := make([]run.DeployTarget, 0, len(results))
	for _, r := range results {
		dts = append(dts, deployTargetOf(runID, r))
	}
	return s.runs.SaveDeployTargets(ctx, runID, dts)
}

// deployTargetOf 把一次目标机结果折成 run 层的持久化形状。清单正文只能经这一个出口下沉:
// 少带一次,「上一版发了什么」就永远查不到了(而回滚正是读它)。
func deployTargetOf(runID string, r TargetResult) run.DeployTarget {
	return run.DeployTarget{
		RunID: runID, ServerID: r.ServerID, ServerName: r.ServerName,
		Status: r.Status, Message: r.Message,
		StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
		Manifests: r.Manifests,
	}
}

// runCommandOnly 执行「命令型」部署:在每台目标机直接跑 cfg["restartCommand"](无构建产物)。
// 命令文本里的 {{param}} 已在 build 层(runDeployJob)用本次运行参数渲染好;此处只逐机执行 +
// 经 s.exec 把命令/输出实时回流步骤日志(脱敏由 sink Masker 兜底)+ 持久化每机结果。
// hc 非 nil 时命令成功后再探测该机(与产物部署路径同一门控语义)。
func (s *service) runCommandOnly(ctx context.Context, runID string, serverIDs []string, cfg map[string]string, hc *HealthCheck) ([]TargetResult, error) {
	command := strings.TrimSpace(cfg["restartCommand"])
	if command == "" {
		return nil, fmt.Errorf("deploy: 命令型部署缺少 restartCommand")
	}
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
		out, err := s.exec(ctx, sid, []string{"sh", "-c", command})
		fin := time.Now().UTC()
		tr := TargetResult{ServerID: sid, ServerName: srv.Name, StartedAt: started, FinishedAt: &fin}
		switch {
		case err != nil:
			tr.Status = run.TargetFailed
			tr.Message = humanExecError(err)
		case out != nil && out.ExitCode != 0:
			tr.Status = run.TargetFailed
			tr.Message = fmt.Sprintf("命令退出码 %d", out.ExitCode)
		default:
			// 命令成功 != 服务可用:配了健康探测就继续在同一条 exec 链路上探测,失败判该机 failed。
			tr.Status = run.TargetSuccess
			tr.Message = "命令执行成功"
			if hc.enabled() {
				if herr := s.runHealthCheck(ctx, sid, hc); herr != nil {
					tr.Status = run.TargetFailed
					tr.Message = herr.Error()
				} else {
					tr.Message = "命令执行成功;健康检查通过"
				}
			}
		}
		results = append(results, tr)
	}
	dts := make([]run.DeployTarget, 0, len(results))
	for _, r := range results {
		dts = append(dts, deployTargetOf(runID, r))
	}
	if err := s.runs.SaveDeployTargets(ctx, runID, dts); err != nil {
		return nil, err
	}
	return results, nil
}

// CfgKeyArtifactFrom 是部署节点 cfg 的「产物来源任务」键(值 = 上游构建任务的 job ID)。
// 与 pipeline.ConfigKeyArtifactFrom 逐字一致(build 层原样透传;两侧键名不同就会静默失配)。
const CfgKeyArtifactFrom = "artifactFrom"

// artifactsFromSource 收窄产物到「指定来源任务」产出的那些。
// 先比 metadata.sourceJobId(ID 唯一,配置存的就是它);再兜底比 sourceJob(任务名)——
// 让手填/历史数据也能点中,名字重复时按顺序取第一个命中。
func artifactsFromSource(arts []run.Artifact, ref string) []run.Artifact {
	var out []run.Artifact
	for _, a := range arts {
		if !deployableStageArtifact(a) {
			continue
		}
		if id, ok := a.Metadata["sourceJobId"].(string); ok && id != "" && id == ref {
			out = append(out, a)
			continue
		}
		if name, ok := a.Metadata["sourceJob"].(string); ok && name != "" && name == ref {
			out = append(out, a)
		}
	}
	return out
}

// deployableStageArtifact 判定产物在「流水线部署节点」可被部署:文件发布类(dist/jar/archive)
// 或镜像类(image)。其余类型不可部署(无对应编排)。
func deployableStageArtifact(a run.Artifact) bool {
	return fileDeployArtifact(a) || a.Type == run.ArtifactImage
}

// pickStageArtifact 从该 run 的产物里挑「部署节点」要部署的一件,返回 nil = 无可部署产物。
//
// 选取规则(参数自由,不写死单一类型):
//   - prefer == "image":优先选首个 image 产物;无 image → 退回首个文件发布产物(尽力部署)。
//   - prefer 为文件类(dist/jar/archive):优先选该精确类型;无则退回任一文件发布产物。
//   - prefer 空:默认优先文件发布产物(保持既有行为),无文件发布则退回 image。
//
// 任何情况下都只在「可部署产物」(deployableStageArtifact)中挑选;选定的类型由
// deployWithStrategy 自动路由到文件发布 or 镜像编排。
func pickStageArtifact(arts []run.Artifact, prefer string) *run.Artifact {
	prefer = strings.ToLower(prefer)
	var firstImage, firstRelease, firstExact *run.Artifact
	for i := range arts {
		a := arts[i]
		if !deployableStageArtifact(a) {
			continue
		}
		if firstExact == nil && prefer != "" && a.Type == prefer {
			firstExact = &arts[i]
		}
		if a.Type == run.ArtifactImage && firstImage == nil {
			firstImage = &arts[i]
		}
		if fileDeployArtifact(a) && firstRelease == nil {
			firstRelease = &arts[i]
		}
	}
	// 1) 显式偏好且精确命中 → 选它。
	if firstExact != nil {
		return firstExact
	}
	// 2) 显式偏好 image(未精确命中走这里)→ image 优先,退回文件发布。
	if prefer == run.ArtifactImage {
		if firstImage != nil {
			return firstImage
		}
		return firstRelease
	}
	// 3) 默认 / 偏好文件类:文件发布优先(保持既有行为),退回 image。
	if firstRelease != nil {
		return firstRelease
	}
	return firstImage
}

// RetryFailed 仅重试该 run 当前 failed/rolled_back 的目标(Story 4.5;FR-13)。见 Service 接口注释。
func (s *service) RetryFailed(ctx context.Context, in RetryInput) ([]TargetResult, error) {
	// 1) 校验 run 存在 + 处于失败/部分失败态(成功 run 无失败目标可重试)。
	rn, err := s.runs.Get(ctx, in.RunID)
	if err != nil {
		if errors.Is(err, run.ErrNotFound) {
			return nil, ErrRunNotFound
		}
		return nil, err
	}
	if rn.Status != run.StatusFailed && rn.Status != run.StatusPartialFailed {
		// 仅失败/部分失败的部署有「失败目标」可重试;成功/进行中/排队 → 422 人读。
		return nil, ErrNoFailedTargets
	}

	// 2) 取该 run 既有部署目标;无 → 未部署过(不可重试)。
	existing, err := s.runs.ListDeployTargets(ctx, in.RunID)
	if err != nil {
		return nil, err
	}
	if len(existing) == 0 {
		return nil, ErrRunNotDeployed
	}

	// 3) 取失败/回滚目标集合;若请求给定 ServerIDs,取其与失败集合的交集(只重试已有失败目标)。
	wantSet := map[string]struct{}{}
	for _, sid := range in.ServerIDs {
		wantSet[sid] = struct{}{}
	}
	retrySIDs := make([]string, 0, len(existing))
	for i := range existing {
		t := existing[i]
		if t.Status != run.TargetFailed && t.Status != run.TargetRolledBack {
			continue
		}
		if len(wantSet) > 0 {
			if _, ok := wantSet[t.ServerID]; !ok {
				continue
			}
		}
		retrySIDs = append(retrySIDs, t.ServerID)
	}
	if len(retrySIDs) == 0 {
		return nil, ErrNoFailedTargets
	}

	// 4) 定位产物(复用上次部署的产物;由请求携带 artifactId,必须属于该 run)。
	arts, err := s.runs.ListArtifacts(ctx, in.RunID)
	if err != nil {
		return nil, err
	}
	var artifact *run.Artifact
	for i := range arts {
		if arts[i].ID == in.ArtifactID {
			a := arts[i]
			artifact = &a
			break
		}
	}
	if artifact == nil {
		return nil, ErrArtifactNotFound
	}

	// 5) 解析待重试服务器(任一不存在 → 422,整次拒绝)。
	servers := make([]*target.Server, 0, len(retrySIDs))
	for _, sid := range retrySIDs {
		srv, gerr := s.targets.Get(ctx, sid)
		if gerr != nil {
			if errors.Is(gerr, target.ErrNotFound) {
				return nil, ErrServerNotFound
			}
			return nil, gerr
		}
		servers = append(servers, srv)
	}

	// 6) 对这些机并行重跑 deployOne(复用产物 + 配置)。
	retried := s.deployFanout(ctx, servers, *artifact, in.Config, in.HealthCheck)

	// 7) **逐目标 upsert**:只更新被重试目标对应行,**保留**本次未重试的成功目标(别用整批删的 SaveDeployTargets)。
	dts := make([]run.DeployTarget, 0, len(retried))
	for _, r := range retried {
		dts = append(dts, deployTargetOf(in.RunID, r))
	}
	if err := s.runs.UpsertDeployTargets(ctx, in.RunID, dts); err != nil {
		return nil, err
	}

	// 8) 据**全量**最新目标重算 run 终态(全成功 → success;仍有失败 → partial_failed/failed)。
	all, err := s.runs.ListDeployTargets(ctx, in.RunID)
	if err != nil {
		return nil, err
	}
	final := overallStatusOfTargets(all)
	if err := s.runs.SetDeployTerminal(ctx, in.RunID, final); err != nil {
		return nil, err
	}

	// 返回该 run 全量最新 targets(经 TargetResult 形状,供 HTTP 层映射;HTTP 层亦会回读权威结果)。
	out := make([]TargetResult, 0, len(all))
	for i := range all {
		t := all[i]
		out = append(out, TargetResult{
			ServerID:   t.ServerID,
			ServerName: t.ServerName,
			Status:     t.Status,
			Message:    t.Message,
			StartedAt:  t.StartedAt,
			FinishedAt: t.FinishedAt,
		})
	}

	// 重试后仍失败 → best-effort 触发 AI 诊断(Story 4.6;不阻断返回)。
	s.seedDiagnosisOnFailure(ctx, in.RunID, final, out)

	return out, nil
}

// deployFanout 并行扇出多机部署(Story 4.5):每机独立 goroutine + recover,有界信号量
// (maxParallelDeploys)限并发 SSH;单机 panic 不连累其它机(recover → 该机 failed 人读)。
// 结果按 servers 输入顺序回填(稳定可断言),失败台不阻断其它台。
func (s *service) deployFanout(ctx context.Context, servers []*target.Server, a run.Artifact, cfg map[string]string, hc *HealthCheck) []TargetResult {
	results := make([]TargetResult, len(servers))
	sem := make(chan struct{}, maxParallelDeploys)
	var wg sync.WaitGroup

	for i := range servers {
		wg.Add(1)
		go func(idx int, srv *target.Server) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			// 单机 panic 兜底:绝不让一台崩溃带垮整次扇出(goroutine panic 会终止进程)。
			defer func() {
				if rec := recover(); rec != nil {
					finish := time.Now().UTC()
					results[idx] = TargetResult{
						ServerID:   srv.ID,
						ServerName: srv.Name,
						Status:     run.TargetFailed,
						Message:    "部署执行异常中断",
						StartedAt:  finish,
						FinishedAt: &finish,
					}
				}
			}()
			results[idx] = s.deployOne(ctx, srv, a, cfg, hc)
		}(i, servers[i])
	}
	wg.Wait()
	return results
}

// deployOne 在一台目标机上按产物类型分派部署编排:文件类直铺(release.go)、镜像容器编排
// (image_release.go)。执行错误**不上抛**:一律映射为 status + 人读 message(绝无明文密钥)。
func (s *service) deployOne(ctx context.Context, srv *target.Server, a run.Artifact, cfg map[string]string, hc *HealthCheck) TargetResult {
	// dist / jar / archive 走「产物直铺到部署路径 + 重启 + 健康门控」(见 release.go)。
	if fileDeployArtifact(a) {
		return s.deployFileOne(ctx, srv, a, cfg, hc)
	}

	// image 走「pull → 停旧起新 → 健康门控 → 失败回滚上一镜像」(复用蓝绿单机原语,每机独立)。
	// 补齐此前 buildImageDeploy 扁平路径无回滚的缺口:滚动 / 金丝雀 image 部署失败也能回滚。
	if a.Type == run.ArtifactImage {
		return s.deployImageOne(ctx, srv, a, cfg, hc, time.Now().UTC())
	}

	// 到这里只剩未知类型:既非文件直铺(dist/jar/archive)也非镜像 → 无对应编排,直接 failed 人读。
	finish := time.Now().UTC()
	return TargetResult{
		ServerID:   srv.ID,
		ServerName: srv.Name,
		StartedAt:  finish,
		Status:     run.TargetFailed,
		Message:    fmt.Sprintf("不支持的产物类型:%s", a.Type),
		FinishedAt: &finish,
	}
}

// overallStatus 据每机结果聚合 run 终态:
//
//	全成功 → success;有成功有失败 → partial_failed;全失败 → failed。
func overallStatus(results []TargetResult) string {
	anySuccess, anyFailed := false, false
	for _, r := range results {
		switch r.Status {
		case run.TargetSuccess:
			anySuccess = true
		default:
			anyFailed = true
		}
	}
	switch {
	case anyFailed && anySuccess:
		return run.StatusPartialFailed
	case anyFailed:
		return run.StatusFailed
	default:
		return run.StatusSuccess
	}
}

// overallStatusOfTargets 据**全量持久化目标**聚合 run 终态(retry 后重算用)。
// 与 overallStatus 同语义,但作用于 run.DeployTarget(rolled_back 计为失败侧:有成功有失败 →
// partial_failed;全失败/全回滚 → failed;全成功 → success)。
func overallStatusOfTargets(targets []run.DeployTarget) string {
	anySuccess, anyFailed := false, false
	for i := range targets {
		switch targets[i].Status {
		case run.TargetSuccess:
			anySuccess = true
		default:
			anyFailed = true
		}
	}
	switch {
	case anyFailed && anySuccess:
		return run.StatusPartialFailed
	case anyFailed:
		return run.StatusFailed
	default:
		return run.StatusSuccess
	}
}

// truncate 截断字符串到 truncateLen(防超大输出)。
func truncate(s string) string {
	if len(s) <= truncateLen {
		return s
	}
	return s[:truncateLen] + "…(已截断)"
}

// humanExecError 把 target 层执行错误映射为人读文案(绝不含凭据明文 / 内部栈)。
// target.Exec 已把连接 / 认证类错误映射为领域错误,本层进一步人读化。
func humanExecError(err error) string {
	switch {
	case errors.Is(err, target.ErrAuth):
		return "SSH 认证失败:密钥或口令无效,或无登录权限"
	case errors.Is(err, target.ErrUnreachable):
		return "无法连接服务器:端口未开放、主机不可达或超时"
	case errors.Is(err, target.ErrVaultUnconfigured):
		return "保险库未配置 master key,无法取 SSH 凭据"
	case errors.Is(err, target.ErrCredentialNotFound):
		return "引用的 SSH 凭据不存在"
	case errors.Is(err, context.DeadlineExceeded):
		return "部署执行超时"
	case errors.Is(err, context.Canceled):
		return "部署已取消(运行被取消或服务退出)"
	default:
		// 兜底:不泄漏内部细节(target 层错误体已无凭据明文)。
		return "部署执行失败"
	}
}
