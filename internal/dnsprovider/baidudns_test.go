package dnsprovider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// --- 凭据解析 ---------------------------------------------------------------

func TestParseBaiduCred(t *testing.T) {
	if _, _, err := parseBaiduCred("no-comma"); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("无逗号应 ErrInvalidCredential, got %v", err)
	}
	if _, _, err := parseBaiduCred(",sk"); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("空 ak 应 ErrInvalidCredential, got %v", err)
	}
	if _, _, err := parseBaiduCred("ak,"); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("空 sk 应 ErrInvalidCredential, got %v", err)
	}
	ak, sk, err := parseBaiduCred("  baiduAKtestExample , baiduSKtestExample  ")
	if err != nil || ak != "baiduAKtestExample" || sk != "baiduSKtestExample" {
		t.Fatalf("应 trim 切分, got %q/%q/%v", ak, sk, err)
	}
}

// --- 签名已知向量(bce-auth-v1,由官方 bce-sdk-go 算出)-----------------------
// 向量不是我们自己实现回灌自己的,而是 /tmp 里一个小程序按 bce-sdk-go/auth/signer.go 的
// 装配顺序 + SDK 自己的 util.UriEncode / util.HmacSha256Hex 跑出来的固定输入结果。
// 这两处正是最容易签错的地方:规范头 key/value **都要** UriEncode,且规范请求末尾**没有**换行。

const (
	baiduTestAK = "baiduAKtestExample"
	baiduTestSK = "baiduSKtestExample"

	// VerifyZone 发的那次列表请求(GET /v1/dns/zone?maxKeys=100&name=example.com)。
	baiduVectorList = "bce-auth-v1/baiduAKtestExample/2026-01-02T03:04:05Z/3600/content-type;host;x-bce-date/" +
		"fab659ff9ce2a2a7c052ddaee42707b5bfae6a86a20e031031149565e33bda6f"
	// 添加记录那次写请求(POST /v1/dns/zone/example.com/record,无查询串)。
	baiduVectorWrite = "bce-auth-v1/baiduAKtestExample/2026-01-02T03:04:05Z/3600/content-type;host;x-bce-date/" +
		"146149edc2580fc9e270c1b97a106a7a69b2d5172795693c262c4334df20efde"
)

func TestBceAuthorizationKnownVector(t *testing.T) {
	got := bceAuthorization(baiduTestAK, baiduTestSK, http.MethodGet, "dns.baidubce.com", "/v1/dns/zone",
		map[string]string{"name": "example.com", "maxKeys": "100"}, "2026-01-02T03:04:05Z")
	if got != baiduVectorList {
		t.Fatalf("列表签名不符官方 SDK 向量\n got=%q\nwant=%q", got, baiduVectorList)
	}
	got = bceAuthorization(baiduTestAK, baiduTestSK, http.MethodPost, "dns.baidubce.com", "/v1/dns/zone/example.com/record",
		nil, "2026-01-02T03:04:05Z")
	if got != baiduVectorWrite {
		t.Fatalf("写请求签名不符官方 SDK 向量\n got=%q\nwant=%q", got, baiduVectorWrite)
	}
}

// fixedBaiduClient 造一个钉死 x-bce-date 的百度客户端(供签名与请求流断言)。
func fixedBaiduClient(cred string, rt http.RoundTripper) *baiduDNSClient {
	c := newBaiduDNSClient(cred, rt, "")
	c.nowFn = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }
	return c
}

func baiduJSON(body string) *http.Response { return jsonResp(200, body) }

// --- VerifyZone -------------------------------------------------------------

func TestBaiduVerifyZoneSuccess(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet {
			t.Fatalf("查域名列表应 GET, got %s", r.Method)
		}
		if r.URL.Path != "/v1/dns/zone" {
			t.Fatalf("应走 /v1/dns/zone(公网 DNS 按域名寻址), got %q", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("name") != "example.com" || q.Get("maxKeys") != "100" {
			t.Fatalf("应带 name/maxKeys, got %v", q)
		}
		if r.Header.Get("Authorization") != baiduVectorList {
			t.Fatalf("Authorization 不符官方 SDK 向量: %q", r.Header.Get("Authorization"))
		}
		// 签名里签的就是这个头,发出去的必须一字不差。
		if r.Header.Get("x-bce-date") != "2026-01-02T03:04:05Z" {
			t.Fatalf("x-bce-date 应与签名时刻一致, got %q", r.Header.Get("x-bce-date"))
		}
		return baiduJSON(`{"zones":[{"name":"example.com","status":"running"}]}`), nil
	})
	c := fixedBaiduClient(baiduTestAK+","+baiduTestSK, rt)
	if err := c.VerifyZone(context.Background(), "example.com"); err != nil {
		t.Fatalf("VerifyZone: %v", err)
	}
}

