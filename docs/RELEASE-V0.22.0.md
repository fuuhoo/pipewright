# v0.22.0 发布清单

> 发布日：2026-11-19
> **项目标签与多选搜索筛选 + 项目卡拖拽排序 + 手动触发指定主机 + 变量组 .env 导入 + vfox rsync 镜像 + 快照跳过点目录**

## 功能与改动

### A: 项目标签与多选搜索筛选（提交 e6787fe）

- **问题**: 项目多了以后只能靠分组和名字找;标签是给「看的人」用的分类,应该能自由加、能按标签筛。
- **改动**:
  - 迁移 `0096_project_tags`(`projects.tags_json`,默认 `'[]'`):标签只做展示/筛选,
    不参与权限与流水线,不拆关联表;去重不区分大小写,顺序按用户添加顺序。
  - 后端 `internal/project` 增加 `Tags` 字段与规范化(最多 20 个、单个 24 字符),
    项目创建/更新/复制口径一致;HTTP 契约带 `tags`。
  - 前端 `tagFilter.ts`:`allTags` 汇总、`matchesTagFilter`(**命中任一**语义、大小写不敏感、不做子串匹配)、
    `mergeTags`;卡片上可编辑标签,空标签保存即清除。
  - 筛选器 `TagFilterSelect.vue`:可输入搜索的多选下拉(照 `ProjectPicker` 形态),勾选不因搜索词变化而丢,
    面板内提示「勾选多个时,显示命中任一标签的项目」,`清除筛选` 一键回全量;
    项目删除导致某标签消失时,已选值自动收窄。
- **验证**: `tagFilter.test.ts`(OR 语义/大小写/精确匹配)、`:18080` 一次性实例实测
  (种子 4 个项目 → 多选筛出 3 个、搜索「网」只剩网关、键盘 Enter/Esc、外部点击收起、清除恢复 4 个)。

### B: 项目卡拖拽排序，顺序按用户保存（提交 e6787fe）

- **问题**: 卡片先后是「看的人」的习惯,不该全组共享;主机卡片已有同款能力。
- **改动**:
  - 迁移 `0094_project_view_prefs`(新表 `user_id` 主键 + `order_json`):只存项目 id,
    改名不受影响,删项目后残留 id 读取时忽略。
  - `internal/projectpref` + `GET/PUT /api/account/project-order`:仅本人可读写。
  - 前端 `projectViewOrder.ts` 负责把「保存顺序 + 新项目」合并成稳定列表;卡片加拖拽握把,
    松手后防抖保存,失败回滚。
- **验证**: `projectpref_test.go`、`projectViewOrder.test.ts`、`ProjectCard.order.test.ts`;
  `:18080` 实测拖动两张卡并刷新页面顺序保持。

### C: 手动触发可选指定主机（提交 e6787fe）

- **问题**: 手动触发时同一流水线可能想跑在「这台机器」语境下,原来只能从部署节点配置间接推断。
- **改动**:
  - 迁移 `0095_run_trigger_server`(`pipeline_runs.trigger_server_id` / `trigger_server_name`):
    **只留痕,不改部署落点**;机器名存触发时刻快照,机器改名/删除后历史仍可读。
  - `GET /api/runs/host-candidates`:候选取该流水线部署节点配置过的主机,按可见范围过滤。
  - 触发接口校验所选主机在候选内;webhook 触发保持原行为(两列为空)。
  - 前端触发弹窗加主机选择;运行详情/列表展示「触发主机」。
- **验证**: `run_hosts_test.go`、`run_test.go`、`TriggerRunModal`/`RunDetail` 相关 vitest。

### D: 变量组支持粘贴文本或上传 .env 导入（提交 3ff3304）

- **问题**: 已有 `.env` 的人只能逐行录入。
- **改动**: `envFileImport.ts` 纯函数按行解析(`KEY=VALUE`、跳过注释/空行、容忍 `export` 前缀与引号),
  冲突时以导入内容为准;复用库页加「导入」入口,可粘贴或选择文件;八语言文案同步。
- **验证**: `envFileImport.test.ts`。

### E: vfox 1.0.15 内置 rsync（提交 441aa60）

- **问题**: 「复制文件」步骤在容器内没有 rsync 时退回逐文件拷贝,大目录慢且丢权限位。
- **改动**: `vfox-full:1.0.15` 在 1.0.14 叠加层上补装 rsync,登记为内置构建环境(SortOrder 87);
  `deploy/vfox/Dockerfile.rsync` 记录构建步骤;前端在镜像不含 rsync 时给降级提示。
- **验证**: `buildEnvRsync.test.ts`;镜像已推送 ACR 并抽查存在。

### F: 工作区快照跳过点开头的目录（提交 36af22c）

