# Pipewright 构建环境预置与管理 —— v6.2 设计文档

> 版本：v6.2（基于 v6 + P0 安全修订 + 与现有代码架构对齐）
> 适用范围：Pipewright 平台改造，构建环境统一预置、配置资源管理、凭据分级（多用户基础）、YAML 仅禁用编辑入口。
> 边界：本计划**只输出最终 DDL 与实现设计**，不输出迁移脚本；升级路径由迁移团队另行处理。

---

## 一、本版本相对 v6 的核心调整

v6 是设计稿，与现有代码存在系统性偏差。v6.2 已对齐到现状：

| # | v6 假设 | 现状 | v6.2 决策 |
|---|---|---|---|
| 1 | 新建 `regular_users` 表 | 不存在；仅 `admin_user CHECK(id=1)` 单行 | `admin_user` 升级为多管理员表（解除 CHECK），不再有 `regular_users` 与 admin_user 二元结构 |
| 2 | 新建 `build_envs` 表 + `internal/buildenv` 包 | `internal/environments` 是"部署环境聚合"，零迁移只读 JOIN | `internal/environments` 重命名为 `internal/deployenv`；新建 `internal/buildenv` 承载构建环境管理 |
| 3 | 新建 `config_profiles` 表（自包含） | `pipeline_settings.build_json` 内的 `ImageRegistry`/`Toolchain` 承载类似职责 | `config_profiles` 表新增（用于 Maven/npm/pip/goproxy 配置文件），不与 pipeline_settings 重复 |
| 4 | `credentials` 表缺 `username`/`description`/`enabled` 等 | 0048 迁移已加 `username` | **追加 0049 迁移**：加 `description`、`enabled`、`disabled_by`、`disabled_at`、`owner_id`、`scope` 强一致 CHECK |
| 5 | `vault` 包仅作加密原语，业务层新建 `internal/credential` | `internal/vault` 已是完整业务层（CRUD + 在用守卫 + audit） | **扩展 `internal/vault`**，加 RBAC 字段与方法，不新建平行包 |
| 6 | "管理员也能创建 personal 凭据"通过前端 UI 区分 | `vault` 包**不感知用户身份** | 在 vault 层加 `Actor` 注入；service 层做 owner_id 校验 |
| 7 | 删 `PATCH/PUT /api/pipelines/:id/yaml` | 该端点不存在 | 真实禁用目标是 `POST /api/projects/{id}/pipeline/import{save:true}` 的 `save` 子句 + `PUT /api/projects/{id}/pipeline` 的 YAML 入参 |

---

## 二、需求总结（v6 §2 不变，本版本继续生效）

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
| R14 | 禁用 YAML **直接编辑**，保留 YAML 导入/导出 | 前端隐藏 YAML 编辑入口；后端拒绝 YAML 入参保存 |
| R15 | 支持管理员与普通用户多角色 | `admin_user` 升级为多管理员表，新增 `users` 表存普通用户；凭据 owner_id 指向 `users.id` |
| R16 | 管理员也能创建 personal 凭据 | 语义"以个人身份创建"；owner_id 指向该管理员自己的 `users.id` |
| R17 | 镜像检查每次结果覆盖前次 | 不存历史快照；审计由 internal/audit 触发 |

---

## 三、核心设计决策

### 3.1 镜像来源

只保留两种来源：

| 来源 | 说明 | 凭据 |
|---|---|---|
| `official` | Docker Hub 官方镜像 | 无需凭据 |
| `custom` | 任意自定义地址，完整镜像地址必填 | 可选关联凭据 |

系统**不做任何地址拼接**，填什么拉什么。

### 3.2 镜像检查与状态

| 状态 | 含义 | 是否可启用 |
|---|---|---|
| `unchecked` | 未检查（新创建或地址刚修改） | **默认拒**（P0 #4 修订） |
| `checking` | 检查中 | 不可启用 |
| `available` | 镜像存在 | 可启用 |
| `unavailable` | 镜像不存在或检查失败 | **不可启用** |

**自动检查**：服务启动后，后台任务遍历 `build_envs`，并发执行 `docker manifest inspect`（默认 10 并发，每任务 60s 超时）。通过 `errgroup` 单一启动，进程重启不重复打 docker daemon。

**手动检查**：`docker manifest inspect`，60s 超时。

**手动拉取**：`docker pull`，240s 超时（60s × 4），用于修改地址后验证。Pull 前按需 login，pull 完立即 logout。

**结果覆盖**：每次检查直接 UPDATE 当前行（`image_check_status` / `image_check_error` / `image_checked_at`），不做历史隔离；历史快照由 `internal/audit` 触发（append-only）。

### 3.3 配置资源

- 文件落到宿主固定目录（`${PIPEWRIGHT_DATA_DIR}/config_profiles/<id>/<filename>`），不再校验白名单路径。
- 数据库**双存储**：`file_path`（宿主机路径）+ `content`（文件内容文本）。
- **权威路径是磁盘文件**：`file_path` 指向的文件为运行时唯一真相；`content` 仅作冗余快照（用于：审计、导入/导出、文件丢失时的回填）。
- **`is_builtin=1` 的内置配置资源不允许通过 web API 修改 `target_path` / `content` / `file_path` 任一字段**（前端隐藏编辑入口，后端 service 在保存前对 `is_builtin` 行做字段白名单，仅允许改 `description` 与 `enabled`）。
- **写路径原子性**：Save 时先写临时文件 `<filename>.tmp.<pid>` → `fsync` → `os.Rename` 到 `<filename>`，再在同一 SQL 事务里 `UPDATE config_profiles SET file_path=?, content=?, updated_at=? WHERE id=?`。**任何一步失败 → 整笔回滚**（删除临时文件 + DB 事务 rollback）。
- **读路径优先级**：`runner.ConfigInjector` 优先 `os.ReadFile(file_path)`，若文件不存在才回退 `DB.content` 并记录 warning。
- `target_path` 字段记录"容器内目标路径"（如 `/root/.m2/settings.xml`），构建时由 `runner.ConfigInjector` 拷贝到容器。
- 独立管理页面，支持在线编辑与文件上传（**multipart 上传**，本期新增）。
- 内置配置资源仅可查看，不可编辑删除。
- 自定义配置资源可编辑、删除、上传文件。
- 按语言与配置类型分组管理。

### 3.4 凭据分级（v6.2 修订：在现有 `credentials` 表上扩展，不新建表）

| scope | 创建者 | owner_id | 可见范围 | 典型用途 |
|---|---|---|---|---|
| `global` | 仅管理员 | `''` | 所有用户 | 系统级通知 Token、共享部署密钥、镜像仓库凭证 |
| `personal` | 任意登录用户（含管理员） | `users.id` | **仅创建者本人** | 个人 Git Token、个人 SSH 密钥、个人 API Key |

- **作用域/owner 一致性**：DB 层加 CHECK 约束（`scope='global' AND owner_id=''` 或 `scope='personal' AND owner_id<>''`）。
- 凭据 `owner_id` **统一指向 `users.id`**（UUID v4）。管理员创建 personal 凭据时，owner_id 指向该管理员在 `users` 表里对应的那一行。
- 管理员可查看个人凭据的**元数据**（名称、创建者、创建时间、最后使用时间），但**无法查看明文**，也无法在流水线中引用他人的个人凭据。
- 管理员可禁用（`enabled=0` + `disabled_by` + `disabled_at`）违规的个人凭据，但不可删除。
- 字段语义沿用现有 vault 一切约束（NaCl secretbox 加密、`masked_value`、`last_used_at`、`username`）。
- **本期实现要点**：vault 包引入 `Actor` 概念；`List()`、`Get()`、`Update()`、`Delete()` 都按 Actor 过滤；`Vault.CredentialView` 结构体加 `OwnerID`/`Description`/`Enabled`/`DisabledBy`/`DisabledAt`/`CreatedBy` 字段。

### 3.5 用户模型（γ2 方案 + v6.2 微调）

```
users (本期新增,普通用户 + 管理员个人行)
├── role='admin' 的若干行
└── role='user'  的若干行

admin_user (升级为多管理员表,解除 CHECK(id=1))
├── 多行管理员账户(每行对应一个实际管理员身份)
└── username/password_hash 与 users.role='admin' 的那一行同步

sessions (不变)
└── token + csrf_token + created_at + expires_at + last_seen_at
```

**v6.2 修订点**：

