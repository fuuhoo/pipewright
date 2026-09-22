package project

import (
	"context"
	"errors"
	"net/url"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/huangchengsir/pipewright/internal/gitauth"
)

// TestProberUnreachable 验证真实 go-git prober 对不可达地址返回 ErrRepoUnreachable
// (且错误不含 token)。用 RFC2606 保留的不可解析主机,确保不真正触网外部服务。
func TestProberUnreachable(t *testing.T) {
	pr := goGitProber{}
	_, err := pr.Probe(context.Background(), "https://nonexistent.invalid/foo/bar.git", "", "supersecrettoken")
	if !errors.Is(err, ErrRepoUnreachable) {
		t.Fatalf("err = %v, want ErrRepoUnreachable", err)
	}
	if strings.Contains(err.Error(), "supersecrettoken") {
		t.Fatalf("错误信息泄漏了 token: %v", err)
	}
}

// TestProberEmptyURL 验证空 URL 立即判不可达,不触网。
func TestProberEmptyURL(t *testing.T) {
	pr := goGitProber{}
	if _, err := pr.Probe(context.Background(), "   ", "", "tok"); !errors.Is(err, ErrRepoUnreachable) {
		t.Fatalf("err = %v, want ErrRepoUnreachable", err)
	}
}

// TestProberSSRFRejectsFileScheme 验证生产路径(默认严格)拒绝 file:// scheme。
func TestProberSSRFRejectsFileScheme(t *testing.T) {
	pr := goGitProber{} // 生产默认:allowInsecureSchemes=false
	if _, err := pr.Probe(context.Background(), "file:///etc/passwd", "", ""); !errors.Is(err, ErrRepoUnreachable) {
		t.Fatalf("file:// 应被拒为 ErrRepoUnreachable, got %v", err)
	}
}

// TestProberSSRFRejectsUnsupportedScheme 验证拒绝 git:// / ftp:// 等无认证意义的 scheme。
// 注:ssh://(含 scp 语法)自 git over SSH 起为受支持协议,不在此列。
func TestProberSSRFRejectsUnsupportedScheme(t *testing.T) {
	pr := goGitProber{}
	for _, u := range []string{"git://host/repo.git", "ftp://host/x"} {
		if _, err := pr.Probe(context.Background(), u, "", "tok"); !errors.Is(err, ErrRepoUnreachable) {
			t.Fatalf("%s 应被拒为 ErrRepoUnreachable, got %v", u, err)
		}
	}
}

// TestProberSSHRejectsNonKeySecret 验证 SSH 地址配非私钥 secret 归为凭据错误
// (而非「不可达」——让用户能区分「地址对但凭据不对」)。
func TestProberSSHRejectsNonKeySecret(t *testing.T) {
	pr := goGitProber{}
	if _, err := pr.Probe(context.Background(), "ssh://git@10.0.0.5:2222/org/repo.git", "", "not-a-pem"); !errors.Is(err, ErrCredentialError) {
		t.Fatalf("SSH 地址 + 非私钥 secret 应归 ErrCredentialError, got %v", err)
	}
}

// TestClassifyProbeErrSSHAuthFailure 验证 SSH 握手期的鉴权失败归为凭据错误。
// go-git 的 ssh 传输不包装 transport 哨兵错误,若按默认分支处理会被误报成「仓库不可达」。
func TestClassifyProbeErrSSHAuthFailure(t *testing.T) {
	cases := []string{
		"ssh: handshake failed: ssh: unable to authenticate, attempted methods [none publickey], no supported methods remain",
		"ssh: handshake failed: ssh: unable to authenticate, attempted methods [publickey]",
	}
	for _, msg := range cases {
		if got := classifyProbeErr(errors.New(msg)); !errors.Is(got, ErrCredentialError) {
			t.Fatalf("SSH 鉴权失败应归 ErrCredentialError, got %v (%s)", got, msg)
		}
	}
	// 反向:连接层失败仍归不可达,不得被文本规则误伤。
	for _, msg := range []string{"dial tcp 10.0.0.5:22: connect: connection refused", "repository not found"} {
		if got := classifyProbeErr(errors.New(msg)); !errors.Is(got, ErrRepoUnreachable) {
			t.Fatalf("%q 应归 ErrRepoUnreachable, got %v", msg, got)
		}
	}
}

