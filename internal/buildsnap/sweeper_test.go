package buildsnap

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakePruner 记录每次 Prune 的参数,并可注入一次失败。
type fakePruner struct {
	mu     sync.Mutex
	calls  []time.Duration // 每次调用的 ttl
	times  []time.Time
	err    error
	ttlGot time.Duration
}

func (f *fakePruner) Prune(_ context.Context, now time.Time, ttl time.Duration) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, ttl)
	f.times = append(f.times, now)
	f.ttlGot = ttl
	if f.err != nil {
		return 0, f.err
	}
	return 1, nil
}

func (f *fakePruner) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// waitFor 轮询等到 pruner 被调用至少 n 次,超时即失败。
func waitFor(t *testing.T, f *fakePruner, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if f.count() >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("等了 2s,清理器只被调用 %d 次(want >=%d)", f.count(), n)
}

// TestNewSweeperDefaults 验证零值参数退化为默认保留窗口与间隔(装配处传 0 不应退化成"每纳秒扫一次")。
func TestNewSweeperDefaults(t *testing.T) {
	f := &fakePruner{}
	s := NewSweeper(f, 0, 0)
	if s.ttl != DefaultTTL || s.interval != 10*time.Minute || s.firstDelay != time.Minute {
		t.Fatalf("默认值错:ttl=%s interval=%s first=%s", s.ttl, s.interval, s.firstDelay)
	}
	if neg := NewSweeper(f, -time.Hour, -time.Minute); neg.ttl != DefaultTTL || neg.interval != 10*time.Minute {
		t.Fatalf("负值应同样退化为默认:ttl=%s interval=%s", neg.ttl, neg.interval)
	}
	custom := NewSweeper(f, 15*time.Minute, 30*time.Second)
	if custom.ttl != 15*time.Minute || custom.interval != 30*time.Second {
		t.Fatalf("显式值被改写:ttl=%s interval=%s", custom.ttl, custom.interval)
	}
}

// TestSweeperRunsPeriodically 验证首扫 + 周期扫都真发生,且 Stop 之后不再调用。
func TestSweeperRunsPeriodically(t *testing.T) {
	f := &fakePruner{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewSweeper(f, time.Hour, time.Millisecond)
	s.firstDelay = time.Millisecond
	s.Start(ctx)

	waitFor(t, f, 2)
	s.Stop()

	f.mu.Lock()
	after := len(f.calls)
	f.mu.Unlock()
	if f.ttlGot != time.Hour {
		t.Fatalf("Prune 收到的 ttl = %s (want 1h)", f.ttlGot)
	}
	waitForQuiet(t, f, after)
}

// TestSweeperSurvivesPruneError 验证清理失败只记日志:下一轮仍继续(一次数据库抖动不该让快照永久不清)。
func TestSweeperSurvivesPruneError(t *testing.T) {
	f := &fakePruner{err: errors.New("disk busy")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewSweeper(f, DefaultTTL, time.Millisecond)
	s.firstDelay = time.Millisecond
	s.Start(ctx)

	waitFor(t, f, 3)
	s.Stop()
}

// TestSweeperStopsOnContextCancel 验证 ctx 取消也能退出 goroutine(与 Stop 两条腿都要有效)。
func TestSweeperStopsOnContextCancel(t *testing.T) {
	f := &fakePruner{}
	ctx, cancel := context.WithCancel(context.Background())
	s := NewSweeper(f, DefaultTTL, time.Millisecond)
	s.firstDelay = time.Millisecond
	s.Start(ctx)
	waitFor(t, f, 1)
	cancel()

	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("ctx 取消后 Stop 未返回")
	}
}

// waitForQuiet 断言 stop 之后调用次数不再增长。
func waitForQuiet(t *testing.T, f *fakePruner, before int) {
	t.Helper()
	time.Sleep(20 * time.Millisecond)
	if got := f.count(); got != before {
		t.Fatalf("Stop 后仍在清理:%d → %d", before, got)
	}
}
