package deploy

// stage_batch.go 是「首批后暂停」在**流水线部署节点**上的两条后腿。
//
// 与独立部署路径(ContinueDeploy / AbortDeploy)的区别只有一件:那里的 run 就是这次部署,
// 续发完要把终态重算回 run;这里的 run 是一条还在跑的流水线,部署节点之上还有调度器,
// 节点成没成由它判 —— 所以这两条只回写目标行(Upsert,不整批重写,免得抹掉同级其它部署节点的行),
// 绝不碰 run 终态。
//
// pending 的落点由调用方(DAG 层)把它上一轮拿到的结果原样带回来,而不是这里回查数据库:
// 同一阶段里并行的部署节点各自会重写 deploy_targets,把「谁还在等人确认」寄托在那张表上,
// 就等于把暂停的成败系在邻居什么时候写完。

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fuuhoo/pipewright/internal/run"
	"github.com/fuuhoo/pipewright/internal/target"
)

// ResumeStageTargets 见 Service 注释:续发暂停中的落点,滚动铺完并回写目标行。
func (s *service) ResumeStageTargets(ctx context.Context, runID string, serverIDs []string, cfg map[string]string) ([]TargetResult, error) {
	if len(serverIDs) == 0 {
		return nil, ErrNoPendingTargets
	}
	artifact, err := s.pickStageArtifactFor(ctx, runID, cfg)
	if err != nil {
		return nil, err
	}
	servers := make([]*target.Server, 0, len(serverIDs))
	for _, sid := range serverIDs {
		srv, gerr := s.targets.Get(ctx, sid)
		if gerr != nil {
			if errors.Is(gerr, target.ErrNotFound) {
				return nil, ErrServerNotFound
			}
			return nil, gerr
		}
		servers = append(servers, srv)
	}
	// 首批已经验证过这一版,其余直接铺(rolling):再套一层分批只会让人再确认一次。
	results := s.deployFanout(ctx, servers, *artifact, cfg, HealthCheckFromConfig(cfg))
	if err := s.upsertStageTargets(ctx, runID, results); err != nil {
		return nil, err
	}
	return results, nil
}

// AbortStageTargets 见 Service 注释:把暂停中的落点标成「已中止」,不触碰已发批次。
func (s *service) AbortStageTargets(ctx context.Context, runID string, serverIDs []string) ([]TargetResult, error) {
	if len(serverIDs) == 0 {
		return nil, ErrNoPendingTargets
	}
	names := make(map[string]string, len(serverIDs))
	for _, sid := range serverIDs {
		srv, gerr := s.targets.Get(ctx, sid)
		if gerr != nil {
			if errors.Is(gerr, target.ErrNotFound) {
				// 机器已从台账消失:没有要中止的进程,记一行「未部署」即可,不该让中止本身失败。
				names[sid] = sid + "(已从主机台账删除)"
				continue
			}
			return nil, gerr
		}
		names[sid] = srv.Name
	}
	now := time.Now().UTC()
	results := make([]TargetResult, 0, len(serverIDs))
	for _, sid := range serverIDs {
		results = append(results, TargetResult{
			ServerID: sid, ServerName: names[sid],
			Status: run.TargetFailed, Message: "已中止分批部署:本机未部署,保留旧版本",
			StartedAt: now, FinishedAt: &now,
		})
	}
	if err := s.upsertStageTargets(ctx, runID, results); err != nil {
		return nil, err
	}
	return results, nil
}

// upsertStageTargets 按 (run, server) 覆盖这批目标的行 —— 整批重写会抹掉同级其它部署节点的结果。
func (s *service) upsertStageTargets(ctx context.Context, runID string, results []TargetResult) error {
	dts := make([]run.DeployTarget, 0, len(results))
	for _, r := range results {
		dts = append(dts, deployTargetOf(runID, r))
	}
	if err := s.runs.UpsertDeployTargets(ctx, runID, dts); err != nil {
		return fmt.Errorf("deploy: 回写分批目标失败: %w", err)
	}
	return nil
}
