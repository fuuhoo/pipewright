package pipeline

import (
	"errors"
	"strings"
	"testing"
)

// 目录快照:node20(启用)/ java21+mvn(启用)/ old-ruby(禁用)/ private runner(禁用)。
func testSnapshot() BuildEnvSnapshot {
	return NewBuildEnvSnapshot([]BuildEnvOption{
		{ID: "env-node20", Language: "node", Version: "20", DisplayName: "Node.js 20", Image: "node:20-alpine", Enabled: true, CheckStatus: "available"},
		{ID: "env-java21", Language: "java", Version: "21", DisplayName: "Java 21", Image: "eclipse-temurin:21-jdk-alpine", Enabled: true},
		{ID: "env-mvn", Language: "java", Version: "21-maven", DisplayName: "Java 21 + Maven 3.9", Image: "maven:3.9-eclipse-temurin-21", Enabled: true},
		{ID: "env-old-ruby", Language: "ruby", Version: "2.7", DisplayName: "Ruby 2.7", Image: "ruby:2.7", Enabled: false},
	}, []ConfigProfileOption{
		// FilePath/TargetPath 是执行期注入用的字段;保存期会顺带判资源自身可注入(见 profileDefect)。
		{ID: "cp-npmrc", Language: "node", Enabled: true, FilePath: "/data/config_profiles/cp-npmrc/.npmrc", TargetPath: "/root/.npmrc"},
		{ID: "cp-mvn", Language: "java", Enabled: true, FilePath: "/data/config_profiles/cp-mvn/settings.xml", TargetPath: "/root/.m2/settings.xml"},
		{ID: "cp-off", Language: "node", Enabled: false, FilePath: "/data/config_profiles/cp-off/x", TargetPath: "/root/x"},
	})
}

func specWithJob(jobType string, cfg map[string]any) Spec {
	return Spec{Stages: []Stage{
		{ID: "stg_src", Name: "流水线源", Kind: KindSource, Jobs: []Job{{ID: "job_src", Name: "源", Type: "git_source", Config: map[string]any{}}}},
		{ID: "stg_build", Name: "构建", Kind: KindBuild, Jobs: []Job{{ID: "job_b", Name: "构建", Type: jobType, Config: cfg}}},
	}}
}

func TestNeedsBuildEnv(t *testing.T) {
	cases := []struct {
		jobType string
		cfg     map[string]any
		want    bool
	}{
		{"script", nil, true},
		{"custom", nil, true},
		{"build_frontend", nil, true},
		{"build_backend", nil, true},
		{"templated", nil, true},
		{"build_image", map[string]any{"buildModel": "toolchain"}, true},
		{"build_image", map[string]any{"buildModel": "dockerfile"}, false},
		{"build_image", nil, false}, // 缺省 = 模型 A(docker build),镜像由 Dockerfile 的 FROM 决定
		{"git_source", nil, false},
		{"push_image", nil, false},
		{"deploy_ssh", nil, false},
		{"notify", nil, false},
	}
	for _, c := range cases {
		if got := NeedsBuildEnv(c.jobType, c.cfg); got != c.want {
			t.Errorf("NeedsBuildEnv(%q,%v) = %v, want %v", c.jobType, c.cfg, got, c.want)
		}
	}
}

func TestValidateBuildEnvRefs(t *testing.T) {
	snap := testSnapshot()
	cases := []struct {
		name     string
		jobType  string
		cfg      map[string]any
		wantCode string // "" = 应无问题
	}{
		{"按 ID 选择", "script", map[string]any{"buildEnvId": "env-node20"}, ""},
		{"未选择", "script", map[string]any{"commands": "npm run build"}, ProblemBuildEnvMissing},
		{"ID 不存在", "script", map[string]any{"buildEnvId": "env-gone"}, ProblemBuildEnvUnknown},
		{"ID 已禁用", "script", map[string]any{"buildEnvId": "env-old-ruby"}, ProblemBuildEnvDisabled},
		{"旧镜像命中目录", "build_frontend", map[string]any{"image": "node:20-alpine"}, ""},
		{"旧镜像不在目录", "build_frontend", map[string]any{"image": "evil.internal/x:1"}, ProblemBuildEnvOffCatalog},
		{"旧工具链命中镜像", "build_image", map[string]any{"buildModel": "toolchain", "toolchainLanguage": "eclipse-temurin", "toolchainVersion": "21-jdk-alpine"}, ""},
		{"旧工具链按语言版本命中", "build_image", map[string]any{"buildModel": "toolchain", "toolchainLanguage": "java", "toolchainVersion": "21"}, ""},
		{"旧工具链不在目录", "build_image", map[string]any{"buildModel": "toolchain", "toolchainLanguage": "clojure", "toolchainVersion": "1.11"}, ProblemBuildEnvOffCatalog},
		{"自定义节点参数留待运行时", "templated", map[string]any{"image": "{{image}}"}, ""},
		{"配置资源不存在", "script", map[string]any{"buildEnvId": "env-node20", "configProfileIds": "cp-gone"}, ProblemConfigProfileUnknown},
		{"配置资源已禁用", "script", map[string]any{"buildEnvId": "env-node20", "configProfileIds": "cp-off"}, ProblemConfigProfileUnknown},
		{"配置资源语言不匹配", "script", map[string]any{"buildEnvId": "env-node20", "configProfileIds": "cp-mvn"}, ProblemConfigProfileLanguage},
		{"配置资源语言匹配", "script", map[string]any{"buildEnvId": "env-node20", "configProfileIds": "cp-npmrc"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			problems := ValidateBuildEnvRefs(specWithJob(c.jobType, c.cfg), snap)
			if c.wantCode == "" {
				if len(problems) != 0 {
					t.Fatalf("want no problems, got %+v", problems)
				}
				return
			}
			if len(problems) == 0 {
				t.Fatalf("want problem %q, got none", c.wantCode)
			}
			if problems[0].Code != c.wantCode {
				t.Fatalf("code = %q, want %q (problems=%+v)", problems[0].Code, c.wantCode, problems)
			}
			if problems[0].Stage == "" || problems[0].Job == "" {
				t.Errorf("问题应定位到阶段/节点:%+v", problems[0])
			}
		})
	}
}

