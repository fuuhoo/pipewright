package ai

// 节点目录(AI 生成流水线的「可用工具清单」)。
//
// 痛点:旧 prompt 只举 git_source/build_image/deploy 三种 type,LLM 不知道产品其余节点存在,
// 只会产最粗的三段式。这里把**全部可用节点**作为工具清单喂给 LLM:内置节点(下方 Go 目录,
// 新增模板节点在此登记即自动生效)+ 复用库里的用户自定义节点(运行时从 DB 动态拼入,见
// httpapi 装配)。目录与前端 jobConfigSchema 的节点种类对齐,改动时请同步两侧。

// NodeKind 是一个可用节点的描述(喂给 LLM 的工具条目)。
type NodeKind struct {
	Type        string // job.type(LLM 只能从目录里选)
	Label       string // 人读名
	Category    string // source|build|deploy|quality|notify|custom
	Description string // 用途 + 关键配置/适用场景(给 LLM 判断何时用)
	Custom      bool   // true = 复用库里的用户自定义节点(按 Label 名称选用,type 通常为 templated)
}

// BuiltinNodeCatalog 返回内置节点的工具清单(新增内置/模板节点在此登记即生效)。
// 顺序按典型流水线推进(源 → 构建 → 部署 → 通知),便于 LLM 组合。
func BuiltinNodeCatalog() []NodeKind {
	return []NodeKind{
		{Type: "git_source", Label: "Gitee 源", Category: "source",
			Description: "拉取 Git 仓库源码到构建工作区。每条流水线必须恰有一个 source 阶段,含一个 git_source。"},
		{Type: "build", Label: "构建", Category: "build",
			Description: "唯一的构建任务,config.artifactType 必须先选档位:" +
				"image = 构建 Docker 镜像(buildModel=dockerfile 时给 dockerfilePath/context;toolchain 时选 buildEnvId + buildCommand)," +
				"file = 产物(工作区里的一个文件或目录):在预置构建环境容器里跑 commands(多行),用 artifactPath 收产物。" +
				"档位=image 时 pushImage=false 表示只构建不推送(缺省推送到运行环境绑定的镜像仓,不需要单独的推送节点)。" +
				"需要镜像部署就用 artifactType=image;前端/后端项目用档位 file 并预填各自构建命令。"},
		{Type: "script", Label: "自定义脚本", Category: "build",
			Description: "隔离容器内执行任意命令(跑测试、lint、代码扫描、自定义步骤等)。"},
		{Type: "deploy_ssh", Label: "部署", Category: "deploy",
			Description: "经 SSH 把产物(jar/dist/image)或命令部署到目标服务器。可选 healthProbe 做部署后健康门控(不通则该节点失败)。前端静态站点部署也用它:artifactType=dist + strategy=rolling + restartCommand=\"nginx -s reload\"。"},
		{Type: "deploy_docker", Label: "Docker 部署", Category: "deploy",
			Description: "在目标机以 docker 交付,config.dockerMode 必填两选一:" +
				"run = 单容器(发上游构建出的镜像,停旧起新、失败回滚上一镜像),配 containerName/ports/runArgs;" +
				"compose = 整份 docker-compose.yml + 项目名(config.stackName),交目标机的 compose CLI 编排;" +
				"正文来源二选一:composeSource=repo + composeFile(读项目仓库里那份文件,随仓库演进)或 composeSource=paste + composeYaml(把正文粘在节点里)。" +
				"仓库里有 docker-compose.yml 就选 compose + composeSource=repo;两种方式都可选 healthProbe 做部署后健康门控。"},
		{Type: "deploy_k8s", Label: "K8s 发布", Category: "deploy",
			Description: "直接把上游构建出的镜像发到 Kubernetes 集群(平台直连集群 API,不经目标机 SSH)。" +
				"config.clusterId 选集群、workloadName 必填、namespace 可留空(留空 = 用集群登记的默认命名空间)、" +
				"workloadKind(Deployment|StatefulSet,缺省 Deployment);" +
				"多容器工作负载要给 containerName。只支持滚动:换镜像后等集群滚完即成败判据,不支持 healthProbe 与其它发布策略;" +
				"且只换已有负载的镜像,不创建负载。"},
		{Type: "notify", Label: "通知", Category: "notify",
			Description: "运行到此节点时向已配渠道(飞书/Webhook/邮件)发通知,支持标题/正文模板。"},
		{Type: "templated", Label: "自定义节点", Category: "custom",
			Description: "用户自定义节点:参数 + 命令模板({{参数}}),用于产品未内置的步骤。"},
	}
}
