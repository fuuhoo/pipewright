# v0.23.0 发布清单

> 发布日：2026-11-19
> **步骤构建器新增重命名 + 复制拆成目录内容/文件夹本身/文件三种写法 + 字段渲染链修复**

## 功能与改动

### A: 步骤构建器新增「重命名」步骤（提交 5e1c16c）

- **问题**: 步骤构建器只有复制、没有改名;发布产物想从 `dist` 改成带版本号的名字只能切回原始参数手写 mv。
- **改动**:
  - `stepCompile.ts` 新增 `rename` 步骤类型:`renameLine` 编译成 `mv '源' '目标'`,
    两格都空或只有根路径时返回空行(不写半步)。
  - 反解析只认「恰好两个路径 token」的 `mv` 行,`-f` 之类带选项的行留给运行命令卡片,
    尾斜杠原样保留(改名成文件还是拷进目录由用户自己写清)。
  - `StepBuilder.vue` 增加重命名卡片(源 / 目标两格,logblue 强调色),
    步骤间「+」菜单与底部「加步骤」同步放出。
- **验证**: `stepCompile.test.ts` 覆盖编译、空值/根路径拒绝、尾斜杠保留、含单引号路径轮转、
  手写裸 `mv` 识别、三 token 行不误认;`StepBuilder.insert.test.ts` 实测插入到第 2 位并把
  焦点落到源路径。

### B: 复制拆成「目录内容 / 文件夹本身 / 文件」三种写法（提交 5e1c16c）

- **问题**: 原来复制只有「复制文件夹本身」与「复制目录内容」两档。用户复制单个文件时,
  目标被自动补尾斜杠并预建目录,`cp` 会把文件当成目录投放,得到的路径和预期不一样。
- **改动**:
  - `CopyMode` 扩为 `contents | entry | file`:
    - `contents`:复制源目录里的内容,编译保留 `mkdir -p '目标/'` 与 `源/`(cp 用 `源/.`)。
    - `entry`:把文件夹本身拷进目标目录,目标按目录处理,保留 `mkdir -p '目标/'` 与尾斜杠。
    - `file`:目标按原样使用 —— **不预建目录、不补尾斜杠**,`cp -a 源 目标` 语义:
      目标不存在就是新文件名,已存在且是目录时放进去保留原名;用户自己写了 `/` 才按目录处理并建目录。
  - 高级复制(rsync 优先 / cp 兜底)与普通复制(`plain`)两条路径都按同一套规则编译;
    改成普通复制时清空排除项(cp 不吃排除,留着等于骗用户)。
  - 老的 `entry` 配置读回仍是文件夹语义(目标保留尾斜杠),重编译字节一致,不会静默改语义。
  - 8 个语种补齐「复制文件」选项与三种写法的提示词,占位符里的尾斜杠说明同步改口径。
- **验证**: `stepCompile.test.ts` 覆盖三种写法的正反编译与轮转、用户手写尾斜杠按目录处理、
  手写 `cp -a 源 目标` 识别为文件写法、`-R/-r` 归一成 `-a`;`:18080` 一次性实例实测
  `dist/app.jar → out/app-v1.jar` 编译成
  `rsync -a 'dist/app.jar' 'out/app-v1.jar'; else cp -a …`,没有 mkdir、没有尾斜杠。

### C: 复用库指令选择旁的空提示不再截断字段渲染链（提交 301a53e）

- **问题**: 「复用库没有指令」的提示原本是与 select 并列的独立 `v-if`,夹在字段渲染的
  `v-else-if` 链中间,把链截成两段后末尾的 `v-else` 输入框变成「总是渲染」,
  多行字段(执行命令 / 产物路径 / 依赖缓存目录)会多出一个空单行框。
- **改动**: 把提示收进 `commandref` 分支自身的 `<template>` 里,链恢复连续。
- **验证**: `JobDrawer.fieldPresets.test.ts` 新增用例,断言三个多行字段各只有一个 textarea、零 input。

## 迁移

**零迁移**。本版只改前端步骤编译与字段渲染,后端与数据库结构未动,老库直接升。

## 版本号