func TestBaiduVerifyZoneNotFound(t *testing.T) {
	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		// name 是精确筛选,但列表里回的是别的域 → 判不可管理。
		return baiduJSON(`{"zones":[{"name":"other.com","status":"running"}]}`), nil
	})
	c := fixedBaiduClient(baiduTestAK+","+baiduTestSK, rt)
	err := c.VerifyZone(context.Background(), "example.com")
	if !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("域名不受该凭据管理应 ErrVerifyFailed, got %v", err)
	}
	if !strings.Contains(err.Error(), "不存在或不受该凭据管理") {
		t.Fatalf("应给可行动的人话, got %v", err)
	}
}

func TestBaiduVerifyZoneNotRunning(t *testing.T) {
	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		// 域名在但被暂停/锁定:解析不生效,DNS-01 必然失败,要在这里就拦住。
		return baiduJSON(`{"zones":[{"name":"example.com","status":"lock"}]}`), nil
	})
	c := fixedBaiduClient(baiduTestAK+","+baiduTestSK, rt)
	if err := c.VerifyZone(context.Background(), "example.com"); !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("域名非 running 应 ErrVerifyFailed, got %v", err)
	}
}

func TestBaiduVerifyZoneAuthFail(t *testing.T) {
	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return jsonResp(403, `{"code":"SignatureDoesNotMatch","message":"The signature does not match.","requestId":"r1"}`), nil
	})
	c := fixedBaiduClient(baiduTestAK+",wrong-sk", rt)
	err := c.VerifyZone(context.Background(), "example.com")
	if !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("验签失败应 ErrVerifyFailed, got %v", err)
	}
	if !strings.Contains(err.Error(), "凭据无效或无权限") {
		t.Fatalf("应点名凭据问题, got %v", err)
	}
	if strings.Contains(err.Error(), "wrong-sk") || strings.Contains(err.Error(), baiduTestAK) {
		t.Fatalf("错误文本不应含凭据: %v", err)
	}
}

func TestBaiduInvalidCredentialSendsNothing(t *testing.T) {
	called := false
	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		called = true
		return baiduJSON(`{}`), nil
	})
	c := fixedBaiduClient("no-comma", rt)
	if err := c.VerifyZone(context.Background(), "example.com"); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("坏凭据应 ErrInvalidCredential, got %v", err)
	}
	if called {
		t.Fatalf("坏凭据不应发任何请求")
	}
}

// --- A 记录 -----------------------------------------------------------------

func TestBaiduEnsureARecordCreate(t *testing.T) {
	var post *http.Request
	var body map[string]any
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodGet:
			if r.URL.Query().Get("rr") != "app-abc" {
				t.Fatalf("列表应带 rr=app-abc, got %q", r.URL.Query().Get("rr"))
			}
			return baiduJSON(`{"records":[]}`), nil
		case http.MethodPost:
			raw, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatalf("实体非 JSON: %q", raw)
			}
			post = r
			// 写接口按文档不返回实体,只判状态码。
			return jsonResp(200, ``), nil
		}
		t.Fatalf("未预期方法 %s", r.Method)
		return nil, nil
	})
	c := fixedBaiduClient(baiduTestAK+","+baiduTestSK, rt)
	if err := c.EnsureARecord(context.Background(), "example.com", "app-abc.example.com", "203.0.113.5"); err != nil {
		t.Fatalf("EnsureARecord: %v", err)
	}
	if post.URL.Path != "/v1/dns/zone/example.com/record" {
		t.Fatalf("添加记录路径不符: %q", post.URL.Path)
	}
	// 写请求同样要钉死签名(实体的有无不影响 bce-auth-v1 的规范请求)。
	if post.Header.Get("Authorization") != baiduVectorWrite {
		t.Fatalf("写请求 Authorization 不符官方 SDK 向量: %q", post.Header.Get("Authorization"))
	}
	if body["rr"] != "app-abc" || body["type"] != "A" || body["value"] != "203.0.113.5" ||
		body["line"] != baiduDefaultLine || body["ttl"] != float64(baiduDefaultTTL) {
		t.Fatalf("添加记录实体不符: %v", body)
	}
}

