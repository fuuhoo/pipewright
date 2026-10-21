package dnsprovider

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// baiduAPIBase 是百度智能云「智能云解析 DNS」(公网 DNS)服务域名。测试经 RoundTripper 拦截,不触真网络。
// 公网 DNS 是全局产品:服务域名不带 region,域名一律**按名字**寻址(zone/{zoneName}/…),不按 id。
const baiduAPIBase = "https://dns.baidubce.com"

// 百度 BCE 签名(bce-auth-v1)固定值。
const (
	// baiduAlgorithm 是认证串前缀的字面量。
	baiduAlgorithm = "bce-auth-v1"
	// baiduExpiration 是签名有效期(秒):时间戳与它都进 authStringPrefix,改了会验签失败。
	baiduExpiration = 3600
	// baiduDefaultLine 是「default」线路(百度线路取值是小写英文,与腾讯的「默认」不同)。
	baiduDefaultLine = "default"
	// baiduDefaultTTL 建记录时给的秒数。
	baiduDefaultTTL = 600
	// baiduSignedHeaders 参与签名的头:百度只要求 host + x-bce-date;签得越少越不易与实发头不一致。
	baiduSignedHeaders = "content-type;host;x-bce-date"
	// baiduContentType 同时进签名与实际请求头,必须一致。
	baiduContentType = "application/json; charset=utf-8"
)

// 凭据约定(vault 单字串存储模型不变):百度智能云用 IAM 的 "AccessKeyID,SecretAccessKey"。
// 按**第一个逗号**切分,两侧 trim;无逗号 / 任一段空 → ErrInvalidCredential。
// 绝不把 ak/sk 任何片段回显到错误/日志/DTO。
type baiduDNSClient struct {
	cred string // 形如 "AccessKeyID,SecretAccessKey";仅进程内,不入库/日志/响应
	hc   *http.Client
	base string

	// 以下供测试注入,钉死签名(生产为 nil → 用 time.Now)。
	nowFn func() time.Time
}

// newBaiduDNSClient 构造百度智能云 DNS 客户端。cred 为保险库里存的 "AccessKeyID,SecretAccessKey" 原串。
// transport 为 nil 用默认(带超时),测试可注入 mock RoundTripper。
func newBaiduDNSClient(cred string, transport http.RoundTripper, base string) *baiduDNSClient {
	hc := &http.Client{Timeout: 15 * time.Second}
	if transport != nil {
		hc.Transport = transport
	}
	if base == "" {
		base = baiduAPIBase
	}
	return &baiduDNSClient{cred: strings.TrimSpace(cred), hc: hc, base: strings.TrimRight(base, "/")}
}

// parseBaiduCred 校验 "AccessKeyID,SecretAccessKey" 形态(按第一个逗号切分,两侧 trim)。
// 任一为空 → ErrInvalidCredential。返回 (ak, sk)。
func parseBaiduCred(raw string) (ak, sk string, err error) {
	idx := strings.IndexByte(raw, ',')
	if idx < 0 {
		return "", "", fmt.Errorf("%w:百度智能云凭据须为「AccessKeyID,SecretAccessKey」(逗号分隔)", ErrInvalidCredential)
	}
	ak = strings.TrimSpace(raw[:idx])
	sk = strings.TrimSpace(raw[idx+1:])
	if ak == "" || sk == "" {
		return "", "", fmt.Errorf("%w:百度智能云凭据「AccessKeyID,SecretAccessKey」两段均不可为空", ErrInvalidCredential)
	}
	return ak, sk, nil
}

func (c *baiduDNSClient) now() time.Time {
	if c.nowFn != nil {
		return c.nowFn()
	}
	return time.Now().UTC()
}

// baiduPercentEncode 做 RFC3986 百分号编码(unreserved 之外全编码)。
// keepSlash 用于规范 URI:路径的斜杠是结构而非内容,不能变成 %2F。
func baiduPercentEncode(s string, keepSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'A' && ch <= 'Z', ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9',
			ch == '-', ch == '_', ch == '.', ch == '~':
			b.WriteByte(ch)
		case keepSlash && ch == '/':
			b.WriteByte(ch)
		default:
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}

// baiduCanonicalQuery 把查询参数按编码后的 key 排序,拼成 key=value&...(缺值只写 key)。
func baiduCanonicalQuery(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		if params[k] == "" {
			parts = append(parts, baiduPercentEncode(k, false))
			continue
		}
		parts = append(parts, baiduPercentEncode(k, false)+"="+baiduPercentEncode(params[k], false))
	}
	return strings.Join(parts, "&")
}

