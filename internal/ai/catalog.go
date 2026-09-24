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
		{Type: "notify", Label: "通知", Category: "notify",
			Description: "运行到此节点时向已配渠道(飞书/Webhook/邮件)发通知,支持标题/正文模板。"},
		{Type: "templated", Label: "自定义节点", Category: "custom",
			Description: "用户自定义节点:参数 + 命令模板({{参数}}),用于产品未内置的步骤。"},
	}
}
