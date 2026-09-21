package buildenv

import (
	"context"
	"fmt"
)

// ResolveImage 据 language+version 查 build_envs 返回真实 image(v6.2 §5.5)。
//
//   - 找不到         → ValidationError{Code: ENV_NOT_FOUND}
//   - 未启用         → ValidationError{Code: ENV_DISABLED}
//   - 镜像不可用     → ValidationError{Code: IMAGE_UNAVAILABLE}
//   - 找到           → 原样返回 env.Image(系统不拼接)
//
// 调用方(build 包 / pipeline.ValidateBuildEnvironment)凭 ValidationError.Code
// 决定是否向用户暴露具体语言/版本信息。
func ResolveImage(ctx context.Context, lang, ver string, repo Repo) (string, error) {
	env, err := repo.GetByLanguageVersion(lang, ver)
	if err != nil {
		return "", fmt.Errorf("查询构建环境失败: %w", err)
	}
	if env == nil {
		return "", &ValidationError{
			Code:    "ENV_NOT_FOUND",
			Message: fmt.Sprintf("构建环境 %s/%s 未预置", lang, ver),
		}
	}
	if !env.Enabled {
		return "", &ValidationError{
			Code:    "ENV_DISABLED",
			Message: fmt.Sprintf("构建环境 %s/%s 已禁用", lang, ver),
		}
	}
	if env.ImageCheckStatus == StatusUnavailable {
		return "", &ValidationError{
			Code:    "IMAGE_UNAVAILABLE",
			Message: fmt.Sprintf("构建环境 %s/%s 的镜像不可拉取,请联系管理员", lang, ver),
		}
	}
	return env.Image, nil
}

// ResolveByID 按 build_envs.id 解析(ServiceSpec.BuildEnvID 走这条)。
func ResolveByID(ctx context.Context, id string, repo Repo) (string, error) {
	env, err := repo.GetByID(id)
	if err != nil {
		return "", err
	}
	if env == nil {
		return "", &ValidationError{Code: "ENV_NOT_FOUND", Message: "构建环境不存在"}
	}
	if !env.Enabled {
		return "", &ValidationError{Code: "ENV_DISABLED", Message: "构建环境已禁用"}
	}
	if env.ImageCheckStatus == StatusUnavailable {
		return "", &ValidationError{Code: "IMAGE_UNAVAILABLE", Message: "镜像不可拉取"}
	}
	return env.Image, nil
}

// ValidateCredentialAccess 校验引用的 credential 是否存在(供 ServiceSpec.BuildEnvID
// 链路用;若 build_env 引用了 credential,创建/更新 ServiceSpec 时也要确认)。
// 实际 credential 是否可用由 vault.Exists 校验。
type CredentialRefChecker interface {
	Exists(ctx context.Context, id string) (bool, error)
}

// IsValidEnv 接受 language+version,做"可用且可启用"判断(给流水线校验路径用)。
func IsValidEnv(ctx context.Context, lang, ver string, repo Repo) bool {
	_, err := ResolveImage(ctx, lang, ver, repo)
	return err == nil
}