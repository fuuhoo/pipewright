// 工具清单:一份「平台上有哪些 MCP 工具」的名册,与 tools.go 的注册动作分列两处。
//
// 为什么需要单独一份:签发端点时要按名字校验(设置页勾的工具名必须是真存在的),
// 而校验方(httpapi)不能靠「试着注册一下再报错」来判存在性。清单同时给设置页画勾选框
// 用(标题 + 只读位决定分组展示与提示文案)。
//
// 这份名册与 registerTools 里的注册必须同步,漂移由 TestMCP_CatalogMatchesServer
// 用真 server 的 tools/list 兜住:多一个、少一个、标题或只读位对不上都会红。
package mcpsrv

// ToolInfo 是一条工具的对外描述(不含 handler,因此可以被 httpapi 安全地序列化)。
type ToolInfo struct {
	Name     string
	Title    string
	ReadOnly bool
}

// toolCatalog 按「先读后写、先粗后细」的顺序排列 —— 设置页直接按这个顺序渲染。
func toolCatalog() []ToolInfo {
	return []ToolInfo{
		{Name: "list_projects", Title: "项目列表", ReadOnly: true},
		{Name: "list_build_envs", Title: "构建环境", ReadOnly: true},
		{Name: "get_run", Title: "运行详情", ReadOnly: true},
		{Name: "get_run_logs", Title: "运行日志", ReadOnly: true},
		{Name: "create_project", Title: "新建项目", ReadOnly: false},
		{Name: "set_pipeline", Title: "设置流水线", ReadOnly: false},
		{Name: "trigger_run", Title: "触发运行", ReadOnly: false},
		{Name: "cancel_run", Title: "取消运行", ReadOnly: false},
	}
}

// ToolNames 是全部工具名,供签发接口校验「勾的工具名确实存在」。
func ToolNames() []string {
	catalog := toolCatalog()
	out := make([]string, 0, len(catalog))
	for _, t := range catalog {
		out = append(out, t.Name)
	}
	return out
}

// ToolCatalog 返回清单副本(调用方是 HTTP 层,不能让它改到包内数据)。
func ToolCatalog() []ToolInfo { return toolCatalog() }

// toolFilter 把端点的工具白名单收成一次判定函数。
//
// 空列表 = 不限(与 apitoken 的「'[]' 表示不限」同口径),此时返回 nil 且 keep 恒真。
// 名单里出现不认识的名字不做特殊处理:它匹配不到任何工具,自然被忽略 ——
// 拦这种输入是签发接口的事(见 httpapi 的 allowedTools 校验)。
type toolFilter struct {
	all  bool
	keep map[string]bool
}

func newToolFilter(allowed []string) toolFilter {
	if len(allowed) == 0 {
		return toolFilter{all: true}
	}
	set := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		set[name] = true
	}
	return toolFilter{keep: set}
}

// keepTool 报告该不该注册这个名字的工具。
func (f toolFilter) keepTool(name string) bool {
	return f.all || f.keep[name]
}
