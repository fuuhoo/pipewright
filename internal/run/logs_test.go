package run

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/fuuhoo/pipewright/internal/mask"
)

// TestAppendLogSeqMonotonic 验证 AppendLog 在同一 run 内分配单调递增 seq(从 1 起)。
func TestAppendLogSeqMonotonic(t *testing.T) {
	db := testDB(t)
	svc := New(db)
	projID := seedProject(t, db)
	r, err := svc.Create(context.Background(), projID, Trigger{Type: TriggerManual})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	for i := 1; i <= 5; i++ {
		seq, err := svc.AppendLog(context.Background(), r.ID, streamStdout, 0, "line")
		if err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
		if seq != i {
			t.Fatalf("第 %d 次 AppendLog seq 应为 %d, got %d", i, i, seq)
		}
	}
}

// TestGetLogsSincePaging 验证 GetLogs 按 sinceSeq 升序分页返回。
func TestGetLogsSincePaging(t *testing.T) {
	db := testDB(t)
	svc := New(db)
	projID := seedProject(t, db)
	r, _ := svc.Create(context.Background(), projID, Trigger{Type: TriggerManual})

	for i := 0; i < 4; i++ {
		if _, err := svc.AppendLog(context.Background(), r.ID, streamStdout, i, "L"); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}

	all, err := svc.GetLogs(context.Background(), r.ID, 0)
	if err != nil {
		t.Fatalf("GetLogs: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("sinceSeq=0 应返回 4 行, got %d", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i].Seq <= all[i-1].Seq {
			t.Fatalf("seq 应升序: %d <= %d", all[i].Seq, all[i-1].Seq)
		}
	}

	rest, _ := svc.GetLogs(context.Background(), r.ID, 2)
	if len(rest) != 2 {
		t.Fatalf("sinceSeq=2 应返回 2 行, got %d", len(rest))
	}
	if rest[0].Seq != 3 {
		t.Fatalf("sinceSeq=2 首行 seq 应为 3, got %d", rest[0].Seq)
	}
}

// TestGetLogsTailWindow 验证 GetLogsTail:倒着取、正着给(升序),beforeSeq 往上翻页,
// limit<=0 退化成全量。这是详情页「默认只搬最近一屏 + 显示更早」的读法。
func TestGetLogsTailWindow(t *testing.T) {
	db := testDB(t)
	svc := New(db)
	projID := seedProject(t, db)
	r, _ := svc.Create(context.Background(), projID, Trigger{Type: TriggerManual})

	for i := 1; i <= 10; i++ {
		if _, err := svc.AppendLog(context.Background(), r.ID, streamStdout, 0, "L"+strconv.Itoa(i)); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}

	tail, err := svc.GetLogsTail(context.Background(), r.ID, 0, 3, -1)
	if err != nil {
		t.Fatalf("GetLogsTail: %v", err)
	}
	if len(tail.Lines) != 3 || tail.Lines[0].Seq != 8 || tail.Lines[2].Seq != 10 {
		t.Fatalf("tail=3 应为 seq 8,9,10 升序, got %+v", seqsOf(tail.Lines))
	}
	// Total 是整条日志的行数,不是本页的 —— 前端靠它写「已加载 3 / 共 10 行」。
	if tail.Total != 10 {
		t.Fatalf("total 应为全量 10 行, got %d", tail.Total)
	}

	earlier, err := svc.GetLogsTail(context.Background(), r.ID, 8, 3, -1)
	if err != nil {
		t.Fatalf("GetLogsTail(beforeSeq=8): %v", err)
	}
	if len(earlier.Lines) != 3 || earlier.Lines[0].Seq != 5 || earlier.Lines[2].Seq != 7 {
		t.Fatalf("beforeSeq=8 应为 seq 5,6,7, got %+v", seqsOf(earlier.Lines))
	}
	if earlier.Total != 10 {
		t.Fatalf("翻页时 total 仍应是 10, got %d", earlier.Total)
	}

	// 窗口比总行数大 → 从第 1 行起的全量。
	wide, _ := svc.GetLogsTail(context.Background(), r.ID, 0, 99, -1)
	if len(wide.Lines) != 10 || wide.Lines[0].Seq != 1 {
		t.Fatalf("tail 超过总行数应回全量且首行 seq=1, got %d 行", len(wide.Lines))
	}

	// limit<=0 = 不设上限(与 GetLogs 同口径)。
	all, _ := svc.GetLogsTail(context.Background(), r.ID, 0, 0, -1)
	if len(all.Lines) != 10 {
		t.Fatalf("limit=0 应回全部 10 行, got %d", len(all.Lines))
	}

	// 翻到顶:seq<1 没有行,返回空切片(非 nil,前端直接 length 判)。
	top, _ := svc.GetLogsTail(context.Background(), r.ID, 1, 3, -1)
	if top.Lines == nil || len(top.Lines) != 0 {
		t.Fatalf("beforeSeq=1 应回空切片, got %+v", top.Lines)
	}
	if top.Total != 10 {
		t.Fatalf("空页也应报出总行数 10, got %d", top.Total)
	}

	// 另一个 run 的行数不能串味。
	r2, _ := svc.Create(context.Background(), projID, Trigger{Type: TriggerManual})
	other, _ := svc.GetLogsTail(context.Background(), r2.ID, 0, 3, -1)
	if len(other.Lines) != 0 || other.Total != 0 {
		t.Fatalf("无日志的 run 应为空 + total 0, got %d 行 total=%d", len(other.Lines), other.Total)
	}
}

