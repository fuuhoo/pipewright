package dnsprovider

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/fuuhoo/pipewright/internal/vault"
)

// vaultReader 抽象「按 id 取凭据明文(不刷新 last_used_at)+ 校验存在」的最小能力(注入便于单测)。
// 复用 vault.Vault 的 Reveal/Exists;本包只读取 DNS token 明文,绝不入库/日志/响应。
type vaultReader interface {
	Reveal(id string) (string, error)
	Exists(id string) (bool, error)
}

// RouteCreator 抽象「创建一条 DNS-01 反代路由」的能力(由上层用 proxy.Service 适配注入,避免本包
// import proxy 形成环)。实现须落库一条带 DNS 提供商引用的路由并完成 Caddy 编排,返回 routeID。
type RouteCreator interface {
	// CreateDNS01Route 创建一条 DNS-01 反代路由(域名 + DNS 提供商引用 + 上游)。返回新路由 id。
	CreateDNS01Route(ctx context.Context, in CreateDNS01RouteInput) (routeID string, err error)
}

// CreateDNS01RouteInput 是创建 DNS-01 反代路由的入参(冻结契约,proxy 适配器消费)。
type CreateDNS01RouteInput struct {
	ServerID          string
	Domain            string
	UpstreamContainer string
	UpstreamPort      int
	DNSProviderID     string
}

// dialFactory 据提供商类型 + token 造一个 DNSClient(注入便于单测;生产用 prodDialFactory)。
type dialFactory func(providerType, token string) DNSClient

// prodDialFactory 是生产工厂:真实 net/http 客户端(Cloudflare 真连,其余桩)。
func prodDialFactory(providerType, token string) DNSClient {
	return newDNSClient(providerType, token, nil, "")
}

// service 是 store + vault + routeCreator + dialFactory 支撑的 Service 实现。
type service struct {
	store   *Store
	vault   vaultReader
	routes  RouteCreator
	dial    dialFactory
	randGen func(n int) (string, error) // 随机子域名后缀生成器(注入便于单测;默认 crypto/rand)
}

// New 构造 Service。
//   - db:参数化 SQL 触库。
//   - v:凭据保险库(取 DNS token 明文);nil/未配置时 Zones/Allocate 返回 ErrVaultUnconfigured。
//   - routes:DNS-01 反代路由创建器(proxy 适配器);nil 时 AllocateSubdomain 返回 ErrAllocate。
//
// 不在此做任何重活(无 init 副作用)。
func New(db *sql.DB, v vault.Vault, routes RouteCreator) Service {
	var vr vaultReader
	if v != nil {
		vr = v
	}
	return &service{
		store:   NewStore(db),
		vault:   vr,
		routes:  routes,
		dial:    prodDialFactory,
		randGen: randSuffix,
	}
}

func (s *service) List(ctx context.Context) ([]Provider, error) { return s.store.list(ctx) }

func (s *service) Get(ctx context.Context, id string) (*Provider, error) { return s.store.get(ctx, id) }

func (s *service) Create(ctx context.Context, in CreateInput) (*Provider, error) {
	in.Type = strings.ToLower(strings.TrimSpace(in.Type))
	in.Name = strings.TrimSpace(in.Name)
	in.CredentialID = strings.TrimSpace(in.CredentialID)
	if err := validateCreate(in); err != nil {
		return nil, err
	}
	// 校验凭据存在(避免悬挂引用);vault 未配置时仍允许登记(凭据存在性在 Verify/Allocate 时再校）。
	if s.vault != nil {
		ok, err := s.vault.Exists(in.CredentialID)
		if err != nil {
			if !errors.Is(err, vault.ErrVaultUnconfigured) {
				return nil, fmt.Errorf("dnsprovider: check credential: %w", err)
			}
		} else if !ok {
			return nil, ErrCredentialNotFound
		}
	}
	p := newProvider(in)
	if err := s.store.insert(ctx, p); err != nil {
		return nil, err
	}
	return s.store.get(ctx, p.ID)
}

func (s *service) Delete(ctx context.Context, id string) error { return s.store.del(ctx, id) }

// Zones 实时列出该凭据账号里托管的全部根域(归一、去重、字典序)。
//
// 一次列表调用同时回答两件事:凭据能不能用(报错即不可用),以及它能管哪些域(zone 推导与
// 前端域名下拉的候选都来自这里)。账号下一条域名都没有并不报错——返回空清单,让调用方把
// 「凭据有效但账号里没有域」如实说给用户。
func (s *service) Zones(ctx context.Context, id string) ([]string, error) {
	p, err := s.store.get(ctx, id)
	if err != nil {
		return nil, err
	}
	client, err := s.clientFor(p)
	if err != nil {
		return nil, err
	}
	return client.ListZones(ctx)
}

