# HTTP API 层与中间件链现状调研

工作目录: `/home/fubin/code/pipewright`（路径均为相对路径或绝对路径；行号引用实际文件）。

---

## 1. 路由结构

### 1.1 路由注册主函数

- `internal/httpapi/router.go:397-912` — 整个 HTTP handler 由 `func New(webFS fs.FS, authn auth.Authenticator, opts ...Option) http.Handler` 构造。
- `cmd/pipewright/main.go:567-576` — 在 main 里实例化 `http.Server` 时注入 `httpapi.New(webFS, authSvc, httpapi.WithVault(...), ...)`，全路由都在那一长串 Option 列表里。
- 路由器: `github.com/go-chi/chi/v5` (`router.go:17, 408`)。

### 1.2 全局中间件链（路由根）

```go
// internal/httpapi/router.go:408-412
r := chi.NewRouter()
r.Use(middleware.Recoverer)
// Parse request locale 并包装 writer,使 writeError 能按 UI 语言本地化错误消息
r.Use(localeMiddleware)
```

只有 2 个全局中间件：`middleware.Recoverer`（chi 内置 panic 恢复）与 `localeMiddleware`（自定义，写入 `X-Pipewright-Locale` / `Accept-Language` 解析后的 locale 到包装的 `ResponseWriter`）。

### 1.3 `/api/...` 受保护组的中间件链

```go
// internal/httpapi/router.go:441-447
r.Route("/api", func(ar chi.Router) {
    ar.Use(func(next http.Handler) http.Handler {
        return requireAuth(svc, next)
    })
    ar.Use(func(next http.Handler) http.Handler {
        return requireCSRF(next)
    })
    ...
```

`/api` 下路由统一先 `requireAuth`，再 `requireCSRF`。公开入口(注册在 `/api` 组外)：
- `/api/auth/{login,session,logout}` — `router.go:421-428`
- `/api/webhooks/{token}` — `router.go:432`（靠 token 验签）
- `/approvals` + `/approvals/act` — `router.go:437-438`（靠 HMAC token 即认证）

### 1.4 路由分组（按业务领域）

下表 `meth` 列出了实际 method 注册；按需求逐一列举：

**审计 admin/audit：**
| Method | Path | 行号 | 鉴权 |
|---|---|---|---|
| GET | `/api/audit` | router.go:459 | requireAuth + requireCSRF（CSRF 豁免 GET） |

**凭证 credential（vault）：**
| Method | Path | 行号 | 鉴权 |
|---|---|---|---|
| GET | `/api/credentials` | router.go:463 | auth |
| POST | `/api/credentials` | router.go:464 | auth + CSRF |
| PATCH | `/api/credentials/{id}` | router.go:465 | auth + CSRF |
| DELETE | `/api/credentials/{id}` | router.go:466 | auth + CSRF |
| POST | `/api/credentials/{id}/reveal` | router.go:468 | auth + CSRF |

**项目 project：**
| Method | Path | 行号 | 鉴权 |
|---|---|---|---|
| GET | `/api/projects` | router.go:473 | auth |
| POST | `/api/projects` | router.go:474 | auth + CSRF |
| POST | `/api/projects/test-clone` | router.go:475 | auth + CSRF |
| PATCH | `/api/projects/{id}` | router.go:476 | auth + CSRF |
| DELETE | `/api/projects/{id}` | router.go:477 | auth + CSRF |
| POST | `/api/projects/{id}/runs` | router.go:605 | auth + CSRF |

**环境 environment（双层：promotion flow + GitLab-style 一等公民）：**
| Method | Path | 行号 | 鉴权 |
|---|---|---|---|
| GET | `/api/projects/{id}/environments` | router.go:506 | auth |
| PUT | `/api/projects/{id}/environments` | router.go:507 | auth + CSRF |
| GET | `/api/projects/{id}/promotions` | router.go:508 | auth |
| GET | `/api/projects/{id}/environments/deployments` | router.go:514 | auth |
| GET | `/api/projects/{id}/environments/{env}/history` | router.go:515 | auth |
| POST | `/api/projects/{id}/environments/{env}/rollback` | router.go:516 | auth + CSRF |
| POST | `/api/runs/{id}/promote` | router.go:564 | auth + CSRF |
| GET | `/api/runs/{id}/promotions` | router.go:565 | auth |

