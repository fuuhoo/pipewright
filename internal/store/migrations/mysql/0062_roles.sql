-- 0062 roles / role_perms(MySQL):可配置角色,只存自定义角色。
-- 内置五档的点集在代码表 internal/access/perms.go,库里不复制一份,理由见 sqlite 同序注释。
-- name 的 UNIQUE 在 MySQL 默认 utf8mb4 排序规则下大小写不敏感,sqlite 侧由服务层做大小写不敏感查重。

CREATE TABLE IF NOT EXISTS roles (
    id          VARCHAR(64) PRIMARY KEY,
    name        VARCHAR(255) NOT NULL UNIQUE,
    description VARCHAR(255) NOT NULL DEFAULT '',
    base_role   VARCHAR(64) NOT NULL DEFAULT '',
    created_by  VARCHAR(64) NOT NULL DEFAULT '',
    created_at  VARCHAR(32) NOT NULL,
    updated_at  VARCHAR(32) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS role_perms (
    role_id VARCHAR(64) NOT NULL,
    perm_id VARCHAR(64) NOT NULL,
    PRIMARY KEY (role_id, perm_id),
    FOREIGN KEY (role_id) REFERENCES roles (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE INDEX idx_role_perms_role_id ON role_perms (role_id);
