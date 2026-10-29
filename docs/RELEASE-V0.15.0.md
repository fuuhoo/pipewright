# v0.15.0 发布清单

> 发布日：2026-10-29
> **一人多角色 + 建组功能点 + 状态页指标后台采样 + 穿梭框统一**

## 功能与改动

### A: 一人可挂多个角色（迁移 0084）
- **问题**: `users.role` / `sessions.role` 各存一个角色 id,「一人多岗」只能靠给角色勾更多点来凑,
  但那样一来「张三 = 运维 + 只读」这件事在页面上就消失了:角色数、删除挡门、按角色筛人
  全都只剩一个主角色。
- **改动**:
  - 迁移 [0084_user_roles.sql](file:///Users/cw/code/pipewright/internal/store/migrations/sqlite/0084_user_roles.sql):
    建 `user_roles(user_id, role_id, created_at)`,把每个非空 `users.role` 回填一行(幂等);
    `users.role` 退化为**主角色**(展示与旧代码兜底)。
  - 判定侧改为对角色集合求并集:[perms.go:163](file:///Users/cw/code/pipewright/internal/access/perms.go#L163) `SplitRoles`,
    `HasPerm` / `PermsFor` / `CapabilitiesFor` 全部按并集;空串仍按 admin 处理([perms.go:194](file:///Users/cw/code/pipewright/internal/access/perms.go#L194) `IsAdminRole`)。
  - 会话与 API token 的角色字段存选择器串(逗号分隔,主角色在首位),由
    [users.go:381](file:///Users/cw/code/pipewright/internal/users/users.go#L381) `RoleSelectorForUser` 在登录时补全;
    库里的账号一个角色都没有时拒绝登录,绝不把「空 = admin」签到新会话上。
  - 角色计数、按角色筛人、删除挡门都以并集为准。
- **验证**: 给一个账号挂两个角色 → 登录后 `/api/auth/session` 的 `roles` 是数组、`role` 是选择器串;
  两个角色的权限点都生效;角色列表的账号数把该账号算进两个角色。

### B: 建组改为 group.create 功能点
- **问题**: 建组只在管理员路由上,分组管理员能改名册却不能建组,每次都要找管理员。
- **改动**: [access_guard.go:217](file:///Users/cw/code/pipewright/internal/httpapi/access_guard.go#L217) `requireGroupCreate`,
  路由从 admin 组挪到登录用户组(删组仍是管理员动作);前端入口用同一个点收起。
- **验证**: 无 `group.create` 的账号 POST `/api/groups` → 403 `role_denied`;有该点的账号建组成功。

### C: 状态页指标改后台采样
- **问题**: 每打开一个页面,前端就各自拉一次 `/api/servers/metrics`,开几个前端就对每台机器跑几轮
  SSH 采集,采集时刻也各不同;首屏还得等一整轮往返才有数。
- **改动**:
  - [server_metrics_cache.go:39](file:///Users/cw/code/pipewright/internal/httpapi/server_metrics_cache.go#L39) `NewMetricsCache`,
    [main.go:747](file:///Users/cw/code/pipewright/cmd/pipewright/main.go#L747) `PIPEWRIGHT_SERVER_METRICS_INTERVAL`
    (默认 60s,置 0 关闭后台采集);启动即先采一轮。
  - 端点只读快照(nil 缓存时回退按请求采集,供测试/调试);DTO 补 `lastAttemptAt` / `lastError` /
    `stale` / `collecting`([server_metrics.go:215](file:///Users/cw/code/pipewright/internal/httpapi/server_metrics.go#L215))。
  - 采集失败只标 stale,不覆盖上一次成功快照。
- **验证**: 删掉 `PIPEWRIGHT_SERVER_METRICS_INTERVAL` 启服务 → 首屏几十秒内出现数据(启动先采);
  反复刷新页面,机器侧的 SSH 连接数不再随页面数翻倍;把一台机器密码改错 → 页面显示过期标记与失败原因,
  数值仍是上一次成功的。

### D: 前端权限树、多角色选择与统一穿梭框
- **改动**:
  - 账号页「设置权限」统一成一个弹窗两个 tab(角色 / 分组),角色与分组都走穿梭框,右侧权限树按所选
    角色并集实时预览,并标出每个权限点来自哪个角色([PermissionTree.vue](file:///Users/cw/code/pipewright/web/src/components/permissions/PermissionTree.vue))。
  - 角色编辑页改成左表单右权限树并排;角色列表的「N 个账号」可点开查看只读名单
    ([Roles.vue:350](file:///Users/cw/code/pipewright/web/src/views/admin/Roles.vue#L350) `openMembers`)。
  - [TransferList.vue:60](file:///Users/cw/code/pipewright/web/src/components/ui/TransferList.vue#L60) 统一规则:
    每栏独立搜索、默认 8 条一页、最新创建在前;搜索回到第 1 页,箭头只移动当前页勾选项。
- **验证**: 账号页勾/取消角色,右侧权限点当场增减;角色页点账号数打开名单,搜索与翻页正常;
  组管理、账号分组两个穿梭框的排序与分页一致(最新在前)。

### E: 状态页指标占位卡片
- **改动**: 快照未到时用占位卡片顶住布局,每 5s 重试直到拿到数据,之后 60s 轮询;
  `stale` / `collecting` / `lastError` 分三档展示;未采到或失败的机器排到最后。
- **验证**: 重启服务后立刻打开状态页 → 先看到「正在采集」占位,随后自动变成数据;断开一台机器 →
  该卡片显示过期时刻,且排到列表末尾。

### 迁移
这一版**有 1 个迁移 ×2 方言(0084)**,老库要跟着升:

```bash
git diff --name-only v0.14.0..v0.15.0 -- internal/store/migrations
# internal/store/migrations/mysql/0084_user_roles.sql
# internal/store/migrations/sqlite/0084_user_roles.sql
```

形态:只有 `CREATE TABLE IF NOT EXISTS` + `CREATE INDEX` + 幂等 `INSERT OR IGNORE` 回填,
没有 `DROP`、没有表重建、没有外键(内置 admin 不在 roles 表里,加外键会把管理员挡在门外)。

---

## 提交摘要

```bash
6becfa5 feat(权限): 一人可挂多个角色,建组改为 group.create 功能点
843d788 feat(服务器): 状态页指标改后台采样,端点只读快照缓存
774b38f feat(前端): 权限树、多角色选择与统一穿梭框
9b595aa feat(前端): 状态页指标用占位卡片接住首屏,并区分过期与失败
c2e2879 chore(前端): 重新生成 dist 入口,指向上一条打包产物
```

本地全量验证:
- 后端: `make fmt-check vet test` 全绿
- 前端: `npm run typecheck && npm run test`(97 文件 / 1186 用例)与 `npm run build` 全绿
- 二进制: `make go-build` 后 `--version` 不是 dev

---

## 发版流程

1. **推送 master**: `git push git@github.com:fuuhoo/pipewright.git master` ✅
2. **打 tag**: `git tag -a v0.15.0 -m "v0.15.0: 一人多角色、建组功能点、状态页指标后台采样"` ✅
3. **推 tag**: `git push git@github.com:fuuhoo/pipewright.git v0.15.0` ✅
4. **盯四条 workflow**: `release`, `aliyun-image`, `caddy-image`, `ci`(push & tag)✅ 五条 run 一次全绿
5. **终检**: 见下

---

## 发版结果(2026-10-29)

- 五条 run 全绿:`ci`(master push)3m32s、`ci`(tag)4m31s、`release` 6m10s、`aliyun-image` 4m26s、
  `caddy-image` **20m19s**(又是最慢的一条)。
- Release:`draft=false`、`prerelease=false`、7 个资产;六个平台包 `shasum -a 256 -c checksums.txt` 逐行 OK;
  `releases/latest` 指向 v0.15.0。
- ghcr:`latest` 两个子 manifest digest 与 `0.15.0` 相同(`53d67977…` / `ec1ef9cd…`);
  镜像内二进制 `--version` = `0.15.0 (commit 4453df9, …, linux/arm64)`。
- ACR(本机登录仍有效):四标签 `docker manifest inspect` 均列出 amd64 + arm64;
  `0.15.0` = `latest` = `sha256:aab093dd…`,`0.15.0-docker` = `latest-docker` = `sha256:c259007e…`。
- 发布产物抽验:darwin/arm64 二进制 `--version` = `0.15.0 (commit 4453df9)`;空目录里
  `reset-admin-password --generate` 退出码 2、报出解析到的绝对路径,目录里没有留下 `.db`。
- 本机 8080 / 8081 两个实例已换到 `index-BrtSu2VA.js` 新包并返回 200。

### 环境备注

- `~/github/pat.txt` 的 token 已失效(401 Bad credentials),本次用匿名 runs 接口查流水线、
  `docker buildx imagetools inspect` 验 ACR;`jobs/{id}/logs` 匿名 403,下次发版前需换新 token。