**流水线 pipeline（Story 2.2 spec + 2.4 settings + 2.6 validation + 2.5 AI + FR-8-12 import）：**
| Method | Path | 行号 | 鉴权 |
|---|---|---|---|
| GET | `/api/projects/{id}/pipeline` | router.go:522 | auth |
| PUT | `/api/projects/{id}/pipeline` | router.go:523 | auth + CSRF |
| POST | `/api/projects/{id}/pipeline/import` | router.go:527 | auth + CSRF |
| GET | `/api/projects/{id}/pipeline/settings` | router.go:532 | auth |
| PUT | `/api/projects/{id}/pipeline/settings` | router.go:533 | auth + CSRF |
| GET | `/api/projects/{id}/pipeline/validation` | router.go:539 | auth |
| POST | `/api/projects/{id}/pipeline/ai-generate` | router.go:640 | auth + CSRF |
| POST | `/api/projects/{id}/pipeline/ai-apply` | router.go:641 | auth + CSRF |
| POST | `/api/projects/{id}/pipeline/analyze-risks` | router.go:648 | auth + CSRF |
| POST | `/api/projects/{id}/pipeline/apply-template` | router.go:883 | auth + CSRF |

### 1.5 其他重要路由

- **Auth**： `/api/auth/{login,session,logout}` (router.go:421-428)
- **触发 trigger + cron + 串 chain + 并发 concurrency + 参数 parameters + runner**：
  - `GET/PUT /api/projects/{id}/trigger` (router.go:482-483)
  - `POST /api/projects/{id}/trigger/secret/reset` (router.go:484)
  - `GET/PUT /api/projects/{id}/cron` (router.go:487-488)
  - `GET/PUT /api/projects/{id}/concurrency` (router.go:491-492)
  - `GET/PUT /api/projects/{id}/parameters` (router.go:495-496)
  - `GET/PUT /api/projects/{id}/chain` (router.go:500-501)
  - `GET/PUT /api/projects/{id}/runner` (router.go:677-678)
- **运行 runs + 部署 + 审批 + AI 诊断 + 差异**： router.go:545-605
- **版本**：`GET /version` (router.go:416)、`GET /api/version/check` (router.go:454)、`POST /api/version/update` (router.go:456)
- **账户 account**： `/api/account/{password,sessions,...}` (router.go:611-613)
- **AI 设置 settings/ai**： router.go:618-620
- **诊断统计 settings/diagnosis-stats**： router.go:624
- **AI 命令 /ai/{command,explain,complete,compose}**： router.go:653-656
- **源码 source + pac + refs**： router.go:662-676
- **目标服务器目标 server + 容器 + 网络 + volume + image + stack + 服务操作 + 终端**： router.go:685-763
- **通知 notification channels/routes/templates/config**： router.go:769-840
- **异常 anomaly + 指标 metrics/history/dora**： router.go:849-873
- **OAuth apps/authorize/callback**： router.go:865-868
- **模板 templates + 变量组 variable-groups + 自定义节点 custom-nodes**： router.go:879-903
- **保留 retention**： router.go:773-774
- **代理 proxy + DNS + 预览 preview-envs**： router.go:782-819

---

## 2. 中间件

### 2.1 中间件函数清单

| 名字 | 文件:行号 | 作用 |
|---|---|---|
| `middleware.Recoverer` | router.go:409 | chi 内置 panic 恢复（全局） |
| `localeMiddleware` | locale.go:46-51 | 解析 `X-Pipewright-Locale` / `Accept-Language`，包装 `ResponseWriter` |
| `requireAuth(svc, next)` | router.go:917-941 | 校验 `pipewright_session` cookie，注入 `Session` 到 ctx；未登录 → 401 |
| `requireCSRF(next)` | router.go:957-977 | 写方法（非 GET/HEAD/OPTIONS）校验 `X-CSRF-Token` header == `session.CSRFToken`；失败 → 403 `csrf_invalid` |

### 2.2 "需要登录"的 token 校验中间件

`requireAuth`（router.go:917-941）。完整摘录：

