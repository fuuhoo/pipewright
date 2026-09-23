# 需要人工验证的清单 — Pipewright v6.2

> **状态:WIP**(活文档)。
> 所有标 ✅ 的项均由联调/单测覆盖;标 ⚠️ 的需在有 docker / MySQL 的真实环境验证;
> 标 🚧 的项是 v6.2 设计文档要求但本期未实现(详见 § 4)。
>
> 上次更新:2026-09-24 —— 本轮做完分组权限(P0–P5):数据/判定/HTTP 收口/前端四段,四角色真机冒烟全绿(§ 4b);credentials owner 列随 `0058` 闭环(C1 撤销)。
> 相关文档:`docs/功能修改plan-v6.2.md`、`docs/v6.2-IMPLEMENTATION-STATUS.md`(需求/阶段对账,§2.8 是镜像通路清单、§2.10 是分组权限)。

---

## 1. 顶层状态总览

| 阶段 | 状态 | 说明 |
|---|---|---|
| 后端 16 个阶段(1-16) | ✅ 全部完成 | 阶段 11/12 本轮补齐(镜像白名单保存期+执行期、build 包走目录),见 `git log` 与工作区改动 |
| 构建环境「唯一来源」全链路 | ✅ 本轮完成 | 手填镜像通路已消灭:表单 / 「原始参数」 / 默认值 / AI 生成 / push registry 五条都收到预置目录;详见 `docs/v6.2-IMPLEMENTATION-STATUS.md` §2.8 |
| 前端 admin 5 页 + 我的凭据 + 菜单隔离 + 删 YAML | ✅ `5086691` | 41 个 i18n 文件、6 Vue 页、1 e2e 套件(9/9) |
| 前端阶段 20(节点用目录选择器) | ✅ 本轮完成 | 节点抽屉「构建环境」下拉 + 「配置资源」多选(按语言过滤) |
| 前后端联调 | ✅ `29b19ff` + `e4f7b90` | 真实 UI → 真实 Go → 真 SQLite 8/8 通过,修了 3 个集成 bug |
| 真机 docker 验证 | ✅ 本机(docker 29.4.0) | A1/A3/A5/A5b/A5c 通过;A2 真实拉取与 A4 并发上限待验(见 §2) |
| 一键启动脚本 start.sh | ✅ `607b725` | 本地原生端到端跑通,Compose 路径未真跑 |
| 验收 + 不变式测试 | ✅ `af906d6` | 26 条不变式测试覆盖 R3/R8/R11/R13/R15/R16/P0#3/RBAC 等 |
| 分组权限(P0–P5) | ✅ 本轮完成(未提交) | 数据/判定/HTTP 收口/前端四段齐全;真机矩阵 79 条断言全绿(`scripts/perm-matrix.py`),报告 `docs/分组权限测试报告.md`,复现步骤见 § 4b |

---

## 2. ⚠️ 需要真实 docker + 网络验证(5 项)

沙箱环境无 docker / 无外网,以下需你在有 docker 的机器上验证。

