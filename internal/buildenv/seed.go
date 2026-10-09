// Package buildenv — 内置数据 seed(v6.2 §阶段 16)。
//
// 启动时逐条预置常用构建环境(覆盖主流语言),便于用户开箱即用;幂等到 (language, version)
// 粒度:已存在的组合跳过,新增的内置项在升级后自动补上。
//
// 镜像字段(image)默认是上游官方镜像名,自建镜像在 SeedEnv 上标 SourceType=custom;管理员可在前端覆盖。
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
	// SourceType 为空时按 SourceOfficial 落库(上游官方镜像);自建/私有仓库镜像显式写 SourceCustom。
	SourceType string
	Enabled    bool
	SortOrder  int
}

// builtinBuildEnvs 是内置列表。
// 19 个,覆盖:Node / Python / Java(+Maven 工具链) / Go / Ruby / PHP / Rust / vfox 全量工具链 / 通用 Alpine。
// sort_order 决定前端目录的展示顺序(按 sort_order 升序),同一语言内部按版本从高到低排。
var builtinBuildEnvs = []SeedEnv{
	{Language: "node", Version: "26", DisplayName: "Node.js 26", Description: "Node.js 26(官方镜像)", Image: "node:26-alpine", Enabled: true, SortOrder: 6},
	{Language: "node", Version: "24", DisplayName: "Node.js 24", Description: "Node.js 24(官方镜像)", Image: "node:24-alpine", Enabled: true, SortOrder: 7},
	{Language: "node", Version: "22", DisplayName: "Node.js 22", Description: "Node.js 22 LTS", Image: "node:22-alpine", Enabled: true, SortOrder: 8},
	{Language: "node", Version: "20", DisplayName: "Node.js 20", Description: "Node.js 20 LTS(官方镜像)", Image: "node:20-alpine", Enabled: true, SortOrder: 10},
	{Language: "node", Version: "18", DisplayName: "Node.js 18", Description: "Node.js 18 LTS", Image: "node:18-alpine", Enabled: true, SortOrder: 11},
	{Language: "node", Version: "16", DisplayName: "Node.js 16", Description: "Node.js 16(已 EOL,仅存量项目)", Image: "node:16-alpine", Enabled: true, SortOrder: 12},
	{Language: "python", Version: "3.12", DisplayName: "Python 3.12", Description: "Python 3.12(官方)", Image: "python:3.12-alpine", Enabled: true, SortOrder: 20},
	{Language: "python", Version: "3.11", DisplayName: "Python 3.11", Description: "Python 3.11", Image: "python:3.11-alpine", Enabled: true, SortOrder: 21},
	{Language: "java", Version: "21", DisplayName: "Java 21", Description: "OpenJDK 21(eclipse-temurin)", Image: "eclipse-temurin:21-jdk-alpine", Enabled: true, SortOrder: 30},
	{Language: "java", Version: "17", DisplayName: "Java 17", Description: "OpenJDK 17 LTS", Image: "eclipse-temurin:17-jdk-alpine", Enabled: true, SortOrder: 31},
	// Maven 工具链:后端构建模板默认命令是 mvn package,JDK 镜像里没有 mvn,故单列一条。
	// 语言仍记 java —— 配置资源(java/settings.xml)按语言匹配注入,记成 maven 就永远配不上。
	{Language: "java", Version: "21-maven", DisplayName: "Java 21 + Maven 3.9", Description: "Maven 3.9(内置 JDK 21,可直接 mvn package)", Image: "maven:3.9-eclipse-temurin-21", Enabled: true, SortOrder: 32},
	// JDK 8/11 用 Debian 版而非 -alpine:temurin 的 alpine 变体只出 amd64,arm64 宿主拉不动。
	{Language: "java", Version: "11", DisplayName: "Java 11", Description: "OpenJDK 11 LTS(eclipse-temurin)", Image: "eclipse-temurin:11-jdk", Enabled: true, SortOrder: 33},
	{Language: "java", Version: "8", DisplayName: "Java 8", Description: "OpenJDK 8(eclipse-temurin)", Image: "eclipse-temurin:8-jdk", Enabled: true, SortOrder: 34},
	{Language: "go", Version: "1.22", DisplayName: "Go 1.22", Description: "Go 1.22(官方 alpine)", Image: "golang:1.22-alpine", Enabled: true, SortOrder: 40},
	{Language: "ruby", Version: "3.3", DisplayName: "Ruby 3.3", Description: "Ruby 3.3(官方 alpine)", Image: "ruby:3.3-alpine", Enabled: true, SortOrder: 50},
	{Language: "php", Version: "8.3", DisplayName: "PHP 8.3", Description: "PHP 8.3 CLI(官方 alpine)", Image: "php:8.3-cli-alpine", Enabled: true, SortOrder: 60},
	{Language: "rust", Version: "1.79", DisplayName: "Rust 1.79", Description: "Rust 1.79(官方 alpine)", Image: "rust:1.79-alpine", Enabled: true, SortOrder: 70},
	{Language: "vfox", Version: "1.0.12", DisplayName: "vfox 全量工具链", Description: "vfox 1.0.12:一个容器内按需 vfox use 切 Node/Java/Go 等版本(SDK 首次使用时下载)", Image: "registry.cn-qingdao.aliyuncs.com/fubin/vfox-full:1.0.12", SourceType: SourceCustom, Enabled: true, SortOrder: 90},
	{Language: "alpine", Version: "latest", DisplayName: "Alpine (通用)", Description: "Alpine 通用 shell 环境(无语言工具链)", Image: "alpine:latest", Enabled: true, SortOrder: 100},
}

// SeedIfEmpty 预置内置构建环境:逐条插入,已存在的 (language, version) 跳过(ErrConflict)。
// 返回新增条数。
//
// 「逐条」而非「表非空即整体跳过」是有意为之:内置目录会随版本增删条目,老库升级后要能
// 补上新内置项(用户自建/改过的条目因唯一键冲突不受影响)。
func SeedIfEmpty(svc *Service) (int, error) {
	inserted := 0
	for _, sd := range builtinBuildEnvs {
		source := sd.SourceType
		if source == "" {
			source = SourceOfficial
		}
		env := &BuildEnv{
			Language:         sd.Language,
			Version:          sd.Version,
			DisplayName:      sd.DisplayName,
			Description:      sd.Description,
			Image:            sd.Image,
			SourceType:       source,
			ImageCheckStatus: StatusUnchecked,
			Enabled:          sd.Enabled,
			SortOrder:        sd.SortOrder,
			CreatedBy:        "system:seed",
		}
		if _, err := svc.Create(env); err != nil {
			// ErrConflict(language, version duplicate)→ 该组合已存在,跳过。
			if errors.Is(err, ErrConflict) {
				continue
			}
			log.Printf("[seed] 警告:seed %s/%s 失败: %v", sd.Language, sd.Version, err)
			continue
		}
		inserted++
	}
	if inserted > 0 {
		log.Printf("[seed] build_envs seed 完成: 新增 %d 条", inserted)
	}
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
