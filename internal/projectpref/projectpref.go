// Package projectpref 保存项目页「项目卡片」的每用户手动顺序。
//
// 和 serverpref(容器页主机卡片)同一口径:顺序是看的人的习惯,不是项目的属性,所以按用户存
// (迁移 0094),而不是在 projects 表上加 sort_order。项目被删后残留的 id 在读取时忽略,
// 不需要任何后台清理任务。
package projectpref

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

// maxIDs 是一份顺序最多记多少个 id。一屏远到不了这个数,超出的只会是坏请求 ——
// 与其整体拒绝让一次拖拽白做,不如截断:读的一侧本来就忽略认不出的 id。
const maxIDs = 400

// idMaxLen 与 projects.id 同量级(uuid);比这更长就不当它是 id。
const idMaxLen = 64

// ErrNoUser 是缺少用户 id 时的入参错误:偏好没有归属就没有意义。
var ErrNoUser = errors.New("projectpref: 缺少用户 id")

// Prefs 是某人的项目卡片顺序。Order 读出永远非 nil(前端不必判空);
// 不在其中的项目按后端默认顺序垫在后面。
type Prefs struct {
	Order []string `json:"order"`
}

// Empty 返回一份干净的空偏好(而不是 nil 切片)。
func Empty() Prefs { return Prefs{Order: []string{}} }

// Service 读写每用户偏好(只经参数化 SQL 触库)。
type Service struct {
	db *sql.DB
}

// NewService 构造偏好服务。
func NewService(db *sql.DB) *Service { return &Service{db: db} }

// Get 读取某人的顺序。从没设置过(无行)或库里那一列坏了都回空偏好且不报错 ——
// 界面按默认顺序摆就行,一列脏数据不该让整个项目页打不开。
func (s *Service) Get(ctx context.Context, userID string) (Prefs, error) {
	if userID == "" {
		return Empty(), nil
	}
	var raw string
	err := s.db.QueryRowContext(ctx,
		`SELECT order_json FROM project_view_prefs WHERE user_id = ?`, userID,
	).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Empty(), nil
	}
	if err != nil {
		return Empty(), fmt.Errorf("projectpref: get: %w", err)
	}
	return Prefs{Order: decodeIDs(raw)}, nil
}

// Save 整份覆盖(幂等;同一人两处同时保存以最后一写为准),返回归一化后真正存下来的那份。
func (s *Service) Save(ctx context.Context, userID string, p Prefs) (Prefs, error) {
	if userID == "" {
		return Empty(), ErrNoUser
	}
	clean := Prefs{Order: normalizeIDs(p.Order)}
	order, err := json.Marshal(clean.Order)
	if err != nil {
		return Empty(), fmt.Errorf("projectpref: marshal order: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO project_view_prefs (user_id, order_json, updated_at) VALUES (?, ?, ?) `+
			store.UpsertSuffix(store.DialectOf(s.db), []string{"user_id"},
				[]string{"order_json", "updated_at"}),
		userID, string(order), now)
	if err != nil {
		return Empty(), fmt.Errorf("projectpref: save: %w", err)
	}
	return clean, nil
}

// normalizeIDs 去掉空串与重复、拒绝超长串,并截到 maxIDs;顺序按传入顺序保留(它就是语义)。
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
