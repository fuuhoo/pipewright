package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/huangchengsir/pipewright/internal/approval"
	"github.com/huangchengsir/pipewright/internal/audit"
	"github.com/huangchengsir/pipewright/internal/auth"
	"github.com/huangchengsir/pipewright/internal/build"
	"github.com/huangchengsir/pipewright/internal/dagrun"
	"github.com/huangchengsir/pipewright/internal/pipeline"
	"github.com/huangchengsir/pipewright/internal/run"
)

// approvals.go 实现人工审批门(Epic 8 · Story 8-4)的 gate hook 与审批端点。
//
// gate hook(NewApprovalGate)注入 dagrun:进入 Gate 阶段前登记待批 + 置 run 状态 waiting_approval +
// 阻塞协调器,直到端点投递批准/拒绝(或取消/超时)。端点(approve/reject)经协调器解析决定 + 审计。

// defaultGateTimeout 是审批门兜底超时:超时自动拒绝以释放被该运行占用的 worker。
// 审批人节奏可较慢,故取较长值;运行亦可经取消端点立即释放。
const defaultGateTimeout = 24 * time.Hour

// ApprovalNotifier 是审批门进入等待态后的 best-effort 通知钩子(供 main 注入)。
// runID/stageID 用于签发签名链接;projectID 用于按项目维度路由通知。
// 实现绝不阻塞 / 不返回错误冒泡(内部 best-effort、自带超时);nil 表示不发通知。
type ApprovalNotifier func(ctx context.Context, projectID, projectName, runID, stageID string)

// NewApprovalGate 构造注入 dagrun 的审批门 hook(供 main 装配)。
// notifier 非 nil 时,在 run 进入 waiting_approval 后被 best-effort 调用(发「需要审批」通知 +
// 签名审批链接);为 nil(或签名/PUBLIC_URL 未配)则不发通知,门行为不变。
func NewApprovalGate(runs run.Service, coord *approval.Coordinator, store *approval.Store, notifier ApprovalNotifier) dagrun.GateFunc {
	return func(ctx context.Context, r *run.Run, stage pipeline.Stage) (bool, error) {
		return awaitDecision(ctx, runs, coord, store, r.ID, stage.ID, stage.Name, "审批门",
			func() {
				if notifier != nil {
					notifier(ctx, r.ProjectID, r.ProjectName, r.ID, stage.ID)
				}
			})
	}
}

// NewDeployGate 构造注入 build 的「首批后暂停」确认 hook(供 main 装配)。
//
// 它和阶段审批门用的是同一套登记 + 阻塞 + 投递机制,只差两件事:门 ID 带 deploy: 前缀(一条记录
// 说的是「这批机器发完了,要不要继续」而不是「这个阶段准不准跑」),以及通知文案走同一个 notifier。
// 批准 → build 层续发其余主机;拒绝/超时/取消 → 其余主机标「已中止」,本节点判失败。
func NewDeployGate(runs run.Service, coord *approval.Coordinator, store *approval.Store, notifier ApprovalNotifier) build.DeployPauseGate {
	return func(ctx context.Context, r *run.Run, jobID, jobName string) (bool, error) {
		gateID := approval.DeployGateID(jobID)
		return awaitDecision(ctx, runs, coord, store, r.ID, gateID, jobName, "分批确认",
			func() {
				if notifier != nil {
					notifier(ctx, r.ProjectID, r.ProjectName, r.ID, gateID)
				}
			})
	}
}

