// k8s_deploy.go 是「K8s 发布」节点(deploy_k8s)的执行链路:平台直连集群 API server,
// 不经过任何一台跳板机。
//
// 它和 docker/SSH 那条腿的根本区别是**没有 shell 可跑**:换镜像是一次 PATCH,滚动是集群
// 控制器自己做的,我们要做的是「等它滚完」。所以这里的门控等价物不是 curl /healthz,
// 而是 kubectl rollout status 的那套判定(见 kube.rolloutDone):控制器已观察到新规格、
// 新 pod 全部就绪、旧 pod 已退。探测不通就判失败,不留「PATCH 成功 = 发布成功」的假绿。
//
// 回滚 = 把读到的上一镜像再 PATCH 回去(我们本来就知道旧值,比查 ReplicaSet 更直接)。
// 引用没变时(同一 commit 重跑)控制器根本不会滚 —— 那一刀改用 restartedAt 注解补上,
// 否则「重新发布一次」会在 API server 那里什么都没发生,而我们报告成功。
package deploy

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/fuuhoo/pipewright/internal/kube"
	"github.com/fuuhoo/pipewright/internal/run"
)

// k8s 部署节点的 cfg 键(与 pipeline.ConfigKey* 逐字一致:build 层原样透传,
// 两侧键名不同就会静默失配 —— 参数形同没填)。
const (
	// CfgKeyClusterID 是目标集群 ID;非空即代表这是一次集群发布(部署层据此分腿)。
	CfgKeyClusterID = "clusterId"
	CfgKeyNamespace = "namespace"
	// CfgKeyWorkloadKind 是负载类型(Deployment | StatefulSet)。
	CfgKeyWorkloadKind = "workloadKind"
	CfgKeyWorkloadName = "workloadName"
	// CfgKeyK8sContainer 是 pod 规格里要换镜像的容器名;留空 = 规格只有一个容器时自动选它。
	CfgKeyK8sContainer = "containerName"
	// CfgKeyRolloutTimeout 是等滚动的秒数(空 = defaultRolloutTimeout)。
	CfgKeyRolloutTimeout = "rolloutTimeout"
	// CfgKeyAutoRollback 是滚动失败后是否回填上一镜像("false" 关闭,其余含空 = 开)。
	CfgKeyAutoRollback = "autoRollback"
	// CfgKeyManifestSource 是发布方式:none(只换镜像,默认)| repo | paste。DAG 已把后两者
	// 收敛成一份 CfgKeyManifestYaml 正文,这里读它只为分辨走哪条腿。
	CfgKeyManifestSource = "manifestSource"
	// CfgKeyManifestFile 是清单的来源仓库路径(仅日志用:正文由 DAG 现读并填进
	// CfgKeyManifestYaml,记一下它原本是哪份文件,免得排查时对着库里那份猜来源)。
	CfgKeyManifestFile = "manifestFile"
	// CfgKeyManifestYaml 是交给集群的清单正文(多文档以 --- 分隔,可含 {{IMAGE}} 占位符)。
	CfgKeyManifestYaml = "manifestYaml"

	// ManifestSource* 与 pipeline.ManifestSource* 同值。
	ManifestSourceNone  = "none"
	ManifestSourcePaste = "paste"
	ManifestSourceRepo  = "repo"
)

// ImagePlaceholder 是清单里指向「本次构建出的那件镜像」的内置占位符。它由部署层替换
// (只有这里知道本次挑中的是哪件产物),其余变量在 DAG 里就该渲染完。
const ImagePlaceholder = "IMAGE"

const (
	// defaultRolloutTimeout 给到 5 分钟:首次在新节点上跑的镜像要拉层,分钟级很正常。
	defaultRolloutTimeout = 5 * time.Minute
	// maxRolloutTimeout 是单次等待上限(30 分钟),防手滑填个「一天」把执行器占死。
	maxRolloutTimeout = 30 * time.Minute
	// rolloutPollInterval 是读一次负载状态的间隔:再密也只是多打几次 GET,滚不动不会变快。
	rolloutPollInterval = 3 * time.Second
	// manifestMaxBytes 与 pipeline 保存期校验、deploy_manifests.body 同一额度:MySQL 的 TEXT
	// 装不下 64 KiB 以上,超量是**静默截断** —— 回滚时会重新应用一份被切掉的清单。
	manifestMaxBytes = 64 << 10
)

