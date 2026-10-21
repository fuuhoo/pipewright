package build

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/pipeline"
	"github.com/fuuhoo/pipewright/internal/target"
)

// fakeRemoteExecer 是可控的 RemoteExecer:记录被投递的命令,按注入返回结果/错误。
// streamChunks 非空时,流式通路按这些块逐次回调(块边界刻意不等于行边界,用来验残行拼接)。
type fakeRemoteExecer struct {
	gotServerID  string
	gotCmds      [][]string
	result       *target.ExecResult
	err          error
	gotStdin     []byte
	streamChunks []string
}

func (f *fakeRemoteExecer) Exec(_ context.Context, serverID string, cmd []string) (*target.ExecResult, error) {
	f.gotServerID = serverID
	f.gotCmds = append(f.gotCmds, cmd)
	return f.result, f.err
}

func (f *fakeRemoteExecer) ExecWithStdin(_ context.Context, serverID string, cmd []string, stdin io.Reader) (*target.ExecResult, error) {
	f.gotServerID = serverID
	f.gotCmds = append(f.gotCmds, cmd)
	if stdin != nil {
		f.gotStdin, _ = io.ReadAll(stdin)
	}
	return f.result, f.err
}

func (f *fakeRemoteExecer) ExecStreamWithExit(_ context.Context, serverID string, cmd []string, onChunk func(stream string, chunk []byte)) (int, error) {
	f.gotServerID = serverID
	f.gotCmds = append(f.gotCmds, cmd)
	if f.err != nil {
		return -1, f.err
	}
	if onChunk != nil {
		for _, c := range f.streamChunks {
			onChunk(target.StreamStdout, []byte(c))
		}
	}
	if f.result == nil {
		return 0, nil // 只喂了 streamChunks 的用例:退出码默认 0
	}
	if onChunk != nil && len(f.streamChunks) == 0 {
		if f.result.Stdout != "" {
			onChunk(target.StreamStdout, []byte(f.result.Stdout))
		}
		if f.result.Stderr != "" {
			onChunk(target.StreamStderr, []byte(f.result.Stderr))
		}
	}
	return f.result.ExitCode, nil
}

func TestSSHCommanderRunMapsResult(t *testing.T) {
	f := &fakeRemoteExecer{result: &target.ExecResult{Stdout: "out", Stderr: "err", ExitCode: 7}}
	c := NewRemoteCommander(f, "srv-1")

	stdout, stderr, code, err := c.Run(context.Background(), "docker", []string{"build", "."}, "")
	if err != nil {
		t.Fatalf("Run err: %v", err)
	}
	if stdout != "out" || stderr != "err" || code != 7 {
		t.Fatalf("got (%q,%q,%d), want (out,err,7)", stdout, stderr, code)
	}
	// 命令必须 array 投递(program + args),且打到正确的远程机。
	if f.gotServerID != "srv-1" {
		t.Fatalf("serverID = %q, want srv-1", f.gotServerID)
	}
	if len(f.gotCmds) != 1 || len(f.gotCmds[0]) != 3 ||
		f.gotCmds[0][0] != "docker" || f.gotCmds[0][1] != "build" || f.gotCmds[0][2] != "." {
		t.Fatalf("cmd = %v, want [docker build .]", f.gotCmds)
	}
}

// TestSSHCommanderRunStreamsStdin 验远程模式的 secret-stdin:口令经管道送达,绝不进 argv。
// (docker login --password-stdin 走的就是这条,所以远程模式不再需要「预先在远程机登录」。)
func TestSSHCommanderRunStreamsStdin(t *testing.T) {
	f := &fakeRemoteExecer{result: &target.ExecResult{Stdout: "Login Succeeded\n", ExitCode: 0}}
	c := NewRemoteCommander(f, "srv-1")

	stdout, _, code, err := c.Run(context.Background(), "docker",
		[]string{"login", "registry.example.com", "-u", "bot", "--password-stdin"}, "hunter2")
	if err != nil {
		t.Fatalf("Run err: %v", err)
	}
	if code != 0 || !strings.Contains(stdout, "Login Succeeded") {
		t.Fatalf("登录结果未按原样取回: code=%d stdout=%q", code, stdout)
	}
	if string(f.gotStdin) != "hunter2" {
		t.Fatalf("口令未作为 stdin 送达:%q", f.gotStdin)
	}
	if strings.Contains(strings.Join(f.gotCmds[0], " "), "hunter2") {
		t.Fatalf("口令漏进 argv:%v", f.gotCmds[0])
	}
}

