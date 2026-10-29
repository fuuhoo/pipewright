-- 0084 user_roles(MySQL):一个账号可以挂多个角色。列义与回填口径见 sqlite 同序注释。
--
-- MySQL 侧另外修两处列宽:
--   users.role    VARCHAR(16) → VARCHAR(64):角色 id 自建时是 UUID(36 字符),16 位会静默截断。
--   sessions.role VARCHAR(16) → VARCHAR(512):会话里存的是**角色集合**("viewer,ops"),不再是单值。
-- SQLite 是动态类型,列宽不生效,故那边只需要建表。

CREATE TABLE IF NOT EXISTS user_roles (
    user_id    VARCHAR(64) NOT NULL,
    role_id    VARCHAR(64) NOT NULL,
    created_at VARCHAR(32) NOT NULL,
    PRIMARY KEY (user_id, role_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE INDEX idx_user_roles_role ON user_roles (role_id);

INSERT IGNORE INTO user_roles (user_id, role_id, created_at)
SELECT id, role, updated_at FROM users WHERE TRIM(role) <> '';

ALTER TABLE users    MODIFY COLUMN role VARCHAR(64) NOT NULL DEFAULT 'user';
ALTER TABLE sessions MODIFY COLUMN role VARCHAR(512) NOT NULL DEFAULT '';
