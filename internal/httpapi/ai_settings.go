package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/huangchengsir/pipewright/internal/ai"
)

// ---- AI 配置 DTO(冻结契约;camelCase;apiKey 仅掩码,绝无明文) ----

type aiBudgetDTO struct {
	MonthlyTokenLimit *int64 `json:"monthlyTokenLimit"`
}

// aiUsageDTO 是这一档**本自然月**(UTC 月,与 monthlyTokenLimit 同口径)的累计用量。
// 字段名自带 month 前缀:读数只覆盖本月,跨月归零,别让人当成历史总量。
// 仅作展示 —— 上限本身仍不强制执行。
type aiUsageDTO struct {
	MonthPrompt     int64 `json:"monthPrompt"`
	MonthCompletion int64 `json:"monthCompletion"`
}

// aiConfigDTO 是**单档协议**的配置条目(apiKey 只暴露掩码 apiKeyMasked,绝无明文)。
type aiConfigDTO struct {
	Configured   bool        `json:"configured"`
	Enabled      bool        `json:"enabled"`
	Provider     string      `json:"provider"`
	BaseURL      string      `json:"baseUrl"`
	Model        string      `json:"model"`
	APIKeyMasked string      `json:"apiKeyMasked"`
	Budget       aiBudgetDTO `json:"budget"`
	Usage        aiUsageDTO  `json:"usage"`
	UpdatedAt    *string     `json:"updatedAt"`
}

// aiTestResultDTO 是 POST test 响应体(冻结契约:ok/latencyMs/detail/error)。
type aiTestResultDTO struct {
	OK        bool    `json:"ok"`
	LatencyMs int64   `json:"latencyMs"`
	Detail    string  `json:"detail"`
	Error     *string `json:"error"`
}

// aiSettingsDTO 是 GET/PUT 响应体:三档协议各自的配置 + 当前生效者。
// 每档一行互不覆盖,所以前端切协议时拿得到那一档自己存过的地址/模型/掩码。
type aiSettingsDTO struct {
	Active  string        `json:"active"`
	Configs []aiConfigDTO `json:"configs"`
}

// toAIConfigDTO 把领域 Config 转契约 DTO(apiKey 仅掩码;updatedAt 未设为 null)。
func toAIConfigDTO(c *ai.Config) aiConfigDTO {
	dto := aiConfigDTO{
		Configured:   c.Configured,
		Enabled:      c.Enabled,
		Provider:     c.Provider,
		BaseURL:      c.BaseURL,
		Model:        c.Model,
		APIKeyMasked: c.APIKeyMasked,
		Budget:       aiBudgetDTO{MonthlyTokenLimit: c.Budget.MonthlyTokenLimit},
		Usage:        aiUsageDTO{MonthPrompt: c.Usage.Prompt, MonthCompletion: c.Usage.Completion},
	}
	if c.UpdatedAt != nil {
		s := c.UpdatedAt.UTC().Format(time.RFC3339)
		dto.UpdatedAt = &s
	}
	return dto
}

func toAISettingsDTO(o *ai.Overview) aiSettingsDTO {
	dto := aiSettingsDTO{Active: o.Active, Configs: make([]aiConfigDTO, 0, len(o.Items))}
	for _, c := range o.Items {
		dto.Configs = append(dto.Configs, toAIConfigDTO(c))
	}
	return dto
}

// writeAIError 把领域错误映射为契约错误码/状态码;绝不回显 apiKey 明文/密文。
func writeAIError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ai.ErrInvalidProvider):
		writeError(w, http.StatusUnprocessableEntity, "invalid_provider", "provider 只能为 claude、openai 或 ollama")
	case errors.Is(err, ai.ErrBaseURLRequired):
		writeError(w, http.StatusUnprocessableEntity, "base_url_required", "baseUrl 不能为空")
	case errors.Is(err, ai.ErrAPIKeyRequired):
		writeError(w, http.StatusUnprocessableEntity, "api_key_required", "该 provider 需要配置 API 密钥")
	case errors.Is(err, ai.ErrVaultUnconfigured):
		writeError(w, http.StatusServiceUnavailable, "vault_unconfigured", "保险库未配置 master key,无法加密或读取 API 密钥")
	default:
		writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
	}
}

// makeGetAISettingsHandler 返回 GET /api/settings/ai handler:三档协议各自的配置 + 生效者。
// 从未配置的那档为惰性空默认(configured/enabled=false);apiKey 仅掩码,绝无明文。
func makeGetAISettingsHandler(svc ai.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "AI 配置服务未初始化")
			return
		}
		overview, err := svc.List(r.Context())
		if err != nil {
			writeAIError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toAISettingsDTO(overview))
	}
}

// makeSaveAISettingsHandler 返回 PUT /api/settings/ai handler:只写 body.provider 那一档,
// 其余两档原样不动。apiKey 只写:省略/空保留该档既有,非空轮换(加密)。绝不在响应回明文。
// enabled=true 表示把这一档设为当前生效(其他档随之停用)。响应同 GET(整份总览)。
func makeSaveAISettingsHandler(svc ai.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "AI 配置服务未初始化")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		var req struct {
			Provider string `json:"provider"`
			BaseURL  string `json:"baseUrl"`
			Model    string `json:"model"`
			// APIKey 用指针区分「省略(nil)」与「显式空串」;两者均保留既有。
			APIKey *string `json:"apiKey"`
			Budget struct {
				MonthlyTokenLimit *int64 `json:"monthlyTokenLimit"`
			} `json:"budget"`
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}

		_, err := svc.Save(r.Context(), ai.SaveInput{
			Provider: req.Provider,
			BaseURL:  req.BaseURL,
			Model:    req.Model,
			APIKey:   req.APIKey,
			Budget:   ai.Budget{MonthlyTokenLimit: req.Budget.MonthlyTokenLimit},
			Enabled:  req.Enabled,
		})
		if err != nil {
			writeAIError(w, err)
			return
		}
		overview, err := svc.List(r.Context())
		if err != nil {
			writeAIError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toAISettingsDTO(overview))
	}
}

// makeTestAISettingsHandler 返回 POST /api/settings/ai/test handler。
// 收可选 draft(省略字段回退已存配置;apiKey 省略=用已存密钥),向 provider 发轻量探测。
// 200 回 {ok,latencyMs,detail,error};error 为人读(绝无密钥)。探测构造失败/校验失败 → 422。
func makeTestAISettingsHandler(svc ai.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "AI 配置服务未初始化")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		// 指针区分省略与显式值;省略字段回退已存配置。
		var req struct {
			Provider *string `json:"provider"`
			BaseURL  *string `json:"baseUrl"`
			Model    *string `json:"model"`
			APIKey   *string `json:"apiKey"`
		}
		// 空体允许(全回退已存配置);解析错误才报 400。
		if r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
				return
			}
		}

		res, err := svc.Test(r.Context(), ai.TestInput{
			Provider: req.Provider,
			BaseURL:  req.BaseURL,
			Model:    req.Model,
			APIKey:   req.APIKey,
		})
		if err != nil {
			writeAIError(w, err)
			return
		}
		dto := aiTestResultDTO{OK: res.OK, LatencyMs: res.LatencyMs, Detail: res.Detail}
		if res.Error != "" {
			dto.Error = &res.Error
		}
		writeJSON(w, http.StatusOK, dto)
	}
}
