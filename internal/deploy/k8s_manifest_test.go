// k8s_manifest_test.go 覆盖「应用清单」这条发布腿(deploy_k8s 节点的 manifestSource=repo/paste):
// 按序把清单里的每一份声明交给集群、负载等它滚完、滚不动时重新应用**上一版实际发出去的正文**,
// 并把这次真正交出去的声明随结果入库。
//
// 这些用例打的是**假 API server**(internal/kube/kubetest),但走的是真客户端:桩替我"想当然"
// 一遍就等于没测 —— 这条链路的全部风险都在请求长什么样(路径、顺序、滚没滚、回滚落到哪一版)。
package deploy

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/huangchengsir/pipewright/internal/kube"
	"github.com/huangchengsir/pipewright/internal/kube/kubetest"
	"github.com/huangchengsir/pipewright/internal/run"
	"github.com/huangchengsir/pipewright/internal/vault"
)

// ---- 脚手架:真 run.Service + 真 kube.Service(指向假集群) -------------------------

type k8sEnv struct {
	svc       Service
	rsvc      run.Service
	db        *sql.DB
	fake      *kubetest.Cluster
	clusterID string
}

// newK8sEnv 起一个假 API server 并把它登记成集群;nsDefault 是该集群的默认命名空间。
func newK8sEnv(t *testing.T, nsDefault string, preseed ...string) *k8sEnv {
	t.Helper()
	db := testDB(t)
	rsvc := run.New(db)
	v := vault.New(db, kubeTestKey())
	ksvc := kube.New(db, v)

	f := kubetest.New()
	for _, d := range preseed {
		f.MustApplyYAML(d)
	}
	url, stop := f.Server()
	t.Cleanup(stop)

	ctx := context.Background()
	cred, err := v.Create(vault.CreateInput{
		Type: vault.TypeKubeConfig, Name: "k8s-test", Secret: f.KubeConfig(url, "tok-test", "", false),
	})
	if err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	cl, err := ksvc.Create(ctx, kube.CreateInput{Name: "orb", CredentialID: cred.ID, NamespaceDefault: nsDefault})
	if err != nil {
		t.Fatalf("seed cluster: %v", err)
	}
	return &k8sEnv{svc: New(&stubTarget{}, rsvc, WithKube(ksvc)), rsvc: rsvc, db: db, fake: f, clusterID: cl.ID}
}

func kubeTestKey() *[32]byte {
	var k [32]byte
	for i := range k {
		k[i] = byte(i + 7)
	}
	return &k
}

// manifestCfg 是一份「粘贴清单」节点的部署 cfg(仓库文件那条路由 build 层现读后填进同一个键,
// 对部署层没有区别 —— 那条路的差异由 dag 层用例覆盖)。
func (e *k8sEnv) manifestCfg(body string) map[string]string {
	return map[string]string{
		CfgKeyClusterID:      e.clusterID,
		CfgKeyManifestSource: ManifestSourcePaste,
		CfgKeyManifestYaml:   body,
	}
}

// deploy 跑一次集群发布(单目标),返回该机结果。
func (e *k8sEnv) deploy(t *testing.T, runID string, cfg map[string]string) TargetResult {
	t.Helper()
	return e.deployCtx(t, context.Background(), runID, cfg)
}

func (e *k8sEnv) deployCtx(t *testing.T, ctx context.Context, runID string, cfg map[string]string) TargetResult {
	t.Helper()
	res, err := e.svc.DeployForStage(ctx, runID, []string{e.clusterID}, cfg, "")
	if err != nil {
		t.Fatalf("DeployForStage: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("want 1 target, got %d", len(res))
	}
	return res[0]
}

// captureCtxLog 收集 fn 这次发布回流到步骤日志的每一行(形如 "stdout → APPLY ..."),
// 供断言「这件事有没有说出口」—— 结果摘要里没写、只有命令日志里才看得见的事实最容易漏。
func captureCtxLog(t *testing.T, fn func(ctx context.Context)) []string {
	t.Helper()
	var mu sync.Mutex
	var lines []string
	ctx := WithCmdLog(context.Background(), func(stream, text string) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, stream+" "+text)
	})
	fn(ctx)
	return lines
}