- `admin_user` 不再是单行。`CHECK(id=1)` 改为不复用 CHECK，但**保留 INTEGER PRIMARY KEY 自增**。
- 新增 `users` 表存普通用户 + 管理员的"个人行"（personal 凭据 owner 视角）。
- 个人凭据 owner_id 统一指向 `users.id`（UUID v4）；管理员自己的 personal 凭据 owner_id 指向该管理员在 users 中的对应记录。
- `admin_user.username/password_hash` 在 `users.role='admin'` 那一行的 `username/password_hash` 同步更新（登录、ChangePassword 时双向同步）。
- **首次启动 Bootstrap**：仅在 `admin_user` 空时通过 `PIPEWRIGHT_ADMIN_PASSWORD` 创建第一个管理员；同步在 `users` 创建 `role='admin'` 的对应行。

### 3.6 设置模块权限隔离

```
⚙️ 设置
├── 🔧 全局设置（仅管理员可见）
│   ├── 构建环境管理
│   ├── 配置资源管理
│   ├── 全局凭据管理
│   ├── 用户管理（邀请、列出、禁用）
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

### 3.7 YAML 处理（v6.2 修订：真实禁用目标）

| 入口 | 现状 | v6.2 动作 |
|---|---|---|
| `POST /api/projects/{id}/pipeline/import`（`save:true`） | 接收 YAML 字符串，解析后保存 | **保留**（导入通道） |
| `PUT /api/projects/{id}/pipeline` | 接收 `stages:[...]` 数组 | **保留**（结构化编辑，**禁止接受 `yaml` 字段**） |
| `GET /api/projects/{id}/pipeline` | 返回 `{spec, yaml, stages}` | **保留**（导出在响应里映出，只读） |
| `GET /api/projects/{id}/pac/preview` | 拉 `.pipewright.yml` 预览 | **保留**（pacloader 通道） |
| `PATCH /api/pipelines/:id/yaml` | **不存在** | 无操作 |

**前端动作**：

- 删除 `web/src/components/pipeline/PipelineCanvas.vue` 的 YAML 折叠区（原 325-400 行）。
- 删除 `web/src/components/pipeline/YamlImportModal.vue` 中"导入后编辑并保存"按钮（保留导入预览）。
- 后端 `PUT /api/projects/{id}/pipeline` handler（`internal/httpapi/pipelines.go:204-227`）：如请求 body 含 `yaml` 字段 → 返回 400 `yaml_direct_edit_disabled`。

### 3.8 构建边界

- 构建**始终在 pipewright 进程所在的宿主机执行**，不区分"二进制部署"还是"Docker 部署"。
- 镜像拉取、检查、推送都在宿主机执行。
- `internal/build/remote.go` 与 `ProjectRunner.remote` 类型保留，**远程构建机的镜像可用性超出本计划范围**，留作后续 story。

---

## 四、数据模型（最终 DDL）

> 本节输出**最终状态表结构**，不输出迁移脚本。所有 SQLite 迁移文件新建到 `internal/store/migrations/sqlite/`，对应 MySQL 版本同步新建到 `internal/store/migrations/mysql/`。

### 4.1 `admin_user`（升级为多管理员表，移除 CHECK 约束）

> 迁移文件：`0049_admin_user_multi.sql`（SQLite + MySQL 各一份）

```sql
-- SQLite
CREATE TABLE IF NOT EXISTS admin_user (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT    NOT NULL UNIQUE,
    password_hash TEXT    NOT NULL,
    created_at    TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL
);
-- 迁移要点:
-- 1. 新表迁移不能重建;必须 ALTER TABLE 移除 CHECK 约束。
--    SQLite 实际做法:用 PRAGMA writable_schema 移除约束,或新建表 0049_admin_user_multi.sql 整张重定义。
--    MySQL 做法:DROP CHECK + ALTER COLUMN。
-- 2. 已有 id=1 行保留;id 改为自增 INTEGER PRIMARY KEY。
```

```sql
-- MySQL
CREATE TABLE IF NOT EXISTS admin_user (
    id            INT AUTO_INCREMENT PRIMARY KEY,
    username      VARCHAR(255) NOT NULL UNIQUE,
    password_hash VARCHAR(255) NOT NULL,
    created_at    VARCHAR(32)  NOT NULL,
    updated_at    VARCHAR(32)  NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
-- 迁移要点:ALTER TABLE admin_user DROP CHECK (id = 1), MODIFY COLUMN id INT AUTO_INCREMENT;
```

### 4.2 `users`（新增，存普通用户 + 管理员个人行）

> 迁移文件：`0049_users.sql`

```sql
-- SQLite
CREATE TABLE users (
    id            TEXT PRIMARY KEY,  -- uuid v4
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,    -- argon2id,与 admin_user 同参数;admin 行与 admin_user 同步
    role          TEXT NOT NULL DEFAULT 'user',  -- 'user' | 'admin'
    enabled       INTEGER NOT NULL DEFAULT 1,
    description   TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    last_login_at TEXT
);
CREATE INDEX idx_users_username ON users(username);
CREATE INDEX idx_users_enabled  ON users(enabled);
```

```sql
-- MySQL
CREATE TABLE users (
    id            VARCHAR(64)  PRIMARY KEY,
    username      VARCHAR(255) NOT NULL UNIQUE,
    password_hash VARCHAR(255) NOT NULL,
    role          VARCHAR(16)  NOT NULL DEFAULT 'user',
    enabled       TINYINT      NOT NULL DEFAULT 1,
    description   TEXT         NOT NULL,
    created_at    VARCHAR(32)  NOT NULL,
    updated_at    VARCHAR(32)  NOT NULL,
    last_login_at VARCHAR(32)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE INDEX idx_users_username ON users(username);
CREATE INDEX idx_users_enabled  ON users(enabled);
```

### 4.3 `build_envs`（新增）

> 迁移文件：`0049_build_envs.sql`

```sql
CREATE TABLE build_envs (
    id                 TEXT PRIMARY KEY,                  -- uuid v4
    language           TEXT NOT NULL,
    version            TEXT NOT NULL,
    display_name       TEXT NOT NULL,
    description        TEXT NOT NULL DEFAULT '',

    source_type        TEXT NOT NULL DEFAULT 'official',  -- 'official' | 'custom'
    image              TEXT NOT NULL,                     -- 原样使用,系统不处理
    credential_id      TEXT NOT NULL DEFAULT '',          -- 引用 credentials.id

    image_check_status TEXT NOT NULL DEFAULT 'unchecked', -- 覆盖式写入
    image_check_error  TEXT NOT NULL DEFAULT '',
    image_checked_at   TEXT,

    enabled            INTEGER NOT NULL DEFAULT 1,
    sort_order         INTEGER NOT NULL DEFAULT 0,
    created_by         TEXT NOT NULL,                     -- users.id
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,

    UNIQUE(language, version)
);
CREATE INDEX idx_build_envs_language     ON build_envs(language);
CREATE INDEX idx_build_envs_enabled      ON build_envs(enabled);
CREATE INDEX idx_build_envs_check_status ON build_envs(image_check_status);
```

### 4.4 `config_profiles`（新增）

> 迁移文件：`0049_config_profiles.sql`

```sql
CREATE TABLE config_profiles (
    id           TEXT PRIMARY KEY,                         -- uuid v4
    language     TEXT NOT NULL,
    config_type  TEXT NOT NULL,                            -- 'maven' | 'npm' | 'pip' | 'goproxy' | 'env'
    name         TEXT NOT NULL,
    target_path  TEXT NOT NULL,                            -- 容器内目标路径
    file_path    TEXT NOT NULL,                            -- 宿主机 ${DATA_DIR}/config_profiles/<id>/<filename>
    content      TEXT NOT NULL,                            -- 文件内容(双存储,审计/导入导出用)
    is_default   INTEGER NOT NULL DEFAULT 0,
    is_builtin   INTEGER NOT NULL DEFAULT 0,
    description  TEXT NOT NULL DEFAULT '',
    enabled      INTEGER NOT NULL DEFAULT 1,
    created_by   TEXT NOT NULL,                            -- users.id
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,

    UNIQUE(language, config_type, name)
);
CREATE INDEX idx_config_profiles_language ON config_profiles(language);
CREATE INDEX idx_config_profiles_default  ON config_profiles(language, is_default);
CREATE INDEX idx_config_profiles_builtin  ON config_profiles(is_builtin);
```

### 4.5 `credentials`（追加扩展字段 + CHECK 约束）

> 迁移文件：`0049_credentials_owner.sql`

```sql
-- SQLite
ALTER TABLE credentials ADD COLUMN description  TEXT NOT NULL DEFAULT '';
ALTER TABLE credentials ADD COLUMN enabled      INTEGER NOT NULL DEFAULT 1;
ALTER TABLE credentials ADD COLUMN disabled_by  TEXT NOT NULL DEFAULT '';
ALTER TABLE credentials ADD COLUMN disabled_at  TEXT;
ALTER TABLE credentials ADD COLUMN owner_id     TEXT NOT NULL DEFAULT '';
ALTER TABLE credentials ADD COLUMN created_by   TEXT NOT NULL DEFAULT '';

-- CHECK 约束(SQLite 不支持后期加 CHECK;新建一张表做迁移更稳;本期写在一份独立的 0049_credentials_owner.sql,
-- 由迁移团队按需拆为"建新表 + 数据迁移 + 删旧表 + 改新表名"三步)。
-- 终态:
--   UNIQUE(scope, owner_id, name)
--   CHECK ((scope = 'global' AND owner_id = '') OR (scope = 'personal' AND owner_id <> ''))

CREATE INDEX idx_credentials_owner ON credentials(owner_id);
```

**迁移要点（重要）**：

- SQLite 不支持 `ALTER TABLE ... ADD CONSTRAINT`。必须在迁移文件中**新建一张临时表 + 拷贝数据 + 改名**。
- MySQL 直接 `ALTER TABLE credentials ADD CONSTRAINT CHECK (...)`。
- 终态 `UNIQUE(scope, owner_id, name)` 与 `CHECK` 共同保证：
  - global 凭据：`owner_id=''` + `scope='global'`，组内 name 唯一。
  - personal 凭据：`owner_id=<UUID>` + `scope='personal'`，同一 owner 下 name 唯一。
  - global 与 personal 之间允许同名（scope 不同）。

### 4.6 不变的表（v6.2 范围外）

- `admin_user`（升级在 4.1） / `users`（新增在 4.2） / `build_envs`（4.3） / `config_profiles`（4.4） / `credentials`（4.5 扩展）
- `sessions` / `audit_log` / `projects` / `pipelines` / `pipeline_configs` / `pipeline_settings` 等本期不修改。
- `internal/environments` 包对应的部署环境聚合**完全不变**（4.7 边界声明）。

---

## 五、后端设计

### 5.1 领域包结构（v6.2 修订）

```
internal/
├── users/                          ← 新建:普通用户 + 管理员个人行 (NEW)
│   ├── model.go                    -- User 实体 + Repo 接口
│   ├── service.go                  -- CRUD、邀请注册、密码哈希、SyncAdminRow
│   └── consts.go                   -- 用户管理常量
├── buildenv/                        ← 新建:构建环境管理 (NEW)
│   ├── model.go                    -- BuildEnv 实体 + Repo 接口
│   ├── service.go                  -- CRUD、启用/禁用校验(P0 #4 SetEnabled 三态)
│   ├── checker.go                  -- 镜像检查、手动拉取、自动检查(60s/10/覆盖)
│   ├── resolver.go                 -- language+version → image(env_resolver)
│   └── checker_test.go
├── configprofile/                  ← 新建:配置资源管理 (NEW)
│   ├── model.go                    -- ConfigProfile 实体 + Repo 接口
│   ├── service.go                  -- CRUD、默认配置管理、文件上传(原子写 P0 #3)
│   └── files.go                    -- 临时文件 + rename + fsync + 事务封装
├── vault/                          ← 扩展:加 RBAC 层 (MODIFY)
│   ├── vault.go                    -- 加 Actor 注入;List/Get/Update/Delete 按 Actor 过滤
│   ├── crypto.go                   -- 不变
│   ├── masker.go                   -- 不变
│   └── rbac.go                     -- NEW: ListFilter{Scope, OwnerID, IncludeGlobal, IncludePersonal}
├── deployenv/                      ← 改名:从 environments 迁移过来 (RENAME)
│   ├── service.go                  -- 原 environments/environments.go
│   ├── store.go                    -- 原 environments/store.go
│   └── service_test.go             -- 原测试改名
├── environments/                   ← 保留为薄兼容层 (DEPRECATED)
│   └── alias.go                    -- 仅 type alias = X X type aliasService = deployenv.Service
├── auth/                           ← 扩展:多管理员 + 普通用户登录 (MODIFY)
│   ├── service.go                  -- Bootstrap/ChangePassword 加多管理员分支;Login 支持 users 表
│   ├── session.go                  -- Session 加 UserID/Role/IsAdmin 字段
│   ├── rbac.go                     -- NEW: RequireAdmin / RequireUser / RequireAuth (在 router 层用)
│   └── password.go                 -- 不变
├── audit/                          ← 扩展:加新 action 常量 (MODIFY)
│   └── audit.go                                   -- 加 ActionBuildEnvCreate/Update/Delete/Check/Pull,
│                                                    ActionConfigProfileCreate/Update/Delete/Upload,
│                                                    ActionUserInvite/Create/Disable,
│                                                    ActionCredentialDisable
├── httpapi/                        ← 修改:加新 handler + RequireAdmin 中间件 (MODIFY)
│   ├── router.go                   -- 在 requireAuth 之后注册新路由组
│   ├── middleware.go               -- NEW: RequireAdmin / RequireUser / RequireCSRF 抽出(原内联在 router.go)
│   ├── buildenv.go                 -- NEW: 构建环境 CRUD + check/pull 端点
│   ├── configprofile.go            -- NEW: 配置资源 CRUD + multipart 上传
│   ├── users.go                    -- NEW: 用户邀请、列表、禁用
│   ├── credentials.go              -- MODIFY: list/filter 按 scope+owner;disable 端点
│   ├── pipelines.go                -- MODIFY: 拒绝 body.yaml 字段
│   └── audit.go                    -- MODIFY: auditActor 从硬编码改 session.UserID
├── pipeline/                       ← 修改:ValidateJobEnvironment 接入 env_resolver (MODIFY)
│   └── validate.go
├── build/                           ← 修改:接入 env_resolver (MODIFY)
│   ├── dag_stage_exec.go           -- runBuildImageJob 改用 buildenv.ResolveImage
│   ├── script_steps.go             -- runScriptStep 的 image 来源改 env_resolver
│   ├── stage_post.go               -- PostStep 改 env_resolver
│   ├── services.go                 -- ServiceSpec.BuildEnvID 替代 Image(老 Image 字段保留为 admin 逃生通道)
│   └── builder.go                  -- toolchain 构建改用 buildenv.ResolveImage;toolchainImage() 删除
└── config/                         ← 扩展:新 env vars (MODIFY)
    └── config.go                                  -- 加 PIPEWRIGHT_DATA_DIR / PIPEWRIGHT_CHECK_TIMEOUT_SECONDS /
                                                       PIPEWRIGHT_AUTO_CHECK_ON_START / PIPEWRIGHT_CHECK_CONCURRENCY /
                                                       PIPEWRIGHT_ALLOW_UNCHECKED_ENABLE / PIPEWRIGHT_CONFIG_UPLOAD_MAX_SIZE
```

### 5.2 API 端点（v6.2 修订：路径以现有 `internal/httpapi/router.go` 为准）

**认证与会话（扩展）**：

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/auth/login` | 扩展：先查 `admin_user` 表（兼容旧部署），再查 `users` 表；返回 `session.role` |
| POST | `/api/auth/logout` | 不变 |

**管理员端点（`requireAdmin` 中间件保护）**：

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/admin/users/invitations` | 创建普通用户邀请（一次性 token + URL） |
| GET | `/api/admin/users` | 列出所有 users |
| GET | `/api/admin/users/:id` | 获取单个用户信息 |
| DELETE | `/api/admin/users/:id` | 禁用普通用户（`users.enabled=0`，不物理删除） |
| GET | `/api/admin/build-envs` | 列出全部构建环境 |
| POST | `/api/admin/build-envs` | 创建构建环境 |
| GET | `/api/admin/build-envs/:id` | 获取单个 |
| PUT | `/api/admin/build-envs/:id` | 更新 |
| DELETE | `/api/admin/build-envs/:id` | 删除 |
| POST | `/api/admin/build-envs/:id/toggle` | 启用/禁用（P0 #4 三态校验） |
| POST | `/api/admin/build-envs/:id/check` | 手动检查镜像 |
| POST | `/api/admin/build-envs/:id/pull` | 手动拉取镜像 |
| POST | `/api/admin/build-envs/check-all` | 一键检查全部 |
| GET | `/api/admin/config-profiles` | 列出全部 |
| POST | `/api/admin/config-profiles` | 创建配置资源 |
| GET | `/api/admin/config-profiles/:id` | 获取单个 |
| PUT | `/api/admin/config-profiles/:id` | 更新（`is_builtin=1` 时仅改 description/enabled） |
| DELETE | `/api/admin/config-profiles/:id` | 删除（内置不可删） |
| POST | `/api/admin/config-profiles/upload` | **multipart 上传**（`multipart/form-data; filename + content`） |
| GET | `/api/admin/credentials` | 列出 global 凭据 + personal 凭据元数据 |
| POST | `/api/admin/credentials` | 创建 global 凭据 |
| PUT | `/api/admin/credentials/:id` | 更新 global 凭据 |
| DELETE | `/api/admin/credentials/:id` | 删除 global 凭据 |
| POST | `/api/admin/credentials/:id/disable` | 禁用 personal 凭据（仅元数据操作） |
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

**YAML 端点（保留与禁用）**：

| 方法 | 路径 | v6.2 动作 | 说明 |
|---|---|---|---|
| POST | `/api/projects/{id}/pipeline/import` | ✅ 保留 | 导入 YAML → 解析 → 预览（`save:true` 子句保留） |
| GET | `/api/projects/{id}/pipeline` | ✅ 保留 | 返回 `{spec, yaml, stages}`（yaml 字段只读，不接受入参） |
| PUT | `/api/projects/{id}/pipeline` | ✅ 保留 + 拒绝 `body.yaml` | 如 body 含 `yaml` 字段 → 400 `yaml_direct_edit_disabled` |
| GET | `/api/projects/{id}/pac/preview` | ✅ 保留 | pacloader 预览 |
| `PATCH /api/pipelines/:id/yaml` | 不存在 | 无操作 | — |

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
    credRepo CredentialRepo  // 用于 ManualPull 的 docker login
    bin      string           // docker / nerdctl / podman
    timeout  time.Duration
    sem      chan struct{}    // 并发上限(可与 ManualPull 独立 channel)
    pullSem  chan struct{}    // ManualPull 独立并发上限(默认 2;pull 比 inspect 重得多)
    errgrp   *errgroup.Group  // 启动期单一 errgroup,避免重复启动
}

func NewChecker(repo BuildEnvRepo, credRepo CredentialRepo, bin string,
    concurrency int, timeout time.Duration) *Checker {
    return &Checker{
        repo: repo, credRepo: credRepo, bin: bin,
        timeout: timeout,
        sem:     make(chan struct{}, concurrency),
        pullSem: make(chan struct{}, 2),
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
//   - 使用独立的 pullSem(默认 2 并发)
//   - pull 前按需 login,pull 完立即 logout
//   - 结果直接覆盖
func (c *Checker) ManualPull(ctx context.Context, envID string) (*PullResult, error) {
    env, err := c.repo.GetByID(ctx, envID)
    if err != nil {
        return nil, err
    }
    select {
    case c.pullSem <- struct{}{}:
    case <-ctx.Done():
        return nil, ctx.Err()
    }
    defer func() { <-c.pullSem }()

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
        defer c.dockerLogout(ctx, env.Image)
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

// StartAutoCheck 启动后遍历 build_envs,每个 env 异步检查一次。
// 使用 sync.Once 防止服务重启时多次触发。
type Checker struct {
    // ...
    once sync.Once
}

func (c *Checker) StartAutoCheck(ctx context.Context) {
    c.once.Do(func() {
        go func() {
            envs, err := c.repo.List(ctx, BuildEnvFilter{IncludeDisabled: true})
            if err != nil {
                log.Errorf("加载构建环境列表失败: %v", err)
                return
            }
            for _, env := range envs {
                env := env
                go func() {
                    c.Check(ctx, env)
                }()
            }
        }()
    })
}
```

### 5.4 启用/禁用强制逻辑（P0 #4 修订）

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

// SetEnabled 三态校验(P0 #4):
//   unavailable → 拒
//   unchecked   → 默认拒(PIPEWRIGHT_ALLOW_UNCHECKED_ENABLE=false);允许时(PIPEWRIGHT_ALLOW_UNCHECKED_ENABLE=true)放行
//   available   → 允许
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
    }
    return s.repo.SetEnabled(ctx, id, true)
}

func allowUncheckedEnable() bool {
    v := strings.ToLower(strings.TrimSpace(os.Getenv("PIPEWRIGHT_ALLOW_UNCHECKED_ENABLE")))
    return v == "1" || v == "true" || v == "yes"
}
```

### 5.5 环境解析器（env_resolver）

```go
// internal/buildenv/resolver.go

// ResolveImage 据 language+version 查 build_envs 返回真实 image。
//   - 找不到       → ENV_NOT_FOUND
//   - 未启用       → ENV_DISABLED
//   - 镜像不可用   → IMAGE_UNAVAILABLE
//   - 找到         → 原样返回 env.Image,系统不拼接
func ResolveImage(ctx context context.Context, lang, ver string, repo BuildEnvRepo) (string, error) {
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

// ResolveByID 类似 ResolveImage,但按 build_envs.id 查(ServiceSpec.BuildEnvID 走这条)。
func ResolveByID(ctx context context.Context, id string, repo BuildEnvRepo) (string, error) {
    env, err := repo.GetByID(ctx, id)
    if err != nil {
        return "", err
    }
    if env == nil {
        return "", &ValidationError{Code: "ENV_NOT_FOUND", Message: "构建环境不存在"}
    }
    if !env.Enabled {
        return "", &ValidationError{Code: "ENV_DISABLED", Message: "构建环境已禁用"}
    }
    if env.ImageCheckStatus == "unavailable" {
        return "", &ValidationError{Code: "IMAGE_UNAVAILABLE", Message: "镜像不可拉取"}
    }
    return env.Image, nil
}
```

### 5.6 流水线校验

```go
// internal/pipeline/validate.go  (扩展;沿用现有"INSTALLATION_DTO + Issues"返回结构)

// ValidateBuildEnvironment 校验 pipeline 中的 toolchain / image 字段是否对应有效构建环境。
// 既有 validate.go 的 Image 字段为自由字符串;本期改"image 来源"为 buildenv_id / language+version 二选一。
//
// 优先级:
//   1. admin 逃生通道:job.Config["image"] 非空 + role=admin → 允许(image 原样保留)
//   2. job.Config["buildEnvID"] 非空 → resolve by ID
//   3. job.Config["language"]+["job.Config["version"] 非空 → resolve by language+version
//   4. 都空 → LANGUAGE_REQUIRED
func ValidateBuildEnvironment(
    ctx context context.Context,
    jb *Job,
    userRole string,
    envRepo buildenv.BuildEnvRepo,
) (image string, issues []Issue) {
    imageRaw := strings.TrimSpace(cfgString(jb.Config, "image"))
    envID := strings.TrimSpace(cfgString(jb.Config, "buildEnvID"))
    lang := strings.TrimSpace(cfgString(jb.Config, "language"))
    ver := strings.TrimSpace(cfgString(jb.Config, "version"))

    // 1. admin 逃生通道
    if imageRaw != "" && userRole == "admin" {
        return imageRaw, nil
    }

    // 普通用户不得指定 image
    if imageRaw != "" {
        return "", []Issue{{
            Code:    "IMAGE_NOT_ALLOWED",
            Severity: "error",
            Message: "普通用户不可直接指定 image,请使用 language+version 或 buildEnvID 选择预置环境",
        }}
    }

    // 2. buildEnvID
    if envID != "" {
        img, err := buildenv.ResolveByID(ctx, envID, envRepo)
        if err != nil {
            return "", []Issue{{Code: "BUILD_ENV", Severity: "error", Message: err.Error()}}
        }
        return img, nil
    }

    // 3. language + version
    if lang == "" {
        return "", []Issue{{Code: "LANGUAGE_REQUIRED", Severity: "error", Message: "请选择构建语言"}}
    }
    if ver == "" {
        return "", []Issue{{Code: "VERSION_REQUIRED", Severity: "error", Message: "请选择构建版本"}}
    }
    img, err := buildenv.ResolveImage(ctx, lang, ver, envRepo)
    if err != nil {
        return "", []Issue{{Code: "BUILD_ENV", Severity: "error", Message: err.Error()}}
    }
    return img, nil
}
```

### 5.7 凭据 RBAC（v6.2 修订：在现有 vault 包内扩展）

```go
// internal/vault/rbac.go (NEW)

package vault

// Actor 表示当前操作者身份(vault 包按此过滤)。
// Actor 为 nil 表示"管理员(系统内部操作)",可越权访问所有凭据。
type Actor struct {
    UserID    string  // users.id
    Role      string  // "admin" | "user"
}

// ListFilter 按 scope+owner 过滤凭据。
type ListFilter struct {
    Scope            string  // "global" | "personal" | ""
    OwnerID          string  // users.id
    IncludeGlobal    bool
    IncludePersonal  bool
    IncludeDisabled  bool
}

// effectiveFilter 据 Actor 推导出安全 ListFilter:
//   - role=admin + Actor 非 nil → 允许看所有(global+所有 personal 元数据,不含明文)
//   - role=user  → 只能看自己的 personal
//   - Actor 为 nil → 等价 admin,可看所有
func effectiveFilter(actor *Actor) ListFilter {
    if actor == nil || actor.Role == "admin" {
        return ListFilter{IncludeGlobal: true, IncludePersonal: true}
    }
    return ListFilter{Scope: "personal", OwnerID: actor.UserID}
}
```

```go
// internal/vault/vault.go  (扩展)

// List 接受 Actor;不传则视为管理员(legacy 路径行为不变)。
func (s *service) List(actor *Actor) ([]Credential, error) {
    return s.listWithFilter(actor, effectiveFilter(actor))
}

func (s *service) listWithFilter(actor *Actor, f ListFilter) ([]Credential, error) {
    if !s.configured() {
        return nil, ErrVaultUnconfigured
    }
    // 组装 WHERE 子句与参数;有 actor 但 actor.Role != "admin" → 强校验:即使显式
    // 带 IncludePersonal=true 也只返回自己的;admin / nil Actor 按 f 过滤。
    whereParts := []string{"1=1"}
    args := []any{}
    if actor != nil && actor.Role != "admin" {
        whereParts = []string{"scope = ? AND owner_id = ?"}
        args = []any{"personal", actor.UserID}
    } else {
        if !f.IncludeGlobal && !f.IncludePersonal {
            return nil, fmt.Errorf("vault: list filter rejected")
        }
        if f.IncludeGlobal && !f.IncludePersonal {
            whereParts = []string{"scope = 'global'"}
        } else if !f.IncludeGlobal && f.IncludePersonal {
            if f.OwnerID != "" {
                whereParts = []string{"scope = ? AND owner_id = ?"}
                args = []any{"personal", f.OwnerID}
            } else {
                whereParts = []string{"scope = 'personal'"}
            }
        }
        // IncludeGlobal + IncludePersonal:不加额外 where
    }
    if !f.IncludeDisabled {
        whereParts = append(whereParts, "enabled = 1")
    }
    sqlText := "SELECT id, name, type, scope, owner_id, username, masked_value, " +
        "last_used_at, description, created_at FROM credentials WHERE " +
        strings.Join(whereParts, " AND ") +
        " ORDER BY created_at DESC, id"
    rows, err := s.db.Query(sqlText, args...)
    if err != nil {
        return nil, fmt.Errorf("vault: list credentials: %w", err)
    }
    defer func() { _ = rows.Close() }()
    out := make([]Credential, 0)
    for rows.Next() {
        c, err := scanCredentialRBAC(rows)
        if err != nil {
            return nil, err
        }
        out = append(out, *c)
    }
    return out, rows.Err()
}

// Get/GetGitAuth/Reveal 接受 Actor;非本人 personal → ErrAccessDenied。
func (s *service) Get(actor *Actor, id string) (string, error) {
    cred, err := s.getView(actor, id)
    if err != nil {
        return "", err
    }
    // ...解密;记 last_used_at
}

// Create 接受 Actor + OwnerID 推导:
//   - scope='global' && actor.Role != "admin"  → ErrForbidden
//   - scope='personal' → owner_id = actor.UserID(忽略入参)
func (s *service) Create(actor *Actor, in CreateInput) (*Credential, error) {
    // ...
}

// ErrAccessDenied 业务层越权(个人凭据他人访问)。
var ErrAccessDenied = errors.New("vault: access denied")
```

### 5.8 多管理员 bootstrap 与用户同步

```go
// internal/users/consts.go  (NEW)
package users

// AdminUserIDField is reserved for code that needs to refer to the
// canonical admin-user's `users.id` (UUID v4). At Bootstrap time, this
// is assigned to the first admin created by PIPEWRIGHT_ADMIN_PASSWORD.
// Subsequent admin rows get fresh UUIDs; this constant exists for
// invariant tests and discovery.
const BootstrapAdminRegularUserID = "00000000-0000-0000-0000-000000000001"

// internal/users/service.go  (NEW)

// BootstrapAdmin 同步 admin_user 与 users 两张表:
//   - admin_user.id=1 行已存在(由 auth.Bootstrap 创建)
//   - 在 users 表创建 role='admin' 的一行,id=BootstrapAdminRegularUserID
//   - 用户名/密码哈希同步
func (s *Service) BootstrapAdmin(adminUserID int64, passwordHash string) error {
    var adminUsername string
    if err := s.db.QueryRow(`SELECT username FROM admin_user WHERE id = ?`, adminUserID).Scan(&adminUsername); err != nil {
        return fmt.Errorf("users: bootstrap admin lookup: %w", err)
    }
    now := time.Now().UTC().Format(time.RFC3339)
    _, err := s.db.Exec(`
        INSERT INTO users (id, username, password_hash, role, enabled, created_at, updated_at)
        VALUES (?, ?, ?, 'admin', 1, ?, ?)
        ON CONFLICT(username) DO UPDATE SET
            password_hash = excluded.password_hash,
            role          = 'admin',
            enabled       = 1,
            updated_at    = excluded.updated_at
    `, BootstrapAdminRegularUserID, adminUsername, passwordHash, now, now)
    return err
}

// SyncAdminPasswordChange 在 auth.ChangePassword 成功后调用,保持 users.role='admin' 行同步。
func (s *Service) SyncAdminPasswordChange(username, newPasswordHash string) error {
    now := time.Now().UTC().Format(time.RFC3339)
    _, err := s.db.Exec(
        `UPDATE users SET password_hash = ?, updated_at = ? WHERE username = ? AND role = 'admin'`,
        newPasswordHash, now, username,
    )
    return err
}

// Invariant 测试 (internal/users/consts_test.go):
//   - BootstrapAdminRegularUserID 永不变更(变更会破坏所有已签发 admin personal 凭据)
```

### 5.9 中间件扩展

```go
// internal/httpapi/middleware.go (从 router.go 抽出)

// RequireAdmin 在 requireAuth 之后运行;从 session 取 role,非 admin → 403。
func RequireAdmin(sess *auth.Session) bool {
    return sess != nil && sess.Role == "admin"
}

// 在 router.go 受保护组内加 Use 层:
r.Route("/api", func(ar chi.Router) {
    ar.Use(func(next http.Handler) http.Handler { return requireAuth(svc, next) })
    ar.Use(func(next http.Handler) http.Handler { return requireCSRF(next) })

    // 普通用户可访问的子组
    ar.Route("/api/", ...)  // 既有路径
    ar.Route("/api/admin", func(adminR chi.Router) {
        adminR.Use(requireAdmin)  // NEW: 仅 admin 可进
        adminR.Get("/build-envs", ...)
        // ...
    })
})
```

### 5.10 build 接入 env_resolver（v6.2 修订：依据现有 Job.Config KV 结构）

> Job.Config 是 `map[string]any`,字段名沿用 v6.2:
>
> - `image` （字符串，admin 逃生通道）
> - `buildEnvID` （字符串，引用 `build_envs.id`）
> - `language` / `version` （字符串，引用 `build_envs(language, version)`）
> - `commands` / `commandTemplate` / `workDir` / `timeoutSeconds` / `retries` / `cpu` / `memory`（既有）

```go
// internal/build/dag_stage_exec.go  runBuildImageJob (现有 714-719 行的改造)

func (b *Builder) runBuildImageJob(ctx context context.Context, sink run.StepSink,
    rep dagrun.StageReporter, jb pipeline.Job, stageName string,
    proj *project.Project, settings *pipeline.Settings, envName, workspace, commitTag string,
    hasPushJob bool) error {

    // 1. 决定 image 来源(env_resolver 或 admin image 直接)
    sess := auth.SessionFromContext(ctx)  // NEW:从 ctx 取 role
    image, errs := pipeline.ValidateBuildEnvironment(ctx, &jb, sess.Role, b.envRepo)
    if len(errs) > 0 {
        for _, e := range errs {
            _ = rep.Log(ctx, streamStderr, fmt.Sprintf("构建节点「%s」校验失败:%s", jb.Name, e.Message))
        }
        return ErrBuildFailed
    }

    // 2. 构造 BuildConfig(注意:Toolchain 字段不再使用;image 直接来自 env_resolver)
    cfg := pipeline.BuildConfig{
        Model:          cfgString(jb.Config, "buildModel"),
        DockerfilePath: cfgString(jb.Config, "dockerfilePath"),
        Context:        cfgString(jb.Config, "context"),
        Toolchain:      pipeline.Toolchain{Language: cfgString(jb.Config, "language"), Version: cfgString(jb.Config, "version")},
        ArtifactType:   cfgString(jb.Config, "artifactType"),
    }
    if cfg.ArtifactType == "" {
        cfg.ArtifactType = pipeline.ArtifactImage
    }
    if cfg.Model == "" {
        cfg.Model = pipeline.BuildModelDockerfile
    }
    // 将 image 注入到 cfg,让 Builder.build 用
    jb.Config["image"] = image
    // ...

    // 3. 调 Builder.build(不变)
    localTag, art, berr := b.build(ctx, sink, 0, proj, cfg, workspace, commitTag)
    // ...
}
```

```go
// internal/build/script_steps.go  runScriptStep (改造)

func (b *Builder) runScriptStep(ctx context context.Context, step pipeline.PipelineStep, workspace string) (int, error) {
    sess := auth.SessionFromContext(ctx)
    image := step.Image
    if sess == nil || sess.Role != "admin" {
        // 普通用户:image 必须由 env_resolver 解析过(settings save 时已解析并写入 step.Image)
        // 此处校验 step.Image 非空即可
        if image == "" {
            return 1, errors.New("step.image 为空(普通用户须选择预置环境)")
        }
    }
    // ...既有逻辑
}
```

```go
// internal/build/builder.go  toolchainImage 删除(BuildEnv 管理下沉到 buildenv 包)

- 删除 toolchainImage(tc) 函数 (builder.go:635-646)
- 删除对 toolchainImage 的调用 (builder.go:430)
- 改用 buildenv.ResolveImage(cfg.Toolchain.Language, cfg.Toolchain.Version)
```

### 5.11 YAML 端点裁剪（v6.2 修订）

```go
// internal/httpapi/pipelines.go  makeSavePipelineHandler (现有 204-227 行改造)

func makeSavePipelineHandler(pl pipeline.Service) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        var req struct {
            // 既有字段
            Stages  []map[string]any `json:"stages"`
            // 拒绝字段:如下 yamlRaw 非空 → 400
            YAML    string `json:"yaml"`
        }
        // ...
        if req.YAML != "" {
            writeError(w, http.StatusBadRequest, "yaml_direct_edit_disabled",
                "YAML 直接编辑已禁用,请用结构化 stages 入参或 POST /api/projects/{id}/pipeline/import")
            return
        }
        // ...既有保存逻辑
    }
}
```

### 5.12 multipart 上传（v6.2 新增）

> `internal/httpapi/router.go` 全局没设 MaxBytesReader 与 MaxMultipartMemory。本期在 `internal/httpapi/configprofile.go` 内新增上传端点。

```go
// internal/httpapi/configprofile.go  (NEW)

// POST /api/admin/config-profiles/upload
//   multipart/form-data:
//     - "lang"  (表单字段)
//     - "configType" (表单字段)
//     - "name" (表单字段)
//     - "targetPath" (表单字段)
//     - "file" (上传文件,大小由 PIPEWRIGHT_CONFIG_UPLOAD_MAX_SIZE 限制,默认 1MB)
func makeUploadConfigProfileHandler(svc configprofile.Service) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        maxBytes := configUploadMaxBytes()  // 默认 1MB
        r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
        if err := r.ParseMultipartForm(maxBytes); err != nil {
            writeError(w, http.StatusBadRequest, "upload_too_large",
                fmt.Sprintf("上传文件超过 %d 字节", maxBytes))
            return
        }

        file, header, err := r.FormFile("file")
        if err != nil {
            writeError(w, http.StatusBadRequest, "missing_file", "缺少 file 字段")
            return
        }
        defer file.Close()

        // 扩展名白名单(防滥用)
        ext := strings.ToLower(filepath.Ext(header.Filename))
        if !configProfileExtAllowed(ext) {
            writeError(w, http.StatusBadRequest, "invalid_extension",
                "扩展名不在白名单内")
            return
        }

        // 取其他字段
        lang := strings.TrimSpace(r.FormValue("lang"))
        configType := strings.TrimSpace(r.FormValue("configType"))
        name := strings.TrimSpace(r.FormValue("name"))
        targetPath := strings.TrimSpace(r.FormValue("targetPath"))
        // ...校验入参 → svc.Upload(...)
        // 委托给 service.Upload 实现原子写(§3.3 写路径)
    }
}

