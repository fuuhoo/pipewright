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

func TestParseTencentCred(t *testing.T) {
	if _, _, err := parseTencentCred("no-comma"); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("无逗号应 ErrInvalidCredential, got %v", err)
	}
	if _, _, err := parseTencentCred(",key"); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("空 SecretId 应 ErrInvalidCredential, got %v", err)
	}
	if _, _, err := parseTencentCred("id,"); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("空 SecretKey 应 ErrInvalidCredential, got %v", err)
	}
	id, key, err := parseTencentCred("  AKID123 , secretVal  ")
	if err != nil || id != "AKID123" || key != "secretVal" {
		t.Fatalf("应 trim 切分, got %q/%q/%v", id, key, err)
	}
}

// --- 签名已知向量(TC3-HMAC-SHA256,离线独立实现算出)--------------------------
// 任何一处编码/排序/密钥链偏差都会让此用例红。固定输入见 tcTestBody。

const tcTestBody = `{"Domain":"example.com","RecordLine":"默认","RecordType":"A","SubDomain":"app-abc","Value":"203.0.113.5"}`

func TestTC3AuthorizationKnownVector(t *testing.T) {
	got := tc3Authorization("AKIDtestExampleExampleExample000", "testSecretKeyForVector",
		"dnspod.tencentcloudapi.com", 1767323045, []byte(tcTestBody))
	const want = "TC3-HMAC-SHA256 Credential=AKIDtestExampleExampleExample000/2026-01-02/dnspod/tc3_request, " +
		"SignedHeaders=content-type;host, Signature=b334e0ce13624a9a1f6e827ed1c64049679c564e8fb029d50f32878d19cbc350"
	if got != want {
		t.Fatalf("签名不符已知向量\n got=%q\nwant=%q", got, want)
	}
}

// fixedTencentClient 造一个钉死时间戳的腾讯云客户端(供签名与请求流断言)。
func fixedTencentClient(cred string, rt http.RoundTripper) *tencentCloudClient {
	c := newTencentCloudClient(cred, rt, "")
	c.nowFn = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }
	return c
}

// tcBody 读出请求体并解成 map,供逐字段断言(3.0 的参数全在 JSON 实体里)。
func tcBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("读请求体: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("请求体非 JSON: %q", raw)
	}
	return m
}

// tcAction 读 X-TC-Action(公共参数不进实体,靠头带)。
func tcAction(r *http.Request) string { return r.Header.Get("X-TC-Action") }

// --- VerifyZone -------------------------------------------------------------

func TestTencentVerifyZoneSuccess(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost {
			t.Fatalf("3.0 应全用 POST, got %s", r.Method)
		}
		if tcAction(r) != "DescribeDomain" {
			t.Fatalf("应 X-TC-Action=DescribeDomain, got %q", tcAction(r))
		}
		if r.Header.Get("X-TC-Version") != tencentVersion {
			t.Fatalf("应带 X-TC-Version=%s, got %q", tencentVersion, r.Header.Get("X-TC-Version"))
		}
		if r.Header.Get("X-TC-Timestamp") != "1767323045" {
			t.Fatalf("X-TC-Timestamp 应等于固定时刻的 Unix 秒, got %q", r.Header.Get("X-TC-Timestamp"))
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, tencentAlgorithm+" Credential=") || !strings.Contains(auth, "SignedHeaders=content-type;host, Signature=") {
			t.Fatalf("Authorization 形态不符: %q", auth)
		}
		if !strings.Contains(auth, "/"+tencentService+"/tc3_request") {
			t.Fatalf("CredentialScope 应含 service/tc3_request: %q", auth)
		}
		if got := tcBody(t, r)["Domain"]; got != "example.com" {
			t.Fatalf("应带 Domain, got %v", got)
		}
		return jsonResp(200, `{"Response":{"RequestId":"r1"}}`), nil
	})
	c := fixedTencentClient("AKIDid,secretKey", rt)
	if err := c.VerifyZone(context.Background(), "example.com"); err != nil {
		t.Fatalf("VerifyZone: %v", err)
	}
}