// DeleteSubdomainRecord 删除 fqdn 所属区(按 fqdn 从该凭据账号的域名清单推导)的 A 记录
// (回收预览/子域名时清 DNS;幂等)。providerID 不存在 → ErrNotFound;vault 未配/凭据缺失 → 相应错误。
func (s *service) DeleteSubdomainRecord(ctx context.Context, providerID, fqdn string) error {
	p, err := s.store.get(ctx, providerID)
	if err != nil {
		return err
	}
	name := normalizeDomain(fqdn)
	if name == "" {
		return fmt.Errorf("%w:记录名为空", ErrNoHostedZone)
	}
	client, err := s.clientFor(p)
	if err != nil {
		return err
	}
	zone, err := resolveZone(ctx, client, name)
	if err != nil {
		return err
	}
	return client.DeleteARecord(ctx, zone, name)
}

// ProviderType 返回某提供商类型(不取 token);不存在 → ok=false。
func (s *service) ProviderType(ctx context.Context, providerID string) (string, bool, error) {
	p, err := s.store.get(ctx, providerID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	return p.Type, true, nil
}

// ResolveToken 取某提供商 (类型, token 明文)(供 proxy apply 渲染 DNS-01)。token 用完即弃。
func (s *service) ResolveToken(ctx context.Context, providerID string) (string, string, bool, error) {
	p, err := s.store.get(ctx, providerID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", "", false, nil
		}
		return "", "", false, err
	}
	if s.vault == nil {
		return "", "", false, ErrVaultUnconfigured
	}
	token, err := s.vault.Reveal(p.CredentialID)
	if err != nil {
		switch {
		case errors.Is(err, vault.ErrVaultUnconfigured):
			return "", "", false, ErrVaultUnconfigured
		case errors.Is(err, vault.ErrNotFound):
			return "", "", false, ErrCredentialNotFound
		default:
			return "", "", false, ErrVerifyFailed
		}
	}
	return p.Type, token, true, nil
}

// clientFor 取该提供商凭据明文 → 造对应 DNSClient。token 明文仅进程内,调用方用完即弃。
func (s *service) clientFor(p *Provider) (DNSClient, error) {
	if s.vault == nil {
		return nil, ErrVaultUnconfigured
	}
	token, err := s.vault.Reveal(p.CredentialID)
	if err != nil {
		switch {
		case errors.Is(err, vault.ErrVaultUnconfigured):
			return nil, ErrVaultUnconfigured
		case errors.Is(err, vault.ErrNotFound):
			return nil, ErrCredentialNotFound
		default:
			// 解密等内部错误:不泄漏细节。
			return nil, ErrVerifyFailed
		}
	}
	client := s.dial(p.Type, token)
	// 显式清本地 token 引用(client 已持有副本用于建连)。
	token = "" //nolint:ineffassign // 安全归零意图
	_ = token
	return client, nil
}

// EnsureTXTRecord 为 name 落一条 TXT 记录(平台自签证书的 DNS-01 校验题),区按 name 从该凭据
// 账号的域名清单推导。
func (s *service) EnsureTXTRecord(ctx context.Context, providerID, name, value string) error {
	if strings.TrimSpace(value) == "" {
		return ErrEmptyTXTValue
	}
	client, zone, fqdn, err := s.txtTarget(ctx, providerID, name)
	if err != nil {
		return err
	}
	return client.EnsureTXTRecord(ctx, zone, fqdn, value)
}

// DeleteTXTRecord 清理 name 的全部 TXT 记录(幂等)。
func (s *service) DeleteTXTRecord(ctx context.Context, providerID, name string) error {
	client, zone, fqdn, err := s.txtTarget(ctx, providerID, name)
	if err != nil {
		return err
	}
	return client.DeleteTXTRecord(ctx, zone, fqdn)
}

