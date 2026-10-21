package build

// remote_image.go 让远程 runner 拿到本次要用的构建环境镜像(FR-8-14 续):远程机上没有这个镜像时,
// 控制机 `docker save` 出来、边压边经 SSH 管道喂给远程 `docker load` —— 用户不必登录远程机手工拉。
//
// 三个口径:
//   - **每次构建现探、不入库记账**:远程 `docker image inspect` 命中就直接用。远程机是被人手管的机器,
//     今天推过去的镜像明天可能被 `rmi`;把「已推送」当状态存下来就会漂成一个假绿灯,而现探天然自愈、也免一次迁移。
//   - **架构门**:只在「镜像自身架构 == 远程机架构」时推送。架构不同时 `docker load` 照样成功、
//     `docker run` 却起不来(exec format error),推过去比不推更误导 —— 故只提醒,让远程按自己的架构拉。
//   - **推送全程尽力而为**:任何一步失败都只记一行日志。远程还有「自己从仓库拉」这条通路(登录已接上,
//     见 loginForImage 的远程 driver),平台的推送只是省时间,不是唯一出路,绝不因此判构建失败。
//
// 这里投递的命令(镜像 inspect / save / load、uname -m)都是平台自身的基础设施动作,不含用户构建命令。

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// remoteImagePushTimeout 是单次镜像推送的上限(save + 压缩 + 传输 + load 全程)。
// 按实测口径给足:5GB 级环境镜像(带工具链叠加层)gzip -1 + SSH 传输在千兆内网是分钟级,
// 但绝不该超过几十分钟;超了就中断并提示改用本地拉取,而不是把一次构建挂死。
const remoteImagePushTimeout = 20 * time.Minute

// remoteImageSource 是控制机侧的镜像导出能力(默认真容器 CLI;单测注入 fake)。
type remoteImageSource interface {
	// Arch 返回控制机本地该镜像自身的架构(`image inspect` 的 .Architecture)。镜像不在控制机上 → err。
	Arch(ctx context.Context, image string) (string, error)
	// Save 把镜像导出为 gzip 压缩的 tar 流(调用方负责 Close)。
	Save(ctx context.Context, image string) (io.ReadCloser, error)
}

// newRemoteImageSource 是可替换的构造缝(仿 lookPath),供单测注入假导出器。
var newRemoteImageSource = func(bin string) remoteImageSource { return cliImageSource{bin: bin} }

// cliImageSource 用控制机上的容器 CLI(docker/nerdctl/podman,与构建 driver 同一个)导出镜像。
type cliImageSource struct{ bin string }

func (s cliImageSource) Arch(ctx context.Context, image string) (string, error) {
	out, errOut, code, err := execCommander{}.Run(ctx, s.bin,
		[]string{"image", "inspect", "--format", "{{.Architecture}}", image}, "")
	if err != nil {
		return "", fmt.Errorf("无法调用 %s:%v", s.bin, err)
	}
	if code != 0 {
		msg := strings.TrimSpace(errOut)
		if msg == "" {
			msg = strings.TrimSpace(out)
		}
		return "", fmt.Errorf("控制机本地没有镜像 %s(%s)", image, truncateLine(msg))
	}
	return strings.TrimSpace(out), nil
}

// Save 执行 `bin save image`,把 stdout 就地 gzip(level 1:环境镜像动辄数 GB,压缩率换传输时间不值)。
// 导出失败(docker 的报错)通过管道的 CloseWithError 传给读取方,由推送侧如实记出。
func (s cliImageSource) Save(ctx context.Context, image string) (io.ReadCloser, error) {
	cmd := exec.CommandContext(ctx, s.bin, "save", image)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("无法启动 %s save:%v", s.bin, err)
	}
	pr, pw := io.Pipe()
	go func() {
		gw, _ := gzip.NewWriterLevel(pw, gzip.BestSpeed)
		_, cpErr := io.Copy(gw, stdout)
		gw.Close()
		waitErr := cmd.Wait()
		if waitErr != nil {
			msg := strings.TrimSpace(errBuf.String())
			if msg == "" {
				msg = waitErr.Error()
			}
			_ = pw.CloseWithError(fmt.Errorf("%s save %s 失败:%v", s.bin, image, truncateLine(msg)))
			return
		}
		_ = pw.CloseWithError(cpErr)
	}()
	return pr, nil
}

