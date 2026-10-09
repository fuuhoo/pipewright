package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/ai"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/store"
	"github.com/fuuhoo/pipewright/internal/vault"
)

// setupAIServer 构造带 auth + vault + ai 的测试 server;ai 用注入 client(默认指向给定 stub)。
func setupAIServer(t *testing.T, client *http.Client) (*httptest.Server, *http.Client, string) {
	srv, c, csrf, _ := setupAIServerWithStore(t, client)
	return srv, c, csrf
}

// setupAIServerWithStore 同上,另回 *store.Store:月度用量台账没有对外写入口
// (只有内部 chat 记账),测读数就得自己按行灌库。
func setupAIServerWithStore(t *testing.T, client *http.Client) (*httptest.Server, *http.Client, string, *store.Store) {
	t.Helper()
	st := testStoreAuth(t)
	svc := auth.NewService(st.DB, nil, nil)
	if err := svc.Bootstrap("admin", "testpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	v := vault.New(st.DB, testMasterKey())
	if client == nil {
		client = http.DefaultClient
	}
	aiSvc := ai.New(st.DB, v, client)
	srv := httptest.NewServer(New(testWebFSAuth(), svc, WithVault(v), WithAISettings(aiSvc)))
	t.Cleanup(srv.Close)

	c := newTestClient(t)
	csrf := loginWithClient(t, c, srv.URL)
	return srv, c, csrf, st
}

// aiOverview 解析 GET/PUT 响应体:当前生效者 + 三档各自的配置(缺档即契约漏档)。
func aiOverview(t *testing.T, raw []byte) (string, map[string]map[string]any) {
	t.Helper()
	var dto struct {
		Active  string           `json:"active"`
		Configs []map[string]any `json:"configs"`
	}
	if err := json.Unmarshal(raw, &dto); err != nil {
		t.Fatalf("解析总览失败: %v (%s)", err, raw)
	}
	items := make(map[string]map[string]any, len(dto.Configs))
	for _, c := range dto.Configs {
		p, _ := c["provider"].(string)
		items[p] = c
	}
	for _, p := range []string{"claude", "openai", "ollama"} {
		if _, ok := items[p]; !ok {
			t.Fatalf("总览应含 %q 一档: %s", p, raw)
		}
	}
	return dto.Active, items
}

func TestAIGetLazyDefault(t *testing.T) {
	srv, client, csrf := setupAIServer(t, nil)
	resp := doJSON(t, client, http.MethodGet, srv.URL+"/api/settings/ai", csrf, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	active, items := aiOverview(t, raw)
	if active != "" {
		t.Fatalf("无配置时 active 应空: %q", active)
	}
	for p, dto := range items {
		if dto["configured"] != false || dto["enabled"] != false {
			t.Fatalf("%s 默认应 configured/enabled=false: %s", p, raw)
		}
		if dto["apiKeyMasked"] != "" {
			t.Fatalf("%s 默认 apiKeyMasked 应空: %s", p, raw)
		}
		if dto["updatedAt"] != nil {
			t.Fatalf("%s 默认 updatedAt 应 null: %v", p, dto["updatedAt"])
		}
		b, _ := dto["budget"].(map[string]any)
		if _, ok := b["monthlyTokenLimit"]; !ok {
			t.Fatalf("%s budget.monthlyTokenLimit 字段应存在: %s", p, raw)
		}
		u, _ := dto["usage"].(map[string]any)
		if u == nil || u["monthPrompt"] != float64(0) || u["monthCompletion"] != float64(0) {
			t.Fatalf("%s usage 应是零值本月用量(字段恒在): %v / %s", p, dto["usage"], raw)
		}
	}
}

// TestAIGetCarriesMonthUsage 验证 GET 把「本月已用」带进每档的 usage:与 monthlyTokenLimit
// 同一口径(UTC 自然月),上月的行绝不串进本月 —— 否则页面数字只增不减,月上限形同虚设。
func TestAIGetCarriesMonthUsage(t *testing.T) {
	srv, client, csrf, st := setupAIServerWithStore(t, nil)
	now := time.Now().UTC()
	thisMonth := now.Format("2006-01")
	firstOfThisMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	lastMonth := firstOfThisMonth.AddDate(0, 0, -1).Format("2006-01")

	seed := func(provider, month string, prompt, completion int64) {
		t.Helper()
		_, err := st.DB.ExecContext(context.Background(),
			`INSERT INTO ai_token_usage
			   (provider, usage_month, prompt_tokens, completion_tokens, updated_at)
			 VALUES (?, ?, ?, ?, ?)`,
			provider, month, prompt, completion, firstOfThisMonth.Format(time.RFC3339),
		)
		if err != nil {
			t.Fatalf("灌 %s %s 用量: %v", provider, month, err)
		}
	}
	seed("ollama", thisMonth, 12345, 678)
	seed("ollama", lastMonth, 9_999_999, 9_999_999)
	seed("claude", thisMonth, 7, 3)

	resp := doJSON(t, client, http.MethodGet, srv.URL+"/api/settings/ai", csrf, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	_, items := aiOverview(t, raw)

	want := map[string][2]int64{"ollama": {12345, 678}, "claude": {7, 3}, "openai": {0, 0}}
	for p, w := range want {
		u, _ := items[p]["usage"].(map[string]any)
		if u == nil {
			t.Fatalf("%s 应带 usage 对象: %s", p, raw)
		}
		gotP, _ := u["monthPrompt"].(float64)
		gotC, _ := u["monthCompletion"].(float64)
		if int64(gotP) != w[0] || int64(gotC) != w[1] {
			t.Errorf("%s usage = {%v,%v}, want {%d,%d} (%s)", p, gotP, gotC, w[0], w[1], raw)
		}
	}
}

func TestAIPutClaudeMaskedNoPlaintext(t *testing.T) {
	srv, client, csrf := setupAIServer(t, nil)
	body := `{"provider":"claude","model":"claude-sonnet-4","apiKey":"sk-ant-PLAINTEXT-abcd","budget":{"monthlyTokenLimit":1000},"enabled":true}`
	resp := doJSON(t, client, http.MethodPut, srv.URL+"/api/settings/ai", csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("PUT status = %d, body=%s", resp.StatusCode, raw)
	}
	raw, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(raw), "PLAINTEXT") || strings.Contains(string(raw), "sk-ant-PLAINTEXT") {
		t.Fatalf("响应不应含明文 key: %s", raw)
	}
	active, items := aiOverview(t, raw)
	if active != "claude" {
		t.Fatalf("active 应是 claude: %q", active)
	}
	claude := items["claude"]
	if claude["configured"] != true || claude["enabled"] != true {
		t.Fatalf("claude 档应 configured/enabled=true: %s", raw)
	}
	masked, _ := claude["apiKeyMasked"].(string)
	if !strings.Contains(masked, "••••") || !strings.HasSuffix(masked, "abcd") {
		t.Fatalf("apiKeyMasked 应掩码保末 4: %q", masked)
	}
	if claude["baseUrl"] != "https://api.anthropic.com" {
		t.Fatalf("baseUrl 应兜底默认: %v", claude["baseUrl"])
	}
	if claude["model"] != "claude-sonnet-4" {
		t.Fatalf("model 应存下来: %v", claude["model"])
	}
	if claude["updatedAt"] == nil {
		t.Fatalf("保存后 updatedAt 应非 null")
	}
}

func TestAIPutKeyOmittedRetains(t *testing.T) {
	srv, client, csrf := setupAIServer(t, nil)
	base := srv.URL + "/api/settings/ai"
	// 首存带 key。
	r1 := doJSON(t, client, http.MethodPut, base, csrf,
		`{"provider":"claude","apiKey":"sk-ant-firstKEY9","budget":{"monthlyTokenLimit":null},"enabled":true}`)
	raw1, _ := io.ReadAll(r1.Body)
	r1.Body.Close()
	_, items1 := aiOverview(t, raw1)
	mask1, _ := items1["claude"]["apiKeyMasked"].(string)
	if mask1 == "" {
		t.Fatalf("首存应有掩码: %s", raw1)
	}

	// 二次省略 apiKey → 保留旧。
	r2 := doJSON(t, client, http.MethodPut, base, csrf,
		`{"provider":"claude","model":"claude-x","budget":{"monthlyTokenLimit":null},"enabled":true}`)
	raw2, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("二次保存 status=%d: %s", r2.StatusCode, raw2)
	}
	_, items2 := aiOverview(t, raw2)
	claude2 := items2["claude"]
	if claude2["configured"] != true {
		t.Fatalf("省略 key 应仍 configured: %s", raw2)
	}
	if claude2["apiKeyMasked"] != mask1 {
		t.Fatalf("省略 key 应保留旧掩码: %v != %q", claude2["apiKeyMasked"], mask1)
	}
}

// 保存 ollama 只动 ollama 那一行:claude 的地址/模型/密钥原样留着,切回来还在。
func TestAIPutOtherProviderDoesNotClobber(t *testing.T) {
	srv, client, csrf := setupAIServer(t, nil)
	base := srv.URL + "/api/settings/ai"
	put := func(body string) []byte {
		t.Helper()
		r := doJSON(t, client, http.MethodPut, base, csrf, body)
		defer r.Body.Close()
		if r.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(r.Body)
			t.Fatalf("PUT %s status=%d: %s", body, r.StatusCode, raw)
		}
		raw, _ := io.ReadAll(r.Body)
		return raw
	}

	put(`{"provider":"claude","baseUrl":"https://gw.internal/anthropic","model":"claude-sonnet-4",
	     "apiKey":"sk-ant-claudeKEY1","budget":{"monthlyTokenLimit":null},"enabled":true}`)
	raw := put(`{"provider":"ollama","baseUrl":"http://127.0.0.1:11434","model":"llama3",
	     "budget":{"monthlyTokenLimit":null},"enabled":true}`)

	active, items := aiOverview(t, raw)
	claude := items["claude"]
	if claude["baseUrl"] != "https://gw.internal/anthropic" || claude["model"] != "claude-sonnet-4" {
		t.Fatalf("ollama 保存不该覆盖 claude 的地址/模型: %+v", claude)
	}
	if !strings.HasSuffix(claude["apiKeyMasked"].(string), "KEY1") {
		t.Fatalf("claude 自己的密钥应留着: %v", claude["apiKeyMasked"])
	}
	if claude["enabled"] != false {
		t.Fatalf("ollama 成为生效档后 claude 应停用: %+v", claude)
	}
	if active != "ollama" {
		t.Fatalf("active 应是 ollama: %q", active)
	}
	if items["ollama"]["model"] != "llama3" {
		t.Fatalf("ollama 应存自己的模型: %+v", items["ollama"])
	}
	// 从未配过的 openai 一档仍是空的,不会被两边的值污染。
	openai := items["openai"]
	if openai["configured"] != false || openai["baseUrl"] != "" || openai["apiKeyMasked"] != "" {
		t.Fatalf("未配的 openai 应为空默认: %+v", openai)
	}
}

