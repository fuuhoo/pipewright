package buildsnap

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

// seedRun 建一条最小可引用的运行(凭据 → 项目 → run),返回 runID。快照表外键指向 pipeline_runs。
func seedRun(t *testing.T, db *sql.DB) string {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	credID := uuid.NewString()
	mustExec(t, db, `INSERT INTO credentials (id, name, type, scope, ciphertext, masked_value, created_at, updated_at)
		VALUES (?, 'c', 'git_token', '', X'00', 'm', ?, ?)`, credID, now, now)
	projID := uuid.NewString()
	mustExec(t, db, `INSERT INTO projects (id, name, repo_url, default_branch, credential_id, created_at, updated_at)
		VALUES (?, 'acme', 'https://example.com/p.git', 'main', ?, ?, ?)`, projID, credID, now, now)
	runID := uuid.NewString()
	mustExec(t, db, `INSERT INTO pipeline_runs (id, project_id, status, trigger_type, created_at)
		VALUES (?, ?, 'failed', 'manual', ?)`, runID, projID, now)
	return runID
}

func countSnaps(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM run_workspace_snapshots`).Scan(&n); err != nil {
		t.Fatalf("count snapshots: %v", err)
	}
	return n
}

// TestSaveListRoundtrip 验证一份快照写进去、读出来形状不变(声明判定 + 清单 + 截断位 + 时间)。
func TestSaveListRoundtrip(t *testing.T) {
	db := storetest.OpenDB(t)
	runID := seedRun(t, db)
	st := New(db, Limits{})
	ctx := context.Background()

	in := Snapshot{
		RunID: runID, Stage: "构建", JobID: "job-1", JobName: "后端构建", Reason: ReasonArtifactMiss,
		Declared:  []Declared{{Path: "target/app.jar", Status: StatusMissing, Parent: ".", Siblings: []string{"pom.xml", "src/"}, SiblingsMore: 2}},
		Entries:   []Entry{{Path: "src/"}, {Path: "pom.xml", Size: 1234, Dropped: 7}},
		Truncated: true,
	}
	if err := st.Save(ctx, in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if in.ID != "" {
		t.Fatalf("Save 不该改写调用方的 ID")
	}

	out, err := st.ListByRun(ctx, runID)
	if err != nil {
		t.Fatalf("ListByRun: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("快照数 = %d (want 1)", len(out))
	}
	got := out[0]
	if got.RunID != runID || got.Stage != in.Stage || got.JobID != in.JobID || got.JobName != in.JobName || got.Reason != in.Reason {
		t.Fatalf("标量字段漂移:%+v", got)
	}
	if got.ID == "" {
		t.Fatalf("ID 未自动生成")
	}
	if !got.Truncated || got.CreatedAt.IsZero() {
		t.Fatalf("截断位/时间未回读:truncated=%v created=%v", got.Truncated, got.CreatedAt)
	}
	if len(got.Declared) != 1 || got.Declared[0].Status != StatusMissing || got.Declared[0].Parent != "." ||
		got.Declared[0].SiblingsMore != 2 || len(got.Declared[0].Siblings) != 2 {
		t.Fatalf("声明判定未完整回读:%+v", got.Declared)
	}
	if len(got.Entries) != 2 || got.Entries[0].Path != "src/" || got.Entries[1].Size != 1234 || got.Entries[1].Dropped != 7 {
		t.Fatalf("清单未完整回读:%+v", got.Entries)
	}
}

// TestSaveNilSafe 验证未注入仓储(单测 / 禁用采集)时调用不炸、读为空。
func TestSaveNilSafe(t *testing.T) {
	var st *Store
	ctx := context.Background()
	if err := st.Save(ctx, Snapshot{RunID: "x"}); err != nil {
		t.Fatalf("nil Store.Save 应为 no-op,得到 %v", err)
	}
	out, err := st.ListByRun(ctx, "x")
	if err != nil || len(out) != 0 {
		t.Fatalf("nil Store.ListByRun 应为空:%v err=%v", out, err)
	}
}

// TestListUnknownRunEmpty 验证无快照 / run 不存在都返回空切片而非错误(HTTP 层 404 由 run 存在性决定)。
func TestListUnknownRunEmpty(t *testing.T) {
	db := storetest.OpenDB(t)
	st := New(db, Limits{})
	out, err := st.ListByRun(context.Background(), "no-such-run")
	if err != nil || len(out) != 0 {
		t.Fatalf("want 空切片,得到 %+v err=%v", out, err)
	}
}

// TestAllAgesSurviveUntilRunGoes 钉住「快照随运行记录保留」:三份不同年龄的快照(含成功原因)
// 一次性全读得到,库里没有任何按时间过期的通道 —— 唯一让它们消失的办法是删掉那条 run。
func TestAllAgesSurviveUntilRunGoes(t *testing.T) {
	db := storetest.OpenDB(t)
	runID := seedRun(t, db)
	st := New(db, Limits{})
	ctx := context.Background()
	now := time.Now().UTC()

	for i, s := range []Snapshot{
		{RunID: runID, Reason: ReasonSuccess, CreatedAt: now, Entries: []Entry{{Path: "now"}}},
		{RunID: runID, Reason: ReasonFailed, CreatedAt: now.Add(-30 * time.Minute), Entries: []Entry{{Path: "half-hour-ago"}}},
		{RunID: runID, Reason: ReasonArtifactMiss, CreatedAt: now.Add(-72 * time.Hour), Entries: []Entry{{Path: "two-days-ago"}}},
	} {
		if err := st.Save(ctx, s); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
	}
	left, err := st.ListByRun(ctx, runID)
	if err != nil || len(left) != 3 {
		t.Fatalf("三份快照都该还在(没有 TTL 过期通道):%+v err=%v", left, err)
	}
	// ListByRun 按 created_at 升序:三天前那份仍在最前面,和刚写进去的成功那份一起返回。
	if left[0].Reason != ReasonArtifactMiss || left[0].Entries[0].Path != "two-days-ago" {
		t.Fatalf("三天前那份不该被裁掉:%+v", left[0])
	}
	if left[2].Reason != ReasonSuccess || left[2].Entries[0].Path != "now" {
		t.Fatalf("最新的一份归位不对:%+v", left[2])
	}
}

// TestCascadeOnRunDelete 验证删 run 时快照连带消失(表带 ON DELETE CASCADE;
// 保留清理器因此不必认识这张新表)。
func TestCascadeOnRunDelete(t *testing.T) {
	db := storetest.OpenDB(t)
	runID := seedRun(t, db)
	st := New(db, Limits{})
	ctx := context.Background()
	if err := st.Save(ctx, Snapshot{RunID: runID, Reason: ReasonFailed, Entries: []Entry{{Path: "a"}}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if countSnaps(t, db) != 1 {
		t.Fatalf("前置:快照应已落库")
	}
	mustExec(t, db, `DELETE FROM pipeline_runs WHERE id = ?`, runID)
	if n := countSnaps(t, db); n != 0 {
		t.Fatalf("删 run 后残留快照 %d 份(外键级联未生效?)", n)
	}
}

// TestListOrderStable 验证同 run 多份快照按时间升序返回(同秒并列由 id 破)。
func TestListOrderStable(t *testing.T) {
	db := storetest.OpenDB(t)
	runID := seedRun(t, db)
	st := New(db, Limits{})
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)
	for i, p := range []string{"first", "second"} {
		snap := Snapshot{RunID: runID, Reason: ReasonFailed, CreatedAt: base.Add(time.Duration(i) * time.Minute), Entries: []Entry{{Path: p}}}
		if err := st.Save(ctx, snap); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	out, err := st.ListByRun(ctx, runID)
	if err != nil {
		t.Fatalf("ListByRun: %v", err)
	}
	if len(out) != 2 || out[0].Entries[0].Path != "first" || out[1].Entries[0].Path != "second" {
		t.Fatalf("顺序错:%+v", out)
	}
}