func hasLogContaining(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// storedManifests 读回库里「这次实际交出去的声明」,形如 `Deployment shop/api #1 contains v1`。
func storedManifests(t *testing.T, db *sql.DB, runID string) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT kind, namespace, name, doc_ordinal, body FROM deploy_manifests WHERE run_id = ?
		 ORDER BY doc_ordinal`, runID)
	if err != nil {
		t.Fatalf("query manifests: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var kind, ns, name, body string
		var ord int
		if err := rows.Scan(&kind, &ns, &name, &ord, &body); err != nil {
			t.Fatalf("scan manifests: %v", err)
		}
		out = append(out, kind+" "+ns+"/"+name+" #"+strconv.Itoa(ord)+" 含镜像 "+imageIn(body))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate manifests: %v", err)
	}
	return out
}

// imageIn 取正文里那行镜像(没有则 "-"):断言入库的是**渲染后**的那份,而不是 {{IMAGE}}。
func imageIn(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "image:") {
			return strings.TrimSpace(strings.TrimPrefix(t, "image:"))
		}
	}
	return "-"
}

const img1 = "registry.local:5000/shop:v1"
const img2 = "registry.local:5000/shop:v2"

// deploymentManifest 是一份两份文档的清单(Deployment + Service):前者要等滚动,
// 后者应用成功即终态 —— 两条判定共用一次发布,顺序也在这里定。
// ns 为空时清单里不写 metadata.namespace(测「用集群登记的默认命名空间」)。
func deploymentManifest(ns, image string) string {
	meta := "  name: api\n"
	if ns != "" {
		meta = "  name: api\n  namespace: " + ns + "\n"
	}
	return `apiVersion: apps/v1
kind: Deployment
metadata:
` + meta + `spec:
  replicas: 1
  selector:
    matchLabels: {app: api}
  template:
    metadata:
      labels: {app: api}
    spec:
      containers:
      - name: api
        image: ` + image + `
---
apiVersion: v1
kind: Service
metadata:
` + meta + `spec:
  selector: {app: api}
  ports:
  - port: 80
`
}

// firstDoc 取多文档清单的第一份(预置集群现状时只要 Deployment 那一份)。
func firstDoc(manifest string) string {
	if i := strings.Index(manifest, "\n---\n"); i >= 0 {
		return manifest[:i]
	}
	return manifest
}

// ---- 用例 -------------------------------------------------------------------

// TestK8sManifestAppliesEveryDoc 验证清单那条腿的基本盘:两份声明都按序进集群、{{IMAGE}} 换成
// 本次镜像、命名空间由集群登记的默认值补上、结果为成功,且两份正文都随结果入库。
func TestK8sManifestAppliesEveryDoc(t *testing.T) {
	e := newK8sEnv(t, "shop")
	runID, _ := seedSuccessRunWithArtifact(t, e.db, e.rsvc, run.ArtifactImage, img1)

	tr := e.deploy(t, runID, e.manifestCfg(deploymentManifest("", "{{IMAGE}}")))
	if tr.Status != run.TargetSuccess {
		t.Fatalf("status = %s(%s), want success", tr.Status, tr.Message)
	}
	if !strings.Contains(tr.Message, "已应用 2 份清单") {
		t.Errorf("message 该说明发了几份:%s", tr.Message)
	}
	if got := e.fake.ImageOf("Deployment", "shop", "api", "api"); got != img1 {
		t.Errorf("集群里的镜像 = %q, want %q", got, img1)
	}
	if !e.fake.Exists("Service", "shop", "api") {
		t.Error("Service 也该被应用(它没有「滚」可等,但必须发出去)")
	}
	if !strings.Contains(tr.ServerName, "shop") {
		t.Errorf("展示名要带生效命名空间:%s", tr.ServerName)
	}
	got := storedManifests(t, e.db, runID)
	if len(got) != 2 {
		t.Fatalf("两份声明都该入库,got %v", got)
	}
	// 入库的必须是**渲染后**的那份:回滚要直接重放它,存 {{IMAGE}} 等于回滚到一个占位符。
	// (只有 Deployment 那份带镜像字段;Service 不引用镜像,别拿它断言。)
	if !strings.Contains(got[0], "Deployment shop/api #1") || !strings.Contains(got[0], img1) {
		t.Errorf("入库的必须是渲染后的 Deployment 正文:%s", got[0])
	}
	if !strings.Contains(got[1], "Service shop/api #2") {
		t.Errorf("第二份是 Service:%s", got[1])
	}
	for _, row := range got {
		if strings.Contains(row, "{{"+ImagePlaceholder+"}}") {
			t.Errorf("正文里不该残留未替换的占位符:%s", row)
		}
	}
}

// TestK8sManifestNamespaceInYAMLWins 验证「命名空间以 yaml 为准」:清单写了 prod 就不许被集群
// 登记的默认值覆盖 —— 那等于把服务发到另一个命名空间里,而两边都看不出问题。
func TestK8sManifestNamespaceInYAMLWins(t *testing.T) {
	e := newK8sEnv(t, "shop")
	runID, _ := seedSuccessRunWithArtifact(t, e.db, e.rsvc, run.ArtifactImage, img1)

	tr := e.deploy(t, runID, e.manifestCfg(deploymentManifest("prod", img1)))
	if tr.Status != run.TargetSuccess {
		t.Fatalf("status = %s(%s)", tr.Status, tr.Message)
	}
	if !e.fake.Exists("Deployment", "prod", "api") {
		t.Fatalf("清单写的 prod 必须生效,集群现状:%v", e.fake.Requests())
	}
	if e.fake.Exists("Deployment", "shop", "api") {
		t.Error("默认命名空间不该盖掉清单里写的那一个")
	}
}

// TestK8sManifestWithoutImagePlaceholderKeepsPinnedImage 清单没写 {{IMAGE}} 时,镜像以清单自己钉的
// 为准(不拿本次产物去覆盖);但必须在日志里说清「本次那件镜像没被引用」,否则这次成功是假的。
func TestK8sManifestWithoutImagePlaceholderKeepsPinnedImage(t *testing.T) {
	e := newK8sEnv(t, "shop")
	runID, _ := seedSuccessRunWithArtifact(t, e.db, e.rsvc, run.ArtifactImage, img2)

	lines := captureCtxLog(t, func(ctx context.Context) {
		tr := e.deployCtx(t, ctx, runID, e.manifestCfg(deploymentManifest("shop", img1)))
		if tr.Status != run.TargetSuccess {
			t.Fatalf("status = %s(%s)", tr.Status, tr.Message)
		}
	})
	if got := e.fake.ImageOf("Deployment", "shop", "api", "api"); got != img1 {
		t.Errorf("镜像该是清单钉的 %q,got %q", img1, got)
	}
	if !hasLogContaining(lines, "{{IMAGE}}") {
		t.Errorf("没引用本次镜像这件事必须说出口,日志:%v", lines)
	}
}

// TestK8sManifestRolloutFailureReappliesPreviousVersion 是本条腿存在的理由:滚不动时重新应用
// 「上一版实际发出去的声明」,而不是只把镜像字段抹回去 —— 上一版整份规格才回得去。
func TestK8sManifestRolloutFailureReappliesPreviousVersion(t *testing.T) {
	e := newK8sEnv(t, "shop")
	runA, _ := seedSuccessRunWithArtifact(t, e.db, e.rsvc, run.ArtifactImage, img1)
	cfgA := e.manifestCfg(deploymentManifest("shop", "{{IMAGE}}"))
	if tr := e.deploy(t, runA, cfgA); tr.Status != run.TargetSuccess {
		t.Fatalf("首次发布应成功:%s", tr.Message)
	}

	// 之后每次规格变更都不收敛:模拟「新 pod 起不来」这一类真实失败。
	e.fake.SetRolloutStall(-1)
	runB, _ := seedSuccessRunWithArtifact(t, e.db, e.rsvc, run.ArtifactImage, img2)
	cfgB := e.manifestCfg(deploymentManifest("shop", "{{IMAGE}}"))
	cfgB[CfgKeyRolloutTimeout] = "1" // 等 1 秒就判滚不动:测试不等满默认 5 分钟

	tr := e.deploy(t, runB, cfgB)
	if tr.Status != run.TargetRolledBack {
		t.Fatalf("status = %s(%s), want rolled_back", tr.Status, tr.Message)
	}
	if got := e.fake.ImageOf("Deployment", "shop", "api", "api"); got != img1 {
		t.Errorf("回滚后集群里该是上一版镜像 %q,got %q", img1, got)
	}
	// 入库的那份也跟着回到上一版:否则「这次发了什么」记的是集群里并不存在的那一份。
	for _, row := range storedManifests(t, e.db, runB) {
		if strings.Contains(row, "Deployment") && !strings.Contains(row, img1) {
			t.Errorf("回滚目标的 Deployment 正文该是重放的那一版:%s", row)
		}
	}
}

// TestK8sManifestFirstFailureHasNoPreviousVersion 滚不动、而库里又没有「上一版实际应用过的正文」
// 时必须停在 failed 并说清原因:悄悄回填一个不存在的版本,或假装成功,都比报错更坏。
//
// 集群里先有那份负载(所以这次 apply 真的触发了滚动),但**没有发布历史** —— 这正是「接管一个
// 早就在跑的服务」的第一次发布:负载滚不动,而我们无处可退。
func TestK8sManifestFirstFailureHasNoPreviousVersion(t *testing.T) {
	e := newK8sEnv(t, "shop", firstDoc(deploymentManifest("shop", img2)))
	e.fake.SetRolloutStall(-1)
	runID, _ := seedSuccessRunWithArtifact(t, e.db, e.rsvc, run.ArtifactImage, img1)
	cfg := e.manifestCfg(deploymentManifest("shop", "{{IMAGE}}"))
	cfg[CfgKeyRolloutTimeout] = "1"

	tr := e.deploy(t, runID, cfg)
	if tr.Status != run.TargetFailed {
		t.Fatalf("status = %s(%s), want failed", tr.Status, tr.Message)
	}
	if !strings.Contains(tr.Message, "第一次发布") || !strings.Contains(tr.Message, "没有上一版") {
		t.Errorf("message 要说清「无上一版可回滚」:%s", tr.Message)
	}
	// 关掉自动回滚的那一路同样不许假装回滚。
	e2 := newK8sEnv(t, "shop", firstDoc(deploymentManifest("shop", img2)))
	e2.fake.SetRolloutStall(-1)
	runID2, _ := seedSuccessRunWithArtifact(t, e2.db, e2.rsvc, run.ArtifactImage, img1)
	cfg2 := e2.manifestCfg(deploymentManifest("shop", "{{IMAGE}}"))
	cfg2[CfgKeyRolloutTimeout] = "1"
	cfg2[CfgKeyAutoRollback] = "false"
	if tr2 := e2.deploy(t, runID2, cfg2); tr2.Status != run.TargetFailed || !strings.Contains(tr2.Message, "未开启自动回滚") {
		t.Errorf("autoRollback=false 该停在 failed 并说明:%+v", tr2)
	}
}

// TestK8sManifestRejectsUnrenderedVariable 渲染后仍留着占位符 → 一份都不许发出去。
// 未渲染的 {{X}} 在 YAML 里会被解析成嵌套映射而不报错,所以这一刀必须落在发请求之前。
func TestK8sManifestRejectsUnrenderedVariable(t *testing.T) {
	e := newK8sEnv(t, "shop")
	runID, _ := seedSuccessRunWithArtifact(t, e.db, e.rsvc, run.ArtifactImage, img1)

	tr := e.deploy(t, runID, e.manifestCfg(deploymentManifest("shop", "registry.local:5000/shop:{{TAG}}")))
	if tr.Status != run.TargetFailed {
		t.Fatalf("status = %s(%s), want failed", tr.Status, tr.Message)
	}
	if !strings.Contains(tr.Message, "{{TAG}}") {
		t.Errorf("message 要点名是哪个变量没给全:%s", tr.Message)
	}
	if e.fake.Exists("Deployment", "shop", "api") {
		t.Error("清单没通过校验时不该有任何东西进集群")
	}
	if rows := storedManifests(t, e.db, runID); len(rows) != 0 {
		t.Errorf("没发出去的东西不留痕:%v", rows)
	}
}

// TestK8sManifestApplyPathMissing 集群里连路径都没有(命名空间还没建 / 集群版本没这类资源):
// 报的是「寻不到路径」而不是「部署失败」。
func TestK8sManifestApplyPathMissing(t *testing.T) {
	e := newK8sEnv(t, "shop")
	e.fake.SetMissing(true)
	runID, _ := seedSuccessRunWithArtifact(t, e.db, e.rsvc, run.ArtifactImage, img1)

	tr := e.deploy(t, runID, e.manifestCfg(deploymentManifest("shop", "{{IMAGE}}")))
	if tr.Status != run.TargetFailed {
		t.Fatalf("status = %s(%s), want failed", tr.Status, tr.Message)
	}
	if !strings.Contains(tr.Message, "命名空间") || !strings.Contains(tr.Message, "还没建") {
		t.Errorf("要把「路径寻不到」的两种成因讲出来:%s", tr.Message)
	}
}

// TestK8sManifestImageLegStillWorks 加这条腿不能动原来那条:manifestSource 留空 / none 时
// 依旧走「读旧镜像 → 换镜像 → 等滚动」(负载寻址来自 workloadName)。
func TestK8sManifestImageLegStillWorks(t *testing.T) {
	e := newK8sEnv(t, "shop", deploymentManifest("shop", img1))
	runID, _ := seedSuccessRunWithArtifact(t, e.db, e.rsvc, run.ArtifactImage, img2)
	cfg := map[string]string{
		CfgKeyClusterID:    e.clusterID,
		CfgKeyWorkloadName: "api",
		CfgKeyNamespace:    "shop",
	}
	if tr := e.deploy(t, runID, cfg); tr.Status != run.TargetSuccess {
		t.Fatalf("status = %s(%s)", tr.Status, tr.Message)
	}
	if got := e.fake.ImageOf("Deployment", "shop", "api", "api"); got != img2 {
		t.Errorf("换镜像那条腿该把镜像换成 %q,got %q", img2, got)
	}
	if rows := storedManifests(t, e.db, runID); len(rows) != 0 {
		t.Errorf("只换镜像的发布没有清单可入库:%v", rows)
	}
}

// TestK8sPlanRequiresManifestBody 清单来源已选定却没有正文(绕过保存直接改库):停在执行之前,
// 且说的不是「缺负载名」那种不相干的话。
func TestK8sPlanRequiresManifestBody(t *testing.T) {
	cfg := map[string]string{CfgKeyClusterID: "c1", CfgKeyManifestSource: ManifestSourcePaste, CfgKeyManifestYaml: "   "}
	if _, err := k8sPlanOf(cfg); !strings.Contains(err.Error(), "清单正文") {
		t.Fatalf("err = %v, want 缺清单正文", err)
	}
	// 清单那条腿不需要负载名(发的是清单里那几个对象)。
	ok := map[string]string{CfgKeyClusterID: "c1", CfgKeyManifestSource: ManifestSourcePaste, CfgKeyManifestYaml: "kind: Deployment"}
	if _, err := k8sPlanOf(ok); err != nil {
		t.Fatalf("带正文的清单节点不该被判缺项:%v", err)
	}
	if _, err := k8sPlanOf(map[string]string{CfgKeyClusterID: "c1", CfgKeyManifestSource: "url"}); err == nil {
		t.Fatal("清单来源非法要挡下")
	}
}
