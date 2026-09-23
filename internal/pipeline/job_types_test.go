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
// 派发/校验只认折算结果(image → build_image,jar/dist → script)。
func TestEffectiveJobTypeByArtifactTier(t *testing.T) {
	cases := []struct {
		name    string
		jobType string
		cfg     map[string]any
		want    string
	}{
		{"镜像档位", JobTypeBuild, map[string]any{ConfigKeyArtifactType: ArtifactImage}, JobTypeBuildImage},
		{"jar 档位", JobTypeBuild, map[string]any{ConfigKeyArtifactType: ArtifactJAR}, StepTypeScript},
		{"dist 档位", JobTypeBuild, map[string]any{ConfigKeyArtifactType: ArtifactDist}, StepTypeScript},
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
	for _, tier := range []string{ArtifactImage, ArtifactJAR, ArtifactDist} {
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