新功能、接口与数据向后兼容 → **minor**:v0.22.0 → **v0.23.0**。

## 发版实况

- **推送与 tag**:tag `v0.23.0`(标注对象 `4a2e377`)指向 `26ae8da`,远程 `refs/tags/v0.23.0^{}` 与本地一致,tag 未移动。
  本版提交:`301a53e`(字段渲染链修复)→ `5e1c16c`(重命名 + 复制三种写法)→ `26ae8da`(重新生成 dist 入口)。
- **workflow 结论**(sha `26ae8da` 共 5 条,**一次全绿**):
  - `ci` #67(master 推)09:10:27→09:13:26 success(2m59s);
  - `ci` #68(tag 推)09:10:40→09:14:22 success(3m42s);
  - `release` #24 09:10:40→09:14:48 success(4m08s);
  - `aliyun-image` #23 09:10:40→09:15:07 success(4m27s);
  - `caddy-image` #25 09:10:40→09:29:35 success(18m55s,照例最慢)。
  - v0.22.0 把 `CADDY_VERSION` 提到 2.11.7 的修法生效,本次 caddy-image 一次绿;
    随车镜像重新带上了 `v0.23.0` 标签,**v0.22.0 缺的那个 caddy 版本标签补齐**。
- **第 7 步硬证据(产物级)**:
  - GitHub Release `v0.23.0` 资产 7 个 = 四平台 `tar.gz` + 两个 windows `zip` + `checksums.txt`;
    六个包本机 `shasum -a 256 -c checksums.txt` **逐行 OK**(两个 ARM 包首次下载因 CDN 超时得到 0 字节,
    重下后通过;见下文「新的坑」)。
  - `releases/latest` 的 302 落在 `/releases/tag/v0.23.0`(非草稿、非预发布)。
  - ghcr:`latest` 与 `0.23.0` 同为 `sha256:eb7e2a45…`,双架构。
  - ACR(本机 `docker login` 仍有效,`imagetools inspect` 取证):`0.23.0` = `latest` = `sha256:c579437b…`,
    `0.23.0-docker` = `latest-docker` = `sha256:fe07ad71…`,均双架构。
  - 随车 Caddy:`ghcr.io/fuuhoo/pipewright-caddy:latest` 与 `v0.23.0` 同为 `sha256:1c7d8d81…`。
  - 版本自述:解包的 `darwin_arm64` 二进制报
    `pipewright 0.23.0 (commit 26ae8da, built 2026-11-19T09:10:02Z, go1.26.11, darwin/arm64)` ——
    commit 是打 tag 那条,不是 `dev`。
  - 空目录抽查发布二进制:`reset-admin-password --generate` 退出码 **2**,目录里**没有留下 `.db`**。
- **发版前本地门禁**:`make fmt-check` / `make vet` / `make test` 全绿,
  `npm run typecheck` / `npm run test`(115 文件 / 1369 用例)/ `npm run build` 全绿,
  `make go-build` 正常注入版本。`:18080` 一次性实例完成交互实测:步骤间「+」菜单含重命名、
  复制下拉有「复制目录内容 / 复制文件夹本身 / 复制文件」三档,
  `dist/app.jar → out/app-v1.jar` 编译成不带 mkdir、不带尾斜杠的 rsync/cp 行,
  重命名生成 `mv 'dist' 'release'`。
- **本地实例**:`:8080` 已用带 tag 版本重建并重启,`/version` 报 `v0.23.0` / commit `26ae8da`,
  入口 `index-D9Kk5b6W.js`,与 `web/dist` 一致。`:8081` 本轮未在运行(Docker 守护进程无响应,端口未监听),
  未做刷新。
- **新的坑**:① 发版终检脚本第 5 项(镜像内二进制自述)走 `docker run … --version`,
  在到 ghcr 慢的网络里会卡在拉镜像上半小时以上;改用 `docker buildx imagetools inspect` 查 digest +
  直接跑 Release 里的平台包取证即可。② GitHub Release 资产下载会「成功返回但留下 0 字节文件」
  (`curl --max-time` 到点退出),`shasum` 前先看文件大小,别把 0 字节当成发布失败。
