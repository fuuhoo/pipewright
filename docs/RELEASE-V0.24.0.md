# v0.24.0 发布清单

> 发布日：2026-11-20
> **运行详情直达编排与源码 + 日志窗口收到 500 行 + 项目筛选跨菜单保留**

## 功能与改动

### A: 运行详情标题直达编排，项目一格加「查看源码」（提交 4b5fcfd）

- **问题**: 在运行详情里看到项目名，最自然的下一步是回该项目的编排页；以前只能绕回项目列表。
  源码入口则只有流水线配置的源节点里有，运行现场看不到。
- **改动**:
  - 标题里的项目名改成按钮，按 `project.view` 开出口；没有这个权限的角色仍显示纯文本，
    不制造点了会被守卫踢回的按钮。
  - 「项目」一格补「查看源码」，地址走 `router.resolve`（`projectCodeHref`，项目默认分支），
    `target="_blank"` 新标签打开站内代码页，与流水线配置源节点同一条链路。
  - 八个语言包补齐 `openPipelineHint` / `openPipelineAria` / `viewSourceLabel` / `viewSourceTitle`。
- **验证**: `:18080` 一次性实例登录后确认标题按钮可点、源码链接指向 `/projects/<id>/code` 且新标签打开；
  八个语言包 key 对齐由 `npm run test` 的 i18n 三网关守住。

### B: 运行日志尾部窗口收到 500 行，「显示更早」按页补（提交 3cb73fb）

- **问题**: 一屏挂 1500 行在长日志上仍会压住主线程，打开上万行的已结束运行仍能明显卡一下。
- **改动**:
  - `LOG_TAIL_WINDOW` / `LOG_WINDOW_STEP` 从 1500 收到 500：初始只挂最近 500 行，
    点一次「显示更早」补一页；计数继续用服务端 `total` 报「已加载 / 共」，不把整条日志谎报成一窗。
  - 测试里写死的 3000/1500 改为跟常量走，下次调窗口不用两处对数字。
- **验证**: `:18080` 一次性实例种 1200 行日志：首屏渲染 seq 701–1200，按钮「显示更早的 500 行」；
  第一次点击后 1000 行（201–1200），第二次点击后全量 1200 行、按钮消失，翻页时滚动位置锚定。

### C: 项目筛选跨菜单保留，并加一键清除（提交 42efb51）

- **问题**: 切去运行 / 服务器等菜单再回项目页，组件卸载重挂，搜索、状态、分组、服务、标签筛选全部归零，
  刚收窄的列表一次菜单切换就没了。
- **改动**:
  - 五个维度的筛选存 `sessionStorage`（`pipewright.projects.filters`）：切菜单、刷新都保留，
    新标签页从默认值开始；视图模式仍是跨会话的 `localStorage` 偏好，不混在一起。
  - 读回时做类型校验；分组 / 服务 / 标签在数据加载后按当前可选值剪一遍，被删掉的筛选不会留下恒空列表。
  - 工具栏在确有筛选时出现「清除筛选」，一次把五个维度归位；无结果空状态里的清除按钮复用它。
- **验证**: `:18080` 一次性实例（4 个项目）筛成 1/4 → 切「运行」再回「项目」：搜索、成功档、分组、
  服务、`frontend` 标签全部保留，计数仍为「1 个项目 (共 4 个)」；刷新后保留；点清除回到 4 个项目、
  按钮消失；空结果页的清除按钮同样有效。

## 迁移

**零迁移**。本版只改前端页面、日志窗口常量与文案，后端与数据库结构未动，老库直接升。

## 版本号

运行详情新增两个入口、项目筛选新增持久化与清除按钮，均为向后兼容的新功能 → **minor**:v0.23.1 → **v0.24.0**。

## 发版实况

- **推送与 tag**:tag `v0.24.0`（标注对象 `e26fa21`）指向 `ec044ee`，远程 `refs/tags/v0.24.0` 与本地一致，
  tag 未移动。本版提交：`4b5fcfd`（运行详情直达编排 + 源码）→ `3cb73fb`（日志窗口 500 行）→
  `42efb51`（项目筛选跨菜单保留 + 一键清除）→ `ec044ee`（重新生成 dist 入口）。
- **workflow 结论**（sha `ec044ee` 共 5 条，**一次全绿**）:
  - `ci` #71（master 推）08:55:50Z→09:01:08Z success（5m18s）；
  - `ci` #72（tag 推）08:56:10Z→08:59:10Z success（3m00s）；
  - `release` #26 08:56:10Z→09:00:43Z success（4m33s）；
  - `aliyun-image` #25 08:56:10Z→09:00:50Z success（4m40s）；
  - `caddy-image` #27 08:56:10Z→09:18:42Z success（22m32s，照例最慢）。
- **第 7 步硬证据（产物级）**:
  - GitHub Release `v0.24.0` 资产 7 个 = 四平台 `tar.gz` + 两个 windows `zip` + `checksums.txt`；
    六个包本机 `shasum -a 256 -c checksums.txt` **逐行 OK**（两个包首轮下载超时，续传后校验通过）。
  - `releases/latest` 的 302 落在 `/releases/tag/v0.24.0`（非草稿、非预发布）。
  - ghcr:`latest` 与 `0.24.0` 同为 `sha256:20d6e37c…`，双架构索引。
  - ACR（本机 `docker login` 仍有效，`manifest inspect` 取证）：`0.24.0` = `latest` =
    `sha256:818af496…`，`0.24.0-docker` = `latest-docker` = `sha256:752029fb…`，两条都列出 amd64 + arm64。
  - 随车 Caddy:`ghcr.io/fuuhoo/pipewright-caddy:latest` 与 `v0.24.0` 同为 `sha256:f93ec5fd…`。
  - 版本自述:Release 里的 `darwin_arm64` 二进制报
    `pipewright 0.24.0 (commit ec044ee, built 2026-11-20T08:55:33Z, go1.26.11, darwin/arm64)` ——
    commit 是打 tag 那条，不是 `dev`。
  - 空目录抽查发布二进制：`reset-admin-password --generate` 退出码 **2**，目录里**没有留下 `.db`**。
- **发版前本地门禁**:`make fmt-check` / `make vet` / `make test` 全绿；
  `npm run typecheck` / `npm run test`（115 文件 / 1374 用例）/ `npm run build` 全绿。
  `:18080` 一次性实例完成三项交互实测（运行详情入口、日志翻页、项目筛选切换/刷新/清除）。
- **本地实例**:`:8080` 已用 tag 版本重建并重启，`/version` 报 `v0.24.0` / commit `ec044ee`，
  入口 `index-dHYrhcWk.js`，与 `web/dist` 一致。
- **网络备注**:本机直连 `github.com` 与 `ghcr.io` 间歇性超时（`unexpected EOF`）。
  Release 302 与资产页重试后正常；ghcr 的索引 digest 改用
  `imagetools inspect --raw | shasum -a 256` 取证，与标签 digest 一致。ACR 侧不受影响。
- **沿用上版结论**:镜像自述一项仍跳过 `docker run`（ghcr 拉取会长时间卡住），
  改用 Release 包自述 + `imagetools inspect` 取证。
