package dnsprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// dnspodAPIBase 是腾讯云 DNSPod 经典 API(dnsapi.cn)根。测试经 RoundTripper 拦截,不触真网络。
const dnspodAPIBase = "https://dnsapi.cn"

// 凭据约定(vault 单字串存储模型不变):DNSPod 用经典 login_token = "<ID>,<Token>"。
// 即在保险库里存 "12345,abcdef0123456789..." 这样一个逗号分隔字串;客户端按**第一个逗号**切分,
// 两侧 trim 空白。无逗号 → ErrInvalidCredential。绝不把 token 任何片段回显到错误/日志/DTO。
type dnspodClient struct {
	loginToken string // 形如 "<id>,<token>";仅进程内,不入库/日志/响应
	hc         *http.Client
	base       string
}

// newDNSPodClient 构造 DNSPod 客户端。loginToken 为保险库里存的 "id,token" 原串(校验留给请求时,
// 以便统一映射 ErrInvalidCredential)。transport 为 nil 时用默认(带超时),测试可注入 mock。
func newDNSPodClient(loginToken string, transport http.RoundTripper, base string) *dnspodClient {
	hc := &http.Client{Timeout: 15 * time.Second}
	if transport != nil {
		hc.Transport = transport
	}
	if base == "" {
		base = dnspodAPIBase
	}
	return &dnspodClient{loginToken: strings.TrimSpace(loginToken), hc: hc, base: strings.TrimRight(base, "/")}
}

// parseDNSPodToken 校验 "id,token" 形态(按第一个逗号切分,两侧 trim);任一为空 → ErrInvalidCredential。
// 返回归一化后的 login_token("id,token",已 trim),供表单字段直接使用。
func parseDNSPodToken(raw string) (loginToken string, err error) {
	idx := strings.IndexByte(raw, ',')
	if idx < 0 {
		return "", fmt.Errorf("%w:DNSPod 凭据须为「ID,Token」(逗号分隔)", ErrInvalidCredential)
	}
	id := strings.TrimSpace(raw[:idx])
	tok := strings.TrimSpace(raw[idx+1:])
	if id == "" || tok == "" {
		return "", fmt.Errorf("%w:DNSPod 凭据「ID,Token」两段均不可为空", ErrInvalidCredential)
	}
	return id + "," + tok, nil
}

// dnspodStatus 是 DNSPod 经典 API 的通用 status 信封。code=="1" 为成功。
type dnspodStatus struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// dpRecord / dpRecordListResp / dpMutateResp 取我们需要的字段子集。
type dpRecord struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

type dpRecordListResp struct {
	Status  dnspodStatus `json:"status"`
	Records []dpRecord   `json:"records"`
}

type dpMutateResp struct {
	Status dnspodStatus `json:"status"`
}

// post 发一个 form-POST 到 dnsapi.cn,自动带 login_token / format=json。错误绝不含 token。
// out 为解信封目标(传入指针)。
func (c *dnspodClient) post(ctx context.Context, path string, fields url.Values, out any) error {
	loginToken, err := parseDNSPodToken(c.loginToken)
	if err != nil {
		return err
	}
	form := url.Values{}
	for k, v := range fields {
		form[k] = v
	}
	form.Set("login_token", loginToken)
	form.Set("format", "json")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("%w:构造 DNSPod 请求失败", ErrVerifyFailed)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// DNSPod 经典 API 要求带 UserAgent(否则可能拒绝)。
	req.Header.Set("User-Agent", "Pipewright/1.0 (dns@pipewright.local)")

	resp, err := c.hc.Do(req)
	if err != nil {
		// 绝不回显 err.Error()(可能含 URL/token);给固定人话。
		return fmt.Errorf("%w:无法连接 DNSPod API", ErrVerifyFailed)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w:DNSPod API 拒绝(HTTP %d)", ErrVerifyFailed, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%w:DNSPod API 响应解析失败", ErrVerifyFailed)
	}
	return nil
}

// dpStatusErr 把非成功的 status 映射成人话错误(绝不含 token);DNSPod 的 message 不含凭据。
func dpStatusErr(base error, st dnspodStatus) error {
	msg := strings.TrimSpace(st.Message)
	// 常见鉴权失败码(login_token 无效 / 已删):给更明确的提示。
	switch st.Code {
	case "-1", "-2", "-7", "-8", "85":
		return fmt.Errorf("%w:DNSPod 凭据无效或无权限", base)
	}
	if msg != "" {
		return fmt.Errorf("%w:DNSPod API 返回错误(code %s):%s", base, st.Code, msg)
	}
	return fmt.Errorf("%w:DNSPod API 返回错误(code %s)", base, st.Code)
}

