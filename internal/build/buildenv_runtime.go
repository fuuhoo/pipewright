package build

import (
	"context"
	"fmt"
	"strings"

	"github.com/fuuhoo/pipewright/internal/pipeline"
)

// buildenv_runtime.go 是执行期的预置目录解析入口(#9/#10)。
//
// 铁律:镜像地址**只有一个来源** —— 预置构建环境目录。job 配置里的 `image` /
// `toolchainLanguage`+`toolchainVersion` 自 R4 起只是「查目录的键」(旧配置与导入模板的兼容写法),
// 目录里没有的镜像一律拒绝执行,而不是退回去 docker run 一个任意地址。
// 这条与保存期校验(#8)共用 pipeline.BuildEnvSnapshot,规则不会两处漂移。

// jobRuntime 解析一个 job 的运行期构建环境(镜像 + 拉取凭据 + 配置资源挂载)。
//
// renderedImage 是自定义节点把 {{参数}} 渲染完得到的镜像文本(保存期无法求值,故在此二次校验)。
func (b *Builder) jobRuntime(cfg map[string]any, renderedImage, stage, job string) (pipeline.ResolvedJobEnv, error) {
	snap, err := b.buildEnvSnapshot(stage, job)
	if err != nil {
		return pipeline.ResolvedJobEnv{}, err
	}
	return snap.ResolveJobEnv(cfg, renderedImage, stage, job)
}

// toolchainRuntime 解析「工具链构建(build_image 模型 B)」的语言+版本为目录镜像。
func (b *Builder) toolchainRuntime(tc pipeline.Toolchain, stage, job string) (pipeline.ResolvedJobEnv, error) {
	snap, err := b.buildEnvSnapshot(stage, job)
	if err != nil {
		return pipeline.ResolvedJobEnv{}, err
	}
	return snap.ResolveToolchainEnv(tc, stage, job)
}

// buildEnvSnapshot 即时取目录快照(不缓存:管理员改目录与正在跑的构建是并发两件事,
// 缓存会让「刚禁用的环境仍被用上」)。未装配目录 → 诚实报错,绝不回退到手填镜像通路。
func (b *Builder) buildEnvSnapshot(stage, job string) (pipeline.BuildEnvSnapshot, error) {
	if b.envGate == nil {
		return pipeline.BuildEnvSnapshot{}, fmt.Errorf("阶段「%s」的节点「%s」无法执行:平台未装配预置构建环境目录,请在「构建环境」中新增环境后于节点上选择", stage, job)
	}
	snap, err := b.envGate.Snapshot()
	if err != nil {
		return pipeline.BuildEnvSnapshot{}, fmt.Errorf("读取预置构建环境目录失败(节点「%s」):%v", job, err)
	}
	return snap, nil
}

// loginForImage 按环境绑定的凭据登录镜像仓库(私有构建镜像需登录才能拉取)。
//
// 口令经 driver.Login 的 --password-stdin 注入,不进 argv/日志。登录失败只记一行日志、
// 不判失败:镜像可能已在远程/本地缓存或是公开的,真拉不到时 docker 自己会给出准确错误。
func (b *Builder) loginForImage(ctx context.Context, image, credentialID string, onLine func(stream, line string)) {
	if credentialID == "" || image == "" {
		return
	}
	host := imageRegistryHost(image)
	if host == "" {
		return // Docker Hub 官方库短名:无仓库可登
	}
	user, pass := b.revealRegistryCred(credentialID)
	if user == "" && pass == "" {
		onLine(streamStdout, "⚠ 构建镜像 "+image+" 绑定的凭据已取不到内容,跳过登录(拉取若失败请重设凭据)")
		return
	}
	onLine(streamStdout, "→ 登录镜像仓库 "+host+" 以拉取构建镜像…")
	code, err := b.driver.Login(ctx, host, user, pass, onLine)
	pass = ""
	if err != nil && code < 0 {
		onLine(streamStdout, "⚠ 无法调用容器 CLI 登录,继续尝试拉取")
		return
	}
	if code != 0 {
		onLine(streamStdout, "⚠ 仓库登录失败,继续尝试拉取(镜像已缓存时可照常)")
	}
}

// imageRegistryHost 取镜像引用里的仓库主机段:首段含 "." 或 ":" 才算 registry
// (否则是 Docker Hub 官方库前缀,如 node:20 → "")。
func imageRegistryHost(image string) string {
	i := strings.Index(image, "/")
	if i < 0 {
		return ""
	}
	host := image[:i]
	if !strings.ContainsAny(host, ".:") {
		return ""
	}
	return host
}
