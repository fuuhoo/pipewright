// Package serverpref 保存容器页「主机卡片」的每用户视图偏好:置顶了哪几台、手动拖成的顺序。
//
// 为什么按用户存一份,而不是在 servers 表上加 pinned / sort_order 两列:顺序与置顶是**看的人**的
// 习惯,不是主机的属性 —— 一台机器不该因为某个人把它拖到后面,对管理员也跟着靠后。
//
// 存的全是服务器 id(见迁移 0093 的注释):主机改名不影响,主机被删后残留的 id 在读取时忽略,
// 所以不需要任何后台清理任务。
package serverpref

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fuuhoo/pipewright/internal/store"
)

// maxIDs 是一份偏好最多记多少个 id。界面一屏远到不了这个数,超出的只会是坏请求 ——
// 与其整体拒绝(让用户的一次拖拽白做),不如截断:读的一侧本来就忽略认不出的 id。
const maxIDs = 400

// idMaxLen 与 servers.id 同量级(uuid);比这更长就不当它是 id。
const idMaxLen = 64

// ErrNoUser 是缺少用户 id 时的入参错误:偏好没有归属就没有意义,写下去只会造出谁也读不到的行。
var ErrNoUser = errors.New("serverpref: 缺少用户 id")

// Prefs 是某人的主机视图偏好。两个列表都是确定性顺序,读出来永远非 nil(前端不必判空)。
//
//	Pinned: 置顶的主机 id,顺序即置顶区展示顺序
//	Order : 手动拖出来的完整顺序;不在其中的主机按后端默认顺序垫在后面
type Prefs struct {
	Pinned []string `json:"pinned"`
	Order  []string `json:"order"`
}

// Empty 返回一份干净的空偏好(而不是 nil 切片)。
func Empty() Prefs { return Prefs{Pinned: []string{}, Order: []string{}} }

// Service 读写每用户偏好(只经参数化 SQL 触库)。
type Service struct {
	db *sql.DB
}

// NewService 构造偏好服务。
func NewService(db *sql.DB) *Service { return &Service{db: db} }

// Get 读取某人的偏好。从没设置过(无行)回空偏好;库里那两列 JSON 坏了也回空偏好且不报错 ——
// 界面按默认顺序摆就行,不该因为一列脏数据让整页主机列表打不开。
func (s *Service) Get(ctx context.Context, userID string) (Prefs, error) {
	if userID == "" {
		return Empty(), nil
	}
	var pinnedRaw, orderRaw string
	err := s.db.QueryRowContext(ctx,
		`SELECT pinned_json, order_json FROM server_view_prefs WHERE user_id = ?`, userID,
	).Scan(&pinnedRaw, &orderRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return Empty(), nil
	}
	if err != nil {
		return Empty(), fmt.Errorf("serverpref: get: %w", err)
	}
	return Prefs{Pinned: decodeIDs(pinnedRaw), Order: decodeIDs(orderRaw)}, nil
}

// Save 整份覆盖(幂等;同一人两处同时保存以最后一写为准),返回归一化后真正存下来的那份。
// 空串、重复、超长 id 在这里就清掉,库里永远是干净列表,前端拿到回包也能立刻和界面对齐。
func (s *Service) Save(ctx context.Context, userID string, p Prefs) (Prefs, error) {
	if userID == "" {
		return Empty(), ErrNoUser
	}
	clean := Prefs{Pinned: normalizeIDs(p.Pinned), Order: normalizeIDs(p.Order)}
	pinned, err := json.Marshal(clean.Pinned)
	if err != nil {
		return Empty(), fmt.Errorf("serverpref: marshal pinned: %w", err)
	}
	order, err := json.Marshal(clean.Order)
	if err != nil {
		return Empty(), fmt.Errorf("serverpref: marshal order: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO server_view_prefs (user_id, pinned_json, order_json, updated_at) VALUES (?, ?, ?, ?) `+
			store.UpsertSuffix(store.DialectOf(s.db), []string{"user_id"},
				[]string{"pinned_json", "order_json", "updated_at"}),
		userID, string(pinned), string(order), now)
	if err != nil {
		return Empty(), fmt.Errorf("serverpref: save: %w", err)
	}
	return clean, nil
}

// normalizeIDs 去掉空串与重复、拒绝长得像注入载体的超长串,并截到 maxIDs。
// 顺序按传入顺序保留(这份列表的顺序就是它的语义)。
func normalizeIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" || len(id) > idMaxLen || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
		if len(out) >= maxIDs {
			break
		}
	}
	return out
}

// decodeIDs 把库里那一列读成 id 列表;解析失败按「没有偏好」处理(见 Get 的说明)。
func decodeIDs(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{}
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return []string{}
	}
	return normalizeIDs(ids)
}
