package retention

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/artifactstore"
	"github.com/fuuhoo/pipewright/internal/storetest"
	"github.com/google/uuid"
)

// seedArtifact 给某条运行插一行产物,reference 即制品库句柄。
func seedArtifact(t *testing.T, db *sql.DB, runID, ref string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db,
		`INSERT INTO run_artifacts (id, run_id, type, name, reference, size_bytes, metadata_json, created_at)
		 VALUES (?, ?, 'jar', 'app.jar', ?, 1024, '', ?)`,
		uuid.NewString(), runID, ref, now)
}

// putOld 往制品库存一份字节并把落盘时间钉到 want(造「过了宽限期的产物」)。
// 路径按库的分片规则自己拼(root/前 2 位/hash),测试里拿着 root 就直接拼,不另开导出面。
func putOld(t *testing.T, root string, s *artifactstore.Store, content string, want time.Time) string {
	t.Helper()
	key, _, err := s.Put(strings.NewReader(content))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	p := filepath.Join(root, key[:2], key)
	if err := os.Chtimes(p, want, want); err != nil {
		t.Fatalf("Chtimes %s: %v", p, err)
	}
	return key
}

// TestArtifactGCReclaimsAfterRunDeletion 是这条链路的主干用例:
// 产物行在 → 字节有人认,不删;运行被裁掉(产物行随外键级联消失)→ 同一份字节变孤儿,下一轮删掉。
func TestArtifactGCReclaimsAfterRunDeletion(t *testing.T) {
	ctx := context.Background()
	db := storetest.OpenDB(t)
	svc := NewService(db)
	root := t.TempDir()
	store, err := artifactstore.New(root)
	if err != nil {
		t.Fatalf("artifactstore.New: %v", err)
	}

	now := time.Now().UTC()
	old := now.Add(-72 * time.Hour)
	proj := seedProject(t, db)
	runID := insertRun(t, db, proj, "success", old)
	key := putOld(t, root, store, "jar bytes", old)
	seedArtifact(t, db, runID, key)

	gc := NewArtifactGC(db, store, time.Hour)

	res, err := gc.Run(ctx, now)
	if err != nil {
		t.Fatalf("GC.Run(运行还在): %v", err)
	}
	if res.Deleted != 0 || !store.Has(key) {
		t.Fatalf("运行还在时不该删字节:res=%+v has=%v", res, store.Has(key))
	}
	if res.Kept != 1 {
		t.Fatalf("Kept = %d, want 1", res.Kept)
	}

	// 开启保留策略(每项目只留 1 条)→ 用更新的一条顶掉旧运行,裁完之后字节没人认了。
	if err := svc.SetConfig(ctx, Config{Enabled: true, KeepPerProject: 1}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	insertRun(t, db, proj, "success", now)
	n, err := svc.Prune(ctx, now)
	if err != nil || n != 1 {
		t.Fatalf("Prune = %d,%v, want 1,nil", n, err)
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(1) FROM run_artifacts WHERE reference = ?`, key).Scan(&rows); err != nil {
		t.Fatalf("数产物行: %v", err)
	}
	if rows != 0 {
		t.Fatal("删 run 没级联清掉产物行(本用例的前提不成立)")
	}

	res, err = gc.Run(ctx, now)
	if err != nil {
		t.Fatalf("GC.Run(裁完之后): %v", err)
	}
	if res.Deleted != 1 || store.Has(key) {
		t.Fatalf("孤儿字节应被回收:res=%+v has=%v", res, store.Has(key))
	}
	if res.Freed <= 0 {
		t.Fatalf("Freed 应如实报字节数,得到 %d", res.Freed)
	}
}

// TestArtifactGCLeavesBytesInsideGrace 验证宽限期存在的理由:构建是「先把字节 Put 进库、
// 再往 run_artifacts 写行」,中间这一小段它确实零引用,绝不能被当成孤儿。
func TestArtifactGCLeavesBytesInsideGrace(t *testing.T) {
	root := t.TempDir()
	db := storetest.OpenDB(t)
	store, err := artifactstore.New(root)
	if err != nil {
		t.Fatalf("artifactstore.New: %v", err)
	}
	key, _, err := store.Put(strings.NewReader("正在归档的字节"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	gc := NewArtifactGC(db, store, time.Hour)
	res, err := gc.Run(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("GC.Run: %v", err)
	}
	if res.Deleted != 0 || res.Young != 1 || !store.Has(key) {
		t.Fatalf("宽限期内的字节被误删:res=%+v has=%v", res, store.Has(key))
	}
}

// TestArtifactGCStopsWhenLiveSetUnreadable 是「读不到活引用就绝不开删」的反向用例:
// 拿着空集合去扫等于删光制品库,所以查询失败必须报错、一个字节也不动。
func TestArtifactGCStopsWhenLiveSetUnreadable(t *testing.T) {
	root := t.TempDir()
	db := storetest.OpenDB(t)
	store, err := artifactstore.New(root)
	if err != nil {
		t.Fatalf("artifactstore.New: %v", err)
	}
	old := time.Now().UTC().Add(-3 * time.Hour)
	dropped := putOld(t, root, store, "orphan bytes", old)
	if _, err := db.Exec(`DROP TABLE run_artifacts`); err != nil {
		t.Fatalf("DROP TABLE: %v", err)
	}

	gc := NewArtifactGC(db, store, time.Hour)
	if _, err := gc.Run(context.Background(), time.Now().UTC()); err == nil {
		t.Fatal("读不到活引用时应报错,而不是静默「无事可做」")
	}
	if !store.Has(dropped) {
		t.Fatal("活引用读不到时不该删任何字节")
	}
}

type fakePruner struct {
	deleted int
	err     error
}

func (f fakePruner) Prune(context.Context, time.Time) (int, error) { return f.deleted, f.err }

type fakeCollector struct {
	ran int
	res artifactstore.SweepResult
	err error
}

func (f *fakeCollector) Run(context.Context, time.Time) (artifactstore.SweepResult, error) {
	f.ran++
	return f.res, f.err
}

// TestSweeperOrphansRunAfterPrune 验证钩子:每轮裁剪后跑一次回收,且 Prune 失败也照跑
// —— 孤儿回收与保留策略无关(策略默认关,但删项目同样造孤儿)。
func TestSweeperOrphansRunAfterPrune(t *testing.T) {
	col := &fakeCollector{res: artifactstore.SweepResult{Deleted: 3, Freed: 4096}}
	s := NewSweeper(fakePruner{}, time.Hour)
	s.WithOrphans(col)
	s.runOnce(context.Background())
	if col.ran != 1 {
		t.Fatalf("回收跑了 %d 次, want 1", col.ran)
	}

	broken := &fakeCollector{}
	NewSweeper(fakePruner{err: errors.New("db busy")}, time.Hour).WithOrphans(broken).runOnce(context.Background())
	if broken.ran != 1 {
		t.Fatal("Prune 失败也应照做孤儿回收(两者互不依赖)")
	}
}

// TestSweeperWithoutCollector 验证没挂回收器时一切照旧(制品库不可用的部署形态)。
func TestSweeperWithoutCollector(t *testing.T) {
	NewSweeper(fakePruner{deleted: 2}, time.Hour).runOnce(context.Background()) // 不该 panic
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{999, "999 B"},
		{1024, "1.0 KiB"},
		{1536 * 1024, "1.5 MiB"},
		{3 * 1024 * 1024, "3.0 MiB"},
		{5 * 1024 * 1024 * 1024, "5.0 GiB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.n); got != c.want {
			t.Errorf("humanBytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}
