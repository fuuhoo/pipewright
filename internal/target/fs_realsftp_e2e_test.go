//go:build realssh

package target

// fs_realsftp_e2e_test.go —— 真 SFTP 端到端(经本机 sshd)。
//
// 为什么值得单独一条:假拨号器证不到「sshd 到底有没有起 sftp-server」「状态码怎么回」
// 「rename 覆盖是否真原子」这些只有真服务端才知道的事。macOS 自带 /usr/libexec/sftp-server
// (见 /etc/ssh/sshd_config 的 Subsystem 行),本机自连即是最省事的一条真链路。
//
// 默认不跑:
//
//	export PATH="$HOME/sdk/go/bin:$PATH"
//	DEPLOY_SSH_KEY=$HOME/.ssh/id_ed25519 DEPLOY_SSH_USER=$USER \
//	  go test -tags realssh -run TestRealSFTP ./internal/target/ -v

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"testing"
	"time"

	"github.com/huangchengsir/pipewright/internal/store"
	"github.com/huangchengsir/pipewright/internal/vault"
)

// newRealWorkspace 登记本机 sshd 为一台服务器并开一个远程文件工作区。
func newRealWorkspace(t *testing.T) Workspace {
	t.Helper()
	keyPath := os.Getenv("DEPLOY_SSH_KEY")
	if keyPath == "" {
		t.Skip("DEPLOY_SSH_KEY 未设置;跳过真 SFTP 端到端")
	}
	priv, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("读私钥: %v", err)
	}
	sshUser := os.Getenv("DEPLOY_SSH_USER")
	if sshUser == "" {
		if u, uerr := user.Current(); uerr == nil {
			sshUser = u.Username
		}
	}

	dbPath := filepath.Join(t.TempDir(), "fs.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var key [32]byte
	copy(key[:], []byte("0123456789abcdef0123456789abcdef"))
	v := vault.New(st.DB, &key)
	cred, err := v.Create(vault.CreateInput{Name: "fs-ssh", Type: vault.TypeSSHKey, Secret: string(priv)})
	if err != nil {
		t.Fatalf("vault.Create: %v", err)
	}
	svc := New(st.DB, v, nil) // nil dialer → 真 x/crypto/ssh
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	srv, err := svc.Create(ctx, CreateInput{
		Name: "fs-localhost", Host: "127.0.0.1", Port: 22, User: sshUser, CredentialID: cred.ID,
	})
	if err != nil {
		t.Fatalf("target.Create: %v", err)
	}
	opener, ok := svc.(WorkspaceOpener)
	if !ok {
		t.Fatal("真 Service 应满足 WorkspaceOpener")
	}
	ws, err := opener.OpenWorkspace(ctx, srv.ID)
	if err != nil {
		t.Fatalf("OpenWorkspace: %v", err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	return ws
}

func TestRealSFTPReadWriteRoundTrip(t *testing.T) {
	ws := newRealWorkspace(t)
	if got := ws.Backend(); got != "sftp" {
		t.Fatalf("Backend = %q, want sftp", got)
	}
	ctx := context.Background()
	dir := t.TempDir()

	if err := ws.WriteFile(ctx, filepath.Join(dir, "hello.txt"), bytes.NewBufferString("hello")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// 覆盖写必须是原子的:落定后既没有半截文件,也不该留下 .pipewright-tmp。
	if err := ws.WriteFile(ctx, filepath.Join(dir, "hello.txt"), bytes.NewBufferString("bye")); err != nil {
		t.Fatalf("覆盖写: %v", err)
	}
	data, trunc, err := ws.ReadFile(ctx, filepath.Join(dir, "hello.txt"), 1<<20)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "bye" || trunc {
		t.Fatalf("正文 = %q(truncated=%v), want \"bye\"", data, trunc)
	}

	entries, err := ws.ReadDir(ctx, dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "hello.txt" || entries[0].Size != 3 {
		t.Fatalf("目录项 = %+v", entries)
	}
	for _, e := range entries {
		if e.MtimeUnix == 0 {
			t.Errorf("%s 的 mtime 没取到", e.Name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "hello.txt.pipewright-tmp")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("临时文件没被清掉(或 rename 不是原子覆盖)")
	}
}

func TestRealSFTPDirOps(t *testing.T) {
	ws := newRealWorkspace(t)
	ctx := context.Background()
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")

	if err := ws.Mkdir(ctx, sub); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	st, err := ws.Stat(ctx, sub)
	if err != nil {
		t.Fatalf("Stat(dir): %v", err)
	}
	if !st.IsDir || st.Name != "sub" {
		t.Fatalf("目录属性 = %+v", st)
	}

	if err := ws.WriteFile(ctx, filepath.Join(sub, "a"), bytes.NewBufferString("x")); err != nil {
		t.Fatalf("写入子目录: %v", err)
	}
	// 非空目录删除必须失败:这层不该替界面提供 rm -rf。
	if err := ws.Remove(ctx, sub); err == nil {
		t.Fatal("删非空目录竟然成功了 —— 递归删除保险失效")
	}
	if err := ws.Remove(ctx, filepath.Join(sub, "a")); err != nil {
		t.Fatalf("删文件: %v", err)
	}
	if err := ws.Remove(ctx, sub); err != nil {
		t.Fatalf("删空目录: %v", err)
	}

	if err := ws.WriteFile(ctx, filepath.Join(dir, "from.txt"), bytes.NewBufferString("mv")); err != nil {
		t.Fatalf("写入待改名文件: %v", err)
	}
	if err := ws.Rename(ctx, filepath.Join(dir, "from.txt"), filepath.Join(dir, "to.txt")); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := ws.Stat(ctx, filepath.Join(dir, "from.txt")); !errors.Is(err, ErrRemoteNotFound) {
		t.Errorf("改名后原路径应报不存在,得 %v", err)
	}
}

func TestRealSFTPNotFoundAndStream(t *testing.T) {
	ws := newRealWorkspace(t)
	ctx := context.Background()
	dir := t.TempDir()

	if _, err := ws.Stat(ctx, filepath.Join(dir, "missing")); !errors.Is(err, ErrRemoteNotFound) {
		t.Errorf("不存在应折成 ErrRemoteNotFound(界面按 404 处理),得 %v", err)
	}
	if _, err := ws.ReadDir(ctx, filepath.Join(dir, "missing")); !errors.Is(err, ErrRemoteNotFound) {
		t.Errorf("目录不存在同样应报 ErrRemoteNotFound,得 %v", err)
	}

	payload := bytes.Repeat([]byte("0123456789"), 40000) // 400 KiB:验流式而非整包进内存
	if err := ws.WriteFile(ctx, filepath.Join(dir, "big.bin"), bytes.NewReader(payload)); err != nil {
		t.Fatalf("写入大文件: %v", err)
	}
	rc, err := ws.OpenRead(ctx, filepath.Join(dir, "big.bin"))
	if err != nil {
		t.Fatalf("OpenRead: %v", err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("读流: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("流式读回 %d 字节,与写入的 %d 字节不一致", len(got), len(payload))
	}
}

func TestRealSFTPReadFileTruncates(t *testing.T) {
	ws := newRealWorkspace(t)
	ctx := context.Background()
	dir := t.TempDir()
	p := filepath.Join(dir, "t.txt")
	if err := ws.WriteFile(ctx, p, bytes.NewBufferString("0123456789")); err != nil {
		t.Fatalf("写入: %v", err)
	}
	data, trunc, err := ws.ReadFile(ctx, p, 4)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "0123" || !trunc {
		t.Errorf("limit=4 应得 \"0123\"+truncated,得 %q(truncated=%v)", data, trunc)
	}
}

func TestRealSFTPCancelledCtxTearsDown(t *testing.T) {
	// 取消必须把整条连接拆掉:否则一次「客户端跑了」会挂住一条 SSH 连接与 goroutine。
	ws := newRealWorkspace(t)
	dir := t.TempDir()
	if err := ws.WriteFile(context.Background(), filepath.Join(dir, "seed"), bytes.NewBufferString("s")); err != nil {
		t.Fatalf("写入: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ws.ReadDir(ctx, dir); !errors.Is(err, context.Canceled) {
		t.Errorf("已取消的 ctx 应立刻得到 context.Canceled,得 %v", err)
	}
	// 连接已拆:后续操作必须以错误收,而不是永久阻塞。
	if _, err := ws.ReadDir(context.Background(), dir); err == nil {
		t.Error("拆掉的连接上不该还能列目录")
	}
}
