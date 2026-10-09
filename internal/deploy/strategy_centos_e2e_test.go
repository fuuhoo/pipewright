package deploy

// strategy_centos_e2e_test.go 是部署策略(Story 8-8 / FR-8-8:金丝雀 / 蓝绿)的**真多机 e2e**:
// 复用 multi_target_centos_e2e 的 CentOS+sshd 容器集群 + 真 SSH,验 fake 证不到的真实策略语义:
//   - 金丝雀:金丝雀台不可达 → 其余台**绝不被部署**(真容器内断言部署目录未创建,仍运行旧版本)。
//   - 蓝绿成功:全机先铺产物、统一重启 → 每台产物真落在部署路径下(无 releases/current 壳)。
//   - 蓝绿激活:健康检查仅一台失败 → 该机 failed(直铺无上一版本可回滚),其余机保持 success。
//
// 默认 SKIP(PIPEWRIGHT_E2E_DEPLOY=1 启用)。跑法:
//   PIPEWRIGHT_E2E_DEPLOY=1 go test ./internal/deploy/ -run E2EStrategy -v

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/run"
	"github.com/google/uuid"
)

// TestE2EStrategyCanaryAbort:3 台,金丝雀(第 1 台)不可达 → 金丝雀 failed、其余 2 台**未被部署**。
func TestE2EStrategyCanaryAbort(t *testing.T) {
	fleet, key := startCentOSFleet(t, 3)
	h := newMultiHarness(t, fleet, key)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	base := "/opt/pw-canary-" + uuid.NewString()[:8]
	runID, artID := seedSuccessRunWithArtifact(t, h.db, h.rsvc, run.ArtifactDist, "dist/shop-v1.tar.gz")

	// 金丝雀(第 1 台,serverIDs[0])部署前宕机 → SSH 不可达 → 金丝雀失败。
	canary := fleet[0]
	if out, err := exec.Command(canary.dockerBin, "stop", "-t", "1", canary.Name).CombinedOutput(); err != nil {
		t.Fatalf("停金丝雀台失败: %v\n%s", err, out)
	}

	res, err := h.dsvc.Deploy(ctx, DeployInput{
		RunID: runID, ArtifactID: artID, ServerIDs: h.serverIDs,
		Strategy: "canary",
		Config:   map[string]string{"releaseBase": base},
	})
	if err != nil {
		t.Fatalf("金丝雀 Deploy: %v", err)
	}
	// 金丝雀台 failed;其余 2 台被中止 → 也 failed(含「未部署」)。
	if res[0].Status != run.TargetFailed {
		t.Fatalf("金丝雀台应 failed,实际 %s", res[0].Status)
	}
	for i := 1; i < 3; i++ {
		if res[i].Status != run.TargetFailed || !strings.Contains(res[i].Message, "未部署") {
			t.Fatalf("其余台 %d 应 failed+未部署,实际 %s / %q", i, res[i].Status, res[i].Message)
		}
	}
	// 关键:其余 2 台真容器内**绝无部署目录**(金丝雀门控拦住了,仍运行旧版本)。
	for _, idx := range []int{1, 2} {
		if _, err := fleet[idx].Exec(t, "test", "-e", base); err == nil {
			t.Fatalf("金丝雀失败后第 %d 台仍被部署(部署目录存在),金丝雀门控失效", idx+1)
		}
	}
	rn, _ := h.rsvc.Get(ctx, runID)
	if rn.Status != run.StatusFailed {
		t.Fatalf("金丝雀中止 run 终态应 failed,实际 %s", rn.Status)
	}
	t.Logf("✅ 金丝雀台不可达 → 其余 2 台真容器内零部署目录(门控拦截,未部署)")
}