func TestTencentVerifyZoneAuthFail(t *testing.T) {
	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		// 3.0 出错时 HTTP 仍是 200,错误在 Response.Error 里。
		return jsonResp(200, `{"Response":{"Error":{"Code":"AuthFailure.SecretIdNotFound","Message":"The SecretId is not found."}}}`), nil
	})
	c := fixedTencentClient("AKIDid,wrong-key", rt)
	err := c.VerifyZone(context.Background(), "example.com")
	if !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("鉴权失败应 ErrVerifyFailed, got %v", err)
	}
	if !strings.Contains(err.Error(), "凭据无效或无权限") {
		t.Fatalf("应给可行动的人话, got %v", err)
	}
	if strings.Contains(err.Error(), "wrong-key") || strings.Contains(err.Error(), "AKIDid") {
		t.Fatalf("错误文本不应含凭据: %v", err)
	}
}

func TestTencentVerifyZoneDomainNotOwned(t *testing.T) {
	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return jsonResp(200, `{"Response":{"Error":{"Code":"FailedOperation.NotDomainOwner","Message":"not owner"}}}`), nil
	})
	c := fixedTencentClient("AKIDid,secretKey", rt)
	if err := c.VerifyZone(context.Background(), "nope.com"); !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("非本人域名应 ErrVerifyFailed, got %v", err)
	}
}

func TestTencentInvalidCredentialSendsNothing(t *testing.T) {
	called := false
	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		called = true
		return jsonResp(200, `{}`), nil
	})
	c := fixedTencentClient("no-comma", rt)
	if err := c.VerifyZone(context.Background(), "example.com"); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("坏凭据应 ErrInvalidCredential, got %v", err)
	}
	if called {
		t.Fatalf("坏凭据不应发任何请求")
	}
}

// --- A 记录 -----------------------------------------------------------------

func TestTencentEnsureARecordCreate(t *testing.T) {
	var created map[string]any
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch tcAction(r) {
		case "DescribeRecordList":
			body := tcBody(t, r)
			if body["SubDomain"] != "app-abc" || body["RecordType"] != "A" {
				t.Fatalf("列表应带 SubDomain/RecordType, got %v", body)
			}
			if body["ErrorOnEmpty"] != "no" {
				t.Fatalf("应带 ErrorOnEmpty=no(否则无记录会报错), got %v", body["ErrorOnEmpty"])
			}
			return jsonResp(200, `{"Response":{"RecordList":[]}}`), nil
		case "CreateRecord":
			created = tcBody(t, r)
			return jsonResp(200, `{"Response":{"RecordId":9001}}`), nil
		}
		t.Fatalf("未预期 Action %q", tcAction(r))
		return nil, nil
	})
	c := fixedTencentClient("AKIDid,secretKey", rt)
	if err := c.EnsureARecord(context.Background(), "example.com", "app-abc.example.com", "203.0.113.5"); err != nil {
		t.Fatalf("EnsureARecord: %v", err)
	}
	if created["Value"] != "203.0.113.5" || created["RecordLine"] != tencentDefaultLine || created["Domain"] != "example.com" {
		t.Fatalf("CreateRecord 字段不符: %v", created)
	}
	if _, ok := created["RecordId"]; ok {
		t.Fatalf("新建不该带 RecordId: %v", created)
	}
}