// 领域错误(定位类,上抛给 HTTP / DAG 层映射成人读失败)。
var (
	// ErrKubeUnavailable 表示集群服务未注入(平台未启用 k8s 能力)。
	ErrKubeUnavailable = errors.New("deploy: kube service not configured")
	// ErrClusterNotFound 表示指定的集群不存在。
	ErrClusterNotFound = errors.New("deploy: kube cluster not found")
	// ErrImageArtifactRequired 表示本次挑到的产物不是镜像 —— 集群没有「铺文件」这个发布法。
	ErrImageArtifactRequired = errors.New("deploy: k8s release requires an image artifact")
	// ErrK8sConfigIncomplete 表示集群发布的必填项没填全。
	ErrK8sConfigIncomplete = errors.New("deploy: k8s release config incomplete")
)

// K8sRouteOf 报告这份 cfg 是不是要发到集群(deploy_k8s 节点的 cfg 必带 clusterId)。
// 部署层用它分腿:是集群就不进逐机策略链路(见 DeployForStage)。
func K8sRouteOf(cfg map[string]string) bool {
	return strings.TrimSpace(cfg[CfgKeyClusterID]) != ""
}

// k8sPlan 是一次集群发布的入参(从 cfg 收敛而来)。
type k8sPlan struct {
	clusterID    string
	workload     kube.Workload
	container    string
	timeout      time.Duration
	autoRollback bool
	// manifest 非空即「按清单发布」这条腿:正文已由 DAG 收敛(仓库文件或节点粘贴),
	// 此时 workload 的 Name/Kind 不参与寻址 —— 发出去的是清单里那几个对象。
	manifest    string
	manifestSrc string // 人读来源("节点粘贴" / "仓库文件 deploy/k8s.yaml"),只进日志
}

// usesManifest 报告这次发布走哪条腿。
func (p k8sPlan) usesManifest() bool { return p.manifest != "" }

// k8sPlanOf 从 cfg 组装发布计划;必填项缺失 → ErrK8sConfigIncomplete(带具体缺哪个)。
func k8sPlanOf(cfg map[string]string) (k8sPlan, error) {
	p := k8sPlan{
		clusterID: strings.TrimSpace(cfg[CfgKeyClusterID]),
		workload: kube.Workload{
			Kind:      strings.TrimSpace(cfg[CfgKeyWorkloadKind]),
			Namespace: strings.TrimSpace(cfg[CfgKeyNamespace]),
			Name:      strings.TrimSpace(cfg[CfgKeyWorkloadName]),
		},
		container:    strings.TrimSpace(cfg[CfgKeyK8sContainer]),
		timeout:      defaultRolloutTimeout,
		autoRollback: strings.TrimSpace(cfg[CfgKeyAutoRollback]) != "false",
	}
	if p.workload.Kind == "" {
		p.workload.Kind = "Deployment" // 与前端默认值同一收敛点
	}
	if secs := cfgInt(cfg, CfgKeyRolloutTimeout, 0); secs > 0 {
		p.timeout = time.Duration(secs) * time.Second
		if p.timeout > maxRolloutTimeout {
			p.timeout = maxRolloutTimeout
		}
	}
	var missing []string
	manifestMode := false
	switch src := strings.TrimSpace(cfg[CfgKeyManifestSource]); src {
	case "", ManifestSourceNone:
		// 只换镜像:寻址靠 clusterId + workloadName。
	case ManifestSourcePaste, ManifestSourceRepo:
		manifestMode = true
		body := strings.TrimSpace(cfg[CfgKeyManifestYaml])
		switch {
		case body == "":
			// DAG 会在读仓库文件/取粘贴正文时先失败;走到这里说明有人绕过了保存(改库、旧 .pipewright.yml)。
			missing = append(missing, "清单正文")
		case len(body) > manifestMaxBytes:
			return k8sPlan{}, fmt.Errorf("%w:清单正文 %d 字节,超过 %d KiB 上限(超量会被数据库静默截断,回滚时就应用一份缺尾的声明)",
				ErrK8sConfigIncomplete, len(body), manifestMaxBytes>>10)
		}
		p.manifest = body
		p.manifestSrc = "节点粘贴的正文"
		if f := strings.TrimSpace(cfg[CfgKeyManifestFile]); src == ManifestSourceRepo && f != "" {
			p.manifestSrc = "仓库文件 " + f
		}
	default:
		return k8sPlan{}, fmt.Errorf("%w:清单来源 %q 非法(仅 none / repo / paste)", ErrK8sConfigIncomplete, src)
	}
	if !manifestMode && p.workload.Name == "" {
		missing = append(missing, "负载名")
	}
	// 命名空间**不在这里必填**:留空是「用集群登记的默认命名空间」的常规写法,而默认值长在
	// 那一行记录上,只有逐集群阶段才拿得到(见 releaseOneCluster)。清单同理 —— 清单自己写了
	// metadata.namespace 时压根不需要默认值,所以那条判定也推迟到那一层。
	if len(missing) > 0 {
		return k8sPlan{}, fmt.Errorf("%w:缺少 %s", ErrK8sConfigIncomplete, strings.Join(missing, " / "))
	}
	return p, nil
}

