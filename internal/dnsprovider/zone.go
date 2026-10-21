package dnsprovider

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// zoneLister 是「列出凭据账号下托管的全部根域」这一项能力(DNSClient 已含它)。
// 单开一个窄接口只为让 resolveZone 能脱离 A/TXT 记录语义单测。
type zoneLister interface {
	ListZones(ctx context.Context) ([]string, error)
}

// normalizeDomain 把域名归一成比较用形态:小写、去首尾空白、去末尾点。
// 根域与记录名(FQDN)都用它 —— 两条线都要「同一形态才可比」,否则同名会落两条记录。
func normalizeDomain(s string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
}

// matchZone 在 zones 里为 name 挑出覆盖它的那个根域;都不覆盖 → ok=false。
//
// 多个区都能覆盖时取**最长**的那个:账号里同时托管 example.com 与 preview.example.com 很常见,
// 而 preview.example.com 一旦是独立区,它在 example.com 里就是一处 zone cut(NS 已交出去),
// 把 pr-1.preview 写进父区不会生效。取最长后缀才落在真正负责这段名字的区上。
func matchZone(name string, zones []string) (string, bool) {
	n := normalizeDomain(name)
	if n == "" {
		return "", false
	}
	best := ""
	for _, z := range zones {
		zz := normalizeDomain(z)
		if zz == "" {
			continue
		}
		if zz == n || strings.HasSuffix(n, "."+zz) {
			if len(zz) > len(best) {
				best = zz
			}
		}
	}
	return best, best != ""
}

// resolveZone 取该凭据账号托管的区清单,为 name 选出覆盖它的那个。
//
// 这道匹配是**权限边界**而不是格式问题:一条提供商记录代表「这份凭据能管账号里那些域」,
// 若把 name 原样透传,一个写错的 `_acme-challenge.other.com` 就会拿这份凭据去请求改别的域,
// 而且 DNS API 多半静默接受(阿里云按 DomainName 查、Cloudflare 按 name 找不到区才报错)。
// 清单里没有覆盖它的区 → ErrNoHostedZone。
func resolveZone(ctx context.Context, l zoneLister, name string) (string, error) {
	zones, err := l.ListZones(ctx)
	if err != nil {
		return "", err
	}
	zone, ok := matchZone(name, zones)
	if !ok {
		return "", fmt.Errorf("%w:%s 不在该凭据账号托管的任何域名下", ErrNoHostedZone, strings.TrimSpace(name))
	}
	return zone, nil
}

// sortNormalizedZones 归一、去空、去重后按字典序返回(各家 API 的分页顺序不稳定,清单要可比)。
func sortNormalizedZones(zones []string) []string {
	seen := make(map[string]struct{}, len(zones))
	out := make([]string, 0, len(zones))
	for _, z := range zones {
		zz := normalizeDomain(z)
		if zz == "" {
			continue
		}
		if _, dup := seen[zz]; dup {
			continue
		}
		seen[zz] = struct{}{}
		out = append(out, zz)
	}
	sort.Strings(out)
	return out
}