| # | 验证项 | 操作 | 预期 | 状态 |
|---|---|---|---|---|
| A1 | 构建环境**手动检查**镜像 | `POST /api/admin/build-envs/{id}/check` 或点前端【检查】 | `imageCheckStatus: "available"`;不存在的镜像 → `"unavailable"` + 错误信息 | ✅ 本机(docker 29.4.0):`maven:3.9-...` → unavailable + registry 探测超时消息;启动期自动检查给全 12 条三态 |
| A2 | **手动拉取** `docker pull` | 改 image → 点【拉取】 | 镜像落到本地;私有仓库先 login 后 logout,`~/.docker/config.json` 无残留 | 🟡 接口路径已验(`/pull` 异步置 checking);**真实拉取未验** —— 本机到 docker hub 出网不通 |
| A3 | **启动期自动检查** + `sync.Once` | 起服务两次看 log | 两次都跑自动检查,但同进程不重复打 docker daemon | ✅ 本机(10:53 重启后 12 条环境全部拿到真实三态) |
| A4 | **并发上限 10** + 单任务 60s | `PIPEWRIGHT_CHECK_CONCURRENCY=2` + 11 个 seed 环境 | 同时只有 2 个 inspect 进程(`ps aux \| grep docker` 验证) | ⚠️ 待验 |
| A5 | **实际构建跑通(镜像来自预置目录)** | 项目 test 存一条 script 节点:`buildEnvId=alpine` + `echo PRESET_OK && cat /etc/os-release` | 构建成功、容器身份即所选镜像 | ✅ 本机 run `f9775fe6`:日志见 `$ docker run ... alpine:latest` → `PRESET_OK` / `ID=alpine` |
| A5b | **配置资源注入容器** | 同节点加 `configProfileIds=default-npmrc`(node 环境) + `wc -c /root/.npmrc` | 容器内目标路径存在且内容来自目录文件 | ✅ 本机 run `1c050ea8`:`-v .../config_profiles/.../.npmrc:/root/.npmrc` → `90 /root/.npmrc` |
| A5c | **手填镜像被拒(保存期)** | PUT `/pipeline`,script 节点 config 只给 `image=ubuntu:24.04` | 422 + 可读消息指向该节点 | ✅ HTTP 422 `build_env_required`:「阶段「构建」的节点「手填镜像探针」的镜像不在预置目录内(image=ubuntu:24.04),请重新选择构建环境」 |

**快速命令**(本机已跑通,留给你复现;假设后端在 :8080、vault 配置 OK):

```bash
C=$(mktemp -d)/jar
curl -s -c $C -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"'"$(grep ^PIPEWRIGHT_ADMIN_PASSWORD .env | cut -d= -f2)"'"}' \
  http://127.0.0.1:8080/api/auth/login > /dev/null
K=$(grep csrf "$C" | awk '{print $7}')   # 会话 cookie + 可读的 csrf cookie,写接口要带 X-CSRF-Token

# 拿一个 build_env id(真实环境里 available 的才算可用)
ID=$(curl -s -b $C http://127.0.0.1:8080/api/admin/build-envs?includeDisabled=1 \
     | python3 -c 'import sys,json; print(json.load(sys.stdin)["items"][0]["id"])')

# A1: 手动检查
curl -s -b $C -H "X-CSRF-Token: $K" -X POST http://127.0.0.1:8080/api/admin/build-envs/$ID/check
# A2: 手动拉取
curl -s -b $C -H "X-CSRF-Token: $K" -X POST http://127.0.0.1:8080/api/admin/build-envs/$ID/pull
# 一键检查全部
curl -s -b $C -H "X-CSRF-Token: $K" -X POST http://127.0.0.1:8080/api/admin/build-envs/check-all

# A5/A5b/A5c:保存 + 触发 + 取日志(项目 id 换成自己的)
P=<projectId>
curl -s -b $C -H "X-CSRF-Token: $K" -H 'Content-Type: application/json' \
  -d @pipeline.json -X PUT http://127.0.0.1:8080/api/projects/$P/pipeline
R=$(curl -s -b $C -H "X-CSRF-Token: $K" -H 'Content-Type: application/json' \
     -d '{"branch":"master"}' -X POST http://127.0.0.1:8080/api/projects/$P/runs \
   | python3 -c 'import sys,json; print(json.load(sys.stdin)["id"])')
curl -s -b $C "http://127.0.0.1:8080/api/runs/$R"          | python3 -m json.tool | head   # status
curl -s -b $C "http://127.0.0.1:8080/api/runs/$R/logs"     | python3 -c 'import sys,json;[print(e["text"]) for e in json.load(sys.stdin)["lines"]]'
```

