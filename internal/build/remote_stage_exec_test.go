package build

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/dagrun"
	"github.com/fuuhoo/pipewright/internal/pipeline"
	"github.com/fuuhoo/pipewright/internal/project"
	"github.com/fuuhoo/pipewright/internal/run"
	"github.com/fuuhoo/pipewright/internal/target"
)

// fakeRemoteTarget 记录远程 Exec 命令 + Upload/stdin 内容(校验派发 + 密钥安全)。
type fakeRemoteTarget struct {
	serverIDs    map[string]bool
	cmds         [][]string
	uploaded     []byte
	stdins       [][]byte // ExecWithStdin 收到的字节(逐次)
	stdinEcho    string   // ExecWithStdin 回显到 stdout 的内容(模拟远端命令的输出)
	streamChunks []string // ExecStreamWithExit 逐块回调的 stdout 片段
	exitCode     int      // ExecStreamWithExit 的退出码
}

func (f *fakeRemoteTarget) Exec(_ context.Context, serverID string, cmd []string) (*target.ExecResult, error) {
	if f.serverIDs == nil {
		f.serverIDs = map[string]bool{}
	}
	f.serverIDs[serverID] = true
	f.cmds = append(f.cmds, cmd)
	return &target.ExecResult{ExitCode: 0}, nil
}

func (f *fakeRemoteTarget) Upload(_ context.Context, serverID string, content io.Reader, _ string) error {
	if f.serverIDs == nil {
		f.serverIDs = map[string]bool{}
	}
	f.serverIDs[serverID] = true
	b, _ := io.ReadAll(content)
	f.uploaded = append(f.uploaded, b...)
	return nil
}

// ExecStream 默认不支持(这些用例没声明产物,不会走到);simRemote 覆盖它模拟远程归档。
func (f *fakeRemoteTarget) ExecStream(_ context.Context, serverID string, cmd []string) (io.ReadCloser, error) {
	if f.serverIDs == nil {
		f.serverIDs = map[string]bool{}
	}
	f.serverIDs[serverID] = true
	f.cmds = append(f.cmds, cmd)
	return nil, fmt.Errorf("fakeRemoteTarget: 未装配远程归档,不该被调用:%v", cmd)
}

// ExecWithStdin 记录 stdin 字节(用例据此断言口令/镜像字节只进管道、不进 argv)。
// stdinEcho 非空时把它当远程命令的输出回显,便于断言「字节真到了远端」。
func (f *fakeRemoteTarget) ExecWithStdin(_ context.Context, serverID string, cmd []string, stdin io.Reader) (*target.ExecResult, error) {
	if f.serverIDs == nil {
		f.serverIDs = map[string]bool{}
	}
	f.serverIDs[serverID] = true
	f.cmds = append(f.cmds, cmd)
	b, _ := io.ReadAll(stdin)
	f.stdins = append(f.stdins, b)
	return &target.ExecResult{ExitCode: 0, Stdout: f.stdinEcho}, nil
}

// ExecStreamWithExit 记命令后按 streamChunks 逐块回调,回 exitCode(远程流式的 fake 等价物)。
func (f *fakeRemoteTarget) ExecStreamWithExit(_ context.Context, serverID string, cmd []string, onChunk func(stream string, chunk []byte)) (int, error) {
	if f.serverIDs == nil {
		f.serverIDs = map[string]bool{}
	}
	f.serverIDs[serverID] = true
	f.cmds = append(f.cmds, cmd)
	for _, c := range f.streamChunks {
		if onChunk != nil {
			onChunk(streamStdout, []byte(c))
		}
	}
	return f.exitCode, nil
}

type fakeRunnerLookup struct {
	serverID string
}

func (l fakeRunnerLookup) RunnerFor(_ context.Context, _ string) (string, bool) {
	return l.serverID, l.serverID != ""
}

// 带 git token 的测试 Builder(校验 token 绝不上远程)。
func newRemoteTestBuilder(drv Driver) *Builder {
	return &Builder{
		projects: fakeProjects{proj: &project.Project{ID: "p1", RepoURL: "https://example.com/r.git", CredentialID: "cred"}},
		settings: fakeSettings{settings: &pipeline.Settings{}},
		vault:    fakeVault{secrets: map[string]string{"cred": gitTokenSecret}},
		driver:   drv,
		cloner:   &markerCloner{file: "README.md", content: "hi"},
		envGate:  testBuildEnvGate,
	}
}

const gitTokenSecret = "SUPERSECRET-GIT-TOKEN-xyz"

