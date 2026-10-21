package target

// container_target_e2e_test.go 验「目标服务器领域层 + 通用 SSH 执行基座」对一台**真 Linux
// SSH 容器**(alpine+sshd,startSSHContainer)端到端跑通 —— Epic 4 部署 / Epic 6 运维都构建在
// 这条 Exec/Test/ExecStream 链路之上。fake dialer 单测证不到真握手/真凭据经 vault 取用/真远端执行。
//
// 默认 SKIP(PIPEWRIGHT_E2E_DEPLOY=1 启用)。跑法:
//
//	PIPEWRIGHT_E2E_DEPLOY=1 go test ./internal/target/ -run E2EContainerTarget -v

import (
	"context"
	"database/sql"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/store"
	"github.com/fuuhoo/pipewright/internal/vault"
)

// newE2EService 建一个真 vault + 真 dialer 的 target.Service,把容器登记为目标服务器,返回 (svc, serverID)。
func newE2EService(t *testing.T, c *sshContainer) (Service, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "e2e.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return newE2EServiceWithDB(t, st.DB, c)
}

// newE2EServiceWithDB 同上但用给定 *sql.DB(供需共用一个 DB 的用例)。
func newE2EServiceWithDB(t *testing.T, db *sql.DB, c *sshContainer) (Service, string) {
	t.Helper()
	var key [32]byte
	copy(key[:], []byte("0123456789abcdef0123456789abcdef"))
	v := vault.New(db, &key)
	cred, err := v.Create(vault.CreateInput{Name: "e2e-ssh", Type: vault.TypeSSHKey, Secret: c.PrivateKey})
	if err != nil {
		t.Fatalf("vault.Create: %v", err)
	}
	svc := New(db, v, nil) // nil dialer → 真 x/crypto/ssh
	srv, err := svc.Create(context.Background(), CreateInput{
		Name: "e2e-target", Host: c.Host, Port: c.Port, User: "root", CredentialID: cred.ID,
	})
	if err != nil {
		t.Fatalf("target.Create: %v", err)
	}
	return svc, srv.ID
}

// TestE2EContainerTargetExec 验真容器目标:Test 连通、Exec 真执行、退出码透传、ExecStream 真流式。
func TestE2EContainerTargetExec(t *testing.T) {
	c := startSSHContainer(t)
	svc, id := newE2EService(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Test():真 SSH 连通 + uname 探测。
	tr, err := svc.Test(ctx, id)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if !tr.OK {
		t.Fatalf("Test 应连通,got ok=false err=%q", tr.Err)
	}
	if !strings.Contains(tr.Output, "Linux") {
		t.Fatalf("uname 输出应含 Linux(真 Linux 目标),got %q", tr.Output)
	}

	// Exec:真执行 echo,stdout 透传。
	res, err := svc.Exec(ctx, id, []string{"echo", "hello-e2e"})
	if err != nil {
		t.Fatalf("Exec echo: %v", err)
	}
	if res.ExitCode != 0 || !strings.Contains(res.Stdout, "hello-e2e") {
		t.Fatalf("Exec echo 异常: exit=%d stdout=%q", res.ExitCode, res.Stdout)
	}

	// 退出码真透传(false → 1)。
	res2, err := svc.Exec(ctx, id, []string{"false"})
	if err != nil {
		t.Fatalf("Exec false: %v", err)
	}
	if res2.ExitCode == 0 {
		t.Fatalf("false 应非零退出,got %d", res2.ExitCode)
	}

	// ExecStream:真流式读 stdout。
	rc, err := svc.ExecStream(ctx, id, []string{"printf", "L1\\nL2\\n"})
	if err != nil {
		t.Fatalf("ExecStream: %v", err)
	}
	b, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !strings.Contains(string(b), "L1") || !strings.Contains(string(b), "L2") {
		t.Fatalf("ExecStream 输出异常: %q", string(b))
	}
	t.Logf("真容器目标 Exec/Test/ExecStream OK;uname=%s", strings.TrimSpace(tr.Output))
}

// TestE2EContainerTargetStreamExit 验真 SSH 通道上的两条新通路(远程构建地基):
//   - ExecStreamWithExit:stdout/stderr **分流**逐块回调 + 远端命令真实退出码;
//   - ExecWithStdin:字节流进管道(口令/镜像 tar 这类绝不进 argv 的内容)。
func TestE2EContainerTargetStreamExit(t *testing.T) {
	c := startSSHContainer(t)
	svc, id := newE2EService(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var outLines, errLines []string
	code, err := svc.ExecStreamWithExit(ctx, id,
		[]string{"sh", "-c", `echo O1; echo O2; echo E1 >&2; exit 41`},
		func(stream string, chunk []byte) {
			for _, ln := range strings.Split(strings.TrimRight(string(chunk), "\n"), "\n") {
				if stream == StreamStderr {
					errLines = append(errLines, ln)
				} else {
					outLines = append(outLines, ln)
				}
			}
		})
	if err != nil {
		t.Fatalf("ExecStreamWithExit: %v", err)
	}
	if code != 41 {
		t.Fatalf("退出码 = %d, want 41(真退出码透传)", code)
	}
	if strings.Join(outLines, ",") != "O1,O2" || strings.Join(errLines, ",") != "E1" {
		t.Fatalf("stdout/stderr 未分流: out=%v err=%v", outLines, errLines)
	}

	// stdin 通路:字节流喂给远端 `cat > 文件`,落盘内容一致(供 docker load / --password-stdin)。
	payload := "line-a\nline-b\n"
	res, err := svc.ExecWithStdin(ctx, id,
		[]string{"sh", "-c", `mkdir -p /tmp/pw-stdin && cat > /tmp/pw-stdin/probe.txt`}, strings.NewReader(payload))
	if err != nil {
		t.Fatalf("ExecWithStdin: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("写入应成功,got %d stderr=%q", res.ExitCode, res.Stderr)
	}
	back, err := svc.Exec(ctx, id, []string{"cat", "/tmp/pw-stdin/probe.txt"})
	if err != nil {
		t.Fatalf("Exec cat: %v", err)
	}
	if back.Stdout != payload {
		t.Fatalf("stdin 落盘内容不符:%q", back.Stdout)
	}
	// 远端读自己 stdin 的退出码也真透传(docker login 失败就是这个形状)。
	r2, e2 := svc.ExecWithStdin(ctx, id, []string{"sh", "-c", "exit 7"}, strings.NewReader("x"))
	if e2 != nil {
		t.Fatalf("ExecWithStdin(非零退出)不应报传输错: %v", e2)
	}
	if r2 == nil || r2.ExitCode != 7 {
		t.Fatalf("ExecWithStdin 退出码未透传: %+v", r2)
	}
}
