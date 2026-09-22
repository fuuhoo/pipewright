# Pipewright 构建环境预置与管理 —— 完整设计文档

> 版本：v6（整合用户模型 γ2、管理员 personal 凭据、固定路径 config_profiles、本机构建边界、最终 DDL）
> 适用范围：Pipewright 平台改造，实现构建环境统一预置、镜像检查、配置资源管理、凭据分级、YAML 仅禁用编辑入口。
> 边界声明：本计划**只输出最终 DDL 与实现设计**，不输出迁移脚本；升级路径由迁移团队另行处理，不在本计划范围。

---

## 一、概述

将 Pipewright 的构建环境从"每个项目自由指定任意镜像"改造为"管理员统一预置，普通用户只能从预置目录中选择"的模式。同时引入镜像检查、手动拉取、配置资源独立管理、凭据分级（全局/个人）、设置模块权限隔离，并禁用 YAML **直接编辑**（保留导入导出）。

核心目标：

- 管理员统一管控构建环境与配置资源。
- 普通用户仅能从预置列表中选择，不感知底层镜像细节。
- 镜像拉取完全由系统决定，不拼接任何地址。
- 支持官方与自定义镜像来源，自定义时可关联凭据。
- 服务启动后自动检查镜像，不可用时强制禁用。
- 配置资源独立管理，文件保存到宿主固定目录。
- 凭据支持全局与个人两级，管理员也能创建 personal 凭据（个人身份），个人凭据仅本人可见。
- 保留 YAML 导入/导出，但禁止直接编辑 YAML。

---

## 二、需求总结

| 编号 | 需求 | 说明 |
|---|---|---|
| R1 | 仅管理员可创建预置环境 | 普通用户无创建、编辑、删除权限 |
| R2 | 预置环境支持自定义 Docker 镜像地址 | 管理员可填写官方短名或完整地址 |
| R3 | 预置环境支持自定义环境说明和版本 | 描述、语言、版本号均需填写 |
| R4 | 用户只能从预置目录中选择构建环境 | 不可自由输入镜像地址 |
| R5 | 支持按语言分组、按版本下拉选择 | 如选择 Node → 下拉选 22 或 24 |
| R6 | Maven 配置作为一种可预置的配置资源 | 独立页面管理，支持上传文件 |
| R7 | 其他语言的配置同理 | 独立页面管理，支持上传文件 |
| R8 | 镜像拉取完全由系统决定，不拼接任何地址 | 用户填什么地址，系统就用什么地址 |
| R9 | 支持私有仓库 / 阿里云拉取凭证 | 凭据独立管理，构建环境可关联 |
| R10 | 默认官方，选择"其他"时填入镜像地址 + 可选凭据 | 不再有独立仓库概念 |
| R11 | 启动后自动检查镜像；镜像不存在时显示"不存在"且不可启用 | 强制不可用 |
| R12 | 支持手动检查 + 手动拉取 | 修改地址后可手动拉取验证 |
| R13 | 配置资源独立页面，支持上传文件 | 文件保存到宿主固定目录，运行时按配置拷贝到容器 |
| R14 | 禁用 YAML **直接编辑**，保留 YAML 导入/导出 | 前端隐藏 YAML 编辑入口；后端不提供"改 YAML 保存"端点 |
| R15 | 支持管理员与普通用户多角色 | 管理员在 regular_users 里也有一行（role='admin'），凭据 owner_id 统一指向 regular_users.id |
| R16 | 管理员也能创建 personal 凭据 | 语义是"以个人身份创建"，与 global 凭据视觉区分 |
| R17 | 镜像检查每次结果覆盖前次 | 不存历史快照；审计由 internal/audit 触发 |

---

## 三、核心设计决策

### 3.1 镜像来源

只保留两种镜像来源：

| 来源 | 说明 | 凭据 |
|---|---|---|
| `official` | Docker Hub 官方镜像 | 无需凭据 |
| `custom` | 任意自定义地址，需填入完整镜像地址（必填） | 可选关联凭据 |

系统**不做任何地址拼接**，填什么拉什么。

### 3.2 镜像检查与状态

| 状态 | 含义 | 是否可启用 |
|---|---|---|
| `unchecked` | 未检查（新创建或地址刚修改） | 可启用（有提示） |
| `checking` | 检查中 | 不可启用 |
| `available` | 镜像存在 | 可启用 |
| `unavailable` | 镜像不存在或检查失败 | **不可启用** |

**自动检查**：服务启动后，后台任务遍历所有构建环境，并发执行 `docker manifest inspect`（默认 10 并发，每任务 60s 超时）。

**手动检查**：调用 `docker manifest inspect`，快速判断远程镜像是否存在（60s 超时）。

**手动拉取**：调用 `docker pull`，实际下载镜像（240s 超时 = 60s × 4），用于修改地址后验证。Pull 前按需 login，pull 完立即 logout。

**结果覆盖**：每次检查结果直接 `UPDATE` 当前行（`image_check_status` / `image_check_error` / `image_checked_at` 三列覆盖），不做历史隔离；历史快照由 `internal/audit` 触发（append-only）。

### 3.3 配置资源

- 文件落到宿主固定目录（`${PIPEWRIGHT_DATA_DIR}/config_profiles/<id>/<filename>`），不再校验白名单路径。
- 数据库**双存储**：`file_path`（宿主机路径）+ `content`（文件内容文本），便于审计与导入导出。
- **权威路径是磁盘文件**：`file_path` 指向的文件为运行时唯一真相；`content` 仅作冗余快照（用于：审计、导入/导出、文件丢失时的回填）。**`is_builtin=1` 的内置配置资源不允许通过 web API 修改 `target_path` / `content` / `file_path` 任一字段**（前端隐藏编辑入口，后端 service 在保存前对 `is_builtin` 行做字段白名单，仅允许改 `description` 与 `enabled`）。
- 写路径原子性：Save 时先写临时文件 `<filename>.tmp.<pid>` → `fsync` → `os.Rename` 到 `<filename>`，再在同一 SQL 事务里 `UPDATE config_profiles SET file_path=?, content=?, updated_at=? WHERE id=?`。**任何一步失败 → 整笔回滚**（删除临时文件 + DB 事务 rollback）。
- 读路径优先级：`runner.ConfigInjector` 优先 `os.ReadFile(file_path)`，若文件不存在才回退 `DB.content` 并记录一条 warning（用于文件被误删场景，不阻断构建但留下可审计信号）。
- `target_path` 字段记录"容器内目标路径"（如 `/root/.m2/settings.xml`），构建时由 `runner.ConfigInjector` 拷贝到容器。
- 独立管理页面，支持在线编辑与文件上传。
- 内置配置资源仅可查看，不可编辑删除。
- 自定义配置资源可编辑、删除、上传文件。
- 按语言与配置类型分组管理。

### 3.4 凭据分级

| scope | 创建者 | owner_id | 可见范围 | 典型用途 |
|---|---|---|---|---|
| `global` | 仅管理员 | 空字符串 | 所有用户（在流水线中可选择） | 系统级通知 Token、共享部署密钥、镜像仓库凭证 |
| `personal` | 任意登录用户（含管理员） | `regular_users.id` | **仅创建者本人** | 个人 Git Token、个人 SSH 密钥、个人 API Key |

- 凭据 `owner_id` **统一指向 `regular_users.id`**（管理员创建 personal 凭据时，owner_id 指向管理员在 regular_users 里的那一行）。
- 管理员可查看个人凭据的**元数据**（名称、创建者、创建时间、最后使用时间），但**无法查看明文**，也无法在流水线中引用他人的个人凭据。
- 管理员可禁用（`enabled=0` + `disabled_by` + `disabled_at`）违规的个人凭据，但不可删除。
- 字段语义沿用现有 vault 一切约束（NaCl secretbox 加密、`masked_value`、`last_used_at`、`username`）。

### 3.5 用户模型（γ2 方案）

```
users (概念层)
├── admin_user (系统内置超级管理员,id=1,Bootstrap 锚点)
│       └── 同步在 regular_users 中存在 role='admin' 的一行
│
└── regular_users (普通用户 + 管理员个人行)
        ├── role='admin' 的那一行(与 admin_user 同步)
        └── role='user' 的若干行
```