```go
// router.go:917-941
func requireAuth(svc auth.Authenticator, next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if svc == nil {
            writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
            return
        }
        cookie, err := r.Cookie(cookieSession)
        if err != nil {
            writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
            return
        }
        sess, err := svc.Verify(cookie.Value)
        if err != nil {
            if errors.Is(err, auth.ErrSessionNotFound) {
                writeError(w, http.StatusUnauthorized, "unauthorized", "会话已过期,请重新登录")
                return
            }
            writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
            return
        }
        ctx := context.WithValue(r.Context(), contextKeySession, sess)
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}
```

会话由 `auth.Service` (internal/auth/service.go) 通过会话 cookie 持有；`Session` 结构 (internal/auth/session.go:27-33) 含 `Token/CSRFToken/CreatedAt/ExpiresAt/LastSeenAt`，**没有 role/admin 字段**。

### 2.3 RequireAdmin 类似中间件

**未找到**。

- `grep -rni "RequireAdmin|requireAdmin|RequireRole|IsAdmin|isAdmin|adminAuth" internal/ --include="*.go"` 返回空。
- 平台为「单管理员」架构：`auditActor = "admin"`（audit.go:17，注释明确「本平台为单管理员账户」）；所有登录用户即 admin。
- `auth.Session` 无 role/admin 字段（internal/auth/session.go:27-33）；`auth.Authenticator` 接口只有 `Login/Verify/Logout/AdminUsername` 四个方法（service.go:13-22）。
- admin_user 表只有 1 行（service.go:113 中 `WHERE id = 1`），bootstrap 时只有 admin 账户（service.go:74-106）。

如果 v6 设计文档要新增多用户/角色，应在此层加入 `RequireRole("admin")` 或 `RequireAdmin` 中间件 + 在 `Session` 上扩展字段 + 新增 user 表。

### 2.4 是否已有 RateLimit 中间件

**未找到**（HTTP API 层）。

`grep -rni "ratelimit|rate-limit|rate_limit|RateLimit" --include="*.go"` 仅在：
- `internal/version/update_test.go`（更新检测测试，与 HTTP 中间件无关）
- `internal/proxy/caddy.go`（Caddy 配置里的 `ratelimit` 注释，反代层特性）

认证层有锁定（auth.LockoutManager 限制登录失败次数），但 HTTP 中间件链没有 RateLimit。

### 2.5 chi Logger 中间件

**未使用**。`grep "middleware\." internal/httpapi/ --include="*.go"` 仅 1 行：`router.go:409: r.Use(middleware.Recoverer)`。

---

## 3. HTTP handler 实现

### 3.1 handler 文件划分

`internal/httpapi/` 下有 **60 个 handler 实现文件**（不含 `_test.go`）。每个文件按业务主题切分，例如：

- `account.go` — 账户设置
- `audit.go` — 审计查询 + `auditActor` + `clientIP` + `recordAudit`
- `ai_command.go` / `ai_generate.go` / `ai_risk.go` / `ai_settings.go` — AI 端点
- `approval_link.go` — 公开审批链接
- `approvals.go` — 审批决策
- `chain.go` — 流水线串联
- `concurrency.go` — 并发限制
- `container_create.go` / `container_diagnose.go` / `container_inspect.go` / `container_terminal.go` — 容器操作
- `credentials.go` — 凭据 vault
- `cron.go` — cron 触发
- `custom_nodes.go` — 自定义节点
- `deploy.go` — 部署
- `diagnosis_feedback.go` — 诊断反馈
- `dnsprovider.go` — DNS 提供商
- `dora.go` — DORA 指标
- `environments.go` — GitLab 一等公民环境
- `locale.go` — locale 中间件 + writer 包装
- `metrics.go` — 指标
- `notifications.go` / `notify_config.go` / `notify_hook.go` — 通知
- `oauth.go` — OAuth
- `pac_preview.go` — pac 预览
- `parameters.go` — 运行参数
- `pipeline_settings.go` / `pipeline_validation.go` / `pipelines.go` — 流水线
- `previewenv.go` / `previewenv_hook.go` — 预览环境
- `projects.go` — 项目
- `promotion.go` — 晋级
- `proxy.go` — 反代
- `prstatus.go` — PR 状态回写
- `refs.go` — refs
- `retention_config.go` — 保留策略
- `router.go` — 路由 + 中间件 + auth handler + JSON helpers
- `run_artifacts.go` / `run_diagnose.go` / `run_diff.go` / `run_secrets.go` / `run_test_report.go` / `runs.go` — 运行
- `server_container_stats.go` / `server_containers.go` / `server_images.go` / `server_logs.go` / `server_metrics.go` / `server_ops.go` / `server_prune.go` / `server_stacks.go` / `server_volnet.go` / `servers.go` — 服务器/容器/网络/卷
- `source.go` — 源码读取
- `templates.go` — 模板
- `triggers.go` — 触发
- `variable_groups.go` — 变量组
- `version.go` — 版本
- `webhooks.go` — webhook