> **key 名注意**:配置资源的引用键是 `configProfileIds`(小写 d)、构建环境是 `buildEnvId`。
> 写成 `configProfileIDs` 不会报错(未知键原样透传),但也就不会注入 —— 我在 A5b 第一趟就踩了这个。

---

**观察(不阻塞,但值得决定要不要改)**:本机到 docker hub 出网不通时,`/check` 一律 unavailable,
**即使镜像本地已存在**(如 `php:8.3-cli-alpine` 在本地已有,仍被标 unavailable)。
执行侧不受影响(`docker run` 会用本地镜像,A5 就是证据),但 UI 上"不可用"的口径偏严 ——
若希望"本地有即算可用",检查逻辑要先探本地 `docker image inspect` 再探 registry。

## 3. ⚠️ 需要 MySQL 验证(2 项)

```bash
# 准备
export PIPEWRIGHT_TEST_MYSQL_DSN='root:pw@tcp(127.0.0.1:3306)/pipewright'
# B1: 56 个迁移在 MySQL 跑通
GOTOOLCHAIN=local go test ./internal/storetest/ -count=1 -v -run TestMigrationsApplied
# B2: 双方言 CRUD 一致
GOTOOLCHAIN=local go test ./internal/users/ ./internal/buildenv/ ./internal/configprofile/ ./internal/vault/ -count=1
```

预期:
- B1: 56 个迁移全 PASS,`schema_migrations` 行数 = 56(SQLite 侧本机已跑到 0058)
- B2: 所有跨方言单测 PASS,UNIQUE/CONSTRAINT 行为一致

---

## 4. 🚧 v6.2 设计要求但未实现 / 本轮补齐(C1/C3/C4 已闭环,仅剩 C2)

| # | 项 | 现状 | 影响 | 后续故事 |
|---|---|---|---|---|
| ~~C1~~ | **P0 #2**:credentials 加 `owner_id/enabled/description/disabled_by` | ✅ 本轮完成(`0058_credential_owner`) | — | scope/owner 的 CHECK 由 Go 侧写入前校验代替(SQLite 不能给已有表补 CHECK);见 `docs/v6.2-IMPLEMENTATION-STATUS.md` §2.5 |
| C2 | 用户**邀请注册流**(`/api/admin/users/invitations` + `/api/auth/register`) | ❌ 未实现 | admin 只能手动 INSERT users(我联调就是这么干的) | 独立 story |
| ~~C3~~ | `internal/build` 接入构建环境目录(阶段 15b) | ✅ 本轮完成 | — | 见 `docs/v6.2-IMPLEMENTATION-STATUS.md` §2.8 |
| ~~C4~~ | 配置资源注入容器(原 `runner.ConfigInjector`) | ✅ 本轮完成(改走 `-v` 挂载,非 `docker cp`) | — | 真机证据见 §2 A5b |

**v6.2 收尾追加的边界(不是未做,是明确豁免)**:阶段旁挂服务(`StageServicesEditor`)、阶段后置步骤、
非 DAG 的 `settings.Steps[].Image` 三处仍可手填镜像 —— 它们是临时 side-car/清理容器,不属于「构建环境」。
`push_image` 的 registry 不在节点上填,由**运行所在环境绑定**的镜像仓 + 凭据在服务端解析。

---

## 4b. 🔐 分组权限真机冒烟(本轮已跑通,可复现)

这一项**不需要外部条件**(docker/MySQL 都不用),只要两个浏览器会话。目的是确认权限真的在服务端生效,而不是只把按钮藏起来。

