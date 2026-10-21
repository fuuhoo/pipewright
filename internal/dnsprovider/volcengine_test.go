package dnsprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// --- 凭据解析 ---------------------------------------------------------------

func TestParseVolcCred(t *testing.T) {
	if _, _, err := parseVolcCred("no-comma"); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("无逗号应 ErrInvalidCredential, got %v", err)
	}
	if _, _, err := parseVolcCred(",sk"); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("空 ak 应 ErrInvalidCredential, got %v", err)
	}
	if _, _, err := parseVolcCred("ak,"); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("空 sk 应 ErrInvalidCredential, got %v", err)
	}
	ak, sk, err := parseVolcCred("  AKLTtestExampleExample , volcSKtestExample  ")
	if err != nil || ak != volcTestAK || sk != volcTestSK {
		t.Fatalf("应 trim 切分, got %q/%q/%v", ak, sk, err)
	}
}

// --- 签名已知向量(HMAC-SHA256,由官方 volc-sdk-golang 算出)------------------
// 向量来自 /tmp 里一个小程序直接调 volc-sdk-golang/base.GetSignRequest(火山 dns 产品用的就是它)。
// 两处最容易踩的坑都钉在这里:
//  1. 密钥链起点是**裸 SecretAccessKey**,不带 "VOLC" 前缀(与腾讯云 TC3 的 "TC3"+SK 不同);
//  2. 读用 GET(业务参数进查询串)、写用 POST(业务参数进实体),实体靠 x-content-sha256 参与签名。

const (
	volcTestAK = "AKLTtestExampleExample"
	volcTestSK = "volcSKtestExample"

	// 读请求(GET /?Action=ListZones&PageNumber=1&PageSize=100&Version=2018-08-01,空实体)。
	volcVectorList = "HMAC-SHA256 Credential=AKLTtestExampleExample/20260102/cn-beijing/dns/request, " +
		"SignedHeaders=content-type;host;x-content-sha256;x-date, Signature=3e29b4eb89937a62c09397e3fec0e8590e9678cb37161099adfe05b2f0bfb21f"
	// 写请求(POST /?Action=CreateRecord&Version=2018-08-01 + 下面那段实体)。
	volcVectorWrite = "HMAC-SHA256 Credential=AKLTtestExampleExample/20260102/cn-beijing/dns/request, " +
		"SignedHeaders=content-type;host;x-content-sha256;x-date, Signature=27482081009c2f5e16ce56435b13cbafa155bc61f8ac02c9b4c6d33f3920d214"

	// 两个实体哈希(SHA-256 of 空串 / of volcCreateBody)。
	volcHashEmpty = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	volcHashWrite = "35c4c06456df63e9dbf30a8b38ec4210d18c4a882c68e76e454aa76c9913db1a"

	// 钉死写向量用的请求体。键序即 Go json.Marshal(map[string]any) 的字典序,与实发字节必须一致。
	volcCreateBody = `{"Host":"app-abc","Line":"default","TTL":600,"Type":"A","Value":"203.0.113.5","ZID":123456}`
)

func TestVolcAuthorizationKnownVector(t *testing.T) {
	if got := sha256hex(nil); got != volcHashEmpty {
		t.Fatalf("空实体哈希不符: %q", got)
	}
	if got := sha256hex([]byte(volcCreateBody)); got != volcHashWrite {
		t.Fatalf("写实体哈希不符: %q", got)
	}

	got := volcSign(volcTestAK, volcTestSK, http.MethodGet, "dns.volcengineapi.com", "20260102", "20260102T030405Z",
		map[string]string{"Action": "ListZones", "Version": volcVersion, "PageNumber": "1", "PageSize": "100"}, nil)
	if got != volcVectorList {
		t.Fatalf("读请求签名不符官方 SDK 向量\n got=%q\nwant=%q", got, volcVectorList)
	}

	got = volcSign(volcTestAK, volcTestSK, http.MethodPost, "dns.volcengineapi.com", "20260102", "20260102T030405Z",
		map[string]string{"Action": "CreateRecord", "Version": volcVersion}, []byte(volcCreateBody))
	if got != volcVectorWrite {
		t.Fatalf("写请求签名不符官方 SDK 向量\n got=%q\nwant=%q", got, volcVectorWrite)
	}
}

