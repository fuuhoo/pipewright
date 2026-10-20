package notify

// cert_events_test.go 替证书监控的三个事件把三道关:
//   - 枚举合法(能不能存进路由配置)、
//   - 清单可见(前端的事件下拉读 Events(),漏了就永远配不出这条路由)、
//   - 八语种标签齐全(漏一种语言,那个语言的界面上就会掉出中文源串)。

import (
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/i18n"
)

var certEvents = []string{EventCertExpiring, EventCertRenewed, EventCertRenewFailed}

func TestCertEventsAreRoutableAndListed(t *testing.T) {
	listed := map[string]bool{}
	for _, e := range Events() {
		listed[e] = true
	}
	for _, e := range certEvents {
		if !validEvent(e) {
			t.Errorf("%s 没进 validEvent:路由存不下来", e)
		}
		if !listed[e] {
			t.Errorf("%s 不在 Events() 里:前端事件清单看不到它", e)
		}
	}
}

// 证书事件是平台级的(不属于任何项目),但通知模板照旧走「项目」槽位放域名 ——
// 用户收到的是「[shop.example.com] 证书已续期」,一眼就知道是哪张证书。
func TestCertEventPayloadNamesTheDomain(t *testing.T) {
	p := EventPayload(i18n.Default, EventCertRenewed, "shop.example.com", "", "", "已续期到 2027-01-18T12:00:00Z;已铺发 2 个落点", 0)
	if !strings.Contains(p.Title, "shop.example.com") {
		t.Errorf("标题没带域名: %q", p.Title)
	}
	if !strings.Contains(p.Title, "证书已续期") {
		t.Errorf("标题没带事件标签: %q", p.Title)
	}
	if !strings.Contains(p.Body, "已铺发 2 个落点") {
		t.Errorf("正文丢了续期结果: %q", p.Body)
	}
	if p.Fields["event"] != EventCertRenewed {
		t.Errorf("事件字段 = %q", p.Fields["event"])
	}
}

func TestCertEventLabelsCoverAllLocales(t *testing.T) {
	for _, e := range certEvents {
		zh := eventLabel(e, i18n.Default)
		if zh == "" || zh == e {
			t.Errorf("%s 没注册中文源串: %q", e, zh)
			continue
		}
		for _, lang := range i18n.Supported {
			if lang == i18n.Default {
				continue
			}
			got := eventLabel(e, lang)
			// 与中文源串一模一样 = 这个语言根本没登记(注册表里没有该 locale)。
			if got == zh {
				t.Errorf("%s 的 %s 标签没翻译: %q", e, lang, got)
			}
		}
	}
}
