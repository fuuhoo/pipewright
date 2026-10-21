package build

// remote_image_test.go 验远程 runner 缺构建镜像时的三条岔路:已有 → 不动;架构一致 → 控制机推;
// 架构不一致 / 控制机没有 / 推失败 → 只记一行说明,绝不判构建失败(远程还有自己拉这条路)。

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/pipeline"
	"github.com/fuuhoo/pipewright/internal/run"
	"github.com/fuuhoo/pipewright/internal/target"
)

// pushTarget 是按命令语义响应的远程 fake:认出 image inspect / uname -m / docker load。
type pushTarget struct {
	imagePresent bool
	machineArch  string // 空 → uname -m 失败
	loadExit     int
	loadErr      error
	execErr      error // image inspect 阶段的 SSH 错误(模拟连不上)

	inspectCmds [][]string
	loadCmd     []string
	loaded      []byte
}

func (p *pushTarget) Exec(_ context.Context, _ string, cmd []string) (*target.ExecResult, error) {
	switch {
	case len(cmd) == 4 && cmd[0] == "docker" && cmd[1] == "image" && cmd[2] == "inspect":
		p.inspectCmds = append(p.inspectCmds, cmd)
		if p.execErr != nil {
			return nil, p.execErr
		}
		if p.imagePresent {
			return &target.ExecResult{}, nil
		}
		return &target.ExecResult{ExitCode: 1, Stderr: "Error: No such image: " + cmd[2]}, nil
	case len(cmd) == 2 && cmd[0] == "uname" && cmd[1] == "-m":
		if p.machineArch == "" {
			return &target.ExecResult{ExitCode: 1}, nil
		}
		return &target.ExecResult{Stdout: p.machineArch + "\n"}, nil
	}
	return &target.ExecResult{}, nil
}

func (p *pushTarget) ExecWithStdin(_ context.Context, _ string, cmd []string, stdin io.Reader) (*target.ExecResult, error) {
	p.loadCmd = cmd
	b, _ := io.ReadAll(stdin)
	p.loaded = b
	if p.loadErr != nil {
		return nil, p.loadErr
	}
	return &target.ExecResult{ExitCode: p.loadExit, Stderr: "no space left on device"}, nil
}

func (p *pushTarget) ExecStreamWithExit(context.Context, string, []string, func(string, []byte)) (int, error) {
	return 0, nil
}
func (p *pushTarget) Upload(context.Context, string, io.Reader, string) error { return nil }
func (p *pushTarget) ExecStream(context.Context, string, []string) (io.ReadCloser, error) {
	return nil, errors.New("pushTarget: 产物回传不该被这些用例触发")
}

// fakeImageSource 替换控制机侧的镜像导出(默认真 docker CLI,用例里给假字节/假架构)。
type fakeImageSource struct {
	arch      string
	archErr   error
	payload   []byte
	saveErr   error
	gotImage  string
	saveCalls int
}

func (f *fakeImageSource) Arch(context.Context, string) (string, error) { return f.arch, f.archErr }

func (f *fakeImageSource) Save(_ context.Context, image string) (io.ReadCloser, error) {
	f.saveCalls++
	f.gotImage = image
	if f.saveErr != nil {
		return nil, f.saveErr
	}
	return io.NopCloser(bytes.NewReader(f.payload)), nil
}

func installImageSource(t *testing.T, src remoteImageSource) {
	t.Helper()
	old := newRemoteImageSource
	newRemoteImageSource = func(string) remoteImageSource { return src }
	t.Cleanup(func() { newRemoteImageSource = old })
}

// collectLines 跑 ensureRemoteImage 并回它记下的日志。
func collectLines(t *testing.T, b *Builder, tgt remoteExec, image string) string {
	t.Helper()
	var got []string
	b.ensureRemoteImage(context.Background(), tgt, "srv-1", image, func(_, line string) { got = append(got, line) })
	return strings.Join(got, "\n")
}

func newPushBuilder() *Builder {
	return &Builder{driver: &recordingDriver{}}
}

// 远程已有镜像 → 一步不做(不 save、不 load),这正是「每次现探」的用意:被人 rmi 了下次自动重推。
func TestEnsureRemoteImageNoopWhenPresent(t *testing.T) {
	src := &fakeImageSource{}
	installImageSource(t, src)
	tgt := &pushTarget{imagePresent: true}

	logs := collectLines(t, newPushBuilder(), tgt, "node:26-alpine")
	if !strings.Contains(logs, "已有构建镜像 node:26-alpine") {
		t.Fatalf("应记下命中,实际:%q", logs)
	}
	if src.saveCalls != 0 || len(tgt.loadCmd) != 0 {
		t.Fatalf("已有镜像不该触发导出/加载:save=%d loadCmd=%v", src.saveCalls, tgt.loadCmd)
	}
}