// fixedVolcClient 造一个钉死 X-Date 的火山客户端(供签名与请求流断言)。
func fixedVolcClient(cred string, rt http.RoundTripper) *volcengineClient {
	c := newVolcengineClient(cred, rt, "")
	c.nowFn = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }
	return c
}

// volcAction 取 RPC 动作名(公共参数与业务参数分开,断言全靠它分流)。
func volcAction(r *http.Request) string { return r.URL.Query().Get("Action") }

// volcBody 读出 POST 实体并解成 map,供逐字段断言。
func volcBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("读请求体: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("实体非 JSON: %q", raw)
	}
	return m
}

const volcOneZone = `{"ResponseMetadata":{"RequestId":"z"},"Result":{"Zones":[{"ZID":123456,"ZoneName":"example.com"}]}}`

// --- ListZones --------------------------------------------------------------

func TestVolcListZonesSuccess(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet {
			t.Fatalf("查域名列表应 GET, got %s", r.Method)
		}
		if volcAction(r) != "ListZones" || r.URL.Query().Get("Version") != volcVersion {
			t.Fatalf("应带 Action=ListZones&Version=%s, got %q", volcVersion, r.URL.RawQuery)
		}
		if r.URL.Query().Get("PageSize") != "100" || r.URL.Query().Get("PageNumber") != "1" {
			t.Fatalf("列表应带分页参数, got %q", r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") != volcVectorList {
			t.Fatalf("Authorization 不符官方 SDK 向量: %q", r.Header.Get("Authorization"))
		}
		// 签名过的两个头,实发必须一字不差,否则服务端按收到的头重算就验不过。
		if r.Header.Get("X-Date") != "20260102T030405Z" {
			t.Fatalf("X-Date 应与签名时刻一致, got %q", r.Header.Get("X-Date"))
		}
		if r.Header.Get("X-Content-Sha256") != volcHashEmpty {
			t.Fatalf("GET 无实体,X-Content-Sha256 应是空串哈希, got %q", r.Header.Get("X-Content-Sha256"))
		}
		return jsonResp(200, `{"ResponseMetadata":{"RequestId":"z"},"Result":{"Zones":[{"ZID":123456,"ZoneName":"Example.COM"},{"ZID":1,"ZoneName":"sub.example.com"}]}}`), nil
	})
	c := fixedVolcClient(volcTestAK+","+volcTestSK, rt)
	zones, err := c.ListZones(context.Background())
	if err != nil {
		t.Fatalf("ListZones: %v", err)
	}
	if strings.Join(zones, " ") != "example.com sub.example.com" {
		t.Fatalf("应归一排序, got %v", zones)
	}
}

func TestVolcListZonesEmpty(t *testing.T) {
	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return jsonResp(200, `{"Result":{"Zones":[]}}`), nil
	})
	c := fixedVolcClient(volcTestAK+","+volcTestSK, rt)
	zones, err := c.ListZones(context.Background())
	if err != nil || len(zones) != 0 {
		t.Fatalf("账号无域名应回空清单, got %v / %v", zones, err)
	}
}

// 火山的业务错误可能藏在 HTTP 200 的信封里,只判状态码会把失败当成功。
func TestVolcBusinessErrorInsideHttp200(t *testing.T) {
	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return jsonResp(200, `{"ResponseMetadata":{"Error":{"Code":"ZoneNotExists","Message":"zone not found"}}}`), nil
	})
	c := fixedVolcClient(volcTestAK+","+volcTestSK, rt)
	_, err := c.ListZones(context.Background())
	if !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("信封里的错误也必须上抛, got %v", err)
	}
	if !strings.Contains(err.Error(), "域名不存在") {
		t.Fatalf("应点名域名问题, got %v", err)
	}
}

