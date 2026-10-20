package dnsprovider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// dns01Value 是 DNS-01 的 TXT 内容(RFC 8555:key authorization 的 SHA-256 base64url,恒 43 字符)。
// 三家都用它,免得看名字以为只有 Cloudflare 用。
const dns01Value = "dVKn3yFqFtGmZK7pQ9oJ3c1kxwT4bY2sN8rL6pE0uZM"

// --- Cloudflare -------------------------------------------------------------

// cfTXTHandler 造一个 Cloudflare 假服务端:zone 固定 z1,dns_records 的列表结果由入参给,
// 并把写操作(POST/PUT/DELETE)记进 calls。
func cfTXTHandler(t *testing.T, listJSON string, calls *[]string) roundTripFunc {
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "/zones") && !strings.Contains(r.URL.Path, "dns_records") && r.Method == http.MethodGet:
			return jsonResp(200, `{"success":true,"result":[{"id":"z1","name":"example.com"}]}`), nil
		case strings.Contains(r.URL.Path, "dns_records") && r.Method == http.MethodGet:
			if got := r.URL.Query().Get("type"); got != "TXT" {
				t.Fatalf("TXT 列表应带 type=TXT, got %q", got)
			}
			if got := r.URL.Query().Get("name"); got != "_acme-challenge.example.com" {
				t.Fatalf("TXT 列表应按记录名查, got %q", got)
			}
			return jsonResp(200, `{"success":true,"result":`+listJSON+`}`), nil
		case r.Method == http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			*calls = append(*calls, "POST:"+string(body))
			return jsonResp(200, `{"success":true,"result":{"id":"rec-new"}}`), nil
		case r.Method == http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			*calls = append(*calls, "PUT "+r.URL.Path+":"+string(body))
			return jsonResp(200, `{"success":true,"result":{}}`), nil
		case r.Method == http.MethodDelete:
			*calls = append(*calls, "DELETE:"+r.URL.Path)
			return jsonResp(200, `{"success":true,"result":{}}`), nil
		}
		t.Fatalf("未预期请求 %s %s", r.Method, r.URL)
		return nil, nil
	})
}

func TestCloudflareEnsureTXTCreatesWhenAbsent(t *testing.T) {
	var calls []string
	c := newCloudflareClient("cf-token", cfTXTHandler(t, `[]`, &calls), "")
	if err := c.EnsureTXTRecord(context.Background(), "example.com", "_acme-challenge.example.com", dns01Value); err != nil {
		t.Fatalf("EnsureTXTRecord: %v", err)
	}
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "POST:") {
		t.Fatalf("无既有记录应只发一次 POST, got %v", calls)
	}
	if !strings.Contains(calls[0], `"type":"TXT"`) || !strings.Contains(calls[0], dns01Value) {
		t.Fatalf("POST 正文应是 TXT + 校验值, got %s", calls[0])
	}
}

// 上次清理失败的残留(同名旧值 + 多条)必须被收敛成一条:DNS-01 见到两条 TXT 会直接判失败。
func TestCloudflareEnsureTXTConvergesStaleRecords(t *testing.T) {
	var calls []string
	list := `[{"id":"rec-A","name":"_acme-challenge.example.com","type":"TXT","content":"stale-old-value"},` +
		`{"id":"rec-B","name":"_acme-challenge.example.com","type":"TXT","content":"another-stale"}]`
	c := newCloudflareClient("cf-token", cfTXTHandler(t, list, &calls), "")
	if err := c.EnsureTXTRecord(context.Background(), "example.com", "_acme-challenge.example.com", dns01Value); err != nil {
		t.Fatalf("EnsureTXTRecord: %v", err)
	}
	var put, del int
	for _, x := range calls {
		switch {
		case strings.HasPrefix(x, "PUT"):
			put++
			if !strings.Contains(x, "rec-A") || !strings.Contains(x, dns01Value) {
				t.Fatalf("应把第一条改成新值, got %s", x)
			}
		case strings.HasPrefix(x, "DELETE"):
			del++
			if !strings.Contains(x, "rec-B") {
				t.Fatalf("多余那条应被删除, got %s", x)
			}
		}
	}
	if put != 1 || del != 1 {
		t.Fatalf("应 PUT 一条 + DELETE 一条, got PUT=%d DELETE=%d (%v)", put, del, calls)
	}
}

