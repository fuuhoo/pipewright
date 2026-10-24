// sweeper.go 是快照的 TTL 清理器:每 10 分钟删一次超过保留窗口的快照。
//
// 为什么不复用 retention.Sweeper:那套按「天」裁剪整条运行,且策略默认关闭(须用户在设置里显式
// 开启才删)。快照的窗口是小时级、且**必须**清 —— 它带着整份目录清单,留着只会白占库。两者节奏与
// 开关语义都不同,所以各起一个轻量 goroutine,互不牵连。

package buildsnap

import (
	"context"
	"log"
	"sync"
	"time"
)

// DefaultTTL 是快照保留窗口(未显式配置时)。
const DefaultTTL = 2 * time.Hour

// Pruner 按 TTL 删旧快照(由 *Store 实现;测试可注入假实现)。
type Pruner interface {
	Prune(ctx context.Context, now time.Time, ttl time.Duration) (int, error)
}

// Sweeper 周期性执行 Prune。模式与 retention.Sweeper 一致:启动后稍等先扫一次,之后按间隔扫。
type Sweeper struct {
	pruner   Pruner
	ttl      time.Duration
	interval time.Duration
	// firstDelay 是启动后的首扫延迟(默认 1 分钟,避开进程启动高峰);测试可缩短。
	firstDelay time.Duration

	stop chan struct{}
	wg   sync.WaitGroup
}

// NewSweeper 构造清理器。ttl<=0 用 DefaultTTL;interval<=0 用 10 分钟。
func NewSweeper(pruner Pruner, ttl, interval time.Duration) *Sweeper {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	return &Sweeper{pruner: pruner, ttl: ttl, interval: interval, firstDelay: time.Minute, stop: make(chan struct{})}
}

// Start 启动后台清理 goroutine;Stop 或 ctx 取消时退出。清理出错仅记日志,不影响平台。
func (s *Sweeper) Start(ctx context.Context) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		first := time.NewTimer(s.firstDelay)
		defer first.Stop()
		select {
		case <-s.stop:
			return
		case <-ctx.Done():
			return
		case <-first.C:
			s.runOnce(ctx)
		}
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.runOnce(ctx)
			}
		}
	}()
}

// Stop 停止清理器并等待 goroutine 退出。
func (s *Sweeper) Stop() {
	close(s.stop)
	s.wg.Wait()
}

func (s *Sweeper) runOnce(ctx context.Context) {
	n, err := s.pruner.Prune(ctx, time.Now().UTC(), s.ttl)
	if err != nil {
		log.Printf("[buildsnap] 快照清理失败(下次再试):%v", err)
		return
	}
	if n > 0 {
		log.Printf("[buildsnap] 已清理过期工作区快照 %d 份(保留窗口 %s)", n, s.ttl)
	}
}
