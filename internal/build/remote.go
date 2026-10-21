package build

// remote.go 是「远程构建 runner 池」(Story 8-14 / FR-8-14)的**远程执行地基**:把构建工具链命令
// 从中控机下沉到一台已登记的远程构建机执行,复用 target 层的 SSH 通道(Epic 4 / Story 4.1)。
//
// 设计:沿用本包既有的 Commander 抽象(driver.go)——shellDriver 经 Commander 跑 docker/nerdctl/podman
// 子命令。本文件提供 **SSH 版 Commander**(sshCommander):同一条 array 命令不在本机 os/exec 跑,
// 而是经 target 层投递到远程机执行,退出码/输出原样取回。于是
//
//	NewRemoteDriver(execer, serverID, "docker")  →  shellDriver{bin:"docker", cmdr: sshCommander}
//
// 即得到一个「在远程机上跑容器工具链」的 Driver,可注入 Builder(WithDriver),无需改 Builder/DAG。
//
// 与本地执行的两处实现差别,都收在这一个文件里:
//   - **流式**:走 target.ExecStreamWithExit(stdout/stderr 分流 + 真退出码),命令一有输出就逐行
//     回调 —— 远程构建的日志与本地一样是实时的(docker 拉取进度、编译器逐行输出边跑边出)。
//   - **stdin**:走 target.ExecWithStdin,所以 `docker login --password-stdin` 这类
//     「秘密只进管道」的操作在远程模式同样成立(口令不进 argv/日志)。
//
// 边界(后续增量另起):多 runner 负载均衡 / 按 stage 选 runner;远程 job 级 timeout/retry;
// 测试报告与质量门禁的远程回采。

import (
	"context"
	"io"
	"strings"

	"github.com/fuuhoo/pipewright/internal/pipeline"
	"github.com/fuuhoo/pipewright/internal/target"
)

// RemoteExecer 抽象「在某远程机上执行一条 array 命令」的能力(target.Service 即满足)。
// 便于 fake 单测注入(不触真 SSH);array 命令经 target 层 shell 转义后交 SSH session(AC-SEC-02)。
type RemoteExecer interface {
	Exec(ctx context.Context, serverID string, cmd []string) (*target.ExecResult, error)
	// ExecWithStdin 把字节流接到远端命令的标准输入,取回 stdout/stderr/退出码。
	// 供 `docker login --password-stdin`(口令只进管道)与 `docker load`(镜像字节进管道)。
	ExecWithStdin(ctx context.Context, serverID string, cmd []string, stdin io.Reader) (*target.ExecResult, error)
	// ExecStreamWithExit 流式跑命令:stdout/stderr 分流逐块回调,结束回真退出码(<0 表示没能启动)。
	ExecStreamWithExit(ctx context.Context, serverID string, cmd []string, onChunk func(stream string, chunk []byte)) (int, error)
}

// sshCommander 是经 SSH 投递到远程机执行的 Commander(实现 build.Commander)。
// 同一条命令不在本机跑,而是 execer 投到远程;退出码/输出原样取回。
type sshCommander struct {
	execer   RemoteExecer
	serverID string
}

// NewRemoteCommander 构造一个把命令投递到 serverID 远程机执行的 Commander。
func NewRemoteCommander(execer RemoteExecer, serverID string) Commander {
	return &sshCommander{execer: execer, serverID: serverID}
}

// NewRemoteDriver 构造一个「在远程机上跑容器工具链」的 Driver:shellDriver 经 SSH Commander 执行,
// bin 为远程机上的容器 CLI 名(空 → 默认 "docker")。可注入 Builder(WithDriver)使构建在远程机发生。
//
// 配置资源的宿主文件在中控机上,远程机的 docker 看不见,故这里把 res.Mounts 清空:
// 远程执行路径会另行为用户记一行「已跳过注入」的诚实说明,而不是让 docker 报一个看不懂的挂载错。
func NewRemoteDriver(execer RemoteExecer, serverID, bin string) Driver {
	if strings.TrimSpace(bin) == "" {
		bin = "docker"
	}
	return &remoteDriver{shellDriver{bin: bin, cmdr: NewRemoteCommander(execer, serverID)}}
}

// remoteDriver 只在 RunToolchain 上剥离宿主挂载,其余能力(登录/构建/推送)与 shellDriver 同语义。
type remoteDriver struct{ shellDriver }

