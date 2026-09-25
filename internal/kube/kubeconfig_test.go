// kubeconfig_test.go 覆盖「能不能从一份 kubeconfig 里问出该连哪、拿什么身份」。
// 重点是那些**看起来能用其实不能用**的接法:exec 插件要本地二进制、CA 文件在平台主机上不存在、
// current-context 指向缺失的 cluster —— 这些必须在这里就报错,而不是等部署时表现成「连不上」。
package kube

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"
)

func TestParseKubeConfigToken(t *testing.T) {
	a, err := parseKubeConfig(kubeConfigWithToken("https://10.0.0.8:6443/", "sa-tok"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if a.server != "https://10.0.0.8:6443" {
		t.Errorf("server = %q(尾部斜杠应去掉,否则拼出的路径是 //apis)", a.server)
	}
	if a.token != "sa-tok" || a.ns != "shop" {
		t.Errorf("access = %+v", a)
	}
}

func TestParseKubeConfigPicksCurrentContext(t *testing.T) {
	raw := `apiVersion: v1
kind: Config
current-context: staging
clusters:
- name: prod
  cluster: {server: https://prod.example:6443}
- name: staging
  cluster: {server: https://staging.example:6443}
users:
- name: p
  user: {token: prod-token}
- name: s
  user: {token: staging-token}
contexts:
- name: prod
  context: {cluster: prod, user: p}
- name: staging
  context: {cluster: staging, user: s}
`
	a, err := parseKubeConfig(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if a.server != "https://staging.example:6443" || a.token != "staging-token" {
		t.Errorf("多 context 时必须按 current-context 取:%+v", a)
	}
}

func TestParseKubeConfigClientCert(t *testing.T) {
	// 自签一对测试证书(仅测试内生成,不外泄);base64 内联即 mTLS 接法。
	crtB64, keyB64 := testCertPairBase64(t)
	raw := `apiVersion: v1
current-context: c
clusters:
- name: k
  cluster:
    server: https://10.0.0.9:6443
    certificate-authority-data: ` + crtB64 + `
users:
- name: u
  user:
    client-certificate-data: ` + crtB64 + `
    client-key-data: ` + keyB64 + `
contexts:
- name: c
  context: {cluster: k, user: u}
`
	a, err := parseKubeConfig(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if a.rootCAs == nil {
		t.Error("CA 未装上")
	}
	if len(a.cert.Certificate) == 0 {
		t.Error("客户端证书未装上")
	}
}

func TestParseKubeConfigRejects(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string // 报错里必须出现的关键词(可操作性)
	}{
		{"空文档", "", "current-context"},
		{"非 YAML", "\tthis is not yaml:\n  - [", "YAML"},
		{"缺 current-context", "clusters: []", "current-context"},
		{"context 指向缺失 cluster", `current-context: c
contexts:
- name: c
  context: {cluster: nope, user: u}
users:
- name: u
  user: {token: t}
`, "nope"},
		{"server 无协议", `current-context: c
clusters:
- name: k
  cluster: {server: 10.0.0.8:6443}
contexts:
- name: c
  context: {cluster: k, user: u}
users:
- name: u
  user: {token: t}
`, "http(s)"},
		{"CA 走文件路径", `current-context: c
clusters:
- name: k
  cluster: {server: https://h:6443, certificate-authority: /home/me/.kube/ca.pem}
contexts:
- name: c
  context: {cluster: k, user: u}
users:
- name: u
  user: {token: t}
`, "certificate-authority-data"},
		{"exec 插件取凭据", `current-context: c
clusters:
- name: k
  cluster: {server: https://h:6443}
contexts:
- name: c
  context: {cluster: k, user: u}
users:
- name: u
  user:
    exec:
      command: aws-vault
      args: ["eks", "get-token"]
`, "ServiceAccount"},
		{"无任何凭据", `current-context: c
clusters:
- name: k
  cluster: {server: https://h:6443}
contexts:
- name: c
  context: {cluster: k, user: u}
users:
- name: u
  user: {}
`, "没有任何凭据"},
		{"CA 数据不是 base64", `current-context: c
clusters:
- name: k
  cluster: {server: https://h:6443, certificate-authority-data: "not base64 !!!"}
contexts:
- name: c
  context: {cluster: k, user: u}
users:
- name: u
  user: {token: t}
`, "base64"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseKubeConfig(tc.raw)
			if err == nil {
				t.Fatal("应报错")
			}
			if !errors.Is(err, ErrKubeConfigInvalid) {
				t.Errorf("err = %v, want ErrKubeConfigInvalid", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("报错应可操作(含 %q):%v", tc.want, err)
			}
		})
	}
}

// 报错里绝不能出现 token / 证书正文:错误会进运行日志与 HTTP 响应。
func TestParseErrorsNeverLeakSecrets(t *testing.T) {
	// 把 server 那一行改成无效键 → cluster 没有地址 → 报错路径被走到,而 token 就在同一份文档里。
	raw := strings.Replace(kubeConfigWithToken("https://10.0.0.8:6443", "topsecret-token-xyz"),
		"server:", "serverTypo:", 1)
	_, err := parseKubeConfig(raw)
	if err == nil {
		t.Fatal("应报错")
	}
	if strings.Contains(err.Error(), "topsecret-token-xyz") {
		t.Errorf("错误体泄漏了 token:%v", err)
	}
}

// testCertPairBase64 现生成一张自签证书与私钥(仅测试内使用,不落盘不外泄)。
func testCertPairBase64(t *testing.T) (crtB64, keyB64 string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "pipewright-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	crt := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	priv := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return base64.StdEncoding.EncodeToString(crt), base64.StdEncoding.EncodeToString(priv)
}