- `admin_user` 保留为 `Bootstrap` 锚点（首次启动口令创建逻辑不变），不动 `id=1` 的 CHECK 约束。
- `regular_users` 表里**管理员也有一行**（`role='admin'`），与 `admin_user.id=1` 业务语义上同一实体。
- 个人凭据 `owner_id` 统一指向 `regular_users.id`，避免"owner_id 到底是 admin_user.id=1 还是 regular_users.id"的歧义。
- 管理员创建 personal 凭据时，前端 UI 文案是"我的凭据"，与"全局凭据"做视觉区分。

### 3.6 设置模块权限隔离

```
⚙️ 设置
├── 🔧 全局设置（仅管理员可见）
│   ├── 构建环境管理
│   ├── 配置资源管理
│   ├── 全局凭据管理
│   ├── 用户管理
│   ├── 系统参数
│   ├── 凭据保险库
│   └── 审计日志
│
└── 👤 个人设置（所有登录用户可见）
    ├── 个人资料
    ├── 我的凭据
    ├── 通知偏好
    ├── 登录安全
    └── 界面偏好
```

### 3.7 YAML 处理

- **保留**：`.pipewright.yml` 导入预览（`POST /api/pipelines/import`），仓库 YAML pacloader（`GET /api/projects/:id/pac`），导出（`GET /api/pipelines/:id/export`）。
- **删除**：前端画布**编辑界面**的 YAML 直接编辑入口（`PipelineCanvas.vue` 的 YAML 折叠区整段移除）。
- **删除**：后端任何"PATCH /api/pipelines/:id/yaml"或"PUT /api/pipelines/:id/yaml"端点（若存在）。
- **不阻断**：导入/导出/pacloader 通道。

### 3.8 构建边界

- 构建**始终在 pipewright 进程所在的宿主机执行**，不区分"二进制部署"还是"Docker 部署"。
- 镜像拉取、检查、推送都在宿主机执行。
- `internal/build/remote.go` 的远程 SSH 通道与 `ProjectRunner` 表保留，**远程构建机的镜像可用性超出本计划范围**，留作后续 story。

---

## 四、数据模型（最终 DDL）

> 本节输出**最终状态表结构**，不输出迁移脚本。`admin_user` 沿用现有结构，其他 4 张表为本计划最终形态。

### 4.1 `admin_user`（沿用，不动）

```sql
CREATE TABLE IF NOT EXISTS admin_user (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    username      TEXT    NOT NULL,
    password_hash TEXT    NOT NULL,
    created_at    TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL
);
```

### 4.2 `regular_users`（新增）

```sql
CREATE TABLE regular_users (
    id            TEXT PRIMARY KEY,                 -- uuid v4
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,                    -- argon2id,与 admin_user 同参数
    role          TEXT NOT NULL DEFAULT 'user',     -- 'user' | 'admin'
    enabled       INTEGER NOT NULL DEFAULT 1,
    description   TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    last_login_at TEXT
);
CREATE INDEX idx_regular_users_username ON regular_users(username);
CREATE INDEX idx_regular_users_enabled  ON regular_users(enabled);
```

### 4.3 `build_envs`（新增）

```sql
CREATE TABLE build_envs (
    id                 TEXT PRIMARY KEY,
    language           TEXT NOT NULL,
    version            TEXT NOT NULL,
    display_name       TEXT NOT NULL,
    description        TEXT NOT NULL DEFAULT '',

    source_type        TEXT NOT NULL DEFAULT 'official',  -- 'official' | 'custom'
    image              TEXT NOT NULL,                     -- 原样使用,系统不处理
    credential_id      TEXT NOT NULL DEFAULT '',

    image_check_status TEXT NOT NULL DEFAULT 'unchecked', -- 覆盖式写入
    image_check_error  TEXT NOT NULL DEFAULT '',
    image_checked_at   TEXT,

    enabled            INTEGER NOT NULL DEFAULT 1,
    sort_order         INTEGER NOT NULL DEFAULT 0,
    created_by         TEXT NOT NULL,
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,

    UNIQUE(language, version)
);
CREATE INDEX idx_build_envs_language    ON build_envs(language);
CREATE INDEX idx_build_envs_enabled     ON build_envs(enabled);
CREATE INDEX idx_build_envs_check_status ON build_envs(image_check_status);
```

### 4.4 `config_profiles`（新增）

```sql
CREATE TABLE config_profiles (
    id           TEXT PRIMARY KEY,
    language     TEXT NOT NULL,
    config_type  TEXT NOT NULL,                       -- 'maven' | 'npm' | 'pip' | 'goproxy' | 'env' | ...
    name         TEXT NOT NULL,
    target_path  TEXT NOT NULL,                       -- 容器内目标路径
    file_path    TEXT NOT NULL,                       -- 宿主机 ${DATA_DIR}/config_profiles/<id>/<filename>
    content      TEXT NOT NULL,                       -- 文件内容(双存储,审计/导入导出用)
    is_default   INTEGER NOT NULL DEFAULT 0,
    is_builtin   INTEGER NOT NULL DEFAULT 0,
    description TEXT NOT NULL DEFAULT '',
    enabled      INTEGER NOT NULL DEFAULT 1,
    created_by   TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,

    UNIQUE(language, config_type, name)
);
CREATE INDEX idx_config_profiles_language ON config_profiles(language);
CREATE INDEX idx_config_profiles_default  ON config_profiles(language, is_default);
CREATE INDEX idx_config_profiles_builtin  ON config_profiles(is_builtin);
```

### 4.5 `credentials`（最终形态）

```sql
CREATE TABLE credentials (
    id              TEXT PRIMARY KEY,                  -- uuid v4
    name            TEXT NOT NULL,
    scope           TEXT NOT NULL DEFAULT '',          -- 'global' | 'personal' | ''
    owner_id        TEXT NOT NULL DEFAULT '',          -- personal 时指向 regular_users.id
    type          TEXT NOT NULL,                       -- 'git_token' | 'ssh_key' | 'registry' | 'username_password' | 'api_key'
    username        TEXT NOT NULL DEFAULT '',
    ciphertext      BLOB NOT NULL,
    masked_value    TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    enabled         INTEGER NOT NULL DEFAULT 1,        -- 软禁用开关
    disabled_by     TEXT NOT NULL DEFAULT '',          -- 禁用操作者的 regular_users.id(uuid 字符串)
    disabled_at     TEXT,
    last_used_at    TEXT,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,

    -- 全局凭据与个人凭据不能同名 (owner_id='' vs owner_id=具体 UUID)
    UNIQUE(scope, owner_id, name),
    -- 作用域/owner 必须一致:global 时 owner 必空,personal 时 owner 必填
    CHECK ((scope = 'global' AND owner_id = '') OR (scope = 'personal' AND owner_id <> ''))
);
CREATE INDEX idx_credentials_owner    ON credentials(owner_id);
CREATE INDEX idx_credentials_enabled ON credentials(enabled);
CREATE INDEX idx_credentials_scope    ON credentials(scope);
```

**约束语义说明**：

- `UNIQUE(scope, owner_id, name)` + `CHECK` 让：
  - 多个 global 凭据 name 必须唯一（不同 scope='global'、owner_id='' 仍视为同组，由 `name` 区分）；
  - 同一 owner 下 personal 凭据 name 必须唯一（不同 owner 之间互不冲突）；
  - global 与 personal 之间允许同名（因为 `scope` 列不同），消除 v5 "全局与个人不可同名"的隐性约束。
- 任何写入路径必须先于 DB 层做这两个约束的语义验证（service 层做 user-friendly 错误，DB 层做兜底）。

---

## 五、后端设计

### 5.1 领域包结构

