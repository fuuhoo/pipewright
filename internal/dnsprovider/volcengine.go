package dnsprovider

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// volcAPIBase 是火山引擎云解析(TrafficRoute DNS)OpenAPI 根。测试经 RoundTripper 拦截,不触真网络。
const volcAPIBase = "https://dns.volcengineapi.com"

// 火山 OpenAPI 公共参数与签名固定值。
const (
	// volcVersion 是云解析产品的 API 版本。
	volcVersion = "2018-08-01"
	// volcService 参与 CredentialScope(签名的第四段固定为 request)。
	volcService = "dns"
	// volcAlgorithm 是签名算法字面量(火山只支持这一种)。
	volcAlgorithm = "HMAC-SHA256"
	// volcContentType 同时进签名与实际请求头,必须一致。
	volcContentType = "application/json"
	// volcDefaultLine 是「默认」线路;火山线路取值是小写英文,与腾讯/DNSPod 的「默认」不同。
	volcDefaultLine = "default"
	// volcDefaultTTL 是建记录时给的秒数(火山允许省略,显式给更利于 golden 断言)。
	volcDefaultTTL = 600

	// volcRegion 进 CredentialScope。取 cn-beijing —— 与官方 volc-sdk-golang/service/dns 的
	// DefaultRegion 一致(云解析是全局服务,这个值只是签名 scope 的一段)。
	volcRegion = "cn-beijing"
)

// 凭据约定(vault 单字串存储模型不变):火山引擎用 "AccessKeyID,SecretAccessKey"。
// 即 IAM 那对 AK/SK,与「密钥对」无关。按**第一个逗号**切分,两侧 trim;无逗号 / 任一段空 →
// ErrInvalidCredential。绝不把 ak/sk 任何片段回显到错误/日志/DTO。
type volcengineClient struct {
	cred string // 形如 "AccessKeyID,SecretAccessKey";仅进程内,不入库/日志/响应
	hc   *http.Client
	base string

	// 以下供测试注入,钉死签名(生产为 nil → 用 time.Now)。
	nowFn func() time.Time
}

// newVolcengineClient 构造火山引擎云解析客户端。cred 为保险库里存的 "AccessKeyID,SecretAccessKey" 原串。
// transport 为 nil 用默认(带超时),测试可注入 mock RoundTripper。
func newVolcengineClient(cred string, transport http.RoundTripper, base string) *volcengineClient {
	hc := &http.Client{Timeout: 15 * time.Second}
	if transport != nil {
		hc.Transport = transport
	}
	if base == "" {
		base = volcAPIBase
	}
	return &volcengineClient{cred: strings.TrimSpace(cred), hc: hc, base: strings.TrimRight(base, "/")}
}

// parseVolcCred 校验 "AccessKeyID,SecretAccessKey" 形态(按第一个逗号切分,两侧 trim)。
// 任一为空 → ErrInvalidCredential。返回 (ak, sk)。
func parseVolcCred(raw string) (ak, sk string, err error) {
	idx := strings.IndexByte(raw, ',')
	if idx < 0 {
		return "", "", fmt.Errorf("%w:火山引擎凭据须为「AccessKeyID,SecretAccessKey」(逗号分隔)", ErrInvalidCredential)
	}
	ak = strings.TrimSpace(raw[:idx])
	sk = strings.TrimSpace(raw[idx+1:])
	if ak == "" || sk == "" {
		return "", "", fmt.Errorf("%w:火山引擎凭据「AccessKeyID,SecretAccessKey」两段均不可为空", ErrInvalidCredential)
	}
	return ak, sk, nil
}

func (c *volcengineClient) now() time.Time {
	if c.nowFn != nil {
		return c.nowFn()
	}
	return time.Now().UTC()
}

// volcPercentEncode 按签名规范做 RFC3986 编码:QueryEscape 后 "+"→"%20"、"*"→"%2A"、"%7E"→"~"。
// 斜杠必须留 %2F(查询串里编码键值与编码路径的规则不同)。
func volcPercentEncode(s string) string {
	e := url.QueryEscape(s)
	e = strings.ReplaceAll(e, "+", "%20")
	e = strings.ReplaceAll(e, "*", "%2A")
	e = strings.ReplaceAll(e, "%7E", "~")
	return e
}

// volcCanonicalQuery 把查询参数按编码后的 key 排序拼规范串(同 key 多值也排序)。
func volcCanonicalQuery(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, volcPercentEncode(k)+"="+volcPercentEncode(params[k]))
	}
	return strings.Join(parts, "&")
}

func volcHMAC(key []byte, msg string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(msg))
	return mac.Sum(nil)
}

