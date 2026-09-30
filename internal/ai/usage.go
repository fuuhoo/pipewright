// usage.go 管 token 用量的两件事:
//   - 单次口径:按 provider 解析 chat 回包里的用量,让「这次 AI 用了多少」可见(结果卡)。
//   - 月度口径:把每次调用的进/出 tokens 累加进 ai_token_usage(协议 × 自然月一行),
//     供设置页把「月 Token 上限」和「本月已用」并排显示。仅统计,不强制执行上限。
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/huangchengsir/pipewright/internal/store"
)

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

// MonthUsage 是某一档协议在一个自然月里的累计用量(读自 ai_token_usage)。
type MonthUsage struct {
	Prompt     int64 `json:"prompt"`     // 本月输入 tokens
	Completion int64 `json:"completion"` // 本月输出 tokens
}

// Empty 表示本月一次都没记上(前端据此显示「本月暂无用量」而不是 0 的数字)。
func (u MonthUsage) Empty() bool { return u.Prompt <= 0 && u.Completion <= 0 }

// usageMonth 把时间折成自然月键('2026-09'),一律 UTC —— 与库里 created_at/updated_at
// 存 RFC3339 UTC 同口径,不掺入服务器本地时区。跨月自动归零(新月份是另一行)。
func usageMonth(t time.Time) string { return t.UTC().Format("2006-01") }

// recordUsage 把一次 chat 的 tokens 累加进该档该月那一行(不存在则新建)。
//
// 只在拿到非零用量后写:兼容端点常不回 usage,补一行 0/0 只会让表变长、页面数字不变。
// best-effort:记账失败绝不拖垮已经生成的结果(统计是附加信息,不是功能本身),
// 所以这里只返回 error 给单测断言,调用点刻意忽略返回值。
func (s *service) recordUsage(ctx context.Context, provider string, u TokenUsage) error {
	if provider == "" || u.Empty() {
		return nil
	}
	nowStr := time.Now().UTC().Format(time.RFC3339)
	// 累加写在冲突分支:两个占位符顺序 = INSERT 值 → SET 值,两方言同一条 SQL 成立
	// (SQLite 的裸列名在 DO UPDATE 里指既有行,MySQL 在 ON DUPLICATE KEY UPDATE 里同义)。
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO ai_token_usage
		   (provider, usage_month, prompt_tokens, completion_tokens, updated_at)
		 VALUES (?, ?, ?, ?, ?) `+
			store.UpsertAssignSuffix(store.DialectOf(s.db),
				[]string{"provider", "usage_month"},
				[]string{"prompt_tokens = prompt_tokens + ?",
					"completion_tokens = completion_tokens + ?", "updated_at = ?"}),
		provider, usageMonth(time.Now()), u.Prompt, u.Completion, nowStr,
		u.Prompt, u.Completion, nowStr,
	)
	if err != nil {
		return fmt.Errorf("ai: record token usage: %w", err)
	}
	return nil
}

// monthUsageByProvider 读当月各档累计用量。只返回有行的档;页面按档取值,
// 缺项 = 这一档本月还没用过(零用量)。
func (s *service) monthUsageByProvider(ctx context.Context) (map[string]MonthUsage, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT provider, prompt_tokens, completion_tokens FROM ai_token_usage WHERE usage_month = ?`,
		usageMonth(time.Now()),
	)
	if err != nil {
		return nil, fmt.Errorf("ai: load token usage: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]MonthUsage)
	for rows.Next() {
		var (
			provider string
			u        MonthUsage
		)
		if err := rows.Scan(&provider, &u.Prompt, &u.Completion); err != nil {
			return nil, fmt.Errorf("ai: scan token usage: %w", err)
		}
		out[provider] = u
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ai: iterate token usage: %w", err)
	}
	return out, nil
}
