package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/huangchengsir/pipewright/internal/audit"
	"github.com/huangchengsir/pipewright/internal/auth"
	"github.com/huangchengsir/pipewright/internal/vault"
)

// credentialDTO 是凭据对外响应体(冻结契约;camelCase;无明文/无密文)。
type credentialDTO struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Type        string  `json:"type"`
	Scope       string  `json:"scope"`
	OwnerID     string  `json:"ownerId"` // personal 归属;global 为 ""
	Username    string  `json:"username"`
	MaskedValue string  `json:"maskedValue"`
	Description string  `json:"description"`
	Enabled     bool    `json:"enabled"`
	DisabledBy  string  `json:"disabledBy"`
	DisabledAt  *string `json:"disabledAt"`
	CreatedBy   string  `json:"createdBy"`
	LastUsedAt  *string `json:"lastUsedAt"` // RFC3339 或 null
	CreatedAt   string  `json:"createdAt"`
}

// toDTO 把领域 Credential 转为契约 DTO。
func toDTO(c *vault.Credential) credentialDTO {
	var lastUsed *string
	if c.LastUsedAt != nil {
		s := c.LastUsedAt.UTC().Format(time.RFC3339)
		lastUsed = &s
	}
	var disabledAt *string
	if c.DisabledAt != nil {
		s := c.DisabledAt.UTC().Format(time.RFC3339)
		disabledAt = &s
	}
	return credentialDTO{
		ID:          c.ID,
		Name:        c.Name,
		Type:        c.Type,
		Scope:       c.Scope,
		OwnerID:     c.OwnerID,
		Username:    c.Username,
		MaskedValue: c.MaskedValue,
		Description: c.Description,
		Enabled:     c.Enabled,
		DisabledBy:  c.DisabledBy,
		DisabledAt:  disabledAt,
		CreatedBy:   c.CreatedBy,
		LastUsedAt:  lastUsed,
		CreatedAt:   c.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// vaultActorFromRequest 把登录会话换算成保险库操作者身份。
//
// 必须挂在 requireAuth 之后;取不到 session 或 UserID 为空时返回 false,
// 调用方回 401——不能退化成 vault 约定的「nil actor = 系统调用 = admin」。
func vaultActorFromRequest(r *http.Request) (*vault.Actor, bool) {
	sess, ok := sessionFromContext(r.Context())
	if !ok || sess == nil || sess.UserID == "" {
		return nil, false
	}
	return &vault.Actor{UserID: sess.UserID, Role: sess.Role}, true
}

// writeVaultError 把领域错误映射为契约错误码/状态码;绝不回显明文/密文。
func writeVaultError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, vault.ErrVaultUnconfigured):
		writeError(w, http.StatusServiceUnavailable, "vault_unconfigured", "保险库未配置 master key")
	case errors.Is(err, vault.ErrNotFound):
		writeError(w, http.StatusNotFound, "credential_not_found", "凭据不存在")
	case errors.Is(err, vault.ErrCredentialInUse):
		writeError(w, http.StatusConflict, "credential_in_use", "凭据正被项目或流水线配置引用,无法删除;请先解除引用")
	case errors.Is(err, vault.ErrInvalidType):
		writeError(w, http.StatusBadRequest, "invalid_credential", "凭据类型非法")
	case errors.Is(err, vault.ErrEncryptedGitSSHKey):
		writeError(w, http.StatusBadRequest, "invalid_credential", "私钥带口令(passphrase),无人值守克隆无法解锁;请先执行 ssh-keygen -p -N \"\" 去掉口令后再录入")
	case errors.Is(err, vault.ErrInvalidGitSSHKey):
		writeError(w, http.StatusBadRequest, "invalid_credential", "git_ssh 凭据需要有效的 PEM 私钥")
	case errors.Is(err, vault.ErrEmptySecret):
		writeError(w, http.StatusBadRequest, "invalid_credential", "secret 不能为空")
	case errors.Is(err, vault.ErrEmptyName):
		writeError(w, http.StatusBadRequest, "invalid_credential", "name 不能为空")
	case errors.Is(err, vault.ErrOwnerRequired):
		writeError(w, http.StatusBadRequest, "invalid_credential", "personal 凭据必须指定归属用户")
	case errors.Is(err, vault.ErrDisabledCredential):
		writeError(w, http.StatusConflict, "credential_disabled", "凭据已被管理员禁用,请先启用")
	case errors.Is(err, vault.ErrForbidden):
		// 普通用户调 admin 端点(双重防线)/ 禁 global 凭据 / 越权访他人 personal
		writeError(w, http.StatusForbidden, "forbidden", "无权执行该操作:global 凭据不可禁用,个人凭据仅管理员可禁用")
	case errors.Is(err, vault.ErrAccessDenied):
		writeError(w, http.StatusForbidden, "access_denied", "无权访问该凭据")
	default:
		// 包含 ErrDecrypt 等内部错误:不泄漏细节。
		writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
	}
}

