package proxy

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/target"
)

// 起反代容器是整条链路里最容易「报错但说不清为什么」的一步:端口被占、ghcr.io 连不上、
// docker 没装,过去都会塌成同一句「启动反代容器失败」。这组测试钉住分类与诊断信息。

func TestListenOnPort(t *testing.T) {
	cases := []struct {
		name string
		out  string
		port int
		want bool
	}{
		{"ss_ipv4", "State Recv-Q Send-Q Local Address:Port Peer Address:Port\nLISTEN 0 128 0.0.0.0:80 0.0.0.0:*", 80, true},
		{"ss_ipv6", "LISTEN 0 128 [::]:443 [::]:*", 443, true},
		{"netstat_macos", "tcp4 0 0 *.80 *.* LISTEN", 80, true},
		{"netstat_loopback", "tcp46 0 0 127.0.0.1.443 *.* LISTEN", 443, true},
		// 子串匹配会把 :8080 当成 :80 —— 正是老实现误报/漏报的来源。
		{"ss_8080_not_80", "LISTEN 0 128 0.0.0.0:8080 0.0.0.0:*", 80, false},
		{"netstat_8080_not_80", "tcp4 0 0 *.8080 *.* LISTEN", 80, false},
		{"empty", "", 443, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := listenOnPort(c.out, c.port); got != c.want {
				t.Fatalf("listenOnPort(%q, %d) = %v, want %v", c.out, c.port, got, c.want)
			}
		})
	}
}

// macOS/BSD 目标主机没有 ss:探测要退到 netstat,而不是当成「无法确认」放行。
func TestPortsBusyFallsBackToNetstat(t *testing.T) {
	ft := &fakeTarget{resultFor: func(cmd []string) *target.ExecResult {
		switch cmd[0] {
		case "ss":
			return &target.ExecResult{ExitCode: 127, Stderr: "sh: ss: command not found"}
		case "netstat":
			return &target.ExecResult{ExitCode: 0, Stdout: "tcp4 0 0 *.443 *.* LISTEN"}
		}
		return nil
	}}
	busy, detail := portsBusy(context.Background(), ft, "srv-1")
	if !busy || !strings.Contains(detail, ":443") {
		t.Fatalf("netstat 回退应发现 443 占用, got busy=%v detail=%q", busy, detail)
	}
}

func TestPortsBusyUnknownWhenNoProbe(t *testing.T) {
	ft := &fakeTarget{resultFor: func(cmd []string) *target.ExecResult {
		return &target.ExecResult{ExitCode: 127}
	}}
	if busy, detail := portsBusy(context.Background(), ft, "srv-1"); busy || detail != "" {
		t.Fatalf("两条探测都不可用时应返回「无法确认」, got busy=%v detail=%q", busy, detail)
	}
}

// docker run 报端口被占 → 归为端口冲突(让用户去腾 80/443),而不是「docker 没装」。
func TestRunCaddyContainerClassifiesPortAllocation(t *testing.T) {
	ft := &fakeTarget{resultFor: func(cmd []string) *target.ExecResult {
		if cmd[0] == "docker" && len(cmd) > 1 && cmd[1] == "run" {
			return &target.ExecResult{ExitCode: 125, Stderr: "docker: Error response from daemon: Bind for 0.0.0.0:80 failed: port is already allocated"}
		}
		return nil
	}}
	err := runCaddyContainer(context.Background(), ft, "srv-1", "ghcr.io/fuuhoo/pipewright-caddy:latest", map[int]int{80: 80, 443: 443})
	if !errors.Is(err, ErrPortConflict) {
		t.Fatalf("应归为 ErrPortConflict, got %v", err)
	}
	if d := DetailOf(err); !strings.Contains(d, "port is already allocated") {
		t.Fatalf("诊断信息应保留 docker 原文, got %q", d)
	}
}