```
internal/
├── regularuser/
│   ├── model.go              -- RegularUser 实体 + Repo 接口
│   └── service.go            -- CRUD、邀请注册、密码哈希、SyncAdminRow
├── buildenv/
│   ├── model.go              -- BuildEnv 实体 + Repo 接口
│   ├── service.go            -- CRUD、启用/禁用、校验
│   ├── checker.go            -- 镜像检查、手动拉取、自动检查(60s/10/覆盖)
│   ├── resolver.go           -- language+version → image(env_resolver)
│   └── checker_test.go
├── configprofile/
│   ├── model.go              -- ConfigProfile 实体 + Repo 接口
│   └── service.go            -- CRUD、默认配置管理、文件上传
├── credential/
│   ├── model.go              -- Credential 实体 + Repo 接口(扩展 vault)
│   └── service.go            -- CRUD、加密存储、验证、权限过滤
├── pipeline/
│   └── validate.go           -- 校验逻辑(扩展,接入 buildenv resolver)
├── auth/
│   ├── rbac.go               -- RequireAdmin / RequireUser / RequireAuth
│   └── session.go            -- Session 增加 role 字段
└── build/
    ├── script_steps.go       -- scriptStepFromJob 改用 env_resolver
    ├── dag_stage_exec.go     -- 同上
    ├── stage_post.go         -- PostStep 改用 env_resolver
    ├── services.go           -- 旁挂服务改用 build_env_id
    └── builder.go            -- toolchain 构建改用 env_resolver
```

### 5.2 API 端点

**认证与会话（扩展）**：

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/auth/login` | 扩展：返回 session.role |
| POST | `/api/auth/logout` | 不变 |

**管理员端点**：

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/admin/users/invitations` | 创建普通用户邀请（一次性 token + URL） |
| GET | `/api/admin/users` | 列出所有 regular_users |
| DELETE | `/api/admin/users/:id` | 禁用普通用户（enabled=0，不物理删除） |
| GET | `/api/admin/users/:id` | 获取单个用户信息 |
| POST | `/api/admin/build-envs` | 创建构建环境 |
| GET | `/api/admin/build-envs` | 列出全部 |
| GET | `/api/admin/build-envs/:id` | 获取单个 |
| PUT | `/api/admin/build-envs/:id` | 更新 |
| DELETE | `/api/admin/build-envs/:id` | 删除 |
| POST | `/api/admin/build-envs/:id/toggle` | 启用/禁用 |
| POST | `/api/admin/build-envs/:id/check` | 手动检查镜像 |
| POST | `/api/admin/build-envs/:id/pull` | 手动拉取镜像 |
| POST | `/api/admin/build-envs/check-all` | 一键检查全部 |
| POST | `/api/admin/config-profiles` | 创建配置资源 |
| GET | `/api/admin/config-profiles` | 列出全部 |
| GET | `/api/admin/config-profiles/:id` | 获取单个 |
| PUT | `/api/admin/config-profiles/:id` | 更新 |
| DELETE | `/api/admin/config-profiles/:id` | 删除（内置不可删） |
| POST | `/api/admin/config-profiles/upload` | 上传配置文件 |
| POST | `/api/admin/credentials` | 创建全局凭据 |
| GET | `/api/admin/credentials` | 列出全局凭据 + 个人凭据元数据 |
| PUT | `/api/admin/credentials/:id` | 更新全局凭据 |
| DELETE | `/api/admin/credentials/:id` | 删除全局凭据 |
| POST | `/api/admin/credentials/:id/disable` | 禁用个人凭据 |
| POST | `/api/admin/credentials/:id/verify` | 验证凭据 |

**普通用户端点**：

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/auth/register` | 用邀请 token 注册（公开端点） |
| GET | `/api/build-envs/languages` | 返回启用环境去重语言列表 |
| GET | `/api/build-envs?language=node` | 返回该语言下所有启用环境 |
| GET | `/api/config-profiles?language=java` | 返回该语言下所有启用配置 |
| GET | `/api/credentials` | 返回自己的 personal 凭据 |
| POST | `/api/credentials` | 创建 personal 凭据 |
| PUT | `/api/credentials/:id` | 更新自己的 personal 凭据 |
| DELETE | `/api/credentials/:id` | 删除自己的 personal 凭据 |

**YAML 端点（保留与删除）**：

| 方法 | 路径 | 状态 | 说明 |
|---|---|---|---|
| POST | `/api/pipelines/import` | ✅ 保留 | 导入 YAML → 解析 → 预览 → 应用 |
| GET | `/api/pipelines/:id/export` | ✅ 保留 | 导出当前画布为 YAML |
| POST | `/api/projects/:id/pac/preview` | ✅ 保留 | pacloader 预览 |
| GET | `/api/projects/:id/pac` | ✅ 保留 | pacloader 取仓库 YAML |
| PATCH/PUT | `/api/pipelines/:id/yaml` | ❌ 删除 | 画布内"改 YAML 保存"端点（如有则删除） |

### 5.3 镜像检查器（60s/10 并发/覆盖）

```go
// internal/buildenv/checker.go

const (
    defaultCheckTimeout     = 60 * time.Second
    defaultPullTimeoutMult  = 4
    defaultCheckConcurrency = 10
)

type Checker struct {
    repo     BuildEnvRepo
    credRepo CredentialRepo
    bin      string
    timeout  time.Duration
    sem      chan struct{}
}

func NewChecker(repo BuildEnvRepo, credRepo CredentialRepo, bin string,
    concurrency int, timeout time.Duration) *Checker {
    return &Checker{
        repo: repo, credRepo: credRepo, bin: bin,
        timeout: timeout,
        sem:     make(chan struct{}, concurrency),
    }
}

// Check 检查单个构建环境的镜像是否存在。
//   - timeout: 60s(可经 PIPEWRIGHT_CHECK_TIMEOUT_SECONDS 调整)
//   - 并发上限 10(sem buffered channel)
//   - 不调用 dockerLogin(避免污染 ~/.docker/config.json)
//   - 结果直接覆盖 build_envs 当前行,不存历史
func (c *Checker) Check(ctx context.Context, env *BuildEnv) (*CheckResult, error) {
    select {
    case c.sem <- struct{}{}:
    case <-ctx.Done():
        return nil, ctx.Err()
    }
    defer func() { <-c.sem }()

    now := time.Now().UTC().Format(time.RFC3339)
    c.repo.UpdateCheckStatus(ctx, env.ID, "checking", "", now)

    checkCtx, cancel := context.WithTimeout(ctx, c.timeout)
    defer cancel()

    cmd := exec.CommandContext(checkCtx, c.bin, "manifest", "inspect", env.Image)
    output, err := cmd.CombinedOutput()
    checkedAt := time.Now().UTC().Format(time.RFC3339)
    if err != nil {
        msg := truncate(string(output), 1024)
        c.repo.UpdateCheckStatus(ctx, env.ID, "unavailable", msg, checkedAt)
        return &CheckResult{Status: "unavailable", Error: msg}, nil
    }
    c.repo.UpdateCheckStatus(ctx, env.ID, "available", "", checkedAt)
    return &CheckResult{Status: "available"}, nil
}

// ManualPull 手动拉取镜像(60s × 4 = 240s 超时)。
//   - pull 前按需 login,pull 完立即 logout
//   - 结果直接覆盖
func (c *Checker) ManualPull(ctx context.Context, envID string) (*PullResult, error) {
    env, err := c.repo.GetByID(ctx, envID)
    if err != nil {
        return nil, err
    }
    select {
    case c.sem <- struct{}{}:
    case <-ctx.Done():
        return nil, ctx.Err()
    }
    defer func() { <-c.sem }()

    now := time.Now().UTC().Format(time.RFC3339)
    c.repo.UpdateCheckStatus(ctx, envID, "checking", "", now)

    if env.CredentialID != "" {
        cred, err := c.credRepo.GetByID(ctx, env.CredentialID)
        if err != nil {
            return c.markUnavailable(ctx, envID, fmt.Sprintf("加载凭据失败: %v", err))
        }
        if err := c.dockerLogin(ctx, env.Image, cred); err != nil {
            return c.markUnavailable(ctx, envID, fmt.Sprintf("登录失败: %v", err))
        }
        defer c.dockerLogout(ctx, env.Image) // 立即清理
    }

    pullCtx, pullCancel := context.WithTimeout(ctx, c.timeout*defaultPullTimeoutMult)
    defer pullCancel()

    cmd := exec.CommandContext(pullCtx, c.bin, "pull", env.Image)
    output, err := cmd.CombinedOutput()
    checkedAt := time.Now().UTC().Format(time.RFC3339)
    if err != nil {
        msg := truncate(string(output), 4096)
        c.repo.UpdateCheckStatus(ctx, envID, "unavailable", msg, checkedAt)
        return &PullResult{Status: "unavailable", Error: msg, Output: string(output)}, nil
    }
    c.repo.UpdateCheckStatus(ctx, envID, "available", "", checkedAt)
    return &PullResult{Status: "available", Output: string(output)}, nil
}

