package ai

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// stubLLMWithUsage 起一个响应里带 token 用量的 chat stub(三档 usage 字段位置各不相同)。
func stubLLMWithUsage(t *testing.T, provider, text string) *httptest.Server {
	t.Helper()
	esc := jsonEscape(text)
	var body string
	switch provider {
	case ProviderClaude:
		body = `{"content":[{"type":"text","text":"` + esc + `"}],"usage":{"input_tokens":1200,"output_tokens":340}}`
	case ProviderOpenAI:
		body = `{"choices":[{"message":{"role":"assistant","content":"` + esc + `"}}],"usage":{"prompt_tokens":1200,"completion_tokens":340}}`
	case ProviderOllama:
		body = `{"message":{"role":"assistant","content":"` + esc + `"},"prompt_eval_count":1200,"eval_count":340}`
	default:
		t.Fatalf("未知 provider %q", provider)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestExtractChatUsagePerProvider(t *testing.T) {
	cases := []struct {
		provider, raw       string
		wantPrompt, wantOut int
	}{
		{ProviderClaude, `{"usage":{"input_tokens":7,"output_tokens":3}}`, 7, 3},
		{ProviderOpenAI, `{"usage":{"prompt_tokens":11,"completion_tokens":5}}`, 11, 5},
		// Ollama 的用量在回包顶层,不在 usage 对象里。
		{ProviderOllama, `{"message":{"role":"assistant","content":"ok"},"prompt_eval_count":18,"eval_count":2}`, 18, 2},
		// 兼容端点常不回 usage:零值,绝不猜。
		{ProviderOpenAI, `{"choices":[{"message":{"content":"ok"}}]}`, 0, 0},
		{ProviderClaude, `<html>502 Bad Gateway</html>`, 0, 0},
		{ProviderClaude, ``, 0, 0},
	}
	for _, c := range cases {
		got := extractChatUsage(c.provider, []byte(c.raw))
		if got.Prompt != c.wantPrompt || got.Completion != c.wantOut {
			t.Errorf("%s <- %s: 得 %+v, want prompt=%d completion=%d", c.provider, c.raw, got, c.wantPrompt, c.wantOut)
		}
	}
}

func TestTokenUsageEmpty(t *testing.T) {
	if !(TokenUsage{}).Empty() {
		t.Errorf("零值应 Empty()=true")
	}
	if (TokenUsage{Completion: 1}).Empty() {
		t.Errorf("只回传一项也算有用量")
	}
	if got := (TokenUsage{Prompt: 7, Completion: 3}).Total(); got != 10 {
		t.Errorf("Total = %d, want 10", got)
	}
}

// TestDiagnoseReportsUsage 验证诊断把这一趟 chat 的用量带进结果(按 provider 各自的字段位置解析)。
func TestDiagnoseReportsUsage(t *testing.T) {
	for _, provider := range []string{ProviderClaude, ProviderOpenAI, ProviderOllama} {
		t.Run(provider, func(t *testing.T) {
			srv := stubLLMWithUsage(t, provider, stubDiagnosisJSON)
			svc, _, _ := newService(t, srv.Client())
			configureEnabled(t, svc, provider, srv.URL)

			d, err := svc.Diagnose(ctx(), DiagnoseInput{
				FailureLog: failureLogWithSecret,
				StepName:   "构建镜像",
				Masker:     maskerWithSecret(),
			})
			if err != nil {
				t.Fatalf("Diagnose: %v", err)
			}
			if d.Status != diagStatusReady {
				t.Fatalf("status = %q, want ready (reason=%q)", d.Status, d.Reason)
			}
			if d.Usage.Prompt != 1200 || d.Usage.Completion != 340 {
				t.Errorf("usage = %+v, want prompt=1200 completion=340", d.Usage)
			}
		})
	}
}

// TestRiskAnnotateReportsUsage 验证风险标注的 LLM 增强把用量带进报告。
func TestRiskAnnotateReportsUsage(t *testing.T) {
	aiJSON := `{"findings":[{"level":"low","stepName":"部署","line":2,"title":"部署后未回滚","why":"失败时无回滚","suggestion":"加 rollout undo"}]}`
	srv := stubLLMWithUsage(t, ProviderOllama, aiJSON)
	svc, _, _ := newService(t, srv.Client())
	configureEnabled(t, svc, ProviderOllama, srv.URL)

	report, err := svc.AnnotateRisks(ctx(), AnnotateRisksInput{
		Steps:  []ScriptStep{{Name: "部署", Image: "node:20.11", Commands: []string{"echo ok", "kubectl apply -f k8s/"}}},
		Masker: maskerForScript(),
	})
	if err != nil {
		t.Fatalf("AnnotateRisks: %v", err)
	}
	if !report.AIEnhanced {
		t.Fatalf("应跑成 LLM 增强 (reason=%q)", report.AIReason)
	}
	if report.Usage.Prompt != 1200 || report.Usage.Completion != 340 {
		t.Errorf("usage = %+v, want prompt=1200 completion=340", report.Usage)
	}
}

// TestChatErrorOllama404NamesModel 验模型名写错时(实测踩过):错误直说「Ollama 里没有这个模型」,
// 不再冒用探测文案「探测失败」、也不泄漏内部哨兵「generate failed」。
func TestChatErrorOllama404NamesModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"model 'Gemm4:e4b' not found"}`))
	}))
	t.Cleanup(srv.Close)
	svc, _, _ := newService(t, srv.Client())
	// configureEnabled 存的模型名就是 test-model。
	configureEnabled(t, svc, ProviderOllama, srv.URL)

	report, err := svc.AnnotateRisks(ctx(), AnnotateRisksInput{
		Steps:  []ScriptStep{{Name: "构建", Image: "node:20.11", Commands: []string{"npm ci"}}},
		Masker: maskerForScript(),
	})
	if err != nil {
		t.Fatalf("AnnotateRisks 不应 error: %v", err)
	}
	if report.AIEnhanced {
		t.Errorf("404 时不应算作增强成功")
	}
	for _, want := range []string{"没有模型", "test-model", "404"} {
		if !strings.Contains(report.AIReason, want) {
			t.Errorf("AIReason 应含 %q,得 %q", want, report.AIReason)
		}
	}
	for _, never := range []string{"探测失败", "generate failed", "ai:"} {
		if strings.Contains(report.AIReason, never) {
			t.Errorf("AIReason 绝不出现 %q,得 %q", never, report.AIReason)
		}
	}
}

// TestChatErrorClaude404PointsAtBaseURL 验证非 ollama 的 404 说「地址或模型不存在」(而不是探测文案)。
func TestChatErrorClaude404PointsAtBaseURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	svc, _, _ := newService(t, srv.Client())
	configureEnabled(t, svc, ProviderClaude, srv.URL)

	_, err := svc.Generate(ctx(), GenerateInput{})
	if err == nil {
		t.Fatalf("404 应返回错误")
	}
	if !strings.Contains(err.Error(), "调用地址或模型不存在") {
		t.Errorf("错误应指向 baseUrl/模型名: %v", err)
	}
	if strings.Contains(err.Error(), "探测失败") {
		t.Errorf("chat 失败不该说探测文案: %v", err)
	}
	if strings.Contains(err.Error(), "sk-secret-KEY9") {
		t.Errorf("错误泄漏明文 key: %v", err)
	}
}

// TestHumanizeDiagnoseErrDropsInternalSentinel 验证内部哨兵不进 UI。
func TestHumanizeDiagnoseErrDropsInternalSentinel(t *testing.T) {
	wrapped := fmt.Errorf("%w: %s", ErrGenerateFailed, "调用模型失败(HTTP 500)")
	if got := humanizeDiagnoseErr(wrapped); got != "调用模型失败(HTTP 500)" {
		t.Errorf("只应留人读尾巴,得 %q", got)
	}
}

// TestUsageMonthKeyIsUTC 验证月键按 UTC 折:服务器在 UTC+8 时,北京时间 9 月 1 日 07:00
// 仍是 8 月的账(与库里其余时间列一律 RFC3339 UTC 同口径,不掺本地时区)。
func TestUsageMonthKeyIsUTC(t *testing.T) {
	utcPlus8 := time.FixedZone("UTC+8", 8*60*60)
	if got := usageMonth(time.Date(2026, 9, 1, 7, 0, 0, 0, utcPlus8)); got != "2026-08" {
		t.Errorf("月键 = %q, want 2026-08", got)
	}
	if got := usageMonth(time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC)); got != "2026-09" {
		t.Errorf("月键 = %q, want 2026-09", got)
	}
}

// TestRecordUsageAccumulatesPerProvider 验证同档多次调用累加进同一行、跨档互不串账,
// 且拿不到用量(兼容端点不回 usage)时不落 0/0 的空行。
func TestRecordUsageAccumulatesPerProvider(t *testing.T) {
	svc, _, _ := newService(t, http.DefaultClient)
	s := svc.(*service)

	record := func(provider string, u TokenUsage) {
		t.Helper()
		if err := s.recordUsage(ctx(), provider, u); err != nil {
			t.Fatalf("recordUsage(%s, %+v): %v", provider, u, err)
		}
	}
	record(ProviderClaude, TokenUsage{Prompt: 1200, Completion: 340})
	record(ProviderClaude, TokenUsage{Prompt: 800, Completion: 60})
	record(ProviderOllama, TokenUsage{Prompt: 7, Completion: 3})
	record(ProviderOpenAI, TokenUsage{}) // 没用量 → 不记账

	ov, err := svc.List(ctx())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	cases := []struct {
		provider                   string
		wantPrompt, wantCompletion int64
	}{
		{ProviderClaude, 2000, 400},
		{ProviderOllama, 7, 3},
		{ProviderOpenAI, 0, 0},
	}
	for _, c := range cases {
		got := itemOf(t, ov, c.provider).Usage
		if got.Prompt != c.wantPrompt || got.Completion != c.wantCompletion {
			t.Errorf("%s 用量 = %+v, want prompt=%d completion=%d", c.provider, got, c.wantPrompt, c.wantCompletion)
		}
	}
}

// TestMonthUsageExcludesOtherMonths 验证「已使用」只算本月:上月那行再大也不进来
// (跨月自动归零,否则页面数字只增不减,月上限就失去意义了)。
func TestMonthUsageExcludesOtherMonths(t *testing.T) {
	svc, db, _ := newService(t, http.DefaultClient)
	s := svc.(*service)
	if err := s.recordUsage(ctx(), ProviderClaude, TokenUsage{Prompt: 5, Completion: 6}); err != nil {
		t.Fatalf("recordUsage: %v", err)
	}

	now := time.Now().UTC()
	firstOfThisMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	prevMonth := firstOfThisMonth.AddDate(0, 0, -1).Format("2006-01")
	if _, err := db.ExecContext(ctx(),
		`INSERT INTO ai_token_usage (provider, usage_month, prompt_tokens, completion_tokens, updated_at)
		 VALUES (?, ?, ?, ?, ?)`,
		ProviderClaude, prevMonth, 9_000_000, 9_000_000, firstOfThisMonth.Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert 上月用量: %v", err)
	}

	ov, err := svc.List(ctx())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := itemOf(t, ov, ProviderClaude).Usage; got.Prompt != 5 || got.Completion != 6 {
		t.Errorf("本月用量 = %+v, want prompt=5 completion=6(上月行不该算进来)", got)
	}
}

// TestChatRecordsUsageIntoMonthLedger 验证真实生成路径会自动记账:调用方只调 Diagnose,
// 不用自己写台账(所有生成入口都收口在 chatWithTokens,漏一处就少一处账)。
func TestChatRecordsUsageIntoMonthLedger(t *testing.T) {
	srv := stubLLMWithUsage(t, ProviderOllama, stubDiagnosisJSON)
	svc, _, _ := newService(t, srv.Client())
	configureEnabled(t, svc, ProviderOllama, srv.URL)

	for i := 0; i < 2; i++ {
		if _, err := svc.Diagnose(ctx(), DiagnoseInput{
			FailureLog: failureLogWithSecret,
			StepName:   "构建镜像",
			Masker:     maskerWithSecret(),
		}); err != nil {
			t.Fatalf("Diagnose #%d: %v", i+1, err)
		}
	}

	ov, err := svc.List(ctx())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// stub 每轮回 1200/340,两轮就该是 2400/680。
	if got := itemOf(t, ov, ProviderOllama).Usage; got.Prompt != 2400 || got.Completion != 680 {
		t.Errorf("两轮生成后本月用量 = %+v, want prompt=2400 completion=680", got)
	}
}