// awaitDecision 是两类人工门(阶段审批门 / 部署首批后暂停)共用的等待体:登记待批 → 置 run 为
// waiting_approval → 阻塞等决定 → 回写记录并放回 running。onWaiting 在进入等待态后调一次(通知)。
//
// 一个 run 可以同时挂着两道门(并行阶段的审批门、部署节点的分批确认)。状态翻转按「还有没有别的门
// 在等」来做:第一道进时才置 waiting,最后一道决完才放回 running —— 否则一道门放行就把 run 报回
// running,另一道门的审批人会看到一个「已经不在等待」的运行。
func awaitDecision(
	ctx context.Context,
	runs run.Service,
	coord *approval.Coordinator,
	store *approval.Store,
	runID, gateID, gateName, kind string,
	onWaiting func(),
) (bool, error) {
	key := approval.Key(runID, gateID)
	_ = store.CreatePending(ctx, runID, gateID, gateName)
	ch, shared := coord.BeginWait(runID, key)
	defer coord.Cancel(key)

	// 已有人在等就别再翻状态(CAS 会从 running 失败),但仍要让自己出现在等待队列里。
	if !shared {
		if err := runs.MarkWaitingApproval(ctx, runID); err != nil {
			// 已终态/被取消:无法进入等待,退化失败让 worker 收尾。
			return false, err
		}
	}
	onWaiting()

	var (
		approved bool
		status   = approval.StatusRejected
		by       string
	)
	select {
	case d := <-ch:
		approved = d.Approved
		by = d.Actor
		if approved {
			status = approval.StatusApproved
		}
	case <-ctx.Done():
		by = "canceled"
		_ = store.Decide(context.Background(), runID, gateID, status, by)
		leaveWaiting(context.Background(), runs, coord, runID)
		return false, ctx.Err()
	case <-time.After(defaultGateTimeout):
		by = "timeout"
		_ = store.Decide(context.Background(), runID, gateID, status, by)
		leaveWaiting(context.Background(), runs, coord, runID)
		return false, fmt.Errorf("%s timed out after %s", kind, defaultGateTimeout)
	}
	_ = store.Decide(context.Background(), runID, gateID, status, by)
	// 决定后置回 running,让 worker 在收尾时按 running→终态 落定。
	// Resolve 已把自己的等待摘掉,所以这里问的是「同 run 还有别的门吗」。
	leaveWaiting(context.Background(), runs, coord, runID)
	return approved, nil
}

// leaveWaiting 只在最后一个人决定完时才把 run 放回 running;还有别的门在等就维持 waiting 不动。
func leaveWaiting(ctx context.Context, runs run.Service, coord *approval.Coordinator, runID string) {
	if coord.HasWaiterFor(runID) {
		return
	}
	_ = runs.ResumeFromApproval(ctx, runID)
}

// makeApprovalDecisionHandler 返回 approve(approve=true)/reject(false)端点 handler。
// body {stageId}(阶段门传阶段 ID,分批确认门传 "deploy:<jobId>");经协调器投递决定。
// 该门当前不在等待 → 409。
//
// actor 取自当前会话而非写死 "admin":审批留痕的意义就在「谁点的」,归属校验由 /api 组的
// accessGuardMiddleware 统一做(runs/{id} 写方法 → ActOperate),这里不重复判定。
func makeApprovalDecisionHandler(coord *approval.Coordinator, store *approval.Store, rec audit.Recorder, ac auth.Authenticator, approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if coord == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "审批门服务未初始化")
			return
		}
		id := chi.URLParam(r, "id")
		actor := actorFromRequest(r, ac)
		r.Body = http.MaxBytesReader(w, r.Body, 1<<13)
		var req struct {
			StageID string `json:"stageId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "请求体格式错误")
			return
		}
		stageID := strings.TrimSpace(req.StageID)
		if stageID == "" {
			writeError(w, http.StatusUnprocessableEntity, "missing_stage", "缺少 stageId")
			return
		}
		key := approval.Key(id, stageID)
		if !coord.Resolve(key, approval.Decision{Approved: approve, Actor: actor}) {
			writeError(w, http.StatusConflict, "not_waiting", "该运行阶段当前不在等待审批")
			return
		}
		action := "run.approve"
		if !approve {
			action = "run.reject"
		}
		recordAudit(r.Context(), rec, audit.Entry{
			Actor:      actor,
			Action:     action,
			TargetType: "run",
			TargetID:   id,
			Detail:     map[string]any{"stageId": stageID},
			IP:         clientIP(r),
		})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "approved": approve})
	}
}

// makeListApprovalsHandler 返回 GET /api/runs/{id}/approvals(某运行的审批门记录列表)。
func makeListApprovalsHandler(store *approval.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "审批门服务未初始化")
			return
		}
		recs, err := store.ListForRun(r.Context(), chi.URLParam(r, "id"))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
			return
		}
		if recs == nil {
			recs = []approval.Record{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": recs})
	}
}
