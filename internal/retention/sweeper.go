package retention

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/fuuhoo/pipewright/internal/artifactstore"
)

// Pruner 是 Sweeper 依赖的清理能力(由 Service 实现;便于测试注入)。
type Pruner interface {
	Prune(ctx context.Context, now time.Time) (int, error)
}

// OrphanCollector 是每轮裁剪**之后**顺带做的一件事:回收制品库孤儿字节(由 *ArtifactGC 实现)。
// 挂在这里而不是单开一个定时器的原因 —— 孤儿正是「删运行」造出来的,裁完立刻扫最贴合;
// 且它和保留策略本身无关(策略默认关,但删项目同样造孤儿),不能跟着 Prune 一起被关掉。
type OrphanCollector interface {
	Run(ctx context.Context, now time.Time) (artifactstore.SweepResult, error)
}

// Sweeper 是后台保留清理器:按固定间隔调用 Prune 裁剪过期运行数据。
// 这是平台首个**通用周期性维护任务**载体(区别于 cron 的「触发流水线运行」),
// 后续日报/提醒等周期任务可循此模式扩展。
type Sweeper struct {
	pruner   Pruner
	interval time.Duration

	// orphans 在 Start 之前由装配方挂上(未挂则整轮跳过),运行期不再改写,无需加锁。
	orphans OrphanCollector

	stop chan struct{}
	wg   sync.WaitGroup
}

// NewSweeper 构造清理器。interval<=0 时用默认 1 小时。未 Start 不起 goroutine。
func NewSweeper(pruner Pruner, interval time.Duration) *Sweeper {
	if interval <= 0 {
		interval = time.Hour
	}
	return &Sweeper{pruner: pruner, interval: interval, stop: make(chan struct{})}
}

// WithOrphans 挂上制品库孤儿字节回收器(须在 Start 之前;nil 表示不挂)。
func (s *Sweeper) WithOrphans(c OrphanCollector) *Sweeper {
	s.orphans = c
	return s
}

// Start 启动后台清理 goroutine:启动后稍作延迟先跑一次,之后每 interval 跑一次。
// Stop / ctx 取消时退出。清理出错仅记日志,不影响平台。
func (s *Sweeper) Start(ctx context.Context) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		// 启动后延迟首扫,避开启动高峰。
		first := time.NewTimer(time.Minute)
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

func (s *Sweeper) runOnce(ctx context.Context) {
	now := time.Now()
	n, err := s.pruner.Prune(ctx, now)
	if err != nil {
		log.Printf("[retention] 清理失败:%v", err)
	} else if n > 0 {
		log.Printf("[retention] 已清理 %d 条过期运行(连带其日志/步骤/产物)", n)
	}
	// 制品库孤儿回收与上面的策略无关(策略默认关,但删项目同样造孤儿),故 Prune 失败也照跑。
	if s.orphans != nil {
		s.runOrphans(ctx, now)
	}
}

// runOrphans 跑一轮孤儿字节回收:best-effort,只记日志。
func (s *Sweeper) runOrphans(ctx context.Context, now time.Time) {
	res, err := s.orphans.Run(ctx, now)
	if err != nil {
		log.Printf("[retention] 制品库孤儿回收失败:%v", err)
		return
	}
	if res.Deleted == 0 && res.Temps == 0 {
		return // 常态是「什么都没得清」,别每小时刷一行噪声
	}
	log.Printf("[retention] 制品库回收孤儿 %d 个(%s),清临时文件 %d 个;%d 个在宽限期内未动",
		res.Deleted, humanBytes(res.Freed), res.Temps, res.Young)
}

// humanBytes 日志用的人读大小。
func humanBytes(n int64) string {
	const unit = 1 << 10
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// Stop 停止清理 goroutine(幂等)并等待退出。
func (s *Sweeper) Stop() {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	s.wg.Wait()
}