func (d *remoteDriver) RunToolchain(ctx context.Context, image, hostDir, mountAt, workdir string, env []string, cmd []string, res pipeline.Resource, onLine func(stream, line string)) (int, error) {
	res.Mounts = nil
	return d.shellDriver.RunToolchain(ctx, image, hostDir, mountAt, workdir, env, cmd, res, onLine)
}

// Run 实现 Commander.Run:把 name+args 投到远程机同步执行,取回 stdout/stderr/退出码。
// stdin 非空 → 经 ExecWithStdin 接到远端标准输入(--password-stdin 这类口令只进管道,不进 argv)。
// exitCode<0 表示无法在远程启动 / SSH 连接错误(err 非 nil)。
func (c *sshCommander) Run(ctx context.Context, name string, args []string, stdin string) (string, string, int, error) {
	cmd := append(append([]string{}, name), args...)
	var (
		res *target.ExecResult
		err error
	)
	if stdin != "" {
		res, err = c.execer.ExecWithStdin(ctx, c.serverID, cmd, strings.NewReader(stdin))
	} else {
		res, err = c.execer.Exec(ctx, c.serverID, cmd)
	}
	if err != nil {
		return "", "", -1, err
	}
	if res == nil {
		return "", "", -1, nil
	}
	return res.Stdout, res.Stderr, res.ExitCode, nil
}

// Stream 实现 Commander.Stream:经 SSH 流式执行,**边跑边逐行回调**(远程日志与本地同为实时),
// 命令结束回真退出码。stdin 非空同样走管道(docker login 就是这么调的)。
func (c *sshCommander) Stream(ctx context.Context, name string, args []string, stdin string, onLine func(stream, line string)) (int, error) {
	cmd := append(append([]string{}, name), args...)
	if stdin != "" {
		// 需要「输入 + 输出」同时流式:target 层没有这种通路。登录用的 --password-stdin 只要退出码,
		// 输出量是一行 "Login Succeeded",故按同步执行取回后再把输出回放成行(不损失信息)。
		res, err := c.execer.ExecWithStdin(ctx, c.serverID, cmd, strings.NewReader(stdin))
		if err != nil {
			return -1, err
		}
		if res == nil {
			return -1, nil
		}
		if onLine != nil {
			emit := func(stream, out string) {
				for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
					if line != "" {
						onLine(stream, line)
					}
				}
			}
			emit(streamStdout, res.Stdout)
			emit(streamStderr, res.Stderr)
		}
		return res.ExitCode, nil
	}

	feed := newRemoteLineFeed(onLine)
	code, err := c.execer.ExecStreamWithExit(ctx, c.serverID, cmd, feed.chunk)
	feed.flush()
	if err != nil {
		return -1, err
	}
	return code, nil
}

// remoteLineFeed 把 target 回调的「TCP 块」切成「行」再交给 onLine。
//
// 块的边界不是行的边界,故每个流各留一段残行:半行先进缓冲,补全了才成行回调。
// 两路(stdout/stderr)在 target 层已串行化,残行互不串味。口径对齐本地 execCommander.Stream
// 用的 bufio.Scanner:按 \n 切、丢掉行尾的 \r、结束时放出没有换行的尾段。
type remoteLineFeed struct {
	onLine func(stream, line string)
	pend   map[string]*strings.Builder
}

func newRemoteLineFeed(onLine func(stream, line string)) *remoteLineFeed {
	return &remoteLineFeed{onLine: onLine, pend: map[string]*strings.Builder{}}
}

func (f *remoteLineFeed) chunk(stream string, b []byte) {
	if f.onLine == nil || len(b) == 0 {
		return
	}
	sb := f.pend[stream]
	if sb == nil {
		sb = &strings.Builder{}
		f.pend[stream] = sb
	}
	sb.Write(b)
	// 一次取出全部完整行;最后一段(无换行)留在缓冲里等下一块。
	whole := sb.String()
	for {
		i := strings.IndexByte(whole, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimSuffix(whole[:i], "\r")
		whole = whole[i+1:]
		f.onLine(stream, line)
	}
	sb.Reset()
	sb.WriteString(whole)
}

// flush 放出各流残留的最后一段(远端最后输出没带换行时不至于丢掉一行)。
func (f *remoteLineFeed) flush() {
	if f.onLine == nil {
		return
	}
	// 顺序稳定:先 stdout 后 stderr,便于日志读起来不跳。
	for _, stream := range []string{streamStdout, streamStderr} {
		sb := f.pend[stream]
		if sb == nil || sb.Len() == 0 {
			continue
		}
		f.onLine(stream, strings.TrimSuffix(sb.String(), "\r"))
		sb.Reset()
	}
}
