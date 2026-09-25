package deploy

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/huangchengsir/pipewright/internal/run"
	"github.com/huangchengsir/pipewright/internal/target"
)

// ---- 纯函数单元 ------------------------------------------------------------

func TestNormalizeStrategy(t *testing.T) {
	cases := map[string]string{
		"":            StrategyRolling,
		"rolling":     StrategyRolling,
		"unknown":     StrategyRolling,
		"canary":      StrategyCanary,
		"CANARY":      StrategyCanary,
		"blue_green":  StrategyBlueGreen,
		"blue-green":  StrategyBlueGreen,
		"BlueGreen":   StrategyBlueGreen,
		" blue green": StrategyBlueGreen,
	}
	for in, want := range cases {
		if got := NormalizeStrategy(in); got != want {
			t.Errorf("NormalizeStrategy(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCanaryCount(t *testing.T) {
	cases := []struct {
		cfg   map[string]string
		total int
		want  int
	}{
		{nil, 1, 1}, // 单机:整体即金丝雀
		{nil, 3, 1}, // 默认 1 台
		{map[string]string{"canaryCount": "2"}, 5, 2},
		{map[string]string{"canaryCount": "9"}, 3, 2}, // 夹紧:至少留 1 台在其余
		{map[string]string{"canaryPercent": "50"}, 4, 2},
		{map[string]string{"canaryPercent": "10"}, 5, 1}, // ceil(0.5)=1
		{map[string]string{"canaryCount": "0"}, 3, 1},    // 非法 → 默认 1
	}
	for _, c := range cases {
		if got := canaryCount(c.cfg, c.total); got != c.want {
			t.Errorf("canaryCount(%v, %d) = %d, want %d", c.cfg, c.total, got, c.want)
		}
	}
}

// ---- 测试用可控 stub:按 serverID 注入失败,记录每机被执行的命令 -----------------

// recordingTarget 包装 stubTarget 语义:execFn 据 (serverID, cmd) 决定结果,并记录触达的 serverID。
type touchRecorder struct {
	mu           sync.Mutex
	touched      map[string]bool // 被 Exec 过的 serverID
	calls2       [][]string      // 全部命令(image 蓝绿断言 pull/run 用)
	failOn       func(serverID string, cmd []string) bool
	inspectImage string // 非空 → docker inspect 返回该镜像(模拟已有上一镜像)
}

func (r *touchRecorder) exec(serverID string, cmd []string) (*target.ExecResult, error) {
	r.mu.Lock()
	if r.touched == nil {
		r.touched = map[string]bool{}
	}
	r.touched[serverID] = true
	r.calls2 = append(r.calls2, cmd)
	r.mu.Unlock()
	// 模拟 docker inspect → 上一镜像(供 image 机群回滚有 prevImage 可回)。
	if len(cmd) >= 2 && cmd[0] == "docker" && cmd[1] == "inspect" {
		if r.inspectImage != "" {
			return &target.ExecResult{ExitCode: 0, Stdout: r.inspectImage}, nil
		}
		return &target.ExecResult{ExitCode: 0}, nil
	}
	if r.failOn != nil && r.failOn(serverID, cmd) {
		return &target.ExecResult{ExitCode: 1, Stderr: "injected failure"}, nil
	}
	return &target.ExecResult{ExitCode: 0}, nil
}

// ---- canary -----------------------------------------------------------------

func TestDeployCanaryProceeds(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	rec := &touchRecorder{}
	tgt := &stubTarget{execFn: rec.exec}
	s1 := seedServer(t, tgt, "web-1")
	s2 := seedServer(t, tgt, "web-2")
	s3 := seedServer(t, tgt, "web-3")
	runID, artID := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactDist, "dist/shop.tar.gz")

	svc := New(tgt, rsvc)
	res, err := svc.Deploy(context.Background(), DeployInput{
		RunID: runID, ArtifactID: artID,
		ServerIDs: []string{s1.ID, s2.ID, s3.ID},
		Strategy:  "canary",
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if len(res) != 3 {
		t.Fatalf("want 3 results, got %d", len(res))
	}
	for i, r := range res {
		if r.Status != run.TargetSuccess {
			t.Fatalf("target %d status = %s (msg %q), want success", i, r.Status, r.Message)
		}
	}
	// 金丝雀全过 → 其余机也应被部署(全 3 台触达)。
	if !rec.touched[s2.ID] || !rec.touched[s3.ID] {
		t.Fatalf("金丝雀通过后其余机应被部署;touched=%v", rec.touched)
	}
}

func TestDeployCanaryAbortsOnFailure(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	s1ID, s2ID, s3ID := "", "", ""
	rec := &touchRecorder{
		failOn: func(serverID string, cmd []string) bool {
			// 金丝雀(第一台)放置命令失败。
			return serverID == s1ID && cmd[0] == "mkdir"
		},
	}
	tgt := &stubTarget{execFn: rec.exec}
	s1 := seedServer(t, tgt, "web-1")
	s2 := seedServer(t, tgt, "web-2")
	s3 := seedServer(t, tgt, "web-3")
	s1ID, s2ID, s3ID = s1.ID, s2.ID, s3.ID
	_ = s2ID
	_ = s3ID
	runID, artID := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactDist, "dist/shop.tar.gz")

	svc := New(tgt, rsvc)
	res, err := svc.Deploy(context.Background(), DeployInput{
		RunID: runID, ArtifactID: artID,
		ServerIDs: []string{s1.ID, s2.ID, s3.ID},
		Strategy:  "canary",
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	// 金丝雀(s1)失败,其余(s2/s3)应被中止 → 全 failed。
	if res[0].Status != run.TargetFailed {
		t.Fatalf("canary status = %s, want failed", res[0].Status)
	}
	for i := 1; i < 3; i++ {
		if res[i].Status != run.TargetFailed {
			t.Fatalf("rest target %d status = %s, want failed(aborted)", i, res[i].Status)
		}
		if !strings.Contains(res[i].Message, "未部署") {
			t.Fatalf("rest target %d message = %q, want 含「未部署」", i, res[i].Message)
		}
	}
	// 关键:被中止的其余机**绝不应被执行任何部署命令**(仍运行旧版本)。
	if rec.touched[s2.ID] || rec.touched[s3.ID] {
		t.Fatalf("金丝雀失败后其余机不应被部署;touched=%v", rec.touched)
	}
}

// ---- blue-green -------------------------------------------------------------

func TestDeployBlueGreenSuccess(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	rec := &touchRecorder{}
	tgt := &stubTarget{execFn: rec.exec}
	s1 := seedServer(t, tgt, "web-1")
	s2 := seedServer(t, tgt, "web-2")
	runID, artID := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactDist, "dist/shop.tar.gz")

	svc := New(tgt, rsvc)
	res, err := svc.Deploy(context.Background(), DeployInput{
		RunID: runID, ArtifactID: artID,
		ServerIDs: []string{s1.ID, s2.ID},
		Strategy:  "blue_green",
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	for i, r := range res {
		if r.Status != run.TargetSuccess {
			t.Fatalf("target %d status = %s (msg %q), want success", i, r.Status, r.Message)
		}
	}
	// 直铺蓝绿不再有 current 切换:两机各自铺进部署路径,无软链/原子 rename 命令。
	for _, c := range rec.calls2 {
		if len(c) > 0 && (c[0] == "ln" || c[0] == "mv" || c[0] == "readlink") {
			t.Fatalf("直铺蓝绿不应出现软链切换命令: %v", c)
		}
	}
	var sawMkdir int
	for _, c := range rec.calls2 {
		if len(c) > 0 && c[0] == "mkdir" {
			sawMkdir++
		}
	}
	if sawMkdir != 2 {
		t.Fatalf("两机应各建一次部署目录, got %d (%v)", sawMkdir, rec.calls2)
	}
}

// 蓝绿安全不变量:预备阶段任一机失败 → 任何机都不进入激活(不重启、不探测),已铺好的机仍跑旧版本。
func TestDeployBlueGreenStageFailAbortsActivate(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	s2ID := ""
	rec := &touchRecorder{
		failOn: func(serverID string, cmd []string) bool {
			return serverID == s2ID && cmd[0] == "mkdir" // 第二台铺产物失败
		},
	}
	tgt := &stubTarget{execFn: rec.exec}
	s1 := seedServer(t, tgt, "web-1")
	s2 := seedServer(t, tgt, "web-2")
	s2ID = s2.ID
	runID, artID := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactDist, "dist/shop.tar.gz")

	svc := New(tgt, rsvc)
	res, err := svc.Deploy(context.Background(), DeployInput{
		RunID: runID, ArtifactID: artID,
		ServerIDs:   []string{s1.ID, s2.ID},
		Strategy:    "blue_green",
		Config:      map[string]string{"restartCommand": "systemctl restart shop"},
		HealthCheck: &HealthCheck{Type: HealthCheckCommand, Command: []string{"true"}, Retries: 1},
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	// 任一就绪失败 → 整体不激活;两机均非 success。
	for i, r := range res {
		if r.Status == run.TargetSuccess {
			t.Fatalf("target %d 不应 success(预备失败应中止全部重启);msg=%q", i, r.Message)
		}
	}
	// 关键:已铺好的 s1 不应被执行健康探测(即没进激活阶段)。
	for _, c := range rec.calls2 {
		if len(c) == 1 && c[0] == "true" {
			t.Fatalf("预备阶段失败后不应有任何健康探测: %v", rec.calls2)
		}
	}
}

// 直铺语义:激活阶段(重启 + 健康)各机独立成败 —— 健康失败机记 failed 并说明无上一版本可回滚,
// 其余机保持 success(没有 current 软链,故无机群级一并回滚;image 蓝绿才有)。
func TestDeployBlueGreenActivateHealthFailIsPerServer(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	badID := ""
	rec := &touchRecorder{
		failOn: func(serverID string, cmd []string) bool {
			// 仅 bad 机健康探测(command=["true"])失败;铺产物/重启均成功。
			return serverID == badID && cmd[0] == "true"
		},
	}
	tgt := &stubTarget{execFn: rec.exec}
	good := seedServer(t, tgt, "web-good")
	bad := seedServer(t, tgt, "web-bad")
	badID = bad.ID
	runID, artID := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactDist, "dist/shop.tar.gz")

	svc := New(tgt, rsvc)
	hc := &HealthCheck{Type: HealthCheckCommand, Command: []string{"true"}, Retries: 1}
	res, err := svc.Deploy(context.Background(), DeployInput{
		RunID: runID, ArtifactID: artID,
		ServerIDs:   []string{good.ID, bad.ID},
		Strategy:    "blue_green",
		HealthCheck: hc,
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if res[0].Status != run.TargetSuccess {
		t.Fatalf("good 机应 success, got %s (msg %q)", res[0].Status, res[0].Message)
	}
	if res[1].Status != run.TargetFailed {
		t.Fatalf("bad 机应 failed(直铺无回滚), got %s (msg %q)", res[1].Status, res[1].Message)
	}
	if !strings.Contains(res[1].Message, "无上一版本可回滚") {
		t.Fatalf("bad 机 message 应说明无版本可回滚: %q", res[1].Message)
	}
}

// ---- image 蓝绿 ----------------------------------------------------------------

func TestDeployBlueGreenImageSuccess(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	rec := &touchRecorder{}
	tgt := &stubTarget{execFn: rec.exec}
	s1 := seedServer(t, tgt, "web-1")
	s2 := seedServer(t, tgt, "web-2")
	runID, artID := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactImage, "registry/app:v2")

	svc := New(tgt, rsvc)
	res, err := svc.Deploy(context.Background(), DeployInput{
		RunID: runID, ArtifactID: artID, ServerIDs: []string{s1.ID, s2.ID}, Strategy: "blue_green",
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	for i, r := range res {
		if r.Status != run.TargetSuccess {
			t.Fatalf("image 蓝绿目标 %d 应 success,实际 %s / %q", i, r.Status, r.Message)
		}
	}
	// 应发生 docker pull(预备)与 docker run(切换)。
	var sawPull, sawRun bool
	for _, c := range rec.calls2 {
		if len(c) >= 2 && c[0] == "docker" && c[1] == "pull" {
			sawPull = true
		}
		if len(c) >= 2 && c[0] == "docker" && c[1] == "run" {
			sawRun = true
		}
	}
	if !sawPull || !sawRun {
		t.Fatalf("image 蓝绿应有 pull + run;pull=%v run=%v", sawPull, sawRun)
	}
}

func TestDeployBlueGreenImageFleetRollback(t *testing.T) {
	db := testDB(t)
	rsvc := run.New(db)
	badID := ""
	rec := &touchRecorder{
		inspectImage: "registry/app:v1", // 模拟已有上一镜像 → 可回滚
		failOn: func(serverID string, cmd []string) bool {
			return serverID == badID && len(cmd) > 0 && cmd[0] == "true" // bad 机健康失败
		},
	}
	tgt := &stubTarget{execFn: rec.exec}
	good := seedServer(t, tgt, "web-good")
	bad := seedServer(t, tgt, "web-bad")
	badID = bad.ID
	runID, artID := seedSuccessRunWithArtifact(t, db, rsvc, run.ArtifactImage, "registry/app:v2")

	svc := New(tgt, rsvc)
	hc := &HealthCheck{Type: HealthCheckCommand, Command: []string{"true"}, Retries: 1}
	res, err := svc.Deploy(context.Background(), DeployInput{
		RunID: runID, ArtifactID: artID, ServerIDs: []string{good.ID, bad.ID},
		Strategy: "blue_green", HealthCheck: hc,
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	for i, r := range res {
		if r.Status != run.TargetRolledBack {
			t.Fatalf("image 机群回滚:目标 %d (%s) 应 rolled_back,实际 %s / %q", i, r.ServerName, r.Status, r.Message)
		}
	}
}
