package deploy

// container_deploy_e2e_test.go 是 Epic 4 部署的**真 e2e**:对一台真 alpine+sshd 容器(当真目标
// 服务器)经真 SSH 真跑文件直铺部署 + 健康门控,并**进容器内 docker exec 断言**文件真落地在
// 部署目录本身(无 releases/<runId> 层、无 current 软链)—— 这是 fake/localhost 证不到的真实路径
// (localhost e2e 里 target==本机,断言落在本机 FS;容器版断言落在容器内,真传输 + 真远端文件系统都被覆盖)。
//
// 默认 SKIP(PIPEWRIGHT_E2E_DEPLOY=1 启用)。跑法:
//
//	PIPEWRIGHT_E2E_DEPLOY=1 go test ./internal/deploy/ -run E2E -v
//
// 容器内有真 docker?没有。故 image 产物部署不在此验(降级);聚焦 dist/file 直铺 + 健康门控。

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/run"
	"github.com/fuuhoo/pipewright/internal/store"
	"github.com/fuuhoo/pipewright/internal/target"
	"github.com/fuuhoo/pipewright/internal/vault"
	"github.com/google/uuid"
)

// e2eHarness 把真 vault+target+run+deploy 串起来,登记容器为目标服务器。
type e2eHarness struct {
	db       *sql.DB
	rsvc     run.Service
	dsvc     Service
	serverID string
	c        *sshContainer
}

func newE2EHarness(t *testing.T, c *sshContainer) *e2eHarness {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "e2e.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var key [32]byte
	copy(key[:], []byte("0123456789abcdef0123456789abcdef"))
	v := vault.New(st.DB, &key)
	cred, err := v.Create(vault.CreateInput{Name: "e2e-ssh", Type: vault.TypeSSHKey, Secret: c.PrivateKey})
	if err != nil {
		t.Fatalf("vault.Create: %v", err)
	}
	tgt := target.New(st.DB, v, nil) // 真 dialer
	srv, err := tgt.Create(context.Background(), target.CreateInput{
		Name: "e2e-target", Host: c.Host, Port: c.Port, User: "root", CredentialID: cred.ID,
	})
	if err != nil {
		t.Fatalf("target.Create: %v", err)
	}
	rsvc := run.New(st.DB)
	return &e2eHarness{db: st.DB, rsvc: rsvc, dsvc: New(tgt, rsvc), serverID: srv.ID, c: c}
}

// containerFileExists 经 docker exec 在**容器内**断言路径存在(真落地证据)。
func (h *e2eHarness) containerFileExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := h.c.Exec(t, "test", "-e", path)
	return err == nil
}

