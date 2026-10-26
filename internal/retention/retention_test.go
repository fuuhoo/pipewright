package retention

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/storetest"
	"github.com/google/uuid"
)

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func seedProject(t *testing.T, db *sql.DB) string {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	credID := uuid.NewString()
	mustExec(t, db, `INSERT INTO credentials (id, name, type, scope, ciphertext, masked_value, created_at, updated_at)
		VALUES (?, 'c', 'git_token', '', X'00', 'm', ?, ?)`, credID, now, now)
	projID := uuid.NewString()
	mustExec(t, db, `INSERT INTO projects (id, name, repo_url, default_branch, credential_id, created_at, updated_at)
		VALUES (?, 'acme', 'https://example.com/p.git', 'main', ?, ?, ?)`, projID, credID, now, now)
	return projID
}

// insertRun 插一条运行(指定状态 + created_at);返回 runID。
func insertRun(t *testing.T, db *sql.DB, projID, status string, createdAt time.Time) string {
	t.Helper()
	id := uuid.NewString()
	ts := createdAt.UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO pipeline_runs (id, project_id, status, trigger_type, created_at)
		VALUES (?, ?, ?, 'manual', ?)`, id, projID, status, ts)
	return id
}

func countRuns(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM pipeline_runs`).Scan(&n); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	return n
}

