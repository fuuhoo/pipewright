-- 0087 server_groups(MySQL):一台服务器可以归属多个资源分组。语义与回填口径见 sqlite 同序注释。

CREATE TABLE IF NOT EXISTS server_groups (
    server_id  VARCHAR(64) NOT NULL,
    group_id   VARCHAR(64) NOT NULL,
    created_at VARCHAR(32) NOT NULL,
    PRIMARY KEY (server_id, group_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE INDEX idx_server_groups_group ON server_groups (group_id);

INSERT IGNORE INTO server_groups (server_id, group_id, created_at)
SELECT id, group_id, updated_at FROM servers WHERE TRIM(group_id) <> '';

DROP INDEX idx_servers_group_id ON servers;
ALTER TABLE servers DROP COLUMN group_id;