// volcSign 按火山 OpenAPI 签名规范(与 AWS SigV4 同构,算法名/密钥前缀不同)算 Authorization 头。
//
//	method/uri/query/body 是即将发出的真实请求内容;host 取 req.URL.Host(测试里是 mock 地址);
//	shortDate(YYYYMMDD)与 fullDate(YYYYMMDD'T'HHMMSS'Z')都来自同一个 X-Date 时刻。
//
// 规范请求 → 待签字符串 → 密钥链 {SK} → {日期} → {region} → dns → request → 签名(hex)。
// 密钥链第一层直接用 SecretAccessKey,**不带 AWS 那种 "AWS4" 前缀**(火山 SDK 的 signingKeyV4 如此)。
func volcSign(secretID, secretKey, method, host, shortDate, fullDate string, query map[string]string, body []byte) string {
	hashedPayload := sha256hex(body)
	canonicalHeaders := "content-type:" + volcContentType + "\n" +
		"host:" + host + "\n" +
		"x-content-sha256:" + hashedPayload + "\n" +
		"x-date:" + fullDate + "\n"
	signedHeaders := "content-type;host;x-content-sha256;x-date"

	canonicalRequest := strings.Join([]string{
		method,
		"/",
		volcCanonicalQuery(query),
		canonicalHeaders,
		signedHeaders,
		hashedPayload,
	}, "\n")

	scope := shortDate + "/" + volcRegion + "/" + volcService + "/request"
	stringToSign := strings.Join([]string{
		volcAlgorithm,
		fullDate,
		scope,
		sha256hex([]byte(canonicalRequest)),
	}, "\n")

	kDate := volcHMAC([]byte(secretKey), shortDate)
	kRegion := volcHMAC(kDate, volcRegion)
	kService := volcHMAC(kRegion, volcService)
	kSigning := volcHMAC(kService, "request")
	signature := hex.EncodeToString(volcHMAC(kSigning, stringToSign))

	return volcAlgorithm + " Credential=" + secretID + "/" + scope +
		", SignedHeaders=" + signedHeaders + ", Signature=" + signature
}

// sha256hex 返回 sha256(data) 的小写十六进制(X-Content-Sha256 与规范请求体哈希都用它)。
func sha256hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// volcErr 是 OpenAPI 统一错误(业务错误可能在 HTTP 200 里,也可能带 4xx 状态码)。
type volcErr struct {
	Code    string `json:"Code"`
	Message string `json:"Message"`
}

type volcEnvelope struct {
	ResponseMetadata struct {
		RequestID string   `json:"RequestId"`
		Error     *volcErr `json:"Error"`
	} `json:"ResponseMetadata"`
}

// volcCallError 把错误码映射人话(绝不含 ak/sk;火山 Code/Message 不含凭据明文)。
func volcCallError(base error, e volcErr) error {
	switch e.Code {
	case "AccessDenied", "AccessDenied.Unauthorized", "InvalidCredential", "SignatureDoesNotMatch",
		"InvalidAccessKeyId.NotFound", "AuthFailure":
		return fmt.Errorf("%w:火山引擎凭据无效或无权限", base)
	case "ZoneNotExists", "NoSuchResource", "InvalidZone":
		return fmt.Errorf("%w:火山引擎域名不存在或不受该凭据管理", base)
	case "RecordDuplicate", "InvalidRecord", "RecordNotExists":
		return fmt.Errorf("%w:火山引擎解析记录冲突或不存在", base)
	}
	if msg := strings.TrimSpace(e.Message); msg != "" {
		return fmt.Errorf("%w:火山引擎 DNS API 返回错误(%s):%s", base, e.Code, msg)
	}
	return fmt.Errorf("%w:火山引擎 DNS API 返回错误(%s)", base, e.Code)
}

