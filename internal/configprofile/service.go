package configprofile

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Service 是 configprofile 领域对外接口(httpapi 调用)。
type Service struct {
	repo    Repo
	dataDir string // ${PIPEWRIGHT_DATA_DIR} 默认 ./data
}

// NewService 构造 Service;dataDir 为空时用 "./data"。
func NewService(repo Repo, dataDir string) *Service {
	if dataDir == "" {
		dataDir = "./data"
	}
	return &Service{repo: repo, dataDir: dataDir}
}

// DataDir 暴露当前使用的 dataDir(测试断言 / 调试用)。
func (s *Service) DataDir() string { return s.dataDir }

// Create 完整流程:校验 → 写磁盘(atomic) → DB INSERT;任一失败→ DB 回滚。
//   - filename 取 target_path 的基名(targetPath="/root/.m2/settings.xml" → filename="settings.xml")
//   - file_path = ProfilePath(dataDir, id, filename)
//   - is_builtin=1 入参必拒(内置配置由 seed 阶段 0x16 统一灌入,不允许 web API 创建)
func (s *Service) Create(in *ConfigProfile) (*ConfigProfile, error) {
	if in.IsBuiltin {
		return nil, wrapErr(ErrInvalidInput, "is_builtin=1 不允许通过 API 创建(由 seed 阶段统一灌入)")
	}
	if err := in.ValidateCreate(); err != nil {
		return nil, err
	}
	filename := filepath.Base(in.TargetPath)
	if filename == "" || filename == "." || filename == "/" {
		return nil, wrapErr(ErrInvalidInput, "target_path 必须含有效文件名")
	}
	if in.FilePath == "" {
		in.ID = "sec-" + sanitizeID(in.Language) + "-" + sanitizeID(in.ConfigType) + "-" + sanitizeID(in.Name)
		// UUID 兜底防碰撞
		if _, err := s.repo.GetByID(in.ID); err == nil {
			in.ID = "sec-" + in.ID + "-dup"
		}
		in.FilePath = ProfilePath(s.dataDir, in.ID, filename)
	}

	// 原子写文件
	if err := AtomicWriteFile(in.FilePath, []byte(in.Content), 0o644); err != nil {
		return nil, fmt.Errorf("configprofile: 写文件失败: %w", err)
	}

	if err := s.repo.Create(in); err != nil {
		// DB 失败 → 删文件(回滚)
		_ = RemoveFile(in.FilePath)
		return nil, err
	}
	return in, nil
}

// UploadInput 是 multipart 上传的扁平入参(由 httpapi/handler 拆解后传入)。
type UploadInput struct {
	Language    string
	ConfigType  string
	Name        string
	TargetPath  string
	Filename    string // 原始文件名(仅用于取扩展名白名单)
	Content     []byte // 文件字节
	Description string
	IsDefault   bool
	CreatedBy   string
}

// Upload 等价 Create 但接 []byte;扩展名校验自行。
func (s *Service) Upload(in *UploadInput) (*ConfigProfile, error) {
	if !IsExtAllowed(in.Filename) {
		return nil, wrapErr(ErrInvalidInput, "扩展名不在白名单内(允许:.xml/.conf/.npmrc/.ini/.env/.toml/.yaml/.yml)")
	}
	cp := &ConfigProfile{
		Language:    in.Language,
		ConfigType:  in.ConfigType,
		Name:        in.Name,
		TargetPath:  in.TargetPath,
		Content:     string(in.Content),
		Description: in.Description,
		IsDefault:   in.IsDefault,
		IsBuiltin:   false,
		Enabled:     true,
		CreatedBy:   in.CreatedBy,
	}
	// 与 Create 共享路径(重设 filename 让 Create 推算)
	return s.Create(cp)
}

// GetByID 单条;不存在 → ErrNotFound。
func (s *Service) GetByID(id string) (*ConfigProfile, error) { return s.repo.GetByID(id) }

// List 按 filter。
func (s *Service) List(filter ListFilter) ([]*ConfigProfile, error) { return s.repo.List(filter) }

// Update 更新;is_builtin=1 行只允许 description / enabled(P0 #3 字段白名单)。
// 其他字段变更请求返回 ErrBuiltinReadonly。
func (s *Service) Update(in *ConfigProfile) (*ConfigProfile, error) {
	old, err := s.repo.GetByID(in.ID)
	if err != nil {
		return nil, err
	}
	if old.IsBuiltin {
		// 字段白名单:仅 description / enabled 可改
		allowed := func() bool {
			if old.Language != in.Language {
				return false
			}
			if old.ConfigType != in.ConfigType {
				return false
			}
			if old.Name != in.Name {
				return false
			}
			if old.TargetPath != in.TargetPath {
				return false
			}
			if old.Content != in.Content {
				return false
			}
			if old.IsDefault != in.IsDefault {
				return false
			}
			return true
		}()
		if !allowed {
			return nil, ErrBuiltinReadonly
		}
		// 白名单允许 → 不重写文件、不改 file_path
		in.TargetPath = old.TargetPath
		in.FilePath = old.FilePath
		in.Content = old.Content
	} else {
		// 非内置:基础校验 + 写文件
		if err := in.ValidateCreate(); err != nil {
			return nil, err
		}
		filename := filepath.Base(in.TargetPath)
		if filename == "" || filename == "." || filename == "/" {
			return nil, wrapErr(ErrInvalidInput, "target_path 必须含有效文件名")
		}
		newFilePath := ProfilePath(s.dataDir, in.ID, filename)
		if newFilePath != in.FilePath {
			in.FilePath = newFilePath
		}
		if err := AtomicWriteFile(in.FilePath, []byte(in.Content), 0o644); err != nil {
			return nil, fmt.Errorf("configprofile: 写文件失败: %w", err)
		}
	}

	if err := s.repo.Update(in); err != nil {
		return nil, err
	}
	return s.repo.GetByID(in.ID)
}

// Delete 删 DB + 删磁盘。is_builtin=1 拒绝删。
func (s *Service) Delete(id string) error {
	p, err := s.repo.GetByID(id)
	if err != nil {
		return err
	}
	if p.IsBuiltin {
		return wrapErr(ErrBuiltinReadonly, "内置配置资源不可删")
	}
	if err := s.repo.Delete(id); err != nil {
		return err
	}
	if p.FilePath != "" {
		_ = RemoveFile(p.FilePath)
		// 顺带清目录(id 是 UUID,空目录也无妨)
		_ = os.Remove(ProfileDir(s.dataDir, id))
	}
	return nil
}

// ReadDiskContent 读磁盘文件(运维调试 / 校验用;Runner 实际也走这条路)。
// 文件丢失 → 错误(不静默回退到 DB.content,以便运维发现异常)。
func (s *Service) ReadDiskContent(p *ConfigProfile) (string, error) {
	b, err := os.ReadFile(p.FilePath)
	if err != nil {
		return "", fmt.Errorf("configprofile: 读文件失败(%s): %w", p.FilePath, err)
	}
	return string(b), nil
}

// sanitizeID 把入参转成 file-system safe 字符串(ID 生成用)。
// 留作防御:正常 Language/ConfigType/Name 都是人可读短字符串;若含路径分隔符会被 filepath.Base 折断。
func sanitizeID(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "..", "_")
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	return s
}

var _ = context.Background