func configUploadMaxBytes() int64 {
    v := strings.TrimSpace(os.Getenv("PIPEWRIGHT_CONFIG_UPLOAD_MAX_SIZE"))
    if v == "" {
        return 1 << 20  // 1MB
    }
    n, err := strconv.ParseInt(v, 10, 64)
    if err != nil || n <= 0 {
        return 1 << 20
    }
    return n
}

var allowedConfigExts = map[string]bool{
    ".xml": true, ".conf": true, ".npmrc": true, ".ini": true,
    ".env": true, ".toml": true, ".yaml": true, ".yml": true,
}

func configProfileExtAllowed(ext string) bool { return allowedConfigExts[ext] }
```

### 5.13 audit 扩展

```go
// internal/audit/audit.go  (MODIFY)

const (
    // 既有 (32-35):
    ActionCredentialCreate   = "credential_create"
    ActionCredentialUpdate   = "credential_update"
    ActionCredentialDelete   = "credential_delete"
    ActionCredentialReveal   = "credential_reveal"

    // 新增:
    ActionCredentialDisable  = "credential_disable"  // admin 禁用 personal
    ActionBuildEnvCreate     = "build_env_create"
    ActionBuildEnvUpdate     = "build_env_update"
    ActionBuildEnvDelete     = "build_env_delete"
    ActionBuildEnvToggle     = "build_env_toggle"
    ActionBuildEnvCheck      = "build_env_check"
    ActionBuildEnvPull       = "build_env_pull"
    ActionConfigProfileCreate  = "config_profile_create"
    ActionConfigProfileUpdate  = "config_profile_update"
    ActionConfigProfileDelete  = "config_profile_delete"
    ActionConfigProfileUpload  = "config_profile_upload"
    ActionUserInvite         = "user_invite"
    ActionUserCreate         = "user_create"
    ActionUserDisable        = "user_disable"
    ActionYAMLEditRejected   = "yaml_edit_rejected"  // body.yaml 命中拦截
)
```

```go
// internal/httpapi/audit.go  (MODIFY)

