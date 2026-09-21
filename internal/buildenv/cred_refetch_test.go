package buildenv

import (
	"context"
	"errors"
	"testing"

	"github.com/huangchengsir/pipewright/internal/storetest"
	"github.com/huangchengsir/pipewright/internal/vault"
)

func testMasterKey() *[32]byte {
	var k [32]byte
	for i := range k {
		k[i] = byte(i + 11)
	}
	return &k
}

// TestVaultCredentialRefetch_GitToken 验证 git_token 凭据返回 username + token。
func TestVaultCredentialRefetch_GitToken(t *testing.T) {
	db := storetest.OpenDB(t)
	v := vault.New(db, testMasterKey())
	cred, err := v.Create(vault.CreateInput{
		Name: "git-cred", Type: vault.TypeGitToken,
		Username: "oauth2", Secret: "ghp_secret_abc",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	ref := NewVaultCredentialRefetch(v)
	u, tok, err := ref.GetByID(context.Background(), cred.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if u != "oauth2" || tok != "ghp_secret_abc" {
		t.Fatalf("u=%q tok=%q", u, tok)
	}
}

// TestVaultCredentialRefetch_NotFound 验证 ErrNotFound 透传。
func TestVaultCredentialRefetch_NotFound(t *testing.T) {
	db := storetest.OpenDB(t)
	v := vault.New(db, testMasterKey())
	ref := NewVaultCredentialRefetch(v)
	_, _, err := ref.GetByID(context.Background(), "non-existent-uuid")
	if !errors.Is(err, vault.ErrNotFound) {
		t.Fatalf("应 ErrNotFound, got %v", err)
	}
}

// TestVaultCredentialRefetch_NilVault 验证 nil vault 不 panic。
func TestVaultCredentialRefetch_NilVault(t *testing.T) {
	ref := NewVaultCredentialRefetch(nil)
	_, _, err := ref.GetByID(context.Background(), "any-id")
	if err == nil {
		t.Fatal("nil vault 应返回错误")
	}
}
