-- 0054 resource_groups(MySQL):资源分组,见 sqlite/0054_resource_groups.sql 注释。
-- 表名避开保留字 groups;MySQL 逐条执行(无事务),故不含条件式 DDL。

CREATE TABLE IF NOT EXISTS resource_groups (
    id          VARCHAR(64)  PRIMARY KEY,
    name        VARCHAR(255) NOT NULL,
    description VARCHAR(512) NOT NULL DEFAULT '',
    visibility  VARCHAR(16)  NOT NULL DEFAULT 'private',
    owner_id    VARCHAR(64)  NOT NULL,
    created_by  VARCHAR(64)  NOT NULL,
    created_at  VARCHAR(32)  NOT NULL,
    updated_at  VARCHAR(32)  NOT NULL,

    UNIQUE KEY uniq_resource_groups_name (name),
    CONSTRAINT chk_resource_groups_visibility CHECK (visibility IN ('public', 'private'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE INDEX idx_resource_groups_visibility ON resource_groups(visibility);
CREATE INDEX idx_resource_groups_owner      ON resource_groups(owner_id);
