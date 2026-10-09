package deploy

// stage_batch_test.go 钉住「首批后暂停」在流水线部署节点上的三条腿:
// 暂停只停在首批(其余一台都不该被碰过)、确认后续发的是**同一件产物**、中止留下的
// 是「未部署」而不是「成功」。这三件任何一件静默失效,分批就变成一个会让人误判已发完的装饰。

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/fuuhoo/pipewright/internal/run"
	"github.com/fuuhoo/pipewright/internal/target"
)

func stageCfg() map[string]string {
	return map[string]string{"deployPath": "/srv/app", "restartCommand": "systemctl restart shop"}
}

// statusByServer 把目标结果折成 serverID→status,便于按机器断言。
func statusByServer(res []TargetResult) map[string]string {
	m := make(map[string]string, len(res))
	for _, r := range res {
		m[r.ServerID] = r.Status
	}
	return m
}

func TestStageInteractiveFirstBatchPauses(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	rec := &touchRecorder{}
	tgt := &stubTarget{execFn: rec.exec}
	s1 := seedServer(t, tgt, "web-1")
	s2 := seedServer(t, tgt, "web-2")
	s3 := seedServer(t, tgt, "web-3")
	runID, _ := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactDist, "dist/shop.tar.gz")

	svc := New(tgt, rsvc)
	res, err := svc.DeployForStage(context.Background(), runID, []string{s1.ID, s2.ID, s3.ID}, stageCfg(), StrategyInteractive)
	if err != nil {
		t.Fatalf("DeployForStage: %v", err)
	}
	got := statusByServer(res)
	if got[s1.ID] != run.TargetSuccess {
		t.Fatalf("首批 = %s, want success", got[s1.ID])
	}
	for _, sid := range []string{s2.ID, s3.ID} {
		if got[sid] != run.TargetPending {
			t.Errorf("其余 %s = %s, want pending(暂停没生效就等于一次发完)", sid, got[sid])
		}
		if rec.touched[sid] {
			t.Errorf("暂停期间 %s 竟被执行了部署命令", sid)
		}
	}
	// 落库的 pending 行是运行详情页显示「还有几台没发」的唯一依据。
	targets, terr := rsvc.ListDeployTargets(context.Background(), runID)
	if terr != nil {
		t.Fatalf("ListDeployTargets: %v", terr)
	}
	var pending int
	for _, d := range targets {
		if d.Status == run.TargetPending {
			pending++
		}
	}
	if pending != 2 {
		t.Fatalf("库里 pending = %d, want 2(targets=%+v)", pending, targets)
	}
}

// TestStageResumeDeploysOnlyPending:确认之后其余才动,且已发过的首批不重来一遍。
func TestStageResumeDeploysOnlyPending(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	var mu sync.Mutex
	commands := map[string]int{} // 每台被下达的部署命令条数
	rec := &touchRecorder{}
	tgt := &stubTarget{execFn: func(sid string, cmd []string) (*target.ExecResult, error) {
		mu.Lock()
		commands[sid]++
		mu.Unlock()
		return rec.exec(sid, cmd)
	}}
	s1 := seedServer(t, tgt, "web-1")
	s2 := seedServer(t, tgt, "web-2")
	runID, _ := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactDist, "dist/shop.tar.gz")

	svc := New(tgt, rsvc)
	if _, err := svc.DeployForStage(context.Background(), runID, []string{s1.ID, s2.ID}, stageCfg(), StrategyInteractive); err != nil {
		t.Fatalf("DeployForStage: %v", err)
	}
	mu.Lock()
	firstBatchCmds := commands[s1.ID]
	mu.Unlock()
	if firstBatchCmds == 0 {
		t.Fatalf("前置断言:首批应先被部署过")
	}

	rest, err := svc.ResumeStageTargets(context.Background(), runID, []string{s2.ID}, stageCfg())
	if err != nil {
		t.Fatalf("ResumeStageTargets: %v", err)
	}
	if len(rest) != 1 || rest[0].Status != run.TargetSuccess {
		t.Fatalf("续发结果 = %+v, want 1 success", rest)
	}
	if !rec.touched[s2.ID] {
		t.Fatalf("续发没真的碰 %s", s2.ID)
	}
	mu.Lock()
	defer mu.Unlock()
	if commands[s1.ID] != firstBatchCmds {
		t.Errorf("首批被重发了一遍:暂停前 %d 条命令,续发后 %d 条", firstBatchCmds, commands[s1.ID])
	}
	if commands[s2.ID] == 0 {
		t.Errorf("待确认的那台一条命令都没收到")
	}
}

