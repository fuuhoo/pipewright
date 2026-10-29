package proxy

// caddy_image.go 让目标主机在拉不到反代镜像时,直接吃控制机本地那一份:
// 控制机 `docker save` → gzip → SSH 管道 → 目标机 `docker load`。
//
// 为什么需要:pipewright-caddy 放在 ghcr.io,受限网络(内网/被墙/限速)的目标主机经常整段
// 拉不动,而控制机——尤其刚部署过反代的那台——往往已经有一份。推过去省掉「登录目标机手拉」。
//
// 三个口径与构建镜像的远程推送一致(internal/build/remote_image.go):
//   - 每次现探:目标机 inspect 命中就不推;不记账(目标机被 rmi 了下次自动重推)。
//   - 架构门:镜像架构 ≠ 目标机架构就不推 —— `docker load` 会成功、`docker run` 却报
//     exec format error,推过去比不推更误导。
//   - 尽力而为:推不成只把原因带回 ErrCaddyImagePull,HTTP 层仍提示换镜像/预拉。

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/fuuhoo/pipewright/internal/target"
)

// caddyImagePushTimeout 是单次推送的上限(save + 压缩 + SSH 传输 + load 全程)。
// 反代镜像本身不大(百 MB 级),但受限网络的 SSH 带宽可能只有几百 KB/s,给足十分钟;
// 超了就中断并回落到「换镜像/预拉」的提示,而不是把一次部署请求挂死。
const caddyImagePushTimeout = 10 * time.Minute

// caddyImageLocal 是控制机侧的镜像导出能力(默认走 docker CLI;单测注入 fake)。
type caddyImageLocal interface {
	// Arch 返回控制机本地该镜像自身的架构(`image inspect` 的 .Architecture)。镜像不在 → err。
	Arch(ctx context.Context, image string) (string, error)
	// Save 把镜像导出为 gzip 压缩的 tar 流(调用方负责 Close)。
	Save(ctx context.Context, image string) (io.ReadCloser, error)
}

// newCaddyImageLocal 是可替换的构造缝,供单测注入假导出器。
var newCaddyImageLocal = func() (caddyImageLocal, error) {
	bin, err := exec.LookPath("docker")
	if err != nil {
		return nil, fmt.Errorf("控制机上找不到 docker 命令,无法把本地镜像推到目标主机")
	}
	return dockerLocalImage{bin: bin}, nil
}

// dockerLocalImage 用控制机上的 docker CLI 导出镜像。
type dockerLocalImage struct{ bin string }

func (s dockerLocalImage) Arch(ctx context.Context, image string) (string, error) {
	cmd := exec.CommandContext(ctx, s.bin, "image", "inspect", "--format", "{{.Architecture}}", image)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(errBuf.String())
		if msg == "" {
			msg = strings.TrimSpace(string(out))
		}
		return "", fmt.Errorf("控制机本地没有镜像 %s(%s)", image, truncateOutput(msg))
	}
	return strings.TrimSpace(string(out)), nil
}

// Save 执行 `docker save image`,把 stdout 就地 gzip(level 1:压缩率换传输时间不值)。
// 导出失败通过管道的 CloseWithError 传给读取方,由推送侧如实报出。
func (s dockerLocalImage) Save(ctx context.Context, image string) (io.ReadCloser, error) {
	cmd := exec.CommandContext(ctx, s.bin, "save", image)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("无法启动 docker save:%v", err)
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
			_ = pw.CloseWithError(fmt.Errorf("docker save %s 失败:%s", image, truncateOutput(msg)))
			return
		}
		_ = pw.CloseWithError(cpErr)
	}()
	return pr, nil
}

// pushCaddyImage 把控制机本地的反代镜像推给目标主机(字节只进 SSH 管道,不落目标机临时文件)。
// 返回的 error 已是人话,由调用方拼进 ErrCaddyImagePull 的诊断尾巴。
func pushCaddyImage(ctx context.Context, tg target.Service, serverID, image string) error {
	src, err := newCaddyImageLocal()
	if err != nil {
		return err
	}

	// 1) 镜像自身架构:控制机上取(取不到 = 控制机也没这个镜像,那就推不了)。
	imageArch, aerr := src.Arch(ctx, image)
	if aerr != nil {
		return aerr
	}

	// 2) 目标机架构。
	archRes, rerr := tg.Exec(ctx, serverID, []string{"uname", "-m"})
	if rerr != nil {
		return fmt.Errorf("无法确认目标主机架构,跳过推送:%v", mapExecErr(rerr))
	}
	if archRes == nil || archRes.ExitCode != 0 {
		return fmt.Errorf("目标主机 uname -m 未成功,跳过推送")
	}
	ia, ra := normalizeImageArch(imageArch), normalizeImageArch(strings.TrimSpace(archRes.Stdout))
	if ia == "" || ra == "" {
		return fmt.Errorf("架构口径无法比对(镜像 %s / 目标机 %s),跳过推送", imageArch, strings.TrimSpace(archRes.Stdout))
	}
	if ia != ra {
		return fmt.Errorf("架构不一致:镜像 %s 是 %s,目标主机是 %s,推过去也起不来", image, archLabelOf(ia), archLabelOf(ra))
	}

	// 3) 推:控制机 save|gzip → SSH 管道 → 目标机 docker load。
	pushCtx, cancel := context.WithTimeout(ctx, caddyImagePushTimeout)
	defer cancel()
	stream, serr := src.Save(pushCtx, image)
	if serr != nil {
		return fmt.Errorf("导出本地镜像失败:%v", serr)
	}
	defer func() { _ = stream.Close() }()

	loadRes, lerr := tg.ExecWithStdin(pushCtx, serverID, []string{"docker", "load"}, stream)
	if lerr != nil {
		// 读侧错误(save 失败)优先如实报出,否则只看到一句「推送失败」。
		if cerr := stream.Close(); cerr != nil {
			return fmt.Errorf("导出本地镜像失败:%v", cerr)
		}
		return fmt.Errorf("推送镜像失败:%v", mapExecErr(lerr))
	}
	if loadRes != nil && loadRes.ExitCode != 0 {
		return fmt.Errorf("目标主机 docker load 未成功:%s", truncateOutput(firstNonEmpty(loadRes.Stderr, loadRes.Stdout)))
	}
	return nil
}

// normalizeImageArch 把 `uname -m` 与 docker 的 .Architecture 两套口径归一
// (amd64↔x86_64、arm64↔aarch64);认不出 → ""(交由调用方按「无法比对」处理,绝不猜)。
func normalizeImageArch(s string) string {
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

// archLabelOf 给人读的架构名。
func archLabelOf(s string) string {
	if s == "amd64" {
		return "x86_64"
	}
	if s == "arm64" {
		return "arm64"
	}
	return s
}

// truncateOutput 把 docker 的多行报错压成一行并限长(按字符截,避免切坏 UTF-8)。
func truncateOutput(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 200 {
		return string(r[:200]) + "…"
	}
	return s
}
