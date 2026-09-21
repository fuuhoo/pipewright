// Package buildenv — 凭据桥接(v6.2 §3.1)。
//
// buildenv.Checker.ManualPull 需要按 credential_id 取凭据做 docker login,
// 但 buildenv 包不直接依赖 vault 包(避免构建环)。本文件定义
// VaultCredentialRefetch 适配器,放在 buildenv 包内但接受 vault.Vault
// 接口由 main.go 注入;测试时可注入 mock。
package buildenv

import (
	"context"
	"errors"

	"github.com/huangchengsir/pipewright/internal/vault"
)

// vaultCredentialRefetch 是 buildenv.CredentialRefetch 的 vault 实现。
type vaultCredentialRefetch struct {
	v vault.Vault
}

// NewVaultCredentialRefetch 把 vault.Vault 适配为 buildenv.CredentialRefetch。
// 用于 Checker.ManualPull:按 credential_id 取 username + secret(token/password)
// 给 docker login 用。MasterKey 未配置时 GetByID 返回 ErrVaultUnconfigured,
// 调用方应将其映射为「无需登录即可 pull」或直接报错。
func NewVaultCredentialRefetch(v vault.Vault) CredentialRefetch {
	return &vaultCredentialRefetch{v: v}
}

// GetByID 返回凭据明文 username + token。ErrVaultUnconfigured / ErrNotFound 由
// caller 区分;其它错误包装为 ErrInvalidInput。
func (r *vaultCredentialRefetch) GetByID(ctx context.Context, id string) (username, token string, err error) {
	if r.v == nil {
		return "", "", errors.New("buildenv: vault not configured")
	}
	ga, err := r.v.GetGitAuth(id)
	if err != nil {
		return "", "", err
	}
	return ga.Username, ga.Token, nil
}