- 删除硬编码常量 auditActor = "admin"
- 改为:从 session 取 userID + role;role=admin 的写操作 actor = "admin:<username>";普通用户 = "user:<username>"
- auditActor() 函数从 session.SessionMeta 提取
```

### 5.14 远程构建（不修改）

`internal/build/remote.go` 与 `remote_stage_exec.go` **不在本计划修改**。构建始终在宿主机执行；远程构建机的镜像可用性超出本计划范围。

---

## 六、前端设计

### 6.1 路由结构（v6.2 修订：路径以现有 `/api/admin/*` 命名风格）

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
│  │ │ 21   │ Java 21      │ registry.compa.. │ ○ 未检查│ 禁用 │编││  │
│  │ └──────┴──────────────┴──────────────────┴────────┴──────┴──┘│  │
│  └──────────────────────────────────────────────────────────────┘  │
│                                                                      │
│  Java 17 不可用原因:                                                  │
│  manifest inspect 失败: unauthorized: authentication required         │
│  [重新检查]  [手动拉取]  [更换地址]                                    │
└─────────────────────────────────────────────────────────────────────┘
```

（P0 #4 修订：Java 21 `unchecked` 状态下，启用列显示"禁用"且不可点击；需先检查/拉取。）

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
│  启用          [●]  (镜像不可用或未检查时不可勾选)           │
├─────────────────────────────────────────────────────────────┤
│                                          [取消]  [保存]      │
└─────────────────────────────────────────────────────────────┘
```

（P0 #4 修订：底部"启用"在 unchecked / unavailable 时禁用 + tooltip 提示。）

### 6.4 配置资源管理页

```
┌─────────────────────────────────────────────────────────────┐
│  配置资源管理                              [+ 新增配置资源]   │
├─────────────────────────────────────────────────────────────┤
│  语言筛选: [全部 ▼]    类型: [全部 ▼]                        │
├─────────────────────────────────────────────────────────────┤
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

新增/编辑配置资源（multipart 上传）：

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
│  │        支持: .xml, .conf, .npmrc, .ini, .env, .toml, .yaml, .yml │
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
│                                                              │
│  ── 个人凭据(元数据) ──────────────────────────────          │
│  ┌────────────────────┬──────────────┬──────────┬──────────┐│
│  │ 名称               │ 所有者        │ 创建时间    │ 操作     ││
│  ├────────────────────┼──────────────┼──────────┼──────────┤│
│  │ my-git-token       │ admin        │ 2天前     │禁用       ││
│  │ bob-git-token      │ bob          │ 1周前     │禁用       ││
│  └────────────────────┴──────────────┴──────────┴──────────┘│
│  提示: 仅显示元数据;明文仅本人可见                             │
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
│  ┌──────────────────────────────────────────┐ │  │
│  │ 已选: Node.js 22 (node:22-alpine)         │ │  │
│  │ 说明: 适用于前端项目构建                    │ │  │
│  └──────────────────────────────────────────┘ │  │
│                                                  │
│  (admin 角色额外显示)                            │
│  ── 自定义镜像 (admin 逃生通道) ──────────       │
│  自定义镜像    [                     ] (可选)    │
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

- 删除 `web/src/components/pipeline/PipelineCanvas.vue` 的 YAML 折叠区（原 325-400 行）。
- 删除 `web/src/components/pipeline/YamlImportModal.vue` 中"导入后直接编辑并保存"按钮（保留导入预览）。
- 后端 `PUT /api/projects/{id}/pipeline` 拒绝 `body.yaml` 入参（详见 §5.11）。

---

## 七、权限与凭据模型

### 7.1 角色定义

| 角色 | 权限 |
|---|---|
| `admin` | 可访问 `/api/admin/*` 全部端点；可管理构建环境、配置资源、全局凭据、用户、系统参数、审计日志；可在流水线中直接指定 `image`（逃生通道）；能创建 personal 凭据（以个人身份） |
| `user` | 只能访问只读端点；流水线中只能选择预置的 `language + version` 或 `buildEnvID`；不可直接指定 `image`；可管理自己的 personal 凭据 |

### 7.2 凭据权限矩阵

| 操作 | global 凭据 | personal 凭据 |
|---|---|---|
| 创建 | 仅管理员 | 任何登录用户（含管理员） |
| 查看列表 | 所有用户（admin 可见个人凭据元数据） | 仅创建者本人 |
| 查看明文 | 仅管理员（操作时） | 仅创建者本人 |
| 编辑 | 仅管理员 | 仅创建者本人 |
| 删除 | 仅管理员 | 仅创建者本人 |
| 在流水线中引用 | 所有用户 | 仅创建者本人 |
| 管理员禁用（`enabled=0`） | 不适用 | 可禁用（不可物理删除） |

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

## 八、配置项与环境变量（v6.2 修订：新增配置项同步到 `internal/config/config.go`）

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
| `PIPEWRIGHT_DATA_DIR` | `./data` | 数据目录（config_profiles 落到此目录下；缺省时与 DB 同目录） |
| `PIPEWRIGHT_ALLOW_UNCHECKED_ENABLE` | `false` | **紧急逃生**：允许在镜像 unchecked 时直接启用 |

---

## 九、实施计划（v6.2 修订：依现实代码结构调整）

```
阶段 1:admin_user 升级为多管理员 + users 表新增 + Bootstrap 双向 ──┐
阶段 2:internal/environments 重命名为 internal/deployenv ─────────┤
阶段 3:internal/buildenv 包(model + service + checker + resolver)─┤
阶段 4:internal/configprofile 包(model + service + multipart 上传)─┤
阶段 5:internal/vault 扩展 RBAC(Actor 注入 + ListFilter)─────────┤
阶段 6:internal/auth 扩展(Session.Role + 多管理员 login)────────┤
阶段 7:internal/audit 扩展(action 常量 + actor 注入)─────────────┤
阶段 8:internal/httpapi 中间件(RequireAdmin 抽出)──────────────┤
阶段 9:internal/httpapi buildenv / configprofile / users handler─┤
阶段 10:internal/httpapi credentials handler 改造 + admin/disable ─┤
阶段 11:internal/pipeline.ValidateBuildEnvironment 接入 ──────────┤
阶段 12:internal/build dag_stage_exec/script_steps/builder 接入 ──┤
阶段 13:YAML 端点裁剪(pipelines.go 拒绝 body.yaml)──────────────┤
阶段 14:internal/config 新 env vars─────────────────────────────┤
阶段 15:cmd/pipewright/main.go 装配新领域包 + 启动自动检查 ──────┤
阶段 16:内置数据 seed(build_envs 内置 + config_profiles 内置) ─┤
阶段 17:前端 admin/* 页面(BuildEnvs/ConfigProfiles/Credentials/Users)─┐
阶段 19:前端 personal/* 页面(Profile/Credentials 等) ────────────┤
阶段 20:前端 ScriptJobEditor/ScriptStepEditor 适配环境选择器 ────┤
阶段 21:前端 PipelineCanvas 删除 YAML 折叠区 ────────────────────┤
阶段 22:测试贯穿(单测 + 集成) ─────────────────────────────────┘
```

**关键调整说明**：

- 阶段 1 是基础（admin_user 升多管理员 + users 表）；必须先做，否则阶段 5-6 的多用户 RBAC 无从落地。
- 阶段 2 是包重命名（environments → deployenv）；可在阶段 1 后独立进行，0 业务影响。
- 阶段 5（vault 扩展 RBAC）必须先于阶段 10（credentials handler 改造）。
- 阶段 13（YAML 端点裁剪）独立可测；完成后立即禁用 body.yaml。
- 阶段 16（内置数据 seed）排在阶段 15（装配）之前，确保自动检查启动时有数据可查。
- 阶段 22（测试）贯穿每个阶段，不只在末尾。

---

## 十、关键文件清单（v6.2 修订：现实存在文件加 NEW/MODIFY/RENAME 标记）

| 文件路径 | 类型 | 说明 |
|---|---|---|
| `internal/store/migrations/sqlite/0049_admin_user_multi.sql` | 新增 | admin_user 升级为多管理员表 |
| `internal/store/migrations/mysql/0049_admin_user_multi.sql` | 新增 | 同上 MySQL |
| `internal/store/migrations/sqlite/0049_users.sql` | 新增 | users 表 |
| `internal/store/migrations/mysql/0049_users.sql` | 新增 | 同上 MySQL |
| `internal/store/migrations/sqlite/0049_build_envs.sql` | 新增 | build_envs 表 |
| `internal/store/migrations/mysql/0049_build_envs.sql` | 新增 | 同上 MySQL |
| `internal/store/migrations/sqlite/0049_config_profiles.sql` | 新增 | config_profiles 表 |
| `internal/store/migrations/mysql/0049_config_profiles.sql` | 新增 | 同上 MySQL |
| `internal/store/migrations/sqlite/0049_credentials_owner.sql` | 新增 | credentials 加 owner_id 等列 + CHECK |
| `internal/store/migrations/mysql/0049_credentials_owner.sql` | 新增 | 同上 MySQL |
| `internal/users/consts.go` | 新增 | BootstrapAdminRegularUserID 常量 |
| `internal/users/model.go` | 新增 | User 实体 + Repo 接口 |
| `internal/users/service.go` | 新增 | CRUD、邀请、BootstrapAdmin、SyncAdminPasswordChange |
| `internal/buildenv/model.go` | 新增 | BuildEnv 实体 |
| `internal/buildenv/service.go` | 新增 | 业务逻辑 + SetEnabled 三态（P0 #4） |
| `internal/buildenv/checker.go` | 新增 | 镜像检查 + ManualPull + StartAutoCheck（sync.Once） |
| `internal/buildenv/resolver.go` | 新增 | ResolveImage / ResolveByID |
| `internal/configprofile/model.go` | 新增 | ConfigProfile 实体 |
| `internal/configprofile/service.go` | 新增 | CRUD + 原子写文件（P0 #3） |
| `internal/configprofile/files.go` | 新增 | tmp+fsync+rename 工具 |
| `internal/vault/rbac.go` | 新增 | Actor + ListFilter + effectiveFilter |
| `internal/vault/vault.go` | 修改 | List/Get/Create/Update/Delete 接受 Actor |
| `internal/deployenv/service.go` | 改名 | 从 internal/environments 迁过来 |
| `internal/deployenv/store.go` | 改名 | 同上 |
| `internal/deployenv/service_test.go` | 改名 | 同上 |
| `internal/environments/alias.go` | 新增 | 薄兼容层 type alias |
| `internal/environments/environments.go` | 删除 | 迁到 deployenv |
| `internal/environments/store.go` | 删除 | 同上 |
| `internal/environments/environments_test.go` | 删除 | 同上 |
| `internal/auth/service.go` | 修改 | Login 支持 users 表；Bootstrap/ChangePassword 双向同步 |
| `internal/auth/session.go` | 修改 | Session 加 UserID/Role 字段 |
| `internal/auth/rbac.go` | 新增 | RequireAdmin / RequireUser |
| `internal/audit/audit.go` | 修改 | 加新 Action 常量 |
| `internal/httpapi/middleware.go` | 新增 | RequireAdmin / RequireCSRF 抽出 |
| `internal/httpapi/router.go` | 修改 | 加 `/api/admin` 子组；中间件层加 RequireAdmin |
| `internal/httpapi/audit.go` | 修改 | auditActor 从硬编码改 session 注入 |
| `internal/httpapi/buildenv.go` | 新增 | 构建环境 CRUD + check/pull 端点 |
| `internal/httpapi/configprofile.go` | 新增 | 配置资源 CRUD + multipart upload |
| `internal/httpapi/users.go` | 新增 | 用户管理 |
| `internal/httpapi/credentials.go` | 修改 | list/filter 按 scope+owner;disable 端点 |
| `internal/httpapi/pipelines.go` | 修改 | 拒绝 body.yaml |
| `internal/pipeline/validate.go` | 修改 | 加 ValidateBuildEnvironment |
| `internal/build/dag_stage_exec.go` | 修改 | runBuildImageJob 接入 env_resolver |
| `internal/build/script_steps.go` | 修改 | runScriptStep 校验 image 来源 |
| `internal/build/builder.go` | 修改 | 删除 toolchainImage；改用 env_resolver |
| `internal/build/stage_post.go` | 修改 | PostStep 改用 env_resolver |
| `internal/build/services.go` | 修改 | ServiceSpec.BuildEnvID 字段 |
| `internal/build/remote.go` | 不动 | 远程构建超出本计划 |
| `internal/build/remote_stage_exec.go` | 不动 | 同上 |
| `internal/config/config.go` | 修改 | 加新 env vars |
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
| `web/src/components/ConfigProfileModal.vue` | 新增 | 配置资源表单（含 multipart 上传） |
| `web/src/components/CredentialModal.vue` | 新增 | 凭据表单 |
| `web/src/components/ScriptJobEditor.vue` | 修改 | 用户端环境选择器（language+version 下拉） |
| `web/src/components/pipeline/PipelineCanvas.vue` | 修改 | 删除 YAML 折叠区（原 325-400） |
| `web/src/components/pipeline/YamlImportModal.vue` | 修改 | 删除"导入后直接保存"按钮 |
| `web/src/router/index.js` | 修改 | 路由守卫（role 检查） |
| `web/src/views/PipelineEditor.vue` | 修改 | 移除 YAML 编辑入口 |

---

## 十一、兼容性与边界

### 11.1 边界声明

- **迁移脚本**：本计划只输出最终 DDL，不输出迁移脚本。升级路径由迁移团队另行处理。
- **远程构建**：本计划假设构建始终在宿主机执行。`internal/build/remote.go` 与 `ProjectRunner.remote` 类型保留但不在本计划修改。
- **全新部署**：从零建表是 DDL 状态。
- **现有部署兼容**：
    - `admin_user` 现有 id=1 行升级保留（移除 CHECK 约束，id 改自增）。
    - `credentials` 现有 `scope` 默认值 `''` 行升级为 `scope='global'`（迁移团队处理）。
    - 既有 `vault.Service` 接口签名不变（增 Actor 参数向后兼容，nil Actor 视为管理员）。
    - 既有 `internal/environments` 路径保留为薄兼容层（type alias 到 deployenv），后续版本删除。

### 11.2 风险与缓解

| 风险 | 缓解措施 |
|---|---|
| 镜像不存在导致构建失败 | 自动检查 + 强制禁用不可用环境；手动拉取验证；env_resolver 在 unavailable 时拒绝构建 |
| 凭据泄露 | NaCl secretbox 加密；vault 层按 Actor 过滤；日志脱敏 |
| 普通用户通过"改 YAML 保存"端点绕过 | 后端拒绝 `body.yaml`；前端 PipelineCanvas 移除 YAML 折叠区；管理员逃生通道不影响普通用户 |
| 个人凭据被他人引用 | vault 层强校验 owner_id == actor.UserID |
| 镜像检查任务挂死 | 每任务 60s timeout + 全局 10 并发上限；exec.CommandContext 强制超时；sync.Once 防重复启动 |
| docker login 污染宿主 ~/.docker/config.json | Check 路径不 login；Pull 路径 login 后立即 logout |
| 管理员 personal 凭据误用 | admin 创建 personal 时 owner_id 指向 admin 在 users 的行；与 global 凭据视觉区分 |
| 多用户引入认证漏洞 | 复用 argon2id + CSRF；dummyHash 时序保护不变；RequireAdmin 中间件鉴权 |
| admin_user 升多管理员兼容性 | id=1 行保留；自增从 2 起；历史 sessions 不失效 |
| environments 包改名破坏外部引用 | 保留 internal/environments/alias.go 薄兼容层；router 端点不变 |

---

## 十二、附录：内置数据（v6.2 修订：seed 在阶段 16 实施）

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

### 12.3 内置管理员在 users 表的同步

`admin_user.id=1` 创建时，同步在 `users` 创建 `id=users.BootstrapAdminRegularUserID`（`00000000-0000-0000-0000-000000000001`）, `role='admin'`, `username` 与 `password_hash` 与 `admin_user` 同步。**该 UUID 由 `users.BootstrapAdminRegularUserID` 常量统一引用，不得在调用点硬编码**。

### 12.4 内置 Maven settings.xml

（与 v6 §12.4 一致）

### 12.5 内置 NPM `.npmrc`

（与 v6 §12.5 一致）

### 12.6 内置 PIP `pip.conf`

（与 v6 §12.6 一致）

### 12.7 内置 GOPROXY

（与 v6 §12.7 一致）

---

## 十三、变更摘要

### 13.1 v6 → v6.1（P0 安全修订）

| P0 | 修订 | 落地位置 |
|---|---|---|
| #1 | admin UUID 抽 `users.BootstrapAdminRegularUserID` 常量；invariant 测试 | §5.8 / §12.3 |
| #2 | `credentials` 新增 CHECK 约束；UNIQUE+CHECK 双约束 | §4.5 |
| #3 | `config_profiles` 写路径原子（tmp + rename + 事务）；运行时以磁盘为权威；内置字段白名单 | §3.3 |
| #4 | SetEnabled 三态（unavailable 拒 / unchecked 默认拒 / available 放）；新增 `PIPEWRIGHT_ALLOW_UNCHECKED_ENABLE` | §5.4 / §八 |

### 13.2 v6.1 → v6.2（与现状对齐）

| 修订项 | v6.1 假设 | v6.2 决策 | 理由 |
|---|---|---|---|
| 用户模型 | 新建 `regular_users` 表 | `admin_user` 升级为多管理员 + `users` 表存普通用户 | 现实 admin_user 是 CHECK(id=1)；与其新建平行表不如升级 |
| 包命名 | 新建 `internal/buildenv` | `internal/environments` 重命名为 `internal/deployenv`；`buildenv` 让出 | 现实已有 environments 包，命名冲突 |
| 凭据扩展 | 新建 `internal/credential` | 直接扩展 `internal/vault`（加 RBAC 层） | 现实 vault 已是完整业务层；平行包是浪费 |
| YAML 端点 | 删除 `PATCH/PUT /api/pipelines/:id/yaml` | 后端 `PUT /api/projects/{id}/pipeline` 拒绝 `body.yaml` | 现实该端点不存在；真实禁用目标是结构化编辑入口 |
| audit actor | 全文 search/replace | session 注入 + auditActor 函数化 | 现实是常量；改造范围小 |
| admin 同步 | SyncAdminRow 函数 | BootstrapAdmin + SyncAdminPasswordChange | 现实 Bootstrap 在 auth/service.go；新逻辑放在 users/service.go 与之并列 |
| 领域包变更 | 仅加 NEW | 加 NEW + MODIFY + RENAME + 保留 alias | 真实反映代码变动类型 |
| 路由分组 | 既有 admin 在 `/admin/*`（前端） | 新增后端 `/api/admin/*`（统一 RequireAdmin 中间件） | 真实后端没 admin 命名空间 |
| multipart | 未提及 | 新增 multipart 上传 + MaxBytesReader + 扩展名白名单 | 现实无文件上传；v6.2 新增 |
| 自动检查启动 | 单一 goroutine | sync.Once 防服务重启重复触发 | 现实没有 errgroup 防重复 |
| 远程构建 | 不变 | 不变 | 同 v6 |

---

> 本文档为 Pipewright 构建环境预置与管理功能的 **v6.2** 设计说明。覆盖用户多角色（admin_user 升级 + users 表）、最终 DDL、凭据 RBAC（扩展 vault 包）、固定路径配置资源、镜像检查（60s/10/覆盖）、YAML 仅禁用编辑入口、本机构建边界等关键决策点，并完成 P0 安全修订 + 与现有代码架构对齐。所有迁移脚本由迁移团队另行处理；远程构建细节留作后续 story。