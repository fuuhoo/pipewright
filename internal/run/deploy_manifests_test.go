package run

import (
	"context"
	"testing"
	"time"
)

// deploy_manifests_test.go 盯的是回滚的数据来源:「上一版实际发了什么」必须只有一个答案,
// 而且答案只能来自成功的发布 —— 拿失败那版的正文去回滚会越滚越坏。

// manifest 造一份清单行(测试里正文只关心可辨识的 tag)。
func manifest(name, body string, ordinal int) ManifestDoc {
	return ManifestDoc{Kind: "Deployment", Namespace: "shop", Name: name, Ordinal: ordinal, Body: body}
}

// target 造一个 k8s 目标结果(ServerID 即 clusterID;offset 决定「谁更新」)。
func target(cluster string, offset time.Duration, status string, docs ...ManifestDoc) DeployTarget {
	stamp := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC).Add(offset)
	return DeployTarget{ServerID: cluster, ServerName: cluster, Status: status, StartedAt: stamp, Manifests: docs}
}

// TestLastAppliedManifestUsesPreviousSuccess 验证回滚读到的是「上一版成功发布」的正文,
// 且首次发布(无历史)与当前批次自身都被正确区分。
func TestLastAppliedManifestUsesPreviousSuccess(t *testing.T) {
	db := testDB(t)
	svc := New(db)
	projID := seedProject(t, db)
	ctx := context.Background()

	r1 := seedRunRow(t, svc, projID, StatusSuccess)
	if err := svc.SaveDeployTargets(ctx, r1, []DeployTarget{
		target("c1", 0, TargetSuccess, manifest("api", "image: api:v1", 1)),
	}); err != nil {
		t.Fatalf("save v1: %v", err)
	}

	// 当前 run 还没有历史可回滚:排除自身后无行 → (nil, nil)。
	if got, err := svc.LastAppliedManifest(ctx, "c1", "Deployment", "shop", "api", r1); err != nil || got != nil {
		t.Fatalf("首次发布应无上一版,got=%+v err=%v", got, err)
	}

	r2 := seedRunRow(t, svc, projID, StatusSuccess)
	if err := svc.SaveDeployTargets(ctx, r2, []DeployTarget{
		target("c2", time.Minute, TargetSuccess, manifest("api", "image: api:v2", 1)),
	}); err != nil {
		t.Fatalf("save v2: %v", err)
	}

	// 换集群查同一个对象:不该串台读到 c1 的 v1,也不该读到 c2 的上一版。
	if got, err := svc.LastAppliedManifest(ctx, "c1", "Deployment", "shop", "api", r2); err != nil {
		t.Fatalf("load: %v", err)
	} else if got == nil || got.Body != "image: api:v1" {
		t.Fatalf("c1 的上一版 = %+v, want image: api:v1", got)
	}
	if got, err := svc.LastAppliedManifest(ctx, "c2", "Deployment", "shop", "api", r2); err != nil {
		t.Fatalf("load: %v", err)
	} else if got != nil {
		t.Fatalf("c2 首次发布不该有上一版,got=%+v", got)
	}
}

// TestLastAppliedManifestSkipsFailedRelease 验证失败发布不参与「上一版」:
// 中间那次发坏了,回滚要退到它之前那版成功的,而不是把坏配置再发一遍。
func TestLastAppliedManifestSkipsFailedRelease(t *testing.T) {
	db := testDB(t)
	svc := New(db)
	projID := seedProject(t, db)
	ctx := context.Background()

	seed := func(offset time.Duration, status, body string) string {
		t.Helper()
		id := seedRunRow(t, svc, projID, StatusSuccess)
		if err := svc.SaveDeployTargets(ctx, id, []DeployTarget{
			target("c1", offset, status, manifest("api", body, 1)),
		}); err != nil {
			t.Fatalf("save %s: %v", body, err)
		}
		return id
	}
	seed(0, TargetSuccess, "image: api:v1")
	seed(time.Minute, TargetFailed, "image: api:broken")

	got, err := svc.LastAppliedManifest(ctx, "c1", "Deployment", "shop", "api", "r-now")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got == nil || got.Body != "image: api:v1" {
		t.Fatalf("上一版 = %+v, want 跳过失败的 v1", got)
	}
}