func runExists(t *testing.T, db *sql.DB, id string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM pipeline_runs WHERE id = ?`, id).Scan(&n); err != nil {
		t.Fatalf("exists: %v", err)
	}
	return n > 0
}

// TestPruneKeepPerProject 验证每项目保留最近 N 条终态运行,多出的(更旧的)被删。
func TestPruneKeepPerProject(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := NewService(db)
	proj := seedProject(t, db)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var ids []string
	for i := 0; i < 5; i++ { // 5 条,created_at 递增(i 越大越新)
		ids = append(ids, insertRun(t, db, proj, "success", base.Add(time.Duration(i)*time.Hour)))
	}
	if err := svc.SetConfig(context.Background(), Config{Enabled: true, KeepPerProject: 2}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	n, err := svc.Prune(context.Background(), base.Add(100*time.Hour))
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 3 {
		t.Fatalf("应删除 3 条(保留最新 2),实删 %d", n)
	}
	// 最新两条(ids[3], ids[4])应保留;最旧三条删除。
	if !runExists(t, db, ids[4]) || !runExists(t, db, ids[3]) {
		t.Fatal("最新 2 条应保留")
	}
	if runExists(t, db, ids[0]) || runExists(t, db, ids[1]) || runExists(t, db, ids[2]) {
		t.Fatal("较旧 3 条应被删")
	}
}

// TestPruneSkipsNonTerminal 验证进行中的运行(queued/running/waiting_approval)绝不被删。
func TestPruneSkipsNonTerminal(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := NewService(db)
	proj := seedProject(t, db)
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	running := insertRun(t, db, proj, "running", old)
	queued := insertRun(t, db, proj, "queued", old)
	waiting := insertRun(t, db, proj, "waiting_approval", old)
	done := insertRun(t, db, proj, "success", old)

	if err := svc.SetConfig(context.Background(), Config{Enabled: true, MaxAgeDays: 1}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	n, err := svc.Prune(context.Background(), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("仅终态(success)应被删,实删 %d", n)
	}
	if runExists(t, db, done) {
		t.Fatal("旧的 success 应被删")
	}
	for _, id := range []string{running, queued, waiting} {
		if !runExists(t, db, id) {
			t.Fatalf("进行中运行 %s 不应被删", id)
		}
	}
}

// TestPruneMaxAge 验证按年龄删除:早于 cutoff 的删,之后的留。
func TestPruneMaxAge(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := NewService(db)
	proj := seedProject(t, db)
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	oldRun := insertRun(t, db, proj, "failed", now.AddDate(0, 0, -40)) // 40 天前
	newRun := insertRun(t, db, proj, "failed", now.AddDate(0, 0, -3))  // 3 天前

	if err := svc.SetConfig(context.Background(), Config{Enabled: true, MaxAgeDays: 30}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	n, err := svc.Prune(context.Background(), now)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 1 || runExists(t, db, oldRun) || !runExists(t, db, newRun) {
		t.Fatalf("应只删 40 天前那条(n=%d, old存在=%v, new存在=%v)", n, runExists(t, db, oldRun), runExists(t, db, newRun))
	}
}

// TestPruneUnionKeepsWithinAge 钉住「并集保留」的核心口径:配置了条数**和**年龄两个维度时,
// 一条运行要两个维度都越界才删 —— 超出保留条数但还没到龄的,必须留着。
// 这正是用户要的「只保留最近一年 / 或者 1000 条,两者符合其一就保留」。
func TestPruneUnionKeepsWithinAge(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := NewService(db)
	proj := seedProject(t, db)
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	recent := make([]string, 0, 3)
	for i := 0; i < 3; i++ { // 3 条都在年龄窗口内(1~3 天前)
		recent = append(recent, insertRun(t, db, proj, "success", now.AddDate(0, 0, -(i+1))))
	}
	old1 := insertRun(t, db, proj, "failed", now.AddDate(0, 0, -40))
	old2 := insertRun(t, db, proj, "success", now.AddDate(0, 0, -41))

	// keepGlobal=2 + maxAgeDays=30:全局最近 2 条 + 30 天内。
	if err := svc.SetConfig(context.Background(), Config{Enabled: true, KeepGlobal: 2, MaxAgeDays: 30}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	n, err := svc.Prune(context.Background(), now)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	// 排名 3 的那条(2 天前)虽超出条数但没到龄 → 留;两条 40+ 天的既超条数又到龄 → 删。
	if n != 2 {
		t.Fatalf("应删 2 条(仅两个维度都越界的),实删 %d", n)
	}
	for _, id := range append(recent, old1, old2) {
		want := !(id == old1 || id == old2)
		if got := runExists(t, db, id); got != want {
			t.Fatalf("运行 %s 存在=%v,期望=%v", id, got, want)
		}
	}
}

// TestPruneGlobalKeepRanksAcrossProjects 验证 keepGlobal 数的是**全局总条数**,不分项目。
// 若把它当每项目来排名,安静项目那两条旧的会活下来 —— 这条测试就是用来区分两种口径的。
func TestPruneGlobalKeepRanksAcrossProjects(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := NewService(db)
	pQuiet, pBusy := seedProject(t, db), seedProject(t, db)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// 安静项目只有两条,且都是最旧的;繁忙项目三条更新。
	q1 := insertRun(t, db, pQuiet, "success", base.Add(1*time.Hour))
	q2 := insertRun(t, db, pQuiet, "failed", base.Add(2*time.Hour))
	var busy []string
	for i := 0; i < 3; i++ {
		busy = append(busy, insertRun(t, db, pBusy, "success", base.Add(time.Duration(i+3)*time.Hour)))
	}

	// 全局保留最近 2 条 → 留 busy 最新的两条(5h、4h),其余 3 条(含 3h 那条和安静项目全部)删。
	if err := svc.SetConfig(context.Background(), Config{Enabled: true, KeepGlobal: 2}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	n, err := svc.Prune(context.Background(), base.Add(100*time.Hour))
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 3 {
		t.Fatalf("全局只留 2 条,5 条终态应删 3,实删 %d", n)
	}
	for _, id := range busy[1:] {
		if !runExists(t, db, id) {
			t.Fatalf("全局最近 2 条之一 %s 不应被删", id)
		}
	}
	for _, id := range []string{q1, q2, busy[0]} {
		if runExists(t, db, id) {
			t.Fatalf("超出全局保留条数的 %s 应被删(说明 keepGlobal 被当成了每项目口径)", id)
		}
	}
}

// TestPruneUnionPerProjectRescuesFromGlobal 验证另一条并集腿:某项目跑得少,即使它的运行
// 排在 keepGlobal 之外,只要仍在 keepPerProject 之内就应保留(旧维度不作死)。
func TestPruneUnionPerProjectRescuesFromGlobal(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := NewService(db)
	pBusy, pQuiet := seedProject(t, db), seedProject(t, db)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// 安静项目只有 1 条,但时间上排在繁忙项目的多条之后(全局排名靠后)。
	quiet := insertRun(t, db, pQuiet, "success", base.Add(1*time.Hour))
	var busy []string
	for i := 0; i < 4; i++ {
		busy = append(busy, insertRun(t, db, pBusy, "success", base.Add(time.Duration(i+2)*time.Hour)))
	}

	// keepGlobal=2 + keepPerProject=1:quiet 全局第 5(越界)但项目内第 1(未越界)→ 留。
	if err := svc.SetConfig(context.Background(), Config{Enabled: true, KeepGlobal: 2, KeepPerProject: 1}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	n, err := svc.Prune(context.Background(), base.Add(1000*time.Hour))
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if !runExists(t, db, quiet) {
		t.Fatalf("项目内最近 1 条应被并集保留,却删了(共删 %d)", n)
	}
	// 繁忙项目:最近 1 条两维度都满足 → 留;第 2 条全局内(排名 2)但项目内越界 → 留(并集);
	// 第 3、4 条两个维度都越界 → 删。
	if n != 2 {
		t.Fatalf("应删 2 条,实删 %d", n)
	}
}

// TestPruneDisabledNoop 验证总开关关 / 无任何条件时不删。
func TestPruneDisabledNoop(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := NewService(db)
	proj := seedProject(t, db)
	old := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		insertRun(t, db, proj, "success", old)
	}
	// 关闭 → 不删
	_ = svc.SetConfig(context.Background(), Config{Enabled: false, KeepPerProject: 1, MaxAgeDays: 1})
	if n, _ := svc.Prune(context.Background(), time.Now()); n != 0 {
		t.Fatalf("关闭时应不删,实删 %d", n)
	}
	// 开但无条件(keep=0 且 age=0) → 不删
	_ = svc.SetConfig(context.Background(), Config{Enabled: true})
	if n, _ := svc.Prune(context.Background(), time.Now()); n != 0 {
		t.Fatalf("无条件时应不删,实删 %d", n)
	}
	if countRuns(t, db) != 3 {
		t.Fatal("3 条都应保留")
	}
}

// TestPruneCleansNonCascadeChildren 验证删 run 时连带清掉无级联的 run_logs + deploy_targets
// (不留孤儿、不因 deploy_targets RESTRICT 外键报错)。
func TestPruneCleansNonCascadeChildren(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := NewService(db)
	proj := seedProject(t, db)
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	run := insertRun(t, db, proj, "success", old)
	// run_logs(无外键)
	mustExec(t, db, `INSERT INTO run_logs (id, run_id, seq, ts, stream, step_ordinal, text)
		VALUES (?, ?, 1, ?, 'stdout', 0, 'hello')`, uuid.NewString(), run, old.Format(time.RFC3339))
	// deploy_targets(RESTRICT 外键)
	mustExec(t, db, `INSERT INTO deploy_targets (id, run_id, server_id, server_name, status, started_at)
		VALUES (?, ?, 'srv', 'srv-1', 'success', ?)`, uuid.NewString(), run, old.Format(time.RFC3339))

	if err := svc.SetConfig(context.Background(), Config{Enabled: true, MaxAgeDays: 1}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	n, err := svc.Prune(context.Background(), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Prune 应成功(不被 deploy_targets RESTRICT 卡住): %v", err)
	}
	if n != 1 || runExists(t, db, run) {
		t.Fatal("run 应被删")
	}
	for _, tbl := range []string{"run_logs", "deploy_targets"} {
		var c int
		if err := db.QueryRow(`SELECT COUNT(1) FROM `+tbl+` WHERE run_id = ?`, run).Scan(&c); err != nil {
			t.Fatalf("count %s: %v", tbl, err)
		}
		if c != 0 {
			t.Fatalf("%s 应被连带清空,残留 %d 条(孤儿)", tbl, c)
		}
	}
}

// TestConfigRoundTripNormalizesNegatives 验证配置往返 + 负值归零(三个维度都走一遍)。
func TestConfigRoundTripNormalizesNegatives(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := NewService(db)
	if err := svc.SetConfig(context.Background(), Config{Enabled: true, KeepGlobal: -7, KeepPerProject: -5, MaxAgeDays: -1}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	got, err := svc.GetConfig(context.Background())
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if !got.Enabled || got.KeepGlobal != 0 || got.KeepPerProject != 0 || got.MaxAgeDays != 0 {
		t.Fatalf("负值应归零, got %+v", got)
	}
	// 正常值原样往返
	if err := svc.SetConfig(context.Background(), Config{Enabled: true, KeepGlobal: 1000, KeepPerProject: 20, MaxAgeDays: 365}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	got, err = svc.GetConfig(context.Background())
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if got != (Config{Enabled: true, KeepGlobal: 1000, KeepPerProject: 20, MaxAgeDays: 365}) {
		t.Fatalf("往返不一致, got %+v", got)
	}
}

// insertDelivery 直接插一条通知台账行(projectID/runID 传空串 = 存 NULL);返回 id。
func insertDelivery(t *testing.T, db *sql.DB, projectID, runID, createdAt string) string {
	t.Helper()
	id := uuid.NewString()
	var p, r any
	if projectID != "" {
		p = projectID
	}
	if runID != "" {
		r = runID
	}
	mustExec(t, db, `INSERT INTO notification_deliveries
		(id, event, project_id, run_id, channel_id, channel_name, status, title, error_text, created_at)
		VALUES (?, 'cert_expiring', ?, ?, 'ch-1', '默认渠道', 'ok', '证书即将到期', '', ?)`, id, p, r, createdAt)
	return id
}

func deliveryExists(t *testing.T, db *sql.DB, id string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM notification_deliveries WHERE id = ?`, id).Scan(&n); err != nil {
		t.Fatalf("delivery exists: %v", err)
	}
	return n > 0
}