// deployToK8s 逐集群(本期一个节点一个集群)执行集群发布,返回每集群结果。
//
// 执行失败**不上抛**(该集群 status=failed / rolled_back + 人读 message);定位类错误
// (集群不存在、产物不是镜像、kubeconfig 不可用)上抛,由调用方在发请求前挡下。
// runID 只用于一处:回滚查「上一版」时要把本次排除掉(重发同一 run 时库里已有本批正文)。
func (s *service) deployToK8s(ctx context.Context, runID string, clusterIDs []string, cfg map[string]string, a run.Artifact) ([]TargetResult, error) {
	if s.kube == nil {
		return nil, ErrKubeUnavailable
	}
	if a.Type != run.ArtifactImage {
		return nil, fmt.Errorf("%w:本次产物是 %s;集群只认镜像(上游构建改「镜像」档位,或改用 SSH / docker 节点发文件)", ErrImageArtifactRequired, a.Type)
	}
	plan, err := k8sPlanOf(cfg)
	if err != nil {
		return nil, err
	}
	image := strings.TrimSpace(a.Reference)
	if image == "" {
		return nil, fmt.Errorf("%w:镜像产物的引用为空", ErrImageArtifactRequired)
	}
	if !looksLikeRemoteImage(image) {
		// 不判死:公共镜像 / 已在节点上的镜像也能跑。但这是「超时且没人知道为什么」的头号成因,
		// 所以把话讲在前头。
		cmdLogFrom(ctx)(cmdStreamStdout, "· 提示:镜像引用没有仓库主机,集群将按 docker.io 解析;自建镜像请先在「触发设置 → 环境」绑定镜像仓库")
	}

	out := make([]TargetResult, 0, len(clusterIDs))
	for _, cid := range clusterIDs {
		res, err := s.releaseOneCluster(ctx, runID, cid, plan, image)
		if err != nil {
			if errors.Is(err, kube.ErrNotFound) {
				return nil, ErrClusterNotFound
			}
			return nil, err // 凭据 / 保险库类:整次发布停在开始执行之前
		}
		out = append(out, res)
	}
	return out, nil
}

