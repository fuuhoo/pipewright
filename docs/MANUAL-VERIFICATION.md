# 需要人工验证的清单 — Pipewright v6.2

> **状态:WIP**(活文档)。
> 所有标 ✅ 的项均由联调/单测覆盖;标 ⚠️ 的需在有 docker / MySQL 的真实环境验证;
> 标 🚧 的项是 v6.2 设计文档要求但本期未实现(详见 § 4)。
>
> 上次更新:v6.2 联调通过 + start.sh 提交后。
> 相关文档:`docs/功能修改plan-v6.2.md`、源码 commit `7abe79e` 起的几条历史。

---

## 1. 顶层状态总览

| 阶段 | 状态 | 说明 |
|---|---|---|
| 后端 16 个阶段(1-16,缺 11) | ✅ 全部 commit | 见 `git log --oneline` 全部 `阶段 N:` 前缀 |
| 前端 admin 5 页 + 我的凭据 + 菜单隔离 + 删 YAML | ✅ `5086691` | 41 个 i18n 文件、6 Vue 页、1 e2e 套件(9/9) |
| 前后端联调 | ✅ `29b19ff` + `e4f7b90` | 真实 UI → 真实 Go → 真 SQLite 8/8 通过,修了 3 个集成 bug |
| 一键启动脚本 start.sh | ✅ `607b725` | 本地原生端到端跑通,Compose 路径未真跑 |
| 验收 + 不变式测试 | ✅ `af906d6` | 26 条不变式测试覆盖 R3/R8/R11/R13/R15/R16/P0#3/RBAC 等 |

---

## 2. ⚠️ 需要真实 docker + 网络验证(5 项)

沙箱环境无 docker / 无外网,以下需你在有 docker 的机器上验证。

| # | 验证项 | 操作 | 预期 | 优先级 |
|---|---|---|---|---|
| A1 | 构建环境**手动检查**镜像 | 起服务 → `POST /api/admin/build-envs/{id}/check` 或点前端【检查】 | `imageCheckStatus: "available"`;不存在的镜像 → `"unavailable"` + 错误信息 | 高 |
| A2 | **手动拉取** `docker pull` | 改 image → 点【拉取】 | 镜像落到本地;私有仓库先 login 后 logout,`~/.docker/config.json` 无残留 | 高 |
| A3 | **启动期自动检查** + `sync.Once` | 起服务两次看 log | 两次都跑自动检查,但同进程不重复打 docker daemon | 中 |
| A4 | **并发上限 10** + 单任务 60s | `PIPEWRIGHT_CHECK_CONCURRENCY=2` + 11 个 seed 环境 | 同时只有 2 个 inspect 进程(`ps aux | grep docker` 验证) | 中 |
| A5 | **实际构建跑通** | 接真实 git 仓库 + Node 项目,流水线选 `node:20-alpine` 跑一次 | 构建成功、产物归档、runner log 无错误 | 高 |

**快速命令**(假设已起后端,vault 配置 OK):

```bash
# A1+A2:用 admin 登录拿 cookie + csrf
C=$(mktemp)
curl -s -c $C -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"'"$(grep ^PIPEWRIGHT_ADMIN_PASSWORD .env | cut -d= -f2)"'"}' \
  http://127.0.0.1:8080/api/auth/login > /dev/null
K=$(grep pipewright_csrf "$C" | awk '{print $7}')

# 拿一个 seed 的 build_env id(注意沙箱里 imageCheckStatus 是 unavailable,真实环境应是 available)
ID=$(curl -s -b $C http://127.0.0.1:8080/api/admin/build-envs?includeDisabled=1 \
     | python3 -c 'import sys,json; print(json.load(sys.stdin)["items"][0]["id"])')

# A1: 手动检查
curl -s -b $C -H "X-CSRF-Token: $K" -X POST http://127.0.0.1:8080/api/admin/build-envs/$ID/check
# A2: 手动拉取
curl -s -b $C -H "X-CSRF-Token: $K" -X POST http://127.0.0.1:8080/api/admin/build-envs/$ID/pull
# 一键检查全部
curl -s -b $C -H "X-CSRF-Token: $K" -X POST http://127.0.0.1:8080/api/admin/build-envs/check-all
```

---

## 3. ⚠️ 需要 MySQL 验证(2 项)

```bash
# 准备
export PIPEWRIGHT_TEST_MYSQL_DSN='root:pw@tcp(127.0.0.1:3306)/pipewright'
# B1: 51 个迁移在 MySQL 跑通
GOTOOLCHAIN=local go test ./internal/storetest/ -count=1 -v -run TestMigrationsApplied
# B2: 双方言 CRUD 一致
GOTOOLCHAIN=local go test ./internal/users/ ./internal/buildenv/ ./internal/configprofile/ ./internal/vault/ -count=1
```

预期:
- B1: 51 个迁移全 PASS,`schema_migrations` 行数 = 51
- B2: 所有跨方言单测 PASS,UNIQUE/CONSTRAINT 行为一致

---

## 4. 🚧 v6.2 设计要求但本期未实现 / 未落地(4 项)

| # | 项 | 现状 | 影响 | 后续故事 |
|---|---|---|---|---|
| C1 | **P0 #2**:credentials 加 `owner_id/enabled/description/disabled_by` + scope/owner CHECK 强一致约束 | ❌ 0053_credentials_owner 迁移撤销;`vault.ListWithActor`/`DisableWithActor` 走兼容 SQL | 1) `GET /api/credentials` 返回全量(不按 owner 过滤)2) 禁用 personal 接口返回 200 但**不落库**(enabled 列不存在) | 重做迁移 + 字段级测试 |
| C2 | 用户**邀请注册流**(`/api/admin/users/invitations` + `/api/auth/register`) | ❌ 未实现 | admin 只能手动 INSERT users(我联调就是这么干的) | 独立 story |
| C3 | `internal/build` 接入 `buildenv.ResolveImage`(阶段 15b) | ❌ 未实现 | job 镜像仍走 `toolchainImage()`,不会用构建环境预置的镜像 | 阶段 15b 单独推进 |
| C4 | `runner.ConfigInjector` 把 config_profile 拷贝进容器 | ❌ 未实现 | config_profile 目前只落盘,构建时不会进容器 | 阶段 15b + runner 联调 |

**C1 决策点(请告诉我走 A 还是 B)**:

- **A**:保留现状 — 接口是占位,等 0053 重做后一并生效(前端 I4 显示「已禁用」但实际没改,需要修文案)
- **B**:现在就把 disable 端点改为返回 501/403 + 前端 banner 提示「功能暂未启用」,避免误导

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

按「信息密度 / 阻塞后续」排序:

1. **C1**(P0 #2 决策)— 选 A 还是 B,影响下一阶段交付的边界
2. **A5**(实际构建跑通)— 验证 v6.2 整个构建环境管理是否真能提升部署体验
3. **A1**(手动检查镜像)— 同步 checker + 后端三态响应
4. **§ 6 start.sh compose** — 一键部署能不能用
5. **B1**(MySQL)— 数据迁移在另一边的可靠性
6. **D1**(i18n)— 7 语言译文审阅(工作量大但不阻塞)
7. C2/C3/C4 — 后续 story

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
