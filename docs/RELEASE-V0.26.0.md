# v0.26.0 发布清单

> 发布日：2026-11-27
> **手动运行可指定 Tag（发布该 Tag 对应的固定提交）+ 容器页筛选行改为单行工具栏**

## 功能与改动

### A: 运行支持指定 tag，解析成固定提交后再发布（提交 696b36e、9560ea2）

- **问题**: 想发某个已打 tag 的版本时，只能先在 Git 里查出它的提交号再手填 commit；填错或仓库
  又往前走了，发出去的就不是那个版本。把 tag 混进分支下拉也不可行 —— 选分支要翻过整串版本号，
  选 tag 又容易点到同名的旧分支，选错就是发错代码。
- **改动**:
  - 后端 `POST /api/projects/{id}/runs` 新增可选 `tag`。指定后在触发入口就用仓库缓存把 tag 换成
    它对应的提交 SHA，写进已有的 `run.Trigger.Commit`，**不新增库字段**：经典构建、DAG 构建和
    仓库文件读取三条路本来就只认这个 SHA，因此自然 checkout 同一个提交，不必各自再去认 tag。
  - `repocache.ListRefs` 对注解 tag 先 peel 到真实提交再填 SHA —— 注解 tag 的 ref 指向 tag object，
    不 peel 下拉里给出的就是错的提交号。
  - 错误口径：tag 与手填 commit 同时给 → `422 conflicting_ref`；tag 不存在 → `422 invalid_tag`；
    代码管理区未启用 → `503`；仓库不可达或凭据失效 → `502`。原则是「点错了别默默跑错代码」。
  - 前端两个入口（项目卡触发弹窗、运行页快速触发）各加一个独立的可选 Tag 字段，复用已有的可搜索
    `RefPicker`，分支与 tag 分成两个下拉；选了 tag 就清空并禁用 commit 输入，提交时只带 tag。
    顺带修 `RefPicker` 的无障碍名：纯 tag 下拉的按钮以前也念「分支」。
  - i18n 八语补 `tag` / `tagHint` / `tagPlaceholder` 与运行页对应文案。
- **验证**: 后端 `internal/httpapi/manual_tag_test.go` 覆盖固定 SHA 落库、四种错误码；
  `repocache` 新增 `TestListRefsPeelsAnnotatedTagToCommit`（轻量 tag 与注解 tag 都报真实提交）。
  `:18080` 一次性实例起真实 smart-HTTP Git 服务后实测：下拉可搜索、选中 tag 后 commit 清空并置灰，
  两次运行（`c3f3c685…`、`a935ca3c…`）落库的 `Trigger.Commit` 均为 `34b1db6d0766453358d1061ba44d51bb65fafedc`。

### B: 容器页筛选行改成单行工具栏，控件统一等高（提交 71fcb29）

- **问题**: 状态段之外的控件（视图切换 / 分组 / 每页 / 搜索 / 全部折叠）整包塞在一个右对齐块里，
  宽度一不够就整块换行，状态段和它之间留下大片空白，看着像少了半屏控件；同一行里胶囊 32px、
  下拉 36px、每页胶囊 31px 混排，读起来是「没对齐」。
- **改动**:
  - 去掉 `.controls-right` 这层包裹，六个控件平级从左到右连续排；搜索框改成这一行唯一的弹性项
    （`flex: 1 1 260px`）吃掉剩余宽度，右侧控件永远贴着右边缘，窄屏时它整段挪到下一行铺满，
    而不是半截挤在行尾。
  - 状态段与视图段之间留 22px 分簇，间距挂在段尾而不是视图段段首，换行后第二行仍齐左不缩进。
  - 五种控件统一 34px 高；两个下拉与搜索框的纵向 padding 改由固定高度居中，避免文字被裁。
- **验证**: 一次性实例（3 台机器）按容器宽度量测：1400/1200px 单行且各控件 `y` 相同、高 34px，
  搜索框分别 503/303px 吸收剩余；1000/820/640px 换行后每段齐左连续，无中途空洞。

## 迁移

**零迁移**。本版只在触发入口解析 tag 并复用已有的 `run.Trigger.Commit`，容器页只改布局样式，
表结构与既有数据不变，老库直接升。

## 版本号

两项都是新功能、接口与数据向后兼容 → **minor**：v0.25.0 → **v0.26.0**。

## 发版实况

- **推送与 tag**：tag `v0.26.0`（标注对象）指向 `521a499`，先推 `master`（`cf83d6c..521a499`）
  再推 tag，远程主干与 tag 均与本地一致，tag 未移动。本版提交：`696b36e`（后端 tag 触发 +
  repocache peel）→ `9560ea2`（前端 Tag 下拉与八语文案）→ `71fcb29`（容器页工具栏）→
  `521a499`（重新生成 dist 入口）。
- **本地门禁**：`make fmt-check` / `make vet` 干净；`go test -count=1` 全绿；前端
  `vue-tsc` 通过、`eslint` 0 error（15 条既有 warning）、vitest 115 文件 / 1376 用例通过。
- **workflow 结论**（sha `521a499` 共 5 条，**一次全绿**）：
  - `ci` #75（master 推）07:21:06Z→07:24:23Z success（3m17s）；
  - `ci` #76（tag 推）07:21:13Z→07:24:35Z success（3m22s）；
  - `release` #28 07:21:12Z→07:25:28Z success（4m16s）；
  - `aliyun-image` #27 07:21:12Z→07:25:21Z success（4m09s）；
  - `caddy-image` #29 07:21:12Z→07:36:24Z success（15m12s，本版最慢）。
- **产物级终检**：
  - `releases/latest` 的 302 落在 `/releases/tag/v0.26.0`（非草稿非预发布）；资产 7 个
    （六平台包 + `checksums.txt`），逐个下载后 `shasum -a 256 -c checksums.txt` **六行全 OK**。
  - ghcr `pipewright:latest` = `0.26.0` = `sha256:4f4ab52d68fbbf994c746ffa01a84dea7bfcd98bffed01a074f1017d2fec47d1`。
  - ACR `pipewright:latest` = `0.26.0` = `sha256:206d9f3019b8b11b06a584a9fb2535fa114a02e8fa5892dd6f411c15f1d78f6d`，
    `latest-docker` = `0.26.0-docker` = `sha256:88643663cd5c30455bc718b52ba275c8e1a761fe094508446689a5d16481edd3`，
    两档均含 `linux/amd64` + `linux/arm64`。
  - 随车 `pipewright-caddy:latest` = `v0.26.0` = `sha256:34de896a30bf7448a6e4989598ad3379bdb02d0fb480a54d4ca7835d48e77f24`。
  - 解包的 `darwin_arm64` 二进制自述 `pipewright 0.26.0 (commit 521a499, built 2026-11-27T07:20:37Z, go1.26.11, darwin/arm64)`，
    commit 与打了 tag 的那条一致；空目录跑 `reset-admin-password --generate` 退出码 2 且未留下 `.db`。
  - **镜像自述一项未做 `docker run`**：`ghcr.io` 清单请求当日仍报 `EOF`，改用
    `imagetools inspect` 取 digest + 同一 commit 的 Release 包自述取证（与 v0.25.0 同法）。
  - 版本自述要用 `--version`（`-version` / `-v` 同样有效）；写成 `/version` 或 `version` 不会命中
    参数解析，会当成正常启动直接开库、绑端口。
