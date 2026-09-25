// apply_test.go 锁住清单这条腿的两端:**解析期挡下什么**,以及**交到集群手里的请求长什么样**。
//
// 断言重点刻意放在「没发生什么」上:Secret 不能发出去、未渲染的占位符不能发出去、
// 同样的清单重跑不该让控制器再滚一次。这些「不该」用接口桩是测不出来的。
package kube

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/huangchengsir/pipewright/internal/kube/kubetest"
)

func TestParseManifestsSplitsDocsAndStampsNamespace(t *testing.T) {
	const text = `apiVersion: apps/v1
kind: Deployment
metadata: {name: api} # 没写命名空间:该拿集群登记的默认值
spec:
  replicas: 2
  template:
    spec:
      containers:
      - name: app
        image: "registry.local/api:aaaaaaa"
---
# 只有注释的档:不算资源,也不报错
---

---
apiVersion: v1
kind: Service
metadata: {name: api, namespace: edge}
spec:
  ports: [{port: 80}]
`
	got, err := ParseManifests(text, "shop")
	if err != nil {
		t.Fatalf("ParseManifests: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("应拆出 2 份可用文档,实际 %d:%v", len(got), got)
	}
	deploy, svc := got[0], got[1]
	if deploy.Kind != "Deployment" || deploy.Namespace != "shop" || deploy.Name != "api" {
		t.Errorf("第一份 = %+v", deploy)
	}
	if deploy.DocNum != 1 || svc.DocNum != 4 {
		t.Errorf("文档序号要按原文数(报错时说得出是哪一份):%d / %d", deploy.DocNum, svc.DocNum)
	}
	// 写了的命名空间不能被默认值覆盖 —— 这条是「命名空间以 yaml 为准」的全部含义。
	if svc.Namespace != "edge" {
		t.Errorf("Service 命名空间 = %q,应为 edge(默认值只补空缺)", svc.Namespace)
	}
	if deploy.plural != "deployments" || svc.plural != "services" {
		t.Errorf("plural 查表 = %q / %q", deploy.plural, svc.plural)
	}
	// 正文里必须留着作者写的意思(注释与书写顺序都算),否则回滚件读起来像机器吐的。
	if !strings.Contains(deploy.Body, "# 没写命名空间") {
		t.Errorf("正文丢了注释:%s", deploy.Body)
	}
}

func TestParseManifestsRefusesToGuessNamespace(t *testing.T) {
	const text = `apiVersion: v1
kind: ConfigMap
metadata: {name: app-settings}
data: {LOG_LEVEL: debug}
`
	if _, err := ParseManifests(text, ""); err == nil {
		t.Fatal("清单没写命名空间、集群也没登记默认值时必须报错,不能静默发到 default")
	} else if !errors.Is(err, ErrManifestInvalid) || !strings.Contains(err.Error(), "default") {
		t.Errorf("err = %v", err)
	}
	// 给了默认值就顺了 —— 同一份文本,只差那一格配置。
	got, err := ParseManifests(text, "shop")
	if err != nil {
		t.Fatalf("带默认命名空间应通过:%v", err)
	}
	if got[0].Namespace != "shop" {
		t.Errorf("namespace = %q", got[0].Namespace)
	}
}

func TestParseManifestsRejectsUnusableDocs(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string // 报错里必须出现的关键词:光「不合法」没法照着改
	}{
		{
			name: "Secret 点名拒",
			text: "apiVersion: v1\nkind: Secret\nmetadata: {name: s, namespace: shop}\ndata: {token: YWJj}\n",
			want: "机密",
		},
		{
			name: "类型不在白名单",
			text: "apiVersion: apps/v1\nkind: DaemonSet\nmetadata: {name: d, namespace: shop}\n",
			want: "DaemonSet",
		},
		{
			name: "kind 与 apiVersion 不配对",
			text: "apiVersion: apps/v1\nkind: Service\nmetadata: {name: s, namespace: shop}\n",
			want: "v1",
		},
		{
			name: "缺 kind",
			text: "apiVersion: v1\nmetadata: {name: s, namespace: shop}\n",
			want: "kind",
		},
		{
			name: "缺资源名",
			text: "apiVersion: v1\nkind: Service\nmetadata: {namespace: shop}\n",
			want: "资源名",
		},
		{
			name: "名字能改 URL 指向",
			text: "apiVersion: v1\nkind: Service\nmetadata: {name: ../../admin, namespace: shop}\n",
			want: "非法",
		},
		{
			name: "变量没被替换(块式)",
			text: "apiVersion: v1\nkind: Service\nmetadata:\n  name: svc-{{ENV}}\n  namespace: shop\n",
			want: "{{ENV}}",
		},
		{
			// 未加引号的 {{X}} 写在流式 {...} 里会被当成嵌套映射:YAML 直接语法失败。
			// 报错必须自己点出变量,否则「缺个 ,」没人反推得出来。
			name: "变量没被替换(流式,坏掉的是 YAML 本身)",
			text: "apiVersion: v1\nkind: Service\nmetadata: {name: svc-{{ENV}}, namespace: shop}\n",
			want: "{{ENV}}",
		},
		{
			name: "非资源对象",
			text: "- one\n- two\n",
			want: "没有一份",
		},
		{
			name: "空文本",
			text: "",
			want: "没有一份",
		},
		{
			name: "YAML 本身不合法",
			text: "apiVersion: v1\n\tkind: Service\n",
			want: "第 1 份文档解析失败",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseManifests(tc.text, "shop")
			if err == nil {
				t.Fatal("应被拒")
			}
			if !errors.Is(err, ErrManifestInvalid) {
				t.Errorf("错误类型应是 ErrManifestInvalid,收到 %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("报错里该有 %q:%v", tc.want, err)
			}
		})
	}
}