// StartAutoCheck 启动后遍历 build_envs,每个 env 异步检查一次(每次结果覆盖)。
func (c *Checker) StartAutoCheck(ctx context.Context) {
    go func() {
        envs, err := c.repo.List(ctx, BuildEnvFilter{IncludeDisabled: true})
        if err != nil {
            log.Errorf("加载构建环境列表失败: %v", err)
            return
        }
        for _, env := range envs {
            env := env
            go func() {
                c.Check(ctx, env) // 每次覆盖,不做历史隔离
            }()
        }
    }()
}
```

### 5.4 启用/禁用强制逻辑

```go
// internal/buildenv/service.go

func (s *Service) Update(ctx context.Context, env *BuildEnv) error {
    old, err := s.repo.GetByID(ctx, env.ID)
    if err != nil {
        return err
    }
    // image / source_type / credential_id 任一变更 → 重置为 unchecked
    if old.Image != env.Image || old.SourceType != env.SourceType || old.CredentialID != env.CredentialID {
        env.ImageCheckStatus = "unchecked"
        env.ImageCheckError = ""
        env.ImageCheckedAt = ""
    }

    // 已知不可用 → 强制保持 disabled 同步
    if env.ImageCheckStatus == "unavailable" {
        env.Enabled = false
    }

    return s.repo.Update(ctx, env)
}

// SetEnabled 启用/禁用。
// 启用检查顺序(严格):unavailable → 拒;unchecked → 拒(除非环境变量 PIPEWRIGHT_ALLOW_UNCHECKED_ENABLE=true,默认 false);
//
//	available → 允许。
//
// P0 #4 修订原因:避免“改了 image 未检查 → 构建时才报镜像不可用”的延迟错误。
func (s *Service) SetEnabled(ctx context.Context, id string, enabled bool) error {
    env, err := s.repo.GetByID(ctx, id)
    if err != nil {
        return err
    }
    if !enabled {
        return s.repo.SetEnabled(ctx, id, false)
    }
    switch env.ImageCheckStatus {
    case "unavailable":
        return &ValidationError{
            Code:    "IMAGE_UNAVAILABLE",
            Message: "镜像不存在或不可拉取,无法启用。请先检查镜像或更换地址",
        }
    case "unchecked":
        if !allowUncheckedEnable() {
            return &ValidationError{
                Code:    "IMAGE_NOT_CHECKED",
                Message: "镜像未检查过,请先点击【手动检查】或【手动拉取】验证可用性后再启用",
            }
        }
        // allowUncheckedEnable=true 时才放行(紧急场景)
    }
    return s.repo.SetEnabled(ctx, id, true)
}

func allowUncheckedEnable() bool {
    v := strings.ToLower(strings.TrimSpace(os.Getenv("PIPEWRIGHT_ALLOW_UNCHECKED_ENABLE")))
    return v == "1" || v == "true" || v == "yes"
}
```

**P0 #4 修订点**：

- `SetEnabled(enabled=true)` 现在对 `unchecked` 状态默认拒绝。
- 引入 `PIPEWRIGHT_ALLOW_UNCHECKED_ENABLE`（默认 `false`）作为**紧急逃生通道**，仅供紧急恢复使用。文档明示不推荐。
- 与 `Update` 中"unavailable 自动转 disabled"的策略合并，闭环:改 image 后状态必然是 unchecked → 启用被拒 → 强制走 check → 通过后才允许启用。

### 5.5 环境解析器（env_resolver）

```go
// internal/buildenv/resolver.go

// ResolveImage 据 language+version 查 build_envs 返回真实 image。
//   - 找不到 → ENV_NOT_FOUND
//   - 未启用 → ENV_DISABLED
//   - 镜像不可用 → IMAGE_UNAVAILABLE
//   - 找到 → 原样返回 env.Image,系统不拼接
func ResolveImage(ctx context.Context, lang, ver string, repo BuildEnvRepo) (string, error) {
    env, err := repo.GetByLanguageVersion(ctx, lang, ver)
    if err != nil {
        return "", fmt.Errorf("查询构建环境失败: %w", err)
    }
    if env == nil {
        return "", &ValidationError{
            Code:    "ENV_NOT_FOUND",
            Message: fmt.Sprintf("构建环境 %s/%s 未预置", lang, ver),
        }
    }
    if !env.Enabled {
        return "", &ValidationError{
            Code:    "ENV_DISABLED",
            Message: fmt.Sprintf("构建环境 %s/%s 已禁用", lang, ver),
        }
    }
    if env.ImageCheckStatus == "unavailable" {
        return "", &ValidationError{
            Code:    "IMAGE_UNAVAILABLE",
            Message: fmt.Sprintf("构建环境 %s/%s 的镜像不可拉取,请联系管理员", lang, ver),
        }
    }
    return env.Image, nil
}
```

### 5.6 流水线校验

```go
// internal/pipeline/validate.go

func ValidateJobEnvironment(
    ctx context.Context,
    job *Job,
    userRole string,
    envRepo buildenv.BuildEnvRepo,
    cfgRepo configprofile.ConfigProfileRepo,
) error {
    // 管理员可直接指定 image(逃生通道)
    if userRole == "admin" && job.Script.Image != "" {
        return nil
    }
    // 普通用户不可直接指定 image
    if job.Script.Image != "" {
        return &ValidationError{
            Code:    "IMAGE_NOT_ALLOWED",
            Message: "普通用户不可直接指定 image,请使用 language + version 选择预置环境",
        }
    }
    // 校验 language + version
    if job.Script.Language == "" {
        return &ValidationError{Code: "LANGUAGE_REQUIRED", Message: "请选择构建语言"}
    }
    if job.Script.Version == "" {
        return &ValidationError{Code: "VERSION_REQUIRED", Message: "请选择构建版本"}
    }
    env, err := envRepo.GetByLanguageVersion(ctx, job.Script.Language, job.Script.Version)
    if err != nil {
        return fmt.Errorf("查询构建环境失败: %w", err)
    }
    if env == nil || !env.Enabled {
        return &ValidationError{
            Code:    "ENV_NOT_FOUND",
            Message: fmt.Sprintf("构建环境 %s/%s 未预置或已禁用", job.Script.Language, job.Script.Version),
        }
    }
    if env.ImageCheckStatus == "unavailable" {
        return &ValidationError{
            Code:    "IMAGE_UNAVAILABLE",
            Message: "所选构建环境的镜像不存在,请联系管理员",
        }
    }
    // 校验 config_profile
    if job.Script.ConfigProfileID != "" {
        cfg, err := cfgRepo.GetByID(ctx, job.Script.ConfigProfileID)
        if err != nil {
            return fmt.Errorf("查询配置资源失败: %w", err)
        }
        if cfg == nil || !cfg.Enabled {
            return &ValidationError{Code: "CONFIG_NOT_FOUND", Message: "所选配置资源不可用"}
        }
        if cfg.Language != job.Script.Language {
            return &ValidationError{Code: "CONFIG_LANGUAGE_MISMATCH", Message: "配置资源语言不匹配"}
        }
    }
    return nil
}
```

### 5.7 凭据权限过滤

```go
// ListCredentials 按 role 过滤
func ListCredentials(ctx context.Context, session *Session) ([]*Credential, error) {
    if session.Role == "admin" {
        return repo.ListAll(ctx, ListFilter{
            IncludeGlobal:   true,
            IncludePersonal: true,
        })
    }
    return repo.List(ctx, ListFilter{
        Scope:   "personal",
        OwnerID: session.UserID,
    })
}