// 镜像拉不下来且控制机也推不了 → 明确报拉取失败(指向 ghcr.io / PIPEWRIGHT_CADDY_IMAGE),不放行到 docker run。
func TestRunCaddyContainerImagePullFailure(t *testing.T) {
	withFakeImageLocal(t, &fakeImageLocal{archErr: errors.New("控制机本地没有镜像")})
	ft := &fakeTarget{resultFor: func(cmd []string) *target.ExecResult {
		switch {
		case cmd[0] == "docker" && len(cmd) > 1 && cmd[1] == "pull":
			return &target.ExecResult{ExitCode: 1, Stderr: "Error response from daemon: Get \"https://ghcr.io/v2/\": dial tcp: i/o timeout"}
		case cmd[0] == "docker" && len(cmd) > 1 && cmd[1] == "image":
			return &target.ExecResult{ExitCode: 1, Stderr: "Error: No such image"}
		}
		return nil
	}}
	err := runCaddyContainer(context.Background(), ft, "srv-1", "ghcr.io/fuuhoo/pipewright-caddy:latest", map[int]int{80: 80})
	if !errors.Is(err, ErrCaddyImagePull) {
		t.Fatalf("应报 ErrCaddyImagePull, got %v", err)
	}
	for _, call := range ft.execCalls {
		if len(call) > 1 && call[0] == "docker" && call[1] == "run" {
			t.Fatalf("本地没镜像时不该继续 docker run: %v", call)
		}
	}
}

// 目标机拉不到、也没有该镜像时,把控制机本地那份推过去(save|gzip → SSH 管道 → 远程 docker load),
// 推完照常起容器 —— 受限网络里这就是唯一走得通的路。
func TestRunCaddyContainerPushesLocalImageWhenRemoteCannotPull(t *testing.T) {
	withFakeImageLocal(t, &fakeImageLocal{arch: "amd64", payload: "FAKE-TAR-BYTES"})
	ft := &fakeTarget{resultFor: func(cmd []string) *target.ExecResult {
		switch {
		case cmd[0] == "docker" && len(cmd) > 1 && cmd[1] == "pull":
			return &target.ExecResult{ExitCode: 1, Stderr: "dial tcp: i/o timeout"}
		case cmd[0] == "docker" && len(cmd) > 1 && cmd[1] == "image":
			return &target.ExecResult{ExitCode: 1, Stderr: "No such image"}
		case cmd[0] == "uname":
			return &target.ExecResult{ExitCode: 0, Stdout: "x86_64\n"}
		}
		return nil
	}}
	err := runCaddyContainer(context.Background(), ft, "srv-1", "ghcr.io/fuuhoo/pipewright-caddy:latest", map[int]int{80: 80})
	if err != nil {
		t.Fatalf("推送成功后应照常起容器, got %v", err)
	}
	if len(ft.stdinCalls) != 1 || strings.Join(ft.stdinCalls[0], " ") != "docker load" {
		t.Fatalf("应以 docker load 接收镜像字节, got %v", ft.stdinCalls)
	}
	if string(ft.stdinBytes) != "FAKE-TAR-BYTES" {
		t.Fatalf("推送到目标机的字节应与本地导出流一致, got %q", string(ft.stdinBytes))
	}
	ran := false
	for _, call := range ft.execCalls {
		if len(call) > 1 && call[0] == "docker" && call[1] == "run" {
			ran = true
		}
	}
	if !ran {
		t.Fatal("推送成功后应执行 docker run")
	}
}

// 架构不一致:docker load 会得逞、run 却报 exec format error —— 不推,并说清两边架构。
func TestPullCaddyImageRefusesCrossArchPush(t *testing.T) {
	withFakeImageLocal(t, &fakeImageLocal{arch: "amd64", payload: "X"})
	ft := &fakeTarget{resultFor: pullFailedAndNoImage(func(cmd []string) *target.ExecResult {
		if cmd[0] == "uname" {
			return &target.ExecResult{ExitCode: 0, Stdout: "aarch64"}
		}
		return nil
	})}
	err := pullCaddyImage(context.Background(), ft, "srv-1", "ghcr.io/fuuhoo/pipewright-caddy:latest")
	if !errors.Is(err, ErrCaddyImagePull) {
		t.Fatalf("应报 ErrCaddyImagePull, got %v", err)
	}
	if d := DetailOf(err); !strings.Contains(d, "架构不一致") {
		t.Fatalf("应说明架构不一致, got %q", d)
	}
	if len(ft.stdinCalls) != 0 {
		t.Fatalf("架构不一致时不该推送, got %v", ft.stdinCalls)
	}
}

