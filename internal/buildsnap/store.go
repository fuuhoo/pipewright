// store.go 是快照的持久化:写入、按 run 读取、按 TTL 删除。
//
// 表 run_workspace_snapshots(迁移 0081)带 ON DELETE CASCADE,所以保留清理器删 run 时
// 快照连带消失;本文件的 Prune 只负责另一件事 —— 快照比 run 短得多(默认 2 小时),
// 因为它只是"刚才那次失败"的现场,不是需要长期回看的数据。
package buildsnap

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Store 是快照仓储(main 注入给构建器与 HTTP 层;nil = 不采集、不查询)。
type Store struct {
	db     *sql.DB
	limits Limits
}

// New 构造仓储。limits 零值按默认上限处理。
func New(db *sql.DB, limits Limits) *Store {
	return &Store{db: db, limits: limits.withDefaults()}
}

// CollectLimits 回读生效的采集上限 —— 采集器与仓储共用同一份口径(main 只配一次)。
// nil 仓储返回零值,Collect 会退化为默认上限。
func (s *Store) CollectLimits() Limits {
	if s == nil {
		return Limits{}
	}
	return s.limits
}

// Save 落一条快照。快照只是排障辅助,任何失败都由调用方记一行日志吞掉,绝不让构建失败。
func (s *Store) Save(ctx context.Context, snap Snapshot) error {
	if s == nil || s.db == nil {
		return nil
	}
	declaredJSON, err := json.Marshal(snap.Declared)
	if err != nil {
		return err
	}
	entriesJSON, err := json.Marshal(snap.Entries)
	if err != nil {
		return err
	}
	if snap.ID == "" {
		snap.ID = uuid.NewString()
	}
	if snap.CreatedAt.IsZero() {
		snap.CreatedAt = time.Now().UTC()
	}
	truncated := 0
	if snap.Truncated {
		truncated = 1
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO run_workspace_snapshots
		   (id, run_id, stage, job_id, job_name, reason, declared_json, entries_json, entry_count, truncated, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		snap.ID, snap.RunID, snap.Stage, snap.JobID, snap.JobName, snap.Reason,
		string(declaredJSON), string(entriesJSON), len(snap.Entries), truncated,
		snap.CreatedAt.UTC().Format(time.RFC3339),
	)
	return err
}

// ListByRun 取某次运行的全部快照(按 created_at 升序,id 破同秒并列 —— 与 run_artifacts 同口径)。
// 无快照 → 空切片;run 不存在不报错(HTTP 层据 run 存在性决定 404)。
func (s *Store) ListByRun(ctx context.Context, runID string) ([]Snapshot, error) {
	if s == nil || s.db == nil {
		return []Snapshot{}, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, run_id, stage, job_id, job_name, reason, declared_json, entries_json, truncated, created_at
		 FROM run_workspace_snapshots WHERE run_id = ? ORDER BY created_at ASC, id ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Snapshot
	for rows.Next() {
		var (
			snap                    Snapshot
			declaredRaw, entriesRaw string
			truncated               int
			createdRaw              string
		)
		if serr := rows.Scan(&snap.ID, &snap.RunID, &snap.Stage, &snap.JobID, &snap.JobName,
			&snap.Reason, &declaredRaw, &entriesRaw, &truncated, &createdRaw); serr != nil {
			return nil, serr
		}
		snap.Truncated = truncated != 0
		snap.Declared = decodeJSON[Declared](declaredRaw)
		snap.Entries = decodeJSON[Entry](entriesRaw)
		if t, terr := time.Parse(time.RFC3339, createdRaw); terr == nil {
			snap.CreatedAt = t.UTC()
		}
		out = append(out, snap)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, rerr
	}
	if out == nil {
		out = []Snapshot{}
	}
	return out, nil
}

// Prune 删除 created_at 早于 now-ttl 的快照,返回删除条数。ttl<=0 视为未启用,不删。
func (s *Store) Prune(ctx context.Context, now time.Time, ttl time.Duration) (int, error) {
	if s == nil || s.db == nil || ttl <= 0 {
		return 0, nil
	}
	cutoff := now.UTC().Add(-ttl).Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx, `DELETE FROM run_workspace_snapshots WHERE created_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// decodeJSON 解一个 JSON 数组列。解不动就退化为空切片:快照是**可丢的排障辅助数据**,
// 一行脏数据不该让整个运行详情 500(与 run_artifacts 对 metadata 的宽容口径一致)。
func decodeJSON[T any](raw string) []T {
	out := []T{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	return out
}