// ValidateCredentialAccess 校验引用凭据的归属
func ValidateCredentialAccess(ctx context.Context, credentialID string, session *Session) error {
    cred, err := repo.GetByID(ctx, credentialID)
    if err != nil {
        return err
    }
    if cred.Scope == "global" {
        return nil
    }
    if cred.OwnerID != session.UserID {
        return &ValidationError{
            Code:    "CREDENTIAL_ACCESS_DENIED",
            Message: "无权使用他人的个人凭据",
        }
    }
    return nil
}
```

### 5.8 管理员在 regular_users 的同步

```go
// internal/regularuser/consts.go  (新建,集中权威常量)
package regularuser

// AdminRegularUserID 是管理员在 regular_users 表中对应的固定 UUID。
// 业务上与 admin_user.id=1 是同一实体,凡涉及 personal 凭据 owner_id、
// 审计 actor、创建者引用等所有场景,必须使用本常量。
//
// 不要在代码中拼写该字符串,统一引用本常量。Linter 规则或 code review 必须拒绝硬编码。
const AdminRegularUserID = "00000000-0000-0000-0000-000000000001"

// internal/regularuser/service.go

// SyncAdminRow 在 regular_users 中创建/更新管理员行(与 admin_user 同步)。
//   - 首次启动 Bootstrap 调用一次
//   - ChangePassword 时同步更新 password_hash
//   - ID 永远使用 AdminRegularUserID,绝不调用 uuid 生成
func (s *Service) SyncAdminRow(username, passwordHash string) error {
    now := time.Now().UTC().Format(time.RFC3339)
    _, err := s.db.Exec(`
        INSERT INTO regular_users (id, username, password_hash, role, enabled, created_at, updated_at)
        VALUES (?, ?, ?, 'admin', 1, ?, ?)
        ON CONFLICT(id) DO UPDATE SET
            username       = excluded.username,
            password_hash  = excluded.password_hash,
            role           = 'admin',
            enabled        = 1,
            updated_at     = excluded.updated_at
    `, AdminRegularUserID, username, passwordHash, now, now)
    return err
}
```

**P0 #1 修订点**：

1. UUID 集中到 `regularuser.AdminRegularUserID`，加文件级注释明确"唯一权威"。
2. `ON CONFLICT(id)` 而非 `ON CONFLICT(username)`——确保 id 是主索引，避免未来 admin 改名破坏同步。
3. **Invariant 测试**（阶段 13 落实）：

```go
// internal/regularuser/consts_test.go
func TestAdminRegularUserID_Stable(t *testing.T) {
    // 任何人不允许修改该值
    if regularuser.AdminRegularUserID != "00000000-0000-0000-0000-000000000001" {
        t.Fatalf("AdminRegularUserID 变更,会破坏所有 admin personal 凭据归属")
    }
}
```

4. 文档其他位置凡涉及 "admin 在 regular_users 的 id"，统一用 `AdminRegularUserID` 替代硬编码。

### 5.9 4.7 节修复要点

#### 5.9.1 `scriptStepFromJob` 改造

```go
// internal/build/dag_stage_exec.go(原第 857 行)
// 旧:image := renderTemplate(cfgString(jb.Config, "image"), ctx)
// 新:从 language + version 查 env
func scriptStepFromJob(ctx context.Context, jb pipeline.Job, envRepo buildenv.BuildEnvRepo) (pipeline.PipelineStep, error) {
    lang := cfgString(jb.Config, "toolchainLanguage")
    ver := cfgString(jb.Config, "toolchainVersion")
    image, err := buildenv.ResolveImage(ctx, lang, ver, envRepo)
    if err != nil {
        return pipeline.PipelineStep{}, err
    }
    // commands / workDir / env 沿用现有逻辑
    ...
    return pipeline.PipelineStep{
        Image:    image, // 已是 env.Image,非拼接
        Commands: cmds,
        Env:      env,
        WorkDir:  workdir,
    }, nil
}
```

#### 5.9.2 `PostStep` 改造

`stage_post.go` 同理，PostStep 改用 `language + version`，构建时经 `env_resolver` 解析。

#### 5.9.3 旁挂服务 `ServiceSpec` 改造

```go
// 旧:
type ServiceSpec struct {
    Name  string
    Image string
    ...
}
// 新:
type ServiceSpec struct {
    Name       string
    BuildEnvID string   // 指向 build_envs.id
    ...
}
```

已有数据迁移（独立 story）：扫描所有项目，把 `service.image` 匹配到 `build_envs.image`；匹配不到则自动创建 `language='custom'` 的 env。

#### 5.9.4 工具链构建 (`builder.go:636`)

`toolchainImage(tc)` 删除，改为在 `build()` 内调用 `buildenv.ResolveImage(ctx, tc.Language, ts.Version, envRepo)`，得到的 image 传给 `driver.RunToolchain`。

#### 5.9.5 远程构建（不修改）

`internal/build/remote.go` 与 `remote_stage_exec.go` **不在本计划修改**。构建始终在宿主机执行；远程构建机的镜像可用性超出本计划范围。

---

## 六、前端设计

### 6.1 路由结构

```
/admin
  /settings
    /build-envs          -- 构建环境管理
    /config-profiles     -- 配置资源管理
    /credentials         -- 全局凭据管理
    /users               -- 用户管理(邀请、列出、禁用)
    /system              -- 系统参数
    /audit               -- 审计日志
  /personal
    /profile             -- 个人资料
    /credentials         -- 我的凭据(管理员也能创建 personal)
    /notifications       -- 通知偏好
    /security            -- 登录安全
```

### 6.2 管理员构建环境列表页

```
┌─────────────────────────────────────────────────────────────────────┐
│  构建环境管理                              [+ 新增构建环境] [检查全部]│
├─────────────────────────────────────────────────────────────────────┤
│  语言: [全部 ▼]   状态: [全部 ▼]   镜像状态: [全部 ▼]                │
├─────────────────────────────────────────────────────────────────────┤
│  ┌──────────────────────────────────────────────────────────────┐  │
│  │ Node.js                                                        │  │
│  │ ┌──────┬──────────────┬──────────────────┬────────┬──────┬──┐│  │
│  │ │ 版本 │ 显示名称      │ 镜像地址          │ 镜像   │ 状态 │操││  │
│  │ ├──────┼──────────────┼──────────────────┼────────┼──────┼──┤│  │
│  │ │ 22   │ Node.js 22   │ node:22-alpine   │ ✓ 可用 │ 启用 │编││  │
│  │ │ 24   │ Node.js 24   │ node:24-alpine   │ ✓ 可用 │ 启用 │编││  │
│  │ └──────┴──────────────┴──────────────────┴────────┴──────┴──┘│  │
│  ├──────────────────────────────────────────────────────────────┤  │
│  │ Java                                                           │  │
│  │ ┌──────┬──────────────┬──────────────────┬────────┬──────┬──┐│  │
│  │ │ 11   │ Java 11      │ eclipse-temurin..│ ✓ 可用 │ 启用 │编││  │
│  │ │ 17   │ Java 17      │ eclipse-temurin..│ ✗ 不存在│ 禁用 │编││  │
│  │ │ 21   │ Java 21      │ registry.compa.. │ ○ 未检查│ 启用 │编││  │
│  │ └──────┴──────────────┴──────────────────┴────────┴──────┴──┘│  │
│  └──────────────────────────────────────────────────────────────┘  │
│                                                                      │
│  Java 17 不可用原因:                                                  │
│  manifest inspect 失败: unauthorized: authentication required         │
│  [重新检查]  [手动拉取]  [更换地址]                                    │
└─────────────────────────────────────────────────────────────────────┘
```

### 6.3 管理员构建环境表单

```
┌─────────────────────────────────────────────────────────────┐
│  编辑构建环境: Java 11                                       │
├─────────────────────────────────────────────────────────────┤
│  语言 *        [Java          ▼]                             │
│  版本 *        [11            ]                              │
│  显示名称 *    [Java 11       ]                              │
│                                                              │
│  ── 镜像配置 ──────────────────────────────                  │
│  镜像来源 *    [官方 (Docker Hub)  ▼]                        │
│                ├─ 官方 (Docker Hub)                          │
│                └─ 其他                                       │
│                                                              │
│  镜像地址 *    [eclipse-temurin:11-jdk              ]        │
│                                                              │
│  (选择"其他"时显示)                                          │
│  拉取凭证      [                ▼]  (可选)                   │
│                ├─ (无)                                       │
│                └─ aliyun-registry-cred                       │
│                                                              │
│  ── 镜像状态 ──────────────────────────────                  │
│  状态: ● 可用 (2026-09-21 10:30 检查)                        │
│  [手动检查]  [手动拉取]                                       │
│                                                              │
│  ── 环境信息 ──────────────────────────────                  │
│  环境说明      [适用于传统Java项目构建              ]        │
│  排序          [0             ]                              │
│  启用          [●]  (镜像不可用时不可勾选)                   │
├─────────────────────────────────────────────────────────────┤
│                                          [取消]  [保存]      │
└─────────────────────────────────────────────────────────────┘
```

### 6.4 配置资源管理页

```
┌─────────────────────────────────────────────────────────────┐
│  配置资源管理                              [+ 新增配置资源]   │
├─────────────────────────────────────────────────────────────┤
│  语言筛选: [全部 ▼]    类型: [全部 ▼]                        │
├─────────────────────────────────────────────────────────────────────┤
│  ┌───────────────────────────────────────────────────────┐  │
│  │ Java / Maven                                           │  │
│  │ ┌──────────────────┬────────────┬────────┬──────┬────┐│  │
│  │ │ 名称             │ 文件路径    │ 默认   │ 状态 │操作││  │
│  │ ├──────────────────┼────────────┼────────┼──────┼────┤│  │
│  │ │ 默认Maven配置    │ /root/.m2/ │  ✓     │ 内置 │查看││  │
│  │ │ 公司私服配置     │ /root/.m2/ │        │ 启用 │编辑││  │
│  │ └──────────────────┴────────────┴────────┴──────┴────┘│  │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

