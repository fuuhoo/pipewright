// Package buildenv — 内置数据 seed(v6.2 §阶段 16)。
//
// 首次启动时空 DB 时预置 11 个常用构建环境(覆盖主流语言),便于用户开箱即用。
// 后续启动幂等:已存在的 (language, version) 不再插入,避免重复。
//
// 镜像字段(image)是上游官方镜像名,管理员可在前端覆盖。
// 启用状态默认 true;镜像检查状态默认 unchecked(由阶段 15 的自动检查 goroutine 异步填)。
package buildenv

import (
	"errors"
	"log"
	"strings"
)

// SeedEnv 是内置 seed 的一条构建环境。
type SeedEnv struct {
	Language    string
	Version     string
	DisplayName string
	Description string
	Image       string
	Enabled     bool
	SortOrder   int
}

// builtinBuildEnvs 是 v6.2 阶段 16 的内置列表。
// 11 个,覆盖:Node / Python / Java / Go / Ruby / PHP / Rust / .NET / 通用 Alpine / Ubuntu。
var builtinBuildEnvs = []SeedEnv{
	{Language: "node", Version: "20", DisplayName: "Node.js 20", Description: "Node.js 20 LTS(官方镜像)", Image: "node:20-alpine", Enabled: true, SortOrder: 10},
	{Language: "node", Version: "18", DisplayName: "Node.js 18", Description: "Node.js 18 LTS", Image: "node:18-alpine", Enabled: true, SortOrder: 11},
	{Language: "python", Version: "3.12", DisplayName: "Python 3.12", Description: "Python 3.12(官方)", Image: "python:3.12-alpine", Enabled: true, SortOrder: 20},
	{Language: "python", Version: "3.11", DisplayName: "Python 3.11", Description: "Python 3.11", Image: "python:3.11-alpine", Enabled: true, SortOrder: 21},
	{Language: "java", Version: "21", DisplayName: "Java 21", Description: "OpenJDK 21(eclipse-temurin)", Image: "eclipse-temurin:21-jdk-alpine", Enabled: true, SortOrder: 30},
	{Language: "java", Version: "17", DisplayName: "Java 17", Description: "OpenJDK 17 LTS", Image: "eclipse-temurin:17-jdk-alpine", Enabled: true, SortOrder: 31},
	{Language: "go", Version: "1.22", DisplayName: "Go 1.22", Description: "Go 1.22(官方 alpine)", Image: "golang:1.22-alpine", Enabled: true, SortOrder: 40},
	{Language: "ruby", Version: "3.3", DisplayName: "Ruby 3.3", Description: "Ruby 3.3(官方 alpine)", Image: "ruby:3.3-alpine", Enabled: true, SortOrder: 50},
	{Language: "php", Version: "8.3", DisplayName: "PHP 8.3", Description: "PHP 8.3 CLI(官方 alpine)", Image: "php:8.3-cli-alpine", Enabled: true, SortOrder: 60},
	{Language: "rust", Version: "1.79", DisplayName: "Rust 1.79", Description: "Rust 1.79(官方 alpine)", Image: "rust:1.79-alpine", Enabled: true, SortOrder: 70},
	{Language: "alpine", Version: "latest", DisplayName: "Alpine (通用)", Description: "Alpine 通用 shell 环境(无语言工具链)", Image: "alpine:latest", Enabled: true, SortOrder: 100},
}

// SeedIfEmpty 在 build_envs 为空时插入内置列表;否则跳过(已存在的
// (language, version) 不重复)。返回新增条数。
//
// 用途:首次启动(空 DB)时预置;后续启动幂等。
func SeedIfEmpty(svc *Service) (int, error) {
	existing, err := svc.List(ListFilter{})
	if err != nil {
		return 0, err
	}
	if len(existing) > 0 {
		log.Printf("[seed] build_envs 已有 %d 条,跳过 seed", len(existing))
		return 0, nil
	}
	inserted := 0
	for _, sd := range builtinBuildEnvs {
		env := &BuildEnv{
			Language:       sd.Language,
			Version:        sd.Version,
			DisplayName:    sd.DisplayName,
			Description:    sd.Description,
			Image:          sd.Image,
			SourceType:     SourceOfficial,
			ImageCheckStatus: StatusUnchecked,
			Enabled:        sd.Enabled,
			SortOrder:      sd.SortOrder,
			CreatedBy:      "system:seed",
		}
		if _, err := svc.Create(env); err != nil {
			// ErrConflict(language, version duplicate) → 该组合已存在,跳过。
			if errors.Is(err, ErrConflict) {
				continue
			}
			log.Printf("[seed] 警告:seed %s/%s 失败: %v", sd.Language, sd.Version, err)
			continue
		}
		inserted++
	}
	log.Printf("[seed] build_envs seed 完成: 新增 %d 条", inserted)
	return inserted, nil
}

// TrimImageForLog 把 image 用于日志的脱敏裁剪(只取前 30 字符 + "...")。
func TrimImageForLog(image string) string {
	const max = 30
	if len(image) <= max {
		return image
	}
	return strings.TrimRight(image[:max], "/") + "..."
}
