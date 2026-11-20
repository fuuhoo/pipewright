# v0.23.1 发布清单

> 发布日：2026-11-20
> **步骤菜单点外部收起 + 可视化复制的通配符保留 shell 展开 + 重命名路径框改整行**

## 功能与改动

### A: 「加步骤」与步骤间菜单点外部收起，且两张菜单互斥（提交 51a73f8）

- **问题**: 打开「加步骤」或步骤间的「+」菜单后，点抽屉/画布别处不会收起，菜单一直挂着；
  两张菜单同时打开时还会叠在一起。
- **改动**:
  - `StepBuilder.vue` 在 document 上以捕获阶段监听 `pointerdown`，点到 `.sb-add-menu` /
    `.sb-add-btn` / `.sb-insert-menu` / `.sb-insert-btn` 以外就收起全部菜单。
  - 用 `pointerdown` 而不是 `click`：菜单项自己的 click 必须先跑完，否则选项被外层先收掉、点击落空。
  - 打开一张菜单时自动收起另一张。
- **验证**: `StepBuilder.insert.test.ts` 新增用例：菜单内 `pointerdown` 不收、开「加步骤」时插入菜单让位、
  点 body 后菜单消失且 `aria-expanded=false`、没有误加步骤。

### B: 可视化复制的通配符不再被整段引号（提交 0bf0c4c）

- **问题**: 生成的复制指令把整个源路径单引号化，`cp ./target/*.jar ./docker/test` 这种手写能跑的
  命令，经可视化生成后变成 `cp -a './target/*.jar' './docker/test/'`，`*` 不展开；
  高级复制还会报 `rsync: change_dir "…/target/*.jar" failed`。
- **改动**:
  - `stepCompile.ts` 新增 `shellPathToken`：含 `* ? [ ]` 的路径按段引号，通配符露在引号外交给
    shell 展开；只含安全字符的通配符路径直接裸写，与手写命令逐字一致。
  - **源里带通配符时不再补 `/` 或 `/.`**——那是「目录内容」的语义，通配符匹配到的是文件，
    补了会变成不存在的路径。
  - 反解析新增 `readSourceToken`，按「引号段 + 裸通配符 + 裸路径段」读源；通配符行读回
    「复制文件」写法，重编译字节一致。
- **验证**: `stepCompile.test.ts` 覆盖裸通配符、含空格的分段引号、目录语义去尾斜杠、
  与「复制文件到已有文件夹」的精确输出，以及编译→反解析→再编译的轮转；全量 1374 用例通过。

### C: 重命名的源/目标改上下整行、输入框加高（提交 95271ab）

- **问题**: 抽屉固定 360px，源 / 目标两格并排时每格只剩约 150px，长路径看不全。
- **改动**: 重命名卡片改为上下两行、箭头旋转朝下，每格占满整行（实测 295px 宽）；
  框高 30px → 36px、字号 0.85rem。只作用于重命名，复制卡片布局不动。
- **验证**: `:18080` 一次性实例实测两格均为整行 295×36，箭头 `transform: rotate(90deg)`。

## 迁移

**零迁移**。本版只改前端步骤编译与样式，后端与数据库结构未动，老库直接升。

## 版本号

只含 bug 修复与样式调整，接口与数据向后兼容 → **patch**:v0.23.0 → **v0.23.1**。

## 发版实况

- **推送与 tag**:tag `v0.23.1`(标注对象 `57572d5`)指向 `8e5a1e4`,远程 `refs/tags/v0.23.1^{}` 与本地一致,tag 未移动。
  本版提交:`51a73f8`(菜单点外部收起 + 互斥)→ `0bf0c4c`(通配符复制)→ `95271ab`(重命名整行加高)→
  `8e5a1e4`(重新生成 dist 入口)。
- **workflow 结论**(sha `8e5a1e4` 共 5 条,**一次全绿**):
  - `ci` #69(master 推)06:28:43→06:33:06 success(4m23s);
  - `ci` #70(tag 推)06:28:53→06:32:10 success(3m17s);
  - `release` #25 06:28:53→06:34:25 success(5m32s);
  - `aliyun-image` #24 06:28:53→06:33:39 success(4m46s);
  - `caddy-image` #26 06:28:53→06:44:41 success(15m48s,照例最慢)。
- **第 7 步硬证据(产物级)**:
  - GitHub Release `v0.23.1` 资产 7 个 = 四平台 `tar.gz` + 两个 windows `zip` + `checksums.txt`;
    六个包本机 `shasum -a 256 -c checksums.txt` **逐行 OK**(本次无 0 字节下载)。
  - `releases/latest` 的 302 落在 `/releases/tag/v0.23.1`(非草稿、非预发布)。
  - ghcr:`latest` 与 `0.23.1` 同为 `sha256:8416a7c2…`,双架构。
  - ACR(本机 `docker login` 仍有效,`imagetools inspect` 取证):`0.23.1` = `latest` = `sha256:3a3ccd6e…`,
    `0.23.1-docker` = `latest-docker` = `sha256:1dcccca0…`,均双架构。
  - 随车 Caddy:`ghcr.io/fuuhoo/pipewright-caddy:latest` 与 `v0.23.1` 同为 `sha256:aadbe2f3…`。
  - 版本自述:Release 里的 `darwin_arm64` 二进制报
    `pipewright 0.23.1 (commit 8e5a1e4, built 2026-11-20T06:28:24Z, go1.26.11, darwin/arm64)` ——
    commit 是打 tag 那条,不是 `dev`。
  - 空目录抽查发布二进制:`reset-admin-password --generate` 退出码 **2**,目录里**没有留下 `.db`**。
- **发版前本地门禁**:`make fmt-check` / `make vet` / `make test`(含 `-count=1`)全绿,
  `npm run typecheck` / `npm run test`(115 文件 / 1374 用例)/ `npm run build` 全绿,
  `make go-build` 正常注入版本。`:18080` 一次性实例完成交互实测:重命名两格均为整行 295×36、
  箭头朝下;步骤菜单点外部收起;通配符复制生成 `cp -a ./target/*.jar './docker/test'` 形态。
- **本地实例**:`:8080` 已用 tag 版本重建并重启,`/version` 报 `v0.23.1` / commit `8e5a1e4`,
  入口 `index-DARk8SZz.js`,与 `web/dist` 一致。
- **沿用上版结论**:镜像自述一项仍跳过 `docker run`(ghcr 拉取会长时间卡住),
  改用 `imagetools inspect` + Release 包自述取证,步骤见 `RELEASE-V0.23.0.md`。

