package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/huangchengsir/pipewright/internal/run"
)

// artifact_source_test.go 覆盖「并行构建 → 部署节点按来源任务取产物」:
//   - 同一次 run 里有两件同类型产物(前端 + 后端镜像)时,cfg["artifactFrom"] 决定发哪一件;
//   - 没配来源时行为与从前完全一致(按类型取第一件)—— 回归保护;
//   - 来源任务没产出可部署产物 → 明确报 ErrArtifactSourceNotFound(而不是退而发另一件)。
// 全部用 stubTarget 捕获命令断言,不触真实 SSH / docker。

// addImageFrom 追加一件带「来源任务」metadata 的镜像产物。
func addImageFrom(t *testing.T, rsvc run.Service, runID, ref, jobID, jobName string) {
	t.Helper()
	meta := map[string]any{"sourceStage": "构建", "sourceJob": jobName}
	if jobID != "" {
		meta["sourceJobId"] = jobID
	}
	if _, err := rsvc.AddArtifact(context.Background(), run.Artifact{
		RunID: runID, Type: run.ArtifactImage, Name: ref, Reference: ref, Metadata: meta,
	}); err != nil {
		t.Fatalf("AddArtifact(%s): %v", ref, err)
	}
}

// TestStagePicksArtifactBySourceJob 两件镜像产物 + artifactFrom=后端任务 ID → 只 pull 后端镜像。
// 库里的顺序故意是「web 在前」:不配来源时才会取到它,配了来源必须翻不过来。
func TestStagePicksArtifactBySourceJob(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	tgt := &stubTarget{}
	srv := seedServer(t, tgt, "app-1")
	runID, _ := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactImage, "reg.local/web:1.0")
	if _, err := rsvc.AddArtifact(context.Background(), run.Artifact{
		RunID: runID, Type: run.ArtifactImage, Name: "api", Reference: "reg.local/api:1.0",
		Metadata: map[string]any{"sourceStage": "构建", "sourceJobId": "japi", "sourceJob": "后端构建"},
	}); err != nil {
		t.Fatalf("AddArtifact: %v", err)
	}

	svc := New(tgt, rsvc)
	res, err := svc.DeployForStage(context.Background(), runID, []string{srv.ID},
		map[string]string{"artifactType": "image", CfgKeyArtifactFrom: "japi"}, "")
	if err != nil {
		t.Fatalf("DeployForStage: %v", err)
	}
	if len(res) != 1 || res[0].Status != run.TargetSuccess {
		t.Fatalf("want 1 success, got %+v", res)
	}
	if !hasCmd(tgt.calls, "docker", "pull", "reg.local/api:1.0") {
		t.Fatalf("应部署来源任务的镜像 api,got %v", tgt.calls)
	}
	if hasCmd(tgt.calls, "docker", "pull", "reg.local/web:1.0") {
		t.Fatalf("不该部署另一条并行分支的镜像 web,got %v", tgt.calls)
	}
}

// TestStageArtifactSourceLegacyNameMatch 历史产物只有 sourceJob(名字)时,按名字也能点中。
func TestStageArtifactSourceLegacyNameMatch(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	tgt := &stubTarget{}
	srv := seedServer(t, tgt, "app-1")
	runID, _ := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactImage, "reg.local/api:1.0")
	addImageFrom(t, rsvc, runID, "reg.local/web:1.0", "", "前端构建")

	svc := New(tgt, rsvc)
	if _, err := svc.DeployForStage(context.Background(), runID, []string{srv.ID},
		map[string]string{"artifactType": "image", CfgKeyArtifactFrom: "前端构建"}, ""); err != nil {
		t.Fatalf("DeployForStage: %v", err)
	}
	if !hasCmd(tgt.calls, "docker", "pull", "reg.local/web:1.0") {
		t.Fatalf("按任务名也应点中 web 镜像,got %v", tgt.calls)
	}
}

// TestStageArtifactSourceMissingFailsLoudly 来源任务没产产物 → 报错,绝不退回发另一件。
func TestStageArtifactSourceMissingFailsLoudly(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	tgt := &stubTarget{}
	srv := seedServer(t, tgt, "app-1")
	runID, _ := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactImage, "reg.local/api:1.0")
	addImageFrom(t, rsvc, runID, "reg.local/web:1.0", "jweb", "前端构建")

	svc := New(tgt, rsvc)
	_, err := svc.DeployForStage(context.Background(), runID, []string{srv.ID},
		map[string]string{"artifactType": "image", CfgKeyArtifactFrom: "jdeleted"}, "")
	if !errors.Is(err, ErrArtifactSourceNotFound) {
		t.Fatalf("err = %v, want ErrArtifactSourceNotFound", err)
	}
	if !strings.Contains(err.Error(), "jdeleted") {
		t.Errorf("报错要点名是哪个来源失配,got %q", err.Error())
	}
	if len(tgt.calls) != 0 {
		t.Fatalf("来源失配不该向目标机发任何命令,got %v", tgt.calls)
	}
}

// TestStageWithoutSourceKeepsLegacyPick 不配来源 → 一律不做来源筛选(旧行为,回归保护):
// 上面几个用例已覆盖「取第一件」,这里只钉住「空值绝不报错」—— 存量流水线全走这条。
func TestStageWithoutSourceKeepsLegacyPick(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	tgt := &stubTarget{}
	srv := seedServer(t, tgt, "app-1")
	runID, _ := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactImage, "reg.local/api:1.0")
	addImageFrom(t, rsvc, runID, "reg.local/web:1.0", "jweb", "前端构建")

	svc := New(tgt, rsvc)
	res, err := svc.DeployForStage(context.Background(), runID, []string{srv.ID},
		map[string]string{"artifactType": "image"}, "")
	if err != nil {
		t.Fatalf("DeployForStage: %v", err)
	}
	if len(res) != 1 || res[0].Status != run.TargetSuccess {
		t.Fatalf("want 1 success, got %+v", res)
	}
	if !hasCmd(tgt.calls, "docker", "pull") {
		t.Fatalf("应照常部署一件镜像,got %v", tgt.calls)
	}
}

// TestArtifactsFromSourceIgnoresLegacyMetadata 纯函数口径:无 metadata(历史产物)不匹配,
// ID 优先于任务名,大小写与空白由调用方 trim。
func TestArtifactsFromSourceIgnoresLegacyMetadata(t *testing.T) {
	arts := []run.Artifact{
		{Type: run.ArtifactJar, Name: "no-meta"},
		{Type: run.ArtifactJar, Name: "by-name", Metadata: map[string]any{"sourceJob": "后端构建"}},
		{Type: run.ArtifactJar, Name: "by-id", Metadata: map[string]any{"sourceJobId": "japi", "sourceJob": "后端构建"}},
	}
	if got := artifactsFromSource(arts, "japi"); len(got) != 1 || got[0].Name != "by-id" {
		t.Fatalf("按 ID 应只命中 by-id(名字相同的 by-name 不该被 ID 匹配带上),got %+v", got)
	}
	// 名字这条兜底路:两个同名任务时按入库顺序都命中(名字本就不是唯一标识,故只作兼容)。
	if got := artifactsFromSource(arts, "后端构建"); len(got) != 2 {
		t.Fatalf("按名字兜底应命中两件,got %+v", got)
	}
	if len(artifactsFromSource(arts, "nope")) != 0 {
		t.Error("不存在的来源应为空")
	}
}
