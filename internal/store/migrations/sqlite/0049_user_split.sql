-- 0049 user_split:admin_user 升多管理员 + 新增 users 表(普通用户 + 管理员个人行)。
-- 业务含义:
--   - admin_user:从 CHECK(id=1) 单行约束升级为可多行;id 改自增。保留 id=1 行(已有数据兼容)。
--   - users:存普通用户(role=user) + 管理员个人行(role=admin,username 与 admin_user 同步)。
--            personal 凭据 owner_id 指向 users.id。
--
-- 重要约束:
--   本迁移由 internal/store/store.go 的 applyMigration 在 SQLite 路径下用
--   tx.Begin() / tx.Exec(sqlText) / tx.Commit() 包裹执行(见 store.go:198-216)。
--   故 sqlText 本身**不能再嵌套 BEGIN TRANSACTION / ROLLBACK**(嵌套事务
--     在 modernc.org/sqlite 下会撞 "cannot start a transaction within a transaction")。
--   PRAGMA foreign_keys = OFF / ON 是单条语句,事务内合法;ALTER TABLE ... RENAME TO
--   在 SQLite 是独立 DDL,事务内合法;DROP TABLE 也合法(触发 ON DELETE CASCADE 的依赖
--   由 PRAGMA foreign_keys=OFF 暂时屏蔽,事务末尾 PRAGMA foreign_keys=ON 恢复并触发
--   deferred 完整性检查——但此处无外键引用 admin_user 的表,实际安全)。

-- 1. 暂时关掉外键(DROP/重建 admin_user 期间屏蔽引用完整性)
PRAGMA foreign_keys = OFF;

-- 2. 新建 admin_user_v2(去除 CHECK,id INTEGER PRIMARY KEY AUTOINCREMENT)
CREATE TABLE admin_user_v2 (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT    NOT NULL UNIQUE,
    password_hash TEXT    NOT NULL,
    created_at    TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL
);

-- 3. 拷贝现有 admin_user(id=1 行)到 v2;id 沿用(INSERT ... SELECT 让 v2 自增接受旧值)
INSERT INTO admin_user_v2 (id, username, password_hash, created_at, updated_at)
SELECT id, username, password_hash, created_at, updated_at FROM admin_user;

-- 4. 删旧表,改名 v2 → admin_user
DROP TABLE admin_user;
ALTER TABLE admin_user_v2 RENAME TO admin_user;

-- 5. 新建 users 表(普通用户 + 管理员个人行)
CREATE TABLE users (
    id            TEXT PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL DEFAULT 'user',
    enabled       INTEGER NOT NULL DEFAULT 1,
    description   TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    last_login_at TEXT
);
CREATE INDEX idx_users_username ON users(username);
CREATE INDEX idx_users_enabled  ON users(enabled);

-- 6. 恢复外键检查
PRAGMA foreign_keys = ON;