// ensureRemoteImage 保证远程 runner 用得上本次的构建镜像:已有 → 静默通过;架构一致 → 推送;
// 架构不一致 / 控制机没有 / 任一步失败 → 记一行说明后交远程自行拉取。
func (b *Builder) ensureRemoteImage(ctx context.Context, tgt remoteExec, serverID, image string, onLine func(stream, line string)) {
	if onLine == nil {
		onLine = func(string, string) {}
	}
	image = strings.TrimSpace(image)
	if image == "" {
		return
	}

	// 1) 远程已有?有就一步不做(每次现探,不记账 → 谁 rmi 了下次自动重推)。
	res, err := tgt.Exec(ctx, serverID, []string{remoteContainerCLI, "image", "inspect", image})
	if err != nil {
		onLine(streamStdout, "⚠ 无法确认远程 runner 是否已有构建镜像 "+image+",跳过推送;远程将自行按需拉取")
		return
	}
	if res != nil && res.ExitCode == 0 {
		onLine(streamStdout, "✓ 远程 runner 已有构建镜像 "+image)
		return
	}

	src := newRemoteImageSource(b.driver.Binary())

	// 2) 镜像自身架构:控制机上取(取不到 = 控制机也没这个镜像,那就无能为力)。
	imageArch, aerr := src.Arch(ctx, image)
	if aerr != nil {
		onLine(streamStdout, "⚠ "+aerr.Error()+",无法推送;远程 runner 将自行从仓库拉取(首次会慢)")
		return
	}

	// 3) 远程机架构。
	runnerArch, rerr := remoteMachineArch(ctx, tgt, serverID)
	if rerr != nil {
		onLine(streamStdout, "⚠ 无法确认远程 runner 的 CPU 架构,跳过推送(避免推错架构):"+rerr.Error())
		return
	}
	ia, ra := normalizeArch(imageArch), normalizeArch(runnerArch)
	if ia == "" || ra == "" {
		onLine(streamStdout, fmt.Sprintf("⚠ 架构口径无法比对(镜像 %s / 远程机 %s),跳过推送;远程将自行按需拉取", imageArch, runnerArch))
		return
	}
	if ia != ra {
		// 门:推错了 load 得逞、run 不起来,比不推更坑。只提醒。
		onLine(streamStdout, fmt.Sprintf("⚠ 架构不一致:构建镜像 %s 是 %s,远程 runner 是 %s —— 不推送(推过去也跑不起来)。请在 %s 架构的机器上准备该镜像,或把构建环境换成远程机能用的", image, archLabel(ia), archLabel(ra), archLabel(ra)))
		return
	}

	// 4) 推:控制机 save|gzip → SSH 管道 → 远程 docker load(字节只进管道,不落远程临时文件)。
	onLine(streamStdout, fmt.Sprintf("→ 远程 runner 缺少构建镜像 %s(%s),从控制机推送中…", image, archLabel(ia)))
	pushCtx, cancel := context.WithTimeout(ctx, remoteImagePushTimeout)
	defer cancel()

	stream, serr := src.Save(pushCtx, image)
	if serr != nil {
		onLine(streamStderr, "⚠ 导出镜像失败,跳过推送:"+serr.Error())
		return
	}
	defer func() { _ = stream.Close() }()

	counter := &countingReader{r: stream}
	start := time.Now()
	loadRes, lerr := tgt.ExecWithStdin(pushCtx, serverID, []string{remoteContainerCLI, "load"}, counter)
	elapsed := time.Since(start)
	if lerr != nil {
		msg := lerr.Error()
		if serr := stream.Close(); serr != nil { // 读侧错误(save 失败)优先如实报出
			msg = serr.Error()
		}
		onLine(streamStderr, "⚠ 推送镜像失败,远程将自行按需拉取:"+msg)
		return
	}
	if loadRes != nil && loadRes.ExitCode != 0 {
		onLine(streamStderr, "⚠ 远程 docker load 未成功,远程将自行按需拉取:"+truncateLine(loadRes.Stderr))
		return
	}
	onLine(streamStdout, fmt.Sprintf("✓ 已推送构建镜像 %s(%s · 用时 %s)", image, formatMiB(counter.n), elapsed.Round(time.Second)))
}

// remoteMachineArch 取远程机的机器架构(`uname -m`,与指标采集同一口径)。
func remoteMachineArch(ctx context.Context, tgt remoteExec, serverID string) (string, error) {
	res, err := tgt.Exec(ctx, serverID, []string{"uname", "-m"})
	if err != nil {
		return "", err
	}
	if res == nil || res.ExitCode != 0 {
		return "", fmt.Errorf("远程 uname -m 未成功")
	}
	out := strings.TrimSpace(res.Stdout)
	if out == "" {
		return "", fmt.Errorf("远程 uname -m 无输出")
	}
	return out, nil
}

// normalizeArch 把 `uname -m` 与 docker 的 .Architecture 两套口径归一(amd64↔x86_64、arm64↔aarch64);
// 认不出 → ""(交由调用方按「无法比对」处理,绝不猜)。
func normalizeArch(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	case "i386", "i686", "x86", "386":
		return "386"
	default:
		return ""
	}
}

// archLabel 给人读的架构名。
func archLabel(s string) string {
	if s == "amd64" {
		return "x86_64"
	}
	if s == "arm64" {
		return "arm64"
	}
	return s
}

// countingReader 统计实际写进 SSH 管道的字节数(压缩后体积,即用户关心的传输量)。
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// formatMiB 把字节数说成人类可读的体积(≥1GiB 用 GiB 两位小数,否则 MiB/KB)。
func formatMiB(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GiB", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KiB", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// truncateLine 把 docker 的多行报错压成一行,避免一行日志里塞进整段 usage。
// 按字符截(不是字节):docker 的报错里可能带中文,按字节切会把 UTF-8 切成半个字。
func truncateLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 300 {
		return string(r[:300]) + "…"
	}
	return s
}