// call 发一个签名请求:读用 GET(参数进查询串),写用 POST(参数进 JSON 实体)。
// 签名必须在参数与实体都已定稿之后算,故此函数负责拼装请求体、算两次哈希、发一次请求。
func (c *volcengineClient) call(ctx context.Context, baseErr error, method, action string, params map[string]any, out any) error {
	ak, sk, err := parseVolcCred(c.cred)
	if err != nil {
		return err
	}

	query := map[string]string{"Action": action, "Version": volcVersion}
	var body []byte
	switch method {
	case http.MethodGet:
		for k, v := range params {
			query[k] = volcStringify(v)
		}
	default: // POST:公共参数仍留在查询串,业务参数进实体。
		raw, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("%w:构造火山引擎请求体失败", baseErr)
		}
		body = raw
	}

	u := c.base + "/?" + volcCanonicalQuery(query)
	req, err := http.NewRequestWithContext(ctx, method, u, strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("%w:构造火山引擎请求失败", baseErr)
	}
	now := c.now().UTC()
	shortDate := now.Format("20060102")
	fullDate := now.Format("20060102T150405Z")

	req.Header.Set("Content-Type", volcContentType)
	req.Header.Set("X-Date", fullDate)
	req.Header.Set("X-Content-Sha256", sha256hex(body))
	req.Header.Set("Authorization", volcSign(ak, sk, method, req.URL.Host, shortDate, fullDate, query, body))
	req.Header.Set("User-Agent", "Pipewright/1.0 (dns@pipewright.local)")

	resp, err := c.hc.Do(req)
	if err != nil {
		// 绝不回显 err.Error()(可能含 URL/签名);给固定人话。
		return fmt.Errorf("%w:无法连接火山引擎 DNS API", baseErr)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("%w:读取火山引擎 DNS API 响应失败", baseErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var bad volcEnvelope
		_ = json.Unmarshal(raw, &bad)
		if bad.ResponseMetadata.Error != nil {
			return volcCallError(baseErr, *bad.ResponseMetadata.Error)
		}
		return fmt.Errorf("%w:火山引擎 DNS API 拒绝(HTTP %d)", baseErr, resp.StatusCode)
	}
	// HTTP 200 也可能带错误:先解信封判错,再解业务字段(与腾讯云同一套双解逻辑)。
	var env volcEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("%w:火山引擎 DNS API 响应解析失败", baseErr)
	}
	if env.ResponseMetadata.Error != nil {
		return volcCallError(baseErr, *env.ResponseMetadata.Error)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("%w:火山引擎 DNS API 响应解析失败", baseErr)
		}
	}
	return nil
}

// volcStringify 把参数值转成查询串字面量(数字不带小数点,布尔按 API 要求写 1/0 之外的字面量)。
func volcStringify(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case bool:
		return strconv.FormatBool(x)
	default:
		return fmt.Sprintf("%v", x)
	}
}

// volcZone / volcListZonesResp 取我们需要的字段子集:建记录要的是数字 ZID,不是域名串。
type volcZone struct {
	ZID      int64  `json:"ZID"`
	ZoneName string `json:"ZoneName"`
}

type volcListZonesResp struct {
	Result struct {
		Zones []volcZone `json:"Zones"`
	} `json:"Result"`
}

type volcRecord struct {
	RecordID string `json:"RecordID"`
	Host     string `json:"Host"`
	Type     string `json:"Type"`
	Value    string `json:"Value"`
}

type volcListRecordsResp struct {
	Result struct {
		Records []volcRecord `json:"Records"`
	} `json:"Result"`
}

// resolveZone 把根域名换成数字 ZID:ListZones 逐页翻,按 ZoneName 精确匹配。
// 找不到 → baseErr(火山建/查记录都必须带 ZID,这是本提供商特有的额外一跳)。
func (c *volcengineClient) resolveZone(ctx context.Context, zone string, base error) (int64, error) {
	for page := 1; ; page++ {
		var list volcListZonesResp
		params := map[string]any{"PageSize": 100, "PageNumber": page}
		if err := c.call(ctx, base, http.MethodGet, "ListZones", params, &list); err != nil {
			return 0, err
		}
		for _, z := range list.Result.Zones {
			if strings.EqualFold(z.ZoneName, strings.TrimSuffix(zone, ".")) {
				return z.ZID, nil
			}
		}
		if len(list.Result.Zones) < 100 {
			return 0, fmt.Errorf("%w:火山引擎域名 %s 不存在或不受该凭据管理", base, zone)
		}
	}
}

// records 取 zone 下 sub 的指定类型记录(逐页翻完再按 Host/Type 精确过滤)。
func (c *volcengineClient) records(ctx context.Context, zid int64, sub, recordType string, base error) ([]volcRecord, error) {
	var out []volcRecord
	for page := 1; ; page++ {
		var list volcListRecordsResp
		params := map[string]any{"ZID": zid, "PageSize": 100, "PageNumber": page}
		if err := c.call(ctx, base, http.MethodGet, "ListRecords", params, &list); err != nil {
			return nil, err
		}
		for _, r := range list.Result.Records {
			if strings.EqualFold(r.Type, recordType) && rrEqual(r.Host, sub) {
				out = append(out, r)
			}
		}
		if len(list.Result.Records) < 100 {
			return out, nil
		}
	}
}