func TestAIPutInvalidProvider422(t *testing.T) {
	srv, client, csrf := setupAIServer(t, nil)
	resp := doJSON(t, client, http.MethodPut, srv.URL+"/api/settings/ai", csrf,
		`{"provider":"gemini","enabled":false,"budget":{"monthlyTokenLimit":null}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	var e map[string]map[string]string
	_ = json.Unmarshal(raw, &e)
	if e["error"]["code"] != "invalid_provider" {
		t.Fatalf("应 invalid_provider: %s", raw)
	}
}

func TestAIPutNonOllamaWithoutKey422(t *testing.T) {
	srv, client, csrf := setupAIServer(t, nil)
	resp := doJSON(t, client, http.MethodPut, srv.URL+"/api/settings/ai", csrf,
		`{"provider":"openai","enabled":false,"budget":{"monthlyTokenLimit":null}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	var e map[string]map[string]string
	_ = json.Unmarshal(raw, &e)
	if e["error"]["code"] != "api_key_required" {
		t.Fatalf("应 api_key_required: %s", raw)
	}
}

func TestAITestStubOK(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer stub.Close()

	srv, client, csrf := setupAIServer(t, stub.Client())
	body := `{"provider":"claude","baseUrl":"` + stub.URL + `","apiKey":"sk-ant-draftKEY"}`
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/settings/ai/test", csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("test status = %d: %s", resp.StatusCode, raw)
	}
	raw, _ := io.ReadAll(resp.Body)
	var dto map[string]any
	_ = json.Unmarshal(raw, &dto)
	if dto["ok"] != true {
		t.Fatalf("stub 2xx 应 ok=true: %s", raw)
	}
	if _, ok := dto["latencyMs"]; !ok {
		t.Fatalf("应回 latencyMs: %s", raw)
	}
	if dto["error"] != nil {
		t.Fatalf("成功 error 应 null: %v", dto["error"])
	}
}

func TestAITestStub4xxNoKeyLeak(t *testing.T) {
	const secret = "sk-ant-SECRET-LEAK"
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer stub.Close()

	srv, client, csrf := setupAIServer(t, stub.Client())
	body := `{"provider":"openai","baseUrl":"` + stub.URL + `","apiKey":"` + secret + `"}`
	resp := doJSON(t, client, http.MethodPost, srv.URL+"/api/settings/ai/test", csrf, body)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "SECRET") {
		t.Fatalf("test 错误绝不含密钥: %s", raw)
	}
	var dto map[string]any
	_ = json.Unmarshal(raw, &dto)
	if dto["ok"] != false {
		t.Fatalf("401 应 ok=false: %s", raw)
	}
	if dto["error"] == nil {
		t.Fatalf("失败应有 error: %s", raw)
	}
}

func TestAIGetRequiresAuth(t *testing.T) {
	srv, _, _ := setupAIServer(t, nil)
	resp, err := http.Get(srv.URL + "/api/settings/ai")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无会话 GET status = %d, want 401", resp.StatusCode)
	}
}

func TestAIPutRequiresCSRF(t *testing.T) {
	srv, client, _ := setupAIServer(t, nil)
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/ai",
		strings.NewReader(`{"provider":"ollama","enabled":true,"budget":{"monthlyTokenLimit":null}}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("无 CSRF PUT status = %d, want 403", resp.StatusCode)
	}
}

func TestAITestRequiresCSRF(t *testing.T) {
	srv, client, _ := setupAIServer(t, nil)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/settings/ai/test",
		strings.NewReader(`{"provider":"ollama"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("无 CSRF test status = %d, want 403", resp.StatusCode)
	}
}
