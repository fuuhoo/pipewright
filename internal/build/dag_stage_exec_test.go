package build

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huangchengsir/pipewright/internal/dagrun"
	"github.com/huangchengsir/pipewright/internal/deploy"
	"github.com/huangchengsir/pipewright/internal/pipeline"
	"github.com/huangchengsir/pipewright/internal/project"
	"github.com/huangchengsir/pipewright/internal/run"
)

// ─── 测试替身 ──────────────────────────────────────────────────────────────────

// fakeReporter 记录 dagrun.StageReporter 调用。
type fakeReporter struct {
	logs    []string
	arts    []run.Artifact
	jobDone []string // 记录 "jobID=status",验证节点级上报
}

func (r *fakeReporter) Log(_ context.Context, _ string, line string) error {
	r.logs = append(r.logs, line)
	return nil
}
func (r *fakeReporter) EmitArtifact(_ context.Context, a run.Artifact) error {
	r.arts = append(r.arts, a)
	return nil
}

// 节点级 step:测试 fake 记录 job 终态;JobReporter 返回自身(日志继续累计到同一 fake)。
func (r *fakeReporter) JobRunning(_ context.Context, _ string) error { return nil }
func (r *fakeReporter) JobDone(_ context.Context, jobID, status string) error {
	r.jobDone = append(r.jobDone, jobID+"="+status)
	return nil
}
func (r *fakeReporter) JobReporter(string) dagrun.StageReporter { return r }

// recordingDriver 记录 RunToolchain 调用并按配置回退码/日志(其余 Driver 方法不应被调到)。
type recordingDriver struct {
	code      int
	emit      []string
	gotImage  string
	gotCmd    []string
	gotWork   string
	gotEnv    []string
	gotRes    pipeline.Resource
	callCount int
}

func (d *recordingDriver) Binary() string { return "fake" }
func (d *recordingDriver) RunToolchain(_ context.Context, image, _, _, workdir string, env []string, cmd []string, res pipeline.Resource, onLine func(stream, line string)) (int, error) {
	d.callCount++
	d.gotImage = image
	d.gotCmd = cmd
	d.gotWork = workdir
	d.gotEnv = env
	d.gotRes = res
	for _, l := range d.emit {
		onLine("stdout", l)
	}
	return d.code, nil
}
func (d *recordingDriver) Build(context.Context, string, string, string, []string, []string, func(string, string)) (int, error) {
	panic("Build not expected")
}
func (d *recordingDriver) Tag(context.Context, string, string, func(string, string)) (int, error) {
	panic("Tag not expected")
}
func (d *recordingDriver) Login(context.Context, string, string, string, func(string, string)) (int, error) {
	panic("Login not expected")
}
func (d *recordingDriver) Push(context.Context, string, func(string, string)) (int, error) {
	panic("Push not expected")
}
func (d *recordingDriver) InspectImage(context.Context, string) (string, int64, error) {
	panic("InspectImage not expected")
}

// markerCloner 把一个标记文件写进工作区(替代真触网克隆)。
type markerCloner struct {
	file    string
	content string
}

func (c *markerCloner) Clone(_ context.Context, _, _, _, _, _, destDir string) (*CloneResolved, error) {
	if c.file != "" {
		_ = os.WriteFile(filepath.Join(destDir, c.file), []byte(c.content), 0o644)
	}
	return &CloneResolved{CommitShort: "abc1234"}, nil
}

func newDAGTestBuilder(drv Driver, cl repoCloner) *Builder {
	return &Builder{
		projects: fakeProjects{proj: &project.Project{ID: "p1", RepoURL: "https://example.com/r.git"}},
		settings: fakeSettings{settings: &pipeline.Settings{}},
		vault:    fakeVault{secrets: map[string]string{}},
		driver:   drv,
		cloner:   cl,
		envGate:  testBuildEnvGate,
	}
}

func scriptStage(jobs ...pipeline.Job) pipeline.Stage {
	return pipeline.Stage{ID: "s1", Name: "构建", Kind: pipeline.KindBuild, Jobs: jobs}
}

func scriptJob(name, image, commands string) pipeline.Job {
	return pipeline.Job{Name: name, Type: pipeline.StepTypeScript, Config: map[string]any{"image": image, "commands": commands}}
}

// ─── scriptStepFromJob / splitCommands ──────────────────────────────────────────

// scriptStepBuilder 只带预置目录的构建器(scriptStepFromJob 只用到 envGate)。
func scriptStepBuilder(gate pipeline.BuildEnvGate) *Builder {
	return &Builder{envGate: gate}
}

func TestScriptStepFromJob(t *testing.T) {
	jb := pipeline.Job{
		ID: "j1", Name: "test", Type: "script",
		Config: map[string]any{"image": " node:20 ", "commands": "npm ci\n\nnpm test\n", "workDir": "app"},
	}
	step, err := scriptStepBuilder(testBuildEnvGate).scriptStepFromJob(jb, "构建")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if step.Image != "node:20" {
		t.Errorf("image = %q", step.Image)
	}
	if len(step.Commands) == 0 || step.Commands[0] != "npm ci" {
		t.Errorf("commands = %v", step.Commands)
	}
	if step.WorkDir != "app" {
		t.Errorf("workDir = %q", step.WorkDir)
	}
}

func TestScriptStepFromJobMissing(t *testing.T) {
	b := scriptStepBuilder(testBuildEnvGate)
	// 目录外的镜像 / 没选环境:拒绝执行(#9 的核心断言)。
	if _, err := b.scriptStepFromJob(pipeline.Job{Config: map[string]any{"image": "harbor.evil/x:1", "commands": "x"}}, "构建"); err == nil {
		t.Error("expected error for off-catalog image")
	}
	if _, err := b.scriptStepFromJob(pipeline.Job{Config: map[string]any{"commands": "x"}}, "构建"); err == nil {
		t.Error("expected error for missing build env")
	}
	// 环境合法但命令空:仍要报「缺少执行命令」。
	if _, err := b.scriptStepFromJob(pipeline.Job{Config: map[string]any{"image": "node:20"}}, "构建"); err == nil {
		t.Error("expected error for missing commands")
	}
}

