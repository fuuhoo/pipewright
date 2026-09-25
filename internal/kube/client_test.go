// client_test.go 用 kubetest 的假 API server 验证与集群的所有 HTTP 契约:
// 路径、方法、Content-Type、补丁正文、门控轮询与错误映射。
//
// 为什么打真 HTTP 而不是抽一层接口桩:这层的全部风险都在「请求到底长什么样」——
// 路径拼错、补丁用错类型、把 sidecar 的容器换了。接口桩会把这些都替我「想当然」掉。
// 真集群接入时同一组断言直接复用(见项目内的 e2e 说明),不改动客户端。
package kube

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/huangchengsir/pipewright/internal/kube/kubetest"
)

// seedDeploymentYAML 是种子负载:与真实自建集群最常见的形状对齐(2 副本、单业务容器)。
const seedDeploymentYAML = `apiVersion: apps/v1
kind: Deployment
metadata: {name: api, namespace: shop}
spec:
  replicas: 2
  template:
    spec:
      containers:
      - name: app
        image: registry.local/api:aaaaaaa
`

// sidecarDeploymentYAML 多一个日志容器:它存在的意义就是「不该被动」。
const sidecarDeploymentYAML = `apiVersion: apps/v1
kind: Deployment
metadata: {name: api, namespace: shop}
spec:
  replicas: 2
  template:
    spec:
      containers:
      - name: app
        image: registry.local/api:aaaaaaa
      - name: logging
        image: fluent-bit:2.0
`

func seedCluster(t *testing.T) *kubetest.Cluster {
	t.Helper()
	return clusterWith(t, seedDeploymentYAML)
}

// clusterWith 用清单原文搭一个假集群(预置走 apply,与生产输入同一条路)。
func clusterWith(t *testing.T, docs ...string) *kubetest.Cluster {
	t.Helper()
	f := kubetest.New()
	for _, d := range docs {
		f.MustApplyYAML(d)
	}
	return f
}

func startFake(t *testing.T, f *kubetest.Cluster) (string, func()) {
	t.Helper()
	url, closeFn := f.Server()
	return url, closeFn
}

func testClient(t *testing.T, server string) *Client {
	t.Helper()
	a, err := parseKubeConfig(kubeConfigYAML(server, "tok-123456"))
	if err != nil {
		t.Fatalf("parse kubeconfig: %v", err)
	}
	return newClient(a)
}

// kubeConfigYAML 是严格校验版(不写 insecure-skip-tls-verify):它专门给
// 「自签证书必须连不上」那条断言用,受信版走 kubetest 的构造器。
func kubeConfigYAML(server, token string) string {
	return `apiVersion: v1
kind: Config
current-context: prod-ctx
clusters:
- name: prod
  cluster:
    server: ` + server + `
users:
- name: ci
  user:
    token: ` + token + `
contexts:
- name: prod-ctx
  context:
    cluster: prod
    user: ci
    namespace: shop
`
}

func appDeploy() Workload {
	return Workload{Kind: "Deployment", Namespace: "shop", Name: "api"}
}

