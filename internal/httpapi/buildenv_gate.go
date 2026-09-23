// buildenv_gate.go 将预置目录服务适配成流水线保存期的白名单来源(#8)。
//
// pipeline 包只认中性的 BuildEnvSnapshot(见 internal/pipeline/buildenv_validate.go),不依赖
// buildenv / configprofile;这里做一次性字段搬运。快照**含已禁用条目** —— 「刚被禁用的环境」
// 要报成「已禁用,请重选」而不是「不存在」,根因才对得上管理员刚做的操作。
package httpapi

import (
	"github.com/huangchengsir/pipewright/internal/buildenv"
	"github.com/huangchengsir/pipewright/internal/configprofile"
	"github.com/huangchengsir/pipewright/internal/pipeline"
)

// buildEnvGate 每次保存即时读目录(不缓存):流水线编辑与管理员改目录是并发的两件事,
// 缓存会让「刚禁用的环境仍可选」这类困惑落到用户身上。目录量级是个位数到几十条,读代价可忽略。
type buildEnvGate struct {
	envs     *buildenv.Service
	profiles *configprofile.Service
}

// NewBuildEnvGate 构造流水线保存期的预置目录快照来源。任一服务为 nil → 返回 nil(不启用校验)。
func NewBuildEnvGate(envs *buildenv.Service, profiles *configprofile.Service) pipeline.BuildEnvGate {
	if envs == nil {
		return nil
	}
	return &buildEnvGate{envs: envs, profiles: profiles}
}

func (g *buildEnvGate) Snapshot() (pipeline.BuildEnvSnapshot, error) {
	list, err := g.envs.List(buildenv.ListFilter{IncludeDisabled: true})
	if err != nil {
		return pipeline.BuildEnvSnapshot{}, err
	}
	opts := make([]pipeline.BuildEnvOption, 0, len(list))
	for _, e := range list {
		opts = append(opts, pipeline.BuildEnvOption{
			ID:           e.ID,
			Language:     e.Language,
			Version:      e.Version,
			DisplayName:  e.DisplayName,
			Image:        e.Image,
			Enabled:      e.Enabled,
			CheckStatus:  e.ImageCheckStatus,
			CredentialID: e.CredentialID,
		})
	}

	var profs []pipeline.ConfigProfileOption
	if g.profiles != nil {
		ps, err := g.profiles.List(configprofile.ListFilter{IncludeBuiltin: true, IncludeDisabled: true})
		if err != nil {
			return pipeline.BuildEnvSnapshot{}, err
		}
		profs = make([]pipeline.ConfigProfileOption, 0, len(ps))
		for _, p := range ps {
			// FilePath/TargetPath 只供执行期注入用(#10);HTTP 响应侧由各自 DTO 置空,不经此处泄漏。
			profs = append(profs, pipeline.ConfigProfileOption{
				ID: p.ID, Language: p.Language, Enabled: p.Enabled, FilePath: p.FilePath, TargetPath: p.TargetPath,
			})
		}
	}
	return pipeline.NewBuildEnvSnapshot(opts, profs), nil
}