func TestStageExecutorDispatchesToRemote(t *testing.T) {
	local := &recordingDriver{}
	b := newRemoteTestBuilder(local)
	tgt := &fakeRemoteTarget{}
	exec := NewStageExecutorWithRunner(b, nil, fakeRunnerLookup{serverID: "srv-1"}, tgt)

	rep := &fakeReporter{}
	r := &run.Run{ID: "run-1", ProjectID: "p1", Trigger: run.Trigger{Branch: "main"}}
	stage := scriptStage(scriptJob("build", "node:20", "npm ci"))
	if err := exec(context.Background(), r, stage, rep); err != nil {
		t.Fatalf("remote exec: %v", err)
	}

	// 命令应打到远程 runner(srv-1),且本地 driver 绝不被调(构建下沉了)。
	if !tgt.serverIDs["srv-1"] {
		t.Fatalf("应派发到 srv-1;serverIDs=%v", tgt.serverIDs)
	}
	if local.callCount != 0 {
		t.Fatalf("远程派发时本地 driver 不应被调,实际 %d 次", local.callCount)
	}
	// 应上传工作区 tar.gz + 远端 untar + 远端 docker run。
	if len(tgt.uploaded) == 0 {
		t.Fatal("应上传工作区 tar.gz 到远程")
	}
	var sawUntar, sawDockerRun bool
	for _, c := range tgt.cmds {
		joined := strings.Join(c, " ")
		if strings.Contains(joined, "tar -xzf") {
			sawUntar = true
		}
		if len(c) >= 2 && c[0] == "docker" && c[1] == "run" {
			sawDockerRun = true
		}
	}
	if !sawUntar || !sawDockerRun {
		t.Fatalf("远程应 untar + docker run;untar=%v run=%v cmds=%v", sawUntar, sawDockerRun, tgt.cmds)
	}

	// 安全铁律:git token 绝不出现在任何远程命令 / 上传内容里(token 全程只在控制机)。
	for _, c := range tgt.cmds {
		if strings.Contains(strings.Join(c, " "), gitTokenSecret) {
			t.Fatalf("git token 泄漏进远程命令!%v", c)
		}
	}
	if bytes.Contains(tgt.uploaded, []byte(gitTokenSecret)) {
		t.Fatal("git token 泄漏进上传内容!")
	}
}

func TestStageExecutorLocalWhenNoRunner(t *testing.T) {
	local := &recordingDriver{}
	b := newRemoteTestBuilder(local)
	tgt := &fakeRemoteTarget{}
	// 无 runner(空)→ 走本地。
	exec := NewStageExecutorWithRunner(b, nil, fakeRunnerLookup{serverID: ""}, tgt)

	rep := &fakeReporter{}
	r := &run.Run{ID: "run-1", ProjectID: "p1", Trigger: run.Trigger{Branch: "main"}}
	if err := exec(context.Background(), r, scriptStage(scriptJob("build", "node:20", "echo hi")), rep); err != nil {
		t.Fatalf("local exec: %v", err)
	}
	if local.callCount != 1 {
		t.Fatalf("无 runner 应本地执行(driver 调 1 次),实际 %d", local.callCount)
	}
	if len(tgt.cmds) != 0 || len(tgt.uploaded) != 0 {
		t.Fatal("无 runner 不应有任何远程动作")
	}
}

// lookup/tgt 为 nil → 退化纯本地(不 panic)。
func TestStageExecutorNilRunnerIsLocal(t *testing.T) {
	local := &recordingDriver{}
	b := newRemoteTestBuilder(local)
	exec := NewStageExecutorWithRunner(b, nil, nil, nil)
	rep := &fakeReporter{}
	r := &run.Run{ID: "run-1", ProjectID: "p1", Trigger: run.Trigger{Branch: "main"}}
	if err := exec(context.Background(), r, scriptStage(scriptJob("b", "alpine", "true")), rep); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if local.callCount != 1 {
		t.Fatalf("nil runner 应纯本地,实际 driver 调 %d 次", local.callCount)
	}
}

// remoteScriptJob 造一条带 ID 的 script job(节点级上报按 jobID 归位,没 ID 无从断言)。
func remoteScriptJob(id, name, image, commands string) pipeline.Job {
	return pipeline.Job{ID: id, Name: name, Type: pipeline.StepTypeScript,
		Config: map[string]any{"image": image, "commands": commands}}
}

// nodeStageReporter 是能区分「哪条日志落在哪个节点」的 StageReporter 替身:
// JobReporter(jobID) 返回绑定该 job 的实例,于是能证明远程路径真的按节点归位。
type nodeStageReporter struct {
	running   []string
	done      []string
	byJob     map[string][]string
	stageLogs []string
	arts      []run.Artifact
}

func newNodeReporter() *nodeStageReporter {
	return &nodeStageReporter{byJob: map[string][]string{}}
}

func (r *nodeStageReporter) Log(_ context.Context, _, line string) error {
	r.stageLogs = append(r.stageLogs, line)
	return nil
}
func (r *nodeStageReporter) EmitArtifact(_ context.Context, a run.Artifact) error {
	r.arts = append(r.arts, a)
	return nil
}
func (r *nodeStageReporter) JobRunning(_ context.Context, jobID string) error {
	r.running = append(r.running, jobID)
	return nil
}
func (r *nodeStageReporter) JobDone(_ context.Context, jobID, status string) error {
	r.done = append(r.done, jobID+"="+status)
	return nil
}
func (r *nodeStageReporter) JobReporter(jobID string) dagrun.StageReporter {
	return &nodeJobReporter{root: r, jobID: jobID}
}