新增/编辑配置资源：

```
┌─────────────────────────────────────────────────────────────┐
│  新增配置资源                                                │
├─────────────────────────────────────────────────────────────┤
│  语言 *        [Java          ▼]                             │
│  配置类型 *    [Maven         ▼]                             │
│  名称 *        [公司私服配置   ]                              │
│  容器内目标路径 * [/root/.m2/settings.xml]                   │
│  设为默认      [●]                                           │
│  说明          [使用公司内部Maven私服                  ]      │
│                                                              │
│  ── 配置文件内容 ──────────────────────────────              │
│  输入方式:  ○ 在线编辑   ● 上传文件                           │
│  ┌─────────────────────────────────────────────────────┐    │
│  │        点击或拖拽文件到此处上传                        │    │
│  │        支持: .xml, .conf, .npmrc, .ini, .env          │    │
│  │        大小限制: 1MB                                   │    │
│  └─────────────────────────────────────────────────────┘    │
│  已上传: settings.xml (2.3 KB)                               │
│  说明:文件保存到 ${DATA_DIR}/config_profiles/<id>/settings.xml│
├─────────────────────────────────────────────────────────────┤
│                                          [取消]  [保存]      │
└─────────────────────────────────────────────────────────────┘
```

### 6.5 凭据管理（个人）

```
┌─────────────────────────────────────────────────────────────┐
│  我的凭据                                    [+ 新增凭据]    │
├─────────────────────────────────────────────────────────────┤
│  ┌────────────────────┬──────────────┬──────────┬──────────┐│
│  │ 名称               │ 类型          │ 最后使用  │ 操作     ││
│  ├────────────────────┼──────────────┼──────────┼──────────┤│
│  │ my-git-token       │ Token         │ 2天前    │编辑 删除 ││
│  │ my-ssh-key         │ SSH密钥       │ 1周前    │编辑 删除 ││
│  └────────────────────┴──────────────┴──────────┴──────────┘│
│  提示: 个人凭据仅你本人可见和使用                              │
└─────────────────────────────────────────────────────────────┘
```

### 6.6 凭据管理（全局，仅管理员）

```
┌─────────────────────────────────────────────────────────────┐
│  全局凭据                                    [+ 新增凭据]    │
├─────────────────────────────────────────────────────────────┤
│  ┌────────────────────┬──────────────┬──────────┬──────────┐│
│  │ 名称               │ 类型          │ 创建者    │ 操作     ││
│  ├────────────────────┼──────────────┼──────────┼──────────┤│
│  │ aliyun-registry    │ 用户名密码    │ admin    │编辑 删除 ││
│  │ deploy-key-prod    │ SSH密钥       │ admin    │编辑 删除 ││
│  └────────────────────┴──────────────┴──────────┴──────────┘│
└─────────────────────────────────────────────────────────────┘
```

### 6.7 用户端"构建与制品"面板

```
┌─────────────────────────────────────────────────┐
│  构建与制品                                      │
├─────────────────────────────────────────────────┤
│  步骤名称 *    [Build Frontend      ]            │
│                                                  │
│  ── 构建环境 ──────────────────────────────      │
│  语言 *        [Node.js      ▼]                  │
│  版本 *        [22           ▼]                  │
│  ┌──────────────────────────────────────────┐   │
│  │ 已选: Node.js 22 (node:22-alpine)         │   │
│  │ 说明: 适用于前端项目构建                    │   │
│  └──────────────────────────────────────────┘   │
│                                                  │
│  ── 配置资源 ──────────────────────────────      │
│  NPM配置       [默认NPM配置  ▼]                  │
│                                                  │
│  ── 构建命令 ──────────────────────────────      │
│  ┌──────────────────────────────────────────┐   │
│  │ npm ci                                    │   │
│  │ npm run build                             │   │
│  └──────────────────────────────────────────┘   │
├─────────────────────────────────────────────────┤
│                              [取消]  [保存]      │
└─────────────────────────────────────────────────┘
```

### 6.8 流水线编辑页（YAML 编辑入口移除）

- 删除 `PipelineCanvas.vue` 的 YAML 折叠区（原第 383-400 行）。
- 删除 `PipelineEditor.vue` 中所有 yaml-only 的编辑器入口。
- **保留** `YamlImportModal.vue`（导入预览模态框）。
- `description` 字段始终显示，无脑返回。

---

## 七、权限与凭据模型

### 7.1 角色定义

| 角色 | 权限 |
|---|---|
| `admin` | 可访问 `/api/admin/*` 全部端点；可管理构建环境、配置资源、全局凭据、用户、系统参数、审计日志；可在流水线中直接指定 `image`（逃生通道）；能创建 personal 凭据（以个人身份） |
| `user` | 只能访问只读端点；流水线中只能选择预置的 `language + version + config_profile`；不可直接指定 `image`；可管理自己的 personal 凭据 |

### 7.2 凭据权限矩阵

| 操作 | global 凭据 | personal 凭据 |
|---|---|---|
| 创建 | 仅管理员 | 任何登录用户（含管理员） |
| 查看列表 | 所有用户 | 仅创建者本人 |
| 查看明文 | 仅管理员（操作时） | 仅创建者本人 |
| 编辑 | 仅管理员 | 仅创建者本人 |
| 删除 | 仅管理员 | 仅创建者本人 |
| 在流水线中引用 | 所有用户 | 仅创建者本人 |
| 管理员禁用(`enabled=0`) | 不适用 | 可禁用（不可物理删除） |

### 7.3 设置模块权限分配

| 设置项 | 管理员 | 普通用户 |
|---|---|---|
| 构建环境管理 | ✅ | ❌ |
| 配置资源管理 | ✅ | ❌ |
| 全局凭据管理 | ✅ | ❌ |
| 用户管理 | ✅ | ❌ |
| 系统参数 | ✅ | ❌ |
| 审计日志 | ✅ | ❌ |
| 个人资料 | ✅ | ✅ |
| 我的凭据 | ✅ | ✅ |
| 通知偏好 | ✅ | ✅ |
| 登录安全 | ✅ | ✅ |
| 界面偏好 | ✅ | ✅ |