func TestVolcListZonesAuthFail(t *testing.T) {
	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return jsonResp(401, `{"ResponseMetadata":{"Error":{"Code":"SignatureDoesNotMatch","Message":"The signature does not match."}}}`), nil
	})
	c := fixedVolcClient(volcTestAK+",wrong-sk", rt)
	_, err := c.ListZones(context.Background())
	if !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("验签失败应 ErrVerifyFailed, got %v", err)
	}
	if !strings.Contains(err.Error(), "凭据无效或无权限") {
		t.Fatalf("应点名凭据问题, got %v", err)
	}
	if strings.Contains(err.Error(), "wrong-sk") || strings.Contains(err.Error(), volcTestAK) {
		t.Fatalf("错误文本不应含凭据: %v", err)
	}
}

// 网络层错误原始文本可能带 URL/签名,一律换成固定人话。
func TestVolcTransportErrorLeaksNothing(t *testing.T) {
	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("dial tcp: lookup failed for %s", volcTestSK)
	})
	c := fixedVolcClient(volcTestAK+","+volcTestSK, rt)
	_, err := c.ListZones(context.Background())
	if !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("连不上应 ErrVerifyFailed, got %v", err)
	}
	if strings.Contains(err.Error(), volcTestSK) || strings.Contains(err.Error(), volcTestAK) ||
		strings.Contains(err.Error(), "Signature") || strings.Contains(err.Error(), "dns.volcengineapi.com") {
		t.Fatalf("错误文本不该带任何凭据/签名/内部 URL: %v", err)
	}
}

func TestVolcInvalidCredentialSendsNothing(t *testing.T) {
	called := false
	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		called = true
		return jsonResp(200, `{}`), nil
	})
	c := fixedVolcClient("no-comma", rt)
	if _, err := c.ListZones(context.Background()); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("坏凭据应 ErrInvalidCredential, got %v", err)
	}
	if called {
		t.Fatalf("坏凭据不应发任何请求")
	}
}

// --- 翻页(火山没有「按域名查单个 zone 详情」的读接口,只能列全量)--------------

// volcFillerZones 造 n 条域名,把列表撑满一页以触发翻页。
func volcFillerZones(n int) string {
	parts := make([]string, 0, n)
	for i := 0; i < n; i++ {
		parts = append(parts, `{"ZID":`+strconv.Itoa(9000+i)+`,"ZoneName":"filler`+strconv.Itoa(i)+`.com"}`)
	}
	return strings.Join(parts, ",")
}

func TestVolcListZonesPages(t *testing.T) {
	var pages []string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		p := r.URL.Query().Get("PageNumber")
		pages = append(pages, p)
		switch p {
		case "1":
			return jsonResp(200, `{"Result":{"Zones":[`+volcFillerZones(100)+`]}}`), nil
		case "2":
			return jsonResp(200, volcOneZone), nil
		}
		t.Fatalf("第二页不足 100 条就该停, 不该再请求第 %s 页", p)
		return nil, nil
	})
	c := fixedVolcClient(volcTestAK+","+volcTestSK, rt)
	zones, err := c.ListZones(context.Background())
	if err != nil {
		t.Fatalf("ListZones: %v", err)
	}
	if len(pages) != 2 || pages[0] != "1" || pages[1] != "2" {
		t.Fatalf("应逐页翻到不足一整页, got %v", pages)
	}
	if len(zones) != 101 || zones[0] != "example.com" {
		t.Fatalf("应攒满两页共 101 个域并排序, got %d 个", len(zones))
	}
}

// --- A 记录 -----------------------------------------------------------------

