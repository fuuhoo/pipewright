package dnsprovider

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// tencentAPIBase 是腾讯云「DNS 解析」API 3.0 根(产品 Code: dnspod)。测试经 RoundTripper 拦截,不触真网络。
//
// 与 dnspod.go 的区别(两者都保留,别混):dnspod.go 走的是**经典 API 2.0**(dnsapi.cn form-POST,
// login_token="ID,Token"),那套只覆盖 DNSPod 控制台里的域名;在腾讯云「云解析」控制台添加的域名
// 用 2.0 拿不到(账号体系不同)。3.0 用 SecretId/SecretKey + TC3-HMAC-SHA256 签名,是官方主推的一套。
const tencentAPIBase = "https://dnspod.tencentcloudapi.com"

// 腾讯云 API 3.0 公共参数固定值。
const (
	// tencentVersion 是 dnspod 产品的 API 版本。
	tencentVersion = "2021-03-23"
	// tencentService 参与签名(CredentialScope),dnspod 产品的服务名即此。
	tencentService = "dnspod"
	// tencentAlgorithm 是签名算法字面量。
	tencentAlgorithm = "TC3-HMAC-SHA256"
	// tencentContentType 必须与规范里参与签名的 content-type 完全一致,故两侧都用这一个常量。
	tencentContentType = "application/json; charset=utf-8"
	// tencentDefaultLine 是「默认」线路。3.0 的 CreateRecord/ModifyRecord 要求显式给线路。
	tencentDefaultLine = "默认"
)

// 凭据约定(vault 单字串存储模型不变):腾讯云用 "SecretId,SecretKey"。
// 即在保险库里存 "AKIDxxxxxxxx,yyyyyyyySecret" 这样一个逗号分隔字串;按**第一个逗号**切分,两侧 trim。
// 无逗号 / 任一段空 → ErrInvalidCredential。绝不把 id/key 任何片段回显到错误/日志/DTO。
type tencentCloudClient struct {
	cred string // 形如 "SecretId,SecretKey";仅进程内,不入库/日志/响应
	hc   *http.Client
	base string

	// nowFn 供测试钉死 X-TC-Timestamp(生产为 nil → time.Now)。
	nowFn func() time.Time
}

// newTencentCloudClient 构造腾讯云 DNS 解析 3.0 客户端。cred 为保险库里存的 "SecretId,SecretKey" 原串。
// transport 为 nil 用默认(带超时),测试可注入 mock RoundTripper。
func newTencentCloudClient(cred string, transport http.RoundTripper, base string) *tencentCloudClient {
	hc := &http.Client{Timeout: 15 * time.Second}
	if transport != nil {
		hc.Transport = transport
	}
	if base == "" {
		base = tencentAPIBase
	}
	return &tencentCloudClient{cred: strings.TrimSpace(cred), hc: hc, base: strings.TrimRight(base, "/")}
}

// parseTencentCred 校验 "SecretId,SecretKey" 形态(按第一个逗号切分,两侧 trim)。
// 任一为空 → ErrInvalidCredential。返回 (secretID, secretKey)。
func parseTencentCred(raw string) (id, key string, err error) {
	idx := strings.IndexByte(raw, ',')
	if idx < 0 {
		return "", "", fmt.Errorf("%w:腾讯云凭据须为「SecretId,SecretKey」(逗号分隔)", ErrInvalidCredential)
	}
	id = strings.TrimSpace(raw[:idx])
	key = strings.TrimSpace(raw[idx+1:])
	if id == "" || key == "" {
		return "", "", fmt.Errorf("%w:腾讯云凭据「SecretId,SecretKey」两段均不可为空", ErrInvalidCredential)
	}
	return id, key, nil
}

func (c *tencentCloudClient) now() time.Time {
	if c.nowFn != nil {
		return c.nowFn()
	}
	return time.Now().UTC()
}

// hmacSHA256 返回 HMAC-SHA256(key, msg) 的原始字节(腾讯云 TC3 密钥链逐层派生用它)。
func hmacSHA256(key []byte, msg string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(msg))
	return mac.Sum(nil)
}