// dpDomainListResp 是经典 API Domain.List 的响应体(只取区名与总数)。
// 注意 status 在顶层是**对象**、在 domains[] 里是**字符串**(如 "ok"),两者不能共用一个类型。
type dpDomainListResp struct {
	Status dnspodStatus `json:"status"`
	Info   struct {
		DomainTotal string `json:"domain_total"`
	} `json:"info"`
	Domains []struct {
		Name string `json:"name"`
	} `json:"domains"`
}

// ListZones 列出该 login_token 账号下托管的全部域名:POST /Domain.List 逐页翻(length 上限 2000)。
// DNSPod 的「账号无域名」是 status code "9" 而非空列表,与 Record.List 的 "10" 同法按空处理。
func (c *dnspodClient) ListZones(ctx context.Context) ([]string, error) {
	const length = 200
	var out []string
	for offset := 0; ; offset += length {
		var list dpDomainListResp
		fields := url.Values{"type": {"mine"}, "offset": {strconv.Itoa(offset)}, "length": {strconv.Itoa(length)}}
		if err := c.post(ctx, "/Domain.List", fields, &list); err != nil {
			return nil, err
		}
		if list.Status.Code == "9" {
			return []string{}, nil
		}
		if list.Status.Code != "1" {
			return nil, dpStatusErr(ErrVerifyFailed, list.Status)
		}
		for _, d := range list.Domains {
			out = append(out, d.Name)
		}
		total, err := strconv.Atoi(strings.TrimSpace(list.Info.DomainTotal))
		if err != nil || len(out) >= total || len(list.Domains) == 0 {
			return sortNormalizedZones(out), nil
		}
	}
}

// EnsureARecord 为 zone 下的 name 建/改 A 记录指向 ip(幂等 upsert):
//  1. Record.List(sub_domain=<name 相对 zone>, record_type=A)找既有记录。
//  2. 找到且 value 不同 → Record.Modify;找到且相同 → no-op;没找到 → Record.Create。
//
// name 可能是 FQDN 也可能是相对 sub_domain;此处统一算出相对 zone 的 sub_domain(顶点 → "@")。
func (c *dnspodClient) EnsureARecord(ctx context.Context, zone, name, ip string) error {
	sub := subDomain(name, zone)

	var list dpRecordListResp
	listFields := url.Values{
		"domain":      {zone},
		"sub_domain":  {sub},
		"record_type": {"A"},
	}
	if err := c.post(ctx, "/Record.List", listFields, &list); err != nil {
		return wrapEnsure(err)
	}
	// DNSPod 在「无记录」时返回 code "10"(No records)而非空列表;两者都按「不存在」处理。
	if list.Status.Code != "1" && list.Status.Code != "10" {
		return dpStatusErr(ErrEnsureRecord, list.Status)
	}

	// 在返回的记录里找 sub 对应的 A 记录(DNSPod 的 record.Name 是相对 sub_domain,顶点为 "@")。
	var existing *dpRecord
	for i := range list.Records {
		r := list.Records[i]
		if strings.EqualFold(r.Type, "A") && strings.EqualFold(r.Name, sub) {
			existing = &list.Records[i]
			break
		}
	}

	if existing != nil {
		if existing.Value == ip {
			return nil // 已指向目标 IP,幂等 no-op。
		}
		modFields := url.Values{
			"domain":      {zone},
			"record_id":   {existing.ID},
			"sub_domain":  {sub},
			"record_type": {"A"},
			"record_line": {"默认"},
			"value":       {ip},
		}
		var mod dpMutateResp
		if err := c.post(ctx, "/Record.Modify", modFields, &mod); err != nil {
			return wrapEnsure(err)
		}
		if mod.Status.Code != "1" {
			return dpStatusErr(ErrEnsureRecord, mod.Status)
		}
		return nil
	}

	createFields := url.Values{
		"domain":      {zone},
		"sub_domain":  {sub},
		"record_type": {"A"},
		"record_line": {"默认"},
		"value":       {ip},
	}
	var create dpMutateResp
	if err := c.post(ctx, "/Record.Create", createFields, &create); err != nil {
		return wrapEnsure(err)
	}
	if create.Status.Code != "1" {
		return dpStatusErr(ErrEnsureRecord, create.Status)
	}
	return nil
}

