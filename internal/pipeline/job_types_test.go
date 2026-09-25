package pipeline

import (
	"errors"
	"strings"
	"testing"
)

func specWithJobs(jobs ...Job) Spec {
	return Spec{Stages: []Stage{
		{ID: "src", Name: "流水线源", Kind: KindSource, Jobs: []Job{{ID: "jsrc", Name: "拉取源码", Type: "git_source", Config: map[string]any{}}}},
		{ID: "stg", Name: "部署", Kind: KindDeploy, Jobs: jobs},
	}}
}

// 撤销的类型既不静默放行也不迁移:保存期直接拒绝,并把改配指引写进报错。
func TestNormalizeSpecRejectsRetiredJobType(t *testing.T) {
	_, err := normalizeSpec(specWithJobs(Job{ID: "j1", Name: "健康检查", Type: "health_check", Config: map[string]any{}}))
	if !errors.Is(err, ErrJobTypeRetired) {
		t.Fatalf("err = %v, want wraps ErrJobTypeRetired", err)
	}
	msg := err.Error()
	for _, want := range []string{"健康检查", "health_check", "部署任务"} {
		if !strings.Contains(msg, want) {
			t.Errorf("报错应含 %q,got %q", want, msg)
		}
	}
	// 422 原样回显该消息:哨兵的英文前缀不能混进去。
	if strings.Contains(msg, "pipeline:") {
		t.Errorf("报错不应含哨兵前缀,got %q", msg)
	}
}

func TestRetiredJobTypeTrimsAndIgnoresOthers(t *testing.T) {
	if _, ok := RetiredJobType(" health_check "); !ok {
		t.Error("带空白的类型也应判为已撤销")
	}
	if _, ok := RetiredJobType("deploy_ssh"); ok {
		t.Error("deploy_ssh 不应判为已撤销")
	}
}

// 健康探测的半截配置会静默退化成「不探测」(假绿),保存期必须挡住。
func TestNormalizeSpecValidatesHealthProbe(t *testing.T) {
	cases := []struct {
		name    string
		cfg     map[string]any
		wantErr bool
	}{
		{"未配探测", map[string]any{"serverId": "s1"}, false},
		{"显式不探测", map[string]any{"healthProbe": "none"}, false},
		{"http 缺 url", map[string]any{"healthProbe": "http"}, true},
		{"http 有 url", map[string]any{"healthProbe": "http", "healthUrl": "http://localhost:8080/healthz"}, false},
		{"命令探测缺命令", map[string]any{"healthProbe": "command"}, true},
		{"命令探测有命令", map[string]any{"healthProbe": "command", "healthCommand": "test -f /opt/app/OK"}, false},
		{"探测方式非法", map[string]any{"healthProbe": "tcp"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normalizeSpec(specWithJobs(Job{ID: "j1", Name: "SSH 部署", Type: "deploy_ssh", Config: tc.cfg}))
			if tc.wantErr && !errors.Is(err, ErrHealthProbeInvalid) {
				t.Fatalf("err = %v, want wraps ErrHealthProbeInvalid", err)
			}
			if tc.wantErr && strings.Contains(err.Error(), "pipeline:") {
				t.Errorf("报错不应含哨兵前缀,got %q", err.Error())
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("want no error, got %v", err)
			}
		})
	}
}

// 探测键只在部署类型上校验:脚本节点写个同名的无关键不该被拦。
func TestHealthProbeOnlyAppliesToDeployJobs(t *testing.T) {
	_, err := normalizeSpec(Spec{Stages: []Stage{
		{ID: "src", Name: "流水线源", Kind: KindSource, Jobs: []Job{{ID: "jsrc", Name: "拉取源码", Type: "git_source", Config: map[string]any{}}}},
		{ID: "stg", Name: "构建", Kind: KindBuild, Jobs: []Job{
			{ID: "j1", Name: "脚本", Type: "script", Config: map[string]any{"healthProbe": "http"}},
		}},
	}})
	if err != nil {
		t.Fatalf("非部署节点不该被探测校验拦下,got %v", err)
	}
}