// TestGetLogsTailStepFilter 验证按步骤取尾部:步骤是交错落库的(并行阶段),
// 所以筛完不能假设 seq 连续,而且 total 必须跟着步骤口径走 —— 前端的
// 「已加载 x / 共 y 行」与「显示更早」都靠它。
func TestGetLogsTailStepFilter(t *testing.T) {
	db := testDB(t)
	svc := New(db)
	projID := seedProject(t, db)
	r, _ := svc.Create(context.Background(), projID, Trigger{Type: TriggerManual})

	// 交错写:步骤 0/1 各 5 行,seq 1..10。
	for i := 1; i <= 5; i++ {
		if _, err := svc.AppendLog(context.Background(), r.ID, streamStdout, 0, "a"+strconv.Itoa(i)); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
		if _, err := svc.AppendLog(context.Background(), r.ID, streamStdout, 1, "b"+strconv.Itoa(i)); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}

	// 步骤序号从 0 起,所以 0 是一个**有效**筛选值,不能被当成「不过滤」。
	zero, err := svc.GetLogsTail(context.Background(), r.ID, 0, 99, 0)
	if err != nil {
		t.Fatalf("GetLogsTail(step=0): %v", err)
	}
	if len(zero.Lines) != 5 || zero.Total != 5 {
		t.Fatalf("step=0 应只有 5 行且 total=5, got %d 行 total=%d", len(zero.Lines), zero.Total)
	}
	if got := seqsOf(zero.Lines); got[0] != 1 || got[4] != 9 {
		t.Fatalf("step=0 的 seq 应为 1,3,5,7,9, got %v", got)
	}

	// 尾部窗口 + 步骤:取该步骤最后 2 行。
	tail, _ := svc.GetLogsTail(context.Background(), r.ID, 0, 2, 1)
	if len(tail.Lines) != 2 || tail.Lines[0].Seq != 8 || tail.Lines[1].Seq != 10 {
		t.Fatalf("step=1 tail=2 应为 seq 8,10, got %+v", seqsOf(tail.Lines))
	}
	if tail.Total != 5 {
		t.Fatalf("total 应是该步骤的 5 行, got %d", tail.Total)
	}

	// 在该步骤内往上翻:beforeSeq=8 → seq 4,6。
	earlier, _ := svc.GetLogsTail(context.Background(), r.ID, 8, 2, 1)
	if len(earlier.Lines) != 2 || earlier.Lines[0].Seq != 4 || earlier.Lines[1].Seq != 6 {
		t.Fatalf("step=1 beforeSeq=8 应为 seq 4,6, got %+v", seqsOf(earlier.Lines))
	}

	// 不筛步骤(-1)仍是整条日志的口径。
	all, _ := svc.GetLogsTail(context.Background(), r.ID, 0, 99, -1)
	if len(all.Lines) != 10 || all.Total != 10 {
		t.Fatalf("step=-1 应回全量 10 行, got %d 行 total=%d", len(all.Lines), all.Total)
	}
}

