# v0.18.0 发布清单

> 发布日：2026-10-30
> **SSH 文件部署支持部署前备份 + 保留份数 + 运行详情按目标覆盖式恢复**

## 功能与改动

### A: deploy_ssh 文件档新增「部署前备份」（提交 f264326）
- **问题**: 文件直铺是覆盖式的 —— `activateFileOne` 把产物放到部署目录、重启进程，旧的 jar / 配置
  当场被盖掉。升级失败想退回上一版，只能靠目标机上自己留的副本；平台连「这次铺之前有没有旧文件」
  都无从知晓，更谈不上替用户留一份。
- **改动**:
  - [backup.go:141](file:///Users/cw/code/pipewright/internal/deploy/backup.go#L141) `backupBeforeDeploy`
    在产物落位前打一份归档，落在部署目录内的 `.pw-backups/<20060102T150405Z>-<纳秒>.tar.gz`：
    `dir` 整目录打包、`suffix` 只打包匹配后缀的文件（后缀清单先过
    [safeBackupSuffix:122](file:///Users/cw/code/pipewright/internal/deploy/backup.go#L122)
    白名单 —— 只有字母数字与 `. _ -`，要嵌进远端 shell 脚本里，形状必须钉死）。
  - 保留份数 [backupKeep:82](file:///Users/cw/code/pipewright/internal/deploy/backup.go#L82)：空 = 5，
    夹紧 1..50；[pruneBackups:225](file:///Users/cw/code/pipewright/internal/deploy/backup.go#L225)
    按 mtime 从旧到新裁，`dir` 模式空目录不打包（没什么可留的）。
  - **备份有独立预算**：`backupTimeout` 10 分钟（[backup.go:63](file:///Users/cw/code/pipewright/internal/deploy/backup.go#L63)），
    不跟普通部署命令那 60s 共用 —— 整目录 tar 在慢盘上远超一条 `systemctl restart`。
  - **备份失败 = 该机 failed**，不悄悄跳过：备份是用户明确要的安全网，跳过等于让他在需要恢复时才发现没有。
  - 命令一律数组形态交给 `sh -c`，用户填的后缀、路径作位置参数，不拼进脚本正文。
- **验证**: `internal/deploy/backup_test.go` 六条 —— 打包发生在产物落位之前、后缀被净化、
  备份失败先于铺文件失败、恢复覆盖并重启、非法 backupId 被拒、列表按时间倒序。

### B: 每目标的部署配置随运行落库（迁移 0089）
- **问题**: 恢复要知道「当时铺到哪个目录、重启命令是什么」。这些此前只活在发起部署的前端表单里，
  页面一刷新就没了，历史运行更是无从得知。让恢复请求自带路径 / 命令，等于把目标机上要执行的
  命令交给请求体 —— 越权面直接开在 SSH 上。
- **改动**:
  - 迁移 [0089_deploy_target_backup.sql](file:///Users/cw/code/pipewright/internal/store/migrations/sqlite/0089_deploy_target_backup.sql)：
    `deploy_targets` 加 `deploy_path` / `restart_command` / `backup_mode` 三列（都是
    `NOT NULL DEFAULT ''`）；存量行三列为空 = 「当时没有记录」，不提供恢复入口。
  - [run/deploy_targets.go](file:///Users/cw/code/pipewright/internal/run/deploy_targets.go) 的
    DeployTarget 带三列，Save / Upsert / List 全链路透传；
    [deploy.go:467](file:///Users/cw/code/pipewright/internal/deploy/deploy.go#L467) `deployTargetOf`
    从每机结果取。扇出、蓝绿、交互式首轮三条路径都走
    `annotateFileTargets`（[deploy.go:758](file:///Users/cw/code/pipewright/internal/deploy/deploy.go#L758)），
    中断部署（AbortDeploy）也把已有字段原样抄回，不让一次中断把配置抹成空。
- **验证**: `make test` 全绿；升级老库后既有运行 `backup_mode` 为空 → 不出现恢复按钮，行为与升级前一致。

### C: 备份列表与覆盖式恢复（提交 f264326）
- **改动**:
  - `GET /api/runs/{id}/targets/{serverId}/backups`
    （[router.go:875](file:///Users/cw/code/pipewright/internal/httpapi/router.go#L875)）只读：
    列归档名 / 大小 / 时间，从新到旧。
  - `POST /api/runs/{id}/targets/{serverId}/backups/{backupId}/restore`
    （[router.go:876](file:///Users/cw/code/pipewright/internal/httpapi/router.go#L876)）：
    过 auth + CSRF，审计动作 `deploy.restore_backup`；恢复用服务端落库的部署目录与重启命令，
    解包后按需重启（重启命令为空 = 部署时不重启，恢复也不重启）。
  - [ListBackups:262](file:///Users/cw/code/pipewright/internal/deploy/backup.go#L262) /
    [RestoreBackup:307](file:///Users/cw/code/pipewright/internal/deploy/backup.go#L307)：
    `backupId` 只接受
    [backupArchivePattern](file:///Users/cw/code/pipewright/internal/deploy/backup.go#L66)
    形状的平台归档名，其余一律 404 —— 远端 tar 只解平台自己写的文件。
  - 定位失败给 404 / 422（`server_not_found`），真正解包 / 重启失败给 200 + `ok=false` ——
    前者是「没有这个目标」，后者是「跑了但没成」，前端两种提示分开。
  - 恢复占住目标机（`holdServer` / `occupy`），与部署同一把锁，不跟正在跑的部署抢目录。
- **验证**: 删掉的目标机查备份 → 422 `server_not_found`（此前是 500，已修）；
  篡改 `backupId` → 404；正常恢复 → 归档解回部署目录、重启命令执行、运行日志留痕。

### D: 前端：节点字段 + 运行详情入口（提交 e7515cc）
- **改动**:
  - [jobConfigSchema.ts:803](file:///Users/cw/code/pipewright/web/src/components/pipeline/jobConfigSchema.ts#L803)
    新增三字段：备份方式（不备份 / 整份部署目录 / 只备份匹配后缀的文件）、备份后缀、
    保留份数；只在 SSH 文件直铺且产物不是镜像 / 命令档时出现，隐藏时 `dropWhenHidden` 抹掉旧值。
  - 保留份数前端按 `/^\d+$/` 校验（[jobConfigSchema.ts](file:///Users/cw/code/pipewright/web/src/components/pipeline/jobConfigSchema.ts) 的
    `backupKeepFieldError`）：后端用 `strconv.Atoi`，整串都得是数字，「5abc」不能在前端先放过。
  - 运行详情每张部署目标卡多一个「备份」按钮（仅当该目标 `backupMode` 非空），
    点开是 [DeployBackups.vue](file:///Users/cw/code/pipewright/web/src/components/run/DeployBackups.vue)：
    列现存备份 + 两步确认后覆盖式恢复；恢复入口按 `run.operate` 收起。
  - 文案 8 语言 ×2 命名空间（`pipelineJob` 10 条 / `run` 25 条），key 一致性与消息编译测试全绿。
- **验证**: :18080 临时实例上核过字段随模式显隐、非法保留份数行内报错、
  无备份方式的目标不出按钮、面板打开 / 关闭 / 加载失败重试；重建 8080 / 8081 两实例后真实页面可见。

### E: runner / cron 保存按钮补 scoped 样式（提交 8d0b5ec）
- 两个面板的 `.btn-primary` / `.spinner` 在本组件 scoped 样式里缺失，按钮落成无样式裸按钮；
  补齐对应的 scoped 规则，行为不变。

## 迁移

这一版只有 **1 个迁移 ×2 方言**（0089）：

```bash
git diff --name-only v0.17.0..v0.18.0 -- internal/store/migrations
# 期望 2 个文件:0089_deploy_target_backup ×(sqlite,mysql)
```

形态已核：0089 只有 `ALTER TABLE … ADD COLUMN`（三列，`NOT NULL DEFAULT ''`），
**没有改列、没有回填、没有删列、没有重建表** —— 存量运行三列为空即「当时没记」，不提供恢复入口，
行为与升级前一致；降级时多出的三列不影响旧版本读取。

## 发版实况（供下次对照）

- 五条 run 同起于 04:04Z（master push 的 `ci` 早 11s），**一次全绿**：
  `ci`(master, 3m31s) / `aliyun-image`(3m57s) / `ci`(tag, 4m15s) / `release`(5m06s) /
  `caddy-image`(**18m21s，仍是最慢的一条**)。
- 第 7 步硬证据：`draft=false`、`prerelease=false`、7 个资产（4 tar.gz + 2 windows zip + `checksums.txt`）；
  六个平台包逐行 `shasum -a 256 -c` 全 OK；解出的 darwin/arm64 二进制 `--version` 自述
  `0.18.0 (commit 2059ddc, built 2026-10-30T04:02:59Z, go1.26.10, darwin/arm64)`。
  ghcr `latest` 与 `0.18.0` 的两个子 manifest digest 相同（amd64 `bc50f778…` / arm64 `7a044a9b…`），
  与 v0.17.0 的 `92db76a6…` / `2a86c3f9…` 不同；镜像里 `--entrypoint /pipewright … --version`
  同样自述 `0.18.0 (commit 2059ddc, linux/arm64)`。
  ACR 四标签均列出 amd64 + arm64，`0.18.0` = `latest`（`sha256:35841a25…`），
  `0.18.0-docker` = `latest-docker`（`sha256:d649462f…`）。
- 发布产物抽验：空目录里 `reset-admin-password --generate` 退出码 2、报出解析到的绝对路径，
  `ls` 里没有留下 `.db`。
- 提交拆分：8d0b5ec 按钮样式 → f264326 后端备份/恢复 → e7515cc 前端入口 → 2059ddc dist。
- **一条流程事实**：`api.github.com` 的匿名额度（60/h）在轮询五条 run 时被打满，
  Release 资产只能在 12:40 重置后才清点；别把「查不到」当成「没发出去」。
  （v0.19.0 复核：`~/github/pat.txt` 那条 PAT 仍是 401，红任务的日志与 workflow retry 都取不到，
  重跑只能在网页上点 —— 见 `RELEASE-V0.19.0.md`。）
