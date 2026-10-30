package build

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fuuhoo/pipewright/internal/dagrun"
	"github.com/fuuhoo/pipewright/internal/library"
	"github.com/fuuhoo/pipewright/internal/pipeline"
)

// library_vars.go 把复用库的两类资产接到执行期:
//   - 变量组:流水线设置里的 varGroupIds 在解析配置时合并进 settings.Build.Vars,下游(脚本环境、
//     构建参数、部署正文渲染)拿到的就是一张合并后的变量表,不需要各处再认识「变量组」。
//   - 命名指令:部署节点的前/重启/探测命令可以引用它;正文在运行时取出再按 {{占位符}} 渲染。

// varGroupReader 抽象「按 ID 取复用库变量组」(实现是 library.VarGroupService)。
// 窄接口便于单测注入 fake,也让 build 只依赖它真正用到的能力。
type varGroupReader interface {
	Get(ctx context.Context, id string) (*library.VarGroup, error)
}

// commandReader 抽象「按 ID 取复用库指令」(实现是 library.CommandService)。
type commandReader interface {
	Get(ctx context.Context, id string) (*library.Command, error)
}

// mergeReferencedVarGroups 把流水线引用的变量组合并进 settings.Build.Vars。
//
// 优先级:项目自己的变量 > 先引用的变量组 > 后引用的变量组 —— 同名键先到先得。
// 「项目变量优先」是因为流水线级变量是项目自己的口径,共享变量组只是给一个默认值。
// 合并结果里保留 secret 项的凭据引用:脚本 / 构建能用它们(明文即取即用),
// 而部署正文渲染会跳过 secret 并对其占位符明确报错(见 deployTemplateVars / secretVarNames)。
//
// 引用已失效(变量组被删)时返回错误而不是静默跳过:部署正文可能正靠它渲染路径,
// 悄悄漏掉一个变量只会变成运行到目标机时才爆的一句「路径不对」。
func (b *Builder) mergeReferencedVarGroups(ctx context.Context, settings *pipeline.Settings) error {
	if b.varGroups == nil || settings == nil || len(settings.VarGroupIDs) == 0 {
		return nil
	}
	taken := make(map[string]bool, len(settings.Build.Vars)+8)
	for _, v := range settings.Build.Vars {
		if k := strings.TrimSpace(v.Key); k != "" {
			taken[k] = true
		}
	}
	seenGroup := make(map[string]bool, len(settings.VarGroupIDs))
	for _, gid := range settings.VarGroupIDs {
		gid = strings.TrimSpace(gid)
		if gid == "" || seenGroup[gid] {
			continue
		}
		seenGroup[gid] = true
		group, err := b.varGroups.Get(ctx, gid)
		if err != nil {
			if errors.Is(err, library.ErrVarGroupNotFound) {
				return fmt.Errorf("流水线引用的变量组 %s 已不存在(可能已被删除),请到流水线设置的「变量与缓存」里移除这条引用", gid)
			}
			return fmt.Errorf("读取流水线引用的变量组失败:%v", err)
		}
		for _, v := range group.Vars {
			key := strings.TrimSpace(v.Key)
			if key == "" || taken[key] {
				continue
			}
			taken[key] = true
			v.Key = key
			settings.Build.Vars = append(settings.Build.Vars, v)
		}
	}
	return nil
}

// deployCommandField 取部署节点的一条命令:配了「复用库指令」引用就以指令正文为准,
// 否则用字段里直接填的正文。两种情况都按运行参数 / 流水线变量渲染 {{占位符}},
// 并对残留的加密变量占位符明确报错(命令会落到目标机,机密不该从这条链路走)。
// 返回空串表示该字段没配,调用方整步跳过。
func (b *Builder) deployCommandField(
	ctx context.Context,
	rep dagrun.StageReporter,
	jb pipeline.Job,
	refKey, textKey string,
	params map[string]string,
	settings *pipeline.Settings,
	label string,
) (string, error) {
	ref := strings.TrimSpace(cfgString(jb.Config, refKey))
	body := cfgString(jb.Config, textKey)
	if ref != "" {
		if b.commands == nil {
			_ = rep.Log(ctx, streamStderr, fmt.Sprintf("部署节点「%s」的%s引用了复用库指令,但平台未装配指令库服务", jb.Name, label))
			return "", ErrBuildFailed
		}
		cmd, err := b.commands.Get(ctx, ref)
		if err != nil {
			if errors.Is(err, library.ErrCommandNotFound) {
				_ = rep.Log(ctx, streamStderr, fmt.Sprintf(
					"部署节点「%s」的%s引用了复用库指令 %s,但它已不存在(可能已被删除):请到复用库重新建一条,或改为直接填写命令",
					jb.Name, label, ref))
			} else {
				_ = rep.Log(ctx, streamStderr, fmt.Sprintf("部署节点「%s」的%s读取复用库指令失败:%v", jb.Name, label, err))
			}
			return "", ErrBuildFailed
		}
		// 引用生效时以指令正文为准:同名字段里残留的文字不再执行,免得两份内容各说各话。
		if strings.TrimSpace(body) != "" {
			_ = rep.Log(ctx, streamStdout, fmt.Sprintf("· %s使用复用库指令「%s」(%s),节点里填写的正文已忽略", label, cmd.Name, cmd.ID))
		}
		body = cmd.Body
	}
	rendered := renderTemplate(body, params)
	if names := secretVarNames(rendered, settings); names != "" {
		_ = rep.Log(ctx, streamStderr, fmt.Sprintf(
			"部署节点「%s」的%s引用了加密变量 %s:这条正文会原样发到目标机执行,不能把机密抄进去。"+
				"请改用非加密变量,或让目标机上的服务自己取凭据", jb.Name, label, names))
		return "", ErrBuildFailed
	}
	return rendered, nil
}