// releaseOneCluster 在一个集群上完成「定下命名空间 → 换镜像或应用清单 → 等滚动 → 失败回退」一整圈。
//
// 返回值约定与 SSH 那条腿一致:只有「这个目标根本没法发」(集群/凭据不存在、kubeconfig 不可用)
// 才上抛 error;已经开始发但没发成的,折成 failed / rolled_back 结果 + nil。
func (s *service) releaseOneCluster(ctx context.Context, runID, clusterID string, plan k8sPlan, image string) (TargetResult, error) {
	started := time.Now().UTC()
	cluster, err := s.kube.Get(ctx, clusterID)
	if err != nil {
		return TargetResult{}, err
	}
	label := targetLabel(cluster.Name, plan.workload)
	finish := func(status, message string, docs ...run.ManifestDoc) (TargetResult, error) {
		fin := time.Now().UTC()
		return TargetResult{
			ServerID: clusterID, ServerName: label,
			Status: status, Message: message, StartedAt: started, FinishedAt: &fin,
			Manifests: docs,
		}, nil
	}
	// 留空 = 用集群登记的默认命名空间。这一层兜底是「K8s 集群」页那个字段的唯一去处,
	// 不接就成了一格填了没用的表单。
	if plan.workload.Namespace == "" {
		plan.workload.Namespace = strings.TrimSpace(cluster.NamespaceDefault)
	}
	client, err := s.kube.ClientFor(ctx, clusterID)
	if err != nil {
		return TargetResult{}, err
	}
	if plan.workload.Namespace == "" {
		// 再退一步:kubeconfig 的 context 里通常也写了一个 namespace。
		plan.workload.Namespace = strings.TrimSpace(client.NamespaceHint())
	}
	if plan.usesManifest() {
		// 一次清单发布涉及好几个对象,展示名记到命名空间这一层(各对象名列在 message 里)。
		// 命名空间为空**不在这里判死**:清单自己写了 metadata.namespace 时压根不需要默认值,
		// 「两处都没有」由 kube.ParseManifests 逐份拒绝(见那层「拒绝猜测 default」)。
		label = manifestLabel(cluster.Name, plan.workload.Namespace)
		return s.releaseByManifest(ctx, runID, client, clusterID, plan, image, finish)
	}
	if plan.workload.Namespace == "" {
		return finish(run.TargetFailed, "未填命名空间,且该集群既没登记默认命名空间、kubeconfig 的 context 里也没有")
	}
	// 展示名要带上最终生效的命名空间(finish 闭包按引用读 label,这里改一次即随之下沉)。
	label = targetLabel(cluster.Name, plan.workload)
	st, err := client.Get(ctx, plan.workload)
	if err != nil {
		return finish(run.TargetFailed, kubeHumanError(err))
	}
	container, err := st.ResolveContainer(plan.container)
	if err != nil {
		return finish(run.TargetFailed, err.Error())
	}
	prevImage, err := st.ImageOf(container)
	if err != nil {
		return finish(run.TargetFailed, err.Error())
	}
	cmdLogFrom(ctx)(cmdStreamStdout, fmt.Sprintf("· 目标 %s,容器 %s 当前镜像 %s", plan.workload, container, prevImage))

	// 引用没变:直接 PATCH 是一次空操作(控制器不会滚),补一次 restart 才算真发出去了。
	unchanged := prevImage == image
	if unchanged {
		cmdLogFrom(ctx)(cmdStreamStdout, "· 镜像引用与当前一致:改走 restartedAt,让 pod 真的重建一次")
		err = client.Restart(ctx, plan.workload)
	} else {
		err = client.SetImage(ctx, plan.workload, container, image)
	}
	if err != nil {
		return finish(run.TargetFailed, kubeHumanError(err))
	}
	cmdLogFrom(ctx)(cmdStreamStdout, fmt.Sprintf("→ PATCH %s(%s)", plan.workload, ifElse(unchanged, "rollout restart", "image="+image)))

	_, werr := client.WaitRollout(ctx, plan.workload, plan.timeout, rolloutPollInterval)
	if werr == nil {
		return finish(run.TargetSuccess, fmt.Sprintf("滚动完成:%s → %s", plan.workload, image))
	}
	if ctx.Err() != nil {
		return finish(run.TargetFailed, "发布被取消")
	}
	reason := kubeHumanError(werr)
	if !plan.autoRollback {
		return finish(run.TargetFailed, "滚动未成功且未开启自动回滚:"+reason)
	}
	if prevImage == image {
		// 同一引用滚不动:回填旧值等于回填新值,回不去,只能人工看。
		return finish(run.TargetFailed, "滚动未成功:"+reason+";镜像引用未变,无上一版本可回滚")
	}
	rbErr := client.SetImage(ctx, plan.workload, container, prevImage)
	if rbErr != nil {
		return finish(run.TargetFailed, fmt.Sprintf("滚动未成功(%s),且回滚也失败(%s)—— 请人工检查该负载", reason, kubeHumanError(rbErr)))
	}
	// 回填后不等待:此刻集群正在往旧版本滚,报「已回滚」比再占 5 分钟更符合用户要看到的事实。
	return finish(run.TargetRolledBack, fmt.Sprintf("滚动未成功(%s),已回填上一镜像 %s", reason, prevImage))
}

