//go:build realcluster

// 真集群验:对着**真实 API server** 走一遍「解析清单 → server-side apply → 滚动门控 →
// 换镜像 → 回滚 → 粘贴集群现值再 apply」。这些是假服务器(internal/kube/kubetest)证不了的:
// 假服务器的规则是我们自己写的,它点头不代表真服务器也点头。具体四件事:
//
//  1. SSA 的 Content-Type / fieldManager / force 真被接受(真服务器对未知 patch 类型直接 415);
//  2. 同一份正文重复 apply **不该**白滚一次(真服务器靠内容比对决定 generation 是否进);
//  3. 滚动门控在真 status 上收敛(observedGeneration / updatedReplicas / readyReplicas 由控制器写);
//  4. 从集群读回来的正文(带 resourceVersion / uid / managedFields / status)粘回来必须还能 apply
//     —— 剪不掉服务器写的那些字段就会撞 409,这条只在真服务器上才试得出来。
//
// 默认不参与构建(需 -tags realcluster)。跑法(OrbStack 单节点):
//
//	orbctl start k8s
//	kubectl create namespace pw-smoke
//	docker tag pw-e2e-centos-ssh:test pw-smoke-v2   # 第二个可调度的镜像(同机可拉,免仓库)
//	PW_KUBECONFIG=$HOME/.orbstack/k8s/config.yml \
//	  go test -tags realcluster -run TestRealCluster -v ./internal/kube/
//
// 只在 PW_SMOKE_NS(默认 pw-smoke)里建对象,结束用 kubectl 删该命名空间即可。
package kube

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

// realClusterClient 用真 kubeconfig 直接造客户端(不经 DB/vault:这里要验的是与 API server
// 的契约本身,不是集群登记那条链)。缺 env 就 Skip —— 真机验不是 CI 的常态。
func realClusterClient(t *testing.T) (*Client, string, string, string) {
	t.Helper()
	path := os.Getenv("PW_KUBECONFIG")
	if path == "" {
		t.Skip("PW_KUBECONFIG 未设置;跳过真集群验")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 kubeconfig %s: %v", path, err)
	}
	acc, err := parseKubeConfig(string(raw))
	if err != nil {
		t.Fatalf("解析 kubeconfig: %v", err)
	}
	ns := envOr("PW_SMOKE_NS", "pw-smoke")
	img1 := envOr("PW_SMOKE_IMAGE", "pw-e2e-centos-ssh:test")
	img2 := envOr("PW_SMOKE_IMAGE2", "pw-smoke-v2")
	if img1 == img2 {
		t.Fatal("两个镜像 ref 相同,换镜像那一步什么都验不到")
	}
	c := newClient(acc)
	t.Cleanup(func() {
		// 只清自己建的两个对象;失败不掩盖测试结论(断言已经跑完)。
		for _, spec := range []string{"deployment/pw-smoke-api", "service/pw-smoke-api"} {
			out, err := exec.Command("kubectl", "delete", spec, "-n", ns, "--ignore-not-found", "--wait=false").CombinedOutput()
			if err != nil {
				t.Logf("清理 %s 失败(可手工 kubectl delete ns %s):%v %s", spec, ns, err, out)
			}
		}
	})
	return c, ns, img1, img2
}