按主题各文件成对或四五个一组清晰切分；**无 "pre handler" 这一独立文件**（搜索 `pre_handler/preHandler/PreHandler` 全部为空）。

### 3.2 `/api/pipelines/:id/yaml` 与 `PATCH /api/pipelines/:id/yaml` 端点

**未找到**。

- `grep "pipeline/.*yaml\|/yaml\b\|/pipeline/.*[Yy][Aa][Mm][Ll]"` 全部为空。
- 现有流水线配置入口：
  - `GET /api/projects/{id}/pipeline` 读完整 Config（含 `yaml` 字段，DTO 见 pipelines.go:18）
  - `PUT /api/projects/{id}/pipeline` 以 `{stages:[...]}` 形式覆盖完整 spec（pipelines.go:204-227，body 限 256KB）
  - `POST /api/projects/{id}/pipeline/import` 收 `{yaml:"...", save?:bool}` 把 .pipewright.yml 解析+校验（pipelines.go:237-274）
- 流水线没有独立 id，只挂在 `project.id` 下；故 `/api/pipelines/:id/yaml` 这类 REST 风格端点**结构上不存在**。

### 3.3 错误响应格式（冻结）

```go
// internal/httpapi/router.go:1174-1188
type errBody struct {
    Error errDetail `json:"error"`
}

type errDetail struct {
    Code    string `json:"code"`
    Message string `json:"message"`
}

// writeError 输出统一错误响应:{ "error": { "code": "...", "message": "..." } }
// msg 以 zh-CN 源串写在调用点;此处按请求 locale(localeMiddleware 解析)本地化。
func writeError(w http.ResponseWriter, code int, errCode, msg string) {
    msg = i18n.T(localeOf(w), msg)
    writeJSON(w, code, errBody{Error: errDetail{Code: errCode, Message: msg}})
}
```

JSON 结构（camelCase、嵌套 error）：
```json
{"error":{"code":"invalid_yaml","message":"..."}}
```

常见 HTTP 状态码（写错误码时使用）：
| 状态码 | errCode 例子 | 出现位置 |
|---|---|---|
| 400 Bad Request | `bad_request` / `invalid_credential` / `invalid_stack` / `invalid_service_target` | credentials.go:55, pipelines.go:217, server_stacks.go:756 等 |
| 401 Unauthorized | `unauthorized` / `invalid_credentials` / `locked_out` | router.go:920, 1006-1011 |
| 403 Forbidden | `csrf_invalid` | router.go:967-973 |
| 404 Not Found | `credential_not_found` / `project_not_found` | credentials.go:51, pipelines.go:171 |
| 409 Conflict | `credential_in_use` | credentials.go:53 |
| 422 Unprocessable Entity | `invalid_yaml` / `invalid_stage` / `invalid_job` / `duplicate_id` / `vault_unconfigured` / `repo_unreachable` | pipelines.go:287, projects.go:54-59 |
| 429 Too Many Requests | `locked_out` | router.go:1006 |
| 500 Internal Server Error | `internal` | router.go:935 |
| 503 Service Unavailable | `vault_unconfigured` / `audit_unavailable` / `internal`（服务未初始化） | credentials.go:49, audit.go:104 |

`msg` 中文作为 i18n key，由 `internal/i18n.T(locale, msg)` 实时翻译；未在 i18n 表里的原文原样返回。

---

## 4. 数据上传

### 4.1 multipart / FormFile / 文件上传

**未找到任何 multipart 上传实现**。

- `grep "ParseMultipartForm\|FormFile\|multipart" --include="*.go"` → 全部为空。
- `grep "mime/multipart" --include="*.go"` → 全部为空。
- 全平台**没有** `r.ParseMultipartForm(...)` 或 `r.FormFile(...)` 调用。