// targetFinisher 收尾一个集群目标的结果(见 releaseOneCluster)。docs 是本次实际交到集群手里的
// 清单正文,只有清单那条腿会带它。
type targetFinisher func(status, message string, docs ...run.ManifestDoc) (TargetResult, error)

// releaseByManifest 是「应用清单」这条腿:按清单里的顺序把每一份声明交给集群(server-side
// apply),负载再等它滚完;某一份滚不动时,把**该对象上一版实际应用的正文**重新应用回去。
//
// 与换镜像那条腿的根本区别是「以这份声明为准」:换镜像只碰一个字段,而这里整份规格是输入,
// 所以回滚回填的也只能是一整份上一版声明 —— 那正是 deploy_manifests 那张表存在的理由
// (见 internal/run/deploy_manifests.go)。所以这里的回滚不查 ReplicaSet,而是查我们自己
// 记下的上一版:它才是「上一次成功发出去的东西」。
func (s *service) releaseByManifest(ctx context.Context, runID string, client *kube.Client, clusterID string, plan k8sPlan, image string, finish targetFinisher) (TargetResult, error) {
	lg := cmdLogFrom(ctx)
	body, hasImage := replaceImagePlaceholder(plan.manifest, image)
	if !hasImage {
		// 不判死:清单自己钉住某个 tag 也是写法。但必须说在前头 —— 否则这次「发布成功」里
		// 根本没有本次构建的那件镜像。
		lg(cmdStreamStdout, "· 提示:清单里没有 {{IMAGE}} 占位符,本次构建的镜像 "+image+" 未被引用;镜像以清单写的为准")
	}
	docs, err := kube.ParseManifests(body, plan.workload.Namespace)
	if err != nil {
		return finish(run.TargetFailed, "清单不可用:"+kubeHumanError(err))
	}
	lg(cmdStreamStdout, fmt.Sprintf("· 清单取自%s,共 %d 份声明,按序应用", plan.manifestSrc, len(docs)))

	applied := make([]run.ManifestDoc, 0, len(docs))
	workloads, rolled := 0, false
	for _, d := range docs {
		w, isWorkload := d.AsWorkload()
		var genBefore int64
		if isWorkload {
			// 先读一次 generation:滚没滚要靠它判,不能只看我们这次 apply 成没成。
			// 读不动不拦 apply —— 首次发布时对象本就不存在,真有问题 apply 会自己报。
			if st, gerr := client.Get(ctx, w); gerr == nil {
				genBefore = st.Generation
			}
		}
		created, aerr := client.Apply(ctx, d)
		if aerr != nil {
			// 已应用的那几份如实留在结果里:集群里现在确实有它们。
			return finish(run.TargetFailed, fmt.Sprintf("应用 %s 失败:%s", d, kubeHumanError(aerr)), applied...)
		}
		lg(cmdStreamStdout, fmt.Sprintf("→ APPLY %s(%s)", d, ifElse(created, "新建", "已存在,按声明收敛")))
		doc := run.ManifestDoc{Kind: d.Kind, Namespace: d.Namespace, Name: d.Name, Ordinal: d.DocNum, Body: d.Body}
		applied = append(applied, doc)

		if !isWorkload {
			continue // Service / ConfigMap / Ingress:应用成功即终态,没有「滚」可等
		}
		workloads++
		st, werr := client.WaitRollout(ctx, w, plan.timeout, rolloutPollInterval)
		if werr == nil {
			switch {
			case created || st.Generation > genBefore:
				rolled = true
			default:
				lg(cmdStreamStdout, "· "+d.String()+" 规格与集群现状一致,控制器没有滚动")
			}
			continue
		}
		if ctx.Err() != nil {
			return finish(run.TargetFailed, "发布被取消", applied...)
		}
		reason := kubeHumanError(werr)
		if !plan.autoRollback {
			return finish(run.TargetFailed, fmt.Sprintf("%s 滚动未成功且未开启自动回滚:%s", d, reason), applied...)
		}
		prev, lerr := s.runs.LastAppliedManifest(ctx, clusterID, d.Kind, d.Namespace, d.Name, runID)
		if lerr != nil {
			return finish(run.TargetFailed, fmt.Sprintf("%s 滚动未成功(%s),而上一版清单查不出来:%s —— 未回滚,请人工检查该负载", d, reason, kubeHumanError(lerr)), applied...)
		}
		if prev == nil {
			return finish(run.TargetFailed, fmt.Sprintf("%s 滚动未成功(%s),且这是它第一次发布 —— 没有上一版清单可回滚", d, reason), applied...)
		}
		pdocs, perr := kube.ParseManifests(prev.Body, d.Namespace)
		if perr != nil || len(pdocs) != 1 {
			// 库里那份读不回一份可用声明(手改过库、换过 kubeconfig 默认命名空间):宁可不回滚,
			// 也不能凭猜应用一份东西。
			msg := "重新解析后不是恰好一份声明"
			if perr != nil {
				msg = kubeHumanError(perr)
			}
			return finish(run.TargetFailed, fmt.Sprintf("%s 滚动未成功(%s),而上一版清单不可用:%s —— 未回滚,请人工检查该负载", d, reason, msg), applied...)
		}
		if _, rbErr := client.Apply(ctx, pdocs[0]); rbErr != nil {
			return finish(run.TargetFailed, fmt.Sprintf("%s 滚动未成功(%s),且回滚也失败(%s)—— 请人工检查该负载", d, reason, kubeHumanError(rbErr)), applied...)
		}
		// 集群里现在是上一版,入库的那份也要改成它 —— 否则「这次到底发了什么」记的是假账。
		applied[len(applied)-1] = run.ManifestDoc{Kind: d.Kind, Namespace: d.Namespace, Name: d.Name, Ordinal: d.DocNum, Body: pdocs[0].Body}
		lg(cmdStreamStdout, "· 已重新应用 "+d.String()+" 的上一版清单")
		return finish(run.TargetRolledBack, fmt.Sprintf("%s 滚动未成功(%s),已重新应用上一版清单", d, reason), applied...)
	}

	msg := fmt.Sprintf("已应用 %d 份清单:%s", len(docs), strings.Join(manifestNames(docs), "、"))
	switch {
	case workloads > 0 && !rolled:
		// 规格与集群现状一致时控制器什么都不做 —— 这也是「成功」(期望状态已达成),
		// 但不能报成「滚动完成」:那会让人以为这次换上了一件新镜像。
		msg += "(规格无变化,未触发滚动)"
	case workloads > 0:
		msg += ",滚动完成"
	}
	return finish(run.TargetSuccess, msg, applied...)
}

