package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/fuuhoo/pipewright/internal/target"
)

// togglingDialer 包一层 linuxLikeDialer,可中途切成「不可达」——模拟后一次采集失败。
type togglingDialer struct {
	cmdDialer
	mu   sync.Mutex
	fail bool
}

func (d *togglingDialer) setFail(v bool) {
	d.mu.Lock()
	d.fail = v
	d.mu.Unlock()
}

func (d *togglingDialer) failing() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.fail
}

func (d *togglingDialer) Run(ctx context.Context, addr string, cfg target.SSHConfig, cmd []string) (*target.ExecResult, error) {
	if d.failing() {
		return nil, target.ErrUnreachable
	}
	return d.cmdDialer.Run(ctx, addr, cfg, cmd)
}

func (d *togglingDialer) RunWithStdin(ctx context.Context, addr string, cfg target.SSHConfig, cmd []string, in io.Reader) (*target.ExecResult, error) {
	if d.failing() {
		return nil, target.ErrUnreachable
	}
	return d.cmdDialer.RunWithStdin(ctx, addr, cfg, cmd, in)
}

func TestMetricsCachePlaceholderBeforeFirstCollection(t *testing.T) {
	c := NewMetricsCache(nil)
	got := c.Snapshot("srv-1")
	if !got.Collecting || got.Reachable {
		t.Fatalf("未采过的机器应是收集中占位: %+v", got)
	}
	if got.ServerID != "srv-1" {
		t.Fatalf("serverId = %q", got.ServerID)
	}
}

func TestMetricsCacheCollectingDuringRound(t *testing.T) {
	c := NewMetricsCache(nil)
	c.beginRound([]string{"a", "b"})
	if got := c.Snapshot("a"); !got.Collecting {
		t.Fatalf("本轮还没采到 → collecting, got %+v", got)
	}
	c.record("b", serverMetricsDTO{
		ServerID:    "b",
		Reachable:   true,
		Disk:        &diskMetric{Path: "/", UsedBytes: 1, TotalBytes: 2},
		CollectedAt: "2026-10-28T00:00:00Z",
	}, time.Now())
	if got := c.Snapshot("b"); got.Collecting || !got.Reachable {
		t.Fatalf("本轮已采到 → 不再占位, got %+v", got)
	}
	if got := c.Snapshot("a"); !got.Collecting {
		t.Fatalf("仍在等待 → collecting, got %+v", got)
	}
	c.endRound(map[string]struct{}{"a": {}, "b": {}})
	if got := c.Snapshot("b"); got.Collecting {
		t.Fatalf("轮次结束后不该再标 collecting, got %+v", got)
	}
	// 本轮失败且从未成功:失败原样上报(不是占位),并带尝试时间。
	c.record("c", serverMetricsDTO{ServerID: "c", Reachable: false, Error: "无法连接服务器"}, time.Now())
	got := c.Snapshot("c")
	if got.Collecting || got.Reachable || got.Error == "" || got.LastAttemptAt == "" {
		t.Fatalf("从未成功的失败应直接展示: %+v", got)
	}
	// 探针通了但批量命令全灭(没有任何资源维度):同样算失败,不当作新数据。
	c.record("d", serverMetricsDTO{ServerID: "d", Reachable: true, CPU: &cpuMetric{}}, time.Now())
	if got := c.Snapshot("d"); got.Reachable || got.Error == "" {
		t.Fatalf("空指标应判为采集失败: %+v", got)
	}
}

func TestMetricsCacheRetainsLastSuccessOnFailure(t *testing.T) {
	d := &togglingDialer{cmdDialer: linuxLikeDialer()}
	srv, client, csrf, tsvc := setupServerAPIWithSvc(t, d)
	credID := newSSHCredAPI(t, client, srv.URL, csrf, "pw")
	id := createServerAPI(t, client, srv.URL, csrf, credID)

	c := NewMetricsCache(tsvc)
	ctx := context.Background()
	c.Refresh(ctx)
	first := c.Snapshot(id)
	if !first.Reachable || first.Stale || first.LastError != "" || first.CollectedAt == "" || first.LastAttemptAt == "" {
		t.Fatalf("首轮成功快照不对: %+v", first)
	}

	// 第二次采集失败:旧数据保留、collectedAt 不变,另标 stale + 原因。
	d.setFail(true)
	c.Refresh(ctx)
	second := c.Snapshot(id)
	if !second.Reachable || !second.Stale || second.LastError == "" {
		t.Fatalf("失败后应保留旧数据并标 stale: %+v", second)
	}
	if second.CollectedAt != first.CollectedAt {
		t.Fatalf("collectedAt 应停在最后一次成功: %q vs %q", second.CollectedAt, first.CollectedAt)
	}
	if second.CPU == nil || second.Memory == nil || second.Disk == nil {
		t.Fatalf("旧指标不该被失败清空: %+v", second)
	}

	// 恢复后再成功:stale 标注消失。
	d.setFail(false)
	c.Refresh(ctx)
	third := c.Snapshot(id)
	if third.Stale || third.LastError != "" || !third.Reachable {
		t.Fatalf("恢复后应清掉失败标注: %+v", third)
	}
}

func TestMetricsCacheEndpointsDoNotDial(t *testing.T) {
	d := &countingBatchDialer{}
	var cache *MetricsCache
	// 缓存要与 router 共享同一实例:在装配 option 时用已注入的 target 服务建它。
	srv, client, csrf, _ := setupServerAPIWithSvc(t, d, func(o *options) {
		cache = NewMetricsCache(o.servers)
		o.serverMetricsCache = cache
	})
	credID := newSSHCredAPI(t, client, srv.URL, csrf, "pw")
	id := createServerAPI(t, client, srv.URL, csrf, credID)

	// 后台刷一轮,记录拨号次数;之后所有 HTTP 读都不得再拨号。
	cache.Refresh(context.Background())
	runs, batches, cmds := d.counts()
	if runs == 0 || batches == 0 {
		t.Fatalf("后台刷新应先采一轮: runs=%d batches=%d", runs, batches)
	}

	getJSON := func(path string, out any) {
		t.Helper()
		resp, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status=%d: %s", path, resp.StatusCode, raw)
		}
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("unmarshal %s: %v: %s", path, err, raw)
		}
	}

	var single serverMetricsDTO
	getJSON("/api/servers/"+id+"/metrics", &single)
	if !single.Reachable || single.CollectedAt == "" || single.LastAttemptAt == "" {
		t.Fatalf("单台端点应读缓存: %+v", single)
	}

	var body struct {
		Items      []serverMetricsDTO `json:"items"`
		Collecting bool               `json:"collecting"`
	}
	getJSON("/api/servers/metrics", &body)
	if len(body.Items) != 1 || !body.Items[0].Reachable {
		t.Fatalf("批量应返回缓存里的那台: %+v", body)
	}

	respStream, err := client.Get(srv.URL + "/api/servers/metrics?stream=1")
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	streamRaw, _ := io.ReadAll(respStream.Body)
	respStream.Body.Close()
	if respStream.StatusCode != http.StatusOK || len(streamRaw) == 0 {
		t.Fatalf("stream status=%d body=%q", respStream.StatusCode, streamRaw)
	}

	runs2, batches2, cmds2 := d.counts()
	if runs2 != runs || batches2 != batches || len(cmds2) != len(cmds) {
		t.Fatalf("读端点不应触发采集: before runs=%d batches=%d cmds=%d, after runs=%d batches=%d cmds=%d",
			runs, batches, len(cmds), runs2, batches2, len(cmds2))
	}
}