// TestStreamLogsAscending 验证 StreamLogs 按 seq 升序逐行交给 fn,fn 报错即中止。
func TestStreamLogsAscending(t *testing.T) {
	db := testDB(t)
	svc := New(db)
	projID := seedProject(t, db)
	r, _ := svc.Create(context.Background(), projID, Trigger{Type: TriggerManual})
	for i := 1; i <= 6; i++ {
		if _, err := svc.AppendLog(context.Background(), r.ID, streamStdout, 0, "L"); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}

	var got []int
	err := svc.StreamLogs(context.Background(), r.ID, func(l LogLine) error {
		got = append(got, l.Seq)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamLogs: %v", err)
	}
	if len(got) != 6 || got[0] != 1 || got[5] != 6 {
		t.Fatalf("应升序交 6 行, got %+v", got)
	}

	stop := errors.New("客户端断开")
	got = nil
	if serr := svc.StreamLogs(context.Background(), r.ID, func(l LogLine) error {
		got = append(got, l.Seq)
		if l.Seq >= 2 {
			return stop
		}
		return nil
	}); !errors.Is(serr, stop) {
		t.Fatalf("fn 报错应中止并回传, got %v", serr)
	}
	if len(got) != 2 {
		t.Fatalf("中止前只应交到第 2 行, got %+v", got)
	}
}

func seqsOf(lines []LogLine) []int {
	out := make([]int, 0, len(lines))
	for _, l := range lines {
		out = append(out, l.Seq)
	}
	return out
}

// TestStubRunnerEmitsMaskedLogs 端到端验证:桩 runner 逐行 emit 日志(含假 secret)→
// 经 dbStepSink.Log 脱敏落库 → run_logs 表/GetLogs 中假 secret 一律 [MASKED]、绝无明文(AC-SEC-04)。
func TestStubRunnerEmitsMaskedLogs(t *testing.T) {
	db := testDB(t)
	svc := New(db)
	masker := mask.NewMasker()
	masker.RegisterSecret(StubFailureSecret)
	// FailAt=0 让首步失败,既 emit stdout(含假 secret 行)又 emit stderr。
	pool := NewWorkerPool(svc,
		WithConcurrency(1),
		WithRunner(&StubRunner{Steps: []string{"构建镜像"}, FailAt: 0}),
		WithLogMasker(masker),
	)
	pool.Start()
	t.Cleanup(func() { pool.Stop(context.Background()) })

	projID := seedProject(t, db)
	r, _ := svc.Create(context.Background(), projID, Trigger{Type: TriggerManual})
	waitForStatus(t, svc, r.ID, StatusFailed)

	logs, err := svc.GetLogs(context.Background(), r.ID, 0)
	if err != nil {
		t.Fatalf("GetLogs: %v", err)
	}
	if len(logs) == 0 {
		t.Fatalf("失败 run 应有日志行")
	}

	sawMasked := false
	hasStderr := false
	for _, l := range logs {
		if strings.Contains(l.Text, StubFailureSecret) {
			t.Fatalf("日志行含明文假 secret(脱敏失败): %q", l.Text)
		}
		if strings.Contains(l.Text, mask.Placeholder) {
			sawMasked = true
		}
		if l.Stream == streamStderr {
			hasStderr = true
		}
	}
	if !sawMasked {
		t.Fatalf("应有一行被替换为 %s(验脱敏命中)", mask.Placeholder)
	}
	if !hasStderr {
		t.Fatalf("失败步骤应追加 stderr 日志行")
	}

	// 裸 DB 断言:run_logs 表内绝无明文假 secret。
	var leak int
	if err := db.QueryRow(
		"SELECT COUNT(1) FROM run_logs WHERE run_id = ? AND `text` LIKE ?",
		r.ID, "%"+StubFailureSecret+"%",
	).Scan(&leak); err != nil {
		t.Fatalf("查 run_logs 明文泄漏: %v", err)
	}
	if leak != 0 {
		t.Fatalf("run_logs 表残留 %d 行明文假 secret", leak)
	}
}