// containerReadlink 经 docker exec 读容器内软链指向(零停机切换证据)。
func (h *e2eHarness) containerReadlink(t *testing.T, link string) string {
	t.Helper()
	out, err := h.c.Exec(t, "readlink", link)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// TestE2EDistDeployLandsInContainer 验 dist 产物经真 SSH 真**直铺** → 进容器断言文件就在部署目录下。
func TestE2EDistDeployLandsInContainer(t *testing.T) {
	c := startSSHContainer(t)
	h := newE2EHarness(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	base := "/opt/pw-e2e-dist-" + uuid.NewString()[:8]
	runID, artID := seedSuccessRunWithArtifact(t, h.db, h.rsvc, run.ArtifactDist, "dist/shop-v1.tar.gz")

	res, err := h.dsvc.Deploy(ctx, DeployInput{
		RunID: runID, ArtifactID: artID, ServerIDs: []string{h.serverID},
		Config: map[string]string{"deployPath": base},
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if len(res) != 1 || res[0].Status != run.TargetSuccess {
		t.Fatalf("dist 部署应 success: %+v", res)
	}

	// 关键:进容器内断言产物文件真落在部署目录本身(经真 SSH 真传输到真远端 FS)。
	artFile := base + "/shop-v1.tar.gz"
	if !h.containerFileExists(t, artFile) {
		t.Fatalf("容器内产物文件未直铺落地: %s", artFile)
	}
	// 既无 releases/ 层,也无 current 软链。
	if h.containerFileExists(t, base+"/releases") {
		t.Fatalf("直铺模式不应有 releases/ 目录: %s", base+"/releases")
	}
	if link := h.containerReadlink(t, base+"/current"); link != "" {
		t.Fatalf("直铺模式不应有 current 软链: %q", link)
	}
	// 落地内容真为 reference 文本(base64 解码经真 SSH 写入)。
	content, _ := h.c.Exec(t, "cat", artFile)
	if !strings.Contains(content, "dist/shop-v1.tar.gz") {
		t.Fatalf("容器内产物内容异常: %q", content)
	}
	if strings.Contains(res[0].Message, "PRIVATE KEY") {
		t.Fatalf("message 泄漏私钥!")
	}
	t.Logf("dist 真直铺落地容器 OK: %s", artFile)
}

// TestE2EOverwriteInPlaceAndHealthFail 验两版依次发布都落在同一部署目录(就地覆盖),
// 且健康失败该机记 failed —— 直铺没有 current,故**没有**回滚可言。
func TestE2EOverwriteInPlaceAndHealthFail(t *testing.T) {
	c := startSSHContainer(t)
	h := newE2EHarness(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	base := "/opt/pw-e2e-zdt-" + uuid.NewString()[:8]

	// v1:健康检查通过(command true)→ 产物直铺进 base。
	run1, art1 := seedSuccessRunWithArtifact(t, h.db, h.rsvc, run.ArtifactDist, "dist/shop-v1.tar.gz")
	res1, err := h.dsvc.Deploy(ctx, DeployInput{
		RunID: run1, ArtifactID: art1, ServerIDs: []string{h.serverID},
		Config:      map[string]string{"deployPath": base},
		HealthCheck: &HealthCheck{Type: HealthCheckCommand, Command: []string{"true"}, Retries: 1},
	})
	if err != nil {
		t.Fatalf("v1 Deploy: %v", err)
	}
	if res1[0].Status != run.TargetSuccess {
		t.Fatalf("v1 应 success: %+v", res1[0])
	}
	if !h.containerFileExists(t, base+"/shop-v1.tar.gz") {
		t.Fatalf("v1 产物未落在部署目录: %s", base+"/shop-v1.tar.gz")
	}

	// v2:健康检查必失败(command false)→ 铺完健康不过 → failed(直铺无版本可回滚)。
	run2, art2 := seedSuccessRunWithArtifact(t, h.db, h.rsvc, run.ArtifactDist, "dist/shop-v2.tar.gz")
	res2, err := h.dsvc.Deploy(ctx, DeployInput{
		RunID: run2, ArtifactID: art2, ServerIDs: []string{h.serverID},
		Config:      map[string]string{"deployPath": base},
		HealthCheck: &HealthCheck{Type: HealthCheckCommand, Command: []string{"false"}, Retries: 1},
	})
	if err != nil {
		t.Fatalf("v2 Deploy: %v", err)
	}
	if res2[0].Status != run.TargetFailed {
		t.Fatalf("v2 健康失败应 failed(无回滚): %+v", res2[0])
	}
	if !strings.Contains(res2[0].Message, "无上一版本可回滚") {
		t.Fatalf("message 应说明无版本可回滚: %q", res2[0].Message)
	}

	// 关键:v2 产物落在**同一目录**(就地覆盖),目录里没有 releases/current 这层壳。
	if !h.containerFileExists(t, base+"/shop-v2.tar.gz") {
		t.Fatalf("v2 产物应直铺在同一部署目录: %s", base+"/shop-v2.tar.gz")
	}
	if h.containerFileExists(t, base+"/releases") {
		t.Fatalf("直铺模式不应有 releases/ 目录")
	}
	// run 终态:目标失败 → failed。
	rn, _ := h.rsvc.Get(ctx, run2)
	if rn.Status != run.StatusFailed {
		t.Fatalf("v2 run 终态应 failed,got %s", rn.Status)
	}
	t.Logf("两版同目录直铺 OK: %s(v2 健康失败记 failed)", base)
}

// TestE2EHealthGate 验健康门控:通过型(test -f 落地文件)→ success;必失败型 → 首次部署无可回滚 → failed。
func TestE2EHealthGate(t *testing.T) {
	c := startSSHContainer(t)
	h := newE2EHarness(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 通过型:健康命令在容器内真跑 `test -f <落地文件>`,文件由本次部署真直铺 → 通过。
	basePass := "/opt/pw-e2e-hpass-" + uuid.NewString()[:8]
	runP, artP := seedSuccessRunWithArtifact(t, h.db, h.rsvc, run.ArtifactDist, "dist/shop.tar.gz")
	landed := basePass + "/shop.tar.gz"
	resP, err := h.dsvc.Deploy(ctx, DeployInput{
		RunID: runP, ArtifactID: artP, ServerIDs: []string{h.serverID},
		Config:      map[string]string{"deployPath": basePass},
		HealthCheck: &HealthCheck{Type: HealthCheckCommand, Command: []string{"test", "-f", landed}, Retries: 2, IntervalSeconds: 1},
	})
	if err != nil {
		t.Fatalf("通过型 Deploy: %v", err)
	}
	if resP[0].Status != run.TargetSuccess {
		t.Fatalf("健康门控应通过(文件已真落地): %+v", resP[0])
	}
	if !strings.Contains(resP[0].Message, "健康检查通过") {
		t.Fatalf("成功 message 应含『健康检查通过』: %q", resP[0].Message)
	}

	// 必失败型(健康探测文件不存在)→ failed(直铺无版本可回滚)。
	baseFail := "/opt/pw-e2e-hfail-" + uuid.NewString()[:8]
	runF, artF := seedSuccessRunWithArtifact(t, h.db, h.rsvc, run.ArtifactDist, "dist/shop.tar.gz")
	resF, err := h.dsvc.Deploy(ctx, DeployInput{
		RunID: runF, ArtifactID: artF, ServerIDs: []string{h.serverID},
		Config:      map[string]string{"deployPath": baseFail},
		HealthCheck: &HealthCheck{Type: HealthCheckCommand, Command: []string{"test", "-f", "/nonexistent/pw-e2e-missing"}, Retries: 1},
	})
	if err != nil {
		t.Fatalf("必失败型 Deploy: %v", err)
	}
	if resF[0].Status != run.TargetFailed {
		t.Fatalf("首次部署健康失败应 failed(无可回滚): %+v", resF[0])
	}
	if !strings.Contains(resF[0].Message, "健康检查") {
		t.Fatalf("失败 message 应含健康检查原因: %q", resF[0].Message)
	}
	t.Logf("健康门控 OK: 通过型 success、必失败型 failed")
}