func TestVolcEnsureARecordCreate(t *testing.T) {
	var created map[string]any
	var createReq *http.Request
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch volcAction(r) {
		case "ListZones":
			return jsonResp(200, volcOneZone), nil
		case "ListRecords":
			if r.URL.Query().Get("ZID") != "123456" {
				t.Fatalf("列表应带解析出的数字 ZID, got %q", r.URL.Query().Get("ZID"))
			}
			return jsonResp(200, `{"Result":{"Records":[]}}`), nil
		case "CreateRecord":
			created = volcBody(t, r)
			createReq = r
			return jsonResp(200, `{"Result":{"RecordID":"rec-1"}}`), nil
		}
		t.Fatalf("未预期 Action %q", volcAction(r))
		return nil, nil
	})
	c := fixedVolcClient(volcTestAK+","+volcTestSK, rt)
	if err := c.EnsureARecord(context.Background(), "example.com", "app-abc.example.com", "203.0.113.5"); err != nil {
		t.Fatalf("EnsureARecord: %v", err)
	}
	// 实体要正好等于钉死向量算出来的那段字节:键序、数字形态差一个字符就验签失败。
	raw, _ := json.Marshal(created)
	if string(raw) != volcCreateBody {
		t.Fatalf("建记录实体与签名的实体不一致\n got=%s\nwant=%s", raw, volcCreateBody)
	}
	if createReq.Method != http.MethodPost {
		t.Fatalf("写操作应 POST, got %s", createReq.Method)
	}
	if createReq.Header.Get("Authorization") != volcVectorWrite {
		t.Fatalf("写请求 Authorization 不符官方 SDK 向量: %q", createReq.Header.Get("Authorization"))
	}
	if createReq.Header.Get("X-Content-Sha256") != volcHashWrite {
		t.Fatalf("X-Content-Sha256 应是实体哈希, got %q", createReq.Header.Get("X-Content-Sha256"))
	}
}

func TestVolcEnsureARecordUpdate(t *testing.T) {
	var updated map[string]any
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch volcAction(r) {
		case "ListZones":
			return jsonResp(200, volcOneZone), nil
		case "ListRecords":
			return jsonResp(200, `{"Result":{"Records":[{"RecordID":"rec-7","Host":"app-abc","Type":"A","Value":"1.2.3.4"}]}}`), nil
		case "UpdateRecord":
			updated = volcBody(t, r)
			return jsonResp(200, `{"Result":{}}`), nil
		case "CreateRecord":
			t.Fatalf("已有记录不应再 Create")
			return nil, nil
		}
		t.Fatalf("未预期 Action %q", volcAction(r))
		return nil, nil
	})
	c := fixedVolcClient(volcTestAK+","+volcTestSK, rt)
	if err := c.EnsureARecord(context.Background(), "example.com", "app-abc.example.com", "203.0.113.9"); err != nil {
		t.Fatalf("EnsureARecord: %v", err)
	}
	if updated["RecordID"] != "rec-7" || updated["Value"] != "203.0.113.9" || updated["ZID"] != float64(123456) {
		t.Fatalf("UpdateRecord 应改既有 RecordID 并带 ZID, got %v", updated)
	}
}

func TestVolcEnsureARecordNoOp(t *testing.T) {
	writes := 0
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch volcAction(r) {
		case "ListZones":
			return jsonResp(200, volcOneZone), nil
		case "ListRecords":
			return jsonResp(200, `{"Result":{"Records":[{"RecordID":"rec-7","Host":"app-abc","Type":"A","Value":"203.0.113.5"}]}}`), nil
		default:
			writes++
			return jsonResp(200, `{}`), nil
		}
	})
	c := fixedVolcClient(volcTestAK+","+volcTestSK, rt)
	if err := c.EnsureARecord(context.Background(), "example.com", "app-abc.example.com", "203.0.113.5"); err != nil {
		t.Fatalf("EnsureARecord: %v", err)
	}
	if writes != 0 {
		t.Fatalf("值相同应幂等 no-op, 实际发了 %d 次写", writes)
	}
}

// --- TXT 记录(DNS-01)------------------------------------------------------