// ─── NewStageExecutor(fake driver,无 Docker)───────────────────────────────────

func TestStageExecutorRunsScriptJobInContainer(t *testing.T) {
	drv := &recordingDriver{code: 0, emit: []string{"hello from container"}}
	b := newDAGTestBuilder(drv, &markerCloner{})
	exec := NewStageExecutor(b, nil)
	rep := &fakeReporter{}

	err := exec(context.Background(), &run.Run{ProjectID: "p1"},
		scriptStage(scriptJob("unit", "busybox", "echo hello")), rep)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if drv.callCount != 1 {
		t.Fatalf("RunToolchain called %d times, want 1", drv.callCount)
	}
	if drv.gotImage != "busybox" {
		t.Errorf("image = %q", drv.gotImage)
	}
	// 多行命令合成 sh -c set -e 脚本。
	if len(drv.gotCmd) != 3 || drv.gotCmd[0] != "sh" || drv.gotCmd[1] != "-c" || !strings.Contains(drv.gotCmd[2], "echo hello") {
		t.Errorf("cmd = %v", drv.gotCmd)
	}
	if !strings.Contains(strings.Join(rep.logs, "\n"), "hello from container") {
		t.Errorf("container log not forwarded to reporter: %v", rep.logs)
	}
}

func TestStageExecutorInjectsRunParams(t *testing.T) {
	drv := &recordingDriver{code: 0}
	b := newDAGTestBuilder(drv, &markerCloner{})
	exec := NewStageExecutor(b, nil)
	r := &run.Run{ProjectID: "p1", Trigger: run.Trigger{Params: map[string]string{"DEPLOY_ENV": "staging", "VER": "1.2"}}}
	if err := exec(context.Background(), r, scriptStage(scriptJob("unit", "busybox", "echo hi")), &fakeReporter{}); err != nil {
		t.Fatalf("exec: %v", err)
	}
	env := strings.Join(drv.gotEnv, " ")
	if !strings.Contains(env, "DEPLOY_ENV=staging") || !strings.Contains(env, "VER=1.2") {
		t.Errorf("run params not injected into container env: %v", drv.gotEnv)
	}
}

// scriptJobWithConfig 构造一条带额外 config 键的 script job(timeout/retry/cpu/memory 等)。
func scriptJobWithConfig(name, image, commands string, extra map[string]any) pipeline.Job {
	cfg := map[string]any{"image": image, "commands": commands}
	for k, v := range extra {
		cfg[k] = v
	}
	return pipeline.Job{Name: name, Type: pipeline.StepTypeScript, Config: cfg}
}

// TestStageExecutorPassesResourceToDriver:job.Config 的 cpu/memory 应透传进 RunToolchain。
func TestStageExecutorPassesResourceToDriver(t *testing.T) {
	drv := &recordingDriver{code: 0}
	b := newDAGTestBuilder(drv, &markerCloner{})
	exec := NewStageExecutor(b, nil)
	job := scriptJobWithConfig("unit", "busybox", "echo hi", map[string]any{"cpu": "1.5", "memory": "512m"})
	if err := exec(context.Background(), &run.Run{ProjectID: "p1"}, scriptStage(job), &fakeReporter{}); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if drv.gotRes.CPU != "1.5" || drv.gotRes.Memory != "512m" {
		t.Errorf("resource not passed to driver: %+v", drv.gotRes)
	}
}

// flakyDriver 在前 failUntil 次返回非零退出,其后返回 0;记录被调次数。可注入每次调用的阻塞时长(供超时测试)。
type flakyDriver struct {
	failUntil int // 前 failUntil 次返回非零(模拟失败)
	calls     int
	block     time.Duration // 每次调用阻塞时长(>0 时配合超时 ctx 测试)
}

func (d *flakyDriver) Binary() string { return "fake" }
func (d *flakyDriver) RunToolchain(ctx context.Context, _, _, _, _ string, _ []string, _ []string, _ pipeline.Resource, _ func(string, string)) (int, error) {
	d.calls++
	if d.block > 0 {
		select {
		case <-time.After(d.block):
		case <-ctx.Done():
			return -1, ctx.Err() // 被(超时)取消:无法完成,exitCode<0
		}
	}
	if d.calls <= d.failUntil {
		return 1, nil // 非零退出 → ErrBuildFailed
	}
	return 0, nil
}
func (d *flakyDriver) Build(context.Context, string, string, string, []string, []string, func(string, string)) (int, error) {
	panic("Build not expected")
}
func (d *flakyDriver) Tag(context.Context, string, string, func(string, string)) (int, error) {
	panic("Tag not expected")
}
func (d *flakyDriver) Login(context.Context, string, string, string, func(string, string)) (int, error) {
	panic("Login not expected")
}
func (d *flakyDriver) Push(context.Context, string, func(string, string)) (int, error) {
	panic("Push not expected")
}
func (d *flakyDriver) InspectImage(context.Context, string) (string, int64, error) {
	panic("InspectImage not expected")
}

// TestStageExecutorRetriesToSuccess:retries=2、前 2 次失败 → 第 3 次成功,共 3 次尝试,阶段成功。
func TestStageExecutorRetriesToSuccess(t *testing.T) {
	drv := &flakyDriver{failUntil: 2}
	b := newDAGTestBuilder(drv, &markerCloner{})
	exec := NewStageExecutor(b, nil)
	job := scriptJobWithConfig("unit", "busybox", "flaky", map[string]any{"retries": 2})
	if err := exec(context.Background(), &run.Run{ProjectID: "p1"}, scriptStage(job), &fakeReporter{}); err != nil {
		t.Fatalf("exec should succeed after retries: %v", err)
	}
	if drv.calls != 3 {
		t.Errorf("attempts = %d, want 3 (1 + 2 retries)", drv.calls)
	}
}