func TestBaiduEnsureARecordUpdate(t *testing.T) {
	var putPath string
	var body map[string]any
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodGet:
			return baiduJSON(`{"records":[{"id":"1234","rr":"app-abc","type":"A","value":"1.2.3.4"}]}`), nil
		case http.MethodPut:
			putPath = r.URL.Path
			raw, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatalf("实体非 JSON: %q", raw)
			}
			return jsonResp(200, ``), nil
		case http.MethodPost:
			t.Fatalf("已有记录不应再添加")
			return nil, nil
		}
		t.Fatalf("未预期方法 %s", r.Method)
		return nil, nil
	})
	c := fixedBaiduClient(baiduTestAK+","+baiduTestSK, rt)
	if err := c.EnsureARecord(context.Background(), "example.com", "app-abc.example.com", "203.0.113.9"); err != nil {
		t.Fatalf("EnsureARecord: %v", err)
	}
	if putPath != "/v1/dns/zone/example.com/record/1234" {
		t.Fatalf("更新应按记录 id 定位, got %q", putPath)
	}
	if body["value"] != "203.0.113.9" || body["rr"] != "app-abc" {
		t.Fatalf("更新实体不符: %v", body)
	}
	if _, ok := body["line"]; ok {
		t.Fatalf("更新实体不许带 line(线路在添加时定,文档明确更新不改线路): %v", body)
	}
}

func TestBaiduRecordIDAcceptsNumber(t *testing.T) {
	// 文档示例 id 是字符串,真机见过数字形态;解错就永远更新/删除不了那条记录。
	var putPath string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodGet:
			return baiduJSON(`{"records":[{"id":987654,"rr":"app-abc","type":"A","value":"1.2.3.4"}]}`), nil
		case http.MethodPut:
			putPath = r.URL.Path
			return jsonResp(200, ``), nil
		}
		t.Fatalf("未预期方法 %s", r.Method)
		return nil, nil
	})
	c := fixedBaiduClient(baiduTestAK+","+baiduTestSK, rt)
	if err := c.EnsureARecord(context.Background(), "example.com", "app-abc.example.com", "203.0.113.9"); err != nil {
		t.Fatalf("EnsureARecord: %v", err)
	}
	if !strings.HasSuffix(putPath, "/record/987654") {
		t.Fatalf("数字 id 也应能用, got %q", putPath)
	}
}

func TestBaiduEnsureARecordNoOp(t *testing.T) {
	writes := 0
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet {
			return baiduJSON(`{"records":[{"id":"1234","rr":"app-abc","type":"A","value":"203.0.113.5"}]}`), nil
		}
		writes++
		return jsonResp(200, ``), nil
	})
	c := fixedBaiduClient(baiduTestAK+","+baiduTestSK, rt)
	if err := c.EnsureARecord(context.Background(), "example.com", "app-abc.example.com", "203.0.113.5"); err != nil {
		t.Fatalf("EnsureARecord: %v", err)
	}
	if writes != 0 {
		t.Fatalf("值相同应幂等 no-op, 实际发了 %d 次写", writes)
	}
}

// --- TXT 记录(DNS-01)------------------------------------------------------

func TestBaiduEnsureTXTRecordConvergesToOne(t *testing.T) {
	var deleted []string
	var putBody map[string]any
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodGet:
			return baiduJSON(`{"records":[` +
				`{"id":"a1","rr":"_acme-challenge.app","type":"TXT","value":"old"},` +
				`{"id":"a2","rr":"_acme-challenge.app","type":"TXT","value":"stale"}]}`), nil
		case http.MethodDelete:
			deleted = append(deleted, r.URL.Path)
			return jsonResp(200, ``), nil
		case http.MethodPut:
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &putBody)
			return jsonResp(200, ``), nil
		case http.MethodPost:
			t.Fatalf("已有记录不应再添加")
			return nil, nil
		}
		t.Fatalf("未预期方法 %s", r.Method)
		return nil, nil
	})
	c := fixedBaiduClient(baiduTestAK+","+baiduTestSK, rt)
	ctx := context.Background()
	if err := c.EnsureTXTRecord(ctx, "example.com", "_acme-challenge.app.example.com", "digest-new"); err != nil {
		t.Fatalf("EnsureTXTRecord: %v", err)
	}
	// DNS-01 只允许一条 TXT:多余的那条必须删掉(留两条 CA 直接判失败)。
	if len(deleted) != 1 || !strings.HasSuffix(deleted[0], "/record/a2") {
		t.Fatalf("应删掉多余的第二条, got %v", deleted)
	}
	if putBody["value"] != "digest-new" || putBody["rr"] != "_acme-challenge.app" {
		t.Fatalf("首条应改成新值, got %v", putBody)
	}
}