// bceAuthorization 按 bce-auth-v1 算 Authorization 头的值。
//
// 百度的规范请求是「方法 \n 规范URI \n 规范查询串 \n 规范头」四行,**不含请求体哈希**
// (与 AWS SigV4 / 火山不同),故同一实体重复签名结果稳定。
// 规范头里 key 与 value **都要**按 BCE 的 UriEncode 编码(content-type 的值因此带 %2F/%20/%3D),
// 且条目之间以 \n 分隔、末尾没有换行 —— 照 bce-sdk-go/auth/signer.go 的 getCanonicalHeaders。
// 密钥链只有一层:signingKey = hex(HMAC-SHA256(SK, authStringPrefix))。
func bceAuthorization(ak, sk, method, host, path string, query map[string]string, bceDate string) string {
	prefix := baiduAlgorithm + "/" + ak + "/" + bceDate + "/" + strconv.Itoa(baiduExpiration)
	// 参与签名的头按名字典序;下面三条即 signer.go 排完序的结果。
	canonicalHeaders := "content-type:" + baiduPercentEncode(baiduContentType, false) + "\n" +
		"host:" + baiduPercentEncode(host, false) + "\n" +
		"x-bce-date:" + baiduPercentEncode(bceDate, false)
	canonicalRequest := strings.Join([]string{
		method,
		baiduPercentEncode(path, true),
		baiduCanonicalQuery(query),
		canonicalHeaders,
	}, "\n")

	mac := hmac.New(sha256.New, []byte(sk))
	mac.Write([]byte(prefix))
	signingKey := hex.EncodeToString(mac.Sum(nil))

	sig := hmac.New(sha256.New, []byte(signingKey))
	sig.Write([]byte(canonicalRequest))
	return prefix + "/" + baiduSignedHeaders + "/" + hex.EncodeToString(sig.Sum(nil))
}

