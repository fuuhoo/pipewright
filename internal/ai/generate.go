// generate.go 是「AI 生成流水线提案」(Story 2.5 / FR-8)。
//
// Service.Generate 据仓库分析(RepoAnalysis)+ 用户自然语言补充构造 prompt(含期望
// JSON schema),调 LLM 的 chat 接口(claude /v1/messages、openai /v1/chat/completions、
// ollama /api/chat,均经注入的 http.Client),取回文本,容错剥离 markdown ```json fence
// 后 json.Unmarshal 到 Proposal,并给每个 stage/job/branchMapping 补 `p_` 前缀 id 供前端逐项勾选。
//
// 安全(AC-SEC):apiKey 仅进程内解密取用、用完即弃;绝不进 prompt / 日志 / 错误 / 响应。
// 调用 / 解析失败统一映射为 ErrGenerateFailed 包裹的人读错误(不 %w 底层网络错误,避免
// URL 中密钥外泄)。未配置 / 未启用 → ErrAINotConfigured(上层据此 available=false 优雅降级)。
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// maxLLMRespBytes 是 LLM 响应体读取上限(防异常大响应吃内存)。
const maxLLMRespBytes = 1 << 20 // 1MB

// generateMaxTokens 是 chat 请求的 max_tokens(够装一份结构化提案)。
const generateMaxTokens = 2048

// GenerateInput 是生成提案的入参:仓库分析 + 自然语言补充(均可空)+ 可用节点目录。
type GenerateInput struct {
	Analysis RepoAnalysis
	NL       string
	// Catalog 是可用节点工具清单(内置 + 复用库自定义节点);空时回退内置目录,
	// 使 LLM 始终知道全部可组合节点,而非只会产粗粒度三段式。
	Catalog []NodeKind
	// BuildEnvs 是已启用的预置构建环境目录(R4:镜像的唯一来源)。空目录时 prompt 明确要求
	// 留空给用户选 —— LLM 手写镜像串(哪怕看着合理)也会被保存校验(#8)拒掉,不如不填。
	BuildEnvs []BuildEnvOption
}

// BuildEnvOption 是一条可引用的预置构建环境:LLM 只能在 config.buildEnvId 里写这里的 ID。
// Image 只是给模型判断「哪个环境语言/版本对得上这个仓库」的线索,不是让它抄写镜像名。
type BuildEnvOption struct {
	ID    string
	Label string
	Image string
}

// ---- Proposal 结构(冻结契约;对齐 2-2 spec / 2-4 build / 2-3 branchMappings 子集) ----

// ProposalJob 是提案中的一个任务(对齐 pipeline.Job 子集)。
// Config 是 LLM 据仓库分析填好的可执行配置(命令/镜像/Dockerfile 路径/端口等),
// 让生成的流水线**直接可用**;环境相关项(serverIds/渠道/凭据)留空交用户选。
type ProposalJob struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	Type    string         `json:"type"`
	Summary string         `json:"summary"`
	Config  map[string]any `json:"config,omitempty"`
	// Needs 是同阶段内本 job 依赖的其它 job 的 name(有数据依赖必填,如镜像依赖后端构建产物)。
	// 无 needs = 与同阶段其它 job 并行;有 needs = 等依赖完成后串行。apply 时按 name 映射为 job id。
	Needs []string `json:"needs,omitempty"`
}

// ProposalStage 是提案中的一个阶段(对齐 pipeline.Stage 子集)。
type ProposalStage struct {
	ID   string        `json:"id"`
	Name string        `json:"name"`
	Kind string        `json:"kind"`
	Jobs []ProposalJob `json:"jobs"`
}

// ProposalToolchain 对齐 settings.Toolchain。
type ProposalToolchain struct {
	Language string `json:"language"`
	Version  string `json:"version"`
}

// ProposalBuild 对齐 settings.BuildConfig 的非 secret 子集(绝不含 credentialId)。
type ProposalBuild struct {
	Model          string            `json:"model"`
	Toolchain      ProposalToolchain `json:"toolchain"`
	ArtifactType   string            `json:"artifactType"`
	DockerfilePath string            `json:"dockerfilePath"`
}