// TestLastAppliedManifestRequiresNamespace 验证命名空间参与匹配:
// 同名对象在两个 namespace 里是两个东西,回滚不能跨命名空间取正文。
func TestLastAppliedManifestRequiresNamespace(t *testing.T) {
	db := testDB(t)
	svc := New(db)
	projID := seedProject(t, db)
	ctx := context.Background()

	id := seedRunRow(t, svc, projID, StatusSuccess)
	if err := svc.SaveDeployTargets(ctx, id, []DeployTarget{
		target("c1", 0, TargetSuccess, ManifestDoc{Kind: "Deployment", Namespace: "shop", Name: "api", Body: "ns: shop"}),
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := svc.LastAppliedManifest(ctx, "c1", "Deployment", "prod", "api", "other")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got != nil {
		t.Fatalf("prod 命名空间无历史,却读到 %+v", got)
	}
}

// TestSaveDeployTargetsReplacesManifests 验证整批重写时清单一起替换:
// 这次只发一台,上次那台的正文不能留下来冒充这一版的记录。
func TestSaveDeployTargetsReplacesManifests(t *testing.T) {
	db := testDB(t)
	svc := New(db)
	projID := seedProject(t, db)
	ctx := context.Background()
	runID := seedRunRow(t, svc, projID, StatusSuccess)

	if err := svc.SaveDeployTargets(ctx, runID, []DeployTarget{
		target("c1", 0, TargetSuccess, manifest("api", "image: api:v1", 1)),
		target("c2", 0, TargetSuccess, manifest("api", "image: api:v1", 1)),
	}); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := svc.SaveDeployTargets(ctx, runID, []DeployTarget{
		target("c1", time.Minute, TargetSuccess, manifest("api", "image: api:v2", 1)),
	}); err != nil {
		t.Fatalf("second save: %v", err)
	}

	if got, err := svc.LastAppliedManifest(ctx, "c2", "Deployment", "shop", "api", "other"); err != nil {
		t.Fatalf("load: %v", err)
	} else if got != nil {
		t.Fatalf("整批重写应连被去掉的目标一起清,却读到 %+v", got)
	}
	if got, err := svc.LastAppliedManifest(ctx, "c1", "Deployment", "shop", "api", "other"); err != nil {
		t.Fatalf("load: %v", err)
	} else if got == nil || got.Body != "image: api:v2" {
		t.Fatalf("c1 最新正文 = %+v, want v2", got)
	}
}

// TestUpsertDeployTargetsKeepsOtherTargets 验证逐目标 upsert 不碰未列出目标的正文:
// 只重试失败机时,成功机的历史正文必须原样还在。
func TestUpsertDeployTargetsKeepsOtherTargets(t *testing.T) {
	db := testDB(t)
	svc := New(db)
	projID := seedProject(t, db)
	ctx := context.Background()
	runID := seedRunRow(t, svc, projID, StatusSuccess)

	if err := svc.SaveDeployTargets(ctx, runID, []DeployTarget{
		target("c1", 0, TargetSuccess, manifest("api", "image: api:ok", 1)),
		target("c2", 0, TargetFailed, manifest("api", "image: api:bad", 1)),
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := svc.UpsertDeployTargets(ctx, runID, []DeployTarget{
		target("c2", time.Minute, TargetSuccess, manifest("api", "image: api:fixed", 1)),
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// c1 未参与重试 → 正文一字不动。
	if got, err := svc.LastAppliedManifest(ctx, "c1", "Deployment", "shop", "api", "other"); err != nil {
		t.Fatalf("load c1: %v", err)
	} else if got == nil || got.Body != "image: api:ok" {
		t.Fatalf("c1 正文被改动: %+v", got)
	}
	// c2 重试后自身成为成功版。
	if got, err := svc.LastAppliedManifest(ctx, "c2", "Deployment", "shop", "api", "other"); err != nil {
		t.Fatalf("load c2: %v", err)
	} else if got == nil || got.Body != "image: api:fixed" {
		t.Fatalf("c2 正文 = %+v, want fixed", got)
	}
}

// TestRolledBackVersionStopsBeingPrevious 验证「撤销」会退出历史:某版被回滚掉后,
// 下一次回滚不能再把它当上一版(否则回滚会在两版之间来回弹),而正文行本身不删 ——
// 可见性只由 deploy_targets.status 决定,历史仍然完整可查。
func TestRolledBackVersionStopsBeingPrevious(t *testing.T) {
	db := testDB(t)
	svc := New(db)
	projID := seedProject(t, db)
	ctx := context.Background()

	r1 := seedRunRow(t, svc, projID, StatusSuccess)
	if err := svc.SaveDeployTargets(ctx, r1, []DeployTarget{
		target("c1", 0, TargetSuccess, manifest("api", "image: api:v1", 1)),
	}); err != nil {
		t.Fatalf("save v1: %v", err)
	}
	r2 := seedRunRow(t, svc, projID, StatusSuccess)
	if err := svc.SaveDeployTargets(ctx, r2, []DeployTarget{
		target("c1", time.Minute, TargetSuccess, manifest("api", "image: api:v2", 1)),
	}); err != nil {
		t.Fatalf("save v2: %v", err)
	}
	prev := func() string {
		t.Helper()
		got, err := svc.LastAppliedManifest(ctx, "c1", "Deployment", "shop", "api", "r-future")
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if got == nil {
			return "<nil>"
		}
		return got.Body
	}
	if got := prev(); got != "image: api:v2" {
		t.Fatalf("上一版 = %q, want v2", got)
	}

	// 回滚 r2(不带清单的 upsert:只翻结果状态,正文一个字都不动)。
	if err := svc.UpsertDeployTargets(ctx, r2, []DeployTarget{
		target("c1", 2*time.Minute, TargetRolledBack),
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if got := prev(); got != "image: api:v1" {
		t.Fatalf("v2 已回滚,上一版应退到 v1,got %q", got)
	}
	var rows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM deploy_manifests WHERE run_id = ?`, r2).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Fatalf("无清单的 upsert 不该删正文,rows = %d, want 1", rows)
	}
}

// TestSaveDeployTargetsStoresEveryDoc 验证一份多文档清单逐份入库(含在文件里的序号):
// 一次发布可能同时发 Service + Deployment + Ingress,回滚要能按对象各自取回自己那版正文。
func TestSaveDeployTargetsStoresEveryDoc(t *testing.T) {
	db := testDB(t)
	svc := New(db)
	projID := seedProject(t, db)
	ctx := context.Background()
	runID := seedRunRow(t, svc, projID, StatusSuccess)

	docs := []ManifestDoc{
		{Kind: "Service", Namespace: "shop", Name: "api", Ordinal: 1, Body: "kind: Service"},
		{Kind: "Deployment", Namespace: "shop", Name: "api", Ordinal: 2, Body: "kind: Deployment"},
		{Kind: "Ingress", Namespace: "shop", Name: "api-gw", Ordinal: 3, Body: "kind: Ingress"},
	}
	if err := svc.SaveDeployTargets(ctx, runID, []DeployTarget{
		target("c1", 0, TargetSuccess, docs...),
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	// 这一版属于当前 run:排除当前 run 后必须什么都读不到(否则重试会把本批当成"上一版")。
	for _, d := range docs {
		got, err := svc.LastAppliedManifest(ctx, "c1", d.Kind, d.Namespace, d.Name, runID)
		if err != nil {
			t.Fatalf("load %s: %v", d.Kind, err)
		}
		if got != nil {
			t.Fatalf("排除当前 run 后不该读到本批 %s: %+v", d.Kind, got)
		}
	}

	// 换一次发布(不同 run)再读:每个对象各自回到自己那版正文与序号。
	next := seedRunRow(t, svc, projID, StatusRunning)
	if err := svc.UpsertDeployTargets(ctx, next, []DeployTarget{
		target("c1", time.Minute, TargetSuccess, docs...),
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	for _, d := range docs {
		got, err := svc.LastAppliedManifest(ctx, "c1", d.Kind, d.Namespace, d.Name, "r-other")
		if err != nil {
			t.Fatalf("load %s: %v", d.Kind, err)
		}
		if got == nil || got.Body != d.Body || got.Ordinal != d.Ordinal {
			t.Fatalf("%s 读回 %+v, want body=%q ordinal=%d", d.Kind, got, d.Body, d.Ordinal)
		}
	}
}