---

## 八、配置项与环境变量

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `PIPEWRIGHT_ENFORCE_BUILD_ENV` | `true` | 是否启用构建环境白名单校验 |
| `PIPEWRIGHT_UI_ONLY` | `true` | 是否禁用 YAML 直接编辑（导入/导出保留） |
| `PIPEWRIGHT_BUILDER` | `auto` | 容器运行时检测 |
| `PIPEWRIGHT_RUNTIME` | 自动 | 容器内运行检测 |
| `PIPEWRIGHT_CONFIG_UPLOAD_MAX_SIZE` | `1048576` | 配置文件上传大小限制（字节） |
| `PIPEWRIGHT_AUTO_CHECK_ON_START` | `true` | 服务启动后是否自动检查镜像 |
| `PIPEWRIGHT_CHECK_CONCURRENCY` | `10` | 镜像检查并发上限 |
| `PIPEWRIGHT_CHECK_TIMEOUT_SECONDS` | `60` | 单次镜像检查（inspect）超时秒数 |
| `PIPEWRIGHT_PULL_TIMEOUT_MULTIPLIER` | `4` | pull 超时倍数（pull = timeout × N） |
| `PIPEWRIGHT_DATA_DIR` | `./data` | 数据目录（config_profiles 落到此目录下） |
| `PIPEWRIGHT_ALLOW_UNCHECKED_ENABLE` | `false` | **紧急逃生**：允许在镜像 unchecked 时直接启用（仅供恢复场景，不推荐） |

---

## 九、实施计划

```
阶段 1:regular_users service + Bootstrap 同步 ──┐
                                                │
阶段 2:凭据扩展(regular_users + credentials 关联) ──┤
                                                │
阶段 3:RBAC 中间件 + Session.role 扩展 ──────────┤
                                                │
阶段 4:build_envs + config_profiles service ────┤
                                                │
阶段 5:镜像检查器(60s/10/覆盖)───────────────┤
                                                │
阶段 6:env_resolver + builder/services/post 接入 ┤
                                                │
阶段 7:流水线校验 ValidateJobEnvironment ───────┤
                                                │
阶段 8:YAML 端点裁剪 + RequireUIOnly 中间件 ────┤
                                                │
阶段 9:API 层(build_envs/config_profiles/credentials/users)─┐
                                                              │
阶段 10:前端管理页(admin/*)───────────────────────────────────┤
                                                              │
阶段 11:前端个人设置页(personal/*)─────────────────────────────┤
                                                              │
阶段 12:前端 YAML 编辑入口移除 + 编辑器适配 ──────────────────┤
                                                              │
阶段 13:内置数据 seed ─────────────────────────────────────────┤
                                                              │
阶段 14:启动自动检查装配 ──────────────────────────────────────┤
                                                              │
阶段 15:测试(单元+集成+E2E,贯穿每个阶段)─────────────────────┘
```

**关键调整说明**：
- 阶段 1 优先做 `regular_users` + Bootstrap 同步（γ2 方案的基础）。
- 阶段 1 子任务 1.1：创建 `internal/regularuser/consts.go` 定义 `AdminRegularUserID` 常量（**P0 #1 修订**）。
- 阶段 4 子任务 4.3：`config_profiles` service 实现原子写文件 + DB 事务（**P0 #3 修订**）。
- 阶段 4 子任务 4.4：`build_envs` service 实现 `SetEnabled` 三态校验（**P0 #4 修订**）。
- 阶段 5 单独做镜像检查器（不含 builder 接入），便于单测。
- 阶段 6 是 4.7 节修复的关键阶段，独立可测。
- 阶段 13（内置数据）排在阶段 14（自动检查装配）之前，确保自动检查启动时有数据可查。
- 阶段 13 子任务 13.2：`credentials` 表新增 CHECK 约束（**P0 #2 修订**）；同步声明 `UNIQUE(scope, owner_id, name)` 的语义。
- 阶段 15 测试贯穿每个阶段，不是只在末尾。

---

## 十、关键文件清单

| 文件路径 | 类型 | 说明 |
|---|---|---|
| `internal/regularuser/model.go` | 新增 | RegularUser 实体 |
| `internal/regularuser/service.go` | 新增 | CRUD、邀请注册、SyncAdminRow |
| `internal/buildenv/model.go` | 新增 | BuildEnv 实体 |
| `internal/buildenv/service.go` | 新增 | 业务逻辑、启用/禁用校验 |
| `internal/buildenv/checker.go` | 新增 | 镜像检查、手动拉取、自动检查（60s/10/覆盖） |
| `internal/buildenv/resolver.go` | 新增 | language+version → image（env_resolver） |
| `internal/configprofile/model.go` | 新增 | ConfigProfile 实体 |
| `internal/configprofile/service.go` | 新增 | 业务逻辑 + 文件上传 |
| `internal/credential/model.go` | 扩展 | 在 vault 包内扩展 owner_id / disabled / description |
| `internal/credential/service.go` | 扩展 | 权限过滤（admin/user 分别返回） |
| `internal/auth/rbac.go` | 新增 | RequireAdmin / RequireUser / RequireAuth |
| `internal/auth/session.go` | 修改 | Session 增加 Role 字段 |
| `internal/auth/service.go` | 修改 | Bootstrap 同步到 regular_users；ChangePassword 同步 |
| `internal/httpapi/buildenv.go` | 新增 | 构建环境处理器 |
| `internal/httpapi/configprofile.go` | 新增 | 配置资源处理器 |
| `internal/httpapi/credential.go` | 新增/扩展 | 凭据处理器（含 personal/global 区分） |
| `internal/httpapi/users.go` | 新增 | 用户管理（邀请、列出、禁用） |
| `internal/httpapi/middleware.go` | 新增/扩展 | RequireUIOnly + RequireAdmin/RequireUser |
| `internal/pipeline/validate.go` | 扩展 | 校验逻辑接入 env_resolver |
| `internal/build/script_steps.go` | 修改 | scriptStepFromJob 改用 env_resolver |
| `internal/build/dag_stage_exec.go` | 修改 | 同上 |
| `internal/build/stage_post.go` | 修改 | PostStep 改用 env_resolver |
| `internal/build/services.go` | 修改 | ServiceSpec.BuildEnvID 替代 Image |
| `internal/build/builder.go` | 修改 | toolchain 构建改用 env_resolver |
| `internal/build/remote.go` | 不动 | 远程构建超出本计划 |
| `internal/build/remote_stage_exec.go` | 不动 | 同上 |
| `internal/pipeline/pipeline.go` | 修改 | ServiceSpec 字段调整 |
| `cmd/pipewright/main.go` | 修改 | 装配新领域包 + 启动自动检查 |
| `web/src/views/admin/BuildEnvs.vue` | 新增 | 构建环境管理 |
| `web/src/views/admin/ConfigProfiles.vue` | 新增 | 配置资源管理 |
| `web/src/views/admin/Credentials.vue` | 新增 | 全局凭据管理 |
| `web/src/views/admin/Users.vue` | 新增 | 用户管理 |
| `web/src/views/admin/System.vue` | 新增 | 系统参数 |
| `web/src/views/admin/Audit.vue` | 新增 | 审计日志 |
| `web/src/views/personal/Profile.vue` | 新增 | 个人资料 |
| `web/src/views/personal/Credentials.vue` | 新增 | 我的凭据 |
| `web/src/views/personal/Notifications.vue` | 新增 | 通知偏好 |
| `web/src/views/personal/Security.vue` | 新增 | 登录安全 |
| `web/src/components/BuildEnvModal.vue` | 新增 | 构建环境表单 |
| `web/src/components/ConfigProfileModal.vue` | 新增 | 配置资源表单 |
| `web/src/components/CredentialModal.vue` | 新增 | 凭据表单 |
| `web/src/components/ScriptJobEditor.vue` | 修改 | 用户端环境选择器 |
| `web/src/components/pipeline/PipelineCanvas.vue` | 修改 | 删除 YAML 折叠区 |
| `web/src/components/pipeline/YamlImportModal.vue` | 保留 | 导入预览模态框 |
| `web/src/router/index.js` | 修改 | 路由守卫（含 role 检查） |
| `web/src/views/PipelineEditor.vue` | 修改 | 移除 YAML 编辑入口 |