// removeVolcRecords 逐条 DeleteRecord(全删,不只删第一条)。
func (c *volcengineClient) removeVolcRecords(ctx context.Context, zid int64, records []volcRecord, base error) error {
	for _, r := range records {
		if err := c.call(ctx, base, http.MethodPost, "DeleteRecord", map[string]any{
			"ZID":      zid,
			"RecordID": r.RecordID,
		}, nil); err != nil {
			return err
		}
	}
	return nil
}

// VerifyZone 校验当前凭据可管理 zone:ListZones 里能精确找到这个 ZoneName 即成功。
// 火山没有「按域名查单个 zone 详情」的读接口(CheckZone 问的是子域能否添加),故走列表匹配。
func (c *volcengineClient) VerifyZone(ctx context.Context, zone string) error {
	_, err := c.resolveZone(ctx, zone, ErrVerifyFailed)
	return err
}

// EnsureARecord 为 zone 下 name 建/改 A 记录指向 ip(幂等 upsert)。
func (c *volcengineClient) EnsureARecord(ctx context.Context, zone, name, ip string) error {
	zid, err := c.resolveZone(ctx, zone, ErrEnsureRecord)
	if err != nil {
		return err
	}
	sub := subDomain(name, zone)
	existing, err := c.records(ctx, zid, sub, "A", ErrEnsureRecord)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		if existing[0].Value == ip {
			return nil // 幂等 no-op。
		}
		return c.call(ctx, ErrEnsureRecord, http.MethodPost, "UpdateRecord", map[string]any{
			"ZID":      zid,
			"RecordID": existing[0].RecordID,
			"Host":     sub,
			"Type":     "A",
			"Value":    ip,
			"TTL":      volcDefaultTTL,
			"Line":     volcDefaultLine,
		}, nil)
	}
	return c.call(ctx, ErrEnsureRecord, http.MethodPost, "CreateRecord", map[string]any{
		"ZID":   zid,
		"Host":  sub,
		"Type":  "A",
		"Value": ip,
		"TTL":   volcDefaultTTL,
		"Line":  volcDefaultLine,
	}, nil)
}

// DeleteARecord 删除 zone 下 name 的 A 记录(幂等:无记录视为成功)。
func (c *volcengineClient) DeleteARecord(ctx context.Context, zone, name string) error {
	zid, err := c.resolveZone(ctx, zone, ErrDeleteRecord)
	if err != nil {
		return err
	}
	existing, err := c.records(ctx, zid, subDomain(name, zone), "A", ErrDeleteRecord)
	if err != nil {
		return err
	}
	return c.removeVolcRecords(ctx, zid, existing, ErrDeleteRecord)
}

// EnsureTXTRecord 为 zone 下 name 落一条 TXT 记录,内容归一为 value(收敛成一条,理由见 TXTClient)。
func (c *volcengineClient) EnsureTXTRecord(ctx context.Context, zone, name, value string) error {
	zid, err := c.resolveZone(ctx, zone, ErrEnsureTXT)
	if err != nil {
		return err
	}
	sub := subDomain(name, zone)
	existing, err := c.records(ctx, zid, sub, "TXT", ErrEnsureTXT)
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		return c.call(ctx, ErrEnsureTXT, http.MethodPost, "CreateRecord", map[string]any{
			"ZID":   zid,
			"Host":  sub,
			"Type":  "TXT",
			"Value": value,
			"TTL":   volcDefaultTTL,
			"Line":  volcDefaultLine,
		}, nil)
	}
	if err := c.removeVolcRecords(ctx, zid, existing[1:], ErrEnsureTXT); err != nil {
		return err
	}
	if txtEqual(existing[0].Value, value) {
		return nil // 已是目标值(多余条目已清干净),不再发 Update。
	}
	return c.call(ctx, ErrEnsureTXT, http.MethodPost, "UpdateRecord", map[string]any{
		"ZID":      zid,
		"RecordID": existing[0].RecordID,
		"Host":     sub,
		"Type":     "TXT",
		"Value":    value,
		"TTL":      volcDefaultTTL,
		"Line":     volcDefaultLine,
	}, nil)
}

// DeleteTXTRecord 删除 zone 下 name 的全部 TXT 记录(幂等:无记录返回 nil)。
func (c *volcengineClient) DeleteTXTRecord(ctx context.Context, zone, name string) error {
	zid, err := c.resolveZone(ctx, zone, ErrDeleteTXT)
	if err != nil {
		return err
	}
	existing, err := c.records(ctx, zid, subDomain(name, zone), "TXT", ErrDeleteTXT)
	if err != nil {
		return err
	}
	return c.removeVolcRecords(ctx, zid, existing, ErrDeleteTXT)
}
