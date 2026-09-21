package buildenv

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Service 是构建环境领域对外接口(由 httpapi.BuildEnv handler 调用)。
type Service struct {
	repo Repo
}

// NewService 构造 Service。
func NewService(repo Repo) *Service { return &Service{repo: repo} }

// Create 构造并保存新构建环境;id 由内部生成(UUID v4)。
func (s *Service) Create(in *BuildEnv) (*BuildEnv, error) {
	if in == nil {
		return nil, ErrInvalidInput
	}
	if in.ImageCheckStatus == "" {
		in.ImageCheckStatus = StatusUnchecked
	}
	if in.SourceType == "" {
		in.SourceType = SourceOfficial
	}
	if err := in.Validate(); err != nil {
		return nil, err
	}
	if in.ID == "" {
		in.ID = uuid.NewString()
	}
	if err := s.repo.Create(in); err != nil {
		return nil, err
	}
	return s.repo.GetByID(in.ID)
}

// GetByID 单条;不存在 → (nil, ErrNotFound)。
func (s *Service) GetByID(id string) (*BuildEnv, error) { return s.repo.GetByID(id) }

// GetByLanguageVersion 单条;不存在 → (nil, nil)。
func (s *Service) GetByLanguageVersion(lang, ver string) (*BuildEnv, error) {
	return s.repo.GetByLanguageVersion(lang, ver)
}

// List 按 filter。
func (s *Service) List(filter ListFilter) ([]*BuildEnv, error) { return s.repo.List(filter) }

// Update 更新;若 image/source_type/credential_id 任一变化 → 重置为 unchecked。
// 若 unavailable → 强制 enabled=false。
func (s *Service) Update(in *BuildEnv) (*BuildEnv, error) {
	old, err := s.repo.GetByID(in.ID)
	if err != nil {
		return nil, err
	}
	if old.Image != in.Image || old.SourceType != in.SourceType || old.CredentialID != in.CredentialID {
		in.ImageCheckStatus = StatusUnchecked
		in.ImageCheckError = ""
		in.ImageCheckedAt = nil
	}
	if err := in.Validate(); err != nil {
		return nil, err
	}
	if err := s.repo.Update(in); err != nil {
		return nil, err
	}
	return s.repo.GetByID(in.ID)
}

// Delete 按 ID 删。
func (s *Service) Delete(id string) error { return s.repo.Delete(id) }

// SetEnabled 启用/禁用 — P0 #4 三态校验:
//   unavailable     → 拒(IMAGE_UNAVAILABLE)
//   unchecked       → 默认拒(IMAGE_NOT_CHECKED);环境变量 PIPEWRIGHT_ALLOW_UNCHECKED_ENABLE=true 时放行
//   available/checking → 允许
func (s *Service) SetEnabled(id string, enabled bool) error {
	env, err := s.repo.GetByID(id)
	if err != nil {
		return err
	}
	if !enabled {
		return s.repo.SetEnabled(id, false)
	}
	switch env.ImageCheckStatus {
	case StatusUnavailable:
		return &ValidationError{
			Code:    "IMAGE_UNAVAILABLE",
			Message: "镜像不存在或不可拉取,无法启用。请先检查镜像或更换地址",
		}
	case StatusUnchecked:
		if !allowUncheckedEnable() {
			return &ValidationError{
				Code:    "IMAGE_NOT_CHECKED",
				Message: "镜像未检查过,请先点击【手动检查】或【手动拉取】验证可用性后再启用",
			}
		}
	}
	return s.repo.SetEnabled(id, true)
}

// allowUncheckedEnable 紧急逃生开关;默认 false。
func allowUncheckedEnable() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("PIPEWRIGHT_ALLOW_UNCHECKED_ENABLE")))
	return v == "1" || v == "true" || v == "yes"
}

// ListEnabledLanguages 取所有启用环境的去重语言列表(供前端下拉)。
// 返回字符串按字典序排序。
func (s *Service) ListEnabledLanguages(ctx context.Context) ([]string, error) {
	envs, err := s.repo.List(ListFilter{}) // 仅返回 enabled=1
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	out := []string{}
	for _, e := range envs {
		if _, ok := seen[e.Language]; ok {
			continue
		}
		seen[e.Language] = struct{}{}
		out = append(out, e.Language)
	}
	// 简单字典序;前端如需 UI 排序可在调用方处理。
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	_ = ctx
	return out, nil
}

// Now 是构建环境时间字段的"now"工具函数;测试可注入。
var Now = func() time.Time { return time.Now().UTC() }

// FmtTime 调格式化。
func FmtTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// ParseTime 反向;测试用。
func ParseTime(s string) (time.Time, error) {
	t, err := time.Parse("2006-01-02T15:04:05Z", s)
	if err == nil {
		return t, nil
	}
	t2, err2 := time.Parse(time.RFC3339, s)
	if err2 == nil {
		return t2, nil
	}
	return time.Time{}, fmt.Errorf("buildenv: parse time %q", s)
}