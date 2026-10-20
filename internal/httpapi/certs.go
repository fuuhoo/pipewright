package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/fuuhoo/pipewright/internal/access"
	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/auth"
	"github.com/fuuhoo/pipewright/internal/cert"
	"github.com/fuuhoo/pipewright/internal/notify"
	"github.com/go-chi/chi/v5"
)

// certs.go —— 证书这条线的 HTTP 契约:登记 / 导入 / 签发 / 发布 / 取证书包。
//
// 两条轴各管一件事(与全站口径一致,见 docs/权限架构说明.md):
//   - 功能轴:读要 cert.view,写要 cert.manage。证书不是普通配置行 —— cert.manage 会往别人的
//     机器上写私钥并跑用户填的指令,所以它与 server.exec、container.operate 同档。
//   - 数据轴:凡是落到某台目标机的动作(新增/改/删落点、发布),除功能点外还要
//     Can(actor, KindServer, 那台机器, ActOperate)。缺这一道,持有 cert.manage 的角色就能
//     把证书铺进任何它「看得见入口」却不该碰的主机。
//
// 私钥(outgoing 唯一出口是 /bundle)绝不进列表、详情、错误体与审计:AC-SEC-01。

// 两个功能点 ID(internal/access/perms.go 的字典项)。写成常量是因为判定散在十几个 handler 里,
// 手抄错一个字母不会报错,只会让那个入口对所有人静默消失。
const (
	permCertView   = "cert.view"
	permCertManage = "cert.manage"
)