// ProposalBranchMapping 对齐 trigger.BranchMapping 子集。
type ProposalBranchMapping struct {
	ID            string `json:"id"`
	BranchPattern string `json:"branchPattern"`
	Environment   string `json:"environment"`
}

// Proposal 是 LLM 生成的结构化流水线提案(供前端逐项勾选后 apply)。
type Proposal struct {
	Stages         []ProposalStage         `json:"stages"`
	Build          ProposalBuild           `json:"build"`
	BranchMappings []ProposalBranchMapping `json:"branchMappings"`
	Rationale      string                  `json:"rationale"`
}

func (s *service) Generate(ctx context.Context, in GenerateInput) (*Proposal, error) {
	// 读配置 + 既有密文;未配置 / 未启用 → ErrAINotConfigured(优雅降级)。
	cfg, sealed, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	if cfg == nil || !cfg.Configured || !cfg.Enabled {
		return nil, ErrAINotConfigured
	}

	provider := cfg.Provider
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL(provider)
	}
	if baseURL == "" {
		return nil, ErrAINotConfigured
	}

	// apiKey:非 ollama 解密既有密钥进程内取用;ollama 无需 key。
	apiKey := ""
	if provider != ProviderOllama {
		if len(sealed) == 0 {
			return nil, ErrAINotConfigured
		}
		plain, oerr := s.openSecret(sealed)
		if oerr != nil {
			return nil, oerr
		}
		apiKey = plain
	}

	prompt := buildPrompt(in)

	text, _, err := s.chat(ctx, provider, baseURL, cfg.Model, apiKey, prompt)
	apiKey = "" // 明文用完即弃
	_ = apiKey
	if err != nil {
		return nil, err // 已是 ErrGenerateFailed 包裹的人读错误(绝无密钥)
	}

	proposal, err := parseProposal(text)
	if err != nil {
		return nil, err
	}
	assignProposalIDs(proposal)
	return proposal, nil
}

// chat 按 provider 构造 chat 请求并取回助手文本 + token 用量(经注入 http.Client),使用默认 max_tokens。
// 失败统一返回 ErrGenerateFailed 包裹的人读错误(不携带底层网络错误,避免 URL 密钥外泄)。
func (s *service) chat(ctx context.Context, provider, baseURL, model, apiKey, prompt string) (string, TokenUsage, error) {
	return s.chatWithTokens(ctx, provider, baseURL, model, apiKey, prompt, generateMaxTokens)
}

