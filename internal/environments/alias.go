// Package environments 是 v6.2 之前的"部署环境"包名兼容层。
//
// v6.2 阶段 2 将本包迁至 internal/deployenv(语义未变,只是命名调整)。
// 本文件对 deployenv 的导出做 type alias,让旧 import "internal/environments"
// 的代码仍可见同名符号(类型、常量、错误)。实例化请直接走 deployenv.NewService。
//
// 后续 v6.3+ 计划删除本文件;新代码请直接 import "internal/deployenv"。
package environments

import "github.com/huangchengsir/pipewright/internal/deployenv"

// 类型别名:旧 import "internal/environments" 仍可见同名符号;
// 类型身份与 deployenv.* 完全相同(deployenv.X 与 environments.X == X)。
type (
	Service             = deployenv.Service
	Deployment          = deployenv.Deployment
	EnvironmentTimeline = deployenv.EnvironmentTimeline
	TargetSummary       = deployenv.TargetSummary
	Artifact            = deployenv.Artifact
)

// 错误别名:旧调用方可继续用 environments.ErrXxx。
var (
	ErrProjectNotFound  = deployenv.ErrProjectNotFound
	ErrEnvNotFound      = deployenv.ErrEnvNotFound
	ErrNoRollbackTarget = deployenv.ErrNoRollbackTarget
)

// 常量别名。
const (
	DeployStatusSuccess       = deployenv.DeployStatusSuccess
	DeployStatusPartialFailed = deployenv.DeployStatusPartialFailed
	DeployStatusFailed        = deployenv.DeployStatusFailed
)