func TestBaiduEnsureTXTRecordPaginates(t *testing.T) {
	// 分页:isTruncated + nextMarker 时必须翻完,否则第二页上的旧 TXT 会留在域里坑死签发。
	var markers []string
	var deleted []string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodGet:
			markers = append(markers, r.URL.Query().Get("marker"))
			if len(markers) == 1 {
				return baiduJSON(`{"records":[{"id":"p1a","rr":"_acme-challenge","type":"TXT","value":"digest"}],` +
					`"isTruncated":true,"nextMarker":"pg2"}`), nil
			}
			return baiduJSON(`{"records":[{"id":"p2a","rr":"_acme-challenge","type":"TXT","value":"stale"}]}`), nil
		case http.MethodDelete:
			deleted = append(deleted, r.URL.Path)
			return jsonResp(200, ``), nil
		case http.MethodPut, http.MethodPost:
			// 首条已是目标值 → 除清理第二页那条外不该再有写。
			t.Fatalf("值已相同时不应发 %s", r.Method)
			return nil, nil
		}
		t.Fatalf("未预期方法 %s", r.Method)
		return nil, nil
	})
	c := fixedBaiduClient(baiduTestAK+","+baiduTestSK, rt)
	if err := c.EnsureTXTRecord(context.Background(), "example.com", "_acme-challenge.example.com", "digest"); err != nil {
		t.Fatalf("EnsureTXTRecord: %v", err)
	}
	if len(markers) != 2 || markers[1] != "pg2" {
		t.Fatalf("第二次列表应带上 nextMarker, got %v", markers)
	}
	if len(deleted) != 1 || !strings.HasSuffix(deleted[0], "/record/p2a") {
		t.Fatalf("第二页那条旧值也要删, got %v", deleted)
	}
}

func TestBaiduApexUsesAt(t *testing.T) {
	var body map[string]any
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodGet:
			// 顶点已有记录:百度回的是 rr="@"。
			return baiduJSON(`{"records":[{"id":"apex1","rr":"@","type":"TXT","value":"digest"}]}`), nil
		case http.MethodPost:
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			return jsonResp(200, ``), nil
		}
		t.Fatalf("顶点值已相同时不应再发 %s", r.Method)
		return nil, nil
	})
	c := fixedBaiduClient(baiduTestAK+","+baiduTestSK, rt)
	if err := c.EnsureTXTRecord(context.Background(), "example.com", "example.com", "digest"); err != nil {
		t.Fatalf("EnsureTXTRecord(顶点): %v", err)
	}
	if body != nil {
		t.Fatalf("rr=@ 应被认成顶点那条记录, 实际又建了一条: %v", body)
	}

	// 空列表时顶点必须建 rr="@" 的记录(百度的顶点写法)。
	var postBody map[string]any
	rt2 := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet {
			return baiduJSON(`{"records":[]}`), nil
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &postBody)
		return jsonResp(200, ``), nil
	})
	c2 := fixedBaiduClient(baiduTestAK+","+baiduTestSK, rt2)
	if err := c2.EnsureTXTRecord(context.Background(), "example.com", "example.com", "digest"); err != nil {
		t.Fatalf("EnsureTXTRecord(顶点新建): %v", err)
	}
	if postBody["rr"] != "@" {
		t.Fatalf("顶点新建 rr 应为 @, got %v", postBody["rr"])
	}
}

func TestBaiduDeleteTXTRecordIdempotent(t *testing.T) {
	writes := 0
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet {
			return baiduJSON(`{"records":[]}`), nil
		}
		writes++
		return jsonResp(200, ``), nil
	})
	c := fixedBaiduClient(baiduTestAK+","+baiduTestSK, rt)
	if err := c.DeleteTXTRecord(context.Background(), "example.com", "_acme-challenge.example.com"); err != nil {
		t.Fatalf("DeleteTXTRecord: %v", err)
	}
	if writes != 0 {
		t.Fatalf("无记录应幂等, 实际发了 %d 次写", writes)
	}
}

// --- 走 service 的 TXT 通路(证书管理那条线)--------------------------------

func TestBaiduEnsureTXTViaService(t *testing.T) {
	ctx := context.Background()
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodGet:
			if strings.HasPrefix(r.URL.Path, "/v1/dns/zone/example.com/") {
				return baiduJSON(`{"records":[]}`), nil
			}
			return baiduJSON(`{"zones":[{"name":"example.com","status":"running"}]}`), nil
		default:
			return jsonResp(200, ``), nil
		}
	})
	svc := newTestService(t, stubVault{tokens: map[string]string{"cred-1": baiduTestAK + "," + baiduTestSK}}, &stubRouteCreator{}, dialDNS(rt))
	p, err := svc.Create(ctx, CreateInput{Type: TypeBaiduDNS, Name: "百度", CredentialID: "cred-1", BaseDomain: "example.com"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.EnsureTXTRecord(ctx, p.ID, "_acme-challenge.app.example.com", "digest"); err != nil {
		t.Fatalf("EnsureTXTRecord: %v", err)
	}
	if err := svc.EnsureTXTRecord(ctx, p.ID, "_acme-challenge.other.com", "digest"); !errors.Is(err, ErrTXTOutsideZone) {
		t.Fatalf("别的域应 ErrTXTOutsideZone, got %v", err)
	}
}
