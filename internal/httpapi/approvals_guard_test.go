package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/approval"
	"github.com/fuuhoo/pipewright/internal/audit"
	"github.com/fuuhoo/pipewright/internal/mask"
	"github.com/fuuhoo/pipewright/internal/run"
)

// approvals_guard_test.go —— 审批 / 分批确认端点的两道关:
//  1. 归属:私有组运行的审批门,局外人连列表都看不到,点批准也点不动(门还在等)。
//  2. 留痕:决定里的 actor 与审计行都必须是**真实点按钮的人**,而不是写死的 admin ——
//     「谁放行了这一半部署」正是这套留痕唯一要回答的问题。

// deployGateKey 造一条「首批后暂停」的门键,与 build 层走同一个构造函数。
func deployGateKey(runID, jobID string) (string, string) {
	gateID := approval.DeployGateID(jobID)
	return gateID, approval.Key(runID, gateID)
}

func approvalURL(env *guardEnv, runID, action string) string {
	return env.srv.URL + "/api/runs/" + runID + "/" + action
}

func TestListApprovals_RequiresView(t *testing.T) {
	env := setupGuard(t)
	url := approvalURL(env, env.runInPriv, "approvals")

	if got := statusOf(t, env.other, http.MethodGet, url, "", ""); got != http.StatusForbidden {
		t.Fatalf("局外人读私有组运行的审批记录 = %d, want 403", got)
	}
	if got := statusOf(t, env.member, http.MethodGet, url, "", ""); got != http.StatusOK {
		t.Fatalf("成员读私有组运行的审批记录 = %d, want 200", got)
	}
}

func TestApprovalDecision_GuardAndActor(t *testing.T) {
	env := setupGuard(t)
	ctx := context.Background()
	gateID, key := deployGateKey(env.runInPriv, "job-deploy")

	if err := env.astore.CreatePending(ctx, env.runInPriv, gateID, "发布到主机"); err != nil {
		t.Fatalf("create pending: %v", err)
	}
	ch := env.coord.Wait(key)

	approve := `{"stageId":"` + gateID + `"}`

	// 局外人:403,而且门必须仍在等 —— 拒绝不能顺手把等待者摘掉。
	if got := statusOf(t, env.other, http.MethodPost, approvalURL(env, env.runInPriv, "approve"), env.otherCS, approve); got != http.StatusForbidden {
		t.Fatalf("局外人批准 = %d, want 403", got)
	}
	if !env.coord.IsWaiting(key) {
		t.Fatal("局外人被 403 之后门不该被解析掉")
	}
	select {
	case d := <-ch:
		t.Fatalf("局外人的决定不该投递: %+v", d)
	default:
	}

	// 成员:200,决定带着自己的身份进来。
	resp := doJSON(t, env.member, http.MethodPost, approvalURL(env, env.runInPriv, "approve"), env.memberCS, approve)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("成员批准 = %d, want 200", resp.StatusCode)
	}
	wantActor := "user:" + env.memberUID

	var delivered approval.Decision
	select {
	case delivered = <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("审批决定未投递给等待方")
	}
	if !delivered.Approved {
		t.Fatalf("投递的决定 = %+v, want approved", delivered)
	}
	if delivered.Actor != wantActor {
		t.Fatalf("投递 actor = %q, want %q", delivered.Actor, wantActor)
	}

	// 审计行也不能停留在 "admin"。
	rec := audit.New(env.db, mask.NewMasker(), nil)
	res, err := rec.List(ctx, audit.ListFilter{Action: "run.approve", TargetType: "run"})
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	var hits int
	for _, e := range res.Entries {
		if e.TargetID != env.runInPriv {
			continue
		}
		hits++
		if e.Actor != wantActor {
			t.Fatalf("审计 actor = %q, want %q", e.Actor, wantActor)
		}
		if detail, _ := e.Detail["stageId"].(string); detail != gateID {
			t.Fatalf("审计 detail.stageId = %v, want %q", e.Detail["stageId"], gateID)
		}
	}
	if hits != 1 {
		t.Fatalf("本次批准的审计应有 1 条, got %d(%+v)", hits, res.Entries)
	}
}