// chatWithTokens 同 chat,但允许调用方指定 claude 的 max_tokens(openai/ollama 沿用其默认)。
// 复用同一套 per-provider 请求构造 / 响应解析 / 错误脱敏逻辑(供风险标注等较短回包场景调小)。
func (s *service) chatWithTokens(ctx context.Context, provider, baseURL, model, apiKey, prompt string, maxTokens int) (string, TokenUsage, error) {
	base := strings.TrimRight(baseURL, "/")

	var (
		endpoint string
		header   = map[string]string{"Content-Type": "application/json"}
		body     []byte
		err      error
	)

	switch provider {
	case ProviderClaude:
		endpoint = base + "/v1/messages"
		header["x-api-key"] = apiKey
		header["anthropic-version"] = "2023-06-01"
		body, err = json.Marshal(map[string]any{
			"model":      model,
			"max_tokens": maxTokens,
			"messages": []map[string]string{
				{"role": "user", "content": prompt},
			},
		})
	case ProviderOpenAI:
		endpoint = base + "/v1/chat/completions"
		header["Authorization"] = "Bearer " + apiKey
		body, err = json.Marshal(map[string]any{
			"model": model,
			"messages": []map[string]string{
				{"role": "user", "content": prompt},
			},
		})
	case ProviderOllama:
		endpoint = base + "/api/chat"
		body, err = json.Marshal(map[string]any{
			"model":  model,
			"stream": false,
			"messages": []map[string]string{
				{"role": "user", "content": prompt},
			},
		})
	default:
		return "", TokenUsage{}, fmt.Errorf("%w: 不支持的 provider", ErrGenerateFailed)
	}
	if err != nil {
		return "", TokenUsage{}, fmt.Errorf("%w: 请求构造失败", ErrGenerateFailed)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", TokenUsage{}, fmt.Errorf("%w: 请求构造失败(baseUrl 可能非法)", ErrGenerateFailed)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}

	resp, err := s.chatClient.Do(req)
	if err != nil {
		// 绝不回显底层错误(可能含 endpoint/凭据细节)。
		return "", TokenUsage{}, fmt.Errorf("%w: %s", ErrGenerateFailed, mapTransportError(err))
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxLLMRespBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", TokenUsage{}, fmt.Errorf("%w: %s", ErrGenerateFailed, mapChatStatusError(provider, model, resp.StatusCode))
	}

	text, err := extractChatText(provider, raw)
	if err != nil {
		return "", TokenUsage{}, err
	}
	usage := extractChatUsage(provider, raw)
	// 月度账在这里记:所有生成路径(生成/诊断/风险/命令助手/compose)都收口于此,
	// 记不上也不影响已经拿到手的回复。
	_ = s.recordUsage(ctx, provider, usage)
	return text, usage, nil
}

// extractChatText 按 provider 从响应 JSON 取助手回复文本。
func extractChatText(provider string, raw []byte) (string, error) {
	switch provider {
	case ProviderClaude:
		var r struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(raw, &r) != nil {
			return "", fmt.Errorf("%w: 响应解析失败", ErrGenerateFailed)
		}
		var b strings.Builder
		for _, c := range r.Content {
			if c.Type == "" || c.Type == "text" {
				b.WriteString(c.Text)
			}
		}
		return b.String(), nil
	case ProviderOpenAI:
		var r struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if json.Unmarshal(raw, &r) != nil || len(r.Choices) == 0 {
			return "", fmt.Errorf("%w: 响应解析失败", ErrGenerateFailed)
		}
		return r.Choices[0].Message.Content, nil
	case ProviderOllama:
		var r struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(raw, &r) != nil {
			return "", fmt.Errorf("%w: 响应解析失败", ErrGenerateFailed)
		}
		return r.Message.Content, nil
	default:
		return "", fmt.Errorf("%w: 不支持的 provider", ErrGenerateFailed)
	}
}

// parseProposal 容错解析 LLM 文本为 Proposal:剥离 markdown ```json fence + 前后噪声后 Unmarshal。
func parseProposal(text string) (*Proposal, error) {
	jsonText := stripJSONFence(text)
	if strings.TrimSpace(jsonText) == "" {
		return nil, fmt.Errorf("%w: 模型未返回可解析内容", ErrGenerateFailed)
	}
	var p Proposal
	if err := json.Unmarshal([]byte(jsonText), &p); err != nil {
		return nil, fmt.Errorf("%w: 模型返回的不是有效 JSON 提案", ErrGenerateFailed)
	}
	return &p, nil
}

// stripJSONFence 容错剥离 markdown 代码围栏并截取首个 JSON 对象。
// 处理:```json ... ```、``` ... ```、以及文本前后包裹的解释性文字。
func stripJSONFence(text string) string {
	t := strings.TrimSpace(text)
	if strings.HasPrefix(t, "```") {
		// 去掉首行围栏(```json / ```)。
		if idx := strings.IndexByte(t, '\n'); idx >= 0 {
			t = t[idx+1:]
		} else {
			t = strings.TrimPrefix(t, "```")
		}
		// 去掉尾部围栏。
		if idx := strings.LastIndex(t, "```"); idx >= 0 {
			t = t[:idx]
		}
		t = strings.TrimSpace(t)
	}
	// 截取首个 '{' 到末个 '}'(去掉模型可能附带的解释文字)。
	start := strings.IndexByte(t, '{')
	end := strings.LastIndexByte(t, '}')
	if start >= 0 && end >= start {
		return t[start : end+1]
	}
	return t
}

// assignProposalIDs 给每个 stage/job/branchMapping 补 `p_` 前缀 id(供前端逐项勾选)。
// 已有 id 保留;空 id 按序号生成稳定值(p_sN / p_jN / p_mN)。
func assignProposalIDs(p *Proposal) {
	if p.Stages == nil {
		p.Stages = []ProposalStage{}
	}
	if p.BranchMappings == nil {
		p.BranchMappings = []ProposalBranchMapping{}
	}
	jobSeq := 0
	for i := range p.Stages {
		if strings.TrimSpace(p.Stages[i].ID) == "" {
			p.Stages[i].ID = fmt.Sprintf("p_s%d", i+1)
		}
		if p.Stages[i].Jobs == nil {
			p.Stages[i].Jobs = []ProposalJob{}
		}
		for j := range p.Stages[i].Jobs {
			jobSeq++
			if strings.TrimSpace(p.Stages[i].Jobs[j].ID) == "" {
				p.Stages[i].Jobs[j].ID = fmt.Sprintf("p_j%d", jobSeq)
			}
		}
	}
	for i := range p.BranchMappings {
		if strings.TrimSpace(p.BranchMappings[i].ID) == "" {
			p.BranchMappings[i].ID = fmt.Sprintf("p_m%d", i+1)
		}
	}
}

// buildPrompt 构造发给 LLM 的 prompt:仓库分析 + 自然语言补充 + 期望 JSON schema。
// 绝不含任何 apiKey / token(仅分析结论 + 用户文本)。
func buildPrompt(in GenerateInput) string {
	var b strings.Builder
	b.WriteString("你是 CI/CD 流水线配置助手。请根据下面的仓库分析与用户补充,生成一份流水线配置提案。\n\n")

	b.WriteString("## 仓库分析\n")
	a := in.Analysis
	if a.Cloned {
		b.WriteString("- 已克隆仓库并读取 manifest。\n")
		writeKV(&b, "主语言", a.Language)
		writeKV(&b, "语言/工具链版本", a.LanguageVersion)
		writeKV(&b, "构建工具", a.BuildTool)
		writeKV(&b, "产物类型提示", a.ArtifactHint)
		if a.HasDockerfile {
			b.WriteString("- 仓库根含 Dockerfile。\n")
		}
		if len(a.Signals) > 0 {
			b.WriteString("- 检测证据:" + strings.Join(a.Signals, "、") + "\n")
		}
	} else {
		b.WriteString("- 仓库未能克隆(网络/权限受限),请仅凭仓库地址与用户补充做合理推断。\n")
	}

	b.WriteString("\n## 用户补充(自然语言)\n")
	if strings.TrimSpace(in.NL) != "" {
		b.WriteString(strings.TrimSpace(in.NL) + "\n")
	} else {
		b.WriteString("(无)\n")
	}

	// 可用节点工具清单:LLM 的 job.type 只能从这里选,从而充分利用产品全部节点(而非只会三段式)。
	catalog := in.Catalog
	if len(catalog) == 0 {
		catalog = BuiltinNodeCatalog()
	}
	b.WriteString("\n## 可用节点类型(job.type 只能取下列之一;按需组合多阶段,阶段内无依赖的 job 会并行)\n")
	var customs []NodeKind
	for _, nk := range catalog {
		if nk.Custom {
			customs = append(customs, nk)
			continue
		}
		b.WriteString("- " + nk.Type + "(" + nk.Label + "·" + nk.Category + "):" + nk.Description + "\n")
	}
	if len(customs) > 0 {
		b.WriteString("### 复用库中的自定义节点(可直接选用:job.type 用 templated,job.name 用下列名称)\n")
		for _, nk := range customs {
			b.WriteString("- 「" + nk.Label + "」:" + nk.Description + "\n")
		}
	}

	// 构建环境目录:容器类节点的镜像**只能**引用这里的 ID(R4)。不给这份清单时模型会自己
	// 编 node:20 / maven:3.9 之类的镜像串,那种串一律过不了保存白名单(#8),提案直接落不了地。
	b.WriteString("\n## 可用构建环境(镜像的唯一来源;凡进容器的节点都要选一个)\n")
	if len(in.BuildEnvs) == 0 {
		b.WriteString("- 当前没有已启用的构建环境。需要在容器里跑的节点(构建任务的 jar/dist 档位与 toolchain 镜像档位、script)config 里**不要写 image**,也不要编 buildEnvId,留空由用户在画布上选。\n")
	} else {
		for _, e := range in.BuildEnvs {
			line := "- " + e.ID + "(" + e.Label
			if strings.TrimSpace(e.Image) != "" {
				line += "·镜像 " + e.Image
			}
			b.WriteString(line + ")\n")
		}
		b.WriteString("- 用法:在这些节点的 config 里写 `\"buildEnvId\": \"<上面的 ID>\"`,按仓库语言/版本挑最贴近的一条。**绝不要写 image 键,也不要自造镜像名** —— 手填镜像会被平台拦截。\n")
	}
	b.WriteString(`
## 串行 / 并行(关键!用每个 job 的 needs 表达节点依赖)
同一阶段内:**没有 needs 的 job 并行执行;有 needs 的 job 必须等其依赖的 job 完成后才串行执行**。
needs 填「本阶段内它所依赖的其它 job 的 name」(数组)。**凡有数据/产物依赖的 job 必须设 needs**,否则会并行跑、拿不到上游产物而失败。常见依赖:
- 构建镜像(档位 image)的 Dockerfile 里 COPY 了 jar/dist → 该 job 必须 needs 填产出这些文件的构建 job 的 name,**不能并行!**
- 互不依赖的(如前端构建与后端构建)不写 needs,让它们并行加速。

## 组合指引
- monorepo 前后端可并行:同一构建阶段放两个构建任务(档位 dist 与 jar,互不依赖 → 并行),再用档位 image 的构建任务 needs 串在它们之后。
- 有 Dockerfile → 构建任务档位 image(buildModel=dockerfile);无 Dockerfile 但要发镜像 → 同一任务改 buildModel=toolchain 并选 buildEnvId。**不要再单独加推送节点**:推镜像由该任务的 pushImage 开关(缺省推送)完成。
- 只要文件产物(前端 dist / 后端 jar)→ 构建任务给对应档位 + commands + artifactPath 即可。
- 部署后的健康探测**不是独立节点**:写在部署任务的 healthProbe 等 config 键上(见下);关键阶段(部署成功/失败)可接 notify。
- 交付方式是 docker(要容器起的镜像,或仓库里本就有 docker-compose.yml)→ 部署任务用 deploy_docker 并按上面选 dockerMode;只是往机器上铺文件再重启(jar/dist + nginx)→ 用 deploy_ssh;仓库面向 Kubernetes 部署(有 Helm chart / k8s 清单,或明确要求发集群)→ 用 deploy_k8s。
- 仅在内置节点无法表达的步骤才用 script 或库中的自定义节点(templated)。

## 每个节点的 config(关键!尽量据仓库分析填满,让流水线直接可用)
为每个 job 填 "config" 对象,**凡能从仓库分析推断的都填**;只有环境相关项(serverIds/channel/credentialId/clusterId)留空给用户选 —— 这些是「发到哪」的落点,模型无从得知,编一个 ID 比留空更糟(留空会在应用后被保存校验明确指出,编错则可能发到不该发的机器)。各类型 config 字段:
- build:artifactType(**必填档位**:"image" | "file";file 就是「产物」——工作区里的一个文件或目录)。
  - 档位 file(在构建环境容器里跑命令收产物):buildEnvId(运行镜像,**只能填「可用构建环境」里的 ID**,按分析出的语言/版本挑最贴近的一条;写 image 键或自造镜像名会被拦截)、commands(多行命令,据构建工具写,如 "cd <子目录>\nmvn -B -DskipTests package")、artifactPath(产物路径,如 "backend/target/*.jar"、"frontend/dist")。命令里的子目录要用分析里检测到的真实路径(如 backend/、frontend/)。
  - 档位 image:buildModel("dockerfile" 有 Dockerfile 否则 "toolchain")、dockerfilePath(检测到的 Dockerfile 路径,如 "backend/Dockerfile")、context(Dockerfile 所在目录,如 "backend");buildModel 为 "toolchain" 时同样必须给 buildEnvId 并填 buildCommand。pushImage 只在「只构建不推送」时才写 "false"。
- deploy_ssh:serverIds(落点主机,**留空**给用户选)、artifactType("image"|"file"|不填=按产物自动判断;与构建档位同一套词,历史的 dist/jar/archive 都归 "file")、containerName(image 档位时据项目名取,如 "<proj>-app")、ports(如 "8080:8080")、strategy("rolling"(缺省,所有主机同时发)|"canary"(分批:首批过了再发其余)|"interactive"(首批后暂停:等人工确认才发其余)|"blue-green"(逐台起新探活再切))、canaryCount(canary/interactive:首批几台,正整数)、deployPath(file 档位的发布目录)、restartCommand(file 档位的重启命令)。**只有用户明确说了「分批/灰度/蓝绿/先发几台/首批后暂停」才写非 rolling 的策略,并且那前提是它真的有多台机器可分批**(单主机节点上分批与一次性没有区别,保存会被拒)。前端静态站点用同一类型:artifactType="file" + restartCommand="nginx -s reload"(策略不用写)。健康门控写在本节点:healthProbe("http"|"command"|不填=不探测)、healthUrl(探测地址,据服务端口/框架填,Spring Boot 用 "http://localhost:<宿主端口>/actuator/health",其它用 "http://localhost:<端口>/healthz")、healthCommand(command 方式时在目标机跑的命令)、healthRetries(如 "10")、healthInterval(间隔秒,如 "3")、healthTimeout(单次超时秒)。
- deploy_docker:以 docker 交付。**dockerMode 必填**,两选一:
  - "run"(单容器):发的一定是上游构建出的镜像(artifactType 不用填),配 containerName(据项目名取,如 "<proj>-app")、ports(如 "8080:8080")、runArgs(可选,如 "--restart always");strategy/canaryCount 同 deploy_ssh(同样只在有多台机器时才写非 rolling)。
  - "compose":stackName(项目名,仅字母数字与 . _ -,不以 - 开头)+ 正文来源二选一:composeSource="repo" + composeFile(仓库里那份文件的相对路径,如 "docker-compose.yml" / "deploy/docker-compose.yml")—— **仓库里已有 compose 文件时用这个,正文随仓库演进,不用抄**;或 composeSource="paste"(缺省)+ composeYaml(整份正文,换行写成 \n,内容来自别处时用它)。仓库里有 docker-compose.yml 时优先 compose + composeSource="repo"。**这一档不读 strategy**(整份 YAML 交目标机的 compose CLI 自己编排),所以别写 strategy/canaryCount。
  两种方式都可选健康门控(healthProbe/healthUrl/healthCommand/healthRetries/healthInterval/healthTimeout,同上);serverIds 留空(用户选目标机)。
- deploy_k8s:交付到 Kubernetes 集群(平台直连集群 API,不选目标机,上游必须是档位 image 的构建任务)。**必填** clusterId(环境相关项,留空给用户选集群)。发布方式 **manifestSource 必填**,三选一:
  - "none"(缺省):只换已有负载的镜像,所以 **workloadName 必填**(如 "api";据清单/仓库里的资源名取,推不出就留空给用户填);namespace 能据仓库清单推断就填,推不出就**留空**(= 用集群登记的默认命名空间);workloadKind 填 "Deployment" 或 "StatefulSet"(缺省 Deployment);工作负载有多个容器时必须填 containerName(否则留空)。这条腿不创建负载:仓库里有清单却没建过负载时,提示用户先用 repo/paste 方式发一次。
  - "repo":应用项目仓库里那份清单(随仓库演进,**仓库已有 k8s 清单/Chart 渲染出的清单时优先用它**),配 manifestFile(仓库根下相对路径,必须以 .yml 或 .yaml 结尾,如 "deploy/k8s/api.yaml")。
  - "paste":把清单正文粘进节点,配 manifestYaml(多文档用 --- 分隔,镜像处写 {{IMAGE}} 占位符,整份不超过 64 KiB;换行写成 \n)。
  repo 与 paste 两模式下「发到哪」全部由清单自身决定:namespace **必须留空**(填了会被保存期拒掉),workloadName / workloadKind / containerName 这几格在清单档不生效(清单里每个负载都会被等),所以也别写。可选 rolloutTimeout(等滚完成的秒数,1..1800,缺省 300)、autoRollback("true"|"false",缺省开)。这类节点**不写** healthProbe/serverIds/deployPath/strategy 等键(成败按集群滚动状态判定,只支持滚动)。
- script:同 build 的 file 档位那套键(buildEnvId/commands/artifactPath…)。
- notify:titleTemplate/bodyTemplate(可用 {{project}} {{branch}} {{status}});channel 留空(用户选渠道)。
- git_source:config 留空 {}。

## 输出要求
只输出一个 JSON 对象(不要任何解释文字、不要 markdown 代码块),严格符合以下结构:
{
  "stages": [
    { "name": "流水线源", "kind": "source", "jobs": [ { "name": "拉取源码", "type": "git_source", "summary": "...", "config": {} } ] },
    { "name": "构建", "kind": "build", "jobs": [
        { "name": "后端构建", "type": "build", "summary": "...", "config": { "artifactType": "file", "buildEnvId": "<可用构建环境的 ID>", "commands": "cd backend\nmvn -B -DskipTests package", "artifactPath": "backend/target/*.jar" } },
        { "name": "前端构建", "type": "build", "summary": "...", "config": { "artifactType": "file", "buildEnvId": "<可用构建环境的 ID>", "commands": "cd frontend\nnpm install\nnpm run build", "artifactPath": "frontend/dist" } },
        { "name": "构建镜像", "type": "build", "summary": "...", "needs": ["后端构建", "前端构建"], "config": { "artifactType": "image", "buildModel": "dockerfile", "dockerfilePath": "backend/Dockerfile", "context": "backend" } } ] },
    { "name": "部署", "kind": "deploy", "jobs": [
        { "name": "SSH 部署", "type": "deploy_ssh", "summary": "...", "config": { "artifactType": "image", "containerName": "app", "ports": "8080:8080", "strategy": "rolling", "healthProbe": "http", "healthUrl": "http://localhost:8080/actuator/health", "healthRetries": "10", "healthInterval": "3" } } ] }
  ],
  "build": { "model": "toolchain|dockerfile", "toolchain": { "language": "node|go|java|python", "version": "..." }, "artifactType": "image|file", "dockerfilePath": "" },
  "branchMappings": [ { "branchPattern": "main", "environment": "生产" } ],
  "rationale": "一句话说明推荐依据"
}
约束:
- job.type 必须取自上面「可用节点类型」清单;kind 为 source/build/deploy/quality/notify/custom;必须恰有一个 source 阶段(含 git_source)。
- config 尽量据仓库分析填满(命令/镜像/Dockerfile 路径/context/端口/产物),让流水线开箱即跑;仅 serverIds/channel/credentialId 等环境相关项留空(落点由用户在向导里勾,模型不该猜)。
- 充分利用合适的节点类型,别把所有事都塞进一个构建任务;不要包含任何凭据、密钥或 credentialId。
`)
	return b.String()
}

// writeKV 写一行 "- key:value"(value 为空跳过)。
func writeKV(b *strings.Builder, key, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	b.WriteString("- " + key + ":" + value + "\n")
}