// TestE2EStrategyBlueGreenSuccess:2 台蓝绿,全机先铺产物 → 统一激活 → 每台产物真落在部署路径下。
func TestE2EStrategyBlueGreenSuccess(t *testing.T) {
	fleet, key := startCentOSFleet(t, 2)
	h := newMultiHarness(t, fleet, key)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	base := "/opt/pw-bg-" + uuid.NewString()[:8]
	runID, artID := seedSuccessRunWithArtifact(t, h.db, h.rsvc, run.ArtifactDist, "dist/shop-v1.tar.gz")

	res, err := h.dsvc.Deploy(ctx, DeployInput{
		RunID: runID, ArtifactID: artID, ServerIDs: h.serverIDs,
		Strategy: "blue_green",
		Config:   map[string]string{"deployPath": base},
	})
	if err != nil {
		t.Fatalf("蓝绿 Deploy: %v", err)
	}
	for i, r := range res {
		if r.Status != run.TargetSuccess {
			t.Fatalf("蓝绿目标 %d 应 success,实际 %s / %q", i, r.Status, r.Message)
		}
	}
	for i, c := range fleet {
		if _, err := c.Exec(t, "test", "-e", base+"/shop-v1.tar.gz"); err != nil {
			t.Fatalf("第 %d 台产物未直铺到部署路径: %s", i+1, base+"/shop-v1.tar.gz")
		}
		if _, err := c.Exec(t, "test", "-e", base+"/releases"); err == nil {
			t.Fatalf("第 %d 台不应有 releases/ 目录", i+1)
		}
	}
	t.Logf("✅ 蓝绿:2 台先铺产物、统一激活,每台产物真落在部署路径下(无 releases/current)")
}

// TestE2EStrategyBlueGreenActivatePerServer:蓝绿激活阶段健康检查仅 1 台失败 → 该机 failed
// (直铺无版本可回滚),其余机保持 success(不再有「要么全切要么全退」的机群回滚)。
func TestE2EStrategyBlueGreenActivatePerServer(t *testing.T) {
	fleet, key := startCentOSFleet(t, 2)
	h := newMultiHarness(t, fleet, key)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	base := "/opt/pw-bgrb-" + uuid.NewString()[:8]

	// 1) 先滚动部署 v1 到两台(部署目录下有 v1 产物)。
	runV1, artV1 := seedSuccessRunWithArtifact(t, h.db, h.rsvc, run.ArtifactDist, "dist/shop-v1.tar.gz")
	if _, err := h.dsvc.Deploy(ctx, DeployInput{
		RunID: runV1, ArtifactID: artV1, ServerIDs: h.serverIDs,
		Config: map[string]string{"deployPath": base},
	}); err != nil {
		t.Fatalf("v1 预部署: %v", err)
	}
	for i, c := range fleet {
		if _, err := c.Exec(t, "test", "-e", base+"/shop-v1.tar.gz"); err != nil {
			t.Fatalf("v1 预部署后第 %d 台产物未落地", i+1)
		}
	}

	// 2) 健康检查:test -f /tmp/pw-healthy。仅第 1 台(good)创建该文件 → 第 2 台(bad)健康必失败。
	if _, err := fleet[0].Exec(t, "touch", "/tmp/pw-healthy"); err != nil {
		t.Fatalf("good 台创建健康标记失败: %v", err)
	}

	// 3) 蓝绿发 v2 + 命令健康检查:bad 台 failed、good 台 success(各机独立,无机群回滚)。
	runV2, artV2 := seedSuccessRunWithArtifact(t, h.db, h.rsvc, run.ArtifactDist, "dist/shop-v2.tar.gz")
	hc := &HealthCheck{Type: HealthCheckCommand, Command: []string{"test", "-f", "/tmp/pw-healthy"}, Retries: 1}
	res, err := h.dsvc.Deploy(ctx, DeployInput{
		RunID: runV2, ArtifactID: artV2, ServerIDs: h.serverIDs,
		Strategy:    "blue_green",
		HealthCheck: hc,
		Config:      map[string]string{"deployPath": base},
	})
	if err != nil {
		t.Fatalf("v2 蓝绿 Deploy: %v", err)
	}
	if res[0].Status != run.TargetSuccess {
		t.Fatalf("good 台应 success,实际 %s / %q", res[0].Status, res[0].Message)
	}
	if res[1].Status != run.TargetFailed {
		t.Fatalf("bad 台应 failed(直铺无回滚),实际 %s / %q", res[1].Status, res[1].Message)
	}
	// 两台产物都已就地覆盖为 v2(直铺没有 current 可退回 v1)。
	for i, c := range fleet {
		if _, err := c.Exec(t, "test", "-e", base+"/shop-v2.tar.gz"); err != nil {
			t.Fatalf("第 %d 台 v2 产物未直铺落地", i+1)
		}
		if _, err := c.Exec(t, "test", "-e", base+"/current"); err == nil {
			t.Fatalf("第 %d 台不应有 current 软链", i+1)
		}
	}
	t.Logf("✅ 蓝绿激活:1 台健康失败记 failed、其余 success(直铺无机群回滚)")
}