// txtTarget 把「读提供商 → 取凭据 → 判 TXT 能力 → 由记录名推区」串成一条,返回 (客户端, zone, 归一后的 FQDN)。
//
// 顺序是有意的,两处都不能换:
//   - 能力判定排在推区之前:不会写 TXT 的客户端拿不到清单也就推不出区,错误会退化成
//     「不在任何域名下」,而真正该说的是「这个提供商不支持写 TXT」。
//   - 区匹配是**权限边界**而不是格式问题:一条提供商记录代表「这份凭据能管账号里那些域」,
//     若把 name 原样透传,一个写错的 `_acme-challenge.other.com` 就会拿这份凭据去请求改别的域,
//     而且 DNS API 多半静默接受(阿里云按 DomainName 查、Cloudflare 按 name 找不到区才报错)。
func (s *service) txtTarget(ctx context.Context, providerID, name string) (TXTClient, string, string, error) {
	p, err := s.store.get(ctx, providerID)
	if err != nil {
		return nil, "", "", err
	}
	fqdn := normalizeDomain(name)
	if fqdn == "" {
		return nil, "", "", fmt.Errorf("%w:记录名为空", ErrNoHostedZone)
	}
	client, err := s.clientFor(p)
	if err != nil {
		return nil, "", "", err
	}
	tc, ok := client.(TXTClient)
	if !ok {
		return nil, "", "", fmt.Errorf("%w(%s 不支持写 TXT 记录,平台自签证书需要它)", ErrProviderNotImplemented, p.Type)
	}
	zone, err := resolveZone(ctx, client, fqdn)
	if err != nil {
		return nil, "", "", err
	}
	return tc, zone, fqdn, nil
}

// AllocateSubdomain 瞬时分配子域名(E3.3 + E3.4):
//  1. 取提供商 + 校验入参(上游容器/端口、宿主机 IP、要落到哪个根域)。
//  2. 列该凭据账号托管的域名(顺带证明凭据可用),确认 in.Zone 确实在它覆盖范围内。
//  3. crypto/rand 挑可读后缀,组成 app-<6char>.<zone>;若与已建路由域名冲突则重试(有限次)。
//  4. 经 DNSClient 建 A 记录指向 hostIP。
//  5. 经 RouteCreator 创建一条 DNS-01 反代路由(证书即刻可签)。
func (s *service) AllocateSubdomain(ctx context.Context, in AllocateInput) (*RouteRef, error) {
	if s.routes == nil {
		return nil, fmt.Errorf("%w:路由创建器未装配", ErrAllocate)
	}
	p, err := s.store.get(ctx, in.ProviderID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.ServerID) == "" {
		return nil, ErrAllocate
	}
	if strings.TrimSpace(in.UpstreamContainer) == "" {
		return nil, ErrAllocate
	}
	if in.UpstreamPort < 1 || in.UpstreamPort > 65535 {
		return nil, ErrAllocate
	}
	if !validIPv4(in.HostIP) {
		// 宿主机 IP 必须是合法 IPv4(由上层据 server.Host 解析得出,非用户自由文本)。
		return nil, fmt.Errorf("%w:宿主机 IP 非法", ErrAllocate)
	}
	zone := normalizeDomain(in.Zone)
	if !ValidBaseDomain(zone) {
		// 随机后缀推不出区,故区必须由调用方给;给了仍要先过格式闸。
		return nil, fmt.Errorf("%w:根域格式非法", ErrAllocate)
	}

	client, err := s.clientFor(p)
	if err != nil {
		return nil, err
	}
	// 一次列表:既证明凭据可用,又把「这个区归不归这份凭据管」钉成事实(杜绝越权写记录)。
	// 传 zone 本身即可 —— 它要么就是账号里的区,要么是某个区的子段(如只托管 example.com 却选
	// preview.example.com),两种都该由父区负责;两个方向都不沾的域必须硬失败。
	hosted, err := client.ListZones(ctx)
	if err != nil {
		return nil, err
	}
	target, ok := matchZone(zone, hosted)
	if !ok {
		return nil, fmt.Errorf("%w:%s 不在该凭据账号托管的任何域名下", ErrNoHostedZone, zone)
	}

	// 有限次随机挑子域名,避开与已建路由域名冲突(由 RouteCreator 的 domain 唯一约束兜底)。
	const maxAttempts = 5
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		suffix, gerr := s.randGen(6)
		if gerr != nil {
			return nil, fmt.Errorf("%w:随机子域名生成失败", ErrAllocate)
		}
		domain := "app-" + suffix + "." + zone

		// 建 A 记录指向宿主机 IP(幂等)。确定性错误(未实现 / 凭据格式非法)直接上抛,不重试;
		// 其余(瞬时网络 / API)记为 lastErr 后换下一个后缀重试。
		if err := client.EnsureARecord(ctx, target, domain, in.HostIP); err != nil {
			if errors.Is(err, ErrProviderNotImplemented) || errors.Is(err, ErrInvalidCredential) {
				return nil, err
			}
			lastErr = err
			continue
		}

		// 创建 DNS-01 反代路由。domain 唯一冲突(极小概率随机撞车)→ 重试下一个后缀。
		routeID, rerr := s.routes.CreateDNS01Route(ctx, CreateDNS01RouteInput{
			ServerID:          in.ServerID,
			Domain:            domain,
			UpstreamContainer: in.UpstreamContainer,
			UpstreamPort:      in.UpstreamPort,
			DNSProviderID:     p.ID,
		})
		if rerr != nil {
			if isDomainCollision(rerr) {
				lastErr = rerr
				continue
			}
			return nil, rerr
		}
		return &RouteRef{RouteID: routeID, Domain: domain, ProviderID: p.ID}, nil
	}
	if lastErr != nil {
		return nil, fmt.Errorf("%w:多次尝试仍失败", ErrAllocate)
	}
	return nil, ErrAllocate
}

