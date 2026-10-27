<div align="center">

# Pipewright

**一个轻量、自托管的 CI/CD + 部署 + 运维一体化平台。**
单个 Go 静态二进制(内嵌前端,运行时零依赖),
一个工具替掉「CI + Ansible/Kamal + Portainer」三件套。

[![Release](https://img.shields.io/github/v/release/fuuhoo/pipewright)](https://github.com/fuuhoo/pipewright/releases)
[![CI](https://github.com/fuuhoo/pipewright/actions/workflows/ci.yml/badge.svg)](https://github.com/fuuhoo/pipewright/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

简体中文 | [English](README.en.md)

</div>

---

## 为什么是 Pipewright

主流方案要么重(Jenkins 一堆插件 + JVM),要么是三个工具拼装(Woodpecker/Drone + Ansible/Kamal + Portainer)。Pipewright 把「持续集成、多服务器部署、服务器/容器运维」装进**一个静态二进制**:下载、启动、打开浏览器,就是全部安装过程。

如今它已长过那三件套:把部署好的服务挂上 HTTPS 域名(原本的 nginx + certbot 那一步)、给每个 PR 一个能点开的预览地址,也都内置了。

| | Pipewright | Jenkins | Drone + Ansible + Portainer |
|---|:---:|:---:|:---:|
| 单二进制部署 | ✅ | ❌ JVM + 插件 | ❌ 三套拼装 |
| 可视化流水线编排(DAG) | ✅ 内置画布 | 插件 | YAML 手写 |
| 流水线即代码(按分支演进) | ✅ | 插件 | ✅ |
| 隔离构建 | ✅ | ✅ | ✅ |
| 多服务器部署(SSH,免 Agent) | ✅ 内置 | 插件 | Ansible |
| 服务器 / 容器运维 | ✅ 内置 | ❌ | Portainer |
| 镜像零停机 + 失败回滚 | ✅ | 插件 | 自己写 |
| 自动 HTTPS + 域名反向代理 | ✅ 内置 | ❌ | ❌ |
| Per-PR 预览环境 | ✅ 内置 | ❌ | ❌ |
| DORA 指标 | ✅ 内置 | 插件 | ❌ |
| AI 失败诊断 | ✅ 可选 | ❌ | ❌ |
| 一键自更新 | ✅ | ❌ | ❌ |

## 界面预览

**全局概览** —— 项目、运行成功率、环境部署态、服务器健康、DORA 指标,一屏全局:

![Dashboard](docs/screenshots/dashboard.png)

**可视化流水线编排** —— 阶段/任务两级 DAG 画布:横向连线串行、纵向并排真并行,支持矩阵构建、人工审批门、旁挂服务、阶段后置步骤;画布与 YAML 双向往返:

![流水线编排画布](docs/screenshots/pipeline-canvas.png)

**运行详情** —— 阶段流转、实时日志(SSE 推送 + 历史回放)、构建产物与镜像引用、逐步骤状态:

![运行详情](docs/screenshots/run-detail.png)

**容器管理** —— 跨主机容器/镜像/Stacks/卷/网络一站管理,生命周期操作、实时 stats、日志、交互终端:

![容器管理](docs/screenshots/containers.png)

## 能力总览

- **🔐 安全地基** —— 单管理员认证(argon2id + CSRF)· 凭据加密保险库(NaCl secretbox,掩码呈现,绝无明文)· OAuth 应用接入 Gitee / GitHub / GitLab / Gitea / 自建实例(拿到的 access token 直接存成可复用的保险库凭据)· append-only 审计(SQLite trigger 硬拦 UPDATE/DELETE)+ 可选远端 sink · 按 run 解析真实凭据后对日志 / 诊断 / 通知全链路脱敏。
- **🧩 项目与流水线** —— 可视化编排画布(阶段 DAG + 阶段内任务级 DAG)· 矩阵构建 · 人工审批门(可直接在通知里点签名链接审批)· 旁挂服务(测试挂 DB/Redis)· 阶段 `when` 条件 + 阶段后置步骤 · 类型化运行参数(枚举/布尔/数字,触发时即校验)· 触发方式:webhook、分支→环境映射、5 字段 cron 定时、上游→下游流水线串联(深度门 + 路径门防环)· 项目级并发上限 + 超限 FIFO 排队 · 复用库:流水线模板 / 变量组 / 自定义节点 · 服务端权威合法性校验。
- **📝 流水线即代码** —— 把流水线结构写进 `.pipewright.yml`、按分支各自演进,画布配置始终作为兜底回退([详见下文](#流水线即代码gitops))。
- **🤖 AI 经 MCP 操作平台** —— 同一套 8 个工具挂两种形态(HTTP 端点 `/mcp/<令牌 id>` / `pipewright mcp` stdio),AI 客户端能建项目、存流水线、触发构建、读日志、取消运行;每条端点一把令牌 + 两把尺(能用哪些工具、能看哪些分组),鉴权与审计沿用浏览器那条线([详见下文](#ai-接入mcp))。
- **🏗 隔离构建与产物** —— 版本钉死的容器内隔离构建(docker/nerdctl/podman)· 代码管理区:本地 bare 镜像 + 增量 fetch,秒级出工作区 · 项目依赖缓存:每项目一个固定目录 bind 进构建容器,Maven/npm/Go/pip 跨运行复用,**流水线里零配置** · 项目缓存清单与一键清除:代码镜像 / 依赖缓存在哪、多大当场看得见,怀疑拿到旧代码或旧 SNAPSHOT 时清掉重跑(逐次留审计) · 任务级构建依赖缓存(按分支 + lockfile hash 寻址)· 内容寻址制品库,jar/dist 存**真字节**供部署(而非占位 reference)· 镜像构建 + 推送私有仓库 + 镜像 GC · 每项目可指定远程构建机(构建经 SSH 下沉到远程,token 只留控制机;远程侧按设计不留常驻缓存)· JUnit + Cobertura 测试报告喂质量门禁,不过则阶段失败、阻断下游部署 · 实时终端日志(SSE)+ 历史回放 · 只读代码浏览(Monaco)。
- **🚀 多服务器部署** —— 经 SSH 免 Agent 部署 · 产物直铺到指定目录(`jar`/`dist` 落进去就是内容本身)+ 重启命令 + 健康门控 · 镜像部署停旧起新、失败回滚上一镜像 · **compose 整栈交付**(仓库里那份 `docker-compose.yml` 直接交目标机的 compose CLI,与「容器」页管的是同一份栈)· **K8s 发布**(平台直连集群 API,不经跳板机)· 多机并行扇出 + 部分失败可见 · 命令型部署(无产物,直接重启服务)· **环境一等公民**:逐环境部署时间线、当前活跃版本、一键回滚到上一次全成功部署 · 环境晋级流(dev→staging→prod)+ 逐环境变量/密钥 + 审批门。
- **🌐 自动 HTTPS + 域名反向代理** —— 每台目标主机一个托管 Caddy 容器,复用与容器运维同一套 SSH + docker 手法编排(渲染 Caddyfile → `docker cp` → 优雅 reload)。证书经 Let's Encrypt 自动签发/续期:HTTP-01,或**经 Cloudflare / DNSPod / 阿里云 DNS 走 DNS-01**(通配符必需)。另有:多域名别名、路径路由(`/api`→A、`/`→B)、重定向、访问控制(basic auth、IP 允许/拒绝 CIDR)、HSTS / 安全头 / 压缩、多上游负载均衡 + 主动健康检查故障转移、WebSocket / gRPC(h2c) / TCP 透传(caddy-l4)、按真实 443 握手探测的证书大盘、一键子域名。
- **🔎 Per-PR 预览环境** —— 某 PR 的运行成功部署后,自动分配一次性域名 `pr-<n>-<proj>.<base>`(带自己的证书与路由),评审者点开链接就能看到这条 PR 真实跑起来的样子。同一 PR 幂等复用;自动回收,但**仅在**确证 PR 已关闭/合并时才回收。
- **📣 通知** —— 企业微信 / 钉钉 / 飞书 / Slack / 邮件 / 自定义 webhook · 事件→渠道细粒度路由 · 模板 + 变量自定义 · 飞书富卡片(审批/详情行动按钮 + 发版汇总)· 流水线内通知节点。
- **🖥 服务器与容器运维** —— 多机状态总览(CPU/内存/磁盘)+ 指标时序趋势图 · 容器/镜像/Stacks/卷/网络管理 · 容器创建/inspect/prune · 实时 + 历史服务日志 · 实时 stats · 容器交互终端 · Web 运维终端(主机 shell,完整复制粘贴/信号支持)· 可配置异常检测:定时自动跑、冷却去重、命中走通知渠道。
- **🤖 AI 辅助(可选,完全可降级)** —— 自带 Claude / OpenAI / Ollama 端点(apiKey 密文入保险库)。构建/部署失败自动根因诊断 + 👍/👎 反馈飞轮与准确率统计 · 仓库分析 → 生成流水线草案 · 成功/失败提交差异 · 脚本风险标注 · 运维终端的自然语言→shell 助手与容器诊断。核心 CI/CD 路径完全不依赖它(NFR-10)。
- **📈 度量** —— DORA 四指标(部署频率/变更前置时长/变更失败率/平均恢复时长)开箱即用,并给 Elite/High/Medium/Low 绩效分档。
- **🧹 数据与平台** —— 运行数据保留清理器(默认关;绝不动在跑的运行)· SQLite(纯 Go)或 MySQL · 8 种界面语言(简中 / 繁中 / 英 / 日 / 韩 / 德 / 法 / 西),API 错误信息亦服务端本地化。
- **🔄 检查 + 一键自更新** —— 设置→系统 一键查 GitHub 最新发布并语义比对;二进制部署可页面**一键自动更新**(下载 + 校验和核验 + 原子替换 + 自重启),Docker 部署给出精确升级命令。

> 安全不可妥协:凭据仅以密文存储、命令 array 化防注入、出网 SSRF 收口、日志脱敏。

## 安装 / 部署

三种形态任选,平台本体是单静态二进制、**运行时零依赖**(无需 Go/Node)。

> **Docker 前置**:平台本体不依赖 Docker,但**「隔离构建 / 容器部署」需要 Docker**(没有则降级到桩 runner、不做真实构建)。控制台 / SSH 部署 / 通知不需要。一键脚本会**检测 Docker** 并在缺失时提示;Linux 下可 `INSTALL_DOCKER=1` 自动安装(经官方 get.docker.com),macOS 请装 Docker Desktop。

### ① 一键脚本(Linux / macOS)

从 GitHub Release 下载对应平台的静态二进制装到 `/usr/local/bin`(含校验和核验 + Docker 检测):

```bash
curl -fsSL https://raw.githubusercontent.com/fuuhoo/pipewright/master/install.sh | sh

# 钉版本 / 自定义目录 / Linux 顺带自动装 Docker:
VERSION=v0.13.0 INSTALL_DIR=$HOME/.local/bin INSTALL_DOCKER=1 \
  sh -c "$(curl -fsSL https://raw.githubusercontent.com/fuuhoo/pipewright/master/install.sh)"

# 运行(首次启动引导管理员;master key 用于凭据保险库)
PIPEWRIGHT_MASTER_KEY=$(openssl rand -base64 32) \
PIPEWRIGHT_ADMIN_PASSWORD=change-me \
  pipewright          # 打开 http://localhost:8080,用 admin / change-me 登录
```

**推荐:装为 systemd 服务**(开机自启 + 崩溃重启 + 一键自更新可用;Linux,需 root)。脚本会自动持久化 master key 到 `/etc/pipewright/master.key`、数据落 `/var/lib/pipewright`、配置写 `/etc/pipewright/pipewright.env`:

```bash
SETUP_SERVICE=1 sh -c "$(curl -fsSL https://raw.githubusercontent.com/fuuhoo/pipewright/master/install.sh)"
# 状态 / 日志:systemctl status pipewright  ·  journalctl -u pipewright -f
# 改端口等:编辑 /etc/pipewright/pipewright.env 后 systemctl restart pipewright

# 用 MySQL 而非默认 SQLite(DSN 为 go-sql-driver 格式,parseTime=true 必带):
SETUP_SERVICE=1 PIPEWRIGHT_DB_DRIVER=mysql \
  PIPEWRIGHT_DB_DSN='user:pw@tcp(host:3306)/pipewright?parseTime=true&charset=utf8mb4' \
  sh -c "$(curl -fsSL https://raw.githubusercontent.com/fuuhoo/pipewright/master/install.sh)"
```

**便携形态:二进制与全部数据都落在执行命令的那个目录**(免 root、免系统路径,整目录拷走就是迁移)。切进那个目录跑一条命令,脚本会顺手生成 master key、运行配置与启动器:

```bash
mkdir -p ~/pw && cd ~/pw
LOCAL_INSTALL=1 sh -c "$(curl -fsSL https://raw.githubusercontent.com/fuuhoo/pipewright/master/install.sh)"

PIPEWRIGHT_ADMIN_PASSWORD=change-me ./run.sh   # 默认监听 :8080;口令只在首启建账时用到
```

落点全在实例根(要换地方就显式传 `INSTALL_DIR`):`./pipewright` 二进制 · `./data/`(sqlite 库,`artifacts/ repos/ cache/ config_profiles/` 与它同级)· `./master.key`(0600)· `./pipewright.env`(0600,改端口/切 MySQL 都在这里,重装不覆盖)· `./run.sh` 启动器。数据路径一律写绝对路径,所以从别的目录拉起 `./run.sh` 也还认这一份库。与 `SETUP_SERVICE=1` 互斥(便携形态不写 systemd);整目录迁移时 `master.key` 务必同带,换 key 旧凭据无法解密。

**连构建的临时工作区都收进这一个目录**:脚本生成的 `pipewright.env` 里没有 `TMPDIR`,而构建工作区是 `os.MkdirTemp` 建的、落点跟 `TMPDIR` 走(`internal/build/cloner.go:141`),所以不补就会写到系统 `/tmp`。手动加一行再重跑 `./run.sh` 即可:

```bash
echo "TMPDIR=$(pwd)/work/tmp" >> ./pipewright.env && mkdir -p ./work/tmp
```

项目依赖缓存不用管:进程形态默认就落在库同级的 `data/project-cache`(`internal/projectcache/root.go:46`)。补完在真机(CentOS 7)实测:进程打开的可写文件只剩本目录内的 `pipewright.db{,-wal,-shm}` 与日志,`/tmp`、`/var/tmp`、`~/.cache` 下没有任何 `pipewright*` 残留。唯一仍在目录外的是 docker 镜像本身 —— 构建指挥的是宿主 daemon,镜像落在 `/var/lib/docker`。

> Windows 用户:到 [Releases](https://github.com/fuuhoo/pipewright/releases) 下载 `.zip`。

### ② docker compose(推荐自托管)

```bash
curl -fsSLO https://raw.githubusercontent.com/fuuhoo/pipewright/master/docker-compose.yml
curl -fsSLO https://raw.githubusercontent.com/fuuhoo/pipewright/master/.env.example
cp .env.example .env       # 至少设 PIPEWRIGHT_ADMIN_PASSWORD,并 openssl rand -base64 32 填 MASTER_KEY
docker login registry.cn-qingdao.aliyuncs.com   # ACR 上这两档是**私有**仓库,匿名拉直接 401
docker compose up -d       # 数据持久化在具名卷 pipewright-data;切 MySQL 见 .env 注释
```

> 登录这步别省:实测拿匿名 token 请求 `.../v2/fubin/pipewright/manifests/latest` 返回 HTTP 401,`docker compose up -d` 会卡在 `pull access denied`。ACR 的账号是**独立于阿里云账号密码**的「访问凭证 / 固定密码」,在容器镜像服务控制台设;只读拉取用 RAM 子账号即可。
> 把 `PIPEWRIGHT_IMAGE` 换成 `ghcr.io/fuuhoo/pipewright` 时同样可能要登录(取决于那个包设成公开还是私有 —— 本仓库文档没实测过 ghcr 的匿名拉取,拉不到就先 `docker login ghcr.io`,用户名是你的 GitHub 用户名、密码用带 `read:packages` 的 PAT)。

compose 文件本身只读这几个变量(平台自身的环境变量见下方[配置](#配置环境变量)表):

| 变量 | 说明 | 默认 |
|---|---|---|
| `PIPEWRIGHT_IMAGE` | 镜像仓库前缀 | `registry.cn-qingdao.aliyuncs.com/fubin/pipewright`(阿里云 ACR 青岛,两档都有);`ghcr.io/fuuhoo/pipewright` 也能用,但只有默认档 |
| `PIPEWRIGHT_VERSION` | 镜像标签。**两档**见下 | `latest` |
| `PIPEWRIGHT_PORT` | 宿主侧发布端口 | `8080` |
| `PIPEWRIGHT_WORK_DIR` | 容器内构建的工作区目录(宿主绝对路径)。它同时是**项目依赖缓存**的落点(该目录下的 `pipewright-project-cache/`),所以想让容器里的构建跨运行吃到缓存,只需把这一条路径映射到宿主 | 空(此时容器内隔离构建不可用,缓存落容器 `/tmp`) |
| `PIPEWRIGHT_RUNNER` | 留空即默认 DAG 执行器;`legacy` 回退旧版固定流程 | 空 |
| `PIPEWRIGHT_DATA_DIR` | 「配置资源」上传文件的落点(其下的 `config_profiles/`)。要在容器里构建并勾配置资源,它也得是宿主同路径目录 —— 用 `PIPEWRIGHT_WORK_DIR/data` 就共用同一条映射,不必再加一行 | `./data`(容器 WORKDIR 下,即在具名卷里) |
| `PIPEWRIGHT_BIN_DIR` | 宿主放二进制的目录,仅在「只换二进制」覆盖文件里用(见下) | `./bin` |

数据(sqlite 库、制品库、代码管理区、构建依赖缓存)全落在容器 `/data`,由具名卷 `pipewright-data` 持久化。master key 一旦写入凭据就要长期保存 —— 换 key 会让已存凭据无法解密。

想让数据就是一个能 `rsync` / 能直接看目录的文件夹,把上面那条卷挂载换成宿主 bind 即可(`- /srv/pipewright/data:/data`)。只有一个坑:**默认档以 nonroot(uid 65532)运行**,具名卷会继承镜像里 `/data` 的属主(`Dockerfile:72-74`),而宿主目录通常是 root 所有 —— 首启就会写不进 sqlite 库。要么先 `chown 65532:65532`,要么用 `-docker` 档(它刻意以 root 跑,因为要访问宿主 socket)。

**例外**:项目依赖缓存**不**放 `/data`。它是被 `docker run -v` 挂进构建容器的,而 `-v` 的源路径由**宿主** daemon 解析 —— `/data` 在宿主上的真身是 `/var/lib/docker/volumes/pipewright-data/_data`,拿 `/data/...` 当源只会在宿主根下凭空建一个空目录。所以它跟着 `PIPEWRIGHT_WORK_DIR`(要求宿主同路径)走,详见下节。

「配置资源」的磁盘副本(`PIPEWRIGHT_DATA_DIR` 下的 `config_profiles/`)是同一条规则的第二个受害者:勾上的资源是按 `target_path` 只读挂进构建容器的,路径只在容器里可见时,docker 现在会直接报 `bind source path does not exist`(而不是像过去那样挂进一个空目录,由 Maven 说句 `Is a directory`)。要在容器里构建又用配置资源,把 `PIPEWRIGHT_DATA_DIR` 指到 `PIPEWRIGHT_WORK_DIR/data` 即可 —— 共用那一条同路径映射。留默认值时启动日志会带一条 `[configprofile] 警告` 说明这点。

#### 两档镜像:要不要在容器里打包镜像

| 标签 | 基底 | 容器内能否构建镜像 |
|---|---|---|
| `<版本>` / `latest` | distroless:非 root、无 shell、无容器 CLI | **不能**。启动日志会明说回退桩构建器 |
| `<版本>-docker` / `latest-docker` | alpine + docker CLI,以 root 运行 | 能,还要再挂宿主 socket(见下) |

两档都由 `.github/workflows/aliyun-image.yml` 出多架构镜像并发布到阿里云容器镜像服务(青岛),compose / `.env` 默认就拉它。推 `v*` tag 时 GoReleaser 另外把默认档发到 `ghcr.io/fuuhoo/pipewright`(`latest` / `<版本>`);ghcr 上没有 `-docker` 档 —— 要 `-docker` 就从 ACR 拉,或用根目录 `Dockerfile` 自行 `--target with-docker` 构建。默认档是刻意选的:最小攻击面;只有确实要在平台容器里构建/推镜像,才升 `-docker` 档。

#### 让平台在容器里打包镜像(三处一起改,缺一不可;私有仓库再加第四处)

1. `.env` 里换到 `-docker` 档:`PIPEWRIGHT_VERSION=latest-docker`(默认 `PIPEWRIGHT_IMAGE` 已经是 ACR,两档都有 —— 若换成 `ghcr.io/fuuhoo/pipewright`,ghcr 上没有 `-docker` 标签,会拉不到镜像);
2. 放开 `docker-compose.yml` 的 `- /var/run/docker.sock:/var/run/docker.sock`。**挂 socket 等同授予容器宿主 root 级权限**,只在信任的自托管机器上开;
3. 放开工作区目录的自绑定,并把 `TMPDIR` 指到同一目录 —— 三者的值都是 `.env` 里的 `PIPEWRIGHT_WORK_DIR`。

第 3 步不是洁癖:script 节点跑隔离构建发的是 `docker run -v <工作区>:<挂载点>`,而 daemon 是**宿主**的 —— 它按宿主路径解析 `-v` 的源。路径不一致时 daemon 会在宿主凭空建一个空目录,构建拿到空工作区,症状是日志里全是 `no such file or directory` 而平台侧看着一切正常。`docker-compose.yml` 的 `volumes` 注释里有逐行说明。

**用到私有镜像再加第 4 处**(基础镜像在自己的 ACR / Harbor 里,或 `pushImage` 推私有库)。挂上 socket 之后,构建与推送都是**容器内的 docker CLI** 发的,而它读的是**自己**那个 `~/.docker/config.json` —— 容器里那份是空的,宿主的登录态带不过来。实测症状不是「找不到镜像」而是每个私有镜像都 `denied` / `unauthorized: authentication required`,极易被误读成镜像不存在。修法是把宿主的凭证文件只读挂进去:

```yaml
volumes:
  # 宿主是谁登录的,就挂谁的 config.json;:ro 是刻意的 —— 容器不该往宿主写凭据
  - ${PIPEWRIGHT_HOST_DOCKER_CONFIG:-/root/.docker/config.json}:/root/.docker/config.json:ro
```

`-docker` 档以 root 运行,路径正好对上;`PIPEWRIGHT_HOST_DOCKER_CONFIG` 用于非 root 用户或自定义 `DOCKER_CONFIG` 的机器。只在默认档或非私有仓库时才不需要这一条,所以 `docker-compose.yml` 里它保持注释状态。

起容器后一条命令验证是否真通(`-docker` 档有 shell,可以在容器里直接试):

```bash
docker exec pipewright sh -c 'mkdir -p "$TMPDIR/probe" && printf "FROM alpine:3.20\nRUN echo ok\n" > "$TMPDIR/probe/Dockerfile" && docker build -q "$TMPDIR/probe"'
```

打印出 `sha256:…` 即成。中间那行 `DEPRECATED: The legacy builder is deprecated` 是预期内的:`-docker` 档刻意不带 buildx 插件(带了 `docker build` 就转给 buildx,对宿主 daemon 版本有要求;不带则任何版本都能跑)。

#### 本机已经跑着裸二进制实例?套现成覆盖层 `docker-compose.devtest.yml`

上面三处改动仓库里备了一份现成的叠加文件,顺带把端口与容器名让开,容器形态可以与 :8080 的裸实例并存:

```bash
# .env 里设:PIPEWRIGHT_VERSION=<版本>-docker、PIPEWRIGHT_PORT=8081、PIPEWRIGHT_WORK_DIR=<宿主绝对路径>、
#           PIPEWRIGHT_DATA_DIR=$PIPEWRIGHT_WORK_DIR/data(配置资源也要宿主同路径,见上)
mkdir -p "$PIPEWRIGHT_WORK_DIR"      # 工作区目录得先在宿主存在,同路径映射才有意义
docker compose -p pwtest -f docker-compose.yml -f docker-compose.devtest.yml up -d
```

覆盖层只有三行动作:容器名换 `pipewright-composetest`;`ports` 用 `!override` **替换**而不是追加(compose 默认对列表是追加,不写就同时申请 8080 和 8081,和裸实例抢端口直接起不来);放开 docker.sock 与工作区的同路径自绑定。数据走 `pwtest_pipewright-data` 具名卷、依赖缓存走 `PIPEWRIGHT_WORK_DIR`、配置资源走它的 `data/` 子目录(同一条同路径映射,勾了配置资源的构建节点才挂得进),都在项目名 `pwtest` 底下,与默认那套 compose 互不干扰;`down` 不带 `-v` 即保留数据。挂 socket = 容器拿到宿主 root 级权限,这份文件是给信任的本机环境用的,别抄上生产。

#### 在容器里看宿主机上的镜像 / 容器

这两块走的是 SSH 而非 socket —— 平台的镜像与容器面板一律 SSH 到目标机执行命令,没有「读本机 socket」这条捷径。而容器内的 `localhost` 是容器自己,所以 compose 已配好:

```yaml
extra_hosts:
  - "host.docker.internal:host-gateway"
```

它把 `host.docker.internal` 解析到宿主网关。剩下的一步在界面里:**设置 → 服务器** 新增一台主机填 `host.docker.internal`(端口 22,凭据用宿主的 SSH 私钥或口令)。登记后该服务器卡的「镜像」「容器」屏读的就是宿主 daemon 的内容,部署容器也一样。

> 容器内构建与「SSH 登记宿主」两件事互不依赖:前者只影响流水线里能不能在平台容器里出镜像,后者只影响 UI 能否看见宿主镜像。只用平台做部署、构建在别处的话,一档 distroless + 不挂 socket 就够了。

#### 只换二进制:把程序本体映射进容器

发布镜像里真正属于程序的只有一个约 40MB 的静态二进制,其余都是运行环境(alpine / distroless、docker CLI、CA 证书、`/data`)。宿主拉镜像慢、或者你就是想让「升级」等于换文件,可以让镜像退化成运行环境,把宿主的二进制映射进容器里跑。

在 `docker-compose.yml` 同目录放一个覆盖文件:

```yaml
# docker-compose.binary.yml —— 镜像只当运行环境,程序本体来自宿主
services:
  pipewright:
    # 跑映射进来的那个,而不是镜像里烘焙的 /pipewright
    entrypoint: ["/opt/pipewright/bin/pipewright"]
    volumes:
      # 挂目录而非单文件(原因见下);路径相对本文件所在目录,.env 里给绝对路径也行
      - ${PIPEWRIGHT_BIN_DIR:-./bin}:/opt/pipewright/bin:ro
    healthcheck:
      # 必须跟着改到新路径:镜像里那个 /pipewright 还在,不改的话健康检查探的仍是它
      test: ["CMD", "/opt/pipewright/bin/pipewright", "--version"]
```

compose 合并时 `volumes` 是**追加**(上方的 `pipewright-data:/data` 照旧生效),`entrypoint` 与 `healthcheck` 是覆盖。起容器:

```bash
mkdir -p bin
# 二进制来源二选一 —— 要的是**容器**的平台,不是宿主的:
#   a) 就在目标机上装(只落 ./bin,不写 systemd;变量要给 sh,不是给 curl)
curl -fsSL https://raw.githubusercontent.com/fuuhoo/pipewright/master/install.sh |
  INSTALL_DIR=./bin SETUP_SERVICE=0 sh
#   b) 本机交叉编译(GOARCH 跟着容器架构;BIN= 是为了不覆盖仓库里的 ./pipewright)
GOOS=linux GOARCH=amd64 make go-build BIN=bin/pipewright

docker compose -f docker-compose.yml -f docker-compose.binary.yml up -d
```

之后升级 = 换那个文件 + restart,一次镜像都不拉:

```bash
curl -fsSL https://raw.githubusercontent.com/fuuhoo/pipewright/master/install.sh |
  VERSION=v0.5.2 INSTALL_DIR=./bin SETUP_SERVICE=0 sh
docker compose -f docker-compose.yml -f docker-compose.binary.yml restart
curl -s localhost:8080/version    # {"version":"v0.5.2","commit":"dfe5f43",…} —— platform 就是你编译的那个 arch
```

几个要点,前两条是本机实测结论:

- **架构要对容器**,不是对宿主:macOS 上 `make build` 出的是 darwin 二进制,挂进 linux 容器只会 `exec format error`。二进制是 `CGO_ENABLED=0` 纯静态,同一份文件在两档镜像里都能跑 —— 本机(arm64)在 `latest`(distroless, nonroot)与 `latest-docker`(alpine, root)都验过:容器里镜像自带的 `/pipewright --version` 报 `0.5.0`,映射进来的报 `v0.5.2`,`/version` 返回后者,登录与接口照常。
- **挂目录,别挂单文件**:换二进制的安全姿势是「写临时文件 + rename」(`install.sh` 和平台自更新都这么做),而 rename 会换掉 inode。目录挂载跟着目录走,`restart` 就能看到新版;单文件挂载绑的是创建容器时那个 inode,之后能否看见新版取决于运行时怎么解析宿主路径(本机 OrbStack 上 `restart` 后确实换了,但别把这事赌在 Linux dockerd 上)。坚持挂单文件的话,换完用 `up -d --force-recreate`。
- **权限**:二进制 `0755`,宿主目录得可遍历 —— 默认档以 nonroot(uid 65532)运行,靠的是 others 位的 r-x。SELinux enforcing 的机器(RHEL / Fedora 系)bind 进来可能 `permission denied`,给挂载加 `:z` 再试。
- **别反向降级**:数据卷照旧,新版首启自动跑迁移;但拿旧二进制挂已经有新版 schema 的库不行 —— 迁移只往前走。想回退就连文件一起回退(留一份上一版二进制即可)。
- **界面里的「立即更新」在这个形态下不适用**:容器里有 `/.dockerenv`,平台按 Docker 形态只给你 `docker compose pull && docker compose up -d`(`internal/version/selfupdate.go:29-43`),而 pull 换的是镜像层,真正跑的还是映射进来那个文件。要在 UI 里一键更新,用 ① 的二进制形态。

### ③ docker run(最快试用)

```bash
docker run -d -p 8080:8080 -v pipewright-data:/data \
  -e PIPEWRIGHT_ADMIN_PASSWORD=change-me \
  -e PIPEWRIGHT_MASTER_KEY=$(openssl rand -base64 32) \
  registry.cn-qingdao.aliyuncs.com/fubin/pipewright:latest
```

### ④ 内网机器:GitHub 与 Docker Hub 都不通

这是一台 CentOS 7 内网机上跑通的路线(它到 `raw.githubusercontent.com`、`registry-1.docker.io`、`ghcr.io` 全部超时,到自己那台 ACR 通),按「平台本体 → 构建环境 → 源码 → 依赖」四层逐个解决。

**平台本体**。compose 形态照 ② 走(默认仓库就是 ACR,先 `docker login`)。单机/便携形态下 Release 下不到,就从已经拉下来的镜像里把二进制取出来 —— 它是 `CGO_ENABLED=0` 的静态文件,CentOS 7 这类老系统直接跑得动:

```bash
D=/home/pipewright && mkdir -p $D/data $D/work/tmp
cid=$(docker create registry.cn-qingdao.aliyuncs.com/fubin/pipewright:latest-docker)
docker cp $cid:/pipewright $D/pipewright && docker rm -f $cid && chmod +x $D/pipewright
```

这套动作等于把 ① 便携形态里脚本自动做的事手工做一遍:`master.key` 生成一次(`openssl rand -base64 32 > master.key && chmod 600 master.key`,已有就别再动 —— 换了旧凭据解不开)、`pipewright.env` 手写 ① 列的那几行(记得带上 `TMPDIR`)、`run.sh` 照 ① 的启动器抄。

**构建环境镜像**。预置清单写的是 Docker Hub 名(`node:26-alpine`、`golang:1.22-alpine`…,见 `internal/buildenv/seed.go`),离线机上一个都拉不到,症状是流水线第一个构建节点就报 pull 超时。两条路:

- 把要用的镜像同步进自己的 ACR / Harbor,然后在 **设置 → 构建环境** 逐条改「镜像」地址;镜像多就在能上网的机器导出清单、批量改掉前缀再导入(构建环境支持导出 / 导入)。
- 改完点列表页的「检查全部」(或选中若干条点「检查所选」):每条状态应变成「本地可用」(构建机上已有)或「可拉取」。**改过镜像地址的行会退回「未检查」,要检查通过才能启用**;仍是「不可用」的那条一被选中就会在构建节点失败。离线机上可以顺手点该页的「拉取」,把「可拉取」的镜像提前落到构建机,省掉流水线里那次注定超时的现场拉取。

**源码仓库**。内网自建 Git 服务能用,但有两个硬条件,外加一条凭据上的别扭:

- 传输必须是**智能 HTTP**(`git-http-backend` CGI)或 SSH。把裸仓库用静态文件方式分享出去(如 `python3 -m http.server` 挂 `--bare` 目录)属于 dumb HTTP,go-git 会直接拒成 `repo_unreachable`。
- 地址不能填 `127.0.0.1` / `localhost` / `file://` —— 回环、链路本地(含云元数据 `169.254.169.254`)都在 SSRF 禁止区(`internal/giturl/giturl.go:145`),填了被判非法。**RFC1918 私网与内网域名是放行的**,所以填那台机器的内网 IP 或 DNS 名即可。
- 仓库允许匿名拉取时,项目里仍旧要挂一条凭据:校验规则是「填了仓库地址就必须选凭据」(`internal/project/project.go:204-205`)。而保险库不收空 secret(`internal/vault/vault.go:215-216`),所以建一条 `git_token`、填个不会被用到的占位值即可 —— 匿名 HTTP 仓库不会真的把它带上。

**依赖源**。npm / Maven / pip / Go 拉不到公网时,用 **设置 → 配置资源** 把 `.npmrc` / `settings.xml` / `pip.conf` 挂进构建容器(目标路径该页有芯片可点),`GOPROXY` 之类可以写进流水线命令或环境变量。容器形态下记得:配置资源的 `PIPEWRIGHT_DATA_DIR` 也必须宿主同路径,否则 docker 报 `bind source path does not exist`(见 ② 的 ④ 条注释)。

### 部署完怎么确认这条路真通

界面打得开不代表能用 —— 最容易坏的两件事(「构建拿不到工作区」「私有镜像拉不到」)都不会让平台自己报错。跑一条最小流水线,一次覆盖产物与镜像两条腿:

1. 新建项目,仓库指向你的 git 地址(离线机按上一节);
2. 流水线三段:**源** → **构建产物**(产物档,填构建命令与 `artifactPath`)→ **构建镜像**(镜像档,`pushImage` 关掉,`Dockerfile` 的 `FROM` 用构建机上**已有**的镜像,免得又去拉公网);
3. 跑一次,运行详情页应出现**两条**产物:一条 `archive`(体积就是 `artifactPath` 打包结果)、一条 `image`;
4. 在构建机上 `docker images | grep '^pipewright/'` —— 本地镜像命名规则是 `pipewright/<项目名 slug>:<提交短 sha>`(`internal/build/builder.go:35,462`)。看到它就说明镜像确实落在了宿主 daemon 里,而不是构建器空转;
5. 回运行详情页下载那条 `archive`,能取回字节即产物存储可读。

单机形态在本机直接构建,不需要先在「设置 → 服务器」登记自己;容器形态要能在容器里出镜像,得先按 ② 把 socket 等三处放开。

实测参考值(CentOS 7 单机形态,只依赖标准库的 go 例子,基础镜像构建机上已有):`archive` 4763800 字节、本地图像 13185840 字节;冷跑 17 秒,同项目第二次 2 秒。这个例子不下载依赖,所以差值只反映克隆与打包的开销 —— 依赖型项目的冷暖差距看 `data/project-cache`(平台自带的每项目零配置缓存)。

### 从源码构建

```bash
make build          # 前端构建 → go:embed → 单个静态二进制 ./pipewright(纯 Go,无 CGO)
./pipewright --version
```

### 更新

打开 **设置 → 系统**,点「检查更新」查最新发布;有新版时:

- **二进制部署**:点「立即更新」即自动下载新版 + 校验和核验 + 替换 + 重启(需对二进制文件有写权限;装在 `$HOME/.local/bin` 免 sudo,或用 `SETUP_SERVICE=1` 装的 root systemd 服务亦满足)。
- **Docker 部署**:容器不替换自身镜像,按提示在宿主执行 `docker compose pull && docker compose up -d`(数据卷保留)。
- **映射二进制的 compose 形态**(见上节):换 `./bin/pipewright` 那个文件再 `docker compose -f docker-compose.yml -f docker-compose.binary.yml restart`,不拉镜像;`curl :8080/version` 核对新版本号。
- **便携 / 单机形态**(见 ①):二进制就在实例目录里、属主是你,所以「立即更新」的写权限条件天然满足;但它的下载仍旧走 GitHub Release —— 没有外网时按 ④ 从新版镜像 `docker cp` 出二进制覆盖 `./pipewright`,再重跑 `./run.sh`,`pipewright.db` 与 `master.key` 一个字节都不用动。

### 配置(环境变量)

正常安装只需前两个(若跑在反代后面再加 `PIPEWRIGHT_PUBLIC_URL`),其余都有合理默认值。

**核心**

| 变量 | 说明 | 默认 |
|---|---|---|
| `PIPEWRIGHT_ADMIN_PASSWORD` | 首次启动管理员口令(库里已有管理员时忽略;忘了口令见下方「忘了管理员口令」) | 无(须设置) |
| `PIPEWRIGHT_MASTER_KEY` | 凭据保险库主密钥(base64 的 32 字节);或用 `PIPEWRIGHT_MASTER_KEY_FILE` 指文件 | 未配则保险库禁用 |
| `PIPEWRIGHT_ADDR` | HTTP 监听地址 | `:8080` |
| `PIPEWRIGHT_PUBLIC_URL` | 外部可访问的基址(如 `https://ci.example.com`)。webhook 回调、OAuth 回跳、通知里的签名审批链接、PR 状态跳转链接都需要它 | 无 |
| `PIPEWRIGHT_ADMIN_USERNAME` | 首次启动管理员用户名 | `admin` |
| `PIPEWRIGHT_TRUST_PROXY` | 采信 `X-Forwarded-For` 首段作为审计来源 IP(`1`/`true`/`yes`/`on`)。除非前面确有可信反代,否则别开 —— 否则任意客户端都能伪造审计来源 IP | 关 |
| `PIPEWRIGHT_RELEASE_REPO` | 检查更新所查的 GitHub 仓库(fork 可改) | `fuuhoo/pipewright` |
| `PIPEWRIGHT_RUNTIME` | 设 `docker` 显式声明容器部署形态(影响自更新方式);否则经 `/.dockerenv` 自动探测 | 自动探测 |
| `PIPEWRIGHT_AUDIT_SINK` | 远端审计 sink:`http(s)://` 端点,或填其它值作为第二份本地 JSON Lines 文件路径。本地库被删后审计仍完整 | 无 |

**忘了管理员口令**:`PIPEWRIGHT_ADMIN_PASSWORD` 只在库里还没有管理员那次生效,之后改它不会动账号。在**装本平台的那台机器**上跑一条子命令即可,它只开本机库、不占端口,新口令下一次登录就生效:

```bash
pipewright reset-admin-password --stdin < newpass.txt   # 也支持 --password-file / --generate
docker exec -it <容器名> /pipewright reset-admin-password --stdin   # compose 形态:二进制在镜像根的 /pipewright
```

口令只从标准输入/文件/`--generate` 进,刻意不做 `--password` 参数(ps 与 shell 历史都会留一份),也不回读 `PIPEWRIGHT_ADMIN_PASSWORD`。它只处理内置管理员;别的账号要用另一个管理员登录后在「用户管理」里重置。

> **先在哪个目录跑,决定改的是哪个库。** 库路径默认是相对当前目录的 `pipewright.db`,而这条命令只会动「解析到的那一个」。`docker exec` 默认落在镜像的 `/data`(就是库所在处);要是 `cd` 进了子目录(如 `/data/artifacts`),解析到的会是旁边一个不存在的文件 —— 现在它会**直接停下报错**而不是新建空库假装成功。切回 `/data`,或写全路径:
>
> ```bash
> docker exec -it <容器名> sh -c 'cd /data && /pipewright reset-admin-password --stdin'
> docker exec -it -e PIPEWRIGHT_DB=/data/pipewright.db <容器名> /pipewright reset-admin-password --stdin
> ```
>
> 报错第一行会打出它**实际解析到的库文件绝对路径**和当前目录,拿它核对是不是你要改的库;真改成功了,它也会回一句 `数据库=<路径>`。真要从空库冷启动,再加 `--create-missing`。

**数据库**

| 变量 | 说明 | 默认 |
|---|---|---|
| `PIPEWRIGHT_DB_DRIVER` | 数据库驱动:`sqlite` 或 `mysql` | `sqlite` |
| `PIPEWRIGHT_DB` | SQLite 数据库路径(driver=sqlite 时)。默认值相对当前目录:在哪个目录起服务就用的是哪个库,别在子目录里起 | `pipewright.db` |
| `PIPEWRIGHT_DB_DSN` | 连接串:driver=mysql 时必填(账号密码都在里面),driver=sqlite 时也可代替 `PIPEWRIGHT_DB` 写文件路径 | 无 |

**运行与构建**

| 变量 | 说明 | 默认 |
|---|---|---|
| `PIPEWRIGHT_RUNNER` | 运行执行器:默认 DAG(按画布 stages/script/deploy_ssh/notify 编排执行);设 `legacy` 回退旧版固定流程 | `dag` |
| `PIPEWRIGHT_BUILDER` | `auto` 探测到 docker/nerdctl/podman 用真实构建、否则回退桩;`real` 无容器 CLI 直接启动失败;`stub` 完全不碰容器 | `auto` |
| `PIPEWRIGHT_MAX_CONCURRENT` | 全局同时运行上限(超出保持 queued 排队,FIFO)。项目级上限在界面里配 | worker 数(4) |
| `PIPEWRIGHT_ARTIFACT_DIR` | 制品库目录(jar/dist 真字节) | `<DB 同级>/artifacts` |
| `PIPEWRIGHT_REPO_CACHE_DIR` | 代码管理区(本地 bare 镜像)目录 | `<DB 同级>/repos` |
| `PIPEWRIGHT_NO_REPO_CACHE` | `1` 关闭代码管理区(每次构建直连网络克隆) | 关 |
| `PIPEWRIGHT_CACHE_DIR` | 构建依赖缓存目录(任务里配了 `cachePaths` 的脚本节点,按 key 归档搬运**工作区内**目录) | `<DB 同级>/cache` |
| `PIPEWRIGHT_NO_BUILD_CACHE` | `1` 关闭构建依赖缓存(每次冷构建) | 关 |
| `PIPEWRIGHT_PROJECT_CACHE_DIR` | 项目依赖缓存根:每个项目一个固定目录并 bind 进构建容器(Maven/npm/Go/pip/Gradle 的家目录),流水线里零配置;容器形态下这一路径必须与宿主同路径,否则缓存不进持久盘 | 进程形态 `<DB 同级>/project-cache`;容器形态 `<TMPDIR>/pipewright-project-cache` |
| `PIPEWRIGHT_NO_PROJECT_CACHE` | `1` 关闭项目依赖缓存(依赖每次重下) | 关 |
| `PIPEWRIGHT_NO_IMAGE_GC` | `1` 保留构建出的镜像,不做垃圾回收 | 关 |
| `PIPEWRIGHT_PAC_RUNTIME` | `1` 对**所有项目**强开流水线即代码,无视各项目开关 | 关 |
| `PIPEWRIGHT_CHAIN_MAX_DEPTH` | 流水线串联深度上限(防无限链的硬兜底) | `5` |

**集成**

| 变量 | 说明 | 默认 |
|---|---|---|
| `PIPEWRIGHT_PR_STATUS` | `1` 对**所有项目**强开 PR 状态回写,无视各项目开关 | 关 |
| `PIPEWRIGHT_PR_STATUS_GITHUB_BASE` | GitHub API 基址(GitHub Enterprise 用) | 公有 GitHub |
| `PIPEWRIGHT_PR_STATUS_GITEE_BASE` | Gitee API 基址(自建 Gitee 用) | 公有 Gitee |
| `PIPEWRIGHT_CADDY_IMAGE` | 反代镜像。默认是自构建的 Caddy(含 DNS-01 / ratelimit / layer4 插件);用原版 `caddy:2` 也能跑,但会失去 DNS-01/通配符/TCP 能力 | `ghcr.io/fuuhoo/pipewright-caddy:latest` |
| `PIPEWRIGHT_PREVIEW_SWEEP_INTERVAL` | 预览环境回收扫描间隔(Go duration,如 `10m`) | `5m` |

**运维监控**

| 变量 | 说明 | 默认 |
|---|---|---|
| `PIPEWRIGHT_ANOMALY_INTERVAL` | 异常检测间隔(秒);`0` 关闭定时(仍可手动「立即检测」) | `60` |
| `PIPEWRIGHT_ANOMALY_COOLDOWN` | 同「服务器×规则」重复告警的最小间隔(秒) | `600` |
| `PIPEWRIGHT_METRICS_SAMPLE_INTERVAL` | 服务器指标采样间隔(秒,趋势图数据源);`0` 关闭采样 | `60` |
| `PIPEWRIGHT_METRICS_RETENTION_DAYS` | 指标样本保留天数 | `7` |

## 部署到目标机:四种交付形态

部署节点一律经 SSH 下发,**目标机零 Agent**;命令 array 化(绝不拼进 shell 字符串),产物与正文只以文件落地。按交付物选一种:

| 节点 | 交付什么 | 关键配置 | 失败时 |
|---|---|---|---|
| `deploy_ssh` | jar / dist 等文件产物,直铺进你指定的目录 | `serverIds` + `deployPath`(+ `restartCommand`) | 就地覆盖、无自动回滚(要回到上一版本就重跑那次部署) |
| `deploy_docker` · `run` | 单容器:拉上游镜像 → 停旧起新 | `containerName` / `ports` / `runArgs` | 自动回滚到上一镜像 |
| `deploy_docker` · `compose` | 整栈:一份 `docker-compose.yml` 交目标机的 compose CLI | `stackName` + 正文来源(**不接产物**) | 该机记 failed,其余机器继续,可只重试失败机 |
| `deploy_k8s` | 集群里的镜像或清单 | `clusterId` + `manifestSource` | 滚动失败默认回填上一镜像 |

### compose 整栈部署(`deploy_docker` + `dockerMode: compose`)

目标机装好 docker 与 compose CLI,就能一次交付一套服务。

- **正文来源二选一**:`composeSource: repo` + `composeFile: deploy/docker-compose.yml`(读项目仓库里那份,跟代码一起走 PR 评审、按分支演进),或 `composeSource: paste` + `composeYaml`(把正文粘在节点里)。正文上限 512 KiB,`stackName` ≤128 字符。
- **和「容器」页管的是同一份栈**:正文原样上传为目标机的 `/opt/pipewright/stacks/<stackName>/docker-compose.yml`,再执行 `docker compose -p <stackName> up -d`。流水线发出去的栈因此直接出现在容器页那台机器的 Stacks 里,能在界面上接着看日志、重启、down —— 不是两份互相看不见的部署。
- **时间预算 8 分钟**(建目录 + 上传 + `up`,因为 `up` 要在目标机拉镜像)。这一档**不读 `strategy`**:分批是「多台机器逐台铺」的编排,单个栈没有分批对象;多主机时仍是逐台执行。
- **与 `run` 的分工**:只换镜像、拓扑不变 → `run`(它复用镜像产物链路,自带停旧起新与失败回滚);服务数量/网络/卷要一起变 → `compose`。

健康门控是部署节点上的可选配置(不是独立节点),对 `deploy_ssh` 与 `deploy_docker` 两档都能开:`healthProbe: http` + `healthUrl`(地址是**部署机本机视角**,如 `http://localhost:8080/healthz`),或 `healthProbe: command` + `healthCommand`(在目标机执行,非零退出即不通)。重试节奏 `healthRetries`(默认 3、上限 20)/ `healthInterval`(默认 3 秒、上限 60 秒,填 0 就是不等)/ `healthTimeout`(单次超时,默认 5 秒、上限 60 秒);任意一次通过就放行,次数用尽则**该机判失败并阻断下游**。

`deploy_k8s` 不跑这套门控,配了也会被保存期拒绝 —— 它的落点是一个 API server,整条链路没有 shell;这一档的把关是集群自己的 rollout 就绪(见上表)。

探测不通之后各档做什么并不一样,**只有单容器那一档会自己退回去**:

| 档位 | 探测不通时 |
|---|---|
| `deploy_ssh`(文件直铺 / 部署命令) | 该机记 failed,**不回滚** —— 直铺已就地覆盖,没有「上一版本」可切,要退回就重跑上一次发布 |
| `deploy_docker` · `run` | 停掉新容器、**回滚到上一镜像**(首次部署没有上一镜像,则记 failed) |
| `deploy_docker` · `compose` | 该机记 failed,**不回滚**栈(`up -d` 的结果就停在那里),其余机器继续,可只重试失败机 |
| `deploy_k8s` | 不做探测;滚动不就绪由集群把关,默认回填上一镜像 |

```yaml
version: 1
stages:
  - id: stg_src
    name: 流水线源
    kind: source
    jobs:
      - name: 仓库源
        type: git_source
  - id: stg_deploy
    name: 部署
    kind: deploy
    needs: [stg_src]
    jobs:
      - name: compose 整栈
        type: deploy_docker
        config:
          serverIds: "1"
          dockerMode: compose
          composeSource: repo
          composeFile: deploy/docker-compose.yml
          stackName: shop-web
          healthProbe: http
          healthUrl: http://localhost:8080/healthz
```

## 流水线即代码(GitOps)

把流水线结构写进仓库的 `.pipewright.yml`,**跟代码同源、走 PR 评审、按分支演进**——不再依赖画布配置的隐式漂移。

- **开启**:在项目的流水线页打开「流水线即代码 / Pipeline as code」开关(按项目维度)。
- **生效方式**:开启后,每次运行都从**本次构建分支**的**仓库根**读取 `.pipewright.yml`(分支为空时回退项目默认分支),用其中的流水线 spec 驱动本次运行;不同分支可携带各自的 `.pipewright.yml`。文件用项目绑定的仓库凭据临时拉取,无新增暴露面。
- **永不卡住运行的回退**:文件**缺失** → 回退到画布(UI)里已配置的流水线;文件存在但 **YAML 非法** → 同样回退到已存的画布配置。
- **作用范围**:YAML 只管**流水线结构**(阶段 / 任务 / `needs` / DAG 编排);**变量与缓存、环境与凭据、触发规则**仍来自画布(UI)设置,**不写在 YAML 里**。
- **schema** 与平台「从 YAML 导入」用的是同一套(`version` + `stages` → `jobs`,job 用嵌套 `script:` 块写 `image`/`commands`/`env`/`workdir`)。
- **节点类型**(画布与 YAML 通用):`git_source`、`script`、`build_backend`、`build_frontend`、`build_image`、`push_image`、`deploy_ssh`、`deploy_docker`、`deploy_k8s`、`deploy_frontend`、`notify`、`templated`、`custom`。

```yaml
version: 1
stages:
  - id: stg_src              # needs 按阶段 id 引用,故跨阶段依赖须显式写 id
    name: 流水线源
    kind: source
    jobs:
      - name: Git 源
        type: git_source
  - id: stg_build
    name: 构建
    kind: build
    needs: [stg_src]
    jobs:
      - name: 运行测试
        type: script
        script:
          image: golang:1.23
          commands:
            - go vet ./...
            - go test ./...
          env:
            CGO_ENABLED: "0"
          workdir: src/app
  - id: stg_deploy
    name: 部署
    kind: deploy
    needs: [stg_build]
    gate: true               # 人工审批门
    when:
      branches: [main, release/*]
    jobs:
      - name: SSH 部署
        type: deploy_ssh
        config:
          targetEnv: prod
```

> 也可用全局环境变量 `PIPEWRIGHT_PAC_RUNTIME=1` 对**所有项目**强制开启流水线即代码(无视各项目开关),供向后兼容 / 高级用户使用。

## AI 接入(MCP)

让 AI 客户端(Claude Code / Cursor / 任何 MCP 客户端)替你把活干了:建项目、把流水线存进去、触发构建、读日志、必要时取消。

**第一步永远是建一条 MCP 端点**:网页「设置 → MCP 端点」→ 新增端点。一条端点 = **一条自己的地址**(`/mcp/<令牌 id>`)+ 一把令牌 + 两把尺;它**代表某个具体账号干活**,权限与那人本人登录时完全一致(默认签给当前登录者,也可以指定归属人)。平台只存摘要,明文**只在新增那一次**给出,关掉窗口就再也读不回来,丢了只能撤销重发。改范围走「编辑范围」,地址与明文都不动,客户端不用重新配置。

端点地址上挂着的就是这条凭据的身份:`/mcp/<id>` 只认签发它时给出的那把令牌,拿别的令牌打过来一律 403(先验令牌再比地址,免得用 403 反探端点存不存在)。老地址 `/mcp` 依旧可用,任何一把有效令牌都能连,适合不需要按地址区分凭据的脚本。

**每条端点都能「使用说明」一键复制**:那是段纯文本,把这条端点的地址、能碰哪些工具、怎么填进 Cursor / VS Code / Claude Code 的配置、以及下面那两个坑,一次讲完 —— 整段贴给 AI 就能让它照着接入,不用你先解释一遍平台是什么。内容跟着范围走:只读端点不会写「动手前先确认写操作」这种废话;新增完那一次的说明直接带明文,列表里再打开时明文已经读不回来了,说明里留占位符并提醒你贴出去前先替换。

新增时还能给这条端点套**两把尺**,把它收窄成「只够干某件事」的凭据:

| 尺 | 收窄什么 | 不勾时 |
| --- | --- | --- |
| 可用工具(`allowedTools`) | 工具面:客户端 `tools/list` 里只出现勾选的那些,越界调用当场 `unknown tool` | 不限(平台上的全部工具) |
| 可见分组(`groupScope`) | 数据面:只放出「勾选的分组 ∩ 归属人本来能看的项目」 | 不限(归属人本来能看的全能看) |

两把尺**只做交集、只收窄不放大**——它们不能给端点加任何归属人本人没有的权限。三条预设档(全部 / 只读 / 只读 + 跑构建)只是快捷勾选,发出去的是工具名列表本身。界面「一个没勾」发的是**不带这个键**,不是空数组:空数组会被签发接口当入参错误拒掉,因为「谁能看见什么」的缺省是不限,把缺省写成显式空是反过来的越权。分组范围是硬收窄,**归属人是管理员也照样生效**——端点收窄的语义是「这把凭据只配看见这些」,不是「这个人只配看见这些」。

改范围走 `PATCH /api/admin/api-tokens/{id}`,三态与签发时不同:**不带键 = 不动**,**显式空数组 = 解开回不限**,**非空 = 收窄**(所以「把最后一项取消勾选」就是解开)。界面上只回传真被改过的那把尺——一条端点可能存着一个后来被删掉的分组 id,一律回传两把会把它原样送回后端换 400,所以名册外的历史值会渲染成带「已失效」标签的行,勾掉它才发得出去。

HTTP 形态直连平台,`tools/list` 回的就是收窄后的那几条。stdio 形态(`pipewright mcp`)在本机全量注册工具——范围写在远端令牌的库里,这个进程既验不了令牌也读不到它的范围,所以**它不是放行口子**:越界调用打到对面照样被逐次拦下,只是本地清单里会多显示几个用不了的工具。要客户端一眼只看得到允许的那几个,用 HTTP 形态。

**两种接入形态,同一把令牌、同一套工具**(新增端点后,交付页按你用的客户端给现成片段:Cursor / VS Code / Claude Code / Claude Desktop / 通用):

```jsonc
// stdio:客户端拉起 pipewright mcp,由这个进程反向连到平台。
// 平台在内网、不想给本机开端口时用这条。令牌放 env 别放 args —— 同机其他人 ps 就能看到命令行明文。
// PIPEWRIGHT_URL 填站点根地址,不要填端点地址:/mcp/<id> 后面拼不上 /api/...(工具是回环打 REST 的)。
{
  "mcpServers": {
    "pipewright": {
      "command": "pipewright",
      "args": ["mcp"],
      "env": { "PIPEWRIGHT_URL": "https://pipewright.example.com", "PIPEWRIGHT_TOKEN": "pwt_…" }
    }
  }
}
```

```jsonc
// HTTP:客户端直连这条端点自己的地址。能摸到平台端口时用这条,机器上不用装二进制。
{
  "mcpServers": {
    "pipewright": {
      "type": "http",
      "url": "https://pipewright.example.com/mcp/<令牌 id>",
      "headers": { "Authorization": "Bearer pwt_…" }
    }
  }
}
```

**工具面刻意只做 8 个**,覆盖「从空项目到一次可复盘的构建」这条链:

| 工具 | 作用 |
| --- | --- |
| `list_projects` | 列令牌可见的项目;拿 `id` 供其余工具用 |
| `create_project` | 建项目(可只给 `name` 建纯发布型;要绑仓库必须同时给 `repoUrl` + `credentialId`) |
| `list_build_envs` | 列预置构建环境;拿 `id` 填进流水线 `config.buildEnvId` |
| `set_pipeline` | 用一份 `.pipewright.yml` **整条覆盖**项目流水线(schema 与上面「流水线即代码」同一套) |
| `trigger_run` | 触发一次运行,**立即返回**不等构建跑完 |
| `get_run` | 运行详情:整体 status + 每个步骤的状态/耗时 + 产物清单 + 部署落点 |
| `get_run_logs` | 日志压成纯文本,每行前缀标步骤与流;`sinceSeq` 续读、`maxChars` 控预算 |
| `cancel_run` | 取消排队/运行中的构建;已结束的返回 409 `run_not_cancelable` |

**为什么不会出现「网页上做不了、AI 却做得了」的口子**:这层工具不另写一套 service 调用,而是带着调用方令牌**回环打平台自己的 REST**——角色功能档位、分组数据范围、错误码、审计 actor 全部沿用浏览器那条线。带 Bearer 令牌的请求天然免 CSRF(那是浏览器会话的防线),其余校验一条不少。

**审计口径**:令牌本身只留三条痕——`api_token_create` / `api_token_update` / `api_token_revoke`(出事后要能回答「这把泄露的令牌哪天作废的」,以及「它被放行到过什么范围」)。每次用令牌调接口**不**逐条记「谁用了哪把令牌」,那些动作本来就以归属人的 actor 落在各自业务线(`project_create`、`run_trigger_manual`……),再叠一层只会把流水刷满。

**两个坑**(工具描述里也各写了一遍,但值得先知道):
- 项目若从没配过流水线就触发,平台按默认空流水线跑,结果是「成功但什么都没做」的假绿灯——**先 `set_pipeline` 再 `trigger_run`**。
- `trigger_run` 返回 `queued` 不等于跑完了,判成败看 `get_run` 的终态 + 各步骤,别只看这一条。

## 技术栈

- **后端**:Go · Chi(路由)· modernc/sqlite(纯 Go,无 CGO)+ go-sql-driver/mysql · go-git(不依赖宿主 git)· NaCl secretbox(保险库)· argon2id + bcrypt · golang.org/x/crypto/ssh(免 Agent 部署)· coder/websocket(终端)· Caddy(在目标主机上编排,提供自动 HTTPS)
- **前端**:Vue 3 `<script setup>` · Vite · naive-ui · OKLCH 双主题 · Monaco(只读代码浏览)· Vitest + Playwright · 经 `go:embed` 内嵌进二进制

依赖刻意保持精简:cron 解析器、DAG 引擎、DNS 厂商客户端、制品库/构建缓存、Caddyfile 渲染器全部自己实现,不引三方库。

## 架构

一个进程,约 45 个领域包。`internal/httpapi` 是唯一碰 HTTP 的包,所有领域包注入其中;`cmd/pipewright/main.go` 是唯一的装配层(包内互引经这里的适配器晚绑,保持包间单向依赖)。

```
单静态二进制 (cmd/pipewright)
│
├─ 地基
│  ├── internal/config        env 配置 + master key 加载
│  ├── internal/store         开库 + 迁移(sqlite / mysql 双方言)
│  ├── internal/auth          认证 + 会话 + CSRF
│  ├── internal/vault         凭据加密保险库(secretbox)
│  ├── internal/oauth         OAuth 应用接入 → token 存成保险库凭据
│  ├── internal/audit         append-only 审计 + 可选远端 sink
│  ├── internal/mask          secret 脱敏(日志 / 诊断 / 通知)
│  ├── internal/i18n          用户可见信息的服务端本地化
│  └── internal/version       版本检查 + 一键自更新
│
├─ 流水线
│  ├── internal/project       项目接入 + 仓库探测
│  ├── internal/pipeline      流水线 spec + 构建/部署配置 + 校验
│  ├── internal/pipelineyaml  YAML ↔ spec 双向往返(导入 / 导出)
│  ├── internal/pacloader     流水线即代码加载(.pipewright.yml,按分支)
│  ├── internal/library       可复用模板 + 变量组 + 自定义节点
│  ├── internal/trigger       webhook + 分支→环境映射
│  ├── internal/cron          5 字段 cron 解析器 + 分钟粒度调度器
│  └── internal/chain         上游→下游串联(防环)
│
├─ 执行
│  ├── internal/run           运行模型 + worker pool + 日志 + 产物 + 参数
│  ├── internal/dag           纯 DAG 调度内核(零 I/O)
│  ├── internal/dagrun        DAG 运行编排(阶段级 + 任务级,矩阵展开)
│  ├── internal/build         容器内隔离构建 + 镜像 + 阶段执行器
│  ├── internal/runner        每项目远程构建机配置
│  ├── internal/repocache     代码管理区(本地 bare 镜像 + 增量 fetch)
│  ├── internal/buildcache    构建依赖缓存(分支 + lockfile 寻址)
│  ├── internal/projectcache  项目依赖缓存(每项目固定目录,bind 进构建容器)
│  ├── internal/artifactstore 内容寻址的产物字节存储
│  ├── internal/testreport    JUnit + Cobertura 解析
│  ├── internal/qualitygate   纯质量门禁评估
│  ├── internal/approval      人工审批门(协调器 + 持久化)
│  └── internal/retention     运行数据保留清理
│
├─ 交付
│  ├── internal/target        通用 SSH exec/session 层(部署 + 运维共享)
│  ├── internal/deploy        SSH 部署执行 + 健康门控 + 回滚
│  ├── internal/environments  环境一等公民 + 回滚目标定位
│  ├── internal/promotion     环境晋级流 + 逐环境变量
│  ├── internal/prstatus      PR / 提交状态回写(GitHub / Gitee)
│  ├── internal/proxy         自动 HTTPS + 域名反向代理(Caddy 编排)
│  ├── internal/dnsprovider   Cloudflare / DNSPod / 阿里云 DNS(DNS-01 + 子域名)
│  └── internal/previewenv    Per-PR 预览环境 + 自动回收
│
├─ 可观测
│  ├── internal/notify        多渠道通知 + 事件路由 + 模板
│  ├── internal/anomaly       可配置阈值检测 + 告警
│  ├── internal/metrics       服务器指标时序(趋势图)
│  ├── internal/dora          DORA 指标 + 绩效分档
│  └── internal/ai            可选 AI:诊断 / 仓库分析 / 差异 / 风险标注
│
├── internal/httpapi          唯一对外 HTTP 面(约 188 条路由)
└── web/                      Vue3 前端(go:embed 内嵌)
```

## 开发状态

✅ **已正式发布**,持续迭代中 —— 最新版本见 [Releases](https://github.com/fuuhoo/pipewright/releases)(tag 驱动发版:6 平台二进制 + ghcr 与阿里云 ACR 多架构镜像)。已在真实生产环境承载多项目的构建、部署与日常运维。

## 贡献 / Contributing

欢迎 PR 与 Issue!动手前请读 [CONTRIBUTING.md](CONTRIBUTING.md)(搭环境 / 测试 / 提交规范),并遵守[行为准则](CODE_OF_CONDUCT.md)。安全漏洞请走私密渠道,见 [SECURITY.md](SECURITY.md)。

## License

MIT — 见 [LICENSE](LICENSE)。

---

<div align="center">
<sub>Pipewright —— 把 CI、部署与运维装进一个二进制。</sub>
</div>
