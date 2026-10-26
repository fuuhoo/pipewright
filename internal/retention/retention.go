// Package retention 实现运行数据的保留策略与定期清理(防止 runs/run_logs/run_steps/
// artifacts/构建目录快照无限增长撑爆磁盘)。
//
// 策略(全局单例 retention_config,三个维度都可单独设 0=不限):
//   - Enabled:总开关(0082 起对未配置过的实例默认打开)。
//   - KeepGlobal:全局保留最近 N 条**终态**运行,不分项目(0=不限)。
//   - KeepPerProject:每项目保留最近 N 条终态运行 —— 旧维度,列与判定都还在,新界面不再暴露(0=不限)。
//   - MaxAgeDays:创建时间早于 N 天的**终态**运行视为越界(0=不限)。
//
// **保留是并集**:一条运行只要满足任意一个已配置维度(还在条数之内、或还不算老)就留下,
// 只有把所有已配置维度都越界才删。所以「365 天 + 1000 条」的含义是:一年内的全留,
// 一年外的也只留最近 1000 条 —— 反过来(任一越界即删)会把刚跑完的构建也裁掉。
//
// 安全铁律:只删终态运行(success/failed);进行中(queued/running/waiting_approval)绝不动。
// 删 run 时——deploy_targets 是唯一未配 ON DELETE CASCADE 的子表,显式先删;其余子表
// (run_steps/run_logs/run_artifacts/反馈/审批/测试报告/晋级/构建目录快照/通知台账)经外键级联连带删除。
// 通知台账里不挂在运行上的那部分(证书/异常这类全局事件)级联够不着,由 PruneDeliveries 用同一套维度裁剪。
// 清理为 best-effort:出错记录并返回,绝不影响在跑的运行。
package retention

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Config 是全局保留策略。
type Config struct {
	Enabled        bool
	KeepGlobal     int
	KeepPerProject int
	MaxAgeDays     int
}

// Service 读写保留配置并执行清理(经参数化 SQL 触库)。
type Service struct {
	db *sql.DB
}

// NewService 构造保留服务。
func NewService(db *sql.DB) *Service { return &Service{db: db} }

// GetConfig 读取全局保留配置(单行 id=1;缺行返回零值不报错)。
func (s *Service) GetConfig(ctx context.Context) (Config, error) {
	var (
		enabled, global, keep, age int
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT enabled, keep_global, keep_per_project, max_age_days FROM retention_config WHERE id = 1`,
	).Scan(&enabled, &global, &keep, &age)
	if err == sql.ErrNoRows {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("retention: get config: %w", err)
	}
	return Config{Enabled: enabled != 0, KeepGlobal: global, KeepPerProject: keep, MaxAgeDays: age}, nil
}

// SetConfig 持久化保留配置(负值归一为 0;upsert 单行)。
func (s *Service) SetConfig(ctx context.Context, c Config) error {
	if c.KeepGlobal < 0 {
		c.KeepGlobal = 0
	}
	if c.KeepPerProject < 0 {
		c.KeepPerProject = 0
	}
	if c.MaxAgeDays < 0 {
		c.MaxAgeDays = 0
	}
	enabled := 0
	if c.Enabled {
		enabled = 1
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx,
		`UPDATE retention_config SET enabled = ?, keep_global = ?, keep_per_project = ?, max_age_days = ?, updated_at = ? WHERE id = 1`,
		enabled, c.KeepGlobal, c.KeepPerProject, c.MaxAgeDays, now)
	if err != nil {
		return fmt.Errorf("retention: set config: %w", err)
	}
	// 行不存在(迁移种子未跑)→ 兜底插入。
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO retention_config (id, enabled, keep_global, keep_per_project, max_age_days, updated_at) VALUES (1, ?, ?, ?, ?, ?)`,
			enabled, c.KeepGlobal, c.KeepPerProject, c.MaxAgeDays, now); err != nil {
			return fmt.Errorf("retention: insert config: %w", err)
		}
	}
	return nil
}

