package notify

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/fuuhoo/pipewright/internal/access"
	"github.com/google/uuid"
)

// 投递台账的三档结论(冻结契约;DB 存小写串;JSON 同名):
//   - DeliveryOK      渠道收下并发出。
//   - DeliveryFailed  投递报错(渠道已删、网络失败、对方返回非 2xx…)。
//   - DeliverySkipped 路由配了,但渠道处于停用状态 —— 平台按既有语义「优雅跳过」,不发也不报错。
const (
	DeliveryOK      = "ok"
	DeliveryFailed  = "failed"
	DeliverySkipped = "skipped"
)

// ErrInvalidDeliveryStatus 表示台账列表的 status 筛选不是三档之一。
var ErrInvalidDeliveryStatus = errors.New("notify: invalid delivery status")

// DeliveryService 定义投递台账的读侧(写侧由事件路由在 dispatch 时自己落一行,不对外暴露)。
//
// 台账覆盖的是**路由投递**:某个事件按「事件 → 渠道」路由发出去的那些通知。流水线里的「通知」
// 节点走的是 SendVia(直发单个渠道,不带事件语义),它的结果本来就打进那条运行的日志,不进台账。
type DeliveryService interface {
	// ListDeliveries 分页返回台账(按 created_at 倒序;筛选事件/项目/运行/结论,并随分组可见性收敛)。
	ListDeliveries(ctx context.Context, f DeliveryFilter) (*DeliveryList, error)
}

// Delivery 是一次「事件 → 渠道」投递的台账行(对外视图)。
type Delivery struct {
	ID        string
	Event     string
	ProjectID string // 空 = 全局事件(证书到期/续期、指标异常),不属于任何项目
	// ProjectName 是查询时 JOIN 出的项目名(仅列表回显用;库里没有这一列)。
	ProjectName string
	RunID       string // 空 = 本次投递不挂在某次运行上
	ChannelID   string
	// ChannelName 是投递当时解析到的渠道名(冗余存一份):渠道改名或事后被删,历史仍可辨。
	ChannelName string
	Status      string
	Title       string
	// Body 是渲染后的通知正文(出站内容,构造时已脱敏),按 maxDeliveryBodyRunes 截断。
	// 结论为 failed/skipped 的行也存:「本该发出去的是什么」正是排查要看的那一半。
	Body string
	// ErrorText 是人读失败原因(绝无凭据明文,见 router 侧的双层脱敏);成功/跳过为空。
	ErrorText string
	CreatedAt time.Time
}

// DeliveryFilter 是台账列表的筛选与分页条件。
type DeliveryFilter struct {
	Event     string
	ProjectID string
	RunID     string
	Status    string
	Page      int
	PageSize  int
	// VisibleGroups 是分组可见性:项目维度的投递随所属项目收敛;全局事件(project_id 为空)不受此限。
	VisibleGroups access.ListFilter
}

// DeliveryList 是台账列表结果(形状对齐 run.ListResult)。
type DeliveryList struct {
	Items []Delivery
	Page  int
	Total int
}

// 台账列表分页参数(与 run 列表同档,防滥用)。
const (
	defaultDeliveryPageSize = 20
	maxDeliveryPageSize     = 100
	maxDeliveryPage         = 100000
)

// maxDeliveryBodyRunes 是台账里正文的存留上限(按字符,不按字节)。
//
// 正文是渲染出来的短文案,但这个上限是唯一的生长护栏:发版汇总这类带参数的通知可能很长,
// 而「点开看全文」要的是排查线索,不是逐字存档。截断只可能吃尾,不可能吃出乱码。
const maxDeliveryBodyRunes = 4000

// truncateRunes 按字符截断 s 到 n 个字符(中文安全:切在多字节中间会产出乱码)。
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// validDeliveryStatus 报告 status 是否为台账三档之一(空串=不筛选,合法)。
func validDeliveryStatus(s string) bool {
	switch s {
	case "", DeliveryOK, DeliveryFailed, DeliverySkipped:
		return true
	default:
		return false
	}
}

// DeliveryStatuses 返回台账结论枚举的稳定顺序(供前端筛选下拉)。
func DeliveryStatuses() []string {
	return []string{DeliveryOK, DeliveryFailed, DeliverySkipped}
}

// dispatch 向单个渠道投递一次事件载荷,并把结论顺手落成台账。
//
// 这是「路由发通知」的唯一出口:三档结论(成功 / 失败 / 渠道停用被跳过)在这里一次判清,
// 既回传给路由循环续发其余(best-effort 由调用方掌握),又记下当时发生的事。
func (s *service) dispatch(ctx context.Context, d Delivery, payload Payload) error {
	d.Body = truncateRunes(payload.Body, maxDeliveryBodyRunes)
	ch, sealed, err := s.load(ctx, d.ChannelID)
	switch {
	case err != nil:
		d.Status, d.ErrorText = DeliveryFailed, deliveryErrorText(err)
	case !ch.Enabled:
		// 渠道停用:既有语义是「优雅跳过、不发不报错」,台账记 skipped 而不是 ok ——
		// 否则页面上会出现一条「发送成功」的通知,而它其实一个字都没发出去。
		d.Status, d.ChannelName = DeliverySkipped, ch.Name
	default:
		d.ChannelName = ch.Name
		if derr := s.deliver(ctx, ch, sealed, payload); derr != nil {
			d.Status, d.ErrorText, err = DeliveryFailed, deliveryErrorText(derr), derr
		} else {
			d.Status = DeliveryOK
		}
	}
	s.recordDelivery(ctx, d)
	return err
}

