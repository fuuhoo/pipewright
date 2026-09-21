// Package configprofile — 内置数据 seed(v6.2 §阶段 16)。
//
// 首次启动时预置 4 个常用配置资源(覆盖主流语言):
//   - node/.npmrc:Node.js 镜像仓库加速
//   - python/pip.conf:pip 镜像仓库加速
//   - java/settings.xml:Maven settings(空,默认仓库)
//   - go/.gitconfig:Git user/credential helper 提示(空,用户自填)
//
// 幂等:已存在 (language, config_type, name) 不再插入,避免重复。
// 内容是中性占位(空或一行注释),用户可在前端编辑。
package configprofile

import (
	"errors"
	"log"
	"path/filepath"
)

// SeedProfile 是内置 seed 的一条配置资源。
type SeedProfile struct {
	Language   string
	ConfigType string
	Name       string
	TargetPath string
	Content    string
	IsDefault  bool
	Description string
}

// builtinConfigProfiles 是 v6.2 阶段 16 的内置列表。
// 4 个常用加速 / 占位配置。
var builtinConfigProfiles = []SeedProfile{
	{
		Language: "node", ConfigType: "npmrc", Name: "default-npmrc",
		TargetPath:  "/root/.npmrc",
		Content:     "# 编辑此文件以配置 npm 镜像加速(如 registry=https://registry.npmmirror.com/)\n",
		IsDefault:   true,
		Description: "Node.js npm 配置(默认占位)",
	},
	{
		Language: "python", ConfigType: "pip", Name: "default-pip-conf",
		TargetPath:  "/root/.pip/pip.conf",
		Content:     "[global]\n# index-url = https://pypi.tuna.tsinghua.edu.cn/simple\n",
		IsDefault:   true,
		Description: "Python pip 配置(默认占位)",
	},
	{
		Language: "java", ConfigType: "maven-settings", Name: "default-maven-settings",
		TargetPath:  "/root/.m2/settings.xml",
		Content:     "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<settings xmlns=\"http://maven.apache.org/SETTINGS/1.0.0\"></settings>\n",
		IsDefault:   true,
		Description: "Maven settings.xml(默认占位)",
	},
	{
		Language: "go", ConfigType: "env", Name: "default-go-env",
		TargetPath:  "/root/.env",
		Content:     "# GOPROXY=https://goproxy.cn,direct\n# GO111MODULE=on\n",
		IsDefault:   true,
		Description: "Go 构建环境变量(默认占位)",
	},
}

// SeedIfEmpty 在 config_profiles 为空时插入内置列表。
//
// 绕过 Service.Create(拒绝 is_builtin=1)直接走 repo + AtomicWriteFile,
// 由 seed 阶段独立拥有写内置行的权限。Service.Update 仍守住 builtin 字段
// 白名单(只能改 description/enabled)。
func SeedIfEmpty(repo Repo, dataDir string) (int, error) {
	existing, err := repo.List(ListFilter{IncludeBuiltin: true})
	if err != nil {
		return 0, err
	}
	if len(existing) > 0 {
		log.Printf("[seed] config_profiles 已有 %d 条,跳过 seed", len(existing))
		return 0, nil
	}
	inserted := 0
	for _, sd := range builtinConfigProfiles {
		cp := &ConfigProfile{
			ID:          "sec-" + sanitizeID(sd.Language) + "-" + sanitizeID(sd.ConfigType) + "-" + sanitizeID(sd.Name),
			Language:    sd.Language,
			ConfigType:  sd.ConfigType,
			Name:        sd.Name,
			TargetPath:  sd.TargetPath,
			Content:     sd.Content,
			IsDefault:   sd.IsDefault,
			IsBuiltin:   true, // 标记为内置,只能改 description/enabled
			Enabled:     true,
			Description: sd.Description,
		}
		filename := filepath.Base(cp.TargetPath)
		cp.FilePath = ProfilePath(dataDir, cp.ID, filename)
		// 写文件 + 入库(失败回滚)
		if err := AtomicWriteFile(cp.FilePath, []byte(cp.Content), 0o644); err != nil {
			log.Printf("[seed] 警告:seed %s 写文件失败: %v", cp.ID, err)
			continue
		}
		if err := repo.Create(cp); err != nil {
			_ = RemoveFile(cp.FilePath)
			if errors.Is(err, ErrConflict) {
				continue
			}
			log.Printf("[seed] 警告:seed %s 入库失败: %v", cp.ID, err)
			continue
		}
		inserted++
	}
	log.Printf("[seed] config_profiles seed 完成: 新增 %d 条", inserted)
	return inserted, nil
}