现有所有「文件/字节流」传输都是 **JSON body**：
- 凭证创建 / 更新走 JSON，`r.Body = http.MaxBytesReader(w, r.Body, 1<<20)`（credentials.go:94、138）
- 流水线 spec / settings / cron / chain / concurrency / parameters / promotion / deployments 都走 JSON body
- Stack 部署的 compose YAML **作为 JSON string 字段**传：`type stackDeployRequest struct { Name string; Compose string }` (server_stacks.go:735-738)；server_stacks.go:800 `svc.Upload(cctx, id, strings.NewReader(compose), composePath)` → 字节流经 SSH stdin 上传到 `/opt/pipewright/stacks/<name>/docker-compose.yml`（server_stacks.go:30-32，`stacksBaseDir = "/opt/pipewright/stacks"`，composeMaxBytes = 512KiB）
- Caddyfile 同样：proxy.go 通过 JSON body 收 `content` 字符串，proxy/caddy.go:540 用 `tg.Upload` 流式写到目标机 `/opt/pipewright/caddy/Caddyfile`

如果 v6 要支持「上传 .pipewright.yml 文件」「上传 Kubernetes yaml」「上传 Caddyfile 二进制文件」等，需要新增 multipart 中间件 + 落盘。

### 4.2 当前上传大小限制

**完全在 handler 内的 `http.MaxBytesReader`，没有全局 Server.MaxBytesReader / 没有 chi 中间件层限制**。

`grep "MaxBytesReader" --include="*.go"`（部分）：
```go
internal/httpapi/pipelines.go:211:   r.Body = http.MaxBytesReader(w, r.Body, 1<<18) // 256KB
internal/httpapi/pipelines.go:244:   r.Body = http.MaxBytesReader(w, r.Body, 1<<18) // 256KB
internal/httpapi/credentials.go:94:  r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 私钥可能较大,放宽到 1MB
internal/httpapi/credentials.go:138: r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
internal/httpapi/deploy.go:113:      r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
internal/httpapi/server_stacks.go:749:r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
internal/httpapi/approval_link.go:155:r.Body = http.MaxBytesReader(w, r.Body, 1<<13)
internal/httpapi/promotion.go:128:   r.Body = http.MaxBytesReader(w, r.Body, 1<<18)
internal/httpapi/promotion.go:175:   r.Body = http.MaxBytesReader(w, r.Body, 1<<13)
internal/httpapi/webhooks.go:71:     r.Body = http.MaxBytesReader(w, r.Body, webhookMaxBody)
internal/httpapi/webhooks.go:117:    r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
internal/httpapi/router.go:990:       r.Body = http.MaxBytesReader(w, r.Body, 1<<16)  // 登录 body 限 64KB
```

常规模式：**每个 handler 入口处自行 wrap**。常用档位：
- `1<<13` = 8KB（approval, promote 决策）
- `1<<14` = 16KB（cron, preview-config, proxy enabled/refresh）
- `1<<15` = 32KB（chain）
- `1<<16` = 64KB（deploy, projects, login, diagnosis feedback, notifications config, oauth, server prune）
- `1<<18` = 256KB（pipelines save/import, promotion save）
- `1<<20` = 1MB（credentials create/update, stack deploy）

`http.Server`（cmd/pipewright/main.go:567-576）**没有 `MaxHeaderBytes` 限制**：
```go
srv := &http.Server{
    Addr:              cfg.Addr,
    Handler:           ...,
    ReadHeaderTimeout: 10 * time.Second,
    ReadTimeout:       30 * time.Second,
    WriteTimeout:      0,    // SSE 不被切断
    IdleTimeout:       120 * time.Second,
}
```
注意 `WriteTimeout=0`（SSE 长连接不可被写超时切断）；`ReadTimeout=30s` 是请求全 body 上限；如果 v6 要支持大文件上传，需要在 router 层或单独路由组上调 ReadTimeout/ReadHeaderTimeout 并允许更大的 body。

### 4.3 上传文件落盘路径

**当前没有用户上传文件到中控机本地落盘的实现**。所有「上传」都是 JSON body 然后流到目标主机：

