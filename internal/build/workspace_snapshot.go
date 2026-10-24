package build

// workspace_snapshot.go —— 构建目录快照的两个采集点。
//
// 要解决的场景:产物路径写错。比如命令实际产出在 backend/target/x.jar,节点却声明成
// target/x.jar —— 过去只有日志里一行「产物路径未找到,跳过」,而工作区随即被 rm -rf,
// 用户既看不到"到底生成在哪",也看不到"那一层实际有什么"。这里在出问题那一刻留一份
// 目录清单(采集器见 internal/buildsnap),运行详情「构建目录」栏展示,默认只保留 2 小时。
//
// 触发口径收窄到两条,成功构建因此零额外开销:
//  1. job 失败     → 采该 job 的工作区清单(declared 传 nil);
//  2. 声明的产物路径没命中 → 也采(即使 job 成功),连同逐条判定(missing / unmatched / rejected)
//     与「最近存在的祖先目录 + 其下实际条目」一起记下 —— 这一栏才是"目录写错"的直接证据。
//
// 远程 runner 模式**不采集**:那要在 runner 上多跑一整轮 find 再把清单搬回中控机,代价与收益不称;
// 远程构建的运行详情直接说明"远程构建暂不采集"。
//
// 全程 best-effort:未注入仓储 → 一条都不采;采集或写库出错只记一行日志,绝不让构建因此失败。

import (
	"context"
	"fmt"

	"github.com/fuuhoo/pipewright/internal/buildsnap"
	"github.com/fuuhoo/pipewright/internal/dagrun"
	"github.com/fuuhoo/pipewright/internal/pipeline"
	"github.com/fuuhoo/pipewright/internal/run"
)

// snapshotOnJobFailure 在 script job 失败时采一份工作区目录快照(采集点 1)。
// 被取消的运行不采:现场是半成品,留着只会误导,且取消不该再有后台开销。
func (b *Builder) snapshotOnJobFailure(ctx context.Context, rep dagrun.StageReporter, r *run.Run, stageName string, jb pipeline.Job, workspace string) {
	if canceled(ctx) {
		return
	}
	b.saveWorkspaceSnapshot(ctx, rep, r, stageName, jb, workspace, buildsnap.ReasonFailed, nil)
}

// snapshotIfArtifactMiss 在「声明的产物路径有没命中的」时采一份(采集点 2,job 成功也照采)。
// 全部命中 → 什么都不做,连目录都不遍历。
func (b *Builder) snapshotIfArtifactMiss(ctx context.Context, rep dagrun.StageReporter, r *run.Run, stageName string, jb pipeline.Job, workspace string, declared []buildsnap.Declared) {
	if !buildsnap.HasMiss(declared) {
		return
	}
	if canceled(ctx) {
		return
	}
	b.saveWorkspaceSnapshot(ctx, rep, r, stageName, jb, workspace, buildsnap.ReasonArtifactMiss, declared)
}

// saveWorkspaceSnapshot 遍历工作区 → 落库 → 在日志里留一行指路。
// 落库用去取消的 ctx:失败路径常伴随上层取消,快照正是那时要看的现场。
func (b *Builder) saveWorkspaceSnapshot(ctx context.Context, rep dagrun.StageReporter, r *run.Run, stageName string, jb pipeline.Job, workspace, reason string, declared []buildsnap.Declared) {
	if b.snapshots == nil || r == nil || workspace == "" {
		return
	}
	saveCtx := context.WithoutCancel(ctx)
	snap := buildsnap.Collect(workspace, declared, b.snapshots.CollectLimits())
	snap.RunID, snap.Stage, snap.Reason = r.ID, stageName, reason
	snap.JobID, snap.JobName = jb.ID, jb.Name

	if err := b.snapshots.Save(saveCtx, snap); err != nil {
		_ = rep.Log(saveCtx, streamStderr, "工作区目录快照保存失败(不影响构建):"+err.Error())
		return
	}
	// 只在日志里说一次"去哪儿看",不然采了也没人知道这一栏存在。
	_ = rep.Log(saveCtx, streamStdout, fmt.Sprintf("已保存工作区目录快照(%d 项)→ 运行详情「构建目录」", len(snap.Entries)))
}
