# v0.16.0 发布清单

> 发布日：2026-10-29
> **服务器可归多个分组 + OAuth 建号可设初始口令 + 反代镜像可配置/自动推送 + 纯发布项目 + 复制文件夹**

## 功能与改动

### A: 一台服务器可归多个分组（迁移 0087）
- **问题**: `servers.group_id` 只能存一个分组。机器按「谁能碰」切开时常常同时属于两拨人
  （如「支付」+「生产」），单列只能二选一，选哪个都意味着另一拨人从列表里凭空消失。
- **改动**:
  - 迁移 [0087_server_groups.sql](file:///Users/cw/code/pipewright/internal/store/migrations/sqlite/0087_server_groups.sql):
    建 `server_groups(server_id, group_id, created_at)`，把旧单值回填成一行（空串 = 未归组，不落行），
    随后 `DROP COLUMN servers.group_id` —— 只留一份事实，不留会漂的派生列。
  - 判定侧 [access.go:232](file:///Users/cw/code/pipewright/internal/access/access.go#L232) `Can`:
    View/Operate 走**并集**（任一组有权限即可见/可操作）；Manage 走**交集**（改名/删除/改组
    动的是整台资源，每个所在组都要管得住）。引用悬挂的组仍 fail closed（
    [access.go:307](file:///Users/cw/code/pipewright/internal/access/access.go#L307) `snapshotOrDangling`）。
  - 列表侧 [access.go:490](file:///Users/cw/code/pipewright/internal/access/access.go#L490) `MemberClause`
    把「未归组 = 成员表没有行」编译成 `NOT EXISTS`，与判定同一口径；`servers` 列表改为按
    `server_groups` 过滤并按批补齐 `GroupIDs`（[target.go:434](file:///Users/cw/code/pipewright/internal/target/target.go#L434) `attachGroups`）。
  - HTTP 契约 `groupId` → `groupIds`；登记/改组逐组要求 `Manage`，未归组仍是管理员动作。
  - 不加外键：删组在 `internal/group` 里按「仍被引用」拒绝；级联会把机器静默甩成未归组
    （等于开放给全员）。删机器由 `internal/target` 在同一事务里清成员行。
  - 前端：分组筛选按并集命中（[groupFilter.ts:66](file:///Users/cw/code/pipewright/web/src/lib/groupFilter.ts#L66) `matchesGroup`），
    组标签按「、」串起；设置页多选归组，没权限的组锁住只能保留。
- **验证**: 一台机器同时挂 A、B 两个组 → A 组成员、B 组成员都能在列表里看到并操作它；
  只有 A 组 Manage 的人改归属 → 被拒；未勾任何组 = 未归组，普通用户看不到新增入口。

### B: OAuth 自动建号可首次设置本地口令（迁移 0086）
- **问题**: OAuth/OIDC 首登自动建号时写的是随机占位哈希，明文从未存在。这类账号既不能用
  「用户名 + 口令」登录，也无法走「输入当前口令」的改密路径，账户页只能干瞪眼。
- **改动**:
  - 迁移 [0086_users_local_password.sql](file:///Users/cw/code/pipewright/internal/store/migrations/sqlite/0086_users_local_password.sql):
    `users.has_local_password`（默认 1 保住存量；OAuth 自动建号那批按建号描述回填 0）。
  - [auth/service.go:481](file:///Users/cw/code/pipewright/internal/auth/service.go#L481) `SetInitialPassword`:
    不校验旧口令，安全性全压在「`has_local_password=0`」这条前提上；已有口令的账号一律
    `ErrPasswordAlreadySet`，否则它就是一个绕过改密校验的后门。`UPDATE` 带
    `AND has_local_password = 0`，并发下不会把刚设好的口令再覆盖一次；成功后撤销其它会话。
  - `POST /api/account/password/initial`（[account.go:132](file:///Users/cw/code/pipewright/internal/httpapi/account.go#L132)），
    已有口令 → 409 `password_already_set`；审计带 `initial: true`。
  - 会话 payload 补 `hasLocalPassword`（解析不出用户行时按 true：宁可不给入口，也不给错入口）；
    账户页据此给「设置本地口令」卡片（[SettingsAccount.vue:148](file:///Users/cw/code/pipewright/web/src/views/settings/SettingsAccount.vue#L148)）。
- **验证**: 用一个 OAuth 建号账号登录 → 账户页出现「设置本地口令」而不是「修改密码」；设完可用
  用户名 + 口令登录，再进账户页变成普通改密表单；已有本地口令的账号调该接口 → 409。

### C: 反代镜像可配置，目标机拉不到时从控制机推送（迁移 0085）
- **问题**: 受限网络里目标主机整段连不上 ghcr.io，原来 `docker run` 只回一句
  "Unable to find image"，看不出是连不上、镜像名写错还是镜像不存在；换镜像源还得改环境变量重启。
- **改动**:
  - 迁移 [0085_proxy_config.sql](file:///Users/cw/code/pipewright/internal/store/migrations/sqlite/0085_proxy_config.sql):
    `proxy_config` 单例。优先级「设置页显式配置 > `PIPEWRIGHT_CADDY_IMAGE` > 内置自构建镜像」
    （[settings.go:24](file:///Users/cw/code/pipewright/internal/proxy/settings.go#L24)）。
    引用过 `imageRefRe` 校验后才进 `docker run`；`PUT /api/proxy/settings` 过 auth + CSRF + 审计，
    `GET` 只读，空串 = 清除。
  - [caddy.go:384](file:///Users/cw/code/pipewright/internal/proxy/caddy.go#L384) `pullCaddyImage`：
    先拉；拉失败看目标机本地有没有（离线老部署照常起）；都没有就把控制机本地那份
    `docker save | gzip` 经 SSH 灌进 `docker load`
    （[caddy_image.go:102](file:///Users/cw/code/pipewright/internal/proxy/caddy_image.go#L102) `pushCaddyImage`），
    装卸前先比 `uname -m`，架构不一致直接拒推。控制机也没有才报 `ErrCaddyImagePull`，
    HTTP 502 带「换镜像源 / 手动拉」的人话。
  - 编排错误带 docker 原始输出（`orchestrationErr`，HTTP 层限长单行回显）；`docker run` 的
    "port is already allocated" 归到端口冲突；80/443 探测在 `ss` 之外补 `netstat`（macOS 目标机）。
  - 前端：设置 → 系统 → 反代运行环境，可填镜像、恢复默认，显示本次生效值；README 补推送说明。
- **验证**: 设置页填一个目标机可达的镜像 → 部署反代环境用新镜像；目标机断网但控制机有镜像 →
  部署仍成功（日志可见推送）；控制机也没有 → 502 `caddy_image_pull_failed` 且带原始 pull 输出。

### D: 纯发布项目不再生成源阶段，模板必须带源阶段
- **问题**: 只部署、不拉源码的项目也会拿到一张「Git 源」卡片，永远拉不到东西，还让校验一直报
  `source_stage_missing`。
- **改动**:
  - 源阶段去留由项目是否绑定仓库决定（[pipeline.go:480](file:///Users/cw/code/pipewright/internal/pipeline/pipeline.go#L480) `projectHasRepo`）：
    未绑定 → 默认种子只有一个空部署阶段，读/存时摘掉源阶段并清掉指向它的 `needs`
    （[pipeline.go:565](file:///Users/cw/code/pipewright/internal/pipeline/pipeline.go#L565)）；绑定 → 必须恰好一个源阶段，
    缺 `git_source` 卡片就补回；先按纯发布跑过、之后又绑上仓库的项目，读取时补回源阶段。
  - `normalizeSpec` 不再注入 `git_source`：纯函数拿不到项目行，不在那里猜。
  - 模板是套到任意项目上的骨架，必须先带源阶段（[template.go:218](file:///Users/cw/code/pipewright/internal/library/template.go#L218)）；
    校验侧对未绑仓库的项目跳过源阶段与主凭据检查（[validate.go:185](file:///Users/cw/code/pipewright/internal/pipeline/validate.go#L185)）。
- **验证**: 未绑仓库的项目打开画布 → 只有部署阶段，校验不再报缺源；给它绑上仓库再打开 →
  源阶段自动补回；保存不带源阶段的模板 → 422。

### E: 复制步骤支持「复制文件夹本身」
- **问题**: 复制步骤只有「复制目录内容」（`src/` → `dest/` 铺平），想在目标里得到 `dest/dist`
  只能手写路径，而填 `./` 又会被当成根目录。
- **改动**: 可视化步骤加「复制方式」（[stepCompile.ts:137](file:///Users/cw/code/pipewright/web/src/components/pipeline/stepCompile.ts#L137) `copyStepLine`）：
  复制目录内容（默认，旧语义不变，rsync 源带尾斜杠 / `cp -a 'S/.' 'D/'`）与复制文件夹本身
  （源不带尾斜杠，目标保留原名，文件也可用）。反解析按尾斜杠区分，旧命令行仍读回 contents；
  任何手改都不匹配骨架，退回普通 `command` 步骤。提示文案按 rsync 有无分别说明排除项。
- **验证**: 选「复制文件夹本身」`frontend/dist → out` → 命令为 rsync/cp 的 entry 形态，目标得到 `out/dist`；
  切回「复制目录内容」→ 命令回到旧骨架；旧流水线打开仍是可视步骤。

### F: 运行日志 SSE 只补增量，详情页订阅不再下发日志
- **问题**: 一条 5.4 万行的构建日志，详情页两条订阅各把历史解析一遍就是 10.8 万个事件，
  而详情页根本不消费 log 事件。
- **改动**: `GET /api/runs/{id}/events` 增加 `logs=0`（不回放日志）与 `sinceSeq=N`（只回放 seq>N）
  （[runs.go:329](file:///Users/cw/code/pipewright/internal/httpapi/runs.go#L329)）。终端先用 REST 播种历史，
  再用种子响应的 `nextSeq` 接尾巴；播种失败时游标留 0，退化成全量回放，seq 去重兜住重叠。
  详情页用 `logs=0` 订阅，并在进行中 → 终态时静默补快照，避免整页骨架白闪。
- **验证**: 打开一次长日志运行 → 网络面板里日志只下一次；终端滚动与实时追加正常；运行结束页面不闪骨架。

### G: 登录锁定态给出管理员口令重置说明
- **改动**: 被锁（连续失败 5 次锁 15 分钟）时登录页出现「忘记管理员密码?」，讲清两条
  `reset-admin-password` 命令（二进制 / 容器各一条）、改的是命令当前目录解析到的库，
  并提醒只是锁定的话到期自动解除；命令可一键复制（[Login.vue:357](file:///Users/cw/code/pipewright/web/src/views/Login.vue#L357)）。
  口令只存单向哈希，不做任何在线找回的假承诺。
- **验证**: 连续输错 5 次 → 出现入口；打开弹层 → 两条命令可复制、Esc 关闭；未锁定态不显示入口。

### H: 小修
- 预置构建环境里「镜像复查不可用」的条目仍列出但置灰不可选；已选中的条目事后不可用仍回显原值。
- 状态页骨架卡改用 `MetricsCardSkeleton` 按真卡行高占位；底板高度只留给骨架与采集占位卡，
  真卡（含不可达短卡）按内容长，不再被撑成 600px 空壳。

## 迁移

这一版有 **3 个迁移 ×2 方言**（0085–0087），老库要跟着升：

```bash
git diff --name-only v0.15.0..v0.16.0 -- internal/store/migrations
# 期望 6 个文件:0085_proxy_config / 0086_users_local_password / 0087_server_groups ×(sqlite,mysql)
```

形态已核：0085 只有 `CREATE TABLE IF NOT EXISTS` + 幂等 INSERT；0086 是
`ALTER TABLE … ADD COLUMN`（默认 1）+ 一条按建号描述回填的 `UPDATE`；0087 建 `server_groups`、
回填、`DROP INDEX` + `ALTER TABLE servers DROP COLUMN group_id`。**0087 是本版唯一的破坏性一步**：
旧的 `servers.group_id` 列被删除，降级回 v0.15.0 前请先备份（升级前备份同样适用）。
