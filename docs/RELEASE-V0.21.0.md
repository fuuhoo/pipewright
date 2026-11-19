# v0.21.0 发布清单

> 发布日：2026-11-05
> **项目卡摘要补齐（最近运行状态 + 部署目标）+ 运行日志尾部窗口与全部下载 + 工作区快照轻量清单**

## 功能与改动

### A: 项目卡摘要补齐最近运行状态与部署目标（提交 2db6895）
- **问题**: `lastRunStatus` / `targetServers` 自项目卡诞生起就是占位（`null` / `[]`），卡片恒显
  「尚无运行」—— 字段在契约里挂了名，数据从来没被填过。
- **改动**:
  - [project.go](file:///Users/cw/code/pipewright/internal/project/project.go)：列表与单条查询各带一个
    **标量子查询**取该项目最近一条 `pipeline_runs` 的状态；`TargetServerIDs` 从 `spec_json` 解析
    （配置口径：没跑过也显示）。`ListPaged` 批量读 spec 前**显式 `rows.Close()`** —— sqlite 连接数有限，
    游标没关就发第二条查询会自己把自己堵死。
  - 判点收敛：[job_types.go](file:///Users/cw/code/pipewright/internal/pipeline/job_types.go) 新增
    `JobTypeUsesServerTargets`，保存期校验 `validateDeployTargets` 与卡片摘要共用同一份清单；
    `deploy_k8s` 直连集群 API server、不勾主机，**故意不在此列**。收集走 `SpecDeployServerIDs`
    （去重、按声明顺序）。
  - [projects.go](file:///Users/cw/code/pipewright/internal/httpapi/projects.go)：按请求可见分组把
    落点 id 换成机器名，**不在可见分组或已删的机器宁可少报**，不把别人名下的机器名报出去；
    PATCH 响应用 `bestEffortServerNames`，分组读不出来不让一次已经成功的更新变成 500。
  - 前端顺手修一个谎：`api/projects.ts` 自造的中文 `RunStatus` 联合类型与后端返回的 snake_case
    对不上，**项目页状态筛选选任何一档都是空列表**。现在从 `api/runs.ts` re-export 同一套词；
    `runStatus.ts` 简化成直查；`ProjectCard` 色板补 `waiting_approval`；`Projects.vue` 筛选档位
    改用规范词。
- **验证**: `job_types_test.go`（判点与收集）、`projects_test.go`（子查询与机器名换算）、
  `ProjectCard.test.ts`（`waiting_approval` 色板）、`ProjectCard`/`Projects` 相关 vitest。

### B: 运行日志改尾部窗口取数，新增全部日志下载（提交 393919f）
- **问题**: 几万行的长跑把整条日志一次性搬进前端，主线程卡死在渲染上；日志也带不走。
- **改动**:
  - [runs.go](file:///Users/cw/code/pipewright/internal/httpapi/runs.go)：`GET /runs/{id}/logs` 新增
    `?tail=<n>[&beforeSeq=<seq>][&step=<n>]` 尾部窗口模式 —— 只回**最近** n 行（仍升序），带
    `firstSeq` / `hasEarlier` / `total`（指针语义：全量模式不给，省得前端把「没算」误读成 0）；
    「显示更早」拿 `beforeSeq` 往上翻页。`step` 是步骤筛选，**在后端裁好再搬**（选中某步只取那步的
    一屏，不是下完整条再在前端筛）；`step` 只与 `tail` 同用（400 `step_requires_tail`）。
    全量 `sinceSeq` 口径不动。
  - [service.go](file:///Users/cw/code/pipewright/internal/run/service.go)：`GetLogsTail`
    （beforeSeq / n / step），[bus.go](file:///Users/cw/code/pipewright/internal/run/bus.go) 加
    `LogTail{Lines, Total}`。
  - 新增 `GET /runs/{id}/logs/download`：**逐行流式**落成 .txt，不在内存里聚合全量；权限走
    `/runs/{id}/…` 既有口径 —— `resolveGuardTarget` 只认前三段，深一段的 GET 自动落 `act_view`，
    与产物下载同理，不用额外接线。
  - 前端：`RunTerminal` 默认取最近一屏（`LOG_TAIL_WINDOW`），`runLogBuffer` 支持 prepend 向上拼页；
    「下载全部日志」按钮走 `runLogsDownloadUrl`（会话 Cookie + `<a download>`）；i18n 八语言同步。
- **验证**: `logs_test.go`（尾部窗口/翻页/step 筛选）、`run_logs_test.go`（tail 参数校验与指针语义）、
  `runLogBuffer.test.ts`（prepend）、`RunTerminal.test.ts`（窗口取数与下载入口）。

### C: 工作区快照改轻量清单，点开弹窗才拉全量（提交 0e00471）
- **问题**: 一份快照的目录清单上限 3000 条、单份可达 180KB，运行详情页又有多个构建节点 ——
  开页全摊开就是把卡顿留给前端。
- **改动**:
  - [store.go](file:///Users/cw/code/pipewright/internal/buildsnap/store.go)：新增 `ListMetaByRun`，
    同排序同字段但**不读 `entries_json`**；`entry_count` 列 0081 起就在表里，只是从来没被读过。
    全量读法 `ListByRun` 口径不变。
  - [run_snapshots.go](file:///Users/cw/code/pipewright/internal/httpapi/run_snapshots.go)：
    `GET /runs/{id}/snapshots?meta=1` 走轻量清单，`entries` 恒空、只给 `entryCount`。
  - [WorkspaceSnapshotPanel.vue](file:///Users/cw/code/pipewright/web/src/components/run/WorkspaceSnapshotPanel.vue)：
    改弹窗承载 —— 开页只占一条触发条（哪几个节点留了现场、为什么留、产物路径命中没有、各多少条），
    点开才取全量并缓存。**404 / 503 等价于「这里没有东西」静默**；其它错误如实报加载失败，
    不把失败谎报成没有快照。
  - [workspaceSnapshot.ts](file:///Users/cw/code/pipewright/web/src/lib/workspaceSnapshot.ts) 删掉
    `countNodes`：条数不再在前端数树 —— 触发条用后端 `entryCount`，弹窗里「命中 N / 共 M 条」两个数**同源数清单条目**
    （树里补出来的中间目录不计入命中）。实测原来会写出「命中 2734 / 共 2602 条」这种比总数还大的话；
    截断提示的单位同时改成「行」（zh-CN/zh-TW），否则 400 + 2334 和 2600 对不上。
- **验证**: `store_test.go`（meta 读法不搬 entries、entryCount 正确）、`run_snapshots_test.go`
  （`?meta=1` 响应形状）、`WorkspaceSnapshotPanel.test.ts`（触发条/弹窗加载/错误语义）。

## 迁移

**零迁移**（`git diff --name-only v0.20.0..v0.21.0 -- internal/store/migrations` 为空），老库直接升。
`entry_count` 列 0081 已存在，本版只是开始读它。

## 版本号

新功能、接口与数据向后兼容 → **minor**：v0.20.0 → **v0.21.0**。

## 发版实况

- **推送与 tag**:tag `v0.21.0`(标注对象 `f9cc837`)指向 `8c2dee8` 且已在 origin(`git ls-remote` 复核),tag 没有移动过。
  tag 之后 master 又前进了两条(`eb176e4` 项目卡上次运行改口径、`9f20a04` 重新生成 dist 入口),**这两条不在 v0.21.0 里**,
  留给下一版。
- **第 7 步硬证据(全部是产物级,不靠绿灯)**:
  - GitHub Release `v0.21.0` 资产 7 个 = 四平台 `tar.gz` + 两个 windows `zip` + `checksums.txt`;
    六个包本机 `shasum -a 256 -c checksums.txt` **逐行 OK**。
  - `releases/latest` 的 302 落在 `/releases/tag/v0.21.0` —— 这条同时证明它**既不是草稿也不是预发布**
    (`internal/version/update.go` 的 `resolveLatestViaRedirect` 读的就是这个 location,界面「检查更新」走同一条)。
  - ghcr:`latest` 与 `0.21.0` 同为 `sha256:eef50602…`,与 `0.20.0`(`sha256:4d9627e8…`)不同;清单列 `linux/amd64` + `linux/arm64`。
  - ACR(本机 docker 登录仍有效,用 `docker buildx imagetools inspect` 取证):
    `0.21.0` = `latest` = `sha256:7b8b592a…`,`0.21.0-docker` = `latest-docker` = `sha256:0aae1ec0…`,四标签均双架构;
    上一版 `0.20.0` = `sha256:b69f88c7…`,与本次不同 —— latest 确实切了。
  - 随车反代镜像 `pipewright-caddy:latest` = `v0.21.0` = `sha256:6f0671a6…`。
  - 发布产物里跑出来的版本自述:ghcr 镜像 `--entrypoint /pipewright --version` 与解包后的 darwin_arm64 二进制
    都报 `pipewright 0.21.0 (commit 8c2dee8, built 2026-11-05T00:38:35Z, go1.26.10, …)` —— commit 是打 tag 那条,不是 dev。
  - 空目录抽查发布二进制:`reset-admin-password --generate` 退出码 **2**、按绝对路径报出它解析到的库位置并劝退,
    目录里**没有留下 `.db`**。
  - Release 正文的安装示例写的是 `ghcr.io/fuuhoo/pipewright:0.21.0`(`.goreleaser.yaml` 用 `{{ .Version }}`,不带 `v`),
    该 tag 实测存在。
- **四条 workflow 的结论(10:30 CST 匿名额度复位后补上)**:`8c2dee840e3801be63bd834b1d124535b3a05719` 上共 5 条 run,
  全部 `completed / success` —— caddy-image #21(00:40:11→00:55:16,≈15m05s)、release #21(→00:45:48)、
  ci #59 tag(→00:43:40)、aliyun-image #20(→00:44:05)、ci #58 master(00:39:52→00:43:28)。
- **tag 之后 master 上有一条红**:`9f20a04` 的 ci #60(02:29:20Z→02:32:06Z)backend job 红在
  `go test . ./cmd/... ./internal/...`;本地 darwin 默认时区与 `TZ=UTC` 跑同一套包都是绿的,
  差的是 ubuntu runner —— 具体哪个用例要等 token 或额度复位看日志才能定,记在这儿别丢。
  本次发布判的是 tag 那条 sha 的 5/5 success,master 红不影响 v0.21.0 产物。
- **取证受限**:`~/github/pat.txt` 那条 PAT 仍是 `Bad credentials`(**连续第六次**),回退匿名查询才拿到上面的结论;
  `jobs/{id}/logs` 依旧取不到,所以本次没做逐行日志复核,判定依据是 run 汇总 + 上面那串产物级证据。
  坑记一笔:`head_sha` 查询**只认完整 40 位 sha**,传 `8c2dee8` 会返回空表,看着像「还没跑完」。
- **本地实例**::8080 已重编译重启并确认服务本次产物(`assets/index-CcNFwvJi.js` 与 `web/dist` 同哈希);
  :8081 的镜像重建头三次被 Docker Hub 对基础镜像元数据的 `429 Too Many Requests` 挡住,第四次(10:15)过了 ——
  现在两实例都取到同一个 `index-CcNFwvJi.js`,里面确实带着 `snapEntryTotal` 这一版的新文案。
  踩到的坑记一笔:`docker build … | tail -3 && docker compose … up` 里管道的退出码是 `tail` 的,
  构建失败照样把容器用**旧镜像**重建了一遍并报告成功;要么 `set -o pipefail`,要么先看 `build_exit` 再决定要不要 recreate,
  并且用 `docker images … {{.CreatedSince}}` 复核镜像确实是新的。