// 控制机没装 docker / 没有该镜像 → 不推,原因带回错误详情(而不是静默失败)。
func TestPullCaddyImageReportsMissingLocalSource(t *testing.T) {
	old := newCaddyImageLocal
	newCaddyImageLocal = func() (caddyImageLocal, error) {
		return nil, errors.New("控制机上找不到 docker 命令")
	}
	t.Cleanup(func() { newCaddyImageLocal = old })

	ft := &fakeTarget{resultFor: pullFailedAndNoImage(nil)}
	err := pullCaddyImage(context.Background(), ft, "srv-1", "ghcr.io/fuuhoo/pipewright-caddy:latest")
	if !errors.Is(err, ErrCaddyImagePull) {
		t.Fatalf("应报 ErrCaddyImagePull, got %v", err)
	}
	for _, want := range []string{"控制机上找不到 docker 命令", "i/o timeout"} {
		if d := DetailOf(err); !strings.Contains(d, want) {
			t.Fatalf("错误详情应同时带上拉取失败与推送失败原因(%q), got %q", want, d)
		}
	}
}

// docker load 在目标机失败(如磁盘满)→ 报拉取失败并保留远端原文,不放行到 docker run。
func TestPullCaddyImageReportsRemoteLoadFailure(t *testing.T) {
	withFakeImageLocal(t, &fakeImageLocal{arch: "amd64", payload: "X"})
	ft := &fakeTarget{
		resultFor: pullFailedAndNoImage(func(cmd []string) *target.ExecResult {
			if cmd[0] == "uname" {
				return &target.ExecResult{ExitCode: 0, Stdout: "x86_64"}
			}
			return nil
		}),
		stdinResult: &target.ExecResult{ExitCode: 1, Stderr: "no space left on device"},
	}
	err := pullCaddyImage(context.Background(), ft, "srv-1", "ghcr.io/fuuhoo/pipewright-caddy:latest")
	if !errors.Is(err, ErrCaddyImagePull) {
		t.Fatalf("应报 ErrCaddyImagePull, got %v", err)
	}
	if d := DetailOf(err); !strings.Contains(d, "no space left on device") {
		t.Fatalf("应保留远端 load 的报错原文, got %q", d)
	}
}

// pullFailedAndNoImage 返回「pull 失败 + 目标机没有该镜像」的基础结果,rest 处理其余命令。
func pullFailedAndNoImage(rest func(cmd []string) *target.ExecResult) func(cmd []string) *target.ExecResult {
	return func(cmd []string) *target.ExecResult {
		if len(cmd) > 1 && cmd[0] == "docker" && cmd[1] == "pull" {
			return &target.ExecResult{ExitCode: 1, Stderr: "dial tcp: i/o timeout"}
		}
		if len(cmd) > 1 && cmd[0] == "docker" && cmd[1] == "image" {
			return &target.ExecResult{ExitCode: 1, Stderr: "No such image"}
		}
		if rest != nil {
			return rest(cmd)
		}
		return nil
	}
}

// fakeImageLocal 是 caddyImageLocal 的测试替身。
type fakeImageLocal struct {
	arch    string
	archErr error
	payload string
	saveErr error
}

func (f *fakeImageLocal) Arch(context.Context, string) (string, error) {
	if f.archErr != nil {
		return "", f.archErr
	}
	return f.arch, nil
}

func (f *fakeImageLocal) Save(context.Context, string) (io.ReadCloser, error) {
	if f.saveErr != nil {
		return nil, f.saveErr
	}
	return io.NopCloser(strings.NewReader(f.payload)), nil
}

func withFakeImageLocal(t *testing.T, f caddyImageLocal) {
	t.Helper()
	old := newCaddyImageLocal
	newCaddyImageLocal = func() (caddyImageLocal, error) { return f, nil }
	t.Cleanup(func() { newCaddyImageLocal = old })
}

// 拉取失败但本地已有镜像(离线/受限网络的老部署)→ 照常起容器,不因网络顺手失败而罢工。
func TestRunCaddyContainerUsesLocalImageWhenPullFails(t *testing.T) {
	ft := &fakeTarget{resultFor: func(cmd []string) *target.ExecResult {
		if cmd[0] == "docker" && len(cmd) > 1 && cmd[1] == "pull" {
			return &target.ExecResult{ExitCode: 1, Stderr: "i/o timeout"}
		}
		return nil // docker image inspect → exit 0:本地有镜像
	}}
	if err := runCaddyContainer(context.Background(), ft, "srv-1", "ghcr.io/fuuhoo/pipewright-caddy:latest", map[int]int{80: 80}); err != nil {
		t.Fatalf("本地已有镜像时应照常起容器, got %v", err)
	}
	ran := false
	for _, call := range ft.execCalls {
		if len(call) > 1 && call[0] == "docker" && call[1] == "run" {
			ran = true
		}
	}
	if !ran {
		t.Fatal("应执行 docker run")
	}
}
