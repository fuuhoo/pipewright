package mcpsrv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// endpointKeyT 是私有的 context key 类型:避免与其它包的值撞键。
type endpointKeyT struct{}

// endpointKey 挂的是「这次 MCP 调用该用谁的身份去打内部 REST」。
//
// 由 Handler 在 Bearer 校验通过后写入请求 context,工具处理函数再取出来 —— 这是
// 「工具继承调用方权限」这条链的唯一入口,拿不到它就等于装配出错,只能报错不能放行。
var endpointKey endpointKeyT

func withEndpoint(ctx context.Context, e Endpoint) context.Context {
	return context.WithValue(ctx, endpointKey, e)
}

func endpointFrom(ctx context.Context) (Endpoint, error) {
	e, ok := ctx.Value(endpointKey).(Endpoint)
	if !ok || e == nil {
		return nil, errors.New("服务端内部调用通道未建立,这是服务端装配问题")
	}
	return e, nil
}

// allowedToolsKeyT 同上,是私有的 context key 类型。
type allowedToolsKeyT struct{}

// allowedToolsKey 挂的是「这次请求的端点允许暴露哪些工具」(令牌明文对应的 allowed_tools)。
//
// 由 Handler 在 Bearer 校验通过后写入,SDK 的 getServer 回调读它来决定注册哪几个工具。
// 取不到时第二个返回值为 false —— 调用方必须据此拒绝建 server,不能当成「不限」:
// 「不知道范围」和「范围是全部」是两件事,后者要由令牌自己说出来。
var allowedToolsKey allowedToolsKeyT

func withAllowedTools(ctx context.Context, allowed []string) context.Context {
	return context.WithValue(ctx, allowedToolsKey, allowed)
}

func allowedToolsFrom(ctx context.Context) ([]string, bool) {
	v, ok := ctx.Value(allowedToolsKey).([]string)
	return v, ok
}

// readOnly / write 只是给客户端的提示位:只读工具可被自动放行,写工具需要人确认。
func annotations(title string, readOnly bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		Title:        title,
		ReadOnlyHint: readOnly,
		// 写操作里没有任何「不可逆删除」(这套工具面只能建项目/改流水线/跑/停),
		// 故 destructive 一律 false;幂等性各工具不同,交给描述文字讲清。
		DestructiveHint: ptr(false),
		OpenWorldHint:   ptr(false),
	}
}

func ptr[T any](v T) *T { return &v }

// maxLogCharsDefault / maxLogCharsLimit 是 get_run_logs 单次返回的字符预算。
// 默认 4000:构建日志动辄上万行,一次全吐会把上下文挤满;靠 nextSeq 续读更划算。
const (
	maxLogCharsDefault = 4000
	maxLogCharsLimit   = 20000
)

