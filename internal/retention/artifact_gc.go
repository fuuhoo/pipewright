// artifact_gc.go 回收制品库里的孤儿字节。
//
// 为什么归在保留清理器之后:orphans 正是「裁运行」这一步造出来的 —— run_artifacts 行随 run
// 被外键级联删掉,而产物字节在磁盘库 <artifacts>/<hash 前缀>/<hash> 下没人动(内容寻址库只会
// 去重,不会失效)。同理,删项目连带删运行也造孤儿。所以每轮保留扫描之后立刻问一次库里
// 「还剩哪些句柄有人认」,把盘上没人认、且落盘超过宽限期的字节删掉。
//
// 只认 run_artifacts.reference:那是制品库句柄在库里的唯一落点(见 internal/build 的
// artifact_persist.go);image 类产物存的是 repo:tag,长度不是 64,天然不会误伤。
//
// 宽限期(默认 1 小时)不是保险丝而是必需品:构建先 Put 字节、后写产物行,中间那段窗口里
// 文件确实零引用。见 artifactstore.Put —— 去重命中时也会把 mtime 推到同一理由。
//
// best-effort:出错只记日志,绝不影响在跑的运行(与保留清理本身同口径)。
package retention

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/fuuhoo/pipewright/internal/artifactstore"
)

// DefaultArtifactGrace 是孤儿节字的宽限期:落盘不足这个时间的文件一律不删。
const DefaultArtifactGrace = time.Hour

// ArtifactGC 按库里的活引用回收制品库孤儿字节。store 为 nil(制品库不可用)时不构造。
type ArtifactGC struct {
	db    *sql.DB
	store *artifactstore.Store
	grace time.Duration
}

// NewArtifactGC 构造回收器。grace<=0 用 DefaultArtifactGrace。
func NewArtifactGC(db *sql.DB, store *artifactstore.Store, grace time.Duration) *ArtifactGC {
	if grace <= 0 {
		grace = DefaultArtifactGrace
	}
	return &ArtifactGC{db: db, store: store, grace: grace}
}

// Run 一轮回收:读库里的活句柄 → 交给磁盘库删没人认的字节。
func (g *ArtifactGC) Run(ctx context.Context, now time.Time) (artifactstore.SweepResult, error) {
	live, err := liveReferences(ctx, g.db)
	if err != nil {
		return artifactstore.SweepResult{}, err
	}
	return g.store.Sweep(live, now, g.grace)
}

// liveReferences 取全部产物行的 reference 作为「仍被引用」集合。
// 占位串(文件名 / repo:tag)也照收:Sweep 只按文件名比对,多几个对不上的键无害,
// 反过来(漏收真句柄)才会删错东西 —— 所以这里不做任何过滤。
func liveReferences(ctx context.Context, db *sql.DB) (map[string]struct{}, error) {
	rows, err := db.QueryContext(ctx, `SELECT reference FROM run_artifacts`)
	if err != nil {
		return nil, fmt.Errorf("retention: list artifact references: %w", err)
	}
	defer func() { _ = rows.Close() }()

	live := map[string]struct{}{}
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return nil, fmt.Errorf("retention: scan artifact reference: %w", err)
		}
		live[ref] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("retention: iterate artifact references: %w", err)
	}
	return live, nil
}
