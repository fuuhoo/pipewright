// kubeconfig.go 解析 kubeconfig 文本,产出「连哪个 API server、用什么身份」的最小材料。
//
// 为什么存整份 kubeconfig 而不是「地址 + token」两列:集群接法至少有三种(自建 CA、公网
// 证书、客户端证书 mTLS),而地址与 CA 在 kubeconfig 里本来就是一对。拆开存就会出现两份
// 真值 —— kubeconfig 换了、页面地址没换,发布打到旧集群上没人知道。
//
// 解析失败的信息只说「哪里不对」,绝不带原文(kubeconfig 正文即凭据本体)。
// 不支持的接法(exec / auth-provider / proxy-url)一律明确报错:这些要靠本地二进制或
// 交互式登录才能拿到凭据,无人值守的流水线里只会表现成一句莫名的「连不上」。
package kube

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// 领域错误(错误体不含 kubeconfig 正文 / token / 证书内容)。
var (
	// ErrKubeConfigInvalid 表示 kubeconfig 解析不出可用的 API server 地址与身份。
	ErrKubeConfigInvalid = errors.New("kube: kubeconfig is not usable")
)

// access 是装配 HTTP 客户端所需的材料(进程内;token / 私钥绝不入库、入日志、入响应)。
type clusterAccess struct {
	server   string
	ns       string // kubeconfig 里 context 的默认命名空间(仅作集群行的预填参考)
	insecure bool
	rootCAs  *x509.CertPool
	token    string
	cert     tls.Certificate // 零值 = 无客户端证书
}

type kcCluster struct {
	Server                   string `yaml:"server"`
	CertificateAuthorityData string `yaml:"certificate-authority-data"`
	CertificateAuthority     string `yaml:"certificate-authority"`
	InsecureSkipTLSVerify    bool   `yaml:"insecure-skip-tls-verify"`
	ProxyURL                 string `yaml:"proxy-url"`
	TLSServerName            string `yaml:"tls-server-name"`
}

type kcUser struct {
	Token                 string `yaml:"token"`
	Username              string `yaml:"username"`
	Password              string `yaml:"password"`
	ClientCertificateData string `yaml:"client-certificate-data"`
	ClientCertificate     string `yaml:"client-certificate"`
	ClientKeyData         string `yaml:"client-key-data"`
	ClientKey             string `yaml:"client-key"`
	Exec                  any    `yaml:"exec"`
	AuthProvider          any    `yaml:"auth-provider"`
}

type kcContext struct {
	Cluster   string `yaml:"cluster"`
	Namespace string `yaml:"namespace"`
	AuthInfo  string `yaml:"user"`
}

type kcDoc struct {
	Clusters []struct {
		Name    string    `yaml:"name"`
		Cluster kcCluster `yaml:"cluster"`
	} `yaml:"clusters"`
	Users []struct {
		Name string `yaml:"name"`
		User kcUser `yaml:"user"`
	} `yaml:"users"`
	Contexts []struct {
		Name    string    `yaml:"name"`
		Context kcContext `yaml:"context"`
	} `yaml:"contexts"`
	CurrentContext string `yaml:"current-context"`
}