func TestVolcEnsureTXTRecordConvergesToOne(t *testing.T) {
	var deleted, updated []map[string]any
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch volcAction(r) {
		case "ListZones":
			return jsonResp(200, volcOneZone), nil
		case "ListRecords":
			return jsonResp(200, `{"Result":{"Records":[`+
				`{"RecordID":"t1","Host":"_acme-challenge.app","Type":"TXT","Value":"old"},`+
				`{"RecordID":"t2","Host":"_acme-challenge.app","Type":"TXT","Value":"stale"}]}}`), nil
		case "DeleteRecord":
			deleted = append(deleted, volcBody(t, r))
			return jsonResp(200, `{"Result":{}}`), nil
		case "UpdateRecord":
			updated = append(updated, volcBody(t, r))
			return jsonResp(200, `{"Result":{}}`), nil
		case "CreateRecord":
			t.Fatalf("已有记录不应 Create")
			return nil, nil
		}
		t.Fatalf("未预期 Action %q", volcAction(r))
		return nil, nil
	})
	c := fixedVolcClient(volcTestAK+","+volcTestSK, rt)
	ctx := context.Background()
	if err := c.EnsureTXTRecord(ctx, "example.com", "_acme-challenge.app.example.com", "digest-new"); err != nil {
		t.Fatalf("EnsureTXTRecord: %v", err)
	}
	// 留两条 TXT 会让 CA 直接判 DNS-01 失败,多余那条必须删。
	if len(deleted) != 1 || deleted[0]["RecordID"] != "t2" {
		t.Fatalf("应删掉多余的第二条, got %v", deleted)
	}
	if len(updated) != 1 || updated[0]["Value"] != "digest-new" || updated[0]["RecordID"] != "t1" {
		t.Fatalf("首条应改成新值, got %v", updated)
	}
}

// 火山列表按「本页不满一页即到底」收敛;旧 TXT 落在第二页时,只翻一页就把它漏在域里。
func TestVolcRecordsPaginatesAndConverges(t *testing.T) {
	var pages []string
	writes := 0
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch volcAction(r) {
		case "ListZones":
			return jsonResp(200, volcOneZone), nil
		case "ListRecords":
			p := r.URL.Query().Get("PageNumber")
			pages = append(pages, p)
			if p == "1" {
				return jsonResp(200, `{"Result":{"Records":[`+volcFillerRecords(100)+`]}}`), nil
			}
			return jsonResp(200, `{"Result":{"Records":[{"RecordID":"t1","Host":"_acme-challenge","Type":"TXT","Value":"digest"}]}}`), nil
		default:
			writes++
			return jsonResp(200, `{}`), nil
		}
	})
	c := fixedVolcClient(volcTestAK+","+volcTestSK, rt)
	if err := c.EnsureTXTRecord(context.Background(), "example.com", "_acme-challenge.example.com", "digest"); err != nil {
		t.Fatalf("EnsureTXTRecord: %v", err)
	}
	if len(pages) != 2 || pages[1] != "2" {
		t.Fatalf("应翻满分页, got %v", pages)
	}
	if writes != 0 {
		t.Fatalf("值已是目标时不该有任何写, 实际 %d 次", writes)
	}
}

func volcFillerRecords(n int) string {
	parts := make([]string, 0, n)
	for i := 0; i < n; i++ {
		parts = append(parts, `{"RecordID":"f`+strconv.Itoa(i)+`","Host":"filler`+strconv.Itoa(i)+`","Type":"A","Value":"1.0.0.`+strconv.Itoa(i%256)+`"}`)
	}
	return strings.Join(parts, ",")
}

func TestVolcApexUsesAt(t *testing.T) {
	var created map[string]any
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch volcAction(r) {
		case "ListZones":
			return jsonResp(200, volcOneZone), nil
		case "ListRecords":
			// 顶点已有记录:Host 回 "@"(火山用 @ 表顶点)。
			return jsonResp(200, `{"Result":{"Records":[{"RecordID":"apex1","Host":"@","Type":"TXT","Value":"digest"}]}}`), nil
		case "CreateRecord":
			created = volcBody(t, r)
			return jsonResp(200, `{"Result":{}}`), nil
		}
		t.Fatalf("顶点值已相同时不应再发 %q", volcAction(r))
		return nil, nil
	})
	c := fixedVolcClient(volcTestAK+","+volcTestSK, rt)
	if err := c.EnsureTXTRecord(context.Background(), "example.com", "example.com", "digest"); err != nil {
		t.Fatalf("EnsureTXTRecord(顶点): %v", err)
	}
	if created != nil {
		t.Fatalf("Host=@ 应被认成顶点那条记录, 实际又建了一条: %v", created)
	}

	// 域里没记录时,顶点新建必须发 Host="@"。
	var body map[string]any
	rt2 := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch volcAction(r) {
		case "ListZones":
			return jsonResp(200, volcOneZone), nil
		case "ListRecords":
			return jsonResp(200, `{"Result":{"Records":[]}}`), nil
		default:
			body = volcBody(t, r)
			return jsonResp(200, `{"Result":{}}`), nil
		}
	})
	c2 := fixedVolcClient(volcTestAK+","+volcTestSK, rt2)
	if err := c2.EnsureTXTRecord(context.Background(), "example.com", "example.com", "digest"); err != nil {
		t.Fatalf("EnsureTXTRecord(顶点新建): %v", err)
	}
	if body["Host"] != "@" {
		t.Fatalf("顶点新建 Host 应为 @, got %v", body["Host"])
	}
}