// tc3Authorization 按 TC3-HMAC-SHA256 规范算出 Authorization 头的值。
//
//	host       = 实际发出的 Host(取 req.URL.Host,测试里是 mock 地址,不能用常量否则签不上)
//	timestamp  = Unix 秒,必须与 X-TC-Timestamp 同值
//	body       = 即将发送的**同一份**字节实体(哈希请求体用的是它,重排一次键序就变)
//
// 步骤:规范请求 → 待签名字符串 → 密钥链 TC3{SecretKey} → {日期} → {service} → tc3_request → 签名(hex)。
func tc3Authorization(secretID, secretKey, host string, timestamp int64, body []byte) string {
	sum := sha256.Sum256(body)
	hashedPayload := hex.EncodeToString(sum[:])

	// 参与签名的头按字典序;此处只有 content-type 与 host 两项。
	canonicalHeaders := "content-type:" + tencentContentType + "\n" + "host:" + host + "\n"
	signedHeaders := "content-type;host"

	canonicalRequest := strings.Join([]string{
		http.MethodPost,
		"/",
		"", // 查询串:3.0 POST 全空
		canonicalHeaders,
		signedHeaders,
		hashedPayload,
	}, "\n")

	date := time.Unix(timestamp, 0).UTC().Format("2006-01-02")
	scope := date + "/" + tencentService + "/tc3_request"
	crSum := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{
		tencentAlgorithm,
		strconv.FormatInt(timestamp, 10),
		scope,
		hex.EncodeToString(crSum[:]),
	}, "\n")

	secretDate := hmacSHA256([]byte("TC3"+secretKey), date)
	secretService := hmacSHA256(secretDate, tencentService)
	secretSigning := hmacSHA256(secretService, "tc3_request")
	signature := hex.EncodeToString(hmacSHA256(secretSigning, stringToSign))

	return tencentAlgorithm + " Credential=" + secretID + "/" + scope +
		", SignedHeaders=" + signedHeaders + ", Signature=" + signature
}

// tcError 是腾讯云错误信封(HTTP 恒 200,错误只在 Response.Error 里)。
type tcError struct {
	Code    string `json:"Code"`
	Message string `json:"Message"`
}

// tcCallError 先看 Response.Error(业务错误),再退到 HTTP 状态码 / 解析失败。
type tcCallError struct {
	Response struct {
		Error     *tcError `json:"Error"`
		RequestID string   `json:"RequestId"`
	} `json:"Response"`
}

// call 发一个签名后的 POST(JSON 实体),成功时把响应体解到 out。
// out 须为形如 struct{ Response struct{...} } 的信封指针(3.0 的业务字段一律裹在 Response 下)。
func (c *tencentCloudClient) call(ctx context.Context, baseErr error, action string, payload map[string]any, out any) error {
	secretID, secretKey, err := parseTencentCred(c.cred)
	if err != nil {
		return err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("%w:构造腾讯云请求体失败", baseErr)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w:构造腾讯云请求失败", baseErr)
	}
	ts := c.now().Unix()
	req.Header.Set("Content-Type", tencentContentType)
	req.Header.Set("X-TC-Action", action)
	req.Header.Set("X-TC-Version", tencentVersion)
	req.Header.Set("X-TC-Timestamp", strconv.FormatInt(ts, 10))
	// 签名要在拿到最终 Host 之后算(测试注入的是 mock 地址)。
	req.Header.Set("Authorization", tc3Authorization(secretID, secretKey, req.URL.Host, ts, body))
	req.Header.Set("User-Agent", "Pipewright/1.0 (dns@pipewright.local)")

	resp, err := c.hc.Do(req)
	if err != nil {
		// 绝不回显 err.Error()(可能含 URL/签名);给固定人话。
		return fmt.Errorf("%w:无法连接腾讯云 DNS API", baseErr)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w:腾讯云 DNS API 拒绝(HTTP %d)", baseErr, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("%w:读取腾讯云 DNS API 响应失败", baseErr)
	}
	var env tcCallError
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("%w:腾讯云 DNS API 响应解析失败", baseErr)
	}
	if env.Response.Error != nil {
		return tcErr(baseErr, *env.Response.Error)
	}
	if out != nil {
		// 同一份字节再解一次到业务信封:错误已判过,这里只看 Response 里的字段。
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("%w:腾讯云 DNS API 响应解析失败", baseErr)
		}
	}
	return nil
}

