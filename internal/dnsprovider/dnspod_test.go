package dnsprovider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// readForm 读取并缓存 form-POST body(可多次调用而不耗尽 r.Body)。
func readForm(t *testing.T, r *http.Request) url.Values {
	t.Helper()
	if r.Body == nil {
		return url.Values{}
	}
	body, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	// 回填 body,便于同一 handler 内多次读取。
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	vals, err := url.ParseQuery(string(body))
	if err != nil {
		t.Fatalf("解析 form body: %v", err)
	}
	return vals
}

// readFormField 取某字段(每次重新解析缓存的 body)。
func readFormField(t *testing.T, r *http.Request, key string) string {
	t.Helper()
	return readForm(t, r).Get(key)
}

// --- subDomain 计算单测 ------------------------------------------------------

func TestSubDomain(t *testing.T) {
	cases := []struct {
		name, zone, want string
	}{
		{"app-x.example.com", "example.com", "app-x"},
		{"example.com", "example.com", "@"},
		{"EXAMPLE.COM", "example.com", "@"},
		{"App-X.Example.Com", "example.com", "app-x"},
		{"pr-12-proj.preview.example.com", "example.com", "pr-12-proj.preview"},
		{"app-x.example.com.", "example.com", "app-x"}, // 末尾点
		{"app-x", "example.com", "app-x"},              // 已是相对子域
		{"", "example.com", "@"},
	}
	for _, c := range cases {
		if got := subDomain(c.name, c.zone); got != c.want {
			t.Errorf("subDomain(%q,%q)=%q, want %q", c.name, c.zone, got, c.want)
		}
	}
}

// --- 凭据解析 ---------------------------------------------------------------

func TestParseDNSPodToken(t *testing.T) {
	if _, err := parseDNSPodToken("no-comma"); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("无逗号应 ErrInvalidCredential, got %v", err)
	}
	if _, err := parseDNSPodToken(",tok"); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("空 id 应 ErrInvalidCredential, got %v", err)
	}
	if _, err := parseDNSPodToken("123,"); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("空 token 应 ErrInvalidCredential, got %v", err)
	}
	lt, err := parseDNSPodToken("  123 , abcDEF  ")
	if err != nil || lt != "123,abcDEF" {
		t.Fatalf("应 trim 后拼回, got %q / %v", lt, err)
	}
}

// --- ListZones --------------------------------------------------------------

func TestDNSPodListZonesSuccess(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.URL.Path, "/Domain.List") {
			t.Fatalf("ListZones 应打 Domain.List, got %s", r.URL.Path)
		}
		if got := readFormField(t, r, "login_token"); got != "12345,sEcReT" {
			t.Fatalf("应带 login_token, got %q", got)
		}
		if got := readFormField(t, r, "format"); got != "json" {
			t.Fatalf("应带 format=json, got %q", got)
		}
		if got := readFormField(t, r, "type"); got != "mine" {
			t.Fatalf("只该列自己账号的域名(type=mine), got %q", got)
		}
		return jsonResp(200, `{"status":{"code":"1","message":"Action completed successful"},"info":{"domain_total":"2"},"domains":[{"name":"Example.COM"},{"name":"blog.example.com"}]}`), nil
	})
	c := newDNSPodClient("12345,sEcReT", rt, "")
	zones, err := c.ListZones(context.Background())
	if err != nil {
		t.Fatalf("ListZones: %v", err)
	}
	if strings.Join(zones, ",") != "blog.example.com,example.com" {
		t.Fatalf("应归一并排序, got %v", zones)
	}
}

// 按 info.domain_total 翻页而不是「本页不满就停」:DNSPod 的 length 上限很大但默认值会截,
// 少收一页就等于那个域永远签不出证书。
func TestDNSPodListZonesPagesUntilTotal(t *testing.T) {
	var offsets []string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		off := readFormField(t, r, "offset")
		offsets = append(offsets, off)
		if off == "0" {
			return jsonResp(200, `{"status":{"code":"1"},"info":{"domain_total":"2"},"domains":[{"name":"a.com"}]}`), nil
		}
		return jsonResp(200, `{"status":{"code":"1"},"info":{"domain_total":"2"},"domains":[{"name":"b.com"}]}`), nil
	})
	c := newDNSPodClient("12345,sEcReT", rt, "")
	zones, err := c.ListZones(context.Background())
	if err != nil {
		t.Fatalf("ListZones: %v", err)
	}
	if strings.Join(offsets, ",") != "0,200" {
		t.Fatalf("应翻第二页, got %v", offsets)
	}
	if strings.Join(zones, ",") != "a.com,b.com" {
		t.Fatalf("两页都要收进清单, got %v", zones)
	}
}

func TestDNSPodListZonesNoDomain(t *testing.T) {
	// DNSPod 的「账号无域名」是 code "9" 而不是空列表 —— 与 Record.List 的 "10" 同法当空处理,不是错误。
	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return jsonResp(200, `{"status":{"code":"9","message":"No domain records"},"info":{"domain_total":"0"},"domains":[]}`), nil
	})
	c := newDNSPodClient("12345,sEcReT", rt, "")
	zones, err := c.ListZones(context.Background())
	if err != nil {
		t.Fatalf("账号里没有域名不算失败: %v", err)
	}
	if len(zones) != 0 {
		t.Fatalf("应空清单, got %v", zones)
	}
}