func TestValidateBuildEnvRefsIgnoresNonBuildJobs(t *testing.T) {
	// 源节点/部署/通知即使写着 image 也不参与构建环境校验(它们不进构建容器)。
	spec := Spec{Stages: []Stage{
		{ID: "s", Name: "部署", Kind: KindDeploy, Jobs: []Job{
			{ID: "j1", Name: "SSH 部署", Type: "deploy_ssh", Config: map[string]any{"image": "whatever"}},
			{ID: "j2", Name: "通知", Type: "notify", Config: map[string]any{}},
		}},
	}}
	if problems := ValidateBuildEnvRefs(spec, testSnapshot()); len(problems) != 0 {
		t.Fatalf("want no problems, got %+v", problems)
	}
}

func TestSaveRejectsJobsWithoutPresetBuildEnv(t *testing.T) {
	err := (&service{gate: staticGate{testSnapshot()}}).enforceBuildEnvCatalog(
		specWithJob("script", map[string]any{"image": "evil.internal/x:1"}))
	if err == nil {
		t.Fatal("越界镜像应拒绝保存")
	}
	if !errors.Is(err, ErrBuildEnvRequired) {
		t.Fatalf("err = %v, want wraps ErrBuildEnvRequired", err)
	}
	if !strings.Contains(err.Error(), "构建") || !strings.Contains(err.Error(), "evil.internal/x:1") {
		t.Errorf("报错应点名阶段与当前镜像,got %q", err.Error())
	}
}

func TestEnforceBuildEnvCatalogDegrades(t *testing.T) {
	bad := specWithJob("script", map[string]any{"image": "evil.internal/x:1"})

	// 未注入 gate(单测 / 装配缺席)→ 放行。
	if err := (&service{}).enforceBuildEnvCatalog(bad); err != nil {
		t.Errorf("未注入 gate 应放行,got %v", err)
	}
	// 目录为空(全新安装尚未 seed)→ 放行,由运行时报错。
	empty := &service{gate: staticGate{NewBuildEnvSnapshot(nil, nil)}}
	if err := empty.enforceBuildEnvCatalog(bad); err != nil {
		t.Errorf("空目录应放行,got %v", err)
	}
	// 快照读取失败不该连带锁死流水线编辑。
	if err := (&service{gate: errGate{}}).enforceBuildEnvCatalog(bad); err != nil {
		t.Errorf("目录服务出错应放行,got %v", err)
	}
	// 有目录时正常挡住。
	if err := (&service{gate: staticGate{testSnapshot()}}).enforceBuildEnvCatalog(bad); err == nil {
		t.Fatal("有目录时应拒绝越界镜像")
	}
}

type staticGate struct{ snap BuildEnvSnapshot }

func (g staticGate) Snapshot() (BuildEnvSnapshot, error) { return g.snap, nil }

type errGate struct{}

func (errGate) Snapshot() (BuildEnvSnapshot, error) { return BuildEnvSnapshot{}, errors.New("db down") }

func TestConfigStringList(t *testing.T) {
	cfg := map[string]any{"configProfileIds": " a ,, b\na,\r\n c "}
	got := ConfigStringList(cfg, ConfigKeyConfigProfileIDs)
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if ConfigStringList(map[string]any{}, ConfigKeyConfigProfileIDs) != nil {
		t.Error("空值应为 nil")
	}
}