- **制品**（构建产物归档）：`internal/artifactstore/store.go`，默认 `filepath.Join(filepath.Dir(cfg.DBPath), "artifacts")`（main.go:267），可由 `PIPEWRIGHT_ARTIFACT_DIR` 覆盖（main.go:265）
- **代码缓存**： `filepath.Join(filepath.Dir(cfg.DBPath), "repos")`，env `PIPEWRIGHT_REPO_CACHE_DIR`（main.go:285, 283）
- **构建缓存**： `filepath.Join(filepath.Dir(cfg.DBPath), "cache")`，env `PIPEWRIGHT_CACHE_DIR`（main.go:303, 301）
- **SQLite DB**： `cfg.DBPath` 默认 `"pipewright.db"`（config.go:42），env `PIPEWRIGHT_DB`
- **compose / Caddyfile**：不落中控盘，直接经 `target.Upload` 走 SSH 上传到目标机的 `/opt/pipewright/stacks/<name>/docker-compose.yml` 或 `/opt/pipewright/caddy/Caddyfile`（server_stacks.go:30, proxy/caddy.go）
- **大二进制 binary 自更新**（PIPEWRIGHT_RUNTIME=docker 升级）：写到 `update.go`/`selfupdate.go` 的临时文件，落临时目录，无持久化（version/selfupdate.go:196 用 `io.LimitReader(tr, 200<<20)`）

如果 v6 要做「上传文件到中控」需要新增落盘路径和清理策略。

---

## 5. 环境变量装配

### 5.1 配置来源

**没有使用 viper 或 flag 绑定**，全部 `os.Getenv("PIPEWRIGHT_...")` 或 `internal/config/config.go:37-46` 集中装载。`grep "viper|flag\.String|GetString|BindEnv" --include="*.go"` → 全部为空。

### 5.2 全部 PIPEWRIGHT_* env 列表（去重 + 默认值）

| env var | 读取位置 | 类型/默认值 | 作用 |
|---|---|---|---|
| `PIPEWRIGHT_ADDR` | config.go:39 | string，默认 `:8080` | HTTP 监听地址 |
| `PIPEWRIGHT_ADMIN_PASSWORD` | config.go:44 | string，**无默认（空）** | 首次启动 admin 口令 |
| `PIPEWRIGHT_ADMIN_USERNAME` | config.go:43 | string，默认 `"admin"` | 首次启动 admin 用户名 |
| `PIPEWRIGHT_DB` | config.go:42 | string，默认 `"pipewright.db"` | SQLite DB 文件路径 |
| `PIPEWRIGHT_DB_DRIVER` | config.go:40 | string，默认 `"sqlite"`；另一值 `"mysql"` | 数据库 driver |
| `PIPEWRIGHT_DB_DSN` | config.go:41 | string，无默认 | mysql DSN 或覆盖 sqlite DSN |
| `PIPEWRIGHT_MASTER_KEY` | config.go:81 | string（base64 32B） | vault master key |
| `PIPEWRIGHT_MASTER_KEY_FILE` | config.go:83 | string path | vault master key 文件路径（替代直传） |
| `PIPEWRIGHT_ARTIFACT_DIR` | main.go:265 | string path | 制品库目录（默认 `DB同级/artifacts`） |
| `PIPEWRIGHT_REPO_CACHE_DIR` | main.go:283 | string path | 仓库镜像缓存目录（默认 `DB同级/repos`） |
| `PIPEWRIGHT_NO_REPO_CACHE` | main.go:282 | `=1` 关闭；其他值开 | 关掉仓库缓存 |
| `PIPEWRIGHT_CACHE_DIR` | main.go:301 | string path | 构建依赖缓存目录（默认 `DB同级/cache`） |
| `PIPEWRIGHT_NO_BUILD_CACHE` | main.go:300 | `=1` 关闭 | 关掉构建依赖缓存 |
| `PIPEWRIGHT_BUILDER` | main.go:743 | `real` / `stub` / 其他=auto | legacy runner 模式选择 |
| `PIPEWRIGHT_RUNNER` | main.go:336 | `legacy` 触发 legacy；其他（默认）= dag | 运行器（默认 DAG 调度） |
| `PIPEWRIGHT_NO_IMAGE_GC` | main.go:347, 749, 756 | `!=1` 即开 | 关掉构建镜像 GC |
| `PIPEWRIGHT_RUNTIME` | version/selfupdate.go:31 | `docker` 显式声明 | 自更新探测部署形态 |
| `PIPEWRIGHT_RELEASE_REPO` | version/update.go:61 | string `owner/name` | 检查更新所查 GitHub 仓库 |
| `PIPEWRIGHT_PUBLIC_URL` | main.go:345, 416 | string URL | 公网访问 URL（webhook/OAuth 回调/审批链接） |
| `PIPEWRIGHT_AUDIT_SINK` | audit/sink.go:22, 52 | string spec | 远端审计 sink 配置 |
| `PIPEWRIGHT_CADDY_IMAGE` | proxy/caddy.go:27, 588 | string image ref | 反代镜像覆盖 |
| `PIPEWRIGHT_PAC_RUNTIME` | main.go:363 | `=1` 全局强开 | .pipewright.yml 全局覆盖开关 |
| `PIPEWRIGHT_PR_STATUS` | main.go:411 | `=1` 全局强开 | PR 状态回写全局强开 |
| `PIPEWRIGHT_PR_STATUS_GITHUB_BASE` | main.go:413, 512 | string URL | GitHub API base（企业自托管） |
| `PIPEWRIGHT_PR_STATUS_GITEE_BASE` | main.go:414, 513 | string URL | Gitee API base（企业自托管） |
| `PIPEWRIGHT_CHAIN_MAX_DEPTH` | main.go:431 | 正整数，默认 5 | 流水线串联深度上限 |
| `PIPEWRIGHT_MAX_CONCURRENT` | main.go:458 | 正整数，默认 0（=worker 数） | 全局同时 running 上限 |
| `PIPEWRIGHT_PREVIEW_SWEEP_INTERVAL` | main.go:501 | Go duration，默认 `5m` | 预览环境自动回收间隔 |
| `PIPEWRIGHT_ANOMALY_COOLDOWN` | main.go:539 | 整数秒，默认 600 | 异常检测告警去重窗口 |
| `PIPEWRIGHT_ANOMALY_INTERVAL` | main.go:542 | 整数秒，默认 60；0 关闭 | 异常检测定时器间隔 |
| `PIPEWRIGHT_METRICS_SAMPLE_INTERVAL` | main.go:552 | 整数秒，默认 60；0 关闭 | 服务器指标采样间隔 |
| `PIPEWRIGHT_METRICS_RETENTION_DAYS` | main.go:554 | 正整数，默认 7 | 指标历史保留天数 |
| `PIPEWRIGHT_TRUST_PROXY` | httpapi/audit.go:43 | `1/true/yes/on` 信任 XFF | 审计 IP 取信 X-Forwarded-For |
| `PIPEWRIGHT_TEST_MYSQL_DSN` | storetest/storetest.go:26, 31, 98 | string DSN | 测试用 MySQL DSN |