func TestStageResumeAndAbortRequireTargets(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	tgt := &stubTarget{execFn: (&touchRecorder{}).exec}
	svc := New(tgt, rsvc)
	ctx := context.Background()
	runID, _ := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactDist, "dist/shop.tar.gz")

	if _, err := svc.ResumeStageTargets(ctx, runID, nil, stageCfg()); !errors.Is(err, ErrNoPendingTargets) {
		t.Errorf("ResumeStageTargets(空) = %v, want ErrNoPendingTargets", err)
	}
	if _, err := svc.AbortStageTargets(ctx, runID, nil); !errors.Is(err, ErrNoPendingTargets) {
		t.Errorf("AbortStageTargets(空) = %v, want ErrNoPendingTargets", err)
	}
	// 主机已从台账删掉:中止不该失败(没有进程要停),但要留下一行说明。
	ghost := "00000000-0000-4000-8000-000000000000"
	res, err := svc.AbortStageTargets(ctx, runID, []string{ghost})
	if err != nil {
		t.Fatalf("中止一台已删除的主机不该失败: %v", err)
	}
	if len(res) != 1 || res[0].Status != run.TargetFailed {
		t.Fatalf("结果 = %+v, want 1 failed", res)
	}
}

// TestStageAbortMarksNotDeployed:拒绝 = 其余保留旧版本,并留下人读原因。
func TestStageAbortMarksNotDeployed(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	rec := &touchRecorder{}
	tgt := &stubTarget{execFn: rec.exec}
	s1 := seedServer(t, tgt, "web-1")
	s2 := seedServer(t, tgt, "web-2")
	runID, _ := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactDist, "dist/shop.tar.gz")

	svc := New(tgt, rsvc)
	if _, err := svc.DeployForStage(context.Background(), runID, []string{s1.ID, s2.ID}, stageCfg(), StrategyInteractive); err != nil {
		t.Fatalf("DeployForStage: %v", err)
	}
	res, err := svc.AbortStageTargets(context.Background(), runID, []string{s2.ID})
	if err != nil {
		t.Fatalf("AbortStageTargets: %v", err)
	}
	if len(res) != 1 || res[0].Status != run.TargetFailed {
		t.Fatalf("中止结果 = %+v, want 1 failed", res)
	}
	if !strings.Contains(res[0].Message, "未部署") {
		t.Errorf("中止原因 = %q, want 说明本机未部署", res[0].Message)
	}
	if rec.touched[s2.ID] {
		t.Errorf("中止后 %s 仍被执行了部署命令", s2.ID)
	}
	// 已发的首批不该被抹掉:它是这一版唯一真实在线的机器。
	targets, terr := rsvc.ListDeployTargets(context.Background(), runID)
	if terr != nil {
		t.Fatalf("ListDeployTargets: %v", terr)
	}
	found := false
	for _, d := range targets {
		if d.ServerID == s1.ID {
			found = true
			if d.Status != run.TargetSuccess {
				t.Errorf("首批状态被改写了:= %s, want success", d.Status)
			}
		}
	}
	if !found {
		t.Errorf("中止把首批的行也写丢了:targets=%+v", targets)
	}
}