// 架构一致(镜像 arm64 / 远程 aarch64)→ save 的字节经 docker load 的 stdin 落到远程。
func TestEnsureRemoteImagePushesWhenArchMatches(t *testing.T) {
	src := &fakeImageSource{arch: "arm64", payload: []byte("TARBYTES")}
	installImageSource(t, src)
	tgt := &pushTarget{machineArch: "aarch64"}

	logs := collectLines(t, newPushBuilder(), tgt, "node:26-alpine")
	if string(tgt.loaded) != "TARBYTES" {
		t.Fatalf("镜像字节未走 stdin 送达远程:%q", tgt.loaded)
	}
	if len(tgt.loadCmd) != 2 || tgt.loadCmd[0] != "docker" || tgt.loadCmd[1] != "load" {
		t.Fatalf("应以 docker load 接收:%v", tgt.loadCmd)
	}
	if src.gotImage != "node:26-alpine" {
		t.Fatalf("导出的应是本节点用的镜像,实际 %q", src.gotImage)
	}
	if !strings.Contains(logs, "已推送构建镜像 node:26-alpine") {
		t.Fatalf("成功应记一行,实际:%q", logs)
	}
	// 探测用 image inspect + uname -m 两条,都是平台基础设施命令(不含用户命令)。
	if len(tgt.inspectCmds) != 1 || tgt.inspectCmds[0][3] != "node:26-alpine" {
		t.Fatalf("应先按镜像名探测远程:%v", tgt.inspectCmds)
	}
}

// 架构不一致 → 只提醒,绝不推(load 会成功、run 起不来,推过去更误导)。
func TestEnsureRemoteImageArchGate(t *testing.T) {
	src := &fakeImageSource{arch: "amd64", payload: []byte("NOPE")}
	installImageSource(t, src)
	tgt := &pushTarget{machineArch: "aarch64"}

	logs := collectLines(t, newPushBuilder(), tgt, "reg.example.com/java:17")
	if !strings.Contains(logs, "架构不一致") {
		t.Fatalf("应提醒架构不一致,实际:%q", logs)
	}
	if src.saveCalls != 0 || len(tgt.loaded) != 0 {
		t.Fatalf("架构不一致时不该推送任何字节:save=%d loaded=%d", src.saveCalls, len(tgt.loaded))
	}
}

// 控制机本地没有这个镜像 → 无能为力,说清楚并交远程自己拉(不谎报已推送)。
func TestEnsureRemoteImageControlMissingImage(t *testing.T) {
	src := &fakeImageSource{archErr: errors.New("控制机本地没有镜像 x(No such image)")}
	installImageSource(t, src)
	tgt := &pushTarget{machineArch: "arm64"}

	logs := collectLines(t, newPushBuilder(), tgt, "x")
	if !strings.Contains(logs, "无法推送") || !strings.Contains(logs, "自行从仓库拉取") {
		t.Fatalf("应说明无法推送并交远程拉取,实际:%q", logs)
	}
	if len(tgt.loaded) != 0 {
		t.Fatal("控制机没有镜像时不该有 load 动作")
	}
}

// 远程架构取不到 → 不推(比对不了就别猜)。
func TestEnsureRemoteImageUnknownRunnerArch(t *testing.T) {
	src := &fakeImageSource{arch: "arm64", payload: []byte("BYTES")}
	installImageSource(t, src)
	tgt := &pushTarget{machineArch: ""} // uname -m 退出非零

	logs := collectLines(t, newPushBuilder(), tgt, "x")
	if !strings.Contains(logs, "无法确认远程 runner 的 CPU 架构") {
		t.Fatalf("应说明无法确认架构,实际:%q", logs)
	}
	if len(tgt.loaded) != 0 {
		t.Fatal("架构未知时不该推送")
	}
}

// docker load 失败(如磁盘满)→ 一行告警,不 panic、不判失败。
func TestEnsureRemoteImageLoadFailureIsWarnOnly(t *testing.T) {
	installImageSource(t, &fakeImageSource{arch: "arm64", payload: []byte("BYTES")})
	tgt := &pushTarget{machineArch: "arm64", loadExit: 1}

	logs := collectLines(t, newPushBuilder(), tgt, "x")
	if !strings.Contains(logs, "docker load 未成功") || !strings.Contains(logs, "no space left on device") {
		t.Fatalf("应把远程 load 的失败原因带出来,实际:%q", logs)
	}
}

