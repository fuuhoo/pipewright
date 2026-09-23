package ai

import (
	"strings"
	"testing"
)

// buildPrompt 必须把可用节点目录(内置 + 自定义)当工具清单写进 prompt,
// 否则 LLM 不知道这些节点存在、只会产粗粒度三段式。
func TestBuildPromptIncludesCatalog(t *testing.T) {
	in := GenerateInput{
		Analysis: RepoAnalysis{Cloned: true, Language: "node"},
		Catalog: append(BuiltinNodeCatalog(), NodeKind{
			Type: "templated", Label: "我的扫描节点", Category: "custom",
			Description: "跑安全扫描", Custom: true,
		}),
	}
	p := buildPrompt(in)
	for _, want := range []string{"build", "script", "deploy_ssh", "notify", "可用节点类型"} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt 缺内置节点 %q", want)
		}
	}
	// 构建任务已合并:一个 build 类型 + 产物档位。旧类型再进目录就会让 LLM 产出两套并存的节点。
	for _, gone := range []string{"build_image", "build_frontend", "build_backend", "push_image", "health_check"} {
		if strings.Contains(p, gone) {
			t.Fatalf("prompt 不应再提供已合并/撤销的节点类型 %q", gone)
		}
	}
	// 健康门控改为部署任务的 config 键。
	for _, want := range []string{"healthProbe", "healthUrl"} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt 缺部署节点健康门控键 %q", want)
		}
	}
	// 档位与推送开关是新构建任务的两个关键键,提示词必须讲清。
	for _, want := range []string{"artifactType", "pushImage"} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt 缺构建任务键 %q", want)
		}
	}
	if !strings.Contains(p, "我的扫描节点") {
		t.Fatalf("prompt 应含复用库自定义节点名")
	}
}

// 空 Catalog 时回退内置目录(仍把全部内置节点喂 LLM)。
func TestBuildPromptCatalogFallback(t *testing.T) {
	p := buildPrompt(GenerateInput{Analysis: RepoAnalysis{Cloned: true}})
	if !strings.Contains(p, "## 可用节点类型") || !strings.Contains(p, "build(构建·build)") || !strings.Contains(p, "deploy_ssh") {
		t.Fatalf("空 Catalog 应回退内置目录")
	}
}
