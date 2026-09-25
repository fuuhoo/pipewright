// Package approval 是人工审批门(Epic 8 · Story 8-4)的进程内协调器 + 持久化。
//
// 模型:阶段声明 Gate=true 时,执行该阶段前运行阻塞、状态置 waiting_approval;阻塞在门内的
// worker 经 Coordinator.Wait 注册等待,审批端点经 Coordinator.Resolve 投递「批准/拒绝」决定。
// 决定 + 待批记录持久化到 run_approvals(供 UI 展示 / 审计 / 重启清理)。
//
// 边界(诚实):审批期间 worker 仍持有该 run(在门内阻塞),不释放回池——故 worker 上限即「同时
// 可挂起的待批运行数」;设审批超时兜底(默认),超时自动拒绝以释放 worker。进程重启会丢失内存
// 等待者:重启时把残留 waiting_approval/running 运行清理为 failed(孤儿,无法恢复)。
package approval

import (
	"strings"
	"sync"
)

// Decision 是一次审批结果。
type Decision struct {
	Approved bool
	Actor    string // 审批人(展示/审计;绝无敏感数据)
}

// Coordinator 是进程内审批门协调器(零持久状态;持久化在 Store)。
type Coordinator struct {
	mu      sync.Mutex
	waiters map[string]chan Decision
}

// New 构造协调器。
func New() *Coordinator {
	return &Coordinator{waiters: make(map[string]chan Decision)}
}

// Key 由 runID + stageID 组成审批门唯一键。
func Key(runID, stageID string) string { return runID + "|" + stageID }

// DeployGatePrefix 标出一条待批记录来自**部署节点的「首批后暂停」**,而不是阶段审批门。
// 两类门共用同一套登记/投递/持久化机制,靠这个前缀分辨是「整个阶段等批」还是「这批机器发完等确认」
// —— 前端据此决定按钮文案与落点列表,审计据此分清谁放行了一半的部署。
const DeployGatePrefix = "deploy:"

// DeployGateID 是部署节点分批确认的门 ID(jobID 在 run 内唯一)。
func DeployGateID(jobID string) string { return DeployGatePrefix + jobID }

// IsDeployGate 报告一个门 ID 是不是部署分批确认(阶段 ID 不会带这个前缀:它们由解析器生成)。
func IsDeployGate(gateID string) bool { return strings.HasPrefix(gateID, DeployGatePrefix) }

// Wait 为 key 注册一个等待,返回接收决定的只读 channel(缓冲 1,Resolve 不阻塞)。
// 同 key 重复 Wait 覆盖旧等待者(旧 channel 永不收到决定;调用方应在 defer 里 Cancel 清理)。
func (c *Coordinator) Wait(key string) <-chan Decision {
	ch := make(chan Decision, 1)
	c.mu.Lock()
	c.waiters[key] = ch
	c.mu.Unlock()
	return ch
}

// Resolve 把决定投递给等待 key 的 worker。无等待者(未到门 / 已超时 / 已决) → false。
// 投递后移除等待者(同一门只决一次)。
func (c *Coordinator) Resolve(key string, d Decision) bool {
	c.mu.Lock()
	ch, ok := c.waiters[key]
	if ok {
		delete(c.waiters, key)
	}
	c.mu.Unlock()
	if !ok {
		return false
	}
	ch <- d // 缓冲 1,不阻塞
	return true
}

// Cancel 移除 key 的等待者(worker 退出 / 取消 / 超时时清理)。幂等。
func (c *Coordinator) Cancel(key string) {
	c.mu.Lock()
	delete(c.waiters, key)
	c.mu.Unlock()
}

// IsWaiting 报告 key 是否有等待者(端点据此区分 404「未在审批」)。
func (c *Coordinator) IsWaiting(key string) bool {
	c.mu.Lock()
	_, ok := c.waiters[key]
	c.mu.Unlock()
	return ok
}

// HasWaiterFor 报告该 run 是否还有门在等人。一个 run 可以同时挂两道门(并行阶段的审批门,
// 或部署节点的「首批后暂停」),运行状态该由**最后一道**决定才放回 running。
func (c *Coordinator) HasWaiterFor(runID string) bool {
	prefix := runID + "|"
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.waiters {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}

// BeginWait 注册 key 的等待,并报告注册**之前**该 run 是否已有别的门在等。
// 判定与注册放在同一把锁里:两个并行门同时进来时,不会都以为自己是第一个、
// 于是第二个把「running → waiting」的 CAS 失败当成自己出错。
func (c *Coordinator) BeginWait(runID, key string) (<-chan Decision, bool) {
	ch := make(chan Decision, 1)
	prefix := runID + "|"
	c.mu.Lock()
	shared := false
	for k := range c.waiters {
		if strings.HasPrefix(k, prefix) {
			shared = true
			break
		}
	}
	c.waiters[key] = ch
	c.mu.Unlock()
	return ch, shared
}

// PendingKeys 返回当前所有等待中的 key(调试/列举)。
func (c *Coordinator) PendingKeys() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.waiters))
	for k := range c.waiters {
		out = append(out, k)
	}
	return out
}
