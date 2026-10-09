package build

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuuhoo/pipewright/internal/pipeline"
	"github.com/fuuhoo/pipewright/internal/run"
)

// fakeReportSink 记录 SaveTestReport 调用(替代真 run.Service)。
type fakeReportSink struct {
	saved []run.TestReport
}

func (s *fakeReportSink) SaveTestReport(_ context.Context, tr run.TestReport) (*run.TestReport, error) {
	s.saved = append(s.saved, tr)
	out := tr
	return &out, nil
}

// reportStage 构造一个声明了测试报告 + 门禁的阶段。
func reportStage(cfg map[string]any) pipeline.Stage {
	base := map[string]any{"image": "node:20", "commands": "echo run"}
	for k, v := range cfg {
		base[k] = v
	}
	return pipeline.Stage{ID: "s1", Name: "测试", Jobs: []pipeline.Job{{
		ID: "j1", Name: "test", Type: pipeline.StepTypeScript, Config: base,
	}}}
}

const junitPass = `<testsuites tests="3" failures="0" errors="0" skipped="0" time="1.0">
  <testsuite name="a" tests="3" failures="0" errors="0" skipped="0"/>
</testsuites>`

const junitFail = `<testsuites tests="3" failures="1" errors="0" skipped="0" time="1.0">
  <testsuite name="a" tests="3" failures="1" errors="0" skipped="0"/>
</testsuites>`

func writeReport(t *testing.T, ws, rel, content string) {
	t.Helper()
	p := filepath.Join(ws, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCollectStageReport_NoDeclaration(t *testing.T) {
	ws := t.TempDir()
	rep := &fakeReporter{}
	sink := &fakeReportSink{}
	stage := pipeline.Stage{ID: "s1", Name: "x", Jobs: []pipeline.Job{
		{Type: pipeline.StepTypeScript, Config: map[string]any{"image": "x", "commands": "y"}},
	}}
	if err := collectStageReport(context.Background(), sink, &run.Run{ID: "r1"}, stage, ws, rep); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(sink.saved) != 0 {
		t.Errorf("saved %d reports, want 0 (no declaration)", len(sink.saved))
	}
}

func TestCollectStageReport_PassPersists(t *testing.T) {
	ws := t.TempDir()
	writeReport(t, ws, "reports/junit.xml", junitPass)
	rep := &fakeReporter{}
	sink := &fakeReportSink{}
	stage := reportStage(map[string]any{
		"testReport": "junit", "reportPath": "reports/junit.xml", "gateMaxFailures": "0",
	})
	if err := collectStageReport(context.Background(), sink, &run.Run{ID: "r1"}, stage, ws, rep); err != nil {
		t.Fatalf("err = %v, want nil (gate passes)", err)
	}
	if len(sink.saved) != 1 {
		t.Fatalf("saved %d reports, want 1", len(sink.saved))
	}
	got := sink.saved[0]
	if got.Total != 3 || got.Passed != 3 || got.Failed != 0 {
		t.Errorf("summary = %+v, want total=3 passed=3 failed=0", got)
	}
	if !got.GateEnabled || !got.GatePassed {
		t.Errorf("gate = enabled=%v passed=%v, want both true", got.GateEnabled, got.GatePassed)
	}
	if len(rep.jobDone) != 0 {
		t.Errorf("jobDone = %v, want none (门禁放行不得改判节点状态)", rep.jobDone)
	}
}

func TestCollectStageReport_GateBlocks(t *testing.T) {
	ws := t.TempDir()
	writeReport(t, ws, "reports/junit.xml", junitFail)
	rep := &fakeReporter{}
	sink := &fakeReportSink{}
	stage := reportStage(map[string]any{
		"testReport": "junit", "reportPath": "reports/junit.xml", "gateMaxFailures": "0",
	})
	err := collectStageReport(context.Background(), sink, &run.Run{ID: "r1"}, stage, ws, rep)
	if !errors.Is(err, ErrQualityGate) {
		t.Fatalf("err = %v, want ErrQualityGate", err)
	}
	// 仍应持久化汇总(记录门禁不过)。
	if len(sink.saved) != 1 || sink.saved[0].GatePassed {
		t.Errorf("saved = %+v, want 1 report with GatePassed=false", sink.saved)
	}
	// 门禁阻断必须把声明报告的 job step 改判失败:命令吞掉退出码时 step 停在 success,
	// DAG 上就成了「全绿但整体失败」,用户找不到失败点。
	assertStepFailed(t, rep, "j1")
}

// assertStepFailed 断言 fake reporter 收到过把该 job 标为失败的终态上报。
func assertStepFailed(t *testing.T, rep *fakeReporter, jobID string) {
	t.Helper()
	want := jobID + "=" + run.StepFailed
	for i := len(rep.jobDone) - 1; i >= 0; i-- {
		if rep.jobDone[i] == want {
			return
		}
	}
	t.Errorf("jobDone = %v, want last terminal %q", rep.jobDone, want)
}

func TestCollectStageReport_MissingFileWithGateBlocks(t *testing.T) {
	ws := t.TempDir() // 不写报告文件
	rep := &fakeReporter{}
	stage := reportStage(map[string]any{
		"testReport": "junit", "reportPath": "reports/junit.xml", "gateMaxFailures": "0",
	})
	err := collectStageReport(context.Background(), nil, &run.Run{ID: "r1"}, stage, ws, rep)
	if !errors.Is(err, ErrQualityGate) {
		t.Fatalf("err = %v, want ErrQualityGate (gate declared but no report)", err)
	}
	assertStepFailed(t, rep, "j1")
}

func TestCollectStageReport_MissingFileNoGateSoftSkip(t *testing.T) {
	ws := t.TempDir()
	rep := &fakeReporter{}
	sink := &fakeReportSink{}
	stage := reportStage(map[string]any{
		"testReport": "junit", "reportPath": "reports/junit.xml", // 无门禁
	})
	if err := collectStageReport(context.Background(), sink, &run.Run{ID: "r1"}, stage, ws, rep); err != nil {
		t.Fatalf("err = %v, want nil (display-only, soft skip)", err)
	}
	if len(sink.saved) != 0 {
		t.Errorf("saved %d, want 0", len(sink.saved))
	}
}

func TestCollectStageReport_Coverage(t *testing.T) {
	ws := t.TempDir()
	writeReport(t, ws, "junit.xml", junitPass)
	writeReport(t, ws, "cobertura.xml", `<coverage line-rate="0.92"/>`)
	rep := &fakeReporter{}
	sink := &fakeReportSink{}
	stage := reportStage(map[string]any{
		"testReport": "junit", "reportPath": "junit.xml",
		"coverageReport": "cobertura", "coveragePath": "cobertura.xml",
		"gateMinCoverage": "80",
	})
	if err := collectStageReport(context.Background(), sink, &run.Run{ID: "r1"}, stage, ws, rep); err != nil {
		t.Fatalf("err = %v, want nil (92%% >= 80%%)", err)
	}
	if len(sink.saved) != 1 || sink.saved[0].Coverage < 91 || sink.saved[0].Coverage > 93 {
		t.Errorf("coverage = %v, want ~92", sink.saved[0].Coverage)
	}
}

func TestSafeJoinWorkspace_NoTraversal(t *testing.T) {
	got := safeJoinWorkspace("/ws", "../../etc/passwd")
	if got != filepath.Join("/ws", "etc", "passwd") {
		t.Errorf("safeJoinWorkspace traversal not contained: %q", got)
	}
}