// 已是目标值(CF 回显带外层引号)→ 不该再发任何写请求。
func TestCloudflareEnsureTXTNoOpWhenAlreadyCorrect(t *testing.T) {
	var calls []string
	list := `[{"id":"rec-A","name":"_acme-challenge.example.com","type":"TXT","content":"` + dns01Value + `"}]`
	c := newCloudflareClient("cf-token", cfTXTHandler(t, list, &calls), "")
	if err := c.EnsureTXTRecord(context.Background(), "example.com", "_acme-challenge.example.com", dns01Value); err != nil {
		t.Fatalf("EnsureTXTRecord: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("内容已对不该改写记录, got %v", calls)
	}
}

func TestCloudflareDeleteTXTRemovesAll(t *testing.T) {
	var calls []string
	list := `[{"id":"rec-A","type":"TXT","content":"one"},{"id":"rec-B","type":"TXT","content":"two"}]`
	c := newCloudflareClient("cf-token", cfTXTHandler(t, list, &calls), "")
	if err := c.DeleteTXTRecord(context.Background(), "example.com", "_acme-challenge.example.com"); err != nil {
		t.Fatalf("DeleteTXTRecord: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("两条残留都要删(留一条就等于下次签废), got %v", calls)
	}
}

// --- DNSPod -----------------------------------------------------------------

// dpTXTHandler 造 DNSPod 假服务端:Record.List 返回 listJSON,其余写操作记进 calls。
func dpTXTHandler(t *testing.T, listJSON string, calls *[]string) roundTripFunc {
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		fields := readForm(t, r)
		if path == "Record.List" {
			if fields.Get("record_type") != "TXT" {
				t.Fatalf("列表应只查 TXT, got %q", fields.Get("record_type"))
			}
			if fields.Get("sub_domain") != "_acme-challenge" {
				t.Fatalf("sub_domain 应是相对根域的 _acme-challenge, got %q", fields.Get("sub_domain"))
			}
			return jsonResp(200, listJSON), nil
		}
		if fields.Get("login_token") == "" {
			t.Fatalf("写请求应带 login_token")
		}
		*calls = append(*calls, path+":"+fields.Encode())
		return jsonResp(200, `{"status":{"code":"1","message":"ok"}}`), nil
	})
}

func TestDNSPodEnsureTXTCreatesWhenAbsent(t *testing.T) {
	var calls []string
	// code "10" 是 DNSPod 的「无记录」,不能当错误。
	rt := dpTXTHandler(t, `{"status":{"code":"10","message":"No records."}}`, &calls)
	c := newDNSPodClient("12345,secret-token", rt, "")
	if err := c.EnsureTXTRecord(context.Background(), "example.com", "_acme-challenge.example.com", dns01Value); err != nil {
		t.Fatalf("EnsureTXTRecord: %v", err)
	}
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "Record.Create:") {
		t.Fatalf("无记录应只发一次 Record.Create, got %v", calls)
	}
	if !strings.Contains(calls[0], "record_type=TXT") || !strings.Contains(calls[0], "value="+dns01Value) {
		t.Fatalf("Create 正文应是 TXT + 校验值, got %s", calls[0])
	}
}

func TestDNSPodEnsureTXTConvergesStaleRecords(t *testing.T) {
	var calls []string
	list := `{"status":{"code":"1","message":"ok"},"records":[` +
		`{"id":"7","name":"_acme-challenge","type":"TXT","value":"stale"},` +
		`{"id":"8","name":"_acme-challenge","type":"TXT","value":"another-stale"}]}`
	rt := dpTXTHandler(t, list, &calls)
	c := newDNSPodClient("12345,secret-token", rt, "")
	if err := c.EnsureTXTRecord(context.Background(), "example.com", "_acme-challenge.example.com", dns01Value); err != nil {
		t.Fatalf("EnsureTXTRecord: %v", err)
	}
	var mod, rm int
	for _, x := range calls {
		switch {
		case strings.HasPrefix(x, "Record.Remove:"):
			rm++
			if !strings.Contains(x, "record_id=8") {
				t.Fatalf("应删多余那条, got %s", x)
			}
		case strings.HasPrefix(x, "Record.Modify:"):
			mod++
			if !strings.Contains(x, "record_id=7") || !strings.Contains(x, "value="+dns01Value) {
				t.Fatalf("应把第一条改成新值, got %s", x)
			}
		}
	}
	if mod != 1 || rm != 1 {
		t.Fatalf("应 Modify 一条 + Remove 一条, got %d/%d (%v)", mod, rm, calls)
	}
}

func TestDNSPodDeleteTXTRequiresValidCredential(t *testing.T) {
	var called bool
	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		called = true
		return jsonResp(200, `{}`), nil
	})
	c := newDNSPodClient("没有逗号", rt, "")
	err := c.DeleteTXTRecord(context.Background(), "example.com", "_acme-challenge.example.com")
	if !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("坏凭据应 ErrInvalidCredential, got %v", err)
	}
	if called {
		t.Fatalf("坏凭据不该发任何请求")
	}
}

// --- 阿里云 DNS --------------------------------------------------------------

