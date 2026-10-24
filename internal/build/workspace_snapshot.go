package build

// workspace_snapshot.go —— 构建目录快照的采集点。
//
// 要解决的场景:产物路径写错。比如命令实际产出在 backend/target/x.jar,节点却声明成
// target/x.jar —— 过去只有日志里一行「产物路径未找到,跳过」,而工作区随即被 rm -rf,
// 用户既看不到"到底生成在哪",也看不到"那一层实际有什么"。这里在每个构建节点跑完那一刻留一份
// 目录清单(采集器见 internal/buildsnap),运行详情「构建目录」栏展示。
//
// 采集口径:**每个 script 节点执行完都采一份**,成功也采 —— 任何一次运行都回得到当时的目录,
// 不必等到出错才有得看。原因(reason)按"出问题优先"标:失败 > 声明产物未命中 > 成功。
// 镜像构建节点只在失败时采:它成功后的工作区与同一阶段 script 节点那份内容一致,再存一遍是纯重复。
//
// 生命周期跟着运行记录走,没有独立的过期窗口:表带 ON DELETE CASCADE,设置页「运行数据保留」
// 删掉那条 run 时快照连带消失。体积由采集上限把住(深度 6 / 总计 3000 条 / 单目录 200 条),
// 实测撞满上限的一份约 156KB、遍历 27~46ms。
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

// snapshotOnJobFailure 在构建节点失败时采一份(reason=failed,declared 传 nil:
// 失败那一刻产物收集根本没跑,没有判定可带)。
// 被取消的运行不采:现场是半成品,留着只会误导,且取消不该再有后台开销。
func (b *Builder) snapshotOnJobFailure(ctx context.Context, rep dagrun.StageReporter, r *run.Run, stageName string, jb pipeline.Job, workspace string) {
	if canceled(ctx) {
		return
	}
	b.saveWorkspaceSnapshot(ctx, rep, r, stageName, jb, workspace, buildsnap.ReasonFailed, nil)
}

// snapshotOnJobSuccess 在 script 节点执行成功后采一份:声明的产物路径有没命中的 → reason=artifact_miss,
// 连同逐条判定(missing / unmatched / rejected)与「最近存在的祖先目录 + 其下实际条目」一起记下 ——
// 这一栏才是"目录写错"的直接证据;全部命中或压根没声明 → reason=success。
func (b *Builder) snapshotOnJobSuccess(ctx context.Context, rep dagrun.StageReporter, r *run.Run, stageName string, jb pipeline.Job, workspace string, declared []buildsnap.Declared) {
	if canceled(ctx) {
		return
	}
	reason := buildsnap.ReasonSuccess
	if buildsnap.HasMiss(declared) {
		reason = buildsnap.ReasonArtifactMiss
	}
	b.saveWorkspaceSnapshot(ctx, rep, r, stageName, jb, workspace, reason, declared)
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
	// 只在"真出了问题"那两份里指路:成功那份每个节点都会采,往构建日志塞一行就成了纯噪声。
	if reason == buildsnap.ReasonSuccess {
		return
	}
	_ = rep.Log(saveCtx, streamStdout, fmt.Sprintf("已保存工作区目录快照(%d 项)→ 运行详情「构建目录」", len(snap.Entries)))
}