测试内部标记（**非真实 env**）：`PIPEWRIGHT_DAG_OK`（dag_stage_exec_test.go:448 仅作内容标记）、`PIPEWRIGHT_E`、`PIPEWRIGHT_DAG_OK`（误报）。

构建期注入到 job 容器的**容器内环境变量**（非 OS env）：`PIPEWRIGHT_ENV`（internal/build/stage_env.go:25-31），job 容器向该路径写 `KEY=VALUE`，执行器读回注入后续 job。

`.env.example`（`.env.example:1-27`）仅展示：`PIPEWRIGHT_ADMIN_PASSWORD`、`PIPEWRIGHT_MASTER_KEY`、`PIPEWRIGHT_PORT`（仅 docker-compose，**程序未读取 PIPEWRIGHT_PORT**）、`PIPEWRIGHT_VERSION`（docker-compose 镜像版本，非程序 env）、`PIPEWRIGHT_PUBLIC_URL`、`PIPEWRIGHT_DB_DRIVER`、`PIPEWRIGHT_DB_DSN`、`MYSQL_PASSWORD`、`MYSQL_ROOT_PASSWORD`。

### 5.3 题目点名的三个 env

| env var | 状态 |
|---|---|
| `PIPEWRIGHT_DATA_DIR` | **未找到**。`grep "PIPEWRIGHT_DATA_DIR\|DATA_DIR"` 全部为空。**目录推断**逻辑：`filepath.Dir(cfg.DBPath)` 即 DB 文件所在目录（main.go:267, 285, 303），默认无独立 data dir。 |
| `PIPEWRIGHT_AUTO_CHECK_ON_START` | **未找到**。`grep "PIPEWRIGHT_AUTO_CHECK\|AUTO_CHECK_ON_START\|CHECK_TIMEOUT"` 全部为空。**启动期异常检测**目前是无 env 控制（`PIPEWRIGHT_ANOMALY_INTERVAL=0` 才能关掉定时器，main.go:585）；启动期一次性检查没有专门的 env。 |
| `PIPEWRIGHT_CHECK_TIMEOUT_SECONDS` | **未找到**。无任何「check timeout」相关 env。 |