```text
1) 以 admin 登录 → 「设置 / 用户管理」新建三个普通账号(口令随意,≥8 位)
      lead / mem / out
2) 「分组」页新建 private 组 G,组长 = lead,名册加 mem(out 不加)
3) 项目列表把 smoke-go 归到 G;「设置 / 服务器」把一台主机归到 G
4) 另一个浏览器(或无痕窗口)以 out 登录,逐项看:
      - 概览:项目数 / 运行总数 / 服务器在线数都比 admin 少
      - 项目、运行、服务器、容器四个列表里都不出现 G 的资源
      - 直接粘 URL 开 /projects/<G 内项目>/pipeline 与 /runs/<G 内运行>
        → 页面显示「无权访问:该资源属于私有分组,你不在名册里」+ 重试,不是空白也不是 404 文案
      - 「分组」页对 out 是空的;侧栏没有 构建环境 / 配置资源
5) 以 lead 登录:能开 G 的名册增删、能改组内项目/主机归属(含挪回未归组),
   但「重置口令 / 禁用账号」这类平台级操作应当 403
6) 以 admin 把 out 禁用 → 该账号立刻无法再登录(已签发的会话不踢,这是刻意取舍)
```

自动化侧同口径的断言在 `internal/httpapi` 的 guard 测试与 `internal/access` 的表驱动测试里;
上面这遍是补 UI 层(错误态文案、菜单隔离、列表收敛)。

**已经跑过一遍并留了报告**:`docs/分组权限测试报告.md`(79 条断言全绿,含逐条实际响应)。重跑:`python3 scripts/perm-matrix.py`(夹具幂等,`teardown` 子命令清场)。

---

## 5. 🎨 文案 / 翻译审阅(2 项)

| # | 项 | 现状 | 期望 |
|---|---|---|---|
| D1 | **7 个非中文语言的 i18n 译文** | zh-CN 是人工译文;`en`/`zh-TW`/`ja`/`ko`/`es`/`fr`/`de` 用英文占位 | 母语者人工审阅替换。`fallbackLocale=zh-CN`,缺译不会出现裸 key |
| D2 | **P0 #4 错误文案** `IMAGE_NOT_CHECKED`/`IMAGE_UNAVAILABLE` | 现在显示中文(英文:"Image has never been checked") | 8 语言翻译,与 build_envs 命名空间 i18n 一致 |

**重点审阅文件**(英文占位):

```bash
# 5 个新命名空间 × 7 语言 = 35 个文件
ls web/src/i18n/locales/{en,zh-TW,ja,ko,es,fr,de}/{buildEnvs,configProfiles,adminUsers,adminCredentials,myCredentials}.ts
```

---

## 6. ⚠️ start.sh Compose 路径(1 项 — 我没真跑过镜像构建)

`./start.sh compose` 端到端测试:
- ✅ deps 检查(docker + compose plugin)
- ✅ `.env` 生成/复用
- ❌ **完整 `docker compose up -d` → 镜像构建 → 容器启动 → 健康检查** 这一整段(为节省时间,我在 docker build 阶段就停了)

```bash
# 在你的有 docker 机器上:
cd /path/to/pipewright
./start.sh compose           # 本地构建镜像(首次 ~5 分钟)
# 或:
./start.sh compose --pull    # 拉 ghcr.io 预构建镜像

# 启动后验证:
./start.sh status
docker logs pipewright --tail=50
# 浏览器开 http://localhost:8080,用 .env 里的 PIPEWRIGHT_ADMIN_PASSWORD 登录

# MySQL profile:
./start.sh compose --profile mysql
```

---

## 7. 联调 seed 脚本(给你参考,联调前置)

每次后端用新 DB 重启后都要跑一次(`alice` + 2 条凭据):

```bash
# 位置:/tmp/seed_lianjiao.sh(沙箱里临时存的)
# 在干净后端环境(alice 不存在、凭据为空)运行,供 8 个联调 e2e 使用。

bash /tmp/seed_lianjiao.sh
```

**复制粘贴版本**:

