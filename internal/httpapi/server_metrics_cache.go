package httpapi

import (
	"context"
	"sync"
	"time"

	"github.com/fuuhoo/pipewright/internal/target"
)

// MetricsCache 是服务器指标的「后台采、前台读」快照缓存。
//
// 为什么要有它:状态页原先按浏览器轮询触发批量采集 —— 每多开一个前端就多一整轮 SSH。
// 改成后端按固定节奏(默认 60s)采一轮写进缓存,HTTP 端点只读缓存:无论同时开几个页面,
// 同一台机器一分钟只采一次,采集时间点也由后端统一(前端只显示缓存里的 collectedAt)。
//
// 失败语义:后一次采集失败**不覆盖**上一次成功的快照。读出来的 DTO 仍带上次成功的指标,
// 另用 stale + lastError 标注「最近一次采集失败」,lastAttemptAt 说明那次尝试发生在几点;
// collectedAt 仍是数据真正采到的时刻 —— 两个时间分开,界面才能诚实地说清「数据多旧」。
type MetricsCache struct {
	svc target.Service

	mu      sync.Mutex
	entries map[string]*metricsCacheEntry
	// round 是正在进行的刷新轮(serverID → 尚未出结果);nil = 当前没有在采。
	// 读路径据此把本轮还没采到的机器标成 collecting(界面画占位卡)。
	round map[string]struct{}
}

type metricsCacheEntry struct {
	// last 是最近一次采到(reachable=true)的完整结果;失败不覆盖它。
	last *serverMetricsDTO
	// attemptAt/lastErr 记最近一次尝试(成败都记),供「最近采集失败」标注。
	attemptAt time.Time
	lastErr   string
}

// NewMetricsCache 创建缓存。svc 为 nil 的缓存不采任何东西(读方只会拿到收集中占位)。
func NewMetricsCache(svc target.Service) *MetricsCache {
	return &MetricsCache{svc: svc, entries: map[string]*metricsCacheEntry{}}
}

// Run 按 interval 循环 Refresh,直到 ctx 结束。立即先跑一轮:重启后的第一屏不该干等一个周期。
// interval <= 0 时直接返回(调用方据此关闭后台采集)。
func (c *MetricsCache) Run(ctx context.Context, interval time.Duration) {
	if c == nil || c.svc == nil || interval <= 0 {
		return
	}
	c.Refresh(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.Refresh(ctx)
		}
	}
}

// Refresh 采一轮:列出全部服务器(不分可见范围,读路径再按 actor 收敛),有界并发逐台采集,
// 结果并入缓存。单台失败只影响它自己。
func (c *MetricsCache) Refresh(ctx context.Context) {
	if c == nil || c.svc == nil {
		return
	}
	servers, err := c.svc.List(ctx)
	if err != nil {
		// 列不出来就保留上一轮快照:那仍是最后一次真实采集,不是错误数据。
		return
	}
	ids := make([]string, len(servers))
	alive := make(map[string]struct{}, len(servers))
	for i, s := range servers {
		ids[i] = s.ID
		alive[s.ID] = struct{}{}
	}

	c.beginRound(ids)
	events := runHostPool(ctx, ids, metricsConcurrency, func(ctx context.Context, id string) serverMetricsDTO {
		// 批量逐台独立:定位类错误对某台亦只表现为该台 reachable:false(忽略 locErr)。
		dto, _ := collectServerMetrics(ctx, c.svc, id)
		return dto
	})
	for ev := range events {
		c.record(ids[ev.idx], ev.dto, time.Now())
	}
	c.endRound(alive)
}

func (c *MetricsCache) beginRound(ids []string) {
	pending := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		pending[id] = struct{}{}
	}
	c.mu.Lock()
	c.round = pending
	c.mu.Unlock()
}

func (c *MetricsCache) record(id string, dto serverMetricsDTO, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[id]
	if e == nil {
		e = &metricsCacheEntry{}
		c.entries[id] = e
	}
	e.attemptAt = at
	if dto.Reachable && usableMetrics(dto) {
		// 存副本:读路径会在这份底稿上叠加 stale/collecting 等呈现字段,不能写回。
		snap := dto
		e.last = &snap
		e.lastErr = ""
	} else {
		e.lastErr = dto.Error
		if e.lastErr == "" {
			// 探针通了但一条指标命令都没解析出来(连接中途断/批量超时):同样算失败,
			// 别让一份空指标盖掉上一轮的真数据。
			e.lastErr = "采集未返回可用指标"
		}
	}
	if c.round != nil {
		delete(c.round, id)
	}
}

// usableMetrics 判定一次采集是否至少拿到了一个资源维度。
// reachable 只代表探针拨通了;批量命令全灭时 CPU/Memory/Disk 皆空,那不该被当成新数据。
func usableMetrics(dto serverMetricsDTO) bool {
	if dto.Memory != nil || dto.Disk != nil {
		return true
	}
	return dto.CPU != nil && (dto.CPU.Loadavg1 != nil || dto.CPU.Cores != nil || dto.CPU.Usage != nil)
}

// endRound 收尾本轮,并清掉已删除服务器的缓存条目(以本轮列出的名单为准)。
func (c *MetricsCache) endRound(alive map[string]struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.round = nil
	for id := range c.entries {
		if _, ok := alive[id]; !ok {
			delete(c.entries, id)
		}
	}
}

// Snapshot 取一台的当前展示快照。永不触发采集。
//   - 从没采到过 → Collecting:true(本轮在采,或还没轮到它),界面画占位卡。
//   - 采到过 → 上次成功的数据 + stale/lastError/lastAttemptAt 标注。
func (c *MetricsCache) Snapshot(id string) serverMetricsDTO {
	if c == nil {
		return serverMetricsDTO{ServerID: id, Collecting: true}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[id]
	if e == nil {
		return serverMetricsDTO{ServerID: id, Collecting: true}
	}
	collecting := false
	if c.round != nil {
		_, collecting = c.round[id]
	}
	if e.last == nil {
		// 从来没有成功过:把最近一次失败原样摆出来(reachable:false + 原因),不是占位。
		return serverMetricsDTO{
			ServerID:      id,
			Reachable:     false,
			Error:         e.lastErr,
			Collecting:    collecting,
			LastAttemptAt: formatMetricsTime(e.attemptAt),
		}
	}
	out := *e.last
	out.Collecting = collecting
	out.Stale = e.lastErr != ""
	out.LastError = e.lastErr
	out.LastAttemptAt = formatMetricsTime(e.attemptAt)
	return out
}

// Collecting 报告当前是否有一轮刷新在跑(批量响应的顶层标记)。
func (c *MetricsCache) Collecting() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.round != nil
}

func formatMetricsTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
