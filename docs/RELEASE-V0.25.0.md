# v0.25.0 发布清单

> 发布日：2026-11-24
> **项目列表按最近新建/修改倒序 + 复制步骤的源/目标路径改为上下多行**

## 功能与改动

### A: 项目按最近新建/修改倒序，副本排在自定义顺序最前（提交 7c14e93）

- **问题**: 项目列表默认按创建时间倒序，改过配置的项目不会上浮；更麻烦的是保存过手动拖拽顺序后，
  `rankProjects` 把不在顺序里的项目一律沉到末尾 —— 新复制一个项目，它反而排到最后，用户很难找到。
- **改动**:
  - 后端 `ListPaged` 排序改为 `updated_at DESC, created_at DESC, id`：新建与修改同权，最近动过的在前；
    复制项目写入 `created_at = updated_at = now`，副本自然落在第一位。
  - 前端 `rankProjects` 对不在手动顺序里的项目由「垫底」改为「按后端顺序置顶」，已排好的卡片相对次序不变；
    删掉的残留 id 仍被忽略。
  - 后端新增排序用例 `TestListOrdersByRecentChange`（同创建时间、不同修改时间，以及修改时间最新者置顶）；
    前端补「已有自定义顺序时副本仍排最前」用例。
- **验证**: `:18080` 一次性实例种 3 个项目并保存自定义顺序 `p1,p2,p3`，调复制接口生成「项目一副本」后，
  项目页卡片顺序为「项目一副本、项目一、项目二、项目三」，刷新与切菜单后保持。

### B: 复制步骤的源/目标路径改为上下多行（提交 2d4c4a9）

- **问题**: 抽屉宽度只有 360px，复制卡片里源、目标两个输入框并排后各剩约 150px，
  长路径（如 `./build/pkg-shenyang → ./deploy/shenyang`）只能看到开头，容易误以为填错。
- **改动**:
  - 复制与重命名共用同一套路径网格：源、目标各占一整行，箭头旋转为向下，整行宽度都留给路径。
  - 路径输入框统一加高到 36px、字号略放大；重命名原来的专属覆盖收口为共用规则，两处不再各写一份。
- **验证**: `:18080` 一次性实例打开带复制步骤的节点：两个输入框各 295px 宽、上下排列（间距 57px），
  `./build/pkg-shenyang` 与 `./deploy/shenyang` 完整可见；`vue-tsc` 与全量 vitest（115 文件 / 1375 条）通过。

## 迁移

**零迁移**。本版只改项目列表排序与步骤编辑器的布局样式，后端查询语句不涉及表结构，老库直接升。

## 版本号

项目排序口径调整（新功能，向后兼容）+ 步骤卡布局调整 → **minor**:v0.24.0 → **v0.25.0**。

## 发版实况

- **推送与 tag**:tag `v0.25.0`（标注对象 `7f6d2cc`）指向 `c08b4bb`，远程 `refs/heads/master` 与
  `refs/tags/v0.25.0` 均与本地一致，tag 未移动。本版提交：`7c14e93`（项目排序）→ `2d4c4a9`
  （复制步骤路径上下多行）→ `c08b4bb`（重新生成 dist 入口）。
- **workflow 结论**（sha `c08b4bb` 共 5 条，**一次全绿**）:
  - `ci` #73（master 推）07:46:16Z→07:49:44Z success（3m28s）；
  - `ci` #74（tag 推）07:46:22Z→07:49:30Z success（3m08s）；
  - `release` #27 07:46:22Z→07:51:11Z success（4m49s）；
  - `aliyun-image` #26 07:46:22Z→07:50:58Z success（4m36s）；
  - `caddy-image` #28 07:46:22Z→08:06:54Z success（20m32s，本版最慢）。
- **产物级终检**:
  - GitHub Release `v0.25.0`：API 报 `draft=false` / `prerelease=false`，资产 7 个；六个平台包
    `shasum -a 256 -c checksums.txt` 逐行 OK。当日 `github.com` 页面侧不通，`releases/latest` 的 302
    多次「无响应」，以 API 的 latest 结果为准（`v0.25.0`、非草稿非预发布）。
  - ghcr `pipewright:latest` = `0.25.0` = `sha256:01e076fe938e9d7495c0b907cc63a26e0f85eefab405c1e15985ac03eab8c79c`；
    ACR `pipewright:latest` = `0.25.0` = `sha256:ea2529323646bd2ff6525c417b9b6ece52eabe599bb918e1c8f4414d77a428cf`，
    `latest-docker` = `0.25.0-docker` = `sha256:8b0fbf930cf28ee92efbdd888680a606480d39f9c22251623ce16933eb81678b`；
    默认档与 `-docker` 档均含 `linux/amd64` + `linux/arm64`。
  - 随车 `pipewright-caddy:latest` = `v0.25.0` = `sha256:13dda9aff8c17c13b3833f7a03c3b5a56df2334dcc2f19f4be55706c5b0170d9`。
  - 解包的 `darwin_arm64` 二进制自述 `pipewright 0.25.0 (commit c08b4bb, go1.26.11, darwin/arm64)`；
    空目录跑 `reset-admin-password --generate` 退出码 2、报出解析到的绝对路径，且未留下 `.db`。
  - ghcr 的 `docker run` 当日拉取被 `EOF` 打断，镜像自述改用发布资产（同一 `c08b4bb` 构建）取证。