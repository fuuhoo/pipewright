package target

// fs_test.go —— 远程文件层的纯逻辑单测(不触网)。
//
// 真 SFTP 链路的端到端见 fs_realsftp_e2e_test.go(build tag realssh):本文件只覆盖
// 那些「错了会在真机上以 500/空目录形式暴露」的归一与错误映射。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pkg/sftp"

	"github.com/huangchengsir/pipewright/internal/vault"
)

// TestOpenWorkspaceUnsupported 验可选能力的降级:假拨号器不实现 fsDialer 时,
// 拿到的是明确的「不支持」领域错误,而不是 panic 或一句「连不上」。
func TestOpenWorkspaceUnsupported(t *testing.T) {
	db := testDB(t)
	v := vault.New(db, testMasterKey())
	credID := newSSHCred(t, v, "secret-pw")
	svc := New(db, v, &capturingDialer{})

	srv, err := svc.Create(context.Background(), CreateInput{
		Name: "fs-1", Host: "10.0.0.9", Port: 22, User: "deploy", CredentialID: credID,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	opener, ok := svc.(WorkspaceOpener)
	if !ok {
		t.Fatal("真 Service 应满足 WorkspaceOpener(能力在 dialer 层判,不在这里)")
	}
	if _, err := opener.OpenWorkspace(context.Background(), srv.ID); !errors.Is(err, ErrFSUnsupported) {
		t.Errorf("应回 ErrFSUnsupported,得 %v", err)
	}
}

// TestOpenWorkspaceServerNotFound 验「先认服务器再碰凭据」的顺序:未知 ID 直接 404 类错误。
func TestOpenWorkspaceServerNotFound(t *testing.T) {
	db := testDB(t)
	v := vault.New(db, testMasterKey())
	svc := New(db, v, &capturingDialer{})
	opener := svc.(WorkspaceOpener)
	if _, err := opener.OpenWorkspace(context.Background(), "no-such-id"); !errors.Is(err, ErrNotFound) {
		t.Errorf("应回 ErrNotFound,得 %v", err)
	}
}

func TestCleanRemote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "."},           // 空 = 会话起点(家目录),交给协议解析相对路径
		{".", "."},          //
		{"/", "/"},          // 根不能被归成 "."
		{"/etc/", "/etc"},   // 尾斜杠
		{"///a//b", "/a/b"}, // 多余分隔
		{"/etc/../var", "/var"},
		{"~", "~"}, // 不做 shell 展开:波浪号交回协议,由服务端按字面处理
	}
	for _, c := range cases {
		if got := cleanRemote(c.in); got != c.want {
			t.Errorf("cleanRemote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestStatOfFromRealFiles(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "a-dir")
	if err := os.Mkdir(sub, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(f, []byte("hello"), 0o604); err != nil {
		t.Fatalf("write: %v", err)
	}

	st, err := statOfPath(f)
	if err != nil {
		t.Fatalf("statOfPath(file): %v", err)
	}
	if st.Name != "f.txt" || st.Path != f || st.IsDir {
		t.Errorf("file stat = %+v", st)
	}
	if st.Mode != 0o604 {
		t.Errorf("权限位 = %#o, want 0604", st.Mode)
	}
	if st.Size != 5 {
		t.Errorf("size = %d, want 5", st.Size)
	}
	if st.MtimeUnix <= 0 {
		t.Errorf("mtime 未取到: %d", st.MtimeUnix)
	}

	dst, err := statOfPath(sub)
	if err != nil {
		t.Fatalf("statOfPath(dir): %v", err)
	}
	if !dst.IsDir || dst.Name != "a-dir" {
		t.Errorf("dir stat = %+v", dst)
	}
}

func TestStatOfZeroMtime(t *testing.T) {
	// 对端没给时间(旧 sftp-server 只回 mtime 的秒,极端情况回 0)时不该显示成 1970。
	st := statOf("/x", fakeInfo{name: "x", mode: 0o644, mtime: time.Unix(0, 0)})
	if st.MtimeUnix != 0 {
		t.Errorf("零时间应归一为 0,得 %d", st.MtimeUnix)
	}
}

func TestMapSFSErrCodeMapping(t *testing.T) {
	// 这两个映射决定界面是把「文件不存在」当 404 还是当 500;也是唯一能把 SFTP 状态码
	// 认出来的途径 —— *StatusError 不实现 Is(),os.IsNotExist 对它一律为 false。
	nf := mapSFSErr(&sftp.StatusError{Code: uint32(sftp.ErrSSHFxNoSuchFile)})
	if !errors.Is(nf, ErrRemoteNotFound) {
		t.Errorf("NoSuchFile 应折成 ErrRemoteNotFound,得 %v", nf)
	}
	perm := mapSFSErr(&sftp.StatusError{Code: uint32(sftp.ErrSSHFxPermissionDenied)})
	if !errors.Is(perm, ErrRemotePermission) {
		t.Errorf("PermissionDenied 应折成 ErrRemotePermission,得 %v", perm)
	}
	other := mapSFSErr(&sftp.StatusError{Code: uint32(sftp.ErrSSHFxFailure)})
	if errors.Is(other, ErrRemoteNotFound) || errors.Is(other, ErrRemotePermission) {
		t.Errorf("FxFailure 不该被折成导航错误,得 %v", other)
	}
	if mapSFSErr(nil) != nil {
		t.Error("nil 必须原样返回 nil")
	}
	// 系统层错误(非协议状态码)也要认出来:本地 Stat 失败走这条路。
	wrapped := fmt.Errorf("open: %w", os.ErrNotExist)
	if !errors.Is(mapSFSErr(wrapped), ErrRemoteNotFound) {
		t.Errorf("os.ErrNotExist 应折成 ErrRemoteNotFound,得 %v", wrapped)
	}
}

// statOfPath 读真实文件属性后过 statOf(单测里没有「拿远端属性」这一步,只验归一)。
func statOfPath(p string) (FileStat, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return FileStat{}, err
	}
	return statOf(p, fi), nil
}

// fakeInfo 是一个最小 os.FileInfo,用于构造「对端没给时间」这类属性样本。
type fakeInfo struct {
	name  string
	size  int64
	mode  os.FileMode
	mtime time.Time
}

func (f fakeInfo) Name() string       { return f.name }
func (f fakeInfo) Size() int64        { return f.size }
func (f fakeInfo) Mode() os.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return f.mtime }
func (f fakeInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeInfo) Sys() any           { return nil }