// registerTools 按这把令牌的端点白名单把工具挂到 server 上(allowed 为空 = 全部)。
//
// 判的是「名单里勾了才注册」,不是「注册完再把其余的摘掉」:后者一旦有人新加工具
// 却忘了同步 catalog.go 的清单,那个新工具就永远不在摘除名单里、对收窄过的端点也照样可见。
// 名称与 REST 端点的对应关系全在此处,一处可查;新增工具要同时补进 instructions、
// 文档与清单(漂移由 TestMCP_CatalogMatchesServer 拦下)。
func registerTools(s *mcp.Server, allowed []string) {
	f := newToolFilter(allowed)
	if f.keepTool("list_projects") {
		mcp.AddTool(s, &mcp.Tool{
			Name:        "list_projects",
			Description: `列出当前令牌可见的项目,按分组权限收敛(admin 看全部,普通用户只看得到自己被分组的)。返回 projects 数组,外加 page/pageSize/total(总数,用来判断要不要翻页)。每项的 id 就是其余工具要的 projectId。`,
			Annotations: annotations("项目列表", true),
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in listProjectsInput) (*mcp.CallToolResult, any, error) {
			e, err := endpointFrom(ctx)
			if err != nil {
				return nil, nil, err
			}
			page, pageSize := clampPage(in.Page, in.PageSize)
			res, err := e.Do(ctx, http.MethodGet, "/api/projects", url.Values{
				"page":     {strconv.Itoa(page)},
				"pageSize": {strconv.Itoa(pageSize)},
			}, nil)
			if err != nil {
				return nil, nil, err
			}
			out := map[string]any{"projects": passthrough(res), "page": page, "pageSize": pageSize}
			copyHeader(res, out, "X-Total-Count", "total")
			return nil, out, nil
		})
	}

	if f.keepTool("create_project") {
		mcp.AddTool(s, &mcp.Tool{
			Name: "create_project",
			Description: `新建项目。只有 name 是必填:不绑仓库也能建(纯发布型项目)。
要绑 git 仓库就必须同时给 repoUrl 和 credentialId(保险库里的仓库凭据 id),缺一会被拒;这套工具面不提供凭据管理,没有可用凭据时请先在网页端「凭据」页创建,或只建不绑仓库的项目。
groupId 可选,把项目归进某个资源分组;defaultBranch 可选。返回新建项目 DTO,记下它的 id。`,
			Annotations: annotations("新建项目", false),
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in createProjectInput) (*mcp.CallToolResult, any, error) {
			e, err := endpointFrom(ctx)
			if err != nil {
				return nil, nil, err
			}
			if strings.TrimSpace(in.Name) == "" {
				return nil, nil, errors.New("项目名不能为空")
			}
			res, err := e.Do(ctx, http.MethodPost, "/api/projects", nil, map[string]string{
				"name":          in.Name,
				"repoUrl":       in.RepoURL,
				"credentialId":  in.CredentialID,
				"defaultBranch": in.DefaultBranch,
				"groupId":       in.GroupID,
			})
			if err != nil {
				return nil, nil, err
			}
			return nil, passthrough(res), nil
		})
	}

	if f.keepTool("set_pipeline") {
		mcp.AddTool(s, &mcp.Tool{
			Name: "set_pipeline",
			Description: `用一份 .pipewright.yml 覆盖并保存项目的流水线(整条替换,不是追加)。

形状:顶层 version: 1 + stages[]。阶段字段 id/name/kind(source|build|deploy|notify|custom)/needs(引用的是**阶段 id**)/gate/when.branches。阶段下 jobs[],任务字段 name/type/config,脚本类任务还可以用嵌套 script 块写 commands/env/workdir。

最小可用示例:
  version: 1
  stages:
    - id: stg_src
      name: 源码
      kind: source
      jobs:
        - name: 仓库源
          type: git_source
    - id: stg_build
      name: 构建
      kind: build
      needs: [stg_src]
      jobs:
        - name: 打包
          type: build
          config:
            artifactType: file          # file=产物档位,image=镜像档位(必填)
            buildEnvId: "<list_build_envs 的 id>"
            commands: "npm ci\nnpm run build"
            artifactPath: dist

约束(违反会 422,错误里有具体原因,按原因改完重试):
  - 进构建容器的任务必须用 buildEnvId 引用预置构建环境;手填任意镜像会被拒。
  - type: build 必须给 artifactType;deploy/发布类节点必须把它需要的落点字段配全(serverId/集群/命名空间等)。
  - 未绑定仓库的项目不要写 git_source 任务。
保存成功后返回规范化后的流水线(stages + 规范化 YAML),可据此确认平台实际收到了什么。`,
			Annotations: annotations("设置流水线", false),
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in setPipelineInput) (*mcp.CallToolResult, any, error) {
			e, err := endpointFrom(ctx)
			if err != nil {
				return nil, nil, err
			}
			if strings.TrimSpace(in.PipelineYAML) == "" {
				return nil, nil, errors.New("流水线 YAML 不能为空")
			}
			// save:true:这套工具面向「配好就能跑」,预览模式(不落库)对 AI 没有意义。
			res, err := e.Do(ctx, http.MethodPost, "/api/projects/"+url.PathEscape(in.ProjectID)+"/pipeline/import", nil,
				map[string]any{"yaml": in.PipelineYAML, "save": true})
			if err != nil {
				return nil, nil, err
			}
			return nil, passthrough(res), nil
		})
	}

	if f.keepTool("list_build_envs") {
		mcp.AddTool(s, &mcp.Tool{
			Name:        "list_build_envs",
			Description: `列出平台预置的、已启用的构建环境(即可用在流水线里的容器镜像)。可按 language 过滤(如 go、java、node)。每项的 id 就是流水线 config 里的 buildEnvId;imageCheckStatus 不是 ok 时说明该镜像尚未就绪,构建会失败。`,
			Annotations: annotations("构建环境", true),
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in listBuildEnvsInput) (*mcp.CallToolResult, any, error) {
			e, err := endpointFrom(ctx)
			if err != nil {
				return nil, nil, err
			}
			var query url.Values
			if lang := strings.TrimSpace(in.Language); lang != "" {
				query = url.Values{"language": {lang}}
			}
			res, err := e.Do(ctx, http.MethodGet, "/api/build-envs", query, nil)
			if err != nil {
				return nil, nil, err
			}
			return nil, passthrough(res), nil
		})
	}

	if f.keepTool("trigger_run") {
		mcp.AddTool(s, &mcp.Tool{
			Name: "trigger_run",
			Description: `触发一次构建运行,立即返回运行详情(status 通常为 queued),不等构建结束 —— 之后用 get_run 轮询状态、get_run_logs 看日志。
branch 留空则用项目默认分支。params 是运行参数键值表,项目定义了参数类型时(必填/枚举/类型)会在此刻校验,非法直接报错、不会创建运行。
重要:项目若从未配置过流水线,平台会按默认空流水线跑,阶段里没有任务,结果是「成功但什么都没做」的假绿灯 —— 先调 set_pipeline 再触发。`,
			Annotations: annotations("触发运行", false),
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in triggerRunInput) (*mcp.CallToolResult, any, error) {
			e, err := endpointFrom(ctx)
			if err != nil {
				return nil, nil, err
			}
			body := map[string]any{"branch": in.Branch, "commit": in.Commit}
			if len(in.Params) > 0 {
				body["params"] = in.Params
			}
			res, err := e.Do(ctx, http.MethodPost, "/api/projects/"+url.PathEscape(in.ProjectID)+"/runs", nil, body)
			if err != nil {
				return nil, nil, err
			}
			return nil, passthrough(res), nil
		})
	}

	if f.keepTool("get_run") {
		mcp.AddTool(s, &mcp.Tool{
			Name:        "get_run",
			Description: `查一次运行的详情:status(queued/running/success/failed/canceled 等)、每个步骤的 name/stage/status/耗时、产物清单 artifacts 与部署落点 targets。判断「这次到底跑成功了什么」看这一条 + get_run_logs,不要只看 status。`,
			Annotations: annotations("运行详情", true),
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in runIDInput) (*mcp.CallToolResult, any, error) {
			e, err := endpointFrom(ctx)
			if err != nil {
				return nil, nil, err
			}
			res, err := e.Do(ctx, http.MethodGet, "/api/runs/"+url.PathEscape(in.RunID), nil, nil)
			if err != nil {
				return nil, nil, err
			}
			return nil, passthrough(res), nil
		})
	}

	if f.keepTool("get_run_logs") {
		mcp.AddTool(s, &mcp.Tool{
			Name:        "get_run_logs",
			Description: `读一次运行的日志,压成一整段纯文本,每行前缀标了所属步骤序号与流(如 [step3 stdout])。续读:把上次返回的 nextSeq 原样填进 sinceSeq。truncated=true 表示本次因字符预算丢了尾部行,nextSeq 已经退回最后保留的那行之后 —— 用同一个 sinceSeq 配更大的 maxChars 重取即可拿全。complete 说的是运行到没到终态,不是日志读没读完。`,
			Annotations: annotations("运行日志", true),
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in getRunLogsInput) (*mcp.CallToolResult, any, error) {
			e, err := endpointFrom(ctx)
			if err != nil {
				return nil, nil, err
			}
			since := in.SinceSeq
			if since < 0 {
				since = 0
			}
			res, err := e.Do(ctx, http.MethodGet, "/api/runs/"+url.PathEscape(in.RunID)+"/logs",
				url.Values{"sinceSeq": {strconv.Itoa(since)}}, nil)
			if err != nil {
				return nil, nil, err
			}
			var payload runLogsPayload
			if len(res.Body) > 0 {
				if err := json.Unmarshal(res.Body, &payload); err != nil {
					return nil, nil, fmt.Errorf("日志响应解析失败:%v", err)
				}
			}
			budget := in.MaxChars
			switch {
			case budget <= 0:
				budget = maxLogCharsDefault
			case budget > maxLogCharsLimit:
				budget = maxLogCharsLimit
			}
			return nil, foldLogs(payload, since, budget), nil
		})
	}

	if f.keepTool("cancel_run") {
		mcp.AddTool(s, &mcp.Tool{
			Name:        "cancel_run",
			Description: `取消一次正在排队或运行的构建。已经结束的运行会返回 409(run_not_cancelable),这不是错误,无需重试。取消本身会留审计记录。`,
			Annotations: annotations("取消运行", false),
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in runIDInput) (*mcp.CallToolResult, any, error) {
			e, err := endpointFrom(ctx)
			if err != nil {
				return nil, nil, err
			}
			res, err := e.Do(ctx, http.MethodPost, "/api/runs/"+url.PathEscape(in.RunID)+"/cancel", nil, nil)
			if err != nil {
				return nil, nil, err
			}
			return nil, passthrough(res), nil
		})
	}
}

