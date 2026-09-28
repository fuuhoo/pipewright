//go:build realssh

package target

// fs_exec_realsftp_e2e_test.go —— exec 兜底实现的端到端(经本机 sshd)。
//
// 为什么必须真跑:兜底路径的整个价值在于「在没有 sftp-server 的机器上凑出可用的面板」,
// 它的每一步都踩在 shell 与远端工具的差异上 —— glob 的三段式、ls 的第一列、wc 的空白、
// stat 的两种方言、mv -f 的覆盖语义。假拨号器只能验我发了什么命令,验不到远端怎么答。
//
// 本机 sshd 有 sftp-server,所以这里直接拨连接后手工构造 execWorkspace,绕开优先级。
//
// 默认不跑:
//
//	export PATH="$HOME/sdk/go/bin:$PATH"
//	DEPLOY_SSH_KEY=$HOME/.ssh/id_ed25519 DEPLOY_SSH_USER=$USER \
//	  go test -tags realssh -run TestRealExec ./internal/target/ -v

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huangchengsir/pipewright/internal/store"
	"github.com/huangchengsir/pipewright/internal/vault"
)

// newRealExecWorkspace 拨一条真连接并强制走 exec 兜底实现。
func newRealExecWorkspace(t *testing.T) Workspace {
	t.Helper()
	keyPath := os.Getenv("DEPLOY_SSH_KEY")
	if keyPath == "" {
		t.Skip("DEPLOY_SSH_KEY 未设置;跳过 exec 兜底端到端")
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
	dbPath := filepath.Join(t.TempDir(), "fs-exec.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var key [32]byte
	copy(key[:], []byte("0123456789abcdef0123456789abcdef"))
	v := vault.New(st.DB, &key)
	cred, err := v.Create(vault.CreateInput{Name: "fs-exec-ssh", Type: vault.TypeSSHKey, Secret: string(priv)})
	if err != nil {
		t.Fatalf("vault.Create: %v", err)
	}
	svc := New(st.DB, v, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	srv, err := svc.Create(ctx, CreateInput{
		Name: "fs-exec-localhost", Host: "127.0.0.1", Port: 22, User: sshUser, CredentialID: cred.ID,
	})
	if err != nil {
		t.Fatalf("target.Create: %v", err)
	}
	addr, cfg, err := svc.(*service).sshTarget(ctx, srv.ID)
	if err != nil {
		t.Fatalf("sshTarget: %v", err)
	}
	auth, err := authMethods(cfg)
	if err != nil {
		t.Fatalf("authMethods: %v", err)
	}
	client, _, err := dial(ctx, addr, cfg, auth)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	w, err := newExecWorkspace(ctx, client)
	if err != nil {
		_ = client.Close()
		t.Fatalf("newExecWorkspace: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}

func TestRealExecReadWriteRoundTrip(t *testing.T) {
	ws := newRealExecWorkspace(t)
	if got := ws.Backend(); got != "exec" {
		t.Fatalf("Backend = %q, want exec", got)
	}
	ctx := context.Background()
	dir := t.TempDir()
	p := filepath.Join(dir, "hello.txt")

	if err := ws.WriteFile(ctx, p, bytes.NewBufferString("hello")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := ws.WriteFile(ctx, p, bytes.NewBufferString("bye")); err != nil {
		t.Fatalf("覆盖写: %v", err)
	}
	data, trunc, err := ws.ReadFile(ctx, p, 1<<20)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "bye" || trunc {
		t.Fatalf("正文 = %q(truncated=%v), want \"bye\"", data, trunc)
	}
	if _, err := os.Stat(p + execTmpSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("临时文件没被清掉: %v", err)
	}

	entries, err := ws.ReadDir(ctx, dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "hello.txt" || entries[0].Size != 3 {
		t.Fatalf("目录项 = %+v, want 1 条 hello.txt size=3", entries)
	}
	if entries[0].MtimeUnix == 0 {
		t.Errorf("mtime 没取到(stat 方言探测或参数不对)")
	}
	if entries[0].Mode&0o200 == 0 {
		t.Errorf("权限位解析异常: %04o,应含 owner 写位", entries[0].Mode)
	}
}

// TestRealExecListsOddNames 钉住 glob 三段式:点文件、名字带空格、以及 `...` 这类怪名。
func TestRealExecListsOddNames(t *testing.T) {
	ws := newRealExecWorkspace(t)
	ctx := context.Background()
	dir := t.TempDir()
	names := []string{"normal", ".dotfile", "with space", "...triple"}
	for _, n := range names {
		if err := ws.WriteFile(ctx, filepath.Join(dir, n), bytes.NewBufferString("x")); err != nil {
			t.Fatalf("写入 %q: %v", n, err)
		}
	}
	entries, err := ws.ReadDir(ctx, dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	got := make([]string, 0, len(entries))
	for _, e := range entries {
		got = append(got, e.Name)
	}
	if strings.Join(got, "|") != strings.Join(sortedCopy(names), "|") {
		t.Errorf("目录项 = %v, want %v(点文件/空格/三段 glob)", got, names)
	}
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func TestRealExecDirOps(t *testing.T) {
	ws := newRealExecWorkspace(t)
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
	// 非空目录删除必须失败 —— 与 SFTP 实现同一道保险。
	if err := ws.Remove(ctx, sub); !errors.Is(err, ErrRemoteNotEmpty) {
		t.Fatalf("删非空目录应报 ErrRemoteNotEmpty,得 %v", err)
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

func TestRealExecNotFoundAndStream(t *testing.T) {
	ws := newRealExecWorkspace(t)
	ctx := context.Background()
	dir := t.TempDir()

	if _, err := ws.Stat(ctx, filepath.Join(dir, "missing")); !errors.Is(err, ErrRemoteNotFound) {
		t.Errorf("Stat 不存在应折成 ErrRemoteNotFound,得 %v", err)
	}
	if _, err := ws.ReadDir(ctx, filepath.Join(dir, "missing")); !errors.Is(err, ErrRemoteNotFound) {
		t.Errorf("ReadDir 不存在应报 ErrRemoteNotFound,得 %v", err)
	}
	// 对文件列目录:界面上是「点开文件」的误操作,不该报 500。
	if _, err := ws.ReadDir(ctx, dir); err != nil {
		t.Fatalf("列存在的目录: %v", err)
	}

	payload := bytes.Repeat([]byte("0123456789"), 40000) // 400 KiB:验 stdin/stdout 两侧都真流式
	p := filepath.Join(dir, "big.bin")
	if err := ws.WriteFile(ctx, p, bytes.NewReader(payload)); err != nil {
		t.Fatalf("写入大文件: %v", err)
	}
	rc, err := ws.OpenRead(ctx, p)
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
	// 下载不存在的文件必须**在拿到流之前**报错(否则界面只能收到一个 0 字节的下载)。
	if _, err := ws.OpenRead(ctx, filepath.Join(dir, "missing")); !errors.Is(err, ErrRemoteNotFound) {
		t.Errorf("OpenRead 不存在应报 ErrRemoteNotFound,得 %v", err)
	}
}

func TestRealExecReadFileTruncates(t *testing.T) {
	ws := newRealExecWorkspace(t)
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
	// 正好取满不该说被截断。
	data, trunc, err = ws.ReadFile(ctx, p, 10)
	if err != nil || string(data) != "0123456789" || trunc {
		t.Errorf("limit=10 应整份返回,得 %q(truncated=%v) err=%v", data, trunc, err)
	}
}

func TestRealExecRealpathAndSymlink(t *testing.T) {
	ws := newRealExecWorkspace(t)
	ctx := context.Background()
	dir := t.TempDir()
	sub := filepath.Join(dir, "in", "ner")
	if err := ws.Mkdir(ctx, sub); err != nil {
		t.Fatalf("Mkdir 递归: %v", err)
	}
	got, err := ws.Realpath(ctx, filepath.Join(sub, ".."))
	if err != nil {
		t.Fatalf("Realpath: %v", err)
	}
	// 与 SFTP 同义:pwd -P 会把 /var → /private/var 这类符号链接解析掉。
	want, werr := filepath.EvalSymlinks(filepath.Join(dir, "in"))
	if werr != nil {
		t.Fatalf("EvalSymlinks: %v", werr)
	}
	if got != want {
		t.Errorf("Realpath = %q, want %q", got, want)
	}
	if _, err := ws.Realpath(ctx, filepath.Join(dir, "missing")); !errors.Is(err, ErrRemoteNotFound) {
		t.Errorf("Realpath 不存在应报 ErrRemoteNotFound,得 %v", err)
	}
}

func TestRealExecCtxCancelKeepsConnection(t *testing.T) {
	// 兜底实现复用同一条连接:一次操作被取消只能收掉那一条 session,
	// 绝不能拆整条连接 —— 拆了之后每个操作都要重拨,面板就废了。
	ws := newRealExecWorkspace(t)
	dir := t.TempDir()
	if err := ws.WriteFile(context.Background(), filepath.Join(dir, "seed"), bytes.NewBufferString("s")); err != nil {
		t.Fatalf("写入: %v", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ws.ReadDir(cancelled, dir); !errors.Is(err, context.Canceled) {
		t.Fatalf("已取消的 ctx 应立刻得到 context.Canceled,得 %v", err)
	}
	// 同一条连接上继续可用,才叫「没被拆」。
	entries, err := ws.ReadDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("取消后连接应仍然可用,得 %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "seed" {
		t.Fatalf("目录项 = %+v, want 1 条 seed", entries)
	}
}

func TestRealExecAppendChunkReportsTotalSize(t *testing.T) {
	// 兜底只能 cat >> 接在 EOF,回报的是远端实测总长度(wc -c),不是客户端申报数 ——
	// HTTP 层正是拿这个数字判定「下一段该从哪开始」。
	ws := newRealExecWorkspace(t)
	ctx := context.Background()
	dir := t.TempDir()
	p := filepath.Join(dir, "resume.bin")

	n, err := ws.AppendChunk(ctx, p, 0, bytes.NewBufferString("abc"))
	if err != nil {
		t.Fatalf("首段 AppendChunk: %v", err)
	}
	if n != 3 {
		t.Errorf("首段后总长 = %d, want 3", n)
	}
	// 半截块之后再接一段:拼起来仍是完整原文,父目录不可写时才报错。
	n, err = ws.AppendChunk(ctx, p, 3, bytes.NewBufferString("def"))
	if err != nil {
		t.Fatalf("续段 AppendChunk: %v", err)
	}
	if n != 6 {
		t.Errorf("续段后总长 = %d, want 6", n)
	}
	if _, err := ws.AppendChunk(ctx, filepath.Join(dir, "nope", "x"), 0, bytes.NewBufferString("a")); !errors.Is(err, ErrRemoteNotFound) {
		t.Errorf("父目录不存在应回 ErrRemoteNotFound,得 %v", err)
	}
}

func TestRealExecChmod(t *testing.T) {
	ws := newRealExecWorkspace(t)
	ctx := context.Background()
	dir := t.TempDir()
	p := filepath.Join(dir, "key.pem")
	if err := ws.WriteFile(ctx, p, bytes.NewBufferString("secret")); err != nil {
		t.Fatalf("写入: %v", err)
	}
	if err := ws.Chmod(ctx, p, 0o600); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	st, err := ws.Stat(ctx, p)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if st.Mode&0o777 != 0o600 {
		t.Errorf("权限 = %o, want 600", st.Mode&0o777)
	}
	if err := ws.Chmod(ctx, filepath.Join(dir, "missing"), 0o600); !errors.Is(err, ErrRemoteNotFound) {
		t.Errorf("对不存在的路径设权限应回 ErrRemoteNotFound,得 %v", err)
	}
}
