package proxy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fuuhoo/pipewright/internal/store"
)

// CaddyImageSetting 是反代镜像的全局配置与生效值。
// Image 为空表示「没显式设置」;Effective 是本次编排真正会用的镜像。
type CaddyImageSetting struct {
	Image        string `json:"image"`
	Effective    string `json:"effective"`
	DefaultImage string `json:"defaultImage"`
}

// CaddyImage 读取反代镜像配置:显式设置 > 环境变量 PIPEWRIGHT_CADDY_IMAGE > 内置默认。
// 单例行缺失(老库/并发首读)按未设置处理。
func (s *service) CaddyImage(ctx context.Context) (CaddyImageSetting, error) {
	var stored string
	err := s.store.db.QueryRowContext(ctx, `SELECT caddy_image FROM proxy_config WHERE id = 1`).Scan(&stored)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return CaddyImageSetting{}, fmt.Errorf("proxy: load caddy image: %w", err)
	}
	return caddyImageSettingOf(stored), nil
}

// SetCaddyImage 保存反代镜像;空串 = 清除(回落到环境变量/内置默认)。
// 只接受合法镜像引用:它会原样进 docker run 参数,非法字符等于给编排开一道注入面。
func (s *service) SetCaddyImage(ctx context.Context, image string) (CaddyImageSetting, error) {
	image = strings.TrimSpace(image)
	if image != "" && !imageRefRe.MatchString(image) {
		return CaddyImageSetting{}, ErrInvalidCaddyImage
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.store.db.ExecContext(ctx,
		`INSERT INTO proxy_config (id, caddy_image, updated_at) VALUES (1, ?, ?) `+
			store.UpsertSuffix(store.DialectOf(s.store.db), []string{"id"}, []string{"caddy_image", "updated_at"}),
		image, now,
	)
	if err != nil {
		return CaddyImageSetting{}, fmt.Errorf("proxy: save caddy image: %w", err)
	}
	return caddyImageSettingOf(image), nil
}

// effectiveCaddyImage 返回本次编排该用的镜像。读配置失败不阻断部署:退到环境变量/默认,
// 否则一个配置表的瞬时读错会把「部署反代环境」整条路堵死。
func (s *service) effectiveCaddyImage(ctx context.Context) string {
	setting, err := s.CaddyImage(ctx)
	if err != nil {
		return caddyImageRef()
	}
	return setting.Effective
}

// caddyImageSettingOf 由「显式设置」推出生效镜像(显式 > 环境变量 > 内置默认)。
func caddyImageSettingOf(stored string) CaddyImageSetting {
	stored = strings.TrimSpace(stored)
	effective := stored
	if effective == "" {
		effective = caddyImageRef()
	}
	return CaddyImageSetting{Image: stored, Effective: effective, DefaultImage: defaultCaddyImage}
}