// TestPruneDeliveriesUnionDims 验证台账沿用运行保留策略的「并集保留」口径,且条数排名
// 只在**无运行可依**的行里算(证书/异常这类全局事件才是级联够不到的增长源)。
func TestPruneDeliveriesUnionDims(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := NewService(db)
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) string { return now.Add(-d).UTC().Format(time.RFC3339) }

	recent := []string{
		insertDelivery(t, db, "", "", ago(24*time.Hour)),
		insertDelivery(t, db, "", "", ago(2*24*time.Hour)),
		insertDelivery(t, db, "", "", ago(3*24*time.Hour)),
	}
	old1 := insertDelivery(t, db, "", "", ago(40*24*time.Hour))
	old2 := insertDelivery(t, db, "", "", ago(41*24*time.Hour))

	// 最近 2 条 + 30 天:排名 3 那条(3 天前)超条数但未到龄 → 留;两条 40+ 天的两个维度都越界 → 删。
	if err := svc.SetConfig(context.Background(), Config{Enabled: true, KeepGlobal: 2, MaxAgeDays: 30}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	n, err := svc.PruneDeliveries(context.Background(), now)
	if err != nil {
		t.Fatalf("PruneDeliveries: %v", err)
	}
	if n != 2 {
		t.Fatalf("应删 2 条(仅两个维度都越界的),实删 %d", n)
	}
	for _, id := range recent {
		if !deliveryExists(t, db, id) {
			t.Fatalf("并集保留内的台账 %s 不该被删", id)
		}
	}
	for _, id := range []string{old1, old2} {
		if deliveryExists(t, db, id) {
			t.Fatalf("超条数又到龄的台账 %s 应被删", id)
		}
	}
}