// parseKubeConfig 按 current-context 取一份可用接法。多 context 的 kubeconfig 只认
// current-context(与所有 kubectl 调用同一约定);要发别的 context,先 kubectl config use-context。
func parseKubeConfig(raw string) (clusterAccess, error) {
	var doc kcDoc
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		return clusterAccess{}, fmt.Errorf("%w:YAML 解析失败(%v)", ErrKubeConfigInvalid, trimYAMLErr(err))
	}
	if doc.CurrentContext == "" {
		return clusterAccess{}, fmt.Errorf("%w:缺少 current-context(请用 kubectl config use-context 选定后再粘贴)", ErrKubeConfigInvalid)
	}
	var ctx kcContext
	for _, c := range doc.Contexts {
		if c.Name == doc.CurrentContext {
			ctx = c.Context
			break
		}
	}
	if ctx.Cluster == "" {
		return clusterAccess{}, fmt.Errorf("%w:current-context %q 在 contexts 里找不到或缺 cluster", ErrKubeConfigInvalid, doc.CurrentContext)
	}
	var cl kcCluster
	found := false
	for _, c := range doc.Clusters {
		if c.Name == ctx.Cluster {
			cl, found = c.Cluster, true
			break
		}
	}
	if !found {
		return clusterAccess{}, fmt.Errorf("%w:clusters 里没有 %q", ErrKubeConfigInvalid, ctx.Cluster)
	}
	server := strings.TrimSpace(cl.Server)
	if server == "" {
		return clusterAccess{}, fmt.Errorf("%w:cluster %q 没有 server 地址", ErrKubeConfigInvalid, ctx.Cluster)
	}
	if !strings.HasPrefix(server, "http://") && !strings.HasPrefix(server, "https://") {
		return clusterAccess{}, fmt.Errorf("%w:server 地址必须是 http(s):// 开头,收到 %q", ErrKubeConfigInvalid, redactHost(server))
	}
	if cl.ProxyURL != "" {
		return clusterAccess{}, fmt.Errorf("%w:暂不支持 cluster.proxy-url(平台侧请走网络直连)", ErrKubeConfigInvalid)
	}
	if cl.CertificateAuthority != "" {
		return clusterAccess{}, fmt.Errorf("%w:只支持内联的 certificate-authority-data(CA 文件在平台主机上不存在)", ErrKubeConfigInvalid)
	}
	if cl.TLSServerName != "" {
		return clusterAccess{}, fmt.Errorf("%w:暂不支持 tls-server-name", ErrKubeConfigInvalid)
	}

	a := clusterAccess{server: strings.TrimRight(server, "/"), ns: strings.TrimSpace(ctx.Namespace), insecure: cl.InsecureSkipTLSVerify}

	if cl.CertificateAuthorityData != "" {
		pem, err := decodeB64(cl.CertificateAuthorityData)
		if err != nil {
			return clusterAccess{}, fmt.Errorf("%w:certificate-authority-data 不是合法 base64(%v)", ErrKubeConfigInvalid, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return clusterAccess{}, fmt.Errorf("%w:certificate-authority-data 里解析不出 CA 证书", ErrKubeConfigInvalid)
		}
		a.rootCAs = pool
	}
	var user kcUser
	if ctx.AuthInfo != "" {
		for _, u := range doc.Users {
			if u.Name == ctx.AuthInfo {
				user = u.User
				break
			}
		}
	} else if len(doc.Users) == 1 {
		// 少数导出工具会把 user 留空(例如只塞 token);只有一个 user 时不必苛求 context 写全。
		user = doc.Users[0].User
	}
	if user.Exec != nil || user.AuthProvider != nil {
		return clusterAccess{}, fmt.Errorf("%w:不支持 exec / auth-provider 取凭据(需要交互式登录)。请改用 ServiceAccount token 或客户端证书", ErrKubeConfigInvalid)
	}
	switch {
	case user.Token != "":
		a.token = user.Token
	case user.ClientCertificateData != "" || user.ClientKeyData != "":
		crt, err := decodeB64(user.ClientCertificateData)
		if err != nil {
			return clusterAccess{}, fmt.Errorf("%w:client-certificate-data 不是合法 base64(%v)", ErrKubeConfigInvalid, err)
		}
		key, err := decodeB64(user.ClientKeyData)
		if err != nil {
			return clusterAccess{}, fmt.Errorf("%w:client-key-data 不是合法 base64(%v)", ErrKubeConfigInvalid, err)
		}
		pair, err := tls.X509KeyPair(crt, key)
		if err != nil {
			return clusterAccess{}, fmt.Errorf("%w:客户端证书与私钥不成对(%v)", ErrKubeConfigInvalid, errKind(err))
		}
		a.cert = pair
		crt, key = nil, nil
	case user.Username != "" && user.Password != "":
		return clusterAccess{}, fmt.Errorf("%w:不支持 basic-auth(username/password)取凭据,请用 ServiceAccount token", ErrKubeConfigInvalid)
	default:
		return clusterAccess{}, fmt.Errorf("%w:context %q 的 user 没有任何凭据(token / 客户端证书)", ErrKubeConfigInvalid, doc.CurrentContext)
	}
	return a, nil
}

// decodeB64 容忍带换行的 base64(kubeconfig 里 data 字段常被折行)。
func decodeB64(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(strings.Join(strings.Fields(s), ""))
}

// errKind 只保留错误类型名,丢掉可能夹带证书/密钥内容的细节。
func errKind(err error) string {
	return strings.SplitN(err.Error(), ":", 2)[0]
}

// trimYAMLErr 截断 YAML 错误(其原文会带出错行的内容,kubeconfig 的出错行可能就是凭据)。
func trimYAMLErr(err error) error {
	msg := err.Error()
	if i := strings.IndexByte(msg, '\n'); i > 0 {
		msg = msg[:i]
	}
	if len(msg) > 120 {
		msg = msg[:120]
	}
	return errors.New(msg)
}

// redactHost 只保留 host 部分用于报错(路径/查询可能带 token)。
func redactHost(s string) string {
	if i := strings.IndexAny(s[strings.Index(s, "//")+2:], "/?"); i >= 0 {
		return s[:strings.Index(s, "//")+2+i]
	}
	return s
}
