// Package buildenv 提供"构建环境"领域层(v6.2 §3.1)。
//
// 业务含义:
//   - 管理员预置"语言 + 版本 + 镜像"组合;普通用户从预置目录选择,不可自由输入 image。
//   - 镜像拉取完全由系统执行,不拼接地址(填什么拉什么)。
//   - 镜像可用性自动检查 + 手动检查/拉取;不可用(env_resolver 拒)/未检查(SetEnabled 拒)
//     都是 P0 #4 修订点。
//
// 包结构:
//   - model.go:实体 + 错误 + Repo 接口
//   - service.go:CRUD + SetEnabled 三态校验
//   - checker.go:dstUB manifest inspect / pull + StartAutoCheck
//   - resolver.go:language+version → image(env_resolver),build 包调用入口
//
// 边界:
//   - credential_id 引用 credentials.id;删除 build_env 时不强制校验 credential 存在性
//     (vault 包"在用守卫"在删除 credential 时反向校验 build_envs.credential_id)。
//   - image 字段无白名单,管理员填什么就用什么。
package buildenv

import (
	"errors"
	"strings"
	"time"
)

// 镜像来源枚举。
const (
	SourceOfficial = "official"
	SourceCustom   = "custom"
)

// 镜像状态枚举(image_check_status)。
const (
	StatusUnchecked   = "unchecked"
	StatusChecking    = "checking"
	StatusAvailable   = "available"
	StatusUnavailable = "unavailable"
)

// 错误(不含敏感数据)。
var (
	ErrNotFound       = errors.New("buildenv: not found")
	ErrConflict       = errors.New("buildenv: conflict (language, version) duplicate")
	ErrInvalidInput   = errors.New("buildenv: invalid input")
	ErrImageUnavail   = errors.New("buildenv: image unavailable")
	ErrImageNotChecked = errors.New("buildenv: image not checked")
)

// BuildEnv 是对外可见的构建环境视图;不持密的字段值绝不暴露。
type BuildEnv struct {
	ID             string
	Language       string
	Version        string
	DisplayName    string
	Description    string
	SourceType     string
	Image          string
	CredentialID   string
	ImageCheckStatus string
	ImageCheckError  string
	ImageCheckedAt   *time.Time
	Enabled        bool
	SortOrder      int
	CreatedBy      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Validate 校验 Create/Update 入参基础合法性。
//   - language/version/display_name/image 必须非空(trim 后)
//   - source_type ∈ {official, custom}
//   - 启用时若 image_check_status == unavailable → 强制改 false
func (e *BuildEnv) Validate() error {
	if strings.TrimSpace(e.Language) == "" {
		return errWrap(ErrInvalidInput, "language 必填")
	}
	if strings.TrimSpace(e.Version) == "" {
		return errWrap(ErrInvalidInput, "version 必填")
	}
	if strings.TrimSpace(e.DisplayName) == "" {
		return errWrap(ErrInvalidInput, "display_name 必填")
	}
	if strings.TrimSpace(e.Image) == "" {
		return errWrap(ErrInvalidInput, "image 必填")
	}
	if e.SourceType != SourceOfficial && e.SourceType != SourceCustom {
		return errWrap(ErrInvalidInput, "source_type 必须是 official 或 custom")
	}
	if e.ImageCheckStatus == StatusUnavailable {
		e.Enabled = false
	}
	return nil
}

// ListFilter List 查询条件。
type ListFilter struct {
	Language        string // 精确匹配(经 case 字符串取);空=不限
	IncludeDisabled bool   // true=返回 enabled=0 与 enabled=1;false=仅 enabled=1
	SourceType      string // 空=不限
}

// Repo 是 build_envs 表的 SQL 接口,被 Service 依赖。
// 实现位于 internal/buildenv/sqlite.go(MySQL 由同文件 dialect 分支)。
type Repo interface {
	// Create 插入新行;UNIQUE(language, version) 冲突 → ErrConflict。
	Create(env *BuildEnv) error
	// GetByID 取单个;不存在 → ErrNotFound。
	GetByID(id string) (*BuildEnv, error)
	// GetByLanguageVersion 取单个;不存在 → (nil, nil)(非错误)。
	GetByLanguageVersion(lang, ver string) (*BuildEnv, error)
	// List 按 filter 返回;filter=zero value 返回全部(含 disabled)。
	List(filter ListFilter) ([]*BuildEnv, error)
	// Update 全字段更新;不存在 → ErrNotFound。
	Update(env *BuildEnv) error
	// Delete 按 ID 删;不存在 → ErrNotFound。
	Delete(id string) error
	// SetEnabled 仅改 enabled 列;不存在 → ErrNotFound。
	SetEnabled(id string, enabled bool) error
	// UpdateCheckStatus 仅改 image_check_* 三列(覆盖式写入)。
	UpdateCheckStatus(id, status, errMsg, checkedAt string) error
}

// ValidationError 是 P0 #4 与 env_resolver 用的业务错误(供 httpapi 错误映射)。
type ValidationError struct {
	Code    string
	Message string
}

func (v *ValidationError) Error() string { return v.Message }

func errWrap(base error, msg string) error {
	return &wrappedErr{base: base, msg: msg}
}

type wrappedErr struct {
	base error
	msg  string
}

func (w *wrappedErr) Error() string { return w.msg + ": " + w.base.Error() }
func (w *wrappedErr) Unwrap() error { return w.base }