func TestAliDNSEnsureTXTCreatesWhenAbsent(t *testing.T) {
	var added bool
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		q := r.URL.Query()
		switch q.Get("Action") {
		case "DescribeDomainRecords":
			if q.Get("Type") != "TXT" || q.Get("RRKeyWord") != "_acme-challenge" {
				t.Fatalf("列表应查 TXT/_acme-challenge, got %q/%q", q.Get("Type"), q.Get("RRKeyWord"))
			}
			return jsonResp(200, `{"DomainRecords":{"Record":[]}}`), nil
		case "AddDomainRecord":
			added = true
			if q.Get("Type") != "TXT" || q.Get("Value") != dns01Value || q.Get("RR") != "_acme-challenge" {
				t.Fatalf("Add 参数不对: %s", q)
			}
			return jsonResp(200, `{"RecordId":"rec-new"}`), nil
		}
		t.Fatalf("未预期 Action %q", q.Get("Action"))
		return nil, nil
	})
	c := fixedAliClient("testAccessKeyId,testAccessKeySecret", rt)
	if err := c.EnsureTXTRecord(context.Background(), "example.com", "_acme-challenge.example.com", dns01Value); err != nil {
		t.Fatalf("EnsureTXTRecord: %v", err)
	}
	if !added {
		t.Fatalf("无既有记录应 AddDomainRecord")
	}
}

func TestAliDNSEnsureTXTConvergesStaleRecords(t *testing.T) {
	var updatedID string
	var deleted []string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		q := r.URL.Query()
		switch q.Get("Action") {
		case "DescribeDomainRecords":
			// RRKeyWord 是模糊匹配:混进来的别的 RR 的记录不能被删。
			return jsonResp(200, `{"DomainRecords":{"Record":[`+
				`{"RecordId":"r-1","RR":"_acme-challenge","Type":"TXT","Value":"stale"},`+
				`{"RecordId":"r-2","RR":"_acme-challenge","Type":"TXT","Value":"more-stale"},`+
				`{"RecordId":"r-3","RR":"_acme-challenge.sub","Type":"TXT","Value":"别的名字"}]}}`), nil
		case "UpdateDomainRecord":
			updatedID = q.Get("RecordId")
			if q.Get("Value") != dns01Value {
				t.Fatalf("Update 应带新校验值, got %q", q.Get("Value"))
			}
			return jsonResp(200, `{}`), nil
		case "DeleteDomainRecord":
			deleted = append(deleted, q.Get("RecordId"))
			return jsonResp(200, `{}`), nil
		}
		t.Fatalf("未预期 Action %q", q.Get("Action"))
		return nil, nil
	})
	c := fixedAliClient("testAccessKeyId,testAccessKeySecret", rt)
	if err := c.EnsureTXTRecord(context.Background(), "example.com", "_acme-challenge.example.com", dns01Value); err != nil {
		t.Fatalf("EnsureTXTRecord: %v", err)
	}
	if updatedID != "r-1" {
		t.Fatalf("应改第一条, got %q", updatedID)
	}
	if strings.Join(deleted, ",") != "r-2" {
		t.Fatalf("只该删同名多余那条, got %v", deleted)
	}
}

func TestAliDNSTXTErrorIsClassifiedAndLeakFree(t *testing.T) {
	// 凭据无效:阿里云回 403 + InvalidAccessKeyId。错误要挂在 TXT 家族下,且绝不含 ak/sk。
	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return jsonResp(403, `{"Code":"InvalidAccessKeyId.NotFound","Message":"Specified access key is not found."}`), nil
	})
	c := fixedAliClient("testAccessKeyId,testAccessKeySecret", rt)
	err := c.EnsureTXTRecord(context.Background(), "example.com", "_acme-challenge.example.com", dns01Value)
	if !errors.Is(err, ErrEnsureTXT) {
		t.Fatalf("应挂 ErrEnsureTXT, got %v", err)
	}
	if strings.Contains(err.Error(), "testAccessKeySecret") || strings.Contains(err.Error(), "testAccessKeyId") {
		t.Fatalf("错误体泄漏凭据: %v", err)
	}
}

// --- service 层:根域边界与装配 ----------------------------------------------

// aOnlyClient 只实现 DNSClient(没有 TXT 方法)。用它断言:能力缺失要明说,不拿 A 记录糊弄。
type aOnlyClient struct{}

func (aOnlyClient) EnsureARecord(context.Context, string, string, string) error { return nil }
func (aOnlyClient) DeleteARecord(context.Context, string, string) error         { return nil }
func (aOnlyClient) VerifyZone(context.Context, string) error                    { return nil }

