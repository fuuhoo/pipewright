# v0.20.0 发布清单

> 发布日：2026-11-03
> **容器「上次退出」查死因 + 主机卡片置顶/拖动排序（每用户偏好，迁移 0093）+ 流水线步骤编辑（源码入口、步骤间插入、复制/高级复制）**

## 功能与改动

### A: 源节点「查看源码」出口 + 首次种子的仓库镜像（提交 99b28d8）
- **问题**: 源节点的仓库/分支/凭据是项目配置的只读镜像 —— 用户在抽屉里既改不动值，也无从查证
  「这次拉的到底是哪份代码」。
- **改动**:
  - 仓库那一格右侧加一个出口，跳平台内的代码浏览页（新标签）。地址走
    [router/index.ts:344](file:///Users/cw/code/pipewright/web/src/router/index.ts#L344) 的
    `projectCodeHref`（`router.resolve` 派生），**不在组件里手拼** `/projects/{id}/code` ——
    手拼等于把路由表抄第二遍，路径一改就悄悄 404。
  - 出口挂在 schema 上：[jobConfigSchema.ts](file:///Users/cw/code/pipewright/web/src/components/pipeline/jobConfigSchema.ts)
    给只读档新增 `readonlyLink` / `linkLabel` / `linkTitle` 三个可选键，值取不到（项目未绑仓库）
    就不渲染 —— 没有仓库就没有源码可看，给一个点了报错的入口不如不给。
  - `projectId` 沿 `ProjectPipeline → PipelineCanvas → JobDrawer` 透传，与既有的
    `environments` / `channels` 透传同一条链，没有新加 provide/inject。
  - **顺带修这条路径上的一个谎**：[pipeline.go:208](file:///Users/cw/code/pipewright/internal/pipeline/pipeline.go#L208)
    把 `Get` 的两条路径（已有配置 / 首次种子）收敛到同一个 `healSourceView`。以前只有「已有配置」
    那条做仓库镜像回写，首次访问把种子原样返回，于是新项目源节点第一屏写着「项目未绑定仓库」，
    刷新一次才对得上，挂在旁边的源码出口也跟着消失。
- **验证**: `JobDrawer.sourceLink.test.ts` 钉出口渲染条件与 `href`；
  `pipeline_test.go` 的 `TestGetLazyDefaultCarriesSourceMirror` 钉首次 `Get` 就带上 `repoUrl` / `branch`。

### B: 步骤编辑：悬浮显形、步骤间「+」插入、复制分两种写法（提交 6e65648）
- **改动**:
  - 「+ 加一步」的入口改成只在那一条悬停/聚焦时显形；两条步骤之间另给一条「+」，点开出类型菜单，
    插入位置由点的那条缝决定 —— 以前只能「加到最后再拖」。
  - 复制拆成 **复制**（一行 `cp`，`mkdir -p 目标 && cp -a 源 目标`）与 **高级复制**（rsync 优先，
    可排除）。以前只有一种形态：选了带排除项的表单、镜像里又没装 rsync 时，排除项会静默不生效，
    用户以为排除生效了。现在「复制」这条压根不给排除项（提示与反解析判据都写明），要排除必须
    明确切到「高级复制」。
  - 编译与反解析两头都认这两个标签（[stepCompile.ts](file:///Users/cw/code/pipewright/web/src/components/pipeline/stepCompile.ts)）：
    存量 spec 里的旧写法读出来仍归到「高级复制」，不会因为改名把人已有流水线变成空表单。
- **验证**: `StepBuilder.insert.test.ts`（插入位置与类型菜单）、`stepCompile.test.ts`（两种写法的编译/反解析往返）、
  `JobDrawer.stepBuilder.test.ts` 适配新 props；8 locale 补 `sbInsert*` / `sbCopyPlain*` / `stepCopyPlainLabel`，
  i18n 三门禁（keyParity / componentKeys / messageCompile）过。

### C: 容器详情「上次退出」区块（提交 af04942）
- **问题**: 容器一直重启时，日志一翻就滚过去、退出码又被新一轮拉起冲掉 ——
  「它到底为什么重启」在界面上无处可查。
- **改动**:
  - 后端 [container_inspect.go:71](file:///Users/cw/code/pipewright/internal/httpapi/container_inspect.go#L71)
    把 `exitCode` / `oomKilled` / `stateError` / `startedAt` / `finishedAt` / `restartCount`
    连同重启策略与 `restartMaxRetry` 一起透出（以前只有一个 `state` 字符串）。
    `restartMaxRetry` **只在 `on-failure` 有意义**，其它策略下恒为 0，字段注释里写明了别让前端误用。
  - 前端判读集中在 [containerExit.ts:21](file:///Users/cw/code/pipewright/web/src/lib/containerExit.ts#L21)
    的 `lastExitView` 与 [:31](file:///Users/cw/code/pipewright/web/src/lib/containerExit.ts#L31) 的 `exitMeaning`：
    137 → 「OOM 或 SIGKILL；`OOMKilled=false` 则多半是外部 kill」（**不把 137 说成确定 OOM**）、
    143 → SIGTERM、`128+n` → 拆成信号名、1 → 应用自己报错。
  - **docker 的脾气写在代码里**：容器一被重新拉起，`ExitCode` 归 0、`OOMKilled` 归 false、
    `State.Error` 清空，只有 `FinishedAt` / `RestartCount` 留着 —— 所以运行中的容器这一栏仍有内容，
    而「刚崩的那一下」得靠及时读。
  - **inspect 原文可能带密钥**：后端只挑白名单字段往外给，且这一路的响应一条都不落日志。
- **验证**: `containerExit.test.ts`（7 条判读口径）+ `ContainerInspectModal.lastExit.test.ts`；
  `internal/httpapi/container_inspect_test.go` 钉字段透出与截断。

### D: 主机卡片置顶与拖动排序（提交 13e637b，迁移 0093）
- **为什么按用户存**：顺序和置顶是**看的人**的习惯，不是主机的属性。若在 `servers` 上加
  `pinned` / `sort_order` 两列，一台机器会因某个人拖了一下就对管理员也跟着靠后，而且每次增删改主机
  都得顺带维护它 —— 把「我的界面习惯」和「谁都能改的资产数据」搅在同一行。
  新表 [0093_server_view_prefs.sql](file:///Users/cw/code/pipewright/internal/store/migrations/sqlite/0093_server_view_prefs.sql)
  一人一行，只存 id 数组：主机改名/换地址不影响，被删的残留 id 读取时忽略、下次保存自然消失，
  所以不需要额外清理任务。**不设外键**（与 `api_tokens` 同口径），删号由
  [users.go:773](file:///Users/cw/code/pipewright/internal/users/users.go#L773) 的事务显式清。
- **后端**: [serverpref.go:54](file:///Users/cw/code/pipewright/internal/serverpref/serverpref.go#L54) `Get` /
  [:73](file:///Users/cw/code/pipewright/internal/serverpref/serverpref.go#L73) `Save`（整份覆盖、幂等、
  入库前 `normalizeIDs` 去空去重）；两端点都是「本人」的，写在会话所属 user_id 上，
  因此**不需要 access 判定、也不记审计**（界面习惯不是资产变更，记进审计只会淹掉真信号）。
  GET 只读 → 豁免 CSRF；PUT 由 `requireCSRF` 统一要 `X-CSRF-Token`。没设过的人拿到空偏好，不是 404。
- **前端排序**：[serverViewOrder.ts:31](file:///Users/cw/code/pipewright/web/src/lib/serverViewOrder.ts#L31)
  `rankHosts` 用四级键 —— 置顶档 → 手动顺序下标 → 置顶数组下标 → 入参下标（稳定）。
  没设过偏好时原样返回，「在线在前、离线沉底」这条默认口径不动；一旦拖过，手动顺序压过每轮刷新
  的自动沉底（人要的正是「别自己挪」，这是**主动偏离**，注释与测试都钉住了）。
  `moveHostId` 写的是**整份主机名单**的顺序，所以搜索/分组正收窄时拖动，撤筛后顺序仍在；
  落点与原位相同直接 return，不会白白进入「手动顺序模式」。
- **交互取舍**（都是主动偏离，逐条说明）：
  - 握把常驻而非悬停显形 —— 悬停才显形的话没人会发现这一屏能排序。
  - 落点分目标卡的上下半 —— 卡片高度差极大，只认一个落点会点不准。
  - `setDragImage` 用整张卡片快照，而不是浏览器默认的半透明缩略。
  - 另给方向键排序（[ServerCard.vue:187](file:///Users/cw/code/pipewright/web/src/components/ops/ServerCard.vue#L187)），
    让 `tabindex=0` 的握把不只是「聚焦但没功能」。
  - 另给「恢复默认排序」出口，**只清 order、保留 pinned**。
  - 写入 350ms 防抖合并（[Containers.vue:108](file:///Users/cw/code/pipewright/web/src/views/Containers.vue#L108)），
    连点几张图钉不会打出一串 PUT；GET 偏好失败静默回落默认排序（这一屏主角是容器，
    进页面先报「偏好没读到」是噪音），只有 PUT 失败才弹提示。
  - 握把/图钉常态字色取 `--color-dim` 而不是更淡的 `--color-faint`，与同一张卡上的折叠键齐平 ——
    实测深底对比度从 3.72 提到 7.27、浅底 7.46（`--color-faint` 在浅底只有约 2.5，几乎与背景糊成一片）。
- **验证**: `serverViewOrder.test.ts` 18 条、`ServerCard.hostOrder.test.ts` 10 条（含拖自己不上报、
  非本 MIME 的拖动既不高亮也不上报）、`Containers.hostOrder.test.ts` 9 条（含防抖合并、筛着拖仍写全集、
  GET 失败静默）；`internal/serverpref` + `internal/httpapi/server_view_prefs_test.go` 钉两端点契约。
  :18080 一次性实例上用真实浏览器点过：图钉置顶、合成 `DataTransfer` 走完拖动、方向键 nudge、
  「恢复默认排序」、刷新后偏好仍在。

## 迁移

这一版有 **1 个迁移 ×2 方言**（0093），老库要跟着升：

```bash
git diff --name-only v0.19.0..v0.20.0 -- internal/store/migrations
# 期望 2 个文件:sqlite/0093_server_view_prefs.sql 与 mysql/0093_server_view_prefs.sql
```

形态核过（`grep -iE "drop |rename|modify|change column|update .* set"` 对两个文件零命中）：
只有一句 `CREATE TABLE IF NOT EXISTS server_view_prefs`。**没有改列、没有回填、没有删列、没有重建表**，
不存在破坏性升级 —— 新表为空即「没人设过偏好」，前端据此回落默认排序，行为与升级前一致。
MySQL 侧那两个 `DEFAULT ('[]')` 的括号是表达式默认值（LONGTEXT 不允许普通 DEFAULT），
与 0091 同一写法。

## 提交拆分

```
99b28d8 feat(流水线): 源节点仓库那格加「查看源码」出口,首次种子的镜像回写补上
6e65648 feat(流水线): 步骤条悬浮显形、步骤间「+」插入,复制分「复制/高级复制」两种写法
af04942 feat(容器): 详情抽屉加「上次退出」区块,重启中的容器也看得见死因
13e637b feat(容器页): 主机卡片可置顶与拖动排序,偏好按人各存一份(迁移 0093)
4b2ec57 chore(前端): 重新生成 dist 入口,指向本次打包产物
```

拆分时的一处讲究：`web/src/i18n/locales/*/pipelineJob.ts` 一个文件里同时躺着三个功能的键，
而 `StepBuilder.vue` 的改动横跨「悬浮显形/插入条」与「复制两种写法」。所以按**能独立通过检查**的
最小粒度切：源码入口那 2 个键用 `git apply --cached` 单独挑进第一条提交（8 个 locale 各一个 hunk），
步骤那两个功能合成一条 —— 它们是同一个编辑界面的同一轮改动，硬拆只会造出一条编译不过的中间提交。

## 发版实况（供下次对照）

- **五条 run 一次全绿**：`ci`(master push) 3m58s、`ci`(tag) 4m04s、`release` 4m18s、`aliyun-image` 4m42s、
  `caddy-image` **14m38s**（仍是最慢的一条，但比 v0.19.0 的 20m07s 快）。
  打 tag 那条 `4b2ec57`，release #20 / aliyun-image #19 / caddy-image #20 / ci #57（tag）/ ci #56（master）。
- **第 7 步硬证据**：
  - `draft=false`、`prerelease=false`、7 个资产（4 tar.gz + 2 windows zip + `checksums.txt`），
    六个平台包从 `github.com/.../releases/download/v0.20.0/` 直下后逐行 `shasum -a 256 -c` 全 OK。
  - ghcr `latest` 两个子 manifest digest（`2f807ae4…` / `b209b6bf…`）与 `0.20.0` **相同**、
    与 `0.19.0`（`b9fb75bb…` / `b5822b63…`）不同；`0.20.0` 清单列出 amd64 + arm64。
  - ghcr 镜像里的二进制自述 `pipewright 0.20.0 (commit 4b2ec57, built 2026-11-03T02:56:25Z, go1.26.10, linux/amd64)`
    —— commit 就是打 tag 那条，不是 `dev`。
  - ACR 四标签（本机 `docker login` 仍有效，直接 `docker buildx imagetools inspect` 取证）：
    `0.20.0` = `latest` = `sha256:b69f88c7…`，`0.20.0-docker` = `latest-docker` = `sha256:0263909b…`，
    两条都是 amd64 + arm64，且都与 `0.19.0` / `0.19.0-docker` 不同 —— latest 确实切了。
  - 随车 Caddy：`pipewright-caddy:latest` = `v0.20.0` = `sha256:c13203ac…`，`v0.19.0` 是 `02f2a613…`。
  - 发布产物抽验：darwin/arm64 包里的二进制 `--version` = `0.20.0 (commit 4b2ec57)`；
    空目录里 `reset-admin-password --generate` 退出码 **2**、报出解析到的绝对路径，`ls` 里没有留下 `.db`。
  - 安装示例抽验：Release 正文由 `release.footer` 生成，两条 `docker run` 分别写
    `ghcr.io/fuuhoo/pipewright:0.20.0` 与 `…/fubin/pipewright:0.20.0`（都不带 `v`），
    上面两条 manifest 取证正好证明这两个 tag 拉得到。
- **一处没按文档走完的**：界面「检查更新」没点成本地实例 —— 8080 / 8081 跑的是本地无 `-ldflags` 的构建，
  `/version` 自述 `dev`，点它只会得到「有更新」而不是「已是最新」，证明力有限。改用检查更新的**降级同一条路径**取证：
  `curl -I github.com/fuuhoo/pipewright/releases/latest` 的 302 落在 `tag/v0.20.0`（`internal/version/update.go`
  的 `resolveLatestViaRedirect` 读的就是这个 Location），且这条不占 API 额度。
- **两条环境事实**：① `~/github/pat.txt` 那条 PAT 仍是 401（连续第四次），本次全程匿名 API + 产物级取证，
  没取任何 job 日志（也不需要 —— 没有红任务）。② 匿名 API 额度 60/h 在轮询中途打满，复位前
  `runs?head_sha=` 返回的是限流 JSON。
- **这次踩到的一条脚本坑（下次照改）**：盯跑脚本的收尾判据写成 `grep -c "in_progress\|queued" == 0`，
  而额度打满时 `jq` 拿不到 `.workflow_runs` → 状态文件被 `>` **清空** → `grep -c` 得 0 → 脚本误报「全部结束」，
  还把 `== 全部结束 ==` 打成日志。空表和跑完是两种状态，判据得先确认「文件里确实有 5 行且每行都 completed」，
  额度复位后再重查一次才算数。
- **实例刷新**：8080 / 8081 都已在服 `index-C1pQmfzA.js`，与入库的 `web/dist/index.html` 一致。