// makeListCredentialsHandler 返回 GET /api/credentials handler。
// 可见范围由 Actor 决定:admin 看全部(含已禁用);普通用户只看到自己的 personal 凭据。
func makeListCredentialsHandler(v vault.Vault) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if v == nil {
			writeError(w, http.StatusServiceUnavailable, "vault_unconfigured", "保险库未配置 master key")
			return
		}
		actor, ok := vaultActorFromRequest(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
			return
		}
		creds, err := v.ListWithActor(actor, vault.ListFilter{
			IncludeGlobal: true, IncludePersonal: true, IncludeDisabled: true,
		})
		if err != nil {
			writeVaultError(w, err)
			return
		}
		out := make([]credentialDTO, 0, len(creds))
		for i := range creds {
			out = append(out, toDTO(&creds[i]))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// makeCreateCredentialHandler 返回 POST /api/credentials handler。
// 创建成功后追加 credential_create 审计(detail 仅元数据,经 Masker 脱敏,绝无明文)。
//
// 归属规则:普通用户只能建 personal 且 owner 固定为自己(否则等于往全局凭据池里塞私货);
// 管理员可建 global,或代建他人 personal(显式传 ownerId)。
func makeCreateCredentialHandler(v vault.Vault, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if v == nil {
			writeError(w, http.StatusServiceUnavailable, "vault_unconfigured", "保险库未配置 master key")
			return
		}
		actor, ok := vaultActorFromRequest(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 私钥可能较大,放宽到 1MB
		var req struct {
			Name     string `json:"name"`
			Type     string `json:"type"`
			Scope    string `json:"scope"`
			Username string `json:"username"`
			Secret   string `json:"secret"`
			OwnerID  string `json:"ownerId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		scope, ownerID := req.Scope, req.OwnerID
		if !actor.IsAdmin() {
			scope, ownerID = "personal", actor.UserID
		} else if scope == "personal" && ownerID == "" {
			ownerID = actor.UserID
		}
		cred, err := v.Create(vault.CreateInput{
			Name:      req.Name,
			Type:      req.Type,
			Scope:     scope,
			Username:  req.Username,
			Secret:    req.Secret,
			OwnerID:   ownerID,
			CreatedBy: actor.UserID,
		})
		if err != nil {
			writeVaultError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionCredentialCreate,
			TargetType: audit.TargetCredential,
			TargetID:   cred.ID,
			Detail:     map[string]any{"name": cred.Name, "type": cred.Type, "scope": cred.Scope},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusCreated, toDTO(cred))
	}
}

// makeUpdateCredentialHandler 返回 PATCH /api/credentials/{id} handler。
// 更新成功后追加 credential_update 审计(detail 仅元数据 + 是否轮换密钥;绝无明文)。
// 归属校验在 vault.UpdateWithActor:普通用户只能改自己的 personal。
func makeUpdateCredentialHandler(v vault.Vault, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if v == nil {
			writeError(w, http.StatusServiceUnavailable, "vault_unconfigured", "保险库未配置 master key")
			return
		}
		actor, ok := vaultActorFromRequest(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
			return
		}
		id := chi.URLParam(r, "id")
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var req struct {
			Name        *string `json:"name"`
			Scope       *string `json:"scope"`
			Username    *string `json:"username"`
			Description *string `json:"description"`
			Secret      *string `json:"secret"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		cred, err := v.UpdateWithActor(actor, id, vault.UpdateInput{
			Name:        req.Name,
			Scope:       req.Scope,
			Username:    req.Username,
			Description: req.Description,
			Secret:      req.Secret,
		})
		if err != nil {
			writeVaultError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionCredentialUpdate,
			TargetType: audit.TargetCredential,
			TargetID:   cred.ID,
			Detail:     map[string]any{"name": cred.Name, "type": cred.Type, "scope": cred.Scope, "rotated": req.Secret != nil},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusOK, toDTO(cred))
	}
}

// makeRevealCredentialHandler 返回 POST /api/credentials/{id}/reveal handler。
// 解密并回传明文(仅此一处对外暴露明文);每次查看追加 credential_reveal 审计,
// 谁在何时看过哪条凭据均留痕。POST + 登录态 + CSRF(写方法路由),不做成可预取的 GET。
//
// 路由上另挂 RequireAdmin:明文一旦出网就是最终形态,回滚不了,所以取回明文的能力
// 单独收到管理员,不给全体登录用户。
func makeRevealCredentialHandler(v vault.Vault, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if v == nil {
			writeError(w, http.StatusServiceUnavailable, "vault_unconfigured", "保险库未配置 master key")
			return
		}
		actor, ok := vaultActorFromRequest(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
			return
		}
		id := chi.URLParam(r, "id")
		secret, err := v.RevealWithActor(actor, id)
		if err != nil {
			writeVaultError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionCredentialReveal,
			TargetType: audit.TargetCredential,
			TargetID:   id,
			IP:         clientIP(r),
		})
		// 仅回传明文,绝不进日志/诊断;detail 不含 secret。
		writeJSON(w, http.StatusOK, map[string]string{"secret": secret})
	}
}

