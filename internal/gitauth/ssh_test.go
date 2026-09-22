package gitauth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"

	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gogitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"golang.org/x/crypto/ssh"
)

// pemEncode 把 ssh.MarshalPrivateKey* 产出的 block 序列化为 PEM 文本。
func pemEncode(b *pem.Block) []byte { return pem.EncodeToMemory(b) }

// testKeyPEM 生成无口令 OpenSSH 私钥夹具。
func testKeyPEM(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成私钥失败: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "pipewright-test")
	if err != nil {
		t.Fatalf("MarshalPrivateKey 失败: %v", err)
	}
	return string(pemEncode(block))
}

// testEncryptedPEM 生成带口令私钥夹具(无人值守克隆解不开,应被拒)。
func testEncryptedPEM(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成私钥失败: %v", err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("s3cret"))
	if err != nil {
		t.Fatalf("MarshalPrivateKeyWithPassphrase 失败: %v", err)
	}
	return string(pemEncode(block))
}

// testAuthorizedKey 返回一行合法 authorized_keys 内容(供 known_hosts 夹具)。
func testAuthorizedKey(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	pubKey, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		t.Fatal(err)
	}
	return string(bytes.TrimSpace(ssh.MarshalAuthorizedKey(pubKey)))
}

// Resolve:scp 语法归一化 / 私网放行 / 回环与非法 scheme 拒绝。
func TestResolveRepoURLNormalizationAndSSRF(t *testing.T) {
	cases := []struct {
		in      string
		wantURL string
		wantErr error
	}{
		{in: "https://github.com/a/b.git", wantURL: "https://github.com/a/b.git"},
		{in: "ssh://git@172.17.4.41:5052/fuuhoo/sw-lowcode.git", wantURL: "ssh://git@172.17.4.41:5052/fuuhoo/sw-lowcode.git"},
		{in: "git@172.17.4.41:fuuhoo/sw-lowcode.git", wantURL: "ssh://git@172.17.4.41/fuuhoo/sw-lowcode.git"},
		{in: "http://127.0.0.1:8080/a/b.git", wantErr: ErrBlockedHost},
		{in: "https://169.254.169.254/meta", wantErr: ErrBlockedHost},
		{in: "file:///etc/passwd", wantErr: ErrInvalidURL},
		{in: "git://host/a.git", wantErr: ErrInvalidURL},
	}
	for _, c := range cases {
		r, err := ResolveRepoURL(c.in)
		if c.wantErr != nil {
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("%s: err = %v, want %v", c.in, err, c.wantErr)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: 意外错误 %v", c.in, err)
		}
		if r.URL != c.wantURL {
			t.Fatalf("%s: 归一化 = %q, want %q", c.in, r.URL, c.wantURL)
		}
	}
}

// Transport:按协议选择认证方式,且 SSH 用户名取凭据用户名(缺省用 URL 用户)。
func TestTransportSelectsByScheme(t *testing.T) {
	httpAuth, err := Transport("https://gitlab.com/a/b.git", "", "tok123")
	if err != nil {
		t.Fatalf("http Transport: %v", err)
	}
	ba, ok := httpAuth.(*githttp.BasicAuth)
	if !ok {
		t.Fatalf("http 应产出 *BasicAuth,got %T", httpAuth)
	}
	if ba.Username != "git" || ba.Password != "tok123" {
		t.Fatalf("BasicAuth = %s/…, want git/…", ba.Username)
	}

	pem := testKeyPEM(t)
	sshAuth, err := Transport("ssh://deploy@10.0.0.5:2222/org/repo.git", "", pem)
	if err != nil {
		t.Fatalf("ssh Transport: %v", err)
	}
	pk, ok := sshAuth.(*gogitssh.PublicKeys)
	if !ok {
		t.Fatalf("ssh 应产出 *PublicKeys,got %T", sshAuth)
	}
	if pk.User != "deploy" {
		t.Fatalf("用户名应回退到 URL 里的 deploy,got %q", pk.User)
	}
	// 显式凭据用户名优先于 URL 用户。
	if pk2, err := Transport("ssh://deploy@10.0.0.5/org/repo.git", "ci-user", pem); err != nil {
		t.Fatalf("ssh Transport(显式用户名): %v", err)
	} else if pk2.(*gogitssh.PublicKeys).User != "ci-user" {
		t.Fatalf("显式用户名被忽略")
	}
}

// 空 token 对 HTTP 是匿名公开仓;对 SSH 无密钥可用 → 明确错误。
func TestTransportEmptySecret(t *testing.T) {
	if a, err := Transport("https://github.com/a/b.git", "", ""); err != nil || a != nil {
		t.Fatalf("HTTP 空 token 应匿名(nil, nil),got %v %v", a, err)
	}
	if _, err := Transport("ssh://git@10.0.0.5/a/b.git", "", "  "); !errors.Is(err, ErrInvalidPrivateKey) {
		t.Fatalf("SSH 空私钥应 ErrInvalidPrivateKey,got %v", err)
	}
}

// 非私钥 secret / 带口令私钥都要明确报错(而非「仓库不可达」这种误导)。
func TestTransportPrivateKeyErrors(t *testing.T) {
	if _, err := Transport("ssh://git@10.0.0.5/a/b.git", "", "not-a-pem"); !errors.Is(err, ErrInvalidPrivateKey) {
		t.Fatalf("非私钥应 ErrInvalidPrivateKey,got %v", err)
	}
	if _, err := Transport("ssh://git@10.0.0.5/a/b.git", "", testEncryptedPEM(t)); !errors.Is(err, ErrEncryptedPrivateKey) {
		t.Fatalf("带口令私钥应 ErrEncryptedPrivateKey,got %v", err)
	}
}

// 主机密钥策略:默认不校验;配置 known_hosts 后必须能构造出校验回调;文件不可读时报错。
func TestSetSSHHostKeyFile(t *testing.T) {
	t.Cleanup(func() { _ = SetSSHHostKeyFile("") })

	if err := SetSSHHostKeyFile(""); err != nil {
		t.Fatalf("清空(回到不校验)应无错: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "nope")
	if err := SetSSHHostKeyFile(missing); err == nil {
		t.Fatal("known_hosts 不存在应报错(配置收紧时用错路径必须被察觉)")
	}

	dir := t.TempDir()
	kh := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(kh, []byte("[10.0.0.5]:2222 "+testAuthorizedKey(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetSSHHostKeyFile(kh); err != nil {
		t.Fatalf("合法 known_hosts 应可用: %v", err)
	}
	if currentHostKeyCallback() == nil {
		t.Fatal("host key callback 未设置")
	}
}

// 地址与凭据错配要给出明确错误(SSH 地址配 HTTPS token ≠ 网络问题)。
func TestValidateSecretForSSH(t *testing.T) {
	if err := ValidateSecret("ssh://git@10.0.0.5/a/b.git", "plain-token"); !errors.Is(err, ErrInvalidPrivateKey) {
		t.Fatalf("SSH 地址配普通 token 应 ErrInvalidPrivateKey,got %v", err)
	}
	if err := ValidateSecret("https://github.com/a/b.git", "plain-token"); err != nil {
		t.Fatalf("HTTP 地址不做私钥约束,got %v", err)
	}
}