// 删除要清干净同名的每一条,只删第一条会留下旧 TXT 卡住下一次签发。
func TestVolcDeleteTXTRecordRemovesAll(t *testing.T) {
	var ids []string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch volcAction(r) {
		case "ListZones":
			return jsonResp(200, volcOneZone), nil
		case "ListRecords":
			return jsonResp(200, `{"Result":{"Records":[`+
				`{"RecordID":"t1","Host":"_acme-challenge","Type":"TXT","Value":"a"},`+
				`{"RecordID":"t2","Host":"_acme-challenge","Type":"TXT","Value":"b"}]}}`), nil
		case "DeleteRecord":
			b := volcBody(t, r)
			ids = append(ids, fmt.Sprint(b["RecordID"]))
			return jsonResp(200, `{"Result":{}}`), nil
		}
		t.Fatalf("未预期 Action %q", volcAction(r))
		return nil, nil
	})
	c := fixedVolcClient(volcTestAK+","+volcTestSK, rt)
	if err := c.DeleteTXTRecord(context.Background(), "example.com", "_acme-challenge.example.com"); err != nil {
		t.Fatalf("DeleteTXTRecord: %v", err)
	}
	if strings.Join(ids, ",") != "t1,t2" {
		t.Fatalf("两条都应删, got %v", ids)
	}
}

func TestVolcDeleteARecordIdempotent(t *testing.T) {
	writes := 0
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch volcAction(r) {
		case "ListZones":
			return jsonResp(200, volcOneZone), nil
		case "ListRecords":
			return jsonResp(200, `{"Result":{"Records":[]}}`), nil
		default:
			writes++
			return jsonResp(200, `{}`), nil
		}
	})
	c := fixedVolcClient(volcTestAK+","+volcTestSK, rt)
	if err := c.DeleteARecord(context.Background(), "example.com", "app-abc.example.com"); err != nil {
		t.Fatalf("DeleteARecord: %v", err)
	}
	if writes != 0 {
		t.Fatalf("无记录应幂等, 实际发了 %d 次写", writes)
	}
}

// --- 走 service 的 TXT 通路(证书管理那条线)--------------------------------

func TestVolcEnsureTXTViaService(t *testing.T) {
	ctx := context.Background()
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch volcAction(r) {
		case "ListZones":
			return jsonResp(200, volcOneZone), nil
		case "ListRecords":
			return jsonResp(200, `{"Result":{"Records":[]}}`), nil
		default:
			return jsonResp(200, `{"Result":{"RecordID":"rec-9"}}`), nil
		}
	})
	svc := newTestService(t, stubVault{tokens: map[string]string{"cred-1": volcTestAK + "," + volcTestSK}}, &stubRouteCreator{}, dialDNS(rt))
	p, err := svc.Create(ctx, CreateInput{Type: TypeVolcengine, Name: "火山", CredentialID: "cred-1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.EnsureTXTRecord(ctx, p.ID, "_acme-challenge.app.example.com", "digest"); err != nil {
		t.Fatalf("EnsureTXTRecord: %v", err)
	}
	// 越界域名必须硬失败(凭据只能管账号清单里那些域)。
	if err := svc.EnsureTXTRecord(ctx, p.ID, "_acme-challenge.other.com", "digest"); !errors.Is(err, ErrNoHostedZone) {
		t.Fatalf("别的域应 ErrNoHostedZone, got %v", err)
	}
}
