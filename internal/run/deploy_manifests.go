// deploy_manifests.go 是「这次发布到底把哪几份声明交给了集群」的存取(表 0060)。
//
// 为什么单独一张表、而不是在 deploy_targets 上加一列:一行结果是「一个目标」,而一次清单
// 发布是「若干份声明」。压成一列 JSON 就会有两件事做不了 —— 按对象查上一版(回滚要的正是它)
// 得先把全表读进内存,而库里存的正文再也受不到任何约束。
//
// 「哪一版算成功」不在这里再存一遍 status:join deploy_targets(run_id + server_id = cluster_id)
// 拿到的就是唯一真值。两处各记一份成功,迟早会互相说不合,而回滚读到哪一版必须只有一个答案。
package run

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ManifestDoc 是一份实际应用到集群的声明(与 deploy_manifests 一行一对应)。
// 它挂在 DeployTarget 上随结果同事务落库:结果与声明各有各的表,但绝不会出现「有结果没声明」。
type ManifestDoc struct {
	Kind      string // 资源类型(Deployment / Service / ConfigMap / ...)
	Namespace string // 实际生效的命名空间(清单写的,或集群登记的默认值补的)
	Name      string
	Ordinal   int    // 文件里的第几份文档(1 基;应用与重放都按此顺序)
	Body      string // 规范化的 apply 正文;**绝无凭据**(kind: Secret 在解析期就被拒)
}

// saveDeployManifests 把一个目标的清单正文写进 deploy_manifests(在调用方的事务里)。
//
// 先按 (run_id, cluster_id) 清旧:重复部署同一个 run 应当**替换**上一轮发出的声明,与
// deploy_targets 的整批重写同一套语义 —— 否则「上一版」会在同一 run 里读到两份互相矛盾的历史。
func saveDeployManifests(ctx context.Context, tx execer, runID string, t DeployTarget) error {
	if len(t.Manifests) == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM deploy_manifests WHERE run_id = ? AND cluster_id = ?`, runID, t.ServerID); err != nil {
		return fmt.Errorf("run: clear deploy manifests: %w", err)
	}
	now := t.StartedAt.UTC().Format(time.RFC3339)
	for _, m := range t.Manifests {
		ordinal := m.Ordinal
		if ordinal <= 0 {
			ordinal = 1
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO deploy_manifests (id, run_id, cluster_id, kind, namespace, name, doc_ordinal, body, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			uuid.NewString(), runID, t.ServerID, m.Kind, m.Namespace, m.Name, ordinal, m.Body, now,
		); err != nil {
			if isForeignKeyErr(err) {
				return ErrNotFound
			}
			return fmt.Errorf("run: insert deploy manifest: %w", err)
		}
	}
	return nil
}

// execer 让「事务内写入」能被复用而不必把 *sql.Tx 透出到签名外。
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// LastAppliedManifest 取「同一集群、同一对象,上一版成功发布时实际应用的正文」—— 回滚回填的就是它。
//
// 只看 status='success' 那一版:失败的发布并没把东西发成,拿它的正文当「上一版」会让回滚
// 越滚越坏。excludeRunID 传当前 run —— 重发/重试时同一 run 可能已留着本批的行。
// 没有可用历史时返回 (nil, nil):「首次发布无上一版可回滚」是常态,调用方得分得清这两种情况。
func (s *service) LastAppliedManifest(ctx context.Context, clusterID, kind, namespace, name, excludeRunID string) (*ManifestDoc, error) {
	var m ManifestDoc
	var ordinal int
	err := s.db.QueryRowContext(ctx,
		`SELECT m.kind, m.namespace, m.name, m.doc_ordinal, m.body
		 FROM deploy_manifests m
		 JOIN deploy_targets t ON t.run_id = m.run_id AND t.server_id = m.cluster_id
		 WHERE m.cluster_id = ? AND m.kind = ? AND m.namespace = ? AND m.name = ?
		   AND t.status = ? AND m.run_id <> ?
		 ORDER BY m.created_at DESC, m.doc_ordinal DESC
		 LIMIT 1`,
		clusterID, kind, namespace, name, TargetSuccess, excludeRunID,
	).Scan(&m.Kind, &m.Namespace, &m.Name, &ordinal, &m.Body)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("run: load last applied manifest: %w", err)
	}
	m.Ordinal = ordinal
	return &m, nil
}