// TestStageExecutorRetriesExhausted:retries=1、始终失败 → 共 2 次尝试后判失败(ErrBuildFailed)。
func TestStageExecutorRetriesExhausted(t *testing.T) {
	drv := &flakyDriver{failUntil: 99}
	b := newDAGTestBuilder(drv, &markerCloner{})
	exec := NewStageExecutor(b, nil)
	job := scriptJobWithConfig("unit", "busybox", "always-fail", map[string]any{"retries": 1})
	err := exec(context.Background(), &run.Run{ProjectID: "p1"}, scriptStage(job), &fakeReporter{})
	if !errors.Is(err, ErrBuildFailed) {
		t.Fatalf("err = %v, want ErrBuildFailed", err)
	}
	if drv.calls != 2 {
		t.Errorf("attempts = %d, want 2 (1 + 1 retry)", drv.calls)
	}
}

// TestStageExecutorTimeoutTriggersFailure:timeoutSeconds=1、容器阻塞 10s → 超时取消、判失败(非取消整次运行)。
func TestStageExecutorTimeoutTriggersFailure(t *testing.T) {
	drv := &flakyDriver{block: 10 * time.Second}
	b := newDAGTestBuilder(drv, &markerCloner{})
	exec := NewStageExecutor(b, nil)
	rep := &fakeReporter{}
	job := scriptJobWithConfig("unit", "busybox", "sleep 10", map[string]any{"timeoutSeconds": 1})
	start := time.Now()
	err := exec(context.Background(), &run.Run{ProjectID: "p1"}, scriptStage(job), rep)
	if !errors.Is(err, ErrBuildFailed) {
		t.Fatalf("timeout should yield ErrBuildFailed (not run-canceled), got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("timeout did not kick in promptly: %v", elapsed)
	}
	if !strings.Contains(strings.Join(rep.logs, "\n"), "超时") {
		t.Errorf("expected honest timeout log, got %v", rep.logs)
	}
}

func TestStageExecutorNonScriptPlaceholder(t *testing.T) {
	drv := &recordingDriver{}
	b := newDAGTestBuilder(drv, &markerCloner{})
	exec := NewStageExecutor(b, nil)
	rep := &fakeReporter{}

	// 未接入执行的类型诚实占位放行(script/build_image/deploy_ssh/notify 已支持,其余占位)。
	stage := pipeline.Stage{ID: "s", Name: "自定义", Kind: pipeline.KindCustom,
		Jobs: []pipeline.Job{{Name: "扫描", Type: "code_scan", Config: map[string]any{}}}}
	if err := exec(context.Background(), &run.Run{ProjectID: "p1"}, stage, rep); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if drv.callCount != 0 {
		t.Error("placeholder job must not invoke RunToolchain")
	}
	if !strings.Contains(strings.Join(rep.logs, "\n"), "真实执行未接入") {
		t.Errorf("expected honest placeholder log, got %v", rep.logs)
	}
}

// 撤销的 health_check 节点绝不占位放行:静默绿等于冒充门禁生效。
func TestStageExecutorFailsRetiredJobType(t *testing.T) {
	drv := &recordingDriver{}
	b := newDAGTestBuilder(drv, &markerCloner{})
	exec := NewStageExecutor(b, nil)
	rep := &fakeReporter{}

	stage := pipeline.Stage{ID: "s", Name: "门禁", Kind: pipeline.KindCustom,
		Jobs: []pipeline.Job{{Name: "健康检查", Type: "health_check", Config: map[string]any{"probeMode": "http"}}}}
	err := exec(context.Background(), &run.Run{ProjectID: "p1"}, stage, rep)
	if !errors.Is(err, ErrBuildFailed) {
		t.Fatalf("want ErrBuildFailed, got %v", err)
	}
	logs := strings.Join(rep.logs, "\n")
	if !strings.Contains(logs, "已撤销") || !strings.Contains(logs, "部署任务") {
		t.Errorf("报错应给出改配指引, got %v", rep.logs)
	}
}

// imgDriver 是支持 Build/InspectImage 的测试驱动(build_image 真实化测试用)。
type imgDriver struct {
	buildCalls    int
	pushCalls     int
	runCalls      int
	gotPushRef    string
	gotContextDir string
	gotDockerfile string
}

func (d *imgDriver) Binary() string { return "fake" }
func (d *imgDriver) RunToolchain(context.Context, string, string, string, string, []string, []string, pipeline.Resource, func(string, string)) (int, error) {
	d.runCalls++
	return 0, nil
}
func (d *imgDriver) Build(_ context.Context, contextDir, dockerfile, _ string, _, _ []string, _ func(string, string)) (int, error) {
	d.buildCalls++
	d.gotContextDir = contextDir
	d.gotDockerfile = dockerfile
	return 0, nil
}
func (d *imgDriver) Tag(context.Context, string, string, func(string, string)) (int, error) {
	return 0, nil
}
func (d *imgDriver) Login(context.Context, string, string, string, func(string, string)) (int, error) {
	return 0, nil
}
func (d *imgDriver) Push(_ context.Context, ref string, _ func(string, string)) (int, error) {
	d.pushCalls++
	d.gotPushRef = ref
	return 0, nil
}
func (d *imgDriver) InspectImage(context.Context, string) (string, int64, error) {
	return "sha256:deadbeef", 4096, nil
}

func TestStageExecutorBuildImageReal(t *testing.T) {
	drv := &imgDriver{}
	b := newDAGTestBuilder(drv, &markerCloner{})
	exec := NewStageExecutor(b, nil)
	rep := &fakeReporter{}

	stage := pipeline.Stage{ID: "s", Name: "构建", Kind: pipeline.KindBuild,
		Jobs: []pipeline.Job{{ID: "jimg", Name: "镜像", Type: "build_image", Config: map[string]any{
			"artifactType": "image", "buildModel": "dockerfile", "dockerfilePath": "Dockerfile",
		}}}}
	if err := exec(context.Background(), &run.Run{ProjectID: "p1"}, stage, rep); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if drv.buildCalls != 1 {
		t.Fatalf("build_image 应调用 driver.Build 一次,实际 %d", drv.buildCalls)
	}
	if len(rep.arts) != 1 || rep.arts[0].Type != run.ArtifactImage {
		t.Fatalf("应 emit 一件 image 产物,实际 %+v", rep.arts)
	}
	// 产物要带上来源任务(ID + 名字):并行构建时部署节点靠 ID 认领那一件产物。
	meta := rep.arts[0].Metadata
	if meta["sourceJobId"] != "jimg" || meta["sourceJob"] != "镜像" || meta["sourceStage"] != "构建" {
		t.Errorf("产物来源 metadata 不齐:got %+v", meta)
	}
}

// 文件产物同样带来源任务 ID;两个**同名**任务各产一件时,只有 ID 分得清谁是谁
// (后端只对 ID 查唯一,任务名可以重复)。
func TestCollectScriptArtifactsTagsSourceJob(t *testing.T) {
	ws := t.TempDir()
	for _, d := range []string{"web/dist", "api/dist"} {
		if err := os.MkdirAll(filepath.Join(ws, d), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	rep := &fakeReporter{}
	b := &Builder{}
	jobs := []pipeline.Job{
		{ID: "jweb", Name: "构建产物", Type: "script", Config: map[string]any{"artifactPath": "web/dist"}},
		{ID: "japi", Name: "构建产物", Type: "script", Config: map[string]any{"artifactPath": "api/dist"}},
	}
	b.collectScriptArtifacts(context.Background(), jobs, ws, "report", "构建", rep)
	if len(rep.arts) != 2 {
		t.Fatalf("两个任务各产一件,实际 %+v", rep.arts)
	}
	want := []string{"jweb", "japi"}
	for i, art := range rep.arts {
		if got := art.Metadata["sourceJobId"]; got != want[i] {
			t.Errorf("arts[%d].sourceJobId = %v, want %s(完整 %+v)", i, got, want[i], art.Metadata)
		}
		if art.Metadata["sourceJob"] != "构建产物" || art.Metadata["sourceStage"] != "构建" {
			t.Errorf("arts[%d] 来源标注缺失:got %+v", i, art.Metadata)
		}
	}
}

// 合并后的「构建」任务按产物档位派发:镜像 → docker build,产物 → 构建环境容器里跑脚本。
// 断言两条路径互斥(不是一边调 Build 一边也调 RunToolchain),否则档位形同没生效。
func TestStageExecutorBuildTaskTierDispatch(t *testing.T) {
	runTier := func(t *testing.T, tier string) {
		drv := &recordingDriver{code: 0}
		b := newDAGTestBuilder(drv, &markerCloner{})
		rep := &fakeReporter{}
		stage := pipeline.Stage{ID: "s", Name: "构建", Kind: pipeline.KindBuild,
			Jobs: []pipeline.Job{{ID: "j1", Name: "构建", Type: pipeline.JobTypeBuild, Config: map[string]any{
				pipeline.ConfigKeyArtifactType: tier, "image": "node:20", "commands": "mvn -B package",
			}}}}
		if err := NewStageExecutor(b, nil)(context.Background(), &run.Run{ProjectID: "p1"}, stage, rep); err != nil {
			t.Fatalf("exec: %v", err)
		}
		if drv.callCount != 1 {
			t.Fatalf("档位 %s 应在构建容器里跑一次脚本,实际 %d", tier, drv.callCount)
		}
		if drv.gotImage != "node:20" {
			t.Errorf("容器镜像 = %q", drv.gotImage)
		}
		if !strings.Contains(strings.Join(drv.gotCmd, " "), "mvn -B package") {
			t.Errorf("命令未进容器: %v", drv.gotCmd)
		}
	}

	t.Run("镜像档位走 docker build", func(t *testing.T) {
		drv := &imgDriver{}
		b := newDAGTestBuilder(drv, &markerCloner{file: "Dockerfile", content: "FROM scratch\n"})
		rep := &fakeReporter{}
		stage := pipeline.Stage{ID: "s", Name: "构建", Kind: pipeline.KindBuild,
			Jobs: []pipeline.Job{{ID: "j1", Name: "构建", Type: pipeline.JobTypeBuild, Config: map[string]any{
				pipeline.ConfigKeyArtifactType: pipeline.ArtifactImage, "dockerfilePath": "Dockerfile",
			}}}}
		if err := NewStageExecutor(b, nil)(context.Background(), &run.Run{ProjectID: "p1"}, stage, rep); err != nil {
			t.Fatalf("exec: %v", err)
		}
		if drv.buildCalls != 1 || drv.runCalls != 0 {
			t.Errorf("镜像档位应只调 Build(Build=%d Run=%d)", drv.buildCalls, drv.runCalls)
		}
	})

	// file 是当前档位;jar 是收敛前的历史取值 —— 存量流水线不改配置也要走同一条路。
	t.Run("产物档位走脚本容器", func(t *testing.T) { runTier(t, pipeline.ArtifactFile) })
	t.Run("历史 jar 档位仍走脚本容器", func(t *testing.T) { runTier(t, pipeline.ArtifactJAR) })
}

// 「构建后推送」开关必须真的决定推不推:关了就绝不碰 registry(只有 build_image 一路会推,
// 这里两态各测一次),否则推送时机由平台说了算 —— 而用户要的是「先只构建」。
func TestStageExecutorBuildImagePushSwitch(t *testing.T) {
	runCase := func(t *testing.T, pushImage any, wantPush bool, wantLog string) {
		t.Helper()
		drv := &imgDriver{}
		b := newDAGTestBuilder(drv, &markerCloner{file: "Dockerfile", content: "FROM scratch\n"})
		b.settings = fakeSettings{settings: &pipeline.Settings{Environments: []pipeline.Environment{{
			Name:          "prod",
			ImageRegistry: pipeline.ImageRegistry{Type: "docker", URL: "registry.example.com"},
		}}}}
		rep := &fakeReporter{}
		cfg := map[string]any{
			pipeline.ConfigKeyArtifactType: pipeline.ArtifactImage, "dockerfilePath": "Dockerfile",
		}
		if pushImage != nil {
			cfg[pipeline.ConfigKeyPushImage] = pushImage
		}
		stage := pipeline.Stage{ID: "s", Name: "构建", Kind: pipeline.KindBuild,
			Jobs: []pipeline.Job{{ID: "j1", Name: "构建", Type: pipeline.JobTypeBuild, Config: cfg}}}
		r := &run.Run{ProjectID: "p1", Trigger: run.Trigger{ResolvedEnvironment: "prod"}}
		if err := NewStageExecutor(b, nil)(context.Background(), r, stage, rep); err != nil {
			t.Fatalf("exec: %v", err)
		}
		if (drv.pushCalls > 0) != wantPush {
			t.Errorf("push 调用 = %v(pushCalls=%d),want %v", drv.pushCalls > 0, drv.pushCalls, wantPush)
		}
		if wantPush {
			if len(rep.arts) != 1 || !strings.HasPrefix(rep.arts[0].Reference, "registry.example.com/") {
				t.Errorf("产物远端引用未登记: %+v", rep.arts)
			}
		}
		if !strings.Contains(strings.Join(rep.logs, "\n"), wantLog) {
			t.Errorf("日志应含 %q,got %v", wantLog, rep.logs)
		}
	}

	t.Run("缺省推送", func(t *testing.T) { runCase(t, nil, true, "") })
	t.Run("开关关闭只构建", func(t *testing.T) { runCase(t, "false", false, "只构建、不推送") })
}

// TestStageExecutorBuildImageContextSubdir 验证 build_image 的「构建上下文(context)」配置接入:
// monorepo 子目录 Dockerfile(如 backend/Dockerfile 内 `COPY target/x.jar` 相对 backend/)
// 须把 docker build 的 context 设为该子目录,否则 docker 在仓库根找不到 COPY 源(走页面真部署
// 暴露的缺口:前端有 context 字段但后端从不消费,context 恒为仓库根)。
func TestStageExecutorBuildImageContextSubdir(t *testing.T) {
	drv := &imgDriver{}
	b := newDAGTestBuilder(drv, &markerCloner{})
	exec := NewStageExecutor(b, nil)
	rep := &fakeReporter{}

	stage := pipeline.Stage{ID: "s", Name: "构建", Kind: pipeline.KindBuild,
		Jobs: []pipeline.Job{{Name: "后端镜像", Type: "build_image", Config: map[string]any{
			"artifactType": "image", "buildModel": "dockerfile",
			"dockerfilePath": "backend/Dockerfile", "context": "backend",
		}}}}
	if err := exec(context.Background(), &run.Run{ProjectID: "p1"}, stage, rep); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if drv.buildCalls != 1 {
		t.Fatalf("应调用 driver.Build 一次,实际 %d", drv.buildCalls)
	}
	// context=backend → docker build 的 context 目录是 <workspace>/backend(非仓库根),
	// dockerfile 解析为相对仓库根的绝对路径 <workspace>/backend/Dockerfile(不被再 join 进子目录)。
	if !strings.HasSuffix(filepath.Clean(drv.gotContextDir), filepath.Join("backend")) {
		t.Fatalf("context 目录应为 <workspace>/backend,实际 %q", drv.gotContextDir)
	}
	if !strings.HasSuffix(filepath.Clean(drv.gotDockerfile), filepath.Join("backend", "Dockerfile")) {
		t.Fatalf("dockerfile 应为 <workspace>/backend/Dockerfile,实际 %q", drv.gotDockerfile)
	}
	if !filepath.IsAbs(drv.gotDockerfile) {
		t.Fatalf("context 非空时 dockerfile 应为绝对路径(避免被 driver 再 join),实际 %q", drv.gotDockerfile)
	}
}

func TestStageExecutorScriptFailureNonZero(t *testing.T) {
	drv := &recordingDriver{code: 1} // 容器非零退出
	b := newDAGTestBuilder(drv, &markerCloner{})
	exec := NewStageExecutor(b, nil)
	if err := exec(context.Background(), &run.Run{ProjectID: "p1"},
		scriptStage(scriptJob("unit", "busybox", "false")), &fakeReporter{}); err == nil {
		t.Error("expected ErrBuildFailed on nonzero container exit")
	}
}

func TestStageExecutorInvalidScriptConfig(t *testing.T) {
	drv := &recordingDriver{}
	b := newDAGTestBuilder(drv, &markerCloner{})
	exec := NewStageExecutor(b, nil)
	// script job 缺 image。
	bad := pipeline.Job{Name: "x", Type: "script", Config: map[string]any{"commands": "echo hi"}}
	if err := exec(context.Background(), &run.Run{ProjectID: "p1"}, scriptStage(bad), &fakeReporter{}); err == nil {
		t.Error("expected error for invalid script config")
	}
}

// ─── 真 Docker e2e(gated)─────────────────────────────────────────────────────

func TestE2EDockerStageExecutorParamsReal(t *testing.T) {
	drv := dockerReadyOrSkip(t)
	b := newDAGTestBuilder(drv, &markerCloner{})
	exec := NewStageExecutor(b, nil)
	rep := &fakeReporter{}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// 参数化运行:容器内 echo 注入的参数,断言真值被注入并执行。
	r := &run.Run{ProjectID: "p1", Trigger: run.Trigger{Branch: "main", Params: map[string]string{"PW_PARAM": "HELLO_FROM_PARAM"}}}
	job := scriptJob("verify", "busybox", `echo "p=$PW_PARAM"`)
	if err := exec(ctx, r, scriptStage(job), rep); err != nil {
		t.Fatalf("真 Docker 参数化执行失败: %v\n日志:\n%s", err, strings.Join(rep.logs, "\n"))
	}
	if !strings.Contains(strings.Join(rep.logs, "\n"), "p=HELLO_FROM_PARAM") {
		t.Errorf("容器未读到注入的运行参数;日志:\n%s", strings.Join(rep.logs, "\n"))
	}
}

func TestE2EDockerStageExecutorReal(t *testing.T) {
	drv := dockerReadyOrSkip(t)
	b := newDAGTestBuilder(drv, &markerCloner{file: "marker.txt", content: "PIPEWRIGHT_DAG_OK"})
	exec := NewStageExecutor(b, nil)
	rep := &fakeReporter{}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// 真容器:busybox 读回工作区里的标记文件 + echo 命令输出。
	job := scriptJob("verify", "busybox", "cat marker.txt\necho RAN-IN-CONTAINER")
	if err := exec(ctx, &run.Run{ProjectID: "p1", Trigger: run.Trigger{Branch: "main"}},
		scriptStage(job), rep); err != nil {
		t.Fatalf("真 Docker 阶段执行失败: %v\n日志:\n%s", err, strings.Join(rep.logs, "\n"))
	}
	logs := strings.Join(rep.logs, "\n")
	if !strings.Contains(logs, "PIPEWRIGHT_DAG_OK") {
		t.Errorf("容器未读到挂载的工作区文件;日志:\n%s", logs)
	}
	if !strings.Contains(logs, "RAN-IN-CONTAINER") {
		t.Errorf("容器命令未真实执行;日志:\n%s", logs)
	}
}

func TestRenderTemplate(t *testing.T) {
	ctx := map[string]string{"dir": "frontend", "cmd": "npm run build"}
	if got := renderTemplate("cd {{dir}} && {{cmd}}", ctx); got != "cd frontend && npm run build" {
		t.Fatalf("render = %q", got)
	}
	// 未知占位原样保留;无 {{ 零开销直返
	if got := renderTemplate("echo {{missing}} $HOME", ctx); got != "echo {{missing}} $HOME" {
		t.Fatalf("unknown placeholder = %q", got)
	}
}

func TestTemplateContextFreeParams(t *testing.T) {
	// 自由参数表(每行 key=value / key: value)+ config 字段都进上下文。
	cfg := map[string]any{"image": "node:20", "params": "dir=frontend\nbranch: main\n# 注释行无=跳过"}
	ctx := templateContext(cfg)
	if ctx["dir"] != "frontend" || ctx["branch"] != "main" || ctx["image"] != "node:20" {
		t.Fatalf("ctx = %+v", ctx)
	}
}

func TestScriptStepFromTemplatedJob(t *testing.T) {
	jb := pipeline.Job{ID: "j", Name: "自定义", Type: "templated", Config: map[string]any{
		"image": "node:{{ver}}", "ver": "20",
		"commandTemplate": "cd {{dir}}\nnpm ci", "dir": "web",
	}}
	step, err := scriptStepBuilder(testBuildEnvGate).scriptStepFromJob(jb, "构建")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if step.Image != "node:20" {
		t.Errorf("image = %q, want node:20", step.Image)
	}
	if len(step.Commands) != 2 || step.Commands[0] != "cd web" {
		t.Errorf("commands = %v", step.Commands)
	}
}

// stubStageDeployer 捕获 DeployForStage 的入参(只为断言 cfg 透传)。
type stubStageDeployer struct {
	gotCfg      map[string]string
	gotStrategy string
	gotServers  []string
}

func (d *stubStageDeployer) Deploy(context.Context, deploy.DeployInput) ([]deploy.TargetResult, error) {
	panic("Deploy not expected")
}
func (d *stubStageDeployer) RetryFailed(context.Context, deploy.RetryInput) ([]deploy.TargetResult, error) {
	panic("RetryFailed not expected")
}
func (d *stubStageDeployer) ContinueDeploy(context.Context, deploy.ContinueInput) ([]deploy.TargetResult, error) {
	panic("ContinueDeploy not expected")
}
func (d *stubStageDeployer) AbortDeploy(context.Context, deploy.AbortInput) ([]deploy.TargetResult, error) {
	panic("AbortDeploy not expected")
}
func (d *stubStageDeployer) DeployForStage(_ context.Context, _ string, serverIDs []string, cfg map[string]string, strategy string) ([]deploy.TargetResult, error) {
	d.gotCfg = cfg
	d.gotStrategy = strategy
	d.gotServers = serverIDs
	return []deploy.TargetResult{{ServerName: "srv", Status: run.TargetSuccess, Message: "ok"}}, nil
}

// TestRunDeployJobPassesImageParams 证 #55:部署节点把镜像产物参数
// (artifactType/artifactFrom/containerName/ports/runArgs)透传给 deploy.DeployForStage,
// 使流水线部署节点能部署 #51 的镜像产物(而非只透传 releaseBase/restartCommand)。
func TestRunDeployJobPassesImageParams(t *testing.T) {
	dep := &stubStageDeployer{}
	b := &Builder{deployer: dep}
	rep := &fakeReporter{}
	jb := pipeline.Job{ID: "d", Name: "部署", Type: "deploy_ssh", Config: map[string]any{
		"serverId":      "srv-1",
		"artifactType":  "image",
		"artifactFrom":  "j-api", // 产物来源任务:并行构建下指明发哪一件
		"containerName": "myapp",
		"ports":         "8080:80,9000:9000",
		"runArgs":       "-e KEY=v --restart always",
		"strategy":      "blue_green",
		"deployPath":    "/opt/app", // 文件态键仍应透传,不互斥
	}}
	if err := b.runDeployJob(context.Background(), rep, jb, "run-1", nil); err != nil {
		t.Fatalf("runDeployJob err: %v", err)
	}
	for k, want := range map[string]string{
		"artifactType":  "image",
		"artifactFrom":  "j-api",
		"containerName": "myapp",
		"ports":         "8080:80,9000:9000",
		"runArgs":       "-e KEY=v --restart always",
		"releaseBase":   "/opt/app",
	} {
		if got := dep.gotCfg[k]; got != want {
			t.Errorf("cfg[%q] = %q, want %q(完整 cfg=%+v)", k, got, want, dep.gotCfg)
		}
	}
	if dep.gotStrategy != "blue_green" {
		t.Errorf("strategy = %q, want blue_green", dep.gotStrategy)
	}
	// 空键不应混入(保持默认行为)。
	if _, ok := dep.gotCfg["restartCommand"]; ok {
		t.Errorf("空 restartCommand 不应入 cfg:%+v", dep.gotCfg)
	}
}

// TestRunDeployJobPassesHealthProbe 证部署节点的健康探测键透传给 DeployForStage
// (探测能力从撤销的 health_check 节点迁到了部署任务本身)。
func TestRunDeployJobPassesHealthProbe(t *testing.T) {
	dep := &stubStageDeployer{}
	b := &Builder{deployer: dep}
	rep := &fakeReporter{}
	jb := pipeline.Job{ID: "d", Name: "部署", Type: "deploy_ssh", Config: map[string]any{
		"serverId":      "srv-1",
		"healthProbe":   "http",
		"healthUrl":     "http://localhost:{{port}}/healthz",
		"healthRetries": "10",
		"healthInterval": "3",
	}}
	if err := b.runDeployJob(context.Background(), rep, jb, "run-1", map[string]string{"port": "8080"}); err != nil {
		t.Fatalf("runDeployJob err: %v", err)
	}
	if got := dep.gotCfg[deploy.CfgKeyHealthProbe]; got != "http" {
		t.Errorf("healthProbe = %q", got)
	}
	if got := dep.gotCfg[deploy.CfgKeyHealthURL]; got != "http://localhost:8080/healthz" {
		t.Errorf("healthUrl 应按运行参数渲染, got %q", got)
	}
	if got := dep.gotCfg[deploy.CfgKeyHealthRetries]; got != "10" {
		t.Errorf("healthRetries = %q", got)
	}
	if _, ok := dep.gotCfg[deploy.CfgKeyHealthTimeout]; ok {
		t.Errorf("未填的 healthTimeout 不应入 cfg(由领域默认):%+v", dep.gotCfg)
	}
}

// 探测方式选了却没填探测目标是半截配置:静默不探测就是假绿,必须当场判失败。
func TestRunDeployJobFailsOnIncompleteProbe(t *testing.T) {
	cases := []struct {
		name string
		cfg  map[string]any
		want string
	}{
		{"http 缺 url", map[string]any{"healthProbe": "http"}, "未填探测 URL"},
		{"命令探测缺命令", map[string]any{"healthProbe": "command"}, "未填探测命令"},
		{"探测方式非法", map[string]any{"healthProbe": "tcp"}, "非法"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dep := &stubStageDeployer{}
			b := &Builder{deployer: dep}
			rep := &fakeReporter{}
			cfg := map[string]any{"serverId": "srv-1"}
			for k, v := range tc.cfg {
				cfg[k] = v
			}
			jb := pipeline.Job{ID: "d", Name: "部署", Type: "deploy_ssh", Config: cfg}
			if err := b.runDeployJob(context.Background(), rep, jb, "run-1", nil); !errors.Is(err, ErrBuildFailed) {
				t.Fatalf("want ErrBuildFailed, got %v", err)
			}
			if dep.gotCfg != nil {
				t.Error("探测配置不完整时不应发起部署")
			}
			if !strings.Contains(strings.Join(rep.logs, "\n"), tc.want) {
				t.Errorf("日志应含 %q, got %v", tc.want, rep.logs)
			}
		})
	}
}

// ─── 阶段内 job 级 DAG 并发执行(横串竖并)─────────────────────────────────────────

// orderDriver 线程安全记录 RunToolchain 的调用顺序(按 image),并可按 image 配退出码 + 阻塞时长。
type orderDriver struct {
	mu    sync.Mutex
	order []string
	codes map[string]int // image → 退出码(缺省 0)
	block time.Duration  // >0 时每次调用阻塞,放大并发窗口
}

func (d *orderDriver) Binary() string { return "fake" }
func (d *orderDriver) RunToolchain(ctx context.Context, image, _, _, _ string, _ []string, _ []string, _ pipeline.Resource, onLine func(string, string)) (int, error) {
	if d.block > 0 {
		select {
		case <-time.After(d.block):
		case <-ctx.Done():
			return -1, ctx.Err()
		}
	}
	d.mu.Lock()
	d.order = append(d.order, image)
	code := d.codes[image]
	d.mu.Unlock()
	return code, nil
}
func (d *orderDriver) Build(context.Context, string, string, string, []string, []string, func(string, string)) (int, error) {
	panic("Build not expected")
}
func (d *orderDriver) Tag(context.Context, string, string, func(string, string)) (int, error) {
	panic("Tag not expected")
}
func (d *orderDriver) Login(context.Context, string, string, string, func(string, string)) (int, error) {
	panic("Login not expected")
}
func (d *orderDriver) Push(context.Context, string, func(string, string)) (int, error) {
	panic("Push not expected")
}
func (d *orderDriver) InspectImage(context.Context, string) (string, int64, error) {
	panic("InspectImage not expected")
}

func (d *orderDriver) ran(image string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, i := range d.order {
		if i == image {
			return true
		}
	}
	return false
}

// scriptJobID 构造一条带显式 ID + job 级 needs 的 script job(image 即用作顺序标记)。
func scriptJobID(id, image string, needs ...string) pipeline.Job {
	return pipeline.Job{
		ID:     id,
		Name:   id,
		Type:   pipeline.StepTypeScript,
		Config: map[string]any{"image": image, "commands": "echo " + id},
		Needs:  needs,
	}
}

// TestStageExecutorJobDAGRunsAllRespectingDeps:A、B 无依赖(并行),C needs [A,B](串行其后)。
// 三个 job 都执行,且 C 一定在 A、B 之后(DAG 路径生效)。
func TestStageExecutorJobDAGRunsAllRespectingDeps(t *testing.T) {
	drv := &orderDriver{codes: map[string]int{}, block: 20 * time.Millisecond}
	b := newDAGTestBuilder(drv, &markerCloner{})
	b.envGate = gateWithImages("imgA", "imgB", "imgC")
	exec := NewStageExecutor(b, nil)
	stage := pipeline.Stage{ID: "s1", Name: "构建", Kind: pipeline.KindBuild, Jobs: []pipeline.Job{
		scriptJobID("A", "imgA"),
		scriptJobID("B", "imgB"),
		scriptJobID("C", "imgC", "A", "B"),
	}}
	if err := exec(context.Background(), &run.Run{ProjectID: "p1"}, stage, &fakeReporter{}); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if len(drv.order) != 3 {
		t.Fatalf("应执行 3 个 job, got %v", drv.order)
	}
	if drv.order[len(drv.order)-1] != "imgC" {
		t.Errorf("C 必须在 A、B 之后(末位), got order=%v", drv.order)
	}
}

// TestStageExecutorJobDAGFailureSkipsDependents:A 失败 → 其下游 C 被跳过(不执行),阶段失败;
// 与 A 无依赖的 B 仍会执行。
func TestStageExecutorJobDAGFailureSkipsDependents(t *testing.T) {
	drv := &orderDriver{codes: map[string]int{"imgA": 1}} // A 非零退出 → 失败
	b := newDAGTestBuilder(drv, &markerCloner{})
	b.envGate = gateWithImages("imgA", "imgB", "imgC")
	exec := NewStageExecutor(b, nil)
	stage := pipeline.Stage{ID: "s1", Name: "构建", Kind: pipeline.KindBuild, Jobs: []pipeline.Job{
		scriptJobID("A", "imgA"),
		scriptJobID("B", "imgB"),
		scriptJobID("C", "imgC", "A"),
	}}
	err := exec(context.Background(), &run.Run{ProjectID: "p1"}, stage, &fakeReporter{})
	if !errors.Is(err, ErrBuildFailed) {
		t.Fatalf("A 失败应使阶段失败, err = %v", err)
	}
	if drv.ran("imgC") {
		t.Errorf("C 依赖失败的 A,应被跳过而非执行;order=%v", drv.order)
	}
	if !drv.ran("imgB") {
		t.Errorf("B 与 A 无依赖,应仍执行;order=%v", drv.order)
	}
}

// countingCloner 记录 Clone 调用次数:旧版类型分组路径全阶段共享一次克隆;job 级 DAG 路径每个需
// 工作区的 job 各克隆一次。用克隆次数确定性判定走了哪条路(不依赖并发时序)。
type countingCloner struct {
	mu sync.Mutex
	n  int
}

func (c *countingCloner) Clone(_ context.Context, _, _, _, _, _, destDir string) (*CloneResolved, error) {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	return &CloneResolved{CommitShort: "abc1234"}, nil
}

// TestSameStageNoNeedsJobsRunParallel:同阶段两个【无 needs】但都带 ID 的 job(=画布「并行节点」),
// 应走 job 级 DAG 并行路径(各自独立克隆工作区),而非旧版串行单工作区。回归 fix(parallel-jobs-same-stage)。
func TestSameStageNoNeedsJobsRunParallel(t *testing.T) {
	drv := &orderDriver{codes: map[string]int{}}
	cl := &countingCloner{}
	b := newDAGTestBuilder(drv, cl)
	b.envGate = gateWithImages("imgFE", "imgBE")
	exec := NewStageExecutor(b, nil)
	stage := pipeline.Stage{ID: "s1", Name: "构建", Kind: pipeline.KindBuild, Jobs: []pipeline.Job{
		scriptJobID("fe", "imgFE"),
		scriptJobID("be", "imgBE"),
	}}
	if err := exec(context.Background(), &run.Run{ProjectID: "p1"}, stage, &fakeReporter{}); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if len(drv.order) != 2 {
		t.Fatalf("两个 job 都应执行, got order=%v", drv.order)
	}
	if cl.n != 2 {
		t.Fatalf("无 needs 的两 job 应各自独立克隆工作区(DAG 并行路径);clone 次数=%d, want 2(=1 则退回了旧版串行单工作区)", cl.n)
	}
}

// TestSameStageSingleJobUsesLegacyPath:单 job 阶段仍走既有路径(共享单工作区,仅 1 次克隆),
// 不因多 job 并行改动引入额外克隆。
func TestSameStageSingleJobUsesLegacyPath(t *testing.T) {
	drv := &orderDriver{codes: map[string]int{}}
	cl := &countingCloner{}
	b := newDAGTestBuilder(drv, cl)
	b.envGate = gateWithImages("img1")
	exec := NewStageExecutor(b, nil)
	if err := exec(context.Background(), &run.Run{ProjectID: "p1"}, scriptStage(scriptJobID("only", "img1")), &fakeReporter{}); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if cl.n != 1 {
		t.Fatalf("单 job 阶段应共享单一克隆;clone 次数=%d, want 1", cl.n)
	}
}