// 「构建」任务按产物档位折算成真正执行它的类型:档位选错路径就完全不一样,
// 派发/校验只认折算结果(镜像 → build_image,产物 → script)。
func TestEffectiveJobTypeByArtifactTier(t *testing.T) {
	cases := []struct {
		name    string
		jobType string
		cfg     map[string]any
		want    string
	}{
		{"镜像档位", JobTypeBuild, map[string]any{ConfigKeyArtifactType: ArtifactImage}, JobTypeBuildImage},
		{"产物档位", JobTypeBuild, map[string]any{ConfigKeyArtifactType: ArtifactFile}, StepTypeScript},
		{"历史 jar 档位", JobTypeBuild, map[string]any{ConfigKeyArtifactType: ArtifactJAR}, StepTypeScript},
		{"历史 dist 档位", JobTypeBuild, map[string]any{ConfigKeyArtifactType: ArtifactDist}, StepTypeScript},
		{"档位留空", JobTypeBuild, map[string]any{}, StepTypeScript},
		{"旧镜像类型原样", JobTypeBuildImage, map[string]any{ConfigKeyArtifactType: ArtifactJAR}, JobTypeBuildImage},
		{"脚本类型原样", "script", map[string]any{}, "script"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveJobType(tc.jobType, tc.cfg); got != tc.want {
				t.Fatalf("EffectiveJobType = %q, want %q", got, tc.want)
			}
		})
	}
}

// 档位缺失 = 执行侧无从判断该跑脚本还是 build docker,保存期必须挡住(而非猜一条路径)。
func TestNormalizeSpecRequiresBuildArtifactTier(t *testing.T) {
	_, err := normalizeSpec(specWithJobs(Job{ID: "j1", Name: "构建", Type: JobTypeBuild, Config: map[string]any{}}))
	if !errors.Is(err, ErrBuildTaskInvalid) {
		t.Fatalf("err = %v, want wraps ErrBuildTaskInvalid", err)
	}
	if strings.Contains(err.Error(), "pipeline:") {
		t.Errorf("报错不应含哨兵前缀,got %q", err.Error())
	}
	for _, tier := range []string{ArtifactImage, ArtifactFile, ArtifactJAR, ArtifactDist} {
		if _, err := normalizeSpec(specWithJobs(Job{ID: "j1", Name: "构建", Type: JobTypeBuild, Config: map[string]any{ConfigKeyArtifactType: tier}})); err != nil {
			t.Fatalf("档位 %s 应通过校验,got %v", tier, err)
		}
	}
}

// 构建环境是否必填也按折算后的类型判定:镜像档位走 docker build(模型 A)时不该要平台环境,
// 工具链镜像与 jar/dist 才进隔离容器。
func TestNeedsBuildEnvFollowsEffectiveType(t *testing.T) {
	cases := []struct {
		name    string
		jobType string
		cfg     map[string]any
		want    bool
	}{
		{"构建-jar", JobTypeBuild, map[string]any{ConfigKeyArtifactType: ArtifactJAR}, true},
		{"构建-dist", JobTypeBuild, map[string]any{ConfigKeyArtifactType: ArtifactDist}, true},
		{"构建-镜像-工具链", JobTypeBuild, map[string]any{ConfigKeyArtifactType: ArtifactImage, ConfigKeyBuildModel: BuildModelToolchain}, true},
		{"构建-镜像-dockerfile", JobTypeBuild, map[string]any{ConfigKeyArtifactType: ArtifactImage, ConfigKeyBuildModel: BuildModelDockerfile}, false},
		{"旧镜像类型-dockerfile", JobTypeBuildImage, map[string]any{ConfigKeyBuildModel: BuildModelDockerfile}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NeedsBuildEnv(tc.jobType, tc.cfg); got != tc.want {
				t.Fatalf("NeedsBuildEnv = %v, want %v", got, tc.want)
			}
		})
	}
}

// 推送开关只有明确的关闭值才关:缺省必须保持旧行为(构建出镜像即推送)。
func TestPushImageEnabled(t *testing.T) {
	for _, off := range []string{"false", "FALSE", " false ", "0", "no"} {
		if PushImageEnabled(map[string]any{ConfigKeyPushImage: off}) {
			t.Errorf("pushImage=%q 应判为不推送", off)
		}
	}
	for _, on := range []string{"", "true", "yes", "1", "随便"} {
		if !PushImageEnabled(map[string]any{ConfigKeyPushImage: on}) {
			t.Errorf("pushImage=%q 应判为推送", on)
		}
	}
	if !PushImageEnabled(map[string]any{}) {
		t.Error("缺省应推送(旧行为)")
	}
}