// Prune 按当前策略裁剪终态运行,返回删除条数。未启用或三个维度全为「不限」时返回 0,不删。
func (s *Service) Prune(ctx context.Context, now time.Time) (int, error) {
	cfg, err := s.GetConfig(ctx)
	if err != nil {
		return 0, err
	}
	if !cfg.Enabled || (cfg.KeepGlobal <= 0 && cfg.KeepPerProject <= 0 && cfg.MaxAgeDays <= 0) {
		return 0, nil
	}

	ids, err := s.prunableIDs(ctx, cfg, now)
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	if err := s.deleteRuns(ctx, ids); err != nil {
		return 0, err
	}
	return len(ids), nil
}

// prunableIDs 算出应删除的终态运行 id。
//
// **并集保留**:一条运行只在「所有已配置维度都越界」时才删 —— 满足任意一个维度(还在最近 N 条
// 之内、或还没到龄)就留下。只配一个维度时,那个维度单独说了算(否则并集永远不触发,策略形同关闭)。
// 排名在 Go 端算,避免依赖各方言的窗口函数,逻辑可单测。
func (s *Service) prunableIDs(ctx context.Context, cfg Config, now time.Time) ([]string, error) {
	// 按时间倒序扫一趟即可同时拿到两种排名:全局排名是扫过的条数,项目内排名是该项目的计数。
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, project_id, created_at FROM pipeline_runs
		 WHERE status IN ('success','failed')
		 ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("retention: list terminal runs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	cutoff := ""
	if cfg.MaxAgeDays > 0 {
		cutoff = now.UTC().AddDate(0, 0, -cfg.MaxAgeDays).Format(time.RFC3339)
	}

	var toDelete []string
	seenTotal := 0
	perProject := map[string]int{}
	for rows.Next() {
		var id, projectID, createdAt string
		if err := rows.Scan(&id, &projectID, &createdAt); err != nil {
			return nil, fmt.Errorf("retention: scan run: %w", err)
		}
		seenTotal++
		perProject[projectID]++

		configured, exceeded := 0, 0
		if cfg.KeepGlobal > 0 {
			configured++
			if seenTotal > cfg.KeepGlobal {
				exceeded++
			}
		}
		if cfg.KeepPerProject > 0 {
			configured++
			if perProject[projectID] > cfg.KeepPerProject {
				exceeded++
			}
		}
		// created_at 为 UTC RFC3339,可按字符串比较年龄。
		if cfg.MaxAgeDays > 0 {
			configured++
			if cutoff != "" && createdAt < cutoff {
				exceeded++
			}
		}
		if configured > 0 && exceeded == configured {
			toDelete = append(toDelete, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("retention: iterate runs: %w", err)
	}
	return toDelete, nil
}

// 非级联子表:这些表带 run_id 但**未配 ON DELETE CASCADE**(run_logs 无外键、deploy_targets
// 为 RESTRICT),删 run 前须显式清。run_logs 是最大增长源(日志文本),尤其重要。其余子表
// (run_steps/run_artifacts/反馈/审批/测试报告/晋级历史)均级联,随删 run 自动清除。
// 注:webhook_deliveries 为去重账本(删之可能导致重投被重复处理),不在此清。
var nonCascadeChildTables = []string{"run_logs", "deploy_targets"}

// deleteRuns 分批在事务内删除运行:先删非级联子表,再删 run(级联其余子表)。
func (s *Service) deleteRuns(ctx context.Context, ids []string) error {
	const batch = 500
	for start := 0; start < len(ids); start += batch {
		end := start + batch
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		if err := s.deleteChunk(ctx, chunk); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) deleteChunk(ctx context.Context, ids []string) error {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("retention: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, tbl := range nonCascadeChildTables {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM `+tbl+` WHERE run_id IN (`+placeholders+`)`, args...); err != nil {
			return fmt.Errorf("retention: delete %s: %w", tbl, err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM pipeline_runs WHERE id IN (`+placeholders+`)`, args...); err != nil {
		return fmt.Errorf("retention: delete runs: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("retention: commit: %w", err)
	}
	return nil
}

// PruneDeliveries 裁剪通知投递台账中「不挂在现存运行上」的那部分(证书到期/续期、指标异常这类全局事件),
// 返回删除行数。挂在运行上的台账行不在这里管:外键级联随删运行一起清掉,而且只要那条运行还在,
// 它的通知记录就该跟着它 —— 否则运行详情页里的「通知」会凭空变空。
//
// 维度沿用运行保留策略的 KeepGlobal 与 MaxAgeDays,口径与 Prune 一致(并集保留:两个维度都越界才删)。
// 两者都没配(0=不限)或策略关闭 → 返回 0,不删。
func (s *Service) PruneDeliveries(ctx context.Context, now time.Time) (int, error) {
	cfg, err := s.GetConfig(ctx)
	if err != nil {
		return 0, err
	}
	if !cfg.Enabled || (cfg.KeepGlobal <= 0 && cfg.MaxAgeDays <= 0) {
		return 0, nil
	}

	ids, err := s.prunableDeliveryIDs(ctx, cfg, now)
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	if err := s.deleteDeliveries(ctx, ids); err != nil {
		return 0, err
	}
	return len(ids), nil
}

// prunableDeliveryIDs 算出应删除的无主台账行 id(倒序扫一趟,排名即扫过的条数;逻辑同 prunableIDs)。
func (s *Service) prunableDeliveryIDs(ctx context.Context, cfg Config, now time.Time) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT nd.id, nd.created_at FROM notification_deliveries nd
		 WHERE (nd.run_id IS NULL OR NOT EXISTS (SELECT 1 FROM pipeline_runs pr WHERE pr.id = nd.run_id))
		 ORDER BY nd.created_at DESC, nd.id DESC`)
	if err != nil {
		return nil, fmt.Errorf("retention: list deliveries: %w", err)
	}
	defer func() { _ = rows.Close() }()

	cutoff := ""
	if cfg.MaxAgeDays > 0 {
		cutoff = now.UTC().AddDate(0, 0, -cfg.MaxAgeDays).Format(time.RFC3339)
	}

	var toDelete []string
	seen := 0
	for rows.Next() {
		var id, createdAt string
		if err := rows.Scan(&id, &createdAt); err != nil {
			return nil, fmt.Errorf("retention: scan delivery: %w", err)
		}
		seen++

		configured, exceeded := 0, 0
		if cfg.KeepGlobal > 0 {
			configured++
			if seen > cfg.KeepGlobal {
				exceeded++
			}
		}
		// created_at 为 UTC RFC3339,可按字符串比较年龄。
		if cfg.MaxAgeDays > 0 {
			configured++
			if cutoff != "" && createdAt < cutoff {
				exceeded++
			}
		}
		if exceeded == configured {
			toDelete = append(toDelete, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("retention: iterate deliveries: %w", err)
	}
	return toDelete, nil
}

// deleteDeliveries 分批删除台账行(与 deleteRuns 同样的批量上界)。
func (s *Service) deleteDeliveries(ctx context.Context, ids []string) error {
	const batch = 500
	for start := 0; start < len(ids); start += batch {
		end := start + batch
		if end > len(ids) {
			end = len(ids)
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", end-start), ",")
		args := make([]any, end-start)
		for i, id := range ids[start:end] {
			args[i] = id
		}
		if _, err := s.db.ExecContext(ctx,
			`DELETE FROM notification_deliveries WHERE id IN (`+placeholders+`)`, args...); err != nil {
			return fmt.Errorf("retention: delete deliveries: %w", err)
		}
	}
	return nil
}