// TestPruneDeliveriesSkipsRowsOfLiveRuns 钉住台账的归属边界:只要那条运行还在库里,它的通知记录
// 就跟着它活 —— 否则运行详情页里的通知会凭空变空。这类行由 Prune(删运行)时的外键级联清。
func TestPruneDeliveriesSkipsRowsOfLiveRuns(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := NewService(db)
	proj := seedProject(t, db)
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	// 进行中(非终态)的运行不会被 Prune 删,故它的台账行是「活运行 + 老记录」的组合。
	running := insertRun(t, db, proj, "running", now.AddDate(0, 0, -400))
	linked := insertDelivery(t, db, proj, running, now.AddDate(0, 0, -400).UTC().Format(time.RFC3339))
	orphan := insertDelivery(t, db, "", "", now.AddDate(0, 0, -400).UTC().Format(time.RFC3339))

	if err := svc.SetConfig(context.Background(), Config{Enabled: true, MaxAgeDays: 30}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	n, err := svc.PruneDeliveries(context.Background(), now)
	if err != nil {
		t.Fatalf("PruneDeliveries: %v", err)
	}
	if n != 1 || deliveryExists(t, db, orphan) {
		t.Fatalf("只该删不挂在运行上的那条(n=%d, orphan还在=%v)", n, deliveryExists(t, db, orphan))
	}
	if !deliveryExists(t, db, linked) {
		t.Fatal("运行还在,它的通知记录不该被单独裁掉")
	}
}

// TestPruneCascadesDeliveries 验证 0083 的外键级联真生效:删运行连带清掉它的台账行,不留孤儿。
func TestPruneCascadesDeliveries(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := NewService(db)
	proj := seedProject(t, db)
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	run := insertRun(t, db, proj, "success", old)
	deliv := insertDelivery(t, db, proj, run, old.Format(time.RFC3339))

	if err := svc.SetConfig(context.Background(), Config{Enabled: true, MaxAgeDays: 1}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if _, err := svc.Prune(context.Background(), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if runExists(t, db, run) {
		t.Fatal("旧运行应被删")
	}
	if deliveryExists(t, db, deliv) {
		t.Fatal("台账应随运行级联删除(外键未生效会留孤儿)")
	}
}

// TestPruneDeliveriesDisabledNoop 验证总开关关闭 / 两个维度都不限时不删台账。
func TestPruneDeliveriesDisabledNoop(t *testing.T) {
	db := storetest.OpenDB(t)
	svc := NewService(db)
	ancient := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	ids := []string{
		insertDelivery(t, db, "", "", ancient),
		insertDelivery(t, db, "", "", ancient),
	}
	_ = svc.SetConfig(context.Background(), Config{Enabled: false, KeepGlobal: 1, MaxAgeDays: 1})
	if n, err := svc.PruneDeliveries(context.Background(), time.Now()); err != nil || n != 0 {
		t.Fatalf("关闭时不删, got n=%d err=%v", n, err)
	}
	_ = svc.SetConfig(context.Background(), Config{Enabled: true})
	if n, err := svc.PruneDeliveries(context.Background(), time.Now()); err != nil || n != 0 {
		t.Fatalf("无条件时不删, got n=%d err=%v", n, err)
	}
	for _, id := range ids {
		if !deliveryExists(t, db, id) {
			t.Fatal("两条都应保留")
		}
	}
}