func TestDNSPodListZonesAuthFail(t *testing.T) {
	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return jsonResp(200, `{"status":{"code":"-1","message":"Login token error"}}`), nil
	})
	c := newDNSPodClient("12345,bad-secret", rt, "")
	_, err := c.ListZones(context.Background())
	if !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("鉴权失败应 ErrVerifyFailed, got %v", err)
	}
	if strings.Contains(err.Error(), "bad-secret") || strings.Contains(err.Error(), "12345") {
		t.Fatalf("错误文本不应含凭据: %v", err)
	}
}

func TestDNSPodInvalidCredential(t *testing.T) {
	// 保险库存了无逗号串 → ListZones 在请求前就报 ErrInvalidCredential(不发网络)。
	called := false
	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		called = true
		return jsonResp(200, `{"status":{"code":"1"}}`), nil
	})
	c := newDNSPodClient("no-comma-token", rt, "")
	_, err := c.ListZones(context.Background())
	if !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("坏凭据应 ErrInvalidCredential, got %v", err)
	}
	if called {
		t.Fatalf("坏凭据不应发任何请求")
	}
}

// --- EnsureARecord ----------------------------------------------------------

func TestDNSPodEnsureARecordCreate(t *testing.T) {
	var created bool
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "/Record.List"):
			if got := readFormField(t, r, "sub_domain"); got != "app-abc" {
				t.Fatalf("List 应带 sub_domain=app-abc, got %q", got)
			}
			if got := readFormField(t, r, "record_type"); got != "A" {
				t.Fatalf("List 应带 record_type=A, got %q", got)
			}
			// 无记录:DNSPod 返回 code 10。
			return jsonResp(200, `{"status":{"code":"10","message":"No records"},"records":[]}`), nil
		case strings.Contains(r.URL.Path, "/Record.Create"):
			created = true
			if got := readFormField(t, r, "value"); got != "203.0.113.5" {
				t.Fatalf("Create 应带目标 IP, got %q", got)
			}
			if got := readFormField(t, r, "record_line"); got != "默认" {
				t.Fatalf("Create 应带 record_line=默认, got %q", got)
			}
			if got := readFormField(t, r, "sub_domain"); got != "app-abc" {
				t.Fatalf("Create 应带 sub_domain=app-abc, got %q", got)
			}
			return jsonResp(200, `{"status":{"code":"1","message":"ok"}}`), nil
		}
		t.Fatalf("未预期请求 %s", r.URL.Path)
		return nil, nil
	})
	c := newDNSPodClient("12345,sEcReT", rt, "")
	if err := c.EnsureARecord(context.Background(), "example.com", "app-abc.example.com", "203.0.113.5"); err != nil {
		t.Fatalf("EnsureARecord: %v", err)
	}
	if !created {
		t.Fatalf("无既有记录应 Record.Create")
	}
}

func TestDNSPodEnsureARecordModify(t *testing.T) {
	var modifiedID string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "/Record.List"):
			// 既有 A 记录,value 不同 → 应 Modify。
			return jsonResp(200, `{"status":{"code":"1"},"records":[{"id":"99","name":"app-abc","type":"A","value":"1.1.1.1"}]}`), nil
		case strings.Contains(r.URL.Path, "/Record.Modify"):
			modifiedID = readFormField(t, r, "record_id")
			if got := readFormField(t, r, "value"); got != "203.0.113.9" {
				t.Fatalf("Modify 应带新 IP, got %q", got)
			}
			return jsonResp(200, `{"status":{"code":"1"}}`), nil
		}
		t.Fatalf("未预期请求 %s", r.URL.Path)
		return nil, nil
	})
	c := newDNSPodClient("12345,sEcReT", rt, "")
	if err := c.EnsureARecord(context.Background(), "example.com", "app-abc.example.com", "203.0.113.9"); err != nil {
		t.Fatalf("EnsureARecord: %v", err)
	}
	if modifiedID != "99" {
		t.Fatalf("应 Modify record_id=99, got %q", modifiedID)
	}
}

func TestDNSPodEnsureARecordNoOpSameValue(t *testing.T) {
	var mutated bool
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "/Record.List"):
			return jsonResp(200, `{"status":{"code":"1"},"records":[{"id":"99","name":"app-abc","type":"A","value":"203.0.113.5"}]}`), nil
		case strings.Contains(r.URL.Path, "/Record.Modify"), strings.Contains(r.URL.Path, "/Record.Create"):
			mutated = true
			return jsonResp(200, `{"status":{"code":"1"}}`), nil
		}
		t.Fatalf("未预期请求 %s", r.URL.Path)
		return nil, nil
	})
	c := newDNSPodClient("12345,sEcReT", rt, "")
	if err := c.EnsureARecord(context.Background(), "example.com", "app-abc.example.com", "203.0.113.5"); err != nil {
		t.Fatalf("EnsureARecord: %v", err)
	}
	if mutated {
		t.Fatalf("值相同应幂等 no-op,不发 Modify/Create")
	}
}

func TestDNSPodEnsureARecordErrorNoLeak(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/Record.List") {
			return jsonResp(200, `{"status":{"code":"-1","message":"token error"}}`), nil
		}
		t.Fatalf("不应到达 %s", r.URL.Path)
		return nil, nil
	})
	c := newDNSPodClient("12345,top-secret", rt, "")
	err := c.EnsureARecord(context.Background(), "example.com", "app-abc.example.com", "203.0.113.5")
	if !errors.Is(err, ErrEnsureRecord) {
		t.Fatalf("应 ErrEnsureRecord, got %v", err)
	}
	if strings.Contains(err.Error(), "top-secret") {
		t.Fatalf("错误文本不应含凭据: %v", err)
	}
}