// baiduErrBody 是 BCE 统一错误信封(HTTP 非 2xx 时才有)。
type baiduErrBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"requestId"`
}

// baiduErr 把 BCE 错误码映射人话(绝不含 ak/sk;百度 code/message 不含凭据明文)。
func baiduErr(base error, status int, e baiduErrBody) error {
	switch e.Code {
	case "InvalidAccessKeyId", "SignatureDoesNotMatch", "AccessDenied", "AuthorizationMissing",
		"MalformedAuthorization", "OptInRequired", "QualifyNotPass":
		return fmt.Errorf("%w:百度智能云凭据无效或无权限", base)
	case "NoSuchObject", "ZoneNotExist", "NotFound":
		return fmt.Errorf("%w:百度智能云域名不存在或不受该凭据管理", base)
	}
	if code := strings.TrimSpace(e.Code); code != "" {
		return fmt.Errorf("%w:百度智能云 DNS API 返回错误(HTTP %d, %s)", base, status, code)
	}
	return fmt.Errorf("%w:百度智能云 DNS API 拒绝(HTTP %d)", base, status)
}

// call 发一个签名请求(path 为不带查询串的接口路径,params 为查询参数),成功时把响应体解到 out。
// 写接口(添加/更新/删除)按文档「无返回参数」,故 out 传 nil。
func (c *baiduDNSClient) call(ctx context.Context, baseErr error, method, path string, params map[string]string, payload any, out any) error {
	ak, sk, err := parseBaiduCred(c.cred)
	if err != nil {
		return err
	}

	var body []byte
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("%w:构造百度智能云请求体失败", baseErr)
		}
		body = raw
	}

	u := c.base + path
	if len(params) > 0 {
		u += "?" + baiduCanonicalQuery(params)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w:构造百度智能云请求失败", baseErr)
	}
	bceDate := c.now().UTC().Format("2006-01-02T15:04:05Z")
	req.Header.Set("Content-Type", baiduContentType)
	req.Header.Set("x-bce-date", bceDate)
	// 签名要在拿到最终 Host 之后算(测试注入的是 mock 地址)。
	req.Header.Set("Authorization", bceAuthorization(ak, sk, method, req.URL.Host, req.URL.Path, params, bceDate))
	req.Header.Set("User-Agent", "Pipewright/1.0 (dns@pipewright.local)")

	resp, err := c.hc.Do(req)
	if err != nil {
		// 绝不回显 err.Error()(可能含 URL/签名);给固定人话。
		return fmt.Errorf("%w:无法连接百度智能云 DNS API", baseErr)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("%w:读取百度智能云 DNS API 响应失败", baseErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var eb baiduErrBody
		_ = json.Unmarshal(raw, &eb)
		return baiduErr(baseErr, resp.StatusCode, eb)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("%w:百度智能云 DNS API 响应解析失败", baseErr)
		}
	}
	return nil
}

// baiduID 兼容「记录 id 回字符串」与「回数字」两种形态:文档示例是 "1234",官方 SDK 亦为 string,
// 但真机见过数字形态。拿不到 id 就无法更新/删除,故这里宽松解、严进出。
type baiduID string

var errBaiduRecordID = errors.New("baidudns: 记录 id 形态非法")

func (i *baiduID) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*i = baiduID(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err == nil {
		*i = baiduID(n.String())
		return nil
	}
	return errBaiduRecordID
}

// baiduRecord 是解析记录对象里我们要用的字段子集。
type baiduRecord struct {
	ID    baiduID `json:"id"`
	RR    string  `json:"rr"`
	Type  string  `json:"type"`
	Value string  `json:"value"`
}

type baiduListRecordsResp struct {
	Records []baiduRecord `json:"records"`
	// 百度的列表是分页的:nextMarker 非空/截断即接着翻。
	NextMarker  string `json:"nextMarker"`
	IsTruncated bool   `json:"isTruncated"`
}

type baiduListZonesResp struct {
	Zones []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	} `json:"zones"`
	NextMarker  string `json:"nextMarker"`
	IsTruncated bool   `json:"isTruncated"`
}

// VerifyZone 校验当前凭据可管理 zone:GET /v1/dns/zone?name=<zone> 里能精确匹配到 ZoneName。
// 百度公网 DNS **没有**「查单个域名详情」接口(域名相关只有添加/查询列表/删除),故走列表匹配;
// 匹配到的域还须 status 为 running,否则解析不生效,签证书会在 ACME 校验阶段才发现。
func (c *baiduDNSClient) VerifyZone(ctx context.Context, zone string) error {
	for marker := ""; ; {
		params := map[string]string{"name": zone, "maxKeys": "100"}
		if marker != "" {
			params["marker"] = marker
		}
		var list baiduListZonesResp
		if err := c.call(ctx, ErrVerifyFailed, http.MethodGet, "/v1/dns/zone", params, nil, &list); err != nil {
			return err
		}
		for _, z := range list.Zones {
			if strings.EqualFold(z.Name, strings.TrimSuffix(zone, ".")) {
				if z.Status != "" && !strings.EqualFold(z.Status, "running") {
					return fmt.Errorf("%w:百度智能云域名 %s 当前状态 %s,解析未生效", ErrVerifyFailed, zone, z.Status)
				}
				return nil
			}
		}
		// name 是精确筛选语义;查不到即「该凭据管不了这个域」(文档没有专门的「域名不受管理」错误码)。
		if !list.IsTruncated || list.NextMarker == "" {
			return fmt.Errorf("%w:百度智能云域名 %s 不存在或不受该凭据管理", ErrVerifyFailed, zone)
		}
		marker = list.NextMarker
	}
}

// records 取 zone 下 sub 的全部指定类型记录(带 rr 过滤,回读仍按 Type 精确再筛)。
func (c *baiduDNSClient) records(ctx context.Context, zone, sub, recordType string, base error) ([]baiduRecord, error) {
	var out []baiduRecord
	for marker := ""; ; {
		params := map[string]string{"rr": sub, "maxKeys": "100"}
		if marker != "" {
			params["marker"] = marker
		}
		var list baiduListRecordsResp
		if err := c.call(ctx, base, http.MethodGet, "/v1/dns/zone/"+baiduPercentEncode(zone, true)+"/record", params, nil, &list); err != nil {
			return nil, err
		}
		for _, r := range list.Records {
			if strings.EqualFold(r.Type, recordType) && rrEqual(r.RR, sub) {
				out = append(out, r)
			}
		}
		if !list.IsTruncated || list.NextMarker == "" {
			return out, nil
		}
		marker = list.NextMarker
	}
}

// removeBaiduRecords 逐条 DELETE(全删,不只删第一条)。写接口无响应体,判 HTTP 状态即可。
func (c *baiduDNSClient) removeBaiduRecords(ctx context.Context, zone string, records []baiduRecord, base error) error {
	for _, r := range records {
		if r.ID == "" {
			return fmt.Errorf("%w:百度智能云记录缺少 id,无法删除", base)
		}
		path := "/v1/dns/zone/" + baiduPercentEncode(zone, true) + "/record/" + baiduPercentEncode(string(r.ID), true)
		if err := c.call(ctx, base, http.MethodDelete, path, nil, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

// EnsureARecord 为 zone 下 name 建/改 A 记录指向 ip(幂等 upsert)。
// 添加/更新接口按文档不返回记录体,故幂等判定全靠先列表回读。
func (c *baiduDNSClient) EnsureARecord(ctx context.Context, zone, name, ip string) error {
	sub := subDomain(name, zone)
	existing, err := c.records(ctx, zone, sub, "A", ErrEnsureRecord)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		if existing[0].Value == ip {
			return nil // 幂等 no-op。
		}
		return c.putRecord(ctx, zone, existing[0], "A", ip, ErrEnsureRecord)
	}
	return c.postRecord(ctx, zone, sub, "A", ip, ErrEnsureRecord)
}

// DeleteARecord 删除 zone 下 name 的 A 记录(幂等:无记录视为成功)。
func (c *baiduDNSClient) DeleteARecord(ctx context.Context, zone, name string) error {
	existing, err := c.records(ctx, zone, subDomain(name, zone), "A", ErrDeleteRecord)
	if err != nil {
		return err
	}
	return c.removeBaiduRecords(ctx, zone, existing, ErrDeleteRecord)
}

// EnsureTXTRecord 为 zone 下 name 落一条 TXT 记录,内容归一为 value(收敛成一条,理由见 TXTClient)。
func (c *baiduDNSClient) EnsureTXTRecord(ctx context.Context, zone, name, value string) error {
	sub := subDomain(name, zone)
	existing, err := c.records(ctx, zone, sub, "TXT", ErrEnsureTXT)
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		return c.postRecord(ctx, zone, sub, "TXT", value, ErrEnsureTXT)
	}
	if err := c.removeBaiduRecords(ctx, zone, existing[1:], ErrEnsureTXT); err != nil {
		return err
	}
	if txtEqual(existing[0].Value, value) {
		return nil // 已是目标值(多余条目已清干净),不再发更新。
	}
	return c.putRecord(ctx, zone, existing[0], "TXT", value, ErrEnsureTXT)
}

// DeleteTXTRecord 删除 zone 下 name 的全部 TXT 记录(幂等:无记录返回 nil)。
func (c *baiduDNSClient) DeleteTXTRecord(ctx context.Context, zone, name string) error {
	existing, err := c.records(ctx, zone, subDomain(name, zone), "TXT", ErrDeleteTXT)
	if err != nil {
		return err
	}
	return c.removeBaiduRecords(ctx, zone, existing, ErrDeleteTXT)
}

// postRecord 添加一条解析记录:POST /v1/dns/zone/{zone}/record。必带 rr/type/value,line/ttl 给默认值。
func (c *baiduDNSClient) postRecord(ctx context.Context, zone, sub, recordType, value string, base error) error {
	payload := map[string]any{
		"rr":    sub,
		"type":  recordType,
		"value": value,
		"ttl":   baiduDefaultTTL,
		"line":  baiduDefaultLine,
	}
	return c.call(ctx, base, http.MethodPost, "/v1/dns/zone/"+baiduPercentEncode(zone, true)+"/record", nil, payload, nil)
}

// putRecord 就地更新一条记录:PUT /v1/dns/zone/{zone}/record/{id}。
// 按文档,更新实体**不带 line**(线路在添加时定,更新不许改)。
func (c *baiduDNSClient) putRecord(ctx context.Context, zone string, rec baiduRecord, recordType, value string, base error) error {
	if rec.ID == "" {
		return fmt.Errorf("%w:百度智能云记录缺少 id,无法更新", base)
	}
	payload := map[string]any{
		"rr":    rec.RR,
		"type":  recordType,
		"value": value,
		"ttl":   baiduDefaultTTL,
	}
	path := "/v1/dns/zone/" + baiduPercentEncode(zone, true) + "/record/" + baiduPercentEncode(string(rec.ID), true)
	return c.call(ctx, base, http.MethodPut, path, nil, payload, nil)
}
