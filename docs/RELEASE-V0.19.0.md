# v0.19.0 发布清单

> 发布日：2026-10-30
> **归属服务轴（项目按业务系统归类 + 按服务批量执行）+ 复用库可引用（流水线变量组、部署节点命名指令）**

## 功能与改动

### A: 归属服务后端（提交 9c1402e）
- **问题**: 项目只有资源分组这一条轴，而分组管的是「谁能看、谁能操作」。想问「哪些项目属于
  网关这个业务系统」，以前只能靠命名约定或备注 —— 这不是权限问题，是归类问题，用分组硬凑
  等于把可见性拆成一堆没必要的组。
- **改动**:
  - 迁移 [0090_services.sql](file:///Users/cw/code/pipewright/internal/store/migrations/sqlite/0090_services.sql)：
    建 `services` 表（`name` 唯一）+ `projects.service_id`（`NOT NULL DEFAULT ''`）+ 一个索引。
    **为什么是一张表而不是 projects 上一列自由文本**：后续要按服务统计 / 批量操作，改名得一处
    改全量；自由文本拼错就分裂成两个服务，且没法统一改名。
  - 领域层 [servicecatalog.go](file:///Users/cw/code/pipewright/internal/servicecatalog/servicecatalog.go)：
    List 带 `projectCount`（LEFT JOIN 统计，管理页展示与删前提示都要它），
    [Delete:182](file:///Users/cw/code/pipewright/internal/servicecatalog/servicecatalog.go#L182)
    先数引用，仍有项目挂着就回 [ErrInUse:34](file:///Users/cw/code/pipewright/internal/servicecatalog/servicecatalog.go#L34)。
  - **`projects.service_id` 有意不建外键**（与 `projects.group_id` 同口径）：靠 `ON DELETE SET NULL`
    删服务会在背后悄悄清空归属。代价是写入侧要自己挡悬挂引用 ——
    [project.go:237](file:///Users/cw/code/pipewright/internal/project/project.go#L237) `serviceExists`
    非空即必须是真实存在的服务，否则 `ErrServiceNotFound` → 422 `service_not_found`；否则筛选下拉
    会多出一个查无此人的档位。
  - HTTP [services.go](file:///Users/cw/code/pipewright/internal/httpapi/services.go)：
    `GET /api/services` 对所有登录用户开放（它不带权限语义），`POST` / `PATCH` / `DELETE` 是
    `RequireAdmin` —— 改名会改所有人看到的标签，建 / 删更是全局变更，所以三次写都留审计
    （`service_create` / `service_update` / `service_delete`）。
  - 运行侧 [run.go:236](file:///Users/cw/code/pipewright/internal/run/run.go#L236) 的 ListFilter 加
    `ServiceID` / `ServiceUnassigned`，条件落在已 `LEFT JOIN` 的 `p.service_id`
    （[service.go:420](file:///Users/cw/code/pipewright/internal/run/service.go#L420)）：只收窄结果集，
    不改可见范围，`VisibleGroups` 照旧独立生效。
- **验证**: `internal/servicecatalog/servicecatalog_test.go` 钉名字唯一 / 空名 / 删前引用检查；
  `internal/project/project_test.go` 钉悬挂服务被拒；`internal/run/run_test.go` 钉两档筛选互斥
  （`ServiceID` 优先于 `ServiceUnassigned`）。

### B: 复用库引用后端（提交 0193f39）
- **问题**: 多个流水线要用同一批变量（网关地址、镜像 tag、部署目录），复用库早有「变量组」，
  但项目引用不到它 —— 只能在每个项目重抄一遍；部署前的重启 / 探测命令同理，是一条条流水线
  手敲出来的。
- **改动**:
  - 迁移 [0091](file:///Users/cw/code/pipewright/internal/store/migrations/sqlite/0091_pipeline_settings_var_groups.sql)
    给 `pipeline_settings` 加 `var_group_ids`（JSON 数组，默认 `'[]'`）：存量行不引用任何组，
    行为与加列前一字不差。
    [0092](file:///Users/cw/code/pipewright/internal/store/migrations/sqlite/0092_library_commands.sql)
    建 `library_commands`（`name` 唯一 + `body`）。
  - 执行期 [library_vars.go:39](file:///Users/cw/code/pipewright/internal/build/library_vars.go#L39)
    `mergeReferencedVarGroups` 在解析配置时把引用的组并进 `settings.Build.Vars`，
    优先级 **项目自己的变量 > 先引用的组 > 后引用的组**；下游（脚本环境、构建参数、部署正文渲染）
    拿到的就是一张合并后的变量表，不需要各处再认识「变量组」这个概念。
  - **加密变量照旧只存凭据引用**：脚本 / 构建能用它们（明文即取即用），但部署正文渲染跳过 secret
    并对占位符明确报错（[library_vars.go:114](file:///Users/cw/code/pipewright/internal/build/library_vars.go#L114)
    `secretVarNames`）—— 那些正文会落到目标机与部署记录里，不能把机密抄进去；也不静默渲染成空串。
  - 指令 [command.go](file:///Users/cw/code/pipewright/internal/library/command.go) + 路由
    [library_commands.go](file:///Users/cw/code/pipewright/internal/httpapi/library_commands.go)：
    节点存 **ID 不抄正文**，库里改一次所有引用它的流水线跟着变。读对所有登录用户开放（选择器靠它取列表），
    写要 `library.edit` + CSRF + 审计（`command_create/update/delete`），与模板 / 变量组 / 自定义节点
    同一判点（[library_perm_test.go](file:///Users/cw/code/pipewright/internal/httpapi/library_perm_test.go)
    把 `/api/library-commands` 逐格钉进既有那张写方法表）。
  - 引用悬挂的处置：保存设置时逐个查变量组是否存在，缺就 422 `variable_group_not_found`
    （[pipeline_settings.go](file:///Users/cw/code/pipewright/internal/httpapi/pipeline_settings.go)），
    不等运行到部署阶段才发现；指令引用在运行时解析，指向已删的指令 → 节点失败并点名，
    **绝不静默当成空命令**（那会把「没执行」当成功）。
  - [job_types.go](file:///Users/cw/code/pipewright/internal/pipeline/job_types.go) 加三个引用键
    `preDeployCommandRef` / `restartCommandRef` / `healthCommandRef`，并让「填了引用」也算填了探测命令
    （正文运行时才取，保存期拿不到它的内容）。
  - `normalizeVarGroupIDs`（[settings.go](file:///Users/cw/code/pipewright/internal/pipeline/settings.go)）
    去空白项与重复项，并保证 JSON 契约是 `[]` 而不是 `null`。
- **验证**: [library_vars_test.go](file:///Users/cw/code/pipewright/internal/build/library_vars_test.go)
  七条 —— 合并优先级、无引用不动、变量组悬挂即失败、重启命令走引用、指令悬挂即失败、
  发布路径引用 secret 被拒、健康探测走引用；`command_test.go` 钉 CRUD 与重名；
  `pipeline_settings_test.go` 钉 `varGroupIds` 往返。

### C: 前端：归属服务筛选、管理与批量执行（提交 31c9bc0）
- **改动**:
  - [serviceFilter.ts](file:///Users/cw/code/pipewright/web/src/lib/serviceFilter.ts) 沿用分组筛选那套
    哨兵写法：空串是合法值（它就是「未归属」那一档），所以「全部」与「未归属」都得用哨兵区分。
  - 项目页加服务下拉（与分组正交，本地筛 —— 项目列表本来全量取回）；
    **运行页走服务端**（`Runs.vue`：运行列表是分页取回的，本地筛只能筛到这一屏，那不算筛选）。
  - 卡片显示后端 join 好的 `serviceName`，不用再拿 `/api/services` 映射一次。
  - 入口叫「**归属服务管理**」而不是「服务管理」：它挪的是项目归属，和归组同一类动作，
    名字撞车会让人以为是「服务器管理」。管理弹窗是管理员入口（建 / 改名 / 删），
    删除先本地拦一道「仍有项目归属」（后端也会 409 `service_in_use`，但那要等一个来回）。
  - [ServiceBatchRun.vue](file:///Users/cw/code/pipewright/web/src/components/projects/ServiceBatchRun.vue)
    在管理弹窗里选中某个服务后展开，可按分组（含未归组）再收窄，然后逐个触发。
    **走的是既有的单项目触发端点，没有新加批量接口** —— 权限、默认分支、审计都由服务端逐条判，
    失败的原因照实列出来，一个失败不中止整批；`execute()` 里不发 `branch`，空着让服务端解析
    项目自己的默认分支，前端猜一份迟早和远端对不上。回执只留在面板里逐条摆，不外发事件：
    项目卡片的「上次运行」至今是后端占位字段（[projects.go:55](file:///Users/cw/code/pipewright/internal/httpapi/projects.go#L55)
    `LastRunStatus: nil`），刷列表刷不出任何东西。
  - 服务读不到不影响项目列表本身：卡片退化成「未归属」，筛选只剩「全部 / 未归属」，不弹错误横幅
    （与分组同一套降级口径）。
- **验证**: `serviceFilter.test.ts` + `ServiceBatchRun.test.ts`（8 条：候选只含本服务、换分组重置勾选、
  请求体不带 branch、403 报「没有触发权限」而 422 保留服务端消息、断网、空态禁用按钮）+
  `ProjectCard.test.ts`；8 locale 补 `services` 命名空间，i18n 三门禁过。

### D: 前端：变量组引用与指令选择器（提交 ab994fe）
- **改动**:
  - 「变量与缓存」tab 加穿梭框选要引用的变量组（[VarsCacheTab.vue:165](file:///Users/cw/code/pipewright/web/src/components/pipeline/VarsCacheTab.vue#L165)），
    顺序即合并优先级；沿用其它穿梭框的每栏搜索 + 分页口径，不另起一套交互。
  - [jobConfigSchema.ts:48](file:///Users/cw/code/pipewright/web/src/components/pipeline/jobConfigSchema.ts#L48)
    新增字段 `kind: 'commandref'`，部署前 / 重启 / 健康探测三处可选库里的指令；
    为它补了 [`hiddenBy`:98](file:///Users/cw/code/pipewright/web/src/components/pipeline/jobConfigSchema.ts#L98) ——
    配了引用就收起正文格但**保留旧值**，切回「直接填写」不用重打。这与既有
    `dropWhenHidden`（切走即不落库）是两件事，所以做成两个开关而不是一个布尔。
  - 复用库页（[Library.vue](file:///Users/cw/code/pipewright/web/src/views/Library.vue)）补「指令」一档：
    建 / 改 / 删，写要 `library.edit`，与模板 / 变量组 / 自定义节点同一入口形态。
  - 提示文案把「加密变量被路径 / 命令引用是运行时的确定错误」写在前面，不让人以为填上就能跑。
- **验证**: `npm run typecheck` + 1233 条单测全绿；:18080 临时实例上真实点过引用与批量执行；
  重建 8080 / 8081 两实例后页面可见（入口 hash `index-DSMnHco1.js` → `index-TJCaWWLy.js`）。

## 迁移

这一版有 **3 个迁移 ×2 方言**（0090 / 0091 / 0092），老库要跟着升：

```bash
git diff --name-only v0.18.0..v0.19.0 -- internal/store/migrations
# 期望 6 个文件:0090_services / 0091_pipeline_settings_var_groups / 0092_library_commands ×(sqlite,mysql)
```

形态逐条核过（`grep -inE "drop|rename|modify|change column|update .* set"` 对这六个文件零命中）：

- 0090：`CREATE TABLE IF NOT EXISTS services` + `ALTER TABLE projects ADD COLUMN service_id`
  + `CREATE INDEX`（sqlite 用部分索引 `WHERE service_id != ''`，MySQL 用普通索引）。
- 0091：只有 `ALTER TABLE pipeline_settings ADD COLUMN var_group_ids … DEFAULT ('[]')`
  （MySQL 侧那个括号是表达式默认值 —— LONGTEXT 不允许普通 DEFAULT）。
- 0092：只有 `CREATE TABLE IF NOT EXISTS library_commands`。

**没有改列、没有回填、没有删列、没有重建表**，不存在破坏性升级；存量项目的 `service_id` 为空
即「未归属」，存量流水线 `var_group_ids` 为空即「不引用任何组」，行为与升级前一致。

## 提交拆分

```
9c1402e feat(服务): 归属服务目录,项目与运行可按业务系统归类
0193f39 feat(复用库): 变量组可被流水线引用,新增可复用的命名指令
31c9bc0 feat(界面): 归属服务筛选、归属服务管理与按服务批量执行
ab994fe feat(界面): 流水线可引用复用库变量组,部署节点可选用命名指令
6dac278 chore(前端): 重新生成 dist 入口,指向本次打包产物
```

拆分时的两处讲究：
- `cmd/pipewright/main.go` / `router.go` / `audit.go` 三个装配文件被两个后端功能交叉改到，
  所以先把工作树收成「只含归属服务」跑一遍 `go build` + `go vet` + `go test`
  （servicecatalog / project / run / httpapi / store 全绿）再提交第一条，然后把复用库那批
  改动原样放回。中间提交能独立编译，不是只靠最后那条绿灯。
- `router.go` 里 `WithRoles` 的文档注释被上一版新加的 `WithServiceCatalog` 截断了（注释挂到了
  错误的函数上），这次一并修回。

## 发版实况（供下次对照）

- 五条 run 同起于 15:34Z（master push 的 `ci` 早 14s），**四条绿、一条红**：
  `ci`(master, 3m42s) / `release`(4m08s) / `aliyun-image`(6m57s) /
  `caddy-image`(**20m07s，仍是最慢的一条**) 全 `success`；
  `ci`(tag, run #55，3m45s) 红在 **backend** job 的 `go test . ./cmd/... ./internal/...`
  这一步（frontend / mem-budget 两 job 都绿）。
- **这条红按偶发处理，不是代码问题**：同一 commit 6dac278 的另一条 `ci`（master push）绿，
  本地 `go test -count=1 . ./cmd/... ./internal/...`（关缓存，最接近 CI 冷 runner）也绿；
  再把集成最重的五包（`httpapi` / `run` / `project` / `servicecatalog` / `build`）
  `-count=1` 连跑三轮，`FAIL|panic|DATA RACE` 零命中 —— 复现不出来。
  与 v0.16.0 记的那次是**镜像情况**（那回红的是 master push 那条、tag 那条绿）。
- 第 7 步硬证据：`draft=false`、`prerelease=false`、7 个资产（4 tar.gz + 2 windows zip +
  `checksums.txt`）；六个平台包逐行 `shasum -a 256 -c` 全 OK；解出的 darwin/arm64 二进制
  `--version` 自述 `0.19.0 (commit 6dac278, built 2026-10-30T15:33:48Z, go1.26.10, darwin/arm64)`。
  空目录里 `reset-admin-password --generate` 退出码 **2**、报出解析到的绝对路径，`ls` 里没有留下 `.db`
  （注意别用 `... | head; echo $?` 读退出码 —— zsh 里那是 `head` 的状态，实测会误报 0）。
  ghcr `latest` 与 `0.19.0` 两个子 manifest digest 相同（amd64 `b9fb75bb…` / arm64 `b5822b63…`），
  与 v0.18.0 的 `bc50f778…` / `7a044a9b…` 不同；ghcr 镜像里 `--entrypoint /pipewright … --version`
  同样自述 `0.19.0 (commit 6dac278, linux/arm64)`。
  ACR 四标签到位：`0.19.0` = `latest` = `sha256:ec70d958…`（amd64 `0b486f69…` + arm64 `4354a56e…`），
  `0.19.0-docker` = `latest-docker` = `sha256:dead24ed…`（amd64 `f7183d71…` + arm64 `72404f6c…`）。
- **Release 正文里的安装示例抽验过了**：`ghcr.io/fuuhoo/pipewright:0.19.0` 与
  `registry.cn-qingdao.aliyuncs.com/fubin/pipewright:0.19.0` 都**不带前导 v**（`release.footer`
  用的是 `{{ .Version }}`），两个 tag 实测都存在；`releases/latest` 已指向 v0.19.0，
  也就是界面「检查更新」读到的那一条。
  「检查更新」这一步本次**没有用脚本点**：`/api/version` 要登录态，而拿真实账号跑浏览器登录不在
  允许范围内（临时实例 :18080 上验过功能，但那是临时库）—— 用户在 8080 上点一次即可，
  读到 `releases/latest` 就是 v0.19.0 已由 API 侧证实。
- **构建环境镜像这次不用动**：`git diff v0.18.0..v0.19.0 -- internal/buildenv/` 为空，
  内置目录没引新上游镜像。
- **两条环境事实（下次发版前先处理）**：
  1. `~/github/pat.txt` 仍是 `Bad credentials`(401)，所以 `actions/jobs/{id}/logs` 匿名 403
     拿不到日志 —— **红在哪一个测试用例无法取证**；`POST /actions/runs/{id}/retry` 同样要 token，
     重跑只能由用户在网页上点。仓库公开的 `runs?head_sha=` 与 `releases/*` 接口匿名可用，
     本次取证全靠这两条 + `docker buildx imagetools inspect`。
  2. 匿名额度 60/h 在 15:39Z 就被轮询打满，等到 ~16:09Z 重置才继续；这次先按 5min 一次的低频
     取，剩下的额度留给资产与日志。**别把「查不到」当成「没发出去」** —— ghcr / ACR 侧的
     digest 与镜像自述版本不受这个额度影响，是最早拿到的硬证据。