// deliveryErrorText 把投递错误收成台账里那句人读原因。
// 渠道不存在在这条线上是常态(先配路由、后删渠道),领域错误串不该原样甩给用户。
func deliveryErrorText(err error) string {
	if errors.Is(err, ErrNotFound) {
		return "渠道已不存在(可能已被删除)"
	}
	return err.Error()
}

// recordDelivery 尽力写一条台账:落库失败只记日志,绝不回传错误。
//
// 这条线是「发通知」的副作用,不是发送的一部分 —— 台账写不进(表被锁、磁盘满)不能把一次已经
// 发成功的通知报成失败,更不能拦住其余渠道。所以刻意不返回 error。
//
// 写入脱离调用方 ctx:慢渠道把 30s 超时吃满时,那条「失败」恰恰是最该留下的记录。
func (s *service) recordDelivery(ctx context.Context, d Delivery) {
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now().UTC()
	}
	if d.ID == "" {
		d.ID = uuid.NewString()
	}
	var projectID, runID any
	if p := strings.TrimSpace(d.ProjectID); p != "" {
		projectID = p
	}
	if r := strings.TrimSpace(d.RunID); r != "" {
		runID = r
	}
	_, err := s.db.ExecContext(context.WithoutCancel(ctx),
		`INSERT INTO notification_deliveries
		 (id, event, project_id, run_id, channel_id, channel_name, status, title, body, error_text, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.Event, projectID, runID, d.ChannelID, d.ChannelName, d.Status,
		d.Title, d.Body, d.ErrorText, d.CreatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		log.Printf("[notify] 台账写入失败(event=%q,channel=%s,status=%s):%v", d.Event, d.ChannelID, d.Status, err)
	}
}

// ListDeliveries 实现 DeliveryService.ListDeliveries:分页 + 筛选 + 分组收敛,按时间倒序。
func (s *service) ListDeliveries(ctx context.Context, f DeliveryFilter) (*DeliveryList, error) {
	if !validDeliveryStatus(f.Status) {
		return nil, ErrInvalidDeliveryStatus
	}
	// 令牌分组范围与本人可见集无交集 → 一条都不给(fail closed,含全局事件)。
	if f.VisibleGroups.DenyAll {
		return &DeliveryList{Items: []Delivery{}, Page: 1, Total: 0}, nil
	}

	page := clampInt(f.Page, 1, maxDeliveryPage)
	size := clampInt(f.PageSize, defaultDeliveryPageSize, maxDeliveryPageSize)

	where := []string{}
	args := []any{}
	if ev := strings.TrimSpace(f.Event); ev != "" {
		where = append(where, "nd.event = ?")
		args = append(args, ev)
	}
	if pid := strings.TrimSpace(f.ProjectID); pid != "" {
		where = append(where, "nd.project_id = ?")
		args = append(args, pid)
	}
	if rid := strings.TrimSpace(f.RunID); rid != "" {
		where = append(where, "nd.run_id = ?")
		args = append(args, rid)
	}
	if st := strings.TrimSpace(f.Status); st != "" {
		where = append(where, "nd.status = ?")
		args = append(args, st)
	}
	// 分组收敛只作用于「挂在项目上」的那部分;project_id 为 NULL 的全局事件(证书/异常)本就不属于
	// 任何分组,不因此消失。LEFT JOIN 后条件写成「非项目投递 OR 项目在可见组内」。
	if gcond, gargs := f.VisibleGroups.Clause("p.group_id"); gcond != "" {
		where = append(where, "(nd.project_id IS NULL OR "+gcond+")")
		args = append(args, gargs...)
	}
	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}

	const from = ` FROM notification_deliveries nd
	 LEFT JOIN projects p ON p.id = nd.project_id`

	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(1)"+from+clause, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("notify: count deliveries: %w", err)
	}

	listArgs := append(append([]any{}, args...), size, (page-1)*size)
	rows, err := s.db.QueryContext(ctx,
		`SELECT nd.id, nd.event, nd.project_id, nd.run_id, nd.channel_id, nd.channel_name,
		        nd.status, nd.title, nd.body, nd.error_text, nd.created_at, COALESCE(p.name, '')`+from+clause+`
		 ORDER BY nd.created_at DESC, nd.id DESC
		 LIMIT ? OFFSET ?`, listArgs...)
	if err != nil {
		return nil, fmt.Errorf("notify: list deliveries: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]Delivery, 0, size)
	for rows.Next() {
		var (
			d           Delivery
			projectID   sql.NullString
			runID       sql.NullString
			projectName string
			createdStr  string
		)
		if err := rows.Scan(&d.ID, &d.Event, &projectID, &runID, &d.ChannelID, &d.ChannelName,
			&d.Status, &d.Title, &d.Body, &d.ErrorText, &createdStr, &projectName); err != nil {
			return nil, fmt.Errorf("notify: scan delivery: %w", err)
		}
		if projectID.Valid {
			d.ProjectID = projectID.String
		}
		if runID.Valid {
			d.RunID = runID.String
		}
		d.ProjectName = projectName
		created, err := time.Parse(time.RFC3339, createdStr)
		if err != nil {
			return nil, fmt.Errorf("notify: parse delivery created_at: %w", err)
		}
		d.CreatedAt = created
		items = append(items, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("notify: iterate deliveries: %w", err)
	}
	return &DeliveryList{Items: items, Page: page, Total: total}, nil
}

// clampInt 把 v 归一到 [lo, hi]:<=0 用 dflt(仅当 dflt 在区间内),再钳到区间。
func clampInt(v, dflt, hi int) int {
	if v < 1 {
		v = dflt
	}
	if v > hi {
		v = hi
	}
	return v
}
