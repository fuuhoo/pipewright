package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

// 批量「逐台采集」端点的共用骨架(/servers/metrics?stream=1、/servers/containers?stream=1)。
//
// 两个端点解的是同一个问题:一台死机器会拖满 SSH 探针超时,批量「等齐再回」就等于整屏空白
// 陪着它等。形态也必须一致(默认攒齐回 {items:[…]},?stream=1 一行一台),否则前端要为每个
// 端点各写一套读取与容错。
//
// 采集本身永远逐台独立:某台失败只表现为它那一份 reachable:false + 人读 error,不 500。

// streamFrame 是一台采集结果带登记下标的投递单元。
// idx 让默认 JSON 形态能还原成登记顺序 —— 流式的到达顺序是「谁先采完」,不是名单顺序。
type streamFrame[T any] struct {
	idx int
	dto T
}

// wantsNDJSONStream 判定批量端点是否走 NDJSON 逐台流式(?stream=1)。
func wantsNDJSONStream(r *http.Request) bool {
	q := r.URL.Query().Get("stream")
	return q != "" && q != "0" && !strings.EqualFold(q, "false")
}

// runHostPool 有界并发逐台采集(concurrency 限同时开的 SSH 数,防 N 台一起打爆),
// 结果**按完成顺序**送入返回的 channel,全部完成后关闭。
// collect 必须自己把失败折进返回值里(而不是返回 error),否则某台失败会拖塌整批。
func runHostPool[T any](
	ctx context.Context,
	ids []string,
	concurrency int,
	collect func(ctx context.Context, id string) T,
) <-chan streamFrame[T] {
	out := make(chan streamFrame[T], len(ids))
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
			}
			dto := collect(ctx, id)
			// 客户端已断开就别再攒了:没人收的结果留着只是占内存。
			select {
			case out <- streamFrame[T]{idx: i, dto: dto}:
			case <-ctx.Done():
			}
		}(i, id)
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}

// drainHostStream 读完一整批逐台结果,按登记下标还原成有序切片。
// 漏采的台(客户端断开/取消掐在途中)用 missing 补一条兜底 —— 半条零值记录比「没采到」更误导人。
func drainHostStream[T any](events <-chan streamFrame[T], total int, missing func(idx int) T) []T {
	items := make([]T, total)
	seen := make([]bool, total)
	for ev := range events {
		items[ev.idx] = ev.dto
		seen[ev.idx] = true
	}
	for i, ok := range seen {
		if !ok {
			items[i] = missing(i)
		}
	}
	return items
}

// writeNDJSONStream 把逐台结果写成 NDJSON(一行一个 JSON 对象),每行写完立即 flush。
func writeNDJSONStream[T any](ctx context.Context, w http.ResponseWriter, events <-chan streamFrame[T], total int) {
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	// 逐台推送不能被缓存;X-Accel-Buffering 关掉反代(nginx 系)的响应缓冲,否则一行也攒着不发。
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	// 头一写就定了 200;流式响应没有「整包错误」可回,失败只表现为少了行。
	w.WriteHeader(http.StatusOK)

	enc := json.NewEncoder(w)
	written := 0
	for written < total {
		select {
		case ev, ok := <-events:
			if !ok {
				return // 采集已全部完成(或被取消)
			}
			if err := enc.Encode(ev.dto); err != nil {
				return // 客户端断开:再采下去也没人收
			}
			written++
			// flush 失败 = 这个 ResponseWriter 不支持逐块下发,退回攒着发(内容不变,只是不实时)。
			_ = rc.Flush()
		case <-ctx.Done():
			return
		}
	}
}