### 5.4 装配方式

- `internal/config/config.go:37-46` 集中读取核心配置：
  ```go
  func Load() Config {
      return Config{
          Addr:          getenv("PIPEWRIGHT_ADDR", ":8080"),
          DBDriver:      getenv("PIPEWRIGHT_DB_DRIVER", "sqlite"),
          DBDSN:         os.Getenv("PIPEWRIGHT_DB_DSN"),
          DBPath:        getenv("PIPEWRIGHT_DB", "pipewright.db"),
          AdminUsername: getenv("PIPEWRIGHT_ADMIN_USERNAME", "admin"),
          AdminPassword: os.Getenv("PIPEWRIGHT_ADMIN_PASSWORD"),
      }
  }
  ```
- `config.go:80-111` 读 master key（base64 解码 + 32B 长度校验 + 用后清零）。
- 其它 env 全部在 `cmd/pipewright/main.go` 内联读取（main.go:265-555），未抽到 config 包。
- `cmd/pipewright/main.go:661-675` 提供通用 `envDurationSeconds(name, def)`，所有「整数秒」env 都过它（含 0=关闭语义）。

---

## 结论摘要

1. **路由结构**：单点注册（`httpapi.New`），`/api` 组统一过 `requireAuth + requireCSRF`；公开入口仅 `/api/auth/*`、`/api/webhooks/{token}`、`/approvals*`；按业务域分组清晰。
2. **中间件**：仅 4 个（Recoverer、localeMiddleware、requireAuth、requireCSRF），**没有 RequireAdmin、没有 RateLimit、没有 chi Logger**；Session 模型无 role 字段。
3. **HTTP handler**：60+ 文件按域切分；**无 `pre_handler.go` 文件**；`/api/pipelines/:id/yaml` 端点**不存在**（流水线挂在 `project.id` 下，PUT 收 stages、POST `/pipeline/import` 收 YAML）；错误响应固定为 `{"error":{"code","message"}}`，写带中文经 i18n 按 locale 翻译。
4. **数据上传**：**完全没有 multipart/FormFile 实现**；所有「上传」都是 JSON body；body 限在各 handler 内的 `MaxBytesReader`，常用 64KB/256KB/1MB 三档；http.Server 层无 MaxBytesReader/MaxHeaderBytes 设置；文件不落中控盘（仅制品/缓存/repo 镜像落 DB 同级子目录，compose/Caddyfile 走 SSH 上传目标机）。
5. **环境变量**：**未使用 viper/flag**；全部 `os.Getenv("PIPEWRIGHT_...")`，核心 env 在 `internal/config/config.go`，其余分散在 `cmd/pipewright/main.go`；**`PIPEWRIGHT_DATA_DIR` / `PIPEWRIGHT_AUTO_CHECK_ON_START` / `PIPEWRIGHT_CHECK_TIMEOUT_SECONDS` 均不存在**（需要的话应在 config 包或 main.go 新增）。

---

## 待补 / 重点提示

- 如果 v6 文档要求多用户/角色：需要在 `internal/auth` 加 `user` 表 + `Session.Role` 字段 + 新增 `RequireAdmin` 中间件（参考 router.go:917 的 `requireAuth` 模板）。
- 如果 v6 文档要求文件上传（multipart）：需要在 router 加一层 multipart 中间件 + 新增 `multipartMaxBytes` env；在 http.Server 上调 ReadTimeout/MaxHeaderBytes；并明确落盘路径（目前所有文件都不落中控）。
- 如果 v6 文档要求 RateLimit：需要在 router 加一层 chi 中间件（令牌桶或滑动窗口），建议在 `/api/auth/login`、手动触发、Anomaly check 等写方法上做。
- 三个缺失 env（PIPEWRIGHT_DATA_DIR / AUTO_CHECK_ON_START / CHECK_TIMEOUT_SECONDS）：按需在 `internal/config/config.go` 或 `cmd/pipewright/main.go` 新增，并在 `.env.example` 加文档。