// deploySpec 拼一条「源 + 若干自定义阶段」的 spec,供产物来源校验用例复用。
func deploySpec(stages ...Stage) Spec {
	return Spec{Stages: append([]Stage{
		{ID: "src", Name: "流水线源", Kind: KindSource, Jobs: []Job{{ID: "jsrc", Name: "拉取源码", Type: "git_source", Config: map[string]any{}}}},
	}, stages...)}
}

// 产物来源任务存的是 job ID:引用不存在的任务、引用自己、引用不产产物的节点、
// 引用还没跑完的(并行 / 下游)任务,全都该在保存期报错 —— 拖到运行时只剩一句「没有产物」。
func TestNormalizeSpecValidatesArtifactSource(t *testing.T) {
	backend := Job{ID: "japi", Name: "后端构建", Type: "script", Config: map[string]any{"commands": "go build"}}
	frontend := Job{ID: "jweb", Name: "前端构建", Type: "script", Config: map[string]any{"commands": "npm run build"}}
	deploy := func(cfg map[string]any) Job {
		return Job{ID: "jdep", Name: "部署", Type: "deploy_ssh", Config: cfg}
	}
	cases := []struct {
		name    string
		stages  []Stage
		wantErr bool
	}{
		{
			name:   "留空 = 不校验",
			stages: []Stage{{ID: "b", Name: "构建", Kind: KindBuild, Jobs: []Job{backend}}, {ID: "d", Name: "部署", Kind: KindDeploy, Jobs: []Job{deploy(map[string]any{})}}},
		},
		{
			name:   "上游阶段的构建任务",
			stages: []Stage{{ID: "b", Name: "构建", Kind: KindBuild, Jobs: []Job{backend}}, {ID: "d", Name: "部署", Kind: KindDeploy, Jobs: []Job{deploy(map[string]any{ConfigKeyArtifactFrom: "japi"})}}},
		},
		{
			name:    "任务不存在",
			stages:  []Stage{{ID: "b", Name: "构建", Kind: KindBuild, Jobs: []Job{backend}}, {ID: "d", Name: "部署", Kind: KindDeploy, Jobs: []Job{deploy(map[string]any{ConfigKeyArtifactFrom: "gone"})}}},
			wantErr: true,
		},
		{
			name:    "引用自己",
			stages:  []Stage{{ID: "b", Name: "构建", Kind: KindBuild, Jobs: []Job{backend}}, {ID: "d", Name: "部署", Kind: KindDeploy, Jobs: []Job{deploy(map[string]any{ConfigKeyArtifactFrom: "jdep"})}}},
			wantErr: true,
		},
		{
			name:    "引用不产产物的源任务",
			stages:  []Stage{{ID: "b", Name: "构建", Kind: KindBuild, Jobs: []Job{backend}}, {ID: "d", Name: "部署", Kind: KindDeploy, Jobs: []Job{deploy(map[string]any{ConfigKeyArtifactFrom: "jsrc"})}}},
			wantErr: true,
		},
		{
			name: "同阶段:被本任务依赖才算上游",
			stages: []Stage{{ID: "d", Name: "部署", Kind: KindDeploy, Jobs: []Job{
				{ID: "japi", Name: "后端构建", Type: "script", Config: map[string]any{}},
				{ID: "jdep", Name: "部署", Type: "deploy_ssh", Needs: []string{"japi"}, Config: map[string]any{ConfigKeyArtifactFrom: "japi"}},
			}}},
		},
		{
			name: "同阶段并行(无 needs)产物可能还没出",
			stages: []Stage{{ID: "d", Name: "部署", Kind: KindDeploy, Jobs: []Job{
				{ID: "japi", Name: "后端构建", Type: "script", Config: map[string]any{}},
				{ID: "jdep", Name: "部署", Type: "deploy_ssh", Config: map[string]any{ConfigKeyArtifactFrom: "japi"}},
			}}},
			wantErr: true,
		},
		{
			name: "并行分支:未依赖的那条阶段不能作来源",
			stages: []Stage{
				{ID: "api", Name: "后端", Kind: KindBuild, Jobs: []Job{backend}},
				{ID: "web", Name: "前端", Kind: KindBuild, Jobs: []Job{frontend}},
				{ID: "d", Name: "部署", Kind: KindDeploy, Needs: []string{"api"}, Jobs: []Job{deploy(map[string]any{ConfigKeyArtifactFrom: "jweb"})}},
			},
			wantErr: true,
		},
		{
			name: "并行分支:依赖到的那条可以",
			stages: []Stage{
				{ID: "api", Name: "后端", Kind: KindBuild, Jobs: []Job{backend}},
				{ID: "web", Name: "前端", Kind: KindBuild, Jobs: []Job{frontend}},
				{ID: "d", Name: "部署", Kind: KindDeploy, Needs: []string{"api"}, Jobs: []Job{deploy(map[string]any{ConfigKeyArtifactFrom: "japi"})}},
			},
		},
		{
			name: "跨两级上游阶段(传递闭包)",
			stages: []Stage{
				{ID: "api", Name: "后端", Kind: KindBuild, Jobs: []Job{backend}},
				{ID: "mid", Name: "打包", Kind: KindBuild, Needs: []string{"api"}, Jobs: []Job{{ID: "jmid", Name: "打包", Type: "script", Config: map[string]any{}}}},
				{ID: "d", Name: "部署", Kind: KindDeploy, Needs: []string{"mid"}, Jobs: []Job{deploy(map[string]any{ConfigKeyArtifactFrom: "japi"})}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normalizeSpec(deploySpec(tc.stages...))
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("want no error, got %v", err)
				}
				return
			}
			if !errors.Is(err, ErrArtifactSourceInvalid) {
				t.Fatalf("err = %v, want wraps ErrArtifactSourceInvalid", err)
			}
			if strings.Contains(err.Error(), "pipeline:") {
				t.Errorf("报错不应含哨兵前缀,got %q", err.Error())
			}
		})
	}
}

