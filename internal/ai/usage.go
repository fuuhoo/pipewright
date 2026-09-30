// usage.go 解析各 provider chat 回包里的 token 用量,让「这次 AI 用了多少」可见。
// 只做单次调用口径:不入库、不累计(月上限强制执行仍留给后续 Epic,见 ai_config.budget_json)。
package ai

import "encoding/json"

// TokenUsage 是一次 chat 调用的 token 用量。三档 provider 字段名各异,统一成进/出两项。
// 两项皆 0 表示该 provider 这次没回传用量——绝不估算兜底(宁可显示不出来)。
type TokenUsage struct {
	Prompt     int `json:"prompt"`     // 输入 tokens
	Completion int `json:"completion"` // 输出 tokens
}

// Total 是进+出合计。
func (u TokenUsage) Total() int { return u.Prompt + u.Completion }

// Empty 表示拿不到任何用量(前端据此隐藏整行)。
func (u TokenUsage) Empty() bool { return u.Prompt <= 0 && u.Completion <= 0 }

// extractChatUsage 按 provider 从 chat 回包取 token 用量。
// 回包不是预期 JSON / 没有 usage 段 → 返回零值(用量是附加信息,绝不让它变成错误)。
func extractChatUsage(provider string, raw []byte) TokenUsage {
	if len(raw) == 0 {
		return TokenUsage{}
	}
	switch provider {
	case ProviderClaude:
		var r struct {
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(raw, &r) != nil {
			return TokenUsage{}
		}
		return TokenUsage{Prompt: r.Usage.InputTokens, Completion: r.Usage.OutputTokens}
	case ProviderOpenAI:
		var r struct {
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(raw, &r) != nil {
			return TokenUsage{}
		}
		return TokenUsage{Prompt: r.Usage.PromptTokens, Completion: r.Usage.CompletionTokens}
	case ProviderOllama:
		// Ollama 的用量在回包顶层,不在 usage 对象里。
		var r struct {
			PromptEvalCount int `json:"prompt_eval_count"`
			EvalCount       int `json:"eval_count"`
		}
		if json.Unmarshal(raw, &r) != nil {
			return TokenUsage{}
		}
		return TokenUsage{Prompt: r.PromptEvalCount, Completion: r.EvalCount}
	default:
		return TokenUsage{}
	}
}