```bash
#!/usr/bin/env bash
set -euo pipefail
DB="${1:-/tmp/pw.db}"
BASE="${2:-http://127.0.0.1:8080}"

# 1. 普通用户 alice
mkdir -p cmd/tmp-mkuser && cat > cmd/tmp-mkuser/main.go <<'GO'
package main
import (
  "database/sql"; "fmt"; "os"
  _ "modernc.org/sqlite"
  "github.com/huangchengsir/pipewright/internal/auth"
)
func main() {
  h, err := auth.HashPassword(os.Args[2])
  if err != nil { panic(err) }
  db, _ := sql.Open("sqlite", os.Args[1])
  defer db.Close()
  _, err = db.Exec(`INSERT OR REPLACE INTO users (id, username, password_hash, role, enabled, description, created_at, updated_at) VALUES ('11111111-2222-3333-4444-555555555555','alice',?,'user',1,'e2e regular user','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`, h)
  if err != nil { panic(err) }
  fmt.Println("user seeded:", os.Args[3])
}
GO
GOTOOLCHAIN=local go run ./cmd/tmp-mkuser "$DB" 'alice-pass-1234' alice
rm -rf cmd/tmp-mkuser

# 2. 凭据
C=$(mktemp)
curl -s -c "$C" -H 'Content-Type: application/json' -d '{"username":"admin","password":"'"$(grep ^PIPEWRIGHT_ADMIN_PASSWORD .env | cut -d= -f2)"'"}' "$BASE/api/auth/login" > /dev/null
K=$(grep pipewright_csrf "$C" | awk '{print $7}')
curl -s -b "$C" -H "X-CSRF-Token: $K" -H 'Content-Type: application/json' \
  -d '{"name":"e2e-admin-personal","type":"git_token","scope":"personal","secret":"ghp_e2e_personal_123"}' \
  -o /dev/null "$BASE/api/credentials"
curl -s -b "$C" -H "X-CSRF-Token: $K" -H 'Content-Type: application/json' \
  -d '{"name":"e2e-global","type":"git_token","scope":"global","secret":"ghp_e2e_global_123"}' \
  -o /dev/null "$BASE/api/credentials"
```

---

## 8. 优先级建议

按「信息密度 / 阻塞后续」排序(A5/A1/A3 本轮已在本机验掉,从列表移除;C1 已随 `0058` 闭环,也移除):

1. **§ 6 start.sh compose** — 一键部署能不能用
2. **B1**(MySQL)— 数据迁移在另一边的可靠性(现在要跑 56 个,含分组那 5 支)
3. **A2**(外网真实 `docker pull`)+ **A4**(检查并发上限)— 都需要能出网的机器
4. **检查口径**(§2 观察,可选)— 本地已有镜像是否该算 available
5. **D1**(i18n)— 7 语言译文审阅(工作量大但不阻塞)
6. **C2** — 邀请注册流(独立 story)
7. **会话撤销** — 禁用账号 / 重置口令都不踢掉已签发的会话(刻意取舍,见 `docs/v6.2-IMPLEMENTATION-STATUS.md` §2.10);要不要做真撤销是个产品决定

---

## 附录:联调 8 例清单(都已自动通过)

`web/e2e/v62-integration.spec.ts`:

| ID | 场景 |
|---|---|
| I1 | build-envs UI 新建 → 列表+1 → 编辑 → 删除 → 行数还原 |
| I2 | P0#4 三态:unchecked 启用被拒,显示 IMAGE_NOT_CHECKED |
| I3 | config-profiles UI 新建 + multipart 真实文件上传 + 清理 |
| I4 | credentials:personal 可禁用提示、global 按钮置灰 |
| I4b | 禁 global 凭据 → 403(code=forbidden),不是 500 |
| I5 | 操作后审计出现 build_env_create,actor=`admin:admin`(session 注入) |
| I6 | users 页列出 alice,角色标签=user |
| I7 | 重复 (language,version) → 弹窗不关 + 显示冲突文案 |

跑(需后端:vault + 普通用户 + 2 条凭据):

```bash
cd web
PLAYWRIGHT_BROWSERS_PATH=/tmp/pw-browsers \
  npx playwright test e2e/v62-integration.spec.ts --workers=1
```