// AllocateFQDN 为一个**指定的** FQDN 建 A 记录指向 hostIP → 创建一条 DNS-01 反代路由(R4 E4.1)。
// 与 AllocateSubdomain 共享校验/建记录/建路由逻辑,但子域名由调用方给定(不随机挑),用于
// 幂等可预测的预览域名 pr-<n>-<proj>.base。
//   - in.Subdomain 必须非空、且是某个托管区的**子名**(不是区本身);否则 ErrAllocate。
//   - 它落在哪个区由账号域名清单推导,与签发 TXT 同一套(见 zone.go)。
//   - 域名已占用(同 PR 重复分配且未先回收旧路由)→ 透传 proxy 的 domain-taken 错误,供上层处置。
func (s *service) AllocateFQDN(ctx context.Context, in AllocateInput) (*RouteRef, error) {
	if s.routes == nil {
		return nil, fmt.Errorf("%w:路由创建器未装配", ErrAllocate)
	}
	p, err := s.store.get(ctx, in.ProviderID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.ServerID) == "" || strings.TrimSpace(in.UpstreamContainer) == "" {
		return nil, ErrAllocate
	}
	if in.UpstreamPort < 1 || in.UpstreamPort > 65535 {
		return nil, ErrAllocate
	}
	if !validIPv4(in.HostIP) {
		return nil, fmt.Errorf("%w:宿主机 IP 非法", ErrAllocate)
	}
	domain := normalizeDomain(in.Subdomain)
	if domain == "" {
		return nil, fmt.Errorf("%w:子域名为空", ErrAllocate)
	}

	client, cerr := s.clientFor(p)
	if cerr != nil {
		return nil, cerr
	}
	// 区由记录名推导 —— 这道匹配是权限边界(拿不到区 / 名字不在账号任何域下都硬失败)。
	zone, zerr := resolveZone(ctx, client, domain)
	if zerr != nil {
		return nil, zerr
	}
	if domain == zone {
		// 预览域必须是区下的一个子名:直接给区本身(如 example.com)会把宿主机 IP 指到顶点,
		// 顶点的解析通常还归网站自身,不是本功能该动的地方。
		return nil, fmt.Errorf("%w:子域名须落在托管域名下", ErrAllocate)
	}
	// 建 A 记录指向宿主机 IP(幂等)。未实现 / 凭据格式非法 → 直接上抛。
	if err := client.EnsureARecord(ctx, zone, domain, in.HostIP); err != nil {
		return nil, err
	}
	routeID, rerr := s.routes.CreateDNS01Route(ctx, CreateDNS01RouteInput{
		ServerID:          in.ServerID,
		Domain:            domain,
		UpstreamContainer: in.UpstreamContainer,
		UpstreamPort:      in.UpstreamPort,
		DNSProviderID:     p.ID,
	})
	if rerr != nil {
		return nil, rerr
	}
	return &RouteRef{RouteID: routeID, Domain: domain, ProviderID: p.ID}, nil
}

// isDomainCollision 判定路由创建错误是否为「域名已占用」(供子域名重试)。
// 用文本匹配(避免 import proxy 形成环;proxy.ErrDomainTaken 的文本含 "domain already in use")。
func isDomainCollision(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "domain already in use")
}

// randAlphabet 是可读子域名后缀字符集(去掉易混淆的 0/o/1/l/i,纯小写+数字)。
const randAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// randSuffix 经 crypto/rand 生成长度为 n 的可读后缀(无 math/rand、无 time 依赖,测试稳定)。
func randSuffix(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i, x := range b {
		out[i] = randAlphabet[int(x)%len(randAlphabet)]
	}
	return string(out), nil
}

// validIPv4 报告 s 是否为合法 IPv4 地址(子域名 A 记录指向宿主机 IPv4)。
func validIPv4(s string) bool {
	ip := net.ParseIP(strings.TrimSpace(s))
	return ip != nil && ip.To4() != nil
}