// tcErr 把腾讯云错误码映射人话(绝不含凭据;腾讯云 Code/Message 不含 SecretKey)。
func tcErr(base error, e tcError) error {
	switch e.Code {
	case "AuthFailure", "AuthFailure.SecretIdNotFound", "AuthFailure.ExpiredSignature",
		"InvalidParameter.InvalidSignature", "FailedOperation.UnAuthorizedError", "RequestLimitExceeded.SimpleRateLimit":
		return fmt.Errorf("%w:腾讯云凭据无效或无权限", base)
	case "InvalidParameterValue.DomainNotExists", "FailedOperation.NotDomainOwner", "InvalidParameter.DomainRecordExist":
		return fmt.Errorf("%w:腾讯云域名不存在或不受该凭据管理", base)
	case "ResourceNotFound.NoDataOfRecord", "InvalidParameterValue.RecordIdInvalid":
		return fmt.Errorf("%w:腾讯云解析记录不存在", base)
	}
	if msg := strings.TrimSpace(e.Message); msg != "" {
		return fmt.Errorf("%w:腾讯云 DNS API 返回错误(%s):%s", base, e.Code, msg)
	}
	return fmt.Errorf("%w:腾讯云 DNS API 返回错误(%s)", base, e.Code)
}

// tcRecord 是 3.0 记录对象里我们要用的字段子集(RecordId 是数字,与经典 API 的字符串 id 不同)。
type tcRecord struct {
	RecordID int64  `json:"RecordId"`
	Name     string `json:"Name"`
	Type     string `json:"Type"`
	Value    string `json:"Value"`
}

type tcListRecordsResp struct {
	Response struct {
		RecordList []tcRecord `json:"RecordList"`
	} `json:"Response"`
}

// records 查 zone 下 sub 的指定类型记录。
//
// DescribeRecordList 的 SubDomain 是「精确或子域」语义且响应有分页,故带上偏移翻页取全,
// 再按 Name/Type 精确过滤(与阿里云 RRKeyWord 同一套路)。ErrorOnEmpty=no 让「无记录」返回空列表而非报错。
func (c *tencentCloudClient) records(ctx context.Context, zone, sub, recordType string, base error) ([]tcRecord, error) {
	var out []tcRecord
	for offset := 0; ; offset += 100 {
		payload := map[string]any{
			"Domain":       zone,
			"Offset":       offset,
			"Limit":        100,
			"ErrorOnEmpty": "no",
			"RecordType":   recordType,
		}
		if sub != "@" {
			// 顶点用 SubDomain="" 或不传;传 "@" 在 3.0 是「按主机头 @ 精确查」的另一套写法,
			// 为兼容两种账号下的历史数据,顶点直接不带此参数、靠客户端过滤。
			payload["SubDomain"] = sub
		}
		var list tcListRecordsResp
		if err := c.call(ctx, base, "DescribeRecordList", payload, &list); err != nil {
			return nil, err
		}
		for _, r := range list.Response.RecordList {
			if strings.EqualFold(r.Type, recordType) && rrEqual(r.Name, sub) {
				out = append(out, r)
			}
		}
		if len(list.Response.RecordList) < 100 {
			return out, nil
		}
	}
}

// removeTCRecords 逐条 DeleteRecord(全删,不只删第一条)。
func (c *tencentCloudClient) removeTCRecords(ctx context.Context, zone string, records []tcRecord, base error) error {
	for _, r := range records {
		if err := c.call(ctx, base, "DeleteRecord", map[string]any{
			"Domain":   zone,
			"RecordId": r.RecordID,
		}, nil); err != nil {
			return err
		}
	}
	return nil
}

// tcDomain 是 DescribeDomainList 里我们要用的字段子集。
//
// 区名在 **Name**(不是 Domain —— `Domain` 只存在于 DescribeDomain 返回的 DomainInfo 里,
// 照记忆写成 Domain 会解出一个空清单)。Status(ENABLE/PAUSE/SPAM)刻意忽略:暂停只是解析停了,
// 记录仍可读写,「凭据能不能管这个区」与「解析当前是否生效」是两件事,后者由 ACME/访问侧自然暴露。
type tcDomain struct {
	Name string `json:"Name"`
}

