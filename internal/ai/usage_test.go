package ai

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