func TestClientVersionSendsBearerToken(t *testing.T) {
	f := seedCluster(t)
	url, stop := startFake(t, f)
	defer stop()

	v, err := testClient(t, url).Version(context.Background())
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if v != "v1.29.2 / linux/amd64" {
		t.Errorf("version = %q", v)
	}
	if got := f.Requests(); len(got) != 1 || got[0] != "GET /version" {
		t.Errorf("requests = %v", got)
	}
	// 不带凭据的客户端在真集群那里一律 401;这里若漏装配,探测反而会「成功」。
	if got := f.LastAuth(); got != "Bearer tok-123456" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestClientGetReadsImagesAndReplicas(t *testing.T) {
	url, stop := startFake(t, seedCluster(t))
	defer stop()

	st, err := testClient(t, url).Get(context.Background(), appDeploy())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if st.SpecReplicas != 2 || st.Generation != 1 {
		t.Errorf("state = %+v", st)
	}
	img, err := st.ImageOf("") // 单容器:不必指名
	if err != nil {
		t.Fatalf("ImageOf: %v", err)
	}
	if img != "registry.local/api:aaaaaaa" {
		t.Errorf("image = %q", img)
	}
}

func TestClientSetImagePatchesOnlyTargetContainer(t *testing.T) {
	f := clusterWith(t, sidecarDeploymentYAML)
	url, stop := startFake(t, f)
	defer stop()
	cli := testClient(t, url)

	st, err := cli.Get(context.Background(), appDeploy())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	name, err := st.ResolveContainer("app")
	if err != nil {
		t.Fatalf("ResolveContainer: %v", err)
	}
	if err := cli.SetImage(context.Background(), appDeploy(), name, "registry.local/api:bbbbbbb"); err != nil {
		t.Fatalf("SetImage: %v", err)
	}
	if ct := f.LastPatchContentType(); ct != "application/strategic-merge-patch+json" {
		t.Errorf("补丁 Content-Type = %q", ct)
	}
	if got := f.ImageOf("Deployment", "shop", "api", "logging"); got != "fluent-bit:2.0" {
		t.Errorf("sidecar 镜像被动了:%q", got)
	}
	if got := f.ImageOf("Deployment", "shop", "api", "app"); got != "registry.local/api:bbbbbbb" {
		t.Errorf("app 镜像 = %q", got)
	}
}

func TestClientMultiContainerRequiresExplicitName(t *testing.T) {
	f := clusterWith(t, sidecarDeploymentYAML)
	url, stop := startFake(t, f)
	defer stop()
	cli := testClient(t, url)

	st, err := cli.Get(context.Background(), appDeploy())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := st.ResolveContainer(""); err == nil {
		t.Fatal("多容器时空名必须报错,不能猜一个")
	} else if !strings.Contains(err.Error(), "logging") {
		t.Errorf("报错应列出可选容器:%v", err)
	}
	if _, err := st.ImageOf("nope"); !errors.Is(err, ErrNoContainer) {
		t.Errorf("ImageOf(nope) = %v", err)
	}
}

func TestClientWaitRolloutPollsUntilReady(t *testing.T) {
	f := seedCluster(t)
	f.SetRolloutStall(2) // 每次变更后先报两次「未就绪」
	url, stop := startFake(t, f)
	defer stop()
	cli := testClient(t, url)
	// 先真的滚一次:滚动中才有「未就绪」可等,否则第一次读就是完成,测不到轮询。
	if err := cli.SetImage(context.Background(), appDeploy(), "app", "registry.local/api:bbbbbbb"); err != nil {
		t.Fatalf("SetImage: %v", err)
	}

	st, err := cli.WaitRollout(context.Background(), appDeploy(), 10*time.Second, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("WaitRollout: %v", err)
	}
	if st.ReadyReplicas != 2 {
		t.Errorf("就绪副本 %d", st.ReadyReplicas)
	}
	// 未就绪时必须再来一次,而不是读一次就报成功(那正是「秒过」的坑)。
	if n := f.CountRequests("GET /apis/apps/v1/namespaces/shop/deployments/api"); n < 2 {
		t.Errorf("应至少轮询 2 次,实际 %d:%v", n, f.Requests())
	}
}

func TestClientWaitRolloutTimeoutReportsProgress(t *testing.T) {
	f := seedCluster(t)
	f.SetRolloutStall(-1) // 永不收敛
	url, stop := startFake(t, f)
	defer stop()
	cli := testClient(t, url)
	if err := cli.SetImage(context.Background(), appDeploy(), "app", "registry.local/api:bbbbbbb"); err != nil {
		t.Fatalf("SetImage: %v", err)
	}

	_, err := cli.WaitRollout(context.Background(), appDeploy(), 120*time.Millisecond, 10*time.Millisecond)
	if err == nil {
		t.Fatal("永不就绪必须超时报错")
	}
	if !strings.Contains(err.Error(), "未在") || !strings.Contains(err.Error(), "新版本 pod 0/2") {
		t.Errorf("超时报错应带进度:%v", err)
	}
}

func TestClientWaitRolloutToleratesOneReadFailure(t *testing.T) {
	f := seedCluster(t)
	url, stop := startFake(t, f)
	defer stop()
	cli := testClient(t, url)

	// API server 抖一下(GET 返 404)不该立刻判死:让它先失败一次再恢复。
	f.SetMissing(true)
	go func() {
		time.Sleep(30 * time.Millisecond)
		f.SetMissing(false)
	}()
	if _, err := cli.WaitRollout(context.Background(), appDeploy(), 3*time.Second, 10*time.Millisecond); err != nil {
		t.Fatalf("单次读失败应被下一次轮询覆盖:%v", err)
	}
}

func TestClientMapsAuthAndNotFound(t *testing.T) {
	deny := seedCluster(t)
	deny.SetDeny(true)
	url1, stop1 := startFake(t, deny)
	defer stop1()
	_, err := testClient(t, url1).Version(context.Background())
	if !errors.Is(err, ErrDenied) {
		t.Errorf("401 → %v, want ErrDenied", err)
	}
	// 401 的 message 摘录出来才有可操作性(「凭据无效 or 没权限」)。
	if !strings.Contains(err.Error(), "credentials") {
		t.Errorf("错误应摘录 API server 的 reason/message:%v", err)
	}

	gone := seedCluster(t)
	gone.SetMissing(true)
	url2, stop2 := startFake(t, gone)
	defer stop2()
	if _, err := testClient(t, url2).Get(context.Background(), appDeploy()); !errors.Is(err, ErrWorkloadNotFound) {
		t.Errorf("404 → %v, want ErrWorkloadNotFound", err)
	}
}

func TestClientRejectsUnsafeNamesBeforeAnyRequest(t *testing.T) {
	f := seedCluster(t)
	url, stop := startFake(t, f)
	defer stop()
	cli := testClient(t, url)

	for _, w := range []Workload{
		{Kind: "Deployment", Namespace: "shop", Name: "api/../admin"},
		{Kind: "Deployment", Namespace: "SHOP", Name: "api"}, // 大写不是合法 DNS 名
		{Kind: "Deployment", Namespace: "", Name: "api"},     // 空命名空间会落到意外默认值
		{Kind: "Deployment", Namespace: "shop", Name: "-x"},
		{Kind: "Deployment", Namespace: "shop", Name: "api%2f"},
		{Kind: "CronJob", Namespace: "shop", Name: "api"}, // 不在白名单
	} {
		if _, err := cli.Get(context.Background(), w); err == nil {
			t.Errorf("%+v 应被拒", w)
		}
	}
	for _, r := range f.Requests() {
		if strings.Contains(r, "..") || strings.Contains(r, "%") {
			t.Fatalf("非法名竟然发了请求:%v", f.Requests())
		}
	}
	if n := len(f.Requests()); n != 0 {
		t.Errorf("非法输入应在发请求之前就被挡下:%v", f.Requests())
	}
}

func TestClientRestartPatchesAnnotation(t *testing.T) {
	f := seedCluster(t)
	url, stop := startFake(t, f)
	defer stop()
	if err := testClient(t, url).Restart(context.Background(), appDeploy()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if v := templateAnnotation(f, "pipewright.io/restartedAt"); v == "" {
		t.Error("restart 注解未送达")
	}
}

// templateAnnotation 读 pod 模板上的一个注解(集群里现在到底有没有这一刀)。
func templateAnnotation(f *kubetest.Cluster, key string) string {
	doc, ok := f.Doc("Deployment", "shop", "api")
	if !ok {
		return ""
	}
	spec, _ := doc["spec"].(map[string]any)
	tmpl, _ := spec["template"].(map[string]any)
	meta, _ := tmpl["metadata"].(map[string]any)
	ann, _ := meta["annotations"].(map[string]any)
	v, _ := ann[key].(string)
	return v
}

func TestClientStatefulSetUsesItsOwnPath(t *testing.T) {
	f := clusterWith(t, seedDeploymentYAML, `apiVersion: apps/v1
kind: StatefulSet
metadata: {name: db, namespace: shop}
spec:
  replicas: 1
  template:
    spec:
      containers:
      - name: db
        image: registry.local/db:aaaaaaa
`)
	url, stop := startFake(t, f)
	defer stop()
	if _, err := testClient(t, url).Get(context.Background(), Workload{Kind: "StatefulSet", Namespace: "shop", Name: "db"}); err != nil {
		t.Fatalf("Get statefulset: %v", err)
	}
	if got := f.Requests(); len(got) == 0 || !strings.HasSuffix(got[0], "/statefulsets/db") {
		t.Errorf("requests = %v", got)
	}
}

func TestClientOverTLSServer(t *testing.T) {
	f := seedCluster(t)
	url, stop := f.TLSServer()
	defer stop()

	// insecure-skip-tls-verify:与真集群上「自建 CA 且不想粘 CA」的接法同一条路。
	a, err := parseKubeConfig(f.KubeConfig(url, "t1", "shop", true))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := newClient(a).Version(context.Background()); err != nil {
		t.Fatalf("insecure TLS: %v", err)
	}

	// 同一地址,不跳过校验:必须失败在证书上,而不是"看起来连上了"。
	strict, err := parseKubeConfig(kubeConfigYAML(url, "t1"))
	if err != nil {
		t.Fatalf("parse strict: %v", err)
	}
	if _, err := newClient(strict).Version(context.Background()); !errors.Is(err, ErrUnreachable) {
		t.Errorf("未受信证书应 ErrUnreachable, got %v", err)
	}
}

// 客户端证书(mTLS)接法:解析出证书不等于用上了证书。少装配那一步时,请求会匿名发出去,
// 真集群那里只留下一个 401 —— 而「凭据可用」的探测测试照样全绿。
func TestClientPresentsClientCert(t *testing.T) {
	f := seedCluster(t)
	url, stop := f.RequireClientCert()
	defer stop()

	crtB64, keyB64 := testCertPairBase64(t)
	a, err := parseKubeConfig(`apiVersion: v1
kind: Config
current-context: c
clusters:
- name: k
  cluster:
    server: ` + url + `
    insecure-skip-tls-verify: true
users:
- name: u
  user:
    client-certificate-data: ` + crtB64 + `
    client-key-data: ` + keyB64 + `
contexts:
- name: c
  context: {cluster: k, user: u}
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := newClient(a).Version(context.Background()); err != nil {
		t.Fatalf("mTLS: %v", err)
	}
	if !f.SawClientCert() {
		t.Error("握手没出示客户端证书")
	}
	// 证书接法不该再带一个空 Bearer:那是「头存在但值为空」,服务端可能按匿名处理。
	if got := f.LastAuth(); got != "" {
		t.Errorf("不该发 Authorization 头,收到 %q", got)
	}
}