// 白名单之外的类型必须**一个请求都不发**:路径是按 kind 查表拼出来的,猜错 plural
// 就等于把补丁打到另一种资源上,而 API server 不会提醒。
func TestParseRejectionSendsNothing(t *testing.T) {
	f := kubetest.New()
	url, stop := startFake(t, f)
	defer stop()
	cli := testClient(t, url)

	_, err := ParseManifests("apiVersion: apps/v1\nkind: DaemonSet\nmetadata: {name: d, namespace: shop}\n", "shop")
	if err == nil {
		t.Fatal("DaemonSet 应被拒")
	}
	if n := len(f.Requests()); n != 0 {
		t.Errorf("解析期就该挡下,却发了 %d 个请求:%v", n, f.Requests())
	}
	// 对照组:合法清单真能应用出去(证明上一条不是「什么都发不出去」造成的假绿)。
	ms, err := ParseManifests(seedDeploymentYAML, "shop")
	if err != nil {
		t.Fatalf("ParseManifests: %v", err)
	}
	if _, err := cli.Apply(context.Background(), ms[0]); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !f.Exists("Deployment", "shop", "api") {
		t.Error("合法清单没落到集群里")
	}
}

func TestParseManifestsDropsServerWrittenFields(t *testing.T) {
	// 这份是 kubectl get -o yaml 的原样导出:直接粘过来最常见,而带着 resourceVersion
	// 去 apply 只会撞一次谁也看不懂的 409。
	const dump = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: shop
  uid: 1f2e-3d4c
  resourceVersion: "8888"
  generation: 7
  creationTimestamp: "2026-01-01T00:00:00Z"
  managedFields:
  - manager: kubectl
spec:
  replicas: 2
  template:
    spec:
      containers:
      - name: app
        image: registry.local/api:aaaaaaa
status:
  readyReplicas: 2
`
	ms, err := ParseManifests(dump, "shop")
	if err != nil {
		t.Fatalf("ParseManifests: %v", err)
	}
	body := ms[0].Body
	for _, gone := range []string{"resourceVersion", "uid:", "managedFields", "creationTimestamp", "status:", "readyReplicas"} {
		if strings.Contains(body, gone) {
			t.Errorf("正文里不该有服务器自己写的 %s:%s", gone, body)
		}
	}
	// 该留的还得在:replicas 与镜像是声明本体,丢了就等于把负载缩到 0 或换成空镜像。
	for _, keep := range []string{"replicas: 2", "registry.local/api:aaaaaaa", "name: api"} {
		if !strings.Contains(body, keep) {
			t.Errorf("正文丢了 %q:%s", keep, body)
		}
	}
}

func TestApplyCreatesThenReappliesWithoutRolling(t *testing.T) {
	f := kubetest.New()
	url, stop := startFake(t, f)
	defer stop()
	cli := testClient(t, url)

	const manifest = `apiVersion: apps/v1
kind: Deployment
metadata: {name: api, namespace: shop}
spec:
  replicas: 2
  template:
    spec:
      containers:
      - name: app
        image: "{{IMAGE}}"
`
	// 渲染在解析之前:这里的 {{IMAGE}} 已经是本次构建的产物引用。
	ms, err := ParseManifests(strings.ReplaceAll(manifest, "{{IMAGE}}", "registry.local/api:bbbbbbb"), "shop")
	if err != nil {
		t.Fatalf("ParseManifests: %v", err)
	}
	created, err := cli.Apply(context.Background(), ms[0])
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !created {
		t.Error("首次应用应报「新建」(201):这决定了日志里要不要写「建了个负载」")
	}
	if got := f.ImageOf("Deployment", "shop", "api", "app"); got != "registry.local/api:bbbbbbb" {
		t.Errorf("集群里的镜像 = %q", got)
	}
	gen1, _ := f.Generation("Deployment", "shop", "api")

	// 同一份清单重跑(同一 commit 再发一次):规格没变 → 控制器不该再滚一次。
	created, err = cli.Apply(context.Background(), ms[0])
	if err != nil {
		t.Fatalf("第二次 Apply: %v", err)
	}
	if created {
		t.Error("第二次不该再算新建")
	}
	if gen2, _ := f.Generation("Deployment", "shop", "api"); gen2 != gen1 {
		t.Errorf("规格未变却把 generation 从 %d 抬到 %d(会白滚一次)", gen1, gen2)
	}

	// 换了镜像才算一次变更:门控等滚动的前提是控制器确实看到了新规格。
	ms2, err := ParseManifests(strings.ReplaceAll(manifest, "{{IMAGE}}", "registry.local/api:ccccccc"), "shop")
	if err != nil {
		t.Fatalf("ParseManifests(第二版): %v", err)
	}
	if _, err := cli.Apply(context.Background(), ms2[0]); err != nil {
		t.Fatalf("Apply 第二版: %v", err)
	}
	if gen3, _ := f.Generation("Deployment", "shop", "api"); gen3 <= gen1 {
		t.Errorf("换了镜像后 generation 应前进:%d → %d", gen1, gen3)
	}
	if got := f.ImageOf("Deployment", "shop", "api", "app"); got != "registry.local/api:ccccccc" {
		t.Errorf("最终镜像 = %q", got)
	}
}

// 核心组(v1)与有组资源(apps/v1)的路径前缀不同 —— 拼错一个 `/apis` 就是 404,
// 而错误体只会说「找不到资源」。
func TestApplyRequestShapePerGroup(t *testing.T) {
	f := kubetest.New()
	url, stop := startFake(t, f)
	defer stop()
	cli := testClient(t, url)

	ms, err := ParseManifests(`apiVersion: apps/v1
kind: Deployment
metadata: {name: api, namespace: shop}
spec:
  replicas: 1
  template:
    spec:
      containers:
      - name: app
        image: registry.local/api:aaaaaaa
---
apiVersion: v1
kind: Service
metadata: {name: api, namespace: shop}
spec:
  ports:
  - port: 80
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata: {name: web, namespace: shop}
spec:
  rules: [{host: shop.example}]
`, "shop")
	if err != nil {
		t.Fatalf("ParseManifests: %v", err)
	}
	for i, m := range ms {
		if _, err := cli.Apply(context.Background(), m); err != nil {
			t.Fatalf("Apply #%d(%s): %v", i, m, err)
		}
	}
	got := f.Requests()
	// 文档顺序即应用顺序:Service 先于 Deployment 时,先建服务再滚 pod 才是常见接法。
	wantPrefixes := []string{
		"PATCH /apis/apps/v1/namespaces/shop/deployments/api?",
		"PATCH /api/v1/namespaces/shop/services/api?",
		"PATCH /apis/networking.k8s.io/v1/namespaces/shop/ingresses/web?",
	}
	for i, want := range wantPrefixes {
		if i >= len(got) || !strings.HasPrefix(got[i], want) {
			t.Errorf("第 %d 个请求应是 %s,实际 %v", i+1, want, got)
			continue
		}
		// fieldManager 与 force 在 query 里:少了它,SSA 在真集群上不是这个语义。
		if !strings.Contains(got[i], "fieldManager=pipewright") || !strings.Contains(got[i], "force=true") {
			t.Errorf("apply query 不完整:%s", got[i])
		}
	}
	if ct := f.LastPatchContentType(); ct != "application/apply-patch+yaml" {
		t.Errorf("Content-Type = %q,apply 用成 merge-patch 在真集群上是 415", ct)
	}
}

func TestApplyMapsServerErrors(t *testing.T) {
	f := kubetest.New()
	url, stop := startFake(t, f)
	defer stop()
	cli := testClient(t, url)
	ms, err := ParseManifests(seedDeploymentYAML, "shop")
	if err != nil {
		t.Fatalf("ParseManifests: %v", err)
	}

	// apply 时 API server 说路径不存在,几乎只有一种成因:命名空间还没建。
	// 报成「负载不存在」会让人去查名字,而名字是对的。
	f.SetMissing(true)
	_, err = cli.Apply(context.Background(), ms[0])
	if !errors.Is(err, ErrNamespaceMissing) {
		t.Errorf("404 → %v, want ErrNamespaceMissing", err)
	}
	if !strings.Contains(err.Error(), "shop") {
		t.Errorf("报错要点出命名空间:%v", err)
	}

	f.SetMissing(false)
	f.SetDeny(true)
	if _, err := cli.Apply(context.Background(), ms[0]); !errors.Is(err, ErrDenied) {
		t.Errorf("401 → %v, want ErrDenied", err)
	}
}

// 清单里的负载要能被门控复用:非负载类型没有「滚完」这回事,别拿部署等的字段去问它。
func TestManifestAsWorkload(t *testing.T) {
	ms, err := ParseManifests(seedDeploymentYAML+`---
apiVersion: v1
kind: ConfigMap
metadata: {name: cfg, namespace: shop}
data: {A: b}
`, "shop")
	if err != nil {
		t.Fatalf("ParseManifests: %v", err)
	}
	w, ok := ms[0].AsWorkload()
	if !ok || w.Kind != "Deployment" || w.Namespace != "shop" || w.Name != "api" {
		t.Errorf("Deployment 应可转成负载定位:%+v ok=%v", w, ok)
	}
	if _, ok := ms[1].AsWorkload(); ok {
		t.Error("ConfigMap 不该被当负载等滚动")
	}
	if IsWorkloadKind("ConfigMap") || !IsWorkloadKind(" StatefulSet ") {
		t.Error("IsWorkloadKind 判定不对")
	}
}