- **问题**: `.git` / `.cache` 等点目录体积大、排障价值低,快照容易膨胀和超时。
- **改动**: `buildsnap` 归档前按路径段剔除点开头目录,保留普通隐藏文件。
- **验证**: `buildsnap_test.go`。

## 迁移

3 个迁移 × 2 方言,全部向后兼容:

- `0094_project_view_prefs`:只有 `CREATE TABLE IF NOT EXISTS`。
- `0095_run_trigger_server`:两条 `ADD COLUMN ... NOT NULL DEFAULT ''`。
- `0096_project_tags`:一条 `ADD COLUMN ... NOT NULL DEFAULT '[]'`。

逐文件 grep `drop|rename|modify|change column|update .* set` 零命中,**没有改列 / 回填 / 删列**,
老库直接升,老数据按空值/空数组展示。

## 版本号

新功能、接口与数据向后兼容 → **minor**:v0.21.0 → **v0.22.0**。

## 发版实况

- **推送与 tag**:tag `v0.22.0`(标注对象 `67bc790`)指向 `6f1a353`,远程与本地一致,tag 未移动。
  tag 之后 master 前进一条 `de6e450`(Caddy 基础镜像 2.11.7),**不在 v0.22.0 产物里**,见下文。
- **workflow 结论**(sha `6f1a353` 共 5 条):
  - `ci` #63(master 推)00:27:35→00:32:37 success;
  - `ci` #64(tag 推)00:27:43→00:32:38 success;
  - `release` #23 00:27:43→00:32:57 success;
  - `aliyun-image` #22 00:27:43→00:31:30 success;
  - `caddy-image` #23 00:27:43→00:28:52 **failure**。
- **caddy-image 失败原因**:`xcaddy` 拉插件最新版,`caddy-l4@v0.1.3` 的 go.mod 要求
  `caddyserver/caddy/v2@v2.11.7`,而 `deploy/caddy/Dockerfile` 钉在 `2.11.4` → 依赖解析 FATAL。
  与本版代码无关。修法:`de6e450` 把 `CADDY_VERSION` 提到 `2.11.7`(运行镜像与 builder 都已确认存在),
  推 master(ci success)后 `workflow_dispatch` 传 `caddy_version=2.11.7` 补发(run #24 success,
  补发后 `latest` = `sha256:35087058…`,镜像内 `caddy version` 报 **v2.11.7**)。
  **已推送的 tag 不移动**,workflow 的标签规则也只给 tag 事件打 `v*`、手动触发只有 `latest`,
  所以 `pipewright-caddy:v0.22.0` 这个标签缺失,`latest` 已恢复;下一版 tag 构建会重新带上版本标签。
- **第 7 步硬证据(产物级)**:
  - GitHub Release `v0.22.0` 资产 7 个 = 四平台 `tar.gz` + 两个 windows `zip` + `checksums.txt`;
    六个包本机 `shasum -a 256 -c checksums.txt` **逐行 OK**。
  - `releases/latest` 的 302 落在 `/releases/tag/v0.22.0`(非草稿、非预发布)。
  - ghcr:`latest` 与 `0.22.0` 同为 `sha256:dd9919b5…`。
  - ACR(本机 `docker login` 仍有效):`0.22.0` = `latest` = `sha256:b997319b…`,
    `0.22.0-docker` = `latest-docker` = `sha256:31d849d4…`,均双架构。
  - 版本自述:ghcr 镜像 `--entrypoint /pipewright --version` 与解包的 `darwin_arm64` 二进制都报
    `pipewright 0.22.0 (commit 6f1a353, built 2026-11-19T00:27:12Z, go1.26.11, …)` ——
    commit 是打 tag 那条,不是 `dev`。
  - 空目录抽查发布二进制:`reset-admin-password --generate` 退出码 **2**,目录里**没有留下 `.db`**。
- **发版前本地门禁**:`make fmt-check` / `make vet` / `make test` 全绿,
  `npm run typecheck` / `npm run test`(115 文件 / 1354 用例)/ `npm run build` 全绿,
  `make go-build` 正常注入版本;`:18080` 一次性实例完成标签筛选、拖拽排序与触发主机交互实测。
- **本地实例**:`:8080` 已用 `make go-build` 重建并重启(自述 `v0.22.0-2-gd974b32`,含 tag 内容);
  `:8081` 的 `pipewright:dev-docker` 本次构建通过(前几版被 Docker Hub 限流挡过),容器已重建;
  两个实例入口都是 `index-Cy8_mCT9.js`,与 `web/dist` 一致。
- **已知尾巴**:`~/github/pat.txt` 那条 PAT 仍不可用,本次 run 汇总走 `gh` CLI 的 keyring 会话。