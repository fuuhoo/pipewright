package ai

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestChatIgnoresProbeClientTimeout 验证「生成」不吃「探测」的秒级超时:
// 本地模型一轮生成常要几十秒(实测 Ollama ~40s),共用一个 8s 客户端会把所有
// AI 增强(诊断/风险/生成)永久打成优雅降级态。
func TestChatIgnoresProbeClientTimeout(t *testing.T) {
	srv := slowLLMServer(t, 300*time.Millisecond, ProviderOllama, stubDiagnosisJSON)
	// 探测客户端故意只给 20ms,生成必须照常跑完。
	svc, _, _ := newService(t, &http.Client{Timeout: 20 * time.Millisecond})
	configureEnabled(t, svc, ProviderOllama, srv.URL)

	d, err := svc.Diagnose(ctx(), DiagnoseInput{
		FailureLog: failureLogWithSecret,
		StepName:   "构建镜像",
		Masker:     maskerWithSecret(),
	})
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if d.Status != diagStatusReady {
		t.Fatalf("慢生成不该被探测超时打成降级: status=%q reason=%q", d.Status, d.Reason)
	}
}

// slowLLMServer 起一个延时 delay 后才按 provider 回标准用量信封的 stub。
func slowLLMServer(t *testing.T, delay time.Duration, provider, text string) *httptest.Server {
	t.Helper()
	var body string
	switch provider {
	case ProviderClaude:
		body = `{"content":[{"type":"text","text":"` + jsonEscape(text) + `"}],"usage":{"input_tokens":1,"output_tokens":1}}`
	case ProviderOpenAI:
		body = `{"choices":[{"message":{"role":"assistant","content":"` + jsonEscape(text) + `"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
	case ProviderOllama:
		body = `{"message":{"role":"assistant","content":"` + jsonEscape(text) + `"},"prompt_eval_count":1,"eval_count":1}`
	default:
		t.Fatalf("未知 provider %q", provider)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(delay)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}
