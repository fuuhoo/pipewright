package deploy

// artifact_store_centos_e2e_test.go 是「制品库部署真字节」(Story 8-16 / FR-8-16)的**真容器 e2e**:
// 复用 CentOS+sshd 容器,验 fake 证不到的真实链路 —— 把制品库里的 dist **真 tar.gz 字节**经 SSH
// (target.Upload = cat > file 流式)落到目标机,远端解包,容器内 cat 出**真文件树**(非占位 reference 串)。
//
// 默认 SKIP(PIPEWRIGHT_E2E_DEPLOY=1 启用)。跑法:
//   PIPEWRIGHT_E2E_DEPLOY=1 go test ./internal/deploy/ -run E2EArtifactStore -v

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/artifactstore"
	"github.com/fuuhoo/pipewright/internal/run"
	"github.com/google/uuid"
)

// TestE2EArtifactStoreDistUntar:制品库存 dist 的 tar.gz → 部署 → 容器内远端解包出真文件树。
func TestE2EArtifactStoreDistUntar(t *testing.T) {
	fleet, key := startCentOSFleet(t, 1)
	h := newMultiHarness(t, fleet, key)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	store, err := artifactstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	tarGz := buildTestTarGz(t, map[string]string{
		"index.html":    "<html>PIPEWRIGHT-REAL</html>",
		"assets/app.js": "console.log('real dist bytes')",
	})
	skey, _, err := store.Put(bytes.NewReader(tarGz))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	runID, artID := seedSuccessRunWithStoredArtifact(t, h.db, h.rsvc, run.ArtifactDist, skey,
		map[string]any{"stored": true, "format": "tar.gz"})

	base := "/opt/pw-dist-" + uuid.NewString()[:8]
	dsvc := New(h.targets, h.rsvc, WithArtifactStore(store)) // 注入制品库的部署服务
	res, err := dsvc.Deploy(ctx, DeployInput{
		RunID: runID, ArtifactID: artID, ServerIDs: h.serverIDs,
		Config: map[string]string{"deployPath": base},
	})
	if err != nil {
		t.Fatalf("dist Deploy: %v", err)
	}
	if res[0].Status != run.TargetSuccess {
		t.Fatalf("dist 部署应 success,实际 %s / %q", res[0].Status, res[0].Message)
	}

	// 容器内部署目录应被就地解包出**真文件树**(且无残留临时 tar.gz)。
	idx, err := fleet[0].Exec(t, "cat", base+"/index.html")
	if err != nil || !strings.Contains(idx, "PIPEWRIGHT-REAL") {
		t.Fatalf("容器内 index.html 应为真 dist 内容:%q err=%v", idx, err)
	}
	js, err := fleet[0].Exec(t, "cat", base+"/assets/app.js")
	if err != nil || !strings.Contains(js, "real dist bytes") {
		t.Fatalf("容器内 assets/app.js 应为真 dist 内容:%q err=%v", js, err)
	}
	if listing, err := fleet[0].Exec(t, "ls", "-A", base); err == nil &&
		strings.Contains(listing, ".pw-staging-") {
		t.Fatalf("解包后应删除临时 tar.gz,但目录仍残留中间态:%q", listing)
	}
	// 直铺:目录里既没有 releases/ 层也没有 current 软链。
	if _, err := fleet[0].Exec(t, "test", "-e", base+"/releases"); err == nil {
		t.Fatal("直铺模式不应有 releases/ 目录")
	}
	if _, err := fleet[0].Exec(t, "test", "-e", base+"/current"); err == nil {
		t.Fatal("直铺模式不应有 current 软链")
	}
	t.Logf("✅ 制品库 dist(tar.gz)经 SSH Upload + 远端就地解包,CentOS 容器内得到真文件树(无 releases/current)")
}

// buildTestTarGz 把 files(相对路径→内容)打成 tar.gz 字节(供 e2e 造真 dist 产物)。
func buildTestTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar write: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz close: %v", err)
	}
	return buf.Bytes()
}