// ---- 入参 ----

type listProjectsInput struct {
	Page     int `json:"page,omitempty" jsonschema:"页码,从 1 开始,缺省 1"`
	PageSize int `json:"pageSize,omitempty" jsonschema:"每页条数,缺省 50,上限 200"`
}

type createProjectInput struct {
	Name          string `json:"name" jsonschema:"项目名,必填且不能为空白"`
	RepoURL       string `json:"repoUrl,omitempty" jsonschema:"仓库地址;填了就必须同时填 credentialId"`
	CredentialID  string `json:"credentialId,omitempty" jsonschema:"仓库凭据 id(保险库),绑仓库时必填"`
	DefaultBranch string `json:"defaultBranch,omitempty" jsonschema:"默认分支,如 main;留空则触发时也要指定分支"`
	GroupID       string `json:"groupId,omitempty" jsonschema:"归属的资源分组 id,留空为未归组"`
}

type setPipelineInput struct {
	ProjectID    string `json:"projectId" jsonschema:"list_projects 返回的项目 id"`
	PipelineYAML string `json:"pipelineYaml" jsonschema:"完整的 .pipewright.yml 文档正文,整条替换现有流水线"`
}

type listBuildEnvsInput struct {
	Language string `json:"language,omitempty" jsonschema:"按语言过滤,如 go/java/node;留空返回全部已启用环境"`
}