// SSH 都连不上远程 → 探测失败也只是跳过推送,不打断构建。
func TestEnsureRemoteImageProbeFailureSkips(t *testing.T) {
	src := &fakeImageSource{arch: "arm64", payload: []byte("BYTES")}
	installImageSource(t, src)
	tgt := &pushTarget{execErr: errors.New("ssh: connection refused")}

	logs := collectLines(t, newPushBuilder(), tgt, "x")
	if !strings.Contains(logs, "无法确认远程 runner 是否已有构建镜像") {
		t.Fatalf("探测失败应记一行并跳过,实际:%q", logs)
	}
	if src.saveCalls != 0 {
		t.Fatal("探测失败时不该开始导出")
	}
}

// 空镜像名(该节点没用容器)→ 直接返回,不产生任何远程命令。
func TestEnsureRemoteImageEmptyImage(t *testing.T) {
	tgt := &pushTarget{}
	var lines []string
	newPushBuilder().ensureRemoteImage(context.Background(), tgt, "srv-1", "  ", func(_, l string) { lines = append(lines, l) })
	if len(lines) != 0 || len(tgt.inspectCmds) != 0 {
		t.Fatalf("空镜像不该有动作:lines=%v cmds=%v", lines, tgt.inspectCmds)
	}
}

func TestNormalizeArch(t *testing.T) {
	cases := map[string]string{
		"x86_64": "amd64", "amd64": "amd64", " AMD64 ": "amd64",
		"aarch64": "arm64", "arm64": "arm64",
		"i686": "386", "armv7l": "", "riscv64": "", "": "",
	}
	for in, want := range cases {
		if got := normalizeArch(in); got != want {
			t.Errorf("normalizeArch(%q) = %q, want %q", in, got, want)
		}
	}
}

// 接线用例:证明远程路径在 `docker run` 之前真的试过补镜像,而且推送的日志落在**这个节点**上。
// 上面的用例只喂 ensureRemoteImage 本身;这里跑的是整条 script job,缺了它就等于功能写好了没接上。
func TestRemoteStagePushesMissingImageBeforeRunningJob(t *testing.T) {
	tgt := &fakeRemoteTarget{imageAbsent: true, runnerArch: "aarch64"}
	installImageSource(t, &fakeImageSource{arch: "arm64", payload: []byte("IMGBYTES")})

	b := newRemoteTestBuilder(&recordingDriver{})
	exec := NewStageExecutorWithRunner(b, nil, fakeRunnerLookup{serverID: "srv-1"}, tgt)
	rep := newNodeReporter()
	r := &run.Run{ID: "run-8", ProjectID: "p1", Trigger: run.Trigger{Branch: "main"}}
	stage := pipeline.Stage{ID: "s1", Name: "构建", Kind: pipeline.KindBuild, Jobs: []pipeline.Job{
		remoteScriptJob("j-p", "前端构建", "node:20", "npm ci"),
	}}
	if err := exec(context.Background(), r, stage, rep); err != nil {
		t.Fatalf("远程执行失败:%v", err)
	}

	jobLog := strings.Join(rep.byJob["j-p"], "\n")
	if !strings.Contains(jobLog, "→ 远程 runner 缺少构建镜像 node:20") || !strings.Contains(jobLog, "✓ 已推送构建镜像 node:20") {
		t.Fatalf("缺镜像时应先说明再报推送成功(日志归本节点),实际:%q", jobLog)
	}
	var wantsLoad, gotBytes bool
	for _, c := range tgt.cmds {
		if len(c) == 2 && c[0] == "docker" && c[1] == "load" {
			wantsLoad = true
		}
	}
	for _, chunk := range tgt.stdins {
		if string(chunk) == "IMGBYTES" {
			gotBytes = true
		}
	}
	if !wantsLoad || !gotBytes {
		t.Fatalf("镜像字节应经 docker load 的 stdin 送达远程(load=%v bytes=%v):%v", wantsLoad, gotBytes, tgt.cmds)
	}
	// 推送失败也不该把构建判死:这里成功,节点仍归 success(证明走的是同一节点收尾)。
	if strings.Join(rep.done, ",") != "j-p=success" {
		t.Fatalf("节点终态应归本 job,实际 %v", rep.done)
	}
}
