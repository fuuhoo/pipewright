// cluster_test.go 覆盖集群登记的领域规则:必填项、命名空间合法性、分组归属、
// 「凭据必须是能用的 kubeconfig」,以及明文绝不落库(AC-SEC-01)。
package kube

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/huangchengsir/pipewright/internal/storetest"
	"github.com/huangchengsir/pipewright/internal/vault"
)

func newTestService(t *testing.T) (Service, *sql.DB, vault.Vault) {
	t.Helper()
	db := storetest.OpenDB(t)
	v := vault.New(db, testKey())
	return New(db, v), db, v
}

func testKey() *[32]byte {
	var k [32]byte
	for i := range k {
		k[i] = byte(i + 11)
	}
	return &k
}

// addCred 录一条凭据并返回其 ID。
func addCred(t *testing.T, v vault.Vault, credType, secret string) string {
	t.Helper()
	c, err := v.Create(vault.CreateInput{Type: credType, Name: "cred-" + t.Name(), Secret: secret})
	if err != nil {
		t.Fatalf("create credential: %v", err)
	}
	return c.ID
}

func kubeConfigWithToken(server, token string) string {
	return `apiVersion: v1
kind: Config
current-context: c1
clusters:
- name: k1
  cluster:
    server: ` + server + `
users:
- name: u1
  user:
    token: ` + token + `
contexts:
- name: c1
  context: {cluster: k1, user: u1, namespace: shop}
`
}

func TestClusterCRUDRoundTrip(t *testing.T) {
	svc, _, v := newTestService(t)
	ctx := context.Background()
	cid := addCred(t, v, vault.TypeKubeConfig, kubeConfigWithToken("https://10.0.0.8:6443", "sa-tok"))

	created, err := svc.Create(ctx, CreateInput{Name: "prod-hz", CredentialID: cid, NamespaceDefault: "shop"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Name != "prod-hz" || created.NamespaceDefault != "shop" {
		t.Errorf("created = %+v", created)
	}
	// 地址从 kubeconfig 现读,不入库也不手填 —— 列表上看见的就是将要发布去的那个。
	if created.Endpoint != "https://10.0.0.8:6443" {
		t.Errorf("endpoint = %q", created.Endpoint)
	}
	if created.CredentialName == "" {
		t.Error("应 join 出凭据展示名")
	}

	ns := "kube-system"
	if _, err := svc.Update(ctx, created.ID, UpdateInput{NamespaceDefault: &ns}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := svc.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.NamespaceDefault != "kube-system" {
		t.Errorf("namespace = %q", got.NamespaceDefault)
	}

	list, err := svc.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %v (%v)", list, err)
	}
	if err := svc.Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Get(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after delete = %v", err)
	}
}

func TestClusterCreateValidation(t *testing.T) {
	svc, _, v := newTestService(t)
	cid := addCred(t, v, vault.TypeKubeConfig, kubeConfigWithToken("https://10.0.0.8:6443", "tok"))
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		in   CreateInput
		want error
	}{
		{"名字为空", CreateInput{CredentialID: cid}, ErrEmptyName},
		{"凭据未选", CreateInput{Name: "a"}, ErrEmptyCredentialID},
		{"命名空间大写", CreateInput{Name: "a", CredentialID: cid, NamespaceDefault: "Shop"}, ErrBadNamespace},
		{"命名空间含斜杠", CreateInput{Name: "a", CredentialID: cid, NamespaceDefault: "a/b"}, ErrBadNamespace},
		{"命名空间首字符是连字符", CreateInput{Name: "a", CredentialID: cid, NamespaceDefault: "-x"}, ErrBadNamespace},
		{"命名空间留空合法", CreateInput{Name: "a", CredentialID: cid, NamespaceDefault: "  "}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Create(ctx, tc.in)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("留空命名空间应合法:%v", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("Create = %v, want %v", err, tc.want)
			}
		})
	}
}

// 选错凭据(SSH 私钥 / 空串)如果入库成功,症状会晚到且难查:部署时才有句「连不上」。
// 因此登记这一刻就把 kubeconfig 真解析一遍。
func TestClusterRejectsUnusableCredential(t *testing.T) {
	svc, _, v := newTestService(t)
	ctx := context.Background()
	sshCred := addCred(t, v, vault.TypeSSHKey, "-----BEGIN OPENSSH PRIVATE KEY-----\nb251c2Vk\n-----END OPENSSH PRIVATE KEY-----\n")
	if _, err := svc.Create(ctx, CreateInput{Name: "x", CredentialID: sshCred}); !errors.Is(err, ErrKubeConfigInvalid) {
		t.Errorf("SSH 私钥当集群凭据 = %v, want ErrKubeConfigInvalid", err)
	}
	missing := "00000000-0000-0000-0000-000000000000"
	if _, err := svc.Create(ctx, CreateInput{Name: "x", CredentialID: missing}); !errors.Is(err, ErrCredentialNotFound) {
		t.Errorf("不存在的凭据 = %v, want ErrCredentialNotFound", err)
	}
}

// 明文泄漏断言:kubeconfig 只应存在于保险库密文里。
func TestClusterRowHoldsNoSecret(t *testing.T) {
	db := storetest.OpenDB(t)
	v := vault.New(db, testKey())
	svc := New(db, v)
	ctx := context.Background()
	token := "super-secret-sa-token-abcdefg"
	cid := addCred(t, v, vault.TypeKubeConfig, kubeConfigWithToken("https://10.0.0.8:6443", token))
	if _, err := svc.Create(ctx, CreateInput{Name: "prod", CredentialID: cid}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	var blob string
	if err := db.QueryRow(`SELECT name || ' ' || credential_id || ' ' || namespace_default || ' ' || group_id FROM kube_clusters`).Scan(&blob); err != nil {
		t.Fatalf("query row: %v", err)
	}
	if strings.Contains(blob, token) || strings.Contains(blob, "apiVersion") {
		t.Fatalf("集群行里出现了明文凭据:%q", blob)
	}
}

func TestClientForBuildsUsableClient(t *testing.T) {
	svc, _, v := newTestService(t)
	ctx := context.Background()
	cid := addCred(t, v, vault.TypeKubeConfig, kubeConfigWithToken("https://10.0.0.8:6443", "tok"))
	c, err := svc.Create(ctx, CreateInput{Name: "prod", CredentialID: cid})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	client, err := svc.ClientFor(ctx, c.ID)
	if err != nil {
		t.Fatalf("ClientFor: %v", err)
	}
	if client.Endpoint() != "https://10.0.0.8:6443" {
		t.Errorf("endpoint = %q", client.Endpoint())
	}
	if _, err := svc.ClientFor(ctx, "no-such-cluster"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ClientFor(未知集群) = %v", err)
	}
}
