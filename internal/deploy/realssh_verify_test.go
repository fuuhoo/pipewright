//go:build realssh

// 真验:登记 localhost 服务器 + dist 产物 → 真部署(经真实 SSH 到 localhost:22)→ targets 回填。
// 默认不跑(需 -tags realssh + 本机 SSH 到 localhost 可用 + DEPLOY_SSH_KEY 指向私钥)。
//
//	export PATH="$HOME/sdk/go/bin:$PATH"
//	DEPLOY_SSH_KEY=$HOME/.ssh/id_ed25519 DEPLOY_SSH_USER=$USER \
//	  go test -tags realssh -run TestRealLocalhostDistDeploy ./internal/deploy/ -v
package deploy

import (
	"context"
	"os"
	"os/user"
	"strings"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/run"
	"github.com/fuuhoo/pipewright/internal/target"
	"github.com/fuuhoo/pipewright/internal/vault"
	"github.com/google/uuid"
)

func TestRealLocalhostDistDeploy(t *testing.T) {
	keyPath := os.Getenv("DEPLOY_SSH_KEY")
	if keyPath == "" {
		t.Skip("DEPLOY_SSH_KEY 未设置;跳过真验")
	}
	privKey, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("读私钥: %v", err)
	}
	sshUser := os.Getenv("DEPLOY_SSH_USER")
	if sshUser == "" {
		if u, uerr := user.Current(); uerr == nil {
			sshUser = u.Username
		}
	}

	db := testDB(t)

	// 真 vault(32 字节 key)+ 真 target(nil dialer → 真实 x/crypto/ssh)。
	var key [32]byte
	copy(key[:], []byte("0123456789abcdef0123456789abcdef"))
	v := vault.New(db, &key)
	cred, err := v.Create(vault.CreateInput{Name: "loopback-ssh", Type: "ssh_key", Secret: string(privKey)})
	if err != nil {
		t.Fatalf("vault.Create: %v", err)
	}

	tgt := target.New(db, v, nil)
	srv, err := tgt.Create(context.Background(), target.CreateInput{
		Name: "localhost-deploy", Host: "127.0.0.1", Port: 22, User: sshUser, CredentialID: cred.ID,
	})
	if err != nil {
		t.Fatalf("target.Create: %v", err)
	}

	rsvc := run.New(db)
	runID, artID := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactDist, "dist/shop-v1.tar.gz")

	// 唯一直铺目录(便于断言落地);dist 直铺:产物内容就落在 <base>/ 下。
	base := "/tmp/pipewright-deploy-real-" + uuid.NewString()[:8]
	t.Cleanup(func() { _ = os.RemoveAll(base) })

	dsvc := New(tgt, rsvc)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := dsvc.Deploy(ctx, DeployInput{
		RunID: runID, ArtifactID: artID, ServerIDs: []string{srv.ID},
		Config: map[string]string{"deployPath": base},
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("want 1 target, got %d", len(res))
	}
	if res[0].Status != run.TargetSuccess {
		t.Fatalf("真部署应 success,got %s: %s", res[0].Status, res[0].Message)
	}
	t.Logf("真部署结果: status=%s message=%q", res[0].Status, res[0].Message)

	// 断言产物真落在部署目录本身(SSH 真传输 + 真执行),且没有 releases/ 层与 current 软链。
	entries, rerr := os.ReadDir(base)
	if rerr != nil {
		t.Fatalf("部署目录未创建: %v", rerr)
	}
	if len(entries) == 0 {
		t.Fatalf("部署目录为空,产物未落地")
	}
	t.Logf("落地文件: %v", entries)
	for _, e := range entries {
		if e.Name() == "releases" || e.Name() == "current" {
			t.Fatalf("直铺模式不应出现 %s: %v", e.Name(), entries)
		}
	}
	if _, serr := os.Stat(base + "/shop-v1.tar.gz"); serr != nil {
		t.Fatalf("产物未直铺到部署目录: %v", serr)
	}

	// run-detail targets slot 回填。
	targets, _ := rsvc.ListDeployTargets(context.Background(), runID)
	if len(targets) != 1 || targets[0].Status != run.TargetSuccess {
		t.Fatalf("targets 回填异常: %+v", targets)
	}

	// 断言 message / 内容无凭据明文。
	if strings.Contains(res[0].Message, "PRIVATE KEY") || strings.Contains(res[0].Message, "BEGIN OPENSSH") {
		t.Fatalf("message 泄漏私钥!")
	}
}