type nodeJobReporter struct {
	root  *nodeStageReporter
	jobID string
}

func (j *nodeJobReporter) Log(_ context.Context, _, line string) error {
	j.root.byJob[j.jobID] = append(j.root.byJob[j.jobID], line)
	return nil
}
func (j *nodeJobReporter) EmitArtifact(_ context.Context, a run.Artifact) error {
	j.root.arts = append(j.root.arts, a)
	return nil
}
func (j *nodeJobReporter) JobRunning(ctx context.Context, _ string) error {
	return j.root.JobRunning(ctx, j.jobID)
}
func (j *nodeJobReporter) JobDone(ctx context.Context, _, status string) error {
	return j.root.JobDone(ctx, j.jobID, status)
}
func (j *nodeJobReporter) JobReporter(string) dagrun.StageReporter { return j }

// 远程执行按节点上报进度,且远程容器的输出**边跑边**落到对应节点(分块拼成整行)。
// 少了这两件事,远程构建在执行详情里看起来就是「阶段第一个节点在动,别的都没动静」。
func TestRemoteStageReportsPerNodeProgress(t *testing.T) {
	tgt := &fakeRemoteTarget{streamChunks: []string{"npm htt", "p 200 ok\nnext"}}
	b := newRemoteTestBuilder(&recordingDriver{})
	exec := NewStageExecutorWithRunner(b, nil, fakeRunnerLookup{serverID: "srv-1"}, tgt)

	rep := newNodeReporter()
	r := &run.Run{ID: "run-7", ProjectID: "p1", Trigger: run.Trigger{Branch: "main"}}
	stage := pipeline.Stage{ID: "s1", Name: "构建", Kind: pipeline.KindBuild, Jobs: []pipeline.Job{
		remoteScriptJob("j-a", "前端构建", "node:20", "npm ci"),
		remoteScriptJob("j-b", "后端构建", "maven:3.9-eclipse-temurin-21", "mvn package"),
	}}
	if err := exec(context.Background(), r, stage, rep); err != nil {
		t.Fatalf("远程执行: %v", err)
	}

	if strings.Join(rep.running, ",") != "j-a,j-b" {
		t.Fatalf("两节点都应在开跑时标 running,实际 %v", rep.running)
	}
	if got := strings.Join(rep.done, ","); got != "j-a=success,j-b=success" {
		t.Fatalf("两节点都应按序标终态,实际 %q", got)
	}
	// 跨块拼出来的整行归**当前节点**,不落到阶段首个节点。
	if !strings.Contains(strings.Join(rep.byJob["j-a"], "\n"), "npm http 200 ok") {
		t.Fatalf("j-a 应拿到远程实时回流的行,实际 %v", rep.byJob["j-a"])
	}
	if len(rep.byJob["j-b"]) == 0 {
		t.Fatal("j-b 也该有自己的节点日志")
	}
}

// 私有构建镜像的登录在远程 runner 上发生(远程 driver),口令只进 SSH stdin 管道。
// recordingDriver.Login 会 panic —— 它没被调到,正是「没在中控机登录」的证据。
func TestRemoteLoginRunsOnRunner(t *testing.T) {
	tgt := &fakeRemoteTarget{stdinEcho: "Login Succeeded\n"}
	b := newRemoteTestBuilder(&recordingDriver{})
	b.vault = fakeVault{
		secrets: map[string]string{"c1": "hunter2"},
		users:   map[string]string{"c1": "bot"},
	}

	var lines []string
	b.loginForImage(context.Background(), NewRemoteDriver(tgt, "srv-9", "docker"),
		"reg.example.com/team/node:20", "c1", func(_, l string) { lines = append(lines, l) })

	var sawLogin bool
	for _, c := range tgt.cmds {
		if len(c) >= 2 && c[0] == "docker" && c[1] == "login" {
			sawLogin = true
			if strings.Contains(strings.Join(c, " "), "hunter2") {
				t.Fatalf("口令漏进远程 argv:%v", c)
			}
			if c[2] != "reg.example.com" {
				t.Fatalf("应登镜像仓主机,实际 %v", c)
			}
		}
	}
	if !sawLogin {
		t.Fatalf("远程机上应执行 docker login;cmds=%v", tgt.cmds)
	}
	if len(tgt.stdins) != 1 || string(tgt.stdins[0]) != "hunter2" {
		t.Fatalf("口令未经 stdin 管道送达远程:%q", tgt.stdins)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "Login Succeeded") {
		t.Fatalf("远程登录输出应回流到日志:%v", lines)
	}
}