func TestApprovalDecision_RejectAndUnknownGate(t *testing.T) {
	env := setupGuard(t)
	gateID, key := deployGateKey(env.runInPriv, "job-deploy")

	// 没有等待者(未到门 / 已决 / 进程重启)→ 409,而不是假装成功。
	if got := statusOf(t, env.member, http.MethodPost, approvalURL(env, env.runInPriv, "reject"), env.memberCS,
		`{"stageId":"`+gateID+`"}`); got != http.StatusConflict {
		t.Fatalf("拒绝无人等待的门 = %d, want 409", got)
	}

	ch := env.coord.Wait(key)
	resp := doJSON(t, env.member, http.MethodPost, approvalURL(env, env.runInPriv, "reject"), env.memberCS,
		`{"stageId":"`+gateID+`"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("成员拒绝 = %d, want 200", resp.StatusCode)
	}
	var delivered approval.Decision
	select {
	case delivered = <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("拒绝未投递给等待方")
	}
	if delivered.Approved {
		t.Fatalf("投递的决定 = %+v, want rejected", delivered)
	}
	if delivered.Actor != "user:"+env.memberUID {
		t.Fatalf("拒绝 actor = %q, want user:%s", delivered.Actor, env.memberUID)
	}
	// 注意:端点只负责投递 + 审计,`run_approvals` 的 decidedBy 由等待体(awaitDecision)回写
	// —— 见 TestDeployGateEndToEnd。
}

// TestDeployGateEndToEnd 走真实的等待体:门把运行置为 waiting_approval,决定后放回 running,
// 并把 decidedBy 落库 —— 这正是 build 层「首批后暂停」依赖的三件事。
func TestDeployGateEndToEnd(t *testing.T) {
	env := setupGuard(t)
	ctx := context.Background()

	// 门作用于 running 中的运行,夹具里的种子运行是终态,这里改一下状态。
	if _, err := env.db.Exec(`UPDATE pipeline_runs SET status=? WHERE id=?`, run.StatusRunning, env.runInPriv); err != nil {
		t.Fatalf("seed running: %v", err)
	}
	runs := run.New(env.db)

	gate := NewDeployGate(runs, env.coord, env.astore, nil)
	jobID, jobName := "job-deploy", "发布到主机"
	gateID := approval.DeployGateID(jobID)

	done := make(chan gateOutcome, 1)
	go func() {
		ok, err := gate(ctx, &run.Run{ID: env.runInPriv, ProjectID: env.inPrivate}, jobID, jobName)
		done <- gateOutcome{approved: ok, err: err}
	}()

	waitGuardRunStatus(t, env, "waiting_approval")
	if !env.coord.IsWaiting(approval.Key(env.runInPriv, gateID)) {
		t.Fatal("门未登记等待者")
	}
	// 等待期间列表里能看到这条 pending(前端按它渲染「首批确认」卡片)。
	recs, err := env.astore.ListForRun(ctx, env.runInPriv)
	if err != nil {
		t.Fatalf("list approvals: %v", err)
	}
	if len(recs) != 1 || recs[0].Status != approval.StatusPending || recs[0].StageName != jobName {
		t.Fatalf("等待中的审批记录 = %+v, want 1 条 pending/%s", recs, jobName)
	}

	resp := doJSON(t, env.member, http.MethodPost, approvalURL(env, env.runInPriv, "approve"), env.memberCS,
		`{"stageId":"`+gateID+`"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("批准 = %d, want 200", resp.StatusCode)
	}

	select {
	case o := <-done:
		if o.err != nil {
			t.Fatalf("门返回错误: %v", o.err)
		}
		if !o.approved {
			t.Fatal("门应放行")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("门未放行")
	}
	// 放行后放回 running,由 worker 收尾落终态;记录里带上点按钮的人。
	waitGuardRunStatus(t, env, string(run.StatusRunning))
	recs, err = env.astore.ListForRun(ctx, env.runInPriv)
	if err != nil {
		t.Fatalf("list approvals: %v", err)
	}
	if len(recs) != 1 || recs[0].Status != approval.StatusApproved || recs[0].DecidedBy != "user:"+env.memberUID {
		t.Fatalf("放行后的审批记录 = %+v, want approved / user:%s", recs, env.memberUID)
	}
}

type gateOutcome struct {
	approved bool
	err      error
}

// waitGuardRunStatus 等运行状态落到 want —— 门在 goroutine 里翻状态,轮询比 sleep 靠谱。
func waitGuardRunStatus(t *testing.T, env *guardEnv, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		if err := env.db.QueryRow(`SELECT status FROM pipeline_runs WHERE id=?`, env.runInPriv).Scan(&got); err != nil {
			t.Fatalf("read run status: %v", err)
		}
		if got == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("运行状态停在 %q, want %q", got, want)
}
