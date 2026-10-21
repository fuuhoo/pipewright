package dnsprovider

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// stubLister 只实现 zoneLister,让区匹配能脱离六家记录语义单测。
type stubLister struct {
	zones []string
	err   error
	calls int
}

func (s *stubLister) ListZones(context.Context) ([]string, error) {
	s.calls++
	return s.zones, s.err
}

func TestNormalizeDomain(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Example.COM", "example.com"},
		{"  example.com.  ", "example.com"},
		{"example.com..", "example.com."}, // 只剥一个末尾点(FQDN 结尾点),多余点属非法输入不改形态
		{"", ""},
	} {
		if got := normalizeDomain(tc.in); got != tc.want {
			t.Fatalf("normalizeDomain(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMatchZoneLongestSuffixWins(t *testing.T) {
	zones := []string{"example.com", "preview.example.com", "other.cn"}
	for _, tc := range []struct {
		name string
		zone string // 空串 = 不覆盖
	}{
		{"app.example.com", "example.com"},
		{"EXAMPLE.COM.", "example.com"},                     // 大小写与结尾点都要能匹配
		{"example.com", "example.com"},                      // 顶点本身
		{"pr-1.preview.example.com", "preview.example.com"}, // zone cut:取更长的负责区
		{"preview.example.com", "preview.example.com"},
		{"app.other.cn", "other.cn"},
		{"example.com.evil.cn", ""}, // 后缀伪装:必须以「.」为界,不能被 evil 反串
		{"notexample.com", ""},      // 同后缀不同区,不能算覆盖
		{"", ""},
	} {
		got, ok := matchZone(tc.name, zones)
		if tc.zone == "" {
			if ok {
				t.Fatalf("matchZone(%q) 不该覆盖, got %q", tc.name, got)
			}
			continue
		}
		if !ok || got != tc.zone {
			t.Fatalf("matchZone(%q) = %q/%v, want %q", tc.name, got, ok, tc.zone)
		}
	}
}

func TestResolveZoneUsesAccountList(t *testing.T) {
	l := &stubLister{zones: []string{"Example.COM"}}
	zone, err := resolveZone(context.Background(), l, "_acme-challenge.app.example.com")
	if err != nil || zone != "example.com" {
		t.Fatalf("resolveZone: %q / %v", zone, err)
	}
	if l.calls != 1 {
		t.Fatalf("应只列一次区, got %d", l.calls)
	}
}

func TestResolveZoneNoHostedZone(t *testing.T) {
	l := &stubLister{zones: []string{"example.com"}}
	_, err := resolveZone(context.Background(), l, "_acme-challenge.other.com")
	if !errors.Is(err, ErrNoHostedZone) {
		t.Fatalf("清单里没有覆盖它的区应 ErrNoHostedZone, got %v", err)
	}
	// 人话要点名那个名字,否则用户不知道是哪条记录被拦了。
	if !strings.Contains(err.Error(), "_acme-challenge.other.com") {
		t.Fatalf("错误文本应带被拒的记录名, got %v", err)
	}
}

func TestResolveZonePropagatesListError(t *testing.T) {
	l := &stubLister{err: ErrVerifyFailed}
	if _, err := resolveZone(context.Background(), l, "app.example.com"); !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("列区失败应原样上抛, got %v", err)
	}
}

func TestSortNormalizedZones(t *testing.T) {
	got := sortNormalizedZones([]string{" b.com ", "Example.COM", "example.com.", "", "a.cn"})
	if len(got) != 3 || got[0] != "a.cn" || got[1] != "b.com" || got[2] != "example.com" {
		t.Fatalf("应归一、去空、去重、排序, got %v", got)
	}
	if empty := sortNormalizedZones(nil); empty == nil || len(empty) != 0 {
		t.Fatalf("空输入应回非 nil 空清单(JSON 里才是 []), got %#v", empty)
	}
}