// manifestNames 是人读的对象清单(最多列 4 个,再多以总数收尾)。
func manifestNames(docs []kube.Manifest) []string {
	const maxNames = 4
	out := make([]string, 0, min(len(docs), maxNames+1))
	for i, d := range docs {
		if i == maxNames {
			out = append(out, fmt.Sprintf("…共 %d 份", len(docs)))
			break
		}
		out = append(out, d.String())
	}
	return out
}

// replaceImagePlaceholder 把清单里的 {{IMAGE}} 换成本次镜像引用,返回是否换到过。
// 用 ReplaceAllStringFunc 而非 ReplaceAllString:镜像引用里出现 `$` 时后者会被当展开语法。
func replaceImagePlaceholder(text, image string) (string, bool) {
	found := false
	out := reImagePlaceholder.ReplaceAllStringFunc(text, func(string) string {
		found = true
		return image
	})
	return out, found
}

var reImagePlaceholder = regexp.MustCompile(`\{\{\s*IMAGE\s*\}\}`)

// manifestLabel 是清单发布的展示名:一次发好几个对象,记到命名空间这一层比记某一个负载名诚实。
func manifestLabel(clusterName, namespace string) string {
	if strings.TrimSpace(namespace) == "" {
		return clusterName
	}
	return fmt.Sprintf("%s · %s", clusterName, namespace)
}