// makeDeleteCredentialHandler 返回 DELETE /api/credentials/{id} handler。
// 删除成功后追加 credential_delete 审计。
func makeDeleteCredentialHandler(v vault.Vault, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if v == nil {
			writeError(w, http.StatusServiceUnavailable, "vault_unconfigured", "保险库未配置 master key")
			return
		}
		actor, ok := vaultActorFromRequest(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
			return
		}
		id := chi.URLParam(r, "id")
		if err := v.DeleteWithActor(actor, id); err != nil {
			writeVaultError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionCredentialDelete,
			TargetType: audit.TargetCredential,
			TargetID:   id,
			IP:         clientIP(r),
		})
		w.WriteHeader(http.StatusNoContent)
	}
}

// makeSetCredentialEnabledHandler 返回 POST /api/admin/credentials/{id}/{disable|enable}
// handler。admin 切换 personal 凭据可用性(v6.2 §3.4 矩阵);禁用只改元数据,密文与
// 归属都不动,所以可逆。路由已在 RequireAdmin 子组内。
func makeSetCredentialEnabledHandler(v vault.Vault, aud audit.Recorder, ac auth.Authenticator, disable bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if v == nil {
			writeError(w, http.StatusServiceUnavailable, "vault_unconfigured", "保险库未配置 master key")
			return
		}
		actor, ok := vaultActorFromRequest(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
			return
		}
		id := chi.URLParam(r, "id")
		var err error
		if disable {
			err = v.DisableWithActor(actor, id)
		} else {
			err = v.EnableWithActor(actor, id)
		}
		if err != nil {
			writeVaultError(w, err)
			return
		}
		action := audit.ActionCredentialEnable
		if disable {
			action = audit.ActionCredentialDisable
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     action,
			TargetType: audit.TargetCredential,
			TargetID:   id,
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusOK, map[string]any{"disabled": disable})
	}
}