// TestRealLocalhostHealthFail 经真实 SSH 到 localhost 真验直铺 + 健康门控:
//
//	v1 部署(health=true)→ 产物落在 <base>/ + success
//	v2 部署(health=false)→ 产物覆盖到 <base>/ 后健康失败 → failed(直铺无上一版本可回滚)。
//
// 默认不跑(需 -tags realssh + DEPLOY_SSH_KEY)。
func TestRealLocalhostHealthFail(t *testing.T) {
	keyPath := os.Getenv("DEPLOY_SSH_KEY")
	if keyPath == "" {
		t.Skip("DEPLOY_SSH_KEY 未设置;跳过真验")
	}
	privKey, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("读私钥: %v", err)
	}
	sshUser := os.Getenv("DEPLOY_SSH_USER")
	if sshUser == "" {
		if u, uerr := user.Current(); uerr == nil {
			sshUser = u.Username
		}
	}

	db := testDB(t)
	var key [32]byte
	copy(key[:], []byte("0123456789abcdef0123456789abcdef"))
	v := vault.New(db, &key)
	cred, err := v.Create(vault.CreateInput{Name: "loopback-ssh", Type: "ssh_key", Secret: string(privKey)})
	if err != nil {
		t.Fatalf("vault.Create: %v", err)
	}
	tgt := target.New(db, v, nil)
	srv, err := tgt.Create(context.Background(), target.CreateInput{
		Name: "localhost-deploy", Host: "127.0.0.1", Port: 22, User: sshUser, CredentialID: cred.ID,
	})
	if err != nil {
		t.Fatalf("target.Create: %v", err)
	}

	rsvc := run.New(db)
	dsvc := New(tgt, rsvc)
	base := "/tmp/pipewright-rollback-real-" + uuid.NewString()[:8]
	t.Cleanup(func() { _ = os.RemoveAll(base) })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// v1:健康检查必通过(true),产物直铺进 <base>。
	run1, art1 := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactDist, "dist/shop-v1.tar.gz")
	res1, err := dsvc.Deploy(ctx, DeployInput{
		RunID: run1, ArtifactID: art1, ServerIDs: []string{srv.ID},
		Config:      map[string]string{"deployPath": base},
		HealthCheck: &HealthCheck{Type: HealthCheckCommand, Command: []string{"true"}, Retries: 1},
	})
	if err != nil {
		t.Fatalf("v1 Deploy: %v", err)
	}
	if res1[0].Status != run.TargetSuccess {
		t.Fatalf("v1 应 success, got %s: %s", res1[0].Status, res1[0].Message)
	}
	if _, serr := os.Stat(base + "/shop-v1.tar.gz"); serr != nil {
		t.Fatalf("v1 产物未直铺到部署目录: %v", serr)
	}

	// v2:健康检查必失败(false)→ 铺完健康不过 → failed(直铺无版本可回滚)。
	run2, art2 := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactDist, "dist/shop-v2.tar.gz")
	res2, err := dsvc.Deploy(ctx, DeployInput{
		RunID: run2, ArtifactID: art2, ServerIDs: []string{srv.ID},
		Config:      map[string]string{"deployPath": base},
		HealthCheck: &HealthCheck{Type: HealthCheckCommand, Command: []string{"false"}, Retries: 1},
	})
	if err != nil {
		t.Fatalf("v2 Deploy: %v", err)
	}
	if res2[0].Status != run.TargetFailed {
		t.Fatalf("v2 健康失败应 failed(无回滚), got %s: %s", res2[0].Status, res2[0].Message)
	}
	t.Logf("v2 健康失败结果: status=%s message=%q", res2[0].Status, res2[0].Message)

	// 关键断言:目录里就是产物本身 —— 既无 releases/ 层,也无 current 软链。
	if _, serr := os.Stat(base + "/shop-v2.tar.gz"); serr != nil {
		t.Fatalf("v2 产物应直铺在部署目录: %v", serr)
	}
	if _, lerr := os.Lstat(base + "/current"); lerr == nil {
		t.Fatalf("直铺模式不应有 current 软链")
	}
	if _, derr := os.Stat(base + "/releases"); derr == nil {
		t.Fatalf("直铺模式不应有 releases/ 目录")
	}
	// message 无凭据明文。
	if strings.Contains(res2[0].Message, "PRIVATE KEY") || strings.Contains(res2[0].Message, "BEGIN OPENSSH") {
		t.Fatalf("失败 message 泄漏私钥!")
	}
}