type triggerRunInput struct {
	ProjectID string            `json:"projectId" jsonschema:"要触发运行的项目 id"`
	Branch    string            `json:"branch,omitempty" jsonschema:"构建分支,留空用项目默认分支"`
	Commit    string            `json:"commit,omitempty" jsonschema:"锁定到具体 commit;一般不填"`
	Params    map[string]string `json:"params,omitempty" jsonschema:"运行参数键值表,会注入构建容器环境变量;项目有参数定义时按定义校验"`
}

type runIDInput struct {
	RunID string `json:"runId" jsonschema:"运行 id,trigger_run 返回值里的 id 字段"`
}

type getRunLogsInput struct {
	RunID    string `json:"runId" jsonschema:"运行 id"`
	SinceSeq int    `json:"sinceSeq,omitempty" jsonschema:"只取这个序号之后的行;首次留空,之后填上次返回的 nextSeq"`
	MaxChars int    `json:"maxChars,omitempty" jsonschema:"本次最多回多少字符,缺省 4000,上限 20000"`
}

// ---- 出参(仅日志需要重塑形状,其余原样透传 REST DTO)----

// runLogsPayload 是 GET /api/runs/{id}/logs 的响应体形状(与 httpapi 的 runLogsDTO 对齐)。
type runLogsPayload struct {
	Lines []struct {
		Seq         int    `json:"seq"`
		Stream      string `json:"stream"`
		StepOrdinal int    `json:"stepOrdinal"`
		Text        string `json:"text"`
	} `json:"lines"`
	NextSeq  int  `json:"nextSeq"`
	Complete bool `json:"complete"`
}

type runLogsOutput struct {
	Text      string `json:"text"`
	NextSeq   int    `json:"nextSeq"`
	Complete  bool   `json:"complete"`
	Lines     int    `json:"lines"`
	Truncated bool   `json:"truncated"`
}

// foldLogs 把逐行 DTO 压成一整段文本,并在字符预算内截断。
//
// 预算按「整行」砍,并且把 nextSeq 退回到最后保留的那行之后:REST 给的 nextSeq 是
// 「本轮下发到的末行 + 1」,若照原样回传,被预算丢掉的那几行就永久读不到了。
// 一行都放不下时(预算小于首行)退回请求的 sinceSeq,让调用方换个大预算重取。
func foldLogs(p runLogsPayload, since, budget int) runLogsOutput {
	var (
		sb       strings.Builder
		used     int
		kept     int
		lastSeq  = since - 1
		dropped  bool
		budgeted = false
	)
	for _, line := range p.Lines {
		rendered := fmt.Sprintf("[step%d %s] %s\n", line.StepOrdinal, orDefault(line.Stream, "stdout"), line.Text)
		cost := len([]rune(rendered))
		if !budgeted && used+cost > budget {
			dropped = true
			budgeted = true // 一旦超预算,后续行全丢:否则中途留一行会劈开日志顺序
		}
		if !dropped {
			sb.WriteString(rendered)
			used += cost
			kept++
			lastSeq = line.Seq
		}
	}
	nextSeq := p.NextSeq
	if dropped {
		nextSeq = lastSeq + 1
	}
	return runLogsOutput{
		Text:      sb.String(),
		NextSeq:   nextSeq,
		Complete:  p.Complete,
		Lines:     kept,
		Truncated: dropped,
	}
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// clampPage 把分页入参收敛到合法区间:REST 侧对越界值不报错(取默认),这里先夹住
// 是为了让工具返回的 page/pageSize 与实际请求一致。默认值对齐 project.DefaultPageSize。
func clampPage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	switch {
	case pageSize <= 0:
		pageSize = 50
	case pageSize > 200:
		pageSize = 200
	}
	return page, pageSize
}

// copyHeader 把响应头里的分页元信息搬到输出里。
func copyHeader(res *apiCall, out map[string]any, header, field string) {
	if v := res.Header.Get(header); v != "" {
		out[field] = v
	}
}
