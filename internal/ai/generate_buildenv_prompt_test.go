package ai

import (
	"strings"
	"testing"
)

// R4:AI 提案的镜像只能来自预置构建环境目录 —— prompt 必须给出可引用的 ID,
// 且不再教模型手写 image(那种 config 过不了保存白名单 #8)。
func TestBuildPromptUsesBuildEnvCatalog(t *testing.T) {
	p := buildPrompt(GenerateInput{
		Analysis: RepoAnalysis{Cloned: true, Language: "java", BuildTool: "maven"},
		BuildEnvs: []BuildEnvOption{
			{ID: "env-java21", Label: "Java 21", Image: "eclipse-temurin:21-jdk-alpine"},
		},
	})
	for _, want := range []string{"可用构建环境", "env-java21", "buildEnvId"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if strings.Contains(p, `"image": "maven:3.9-eclipse-temurin-21"`) || strings.Contains(p, `"image": "node:20"`) {
		t.Error("prompt still tells the model to hand-write image refs")
	}
}

// 目录为空(管理员还没启用任何环境)时,要求留空而不是让模型编镜像名。
func TestBuildPromptWithoutBuildEnvs(t *testing.T) {
	p := buildPrompt(GenerateInput{Analysis: RepoAnalysis{Cloned: true, Language: "go"}})
	if !strings.Contains(p, "当前没有已启用的构建环境") {
		t.Error("empty catalog should instruct the model to leave the build env unset")
	}
	if strings.Contains(p, "node:20-alpine") || strings.Contains(p, "maven:3.9") {
		t.Error("prompt must not offer example image literals")
	}
}