---

## 十一、兼容性与边界

### 11.1 边界声明

- **迁移脚本**：本计划只输出最终 DDL，不输出迁移脚本。升级路径由迁移团队另行处理。
- **远程构建**：本计划假设构建始终在宿主机执行。`internal/build/remote.go` 与 `ProjectRunner.remote` 类型保留但不在本计划修改；远程构建机的镜像可用性由后续 story 处理。
- **全新部署**：从零建表是 DDL 状态。

### 11.2 风险与缓解

| 风险 | 缓解措施 |
|---|---|
| 镜像不存在导致构建失败 | 自动检查 + 强制禁用不可用环境；手动拉取验证；env_resolver 在 unavailable 时拒绝构建 |
| 凭据泄露 | NaCl secretbox 加密；个人凭据 owner_id 校验；日志脱敏 |
| 普通用户通过"改 YAML 保存"端点绕过 | 删除该端点；前端 PipelineCanvas 移除 YAML 折叠区；管理员逃生通道不影响普通用户 |
| 个人凭据被他人引用 | 后端强制校验 owner_id == session.UserID，拒绝越权访问 |
| 镜像检查任务挂死 | 每任务 60s timeout + 全局 10 并发上限；exec.CommandContext 强制超时 |
| docker login 污染宿主 ~/.docker/config.json | Check 路径不 login；Pull 路径 login 后立即 logout |
| 管理员 personal 凭据误用 | admin 创建 personal 时 owner_id 指向 admin 在 regular_users 的行；与 global 凭据视觉区分 |
| 多用户引入认证漏洞 | 复用 argon2id + CSRF；dummyHash 时序保护不变 |

---

## 十二、附录：内置数据

### 12.1 内置构建环境

| 语言 | 版本 | image | display_name | 来源 |
|---|---|---|---|---|
| node | 22 | `node:22-alpine` | Node.js 22 (Alpine) | official |
| node | 24 | `node:24-alpine` | Node.js 24 (Alpine) | official |
| java | 8 | `eclipse-temurin:8-jdk` | Java 8 (Temurin) | official |
| java | 11 | `eclipse-temurin:11-jdk` | Java 11 (Temurin) | official |
| java | 17 | `eclipse-temurin:17-jdk` | Java 17 (Temurin) | official |
| java | 21 | `eclipse-temurin:21-jdk` | Java 21 (Temurin) | official |
| go | 1.22 | `golang:1.22-alpine` | Go 1.22 | official |
| go | 1.23 | `golang:1.23-alpine` | Go 1.23 | official |
| python | 3.11 | `python:3.11-slim` | Python 3.11 | official |
| python | 3.12 | `python:3.12-slim` | Python 3.12 | official |
| custom | default | `alpine:latest` | 自定义环境 | official |

### 12.2 内置配置资源

| 语言 | 类型 | 名称 | target_path | 默认 | 内置 |
|---|---|---|---|---|---|
| java | maven | 默认Maven配置(阿里云) | `/root/.m2/settings.xml` | 是 | 是 |
| node | npm | 默认NPM配置(淘宝源) | `/root/.npmrc` | 是 | 是 |
| python | pip | 默认PIP配置(清华源) | `/root/.config/pip/pip.conf` | 是 | 是 |
| go | goproxy | 默认GOPROXY配置 | `/root/.config/go/env` | 是 | 是 |

### 12.3 内置管理员在 regular_users 的同步

`admin_user.id=1` 创建时，同步在 `regular_users` 创建 `id=regularuser.AdminRegularUserID` (`00000000-0000-0000-0000-000000000001`), `role='admin'`, `username` 与 `password_hash` 与 `admin_user` 同步。**该 UUID 由 `regularuser.AdminRegularUserID` 常量统一引用，不得在调用点硬编码**。

### 12.4 内置 Maven settings.xml

```xml
<?xml version="1.0" encoding="UTF-8"?>
<settings xmlns="http://maven.apache.org/SETTINGS/1.0.0"
          xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"
          xsi:schemaLocation="http://maven.apache.org/SETTINGS/1.0.0
                              http://maven.apache.org/xsd/settings-1.0.0.xsd">
    <mirrors>
        <mirror>
            <id>aliyun-public</id>
            <mirrorOf>*</mirrorOf>
            <url>https://maven.aliyun.com/repository/public</url>
        </mirror>
    </mirrors>
    <profiles>
        <profile>
            <id>default</id>
            <repositories>
                <repository>
                    <id>aliyun-public</id>
                    <url>https://maven.aliyun.com/repository/public</url>
                    <releases><enabled>true</enabled></releases>
                    <snapshots><enabled>true</enabled></snapshots>
                </repository>
            </repositories>
        </profile>
    </profiles>
    <activeProfiles>
        <activeProfile>default</activeProfile>
    </activeProfiles>
</settings>
```

### 12.5 内置 NPM `.npmrc`

```
registry=https://registry.npmmirror.com
```

### 12.6 内置 PIP `pip.conf`

```ini
[global]
index-url = https://pypi.tuna.tsinghua.edu.cn/simple
trusted-host = pypi.tuna.tsinghua.edu.cn
```

### 12.7 内置 GOPROXY

```
GOPROXY=https://goproxy.cn,direct
```

---

## 十三、变更摘要（vs v5）

| 变更项 | v5 | v6 |
|---|---|---|
| 用户模型 | 新建独立 `users` 表 | γ2 方案：`admin_user` 保留 + `regular_users` 扩展，admin 在 `regular_users` 里也有一行 |
| 凭据 owner_id | 指向通用 `users.id` | 统一指向 `regular_users.id` |
| 管理员 personal 凭据 | 不支持 | 支持（owner_id 指向 admin 在 regular_users 的行） |
| 配置文件路径 | 白名单 | 固定目录（`${DATA_DIR}/config_profiles/<id>/`），不再校验白名单 |
| `config_profiles.content` | 仅 DB | 双存储（DB + 磁盘文件），运行时优先用磁盘 |
| 镜像检查 timeout | 未指定 | 60s（inspect），240s（pull） |
| 镜像检查并发 | 未指定 | 10 |
| 镜像检查结果 | 未明 | 覆盖式（不存历史，审计由 internal/audit 触发） |
| 远程构建 | 计划涉及 | 超出本计划，构建始终在宿主机 |
| 迁移脚本 | 编号 0002-0005 | 不输出迁移脚本，只输出最终 DDL |
| YAML 禁用 | 阻断导入 | 仅删除"改 YAML 保存"端点，保留导入导出 |

### 13.1 v6 → v6.1（P0 安全修订）

| P0 | 修订要点 | 落地位置 |
|---|---|---|
| #1 | admin UUID 抽 `regularuser.AdminRegularUserID` 常量；`ON CONFLICT(id)` 而非 username；新增 invariant 测试 | §5.8 / §12.3 |
| #2 | `credentials` 新增 CHECK 约束（`scope='global' AND owner_id=''` OR `scope='personal' AND owner_id<>''`），与 UNIQUE 共同保证全局/个人强一致 | §4.5 |
| #3 | `config_profiles` 写路径原子（tmp + fsync + rename + 事务）；运行时以磁盘为权威；内置资源字段白名单（仅 description/enabled 可改） | §3.3 |
| #4 | `SetEnabled` 三态校验：`unavailable` 拒、`unchecked` 拒（紧急逃生 `PIPEWRIGHT_ALLOW_UNCHECKED_ENABLE` 默认 `false`）、`available` 放 | §5.4 / §八 |

---

> 本文档为 Pipewright 构建环境预置与管理功能的 **v6.1** 设计说明。基于用户多轮决策迭代，覆盖用户模型（γ2）、最终 DDL、凭据分级、固定路径配置资源、镜像检查（60s/10/覆盖）、YAML 仅禁用编辑入口、本机构建边界等关键决策点，并完成 P0 安全修订。所有迁移细节、远程构建细节留作后续 story 处理。