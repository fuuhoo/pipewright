// Package configprofile 提供"配置资源"(Maven/npm/pip/goproxy 等配置文件)的领域层。
//
// 业务含义(v6.2 §3.3):
//   - 管理员预置配置文件;运行时由 runner.ConfigInjector 拷贝到容器。
//   - 文件落到宿主固定目录 ${PIPEWRIGHT_DATA_DIR}/config_profiles/<id>/<filename>,
//     DB 中存 file_path(权威路径) + content(冗余快照,供审计/导入导出)。
//   - is_builtin=1 行字段白名单(只允许改 description/enabled);is_builtin=0 行全字段可改。
//
// 包结构:
//   - model.go:实体 + Repo 接口
//   - files.go:原子写(tmp + fsync + rename)工具
//   - service.go:CRUD + 文件上传 + 字段白名单校验
//
// 边界:
//   - 不验白名单路径(已固定目录 + UUID 文件名)。
//   - 不接管 runner 路径;仅生成文件。注入逻辑阶段 6 接入 build 包。
package configprofile

import (
	"errors"
	"strings"
)

// 配置文件类型枚举(与 §12.2 一致;运行期不强校验枚举,留扩展空间)。
const (
	TypeMaven   = "maven"
	TypeNPM     = "npm"
	TypePip     = "pip"
	TypeGoProxy = "goproxy"
	TypeEnv     = "env"
	// 预留扩展(自定义类型:任意 *.toml/*.yaml 等)。
)

// AllowedExts 上传时允许的文件扩展名(P0 #3 边界:扩展名白名单防滥用)。
var AllowedExts = map[string]bool{
	".xml":   true,
	".conf":  true,
	".npmrc": true,
	".ini":   true,
	".env":   true,
	".toml":  true,
	".yaml":  true,
	".yml":   true,
}

// 错误。
var (
	ErrNotFound     = errors.New("configprofile: not found")
	ErrConflict     = errors.New("configprofile: conflict (language, config_type, name) duplicate")
	ErrInvalidInput = errors.New("configprofile: invalid input")
	ErrBuiltinReadonly = errors.New("configprofile: builtin profile is read-only (only description/enabled allowed)")
)

// ConfigProfile 是对外可见的视图;不暴露 file_path 内部细节(用户无需关心)。
type ConfigProfile struct {
	ID          string
	Language    string
	ConfigType  string
	Name        string
	TargetPath  string // 容器内目标路径
	FilePath    string // 宿主机固定目录路径(${DATA_DIR}/config_profiles/<id>/<filename>)
	Content     string // 文件内容
	IsDefault   bool
	IsBuiltin   bool
	Description string
	Enabled     bool
	CreatedBy   string
	CreatedAt   string // RFC3339
	UpdatedAt   string
}

// ValidateCreate 校验 Create 输入合法性。
func (p *ConfigProfile) ValidateCreate() error {
	if strings.TrimSpace(p.Language) == "" {
		return wrapErr(ErrInvalidInput, "language 必填")
	}
	if strings.TrimSpace(p.ConfigType) == "" {
		return wrapErr(ErrInvalidInput, "config_type 必填")
	}
	if strings.TrimSpace(p.Name) == "" {
		return wrapErr(ErrInvalidInput, "name 必填")
	}
	if strings.TrimSpace(p.TargetPath) == "" {
		return wrapErr(ErrInvalidInput, "target_path 必填")
	}
	if strings.TrimSpace(p.Content) == "" {
		return wrapErr(ErrInvalidInput, "content 必填")
	}
	return nil
}

// IsExtAllowed 校验文件扩展名是否在白名单。
func IsExtAllowed(filename string) bool {
	idx := strings.LastIndex(filename, ".")
	if idx < 0 {
		return false
	}
	ext := strings.ToLower(filename[idx:])
	return AllowedExts[ext]
}

// ListFilter List 入参。
type ListFilter struct {
	Language       string // 精确匹配
	ConfigType     string
	IncludeBuiltin bool   // 默认 false(只返回用户可编辑的);true=返回全部
	IncludeDisabled bool
}

// Repo SQL 接口。
type Repo interface {
	Create(p *ConfigProfile) error
	GetByID(id string) (*ConfigProfile, error)
	List(filter ListFilter) ([]*ConfigProfile, error)
	Update(p *ConfigProfile) error
	Delete(id string) error
}

func wrapErr(base error, msg string) error {
	return &wrappedErr{base: base, msg: msg}
}

type wrappedErr struct {
	base error
	msg  string
}

func (w *wrappedErr) Error() string { return w.msg + ": " + w.base.Error() }
func (w *wrappedErr) Unwrap() error { return w.base }