// smokeManifest 是一份两文档清单(Service + Deployment):命名空间**写在 YAML 里**,
// 因为「以 yaml 为准」正是本次要验的规则;镜像留成 {{IMAGE}},由调用方渲染。
func smokeManifest(ns, name, image string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: %s
  namespace: %s
  labels:
    app: %s
spec:
  selector:
    app: %s
  ports:
  - port: 80
    targetPort: 22
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: %s
spec:
  replicas: 2
  selector:
    matchLabels:
      app: %s
  template:
    metadata:
      labels:
        app: %s
    spec:
      containers:
      - name: app
        image: %s
        imagePullPolicy: IfNotPresent
`, name, ns, name, name, name, ns, name, name, image)
}

func TestRealClusterApplyAndRollout(t *testing.T) {
	ctx := context.Background()
	c, ns, img1, img2 := realClusterClient(t)

	ver, err := c.Version(ctx)
	if err != nil {
		t.Fatalf("GET version: %v", err)
	}
	t.Logf("真集群版本: %s(endpoint %s)", ver, c.Endpoint())

	docs, err := ParseManifests(smokeManifest(ns, "pw-smoke-api", img1), "default")
	if err != nil {
		t.Fatalf("ParseManifests: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("want 2 docs, got %d", len(docs))
	}
	for _, d := range docs {
		// 清单写了 namespace 就不该被默认值顶掉(默认值只服务"清单没写"的那种)。
		if d.Namespace != ns {
			t.Fatalf("%s 的命名空间 = %q, want %q(清单写的不能被默认值覆盖)", d.Kind, d.Namespace, ns)
		}
	}
	svc, dep := docs[0], docs[1]
	if svc.Kind != "Service" || dep.Kind != "Deployment" {
		t.Fatalf("文档顺序/类型不符:%s, %s", svc.Kind, dep.Kind)
	}

	w, ok := dep.AsWorkload()
	if !ok {
		t.Fatalf("Deployment 清单该能折成 Workload,got %+v", dep)
	}

	// 建:两文档按序 apply(先 Service 后 Deployment,与用户文件里的顺序一致)。
	for _, m := range docs {
		created, err := c.Apply(ctx, m)
		if err != nil {
			t.Fatalf("apply %s: %v", m, err)
		}
		if !created {
			t.Errorf("首次 apply %s 应报「新建」(201),got created=false", m)
		}
	}

	// 滚动门控:真控制器要拉镜像起 2 个 pod,给它时间但不给到无限。
	st, err := c.WaitRollout(ctx, w, 3*time.Minute, 2*time.Second)
	if err != nil {
		t.Fatalf("WaitRollout(首版): %v", err)
	}
	if st.SpecReplicas != 2 || st.ReadyReplicas != 2 {
		t.Fatalf("首版应 2/2 就绪,got spec=%d ready=%d updated=%d avail=%d",
			st.SpecReplicas, st.ReadyReplicas, st.UpdatedReplicas, st.AvailableReplicas)
	}
	if len(st.Containers) != 1 || st.Containers[0].Image != img1 {
		t.Fatalf("首版镜像 = %+v, want %s", st.Containers, img1)
	}

	// 再 apply 同一份正文:内容没变,真服务器**不该**推进 generation(否则每次发布都白滚一轮)。
	before := st.Generation
	if _, err := c.Apply(ctx, dep); err != nil {
		t.Fatalf("重复 apply: %v", err)
	}
	after, err := c.Get(ctx, w)
	if err != nil {
		t.Fatalf("GET after 重复 apply: %v", err)
	}
	if after.Generation != before {
		t.Fatalf("同一份正文重复 apply 不该滚动:generation %d → %d", before, after.Generation)
	}

	// 换镜像 → 门控 → 回滚(平台现在这条腿就是这么发的)。
	// 容器名走 Get + ResolveContainer:这是生产侧的同一条收敛路径 —— 空名只在「规格只有一个容器」时
	// 才允许,多容器(带 sidecar)必须显式点名,否则会连 sidecar 的镜像一起换掉。
	container, err := st.ResolveContainer("")
	if err != nil {
		t.Fatalf("ResolveContainer(空名): %v", err)
	}
	if err := c.SetImage(ctx, w, container, img2); err != nil {
		t.Fatalf("SetImage(%s): %v", img2, err)
	}
	st2, err := c.WaitRollout(ctx, w, 3*time.Minute, 2*time.Second)
	if err != nil {
		t.Fatalf("WaitRollout(新版): %v", err)
	}
	if st2.Containers[0].Name != container || st2.Containers[0].Image != img2 {
		t.Fatalf("换镜像后 = %+v, want %s=%s", st2.Containers, container, img2)
	}
	if st2.Generation <= before {
		t.Fatalf("换了镜像 generation 必须前进,got %d(之前 %d)", st2.Generation, before)
	}
	if err := c.SetImage(ctx, w, container, img1); err != nil {
		t.Fatalf("回滚 SetImage(%s): %v", img1, err)
	}
	if _, err := c.WaitRollout(ctx, w, 3*time.Minute, 2*time.Second); err != nil {
		t.Fatalf("WaitRollout(回滚后): %v", err)
	}
}

// TestRealClusterApplyFromClusterYAML 验的是「把集群现值粘回来再发一次」这条真实用户路径:
// kubectl 输出的正文带着服务器写好的 resourceVersion / uid / managedFields / status,
// 直接 apply 会被 409 或 422 挡回来 —— 解析期剪不掉这些字段就是我们的 bug。
func TestRealClusterApplyFromClusterYAML(t *testing.T) {
	ctx := context.Background()
	c, ns, img1, _ := realClusterClient(t)

	// 先确保对象在(复用上条用例建的东西;单独跑这条时自己建)。
	if _, err := c.Get(ctx, Workload{Kind: "Deployment", Namespace: ns, Name: "pw-smoke-api"}); err != nil {
		docs, perr := ParseManifests(smokeManifest(ns, "pw-smoke-api", img1), "")
		if perr != nil {
			t.Fatalf("ParseManifests: %v", perr)
		}
		for _, d := range docs {
			if _, aerr := c.Apply(ctx, d); aerr != nil {
				t.Fatalf("apply %s: %v", d, aerr)
			}
		}
		if _, err = c.WaitRollout(ctx, Workload{Kind: "Deployment", Namespace: ns, Name: "pw-smoke-api"}, 3*time.Minute, 2*time.Second); err != nil {
			t.Fatalf("WaitRollout: %v", err)
		}
	}

	live, err := exec.Command("kubectl", "get", "deployment", "pw-smoke-api", "-n", ns, "-o", "yaml").Output()
	if err != nil {
		t.Fatalf("kubectl get -o yaml: %v", err)
	}
	if !strings.Contains(string(live), "resourceVersion:") || !strings.Contains(string(live), "managedFields") && !strings.Contains(string(live), "creationTimestamp") {
		t.Fatalf("夹具不成立:kubectl 输出里没看到服务器写的字段,这条验不了什么:\n%.200s", live)
	}

	docs, err := ParseManifests(string(live), "")
	if err != nil {
		t.Fatalf("解析集群正文: %v", err)
	}
	if len(docs) != 1 || docs[0].Kind != "Deployment" {
		t.Fatalf("want 1 Deployment, got %d 份(%s)", len(docs), docs)
	}
	for _, f := range []string{"resourceVersion", "uid", "creationTimestamp", "managedFields", "status:", "generation"} {
		if strings.Contains(docs[0].Body, f) {
			t.Errorf("正文里还留着服务器写的 %q —— 粘回来 apply 会撞 409/422", f)
		}
	}
	created, err := c.Apply(ctx, docs[0])
	if err != nil {
		t.Fatalf("apply 集群正文: %v", err)
	}
	if created {
		t.Error("对象已存在,apply 它不该报「新建」")
	}
}