// TestProberSSRFRejectsMetadataAndLoopback 验证拒绝云元数据(169.254.169.254 链路本地)与回环。
func TestProberSSRFRejectsMetadataAndLoopback(t *testing.T) {
	pr := goGitProber{}
	for _, u := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"https://127.0.0.1/repo.git",
		"http://[::1]/repo.git",
	} {
		if _, err := pr.Probe(context.Background(), u, "", "tok"); !errors.Is(err, ErrRepoUnreachable) {
			t.Fatalf("%s 应被拒为 ErrRepoUnreachable, got %v", u, err)
		}
	}
}

// TestProberSSRFAllowsPrivateIP 验证私网 IP(自托管内网 Git)放行 scheme 校验
// (不被 SSRF 拦截;真实连接因无服务而 ErrRepoUnreachable,但不是被 scheme/host 校验拒的)。
func TestProberSSRFAllowsPrivateIP(t *testing.T) {
	// 直接校验器:私网 IP 字面量放行(SSRF 收口见 internal/giturl)。
	for _, u := range []string{
		"http://10.0.0.5/repo.git",
		"http://172.16.3.4/repo.git",
		"https://192.168.1.10/repo.git",
	} {
		if _, err := gitauth.ResolveRepoURL(u); err != nil {
			t.Fatalf("私网 %s 应放行,got %v", u, err)
		}
	}
}

// TestProberLocalBareRepo 用本地 file:// 裸仓库覆盖 ls-remote 成功路径 + 默认分支探测。
// go-git 的 file 传输不要求 token,故 token 传空;需宿主有 git 以建测试夹具,无 git 则跳过。
func TestProberLocalBareRepo(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("宿主无 git,无法构造本地裸仓库夹具;真实 ls-remote 成功路径跳过")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(gitBin, args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// 建工作仓库并提交,设默认分支为 trunk。
	run("init", "-q", "-b", "trunk")
	run("commit", "-q", "--allow-empty", "-m", "init")

	// file:// 本地夹具走 test-only 注入例外(生产路径默认严格拒 file://)。
	pr := goGitProber{allowInsecureSchemes: true}
	branch, err := pr.Probe(context.Background(), localFileURL(dir), "", "")
	if err != nil {
		t.Fatalf("Probe local repo: %v", err)
	}
	if branch != "trunk" {
		t.Fatalf("defaultBranch = %q, want trunk", branch)
	}
}

// TestProbeRefsBranchAndTagExtraction 锁定引用提取规则:多级分支名保留全名,
// 带注释 tag 的 peeled 引用(^{})与重复项被剔除,HEAD 不进列表,输出按字母序。
func TestProbeRefsBranchAndTagExtraction(t *testing.T) {
	mk := func(name string) *plumbing.Reference {
		return plumbing.NewHashReference(plumbing.ReferenceName(name), plumbing.ZeroHash)
	}
	refs := []*plumbing.Reference{
		plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main")),
		mk("refs/heads/release/1.0"),
		mk("refs/heads/main"),
		mk("refs/heads/main"), // 重复项(同名的 peeled 别名)
		mk("refs/tags/v1.0.0"),
		mk("refs/tags/v1.0.0^{}"), // 带注释 tag 的 peeled 项
	}
	if got, want := branchNamesFromRefs(refs), []string{"main", "release/1.0"}; !slices.Equal(got, want) {
		t.Fatalf("branches = %v, want %v", got, want)
	}
	if got, want := tagNamesFromRefs(refs), []string{"v1.0.0"}; !slices.Equal(got, want) {
		t.Fatalf("tags = %v, want %v", got, want)
	}
}

func localFileURL(path string) string {
	path = filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" {
		path = "/" + path
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
}
