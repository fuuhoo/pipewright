package vault

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// realPEM 生成真实可解析的 OpenSSH 私钥(git_ssh 类型要在入库前解析校验,伪 PEM 夹具不适用)。
func realPEM(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ssh.MarshalPrivateKey(priv, "t")
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(b))
}

func encryptedPEM(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("pw"))
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(b))
}

// TestGitSSHTypeAccepted 验证 git_ssh 是合法类型,且掩码走 SSH 私钥风格(绝不含 PEM 头)。
func TestGitSSHTypeAccepted(t *testing.T) {
	v := New(testDB(t), testKey())
	cred, err := v.Create(CreateInput{Name: "git ssh", Type: TypeGitSSH, Username: "git", Secret: realPEM(t)})
	if err != nil {
		t.Fatalf("Create git_ssh: %v", err)
	}
	if strings.Contains(cred.MaskedValue, "PRIVATE KEY") || strings.Contains(cred.MaskedValue, "OPENSSH") {
		t.Fatalf("掩码泄漏了 PEM 头: %q", cred.MaskedValue)
	}
	// GetGitAuth 类型无关:私钥落在 Token 字段,克隆侧直接可用。
	auth, err := v.GetGitAuth(cred.ID)
	if err != nil {
		t.Fatalf("GetGitAuth: %v", err)
	}
	if auth.Username != "git" || !strings.HasPrefix(auth.Token, "-----BEGIN OPENSSH PRIVATE KEY-----") {
		t.Fatalf("GetGitAuth = %+v", auth)
	}
}

// TestGitSSHRejectsBadSecretAtCreate 验证非私钥/带口令私钥在录入这一刻就被拒。
// 带口令的私钥在无人值守克隆里解不开;晚失败会表现为莫名的「克隆失败」,难排查。
func TestGitSSHRejectsBadSecretAtCreate(t *testing.T) {
	v := New(testDB(t), testKey())
	if _, err := v.Create(CreateInput{Name: "x", Type: TypeGitSSH, Secret: "just-a-token"}); !errors.Is(err, ErrInvalidGitSSHKey) {
		t.Fatalf("非私钥应 ErrInvalidGitSSHKey,got %v", err)
	}
	if _, err := v.Create(CreateInput{Name: "x", Type: TypeGitSSH, Secret: encryptedPEM(t)}); !errors.Is(err, ErrEncryptedGitSSHKey) {
		t.Fatalf("带口令私钥应 ErrEncryptedGitSSHKey,got %v", err)
	}
	// 其它类型不受此约束(ssh_password 存的就是口令串)。
	if _, err := v.Create(CreateInput{Name: "y", Type: TypeSSHPassword, Secret: "just-a-token"}); err != nil {
		t.Fatalf("ssh_password 不应受私钥校验: %v", err)
	}
}

// TestGitSSHRotateValidates 验证轮换密钥同样把关(否则可把私钥换成垃圾值)。
func TestGitSSHRotateValidates(t *testing.T) {
	v := New(testDB(t), testKey())
	cred, err := v.Create(CreateInput{Name: "git ssh", Type: TypeGitSSH, Secret: realPEM(t)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Update(cred.ID, UpdateInput{Secret: strPtr("garbage")}); !errors.Is(err, ErrInvalidGitSSHKey) {
		t.Fatalf("轮换为垃圾值应被拒,got %v", err)
	}
}

func strPtr(s string) *string { return &s }
