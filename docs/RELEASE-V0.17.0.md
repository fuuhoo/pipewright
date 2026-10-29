# v0.17.0 发布清单

> 发布日：2026-10-29
> **反代配置落卷并可整体重下发 + 变量组可归多个分组 + 构建阶段改为按需添加 + 环境入口隐藏**

## 功能与改动

### A: 反代 Caddyfile 落命名卷，新增「重新下发」（提交 299b654）
- **问题**: Caddyfile 原来只写在容器可写层（`docker cp`）。容器被删/重建（升级镜像、`docker prune`、
  主机重启后掉容器）配置就没了，而库里路由还是「已启用」——站点静默 502，直到下一次路由变更才补推。
- **改动**:
  - [caddy.go:35](file:///Users/cw/code/pipewright/internal/proxy/caddy.go#L35) 新增命名卷 `pipewright_caddy_etc`
    挂到容器 `/etc/caddy`（[caddy.go:362](file:///Users/cw/code/pipewright/internal/proxy/caddy.go#L362)）。
  - [caddy.go:447](file:///Users/cw/code/pipewright/internal/proxy/caddy.go#L447) `inspectCaddyConfigMount`
    检查既有容器有没有挂这个卷；`ensureCaddy` 发现没挂（老部署）就一次性重建容器迁到卷上，
    随后由 `apply` 把库里的 Caddyfile 重新落下。
  - 新增 `POST /api/proxy/caddy/reapply?serverId=<id>`（[proxy.go:639](file:///Users/cw/code/pipewright/internal/httpapi/proxy.go#L639)，
    路由 [router.go:1159](file:///Users/cw/code/pipewright/internal/httpapi/router.go#L1159)）：
    `ensureCaddy` → 拉起容器 → 逐个 `docker network connect` 接回上游容器 → `apply` reload。
    幂等、不改库，过 auth + CSRF，审计动作 `proxy.caddy.reapply`
    （[proxy.go:31](file:///Users/cw/code/pipewright/internal/httpapi/proxy.go#L31)）。
  - [proxy.go:695](file:///Users/cw/code/pipewright/internal/proxy/proxy.go#L695) `Reapply`；
    [proxy.go:727](file:///Users/cw/code/pipewright/internal/proxy/proxy.go#L727) `upstreamContainers`
    收集主上游 / 路径规则 / 额外上游 / TCP 透传引用到的全部容器，`address` 类主上游跳过。
  - 面板加「重新下发」按钮（`ReverseProxyPanel.reapply.test.ts`）。
- **验证**: 删掉目标机上的 `pipewright-caddy` 容器 → 点「重新下发」→ 容器重建、配置从卷回来、
  站点恢复；已有挂卷的正常部署再点一次 → 无副作用（reload 幂等）。

### B: 上游支持「地址」形态，一键分配子域名并入启用表单（提交 299b654）
- **问题**: 反代路由的主上游只能填容器名。指向宿主上非容器进程（如自装的服务、宿主反代再回落）
  就得先手建一个容器别名；「一键分配子域名」又是另一个独立组件，启用一条路由要开两处。
- **改动**:
  - 上游类型 `upstreamKind`：`""`/`container`（默认，填容器名）或 `address`（填 `HOST:PORT`）
    （[dnsprovider.go:216](file:///Users/cw/code/pipewright/internal/dnsprovider/dnsprovider.go#L216)，
    透传 [service.go:37](file:///Users/cw/code/pipewright/internal/dnsprovider/service.go#L37)）。
    `address` 上游不做 `docker network connect`（[proxy.go:754](file:///Users/cw/code/pipewright/internal/proxy/proxy.go#L754)）。
  - 一键分配子域名（DNS 提供商 + 托管域）并入「启用反代」表单，`InstantSubdomain.vue` 与其测试删除，
    逻辑与样式合入 `ReverseProxyPanel.vue`（`ReverseProxyPanel.subdomain.test.ts`）。
  - 设置 → 系统里的镜像地址输入框改单列大输入框（`.rt-input--wide` 后置覆盖固定宽度）。
- **验证**: 选「地址」上游填 `127.0.0.1:3000` → 路由渲染 `reverse_proxy 127.0.0.1:3000`、不接容器网络；
  在启用表单里勾选自动子域名 → 建路由 + 建 DNS 记录一步完成；容器上游行为不变。

### C: 变量组可归多个资源分组（迁移 0088，提交 6801e7a）
- **问题**: 变量组是「跨项目共享」的纯功能轴资源，只要角色有 `library.edit` 就能改。一旦组里装着
  某几个项目的密钥引用，「谁能看、谁能改这一份」也该由分组来切，而模型上没有落点。
- **改动**:
  - 迁移 [0088_variable_group_groups.sql](file:///Users/cw/code/pipewright/internal/store/migrations/sqlite/0088_variable_group_groups.sql):
    建 `variable_group_groups(variable_group_id, group_id, created_at)` 成员表。
    **存量变量组不落行 = 未归组**（与升级前「全员可见可改」行为一致，无需回填）。
  - [access.go:59](file:///Users/cw/code/pipewright/internal/access/access.go#L59) 新增 `KindVarGroup`：
    没有自己的功能点，功能轴沿用项目那一档（[roles.go:69](file:///Users/cw/code/pipewright/internal/access/roles.go#L69)）；
    数据轴与服务器逐条同构 —— View/Operate 取**并集**、Manage 取**交集**
    （[access.go:236](file:///Users/cw/code/pipewright/internal/access/access.go#L236) `Can`），
    列表用 `MemberClause` 把「未归组 = 成员表没有行」编译成 `NOT EXISTS`
    （[access.go:495](file:///Users/cw/code/pipewright/internal/access/access.go#L495)）。
  - [vargroup.go:44](file:///Users/cw/code/pipewright/internal/library/vargroup.go#L44) `GroupIDs`；
    建/改支持 `groupIds`（PUT 省略 = 不动归属，空数组 = 移出所有分组），
    [vargroup.go:102](file:///Users/cw/code/pipewright/internal/library/vargroup.go#L102) 列表按可见范围收敛，
    [vargroup.go:335](file:///Users/cw/code/pipewright/internal/library/vargroup.go#L335) `attachGroups` 批量补齐。
  - `internal/group`：组列表带 `VarGroupCount`，删组前按「仍被变量组引用」拒绝
    （[group.go:395](file:///Users/cw/code/pipewright/internal/group/group.go#L395)），与服务器同一口径。
  - HTTP 契约 `groupIds`（[variable_groups.go:37](file:///Users/cw/code/pipewright/internal/httpapi/variable_groups.go#L37)），
    单条由 `accessGuardMiddleware` 判归属，归组本身要求 `ActManage`；
    引用不存在的组 → 422 `group_not_found`。
  - 前端：复用库列表按权限收敛，多分组选择器 + 分组标签（`Library.vue`、`api/groups.ts`、`api/variableGroups.ts`）。
- **验证**: 变量组挂 A、B 两组 → A、B 组成员都能看到并引用；只有 A 组 Manage 的人改组归属 → 被拒；
  未勾任何组 = 未归组，维持全员可见；删仍被变量组引用的分组 → 被拒并提示先移出。

### D: 构建阶段改为按需添加，阶段列可拖拽排序（提交 4f17a19）
- **问题**: 默认流水线预置了一张空的「构建」列。只部署、不打包的项目每次都要先删掉它；
  反过来说，构建只是众多阶段中的一种，不该是必经环节。
- **改动**:
  - [pipeline.go:539](file:///Users/cw/code/pipewright/internal/pipeline/pipeline.go#L539) `defaultSpec`：
    绑了仓库的项目只有源阶段，构建/部署/通知一律不预置；未绑仓库仍只有部署阶段。
  - 画布「+添加阶段」先选种类（构建 / 部署 / 通知 / 自定义）再落列
    （[PipelineCanvas.vue:302](file:///Users/cw/code/pipewright/web/src/components/pipeline/PipelineCanvas.vue#L302) `ADDABLE_KINDS`）。
  - 阶段列可拖拽换序：拖动 MIME `application/x-pipewright-stage`，
    [stageOrder.ts:8](file:///Users/cw/code/pipewright/web/src/components/pipeline/stageOrder.ts#L8) `moveStage`
    把源阶段钉在首位（源列不可拖，落在源列前收口到源列之后）。
- **验证**: 新建绑仓库项目 → 画布只有源阶段；添加「部署」→ 两列命名正确；拖动阶段换序后保存重开顺序保持；
  源列拖不动、也无法把别的列拖到源列之前。

### E: 隐藏「环境」概念入口，PR 预览环境配置保留（提交 9ad7116）
- **背景**: 环境的目标服务器只是声明式元数据，实际部署目标来自部署节点自身的服务器配置；
  环境真正驱动的只有「按名解析镜像仓库」与「部署历史分组」。在补齐语义前先收敛入口。
- **改动**:
  - 去掉侧栏「环境」页、首页环境卡片、运行详情的环境晋级面板、项目的「环境与凭据」tab
    （[AppShell.vue](file:///Users/cw/code/pipewright/web/src/layouts/AppShell.vue)、
    [Dashboard.vue](file:///Users/cw/code/pipewright/web/src/views/Dashboard.vue)、
    [RunDetail.vue](file:///Users/cw/code/pipewright/web/src/views/RunDetail.vue)、
    [ProjectPipeline.vue](file:///Users/cw/code/pipewright/web/src/views/ProjectPipeline.vue)）。
  - 路由、组件、接口、表与数据原样保留（`/environments` 直链仍可用），后续可原路恢复。
  - 触发设置里的「PR 预览环境」配置卡与 `/previews` 预览环境页继续保留。
  - 校验面板的 `envs` 作用域不再跳转（对应 tab 已隐藏）。
- **验证**: 侧栏无「环境」；首页不再有环境卡片；运行详情无晋级面板；项目页三个 tab；
  触发设置里 PR 预览环境配置仍可保存；`/environments` 直链仍能打开。

### F: 容器列表轮询放宽到 60 秒（提交 79896d0）
- **问题**: 容器聚合页每 12 秒逐台 SSH 跑一次 `docker ps`，多机场景下开销偏大。
- **改动**: [Containers.vue:97](file:///Users/cw/code/pipewright/web/src/views/Containers.vue#L97) `POLL_MS`
  12_000 → 60_000；卡片内翻页与本地筛选本就不随轮询重建。
- **验证**: 打开容器页 → 60 秒一次刷新；翻页/筛选在两次刷新之间保持。

## 迁移

这一版只有 **1 个迁移 ×2 方言**（0088）：

```bash
git diff --name-only v0.16.0..v0.17.0 -- internal/store/migrations
# 期望 2 个文件:0088_variable_group_groups ×(sqlite,mysql)
```

形态已核：0088 只有 `CREATE TABLE IF NOT EXISTS` + `CREATE INDEX IF NOT EXISTS`，
**没有改列、没有回填、没有删列** —— 存量变量组自动保持「未归组」，行为与升级前一致，
这一版不存在破坏性迁移，降级也不需要额外处理（0088 的表留着不影响旧版本读取）。

## 发版实况（供下次对照）

- 五条 run 同起于 14:07Z，**一次全绿**：`ci`(tag, 3m18s) / `release`(5m19s) / `aliyun-image`(3m58s) /
  `caddy-image`(14m39s,仍是最慢的一条) / `ci`(master, 2m27s)。
- 第 7 步硬证据：`draft=false`、`prerelease=false`、7 个资产（4 tar.gz + 2 windows zip + `checksums.txt`）；
  `shasum -a 256 -c` 对 darwin_arm64 资产逐行 OK；解出的二进制 `--version` 自述
  `0.17.0 (commit 47595ce, built 2026-10-29T14:06:23Z, go1.26.10, darwin/arm64)`。
  ghcr `latest` 与 `0.17.0` 的两个子 manifest digest 相同（amd64 `92db76a6…` / arm64 `2a86c3f9…`），
  与 v0.16.0 的 `0b7ca7de…` / `63bb4a25…` 不同；镜像里 `--entrypoint /pipewright … --version`
  同样自述 `0.17.0 (commit 47595ce, linux/arm64)`。
  ACR 四标签均列出 amd64 + arm64，`0.17.0` = `latest`（`71b988f1…` / `870ea2d4…`），
  `0.17.0-docker` = `latest-docker`（`49cf4405…` / `f0a2a3d1…`）。
- 提交拆分：299b654 反代 → 6801e7a 变量组 → 4f17a19 流水线 → 9ad7116 环境入口 → 79896d0 轮询 → 47595ce dist。
- 本机 `web/dist` 入口 hash 仍为 `index-BIsaRlnz.js`（重建后字节一致）。
