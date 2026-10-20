package httpapi

// certs_check_test.go 验 POST /api/certs/check 的对外契约:它是证书这条线上唯一「一次点、全库动」
// 的端点(会当场续期并铺发),所以权限、审计、响应形状三样都得钉住;顺带把监控事件 → 通知事件
// 的那张映射表验一遍(映射漏一条,事件会被静默丢弃,页面与渠道两边都看不出问题)。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/cert"
	"github.com/fuuhoo/pipewright/internal/notify"
)

// checkCert 打一发「立即检查」并返回解码后的汇总。
func checkCert(t *testing.T, env *certEnv, c *http.Client, cs string) (int, map[string]any) {
	t.Helper()
	code, body := certCall(t, c, cs, http.MethodPost, env.srv.URL+"/api/certs/check", "")
	return code, decodeJSONBody(t, body)
}

// checkBucket 取汇总里的一个桶;三组必须始终是数组(页面直接 .length,回 null 就白屏)。
func checkBucket(t *testing.T, obj map[string]any, name string) []any {
	t.Helper()
	items, ok := obj[name].([]any)
	if !ok {
		t.Fatalf("汇总里 %s 不是数组: %v", name, obj[name])
	}
	return items
}

func TestCertCheck_SummarizesWithoutTouchingPending(t *testing.T) {
	env := setupCertsAPI(t, true)
	m := newCertMaterial(t, "check.example.com", 8801)
	id := importCert(t, env, env.admin, env.adminCS, m)
	// 新导入的正文还有 90 天,离续期窗口远;改成 20 天才进得了「要人自己换」那一桶
	// (导入的证书平台签不出来,而这张实例又没接 DNS 写入能力,签发路径恒不可用)。
	if _, err := env.db.Exec(`UPDATE certificates SET not_after=? WHERE id=?`,
		time.Now().UTC().AddDate(0, 0, 20).Format(time.RFC3339), id); err != nil {
		t.Fatalf("改写到期时刻: %v", err)
	}
	pendingID := createPendingCert(t, env, "pending-check.example.com")

	code, obj := checkCert(t, env, env.admin, env.adminCS)
	if code != http.StatusOK {
		t.Fatalf("立即检查 = %d: %v", code, obj)
	}
	if got, ok := obj["checked"].(float64); !ok || int(got) != 1 {
		t.Errorf("checked = %v,期望只有那张有正文的被数到", obj["checked"])
	}
	expiring := checkBucket(t, obj, "expiring")
	if len(expiring) != 1 {
		t.Fatalf("剩 20 天的导入证书该出现在 expiring 里: %v", obj)
	}
	first, _ := expiring[0].(map[string]any)
	if first["certId"] != id || first["domain"] != "check.example.com" {
		t.Errorf("expiring 条目对不上: %v", first)
	}
	if d, _ := first["detail"].(string); !strings.Contains(d, "导入的证书") {
		t.Errorf("没说明白「为什么平台不自己续」: %q", d)
	}
	if left, ok := first["daysLeft"].(float64); !ok || int(left) != 20 {
		t.Errorf("daysLeft = %v,期望 20", first["daysLeft"])
	}
	if len(checkBucket(t, obj, "renewed")) != 0 || len(checkBucket(t, obj, "failed")) != 0 {
		t.Errorf("这轮既没续成也没续坏: %v", obj)
	}

	// 待签记录一条都不该出现在汇总里:后台/手动都不替用户起第一单。
	// 私钥也绝不许借这份汇总出站(AC-SEC-01:唯一出口是 /bundle)。
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("编码汇总: %v", err)
	}
	if strings.Contains(string(raw), pendingID) {
		t.Errorf("待签记录被扫描动了: %s", raw)
	}
	assertNoPrivateKeyMaterial(t, "检查汇总", string(raw), m.key)

	n, detail := auditRow(t, env.db, audit.ActionCertCheck)
	if n != 1 {
		t.Fatalf("cert_check 审计应有 1 条,实得 %d", n)
	}
	row := decodeJSONBody(t, detail)
	if row["ok"] != true {
		t.Errorf("审计没记下这轮成功: %s", detail)
	}
	if got, _ := row["expiring"].(float64); int(got) != 1 {
		t.Errorf("审计里的 expiring 数 = %v,期望 1: %s", row["expiring"], detail)
	}
	assertNoPrivateKeyMaterial(t, "检查审计", detail, m.key)
}

// 这一发要的是「按钮不许悄悄被 /certs/{id} 详情吞掉」:chi 的字面段优先级在这里必须真的成立,
// 且权限轴与其它写端点一致(viewer 403 而不是 404)。
func TestCertCheck_PermissionAndRouting(t *testing.T) {
	env := setupCertsAPI(t, true)

	code, obj := checkCert(t, env, env.viewer, env.viewerCS)
	if code != http.StatusForbidden {
		t.Fatalf("viewer 立即检查 = %d, want 403: %v", code, obj)
	}
	if got := decodeCode(t, obj); got != "role_denied" {
		t.Errorf("错误码 = %q, want role_denied", got)
	}
	// 拒绝也要留痕:谁在试着推动全库续期,失败的那一次同样值得复盘。
	if n, _ := auditRow(t, env.db, audit.ActionCertCheck); n != 0 {
		t.Errorf("被权限挡下的检查不该留业务审计,实得 %d 条", n)
	}

	// una 有 cert.manage:她能触发(这动作只吃功能轴,不针对某台主机,所以没有数据轴)。
	code, obj = checkCert(t, env, env.normal, env.normalCS)
	if code != http.StatusOK {
		t.Fatalf("role=user 立即检查 = %d, want 200: %v", code, obj)
	}
}

// 事件映射表:领域种类 → 通知事件。漏一条 = 那条监控事件被静默丢掉(只有日志里一行)。
func TestCertEventMapping(t *testing.T) {
	cases := map[string]string{
		cert.MonitorExpiring:    notify.EventCertExpiring,
		cert.MonitorRenewed:     notify.EventCertRenewed,
		cert.MonitorRenewFailed: notify.EventCertRenewFailed,
		"":                      "",
		"未知种类":                  "",
	}
	for kind, want := range cases {
		if got := certEventFor(kind); got != want {
			t.Errorf("certEventFor(%q) = %q, want %q", kind, got, want)
		}
	}
}