func TestTencentEnsureARecordUpdateAndNoOp(t *testing.T) {
	list := `{"Response":{"RecordList":[{"RecordId":7,"Name":"app-abc","Type":"A","Value":"1.2.3.4"}]}}`
	var modified map[string]any
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch tcAction(r) {
		case "DescribeRecordList":
			return jsonResp(200, list), nil
		case "ModifyRecord":
			modified = tcBody(t, r)
			return jsonResp(200, `{"Response":{"RecordId":7}}`), nil
		case "CreateRecord":
			t.Fatalf("已有记录不应再 Create")
			return nil, nil
		}
		t.Fatalf("未预期 Action %q", tcAction(r))
		return nil, nil
	})
	c := fixedTencentClient("AKIDid,secretKey", rt)
	if err := c.EnsureARecord(context.Background(), "example.com", "app-abc.example.com", "203.0.113.9"); err != nil {
		t.Fatalf("EnsureARecord: %v", err)
	}
	if modified["RecordId"] != float64(7) || modified["Value"] != "203.0.113.9" {
		t.Fatalf("ModifyRecord 应改既有 RecordId=7, got %v", modified)
	}

	// 值已是目标 → 一次列表之外不发任何写请求。
	writes := 0
	rtNoop := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch tcAction(r) {
		case "DescribeRecordList":
			return jsonResp(200, `{"Response":{"RecordList":[{"RecordId":7,"Name":"app-abc","Type":"A","Value":"203.0.113.9"}]}}`), nil
		default:
			writes++
			return jsonResp(200, `{}`), nil
		}
	})
	c2 := fixedTencentClient("AKIDid,secretKey", rtNoop)
	if err := c2.EnsureARecord(context.Background(), "example.com", "app-abc.example.com", "203.0.113.9"); err != nil {
		t.Fatalf("EnsureARecord(no-op): %v", err)
	}
	if writes != 0 {
		t.Fatalf("值相同应幂等 no-op, 实际发了 %d 次写", writes)
	}
}

// --- TXT 记录(DNS-01)------------------------------------------------------

func TestTencentEnsureTXTRecordConvergesToOne(t *testing.T) {
	var deleted, modified []map[string]any
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch tcAction(r) {
		case "DescribeRecordList":
			return jsonResp(200, `{"Response":{"RecordList":[`+
				`{"RecordId":11,"Name":"_acme-challenge.app","Type":"TXT","Value":"old"},`+
				`{"RecordId":12,"Name":"_acme-challenge.app","Type":"TXT","Value":"stale"}]}}`), nil
		case "DeleteRecord":
			deleted = append(deleted, tcBody(t, r))
			return jsonResp(200, `{"Response":{}}`), nil
		case "ModifyRecord":
			modified = append(modified, tcBody(t, r))
			return jsonResp(200, `{"Response":{}}`), nil
		case "CreateRecord":
			t.Fatalf("已有记录不应 Create")
			return nil, nil
		}
		t.Fatalf("未预期 Action %q", tcAction(r))
		return nil, nil
	})
	c := fixedTencentClient("AKIDid,secretKey", rt)
	ctx := context.Background()
	if err := c.EnsureTXTRecord(ctx, "example.com", "_acme-challenge.app.example.com", "digest-new"); err != nil {
		t.Fatalf("EnsureTXTRecord: %v", err)
	}
	if len(deleted) != 1 || deleted[0]["RecordId"] != float64(12) {
		t.Fatalf("多余的第二条应删掉, got %v", deleted)
	}
	if len(modified) != 1 || modified[0]["Value"] != "digest-new" || modified[0]["RecordId"] != float64(11) {
		t.Fatalf("首条应改成新值, got %v", modified)
	}
	if modified[0]["SubDomain"] != "_acme-challenge.app" {
		t.Fatalf("SubDomain 应是左半部分, got %v", modified[0]["SubDomain"])
	}
}

func TestTencentEnsureTXTRecordSkipsModifyWhenSame(t *testing.T) {
	writes := []string{}
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch tcAction(r) {
		case "DescribeRecordList":
			// 回显带外层引号(部分 CA 场景见过),归一后应视为已相同。
			return jsonResp(200, `{"Response":{"RecordList":[{"RecordId":11,"Name":"_acme-challenge","Type":"TXT","Value":"\"digest\""}]}}`), nil
		default:
			writes = append(writes, tcAction(r))
			return jsonResp(200, `{}`), nil
		}
	})
	c := fixedTencentClient("AKIDid,secretKey", rt)
	if err := c.EnsureTXTRecord(context.Background(), "example.com", "_acme-challenge.example.com", "digest"); err != nil {
		t.Fatalf("EnsureTXTRecord: %v", err)
	}
	if len(writes) != 0 {
		t.Fatalf("值相同(仅引号差)不应发写请求, got %v", writes)
	}
}