// targetLabel 是写入 deploy_targets 的展示名:一眼看出发到了哪个集群的哪个负载。
func targetLabel(clusterName string, w kube.Workload) string {
	return fmt.Sprintf("%s · %s/%s", clusterName, w.Namespace, w.Name)
}

// kubeHumanError 把 kube 层错误折成部署日志里的人读原因(绝不含凭据 / 请求头)。
func kubeHumanError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, kube.ErrDenied):
		return "API server 拒绝了请求:凭据无效或该 ServiceAccount 没有相应权限(" + trimMsg(err) + ")"
	case errors.Is(err, kube.ErrUnreachable):
		return "连不上 API server:" + trimMsg(err)
	case errors.Is(err, kube.ErrWorkloadNotFound):
		return "目标负载不存在(检查命名空间与名字)"
	case errors.Is(err, kube.ErrKubeConfigInvalid):
		return "集群凭据里的 kubeconfig 不可用"
	case errors.Is(err, kube.ErrNoContainer):
		return err.Error()
	case errors.Is(err, kube.ErrManifestInvalid):
		return stripSentinel(err, "manifest is not applicable")
	case errors.Is(err, kube.ErrNamespaceMissing):
		return stripSentinel(err, "namespace not found")
	default:
		return trimMsg(err)
	}
}

// stripSentinel 去掉错误串里那句英文哨兵(连同紧随的冒号):用户能行动的信息从「第 N 份
// 文档…」起才是重点,而哨兵文本既看不懂又占掉 message 的开头。
func stripSentinel(err error, sentinel string) string {
	msg := strings.TrimPrefix(strings.TrimSpace(err.Error()), "kube: ")
	msg = strings.TrimPrefix(msg, sentinel+":")
	msg = strings.TrimPrefix(msg, sentinel)
	if len(msg) > truncateLen {
		msg = msg[:truncateLen] + "…"
	}
	return msg
}

// trimMsg 去掉包前缀并截断,防止错误体把日志撑爆。
func trimMsg(err error) string {
	msg := strings.TrimSpace(err.Error())
	msg = strings.TrimPrefix(msg, "kube: ")
	if len(msg) > truncateLen {
		msg = msg[:truncateLen] + "…"
	}
	return msg
}

// looksLikeRemoteImage 判镜像引用是否带仓库主机(与 docker 的解析规则同一思路:
// 首段含 `.` 或 `:` 才是主机名,否则是 docker.io 的命名空间)。
func looksLikeRemoteImage(ref string) bool {
	first := ref
	if i := strings.Index(ref, "/"); i > 0 {
		first = ref[:i]
	} else {
		return false // 没有斜杠 = 裸镜像名,必然落在 docker.io/library
	}
	return strings.ContainsAny(first, ".:") || first == "localhost"
}

func ifElse(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