// 非部署节点带同名键是巧合(不是我们的配置),不该被这条校验管。
func TestArtifactSourceOnlyAppliesToDeployJobs(t *testing.T) {
	_, err := normalizeSpec(deploySpec(Stage{ID: "b", Name: "构建", Kind: KindBuild, Jobs: []Job{
		{ID: "japi", Name: "后端构建", Type: "script", Config: map[string]any{ConfigKeyArtifactFrom: "nope"}},
	}}))
	if err != nil {
		t.Fatalf("脚本节点不该被产物来源校验拦下,got %v", err)
	}
}

// docker 部署节点必须说清用哪种方式:半截的 compose 配置跑到目标机上就是
// 「mkdir + 上传空文件 + up -d」,失败原因还指向别处。项目名的非法字符会直接成为
// 受管目录名,所以同样在保存期挡下(运行时那层是兜底,不是第一道)。
func TestNormalizeSpecValidatesDockerDeploy(t *testing.T) {
	cases := []struct {
		name    string
		cfg     map[string]any
		wantErr bool
	}{
		{"方式留空", map[string]any{}, true},
		{"单容器方式", map[string]any{ConfigKeyDockerMode: DockerModeRun}, false},
		{"方式非法", map[string]any{ConfigKeyDockerMode: "swarm"}, true},
		{"compose 缺项目名", map[string]any{ConfigKeyDockerMode: DockerModeCompose, ConfigKeyComposeYaml: "services: {}"}, true},
		{"compose 项目名含斜杠", map[string]any{ConfigKeyDockerMode: DockerModeCompose, ConfigKeyStackName: "a/b", ConfigKeyComposeYaml: "services: {}"}, true},
		{"compose 项目名以连字符开头", map[string]any{ConfigKeyDockerMode: DockerModeCompose, ConfigKeyStackName: "-app", ConfigKeyComposeYaml: "services: {}"}, true},
		{"compose 项目名超长", map[string]any{ConfigKeyDockerMode: DockerModeCompose, ConfigKeyStackName: strings.Repeat("a", 129), ConfigKeyComposeYaml: "services: {}"}, true},
		{"compose 正文留空", map[string]any{ConfigKeyDockerMode: DockerModeCompose, ConfigKeyStackName: "shop", ConfigKeyComposeYaml: "   "}, true},
		{"compose 正文超限", map[string]any{ConfigKeyDockerMode: DockerModeCompose, ConfigKeyStackName: "shop", ConfigKeyComposeYaml: strings.Repeat("x", composeMaxBytes+1)}, true},
		{"compose 齐备", map[string]any{ConfigKeyDockerMode: DockerModeCompose, ConfigKeyStackName: "shop-web", ConfigKeyComposeYaml: "services:\n  web:\n    image: nginx\n"}, false},
		// 正文来源 = 引用仓库文件:没有正文才是合法的(正文在运行时现读),路径的形状才是本层能判的。
		{"compose 引用仓库文件", map[string]any{ConfigKeyDockerMode: DockerModeCompose, ConfigKeyStackName: "shop", ConfigKeyComposeSource: ComposeSourceRepo, ConfigKeyComposeFile: "deploy/docker-compose.yml"}, false},
		{"compose 引用仓库文件缺路径", map[string]any{ConfigKeyDockerMode: DockerModeCompose, ConfigKeyStackName: "shop", ConfigKeyComposeSource: ComposeSourceRepo}, true},
		{"compose 仓库路径越界", map[string]any{ConfigKeyDockerMode: DockerModeCompose, ConfigKeyStackName: "shop", ConfigKeyComposeSource: ComposeSourceRepo, ConfigKeyComposeFile: "../secrets.yml"}, true},
		{"compose 仓库路径非 yaml", map[string]any{ConfigKeyDockerMode: DockerModeCompose, ConfigKeyStackName: "shop", ConfigKeyComposeSource: ComposeSourceRepo, ConfigKeyComposeFile: "deploy/Dockerfile"}, true},
		{"compose 仓库路径是绝对路径", map[string]any{ConfigKeyDockerMode: DockerModeCompose, ConfigKeyStackName: "shop", ConfigKeyComposeSource: ComposeSourceRepo, ConfigKeyComposeFile: "/etc/docker-compose.yml"}, true},
		{"compose 正文来源非法", map[string]any{ConfigKeyDockerMode: DockerModeCompose, ConfigKeyStackName: "shop", ConfigKeyComposeSource: "url", ConfigKeyComposeYaml: "services: {}"}, true},
		// 换来源时另一路的残留值只是藏起来的旧输入,不该反过来卡住保存(执行侧按来源取用)。
		{"compose 换到仓库文件仍留着粘贴正文", map[string]any{ConfigKeyDockerMode: DockerModeCompose, ConfigKeyStackName: "shop", ConfigKeyComposeSource: ComposeSourceRepo, ConfigKeyComposeFile: "docker-compose.yml", ConfigKeyComposeYaml: "services: {}"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normalizeSpec(specWithJobs(Job{ID: "j1", Name: "Docker 部署", Type: JobTypeDeployDocker, Config: tc.cfg}))
			if tc.wantErr {
				if !errors.Is(err, ErrDockerDeployInvalid) {
					t.Fatalf("err = %v, want wraps ErrDockerDeployInvalid", err)
				}
				// 422 直接回显该消息:哨兵的英文前缀不能混进去。
				if strings.Contains(err.Error(), "pipeline:") {
					t.Errorf("报错不应含哨兵前缀,got %q", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("want no error, got %v", err)
			}
		})
	}
}

// K8s 发布节点分两条路:不带清单=只换镜像(寻址全靠配置格),带清单=清单说话。
// 本测试盯的就是「同一件事只有一处说了算」——尤其是命名空间:清单模式下再填一格,
// 生效的只会是清单里那个,留着它等于给用户一个假的控制项。
func TestNormalizeSpecValidatesK8sDeploy(t *testing.T) {
	cases := []struct {
		name    string
		cfg     map[string]any
		wantErr bool
	}{
		{"未选集群", map[string]any{ConfigKeyWorkloadName: "api"}, true},
		{"只换镜像齐备", map[string]any{ConfigKeyClusterID: "c1", ConfigKeyWorkloadName: "api"}, false},
		{"同时配服务器与集群", map[string]any{ConfigKeyClusterID: "c1", ConfigKeyWorkloadName: "api", "serverId": "s1"}, true},
		{"只换镜像缺负载名", map[string]any{ConfigKeyClusterID: "c1"}, true},
		{"命名空间非法(大写)", map[string]any{ConfigKeyClusterID: "c1", ConfigKeyWorkloadName: "api", ConfigKeyNamespace: "Shop"}, true},
		{"命名空间路径穿越", map[string]any{ConfigKeyClusterID: "c1", ConfigKeyWorkloadName: "api", ConfigKeyNamespace: "../../admin"}, true},
		{"命名空间合法", map[string]any{ConfigKeyClusterID: "c1", ConfigKeyWorkloadName: "api", ConfigKeyNamespace: "shop-prod"}, false},
		{"负载类型不支持", map[string]any{ConfigKeyClusterID: "c1", ConfigKeyWorkloadName: "api", ConfigKeyWorkloadKind: "DaemonSet"}, true},
		{"等待滚动秒数为 0", map[string]any{ConfigKeyClusterID: "c1", ConfigKeyWorkloadName: "api", ConfigKeyRolloutTimeout: "0"}, true},
		{"autoRollback 非布尔", map[string]any{ConfigKeyClusterID: "c1", ConfigKeyWorkloadName: "api", ConfigKeyAutoRollback: "yes"}, true},
		{"策略非 rolling", map[string]any{ConfigKeyClusterID: "c1", ConfigKeyWorkloadName: "api", "strategy": "canary"}, true},
		// 清单来源:none / repo / paste 三选一,写别的等于没配。
		{"清单来源非法", map[string]any{ConfigKeyClusterID: "c1", ConfigKeyManifestSource: "url", ConfigKeyManifestYaml: "kind: Deployment"}, true},
		{"粘贴清单没有正文", map[string]any{ConfigKeyClusterID: "c1", ConfigKeyManifestSource: ManifestSourcePaste, ConfigKeyManifestYaml: "   "}, true},
		{"粘贴清单超限", map[string]any{ConfigKeyClusterID: "c1", ConfigKeyManifestSource: ManifestSourcePaste, ConfigKeyManifestYaml: strings.Repeat("x", manifestMaxBytes+1)}, true},
		{"粘贴清单齐备(负载名可留空)", map[string]any{ConfigKeyClusterID: "c1", ConfigKeyManifestSource: ManifestSourcePaste, ConfigKeyManifestYaml: "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: api\n"}, false},
		// 这条是「命名空间以 yaml」的落地:清单模式再多填一格就是两处各说一套。
		{"清单模式下还填了命名空间", map[string]any{ConfigKeyClusterID: "c1", ConfigKeyNamespace: "shop", ConfigKeyManifestSource: ManifestSourcePaste, ConfigKeyManifestYaml: "kind: Deployment"}, true},
		{"清单模式回到 none 后负载名仍必填", map[string]any{ConfigKeyClusterID: "c1", ConfigKeyManifestSource: ManifestSourceNone}, true},
		{"清单模式回到 none 且命名空间可填", map[string]any{ConfigKeyClusterID: "c1", ConfigKeyManifestSource: ManifestSourceNone, ConfigKeyWorkloadName: "api", ConfigKeyNamespace: "shop"}, false},
		{"仓库清单缺路径", map[string]any{ConfigKeyClusterID: "c1", ConfigKeyManifestSource: ManifestSourceRepo}, true},
		{"仓库清单路径越界", map[string]any{ConfigKeyClusterID: "c1", ConfigKeyManifestSource: ManifestSourceRepo, ConfigKeyManifestFile: "../k8s.yml"}, true},
		{"仓库清单路径非 yaml", map[string]any{ConfigKeyClusterID: "c1", ConfigKeyManifestSource: ManifestSourceRepo, ConfigKeyManifestFile: "deploy/Dockerfile"}, true},
		{"仓库清单齐备", map[string]any{ConfigKeyClusterID: "c1", ConfigKeyManifestSource: ManifestSourceRepo, ConfigKeyManifestFile: "deploy/k8s/deployment.yaml"}, false},
		// 换来源时另一路的残留值只是藏起来的旧输入,不该卡住保存(执行侧按来源取用)。
		{"换到仓库文件仍留着粘贴正文", map[string]any{ConfigKeyClusterID: "c1", ConfigKeyManifestSource: ManifestSourceRepo, ConfigKeyManifestFile: "k8s.yml", ConfigKeyManifestYaml: "kind: Deployment"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normalizeSpec(specWithJobs(Job{ID: "j1", Name: "K8s 发布", Type: JobTypeDeployK8s, Config: tc.cfg}))
			if tc.wantErr {
				if !errors.Is(err, ErrK8sDeployInvalid) {
					t.Fatalf("err = %v, want wraps ErrK8sDeployInvalid", err)
				}
				if strings.Contains(err.Error(), "pipeline:") {
					t.Errorf("报错不应含哨兵前缀,got %q", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("want no error, got %v", err)
			}
		})
	}
}

// 同名的键落在别的任务类型上不相干:deploy_ssh 不该被 docker 方式校验管。
func TestDockerDeployValidationOnlyAppliesToItsJobType(t *testing.T) {
	if _, err := normalizeSpec(specWithJobs(Job{ID: "j1", Name: "SSH 部署", Type: "deploy_ssh", Config: map[string]any{ConfigKeyDockerMode: "compose"}})); err != nil {
		t.Fatalf("deploy_ssh 不该被 docker 方式校验拦下,got %v", err)
	}
}