func TestTencentDeleteTXTRecordIdempotent(t *testing.T) {
	writes := 0
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch tcAction(r) {
		case "DescribeRecordList":
			return jsonResp(200, `{"Response":{"RecordList":[]}}`), nil
		default:
			writes++
			return jsonResp(200, `{}`), nil
		}
	})
	c := fixedTencentClient("AKIDid,secretKey", rt)
	if err := c.DeleteTXTRecord(context.Background(), "example.com", "_acme-challenge.example.com"); err != nil {
		t.Fatalf("DeleteTXTRecord: %v", err)
	}
	if writes != 0 {
		t.Fatalf("无记录应幂等, 实际发了 %d 次写", writes)
	}
}

func TestTencentApexUsesAtAndMatchesEmptyName(t *testing.T) {
	var createSub any
	var looked []map[string]any
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch tcAction(r) {
		case "DescribeRecordList":
			b := tcBody(t, r)
			looked = append(looked, b)
			// 顶点已有记录,Name 回空串(3.0 的一种真实形态)。
			return jsonResp(200, `{"Response":{"RecordList":[{"RecordId":5,"Name":"","Type":"TXT","Value":"digest"}]}}`), nil
		case "CreateRecord":
			createSub = tcBody(t, r)["SubDomain"]
			return jsonResp(200, `{"Response":{}}`), nil
		default:
			t.Fatalf("顶点值已相同时不应再发 %q", tcAction(r))
			return nil, nil
		}
	})
	c := fixedTencentClient("AKIDid,secretKey", rt)
	ctx := context.Background()
	if err := c.EnsureTXTRecord(ctx, "example.com", "example.com", "digest"); err != nil {
		t.Fatalf("EnsureTXTRecord(顶点): %v", err)
	}
	if len(looked) != 1 {
		t.Fatalf("顶点查询应不带 SubDomain 过滤, got %v", looked)
	}
	if _, ok := looked[0]["SubDomain"]; ok {
		t.Fatalf("顶点不应把 @ 当 SubDomain 发出去: %v", looked[0])
	}
	if createSub != nil {
		t.Fatalf("Name 空串应被认成顶点记录(@ 等价), 实际又建了一条 SubDomain=%v", createSub)
	}
}

// --- 走 service 的 TXT 通路(证书管理那条线)--------------------------------

func TestTencentEnsureTXTViaService(t *testing.T) {
	ctx := context.Background()
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch tcAction(r) {
		case "DescribeDomain":
			return jsonResp(200, `{"Response":{"RequestId":"v"}}`), nil
		case "DescribeRecordList":
			return jsonResp(200, `{"Response":{"RecordList":[]}}`), nil
		case "CreateRecord":
			return jsonResp(200, `{"Response":{"RecordId":1}}`), nil
		case "DeleteRecord":
			return jsonResp(200, `{"Response":{}}`), nil
		}
		t.Fatalf("未预期 Action %q", tcAction(r))
		return nil, nil
	})
	svc := newTestService(t, stubVault{tokens: map[string]string{"cred-1": "AKIDid,secretKey"}}, &stubRouteCreator{}, dialDNS(rt))
	p, err := svc.Create(ctx, CreateInput{Type: TypeTencentCloud, Name: "腾讯", CredentialID: "cred-1", BaseDomain: "example.com"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.EnsureTXTRecord(ctx, p.ID, "_acme-challenge.app.example.com", "digest"); err != nil {
		t.Fatalf("EnsureTXTRecord: %v", err)
	}
	// 越界域名必须硬失败(凭据只能管登记时声明的那个 zone)。
	if err := svc.EnsureTXTRecord(ctx, p.ID, "_acme-challenge.other.com", "digest"); !errors.Is(err, ErrTXTOutsideZone) {
		t.Fatalf("别的域应 ErrTXTOutsideZone, got %v", err)
	}
}
