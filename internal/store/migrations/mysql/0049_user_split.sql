-- 0049 user_split:admin_user 升多管理员 + 新增 users 表(普通用户 + 管理员个人行)。
-- 与 sqlite 版本语义对齐,见 sqlite/0049_user_split.sql 注释。
--
-- MySQL 路径逐条执行(无事务,见 store.go:184-189)。
--   id 列 PRIMARY KEY 直接改 AUTO_INCREMENT 是合法的(MySQL 8.0+);id=1 行保留。
--   CHECK 约束名按 MySQL 8.0.16+ 默认规则 admin_user_chk_1(8.0.16 之前 CHECK 不生效,
--     直接 ALTER MODIFY 即可;8.0.16+ 报约束名不一致时手动改名)。

-- 1. 移除 admin_user.id 上的 CHECK(id=1) 约束(8.0.16+)
ALTER TABLE admin_user DROP CHECK admin_user_chk_1;

-- 2. id 列改为 INT NOT NULL AUTO_INCREMENT,自 MySQL 8.0 起合法
ALTER TABLE admin_user MODIFY COLUMN id INT NOT NULL AUTO_INCREMENT;

-- 3. 新建 users 表(普通用户 + 管理员个人行)
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