func TestServiceTXTRejectsNameOutsideZone(t *testing.T) {
	ctx := context.Background()
	var dialed bool
	dial := func(providerType, token string) DNSClient {
		dialed = true
		return newCloudflareClient(token, roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			t.Fatalf("越界的名字不该发请求")
			return nil, nil
		}), "")
	}
	svc := newTestService(t, stubVault{tokens: map[string]string{"cred-1": "cf-token"}}, nil, dial)
	p, err := svc.Create(ctx, CreateInput{Type: "cloudflare", Name: "CF", CredentialID: "cred-1", BaseDomain: "example.com"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, name := range []string{"", "other.com", "_acme-challenge.other.com", "_acme-challenge.example.com.evil.cn", "example.org"} {
		err := svc.EnsureTXTRecord(ctx, p.ID, name, dns01Value)
		if !errors.Is(err, ErrTXTOutsideZone) {
			t.Fatalf("名字 %q 不在根域下应 ErrTXTOutsideZone, got %v", name, err)
		}
		if e := svc.DeleteTXTRecord(ctx, p.ID, name); !errors.Is(e, ErrTXTOutsideZone) {
			t.Fatalf("删除同样该拦住: %v", e)
		}
	}
	// 根域检查排在取凭据之前:越界的名字连 token 都不该被解出来,更不该发给 API。
	if dialed {
		t.Fatalf("越界记录名不该拨号取凭据")
	}
}

func TestServiceTXTRequiresValue(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, stubVault{tokens: map[string]string{"cred-1": "cf-token"}}, nil, prodDialFactory)
	p, _ := svc.Create(ctx, CreateInput{Type: "cloudflare", Name: "CF", CredentialID: "cred-1", BaseDomain: "example.com"})
	if err := svc.EnsureTXTRecord(ctx, p.ID, "_acme-challenge.example.com", "   "); !errors.Is(err, ErrEmptyTXTValue) {
		t.Fatalf("空校验值应 ErrEmptyTXTValue, got %v", err)
	}
	if err := svc.EnsureTXTRecord(ctx, "no-such-provider", "_acme-challenge.example.com", dns01Value); !errors.Is(err, ErrNotFound) {
		t.Fatalf("提供商不存在应 ErrNotFound, got %v", err)
	}
}

func TestServiceTXTNeedsTXTSupport(t *testing.T) {
	ctx := context.Background()
	dial := func(providerType, token string) DNSClient { return aOnlyClient{} }
	svc := newTestService(t, stubVault{tokens: map[string]string{"cred-1": "cf-token"}}, nil, dial)
	p, _ := svc.Create(ctx, CreateInput{Type: "cloudflare", Name: "CF", CredentialID: "cred-1", BaseDomain: "example.com"})
	err := svc.EnsureTXTRecord(ctx, p.ID, "_acme-challenge.example.com", dns01Value)
	if !errors.Is(err, ErrProviderNotImplemented) {
		t.Fatalf("客户端不会写 TXT 时应明说未实现, got %v", err)
	}
	if err := svc.DeleteTXTRecord(ctx, p.ID, "_acme-challenge.example.com"); !errors.Is(err, ErrProviderNotImplemented) {
		t.Fatalf("删除同理: %v", err)
	}
}

// 端到一段(仍不触网):根域取自库里的提供商记录,记录名归一后才交给 API。
func TestServiceEnsureTXTUsesProviderZone(t *testing.T) {
	ctx := context.Background()
	var posted string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/zones"):
			return jsonResp(200, `{"success":true,"result":[{"id":"z1","name":"example.com"}]}`), nil
		case r.Method == http.MethodGet:
			return jsonResp(200, `{"success":true,"result":[]}`), nil
		case r.Method == http.MethodPost:
			b, _ := io.ReadAll(r.Body)
			posted = string(b)
			return jsonResp(200, `{"success":true,"result":{"id":"rec-new"}}`), nil
		}
		t.Fatalf("未预期请求 %s %s", r.Method, r.URL)
		return nil, nil
	})
	svc := newTestService(t, stubVault{tokens: map[string]string{"cred-1": "cf-token"}}, nil,
		func(providerType, token string) DNSClient { return newCloudflareClient(token, rt, "") })
	p, err := svc.Create(ctx, CreateInput{Type: "cloudflare", Name: "CF", CredentialID: "cred-1", BaseDomain: "Example.COM"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// 记录名带大写与末尾点:都该归一后再落 API(否则同名两条,CA 会判多条 TXT)。
	if err := svc.EnsureTXTRecord(ctx, p.ID, "_ACME-Challenge.Example.COM.", dns01Value); err != nil {
		t.Fatalf("EnsureTXTRecord: %v", err)
	}
	if !strings.Contains(posted, `"name":"_acme-challenge.example.com"`) || !strings.Contains(posted, dns01Value) {
		t.Fatalf("应以归一后的 FQDN 建 TXT, got %s", posted)
	}
}