// DeleteARecord 删除 zone 下 name 的 A 记录(幂等:无记录返回 nil)。Record.List 找 → Record.Remove。
func (c *dnspodClient) DeleteARecord(ctx context.Context, zone, name string) error {
	sub := subDomain(name, zone)

	var list dpRecordListResp
	listFields := url.Values{
		"domain":      {zone},
		"sub_domain":  {sub},
		"record_type": {"A"},
	}
	if err := c.post(ctx, "/Record.List", listFields, &list); err != nil {
		return fmt.Errorf("%w:%v", ErrDeleteRecord, err)
	}
	if list.Status.Code == "10" {
		return nil // 无记录 → 幂等成功。
	}
	if list.Status.Code != "1" {
		return dpStatusErr(ErrDeleteRecord, list.Status)
	}
	for i := range list.Records {
		r := list.Records[i]
		if strings.EqualFold(r.Type, "A") && strings.EqualFold(r.Name, sub) {
			var rm dpMutateResp
			if err := c.post(ctx, "/Record.Remove", url.Values{"domain": {zone}, "record_id": {r.ID}}, &rm); err != nil {
				return fmt.Errorf("%w:%v", ErrDeleteRecord, err)
			}
			if rm.Status.Code != "1" {
				return dpStatusErr(ErrDeleteRecord, rm.Status)
			}
			return nil
		}
	}
	return nil
}

// dpTXTRecords 取 zone 下 sub(相对根域)的全部 TXT 记录。
// base 决定错误家族(建/删两条路径的归类不同);DNSPod 的「无记录」是 code "10" 而非空列表,按空处理。
func (c *dnspodClient) dpTXTRecords(ctx context.Context, zone, sub string, base error) ([]dpRecord, error) {
	var list dpRecordListResp
	fields := url.Values{"domain": {zone}, "sub_domain": {sub}, "record_type": {"TXT"}}
	if err := c.post(ctx, "/Record.List", fields, &list); err != nil {
		return nil, wrapAs(err, base)
	}
	if list.Status.Code == "10" {
		return nil, nil
	}
	if list.Status.Code != "1" {
		return nil, dpStatusErr(base, list.Status)
	}
	var out []dpRecord
	for _, r := range list.Records {
		// sub_domain 筛选是「精确或子域」语义,回来仍要按 Type/Name 自己精确过滤(与 A 记录同法)。
		if strings.EqualFold(r.Type, "TXT") && strings.EqualFold(r.Name, sub) {
			out = append(out, r)
		}
	}
	return out, nil
}

// removeDNSPodRecords 逐条 Record.Remove(全删,不只删第一条)。
func (c *dnspodClient) removeDNSPodRecords(ctx context.Context, zone string, records []dpRecord, base error) error {
	for _, r := range records {
		var rm dpMutateResp
		if err := c.post(ctx, "/Record.Remove", url.Values{"domain": {zone}, "record_id": {r.ID}}, &rm); err != nil {
			return wrapAs(err, base)
		}
		if rm.Status.Code != "1" {
			return dpStatusErr(base, rm.Status)
		}
	}
	return nil
}

// EnsureTXTRecord 为 zone 下 name 落一条 TXT 记录,内容归一为 value(收敛成一条,理由见 TXTClient)。
func (c *dnspodClient) EnsureTXTRecord(ctx context.Context, zone, name, value string) error {
	sub := subDomain(name, zone)
	records, err := c.dpTXTRecords(ctx, zone, sub, ErrEnsureTXT)
	if err != nil {
		return err
	}
	lineFields := url.Values{
		"domain":      {zone},
		"sub_domain":  {sub},
		"record_type": {"TXT"},
		"record_line": {"默认"},
		"value":       {value},
	}
	if len(records) == 0 {
		var create dpMutateResp
		if err := c.post(ctx, "/Record.Create", lineFields, &create); err != nil {
			return wrapAs(err, ErrEnsureTXT)
		}
		if create.Status.Code != "1" {
			return dpStatusErr(ErrEnsureTXT, create.Status)
		}
		return nil
	}
	if err := c.removeDNSPodRecords(ctx, zone, records[1:], ErrEnsureTXT); err != nil {
		return err
	}
	if txtEqual(records[0].Value, value) {
		return nil // 已是目标值(多余条目已清干净),不再发 Modify。
	}
	modFields := url.Values{"record_id": {records[0].ID}}
	for k, v := range lineFields {
		modFields[k] = v
	}
	var mod dpMutateResp
	if err := c.post(ctx, "/Record.Modify", modFields, &mod); err != nil {
		return wrapAs(err, ErrEnsureTXT)
	}
	if mod.Status.Code != "1" {
		return dpStatusErr(ErrEnsureTXT, mod.Status)
	}
	return nil
}

// DeleteTXTRecord 删除 zone 下 name 的全部 TXT 记录(幂等:无记录返回 nil)。
func (c *dnspodClient) DeleteTXTRecord(ctx context.Context, zone, name string) error {
	sub := subDomain(name, zone)
	records, err := c.dpTXTRecords(ctx, zone, sub, ErrDeleteTXT)
	if err != nil {
		return err
	}
	return c.removeDNSPodRecords(ctx, zone, records, ErrDeleteTXT)
}