// certDTO 是证书列表/详情的响应体(camelCase)。列表与详情共用同一批字段,详情另带正文两段
// (certPem / chainPem —— 公钥材料,可以回显;私钥只在 BundleDTO 里出现)。
type certDTO struct {
	ID            string   `json:"id"`
	Domain        string   `json:"domain"`
	SANs          []string `json:"sans"`
	Source        string   `json:"source"`
	Status        string   `json:"status"`
	CA            string   `json:"ca"`
	DNSProviderID string   `json:"dnsProviderId"`
	AutoRenew     bool     `json:"autoRenew"`
	KeyAlgo       string   `json:"keyAlgo"`
	NotBefore     string   `json:"notBefore"`
	NotAfter      string   `json:"notAfter"`
	Issuer        string   `json:"issuer"`
	Serial        string   `json:"serial"`
	// HasKey 决定「发布 / 下载证书包」按钮能不能点亮:没有私钥,这两件事都做不了。
	HasKey    bool   `json:"hasKey"`
	LastError string `json:"lastError"`
	DaysLeft  int    `json:"daysLeft"`
	IssuedAt  string `json:"issuedAt"`
	CheckedAt string `json:"checkedAt"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
	CertPEM   string `json:"certPem,omitempty"`
	ChainPEM  string `json:"chainPem,omitempty"`
}

func toCertDTO(c *cert.Certificate, detail bool) certDTO {
	now := time.Now().UTC()
	out := certDTO{
		ID: c.ID, Domain: c.Domain, SANs: c.SANs, Source: c.Source, Status: c.Status,
		CA: c.CA, DNSProviderID: c.DNSProviderID, AutoRenew: c.AutoRenew, KeyAlgo: c.KeyAlgo,
		NotBefore: fmtCertTime(c.NotBefore), NotAfter: fmtCertTime(c.NotAfter),
		Issuer: c.Issuer, Serial: c.Serial, HasKey: c.HasKey, LastError: c.LastError,
		DaysLeft: c.DaysLeft(now),
		IssuedAt: fmtCertTime(c.IssuedAt), CheckedAt: fmtCertTime(c.CheckedAt),
		CreatedAt: fmtCertTime(c.CreatedAt), UpdatedAt: fmtCertTime(c.UpdatedAt),
	}
	if detail {
		out.CertPEM = c.CertPEM
		out.ChainPEM = c.ChainPEM
	}
	return out
}

// fmtCertTime 零值时间回空串:前端拿 "0001-01-01T00:00:00Z" 会当真日期渲染成「距今 6000 天」。
func fmtCertTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// certTargetDTO 是一个发布落点(配置 + 最近一次结果)。
type certTargetDTO struct {
	ID          string `json:"id"`
	CertID      string `json:"certId"`
	ServerID    string `json:"serverId"`
	CertPath    string `json:"certPath"`
	KeyPath     string `json:"keyPath"`
	PostCommand string `json:"postCommand"`
	AutoDeploy  bool   `json:"autoDeploy"`
	LastStatus  string `json:"lastStatus"`
	LastError   string `json:"lastError"`
	DeployedAt  string `json:"deployedAt"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

func toCertTargetDTO(t *cert.DeployTarget) certTargetDTO {
	return certTargetDTO{
		ID: t.ID, CertID: t.CertID, ServerID: t.ServerID,
		CertPath: t.CertPath, KeyPath: t.KeyPath, PostCommand: t.PostCommand,
		AutoDeploy: t.AutoDeploy, LastStatus: t.LastStatus, LastError: t.LastError,
		DeployedAt: fmtCertTime(t.DeployedAt),
		CreatedAt:  fmtCertTime(t.CreatedAt), UpdatedAt: fmtCertTime(t.UpdatedAt),
	}
}

// certAccountDTO 是一个 CA 的 ACME 账户视图。EAB 只回有没有、绝不回正文。
type certAccountDTO struct {
	ID           string `json:"id"`
	CA           string `json:"ca"`
	DirectoryURL string `json:"directoryUrl"`
	Contact      string `json:"contact"`
	AccountURL   string `json:"accountUrl"`
	EABKeyID     string `json:"eabKeyId"`
	HasEABKey    bool   `json:"hasEabKey"`
	RegisteredAt string `json:"registeredAt"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
}

func toCertAccountDTO(a *cert.Account) certAccountDTO {
	return certAccountDTO{
		ID: a.ID, CA: a.CA, DirectoryURL: a.DirectoryURL, Contact: a.Contact,
		AccountURL: a.AccountURL, EABKeyID: a.EABKeyID, HasEABKey: a.HasEABKey,
		RegisteredAt: fmtCertTime(a.RegisteredAt),
		CreatedAt:    fmtCertTime(a.CreatedAt), UpdatedAt: fmtCertTime(a.UpdatedAt),
	}
}

// certDeployResultDTO / certDeployDTO 是一次发布的逐落点结果。
// 整体 HTTP 状态始终是 200:「三台成功一台失败」不是一种 HTTP 错误,前端按 results 逐行说话。
type certDeployResultDTO struct {
	TargetID   string `json:"targetId"`
	ServerID   string `json:"serverId"`
	CertPath   string `json:"certPath"`
	KeyPath    string `json:"keyPath"`
	OK         bool   `json:"ok"`
	Error      string `json:"error"`
	DeployedAt string `json:"deployedAt"`
}

type certDeployDTO struct {
	CertID   string                `json:"certId"`
	Deployed int                   `json:"deployed"`
	Failed   int                   `json:"failed"`
	Results  []certDeployResultDTO `json:"results"`
}

// certBundleDTO 是证书包下载体:唯一带私钥的响应,挂 cert.manage 且每次留审计。
type certBundleDTO struct {
	Domain     string `json:"domain"`
	CertPEM    string `json:"certPem"`
	ChainPEM   string `json:"chainPem"`
	Fullchain  string `json:"fullchain"`
	PrivateKey string `json:"privateKey"`
}

// certErrorText 把上游(ACME problem document 之类)的长文本压成一行、限长的人读原因。
// 与 cert 侧写 last_error 的口径一致:换行压掉(审计一行一条)、限长(CA 的说明段能有好几 KB)。
// 这里出现的只有 CA 的判定文案、URL 与域名,没有凭据材料。
func certErrorText(err error) string {
	if err == nil {
		return ""
	}
	s := strings.Join(strings.Fields(err.Error()), " ")
	const maxRunes = 300
	if r := []rune(s); len(r) > maxRunes {
		return string(r[:maxRunes]) + "…"
	}
	return s
}

// writeCertError 把领域错误映射成契约错误码。顺序敏感:
// 「证书不存在」(404)必须排在其它判定之前,否则不存在会被伪装成格式错误。
func writeCertError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, cert.ErrNotFound):
		writeError(w, http.StatusNotFound, "cert_not_found", "证书不存在")
	case errors.Is(err, cert.ErrDomainExists):
		writeError(w, http.StatusConflict, "domain_exists", "这个域名已有一张证书,换正文请用「替换」")
	case errors.Is(err, cert.ErrInvalidDomain):
		writeError(w, http.StatusBadRequest, "invalid_cert", "域名不合法(小写字母/数字/-,通配只允许最左标签)")
	case errors.Is(err, cert.ErrNoCA):
		writeError(w, http.StatusBadRequest, "invalid_cert", "请选择受支持的 CA")
	case errors.Is(err, cert.ErrNoDNSProvider):
		writeError(w, http.StatusBadRequest, "invalid_cert", "DNS-01 签发要先选一个 DNS 提供商")
	case errors.Is(err, cert.ErrEmptyPEM):
		writeError(w, http.StatusUnprocessableEntity, "invalid_pem", "证书正文 PEM 不能为空")
	case errors.Is(err, cert.ErrNoCertBlock):
		writeError(w, http.StatusUnprocessableEntity, "invalid_pem", "PEM 里没有可解析的证书块(检查是否粘成了私钥)")
	case errors.Is(err, cert.ErrBadKeyPEM):
		writeError(w, http.StatusUnprocessableEntity, "invalid_pem", "私钥无法解析(加密私钥平台解不开,请给未加密的 PKCS#8)")
	case errors.Is(err, cert.ErrKeyMismatch):
		writeError(w, http.StatusUnprocessableEntity, "key_mismatch", "私钥与证书不匹配")
	case errors.Is(err, cert.ErrDomainMismatch):
		writeError(w, http.StatusUnprocessableEntity, "domain_mismatch", "证书正文覆盖的域名与登记的域名不一致")
	case errors.Is(err, cert.ErrNotACME):
		writeError(w, http.StatusConflict, "not_acme", "导入的证书平台无法代签,请换一张重新导入")
	case errors.Is(err, cert.ErrEmptyPath):
		writeError(w, http.StatusBadRequest, "invalid_target", "证书与私钥路径必须是绝对路径")
	case errors.Is(err, cert.ErrSamePath):
		writeError(w, http.StatusBadRequest, "invalid_target", "证书与私钥不能落在同一个路径")
	case errors.Is(err, cert.ErrServerNotFound):
		writeError(w, http.StatusUnprocessableEntity, "server_not_found", "落点引用的目标主机不存在")
	case errors.Is(err, cert.ErrVaultUnconfigured):
		writeError(w, http.StatusServiceUnavailable, "vault_unconfigured", "保险库未配置 master key,存不了私钥")
	case errors.Is(err, cert.ErrPublisherUnconfigured):
		writeError(w, http.StatusServiceUnavailable, "publisher_unconfigured", "本实例没接上目标机 SSH 执行能力,发不了")
	case errors.Is(err, cert.ErrTXTWriterUnconfigured):
		writeError(w, http.StatusServiceUnavailable, "dns_writer_unconfigured", "本实例没接上 DNS TXT 写入能力,签不了")
	case errors.Is(err, cert.ErrNoACMEAccount):
		writeError(w, http.StatusUnprocessableEntity, "no_acme_account", "这个 CA 还没注册 ACME 账户,先到「ACME 账户」注册")
	case errors.Is(err, cert.ErrNoDNS01Challenge):
		writeError(w, http.StatusUnprocessableEntity, "no_dns01", "该 CA 不提供 dns-01 验证方式")
	case errors.Is(err, cert.ErrEABRequired):
		writeError(w, http.StatusUnprocessableEntity, "eab_required", "这个 CA 需要 EAB 凭据(在账户注册时填)")
	case errors.Is(err, cert.ErrBadEABKey):
		writeError(w, http.StatusBadRequest, "invalid_eab", "EAB key 需是 CA 给的 base64 串")
	case errors.Is(err, cert.ErrIssueFailed), errors.Is(err, cert.ErrRegisterFailed):
		// 502:失败发生在平台与 CA 之间,原因对用户可行动(域名没解析 / TXT 没生效 / 速率限制),
		// 所以把压缩后的上游判定文案带回去,而不是只说一句「签发失败」。
		writeError(w, http.StatusBadGateway, "acme_failed", certErrorText(err))
	case errors.Is(err, cert.ErrDeployFailed):
		writeError(w, http.StatusInternalServerError, "deploy_failed", "发布失败,详见逐落点结果")
	default:
		writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
	}
}

// requireCertPerm 判定当前会话角色是否持有某个证书功能点。
//
// 为什么不用 accessGuardMiddleware:那条中间件按 URL 认出「在动哪个分组资源」,而证书本身不
// 归组(它是平台级设置,见 resolveGuardTarget)。证书要的是功能点判定,数据轴只在真的往某台
// 机器写东西时叠加,见 requireServerOperateForCert。
func requireCertPerm(w http.ResponseWriter, r *http.Request, point string) bool {
	sess, ok := sessionFromContext(r.Context())
	if !ok || sess == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
		return false
	}
	if !access.HasPerm(sess.Role, point) {
		writeError(w, http.StatusForbidden, "role_denied", "你的角色不允许这类证书操作,需要管理员调整角色")
		return false
	}
	return true
}

// requireServerOperateForCert 判定 actor 能不能把证书铺到某台目标机(数据轴)。
// 复用 requireTerminalOperate:它判的就是「Can(actor, KindServer, id, ActOperate)」,
// 与开终端同一档 —— 发布能在目标机上写文件并跑指令,权限方向不该比终端更低,也不该更高。
func requireServerOperateForCert(w http.ResponseWriter, r *http.Request, acc *access.Service, serverID string) bool {
	return requireTerminalOperate(w, r, acc, serverID)
}

// writeUnconfigured 是本文件统一的「服务没装配」出口:main 没注入 cert.Service 时端点全 503,
// 而不是 panic 或返回空清单让人以为「一张证书也没有」。
func writeCertUnconfigured(w http.ResponseWriter) {
	writeError(w, http.StatusServiceUnavailable, "internal", "证书服务未初始化")
}

// decodeCertBody 读一段限长的 JSON 体(粘贴的 PEM 也就几 KB,1MB 上限足够又不至于让人
// 拿这个端点灌磁盘)。格式错误一律 400,不回显原文 —— 原文里可能正是用户刚粘的私钥。
func decodeCertBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
		return false
	}
	return true
}

// makeListCertsHandler 返回 GET /api/certs handler → { items: [...] }。
// 列表不含正文与私钥(公钥材料也嫌大,详情再取)。
func makeListCertsHandler(svc cert.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeCertUnconfigured(w)
			return
		}
		if !requireCertPerm(w, r, permCertView) {
			return
		}
		certs, err := svc.List(r.Context())
		if err != nil {
			writeCertError(w, err)
			return
		}
		out := make([]certDTO, 0, len(certs))
		for i := range certs {
			out = append(out, toCertDTO(&certs[i], false))
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// makeGetCertHandler 返回 GET /api/certs/{id} handler(详情:带公钥正文,不带私钥)。
func makeGetCertHandler(svc cert.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeCertUnconfigured(w)
			return
		}
		if !requireCertPerm(w, r, permCertView) {
			return
		}
		c, err := svc.Get(r.Context(), chi.URLParam(r, "id"))
		if err != nil {
			writeCertError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toCertDTO(c, true))
	}
}

// makeListCAsHandler 返回 GET /api/certs/cas handler:内置 CA 清单(纯代码表,不查库)。
// 前端下拉要的是这份名单,所以它与字典同源:改这里就是改页面能选什么。
func makeListCAsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireCertPerm(w, r, permCertView) {
			return
		}
		type caDTO struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			DirectoryURL string `json:"directoryUrl"`
			RequiresEAB  bool   `json:"requiresEab"`
			Staging      bool   `json:"staging"`
		}
		out := make([]caDTO, 0)
		for _, ca := range cert.ListCAs() {
			out = append(out, caDTO{ID: ca.ID, Name: ca.Name, DirectoryURL: ca.DirectoryURL, RequiresEAB: ca.RequiresEAB, Staging: ca.Staging})
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// makeCreateCertHandler 返回 POST /api/certs handler:登记一张待签证书(source=acme)。
// 这里只落记录,不碰 CA —— 真正下单是 /issue,页面可以先把表单填完再决定何时申请。
func makeCreateCertHandler(svc cert.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeCertUnconfigured(w)
			return
		}
		if !requireCertPerm(w, r, permCertManage) {
			return
		}
		var req struct {
			Domain        string   `json:"domain"`
			SANs          []string `json:"sans"`
			CA            string   `json:"ca"`
			DNSProviderID string   `json:"dnsProviderId"`
			AutoRenew     bool     `json:"autoRenew"`
		}
		if !decodeCertBody(w, r, &req) {
			return
		}
		c, err := svc.Create(r.Context(), cert.CreateInput{
			Domain: req.Domain, SANs: req.SANs, CA: req.CA,
			DNSProviderID: req.DNSProviderID, AutoRenew: req.AutoRenew,
		})
		if err != nil {
			writeCertError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionCertCreate,
			TargetType: audit.TargetCert,
			TargetID:   c.ID,
			Detail:     map[string]any{"domain": c.Domain, "ca": c.CA, "dnsProviderId": c.DNSProviderID},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusCreated, toCertDTO(c, false))
	}
}

// importRequest 是「粘贴已有证书」与「就地替换正文」共用的请求体。
type importRequest struct {
	Domain        string `json:"domain"`
	CertPEM       string `json:"certPem"`
	KeyPEM        string `json:"keyPem"`
	ChainPEM      string `json:"chainPem"`
	DNSProviderID string `json:"dnsProviderId"`
	AutoRenew     bool   `json:"autoRenew"`
}

func (q importRequest) toInput() cert.ImportInput {
	return cert.ImportInput{
		Domain: q.Domain, CertPEM: q.CertPEM, KeyPEM: q.KeyPEM, ChainPEM: q.ChainPEM,
		DNSProviderID: q.DNSProviderID, AutoRenew: q.AutoRenew,
	}
}

// makeImportCertHandler 返回 POST /api/certs/import handler:导入用户自有证书(平台不碰 CA)。
func makeImportCertHandler(svc cert.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeCertUnconfigured(w)
			return
		}
		if !requireCertPerm(w, r, permCertManage) {
			return
		}
		var req importRequest
		if !decodeCertBody(w, r, &req) {
			return
		}
		c, err := svc.Import(r.Context(), req.toInput())
		if err != nil {
			writeCertError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionCertImport,
			TargetType: audit.TargetCert,
			TargetID:   c.ID,
			// detail 只留坐标:域名与是否带链。正文(含私钥)永不进审计。
			Detail: map[string]any{"domain": c.Domain, "hasChain": c.ChainPEM != "", "keyAlgo": c.KeyAlgo},
			IP:     clientIP(r),
		})
		writeJSON(w, http.StatusCreated, toCertDTO(c, false))
	}
}

// makeReplaceCertHandler 返回 POST /api/certs/{id}/replace handler:换正文不动落点。
// 单独一个 action 而不是复用 import:换掉的是一张正在服务的证书,出问题时这条是第一条要看的记录。
func makeReplaceCertHandler(svc cert.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeCertUnconfigured(w)
			return
		}
		if !requireCertPerm(w, r, permCertManage) {
			return
		}
		id := chi.URLParam(r, "id")
		var req importRequest
		if !decodeCertBody(w, r, &req) {
			return
		}
		c, err := svc.Replace(r.Context(), id, req.toInput())
		if err != nil {
			writeCertError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionCertReplace,
			TargetType: audit.TargetCert,
			TargetID:   c.ID,
			Detail:     map[string]any{"domain": c.Domain, "notAfter": fmtCertTime(c.NotAfter)},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusOK, toCertDTO(c, false))
	}
}

// makeIssueCertHandler 返回 POST /api/certs/{id}/issue handler:经 ACME DNS-01 签/续一张证书。
//
// 这条请求可能跑几分钟(TXT 生效等待 + 下单),路由的 WriteTimeout 是 0(见 cmd/pipewright/main.go),
// 所以不会被服务端写超时砍断;浏览器侧的等待由前端负责。
func makeIssueCertHandler(svc cert.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeCertUnconfigured(w)
			return
		}
		if !requireCertPerm(w, r, permCertManage) {
			return
		}
		id := chi.URLParam(r, "id")
		c, err := svc.Issue(r.Context(), id)
		if err != nil {
			recordAuditFromRequest(r, aud, ac, audit.Entry{
				Action:     audit.ActionCertIssue,
				TargetType: audit.TargetCert,
				TargetID:   id,
				Detail:     map[string]any{"ok": false, "reason": certErrorText(err)},
				IP:         clientIP(r),
			})
			writeCertError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionCertIssue,
			TargetType: audit.TargetCert,
			TargetID:   c.ID,
			Detail:     map[string]any{"ok": true, "domain": c.Domain, "notAfter": fmtCertTime(c.NotAfter)},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusOK, toCertDTO(c, false))
	}
}

// makeSetAutoRenewHandler 返回 POST /api/certs/{id}/auto-renew handler。
// 开关单独一条审计:自动续期意味着「平台会周期性地拿 DNS 权限去 CA 那里签新证书」,
// 事后要能回答是谁在什么时候打开的。
func makeSetAutoRenewHandler(svc cert.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeCertUnconfigured(w)
			return
		}
		if !requireCertPerm(w, r, permCertManage) {
			return
		}
		var req struct {
			AutoRenew bool `json:"autoRenew"`
		}
		if !decodeCertBody(w, r, &req) {
			return
		}
		c, err := svc.SetAutoRenew(r.Context(), chi.URLParam(r, "id"), req.AutoRenew)
		if err != nil {
			writeCertError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionCertAutoRenew,
			TargetType: audit.TargetCert,
			TargetID:   c.ID,
			Detail:     map[string]any{"domain": c.Domain, "autoRenew": c.AutoRenew},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusOK, toCertDTO(c, false))
	}
}

// makeDeleteCertHandler 返回 DELETE /api/certs/{id} handler(落点配置随行级联删除)。
func makeDeleteCertHandler(svc cert.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeCertUnconfigured(w)
			return
		}
		if !requireCertPerm(w, r, permCertManage) {
			return
		}
		id := chi.URLParam(r, "id")
		// 域名进审计是刻意的:删完这行就再也读不回来,审计里的域名是事后唯一的凭据。
		c, err := svc.Get(r.Context(), id)
		if err != nil {
			writeCertError(w, err)
			return
		}
		if err := svc.Delete(r.Context(), id); err != nil {
			writeCertError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionCertDelete,
			TargetType: audit.TargetCert,
			TargetID:   id,
			Detail:     map[string]any{"domain": c.Domain},
			IP:         clientIP(r),
		})
		w.WriteHeader(http.StatusNoContent)
	}
}

// makeListCertTargetsHandler 返回 GET /api/certs/{id}/targets handler。
func makeListCertTargetsHandler(svc cert.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeCertUnconfigured(w)
			return
		}
		if !requireCertPerm(w, r, permCertView) {
			return
		}
		targets, err := svc.Targets(r.Context(), chi.URLParam(r, "id"))
		if err != nil {
			writeCertError(w, err)
			return
		}
		out := make([]certTargetDTO, 0, len(targets))
		for i := range targets {
			out = append(out, toCertTargetDTO(&targets[i]))
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// targetRequest 是新增/修改落点的请求体(路径 + 生效指令 + 是否自动发布)。
type targetRequest struct {
	ServerID    string `json:"serverId"`
	CertPath    string `json:"certPath"`
	KeyPath     string `json:"keyPath"`
	PostCommand string `json:"postCommand"`
	AutoDeploy  bool   `json:"autoDeploy"`
}

func (q targetRequest) toInput() cert.DeployTargetInput {
	return cert.DeployTargetInput{
		ServerID: q.ServerID, CertPath: q.CertPath, KeyPath: q.KeyPath,
		PostCommand: q.PostCommand, AutoDeploy: q.AutoDeploy,
	}
}

// makeAddCertTargetHandler 返回 POST /api/certs/{id}/targets handler。
// 两条轴都要过:功能点 cert.manage + 那台机器的 ActOperate(否则就是往管不着的机器上写私钥)。
func makeAddCertTargetHandler(svc cert.Service, acc *access.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeCertUnconfigured(w)
			return
		}
		if !requireCertPerm(w, r, permCertManage) {
			return
		}
		certID := chi.URLParam(r, "id")
		var req targetRequest
		if !decodeCertBody(w, r, &req) {
			return
		}
		if !requireServerOperateForCert(w, r, acc, req.ServerID) {
			return
		}
		t, err := svc.AddTarget(r.Context(), certID, req.toInput())
		if err != nil {
			writeCertError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionCertTargetCreate,
			TargetType: audit.TargetCertDeployment,
			TargetID:   t.ID,
			Detail:     map[string]any{"certId": t.CertID, "serverId": t.ServerID, "certPath": t.CertPath, "keyPath": t.KeyPath},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusCreated, toCertTargetDTO(t))
	}
}

// makeUpdateCertTargetHandler 返回 PUT /api/certs/{id}/targets/{targetId} handler。
// 改的是「往哪台机器的哪个路径写」,所以新旧两台机器都要判能不能动:只判新的,就等于允许
// 把落点从自己管的主机改写(旧机器上的私钥再也无从收敛);只判旧的,则能把手伸进别人的机器。
func makeUpdateCertTargetHandler(svc cert.Service, acc *access.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeCertUnconfigured(w)
			return
		}
		if !requireCertPerm(w, r, permCertManage) {
			return
		}
		certID := chi.URLParam(r, "id")
		targetID := chi.URLParam(r, "targetId")
		var req targetRequest
		if !decodeCertBody(w, r, &req) {
			return
		}
		old, err := findCertTarget(r, svc, certID, targetID)
		if err != nil {
			writeCertError(w, err)
			return
		}
		if !requireServerOperateForCert(w, r, acc, old.ServerID) {
			return
		}
		if old.ServerID != strings.TrimSpace(req.ServerID) &&
			!requireServerOperateForCert(w, r, acc, req.ServerID) {
			return
		}
		t, err := svc.UpdateTarget(r.Context(), targetID, req.toInput())
		if err != nil {
			writeCertError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionCertTargetUpdate,
			TargetType: audit.TargetCertDeployment,
			TargetID:   t.ID,
			Detail:     map[string]any{"certId": t.CertID, "serverId": t.ServerID, "certPath": t.CertPath, "keyPath": t.KeyPath},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusOK, toCertTargetDTO(t))
	}
}

// makeDeleteCertTargetHandler 返回 DELETE /api/certs/{id}/targets/{targetId} handler。
// 删配置并不会把目标机上已落盘的证书与私钥收回来,但它是「以后不再往这里发」的边界变更,
// 要留痕,也要确认这台机器本来就在 actor 的操作范围内(否则连删都不必给)。
func makeDeleteCertTargetHandler(svc cert.Service, acc *access.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeCertUnconfigured(w)
			return
		}
		if !requireCertPerm(w, r, permCertManage) {
			return
		}
		certID := chi.URLParam(r, "id")
		targetID := chi.URLParam(r, "targetId")
		old, err := findCertTarget(r, svc, certID, targetID)
		if err != nil {
			writeCertError(w, err)
			return
		}
		if !requireServerOperateForCert(w, r, acc, old.ServerID) {
			return
		}
		if err := svc.DeleteTarget(r.Context(), targetID); err != nil {
			writeCertError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionCertTargetDelete,
			TargetType: audit.TargetCertDeployment,
			TargetID:   targetID,
			Detail:     map[string]any{"certId": certID, "serverId": old.ServerID, "keyPath": old.KeyPath},
			IP:         clientIP(r),
		})
		w.WriteHeader(http.StatusNoContent)
	}
}

// findCertTarget 在一张证书的落点清单里按 id 找一个落点,顺带完成归属校验。
//
// 为什么绕一圈而不是直接按 targetId 改:URL 里的 certId 与库里的落点归属必须对上,
// 否则 PUT /certs/A/targets/B(实际属于 C)会改到别人家去。领域层没有 GetTargetByID 的
// 可见方法,清单就够用。
func findCertTarget(r *http.Request, svc cert.Service, certID, targetID string) (*cert.DeployTarget, error) {
	targets, err := svc.Targets(r.Context(), certID)
	if err != nil {
		return nil, err
	}
	for i := range targets {
		if targets[i].ID == targetID {
			return &targets[i], nil
		}
	}
	return nil, cert.ErrNotFound
}

// makeDeployCertHandler 返回 POST /api/certs/{id}/deploy handler:把证书铺到全部落点。
//
// 发布前先逐个落点判 Can(KindServer, ActOperate):宁可整批不发,也不能发到一半才发现某台
// 不该动 —— 那会在别人的机器上留下一份没有对应记录的私钥。
// 逐落点结果始终 200 返回(部分失败是业务结果,不是 HTTP 错误)。
func makeDeployCertHandler(svc cert.Service, acc *access.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeCertUnconfigured(w)
			return
		}
		if !requireCertPerm(w, r, permCertManage) {
			return
		}
		id := chi.URLParam(r, "id")
		targets, err := svc.Targets(r.Context(), id)
		if err != nil {
			writeCertError(w, err)
			return
		}
		for i := range targets {
			if !requireServerOperateForCert(w, r, acc, targets[i].ServerID) {
				return
			}
		}
		report, err := svc.Deploy(r.Context(), id)
		if err != nil {
			recordAuditFromRequest(r, aud, ac, audit.Entry{
				Action:     audit.ActionCertDeploy,
				TargetType: audit.TargetCert,
				TargetID:   id,
				Detail:     map[string]any{"ok": false, "reason": certErrorText(err), "targets": len(targets)},
				IP:         clientIP(r),
			})
			writeCertError(w, err)
			return
		}
		dto := certDeployDTO{
			CertID:   report.CertID,
			Deployed: report.Deployed(),
			Failed:   report.Failed(),
			Results:  make([]certDeployResultDTO, 0, len(report.Results)),
		}
		for _, res := range report.Results {
			dto.Results = append(dto.Results, certDeployResultDTO{
				TargetID: res.TargetID, ServerID: res.ServerID,
				CertPath: res.CertPath, KeyPath: res.KeyPath,
				OK: res.OK, Error: res.Error, DeployedAt: fmtCertTime(res.DeployedAt),
			})
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionCertDeploy,
			TargetType: audit.TargetCert,
			TargetID:   id,
			// detail 只留计数与坐标:落点上的错误串由领域层保证不含私钥正文。
			Detail: map[string]any{"deployed": dto.Deployed, "failed": dto.Failed, "targets": len(dto.Results)},
			IP:     clientIP(r),
		})
		writeJSON(w, http.StatusOK, dto)
	}
}

// makeCertBundleHandler 返回 GET /api/certs/{id}/bundle handler:证书包(含私钥明文)。
//
// 这是全站唯一把私钥放进响应体的端点,三道收口:要 cert.manage、要审计、响应带 no-store。
// 前端拿到后自行拼成文件下载,所以这里回 JSON 而不是 zip —— 少一层 Content-Type 协商,
// 也就少一处「浏览器缓存了这个响应」的可能。
func makeCertBundleHandler(svc cert.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeCertUnconfigured(w)
			return
		}
		if !requireCertPerm(w, r, permCertManage) {
			return
		}
		id := chi.URLParam(r, "id")
		b, err := svc.Bundle(r.Context(), id)
		if err != nil {
			writeCertError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionCertBundleDownload,
			TargetType: audit.TargetCert,
			TargetID:   id,
			Detail:     map[string]any{"domain": b.Domain},
			IP:         clientIP(r),
		})
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, certBundleDTO{
			Domain: b.Domain, CertPEM: b.CertPEM, ChainPEM: b.ChainPEM,
			Fullchain: b.Fullchain(), PrivateKey: b.KeyPEM,
		})
	}
}

// makeListCertAccountsHandler 返回 GET /api/certs/accounts handler:已注册的 ACME 账户。
func makeListCertAccountsHandler(svc cert.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeCertUnconfigured(w)
			return
		}
		if !requireCertPerm(w, r, permCertView) {
			return
		}
		accounts, err := svc.Accounts(r.Context())
		if err != nil {
			writeCertError(w, err)
			return
		}
		out := make([]certAccountDTO, 0, len(accounts))
		for i := range accounts {
			out = append(out, toCertAccountDTO(&accounts[i]))
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// makeRegisterCertAccountHandler 返回 POST /api/certs/accounts handler:注册(或就地更新)一个 CA 的 ACME 账户。
// EAB key 只写:入库前封成密文,响应与审计都只有 eabKeyId 与有没有 key 的布尔。
func makeRegisterCertAccountHandler(svc cert.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeCertUnconfigured(w)
			return
		}
		if !requireCertPerm(w, r, permCertManage) {
			return
		}
		var req struct {
			CA       string `json:"ca"`
			Contact  string `json:"contact"`
			EABKeyID string `json:"eabKeyId"`
			EABKey   string `json:"eabKey"`
		}
		if !decodeCertBody(w, r, &req) {
			return
		}
		a, err := svc.RegisterAccount(r.Context(), cert.AccountInput{
			CA: req.CA, Contact: req.Contact, EABKeyID: req.EABKeyID, EABKey: req.EABKey,
		})
		if err != nil {
			writeCertError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionCertAccountRegister,
			TargetType: audit.TargetACMEAccount,
			TargetID:   a.ID,
			Detail:     map[string]any{"ca": a.CA, "accountUrl": a.AccountURL, "hasEabKey": a.HasEABKey},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusOK, toCertAccountDTO(a))
	}
}

// certCheckItemDTO / certCheckDTO 是一轮「立即检查」的汇总(逐张说话,页面按三组分列)。
type certCheckItemDTO struct {
	CertID   string `json:"certId"`
	Domain   string `json:"domain"`
	Status   string `json:"status"`
	DaysLeft int    `json:"daysLeft"`
	Detail   string `json:"detail"`
	Deploy   string `json:"deploy"`
}

type certCheckDTO struct {
	Checked  int                `json:"checked"`
	Expiring []certCheckItemDTO `json:"expiring"`
	Renewed  []certCheckItemDTO `json:"renewed"`
	Failed   []certCheckItemDTO `json:"failed"`
}

func toCertCheckItems(items []cert.CheckItem) []certCheckItemDTO {
	out := make([]certCheckItemDTO, 0, len(items))
	for _, it := range items {
		out = append(out, certCheckItemDTO{
			CertID: it.CertID, Domain: it.Domain, Status: it.Status,
			DaysLeft: it.DaysLeft, Detail: it.Detail, Deploy: it.Deploy,
		})
	}
	return out
}

// makeCheckCertsHandler 返回 POST /api/certs/check handler:立刻扫一遍全部证书。
//
// 这不是只读接口:落在续期窗口里、开了自动续期的 acme 证书会**当场真的续一次**并按配置铺发,
// 所以它要 cert.manage、要留审计,而且响应可能要等几十秒(DNS-01 要等 TXT 记录生效)。
// notifier 传 nil:手动这一趟只把结果原样报给页面,既不发通知也不吃掉 7 天的自动提醒额度。
func makeCheckCertsHandler(svc cert.Service, aud audit.Recorder, ac auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeCertUnconfigured(w)
			return
		}
		if !requireCertPerm(w, r, permCertManage) {
			return
		}
		rep, err := svc.CheckOnce(r.Context(), nil)
		if err != nil {
			recordAuditFromRequest(r, aud, ac, audit.Entry{
				Action:     audit.ActionCertCheck,
				TargetType: audit.TargetCert,
				Detail:     map[string]any{"ok": false, "reason": certErrorText(err)},
				IP:         clientIP(r),
			})
			writeCertError(w, err)
			return
		}
		recordAuditFromRequest(r, aud, ac, audit.Entry{
			Action:     audit.ActionCertCheck,
			TargetType: audit.TargetCert,
			Detail: map[string]any{
				"ok": true, "checked": rep.Checked,
				"renewed": len(rep.Renewed), "failed": len(rep.Failed), "expiring": len(rep.Expiring),
			},
			IP: clientIP(r),
		})
		writeJSON(w, http.StatusOK, certCheckDTO{
			Checked:  rep.Checked,
			Expiring: toCertCheckItems(rep.Expiring),
			Renewed:  toCertCheckItems(rep.Renewed),
			Failed:   toCertCheckItems(rep.Failed),
		})
	}
}

// NewCertNotifier 构造 cert.Notifier:三条监控事件映射到 notify 的三个事件枚举,走**全局路由**
// (证书不属任何项目)。与 NewAnomalyNotifier 同一套解耦方式:cert 包不 import notify。
func NewCertNotifier(notifySvc notify.Service) cert.Notifier {
	return certNotifier{notify: notifySvc}
}

// certEventFor 把领域监控种类映射成通知事件枚举。
func certEventFor(kind string) string {
	switch kind {
	case cert.MonitorExpiring:
		return notify.EventCertExpiring
	case cert.MonitorRenewed:
		return notify.EventCertRenewed
	case cert.MonitorRenewFailed:
		return notify.EventCertRenewFailed
	default:
		return ""
	}
}

// certNotifier 把一条证书监控事件映射为通知事件并经渠道异步发送。
type certNotifier struct{ notify notify.Service }

// NotifyCertEvent 实现 cert.Notifier。fire-and-forget:扫描那一轮不该为一条通知等慢渠道。
// 独立 ctx + 超时(与 anomalyNotifier 同口径),失败只记日志。
// Project 位放的是域名:标题渲染成「[example.com] 证书即将到期」,Status 带那行该做什么。
func (n certNotifier) NotifyCertEvent(_ context.Context, e cert.CertEvent) {
	if n.notify == nil {
		return
	}
	event := certEventFor(e.Kind)
	if event == "" {
		log.Printf("[cert] 未知的监控种类 %q,已跳过", e.Kind)
		return
	}
	go func() {
		sendCtx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		if err := n.notify.RouteEventForProject(sendCtx, "", event, notify.TemplateVars{
			Project: e.Domain,
			Status:  e.Message,
			Event:   event,
		}); err != nil {
			log.Printf("[cert] 证书 %s 的 %s 通知 best-effort 失败:%v", e.Domain, event, err)
		}
	}()
}
