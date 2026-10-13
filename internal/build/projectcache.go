package build

import (
	"strconv"
	"strings"

	"github.com/fuuhoo/pipewright/internal/pipeline"
	"github.com/fuuhoo/pipewright/internal/projectcache"
)

// projectcache 接线:把「每项目固定的依赖缓存目录」bind 进本地构建容器,并注入让工具改道过去的 env。
//
// 与任务里配的 cachePaths(internal/buildcache,tar 归档搬运工作区内目录)是并存的两套:
// 那套要人在流水线里写路径、只在同 key 下复用;这套全局按项目落一个固定目录,流水线里一个字
// 都不用写,工具按自己的默认路径(`~/.m2/repository` 等)就能命中。
//
// **只接本地执行路径**。远程 runner 按产品决定不留任何常驻目录,而远程那条是自行直调
// driver.RunToolchain(remote_stage_exec.go)的,天然不经这里的任何函数。

// WithProjectCache 注入每项目依赖缓存布局。nil 不改(保持「每次容器销毁依赖即失」的旧行为)。
func WithProjectCache(l *projectcache.Layout) BuilderOption {
	return func(b *Builder) { b.projectCache = l }
}

// applyProjectCache 给一次容器执行追加该项目的缓存挂载(可写)与缓存 env,返回补完 env 的切片。
// 布局未注入 / 项目 ID 空 → 原样返回,不挂任何东西(绝不半挂)。
func (b *Builder) applyProjectCache(res *pipeline.Resource, env []string, projectID string, onLine func(stream, line string)) []string {
	if b.projectCache == nil || strings.TrimSpace(projectID) == "" {
		return env
	}
	// 已占用的容器路径传进去:与「配置资源」的挂载形成父子关系的缓存位会让路(配置文件被缓存
	// 目录盖住是功能损坏,慢一点不是)。
	taken := make([]string, 0, len(res.Mounts))
	for _, m := range res.Mounts {
		taken = append(taken, m.ContainerPath)
	}
	plan := b.projectCache.Plan(projectID, taken)
	for _, m := range plan.Mounts {
		res.Mounts = append(res.Mounts, pipeline.ContainerMount{HostPath: m.HostPath, ContainerPath: m.ContainerPath, ReadOnly: m.ReadOnly})
	}
	if onLine != nil {
		onLine("stdout", "项目依赖缓存:"+b.projectCache.ProjectDir(projectID)+"(追加挂载 "+strconv.Itoa(len(plan.Mounts))+" 处)")
		for _, s := range plan.Skipped {
			onLine("stdout", "· 缓存位 "+s+" 与已挂载路径冲突,本次跳过(构建照常,只是这一档依赖仍要重下)")
		}
	}
	return append(env, plan.Env...)
}