type tcListDomainsResp struct {
	Response struct {
		DomainList []tcDomain `json:"DomainList"`
	} `json:"Response"`
}

// ListZones 列出该凭据账号下托管的全部域名:Action=DescribeDomainList 按 Offset 翻页。
// Type=MINE 只取「本账号添加的域」,与「凭据可用」判据同口径(共享域协作者拿不到写入权限)。
func (c *tencentCloudClient) ListZones(ctx context.Context) ([]string, error) {
	const limit = 1000
	var out []string
	for offset := 0; ; offset += limit {
		var list tcListDomainsResp
		if err := c.call(ctx, ErrVerifyFailed, "DescribeDomainList", map[string]any{
			"Type":   "MINE",
			"Offset": offset,
			"Limit":  limit,
		}, &list); err != nil {
			return nil, err
		}
		for _, d := range list.Response.DomainList {
			out = append(out, d.Name)
		}
		if len(list.Response.DomainList) < limit {
			return sortNormalizedZones(out), nil
		}
	}
}

// EnsureARecord 为 zone 下 name 建/改 A 记录指向 ip(幂等 upsert)。
func (c *tencentCloudClient) EnsureARecord(ctx context.Context, zone, name, ip string) error {
	sub := subDomain(name, zone)
	existing, err := c.records(ctx, zone, sub, "A", ErrEnsureRecord)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		if existing[0].Value == ip {
			return nil // 幂等 no-op(与阿里云/DNSPod 同口径:只看首条)。
		}
		return c.call(ctx, ErrEnsureRecord, "ModifyRecord", map[string]any{
			"RecordId":   existing[0].RecordID,
			"Domain":     zone,
			"SubDomain":  sub,
			"RecordType": "A",
			"RecordLine": tencentDefaultLine,
			"Value":      ip,
		}, nil)
	}
	return c.call(ctx, ErrEnsureRecord, "CreateRecord", map[string]any{
		"Domain":     zone,
		"SubDomain":  sub,
		"RecordType": "A",
		"RecordLine": tencentDefaultLine,
		"Value":      ip,
	}, nil)
}

// DeleteARecord 删除 zone 下 name 的 A 记录(幂等:无记录视为成功)。
func (c *tencentCloudClient) DeleteARecord(ctx context.Context, zone, name string) error {
	sub := subDomain(name, zone)
	existing, err := c.records(ctx, zone, sub, "A", ErrDeleteRecord)
	if err != nil {
		return err
	}
	return c.removeTCRecords(ctx, zone, existing, ErrDeleteRecord)
}

// EnsureTXTRecord 为 zone 下 name 落一条 TXT 记录,内容归一为 value(收敛成一条,理由见 TXTClient)。
func (c *tencentCloudClient) EnsureTXTRecord(ctx context.Context, zone, name, value string) error {
	sub := subDomain(name, zone)
	existing, err := c.records(ctx, zone, sub, "TXT", ErrEnsureTXT)
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		return c.call(ctx, ErrEnsureTXT, "CreateRecord", map[string]any{
			"Domain":     zone,
			"SubDomain":  sub,
			"RecordType": "TXT",
			"RecordLine": tencentDefaultLine,
			"Value":      value,
		}, nil)
	}
	if err := c.removeTCRecords(ctx, zone, existing[1:], ErrEnsureTXT); err != nil {
		return err
	}
	if txtEqual(existing[0].Value, value) {
		return nil // 已是目标值(多余条目已清干净),不再发 Modify。
	}
	return c.call(ctx, ErrEnsureTXT, "ModifyRecord", map[string]any{
		"RecordId":   existing[0].RecordID,
		"Domain":     zone,
		"SubDomain":  sub,
		"RecordType": "TXT",
		"RecordLine": tencentDefaultLine,
		"Value":      value,
	}, nil)
}

// DeleteTXTRecord 删除 zone 下 name 的全部 TXT 记录(幂等:无记录返回 nil)。
func (c *tencentCloudClient) DeleteTXTRecord(ctx context.Context, zone, name string) error {
	sub := subDomain(name, zone)
	existing, err := c.records(ctx, zone, sub, "TXT", ErrDeleteTXT)
	if err != nil {
		return err
	}
	return c.removeTCRecords(ctx, zone, existing, ErrDeleteTXT)
}