func TestSSHCommanderRunSurfacesExecError(t *testing.T) {
	f := &fakeRemoteExecer{err: errors.New("ssh: connection refused")}
	c := NewRemoteCommander(f, "srv-1")
	_, _, code, err := c.Run(context.Background(), "docker", []string{"ps"}, "")
	if err == nil {
		t.Fatal("want exec error surfaced")
	}
	if code != -1 {
		t.Fatalf("code = %d, want -1 (无法在远程启动)", code)
	}
}

func TestSSHCommanderStreamReplaysLines(t *testing.T) {
	f := &fakeRemoteExecer{result: &target.ExecResult{
		Stdout:   "line1\nline2\n",
		Stderr:   "warn1\n",
		ExitCode: 0,
	}}
	c := NewRemoteCommander(f, "srv-1")

	type ev struct {
		stream, line string
	}
	var got []ev
	code, err := c.Stream(context.Background(), "sh", []string{"-c", "echo line1; echo line2"}, "", func(stream, line string) {
		got = append(got, ev{stream, line})
	})
	if err != nil {
		t.Fatalf("Stream err: %v", err)
	}
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	want := []ev{{"stdout", "line1"}, {"stdout", "line2"}, {"stderr", "warn1"}}
	if len(got) != len(want) {
		t.Fatalf("got %d lines %v, want %v", len(got), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d = %v, want %v", i, got[i], want[i])
		}
	}
}

// TestSSHCommanderStreamSplitsAcrossChunks 验残行拼接:块边界落在行中间时,
// 半行先进缓冲、补全了才成行,末尾无换行的一段也在结束时放出(不因分块而丢字或多出空行)。
func TestSSHCommanderStreamSplitsAcrossChunks(t *testing.T) {
	f := &fakeRemoteExecer{streamChunks: []string{"npm htt", "p 200", " https://x\nnext line", "\nno newline tail"}}
	c := NewRemoteCommander(f, "srv-1")

	var got []string
	code, err := c.Stream(context.Background(), "sh", []string{"-c", "npm ci"}, "", func(_, line string) {
		got = append(got, line)
	})
	if err != nil {
		t.Fatalf("Stream err: %v", err)
	}
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	want := []string{"npm http 200 https://x", "next line", "no newline tail"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("逐行还原 = %v, want %v", got, want)
	}
}

func TestSSHCommanderStreamReturnsRemoteExitCode(t *testing.T) {
	f := &fakeRemoteExecer{result: &target.ExecResult{Stdout: "", Stderr: "boom", ExitCode: 2}}
	c := NewRemoteCommander(f, "srv-1")
	code, err := c.Stream(context.Background(), "false", nil, "", func(string, string) {})
	if err != nil {
		t.Fatalf("Stream err: %v", err)
	}
	if code != 2 {
		t.Fatalf("远程非零退出应原样取回,code = %d want 2", code)
	}
}

// NewRemoteDriver 应产出一个 Binary() 为指定 CLI 的 Driver,且其命令经远程 Commander 投递。
func TestNewRemoteDriverUsesRemoteCommander(t *testing.T) {
	f := &fakeRemoteExecer{result: &target.ExecResult{ExitCode: 0}}
	drv := NewRemoteDriver(f, "srv-1", "")
	if drv.Binary() != "docker" {
		t.Fatalf("默认 Binary = %q, want docker", drv.Binary())
	}
	// 触发一次 RunToolchain:应经远程机执行 `docker run ...`(命令打到 srv-1);
	// 资源规格(cpu/memory)应透传为 --cpus/--memory 投到远程 docker。
	_, err := drv.RunToolchain(context.Background(), "node:20", "/ws", "/src", "/src", nil, []string{"sh", "-c", "npm ci"}, pipeline.Resource{CPU: "2", Memory: "1g"}, func(string, string) {})
	if err != nil {
		t.Fatalf("RunToolchain err: %v", err)
	}
	if f.gotServerID != "srv-1" || len(f.gotCmds) == 0 || f.gotCmds[0][0] != "docker" {
		t.Fatalf("远程工具链命令未经 srv-1/docker 投递:server=%q cmds=%v", f.gotServerID, f.gotCmds)
	}
	if !cmdContainsPair(f.gotCmds[0], "--cpus", "2") || !cmdContainsPair(f.gotCmds[0], "--memory", "1g") {
		t.Fatalf("远程 docker run 未透传资源规格:%v", f.gotCmds[0])
	}

	// podman:Binary 与命令前缀应随之变化。
	drv2 := NewRemoteDriver(f, "srv-2", "podman")
	if drv2.Binary() != "podman" {
		t.Fatalf("Binary = %q, want podman", drv2.Binary())
	}
}

// cmdContainsPair 判断 args 中存在相邻的 (flag, value) 对。
func cmdContainsPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
